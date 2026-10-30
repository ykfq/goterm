package main

// Export and import are driven here without a window: the dialogs are thin
// shells around exportTo/importFrom, and everything that can actually go wrong
// — what goes into the bundle, whether a password can leak in clear, whether the
// wrong passphrase is refused, whether an import duplicates instead of merging —
// is below that shell.

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/testutil"
)

const exportPassphrase = "correct export passphrase"

// seedHosts puts one two-factor host, its password, and a jump host with its own
// password into the given app.
func seedHosts(t *testing.T, app *App) hoststore.Host {
	t.Helper()
	saved, err := app.SaveHost(hoststore.Host{
		Name:          "jump-box",
		Host:          "10.0.0.8",
		Port:          2222,
		User:          "deploy",
		AuthType:      hoststore.AuthPassword,
		TwoFactor:     true,
		ProxyHost:     "bastion.internal",
		ProxyPort:     22,
		ProxyUser:     "jump",
		ProxyAuthType: hoststore.AuthPassword,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	if err := app.SetHostSecret(saved.ID, "hunter2"); err != nil {
		t.Fatalf("host secret: %v", err)
	}
	if err := app.SetHostProxySecret(saved.ID, "jump-secret"); err != nil {
		t.Fatalf("proxy secret: %v", err)
	}
	return saved
}

func TestExportThenImportRoundTripsThroughOneFile(t *testing.T) {
	source, _ := newTestApp(t)
	seedHosts(t, source)

	path := filepath.Join(testutil.TempDir(t), "bundle.json")
	report, err := source.exportTo(path, exportPassphrase)
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if report.Hosts != 1 || report.Secrets != 1 || report.Missing != 0 {
		t.Fatalf("export report = %+v, want one host and one target secret", report)
	}

	// The jump host's credential travels too, so it is not counted as missing.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read export: %v", err)
	}
	for _, secret := range []string{"hunter2", "jump-secret", "jump-box", "bastion.internal"} {
		if bytes.Contains(raw, []byte(secret)) {
			t.Fatalf("the export file contains %q in clear", secret)
		}
	}

	target, _ := newTestApp(t)
	imported, err := target.importFrom(path, exportPassphrase)
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if imported.Added != 1 || imported.Updated != 0 || imported.Skipped != 0 {
		t.Fatalf("import report = %+v", imported)
	}

	list := target.ListHosts()
	if len(list) != 1 {
		t.Fatalf("hosts after import = %d, want 1", len(list))
	}
	if !list[0].TwoFactor {
		t.Fatal("the two-factor flag did not survive the round trip")
	}
	if !list[0].HasSecret || !list[0].HasProxySecret {
		t.Fatalf("secrets did not land: %+v", list[0])
	}

	// The imported host must actually connect the way it was saved: password
	// plus code, and the jump host's own password.
	cfg, err := target.hosts.SSHConfigWith(list[0].ID, 100, 30, hoststore.ConnectOverrides{TwoFactorCode: "654321"})
	if err != nil {
		t.Fatalf("ssh config: %v", err)
	}
	if cfg.Password != "hunter2654321" {
		t.Fatalf("password = %q, want the code appended to the imported secret", cfg.Password)
	}
	if cfg.Proxy == nil || cfg.Proxy.Password != "jump-secret" {
		t.Fatalf("jump-host credential = %+v", cfg.Proxy)
	}

	// Importing the same file again merges, it does not double.
	again, err := target.importFrom(path, exportPassphrase)
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again.Added != 0 || again.Updated != 1 || len(target.ListHosts()) != 1 {
		t.Fatalf("second import = %+v with %d hosts, want one update", again, len(target.ListHosts()))
	}
}

func TestImportRefusesAWrongPassphraseAndAForeignFile(t *testing.T) {
	source, _ := newTestApp(t)
	seedHosts(t, source)

	path := filepath.Join(testutil.TempDir(t), "bundle.json")
	if _, err := source.exportTo(path, exportPassphrase); err != nil {
		t.Fatalf("export: %v", err)
	}

	target, _ := newTestApp(t)
	if _, err := target.importFrom(path, "not it"); err == nil ||
		!strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("wrong passphrase = %v, want it named", err)
	}

	foreign := filepath.Join(filepath.Dir(path), "other.json")
	if err := os.WriteFile(foreign, []byte(`{"hosts":[{"host":"evil","user":"root"}]}`), 0o600); err != nil {
		t.Fatalf("write foreign file: %v", err)
	}
	if _, err := target.importFrom(foreign, "whatever"); err == nil ||
		!strings.Contains(err.Error(), "not a GoTerm host export") {
		t.Fatalf("foreign file = %v, want it refused as not a GoTerm export", err)
	}
	if n := len(target.ListHosts()); n != 0 {
		t.Fatalf("a refused import still changed the list: %d hosts", n)
	}
}

func TestExportNeedsAPassphrase(t *testing.T) {
	app, _ := newTestApp(t)
	seedHosts(t, app)

	path := filepath.Join(testutil.TempDir(t), "bundle.json")
	if _, err := app.exportTo(path, ""); err == nil {
		t.Fatal("an export without a passphrase must not write a file")
	}
	if _, err := os.ReadFile(path); err == nil {
		t.Fatal("the unsealed file was written anyway")
	}
}
