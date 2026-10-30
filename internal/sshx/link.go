package sshx

import (
	"fmt"
	"io"
	"sync"
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

	// The hard stop. A keepalive exists to notice a dead link, not to hold a
	// login open for ever: past `lifetime` this connection is closed whether or
	// not anyone is still typing. The clock starts when the connection does, so a
	// duplicated tab shares the remaining time rather than buying a new window.
	lifetime time.Duration
	started  time.Time

	kaStop    chan struct{}
	kaOnce    sync.Once
	closeOnce sync.Once

	lifeMu    sync.Mutex
	lifeTimer *time.Timer
	expired   bool

	mu       sync.Mutex
	refs     int
	sessions map[*Session]struct{}
}

// newConnRef takes ownership of client, bastion and closers, counting one
// reference for the session that was just opened on them. A positive lifetime
// arms the hard stop immediately.
func newConnRef(client, bastion *ssh.Client, closers []io.Closer, every, lifetime time.Duration) *connRef {
	l := &connRef{
		client:   client,
		bastion:  bastion,
		closers:  closers,
		every:    every,
		started:  time.Now(),
		kaStop:   make(chan struct{}),
		refs:     1,
		sessions: make(map[*Session]struct{}),
	}
	l.lifeMu.Lock()
	l.lifetime = lifetime
	l.lifeMu.Unlock()
	l.armLifetime(lifetime)
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
		l.lifeMu.Lock()
		if l.lifeTimer != nil {
			l.lifeTimer.Stop()
			l.lifeTimer = nil
		}
		l.lifeMu.Unlock()
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

// armLifetime (re)schedules the hard stop against the clock that started with the
// connection. A non-positive duration disables it, which is only what the tests
// ask for: the app always has a limit.
//
// Re-arming is how a lowered setting reaches sessions that are already open. The
// deadline is `started + d`, never `now + d`, so lowering it shortens a session —
// a limit that can be pushed forward by touching a setting is not a limit.
func (l *connRef) armLifetime(d time.Duration) {
	l.lifeMu.Lock()
	defer l.lifeMu.Unlock()
	if l.lifeTimer != nil {
		l.lifeTimer.Stop()
		l.lifeTimer = nil
	}
	l.lifetime = d
	if d <= 0 || l.expired {
		return
	}
	remaining := time.Until(l.started.Add(d))
	if remaining <= 0 {
		// Already past the new deadline (a much lower setting): stop now, off this
		// goroutine, because expire takes the locks below.
		go l.expire()
		return
	}
	l.lifeTimer = time.AfterFunc(remaining, l.expire)
}

// expire ends every session on the connection because the cap was reached.
//
// It says why in both places the user looks — a notice for the moment it
// happens, and the tab's own closed line afterwards — and it does not pretend to
// be a network failure: reconnecting is a thing the user is allowed to do.
func (l *connRef) expire() {
	l.lifeMu.Lock()
	if l.expired {
		l.lifeMu.Unlock()
		return
	}
	l.expired = true
	lifetime := l.lifetime
	if l.lifeTimer != nil {
		l.lifeTimer.Stop()
		l.lifeTimer = nil
	}
	l.mu.Lock()
	list := make([]*Session, 0, len(l.sessions))
	for s := range l.sessions {
		list = append(list, s)
	}
	l.mu.Unlock()
	l.lifeMu.Unlock()

	label := humanLifetime(lifetime)
	for _, s := range list {
		s.mgr.notice(s.id, "info", s.Info().Target+": closed after "+label+", the maximum session lifetime — reconnect to continue")
		s.finish("maximum session lifetime reached (" + label + ")")
	}
	l.close()
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
// probes every interval.
func (l *connRef) startKeepAlive() {
	l.kaOnce.Do(func() {
		if l.every <= 0 {
			return
		}
		go keepAlive(l.kaStop, l.client, l.every, keepAliveStrikes, func(reason string) {
			l.fail("server " + reason)
		})
		if l.bastion != nil {
			go keepAlive(l.kaStop, l.bastion, l.every, keepAliveStrikes, func(reason string) {
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
