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
	"io"
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

// bookFromNow restarts the keystroke window at this instant: the clocks the
// loop books from, and the booking itself, so a test never inherits one made
// during connect.
func bookFromNow(sess *Session) {
	now := time.Now().UnixNano()
	sess.connectedAt.Store(now)
	sess.nudgeSentAt.Store(0)
	sess.nudgeDue.Store(0)
	sess.revive() // the loop may be asleep on the booking being replaced
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

	if !waitFor(t, 4*nudgeAfter, func() bool { return len(*trace) > 0 }) {
		t.Fatalf("no keystroke arrived; the session has booked %v",
			time.Until(time.Unix(0, sess.nudgeDue.Load())).Round(time.Millisecond))
	}
	if got := (*trace)[0]; got != "\"\\r\"" {
		t.Fatalf("the keystroke was %s, want a carriage return", got)
	}
}

// A half-typed command gets one space: it cannot submit the line, and it is
// typed input, which is what a shell's TMOUT and a bastion's activity detector
// count. (A space followed by a backspace was tried first; it did not reset the
// timer on the hosts this was reported from, so the plain space ships.)
// The shell records the wire bytes, so this is about what it actually received.
// A line the user is still editing is never touched. A space was tried here
// ("it cannot submit a line") and it was wrong: it lands wherever the cursor is,
// so `rm /tmp` becomes `rm / tmp` and the command that runs is the one nobody
// read. When the line is not provably empty the nudge sends nothing at all —
// and picks up again at the first line the user submits.
func TestNoKeystrokeWhileTheLineHasText(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	h := newHarness(t, sshtest.Options{
		Shell: func(sh *sshtest.Shell) {
			buf := make([]byte, 64)
			for {
				n, err := sh.In().Read(buf)
				if n > 0 {
					mu.Lock()
					received = append(received, buf[:n]...)
					mu.Unlock()
				}
				if err != nil {
					return
				}
			}
		},
	})
	h.mgr.SetNudge(NudgeOptions{On: true, Every: nudgeAfter})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1
	sess, err := h.mgr.Connect(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	h.send(sess, "rm /tmp")
	bookFromNow(sess)

	got := func() string {
		mu.Lock()
		defer mu.Unlock()
		return string(received)
	}
	// Three windows with the line held: not one byte may be added to it.
	time.Sleep(3 * nudgeAfter)
	if wire := got(); wire != "rm /tmp" {
		t.Fatalf("the nudge touched a line the user was editing: %q", wire)
	}

	// Submit the line, and the keystroke comes back — an Enter, now that the
	// line really is empty.
	h.send(sess, "\r")
	bookFromNow(sess)
	if !waitFor(t, 4*nudgeAfter, func() bool { _, sent, _ := sess.NudgeState(); return sent >= 1 }) {
		t.Fatalf("the keystroke did not resume after the line was submitted: %q", got())
	}
	if wire := got(); strings.Count(wire, "\r") < 2 {
		t.Fatalf("the resumed keystroke was not an Enter on the empty line: %q", wire)
	}
}

func TestNudgeLeavesTheSubmittedCommandLineAlone(t *testing.T) {
	h, sess := nudgeHarness(t)

	typeBytes(t, h, sess, "sleep 0.3; echo printed\r")
	h.expectOutput(sess, "printed")
	time.Sleep(3 * nudgeAfter)

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
	time.Sleep(3 * nudgeAfter)
	if len(*trace) != 0 {
		t.Fatalf("the keystroke reached the shell mid-transfer: %v", *trace)
	}

	sess.SetTransferring(false)
	bookFromNow(sess)
	if !waitFor(t, 4*nudgeAfter, func() bool { return len(*trace) > 0 }) {
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

// TestNudgeReachesACompletelyQuietShell is the case the user hit: a login that
// prints nothing at all until its own TMOUT fires. There is no output to start
// a clock from, and the first keystroke must not wait for one that never comes.
// The shell records what it actually received, so "it was nudged" is a fact
// about the wire rather than about our own bookkeeping.
func TestNudgeReachesACompletelyQuietShell(t *testing.T) {
	var mu sync.Mutex
	var received []byte
	h := newHarness(t, sshtest.Options{
		Shell: func(sh *sshtest.Shell) {
			buf := make([]byte, 64)
			for {
				n, err := sh.In().Read(buf)
				if n > 0 {
					mu.Lock()
					received = append(received, buf[:n]...)
					mu.Unlock()
				}
				if err != nil {
					return
				}
			}
		},
	})
	h.mgr.SetNudge(NudgeOptions{On: true, Every: nudgeAfter})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1

	sess, err := h.mgr.Connect(cfg)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	if n := sess.Info().BytesIn; n != 0 {
		t.Fatalf("this shell was supposed to print nothing, but %d bytes came back", n)
	}

	keystroke := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return len(received) > 0
	}
	if !waitFor(t, 4*nudgeAfter, keystroke) {
		t.Fatal("a shell that printed nothing was never nudged; TMOUT would have killed it")
	}
	// One keystroke per window, not a flood: a shell that answers nothing gives
	// the clock nothing to measure, and the loop must still keep its interval.
	// Before the first keystroke moved the clock, this shell was nudged nine
	// times in two seconds because every pass rebooked from an unchanged clock.
	time.Sleep(2 * nudgeAfter)
	mu.Lock()
	got := string(received)
	mu.Unlock()
	if got != "\r" && got != "\r\r" && got != "\r\r\r" {
		t.Fatalf("the keystrokes were %q; a quiet shell should see one per window", got)
	}
	if len(got) > 3 {
		t.Fatalf("%d keystrokes in three windows: the interval is not being kept", len(got))
	}
}

// A keystroke booked for "every two minutes" must not arrive at two minutes
// and twelve seconds. That is what a jitter added on top of the window cost:
// a shell whose TMOUT is exactly the window lost the race, and the user met a
// disconnect with the feature apparently switched on.
func TestNudgeNeverLandsLaterThanTheWindow(t *testing.T) {
	var mu sync.Mutex
	var stamps []time.Time
	traced = func(string, ...any) {
		mu.Lock()
		stamps = append(stamps, time.Now())
		mu.Unlock()
	}
	t.Cleanup(func() { traced = nil })

	const every = 2 * time.Second
	h := newHarness(t, sshtest.Options{})
	h.mgr.SetNudge(NudgeOptions{On: true, Every: every})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	bookFromNow(sess)

	count := func() int {
		mu.Lock()
		defer mu.Unlock()
		return len(stamps)
	}
	// Three keystrokes give two steady-state gaps; the first one may still be
	// carrying the booking made during connect.
	if !waitFor(t, 5*every, func() bool { return count() >= 4 }) {
		t.Fatalf("only %d keystrokes in %v", count(), 5*every)
	}

	mu.Lock()
	got := append([]time.Time(nil), stamps...)
	mu.Unlock()
	for i := 1; i < len(got); i++ {
		gap := got[i].Sub(got[i-1])
		if gap > every+400*time.Millisecond {
			t.Fatalf("keystroke %d came %v after the one before; the window is %v", i+1, gap.Round(time.Millisecond), every)
		}
		if gap < time.Second {
			t.Fatalf("keystroke %d came %v after the one before; the loop is not waiting for the window", i+1, gap.Round(time.Millisecond))
		}
	}
}

// NudgeState is what the status bar reads, and what tells "the switch is on"
// apart from "a byte left this session".
func TestNudgeStateCountsWhatLeft(t *testing.T) {
	_, sess := nudgeHarness(t)
	if on, sent, _ := sess.NudgeState(); !on || sent != 0 {
		t.Fatalf("fresh session: on=%v sent=%d, want on with nothing sent", on, sent)
	}
	if !waitFor(t, 4*nudgeAfter, func() bool { _, sent, _ := sess.NudgeState(); return sent > 0 }) {
		t.Fatal("the status bar would still show nothing sent")
	}
	if on, sent, dueIn := sess.NudgeState(); !on || sent < 1 || dueIn <= 0 {
		t.Fatalf("after a keystroke: on=%v sent=%d next=%v, want an armed session with a booking", on, sent, dueIn)
	}
}

// A lone ESC is what makes a terminal's line state unknowable — the old code
// treated that as "never nudge again", which is a session that dies with the
// feature apparently on. The line cannot be known to be empty, so the answer is
// a space: never an Enter.
// A lone ESC leaves the line in a state that cannot be proven empty, so the
// nudge stays out of it. That silence must end when the line does: the old bug
// was a state like this silencing the layer for the life of the session.
func TestALoneEscapeSilencesTheNudgeOnlyWhileItStands(t *testing.T) {
	trace := traceNudges(t)
	h, sess := nudgeHarness(t)
	typeBytes(t, h, sess, "\x1b")
	bookFromNow(sess)

	time.Sleep(3 * nudgeAfter)
	if len(*trace) != 0 {
		t.Fatalf("the nudge typed into a line it could not read: %v", *trace)
	}

	// Ctrl-C clears the line, and the keystroke is back. (The shell echoes it as
	// ^C, so the wait is for that rather than for the raw byte.)
	h.send(sess, "\x03")
	h.expectOutput(sess, "^C")
	bookFromNow(sess)
	if !waitFor(t, 4*nudgeAfter, func() bool { return len(*trace) > 0 }) {
		t.Fatal("the nudge never came back after the line was cleared")
	}
	if got := (*trace)[0]; got != "\"\\r\"" {
		t.Fatalf("the keystroke was %s, want an Enter on the now-empty line", got)
	}
}

// Output must not postpone the keystroke. A program that keeps printing does
// not reset a TMOUT — only input does — so a booking that followed the output
// clock would starve the shell exactly when it is busiest.
func TestContinuousOutputDoesNotPostponeTheKeystroke(t *testing.T) {
	trace := traceNudges(t)
	h := newHarness(t, sshtest.Options{
		Shell: func(sh *sshtest.Shell) {
			ticker := time.NewTicker(50 * time.Millisecond)
			defer ticker.Stop()
			for {
				select {
				case <-sh.Done():
					return
				case <-ticker.C:
					if _, err := io.WriteString(sh.Out(), "still printing\r\n"); err != nil {
						return
					}
				}
			}
		},
	})
	h.mgr.SetNudge(NudgeOptions{On: true, Every: nudgeAfter})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1
	sess := h.connect(cfg)
	h.expectOutput(sess, "still printing")
	bookFromNow(sess)

	if !waitFor(t, 5*nudgeAfter, func() bool { return len(*trace) > 0 }) {
		t.Fatal("a shell that prints constantly was never nudged; TMOUT would have killed it")
	}
}
