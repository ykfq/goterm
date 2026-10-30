package hostport

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/hoststore"
)

const canary = "correct-horse-battery-staple"

func sampleBundle() Bundle {
	return Bundle{
		Hosts: []hoststore.Host{
			{
				ID: "h_abc123", Name: "jumpserver", Host: "10.0.0.8", Port: 2222,
				User: "deploy", AuthType: "password", TwoFactor: true,
			},
			{
				ID: "h_def456", Name: "bastion-route", Host: "10.0.0.9", Port: 22,
				User: "root", AuthType: "key", KeyPath: `C:\keys\id_ed25519`,
				ProxyHost: "bastion.example.com", ProxyPort: 22, ProxyUser: "jump", ProxyAuthType: "password",
			},
		},
		Secrets:      map[string]string{"h_abc123": canary},
		ProxySecrets: map[string]string{"h_def456": "jump-secret"},
	}
}

func TestSealRoundTrip(t *testing.T) {
	raw, err := Seal(sampleBundle(), "export-pass")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	got, err := Unseal(raw, "export-pass")
	if err != nil {
		t.Fatalf("unseal: %v", err)
	}
	if got.Format != Format || got.Version != Version {
		t.Fatalf("header = %s/%d", got.Format, got.Version)
	}
	if len(got.Hosts) != 2 || got.Hosts[0].Name != "jumpserver" || !got.Hosts[0].TwoFactor {
		t.Fatalf("hosts = %+v", got.Hosts)
	}
	if got.Secrets["h_abc123"] != canary {
		t.Fatalf("secret did not survive: %q", got.Secrets["h_abc123"])
	}
	if got.ProxySecrets["h_def456"] != "jump-secret" {
		t.Fatalf("jump-host secret did not survive: %q", got.ProxySecrets["h_def456"])
	}
	if got.ExportedAt.IsZero() || time.Since(got.ExportedAt) > time.Hour {
		t.Fatalf("exportedAt = %v", got.ExportedAt)
	}
}

// The file is meant to be safe to mail around: nothing readable may be inside.
func TestSealedFileLeaksNothing(t *testing.T) {
	raw, err := Seal(sampleBundle(), "export-pass")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	for _, needle := range []string{canary, "jump-secret", "jumpserver", "10.0.0.8", "bastion.example.com"} {
		if bytes.Contains(raw, []byte(needle)) {
			t.Fatalf("the sealed file contains %q in clear", needle)
		}
	}
	// It is still JSON with a recognisable header, so an import can say what it
	// is looking at.
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("not JSON: %v", err)
	}
	if env["format"] != Format {
		t.Fatalf("format = %v", env["format"])
	}
}

func TestWrongPassphraseIsReportedAsSuch(t *testing.T) {
	raw, err := Seal(sampleBundle(), "export-pass")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	if _, err := Unseal(raw, "not-the-pass"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
}

func TestTamperedFileIsRefused(t *testing.T) {
	raw, err := Seal(sampleBundle(), "export-pass")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	var env envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatalf("envelope: %v", err)
	}
	// Flip one base64 character of the payload — GCM must notice.
	bytesOfPayload := []byte(env.Payload)
	if bytesOfPayload[10] == 'A' {
		bytesOfPayload[10] = 'B'
	} else {
		bytesOfPayload[10] = 'A'
	}
	env.Payload = string(bytesOfPayload)
	tampered, err := json.Marshal(env)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if _, err := Unseal(tampered, "export-pass"); !errors.Is(err, ErrWrongPassphrase) {
		t.Fatalf("err = %v, want ErrWrongPassphrase", err)
	}
}

func TestForeignJSONIsRefused(t *testing.T) {
	if _, err := Unseal([]byte(`{"hosts":[]}`), "x"); !errors.Is(err, ErrNotABundle) {
		t.Fatalf("err = %v, want ErrNotABundle", err)
	}
	if _, err := Unseal([]byte("not json at all"), "x"); !errors.Is(err, ErrNotABundle) {
		t.Fatalf("err = %v, want ErrNotABundle", err)
	}
}

func TestNewerBundleIsRefused(t *testing.T) {
	raw, err := Seal(sampleBundle(), "export-pass")
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	upgraded := strings.Replace(string(raw), `"version": 1`, `"version": 99`, 1)
	if _, err := Unseal([]byte(upgraded), "export-pass"); !errors.Is(err, ErrNewerBundle) {
		t.Fatalf("err = %v, want ErrNewerBundle", err)
	}
}

func TestEmptyPassphraseIsRefused(t *testing.T) {
	if _, err := Seal(sampleBundle(), ""); err == nil {
		t.Fatal("sealing without a passphrase must fail")
	}
}
