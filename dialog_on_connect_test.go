package main

import (
	"net"
	"strconv"
	"testing"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
)

// countingChooser records which dialog the transfer layer asked for. Opening a
// folder picker on connect put a modal window in front of the terminal before
// the user had done anything, so the invariant is that connecting asks for
// nothing on screen.
type countingChooser struct {
	defaults int
	modals   int
}

func (c *countingChooser) ChooseDownloadFolder(string) (string, error) {
	c.modals++
	return "/tmp", nil
}

func (c *countingChooser) ChooseUpload(bool) ([]string, error) {
	c.modals++
	return nil, nil
}

func (c *countingChooser) DefaultDownloadFolder() (string, error) {
	c.defaults++
	return "/tmp", nil
}

// TestConnectingDoesNotOpenADialog pins that a new terminal only asks for the
// silent default download folder.
func TestConnectingDoesNotOpenADialog(t *testing.T) {
	srv, err := sshtest.New(sshtest.Options{User: "u", Password: "p"})
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

	chooser := &countingChooser{}
	conn, err := newTransferConn(sess, encode.UTF8, chooser)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	if chooser.modals != 0 {
		t.Fatalf("connecting opened %d dialog(s); it must ask nothing", chooser.modals)
	}
	if chooser.defaults != 1 {
		t.Fatalf("connect asked for the silent default %d times, want 1", chooser.defaults)
	}
}
