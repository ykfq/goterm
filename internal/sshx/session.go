package sshx

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// ErrClosed is returned by every operation on a session that already ended.
var ErrClosed = errors.New("session is closed")

// Tunables for the output pump.
const (
	// maxChunk is the largest single event payload we will emit; a burst of
	// output ("cat big.log") is cut into events of at most this size.
	maxChunk = 64 * 1024
	// flushInterval is how long output may sit in the buffer while the stream
	// is quiet. Small enough that an interactive shell still feels instant,
	// large enough that a busy stream produces ~80 events/s instead of ~8000.
	flushInterval = 12 * time.Millisecond
	// chunkQueueDepth decouples the SSH readers from the event emitter.
	chunkQueueDepth = 16
)

// SessionInfo is the serialisable summary of a live session.
type SessionInfo struct {
	ID            string    `json:"id"`
	Target        string    `json:"target"`
	Host          string    `json:"host"`
	Port          int       `json:"port"`
	User          string    `json:"user"`
	ServerVersion string    `json:"serverVersion"`
	Fingerprint   string    `json:"fingerprint"`
	Cols          int       `json:"cols"`
	Rows          int       `json:"rows"`
	CreatedAt     time.Time `json:"createdAt"`
	Status        string    `json:"status"` // connected | closed
	BytesIn       int64     `json:"bytesIn"`
	BytesOut      int64     `json:"bytesOut"`
	CloseReason   string    `json:"closeReason,omitempty"`
	// Via names the jump host this session tunnels through, empty when direct.
	Via string `json:"via,omitempty"`
	// KeepAliveSeconds is the resolved SSH keepalive interval; 0 means off.
	KeepAliveSeconds int `json:"keepAliveSeconds,omitempty"`
	// LogPath is the file this session's output is being recorded to, empty
	// when logging is off.
	LogPath string `json:"logPath,omitempty"`
}

const (
	StatusConnected = "connected"
	StatusClosed    = "closed"
)

// Session is one remote PTY. Several sessions may share one connection — see
// connRef — which is why nothing below owns the client directly.
type Session struct {
	id   string
	cfg  Config
	mgr  *Manager
	sess *ssh.Session
	// The client itself lives on the link (see connRef): it is shared, it is
	// released by refcount, and a second copy here could only ever go stale.
	stdin io.WriteCloser
	// link is the shared connection this session was opened on. Releasing it is
	// how the client outlives the first tab to close.
	link *connRef

	chunks  chan []byte
	seqDone chan struct{} // closed by the waiter goroutine

	// shellTimeoutNoted keeps the TMOUT explanation to one notice per session.
	shellTimeoutNoted atomic.Bool

	writeMu  sync.Mutex
	bytesIn  atomic.Int64
	bytesOut atomic.Int64

	infoMu sync.Mutex
	info   SessionInfo

	// Output flow control: the frontend pauses the pump when its terminal is
	// behind, which is what keeps a firehose (`cat` of a huge file) from
	// piling up in the webview's IPC queue. flowWake is closed on resume; a
	// new one is made on the next pause.
	flowMu     sync.Mutex
	flowPaused bool
	flowWake   chan struct{}

	// logMu guards the session log pointer. The pump is the only writer; this
	// lock covers the user switching recording off while output arrives.
	logMu sync.Mutex
	log   *sessionLog

	finishOnce sync.Once
}

// ID is the handle the frontend uses for every later call.
func (s *Session) ID() string { return s.id }

// Info copies the current summary.
func (s *Session) Info() SessionInfo {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	out := s.info
	out.BytesIn = s.bytesIn.Load()
	out.BytesOut = s.bytesOut.Load()
	return out
}

func (s *Session) setStatus(status, reason string) {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	s.info.Status = status
	if reason != "" {
		s.info.CloseReason = reason
	}
}

func (s *Session) closed() bool {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.info.Status == StatusClosed
}

// SetOutputFlow pauses or resumes delivering this session's output to the UI.
//
// Pausing does not drop anything: the pump stops reading its chunk queue, the
// SSH readers fill it and then block, and the transport's own window closes.
// That backpressure is the point — without it a fast server can outrun the
// terminal and the difference queues up in WebView2's memory.
func (s *Session) SetOutputFlow(paused bool) {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if s.flowPaused == paused {
		return
	}
	s.flowPaused = paused
	if paused {
		s.flowWake = make(chan struct{})
		return
	}
	if s.flowWake != nil {
		close(s.flowWake)
		s.flowWake = nil
	}
}

// flowGate returns a channel to wait on while paused, nil while running.
func (s *Session) flowGate() <-chan struct{} {
	s.flowMu.Lock()
	defer s.flowMu.Unlock()
	if !s.flowPaused {
		return nil
	}
	return s.flowWake
}

// Write sends raw bytes to the remote PTY (input path for xterm and trzsz).
func (s *Session) Write(p []byte) error {
	if s.closed() {
		return ErrClosed
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.stdin.Write(p); err != nil {
		s.finish(inputReason(err))
		return fmt.Errorf("write to %s: %w", s.info.Target, err)
	}
	s.bytesOut.Add(int64(len(p)))
	if s.link != nil {
		// Typed input is what the idle window is measured from. The clock lives
		// on the connection, so a keystroke in any duplicated tab keeps the
		// keepalive defending the whole link.
		s.link.touch()
	}
	return nil
}

// Resize tells the remote PTY the new geometry.
func (s *Session) Resize(cols, rows int) error {
	if cols <= 0 || rows <= 0 {
		return fmt.Errorf("bad window size %dx%d", cols, rows)
	}
	if s.closed() {
		return ErrClosed
	}
	if err := s.sess.WindowChange(rows, cols); err != nil {
		return fmt.Errorf("resize %s: %w", s.info.Target, err)
	}
	s.infoMu.Lock()
	s.info.Cols, s.info.Rows = cols, rows
	s.infoMu.Unlock()
	return nil
}

// Close ends the session on purpose.
func (s *Session) Close() error {
	s.finish("closed by user")
	return nil
}

// finish is the single exit door: idempotent, closes everything, emits once.
func (s *Session) finish(reason string) {
	s.finishOnce.Do(func() {
		s.setStatus(StatusClosed, reason)
		// A paused pump must not be left waiting on a session that is gone:
		// resume it so it drains the last chunks and closes the log file.
		s.SetOutputFlow(false)
		if s.link != nil {
			// The connection belongs to whoever is still using it; this session is
			// simply no longer one of them.
			s.link.untrack(s)
			s.link.release()
		}
		if s.stdin != nil {
			_ = s.stdin.Close()
		}
		if s.sess != nil {
			_ = s.sess.Close()
		}
		s.mgr.forget(s.id)
		s.mgr.emit(EventClosed, ClosedEvent{SessionID: s.id, Reason: reason, Session: s.Info()})
	})
}

// autoLogoutMarker is what bash prints when its own input timer fires
// (`TMOUT`). It is not an SSH-level disconnect: the transport is healthy and
// the keepalives were doing their job — the *shell* decided the user had been
// idle too long, and only input typed into the session resets that timer.
var autoLogoutMarker = []byte("timed out waiting for input")

// readers copies stdout and stderr into the chunk queue.
func (s *Session) readers(wg *sync.WaitGroup, r io.Reader) {
	wg.Add(1)
	go func() {
		defer wg.Done()
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				s.bytesIn.Add(int64(n))
				s.noteShellTimeout(buf[:n])
				chunk := make([]byte, n)
				copy(chunk, buf[:n])
				select {
				case s.chunks <- chunk:
				case <-s.seqDone:
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

// noteShellTimeout explains a `TMOUT` logout the first time it appears in the
// output. Silently watching the session die with "auto logout" on screen is the
// kind of thing a user reasonably reads as "keepalive is broken"; it is not, and
// the notice says what would actually help.
func (s *Session) noteShellTimeout(chunk []byte) {
	if s.shellTimeoutNoted.Load() || !bytes.Contains(chunk, autoLogoutMarker) {
		return
	}
	if !s.shellTimeoutNoted.CompareAndSwap(false, true) {
		return
	}
	s.mgr.noticeAction(s.id, "warn", fmt.Sprintf(
		"%s: the remote shell logged itself out after an input timeout (bash TMOUT). "+
			"SSH keepalives cannot reset that timer: a keepalive never reaches the shell, only typed input does. "+
			"Anti-idle sends a nudge while you are idle; the button turns it on.",
		s.info.Target), "enable-anti-idle")
}

// pump owns the output buffer — one goroutine, so events cannot be
// reordered — and converts byte runs into base64 events. It is also where the
// session log is fed, so the file and the screen see the same bytes in the same
// order.
func (s *Session) pump() {
	ticker := time.NewTicker(flushInterval)
	defer ticker.Stop()
	// The last bytes are flushed to disk when the pump exits, which is after
	// both readers are done: a connection that dies mid-line still leaves a
	// complete log up to the last byte that arrived.
	defer s.closeLog()

	var buf []byte
	flush := func() {
		if len(buf) == 0 {
			s.currentLog().flush()
			return
		}
		s.currentLog().write(buf)
		s.currentLog().flush()
		s.mgr.emit(EventData, DataEvent{SessionID: s.id, Data: base64.StdEncoding.EncodeToString(buf)})
		buf = nil
	}

	// A pause that arrives after finish() has already resumed the gate must not
	// leave this goroutine waiting on a session that is gone: its end is the
	// other way out. Once the session has ended the pause is ignored and the
	// queue is drained, so the log still gets its last bytes.
	flowDisabled := false
	for {
		// Paused by the UI: hand over what is already buffered, then wait.
		if !flowDisabled {
			if gate := s.flowGate(); gate != nil {
				flush()
				select {
				case <-gate:
				case <-s.seqDone:
					flowDisabled = true
				}
				continue
			}
		}
		select {
		case chunk, ok := <-s.chunks:
			if !ok {
				flush()
				return
			}
			buf = append(buf, chunk...)
			for len(buf) >= maxChunk {
				s.currentLog().write(buf[:maxChunk])
				s.mgr.emit(EventData, DataEvent{SessionID: s.id, Data: base64.StdEncoding.EncodeToString(buf[:maxChunk])})
				buf = buf[maxChunk:]
			}
		case <-ticker.C:
			flush()
		}
	}
}

// wait blocks until the remote side hangs up, then closes the session.
func (s *Session) wait() {
	defer close(s.seqDone)
	err := s.sess.Wait()
	s.finish(remoteReason(err))
}

// exitStatus digs a process exit code out of an ssh.Wait() error.
func exitStatus(err error) (int, bool) {
	type exiter interface{ ExitStatus() int }
	if e, ok := err.(exiter); ok {
		return e.ExitStatus(), true
	}
	return 0, false
}

func remoteReason(err error) string {
	if err == nil {
		return "remote shell exited"
	}
	if code, ok := exitStatus(err); ok {
		return fmt.Sprintf("remote shell exited with status %d", code)
	}
	if errors.Is(err, io.EOF) {
		return "remote shell exited"
	}
	if isNoisyDisconnect(err.Error()) {
		return "connection lost"
	}
	return "connection closed: " + err.Error()
}

func inputReason(err error) string {
	if isNoisyDisconnect(err.Error()) {
		return "connection lost"
	}
	return "write failed: " + err.Error()
}

// isNoisyDisconnect classifies the errors a transport death produces, so the
// UI can say "Connection Lost" instead of pasting a Go error at the user.
func isNoisyDisconnect(msg string) bool {
	msg = strings.ToLower(msg)
	for _, s := range []string{"use of closed", "broken pipe", "connection reset", "eof", "timeout", "connection abort"} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
