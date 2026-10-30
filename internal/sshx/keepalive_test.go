package sshx

import (
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

// recordingSender stands in for *ssh.Client so the loop can be tested at
// millisecond scale, and so a failing peer is a one-liner.
type recordingSender struct {
	mu      sync.Mutex
	names   []string
	replies []bool
	err     error
	block   chan struct{} // when set, SendRequest never returns
}

func (s *recordingSender) SendRequest(name string, wantReply bool, _ []byte) (bool, []byte, error) {
	if s.block != nil {
		<-s.block
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.names = append(s.names, name)
	s.replies = append(s.replies, wantReply)
	return false, nil, s.err
}

func (s *recordingSender) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.names)
}

func (s *recordingSender) snapshot() ([]string, []bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.names...), append([]bool(nil), s.replies...)
}

func TestKeepAlivePokesUntilItIsStopped(t *testing.T) {
	sender := &recordingSender{}
	stop := make(chan struct{})
	dead := make(chan string, 1)

	go keepAlive(stop, sender, 10*time.Millisecond, keepAliveStrikes, func(reason string) { dead <- reason })
	deadline := time.Now().Add(2 * time.Second)
	for sender.count() < 3 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(stop)
	time.Sleep(30 * time.Millisecond)

	if sender.count() < 3 {
		t.Fatalf("only %d keepalives went out", sender.count())
	}
	names, wantsReply := sender.snapshot()
	for i, name := range names {
		if name != keepAliveRequest {
			t.Fatalf("probe %d = %q, want %q", i, name, keepAliveRequest)
		}
		if !wantsReply[i] {
			t.Fatalf("probe %d asked for no reply; an unanswered probe cannot prove anything", i)
		}
	}
	select {
	case reason := <-dead:
		t.Fatalf("a healthy connection was declared dead: %s", reason)
	default:
	}
}

func TestKeepAliveDeclaresTheLinkDeadAfterStrikes(t *testing.T) {
	sender := &recordingSender{err: errors.New("use of closed network connection")}
	stop := make(chan struct{})
	dead := make(chan string, 1)

	keepAlive(stop, sender, 5*time.Millisecond, keepAliveStrikes, func(reason string) { dead <- reason })

	select {
	case reason := <-dead:
		if !strings.Contains(reason, "keepalives") || !strings.Contains(reason, "closed") {
			t.Fatalf("reason = %q", reason)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("a peer that never answers was never noticed")
	}
	if got := sender.count(); got != keepAliveStrikes {
		t.Fatalf("probes = %d, want exactly %d before giving up", got, keepAliveStrikes)
	}
}

func TestKeepAliveCanBeTurnedOff(t *testing.T) {
	sender := &recordingSender{}
	keepAlive(make(chan struct{}), sender, 0, keepAliveStrikes, func(string) {})
	if sender.count() != 0 {
		t.Fatalf("a disabled keepalive sent %d probes", sender.count())
	}
}

// A peer that is gone without closing the socket never answers and never errors.
// The probe must still come back, or the loop would sleep through the dropout.
func TestProbeGivesUpOnASilentPeer(t *testing.T) {
	sender := &recordingSender{block: make(chan struct{})}
	started := time.Now()
	err := probe(sender, 40*time.Millisecond)
	if err == nil {
		t.Fatal("a silent peer reported no error")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("probe waited %s; it is supposed to be bounded", elapsed)
	}
}

// The point of the whole feature: an idle session stays up, and the remote shell
// sees nothing. Anything landing in the PTY would echo off the repeater shell —
// and in a real shell, a stray newline runs whatever the user had half typed.
func TestKeepAliveNeverWritesToThePty(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	cfg := h.config()
	cfg.KeepAliveSeconds = 1
	sess := h.connect(cfg)

	h.expectOutput(sess, readyMarker)
	if got := sess.Info().KeepAliveSeconds; got != 1 {
		t.Fatalf("KeepAliveSeconds = %d, want 1", got)
	}

	quiet := h.rec.output(sess.ID())
	time.Sleep(2500 * time.Millisecond) // three probe windows

	if info := sess.Info(); info.Status != StatusConnected {
		t.Fatalf("session died while idle: %+v", info)
	}
	if after := h.rec.output(sess.ID()); string(after) != string(quiet) {
		t.Fatalf("the remote printed more while idle:\nbefore: %q\nafter:  %q", printable(quiet), printable(after))
	}
}

func TestKeepAliveCanBeDisabledPerConfig(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	cfg := h.config()
	cfg.KeepAliveSeconds = -1
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	if got := sess.Info().KeepAliveSeconds; got != 0 {
		t.Fatalf("KeepAliveSeconds = %d, want 0 when disabled", got)
	}
	// Nothing is announced about it: connecting raises no toast at all, because the
	// tab and the shell already say the session is up. The resolved interval lives on
	// the session info for whatever wants to show it.
	if got := h.rec.noticeText(); strings.Contains(got, "connected to") {
		t.Fatalf("connecting should not raise a notice:\n%s", got)
	}
}

func TestKeepAliveIntervalDefaultsAndFloors(t *testing.T) {
	if got := (Config{}).KeepAliveInterval(); got != DefaultKeepAlive {
		t.Fatalf("default interval = %s, want %s", got, DefaultKeepAlive)
	}
	if got := (Config{KeepAliveSeconds: -1}).KeepAliveInterval(); got != 0 {
		t.Fatalf("negative interval = %s, want 0 (off)", got)
	}
	if got := (Config{KeepAliveSeconds: 1}).KeepAliveInterval(); got != time.Second {
		t.Fatalf("1s = %s", got)
	}
	if got := (Config{KeepAliveSeconds: -0}).KeepAliveInterval(); got != DefaultKeepAlive {
		t.Fatalf("zero = %s, want the default", got)
	}
}
