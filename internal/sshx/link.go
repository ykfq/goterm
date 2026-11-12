package sshx

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/sftpx"
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

	// The ledge. A keepalive defence is not a way to hold a login open for ever,
	// but "the user has not typed" is the wrong thing to stop on: a bastion —
	// JumpServer is the one this was written against — watches the *connection*,
	// not the keyboard, and hangs up a session that has sent nothing for an hour,
	// however plainly the person is still reading its output. Stopping the probes
	// on a quiet keyboard therefore does not let the server's idle policy decide;
	// it guarantees the drop, at exactly the moment the user looked away.
	//
	// So the probes do not stop while the user is there. The defence ends once
	// nothing has been typed for `idle` *and* the user has declared themselves
	// away (`detach`, the window obscured or minimised): a login left behind on
	// Friday dies over the weekend instead of surviving until Monday, and one
	// that is on screen is never cut off underneath the person reading it.
	//
	// The clock is per connection (a duplicate shares it), and only typed input
	// resets it. The ledge only narrows the window; it never ends a session
	// that someone has interacted with inside it.
	idleMu       sync.Mutex
	idle         time.Duration
	away         bool
	idlePaused   bool
	lastActivity atomic.Int64 // unix nanoseconds of the last typed input

	kaStop    chan struct{}
	kaOnce    sync.Once
	closeOnce sync.Once

	// sftp is the file-transfer subsystem channel, opened on first use and
	// shared by every tab on this connection. sftpMu guards it.
	sftpMu sync.Mutex
	sftp   *sftpx.Client

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
		l.sftpMu.Lock()
		if l.sftp != nil {
			_ = l.sftp.Close()
			l.sftp = nil
		}
		l.sftpMu.Unlock()
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

// sftpClient opens the file-transfer subsystem on first use and caches it.
// Tabs duplicated from this connection share the channel, so browsing in one
// tab and downloading in another never opens a second subsystem.
func (l *connRef) sftpClient() (*sftpx.Client, error) {
	l.sftpMu.Lock()
	defer l.sftpMu.Unlock()
	if l.sftp != nil {
		return l.sftp, nil
	}
	c, err := sftpx.Open(l.client)
	if err != nil {
		return nil, err
	}
	l.sftp = c
	return c, nil
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

// setDetached records whether the user has put this connection out of sight.
// Going away starts the clock now: a tab obscured at 09:00 and never touched
// must not be defended until 09:00 the next day just because it was typed into
// at 08:59 and the app was then hidden.
func (l *connRef) setDetached(detached bool) {
	l.idleMu.Lock()
	l.away = detached
	l.idleMu.Unlock()
	if detached {
		l.lastActivity.Store(time.Now().UnixNano())
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

// probeAllowed is the keepalive gate. It closes only when both halves of "the
// user is gone" hold: nothing has been typed for the idle window, and the user
// has put the window out of sight. Either one alone is not evidence of an
// abandoned login, and stopping on the weaker signal is what loses sessions to
// a bastion. Running out of the window is announced once, because "the session
// may go away by itself" is worth one line where the user is looking. Resuming
// is announced from touch, the moment the user is back.
func (l *connRef) probeAllowed() bool {
	l.idleMu.Lock()
	limit := l.idle
	paused := l.idlePaused
	quiet := limit > 0 && time.Since(time.Unix(0, l.lastActivity.Load())) >= limit
	away := l.away
	want := quiet && away
	l.idleMu.Unlock()

	if want && !paused {
		l.idleMu.Lock()
		paused = l.idlePaused
		l.idlePaused = true
		l.idleMu.Unlock()
		if !paused {
			l.announce("info", ": away for "+humanLifetime(limit)+" — keepalive signals stopped, so the server may now drop this session")
		}
	}
	if !want && paused {
		// The user came back to a window that is on screen again. Resume at
		// once rather than on a probe that may not come for another interval.
		l.idleMu.Lock()
		was := l.idlePaused
		l.idlePaused = false
		l.idleMu.Unlock()
		if was {
			l.announce("info", ": back at the keyboard — keepalive signals resumed")
		}
	}
	return !paused && !want
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
