package sshx

import (
	"io"
	"strings"
	"testing"

	"github.com/ykfq/goterm/internal/sshtest"
)

// A shell with TMOUT set logs itself out, and says so. The transport is fine —
// the keepalives were doing their job — but a user watching "auto logout" scroll
// past reasonably concludes that GoTerm's keepalive is broken. The session
// notices it and explains, once.
func TestShellTimeoutIsExplainedOnce(t *testing.T) {
	h := newHarness(t, sshtest.Options{
		Shell: func(sh *sshtest.Shell) {
			// Twice, to prove the notice is not repeated per occurrence.
			for i := 0; i < 2; i++ {
				_, _ = io.WriteString(sh.Out(), "timed out waiting for input: auto logout\r\n")
			}
			sh.Exit(1)
		},
	})

	sess := h.connect(h.config())
	h.waitFor(sess, "the remote shell to exit", func() bool {
		_, ended := h.rec.closed(sess.ID())
		return ended
	})

	notices := h.rec.noticeText()
	if !strings.Contains(notices, "TMOUT") {
		t.Fatalf("no TMOUT explanation in the notices:\n%s", notices)
	}
	if !strings.Contains(notices, "Anti-idle") {
		t.Fatalf("the explanation does not say what would help:\n%s", notices)
	}
	if n := strings.Count(notices, "TMOUT"); n != 1 {
		t.Fatalf("the explanation appeared %d times, want once:\n%s", n, notices)
	}
	// And it carries the fix, so the toast can offer it instead of describing it.
	var action string
	for _, notice := range h.rec.notices() {
		if strings.Contains(notice.Message, "TMOUT") {
			action = notice.Action
		}
	}
	if action != "enable-anti-idle" {
		t.Fatalf("TMOUT notice action = %q, want enable-anti-idle", action)
	}
	// The bytes themselves still reached the terminal.
	if !strings.Contains(string(h.rec.output(sess.ID())), "timed out waiting for input") {
		t.Fatal("the shell's message did not reach the terminal")
	}
}

// Ordinary output must not trip the detector.
func TestOrdinaryOutputIsNotMistakenForATimeout(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)
	h.send(sess, "echo timed out waiting\r")
	h.expectOutput(sess, "timed out waiting")

	if strings.Contains(h.rec.noticeText(), "TMOUT") {
		t.Fatalf("a timeout explanation was emitted for ordinary output:\n%s", h.rec.noticeText())
	}
}
