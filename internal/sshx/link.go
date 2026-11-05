package sshx

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// connRef is one SSH connection and the sessions opened on it.
//
// A duplicate session (Xshell's "Duplicate") opens another channel on the *same*
// client: no second handshake, no password, no one-time code. That means the
// client no longer belongs to a single tab — it has to outlive whichever session
// happens to close first — so ownership and the keepalive live here instead.
//
// The refcount is the whole safety property: closing a tab releases one
// reference, and the connection (plus the agent and any jump-host tunnel riding
// on it) is torn down only when the last session using it is gone.
type connRef struct {
	client  *ssh.Client
	bastion *ssh.Client
	closers []io.Closer
	every   time.Duration

	// The idle window. A keepalive exists to notice a dead link and to defend a
	// session that someone is using; it is not a way to hold a login open for
	// ever. Once the user has typed nothing for `idle`, the probes stop and the
	// connection is left to the server's own idle policy — a login forgotten on
	// Friday dies over the weekend instead of surviving until Monday.
	//
	// The clock is per connection (a duplicate shares it), and only typed input
	// resets it: remote output, window resizes and the probes themselves do not
	// count as "the user is here".
	idleMu       sync.Mutex
	idle         time.Duration
	idlePaused   bool
	lastActivity atomic.Int64 // unix nanoseconds of the last typed input

	kaStop    chan struct{}
	kaOnce    sync.Once
	closeOnce sync.Once

	mu       sync.Mutex
	refs     int
	sessions map[*Session]struct{}
}

// newConnRef takes ownership of client, bastion and closers, counting one
// reference for the session that was just opened on them. A positive idle
// window is enforced by the keepalive from the connection's first probe on.
func newConnRef(client, bastion *ssh.Client, closers []io.Closer, every, idle time.Duration) *connRef {
	l := &connRef{
		client:   client,
		bastion:  bastion,
		closers:  closers,
		every:    every,
		idle:     idle,
		kaStop:   make(chan struct{}),
		refs:     1,
		sessions: make(map[*Session]struct{}),
	}
	l.lastActivity.Store(time.Now().UnixNano())
	return l
}

func (l *connRef) track(s *Session) {
	l.mu.Lock()
	l.sessions[s] = struct{}{}
	l.mu.Unlock()
}

func (l *connRef) untrack(s *Session) {
	l.mu.Lock()
	delete(l.sessions, s)
	l.mu.Unlock()
}

// acquire holds the connection open for one more session.
func (l *connRef) acquire() {
	l.mu.Lock()
	l.refs++
	l.mu.Unlock()
}

// release drops one reference, closing everything at zero.
func (l *connRef) release() {
	l.mu.Lock()
	l.refs--
	last := l.refs <= 0
	l.mu.Unlock()
	if last {
		l.close()
	}
}

func (l *connRef) close() {
	l.closeOnce.Do(func() {
		close(l.kaStop)
		if l.client != nil {
			_ = l.client.Close()
		}
		for _, closer := range l.closers {
			if closer != nil {
				_ = closer.Close()
			}
		}
	})
}

// touch records typed input on any tab of this connection. Session.Write is the
// one path keystrokes, pastes and trzsz control data take, so this is exactly
// "the user is at the keyboard".
//
// If the idle window had parked the probes, typing resumes them immediately
// rather than at the next tick: the resume should not depend on how the
// keepalive interval happens to line up with the window.
func (l *connRef) touch() {
	l.lastActivity.Store(time.Now().UnixNano())
	l.idleMu.Lock()
	resumed := l.idlePaused
	l.idlePaused = false
	l.idleMu.Unlock()
	if resumed {
		l.announce("info", ": input detected — keepalive signals resumed")
	}
}

// setIdleTimeout replaces the idle window; a settings change reaches
// connections that are already open through this. There is no timer to rearm —
// the next keepalive tick re-evaluates the window.
func (l *connRef) setIdleTimeout(d time.Duration) {
	l.idleMu.Lock()
	l.idle = d
	l.idleMu.Unlock()
}

// probeAllowed is the keepalive gate: true while the user has been present
// within the idle window. Running out of the window is announced once, because
// "the session may go away by itself" is worth one line where the user is
// looking. Resuming is announced from touch, the moment the user is back.
func (l *connRef) probeAllowed() bool {
	l.idleMu.Lock()
	limit := l.idle
	paused := l.idlePaused
	announce := false
	if limit > 0 {
		want := time.Since(time.Unix(0, l.lastActivity.Load())) >= limit
		if want && !paused {
			paused = true
			l.idlePaused = true
			announce = true
		}
	}
	l.idleMu.Unlock()

	if announce {
		l.announce("info", ": idle for "+humanLifetime(limit)+" — keepalive signals stopped, so the server may now drop this session")
	}
	return !paused
}

// announce pushes one line to every tab on this connection, the way fail does:
// a shared connection has no single "the" session to talk to.
func (l *connRef) announce(level, suffix string) {
	l.mu.Lock()
	list := make([]*Session, 0, len(l.sessions))
	for s := range l.sessions {
		list = append(list, s)
	}
	l.mu.Unlock()

	for _, s := range list {
		s.mgr.notice(s.id, level, s.Info().Target+suffix)
	}
}

// humanLifetime is for the two sentences a user reads, so whole hours and whole
// minutes are spelled out instead of 12h0m0s.
func humanLifetime(d time.Duration) string {
	switch {
	case d >= time.Hour && d%time.Hour == 0:
		if hours := int(d / time.Hour); hours == 1 {
			return "1 hour"
		} else {
			return fmt.Sprintf("%d hours", hours)
		}
	case d >= time.Minute && d%time.Minute == 0:
		if minutes := int(d / time.Minute); minutes == 1 {
			return "1 minute"
		} else {
			return fmt.Sprintf("%d minutes", minutes)
		}
	default:
		return d.String()
	}
}

// startKeepAlive pokes both ends of the connection once: the target, and the
// bastion when the sessions are tunnelled — a bastion that idles out takes the
// tunnel, and with it every session on it, whatever the target thinks.
//
// One loop per *connection*, not per tab: five duplicates must not mean five
// probes every interval. Both loops consult the same idle gate, so a duplicated
// tab neither buys extra probing nor gets its own pause notice.
func (l *connRef) startKeepAlive() {
	l.kaOnce.Do(func() {
		if l.every <= 0 {
			return
		}
		go keepAlive(l.kaStop, l.client, l.every, keepAliveStrikes, l.probeAllowed, func(reason string) {
			l.fail("server " + reason)
		})
		if l.bastion != nil {
			go keepAlive(l.kaStop, l.bastion, l.every, keepAliveStrikes, l.probeAllowed, func(reason string) {
				l.fail("jump host " + reason)
			})
		}
	})
}

// fail is what an unanswered run of keepalives means for a shared connection:
// every session on it is gone, so each one says so instead of looking alive.
func (l *connRef) fail(reason string) {
	l.mu.Lock()
	list := make([]*Session, 0, len(l.sessions))
	for s := range l.sessions {
		list = append(list, s)
	}
	l.mu.Unlock()

	for _, s := range list {
		s.mgr.notice(s.id, "warn", s.Info().Target+": "+reason)
		s.finish("connection lost: " + reason)
	}
}
