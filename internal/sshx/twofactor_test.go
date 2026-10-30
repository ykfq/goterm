package sshx

import (
	"strings"
	"testing"

	"github.com/ykfq/goterm/internal/sshtest"
)

// A TOTP bastion wants the password and the one-time code glued together, with
// no separator. hoststore is what does the gluing; these two tests prove the
// result is what such a server accepts — and that the code-less login it
// replaces does not sneak through.
func TestTwoFactorLoginIsPasswordPlusCode(t *testing.T) {
	h := newHarness(t, sshtest.Options{TwoFactor: true})
	cfg := h.config() // "correct horse battery staple"
	cfg.Password += "123456"

	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)

	if got := h.srv.AuthSuccess(); got != 1 {
		t.Fatalf("successful authentications = %d, want 1", got)
	}
}

func TestTwoFactorLoginWithoutACodeIsRefused(t *testing.T) {
	h := newHarness(t, sshtest.Options{TwoFactor: true})

	_, err := h.mgr.Connect(h.config())
	if err == nil {
		t.Fatal("a password-only login was accepted by a server that wants a code")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("err = %v, want an authentication failure", err)
	}
	if got := h.srv.AuthSuccess(); got != 0 {
		t.Fatalf("successful authentications = %d, want 0", got)
	}
}
