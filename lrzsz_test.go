package main

import (
	"bytes"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
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
func drain(conn interface{ Read([]byte) (int, error) }) *bytes.Buffer {
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
	return mu.Buffer()
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
	defer conn.Close()
	drain(conn)

	if _, err := conn.Write([]byte("rz\r")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(dir, "upload.txt")
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		got, err := os.ReadFile(target)
		if err == nil && bytes.Equal(got, payload) {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	got, err := os.ReadFile(target)
	t.Fatalf("upload did not arrive: %d bytes, err %v, want %d", len(got), err, len(payload))
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
	defer conn.Close()
	drain(conn)

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
	t.Fatalf("download did not arrive: %d bytes, err %v, want %d", len(got), err, len(payload))
}

var _ = errors.New
var _ = zmodem.ErrCanceled
