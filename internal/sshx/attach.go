package sshx

import (
	"encoding/base64"
	"errors"
	"io"
	"sync"
)

// DefaultOutputLimit is how many bytes of terminal output may wait for the
// terminal to read them. When the limit is reached the pump blocks, which
// blocks the SSH readers, which closes the TCP window: the remote shell stops
// sending until the terminal catches up. That is the whole flow-control story
// in 2.x — there is no base64 IPC queue to overflow any more.
const DefaultOutputLimit = 4 << 20

// ErrOutputAttached is returned when a second reader asks for a session's
// output. A terminal owns a session's bytes, so a second one would race.
var ErrOutputAttached = errors.New("session output is already attached")

// outQueue is a bounded, blocking byte queue between the SSH readers and a
// terminal.
type outQueue struct {
	mu     sync.Mutex
	cond   *sync.Cond
	data   []byte
	limit  int
	closed bool
}

func newOutQueue(limit int) *outQueue {
	if limit <= 0 {
		limit = DefaultOutputLimit
	}
	q := &outQueue{limit: limit}
	q.cond = sync.NewCond(&q.mu)
	return q
}

// write appends p, waiting while the queue is full. It never grows past
// limit+len(p), and p is at most maxChunk, so memory stays bounded.
func (q *outQueue) write(p []byte) {
	if len(p) == 0 {
		return
	}
	q.mu.Lock()
	for !q.closed && len(q.data) >= q.limit {
		q.cond.Wait()
	}
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.data = append(q.data, p...)
	q.cond.Broadcast()
	q.mu.Unlock()
}

// Read implements io.Reader: it blocks until bytes arrive or the queue closes.
func (q *outQueue) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.data) == 0 && !q.closed {
		q.cond.Wait()
	}
	if len(q.data) == 0 {
		return 0, io.EOF
	}
	n := copy(p, q.data)
	q.data = q.data[n:]
	if len(q.data) == 0 {
		q.data = nil
	}
	q.cond.Broadcast() // let a blocked writer in
	return n, nil
}

// Close ends the stream. Bytes already queued stay readable; a writer that is
// blocked on a full queue is released and its bytes are dropped, which only
// happens when the terminal that reads them is going away too.
func (q *outQueue) Close() error {
	q.mu.Lock()
	q.closed = true
	q.cond.Broadcast()
	q.mu.Unlock()
	return nil
}

// queueDepth reports the bytes waiting, for tests and the status bar.
func (q *outQueue) queueDepth() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.data)
}

// AttachOutput hands the session's output to one reader, typically the
// terminal plugin's Conn. Attach it as soon as Connect returns: output that
// arrives afterwards goes here instead of to the event stream.
func (s *Session) AttachOutput() (io.ReadCloser, error) {
	s.outMu.Lock()
	defer s.outMu.Unlock()
	if s.out != nil {
		return nil, ErrOutputAttached
	}
	q := newOutQueue(DefaultOutputLimit)
	s.out = q
	return q, nil
}

// OutputQueueDepth is how many bytes are waiting for the attached reader.
// Zero when nothing is attached.
func (s *Session) OutputQueueDepth() int {
	s.outMu.Lock()
	q := s.out
	s.outMu.Unlock()
	if q == nil {
		return 0
	}
	return q.queueDepth()
}

func (s *Session) attachedOutput() *outQueue {
	s.outMu.Lock()
	q := s.out
	s.outMu.Unlock()
	return q
}

// deliver routes one flushed run of output either to the attached terminal or
// to the legacy event stream.
func (s *Session) deliver(p []byte) {
	if q := s.attachedOutput(); q != nil {
		q.write(p)
		return
	}
	s.mgr.emit(EventData, DataEvent{SessionID: s.id, Data: base64.StdEncoding.EncodeToString(p)})
}

// closeOutput releases a blocked terminal reader once the pump has flushed
// everything it had.
func (s *Session) closeOutput() {
	if q := s.attachedOutput(); q != nil {
		_ = q.Close()
	}
}
