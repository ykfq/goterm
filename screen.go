package main

import (
	"io"
	"sync"
)

// tabScreen is a tab's side of its terminal.
//
// The emulator reads and writes one object for the tab's whole life, and the
// session behind it can be replaced. That is what lets a reconnect keep the
// screen — and the scrollback — instead of building a new terminal and wiping
// the history the user was reading: everything the old session printed is still
// above, and the new shell's prompt arrives underneath it.
//
// The one thing it must never do is end. A terminal that sees EOF or an error
// from Conn is over, and it takes the scrollback with it — so when a session
// dies, Read forgets it and waits for the next one instead of returning.
type tabConn interface {
	io.ReadWriteCloser
	Resize(cols, rows int) error
}

type tabScreen struct {
	mu     sync.Mutex
	conn   tabConn
	cols   int
	rows   int
	closed bool
	// wake is closed and replaced on every change, so a Read that is waiting
	// for a session looks again. A channel rather than a condition variable:
	// the reader must also wake when the tab itself closes.
	wake chan struct{}
}

func newTabScreen() *tabScreen { return &tabScreen{wake: make(chan struct{})} }

// attach points the screen at a session, or at nothing (nil) between sessions.
// The geometry the view last asked for is kept and applied to the new session,
// so a reconnect does not start at the size the connection was dialled with.
func (s *tabScreen) attach(c tabConn) {
	s.mu.Lock()
	old := s.conn
	s.conn = c
	cols, rows := s.cols, s.rows
	close(s.wake)
	s.wake = make(chan struct{})
	s.mu.Unlock()

	if old != nil && old != c {
		// Off the UI thread: closing a session can wait on the far side.
		go old.Close()
	}
	if c != nil && cols > 0 && rows > 0 {
		_ = c.Resize(cols, rows)
	}
}

func (s *tabScreen) Read(p []byte) (int, error) {
	for {
		s.mu.Lock()
		c, wake, closed := s.conn, s.wake, s.closed
		s.mu.Unlock()

		switch {
		case closed:
			return 0, io.EOF
		case c != nil:
			n, err := c.Read(p)
			if n > 0 {
				return n, nil
			}
			if err == nil {
				continue
			}
			// The session ended. Ending the terminal here is what used to throw
			// the scrollback away on every dropped connection.
			s.mu.Lock()
			if s.conn == c {
				s.conn = nil
			}
			s.mu.Unlock()
		default:
			<-wake
		}
	}
}

func (s *tabScreen) Write(p []byte) (int, error) {
	s.mu.Lock()
	c, closed := s.conn, s.closed
	s.mu.Unlock()

	if closed {
		return 0, io.ErrClosedPipe
	}
	if c == nil {
		// No session: the keys go nowhere, quietly. An error here would be
		// surfaced as a failed terminal rather than as a tab that is simply
		// disconnected.
		return len(p), nil
	}
	return c.Write(p)
}

func (s *tabScreen) Resize(cols, rows int) error {
	s.mu.Lock()
	s.cols, s.rows = cols, rows
	c := s.conn
	s.mu.Unlock()

	if c != nil {
		return c.Resize(cols, rows)
	}
	return nil
}

func (s *tabScreen) Close() error {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil
	}
	s.closed = true
	c := s.conn
	s.conn = nil
	close(s.wake)
	s.mu.Unlock()

	if c != nil {
		return c.Close()
	}
	return nil
}
