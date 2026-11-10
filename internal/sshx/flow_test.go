package sshx

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

// A terminal that falls behind must be able to stop the server from being read,
// instead of letting the difference pile up in the webview. That is what these
// two tests pin: while paused, nothing is emitted; on resume, everything that
// arrived meanwhile is delivered, in order, with nothing lost.
func TestOutputFlowPausesAndResumes(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	if err := h.mgr.SetOutputFlow(sess.ID(), true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	// Let anything already in flight (the prompt, the last tick) land, so the
	// measurement below is about the pause and not about a race.
	time.Sleep(100 * time.Millisecond)
	before := len(h.rec.output(sess.ID()))

	// 200 lines is a few chunks: enough that an unpaused pump would emit some
	// of them within the window we check.
	h.send(sess, "spam 200\r")
	time.Sleep(300 * time.Millisecond)

	if got := len(h.rec.output(sess.ID())); got != before {
		t.Fatalf("output grew by %d bytes while paused; flow control did nothing", got-before)
	}

	if err := h.mgr.SetOutputFlow(sess.ID(), false); err != nil {
		t.Fatalf("resume: %v", err)
	}
	// The bytes that were held are delivered on resume — and not just the tail:
	// the first line is still there, which is what "paused, not dropped" means.
	h.expectOutput(sess, "line 000001 ")
	h.expectOutput(sess, "line 000200 ")
}

// Pausing a session that then ends must not strand the pump (or its log file).
func TestOutputFlowResumesOnClose(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	dir := t.TempDir()
	if _, err := h.mgr.ConfigureSessionLogging(true, dir); err != nil {
		t.Fatalf("enable logging: %v", err)
	}
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	if err := h.mgr.SetOutputFlow(sess.ID(), true); err != nil {
		t.Fatalf("pause: %v", err)
	}
	h.send(sess, "echo before-close\r")

	if err := h.mgr.Close(sess.ID()); err != nil {
		t.Fatalf("close: %v", err)
	}
	h.waitFor(sess, "closed event", func() bool {
		_, ok := h.rec.closed(sess.ID())
		return ok
	})

	// The pump has to have exited to close the log, and exiting is only
	// possible if the close resumed it: this is the proof that a paused
	// session cannot strand a goroutine and an open file handle.
	h.waitFor(sess, "the pump to close its log", func() bool {
		return sess.currentLog() == nil
	})
	path := sess.Info().LogPath
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !strings.Contains(string(raw), readyMarker) {
		t.Fatalf("log is missing output that was emitted before the pause:\n%s", printable(raw))
	}
}

// A pause can cross the session's close on the wire: the UI saw a backlog just
// as the connection died. That pause must not outlive the session — the pump has
// to notice the end and still drain, or the log file is never closed.
func TestOutputFlowPauseAfterCloseDoesNotStrandThePump(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, sshtest.Options{})
	if _, err := h.mgr.ConfigureSessionLogging(true, dir); err != nil {
		t.Fatalf("enable logging: %v", err)
	}
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)
	path := sess.Info().LogPath

	if err := h.mgr.Close(sess.ID()); err != nil {
		t.Fatalf("close: %v", err)
	}
	h.waitFor(sess, "closed event", func() bool {
		_, ok := h.rec.closed(sess.ID())
		return ok
	})

	// The late flow-control call, after the session already ended.
	sess.SetOutputFlow(true)

	h.waitFor(sess, "the pump to close its log", func() bool {
		return sess.currentLog() == nil
	})
	if raw, err := os.ReadFile(path); err != nil || !strings.Contains(string(raw), readyMarker) {
		t.Fatalf("log did not survive the late pause: err=%v size=%d", err, len(raw))
	}
}

// Deterministic version of the same race, with the two facts that matter stated
// directly: the session has already ended, and a pause arrives afterwards. A
// pump that only listens for "resume" waits forever here and never closes its
// log; the session's end has to be a way out too.
func TestPausedPumpLeavesASessionThatAlreadyEnded(t *testing.T) {
	mgr := NewManager(NewHostKeyStore(filepath.Join(t.TempDir(), "known_hosts.json")))
	s := &Session{
		id:      "s_test",
		mgr:     mgr,
		chunks:  make(chan []byte),
		seqDone: make(chan struct{}),
	}
	close(s.seqDone)      // the session has ended ...
	s.SetOutputFlow(true) // ... and the pause arrived afterwards
	close(s.chunks)       // both readers are done

	done := make(chan struct{})
	go func() {
		s.pump()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("a pause that arrived after the session ended left the pump waiting forever")
	}
}