/*
 * A saved host whose password was never stored.
 *
 * This used to dead-end: SSHConfigWith refused with "edit the host and enter it
 * again", so double-clicking such a host produced an error with no way forward —
 * while an encrypted *key* in the same situation opened a prompt. The fix is the
 * same shape as `sshx.ErrPassphraseRequired`: the store refuses locally (an empty
 * password must never be spent as a failed login on the remote, and its lockout
 * counter) but says so with a marker the frontend matches, and it accepts the
 * answer the prompt collected for this one attempt.
 *
 * These pin both halves of that contract: the words, and the fact that a typed
 * password is used but not persisted.
 */
package hoststore

import (
	"errors"
	"strings"
	"testing"
)

func TestMissingPasswordIsMarkedInsteadOfExplainedAway(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	_, err = hosts.SSHConfig(saved.ID, 100, 30)
	if err == nil {
		t.Fatal("a password host with no stored password produced a config")
	}
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("error %q does not wrap ErrPasswordRequired; the frontend cannot tell this from a real auth failure", err)
	}
	// The target form has no "(proxy)" in it — that suffix is how the frontend
	// decides which prompt to open.
	if strings.Contains(err.Error(), "(proxy)") {
		t.Fatalf("target error carries the jump-host marker: %q", err)
	}
	if !strings.Contains(err.Error(), "no saved password") {
		t.Fatalf("error %q does not say what is wrong", err)
	}
}

func TestMissingJumpHostPasswordIsMarkedAsSuch(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(proxyHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "target-pw"); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	_, err = hosts.SSHConfig(saved.ID, 100, 30)
	if err == nil {
		t.Fatal("a jump host with no stored password produced a config")
	}
	if !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("error %q does not wrap ErrPasswordRequired", err)
	}
	// Same spelling the passphrase marker uses, so one regex shape serves both.
	if !strings.Contains(err.Error(), "password required (proxy)") {
		t.Fatalf("error %q does not carry the jump-host marker", err)
	}
}

func TestTypedPasswordsAreUsedForOneAttemptAndForgotten(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(proxyHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	// What the frontend sends after its two prompts.
	cfg, err := hosts.SSHConfigWith(saved.ID, 100, 30, ConnectOverrides{
		Password:      "typed-target",
		ProxyPassword: "typed-jump",
	})
	if err != nil {
		t.Fatalf("SSHConfigWith(typed passwords): %v", err)
	}
	if cfg.Password != "typed-target" {
		t.Fatalf("password = %q, want the typed one", cfg.Password)
	}
	if cfg.Proxy == nil || cfg.Proxy.Password != "typed-jump" {
		t.Fatalf("proxy = %+v, want the typed jump-host password", cfg.Proxy)
	}

	// Nothing was written: "remember" is a separate SetHostSecret call, and a
	// prompt that was answered but not remembered must leave the ring untouched.
	if stored, err := hosts.Secret(saved.ID); err != nil || stored != "" {
		t.Fatalf("typed password was persisted: %q, %v", stored, err)
	}
	if stored, err := hosts.ProxySecret(saved.ID); err != nil || stored != "" {
		t.Fatalf("typed jump-host password was persisted: %q, %v", stored, err)
	}

	// And the next attempt is back to asking, not to a silent empty login.
	if _, err := hosts.SSHConfigWith(saved.ID, 100, 30, ConnectOverrides{}); !errors.Is(err, ErrPasswordRequired) {
		t.Fatalf("second attempt = %v, want ErrPasswordRequired again", err)
	}
}

func TestTypedPasswordStillCarriesTheTwoFactorCode(t *testing.T) {
	// The code is appended to whatever the password is for this attempt — stored
	// or typed. Getting this wrong sends a valid password without its code to a
	// bastion that will refuse it, which reads as "wrong password".
	hosts, _ := newStore(t)
	saved, err := hosts.Save(twoFactorHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	cfg, err := hosts.SSHConfigWith(saved.ID, 80, 24, ConnectOverrides{Password: "typed", TwoFactorCode: "123456"})
	if err != nil {
		t.Fatalf("SSHConfigWith: %v", err)
	}
	if cfg.Password != "typed123456" {
		t.Fatalf("password = %q, want the typed password with the code appended", cfg.Password)
	}
}
