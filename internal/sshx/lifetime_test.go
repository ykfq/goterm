/*
 * The hard cap on one connection.
 *
 * Keepalives are what keeps a session from dying of a server-side idle timeout.
 * Left at that they also make a login that never ends possible, which is exactly
 * the shape of "keep my session alive past your policy" — so the same connection
 * now also carries a deadline it cannot talk its way out of. These tests are the
 * reason it is a real close and not a suggestion: they connect, wait, and look at
 * the session afterwards.
 */
package sshx

import (
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

func TestConnectionIsClosedWhenTheLifetimeRunsOut(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetMaxLifetime(300 * time.Millisecond)

	sess := h.connect(h.config())
	// It genuinely connected first: the cap is a limit on a working session, not
	// a connect timeout in disguise.
	h.expectOutput(sess, readyMarker)

	h.waitFor(sess, "the cap to close the session", sess.closed)
	info := sess.Info()
	if !strings.Contains(info.CloseReason, "maximum session lifetime") {
		t.Fatalf("close reason = %q, want the lifetime explained", info.CloseReason)
	}
	// Said out loud once, where the user is looking, and it names the number the
	// policy is actually enforcing.
	if notices := h.rec.noticeText(); !strings.Contains(notices, "maximum session lifetime") {
		t.Fatalf("no notice mentioned the cap; notices were:\n%s", notices)
	} else if !strings.Contains(notices, "reconnect") {
		t.Fatalf("the notice does not say reconnecting is allowed:\n%s", notices)
	}
}

func TestDuplicatingDoesNotBuyANewLifetime(t *testing.T) {
	// The clock belongs to the *connection*, not to a tab: a duplicate is another
	// channel on the same login, so it dies with it. A per-session clock would let
	// anyone extend a session for ever by pressing Duplicate.
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetMaxLifetime(400 * time.Millisecond)

	first := h.connect(h.config())
	h.expectOutput(first, readyMarker)
	time.Sleep(200 * time.Millisecond)

	second, err := h.mgr.Duplicate(first.ID())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	h.expectOutput(second, readyMarker)

	h.waitFor(first, "the cap to close the original", first.closed)
	// The original had ~200ms left when the duplicate appeared. If the duplicate
	// had started its own clock it would still have ~400ms to go here.
	h.waitFor(second, "the duplicate to close with the connection", second.closed)
	if !strings.Contains(second.Info().CloseReason, "maximum session lifetime") {
		t.Fatalf("duplicate close reason = %q", second.Info().CloseReason)
	}
}

func TestLoweringTheCapEndsASessionThatIsAlreadyOpen(t *testing.T) {
	// A setting that only affects the *next* connection would be a limit the user
	// could sit on: twelve hours in, set it to one hour, keep working. The deadline
	// is measured from when the connection was made, so lowering it applies now.
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetMaxLifetime(24 * time.Hour)

	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)
	if sess.closed() {
		t.Fatal("session closed before the cap was lowered")
	}

	h.mgr.SetMaxLifetime(150 * time.Millisecond)
	h.waitFor(sess, "the lowered cap to close the session", sess.closed)
}

func TestSessionLifetimeIsClampedBothWays(t *testing.T) {
	cases := []struct {
		hours int
		want  time.Duration
	}{
		{0, DefaultSessionLifetimeHours * time.Hour},  // "unset" means the default, never "no limit"
		{-5, DefaultSessionLifetimeHours * time.Hour}, // ditto
		{1, time.Hour},
		{12, 12 * time.Hour},
		{MaxSessionLifetimeHours, MaxSessionLifetimeHours * time.Hour},
		{99, MaxSessionLifetimeHours * time.Hour}, // a hand-edited file cannot ask for a week
	}
	for _, tc := range cases {
		if got := SessionLifetime(tc.hours); got != tc.want {
			t.Errorf("SessionLifetime(%d) = %s, want %s", tc.hours, got, tc.want)
		}
	}
}

func TestLifetimeLabelsAreReadable(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{12 * time.Hour, "12 hours"},
		{1 * time.Hour, "1 hour"},
		{90 * time.Minute, "90 minutes"},
		{time.Minute, "1 minute"},
	}
	for _, tc := range cases {
		if got := humanLifetime(tc.d); got != tc.want {
			t.Errorf("humanLifetime(%s) = %q, want %q", tc.d, got, tc.want)
		}
	}
}
