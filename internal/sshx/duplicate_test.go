package sshx

import (
	"bytes"
	"testing"

	"github.com/ykfq/goterm/internal/sshtest"
)

// A duplicate is a second terminal on a connection that is already up. The point
// is that nothing is re-authenticated — in front of a JumpServer-style bastion a
// second login would mean a second one-time code, and the whole feature would be
// worthless.
func TestDuplicateOpensASecondTerminalWithoutAnotherLogin(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	src := h.connect(h.config())
	h.expectOutput(src, readyMarker)
	if n := h.srv.AuthSuccess(); n != 1 {
		t.Fatalf("authentications before duplicating = %d, want 1", n)
	}

	dup, err := h.mgr.Duplicate(src.ID())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if dup.ID() == src.ID() {
		t.Fatal("the duplicate came back with the source's session id")
	}
	if n := h.srv.AuthSuccess(); n != 1 {
		t.Fatalf("the server authenticated %d times; a duplicate must reuse the connection", n)
	}
	if got, want := dup.Info().Target, src.Info().Target; got != want {
		t.Fatalf("duplicate target = %q, want %q", got, want)
	}

	// Two real shells: each sees its own typing and none of the other's.
	h.send(src, "echo into-the-source\r")
	h.expectOutput(src, "into-the-source")
	h.send(dup, "echo into-the-duplicate\r")
	h.expectOutput(dup, "into-the-duplicate")
	if bytes.Contains(h.rec.output(dup.ID()), []byte("into-the-source")) {
		t.Fatal("the duplicate printed the source's output — the channels are not separate")
	}
	if !bytes.Contains(h.rec.output(dup.ID()), []byte(readyMarker)) {
		t.Fatal("the duplicate never got its own shell prompt")
	}
}

// Closing the tab that opened the connection must not take the connection with
// it. That is the difference between duplicating a session and logging in twice.
func TestDuplicateOutlivesTheSessionItCameFrom(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	src := h.connect(h.config())
	h.expectOutput(src, readyMarker)

	dup, err := h.mgr.Duplicate(src.ID())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if err := h.mgr.Close(src.ID()); err != nil {
		t.Fatalf("close the source: %v", err)
	}
	h.waitFor(src, "the source session to close", func() bool { return src.closed() })

	if info := dup.Info(); info.Status != StatusConnected {
		t.Fatalf("closing the source killed the duplicate: %+v", info)
	}
	h.send(dup, "echo still-here\r")
	h.expectOutput(dup, "still-here")

	// The last one out turns off the lights: nothing is left registered.
	if err := h.mgr.Close(dup.ID()); err != nil {
		t.Fatalf("close the duplicate: %v", err)
	}
	h.waitFor(dup, "the duplicate to close", func() bool { return dup.closed() })
	if n := h.mgr.Count(); n != 0 {
		t.Fatalf("%d sessions still registered after the last one closed", n)
	}
}

// A closed session has no connection to share. Saying so is better than opening
// a tab that looks alive and cannot type.
func TestDuplicateRefusesAConnectionThatIsGone(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	src := h.connect(h.config())
	h.expectOutput(src, readyMarker)
	if err := h.mgr.Close(src.ID()); err != nil {
		t.Fatalf("close: %v", err)
	}
	h.waitFor(src, "the session to close", func() bool { return src.closed() })

	if _, err := h.mgr.Duplicate(src.ID()); err == nil {
		t.Fatal("duplicating a closed session succeeded")
	}
	if _, err := h.mgr.Duplicate("does-not-exist"); err == nil {
		t.Fatal("duplicating an unknown session succeeded")
	}
}

// Unanswered keepalives mean the connection is gone, and every session riding on
// it is gone with it — each has to say so, not just the one whose probe failed.
func TestDeadKeepaliveTakesEverySessionOnTheConnection(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	src := h.connect(h.config())
	h.expectOutput(src, readyMarker)

	dup, err := h.mgr.Duplicate(src.ID())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	link := src.link
	if link == nil || link != dup.link {
		t.Fatal("the two sessions are not on one shared connection")
	}

	link.fail("3 keepalives in a row were not answered")

	h.waitFor(src, "the source to be declared lost", func() bool { return src.closed() })
	h.waitFor(dup, "the duplicate to be declared lost", func() bool { return dup.closed() })

	for _, sess := range []*Session{src, dup} {
		ev, ok := h.rec.closed(sess.ID())
		if !ok {
			t.Fatalf("no closed event for %s", sess.ID())
		}
		if !bytes.Contains([]byte(ev.Reason), []byte("connection lost")) {
			t.Fatalf("session %s ended with %q, want a connection-lost reason", sess.ID(), ev.Reason)
		}
	}
	explained := 0
	for _, notice := range h.rec.notices() {
		if bytes.Contains([]byte(notice.Message), []byte("keepalives in a row")) {
			explained++
		}
	}
	if explained != 2 {
		t.Fatalf("%d notices explained the dead keepalive, want one per session", explained)
	}
}
