// Package hoststore keeps the user's saved servers.
//
// The JSON file is the source of truth for everything except the secret, which
// lives in internal/secrets. That split is deliberate: hosts.json is safe to
// read, diff and back up, and a leaked copy says "bob-db.internal exists" but
// hands over no passwords.
package hoststore

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/ykfq/goterm/internal/atomicfile"
	"github.com/ykfq/goterm/internal/secrets"
	"github.com/ykfq/goterm/internal/sshx"
)

// AuthType mirrors sshx so the frontend sees one vocabulary.
const (
	AuthPassword = sshx.AuthPassword
	AuthKey      = sshx.AuthKey
	AuthAgent    = sshx.AuthAgent
)

// Host is one saved server. No field here may hold a secret.
type Host struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Group    string `json:"group"`
	Host     string `json:"host"`
	Port     int    `json:"port"`
	User     string `json:"user"`
	AuthType string `json:"authType"`
	KeyPath  string `json:"keyPath"`
	Colour   string `json:"colour"`
	Notes    string `json:"notes"`
	Portable bool   `json:"-"` // reserved: exportable without secrets

	// TwoFactor marks a server that wants a one-time code appended to the
	// password (JumpServer and friends). The code is never stored: it is asked
	// for at connect time and Go appends it to the saved password.
	TwoFactor bool `json:"twoFactor,omitempty"`

	// Jump host (SSH proxy). An empty ProxyHost means a direct connection, so
	// existing hosts.json files keep working unchanged.
	ProxyHost     string `json:"proxyHost,omitempty"`
	ProxyPort     int    `json:"proxyPort,omitempty"`
	ProxyUser     string `json:"proxyUser,omitempty"`
	ProxyAuthType string `json:"proxyAuthType,omitempty"`
	ProxyKeyPath  string `json:"proxyKeyPath,omitempty"`

	HasSecret      bool `json:"hasSecret"`
	HasProxySecret bool `json:"hasProxySecret"`

	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	LastUsed  time.Time `json:"lastUsed,omitempty"`
}

// Store is a loaded hosts.json plus the secret backend.
type Store struct {
	path    string
	secrets *secrets.Store

	mu    sync.RWMutex
	hosts []Host
}

// Open loads (or creates) the host list in dir.
func Open(dir string, secretStore *secrets.Store) (*Store, error) {
	if secretStore == nil {
		return nil, errors.New("hoststore: a secret store is required")
	}
	s := &Store{path: dir + string(os.PathSeparator) + "hosts.json", secrets: secretStore}
	if err := s.load(); err != nil {
		return nil, err
	}
	return s, nil
}

func (s *Store) load() error {
	raw, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			s.hosts = nil
			return nil
		}
		return fmt.Errorf("read %s: %w", s.path, err)
	}
	var hosts []Host
	if err := json.Unmarshal(raw, &hosts); err != nil {
		return fmt.Errorf("%s is not valid JSON: %w (fix or remove it to start fresh)", s.path, err)
	}
	s.hosts = hosts
	return nil
}

func (s *Store) saveLocked() error {
	raw, err := json.MarshalIndent(s.hosts, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(s.path, raw, 0o600)
}

// Secrets reports the secret backend, for the status bar.
func (s *Store) Secrets() secrets.Backend { return s.secrets.Backend() }

// List returns every host, grouped then alphabetical.
func (s *Store) List() []Host {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Host, len(s.hosts))
	for i, h := range s.hosts {
		h.HasSecret = s.secrets.Has(secretKey(h.ID))
		h.HasProxySecret = s.secrets.Has(proxySecretKey(h.ID))
		out[i] = h
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Group != out[j].Group {
			return out[i].Group < out[j].Group
		}
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out
}

// Groups lists the group labels in use, "" first.
func (s *Store) Groups() []string {
	list := s.List()
	seen := map[string]bool{}
	var out []string
	for _, h := range list {
		g := strings.TrimSpace(h.Group)
		if g == "" || seen[g] {
			continue
		}
		seen[g] = true
		out = append(out, g)
	}
	return out
}

// Get finds one host by id.
func (s *Store) Get(id string) (Host, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, h := range s.hosts {
		if h.ID == id {
			h.HasSecret = s.secrets.Has(secretKey(h.ID))
			h.HasProxySecret = s.secrets.Has(proxySecretKey(h.ID))
			return h, true
		}
	}
	return Host{}, false
}

// Save inserts or updates a host. Identity and timestamps are owned here, so a
// client cannot smuggle in a different ID for an existing record.
func (s *Store) Save(h Host) (Host, error) {
	if err := validate(h); err != nil {
		return Host{}, err
	}
	h.Name = strings.TrimSpace(h.Name)
	h.Host = strings.TrimSpace(h.Host)
	h.KeyPath = strings.TrimSpace(h.KeyPath)
	h.Group = strings.TrimSpace(h.Group)
	h.ProxyHost = strings.TrimSpace(h.ProxyHost)
	h.ProxyUser = strings.TrimSpace(h.ProxyUser)
	h.ProxyKeyPath = strings.TrimSpace(h.ProxyKeyPath)
	if h.ProxyHost == "" {
		// A cleared jump host is no jump host at all.
		h.ProxyPort, h.ProxyUser, h.ProxyAuthType, h.ProxyKeyPath = 0, "", "", ""
	} else if h.ProxyPort == 0 {
		h.ProxyPort = sshx.DefaultPort
	}
	if h.Name == "" {
		h.Name = h.Host
	}
	if h.Port == 0 {
		h.Port = sshx.DefaultPort
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	now := time.Now().UTC()
	replaced := false
	for i, existing := range s.hosts {
		if existing.ID != h.ID {
			continue
		}
		h.CreatedAt = existing.CreatedAt
		if h.AuthType != existing.AuthType && existing.AuthType != "" {
			// Changing how we authenticate invalidates the stored secret.
			_ = s.secrets.Delete(secretKey(h.ID))
		}
		if h.ProxyAuthType != existing.ProxyAuthType || h.ProxyHost != existing.ProxyHost {
			// Same rule for the jump host: new endpoint or new method, new secret.
			_ = s.secrets.Delete(proxySecretKey(h.ID))
		}
		h.UpdatedAt = now
		s.hosts[i] = h
		replaced = true
		break
	}
	if !replaced {
		if h.ID != "" {
			return Host{}, fmt.Errorf("no saved host with id %s", h.ID)
		}
		h.ID = newID()
		h.CreatedAt = now
		h.UpdatedAt = now
		s.hosts = append(s.hosts, h)
	}
	if err := s.saveLocked(); err != nil {
		return Host{}, err
	}
	h.HasSecret = s.secrets.Has(secretKey(h.ID))
	h.HasProxySecret = s.secrets.Has(proxySecretKey(h.ID))
	return h, nil
}

// ImportSummary reports what an imported bundle did to this machine's list.
type ImportSummary struct {
	Added   int `json:"added"`
	Updated int `json:"updated"`
	Skipped int `json:"skipped"`
	Secrets int `json:"secrets"`
}

// Import merges hosts, and the secrets that came with them, into the list.
//
// An entry is matched to a local host by id first, then by address and user, so
// importing the same bundle twice updates instead of doubling up; anything that
// matches nothing is added with a freshly minted id. A secret is only written
// when the bundle carries one, so importing a secret-free bundle cannot wipe a
// password that is already stored here.
func (s *Store) Import(hosts []Host, secrets, proxySecrets map[string]string) ImportSummary {
	var sum ImportSummary
	for _, incoming := range hosts {
		origin := incoming.ID
		h := incoming
		// Flags about *this* machine's key ring are not portable, and neither is
		// the reserved Portable bit.
		h.HasSecret, h.HasProxySecret, h.Portable = false, false, false

		target := s.match(h)
		h.ID = target // "" makes Save mint one
		saved, err := s.Save(h)
		if err != nil {
			sum.Skipped++
			continue
		}
		if target == "" {
			sum.Added++
		} else {
			sum.Updated++
		}

		if v := secrets[origin]; v != "" {
			if err := s.SetSecret(saved.ID, v); err == nil {
				sum.Secrets++
			}
		}
		if v := proxySecrets[origin]; v != "" {
			_ = s.SetProxySecret(saved.ID, v)
		}
	}
	return sum
}

// match finds the local host a bundle entry refers to, or "" for "this is new
// here".
func (s *Store) match(h Host) string {
	if h.ID != "" {
		if _, ok := s.Get(h.ID); ok {
			return h.ID
		}
	}
	port := h.Port
	if port == 0 {
		port = sshx.DefaultPort
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, local := range s.hosts {
		if strings.EqualFold(local.Host, strings.TrimSpace(h.Host)) &&
			local.Port == port &&
			local.User == strings.TrimSpace(h.User) {
			return local.ID
		}
	}
	return ""
}

// Delete removes a host and its secret.
func (s *Store) Delete(id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	idx := -1
	for i, h := range s.hosts {
		if h.ID == id {
			idx = i
			break
		}
	}
	if idx < 0 {
		return nil
	}
	s.hosts = append(s.hosts[:idx], s.hosts[idx+1:]...)
	if err := s.saveLocked(); err != nil {
		return err
	}
	if err := s.secrets.Delete(secretKey(id)); err != nil {
		return err
	}
	return s.secrets.Delete(proxySecretKey(id))
}

// SetSecret stores (or with "" removes) a host's password / key passphrase.
func (s *Store) SetSecret(id, secret string) error {
	if _, ok := s.Get(id); !ok {
		return fmt.Errorf("no saved host with id %s", id)
	}
	return s.secrets.Put(secretKey(id), secret)
}

// Secret returns a host's stored secret. It is deliberately not part of Host:
// the list that reaches the UI must never carry passwords.
func (s *Store) Secret(id string) (string, error) {
	value, err := s.secrets.Get(secretKey(id))
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil // key/agent auth legitimately has no secret
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// SetProxySecret stores (or with "" removes) the jump host's password or key
// passphrase. It is a separate item on purpose: a jump host is a different
// machine, with its own credential.
func (s *Store) SetProxySecret(id, secret string) error {
	if _, ok := s.Get(id); !ok {
		return fmt.Errorf("no saved host with id %s", id)
	}
	return s.secrets.Put(proxySecretKey(id), secret)
}

// ProxySecret returns the jump host's stored secret, or "" when there is none.
func (s *Store) ProxySecret(id string) (string, error) {
	value, err := s.secrets.Get(proxySecretKey(id))
	if errors.Is(err, secrets.ErrNotFound) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	return value, nil
}

// Touch records that a host was just connected to.
func (s *Store) Touch(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for i, h := range s.hosts {
		if h.ID == id {
			h.LastUsed = now
			s.hosts[i] = h
			_ = s.saveLocked()
			return
		}
	}
}

// SSHConfig assembles the connect parameters for a saved host, pulling the
// secret out of the backend. The secret never passes through the frontend.
func (s *Store) SSHConfig(id string, cols, rows int) (sshx.Config, error) {
	return s.SSHConfigWith(id, cols, rows, ConnectOverrides{})
}

// ErrPasswordRequired marks a host that authenticates with a password Go does
// not have.
//
// The wording is a contract, not prose: the frontend matches "password required"
// (and the "(proxy)" form) to open a password prompt instead of reporting a
// failed login — exactly what `sshx.ErrPassphraseRequired` does for encrypted
// keys. A host saved without a password used to dead-end here with "edit the host
// and enter it again", which turned a double-click into an auth error with no way
// forward. `store_test.go` pins both sentences so the two halves cannot drift.
var ErrPasswordRequired = errors.New("password required")

// ConnectOverrides is what the connect-time prompts collected. Empty means
// "nothing extra this attempt"; none of it is persisted by this call.
type ConnectOverrides struct {
	// Passphrase overrides a stored private-key passphrase for the target.
	Passphrase string
	// ProxyPassphrase does the same for the jump host's key.
	ProxyPassphrase string
	// Password answers the prompt for a host whose password was never stored (or
	// whose key ring entry went missing). Used for this attempt only.
	Password string
	// ProxyPassword does the same for the jump host's password.
	ProxyPassword string
	// TwoFactorCode is the one-time code for a two-factor host, appended to the
	// saved password because that is how these servers want it.
	TwoFactorCode string
}

// SSHConfigWith is SSHConfig with the prompt answers supplied at connect time,
// which take precedence over anything stored. Taking them as arguments is what
// lets an encrypted key or a two-factor login work without either answer ever
// having to be written to disk.
func (s *Store) SSHConfigWith(id string, cols, rows int, ov ConnectOverrides) (sshx.Config, error) {
	host, ok := s.Get(id)
	if !ok {
		return sshx.Config{}, fmt.Errorf("no saved host with id %s", id)
	}
	secret, err := s.Secret(id)
	if err != nil {
		return sshx.Config{}, fmt.Errorf("unlock %s: %w", host.Name, err)
	}
	if ov.Password != "" {
		// The prompt asked and the user answered, so this attempt uses what they
		// typed. Storing it is a separate, deliberate call (SetHostSecret).
		secret = ov.Password
	}
	if host.AuthType == AuthPassword && secret == "" {
		// Not sent to the server empty: that would spend a failed login on the
		// remote side (and its account-lockout counter) for a mistake we can see
		// before dialing. The marker tells the frontend to ask instead.
		return sshx.Config{}, fmt.Errorf("%w: %s has no saved password", ErrPasswordRequired, host.Name)
	}
	cfg := sshx.Config{
		Host:     host.Host,
		Port:     host.Port,
		User:     host.User,
		AuthType: host.AuthType,
		KeyPath:  host.KeyPath,
		Cols:     cols,
		Rows:     rows,
	}
	switch host.AuthType {
	case AuthPassword:
		cfg.Password = secret
		if host.TwoFactor {
			// The code belongs to a 30-second window, so it is never stored — and
			// an empty one is a refusal, not a silent password-only attempt.
			code := strings.TrimSpace(ov.TwoFactorCode)
			if code == "" {
				return sshx.Config{}, fmt.Errorf("%s needs a two-factor code: enter the one-time code that goes with the saved password", host.Name)
			}
			cfg.Password = secret + code
		}
	case AuthKey:
		cfg.Passphrase = secret
		if ov.Passphrase != "" {
			cfg.Passphrase = ov.Passphrase
		}
	}

	if host.ProxyHost != "" {
		proxy := sshx.ProxyConfig{
			Host:     host.ProxyHost,
			Port:     host.ProxyPort,
			User:     host.ProxyUser,
			AuthType: host.ProxyAuthType,
			KeyPath:  host.ProxyKeyPath,
		}
		switch host.ProxyAuthType {
		case AuthPassword:
			if proxy.Password, err = s.ProxySecret(id); err != nil {
				return sshx.Config{}, fmt.Errorf("unlock %s jump host: %w", host.Name, err)
			}
			if ov.ProxyPassword != "" {
				proxy.Password = ov.ProxyPassword
			}
			if proxy.Password == "" {
				return sshx.Config{}, fmt.Errorf("%w (proxy): %s jump host has no saved password", ErrPasswordRequired, host.Name)
			}
		case AuthKey:
			// The jump host's secret is its key's passphrase; an empty one is what
			// makes GoTerm ask for it instead of failing the login.
			if proxy.Passphrase, err = s.ProxySecret(id); err != nil {
				return sshx.Config{}, fmt.Errorf("unlock %s jump host: %w", host.Name, err)
			}
			if ov.ProxyPassphrase != "" {
				proxy.Passphrase = ov.ProxyPassphrase
			}
		}
		cfg.Proxy = &proxy
	}

	if err := cfg.Normalize(); err != nil {
		return sshx.Config{}, err
	}
	return cfg, nil
}

func validate(h Host) error {
	if strings.TrimSpace(h.Host) == "" {
		return errors.New("host address is required")
	}
	if strings.TrimSpace(h.User) == "" {
		return errors.New("username is required")
	}
	if h.Port < 0 || h.Port > 65535 {
		return fmt.Errorf("port %d out of range", h.Port)
	}
	switch h.AuthType {
	case AuthPassword, AuthKey, AuthAgent:
	case "":
		return errors.New("choose an authentication method")
	default:
		return fmt.Errorf("unknown authentication method %q", h.AuthType)
	}
	if h.AuthType == AuthKey && strings.TrimSpace(h.KeyPath) == "" {
		return errors.New("key authentication needs a private key file")
	}
	if h.TwoFactor && h.AuthType != AuthPassword {
		// A one-time code is appended to the password; with a key or the agent
		// there is no password to append it to, so the box would just lie.
		return errors.New("two-factor authentication needs password authentication")
	}
	if strings.TrimSpace(h.ProxyHost) != "" {
		if strings.TrimSpace(h.ProxyUser) == "" {
			return errors.New("jump host username is required")
		}
		if h.ProxyPort < 0 || h.ProxyPort > 65535 {
			return fmt.Errorf("jump host port %d out of range", h.ProxyPort)
		}
		switch h.ProxyAuthType {
		case AuthPassword, AuthKey, AuthAgent:
		case "":
			return errors.New("choose an authentication method for the jump host")
		default:
			return fmt.Errorf("unknown jump host authentication method %q", h.ProxyAuthType)
		}
		if h.ProxyAuthType == AuthKey && strings.TrimSpace(h.ProxyKeyPath) == "" {
			return errors.New("jump host key authentication needs a private key file")
		}
	}
	return nil
}

func secretKey(id string) string { return "host/" + id + "/secret" }

func proxySecretKey(id string) string { return "host/" + id + "/proxy-secret" }

func newID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("h%d", time.Now().UnixNano())
	}
	return "h_" + hex.EncodeToString(b[:])
}
