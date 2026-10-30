package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
)

// selfTest runs the real SSH stack against the bundled SSH server and reports
// what it observed. It is the "is my install broken?" button: it uses the same
// Manager, PTY pump, base64 pipe and host-key store the app uses for real
// connections, so a pass means those parts work on this machine.
//
// It never reads the user's saved hosts or secrets.
func runSelfTest(passthrough sshx.EmitFunc) (string, error) {
	const (
		user     = "selftest"
		password = "selftest-one-time-password"
	)

	server, err := sshtest.New(sshtest.Options{
		User:     user,
		Password: password,
		Banner:   "GoTerm self-test server",
	})
	if err != nil {
		return "", fmt.Errorf("start local test server: %w", err)
	}
	defer server.Close()

	dir, err := os.MkdirTemp("", "goterm-selftest")
	if err != nil {
		return "", fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir)

	rec := newCollector(passthrough)
	mgr := sshx.NewManager(sshx.NewHostKeyStore(filepath.Join(dir, "known_hosts.json")))
	mgr.SetEmitter(rec.emit)
	defer mgr.CloseAll()

	var steps []string
	record := func(format string, args ...any) {
		steps = append(steps, fmt.Sprintf(format, args...))
	}

	started := time.Now()
	sess, err := mgr.Connect(sshx.Config{
		Host: "127.0.0.1", Port: server.Port(), User: user, Password: password,
		Cols: 100, Rows: 30,
	})
	if err != nil {
		return "", fmt.Errorf("dial the local test server: %w", err)
	}
	record("dial+pty %s", time.Since(started).Round(time.Millisecond))

	if err := waitFor(2*time.Second, func() bool {
		return bytes.Contains(rec.output(sess.ID()), []byte("ready (user="+user))
	}); err != nil {
		return "", fmt.Errorf("no shell banner within 2s: %w", err)
	}
	record("shell banner ok")

	addr := fmt.Sprintf("127.0.0.1:%d", server.Port())
	pin, pinned := mgr.HostKeys().FingerprintOf(addr)
	if !pinned {
		return "", fmt.Errorf("host key was not pinned")
	}
	if pin.Fingerprint != server.HostKeyFingerprint() {
		return "", fmt.Errorf("pinned %s but the server is %s", pin.Fingerprint, server.HostKeyFingerprint())
	}
	record("host key pinned %s", short(pin.Fingerprint))

	// Text round trip.
	if err := mgr.Write(sess.ID(), []byte("echo selftest-ok\r")); err != nil {
		return "", fmt.Errorf("write: %w", err)
	}
	if err := waitFor(2*time.Second, func() bool {
		return bytes.Contains(rec.output(sess.ID()), []byte("selftest-ok"))
	}); err != nil {
		return "", fmt.Errorf("echo did not come back: %w", err)
	}
	record("echo round trip ok")

	// Binary round trip: 4 KiB of random bytes out and back, byte for byte.
	payload := make([]byte, 4096)
	if _, err := rand.Read(payload); err != nil {
		return "", fmt.Errorf("rand: %w", err)
	}
	before := rec.output(sess.ID())
	if err := mgr.Write(sess.ID(), []byte("b64 "+base64.StdEncoding.EncodeToString(payload)+"\r")); err != nil {
		return "", fmt.Errorf("write payload: %w", err)
	}
	if err := waitFor(3*time.Second, func() bool {
		return bytes.Contains(rec.output(sess.ID())[len(before):], payload)
	}); err != nil {
		return "", fmt.Errorf("binary round trip lost bytes: %w", err)
	}
	record("4 KiB binary round trip byte-exact")

	// Resize must reach the far side.
	if err := mgr.Resize(sess.ID(), 132, 43); err != nil {
		return "", fmt.Errorf("resize: %w", err)
	}
	if err := waitFor(2*time.Second, func() bool {
		return bytes.Contains(rec.output(sess.ID()), []byte("[resized cols=132 rows=43]"))
	}); err != nil {
		return "", fmt.Errorf("resize did not reach the server: %w", err)
	}
	record("resize 132x43 delivered")

	// Clean exit, with the reason surfaced.
	if err := mgr.Write(sess.ID(), []byte("exit\r")); err != nil {
		return "", fmt.Errorf("write exit: %w", err)
	}
	if err := waitFor(2*time.Second, func() bool { return rec.closed(sess.ID()) }); err != nil {
		return "", fmt.Errorf("session did not close: %w", err)
	}
	record("remote exit observed")

	return "PASS — " + strings.Join(steps, ", "), nil
}

// collector watches the event stream, forwarding it onward so the UI still sees
// everything, and keeps a byte-accurate transcript per session.
type collector struct {
	mu          sync.Mutex
	transcripts map[string]*bytes.Buffer
	closedIDs   map[string]bool
	passthrough sshx.EmitFunc
}

func newCollector(passthrough sshx.EmitFunc) *collector {
	return &collector{
		transcripts: map[string]*bytes.Buffer{},
		closedIDs:   map[string]bool{},
		passthrough: passthrough,
	}
}

func (c *collector) emit(topic string, payload any) {
	c.mu.Lock()
	switch p := payload.(type) {
	case sshx.DataEvent:
		buf, ok := c.transcripts[p.SessionID]
		if !ok {
			buf = &bytes.Buffer{}
			c.transcripts[p.SessionID] = buf
		}
		if raw, err := base64.StdEncoding.DecodeString(p.Data); err == nil {
			buf.Write(raw)
		}
	case sshx.ClosedEvent:
		c.closedIDs[p.SessionID] = true
	}
	c.mu.Unlock()

	if c.passthrough != nil {
		c.passthrough(topic, payload)
	}
}

func (c *collector) output(id string) []byte {
	c.mu.Lock()
	defer c.mu.Unlock()
	if buf, ok := c.transcripts[id]; ok {
		return append([]byte(nil), buf.Bytes()...)
	}
	return nil
}

func (c *collector) closed(id string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closedIDs[id]
}

func waitFor(timeout time.Duration, cond func() bool) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return nil
		}
		time.Sleep(5 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

func short(fingerprint string) string {
	if i := strings.LastIndex(fingerprint, ":"); i >= 0 && len(fingerprint) > i+8 {
		return fingerprint[:i+9] + "…"
	}
	return fingerprint
}
