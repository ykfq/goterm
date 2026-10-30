package hoststore

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ykfq/goterm/internal/secrets"
	"github.com/ykfq/goterm/internal/sshx"
	"github.com/ykfq/goterm/internal/testutil"
)

func newStore(t *testing.T) (*Store, *secrets.Store) {
	t.Helper()
	dir := testutil.TempDir(t)
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

func webHost() Host {
	return Host{Name: "web-01", Host: "10.0.0.8", Port: 2222, User: "deploy", AuthType: AuthPassword, Group: "prod"}
}

func TestSaveAssignsIdentityAndDefaults(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(Host{Host: "example.com", User: "root", AuthType: AuthPassword})
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("no id assigned")
	}
	if saved.Port != sshx.DefaultPort {
		t.Fatalf("port default = %d", saved.Port)
	}
	if saved.Name != "example.com" {
		t.Fatalf("name default = %q, want the address", saved.Name)
	}
	if saved.CreatedAt.IsZero() || saved.UpdatedAt.IsZero() {
		t.Fatal("timestamps not set")
	}
}

func TestSaveValidates(t *testing.T) {
	hosts, _ := newStore(t)
	cases := []struct {
		name string
		in   Host
		want string
	}{
		{"no address", Host{User: "u", AuthType: AuthPassword}, "host address is required"},
		{"no user", Host{Host: "h", AuthType: AuthPassword}, "username is required"},
		{"no auth type", Host{Host: "h", User: "u"}, "choose an authentication method"},
		{"bogus auth type", Host{Host: "h", User: "u", AuthType: "carrier-pigeon"}, "unknown authentication method"},
		{"key without file", Host{Host: "h", User: "u", AuthType: AuthKey}, "needs a private key file"},
		{"bad port", Host{Host: "h", User: "u", AuthType: AuthAgent, Port: 99999}, "out of range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := hosts.Save(tc.in)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want it to mention %q", err, tc.want)
			}
		})
	}
}

func TestSecretNeverLandsInTheJSONFile(t *testing.T) {
	dir := testutil.TempDir(t)
	secretsStore, err := secrets.OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	hosts, err := Open(dir, secretsStore)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const canary = "canary-password-never-persist"
	saved, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, canary); err != nil {
		t.Fatalf("set secret: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "hosts.json"))
	if err != nil {
		t.Fatalf("read hosts.json: %v", err)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatal("hosts.json contains the password")
	}
	if !strings.Contains(string(raw), "10.0.0.8") {
		t.Fatal("hosts.json lost the address, so the test proves nothing")
	}

	list := hosts.List()
	if len(list) != 1 {
		t.Fatalf("list = %+v", list)
	}
	if !list[0].HasSecret {
		t.Fatal("HasSecret should be true so the UI can show a filled key icon")
	}
	if got, err := hosts.Secret(saved.ID); err != nil || got != canary {
		t.Fatalf("secret = %q, %v", got, err)
	}
}

func TestDeleteTakesTheSecretWithIt(t *testing.T) {
	hosts, secretsStore := newStore(t)
	saved, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "pw"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	if err := hosts.Delete(saved.ID); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, ok := hosts.Get(saved.ID); ok {
		t.Fatal("host survived delete")
	}
	if secretsStore.Has("host/" + saved.ID + "/secret") {
		t.Fatal("the secret outlived its host")
	}
	if err := hosts.Delete("nope"); err != nil {
		t.Fatalf("deleting a missing host should be a no-op, got %v", err)
	}
}

func TestChangingAuthTypeInvalidatesTheStoredSecret(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if err := hosts.SetSecret(saved.ID, "pw"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	saved.AuthType = AuthKey
	saved.KeyPath = `C:\keys\id_ed25519`
	if _, err := hosts.Save(saved); err != nil {
		t.Fatalf("update: %v", err)
	}
	if got, err := hosts.Secret(saved.ID); err != nil || got != "" {
		t.Fatalf("stale password survived an auth-type change: %q %v", got, err)
	}
}

func TestUpdateCannotForgeIdentity(t *testing.T) {
	hosts, _ := newStore(t)
	a, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	b, err := hosts.Save(Host{ID: "not-a-real-id", Host: "h", User: "u", AuthType: AuthAgent})
	if err == nil {
		t.Fatalf("saving with an unknown id should fail, got %+v", b)
	}
	if got, err := hosts.Save(Host{ID: a.ID, Name: "renamed", Host: a.Host, User: a.User, AuthType: a.AuthType}); err != nil {
		t.Fatalf("rename: %v", err)
	} else if got.ID != a.ID || got.Name != "renamed" {
		t.Fatalf("rename result = %+v", got)
	}
	if len(hosts.List()) != 1 {
		t.Fatalf("list = %+v, want one host", hosts.List())
	}
}

func TestSSHConfigCarriesSecretsToTheRightField(t *testing.T) {
	hosts, _ := newStore(t)

	pw, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if _, err := hosts.SSHConfig(pw.ID, 100, 30); err == nil || !strings.Contains(err.Error(), "password required") || !strings.Contains(err.Error(), "no saved password") {
		t.Fatalf("missing password should be marked and explained, got %v", err)
	}
	if err := hosts.SetSecret(pw.ID, "pw"); err != nil {
		t.Fatalf("set secret: %v", err)
	}
	cfg, err := hosts.SSHConfig(pw.ID, 120, 40)
	if err != nil {
		t.Fatalf("config: %v", err)
	}
	if cfg.Password != "pw" || cfg.Passphrase != "" || cfg.Cols != 120 || cfg.Rows != 40 || cfg.Port != 2222 {
		t.Fatalf("config = %+v", cfg)
	}

	keyed, err := hosts.Save(Host{Name: "keyed", Host: "k.example", User: "ubuntu", AuthType: AuthKey, KeyPath: "/home/u/.ssh/id_ed25519"})
	if err != nil {
		t.Fatalf("save key host: %v", err)
	}
	if err := hosts.SetSecret(keyed.ID, "passphrase"); err != nil {
		t.Fatalf("set passphrase: %v", err)
	}
	cfg, err = hosts.SSHConfig(keyed.ID, 80, 24)
	if err != nil {
		t.Fatalf("key config: %v", err)
	}
	if cfg.Passphrase != "passphrase" || cfg.Password != "" || cfg.KeyPath == "" {
		t.Fatalf("key config = %+v", cfg)
	}

	if _, err := hosts.SSHConfig("missing", 80, 24); err == nil {
		t.Fatal("config for an unknown host should fail")
	}
}

func TestPersistenceAndOrdering(t *testing.T) {
	dir := testutil.TempDir(t)
	secretsStore, err := secrets.OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	hosts, err := Open(dir, secretsStore)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, h := range []Host{
		{Name: "zeta", Host: "z", User: "u", AuthType: AuthAgent, Group: "prod"},
		{Name: "alpha", Host: "a", User: "u", AuthType: AuthAgent, Group: "prod"},
		{Name: "solo", Host: "s", User: "u", AuthType: AuthAgent},
	} {
		if _, err := hosts.Save(h); err != nil {
			t.Fatalf("save %s: %v", h.Name, err)
		}
	}

	reopened, err := Open(dir, secretsStore)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	list := reopened.List()
	if len(list) != 3 {
		t.Fatalf("list = %+v", list)
	}
	// Ungrouped sorts first, then prod alphabetically.
	if list[0].Name != "solo" || list[1].Name != "alpha" || list[2].Name != "zeta" {
		t.Fatalf("order = %s, %s, %s", list[0].Name, list[1].Name, list[2].Name)
	}
	groups := reopened.Groups()
	if len(groups) != 1 || groups[0] != "prod" {
		t.Fatalf("groups = %v", groups)
	}
}

func TestTouchRecordsLastUsed(t *testing.T) {
	hosts, _ := newStore(t)
	saved, err := hosts.Save(webHost())
	if err != nil {
		t.Fatalf("save: %v", err)
	}
	if !saved.LastUsed.IsZero() {
		t.Fatal("a fresh host should not look used")
	}
	hosts.Touch(saved.ID)
	got, _ := hosts.Get(saved.ID)
	if got.LastUsed.IsZero() {
		t.Fatal("Touch did not record anything")
	}
	hosts.Touch("does-not-exist") // must not panic
}

func TestOpenRejectsACorruptHostsFile(t *testing.T) {
	dir := testutil.TempDir(t)
	if err := os.WriteFile(filepath.Join(dir, "hosts.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	secretsStore, err := secrets.OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("secrets: %v", err)
	}
	_, err = Open(dir, secretsStore)
	if err == nil || !strings.Contains(err.Error(), "fix or remove") {
		t.Fatalf("err = %v, want an actionable message", err)
	}
}
