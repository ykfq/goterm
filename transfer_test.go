package main

import (
	"bytes"
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

