// Package sshx owns the SSH side of GoTerm: dialing, PTY sessions, host-key
// trust, and the byte pipe between a remote shell and the frontend.
//
// Nothing in this package imports Wails. The frontend is reached through the
// EmitFunc the Manager is handed, which keeps the whole SSH path testable
// against a real in-process SSH server (see internal/sshtest).
package sshx

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Event topics. The frontend subscribes once at startup and routes by
// SessionID, which removes any race between "connect returned" and
// "frontend started listening".
const (
	EventData   = "ssh:data"
	EventClosed = "ssh:closed"
	EventNotice = "ssh:notice"
)

// DataEvent carries PTY output. Data is base64 of the raw bytes: the transport
// into the webview is JSON, so raw 0x00-0xff bytes cannot travel any other way.
type DataEvent struct {
	SessionID string `json:"sessionId"`
	Data      string `json:"data"`
}

// ClosedEvent announces that a session is over, and why.
type ClosedEvent struct {
	SessionID string      `json:"sessionId"`
	Reason    string      `json:"reason"`
	Session   SessionInfo `json:"session"`
}

// NoticeEvent is for TOFU fingerprints, SSH banners and connection errors.
type NoticeEvent struct {
	SessionID string `json:"sessionId,omitempty"`
	Level     string `json:"level"` // info | warn | error
	Message   string `json:"message"`
	// Action names something the user can do about the notice, when there is
	// something. The frontend decides how to render it; unknown actions are
	// ignored, so adding one never breaks an older UI.
	Action string `json:"action,omitempty"`
}

// EmitFunc publishes an event to the frontend.
type EmitFunc func(topic string, payload any)

// Manager tracks live sessions and is the only place that dials.
type Manager struct {
	mu       sync.RWMutex
	sessions map[string]*Session

	hostKeys    *HostKeyStore
	dialTimeout time.Duration

	// maxLifetime is the hard cap on one connection. Zero would mean "no cap",
	// which the app never sets — see SetMaxLifetime.
	maxLifetime atomic.Int64

	emitter atomic.Pointer[EmitFunc]
}

// Session lifetime bounds. The UI may choose within them; Go clamps to them
// whatever it is told, so a stale or hand-edited setting cannot exceed the cap
// and a keepalive cannot hold a login open for ever.
const (
	DefaultSessionLifetimeHours = 12
	MinSessionLifetimeHours     = 1
	MaxSessionLifetimeHours     = 24
)

// SessionLifetime turns the hours a user picked into the duration Go enforces,
// clamping to the bounds above. 0 and negatives mean "use the default" rather
// than "no limit": there is deliberately no way to ask for an endless session.
func SessionLifetime(hours int) time.Duration {
	if hours <= 0 {
		hours = DefaultSessionLifetimeHours
	}
	if hours < MinSessionLifetimeHours {
		hours = MinSessionLifetimeHours
	}
	if hours > MaxSessionLifetimeHours {
		hours = MaxSessionLifetimeHours
	}
	return time.Duration(hours) * time.Hour
}

// NewManager returns a Manager whose events are dropped until SetEmitter.
func NewManager(hostKeys *HostKeyStore) *Manager {
	m := &Manager{
		sessions:    map[string]*Session{},
		hostKeys:    hostKeys,
		dialTimeout: 20 * time.Second,
	}
	m.maxLifetime.Store(int64(SessionLifetime(DefaultSessionLifetimeHours)))
	noop := EmitFunc(func(string, any) {})
	m.emitter.Store(&noop)
	return m
}

// SetMaxLifetime installs a new hard cap and applies it to sessions that are
// already open, not just to the next connection.
//
// The deadline is measured from when each connection was made, so lowering the
// setting shortens a long-running session immediately; raising it gives the
// remaining connections more room but still counts from their own start. Sessions
// that are already past the new deadline end on the spot.
func (m *Manager) SetMaxLifetime(d time.Duration) {
	m.maxLifetime.Store(int64(d))

	seen := make(map[*connRef]struct{})
	m.mu.RLock()
	for _, s := range m.sessions {
		if s.link != nil {
			seen[s.link] = struct{}{}
		}
	}
	m.mu.RUnlock()
	for link := range seen {
		link.armLifetime(d)
	}
}

// MaxLifetime is the cap currently in force.
func (m *Manager) MaxLifetime() time.Duration {
	return time.Duration(m.maxLifetime.Load())
}

// SetEmitter installs (or replaces) the event sink. A nil argument is ignored.
func (m *Manager) SetEmitter(emit EmitFunc) {
	if emit == nil {
		return
	}
	m.emitter.Store(&emit)
}

func (m *Manager) emit(topic string, payload any) {
	if fn := m.emitter.Load(); fn != nil {
		(*fn)(topic, payload)
	}
}

func (m *Manager) notice(sessionID, level, msg string) {
	m.noticeAction(sessionID, level, msg, "")
}

// noticeAction is notice() with an affordance the UI can act on.
func (m *Manager) noticeAction(sessionID, level, msg, action string) {
	m.emit(EventNotice, NoticeEvent{SessionID: sessionID, Level: level, Message: msg, Action: action})
}

// HostKeys exposes the trust store for a "known hosts" UI.
func (m *Manager) HostKeys() *HostKeyStore { return m.hostKeys }

// Connect dials cfg, opens a PTY shell, and registers the session.
//
// On any failure after the dial it unwinds what it opened, so a rejected
// login never leaks a TCP connection.
func (m *Manager) Connect(cfg Config) (*Session, error) {
	if err := cfg.Normalize(); err != nil {
		return nil, err
	}

	auth, offered, agentCloser, err := authMethods(cfg)
	if err != nil {
		return nil, err
	}
	closeAgent := func() {
		if agentCloser != nil {
			_ = agentCloser.Close()
		}
	}

	var (
		tofuNote    string
		fingerprint string
	)
	clientCfg := m.clientConfig(cfg.User, cfg.AuthType+"@"+cfg.Address(), cfg.HostKeyPolicy, auth, &tofuNote, &fingerprint)

	started := time.Now()
	conn, bastion, err := m.dial(cfg, clientCfg, offered)
	if err != nil {
		closeAgent()
		return nil, err
	}

	sh, err := ptyShell(conn, cfg)
	if err != nil {
		_ = conn.Close()
		if bastion != nil {
			_ = bastion.Close()
		}
		closeAgent()
		return nil, err
	}
	closers := []io.Closer{agentCloser}
	if bastion != nil {
		// The tunnel lives inside the bastion: dropping it would kill the
		// session's socket under it.
		closers = append(closers, bastion)
	}

	link := newConnRef(conn, bastion, closers, cfg.KeepAliveInterval(), m.MaxLifetime())
	s := m.register(link, cfg, sh, fingerprint, started)

	if tofuNote != "" {
		m.notice(s.id, "warn", tofuNote)
	}
	// A successful connection announces itself where the user is already looking:
	// the tab goes green and the shell's own banner arrives. It used to also raise a
	// toast with the server version, the handshake time and the keepalive interval,
	// which is a log line dressed as an alert — on every connect, every duplicate
	// and every reconnect. The tab's tooltip and the status bar still carry the
	// details, and nothing is lost by not shouting them.
	return s, nil
}

// Duplicate opens a second terminal on a connection that is already up — the
// Xshell "duplicate session". There is no handshake, no password and no
// one-time code, because there is no new login: this is another channel on the
// same client, and the shell it starts is as real as the first one.
//
// The connection is shared, so closing either tab leaves the other working; it
// is torn down when the last session using it is gone.
func (m *Manager) Duplicate(srcID string) (*Session, error) {
	src, ok := m.Get(srcID)
	if !ok {
		return nil, fmt.Errorf("no session %s — duplicate needs a live connection", srcID)
	}
	if src.closed() {
		return nil, fmt.Errorf("session %s is already closed: %w", srcID, ErrClosed)
	}
	link := src.link
	if link == nil || link.client == nil {
		return nil, fmt.Errorf("session %s has no connection to share", srcID)
	}

	cfg := src.cfg
	// Hold the connection while the channel is being opened: the source tab may
	// be closed at any moment, and that must not pull the client out from under
	// this dial-free "connection".
	link.acquire()
	sh, err := ptyShell(link.client, cfg)
	if err != nil {
		link.release()
		return nil, err
	}

	s := m.register(link, cfg, sh, src.Info().Fingerprint, time.Now())
	return s, nil
}

// ptyShell opens a channel on client, asks for a PTY and starts a login shell.
//
// Connect and Duplicate need exactly this; the only difference between them is
// whether the client was dialled a moment ago or is already carrying a session.
func ptyShell(client *ssh.Client, cfg Config) (*shellPipes, error) {
	sess, err := client.NewSession()
	if err != nil {
		return nil, fmt.Errorf("open session on %s: %w", cfg.Address(), err)
	}
	sh := &shellPipes{sess: sess}
	fail := func(what string, err error) (*shellPipes, error) {
		_ = sess.Close()
		return nil, fmt.Errorf("%s on %s: %w", what, cfg.Address(), err)
	}

	modes := ssh.TerminalModes{
		ssh.ECHO:          1,
		ssh.TTY_OP_ISPEED: 14400,
		ssh.TTY_OP_OSPEED: 14400,
	}
	if err := sess.RequestPty(cfg.Term, cfg.Rows, cfg.Cols, modes); err != nil {
		return fail("request pty", err)
	}
	// Best effort: servers with UsePAM/SetEnv restrictions reject these, and a
	// rejected env var is not a reason to fail the connection.
	_ = sess.Setenv("TERM", cfg.Term)
	_ = sess.Setenv("COLORTERM", "truecolor")
	_ = sess.Setenv("LANG", envOr("LANG", "C.UTF-8"))

	if sh.stdin, err = sess.StdinPipe(); err != nil {
		return fail("open stdin pipe", err)
	}
	if sh.stdout, err = sess.StdoutPipe(); err != nil {
		return fail("open stdout pipe", err)
	}
	if sh.stderr, err = sess.StderrPipe(); err != nil {
		return fail("open stderr pipe", err)
	}
	if err := sess.Shell(); err != nil {
		return fail("start shell", err)
	}
	return sh, nil
}

// shellPipes is a channel that has a PTY and a running shell on it.
type shellPipes struct {
	sess   *ssh.Session
	stdin  io.WriteCloser
	stdout io.Reader
	stderr io.Reader
}

// register wraps a live shell in a Session, remembers it and starts its pumps.
func (m *Manager) register(link *connRef, cfg Config, sh *shellPipes, fingerprint string, started time.Time) *Session {
	id := newSessionID()
	s := &Session{
		id:      id,
		cfg:     cfg,
		mgr:     m,
		sess:    sh.sess,
		stdin:   sh.stdin,
		link:    link,
		chunks:  make(chan []byte, chunkQueueDepth),
		seqDone: make(chan struct{}),
		info: SessionInfo{
			ID:               id,
			Target:           cfg.Target(),
			Host:             cfg.Host,
			Port:             cfg.Port,
			User:             cfg.User,
			ServerVersion:    string(link.client.ServerVersion()),
			Fingerprint:      fingerprint,
			Cols:             cfg.Cols,
			Rows:             cfg.Rows,
			CreatedAt:        started.UTC(),
			Status:           StatusConnected,
			Via:              via(cfg),
			KeepAliveSeconds: int(link.every / time.Second),
		},
	}
	link.track(s)
	m.remember(s)

	var wg sync.WaitGroup
	s.readers(&wg, sh.stdout)
	s.readers(&wg, sh.stderr)
	go func() {
		wg.Wait()
		close(s.chunks) // both readers are done; the pump flushes and exits
	}()
	go s.pump()
	go s.wait()
	link.startKeepAlive()
	return s
}

// via names the jump host for the session summary.
func via(cfg Config) string {
	if cfg.Proxy == nil {
		return ""
	}
	return cfg.Proxy.Target()
}

// clientConfig builds the client config for one endpoint — the target or the
// jump host — each with its own host-key policy and its own entry in the trust
// store. Trusting a jump host deliberately does not trust what is behind it.
func (m *Manager) clientConfig(user, what, policy string, auth []ssh.AuthMethod, tofu, fingerprint *string) *ssh.ClientConfig {
	return &ssh.ClientConfig{
		User:    user,
		Auth:    auth,
		Timeout: m.dialTimeout,
		HostKeyCallback: func(addr string, _ net.Addr, key ssh.PublicKey) error {
			note, err := m.hostKeys.Check(addr, key, policy)
			if err != nil {
				return err
			}
			if note != "" && what != "" {
				note = what + ": " + note
			}
			*tofu = note
			*fingerprint = fingerprintOf(key)
			return nil
		},
		BannerCallback: func(msg string) error {
			if trimmed := strings.TrimSpace(msg); trimmed != "" {
				m.notice("", "info", "server banner: "+trimmed)
			}
			return nil
		},
	}
}

// dial opens the target connection, directly or through the jump host.
//
// The jump-host leg is a plain SSH connection, and the target is reached over a
// direct-tcpip channel — so the bastion needs no shell access, no trzsz, and no
// configuration beyond the sshd default of allowing forwarded channels. The
// jump host never sees the session's bytes: the target's own SSH handshake runs
// end to end over the tunnel, and its host key is checked against the same
// trust store as a direct connection.
func (m *Manager) dial(cfg Config, clientCfg *ssh.ClientConfig, offered []string) (*ssh.Client, *ssh.Client, error) {
	if cfg.Proxy == nil {
		conn, err := net.DialTimeout("tcp", cfg.Address(), m.dialTimeout)
		if err != nil {
			return nil, nil, dialError(cfg, offered, m.dialTimeout, err)
		}
		client, err := m.handshake(conn, cfg.Address(), clientCfg)
		if err != nil {
			return nil, nil, dialError(cfg, offered, m.dialTimeout, err)
		}
		return client, nil, nil
	}

	proxy := *cfg.Proxy
	auth, proxyOffered, agentCloser, err := authMethodsFor(credsForProxy(proxy), "proxy ")
	if err != nil {
		return nil, nil, err
	}
	// The bastion needs the agent only for its own handshake; the target runs a
	// separate authMethods() for itself.
	defer func() {
		if agentCloser != nil {
			_ = agentCloser.Close()
		}
	}()

	var proxyNote, proxyFingerprint string
	bastion, err := ssh.Dial("tcp", proxy.Address(),
		m.clientConfig(proxy.User, "jump host", proxy.HostKeyPolicy, auth, &proxyNote, &proxyFingerprint))
	if err != nil {
		return nil, nil, fmt.Errorf("jump host %s: %w", proxy.Address(),
			dialErrorFor(proxy.Address(), proxy.Host, proxy.Redacted(), proxyOffered, m.dialTimeout, err))
	}
	if proxyNote != "" {
		m.notice("", "warn", proxyNote)
	}

	tunnel, err := openTunnel(bastion, cfg.Address(), m.dialTimeout)
	if err != nil {
		_ = bastion.Close()
		low := strings.ToLower(err.Error())
		if strings.Contains(low, "prohibited") || strings.Contains(low, "open failed") {
			return nil, nil, fmt.Errorf("jump host %s refused to forward the connection to %s — its sshd has AllowTcpForwarding off",
				proxy.Address(), cfg.Address())
		}
		if strings.Contains(low, "timed out") {
			return nil, nil, fmt.Errorf("jump host %s had not reached %s within %s", proxy.Address(), cfg.Address(), m.dialTimeout)
		}
		return nil, nil, fmt.Errorf("jump host %s cannot reach %s: %w", proxy.Address(), cfg.Address(), err)
	}
	client, err := m.handshake(tunnel, cfg.Address(), clientCfg)
	if err != nil {
		_ = bastion.Close()
		return nil, nil, dialError(cfg, offered, m.dialTimeout, err)
	}
	return client, bastion, nil
}

// handshake finishes the SSH handshake on an already-connected socket, but not
// for longer than the dial timeout.
//
// This exists because x/crypto/ssh applies ClientConfig.Timeout to the TCP
// connect only: reading the version banner and running the key exchange carry no
// deadline at all (nothing in that package calls SetDeadline, and the
// channel-backed conn it would call it on answers "ssh: tcpChan: deadline not
// supported"). A host that accepts the socket and then stays quiet — a dead
// port-forward, a bastion pointed at nothing, a load balancer with no backend —
// therefore hangs the connect, and the spinner with it, forever. Pulling the
// connection out from under the handshake is the one thing that does interrupt
// it, which is what the timer does.
func (m *Manager) handshake(conn net.Conn, address string, clientCfg *ssh.ClientConfig) (*ssh.Client, error) {
	var stalled atomic.Bool
	timer := time.AfterFunc(m.dialTimeout, func() {
		stalled.Store(true)
		_ = conn.Close()
	})
	client, chans, reqs, err := ssh.NewClientConn(conn, address, clientCfg)
	timer.Stop()
	if err != nil {
		_ = conn.Close()
		if stalled.Load() {
			// Wording matters: "i/o timeout" is what dialError turns into
			// "no answer from <host> within <timeout>", the same sentence a slow
			// connect already produces.
			return nil, fmt.Errorf("handshake i/o timeout after %s", m.dialTimeout)
		}
		return nil, err
	}
	return ssh.NewClient(client, chans, reqs), nil
}

// openTunnel asks the jump host for a direct-tcpip channel to the target, with a
// deadline. A channel open is just a request on the jump connection and carries
// no timeout of its own, so the only way to bound it is to give up and let the
// caller close the jump connection — which unblocks the open and is what we want
// anyway, because nothing else owns that connection yet. The goroutine's result
// goes into a buffered channel, so it cannot park forever after we walk away.
func openTunnel(bastion *ssh.Client, address string, timeout time.Duration) (net.Conn, error) {
	type result struct {
		conn net.Conn
		err  error
	}
	done := make(chan result, 1)
	go func() {
		conn, err := bastion.Dial("tcp", address)
		done <- result{conn: conn, err: err}
	}()
	select {
	case r := <-done:
		return r.conn, r.err
	case <-time.After(timeout):
		return nil, fmt.Errorf("timed out after %s waiting for the connection to %s to open", timeout, address)
	}
}

// Write forwards raw input bytes to a session.
func (m *Manager) Write(id string, p []byte) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrClosed
	}
	return s.Write(p)
}

// Resize forwards a window size change.
func (m *Manager) Resize(id string, cols, rows int) error {
	s, ok := m.Get(id)
	if !ok {
		return ErrClosed
	}
	return s.Resize(cols, rows)
}

// Close ends one session.
func (m *Manager) Close(id string) error {
	s, ok := m.Get(id)
	if !ok {
		return nil // already gone; closing twice is not an error
	}
	return s.Close()
}

// Get looks a live session up by id.
func (m *Manager) Get(id string) (*Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[id]
	return s, ok
}

// Sessions lists live sessions, oldest first.
func (m *Manager) Sessions() []SessionInfo {
	m.mu.RLock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.Info())
	}
	m.mu.RUnlock()
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// Count is the number of live sessions.
func (m *Manager) Count() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.sessions)
}

// CloseAll ends every session; called on app shutdown so no remote shell is
// left orphaned behind a closed window.
func (m *Manager) CloseAll() {
	m.mu.RLock()
	all := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		all = append(all, s)
	}
	m.mu.RUnlock()
	for _, s := range all {
		_ = s.Close()
	}
}

func (m *Manager) remember(s *Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[s.id] = s
}

func (m *Manager) forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, id)
}

// dialError turns a transport-level failure into something worth showing in a
// toast, keeping the original error wrapped for logs.
func dialError(cfg Config, offered []string, timeout time.Duration, err error) error {
	return dialErrorFor(cfg.Address(), cfg.Host, cfg.Redacted(), offered, timeout, err)
}

func dialErrorFor(address, host, label string, offered []string, timeout time.Duration, err error) error {
	low := strings.ToLower(err.Error())
	switch {
	case errors.Is(err, ErrHostKeyChanged) || strings.Contains(low, "host key changed"):
		return err
	case strings.Contains(low, "unable to authenticate"):
		return fmt.Errorf("authentication failed for %s (offered %s)", label, strings.Join(offered, ", "))
	case strings.Contains(low, "no supported algorithms"):
		return fmt.Errorf("key exchange with %s failed: the server only offers algorithms this build rejects: %w", address, err)
	case strings.Contains(low, "i/o timeout") || os.IsTimeout(err):
		return fmt.Errorf("no answer from %s within %s", address, timeout)
	case strings.Contains(low, "refused"):
		// "connection refused" on Unix, "connectex: ...actively refused it"
		// on Windows — match on the verb, not the phrasing.
		return fmt.Errorf("connection refused by %s — is sshd listening on that port?", address)
	case strings.Contains(low, "no such host"):
		return fmt.Errorf("cannot resolve %s", host)
	case strings.Contains(low, "administratively prohibited"), strings.Contains(low, "open failed"):
		return fmt.Errorf("%s refused to forward the connection (sshd has AllowTcpForwarding off)", host)
	case strings.Contains(low, "network is unreachable"), strings.Contains(low, "no route to host"):
		return fmt.Errorf("no route to %s", address)
	case strings.Contains(low, "handshake failed"):
		return fmt.Errorf("SSH handshake with %s failed: %w", address, err)
	default:
		return fmt.Errorf("connect %s: %w", address, err)
	}
}

func newSessionID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("s_%d", time.Now().UnixNano())
	}
	return "s_" + hex.EncodeToString(b[:])
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
