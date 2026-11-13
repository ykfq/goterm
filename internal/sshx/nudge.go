package sshx

import (
	"math/rand/v2"
	"time"
)

// Idle nudge.
//
// A keepalive request never reaches the shell. A bastion that watches the
// connection (JumpServer, most load balancers) answers it and stays happy, but
// a bastion that watches *the user's activity*, or a shell whose own timer
// counts typed input (`TMOUT`), sees nothing at all — and logs the session out
// however healthy the transport is. The only thing those notice is a byte on
// the shell's input.
//
// So there is a second layer, and it is off unless the user asks for it: after
// a window with no output, GoTerm types one harmless keystroke into the
// session.
//
// The keystroke depends on what is standing at the prompt, because the two
// cases have opposite safe answers:
//
//   - Line empty: a carriage return is safe. It is what a user's Enter does on
//     an empty line — the shell answers with a fresh prompt and nothing runs.
//   - Line has text the user has not submitted: an Enter would run it. That is
//     the accident this whole layer has to avoid, so the nudge is a space with
//     no Enter anywhere near it. The half-typed line gains a space and safely
//     says nothing.
//
// Refusing to nudge while someone is halfway through a command was the old
// behaviour, and it is what left those sessions to die: a `>` prompt with half
// a loop on it, or a command someone walked away from, could never be kept
// alive. A space can.
//
// The one thing the space cannot do is turn a carriage return into something
// else, so "does the line have text" has to be answered from the input stream
// rather than guessed. Session tracks it (see typedLine) from the bytes the
// user actually typed — the transfer filter writes protocol through the same
// path, which is why transfer traffic is tracked apart.
const (
	// DefaultNudgeEvery is the window between nudges, measured from the last
	// output to reach the terminal. Two minutes is inside the five-minute
	// timers most shells and bastions use, with room for a missed tick.
	DefaultNudgeEvery = 2 * time.Minute
	// MinNudgeEvery and MaxNudgeEvery bound the setting.
	MinNudgeEvery = 15 * time.Second
	MaxNudgeEvery = 30 * time.Minute
	// nudgeQuiet needs this much silence first: a keystroke must never land in
	// the middle of a program printing.
	nudgeQuiet = 5 * time.Second
)

// nudgeQuietWait is nudgeQuiet as the loop uses it: the production value,
// shortened only when a test brought its own floor.
func nudgeQuietWait() time.Duration {
	if nudgeFloor > 0 && nudgeFloor < nudgeQuiet {
		return 4 * nudgeFloor
	}
	return nudgeQuiet
}

// nudgePause is how long a keystroke that cannot be sent safely is postponed
// by — and how long the loop waits after output stalled it, instead of looking
// at the clock again in the same millisecond.
var nudgePause = 30 * time.Second

// NudgeOptions is what the app configures, and what reaches a running session
// through Manager.SetNudge.
type NudgeOptions struct {
	// On sends the idle keystroke at all.
	On bool
	// Every is the window between nudges; zero means DefaultNudgeEvery.
	Every time.Duration
	// AutoSubmit allows the empty-line nudge to fall back to a space when it is
	// unsafe — during a transfer, or when the session cannot tell whether the
	// line is empty. Off by default: someone who asked for Enter asked for the
	// thing that resets a shell which only counts completed lines.
	AutoSubmit bool
}

// interval is the configured window with the default filled in.
func (o NudgeOptions) interval() time.Duration {
	if o.Every <= 0 {
		return DefaultNudgeEvery
	}
	return o.Every
}

// clamped keeps the app's value inside what the settings screen allows, so a
// hand-edited file cannot make the nudge a hot loop.
func (o NudgeOptions) clamped() NudgeOptions {
	o.Every = o.interval()
	floor := MinNudgeEvery
	if nudgeFloor > 0 && nudgeFloor < floor {
		// Tests bring their own floor; nothing else may go below the settings
		// minimum.
		floor = nudgeFloor
	}
	if o.Every < floor {
		o.Every = floor
	}
	if o.Every > MaxNudgeEvery {
		o.Every = MaxNudgeEvery
	}
	return o
}

// nudgeLoop types an idle keystroke into the shell for as long as the session
// lives.
//
// Each keystroke is booked in advance, at the moment the last activity was
// seen: "every" seconds after it. Output moves the booking, so a program that
// keeps printing is never interrupted, and the loop is back to sleeping until
// then rather than narrowing its timer towards the next output.
// pause sleeps for d, and returns early when a revive arrives. It reports
// whether that happened, so the caller can rebook.
func (s *Session) pause(d time.Duration) (revived bool) {
	gen := s.nudgeRevive.Load()
	deadline := time.Now().Add(d)
	timer := time.NewTimer(d)
	defer timer.Stop()
	for {
		select {
		case <-timer.C:
			return s.nudgeRevive.Load() != gen
		case <-time.After(100 * time.Millisecond):
			if s.nudgeRevive.Load() != gen {
				return true
			}
			if left := time.Until(deadline); left > 0 {
				timer.Reset(minDuration(left, 100*time.Millisecond))
				continue
			}
			return false
		}
	}
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// revive wakes the loop out of a pause after a transfer: the booking it was
// holding may be a nudgePause away, and a transfer that just ended is exactly
// when the keystroke is worth having again.
func (s *Session) revive() { s.nudgeRevive.Store(time.Now().UnixNano()) }

// nudgeLoop is started once per session. The session's time at the prompt is
// what the first keystroke is measured from, so a session that has been open
// and quiet arrives here already due rather than starting a fresh window.
func (s *Session) nudgeLoop(stop <-chan struct{}, gate func() bool) {
	var lastSeen int64 = -1
	// nudgeSent says the clock has moved past a keystroke at least once; until
	// then every pass rebooks from the activity clock.
	nudgeSent := false
	for {
		opts := s.nudgeOptions()
		if !opts.On {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Second):
			}
			lastSeen = -1
			continue
		}
		// Two windows, in this order: nudgeQuiet of silence, then the
		// configured interval. Both are absolute times, so every pass through
		// the loop — including one cut short by output starting again — waits
		// for exactly what is left rather than for the window from the top.
		now := time.Now()
		seen := s.lastOutput.Load()
		if seen == 0 {
			// Nothing has arrived yet: the window starts when the session did.
			seen = s.connectedAt.Load()
		}
		if lastSeen == -1 || seen != lastSeen {
			lastSeen = seen
			base := time.Unix(0, seen)
			if nudgeSent {
				// After the first keystroke the window counts from *it*, not
				// from a print the shell may never make again.
				base = time.Now()
			}
			quietAt := base.Add(nudgeQuietWait())
			s.nudgeQuietAt.Store(quietAt.UnixNano())
			s.nudgeDue.Store(quietAt.Add(opts.interval() + jitter(opts.interval())).UnixNano())
		}
		quietAt := time.Unix(0, s.nudgeQuietAt.Load())
		due := time.Unix(0, s.nudgeDue.Load())
		if now.Before(quietAt) || now.Before(due) {
			until := quietAt
			if due.After(until) {
				until = due
			}
			if s.pause(until.Sub(now)) {
				lastSeen = -1 // woken: book again from the current clock
			}
			continue
		}
		// The quiet window has passed and the interval with it. Output that
		// arrived in the meantime moved the clock and the next pass rebooks.
		if !s.nudgeOptions().On {
			continue
		}
		if gate != nil && !gate() {
			s.pause(nudgePause)
			lastSeen = seen
			continue
		}
		// From here on the loop keeps its own clock: a session that answers
		// nothing — the quiet login this feature exists for — must still be
		// nudged once per window rather than on every pass.
		nudgeSent = true
		if !s.nudgeOnce() {
			// Nothing can be sent right now. The loop comes back later with
			// this booking still in hand; rebuilding it from an activity clock
			// that has not moved would land in the past and spin.
			if s.pause(nudgePause) {
				lastSeen = -1
				continue
			}
			lastSeen = seen
			continue
		}
		lastSeen = -1
	}
}

// jitter spreads the bookings of many sessions by up to a tenth of the window,
// so a dozen tabs do not type into their shells on the same tick.
func jitter(interval time.Duration) time.Duration {
	span := interval / 10
	if span <= 0 {
		return 0
	}
	return rand.N(span)
}

// nudgeOnce decides what is safe and sends it. It reports whether a keystroke
// went out, so the loop can postpone the next one instead of spinning on a
// state that will not change on its own.
func (s *Session) nudgeOnce() bool {
	if s.closed() {
		return false
	}
	if s.transferring.Load() {
		// A transfer owns the stream; the space would land inside its data.
		return false
	}
	if s.lastOutput.Load() == 0 {
		// The session has printed nothing at all, so an empty line is the only
		// reading of the prompt that is possible — and the only keystroke it
		// allows is the one that runs nothing. Without this, a shell that says
		// nothing for minutes (a quiet login, a `TMOUT` about to fire) would
		// never be nudged at all.
		_ = s.writeNudge([]byte("\r"))
		return true
	}
	state, empty := s.typedLine()
	if !empty && state == "pending" {
		// Half a sequence is on the wire; whatever arrives next completes it,
		// and guessing at the line before then is how a command gets run.
		return false
	}
	_ = empty
	switch state {
	case "empty":
		// The shell's line is empty: Enter prints a fresh prompt and runs
		// nothing, which is the most a shell that only counts completed lines
		// will notice.
		_ = s.writeNudge([]byte("\r"))
	case "moved":
		// The user moved the cursor without typing: the line is still what the
		// shell has, and its own editor keeps that. One right arrow is a
		// keystroke that changes nothing.
		_ = s.writeNudge([]byte("\x1b[C"))
	case "text":
		// Text is on the line: a space is the one keystroke that cannot submit
		// it, and it resets every activity timer that counts typed input.
		_ = s.writeNudge([]byte(" "))
	default:
		// The state cannot be decided — an escape sequence the user started is
		// still unfinished. Say nothing rather than guess at an Enter.
		return false
	}
	return true
}

// writeNudge sends the keystroke and keeps it out of the user's activity clock:
// a nudge is the app keeping the session alive, not the user coming back, and
// counting it would let an abandoned tab hold its login open for the whole idle
// window.
func (s *Session) writeNudge(p []byte) error {
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if s.closed() {
		return ErrClosed
	}
	if _, err := s.stdin.Write(p); err != nil {
		s.finish(inputReason(err))
		return err
	}
	s.bytesOut.Add(int64(len(p)))
	nudgeTrace("%q", string(p))
	return nil
}

// Test hooks. Each has one fixed value in production and is only set by the
// tests in this package:
//
//   - nudgeFloor is the smallest interval a test may configure, so the
//     settings floor does not make a sub-second window untestable;
//   - nudgeTrace reports what a keystroke actually sent (the shell's echo is
//     not enough to tell a space from nothing).
var nudgeFloor time.Duration

var nudgeTrace = func(format string, args ...any) {
	if traced != nil {
		traced(format, args...)
	}
}

var traced func(format string, args ...any)

func (s *Session) nudgeOptions() NudgeOptions {
	s.nudgeMu.RLock()
	defer s.nudgeMu.RUnlock()
	return s.nudge
}

// setNudge stores the options. The bounds move with the caller: a window at or
// above the settings floor is what the user is promised, a window below it is
// only reachable from a test, and that test brought its own floor.
func (s *Session) setNudge(opts NudgeOptions) {
	s.nudgeMu.Lock()
	s.nudge = opts.clamped()
	s.nudgeMu.Unlock()
}

// typedState is what the session knows about the line at the prompt.
type typedState struct {
	// printable means anything printable has been typed since the last line
	// ending. It is never cleared by an erase: after a backspace the line may
	// still hold text, and an Enter then would run it.
	printable bool
	// esc is an escape sequence being decoded: ESC seen, then possibly "[" and
	// parameters. moved records that it was a cursor move rather than an edit.
	esc   []byte
	moved bool
}

// typedLine reports the state of the line standing at the prompt.
//
// "text" means a keystroke that submits a line would be dangerous: printable
// input landed there, or an erase sequence followed it and the line cannot be
// known to be empty any more.
func (s *Session) typedLine() (state string, empty bool) {
	s.typedMu.Lock()
	defer s.typedMu.Unlock()
	t := s.typed
	switch {
	case len(t.esc) > 0:
		// A sequence the user started and has not finished: nothing can be
		// known about the line until its final byte arrives.
		return "pending", false
	case t.printable:
		return "text", false
	case t.moved:
		return "moved", false
	default:
		return "empty", true
	}
}

// trackTyped folds user input into the line state.
func (s *Session) trackTyped(p []byte) {
	s.typedMu.Lock()
	defer s.typedMu.Unlock()
	t := &s.typed
	for _, b := range p {
		// Inside an escape sequence: only its final byte matters. "[", ";"
		// and the digits between them are parameters, not the end.
		if len(t.esc) > 0 {
			t.esc = append(t.esc, b)
			if len(t.esc) == 2 {
				if b != '[' {
					// A two-byte escape (ESC c, ESC 7, ...): done, no effect.
					t.esc = nil
				}
				continue
			}
			if b >= 0x40 && b <= 0x7e {
				switch b {
				case 'C', 'A', 'B', 'D', 'H', 'F':
					t.moved = true
				case 'K', 'J', 'P', '~':
					// Erase, delete, history: the line is no longer known to
					// be empty.
					t.printable = true
				}
				t.esc = nil
			}
			continue
		}
		switch b {
		case '\r', '\n':
			*t = typedState{}
		case 0x03, 0x04: // Ctrl-C, Ctrl-D: the line is gone
			*t = typedState{}
		case 0x1b:
			t.esc = []byte{b}
		case 0x7f, 0x08: // backspace: deletes, but the line may still hold text
			t.printable = true
			t.moved = false
		default:
			if b >= 0x20 {
				t.printable = true
				t.moved = false
			}
		}
	}
}
