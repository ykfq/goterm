package sshx

import (
	"errors"
	"fmt"
	"sync/atomic"
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
	// DefaultKeepAlive is how often an idle connection is poked.
	//
	// Fifteen seconds, and it is deliberately not the keystroke's cadence. The
	// two layers defend different timers: this one keeps a *connection* alive
	// (a NAT table, a load balancer, an sshd `ClientAliveInterval`), and those
	// idle budgets are the ones measured in tens of seconds — a probe every two
	// minutes is already too slow for a one-minute one, and the failure is
	// silent. The cost is one four-byte request and its reply per quarter
	// minute, and nothing is written to the PTY.
	DefaultKeepAlive = 15 * time.Second
	// keepAliveStrikes is how many unanswered probes in a row mean "the link is
	// gone" rather than "the link is slow".
	keepAliveStrikes = 3
	// minKeepAlive floors the interval so a bad config cannot turn the probe
	// into a hot loop.
	minKeepAlive = time.Second
	// probeReplyWithin caps how long one probe waits for its reply. Three of
	// these still fit inside DefaultKeepAlive, so a slow-but-alive server is
	// never misjudged because the cadence and the timeout were the same number.
	probeReplyWithin = 5 * time.Second
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

// keepAlive pokes sender every `every` until stop closes, the transport errors,
// or a probe goes unanswered `strikes` times in a row.
//
// A timeout is not a death certificate. Several SSH servers — bastions included
// — simply do not answer global requests they do not implement, and closing a
// healthy session because of that is the opposite of what a keepalive is for.
// So a run of unanswered probes is reported once (note) and then ignored; only
// an error from the transport itself, or the kernel's own TCP keepalive, ends
// the connection.
//
// gate, when non-nil, decides whether a tick may probe at all. A closed gate
// parks the loop for that tick and resets the strike count: a connection that
// has been idle for hours is not "flaky", and the first probe after the user
// comes back must not be judged by failures from before the pause.
func keepAlive(stop <-chan struct{}, sender probeSender, every time.Duration, strikes int, gate func() bool, dead func(reason string), note func(message string)) {
	if sender == nil || every <= 0 || strikes <= 0 {
		return
	}
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	timeouts := 0
	var inFlight atomic.Bool
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			if gate != nil && !gate() {
				timeouts = 0
				continue
			}
			within := every
			if within > probeReplyWithin {
				within = probeReplyWithin
			}
			err := probeLimited(sender, within, &inFlight)
			if err == nil {
				timeouts = 0
				continue
			}
			if errors.Is(err, errNoReply) {
				timeouts++
				if timeouts == strikes && note != nil {
					note(fmt.Sprintf("the server has not answered %d keepalives in a row; the connection may be gone, but it is not closed on that alone", timeouts))
				}
				continue
			}
			// The transport itself failed: this is a dead connection.
			dead(fmt.Sprintf("%d keepalives in a row failed (%v)", timeouts+1, err))
			return
		}
	}
}

// errNoReply is a probe that was not answered in time, which says nothing about
// whether the connection is alive.
var errNoReply = errors.New("no reply")

// probe sends one request and waits at most `within` for its reply.
//
// The wait is bounded on purpose: a peer that vanished without closing the
// socket (a wifi drop, a suspended laptop) never answers and never errors, so
// a bare SendRequest would block forever and the loop would never notice.
func probe(sender probeSender, within time.Duration) error {
	return probeLimited(sender, within, nil)
}

// probeLimited is probe with at most one request outstanding. A server that
// never answers leaves the request blocked until the connection ends, and one
// such goroutine per unanswered probe would be a slow leak on exactly the
// connections that stay up the longest.
func probeLimited(sender probeSender, within time.Duration, inFlight *atomic.Bool) error {
	if inFlight != nil && !inFlight.CompareAndSwap(false, true) {
		return fmt.Errorf("%w within %s (the last one is still waiting)", errNoReply, within)
	}
	done := make(chan error, 1)
	go func() {
		_, _, err := sender.SendRequest(keepAliveRequest, true, nil)
		if inFlight != nil {
			inFlight.Store(false)
		}
		done <- err
	}()
	timer := time.NewTimer(within)
	defer timer.Stop()

	select {
	case err := <-done:
		return err
	case <-timer.C:
		return fmt.Errorf("%w within %s", errNoReply, within)
	}
}
