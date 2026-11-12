/*
 * The bastion regime.
 *
 * JumpServer does not watch the PTY; it watches the SSH connection. A session
 * that sends nothing for long enough is hung up, whatever the tab on screen
 * looks like. So the definition of "the user is here" that matters to the
 * server is a byte on the wire, not a keystroke: a keyboard that has been still
 * for an hour while someone reads output is still somebody's session.
 *
 * These tests stand up a server with exactly that policy. The policy must fire
 * — otherwise a test that passes proves nothing — and the session must be
 * alive afterwards anyway. See DECISIONS.md for why this narrows plan.md red
 * line 8 rather than escaping it.
 */
package sshx

import (
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

const bastionWindow = 2 * time.Second

// aQuietBastion is a bastion that hangs up on two seconds of silence.
func aQuietBastion(t *testing.T) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.New(sshtest.Options{
		Password:           jumpPassword,
		AllowForwarding:    true,
		HangUpAfterSilence: bastionWindow,
	})
	if err != nil {
		t.Fatalf("start bastion: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// watch proves the policy is really running: with no keepalive at all the same
// server must drop the connection, or "the session survived" means nothing.
func watch(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("waits out a real server policy")
	}
	srv := aQuietBastion(t)
	h := newHarness(t, sshtest.Options{})
	cfg := h.config()
	cfg.Proxy = &ProxyConfig{Host: "127.0.0.1", Port: srv.Port(), User: "tester", Password: jumpPassword}
	cfg.KeepAliveSeconds = -1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	deadline := time.Now().Add(4 * bastionWindow)
	for time.Now().Before(deadline) && srv.HungUp() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if srv.HungUp() == 0 {
		t.Fatal("the bastion never enforced its silence policy; this test cannot prove anything")
	}
	h.waitFor(sess, "the hang-up to reach the client", func() bool { return sess.closed() })
}

// A session tunnelled through a bastion with an idle policy, whose user types
// nothing for many times that policy, must still be there — and must not have
// had a single byte pushed into the shell to achieve it.
func TestJumpHostKeepsAQuietSessionAlive(t *testing.T) {
	watch(t)

	target := newHarness(t, sshtest.Options{})
	bastion := aQuietBastion(t)

	cfg := target.config()
	cfg.Proxy = &ProxyConfig{
		Host: "127.0.0.1", Port: bastion.Port(), User: "tester", Password: jumpPassword,
	}
	cfg.KeepAliveSeconds = 1
	sess := target.connect(cfg)
	target.expectOutput(sess, readyMarker)

	quiet := target.rec.output(sess.ID())
	time.Sleep(7 * time.Second) // three and a half bastion windows; zero keystrokes

	if hung := bastion.HungUp(); hung != 0 {
		t.Fatalf("the bastion hung up %d times on a session the user was watching", hung)
	}
	if info := sess.Info(); info.Status != StatusConnected {
		t.Fatalf("the session did not survive the bastion: %+v\n--- notices ---\n%s",
			info, target.rec.noticeText())
	}
	if after := target.rec.output(sess.ID()); string(after) != string(quiet) {
		t.Fatalf("the keepalive wrote to the shell:\nbefore: %q\nafter:  %q",
			printable(quiet), printable(after))
	}
	// The probe that saved it was a global request, and the request never
	// reached the PTY — the shell would have echoed it if it had.
	if strings.Contains(printable(target.rec.output(sess.ID())), keepAliveRequest) {
		t.Fatal("a keepalive request showed up in the terminal")
	}

	// And typing still works through everything that happened.
	target.send(sess, "echo still-here\r")
	target.expectOutput(sess, "still-here")
}

// The cadence has to fit inside the policy. Forty-five seconds — the interval
// this shipped with first — is longer than any bastion's idle window measured
// in minutes looks like at a glance, and that is exactly how a session is lost:
// the probe is late, not missing. Slowing the probe past the window must fail.
func TestAKeepaliveSlowerThanTheBastionLosesTheSession(t *testing.T) {
	watch(t)

	target := newHarness(t, sshtest.Options{})
	bastion := aQuietBastion(t)
	cfg := target.config()
	cfg.Proxy = &ProxyConfig{
		Host: "127.0.0.1", Port: bastion.Port(), User: "tester", Password: jumpPassword,
	}
	cfg.KeepAliveSeconds = 10 // five times the bastion's window
	sess := target.connect(cfg)
	target.expectOutput(sess, readyMarker)

	deadline := time.Now().Add(4 * bastionWindow)
	for time.Now().Before(deadline) && bastion.HungUp() == 0 {
		time.Sleep(20 * time.Millisecond)
	}
	if bastion.HungUp() == 0 {
		t.Fatal("a keepalive slower than the policy kept the connection alive; the test is not measuring what it claims")
	}
	target.waitFor(sess, "the session to end", func() bool { return sess.closed() })
}

// The same policy on the target's own sshd: a direct session is just as
// exposed, and gets the same defence.
func TestQuietTargetKeepsItsSession(t *testing.T) {
	watch(t)

	srv, err := sshtest.New(sshtest.Options{
		Password:           jumpPassword,
		HangUpAfterSilence: bastionWindow,
	})
	if err != nil {
		t.Fatalf("start server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	h := newHarness(t, sshtest.Options{})
	cfg := h.config()
	cfg.Host, cfg.Port = "127.0.0.1", srv.Port()
	cfg.KeepAliveSeconds = 1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	time.Sleep(6 * time.Second)

	if hung := srv.HungUp(); hung != 0 {
		t.Fatalf("the server hung up %d times on a quiet session", hung)
	}
	if info := sess.Info(); info.Status != StatusConnected {
		t.Fatalf("an idle-policy server hung up on a quiet session: %+v", info)
	}
}
