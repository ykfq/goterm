package sshx

import (
	"errors"
	"fmt"
	"time"
)

// SSH keepalive.
//
// An idle terminal is the first thing a bastion, load balancer or NAT drops —
// JumpServer, for instance, closes a session after 30 minutes without
// activity. The fix is the mechanism the OpenSSH client itself uses: a global
// "keepalive@openssh.com" request with want-reply, which the server answers
// outside the shell channel.
//
// Probing is deliberately not unconditional. A keepalive defends a session that
// someone is using; once the user has been quiet past the configured idle
// window it stops, so a login left behind on Friday dies of the server's own
// idle policy instead of being held open until Monday. The window is measured
// from the last typed input, not from when the connection was made.
//
// Nothing is ever written to the PTY for this. A newline would be input: the
// remote shell would run whatever half-typed line is sitting in its buffer,
// which is exactly the kind of surprise a keepalive must not cause.
const (
	// DefaultKeepAlive is how often an idle session is poked.
	DefaultKeepAlive = 45 * time.Second
	// keepAliveStrikes is how many unanswered probes in a row mean "the link is
	// gone" rather than "the link is slow".
	keepAliveStrikes = 3
	// minKeepAlive floors the interval so a bad config cannot turn the probe
	// into a hot loop.
	minKeepAlive = time.Second
	// keepAliveRequest is the OpenSSH global request. A server that does not
	// implement it still replies "no", and a reply — of either kind — proves
	// the connection is alive.
	keepAliveRequest = "keepalive@openssh.com"
)

// probeSender is the slice of *ssh.Client a keepalive actually needs, which is
// what makes the loop testable without a server.
type probeSender interface {
	SendRequest(name string, wantReply bool, payload []byte) (bool, []byte, error)
}

// keepAlive pokes sender every `every` until stop closes or `strikes` probes in
// a row go unanswered; then it calls dead once and returns.
//
// gate, when non-nil, decides whether a tick may probe at all. A closed gate
// parks the loop for that tick and resets the strike count: a connection that
// has been idle for hours is not "flaky", and the first probe after the user
// comes back must not be judged by failures from before the pause.
func keepAlive(stop <-chan struct{}, sender probeSender, every time.Duration, strikes int, gate func() bool, dead func(reason string)) {
	if sender == nil || every <= 0 || strikes <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	failures := 0
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if gate != nil && !gate() {
				failures = 0
				continue
			}
			if err := probe(sender, every); err != nil {
				failures++
				if failures >= strikes {
					dead(fmt.Sprintf("%d keepalives in a row were not answered (%v)", failures, err))
					return
				}
				continue
			}
			failures = 0
		}
	}
}

// probe sends one request and waits at most `within` for its reply.
//
// The wait is bounded on purpose: a peer that vanished without closing the
// socket (a wifi drop, a suspended laptop) never answers and never errors, so
// a bare SendRequest would block forever and the loop would never notice.
func probe(sender probeSender, within time.Duration) error {
	done := make(chan error, 1)
	go func() {
		_, _, err := sender.SendRequest(keepAliveRequest, true, nil)
		done <- err
	}()
	timer := time.NewTimer(within)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		return errors.New("no reply within " + within.String())
	}
}
