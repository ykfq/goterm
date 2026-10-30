// An encrypted private key must produce a prompt, not a dead end.
//
// The frontend cannot unwrap Go errors — Wails hands it a string — so these
// tests pin both halves of the contract: errors.Is(err, ErrPassphraseRequired)
// for Go callers, and the exact leading words for the JavaScript side.
package sshx

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/testutil"
)

const testPassphrase = "correct horse battery staple"

// writeEncryptedTestKey writes a passphrase-protected PEM key and returns its
// path plus the public half, so a test can authorise it on the server side.
//
// Legacy "RSA PRIVATE KEY" PEM is what x/crypto can decrypt with a passphrase
// (PKCS#8 has no passphrase path there); OpenSSH-format keys are covered by the
// same code path but need the OpenSSH KDF, which is not generated here.
func writeEncryptedTestKey(t *testing.T, dir string) (path string, public ssh.PublicKey) {
	t.Helper()

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY",
		x509.MarshalPKCS1PrivateKey(priv), []byte(testPassphrase), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	path = filepath.Join(dir, "id_rsa_encrypted")
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	public, err = ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return path, public
}

func TestEncryptedKeyAsksForItsPassphrase(t *testing.T) {
	dir := testutil.TempDir(t)
	path, public := writeEncryptedTestKey(t, dir)
	h := newHarness(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{public}})

	cfg := Config{
		Host: "127.0.0.1", Port: h.srv.Port(), User: "tester",
		AuthType: AuthKey, KeyPath: path, Cols: 80, Rows: 24,
	}

	// No passphrase: the error must be recognisable, not a wall of text.
	_, err := h.mgr.Connect(cfg)
	if err == nil {
		t.Fatal("an encrypted key connected without its passphrase")
	}
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("err = %v, want one wrapping ErrPassphraseRequired", err)
	}
	if !strings.HasPrefix(err.Error(), "passphrase required") {
		t.Fatalf("the frontend matches on the leading words; got %q", err.Error())
	}
	if !strings.Contains(err.Error(), filepath.Base(path)) {
		t.Fatalf("the message should name the key: %q", err.Error())
	}

	// A wrong passphrase is the same ask again — the key is not "broken".
	cfg.Passphrase = "definitely not it"
	_, err = h.mgr.Connect(cfg)
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("wrong passphrase: err = %v, want ErrPassphraseRequired", err)
	}
	if !strings.Contains(err.Error(), "wrong passphrase") {
		t.Fatalf("wrong passphrase should be called out: %q", err.Error())
	}

	// The right one connects, and nothing about it is stored anywhere.
	cfg.Passphrase = testPassphrase
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	if got := h.srv.AuthSuccess(); got == 0 {
		t.Fatal("the server never accepted the key")
	}
}

func TestProxyPassphraseErrorSaysItWasTheProxy(t *testing.T) {
	dir := testutil.TempDir(t)
	path, _ := writeEncryptedTestKey(t, dir)

	_, _, _, err := authMethodsFor(credential{AuthType: AuthKey, KeyPath: path}, "proxy ")
	if err == nil {
		t.Fatal("an encrypted proxy key connected without its passphrase")
	}
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("err = %v, want ErrPassphraseRequired", err)
	}
	// Without the marker, a jump-host prompt would look like the target asking.
	if !strings.Contains(err.Error(), "passphrase required (proxy):") {
		t.Fatalf("the error must mark which endpoint asked: %q", err.Error())
	}
}

// A target key may perfectly well live at ~/.ssh/proxy.pem. The word "proxy" in
// a path used to be the frontend's only hint about which key to ask for, and it
// sent that passphrase to the jump host instead — an auth failure with no usable
// prompt. The marker is the only thing allowed to say so.
func TestTargetKeyNamedAfterAProxyIsStillTheTargetsOwnAsk(t *testing.T) {
	dir := testutil.TempDir(t)
	encrypted, _ := writeEncryptedTestKey(t, dir)
	data, err := os.ReadFile(encrypted)
	if err != nil {
		t.Fatalf("read the fixture key: %v", err)
	}
	path := filepath.Join(dir, "proxy.pem")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("stage the key under its misleading name: %v", err)
	}

	_, _, _, err = authMethodsFor(credential{AuthType: AuthKey, KeyPath: path}, "")
	if err == nil {
		t.Fatal("an encrypted key connected with no passphrase")
	}
	if !strings.Contains(err.Error(), "passphrase required") {
		t.Fatalf("the target must still ask for its passphrase: %q", err.Error())
	}
	if strings.Contains(err.Error(), "(proxy)") {
		t.Fatalf("the target's own key was blamed on the jump host: %q", err.Error())
	}
}

// A PuTTY key is unreadable rather than locked. Asking for a passphrase would
// loop the user forever, so the message says what to convert it with.
func TestPuttyKeySaysHowToConvertIt(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "id.ppk")
	if err := os.WriteFile(path, []byte("PuTTY-User-Key-File-2: ssh-rsa\nEncryption: aes256-cbc\n"), 0o600); err != nil {
		t.Fatalf("write the .ppk fixture: %v", err)
	}

	_, _, _, err := authMethodsFor(credential{AuthType: AuthKey, KeyPath: path}, "proxy ")
	if err == nil {
		t.Fatal("a .ppk was accepted as a private key")
	}
	if errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("no passphrase can ever unlock a .ppk: %v", err)
	}
	msg := err.Error()
	if !strings.Contains(msg, ".ppk") || !strings.Contains(msg, "PuTTYgen") {
		t.Fatalf("the message must name the format and the fix: %q", msg)
	}
}

// The file picker shows id_rsa and id_rsa.pub side by side, and picking the
// public half must not surface as a locked key or a mystery parse error.
func TestPublicKeyFileIsNamedAsWhatItIs(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "id_rsa.pub")
	if err := os.WriteFile(path, []byte("ssh-rsa AAAAB3NzaC1yc2EAAAADAQABAAAB me@workstation\n"), 0o600); err != nil {
		t.Fatalf("write the .pub fixture: %v", err)
	}

	_, _, _, err := authMethodsFor(credential{AuthType: AuthKey, KeyPath: path}, "proxy ")
	if err == nil {
		t.Fatal("a public key was accepted as a private key")
	}
	if errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("no passphrase can turn a public key into a private one: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "public key file") || !strings.Contains(msg, "private key") {
		t.Fatalf("the message must say the wrong half was picked and what to pick instead: %q", msg)
	}
}

func TestMissingKeyFileIsNotAPassphraseProblem(t *testing.T) {
	_, _, _, err := authMethodsFor(credential{AuthType: AuthKey, KeyPath: filepath.Join(testutil.TempDir(t), "nope")}, "")
	if err == nil {
		t.Fatal("a missing key file was accepted")
	}
	if errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("a missing key must not open a passphrase prompt: %v", err)
	}
}
