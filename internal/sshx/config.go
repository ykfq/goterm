package sshx

import (
	"fmt"
	"strings"
	"time"
)

// Authentication kinds understood by Config.AuthType.
const (
	AuthPassword = "password"
	AuthKey      = "key"
	AuthAgent    = "agent"
	AuthAuto     = "" // pick from whatever the config actually carries
)

// Host-key verification policies.
const (
	// PolicyTOFU trusts a host the first time it is seen, records its
	// fingerprint, and refuses every later mismatch.
	PolicyTOFU = "tofu"
	// PolicyStrict refuses any host that is not already in the store.
	PolicyStrict = "strict"
)

const (
	DefaultPort = 22
	DefaultCols = 100
	DefaultRows = 30
	DefaultTerm = "xterm-256color"
)

// ProxyConfig is a jump host: the SSH server GoTerm dials first, and through
// which the real target is reached (`ssh -J` semantics, no remote tooling
// needed on the jump host beyond a plain sshd with channel forwarding on).
//
// It carries its own credentials and its own host-key policy because a jump
// host is a different machine with a different key, and trusting one must not
// silently trust the other.
type ProxyConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthType   string `json:"authType"`
	Password   string `json:"password"`
	KeyPath    string `json:"keyPath"`
	Passphrase string `json:"passphrase"`

	// HostKeyPolicy is PolicyTOFU or PolicyStrict; empty means PolicyTOFU.
	HostKeyPolicy string `json:"hostKeyPolicy"`
}

// Config is one SSH endpoint plus the initial terminal geometry.
//
// The JSON tags double as the contract with the frontend: they are the keys
// the TypeScript side sends, so renaming one is a breaking change on both
// sides. Never log a Config without redacting Password and Passphrase.
type Config struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	User       string `json:"user"`
	AuthType   string `json:"authType"`
	Password   string `json:"password"`
	KeyPath    string `json:"keyPath"`
	Passphrase string `json:"passphrase"`

	// Proxy is the jump host to tunnel through, or nil to connect directly.
	Proxy *ProxyConfig `json:"proxy,omitempty"`

	Cols int    `json:"cols"`
	Rows int    `json:"rows"`
	Term string `json:"term"`

	// KeepAliveSeconds overrides how often an idle session is poked with an
	// SSH keepalive request: 0 means DefaultKeepAlive, a negative value turns
	// keepalives off. It is a transport-level request and never touches the PTY.
	KeepAliveSeconds int `json:"keepAliveSeconds,omitempty"`

	// HostKeyPolicy is PolicyTOFU or PolicyStrict; empty means PolicyTOFU.
	HostKeyPolicy string `json:"hostKeyPolicy"`
}

// Normalize fills in defaults and rejects unusable input. It mutates c.
func (c *Config) Normalize() error {
	c.Host = strings.TrimSpace(c.Host)
	c.User = strings.TrimSpace(c.User)
	c.KeyPath = strings.TrimSpace(c.KeyPath)
	if strings.HasPrefix(c.KeyPath, "~") {
		c.KeyPath = expandHome(c.KeyPath)
	}

	if c.Host == "" {
		return fmt.Errorf("host is required")
	}
	if c.Port == 0 {
		c.Port = DefaultPort
	}
	if c.Port < 1 || c.Port > 65535 {
		return fmt.Errorf("port %d out of range", c.Port)
	}
	if c.User == "" {
		return fmt.Errorf("user is required")
	}
	if c.Cols <= 0 {
		c.Cols = DefaultCols
	}
	if c.Rows <= 0 {
		c.Rows = DefaultRows
	}
	if c.Term == "" {
		c.Term = DefaultTerm
	}
	if c.HostKeyPolicy == "" {
		c.HostKeyPolicy = PolicyTOFU
	}
	if c.HostKeyPolicy != PolicyTOFU && c.HostKeyPolicy != PolicyStrict {
		return fmt.Errorf("unknown hostKeyPolicy %q", c.HostKeyPolicy)
	}

	c.AuthType = strings.ToLower(strings.TrimSpace(c.AuthType))
	switch c.AuthType {
	case AuthAuto:
		switch {
		case c.Password != "":
			c.AuthType = AuthPassword
		case c.KeyPath != "":
			c.AuthType = AuthKey
		default:
			c.AuthType = AuthAgent
		}
	case AuthPassword, AuthKey, AuthAgent:
	default:
		return fmt.Errorf("unknown authType %q", c.AuthType)
	}
	switch c.AuthType {
	case AuthPassword:
		if c.Password == "" {
			return fmt.Errorf("password auth needs a password")
		}
	case AuthKey:
		if c.KeyPath == "" {
			return fmt.Errorf("key auth needs a private key path")
		}
	}

	if c.Proxy != nil && strings.TrimSpace(c.Proxy.Host) == "" {
		// An empty proxy block means "no proxy", not a half-filled one.
		c.Proxy = nil
	}
	if c.Proxy != nil {
		if err := c.Proxy.Normalize(); err != nil {
			return fmt.Errorf("proxy: %w", err)
		}
		if c.Proxy.Host == c.Host && c.Proxy.Port == c.Port {
			return fmt.Errorf("proxy cannot be the target itself")
		}
	}
	return nil
}

// Normalize fills in defaults and rejects unusable input for a jump host.
func (p *ProxyConfig) Normalize() error {
	p.Host = strings.TrimSpace(p.Host)
	p.User = strings.TrimSpace(p.User)
	p.KeyPath = strings.TrimSpace(p.KeyPath)
	if strings.HasPrefix(p.KeyPath, "~") {
		p.KeyPath = expandHome(p.KeyPath)
	}
	if p.Host == "" {
		return fmt.Errorf("host is required")
	}
	if p.Port == 0 {
		p.Port = DefaultPort
	}
	if p.Port < 1 || p.Port > 65535 {
		return fmt.Errorf("port %d out of range", p.Port)
	}
	if p.User == "" {
		return fmt.Errorf("user is required")
	}
	if p.HostKeyPolicy == "" {
		p.HostKeyPolicy = PolicyTOFU
	}
	if p.HostKeyPolicy != PolicyTOFU && p.HostKeyPolicy != PolicyStrict {
		return fmt.Errorf("unknown hostKeyPolicy %q", p.HostKeyPolicy)
	}

	p.AuthType = strings.ToLower(strings.TrimSpace(p.AuthType))
	if p.AuthType == AuthAuto {
		switch {
		case p.Password != "":
			p.AuthType = AuthPassword
		case p.KeyPath != "":
			p.AuthType = AuthKey
		default:
			p.AuthType = AuthAgent
		}
	}
	switch p.AuthType {
	case AuthPassword:
		if p.Password == "" {
			return fmt.Errorf("password auth needs a password")
		}
	case AuthKey:
		if p.KeyPath == "" {
			return fmt.Errorf("key auth needs a private key path")
		}
	case AuthAgent:
	default:
		return fmt.Errorf("unknown authType %q", p.AuthType)
	}
	return nil
}

// Address is the jump host's host:port.
func (p ProxyConfig) Address() string { return joinHostPort(p.Host, p.Port) }

// Target is the display name for a jump host.
func (p ProxyConfig) Target() string { return p.User + "@" + p.Address() }

// Redacted is a proxy debug string safe to log.
func (p ProxyConfig) Redacted() string {
	return fmt.Sprintf("%s auth=%s", p.Target(), p.AuthType)
}

// Address is the host:port the socket dials, formatted for net.Dial and for
// known-hosts keys.
func (c Config) Address() string { return joinHostPort(c.Host, c.Port) }

// joinHostPort brackets IPv6 literals so the port cannot be mistaken for part
// of the address.
func joinHostPort(host string, port int) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	return fmt.Sprintf("%s:%d", host, port)
}

// Target is the display name for an endpoint, e.g. root@10.0.0.8:22.
func (c Config) Target() string { return c.User + "@" + c.Address() }

// KeepAliveInterval resolves KeepAliveSeconds into a duration: the default when
// unset, disabled when negative, and floored so a silly value cannot spin.
func (c Config) KeepAliveInterval() time.Duration {
	switch {
	case c.KeepAliveSeconds < 0:
		return 0
	case c.KeepAliveSeconds == 0:
		return DefaultKeepAlive
	default:
		return max(time.Duration(c.KeepAliveSeconds)*time.Second, minKeepAlive)
	}
}

// Redacted is a debug string safe to put in a log line.
func (c Config) Redacted() string {
	via := ""
	if c.Proxy != nil {
		via = " via " + c.Proxy.Redacted()
	}
	return fmt.Sprintf("%s@%s auth=%s%s cols=%d rows=%d", c.User, c.Address(), c.AuthType, via, c.Cols, c.Rows)
}

func expandHome(p string) string {
	if len(p) > 1 && p[0] == '~' && (p[1] == '/' || p[1] == '\\') {
		if home, err := homeDir(); err == nil {
			return home + p[1:]
		}
	}
	return p
}
