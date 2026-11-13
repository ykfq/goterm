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
// So there is a second layer, and it is on: every window (one minute by
// default) GoTerm types one keystroke into the session. It is deliberately as
// dumb as it can be, because every clever gate added in front of it has been a
// way for the session to die quietly:
//
//   - Line empty: a carriage return. It is what a user's Enter does on an
//     empty line — the shell answers with a fresh prompt and nothing runs.
//   - Anything else on the line: a space, with no Enter anywhere near it. A
//     space cannot submit a line, and it is still typed input, which is what
//     `TMOUT` and an activity-watching bastion count.
//
// "Anything else" deliberately swallows the cases the old code used to refuse:
// a half-typed command (a space is safe), a line recalled from history with an
// arrow key (a space is safe), and an escape sequence still being typed (a
// space is safe; an Enter there is not). Refusing was how a `>` prompt with
// half a loop on it, or a lone ESC, could silence the layer for good — and a
// silent layer is a session that dies anyway.
//
// Only two things stop it, and both are red lines: a transfer owns the stream
// (红线 4), and the user has been gone past the idle window (红线 8).
const (
	// DefaultNudgeEvery is the window between keystrokes: one minute, the
	// cadence the user asked for. Half the shortest `TMOUT` anyone sets (two
	// minutes), so a missed tick or a slow tick still leaves one in time.
	DefaultNudgeEvery = time.Minute
	// MinNudgeEvery and MaxNudgeEvery bound the setting.
	MinNudgeEvery = 15 * time.Second
	MaxNudgeEvery = 30 * time.Minute
)

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
// pause sleeps for d and returns early when a revive arrives, so the caller
// rebooks. It reports whether that happened.
//
// It is one timer, not a poll. An idle session can sit here for two minutes at
// a stretch, and waking it ten times a second to read a counter was work
// nobody asked for.
func (s *Session) pause(d time.Duration) (revived bool) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return false
	case <-s.wake():
		return true
	}
}

// wake is the channel a revive is delivered on. One slot, so a signal raised
// while nothing is waiting is remembered once and costs one early pass.
func (s *Session) wake() chan struct{} {
	s.nudgeWakeOnce.Do(func() { s.nudgeWake = make(chan struct{}, 1) })
	return s.nudgeWake
}

// revive wakes the loop out of a pause after a transfer: the booking it was
// holding may be a nudgePause away, and a transfer that just ended is exactly
// when the keystroke is worth having again.
func (s *Session) revive() {
	select {
	case s.wake() <- struct{}{}:
	default:
	}
}

// nudgeLoop is started once per session. The session's time at the prompt is
// what the first keystroke is measured from, so a session that has been open
// and quiet arrives here already due rather than starting a fresh window.
func (s *Session) nudgeLoop(stop <-chan struct{}, gate func() bool) {
	for {
		opts := s.nudgeOptions()
		if !opts.On {
			select {
			case <-stop:
				return
			case <-time.After(5 * time.Second):
			}
			// A fresh window when the switch goes back on.
			s.nudgeDue.Store(0)
			continue
		}
		now := time.Now()
		due := s.nudgeDue.Load()
		if due == 0 {
			// No booking yet: count from the last keystroke, or from the moment
			// the session opened. Output deliberately does not figure here — a
			// program that keeps printing does not reset a TMOUT, so letting
			// output postpone the keystroke is one more way for a session that
			// is busy but typed-at to be logged out.
			from := s.connectedAt.Load()
			if at := s.nudgeSentAt.Load(); at != 0 {
				from = at
			}
			s.nudgeDue.Store(book(time.Unix(0, from), opts.interval()).UnixNano())
			continue
		}
		if at := time.Unix(0, due); now.Before(at) {
			if s.pause(time.Until(at)) {
				s.nudgeDue.Store(0) // woken on purpose: book again
			}
			continue
		}
		if gate != nil && !gate() {
			// The user is gone past the idle window (红线 8): stop defending
			// the login, and look again later in case they come back.
			s.pause(nudgePause)
			s.nudgeDue.Store(0)
			continue
		}
		s.nudgeOnce()
		s.nudgeDue.Store(book(time.Now(), opts.interval()).UnixNano())
	}
}

// book is the next keystroke: one window from `from`, with the jitter taken off
// rather than added, so a booking is never later than the window.
func book(from time.Time, every time.Duration) time.Time {
	return from.Add(every - jitter(every))
}

// jitter spreads the bookings of many sessions by up to a tenth of the window,
// so a dozen tabs do not type into their shells on the same tick. It always
// comes off the booking, never on top of it: a keystroke a few seconds early is
// free, and one a few seconds late is a session lost to a timer that fired
// first.
func jitter(interval time.Duration) time.Duration {
	span := interval / 10
	if span <= 0 {
		return 0
	}
	return rand.N(span)
}

// nudgeOnce types one keystroke into the prompt and reports whether it went
// out. The rule is the user's, in the plainest form:
//
//	line empty   -> Enter   (a fresh prompt, nothing runs)
//	something on it -> a space (cannot submit a line)
//
// "Something" is any state the session is not sure is empty — text, a cursor
// moved through recalled history, or an escape sequence still half-typed. The
// safe answer for all of them is the same, so there is nothing left to get
// wrong.
func (s *Session) nudgeOnce() bool {
	if s.closed() {
		return false
	}
	if s.transferring.Load() {
		// A transfer owns the stream; either keystroke would land inside its
		// data (红线 4).
		s.diary("%s: keystroke suppressed: a transfer owns the stream", s.info.Target)
		return false
	}
	state, _ := s.typedLine()
	key := "space"
	if state == "empty" {
		key = "enter"
		_ = s.writeNudge([]byte("\r"))
	} else {
		_ = s.writeNudge([]byte(" "))
	}
	s.diary("%s: keystroke %s (line=%s)", s.info.Target, key, state)
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
	s.nudgeCount.Add(1)
	s.nudgeSentAt.Store(time.Now().UnixNano())
	nudgeTrace("%q", string(p))
	return nil
}

// NudgeState reports the idle keystroke for this session: whether it is armed,
// how many have gone out, and how long until the next one is due. The status
// bar shows it, because "the setting is on" and "a byte actually left" are
// different claims and only the second one keeps a session alive.
func (s *Session) NudgeState() (on bool, sent int64, dueIn time.Duration) {
	on = s.nudgeOptions().On
	sent = s.nudgeCount.Load()
	if due := s.nudgeDue.Load(); due > 0 {
		dueIn = time.Until(time.Unix(0, due))
		if dueIn < 0 {
			dueIn = 0
		}
	}
	return on, sent, dueIn
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
	next := opts.clamped()
	s.nudgeMu.Lock()
	changed := s.nudge != next
	s.nudge = next
	s.nudgeMu.Unlock()
	if changed {
		// A change applies now, not after whatever window the loop was already
		// asleep in: switching the keystroke on and waiting a minute for the
		// first one looks exactly like a switch that does nothing.
		s.revive()
	}
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
