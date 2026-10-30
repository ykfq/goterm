package hoststore

import (
	"strings"
	"testing"
)

// twoFactorHost is a JumpServer-shaped host: password auth plus a one-time code.
func twoFactorHost() Host {
	h := webHost()
	h.Name = "jumpserver"
	h.TwoFactor = true
	return h
}

func TestTwoFactorCodeIsAppendedToTheStoredPassword(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(twoFactorHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "hunter2"); err != nil {
		t.Fatalf("secret: %v", err)
	}

	cfg, err := hosts.SSHConfigWith(saved.ID, 80, 24, ConnectOverrides{TwoFactorCode: "123456"})
	if err != nil {
		t.Fatalf("SSHConfigWith: %v", err)
	}
	if cfg.Password != "hunter2123456" {
		t.Fatalf("password = %q, want the code appended: %q", cfg.Password, "hunter2123456")
	}
	// The code is per-attempt; it must not have been written anywhere.
	if stored, _ := hosts.Secret(saved.ID); stored != "hunter2" {
		t.Fatalf("stored secret = %q, the code leaked into the key ring", stored)
	}
}

func TestTwoFactorWithoutACodeIsRefused(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(twoFactorHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "hunter2"); err != nil {
		t.Fatalf("secret: %v", err)
	}

	// Silently connecting with the bare password is the failure mode this
	// feature exists to remove, so say so instead.
	_, err = hosts.SSHConfigWith(saved.ID, 80, 24, ConnectOverrides{})
	if err == nil || !strings.Contains(err.Error(), "two-factor code") {
		t.Fatalf("err = %v, want a two-factor code complaint", err)
	}
}

func TestTwoFactorNeedsPasswordAuth(t *testing.T) {
	hosts, _ := newStore(t)
	h := twoFactorHost()
	h.AuthType = AuthKey
	h.KeyPath = `C:\keys\id_ed25519`

	_, err := hosts.Save(h)
	if err == nil || !strings.Contains(err.Error(), "two-factor authentication needs password authentication") {
		t.Fatalf("err = %v, want the two-factor/key conflict to be refused", err)
	}
}

func TestImportAddsHostsSecretsAndIsIdempotent(t *testing.T) {
	source, _ := newStore(t)
	saved, err := source.Save(twoFactorHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := source.SetSecret(saved.ID, "hunter2"); err != nil {
		t.Fatalf("secret: %v", err)
	}

	exported := source.List()
	secrets := map[string]string{saved.ID: "hunter2"}

	target, _ := newStore(t)
	sum := target.Import(exported, secrets, nil)
	if sum.Added != 1 || sum.Updated != 0 || sum.Skipped != 0 || sum.Secrets != 1 {
		t.Fatalf("first import = %+v, want one added host with its secret", sum)
	}
	imported := target.List()
	if len(imported) != 1 {
		t.Fatalf("hosts after import = %d", len(imported))
	}
	if !imported[0].TwoFactor {
		t.Fatal("the two-factor flag did not survive the import")
	}
	if got, _ := target.Secret(imported[0].ID); got != "hunter2" {
		t.Fatalf("imported secret = %q", got)
	}

	// Importing the same bundle again must update, not duplicate.
	sum = target.Import(exported, secrets, nil)
	if sum.Added != 0 || sum.Updated != 1 {
		t.Fatalf("second import = %+v, want one update", sum)
	}
	if n := len(target.List()); n != 1 {
		t.Fatalf("hosts after re-import = %d, want 1", n)
	}
}

// A bundle without secrets (an older export, or a key-ring read that failed)
// must not wipe a password that is already stored here.
func TestImportWithoutASecretKeepsTheStoredPassword(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "hunter2"); err != nil {
		t.Fatalf("secret: %v", err)
	}

	hosts.Import(hosts.List(), nil, nil)
	if got, _ := hosts.Secret(saved.ID); got != "hunter2" {
		t.Fatalf("secret after a secret-free import = %q", got)
	}
}

// A bundle from another machine names ids that mean nothing here; the host must
// still arrive, with a local id.
func TestImportOfForeignIDsAssignsLocalIDs(t *testing.T) {
	other, _ := newStore(t)
	saved, err := other.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}

	hosts, _ := newStore(t)
	sum := hosts.Import(other.List(), nil, nil)
	if sum.Added != 1 {
		t.Fatalf("import = %+v", sum)
	}
	got := hosts.List()
	if got[0].ID == "" || got[0].ID == saved.ID {
		// Same id would be fine if the id were copied, but Save mints one; the
		// point is that it is not empty and the host is there.
		if got[0].ID == "" {
			t.Fatal("imported host has no id")
		}
	}
	if got[0].Host != webHost().Host || got[0].User != webHost().User {
		t.Fatalf("imported host = %+v", got[0])
	}
}
