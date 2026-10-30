package secrets

import (
	"github.com/ykfq/goterm/internal/testutil"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSealedFileRoundTrip(t *testing.T) {
	dir := testutil.TempDir(t)
	store, err := OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if store.Secure() {
		t.Fatal("the file backend must not claim to be an OS key ring")
	}

	if _, err := store.Get("host/1/secret"); err != ErrNotFound {
		t.Fatalf("get of a missing secret = %v, want ErrNotFound", err)
	}
	if store.Has("host/1/secret") {
		t.Fatal("Has reported a secret that was never stored")
	}

	if err := store.Put("host/1/secret", "hunter2"); err != nil {
		t.Fatalf("put: %v", err)
	}
	if got, err := store.Get("host/1/secret"); err != nil || got != "hunter2" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if !store.Has("host/1/secret") {
		t.Fatal("Has said no after a successful Put")
	}

	// The key file must exist with tight permissions on POSIX; on Windows the
	// ACL is what matters and 0600 is a no-op, so only check it is readable.
	if _, err := os.Stat(filepath.Join(dir, keyFileName)); err != nil {
		t.Fatalf("master key missing: %v", err)
	}

	// Reopening must find the secret.
	reopened, err := OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if got, err := reopened.Get("host/1/secret"); err != nil || got != "hunter2" {
		t.Fatalf("after reopen = %q, %v", got, err)
	}

	if err := store.Put("host/1/secret", ""); err != nil {
		t.Fatalf("put empty (delete): %v", err)
	}
	if _, err := store.Get("host/1/secret"); err != ErrNotFound {
		t.Fatalf("after delete = %v, want ErrNotFound", err)
	}
}

func TestSealedFileIsNotPlaintext(t *testing.T) {
	dir := testutil.TempDir(t)
	store, err := OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	const canary = "super-secret-password-canary"
	if err := store.Put("host/7/secret", canary); err != nil {
		t.Fatalf("put: %v", err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, sealedFileName))
	if err != nil {
		t.Fatalf("read vault: %v", err)
	}
	if strings.Contains(string(raw), canary) {
		t.Fatal("the sealed vault contains the secret in plaintext")
	}
	keyRaw, err := os.ReadFile(filepath.Join(dir, keyFileName))
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if strings.Contains(string(keyRaw), canary) {
		t.Fatal("the key file contains the secret")
	}
}

func TestSealedFileQuarantinesAnUnopenableVault(t *testing.T) {
	dir := testutil.TempDir(t)
	vault := filepath.Join(dir, sealedFileName)
	// Base64 that will not authenticate: what a restored backup with the wrong
	// master.key looks like.
	if err := os.WriteFile(vault, []byte("bm90LWEtcmVhbC12YXVsdA=="), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	store, err := OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := store.Get("anything"); err == nil {
		t.Fatal("a vault that cannot be authenticated was treated as fine")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("readdir: %v", err)
	}
	var kept bool
	for _, e := range entries {
		if strings.Contains(e.Name(), "unreadable-") {
			kept = true
		}
	}
	if !kept {
		t.Fatal("the unreadable vault was not preserved under a new name")
	}
	// And the store still works afterwards rather than wedging the app.
	if err := store.Put("a", "b"); err != nil {
		t.Fatalf("put after quarantine: %v", err)
	}
}

func TestSecretsAreSizeLimited(t *testing.T) {
	store, err := OpenSealedFile(testutil.TempDir(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := store.Put("k", strings.Repeat("x", maxSecretLen)); err != nil {
		t.Fatalf("at the limit: %v", err)
	}
	err = store.Put("k", strings.Repeat("x", maxSecretLen+1))
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("over-limit put = %v, want a limit error", err)
	}
	if err := store.Put("", "value"); err == nil {
		t.Fatal("an empty key was accepted")
	}
}

func TestClearRemovesEverything(t *testing.T) {
	dir := testutil.TempDir(t)
	store, err := OpenSealedFile(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if err := store.Put(k, "v-"+k); err != nil {
			t.Fatalf("put %s: %v", k, err)
		}
	}
	if err := store.Clear(); err != nil {
		t.Fatalf("clear: %v", err)
	}
	for _, k := range []string{"a", "b", "c"} {
		if _, err := store.Get(k); err != ErrNotFound {
			t.Fatalf("%s survived Clear: %v", k, err)
		}
	}
}

// TestOSBackendWhenAvailable exercises whatever the platform offers. On Windows
// this is a real Credential Manager write/read/delete; elsewhere it is a no-op
// that documents the fallback.
func TestOSBackendWhenAvailable(t *testing.T) {
	dir := testutil.TempDir(t)
	store, err := Open(dir)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Logf("secret backend: %s (secure=%v)", store.Backend(), store.Secure())

	const key = "host/probe/secret"
	t.Cleanup(func() { _ = store.Delete(key) })

	if err := store.Put(key, "os-backend-value"); err != nil {
		t.Fatalf("put: %v", err)
	}
	got, err := store.Get(key)
	if err != nil || got != "os-backend-value" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := store.Delete(key); err != nil {
		t.Fatalf("delete: %v", err)
	}
	if _, err := store.Get(key); err != ErrNotFound {
		t.Fatalf("after delete = %v, want ErrNotFound", err)
	}
}
