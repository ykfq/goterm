package sshx

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

// The diary is the answer to "it still does not work": a report can say whether
// the keystroke was configured, sent, or suppressed — instead of leaving the
// next round to read code and guess, which is how this took four of them.
func TestTheDiarySaysWhatWasConfiguredAndWhatWentOut(t *testing.T) {
	var diary bytes.Buffer
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetDiag(&diary)
	h.mgr.SetNudge(NudgeOptions{On: true, Every: nudgeAfter})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	bookFromNow(sess)

	// Configured, and with the numbers that matter.
	if !waitFor(t, nudgeAfter, func() bool { return strings.Contains(diary.String(), "nudge=true") }) {
		t.Fatalf("the diary does not say what the session was told:\n%s", diary.String())
	}
	// And what it sent.
	if !waitFor(t, 4*nudgeAfter, func() bool { return strings.Contains(diary.String(), "keystroke enter") }) {
		t.Fatalf("the diary does not record the keystroke:\n%s", diary.String())
	}
	// And how it ended, when it does.
	sess.Close()
	if !waitFor(t, time.Second, func() bool { return strings.Contains(diary.String(), "closed") }) {
		t.Fatalf("the diary does not record the close:\n%s", diary.String())
	}
}

// A session whose keystrokes are being suppressed says so, and why. "0 sent" in
// the status bar is otherwise indistinguishable from a loop that never ran.
func TestTheDiarySaysWhyAKeystrokeWasSuppressed(t *testing.T) {
	var diary bytes.Buffer
	_, sess := nudgeHarness(t)
	sess.mgr.SetDiag(&diary)
	sess.SetTransferring(true)

	if !waitFor(t, 4*nudgeAfter, func() bool {
		return strings.Contains(diary.String(), "suppressed: a transfer")
	}) {
		t.Fatalf("a suppressed keystroke was not explained:\n%s", diary.String())
	}
}
