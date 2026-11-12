/*
 * The idle window, and the ledge it forms with "away".
 *
 * A keepalive exists to notice a dead link and to defend a session that someone
 * is using. It is not a way to hold a login open for ever. But "no keystrokes"
 * is not the same thing as "nobody is there": a bastion hangs up on a quiet
 * connection whether or not someone is reading its output, so a quiet keyboard
 * on a visible window must not stop the probes. The defence ends only when the
 * user has typed nothing for the window *and* has put the window out of sight.
 *
 * These tests pin both halves — an unwatched session stops being defended, a
 * watched one is never cut off underneath the user — plus the plumbing that
 * lets a duplicated tab share the same clock instead of buying its own.
 */
package sshx

import (
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

// idleHarness is the usual harness with a short keepalive interval, so a pause
// notice arrives in about a second rather than 45.
func idleHarness(t *testing.T, idle time.Duration) (*harness, Config) {
	t.Helper()
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetIdleTimeout(idle)
	cfg := h.config()
	cfg.KeepAliveSeconds = 1
	return h, cfg
}

// away is what the app reports when the window is hidden, minimised or behind
// another application.
func (h *harness) away() { h.mgr.SetDetached(true) }

func TestIdleStopNeedsTheWindowToBeOutOfSight(t *testing.T) {
	h, cfg := idleHarness(t, 200*time.Millisecond)
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	// The keyboard is still and the window is on screen. The probes must keep
	// going: this is somebody watching a build, not an abandoned login, and a
	// bastion does not care which one it is.
	time.Sleep(1200 * time.Millisecond)
	if notices := h.rec.noticeText(); strings.Contains(notices, "keepalive signals stopped") {
		t.Fatalf("a visible window's quiet session stopped being defended:\n%s", notices)
	}
	if sess.closed() {
		t.Fatal("the idle window closed the session; it must only stop the probes")
	}

	// The user puts it away: now the defence ends, and it says so once.
	h.away()
	h.waitFor(sess, "the away notice", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals stopped")
	})
	if sess.closed() {
		t.Fatal("going away closed the session; it must only stop the probes")
	}

	// The point of stopping rather than closing: the connection is still there,
	// so typing works and simply brings the probes back.
	h.send(sess, "echo still-here\r")
	h.expectOutput(sess, "still-here")
	h.waitFor(sess, "the keepalive to resume after input", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals resumed")
	})
	if sess.closed() {
		t.Fatalf("session closed after input: %+v", sess.Info())
	}
}

// Coming back into view is the other way out: a window the user restored is
// defended again even if the keyboard has still said nothing.
func TestComingBackIntoViewResumesTheProbes(t *testing.T) {
	h, cfg := idleHarness(t, 150*time.Millisecond)
	h.away()
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	h.waitFor(sess, "the away notice", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals stopped")
	})

	h.mgr.SetDetached(false)
	h.waitFor(sess, "the probes to resume", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals resumed")
	})
	if sess.closed() {
		t.Fatalf("restoring the window closed the session: %+v", sess.Info())
	}
}

func TestActivityDefersTheIdleWindow(t *testing.T) {
	h, cfg := idleHarness(t, 250*time.Millisecond)
	h.away() // the app is behind another window the whole time
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	// Type an empty write every 60ms for well past the window. Nothing reaches
	// the shell, but every write is user activity as far as the window is
	// concerned — even though the window itself is out of sight.
	stop := time.Now().Add(2200 * time.Millisecond)
	for time.Now().Before(stop) {
		h.send(sess, "")
		time.Sleep(60 * time.Millisecond)
	}
	if notices := h.rec.noticeText(); strings.Contains(notices, "keepalive signals stopped") {
		t.Fatalf("keepalive paused while the user was typing:\n%s", notices)
	}
	if sess.closed() {
		t.Fatal("session closed while the user was typing")
	}

	// Silence now: the next tick past the window pauses the probes.
	h.waitFor(sess, "the pause after typing stopped", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals stopped")
	})
}

func TestIdlePauseIsAnnouncedOnce(t *testing.T) {
	h, cfg := idleHarness(t, 150*time.Millisecond)
	h.away()
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	h.waitFor(sess, "the pause notice", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals stopped")
	})
	// Two more probe windows: an idle session must not repeat itself.
	time.Sleep(2200 * time.Millisecond)
	if got := strings.Count(h.rec.noticeText(), "keepalive signals stopped"); got != 1 {
		t.Fatalf("pause announced %d times, want once:\n%s", got, h.rec.noticeText())
	}
}

func TestDuplicatedTabsShareTheIdleClock(t *testing.T) {
	h, cfg := idleHarness(t, 300*time.Millisecond)
	first := h.connect(cfg)
	h.expectOutput(first, readyMarker)

	second, err := h.mgr.Duplicate(first.ID())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	h.expectOutput(second, readyMarker)

	// Typing in the duplicate is still the user being present on this
	// connection — and the window is out of sight — so both tabs keep their
	// probes.
	h.away()
	for i := 0; i < 26; i++ {
		h.send(second, "")
		time.Sleep(100 * time.Millisecond)
	}
	if notices := h.rec.noticeText(); strings.Contains(notices, "keepalive signals stopped") {
		t.Fatalf("typing in a duplicate did not keep the shared clock alive:\n%s", notices)
	}
	if first.closed() {
		t.Fatal("the original tab was closed while the duplicate was in use")
	}

	// And once both go quiet, the shared window expires for both.
	h.waitFor(first, "the pause after both tabs went quiet", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals stopped")
	})
	if second.closed() {
		t.Fatal("the duplicate was closed by the idle window")
	}
}

func TestSetIdleTimeoutReachesOpenSessions(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetIdleTimeout(24 * time.Hour)
	cfg := h.config()
	cfg.KeepAliveSeconds = 1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	// Lowering the window must reach the open connection, not only the next
	// one. A visible window still holds the probes: the setting is half of the
	// decision, not all of it.
	h.mgr.SetIdleTimeout(150 * time.Millisecond)
	time.Sleep(700 * time.Millisecond)
	if notices := h.rec.noticeText(); strings.Contains(notices, "keepalive signals stopped") {
		t.Fatalf("lowering the window stopped a visible session's probes:\n%s", notices)
	}

	h.away()
	h.waitFor(sess, "the lowered window and an away window to pause the probes", func() bool {
		return strings.Contains(h.rec.noticeText(), "keepalive signals stopped")
	})
	if sess.closed() {
		t.Fatal("lowering the idle window closed the session")
	}
}

func TestIdleTimeoutIsClampedBothWays(t *testing.T) {
	cases := []struct {
		hours int
		want  time.Duration
	}{
		{0, DefaultIdleTimeoutHours * time.Hour},  // "unset" means the default, never "no window"
		{-5, DefaultIdleTimeoutHours * time.Hour}, // ditto
		{1, time.Hour},
		{6, 6 * time.Hour},
		{MaxIdleTimeoutHours, MaxIdleTimeoutHours * time.Hour},
		{5000, MaxIdleTimeoutHours * time.Hour}, // a hand-edited file cannot ask for a decade
	}
	for _, tc := range cases {
		if got := IdleTimeout(tc.hours); got != tc.want {
			t.Errorf("IdleTimeout(%d) = %s, want %s", tc.hours, got, tc.want)
		}
	}
}

func TestIdleWindowLabelsAreReadable(t *testing.T) {
	cases := []struct {
		d    time.Duration
		want string
	}{
		{24 * time.Hour, "24 hours"},
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
