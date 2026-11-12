package main

import (
	"bytes"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
)

// TestTrzszDownloadThroughTheFilter drives a real trzsz download against the
// bundled server: the same filter the app uses sits on the real SSH byte
// stream, the remote side speaks the protocol, and the file appears on disk.
func TestTrzszDownloadThroughTheFilter(t *testing.T) {
	serverDir := t.TempDir()
	payload := bytes.Repeat([]byte("trzsz-payload-"), 4096)
	if err := os.WriteFile(filepath.Join(serverDir, "demo.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}

	srv, err := sshtest.New(sshtest.Options{
		User:     "tester",
		Password: "trzsz-pass",
		Shell:    sshtest.TrzszShell{Dir: serverDir}.Run,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()

	host, portText, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}

	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	sess, err := app.mgr.Connect(sshx.Config{
		Host: host, Port: port, User: "tester", Password: "trzsz-pass",
	})
	if err != nil {
		t.Fatal(err)
	}

	downloads := t.TempDir()
	conn, err := newTransferConn(sess, encode.UTF8, fixedChooser{dir: downloads})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	states := make(chan bool, 8)
	conn.filter.SetTransferStateCallback(func(active bool) { states <- active })

	// The terminal side must keep reading: the filter draws progress into the
	// same stream, and an unread pipe would stall the transfer.
	var mu sync.Mutex
	var screen bytes.Buffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				mu.Lock()
				screen.Write(buf[:n])
				mu.Unlock()
			}
			if err != nil {
				return
			}
		}
	}()

	if _, err := conn.Write([]byte("tsz demo.bin\r")); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(downloads, "demo.bin")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(target)
		if err == nil && bytes.Equal(data, payload) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	data, err := os.ReadFile(target)
	mu.Lock()
	tail := screen.String()
	if len(tail) > 2000 {
		tail = tail[len(tail)-2000:]
	}
	mu.Unlock()
	var statesSeen []bool
	for {
		select {
		case s := <-states:
			statesSeen = append(statesSeen, s)
			continue
		default:
		}
		break
	}
	t.Fatalf("download did not arrive intact: %d bytes, err %v, want %d; states=%v; screen=%q", len(data), err, len(payload), statesSeen, tail)
}

// TestDecodingReaderSwitchesMidStream is the settings promise: choosing a new
// remote encoding reaches open terminals, without rebuilding the session.
func TestDecodingReaderSwitchesMidStream(t *testing.T) {
	r, w := io.Pipe()
	defer r.Close()
	defer w.Close()
	d := newDecodingReader(r, encode.GB18030)

	gb, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte("中"))
	if err != nil {
		t.Fatal(err)
	}
	go func() { _, _ = w.Write(gb) }()
	buf := make([]byte, 8)
	n, err := d.Read(buf)
	if err != nil || string(buf[:n]) != "中" {
		t.Fatalf("GB18030 read = %q, %v", buf[:n], err)
	}

	d.SetEncoding(encode.UTF8)
	go func() { _, _ = w.Write([]byte("hi")) }()
	n, err = d.Read(buf)
	if err != nil || string(buf[:n]) != "hi" {
		t.Fatalf("after switching to UTF-8 read = %q, %v", buf[:n], err)
	}
}

// trzszTerminal starts the trzsz server, connects a session, and wraps it in a
// transfer connection with the given chooser. The returned dir is the remote's
// directory, where demo.bin is served from.
func trzszTerminal(t *testing.T, chooser Chooser) (conn *transferConn, dir string, screen *syncBuffer) {
	t.Helper()
	dir = t.TempDir()
	payload := bytes.Repeat([]byte("trzsz-payload-"), 4096)
	if err := os.WriteFile(filepath.Join(dir, "demo.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := sshtest.New(sshtest.Options{
		User:     "tester",
		Password: "trzsz-pass",
		Shell:    sshtest.TrzszShell{Dir: dir}.Run,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })

	host, portText, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	sess, err := app.mgr.Connect(sshx.Config{
		Host: host, Port: port, User: "tester", Password: "trzsz-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })

	conn, err = newTransferConn(sess, encode.UTF8, chooser)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, dir, drain(conn)
}

// askingChooser records the folder questions and answers with folder, or
// refuses when refuse is set. Its default folder is deliberately a different
// directory, so a download that skipped the dialog is visible on disk.
type askingChooser struct {
	mu       sync.Mutex
	asks     int
	folder   string
	refuse   bool
	fallback string
}

func (a *askingChooser) ChooseDownloadFolder(string) (string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.asks++
	if a.refuse {
		return "", errors.New("canceled")
	}
	return a.folder, nil
}

func (a *askingChooser) DefaultDownloadFolder() (string, error) { return a.fallback, nil }

func (a *askingChooser) ChooseUpload(bool) ([]string, error) {
	return nil, errors.New("upload not scripted")
}

func (a *askingChooser) count() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.asks
}

// TestTszAsksWhereToSave is the parity promise: tsz asks the same folder
// question sz asks, and the file lands where the answer said, not in the folder
// recorded at connect time.
func TestTszAsksWhereToSave(t *testing.T) {
	want := bytes.Repeat([]byte("trzsz-payload-"), 4096)
	chosen := t.TempDir()
	fallback := t.TempDir()
	chooser := &askingChooser{folder: chosen, fallback: fallback}

	conn, _, screen := trzszTerminal(t, chooser)
	if _, err := conn.Write([]byte("tsz demo.bin\r")); err != nil {
		t.Fatal(err)
	}

	target := filepath.Join(chosen, "demo.bin")
	if !waitFor(t, 20*time.Second, func() bool {
		got, err := os.ReadFile(target)
		return err == nil && bytes.Equal(got, want)
	}) {
		t.Fatalf("tsz never wrote to the chosen folder: asks=%d, screen=%q",
			chooser.count(), tailOf(screen))
	}
	if chooser.count() == 0 {
		t.Fatal("the file arrived without asking where to save it")
	}
	if _, err := os.Stat(filepath.Join(fallback, "demo.bin")); err == nil {
		t.Fatal("a copy landed in the connect-time default folder as well")
	}
}

// TestTszCancelStartsNothing checks the other half of the dialog: saying no
// must not leave a file behind, and must not leave the session waiting for a
// client that already walked away.
func TestTszCancelStartsNothing(t *testing.T) {
	chosen := t.TempDir()
	fallback := t.TempDir()
	chooser := &askingChooser{folder: chosen, fallback: fallback, refuse: true}

	conn, _, screen := trzszTerminal(t, chooser)
	if _, err := conn.Write([]byte("tsz demo.bin\r")); err != nil {
		t.Fatal(err)
	}

	// Give a transfer that ignored the answer time to finish anyway.
	time.Sleep(3 * time.Second)
	for _, dir := range []string{chosen, fallback} {
		if _, err := os.Stat(filepath.Join(dir, "demo.bin")); err == nil {
			t.Fatalf("a canceled download still wrote into %s", dir)
		}
	}

	// The remote's tsz was waiting for a handshake that will not come. Saying
	// no has to release it, so the shell behind it answers again.
	if _, err := conn.Write([]byte("echo still-here\r")); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		if bytes.Contains(screen.Bytes(), []byte("still-here")) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("the session stayed behind a canceled tsz: screen=%q", tailOf(screen))
}

// TestOnlyTszAsks keeps the question on one side of a line: text that merely
// mentions tsz, or a command that has other work in front of it, goes straight
// to the shell without a dialog.
func TestOnlyTszAsks(t *testing.T) {
	chosen := t.TempDir()
	chooser := &askingChooser{folder: chosen, fallback: t.TempDir()}
	conn, _, screen := trzszTerminal(t, chooser)

	for _, line := range []string{"echo tsz demo.bin", "ls\r", "cd /tmp && tsz demo.bin\r"} {
		if _, err := conn.Write([]byte(line)); err != nil {
			t.Fatal(err)
		}
		if !bytes.HasSuffix([]byte(line), []byte("\r")) {
			if _, err := conn.Write([]byte("\r")); err != nil {
				t.Fatal(err)
			}
		}
	}
	if got := chooser.count(); got != 0 {
		t.Fatalf("a dialog opened %d time(s) for lines that do not run tsz: screen=%q",
			got, tailOf(screen))
	}
}

// tailOf is a bounded view of what the terminal saw, for failure messages.
func tailOf(b *syncBuffer) []byte {
	data := b.Bytes()
	if len(data) > 1200 {
		data = data[len(data)-1200:]
	}
	return data
}

// trzszTerminalNoDrain is trzszTerminal without the reader, for bisection.
func trzszTerminalNoDrain(t *testing.T, dl string) (*transferConn, string) {
	t.Helper()
	dir := t.TempDir()
	payload := bytes.Repeat([]byte("trzsz-payload-"), 4096)
	if err := os.WriteFile(filepath.Join(dir, "demo.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	srv, err := sshtest.New(sshtest.Options{User: "tester", Password: "trzsz-pass", Shell: sshtest.TrzszShell{Dir: dir}.Run})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	host, portText, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		t.Fatal(err)
	}
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	sess, err := app.mgr.Connect(sshx.Config{Host: host, Port: port, User: "tester", Password: "trzsz-pass"})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := newTransferConn(sess, encode.UTF8, fixedChooser{dir: dl})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, dir
}
