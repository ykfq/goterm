package main

/*
 * The idle keystroke, on a tab built exactly the way the app builds one.
 *
 * That last part is the point. internal/sshx proves a session gets nudged, but
 * a session there has no transfer filter; a real tab always does
 * (startTerminal wraps every session in one), and attaching that filter used to
 * mark the session busy for the rest of its life — so every keystroke was
 * suppressed in production while the sshx tests stayed green.
 *
 * This test is slow on purpose: the settings floor is 15 seconds, and pretending
 * otherwise is what let the two halves drift apart.
 */
import (
	"bytes"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/settings"
	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
)

func TestATabWithATransferFilterGetsTheIdleKeystroke(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a real keystroke window")
	}
	var mu sync.Mutex
	var typed []byte
	srv, err := sshtest.New(sshtest.Options{
		User:     "u",
		Password: "p",
		Shell: func(sh *sshtest.Shell) {
			buf := make([]byte, 64)
			for {
				n, err := sh.In().Read(buf)
				if n > 0 {
					mu.Lock()
					typed = append(typed, buf[:n]...)
					mu.Unlock()
				}
				if err != nil {
					return
				}
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	host, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	sess, err := app.mgr.Connect(sshx.Config{Host: host, Port: port, User: "u", Password: "p"})
	if err != nil {
		t.Fatal(err)
	}
	defer sess.Close()
	// Exactly what startTerminal does before the terminal exists.
	conn, err := newTransferConn(sess, encode.UTF8, &countingChooser{})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	var diary bytes.Buffer
	app.mgr.SetDiag(&diary)
	app.mgr.SetNudge(sshx.NudgeOptions{On: true, Every: settings.MinNudgeSeconds * time.Second})

	deadline := time.Now().Add(settings.MinNudgeSeconds*time.Second + 8*time.Second)
	for time.Now().Before(deadline) {
		mu.Lock()
		got := len(typed)
		mu.Unlock()
		if got > 0 {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("no keystroke reached the shell; diary:\n%s", diary.String())
}
