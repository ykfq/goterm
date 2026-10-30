// Jump hosts are part of a saved host, but their credential is a second,
// separate secret: different machine, different password, different leak.
package hoststore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ykfq/goterm/internal/secrets"
	"github.com/ykfq/goterm/internal/testutil"
)

func proxyHost() Host {
	h := webHost()
	h.ProxyHost = "bastion.internal"
	h.ProxyPort = 2200
	h.ProxyUser = "jump"
	h.ProxyAuthType = AuthPassword
	return h
}

// openIn reopens a directory, the way a fresh process would after a restart.
func openIn(t *testing.T, dir string) (*Store, *secrets.Store) {
	t.Helper()
	secretsStore, err := secrets.OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	hosts, err := Open(dir, secretsStore)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return hosts, secretsStore
}

func TestProxyConfigRoundTrips(t *testing.T) {
	dir := testutil.TempDir(t)
	hosts, _ := openIn(t, dir)

	saved, err := hosts.Save(proxyHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ProxyHost != "bastion.internal" || saved.ProxyPort != 2200 || saved.ProxyAuthType != AuthPassword {
		t.Fatalf("proxy fields not stored: %+v", saved)
	}
	const targetPassword = "target-pw-canary"
	if err := hosts.SetSecret(saved.ID, targetPassword); err != nil {
		t.Fatalf("set target secret: %v", err)
	}
	if err := hosts.SetProxySecret(saved.ID, "jump-pw"); err != nil {
		t.Fatalf("set proxy secret: %v", err)
	}

	// A fresh process must see the same thing, including "a secret exists".
	reopened, _ := openIn(t, dir)
	list := reopened.List()
	if len(list) != 1 || !list[0].HasProxySecret {
		t.Fatalf("after reopen: %+v", list)
	}

	cfg, err := reopened.SSHConfig(saved.ID, 80, 24)
	if err != nil {
		t.Fatalf("SSHConfig: %v", err)
	}
	if cfg.Proxy == nil {
		t.Fatal("SSHConfig dropped the jump host")
	}
	if cfg.Proxy.Host != "bastion.internal" || cfg.Proxy.Port != 2200 || cfg.Proxy.User != "jump" {
		t.Fatalf("proxy = %+v", cfg.Proxy)
	}
	if cfg.Proxy.Password != "jump-pw" {
		t.Fatalf("proxy password = %q, want the stored secret", cfg.Proxy.Password)
	}
	// The two endpoints resolve their own credentials, from two separate items.
	if cfg.Password != targetPassword {
		t.Fatalf("target password = %q, want its own secret", cfg.Password)
	}
	if cfg.Proxy.Password == cfg.Password {
		t.Fatal("the proxy and the target share one secret; they must not")
	}

	// And the credential is not in the file that gets backed up.
	raw, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatalf("read hosts.json: %v", err)
	}
	if strings.Contains(string(raw), "jump-pw") || strings.Contains(string(raw), targetPassword) {
		t.Fatal("hosts.json contains a password")
	}
}

func TestProxyWithoutPasswordIsExplained(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(proxyHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "target-pw"); err != nil {
		t.Fatalf("set target secret: %v", err)
	}
	// The message is a marker now, not an explanation: the frontend matches it to
	// open the jump host's password prompt instead of reporting a failed login.
	if _, err := hosts.SSHConfig(saved.ID, 80, 24); err == nil || !strings.Contains(err.Error(), "password required (proxy)") {
		t.Fatalf("SSHConfig = %v, want the jump-host password marker", err)
	}
}

func TestChangingTheProxyDropsItsSecret(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(proxyHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetProxySecret(saved.ID, "jump-pw"); err != nil {
		t.Fatalf("set proxy secret: %v", err)
	}

	// Pointing the host at a different bastion invalidates the old credential.
	edited := proxyHost()
	edited.ID = saved.ID
	edited.ProxyHost = "other-bastion.internal"
	if _, err := hosts.Save(edited); err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if hosts.secrets.Has(proxySecretKey(saved.ID)) {
		t.Fatal("the old jump-host secret survived a jump-host change")
	}
	if err := hosts.SetProxySecret(saved.ID, "jump-pw"); err != nil {
		t.Fatalf("set proxy secret again: %v", err)
	}

	// Same for the method: switching to ssh-agent means there is nothing to keep.
	agentic := proxyHost()
	agentic.ID = saved.ID
	agentic.ProxyAuthType = AuthAgent
	if _, err := hosts.Save(agentic); err != nil {
		t.Fatalf("re-save as agent: %v", err)
	}
	if hosts.secrets.Has(proxySecretKey(saved.ID)) {
		t.Fatal("the jump-host secret survived a method change")
	}
}

func TestProxyClearedMeansDirect(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(proxyHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "target-pw"); err != nil {
		t.Fatalf("set target secret: %v", err)
	}
	direct := webHost()
	direct.ID = saved.ID
	cleared, err := hosts.Save(direct)
	if err != nil {
		t.Fatalf("re-save: %v", err)
	}
	if cleared.ProxyHost != "" || cleared.ProxyUser != "" || cleared.ProxyPort != 0 || cleared.ProxyAuthType != "" {
		t.Fatalf("clearing the jump host left fields behind: %+v", cleared)
	}
	cfg, err := hosts.SSHConfig(saved.ID, 80, 24)
	if err != nil {
		t.Fatalf("SSHConfig: %v", err)
	}
	if cfg.Proxy != nil {
		t.Fatalf("Proxy = %+v, want nil", cfg.Proxy)
	}
}

func TestPassphraseOverridesBeatStoredSecrets(t *testing.T) {
	hosts, _ := newStore(t)
	host := proxyHost()
	host.AuthType = AuthKey
	host.KeyPath = `C:\keys\id_ed25519`
	host.ProxyAuthType = AuthKey
	host.ProxyKeyPath = `C:\keys\jump_ed25519`
	saved, err := hosts.Save(host)
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "stored-target"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	if err := hosts.SetProxySecret(saved.ID, "stored-jump"); err != nil {
		t.Fatalf("set proxy secret: %v", err)
	}

	// What the prompt collected wins for this attempt...
	cfg, err := hosts.SSHConfigWith(saved.ID, 80, 24, ConnectOverrides{Passphrase: "typed-target", ProxyPassphrase: "typed-jump"})
	if err != nil {
		t.Fatalf("SSHConfigWith: %v", err)
	}
	if cfg.Passphrase != "typed-target" || cfg.Proxy.Passphrase != "typed-jump" {
		t.Fatalf("passphrases = %q / %q, want the typed ones", cfg.Passphrase, cfg.Proxy.Passphrase)
	}

	// ...and empty arguments fall back to the key ring, which is how a saved
	// host reconnects without asking again.
	cfg, err = hosts.SSHConfigWith(saved.ID, 80, 24, ConnectOverrides{})
	if err != nil {
		t.Fatalf("SSHConfigWith: %v", err)
	}
	if cfg.Passphrase != "stored-target" || cfg.Proxy.Passphrase != "stored-jump" {
		t.Fatalf("passphrases = %q / %q, want the stored ones", cfg.Passphrase, cfg.Proxy.Passphrase)
	}
}

func TestJumpHostValidation(t *testing.T) {
	hosts, _ := newStore(t)
	cases := []struct {
		name string
		mut  func(*Host)
		want string
	}{
		{"no user", func(h *Host) { h.ProxyUser = "" }, "jump host username is required"},
		{"bad port", func(h *Host) { h.ProxyPort = 70000 }, "jump host port 70000 out of range"},
		{"no method", func(h *Host) { h.ProxyAuthType = "" }, "choose an authentication method for the jump host"},
		{"bogus method", func(h *Host) { h.ProxyAuthType = "carrier-pigeon" }, "unknown jump host authentication method"},
		{"key without file", func(h *Host) { h.ProxyAuthType = AuthKey }, "needs a private key file"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			host := proxyHost()
			tc.mut(&host)
			if _, err := hosts.Save(host); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Save = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}
