// The click-through for a saved host behind a jump host whose key is encrypted:
// ConnectHost with no passphrase must answer with the exact words the UI prompts
// on, and the retry after SetHostProxySecret must connect. This is the seam the
// per-package tests cannot cover — hoststore → manager → sshx, the way App runs it.
package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/testutil"
)

const jumpKeyPassphrase = "second key, second passphrase"

// writeEncryptedKey writes a passphrase-protected PEM key and returns the path
// plus its public half, which a test server authorises.
func writeEncryptedKey(t *testing.T, passphrase string) (string, ssh.PublicKey) {
	t.Helper()
	dir := testutil.TempDir(t)

	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	block, err := x509.EncryptPEMBlock(rand.Reader, "RSA PRIVATE KEY",
		x509.MarshalPKCS1PrivateKey(priv), []byte(passphrase), x509.PEMCipherAES256)
	if err != nil {
		t.Fatalf("encrypt key: %v", err)
	}
	path := filepath.Join(dir, "key_"+fmt.Sprintf("%x", len(passphrase)))
	if err := os.WriteFile(path, pem.EncodeToMemory(block), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	pub, err := ssh.NewPublicKey(priv.Public())
	if err != nil {
		t.Fatalf("public key: %v", err)
	}
	return path, pub
}

// startJumpServer runs a bastion that accepts the given keys and forwards.
func startJumpServer(t *testing.T, authorised ...ssh.PublicKey) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.New(sshtest.Options{
		User:            "tester",
		AllowForwarding: true,
		AuthorizedKeys:  authorised,
	})
	if err != nil {
		t.Fatalf("start jump server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

// startKeyServer runs a target that accepts only the given keys.
func startKeyServer(t *testing.T, authorised ...ssh.PublicKey) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.New(sshtest.Options{User: "tester", AuthorizedKeys: authorised})
	if err != nil {
		t.Fatalf("start key server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func TestAppEncryptedJumpHostKeyAsksThenConnects(t *testing.T) {
	app, rec := newTestApp(t)
	target := startServer(t)
	keyPath, jumpPublic := writeEncryptedKey(t, jumpKeyPassphrase)
	jump := startJumpServer(t, jumpPublic)

	saved, err := app.SaveHost(hoststore.Host{
		Name: "via-jump", Host: "127.0.0.1", Port: target.Port(), User: "tester",
		AuthType:      hoststore.AuthPassword,
		ProxyHost:     "127.0.0.1",
		ProxyPort:     jump.Port(),
		ProxyUser:     "tester",
		ProxyAuthType: hoststore.AuthKey,
		ProxyKeyPath:  keyPath,
	})
	if err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	if err := app.SetHostSecret(saved.ID, "correct horse battery staple"); err != nil {
		t.Fatalf("SetHostSecret: %v", err)
	}

	// Round one, exactly as the sidebar does it: nothing typed yet. The failure
	// has to name itself as a passphrase request *and* as the jump host's, or the
	// UI shows no prompt and the user only sees a dead connection.
	_, err = app.ConnectHost(saved.ID, 80, 24, ConnectAnswers{})
	if err == nil {
		t.Fatal("an encrypted jump-host key connected with no passphrase")
	}
	msg := err.Error()
	if !strings.Contains(msg, "passphrase required") {
		t.Errorf("no prompt would be raised for %q", msg)
	}
	if !strings.Contains(msg, "passphrase required (proxy):") {
		t.Errorf("nothing in %q tells the UI this is the jump host's key", msg)
	}

	// "Remember this passphrase", then connect again without typing anything.
	if err := app.SetHostProxySecret(saved.ID, jumpKeyPassphrase); err != nil {
		t.Fatalf("SetHostProxySecret: %v", err)
	}
	id, err := app.ConnectHost(saved.ID, 80, 24, ConnectAnswers{})
	if err != nil {
		t.Fatalf("ConnectHost after the passphrase was stored: %v", err)
	}
	waitUntil(t, "the shell banner over the tunnel", func() bool {
		return strings.Contains(string(rec.bytesFor(id)), "ready (user=tester")
	})
}

// ConnectHost takes two passphrase arguments. Nothing but this test says which
// one belongs to which machine: swap them inside hoststore and every other test
// still passes, because each of them only ever fills one of the two slots. A user
// with an encrypted key on both ends — same login flow, two prompts — would get a
// prompt that appears to work and a login that never does.
func TestAppPassesEachPassphraseToItsOwnEndpoint(t *testing.T) {
	const (
		targetPass = "the target key has its own passphrase"
		jumpPass   = "the bastion key has a different one"
	)
	app, _ := newTestApp(t)
	targetKey, targetPublic := writeEncryptedKey(t, targetPass)
	jumpKey, jumpPublic := writeEncryptedKey(t, jumpPass)
	target := startKeyServer(t, targetPublic)
	jump := startJumpServer(t, jumpPublic)

	saved, err := app.SaveHost(hoststore.Host{
		Name: "both-ends-encrypted", Host: "127.0.0.1", Port: target.Port(), User: "tester",
		AuthType:      hoststore.AuthKey,
		KeyPath:       targetKey,
		ProxyHost:     "127.0.0.1",
		ProxyPort:     jump.Port(),
		ProxyUser:     "tester",
		ProxyAuthType: hoststore.AuthKey,
		ProxyKeyPath:  jumpKey,
	})
	if err != nil {
		t.Fatalf("SaveHost: %v", err)
	}

	// Nothing typed: the target is resolved first, so it is the target that asks,
	// with no jump-host marker on it.
	_, err = app.ConnectHost(saved.ID, 80, 24, ConnectAnswers{})
	if err == nil || !strings.Contains(err.Error(), "passphrase required") {
		t.Fatalf("the target key should have asked for its passphrase: %v", err)
	}
	if strings.Contains(err.Error(), "(proxy)") {
		t.Fatalf("the target's ask was blamed on the jump host: %q", err.Error())
	}

	// Both answers, in the wrong slots. This must fail: if it ever succeeds, the
	// two arguments have been swapped somewhere and the prompts are lying.
	if _, err := app.ConnectHost(saved.ID, 80, 24, ConnectAnswers{Passphrase: jumpPass, ProxyPassphrase: targetPass}); err == nil {
		t.Fatal("swapped passphrases authenticated both endpoints")
	} else if !strings.Contains(err.Error(), "passphrase required") {
		t.Fatalf("a swapped passphrase must read as a passphrase error, not %q", err.Error())
	}

	// Each answer in its own slot: through the tunnel and into the shell.
	id, err := app.ConnectHost(saved.ID, 80, 24, ConnectAnswers{Passphrase: targetPass, ProxyPassphrase: jumpPass})
	if err != nil {
		t.Fatalf("each passphrase in its own slot: %v", err)
	}
	defer app.CloseSession(id)
	if err := app.SendInput(id, "\n"); err != nil {
		t.Fatalf("the session is not usable: %v", err)
	}
}
