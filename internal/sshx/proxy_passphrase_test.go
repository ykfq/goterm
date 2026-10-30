// A jump host whose private key is encrypted has to produce the same prompt as
// an encrypted target key — and the error has to say "proxy" so the frontend
// asks for the *jump host's* passphrase instead of the target's.
package sshx

import (
	"errors"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/testutil"
)

func TestEncryptedJumpHostKeyAsksForItsPassphrase(t *testing.T) {
	target := newHarness(t, sshtest.Options{})
	keyPath, jumpPublic := writeEncryptedTestKey(t, testutil.TempDir(t))
	jump := newHarness(t, sshtest.Options{
		AllowForwarding: true,
		AuthorizedKeys:  []ssh.PublicKey{jumpPublic},
	})

	cfg := Config{
		Host: "127.0.0.1", Port: target.srv.Port(), User: "tester",
		AuthType: AuthPassword, Password: "correct horse battery staple",
		Cols: 80, Rows: 24,
		Proxy: &ProxyConfig{
			Host: "127.0.0.1", Port: jump.srv.Port(), User: "tester",
			AuthType: AuthKey, KeyPath: keyPath,
		},
	}

	// Without the passphrase the connect must stop at the key, not at the login.
	if _, err := target.mgr.Connect(cfg); err == nil {
		t.Fatal("the jump host accepted an encrypted key with no passphrase")
	} else if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("error is not a passphrase request: %v", err)
	} else {
		// The frontend reads this as a string and splits it two ways: the words
		// "passphrase required" open a prompt, and "proxy" decides which key the
		// prompt is about. Losing either one means no prompt at all.
		msg := err.Error()
		if !strings.Contains(msg, "passphrase required") {
			t.Errorf("error lost the words the frontend matches on: %q", msg)
		}
		// The marker, not the path, is what says which key this is about: the old
		// "does the text contain 'proxy'" test misread a target key named proxy.pem.
		if !strings.Contains(msg, "passphrase required (proxy):") {
			t.Errorf("error does not mark the jump host as the key to unlock: %q", msg)
		}
	}

	// With it, the same connection goes through.
	cfg.Proxy.Passphrase = testPassphrase
	sess, err := target.mgr.Connect(cfg)
	if err != nil {
		t.Fatalf("connect with the passphrase: %v", err)
	}
	defer sess.Close()
}

// A wrong jump-host passphrase must keep asking, not fall through to a plain
// authentication failure.
func TestWrongJumpHostPassphraseStillAsks(t *testing.T) {
	target := newHarness(t, sshtest.Options{})
	keyPath, jumpPublic := writeEncryptedTestKey(t, testutil.TempDir(t))
	jump := newHarness(t, sshtest.Options{
		AllowForwarding: true,
		AuthorizedKeys:  []ssh.PublicKey{jumpPublic},
	})

	cfg := Config{
		Host: "127.0.0.1", Port: target.srv.Port(), User: "tester",
		AuthType: AuthPassword, Password: "correct horse battery staple",
		Cols: 80, Rows: 24,
		Proxy: &ProxyConfig{
			Host: "127.0.0.1", Port: jump.srv.Port(), User: "tester",
			AuthType: AuthKey, KeyPath: keyPath, Passphrase: "not the passphrase",
		},
	}

	_, err := target.mgr.Connect(cfg)
	if err == nil {
		t.Fatal("a wrong passphrase decrypted the jump host key")
	}
	if !errors.Is(err, ErrPassphraseRequired) {
		t.Fatalf("a wrong passphrase must read as a retry-worthy passphrase error: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "passphrase required (proxy):") {
		t.Errorf("a wrong jump-host passphrase must still be marked as the jump host's: %q", msg)
	}
}
