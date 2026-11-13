package sshx

/*
 * The idle keystroke.
 *
 * A keepalive request keeps a connection-level timer happy and never reaches
 * the shell. A shell with `TMOUT`, and a bastion that watches what the user
 * types, count only real input — so the second layer is a keystroke, and the
 * only question that matters is which keystroke is safe right now.
 *
 * These tests pin the answer, and the traps around it:
 *
 *   - an empty line gets Enter, because Enter there runs nothing;
 *   - a half-typed command gets a space with no Enter anywhere near it;
 *   - the command's own line is never modified;
 *   - a transfer owns the stream, so nothing is inserted;
 *   - the keystroke does not count as the user being present.
 */
import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

// nudgeAfter is the window the tests use instead of two minutes. nudgeFloor
// lets it through: the settings floor is for configuration files, not tests.
const nudgeAfter = 300 * time.Millisecond

func init() { nudgeFloor = nudgeAfter }

// nudgeHarness is a session with the keystroke turned on and fast, with its
// window booked from the moment the test starts watching.
func nudgeHarness(t *testing.T) (*harness, *Session) {
	t.Helper()
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetNudge(NudgeOptions{On: true, Every: nudgeAfter})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1 // the keystroke is what is under test, not the probe
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	bookFromNow(sess)
	return h, sess
}

// bookFromNow restarts the keystroke window at this instant on both clocks the
// loop reads, so a test never inherits a booking made during connect.
func bookFromNow(sess *Session) {
	now := time.Now().UnixNano()
	sess.lastOutput.Store(now)
	sess.connectedAt.Store(now)
	sess.nudgeQuietAt.Store(0)
	sess.nudgeDue.Store(0)
}

// typeBytes are what the test sends as "the user typing".
func typeBytes(t *testing.T, h *harness, sess *Session, s string) {
	t.Helper()
	h.send(sess, s)
	// The shell has to have echoed it, or the next nudge would race the type.
	h.expectOutput(sess, strings.TrimRight(s, "\r"))
	time.Sleep(20 * time.Millisecond)
}

// traceNudges records what each keystroke actually sent.
func traceNudges(t *testing.T) *[]string {
	t.Helper()
	var mu sync.Mutex
	seen := make([]string, 0, 4)
	traced = func(format string, args ...any) {
		mu.Lock()
		seen = append(seen, fmt.Sprintf(format, args...))
		mu.Unlock()
	}
	t.Cleanup(func() { traced = nil })
	return &seen
}

func waitFor(t *testing.T, wait time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return cond()
}

func TestNudgeEntersOnAnEmptyLine(t *testing.T) {
	trace := traceNudges(t)
	_, sess := nudgeHarness(t)

	if !waitFor(t, nudgeQuiet+3*nudgeAfter, func() bool { return len(*trace) > 0 }) {
		t.Fatalf("no keystroke arrived; the session has booked %v",
			time.Until(time.Unix(0, sess.nudgeDue.Load())).Round(time.Millisecond))
	}
	if got := (*trace)[0]; got != "\"\\r\"" {
		t.Fatalf("the keystroke was %s, want a carriage return", got)
	}
}

func TestNudgeSpaceWhenACommandIsHalfTyped(t *testing.T) {
	trace := traceNudges(t)
	h, sess := nudgeHarness(t)
	typeBytes(t, h, sess, "echo NEVER-RUNS")

	if !waitFor(t, nudgeQuiet+3*nudgeAfter, func() bool { return len(*trace) > 0 }) {
		t.Fatal("no keystroke arrived on a session sitting on a half-typed command")
	}
	if got := (*trace)[0]; got != "\" \"" {
		t.Fatalf("the keystroke was %s, want a space: an Enter here would run the command", got)
	}

	// The echo of the keystroke comes back from the shell, which is a hop
	// behind: wait for it rather than sampling.
	echoed := func() string { return printable(h.rec.output(sess.ID())) }
	if !waitFor(t, 3*nudgeAfter, func() bool { return strings.Contains(echoed(), "echo NEVER-RUNS ") }) {
		t.Fatalf("no space reached the half-typed line:\n%q", echoed())
	}
	// An Enter would have shown up as a second prompt, and the command would
	// have run.
	if strings.Count(echoed(), "NEVER-RUNS\r\n") > 0 {
		t.Fatalf("the half-typed command was submitted:\n%q", echoed())
	}
}

func TestNudgeLeavesTheSubmittedCommandLineAlone(t *testing.T) {
	h, sess := nudgeHarness(t)

	typeBytes(t, h, sess, "sleep 0.3; echo printed\r")
	h.expectOutput(sess, "printed")
	time.Sleep(nudgeQuietWait() + 2*nudgeAfter)

	out := printable(h.rec.output(sess.ID()))
	// The command's own line is the one the user typed, verbatim: a keystroke
	// inside it would show up as a space before the line ending.
	if !strings.Contains(out, "sleep 0.3; echo printed\\r\\n") {
		t.Fatalf("the command's own line was modified by a keystroke:\n%q", out)
	}
}

func TestNudgeStandsDownDuringATransfer(t *testing.T) {
	trace := traceNudges(t)
	_, sess := nudgeHarness(t)
	sess.SetTransferring(true)
	time.Sleep(2 * nudgeQuietWait())
	if len(*trace) != 0 {
		t.Fatalf("the keystroke reached the shell mid-transfer: %v", *trace)
	}

	sess.SetTransferring(false)
	bookFromNow(sess)
	if !waitFor(t, 3*nudgeQuietWait(), func() bool { return len(*trace) > 0 }) {
		t.Fatal("the keystroke did not resume after the transfer")
	}
}

// The idle window measures the user, not the app: a nudge must not count as
// someone being at the keyboard, or an abandoned tab would hold its login open
// for ever.
func TestNudgeDoesNotCountAsTheUserBeingPresent(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetIdleTimeout(time.Minute)
	cfg := h.config()
	cfg.KeepAliveSeconds = -1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	last := sess.link.lastActivity.Load()
	time.Sleep(50 * time.Millisecond)
	sess.writeNudge([]byte(" "))
	if now := sess.link.lastActivity.Load(); now != last {
		t.Fatal("the keystroke reset the user's activity clock")
	}
}

func TestTypedLineState(t *testing.T) {
	cases := []struct {
		typed string
		want  string
	}{
		{"", "empty"},
		{"ls -l", "text"},
		{"ls -l\r", "empty"},
		{"ls -l\n", "empty"},
		{"half typed", "text"},
		{"half typed\x7f\x7f", "text"}, // backspace deletes but may leave text
		{"oops\x03", "empty"},          // Ctrl-C clears the line
		{"exit\x04", "empty"},          // Ctrl-D likewise
		{"\x1b[C", "moved"},            // right arrow: nothing typed
		{"\x1b[1;5C", "moved"},         // modified arrow
		{"\x1b", "pending"},            // a prefix still being typed
		{"\x1b[", "pending"},           // a bracket that has no final byte yet
	}
	for _, tc := range cases {
		s := &Session{}
		s.trackTyped([]byte(tc.typed))
		if got, _ := s.typedLine(); got != tc.want {
			t.Errorf("after typing %q the line is %q, want %q", tc.typed, got, tc.want)
		}
	}
}

func TestNudgeOptionsAreBounded(t *testing.T) {
	for _, tc := range []struct {
		in   time.Duration
		want time.Duration
	}{
		{0, DefaultNudgeEvery},
		{time.Second, MinNudgeEvery},
		{3 * time.Hour, MaxNudgeEvery},
		{90 * time.Second, 90 * time.Second},
	} {
		nudgeFloor = 0 // the production floor, which is what is being pinned
		got := (NudgeOptions{Every: tc.in}).clamped().Every
		nudgeFloor = nudgeAfter
		if got != tc.want {
			t.Errorf("clamped(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}

// TestIdleKeystrokeDefeatsAUserActivityPolicy is the reason this layer exists at
// all: a bastion that hangs up on silence in the shell, not on the connection.
// The keepalive above it is off, so the session survives only if keystrokes
// really arrive.
func TestIdleKeystrokeDefeatsAUserActivityPolicy(t *testing.T) {
	if testing.Short() {
		t.Skip("waits out a real server policy")
	}
	srv, err := sshtest.New(sshtest.Options{
		Password:           "correct horse battery staple",
		HangUpAfterSilence: bastionWindow,
	})
	if err != nil {
		t.Fatalf("start bastion: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	// The policy must be real: the same server drops a session with nothing
	// sent at all.
	h0 := newHarness(t, sshtest.Options{})
	cfg0 := h0.config()
	cfg0.Host = "127.0.0.1"
	cfg0.Port = srv.Port()
	cfg0.KeepAliveSeconds = -1
	if s0 := h0.connect(cfg0); s0 != nil {
		h0.waitFor(s0, "the bastion to hang up", func() bool { return s0.closed() })
	}
	if srv.HungUp() == 0 {
		t.Fatal("the bastion never enforced its policy; this test cannot prove anything")
	}

	h := newHarness(t, sshtest.Options{})
	h.mgr.SetNudge(NudgeOptions{On: true, Every: nudgeAfter})
	cfg := h.config()
	cfg.Host = "127.0.0.1"
	cfg.Port = srv.Port()
	cfg.KeepAliveSeconds = -1 // no probe to hide behind
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	time.Sleep(4 * bastionWindow)
	if info := sess.Info(); info.Status != StatusConnected {
		t.Fatalf("the session did not survive the policy: %+v\n%s", info, h.rec.noticeText())
	}
}
