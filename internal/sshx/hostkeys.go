package sshx

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/atomicfile"
)

// ErrHostKeyChanged is returned when a server presents a host key that
// differs from the one on record. It is never safe to ignore.
var ErrHostKeyChanged = errors.New("host key changed")

// HostKeyRecord is one entry of the trust-on-first-use store.
type HostKeyRecord struct {
	Address     string    `json:"address"`
	Type        string    `json:"type"`
	Fingerprint string    `json:"fingerprint"`
	AddedAt     time.Time `json:"addedAt"`
	SeenAt      time.Time `json:"seenAt"`
}

// HostKeyStore is a JSON file of "host:port" -> host key, with the same
// guarantee OpenSSH's known_hosts gives you: first contact is trusted and
// remembered, every later mismatch is a hard error.
//
// A file (rather than OpenSSH's own format) keeps parsing honest: we only ever
// compare a key type plus a SHA-256 fingerprint, so there is no reason to
// reimplement known_hosts grammar.
type HostKeyStore struct {
	mu      sync.Mutex
	path    string
	records map[string]HostKeyRecord
}

// NewHostKeyStore loads (or creates) the store at path.
func NewHostKeyStore(path string) *HostKeyStore {
	s := &HostKeyStore{path: path, records: map[string]HostKeyRecord{}}
	s.load()
	return s
}

func (s *HostKeyStore) load() {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return // missing or unreadable: start empty, the next save recreates it
	}
	var onDisk map[string]HostKeyRecord
	if err := json.Unmarshal(b, &onDisk); err != nil {
		return
	}
	if onDisk != nil {
		s.records = onDisk
	}
}

func (s *HostKeyStore) save() error {
	b, err := json.MarshalIndent(s.records, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, b, 0o600)
}

// Check verifies key against the record for addr.
//
// It returns a human-readable note describing what happened, so the UI can
// tell the user "new host, fingerprint …" instead of silently trusting it.
func (s *HostKeyStore) Check(addr string, key ssh.PublicKey, policy string) (string, error) {
	rec := HostKeyRecord{
		Address:     addr,
		Type:        key.Type(),
		Fingerprint: ssh.FingerprintSHA256(key),
		SeenAt:      time.Now().UTC(),
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	known, ok := s.records[normalizeAddr(addr)]

	switch {
	case !ok:
		if policy == PolicyStrict {
			return "", fmt.Errorf("host %s is not in %s (policy=strict): run the connect again with trust-on-first-use, or add its fingerprint %s (%s) manually",
				addr, filepath.Base(s.path), rec.Fingerprint, rec.Type)
		}
		rec.AddedAt = rec.SeenAt
		s.records[normalizeAddr(addr)] = rec
		if err := s.save(); err != nil {
			return "", fmt.Errorf("record new host key: %w", err)
		}
		return fmt.Sprintf("new host %s trusted on first use (%s %s)", addr, rec.Type, rec.Fingerprint), nil

	case known.Fingerprint != rec.Fingerprint:
		return "", fmt.Errorf("%w: %s presented %s (%s) but %s (%s) is on record. The connection is refused; if the server's key really did change, remove the old entry from %s",
			ErrHostKeyChanged, addr, rec.Fingerprint, rec.Type, known.Fingerprint, known.Type, filepath.Base(s.path))

	default:
		known.SeenAt = rec.SeenAt
		s.records[normalizeAddr(addr)] = known
		_ = s.save()
		return "", nil
	}
}

// Forget drops one record (used by the "this host's key changed and I meant it"
// flow in the UI).
func (s *HostKeyStore) Forget(addr string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.records[normalizeAddr(addr)]; !ok {
		return false
	}
	delete(s.records, normalizeAddr(addr))
	_ = s.save()
	return true
}

// List returns every record sorted by address.
func (s *HostKeyStore) List() []HostKeyRecord {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]HostKeyRecord, 0, len(s.records))
	for _, r := range s.records {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Address < out[j].Address })
	return out
}

// FingerprintOf returns the stored fingerprint for addr, if any.
func (s *HostKeyStore) FingerprintOf(addr string) (HostKeyRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.records[normalizeAddr(addr)]
	return r, ok
}

// Path reports where the store lives, for error messages.
func (s *HostKeyStore) Path() string { return s.path }

func normalizeAddr(addr string) string {
	return strings.ToLower(strings.TrimSpace(addr))
}

// fingerprintOf is the wire format GoTerm shows and stores: SHA256:<base64>.
func fingerprintOf(key ssh.PublicKey) string {
	return ssh.FingerprintSHA256(key)
}
