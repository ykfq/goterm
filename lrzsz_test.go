package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshtest/lrzsz"
	"github.com/ykfq/goterm/internal/sshx"
	"github.com/ykfq/goterm/internal/zmodem"
)

// lrzszServer starts the fake SSH host whose shell answers rz/sz, and returns
// its address and the directory both ends share.
func lrzszServer(t *testing.T) (host string, port int, dir string) {
	t.Helper()
	dir = t.TempDir()
	srv, err := sshtest.New(sshtest.Options{
		User:     "tester",
		Password: "lrzsz-pass",
		Shell:    lrzsz.Shell{Dir: dir}.Run,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	h, p, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		t.Fatal(err)
	}
	return h, n, dir
}

// dialLrzsz connects a session to the fake host.
func dialLrzsz(t *testing.T, host string, port int) *sshx.Session {
	t.Helper()
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { app.Close() })
	sess, err := app.mgr.Connect(sshx.Config{
		Host: host, Port: port, User: "tester", Password: "lrzsz-pass",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sess.Close() })
	return sess
}

// drain keeps the terminal side reading: the filter draws into that stream, and
// an unread pipe would stall the session.
func drain(conn interface{ Read([]byte) (int, error) }) *syncBuffer {
	var mu syncBuffer
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				mu.Write(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	return &mu
}

// TestLrzszUpload goes `rz` on the remote, picks the file through the chooser,
// and checks the bytes land on the peer.
func TestLrzszUpload(t *testing.T) {
	host, port, dir := lrzszServer(t)
	source := filepath.Join(t.TempDir(), "upload.txt")
	payload := bytes.Repeat([]byte("lrzsz upload "), 700)
	if err := os.WriteFile(source, payload, 0o644); err != nil {
		t.Fatal(err)
	}

	sess := dialLrzsz(t, host, port)
	conn, err := newTransferConn(sess, encode.UTF8, fixedChooser{dir: dir, upload: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	screen := drain(conn)

	if _, err := conn.Write([]byte("rz\r")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "upload.txt")
	deadline := time.Now().Add(30 * time.Second)
	lastReport := time.Now()
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(target)
		if err == nil && bytes.Equal(got, payload) {
			return
		}
		if time.Since(lastReport) > 2*time.Second {
			lastReport = time.Now()
			t.Logf("waiting: session %s, queue %d, screen %q", sess.Info().Status, sess.OutputQueueDepth(), screen.Bytes())
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, err := os.ReadFile(target)
	sessInfo := sess.Info()
	t.Fatalf("upload did not arrive: %d bytes, err %v, want %d; terminal saw %q; session %+v",
		len(got), err, len(payload), screen.Bytes(), sessInfo)
}

// TestLrzszDownload goes `sz` on the remote and checks the file arrives in the
// folder the chooser picked.
func TestLrzszDownload(t *testing.T) {
	host, port, dir := lrzszServer(t)
	payload := bytes.Repeat([]byte("lrzsz download "), 700)
	if err := os.WriteFile(filepath.Join(dir, "remote.bin"), payload, 0o644); err != nil {
		t.Fatal(err)
	}
	downloads := t.TempDir()

	sess := dialLrzsz(t, host, port)
	conn, err := newTransferConn(sess, encode.UTF8, fixedChooser{dir: downloads})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	screen := drain(conn)

	if _, err := conn.Write([]byte("sz remote.bin\r")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(downloads, "remote.bin")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(target)
		if err == nil && bytes.Equal(got, payload) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, err := os.ReadFile(target)
	t.Fatalf("download did not arrive: %d bytes, err %v, want %d; terminal saw %q",
		len(got), err, len(payload), screen.Bytes())
}

var _ = errors.New
var _ = zmodem.ErrCanceled

// TestLrzszCancelReleasesTheRemote is the "sz got stuck" regression. Dismissing
// the folder dialog has to say so on the wire: a remote sz left waiting for the
// ZRINIT that never comes keeps its shell behind it with the terminal in raw
// mode, so Ctrl+C and Enter both land in a protocol read and only closing the
// tab got out.
func TestLrzszCancelReleasesTheRemote(t *testing.T) {
	host, port, dir := lrzszServer(t)
	if err := os.WriteFile(filepath.Join(dir, "give.txt"), []byte("payload"), 0o644); err != nil {
		t.Fatal(err)
	}
	sess := dialLrzsz(t, host, port)
	conn, err := newTransferConn(sess, encode.UTF8, fixedChooser{dir: dir, refuse: true})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	screen := drain(conn)
	// The highlight layer decorates what the shell prints, so the test reads
	// the screen the way a person would: the words, without the colour codes.
	plain := func() string { return stripPasteControl(string(screen.Bytes())) }

	if _, err := conn.Write([]byte("sz give.txt\r")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 20*time.Second, func() bool { return strings.Contains(plain(), "canceled") }) {
		t.Fatalf("the canceled download was never reported on the wire: screen=%q", plain())
	}

	// The prompt is the proof that matters: the remote's sz exited instead of
	// waiting, so the shell behind it takes commands again.
	if _, err := conn.Write([]byte("ping\r")); err != nil {
		t.Fatal(err)
	}
	if !waitFor(t, 20*time.Second, func() bool { return strings.Contains(plain(), "unknown command") }) {
		t.Fatalf("the session stayed behind a canceled sz: screen=%q", plain())
	}
}

// waitFor polls cond until it holds or the wait runs out.
func waitFor(t *testing.T, wait time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return cond()
}

// TestZmodemCancelIsAbortThenFin reads the cancel sequence back with the same
// parser a peer uses, so what is asserted is the protocol, not the bytes: the
// remote's lrzsz has to see a transfer that is off followed by a session that
// is closed.
func TestZmodemCancelIsAbortThenFin(t *testing.T) {
	r := zmodem.NewReader(bytes.NewReader(zmodem.Cancel()))
	first, err := r.Next()
	if err != nil {
		t.Fatalf("first cancel header: %v", err)
	}
	second, err := r.Next()
	if err != nil {
		t.Fatalf("second cancel header: %v", err)
	}
	if first.Type != zmodem.ZABORT || second.Type != zmodem.ZFIN {
		t.Fatalf("cancel is %d then %d, want ZABORT(%d) then ZFIN(%d)",
			first.Type, second.Type, zmodem.ZABORT, zmodem.ZFIN)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// TestLrzszUploadSurvivesARigidPeer sends a file bigger than one window to a
// receiver that behaves like lrzsz's rz instead of like our own code: it wants
// the descriptor to declare the size, every window to arrive at the offset it
// asked for, and a ZCRCR for each window before it reads the next.
//
// The mirror-image tests above cannot show any of this, because both halves of
// them are ours. With the frames this code used to write — no offset, a
// descriptor of "0 0", ZCRCG windows acknowledged by a ZACK that real rz never
// sends — the transfer stops after the first window and the file never lands.
func TestLrzszUploadSurvivesARigidPeer(t *testing.T) {
	host, port, dir := lrzszServer(t)
	source := filepath.Join(t.TempDir(), "hundred-k.bin")
	payload := bytes.Repeat([]byte("0123456789abcdef"), 1024*7) // 114688 bytes: four windows
	if err := os.WriteFile(source, payload, 0o644); err != nil {
		t.Fatal(err)
	}
	sess := dialLrzsz(t, host, port)
	conn, err := newTransferConn(sess, encode.UTF8, fixedChooser{dir: dir, upload: []string{source}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	screen := drain(conn)

	if _, err := conn.Write([]byte("rzstrict\r")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "hundred-k.bin")
	if !waitFor(t, 45*time.Second, func() bool {
		got, err := os.ReadFile(target)
		return err == nil && bytes.Equal(got, payload)
	}) {
		got, _ := os.ReadFile(target)
		t.Fatalf("a rigid rz never took the file: %d of %d bytes; screen=%q",
			len(got), len(payload), plainOf(screen))
	}
}

// plainOf is what the terminal saw, without the colour codes the highlight
// layer injects into the shell's own words.
func plainOf(b *syncBuffer) string {
	s := stripPasteControl(string(b.Bytes()))
	if len(s) > 600 {
		s = s[len(s)-600:]
	}
	return s
}
