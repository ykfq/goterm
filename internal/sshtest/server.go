// Package sshtest is a real SSH server small enough to reason about, used for
// two things:
//
//   - tests: internal/sshx is driven against this server over a loopback
//     socket, so "the terminal works" is a checked claim, not a hope;
//   - manual runs: `go run ./cmd/sshtest` lets you point GoTerm at
//     127.0.0.1:2222 and exercise the GUI without a remote host.
//
// The default shell is a deterministic PTY emulator ("repeater") that echoes
// input and answers a handful of commands. It is deliberately not a real
// shell: tests need byte-exact output, and a real shell changes its mind
// between Windows builds.
package sshtest

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/crypto/ssh"
)

// Winsize is a terminal geometry in characters.
type Winsize struct {
	Cols int
	Rows int
}

// Shell is what a shell function gets to talk to the client with.
type Shell struct {
	User string

	in      io.Reader
	out     io.Writer
	resizes chan Winsize
	done    chan struct{}
	closeDo sync.Once

	mu      sync.Mutex
	winsize Winsize
	term    string
	exited  bool
	code    int
}

// finishDone closes the done channel exactly once, whoever gets there first.
func (s *Shell) finishDone() { s.closeDo.Do(func() { close(s.done) }) }

// In is the typed byte stream (raw PTY bytes: CR, backspace, UTF-8, ...).
func (s *Shell) In() io.Reader { return s.in }

// Out writes to the client's terminal. Write CRLF, not bare LF — there is no
// line discipline here to translate for you.
func (s *Shell) Out() io.Writer { return s.out }

// Resizes delivers window-change notifications.
func (s *Shell) Resizes() <-chan Winsize { return s.resizes }

// Done is closed when the session ends, from either side.
func (s *Shell) Done() <-chan struct{} { return s.done }

// Winsize reports the current geometry.
func (s *Shell) Winsize() Winsize {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.winsize
}

// Term reports the requested TERM value.
func (s *Shell) Term() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.term
}

// Exit ends the session with an exit status.
func (sh *Shell) Exit(code int) {
	sh.mu.Lock()
	sh.exited = true
	sh.code = code
	sh.mu.Unlock()
}

func (sh *Shell) exitCode() int {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.exited {
		return sh.code
	}
	return 0
}

// clampedCode maps the internal "client hung up" sentinel onto a wire status.
func (sh *Shell) clampedCode() uint32 {
	if c := sh.exitCode(); c >= 0 {
		return uint32(c)
	}
	return 0
}

// ShellFunc runs one interactive session. Returning ends the session.
type ShellFunc func(sh *Shell)

// Options configures a Server.
type Options struct {
	User     string
	Password string

	// AuthorizedKeys, when non-empty, enables publickey auth for these keys.
	AuthorizedKeys []ssh.PublicKey

	// HostKey signs the handshake; an ephemeral ed25519 key is generated when nil.
	HostKey ssh.Signer

	// Banner is sent to the client before authentication (exercises the
	// client's BannerCallback).
	Banner string

	// Shell handles interactive sessions; the repeater is used when nil.
	Shell ShellFunc

	// AllowForwarding opens the server up as a jump host: it accepts
	// "direct-tcpip" channels and dials the destination itself. Off by
	// default — a test server should not forward anywhere unless asked.
	AllowForwarding bool

	// TwoFactor makes password auth demand "<Password><digits>", the shape a
	// TOTP bastion wants: the saved password with a one-time code appended. A
	// bare password is then refused, exactly like the real thing, which is what
	// lets the two-factor path be driven end to end locally.
	TwoFactor bool

	Logger *log.Logger
}

// Server is a listening SSH server.
type Server struct {
	listener net.Listener
	cfg      *ssh.ServerConfig
	hostKey  ssh.Signer
	shell    ShellFunc
	logger   *log.Logger

	mu        sync.Mutex
	conns     []ssh.Conn
	authOK    atomic.Int64
	authFail  atomic.Int64
	forwarded atomic.Int64

	// allowForwarding enables "direct-tcpip" channels, which is what makes
	// this server usable as a jump host.
	allowForwarding bool

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// New starts a server on a free loopback port.
func New(opts Options) (*Server, error) { return NewOn("127.0.0.1:0", opts) }

// NewOn starts a server bound to addr.
func NewOn(addr string, opts Options) (*Server, error) {
	if opts.User == "" {
		opts.User = "tester"
	}
	if opts.Password == "" && len(opts.AuthorizedKeys) == 0 {
		return nil, errors.New("sshtest: set Password or AuthorizedKeys (an open server would hide auth bugs)")
	}
	if opts.Shell == nil {
		opts.Shell = Repeater
	}
	logger := opts.Logger
	if logger == nil {
		logger = log.New(io.Discard, "", 0)
	}

	hostKey := opts.HostKey
	if hostKey == nil {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, fmt.Errorf("generate host key: %w", err)
		}
		hostKey, err = ssh.NewSignerFromKey(priv)
		if err != nil {
			return nil, fmt.Errorf("host signer: %w", err)
		}
	}

	cfg := &ssh.ServerConfig{
		ServerVersion: "SSH-2.0-goterm_sshtest",
		PasswordCallback: func(c ssh.ConnMetadata, pass []byte) (*ssh.Permissions, error) {
			if c.User() == opts.User && passwordAccepted(opts, string(pass)) {
				return &ssh.Permissions{}, nil
			}
			return nil, fmt.Errorf("password rejected for %q", c.User())
		},
		PublicKeyCallback: func(c ssh.ConnMetadata, key ssh.PublicKey) (*ssh.Permissions, error) {
			for _, k := range opts.AuthorizedKeys {
				if bytes.Equal(k.Marshal(), key.Marshal()) {
					if c.User() != opts.User {
						return nil, fmt.Errorf("user %q is not %q", c.User(), opts.User)
					}
					return &ssh.Permissions{}, nil
				}
			}
			return nil, errors.New("key is not authorized")
		},
		AuthLogCallback: func(c ssh.ConnMetadata, method string, err error) {
			if err != nil {
				logger.Printf("auth fail: user=%s method=%s: %v", c.User(), method, err)
				return
			}
			logger.Printf("auth ok: user=%s method=%s", c.User(), method)
		},
	}
	if opts.Banner != "" {
		cfg.BannerCallback = func(ssh.ConnMetadata) string { return opts.Banner }
	}
	cfg.AddHostKey(hostKey)

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen: %w", err)
	}

	s := &Server{listener: ln, cfg: cfg, hostKey: hostKey, shell: opts.Shell, logger: logger,
		allowForwarding: opts.AllowForwarding}
	s.wg.Add(1)
	go s.acceptLoop()
	return s, nil
}

// Addr is the bound "host:port".
func (s *Server) Addr() string { return s.listener.Addr().String() }

// Port is the bound TCP port.
func (s *Server) Port() int {
	if a, ok := s.listener.Addr().(*net.TCPAddr); ok {
		return a.Port
	}
	return 0
}

// HostKeyFingerprint is what a correctly-behaving client should pin.
func (s *Server) HostKeyFingerprint() string {
	return ssh.FingerprintSHA256(s.hostKey.PublicKey())
}

// AuthFailures counts rejected authentications, so a test can prove a wrong
// password was really refused rather than quietly accepted.
func (s *Server) AuthFailures() int64 { return s.authFail.Load() }

// AuthSuccess counts accepted authentications.
func (s *Server) AuthSuccess() int64 { return s.authOK.Load() }

// Close stops listening and drops every live connection.
func (s *Server) Close() error {
	s.closeOnce.Do(func() {
		if err := s.listener.Close(); err != nil {
			s.logger.Printf("close listener: %v", err)
		}
		s.mu.Lock()
		for _, c := range s.conns {
			_ = c.Close()
		}
		s.conns = nil
		s.mu.Unlock()
	})
	s.wg.Wait()
	return nil
}

// CloseAllConns hangs up on clients but keeps listening — used to drive the
// client's "connection lost" path while the port is still live.
func (s *Server) CloseAllConns() {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, c := range s.conns {
		_ = c.Close()
	}
}

// Forwarded counts the direct-tcpip channels this server opened, so a test can
// prove a connection really went through the jump host.
func (s *Server) Forwarded() int64 { return s.forwarded.Load() }

// directTCPIP is the payload of a direct-tcpip channel open (RFC 4254 §7.2).
type directTCPIP struct {
	DestinationHost string
	DestinationPort uint32
	OriginatorHost  string
	OriginatorPort  uint32
}

// handleDirectTCPIP splices a channel to a TCP endpoint, which is the whole job
// of a jump host. It is rejected unless Options.AllowForwarding is set.
func (s *Server) handleDirectTCPIP(newChannel ssh.NewChannel) {
	var req directTCPIP
	if err := ssh.Unmarshal(newChannel.ExtraData(), &req); err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, "invalid direct-tcpip payload")
		return
	}
	if !s.allowForwarding {
		s.logger.Printf("forwarding refused to %s:%d", req.DestinationHost, req.DestinationPort)
		_ = newChannel.Reject(ssh.Prohibited, "port forwarding is disabled")
		return
	}

	addr := net.JoinHostPort(req.DestinationHost, fmt.Sprintf("%d", req.DestinationPort))
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		_ = newChannel.Reject(ssh.ConnectionFailed, err.Error())
		return
	}
	ch, requests, err := newChannel.Accept()
	if err != nil {
		_ = conn.Close()
		return
	}
	go ssh.DiscardRequests(requests)
	s.forwarded.Add(1)
	s.logger.Printf("forwarded to %s", addr)

	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		defer ch.Close()
		defer conn.Close()
		done := make(chan struct{}, 2)
		go func() { _, _ = io.Copy(conn, ch); done <- struct{}{} }()
		go func() { _, _ = io.Copy(ch, conn); done <- struct{}{} }()
		<-done
	}()
}

func (s *Server) acceptLoop() {
	defer s.wg.Done()
	for {
		conn, err := s.listener.Accept()
		if err != nil {
			return // listener closed
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.handleConn(conn)
		}()
	}
}

func (s *Server) handleConn(transport net.Conn) {
	sshConn, chans, _, err := ssh.NewServerConn(transport, s.cfg)
	if err != nil {
		s.authFail.Add(1)
		s.logger.Printf("handshake failed: %v", err)
		_ = transport.Close()
		return
	}
	s.authOK.Add(1)
	s.track(sshConn)
	defer func() {
		_ = sshConn.Close()
		s.untrack(sshConn)
	}()
	s.logger.Printf("session opened: user=%s client=%q", sshConn.User(), string(sshConn.ClientVersion()))

	for newChannel := range chans {
		switch newChannel.ChannelType() {
		case "session":
			ch, requests, err := newChannel.Accept()
			if err != nil {
				s.logger.Printf("accept channel: %v", err)
				return
			}
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				s.handleSession(sshConn, ch, requests)
			}()
		case "direct-tcpip":
			s.handleDirectTCPIP(newChannel)
		default:
			_ = newChannel.Reject(ssh.UnknownChannelType, "only session and direct-tcpip channels are supported")
		}
	}
}

func (s *Server) track(c ssh.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.conns = append(s.conns, c)
}

func (s *Server) untrack(c ssh.Conn) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, existing := range s.conns {
		if existing == c {
			s.conns = append(s.conns[:i], s.conns[i+1:]...)
			return
		}
	}
}

type ptyRequest struct {
	Term     string
	Cols     uint32
	Rows     uint32
	Width    uint32
	Height   uint32
	Modelist string
}

type winsizeRequest struct {
	Cols   uint32
	Rows   uint32
	Width  uint32
	Height uint32
}

type exitStatusRequest struct{ Status uint32 }

// handleSession answers the session-channel protocol. The shell runs on its
// own goroutine so window-change requests keep being served while the shell is
// busy — a real sshd does the same, and GoTerm's resize path depends on it.
func (s *Server) handleSession(conn *ssh.ServerConn, ch ssh.Channel, requests <-chan *ssh.Request) {
	sh := &Shell{
		User:    conn.User(),
		in:      ch,
		out:     ch,
		resizes: make(chan Winsize, 8),
		done:    make(chan struct{}),
		winsize: Winsize{Cols: 80, Rows: 24},
	}

	started := make(chan struct{})
	var startOnce sync.Once
	start := func(run func()) {
		startOnce.Do(func() {
			close(started)
			go run()
		})
	}

	for req := range requests {
		switch req.Type {
		case "pty-req":
			var p ptyRequest
			if err := ssh.Unmarshal(req.Payload, &p); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			sh.mu.Lock()
			sh.winsize = Winsize{Cols: int(p.Cols), Rows: int(p.Rows)}
			sh.term = p.Term
			sh.mu.Unlock()
			_ = req.Reply(true, nil)
		case "env", "subsystem":
			_ = req.Reply(true, nil)
		case "shell":
			_ = req.Reply(true, nil)
			start(func() { s.runShell(sh, ch) })
		case "exec":
			var payload struct{ Command string }
			if err := ssh.Unmarshal(req.Payload, &payload); err != nil {
				_ = req.Reply(false, nil)
				continue
			}
			_ = req.Reply(true, nil)
			command := payload.Command
			start(func() { s.runExec(sh, ch, command) })
		case "window-change":
			var w winsizeRequest
			if err := ssh.Unmarshal(req.Payload, &w); err == nil {
				next := Winsize{Cols: int(w.Cols), Rows: int(w.Rows)}
				sh.mu.Lock()
				sh.winsize = next
				sh.mu.Unlock()
				select {
				case sh.resizes <- next:
				default:
				}
			}
			_ = req.Reply(true, nil)
		default:
			if req.WantReply {
				_ = req.Reply(false, nil)
			}
		}
	}

	// The client tore the channel down.
	sh.Exit(-1)
	sh.finishDone()
	select {
	case <-started: // a shell owns the channel and closes it itself
	default:
		_ = ch.Close()
	}
}

func (s *Server) runShell(sh *Shell, ch ssh.Channel) {
	defer func() {
		if r := recover(); r != nil {
			s.logger.Printf("shell panic: %v", r)
		}
		sh.finishDone()
	}()
	s.shell(sh)
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusRequest{Status: sh.clampedCode()}))
	_ = ch.Close()
}

func (s *Server) runExec(sh *Shell, ch ssh.Channel, command string) {
	defer sh.finishDone()
	Exec(sh, command)
	_, _ = ch.SendRequest("exit-status", false, ssh.Marshal(exitStatusRequest{Status: sh.clampedCode()}))
	_ = ch.Close()
}

// Repeater is the default shell: a deterministic PTY emulator.
//
// It handles prompt, echo, backspace, CR/LF and a handful of commands that let
// a test assert exact bytes:
//
//	echo <text>      print text
//	crlf             print "a\r\nb\r\n"
//	bytes            print raw bytes 0x00..0xff
//	b64 <base64>     print the decoded bytes (binary round-trip proof)
//	resize           print the last window size the server saw
//	term             print TERM
//	spam <lines>     print N numbered lines (throughput)
//	sleep <ms>       block
//	exit [code]      leave
func Repeater(sh *Shell) {
	out := sh.Out()
	writeString(out, "\x1b[0;32msshtest\x1b[0m ready (user="+sh.User+", term="+sh.Term()+")\r\n")

	line := make([]byte, 0, 128)
	buf := make([]byte, 1)
	prompt(out)

	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		for {
			n, err := sh.In().Read(buf)
			if n > 0 && !handleByte(sh, buf[:n], &line) {
				return
			}
			if err != nil {
				return
			}
		}
	}()

	// Resizing shows up in the stream the way SIGWINCH redraws a real prompt.
	for {
		select {
		case w := <-sh.Resizes():
			writeString(out, fmt.Sprintf("\r\n\x1b[2m[resized cols=%d rows=%d]\x1b[0m\r\n", w.Cols, w.Rows))
			prompt(out)
			line = line[:0]
		case <-readerDone:
			return
		case <-sh.Done():
			return
		}
	}
}

func handleByte(sh *Shell, b []byte, line *[]byte) bool {
	out := sh.Out()
	for _, c := range b {
		switch {
		case c == '\r' || c == '\n':
			writeString(out, "\r\n")
			if !runCommand(sh, string(*line)) {
				return false
			}
			*line = (*line)[:0]
			prompt(out)
		case c == 0x7f || c == 0x08: // backspace
			if len(*line) > 0 {
				*line = (*line)[:len(*line)-1]
				writeString(out, "\b \b")
			}
		case c == 0x03: // Ctrl-C
			writeString(out, "^C\r\n")
			*line = (*line)[:0]
			prompt(out)
		case c == 0x04: // Ctrl-D
			writeString(out, "\r\nlogout\r\n")
			sh.Exit(0)
			return false
		case c >= 0x20:
			*line = append(*line, c)
			_, _ = out.Write([]byte{c})
		}
	}
	return true
}

func runCommand(sh *Shell, line string) bool {
	out := sh.Out()
	line = strings.TrimSpace(line)
	if line == "" {
		return true
	}
	cmd, arg, _ := strings.Cut(line, " ")
	arg = strings.TrimSpace(arg)
	switch cmd {
	case "echo":
		writeString(out, arg+"\r\n")
	case "crlf":
		writeString(out, "a\r\nb\r\n")
	case "bytes":
		raw := make([]byte, 256)
		for i := range raw {
			raw[i] = byte(i)
		}
		writeString(out, "\x1b[0m")
		_, _ = out.Write(raw)
		writeString(out, "\r\n[bytes ok]\r\n")
	case "b64":
		raw, err := decodeB64(arg)
		if err != nil {
			writeString(out, "b64: "+err.Error()+"\r\n")
			return true
		}
		_, _ = out.Write(raw)
		writeString(out, fmt.Sprintf("\r\n[b64 ok %d bytes]\r\n", len(raw)))
	case "resize":
		w := sh.Winsize()
		writeString(out, fmt.Sprintf("resize cols=%d rows=%d\r\n", w.Cols, w.Rows))
	case "term":
		writeString(out, sh.Term()+"\r\n")
	case "spam":
		n := atoiDefault(arg, 1000)
		for i := 1; i <= n; i++ {
			writeString(out, fmt.Sprintf("line %06d ", i)+strings.Repeat("x", 60)+"\r\n")
		}
	case "sleep":
		time.Sleep(time.Duration(atoiDefault(arg, 100)) * time.Millisecond)
	case "exit", "logout":
		writeString(out, "logout\r\n")
		sh.Exit(atoiDefault(arg, 0))
		return false
	default:
		writeString(out, fmt.Sprintf("sshtest: %s: command not found\r\n", cmd))
	}
	return true
}

// Exec answers an exec request (ssh host 'command'), reusing the same command
// set so the system ssh.exe can drive this server too.
func Exec(sh *Shell, command string) {
	if strings.TrimSpace(command) == "" {
		return
	}
	runCommand(sh, command)
}

func prompt(out io.Writer) {
	writeString(out, "\x1b[1;36msshtest\x1b[0m:\x1b[1;34m~\x1b[0m$ ")
}

func writeString(w io.Writer, s string) { _, _ = io.WriteString(w, s) }

// passwordAccepted compares the secret the client sent with the configured
// password. With TwoFactor on, a trailing run of digits is read as the one-time
// code and stripped; a password without one is refused, the way a TOTP bastion
// refuses it.
func passwordAccepted(opts Options, submitted string) bool {
	if !opts.TwoFactor {
		return submitted == opts.Password
	}
	end := len(submitted)
	start := end
	for start > 0 && submitted[start-1] >= '0' && submitted[start-1] <= '9' {
		start--
	}
	if start == end {
		return false // no code appended
	}
	return submitted[:start] == opts.Password
}

func decodeB64(s string) ([]byte, error) {
	if s == "" {
		return nil, errors.New("empty input")
	}
	return base64.StdEncoding.DecodeString(s)
}

func atoiDefault(s string, fallback int) int {
	s = strings.TrimSpace(s)
	if s == "" {
		return fallback
	}
	n := 0
	neg := false
	for i, c := range s {
		if i == 0 && c == '-' {
			neg = true
			continue
		}
		if c < '0' || c > '9' {
			return fallback
		}
		n = n*10 + int(c-'0')
	}
	if neg {
		return -n
	}
	return n
}
