// Jump-host (SSH proxy) support, exercised against a second real SSH server
// acting as the bastion: the tunnel is a direct-tcpip channel, and the target's
// own handshake runs end to end through it.
package sshx

import (
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

const jumpPassword = "correct horse battery staple"

func TestConnectThroughJumpHost(t *testing.T) {
	target := newHarness(t, sshtest.Options{})
	jump := newHarness(t, sshtest.Options{AllowForwarding: true})

	cfg := target.config()
	cfg.Proxy = &ProxyConfig{
		Host: "127.0.0.1", Port: jump.srv.Port(), User: "tester", Password: jumpPassword,
	}

	sess := target.connect(cfg)
	target.expectOutput(sess, readyMarker)

	if jump.srv.Forwarded() == 0 {
		t.Fatal("the jump host never opened a direct-tcpip channel")
	}
	if got := sess.Info().Via; got != cfg.Proxy.Target() {
		t.Fatalf("session Via = %q, want %q", got, cfg.Proxy.Target())
	}
	// That is what the status bar renders (`via user@bastion`). Connecting no longer
	// raises a notice, so nothing else has to repeat it.

	// Typing still reaches the target's shell, not the bastion's.
	target.send(sess, "echo through the jump\r")
	target.expectOutput(sess, "echo through the jump")

	// Each endpoint is trusted on its own: the bastion's key and the target's
	// key are separate pins, so trusting one proves nothing about the other.
	pins := target.store.List()
	if len(pins) != 2 {
		t.Fatalf("expected a pin per endpoint, got %d: %+v", len(pins), pins)
	}
	seen := map[string]bool{}
	for _, pin := range pins {
		seen[pin.Address] = true
	}
	if !seen[cfg.Address()] || !seen[cfg.Proxy.Address()] {
		t.Fatalf("pins = %+v, want %s and %s", pins, cfg.Address(), cfg.Proxy.Address())
	}
}

func TestJumpHostAuthFailureNamesTheJumpHost(t *testing.T) {
	target := newHarness(t, sshtest.Options{})
	jump := newHarness(t, sshtest.Options{AllowForwarding: true})

	cfg := target.config()
	cfg.Proxy = &ProxyConfig{
		Host: "127.0.0.1", Port: jump.srv.Port(), User: "tester", Password: "not the password",
	}

	_, err := target.mgr.Connect(cfg)
	if err == nil {
		t.Fatal("a wrong jump-host password still connected")
	}
	// The message has to say *which* machine said no, or the user edits the
	// wrong password.
	if !strings.Contains(err.Error(), "jump host") {
		t.Fatalf("error should name the jump host: %v", err)
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error should explain the failure: %v", err)
	}
	if jump.srv.AuthFailures() == 0 {
		// The server records the refused handshake on its own goroutine, so give
		// it a moment before calling this a miss.
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) && jump.srv.AuthFailures() == 0 {
			time.Sleep(5 * time.Millisecond)
		}
		if jump.srv.AuthFailures() == 0 {
			t.Fatal("the jump host never saw the attempt")
		}
	}
	if jump.srv.Forwarded() != 0 {
		t.Fatal("a rejected bastion must not forward anything")
	}
}

func TestJumpHostWithoutForwardingExplainsItself(t *testing.T) {
	target := newHarness(t, sshtest.Options{})
	jump := newHarness(t, sshtest.Options{}) // AllowForwarding off, like a locked-down sshd

	cfg := target.config()
	cfg.Proxy = &ProxyConfig{
		Host: "127.0.0.1", Port: jump.srv.Port(), User: "tester", Password: jumpPassword,
	}

	_, err := target.mgr.Connect(cfg)
	if err == nil {
		t.Fatal("a bastion that forbids forwarding still connected")
	}
	if !strings.Contains(err.Error(), "refused to forward") {
		t.Fatalf("the error should say forwarding is off: %v", err)
	}
	if !strings.Contains(err.Error(), "AllowTcpForwarding") {
		t.Fatalf("the error should name the sshd option to change: %v", err)
	}
}

func TestProxyConfigNormalization(t *testing.T) {
	t.Run("an empty proxy block means no proxy", func(t *testing.T) {
		cfg := Config{Host: "h", User: "u", Password: "p", Proxy: &ProxyConfig{}}
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		if cfg.Proxy != nil {
			t.Fatalf("Proxy = %+v, want nil", cfg.Proxy)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		cfg := Config{
			Host: "target", User: "u", Password: "p",
			Proxy: &ProxyConfig{Host: "bastion", User: "bu", Password: "bp"},
		}
		if err := cfg.Normalize(); err != nil {
			t.Fatalf("Normalize: %v", err)
		}
		if cfg.Proxy.Port != DefaultPort {
			t.Fatalf("proxy port = %d, want %d", cfg.Proxy.Port, DefaultPort)
		}
		if cfg.Proxy.AuthType != AuthPassword {
			t.Fatalf("proxy auth = %q, want %q", cfg.Proxy.AuthType, AuthPassword)
		}
		if cfg.Proxy.Address() != "bastion:22" {
			t.Fatalf("proxy address = %q", cfg.Proxy.Address())
		}
	})

	t.Run("a jump host needs a user", func(t *testing.T) {
		cfg := Config{Host: "h", User: "u", Password: "p", Proxy: &ProxyConfig{Host: "bastion"}}
		err := cfg.Normalize()
		if err == nil || !strings.Contains(err.Error(), "user is required") {
			t.Fatalf("Normalize = %v, want a missing-user error", err)
		}
		if !strings.HasPrefix(err.Error(), "proxy:") {
			t.Fatalf("the error must say it came from the proxy: %v", err)
		}
	})

	t.Run("the target cannot be its own jump host", func(t *testing.T) {
		cfg := Config{
			Host: "h", Port: 22, User: "u", Password: "p",
			Proxy: &ProxyConfig{Host: "h", Port: 22, User: "bu", Password: "bp"},
		}
		if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "itself") {
			t.Fatalf("Normalize = %v, want a self-reference error", err)
		}
	})

	t.Run("key auth needs a key", func(t *testing.T) {
		cfg := Config{
			Host: "h", User: "u", Password: "p",
			Proxy: &ProxyConfig{Host: "b", User: "bu", AuthType: AuthKey},
		}
		if err := cfg.Normalize(); err == nil || !strings.Contains(err.Error(), "private key path") {
			t.Fatalf("Normalize = %v, want a missing-key error", err)
		}
	})
}

func TestRedactedMentionsTheJumpHost(t *testing.T) {
	cfg := Config{
		Host: "target", User: "u", Password: "p",
		Proxy: &ProxyConfig{Host: "bastion", User: "bu", Password: "bp"},
	}
	if err := cfg.Normalize(); err != nil {
		t.Fatalf("Normalize: %v", err)
	}
	text := cfg.Redacted()
	if !strings.Contains(text, "bastion:22") {
		t.Fatalf("Redacted = %q, want the jump host in it", text)
	}
	// The log line is user-visible in the notice stream: no secrets.
	if strings.Contains(text, "bp") || strings.Contains(text, "p ") {
		t.Fatalf("Redacted leaked a password: %q", text)
	}
}
