// Package secrets stores the small, sharp things: SSH passwords and private-key
// passphrases.
//
// On Windows the preferred home is Credential Manager (generic credentials,
// pure syscalls — no console windows flashing past the user, which is what
// shelling out to cmdkey does). Everywhere else, or when Credential Manager is
// unavailable, secrets land in an AES-256-GCM sealed file whose key lives in a
// separate 0600 file. That fallback is weaker than a real key ring — it is
// protected against a casual read of hosts.json, not against someone already
// running as you — so Store reports which backend is live and the UI says so.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ErrNotFound is returned by Get when no secret is stored under a key.
var ErrNotFound = errors.New("secret not found")

// Backend names the storage implementation actually in use.
type Backend string

const (
	// BackendCredentialManager is the Windows Credential Manager.
	BackendCredentialManager Backend = "windows-credential-manager"
	// BackendSealedFile is the AES-GCM file fallback.
	BackendSealedFile Backend = "sealed-file"
)

// Store keeps secrets under a namespaced key ("hostID:password").
type Store struct {
	ring    ring
	dir     string
	backend Backend
}

type ring interface {
	Get(key string) (string, error)
	Put(key, value string) error
	Delete(key string) error
	Clear() error
	backend() Backend
}

// Open picks the strongest available backend for dir.
//
// GOTERM_SECRETS=file forces the sealed file. That is both a test hook and a
// real option for someone who would rather keep secrets in one backup-able
// file than in Credential Manager; the UI always reports the backend in use.
func Open(dir string) (*Store, error) {
	if strings.EqualFold(os.Getenv("GOTERM_SECRETS"), "file") {
		return OpenSealedFile(dir)
	}
	if r, err := newOSRing(serviceName); err == nil {
		return &Store{ring: r, dir: dir, backend: r.backend()}, nil
	}
	return OpenSealedFile(dir)
}

// OpenSealedFile forces the file backend. Used on non-Windows platforms and by
// tests, so the fallback path is covered even on a machine with a working
// Credential Manager.
func OpenSealedFile(dir string) (*Store, error) {
	r, err := newFileRing(filepath.Join(dir, sealedFileName), filepath.Join(dir, keyFileName))
	if err != nil {
		return nil, err
	}
	return &Store{ring: r, dir: dir, backend: BackendSealedFile}, nil
}

// Backend reports where secrets really live; the UI shows this verbatim rather
// than claiming key-ring protection it may not have.
func (s *Store) Backend() Backend { return s.backend }

// Secure reports whether the backend is an OS key ring rather than a file.
func (s *Store) Secure() bool { return s.backend == BackendCredentialManager }

// Put stores a value, overwriting any previous one. An empty value deletes.
func (s *Store) Put(key, value string) error {
	if key == "" {
		return errors.New("secret key is required")
	}
	if value == "" {
		return s.Delete(key)
	}
	if len(value) > maxSecretLen {
		return fmt.Errorf("secret is %d bytes; the limit is %d", len(value), maxSecretLen)
	}
	return s.ring.Put(key, value)
}

// Get returns a secret, or ErrNotFound.
func (s *Store) Get(key string) (string, error) {
	value, err := s.ring.Get(key)
	if err != nil {
		return "", err
	}
	return value, nil
}

// Has reports whether a secret exists without exposing it.
func (s *Store) Has(key string) bool {
	_, err := s.ring.Get(key)
	return err == nil
}

// Delete removes a secret. Missing secrets are not an error.
func (s *Store) Delete(key string) error { return s.ring.Delete(key) }

// Clear removes every GoTerm secret.
func (s *Store) Clear() error { return s.ring.Clear() }

// maxSecretLen is Credential Manager's 512-byte blob limit, kept below the
// wire cap so both backends behave identically.
const maxSecretLen = 480

const (
	serviceName    = "goterm"
	sealedFileName = "secrets.enc"
	keyFileName    = "master.key"
)
