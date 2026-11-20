package main

/*
 * The screen under a terminal.
 *
 * A reconnect used to build a new terminal, and a new terminal has no
 * scrollback — so everything the user was reading disappeared exactly when a
 * dropped session made them want to read it. The screen now outlives the
 * session, and the one rule that makes that work is that Read never ends the
 * terminal while the tab is open.
 */
import (
	"bytes"
	"io"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
)

// scriptConn is a session's side of a screen: it hands out the bytes it was
// given, then fails the way a dead session does.
type scriptConn struct {
	mu      sync.Mutex
	chunks  [][]byte
	writes  bytes.Buffer
	resizes [][2]int
	closed  bool
}

func (c *scriptConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	if len(c.chunks) == 0 {
		return 0, io.EOF
	}
	n := copy(p, c.chunks[0])
	if n == len(c.chunks[0]) {
		c.chunks = c.chunks[1:]
	} else {
		c.chunks[0] = c.chunks[0][n:]
	}
	return n, nil
}

func (c *scriptConn) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return 0, io.ErrClosedPipe
	}
	c.writes.Write(p)
	return len(p), nil
}

func (c *scriptConn) Resize(cols, rows int) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.resizes = append(c.resizes, [2]int{cols, rows})
	return nil
}

func (c *scriptConn) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	return nil
}

func readWithin(t *testing.T, s *tabScreen, wait time.Duration) (string, bool) {
	t.Helper()
	got := make(chan string, 1)
	go func() {
		buf := make([]byte, 64)
		n, err := s.Read(buf)
		if err != nil {
			got <- ""
			return
		}
		got <- string(buf[:n])
	}()
	select {
	case v := <-got:
		return v, true
	case <-time.After(wait):
		return "", false
	}
}

// A session that ends must not end the terminal: that is the whole bug. One
// reader, as the terminal has one, spanning both sessions.
func TestTheScreenOutlivesASessionThatEnded(t *testing.T) {
	s := newTabScreen()
	first := &scriptConn{chunks: [][]byte{[]byte("before the drop\r\n")}}
	s.attach(first)

	got := make(chan string, 8)
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := s.Read(buf)
			if err != nil {
				got <- "<ended>"
				return
			}
			if n > 0 {
				got <- string(buf[:n])
			}
		}
	}()

	expect := func(want string, wait time.Duration) {
		t.Helper()
		select {
		case v := <-got:
			if v != want {
				t.Fatalf("read %q, want %q", v, want)
			}
		case <-time.After(wait):
			t.Fatalf("nothing read within %v, want %q", wait, want)
		}
	}
	expect("before the drop\r\n", time.Second)

	// The session is over. The reader must wait, not report an end — an end is
	// what takes the scrollback with it.
	select {
	case v := <-got:
		t.Fatalf("the screen ended when its session did (%q)", v)
	case <-time.After(150 * time.Millisecond):
	}

	// A new session arrives and the same reader carries on.
	s.attach(&scriptConn{chunks: [][]byte{[]byte("after the reconnect\r\n")}})
	expect("after the reconnect\r\n", time.Second)
}

// Between sessions the keys go nowhere, and they must not raise an error the
// terminal would report as a broken engine.
func TestKeysGoNowhereBetweenSessions(t *testing.T) {
	s := newTabScreen()
	if n, err := s.Write([]byte("ls\r")); err != nil || n != 3 {
		t.Fatalf("Write while detached = %d, %v; want 3, nil", n, err)
	}

	c := &scriptConn{}
	s.attach(c)
	if n, err := s.Write([]byte("ls\r")); err != nil || n != 3 {
		t.Fatalf("Write while attached = %d, %v", n, err)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.writes.String() != "ls\r" {
		t.Fatalf("the attached session got %q", c.writes.String())
	}
}

// The view tells the screen its size before a session exists; the session that
// arrives later has to be told too, or the reconnect starts at the size the
// connection was dialled with.
func TestTheGeometryIsKeptForTheNextSession(t *testing.T) {
	s := newTabScreen()
	if err := s.Resize(132, 43); err != nil {
		t.Fatal(err)
	}
	c := &scriptConn{}
	s.attach(c)
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.resizes) == 0 || c.resizes[0] != [2]int{132, 43} {
		t.Fatalf("the new session was told %v, want [132 43]", c.resizes)
	}
}

// Closing the tab is what ends the terminal, and it has to end.
func TestClosingTheScreenEndsTheTerminal(t *testing.T) {
	s := newTabScreen()
	c := &scriptConn{}
	s.attach(c)
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 8)
	if _, err := s.Read(buf); err == nil {
		t.Fatal("a closed screen still reads")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.closed {
		t.Fatal("closing the screen did not close the session")
	}
}

// The promise in the user's words: reconnect and the history is still there.
// This one needs the terminal engine, so it skips where the engine cannot load.
func TestAReconnectKeepsTheScrollback(t *testing.T) {
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
	cfg := sshx.Config{Host: host, Port: port, User: "u", Password: "p"}

	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	tab := &Tab{ID: "tab-reconnect", Status: StatusConnecting}
	sess, err := app.mgr.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tab.Sess = sess
	app.startTerminal(tab)
	if tab.Term == nil {
		t.Skipf("terminal engine unavailable: %s", tab.Err)
	}
	first := tab.Term
	tab.Term.Feed([]byte("the line I was reading\r\n"))

	// What reconnect does, without the dial.
	tab.Screen.attach(nil)
	tab.Sess.Close()

	again, err := app.mgr.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tab.Sess = again
	app.startTerminal(tab)
	defer tab.Term.Close()

	if tab.Term != first {
		t.Fatal("the reconnect built a new terminal; the scrollback went with it")
	}
	if text := tab.Term.Text(); !strings.Contains(text, "the line I was reading") {
		t.Fatalf("the history is gone after a reconnect: %q", text)
	}
}
