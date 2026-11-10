package main

import (
	"strings"
	"testing"

	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/hostport"
	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/sftpx"
	"github.com/ykfq/goterm/internal/sshx"
)

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:       "0 B",
		999:     "999 B",
		1024:    "1.0 KB",
		1536:    "1.5 KB",
		1 << 20: "1.0 MB",
		3 << 30: "3.0 GB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSFTPProgressText(t *testing.T) {
	got := sftpProgressText(sshx.SFTPProgressEvent{
		Direction: "upload", Name: "report.pdf", Done: 512, Total: 1024,
	})
	if !strings.Contains(got, "upload") || !strings.Contains(got, "report.pdf") || !strings.Contains(got, "50%") {
		t.Fatalf("progress text %q lacks direction, name or percentage", got)
	}
}

// TestSFTPPanelRendersAndSelectsARow checks the panel the way a user meets it:
// rows by text, a click choosing one, and the action buttons present.
func TestSFTPPanelRendersAndSelectsARow(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	panel := &SFTPPanel{
		Open: true,
		Path: "/srv/data",
		Entries: []sftpx.Entry{
			{Name: "logs", Path: "/srv/data/logs", Dir: true},
			{Name: "report.pdf", Path: "/srv/data/report.pdf", Size: 2048},
		},
	}
	tab := &Tab{ID: "files", Title: "files", Status: StatusConnected, SFTP: panel}
	app.tabs = []*Tab{tab}
	app.active = 0

	tt := ui.NewTester(app.view, 1200, 800)
	if !tt.HasText("report.pdf") || !tt.HasText("logs") {
		t.Fatalf("the listing did not render: %q", tt.Texts())
	}
	if !tt.HasText("Upload…") || !tt.HasText("Download") {
		t.Fatal("the panel's actions are missing")
	}
	if err := tt.Click("report.pdf"); err != nil {
		t.Fatal(err)
	}
	tt.Frame()
	if panel.Selected != "/srv/data/report.pdf" {
		t.Fatalf("clicking the row selected %q", panel.Selected)
	}
}

// TestExportThenImportRestoresHostsAndSecrets covers the whole round trip with
// the real key ring: export, delete locally, unseal, import, read the password
// back.
func TestExportThenImportRestoresHostsAndSecrets(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	h, err := app.hosts.Save(hoststore.Host{
		Name: "Round trip", Host: "127.0.0.1", Port: 2222, User: "tester", AuthType: "password",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.hosts.SetSecret(h.ID, "round-trip-secret"); err != nil {
		t.Fatal(err)
	}
	raw, count, err := app.exportHosts("export-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if count < 1 {
		t.Fatal("export wrote no hosts")
	}
	bundle, err := hostport.Unseal(raw, "export-passphrase")
	if err != nil {
		t.Fatal(err)
	}
	if bundle.Secrets[h.ID] != "round-trip-secret" {
		t.Fatalf("bundle lost the host or its secret: %+v", bundle)
	}

	if err := app.hosts.Delete(h.ID); err != nil {
		t.Fatal(err)
	}
	sum := app.hosts.Import(bundle.Hosts, bundle.Secrets, bundle.ProxySecrets)
	if sum.Added != 1 || sum.Secrets != 1 {
		t.Fatalf("import summary %+v, want 1 added and 1 secret", sum)
	}
	// An import after a delete mints a fresh id for the host, so read the
	// secret through the host that actually landed here.
	var imported hoststore.Host
	for _, candidate := range app.hosts.List() {
		if candidate.Name == "Round trip" {
			imported = candidate
		}
	}
	if imported.ID == "" {
		t.Fatal("the imported host is not in the list")
	}
	got, err := app.hosts.Secret(imported.ID)
	if err != nil || got != "round-trip-secret" {
		t.Fatalf("imported secret = %q, %v", got, err)
	}

	if _, err := hostport.Unseal(raw, "wrong"); err == nil {
		t.Fatal("Unseal accepted the wrong passphrase")
	}
}
