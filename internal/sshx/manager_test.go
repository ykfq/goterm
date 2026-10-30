package sshx

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/testutil"
)

// readyMarker is what the repeater shell prints first. It deliberately avoids
// the word "sshtest" because the shell colourises between the two words, and an
// ANSI reset in the middle of a marker is a great way to write a test that can
// never pass.
const readyMarker = "ready (user=tester"

// recorder collects everything the Manager emits, so assertions are made on
// the exact payloads the frontend would receive.
type recorder struct {
	mu     sync.Mutex
	events []event
	data   map[string]*bytes.Buffer
}

type event struct {
	topic   string
	payload any
}

func newRecorder() *recorder {
	return &recorder{data: map[string]*bytes.Buffer{}}
}

func (r *recorder) emit(topic string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event{topic: topic, payload: payload})
	if de, ok := payload.(DataEvent); ok {
		buf, ok := r.data[de.SessionID]
		if !ok {
			buf = &bytes.Buffer{}
			r.data[de.SessionID] = buf
		}
		raw, err := base64.StdEncoding.DecodeString(de.Data)
		if err != nil {
			panic(fmt.Sprintf("emitted data is not valid base64: %v", err))
		}
		buf.Write(raw)
	}
}

func (r *recorder) output(id string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if buf, ok := r.data[id]; ok {
		return append([]byte(nil), buf.Bytes()...)
	}
	return nil
}

func (r *recorder) countDataEvents() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, e := range r.events {
		if e.topic == EventData {
			n++
		}
	}
	return n
}

func (r *recorder) closed(id string) (ClosedEvent, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.events {
		if ce, ok := e.payload.(ClosedEvent); ok && e.topic == EventClosed && ce.SessionID == id {
			return ce, true
		}
	}
	return ClosedEvent{}, false
}

func (r *recorder) notices() []NoticeEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []NoticeEvent
	for _, e := range r.events {
		if ne, ok := e.payload.(NoticeEvent); ok {
			out = append(out, ne)
		}
	}
	return out
}

func (r *recorder) noticeText() string {
	var b strings.Builder
	for _, n := range r.notices() {
		b.WriteString(n.Level)
		b.WriteString(": ")
		b.WriteString(n.Message)
		b.WriteString("\n")
	}
	return b.String()
}

// harness is a server plus a manager wired to a recorder.
type harness struct {
	t     *testing.T
	srv   *sshtest.Server
	mgr   *Manager
	rec   *recorder
	store *HostKeyStore
}

func newHarness(t *testing.T, opts sshtest.Options) *harness {
	t.Helper()
	if opts.Password == "" && len(opts.AuthorizedKeys) == 0 {
		opts.Password = "correct horse battery staple"
	}
	srv, err := sshtest.New(opts)
	if err != nil {
		t.Fatalf("start sshtest server: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	dir := testutil.TempDir(t)
	store := NewHostKeyStore(filepath.Join(dir, "known_hosts.json"))
	rec := newRecorder()
	mgr := NewManager(store)
	mgr.SetEmitter(rec.emit)
	t.Cleanup(mgr.CloseAll)
	return &harness{t: t, srv: srv, mgr: mgr, rec: rec, store: store}
}

func (h *harness) config() Config {
	host, port, err := netHostPort(h.srv.Addr())
	if err != nil {
		h.t.Fatalf("server addr %q: %v", h.srv.Addr(), err)
	}
	return Config{
		Host:     host,
		Port:     port,
		User:     "tester",
		Password: "correct horse battery staple",
		Cols:     100,
		Rows:     30,
	}
}

func (h *harness) connect(cfg Config) *Session {
	h.t.Helper()
	sess, err := h.mgr.Connect(cfg)
	if err != nil {
		h.t.Fatalf("connect: %v", err)
	}
	return sess
}

// waitFor polls cond until it holds or the deadline passes, then dumps what
// the session actually printed — failures should be self-explaining.
func (h *harness) waitFor(sess *Session, what string, cond func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("timed out waiting for %s\n--- output so far ---\n%s\n--- notices ---\n%s",
		what, printable(h.rec.output(sess.ID())), h.rec.noticeText())
}

func (h *harness) expectOutput(sess *Session, want string) {
	h.t.Helper()
	h.waitFor(sess, "output containing "+strconv.Quote(want), func() bool {
		return bytes.Contains(h.rec.output(sess.ID()), []byte(want))
	})
}

func (h *harness) send(sess *Session, s string) {
	h.t.Helper()
	if err := h.mgr.Write(sess.ID(), []byte(s)); err != nil {
		h.t.Fatalf("write %q: %v", s, err)
	}
}

func TestConnectStreamsShellOutput(t *testing.T) {
	h := newHarness(t, sshtest.Options{Banner: "authorized users only"})
	sess := h.connect(h.config())

	h.expectOutput(sess, readyMarker)
	if !strings.Contains(h.rec.noticeText(), "authorized users only") {
		t.Fatalf("banner notice missing; notices were:\n%s", h.rec.noticeText())
	}
	if !strings.Contains(h.rec.noticeText(), "trusted on first use") {
		t.Fatalf("first-use notice missing; notices were:\n%s", h.rec.noticeText())
	}
	if got := sess.Info().ServerVersion; !strings.Contains(got, "goterm_sshtest") {
		t.Fatalf("ServerVersion = %q, want it to contain goterm_sshtest", got)
	}
	if fp := sess.Info().Fingerprint; fp != h.srv.HostKeyFingerprint() {
		t.Fatalf("pinned fingerprint = %q, want %q", fp, h.srv.HostKeyFingerprint())
	}

	h.send(sess, "echo hello world\r")
	h.expectOutput(sess, "hello world")
}

func TestTermAndInitialWinsizeReachServer(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	cfg := h.config()
	cfg.Cols, cfg.Rows, cfg.Term = 132, 43, "xterm-256color"
	sess := h.connect(cfg)

	h.expectOutput(sess, readyMarker)
	h.send(sess, "term\r")
	h.expectOutput(sess, "xterm-256color")
	h.send(sess, "resize\r")
	h.expectOutput(sess, "resize cols=132 rows=43")
}

func TestResizeReachesRemotePty(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	if err := h.mgr.Resize(sess.ID(), 120, 40); err != nil {
		t.Fatalf("resize: %v", err)
	}
	h.expectOutput(sess, "[resized cols=120 rows=40]")

	// The shell's own view must agree with what we asked for.
	h.send(sess, "resize\r")
	h.expectOutput(sess, "resize cols=120 rows=40")
}

func TestBinaryOutputIsByteExact(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	// 0x00-0xff in one shot: any base64/JSON/UTF-8 mishacking in the pipe shows
	// up as a mismatch here.
	h.send(sess, "bytes\r")
	h.expectOutput(sess, "[bytes ok]")

	want := make([]byte, 256)
	for i := range want {
		want[i] = byte(i)
	}
	got := h.rec.output(sess.ID())
	i := bytes.Index(got, want)
	if i < 0 {
		t.Fatalf("stream does not contain the full 0x00-0xff range\n got: %s", printable(got))
	}
}

func TestBinaryInputIsByteExact(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	payload := make([]byte, 4096)
	if _, err := rand.Read(payload); err != nil {
		t.Fatalf("rand: %v", err)
	}
	// The repeater only accepts printable input, so the payload travels in
	// base64: the server decodes it and writes the raw bytes back.
	h.send(sess, "b64 "+base64.StdEncoding.EncodeToString(payload)+"\r")
	h.expectOutput(sess, fmt.Sprintf("[b64 ok %d bytes]", len(payload)))

	if !bytes.Contains(h.rec.output(sess.ID()), payload) {
		t.Fatalf("round-tripped payload not found in output")
	}
}

func TestWrongPasswordIsRefused(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	cfg := h.config()
	cfg.Password = "wrong"

	_, err := h.mgr.Connect(cfg)
	if err == nil {
		t.Fatal("connect succeeded with a wrong password")
	}
	if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %v, want it to say authentication failed", err)
	}
	// The server records the attempt on its own goroutine; give it a moment
	// rather than asserting on a coin flip.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && h.srv.AuthFailures() == 0 {
		time.Sleep(10 * time.Millisecond)
	}
	if h.srv.AuthFailures() == 0 {
		t.Fatal("server never saw a failed attempt")
	}
	if h.mgr.Count() != 0 {
		t.Fatalf("failed connect left %d sessions behind", h.mgr.Count())
	}
}

func TestHostKeyMismatchRefusesConnection(t *testing.T) {
	dir := testutil.TempDir(t)
	store := NewHostKeyStore(filepath.Join(dir, "known_hosts.json"))
	rec := newRecorder()
	mgr := NewManager(store)
	mgr.SetEmitter(rec.emit)
	defer mgr.CloseAll()

	first, err := sshtest.New(sshtest.Options{Password: "pw"})
	if err != nil {
		t.Fatalf("first server: %v", err)
	}
	addr := first.Addr()
	if _, err := mgr.Connect(Config{Host: "127.0.0.1", Port: portOf(t, addr), User: "tester", Password: "pw"}); err != nil {
		_ = first.Close()
		t.Fatalf("first connect: %v", err)
	}
	if _, ok := store.FingerprintOf(addr); !ok {
		_ = first.Close()
		t.Fatalf("fingerprint for %s was not recorded", addr)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first server: %v", err)
	}

	// Same address, different identity: this is what an interception looks like.
	hostKey := newHostKey(t)
	second, err := sshtest.NewOn(addr, sshtest.Options{Password: "pw", HostKey: hostKey})
	if err != nil {
		t.Fatalf("second server on %s: %v", addr, err)
	}
	defer second.Close()

	_, err = mgr.Connect(Config{Host: "127.0.0.1", Port: portOf(t, addr), User: "tester", Password: "pw"})
	if !errors.Is(err, ErrHostKeyChanged) {
		t.Fatalf("error = %v, want ErrHostKeyChanged", err)
	}
	if !strings.Contains(err.Error(), second.HostKeyFingerprint()) || !strings.Contains(err.Error(), first.HostKeyFingerprint()) {
		t.Fatalf("error should name both fingerprints, got: %v", err)
	}
}

func TestStrictPolicyRejectsUnknownHost(t *testing.T) {
	srv, err := sshtest.New(sshtest.Options{Password: "pw"})
	if err != nil {
		t.Fatalf("server: %v", err)
	}
	defer srv.Close()

	mgr := NewManager(NewHostKeyStore(filepath.Join(testutil.TempDir(t), "kh.json")))
	_, err = mgr.Connect(Config{
		Host: "127.0.0.1", Port: srv.Port(), User: "tester", Password: "pw",
		HostKeyPolicy: PolicyStrict,
	})
	if err == nil || !strings.Contains(err.Error(), "policy=strict") {
		t.Fatalf("error = %v, want a strict-policy refusal", err)
	}
}

func TestRemoteExitClosesSessionAndEmitsOnce(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	h.send(sess, "exit 3\r")

	deadline := time.Now().Add(5 * time.Second)
	var ce ClosedEvent
	for time.Now().Before(deadline) {
		if got, ok := h.rec.closed(sess.ID()); ok {
			ce = got
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if ce.SessionID == "" {
		t.Fatal("no closed event")
	}
	if !strings.Contains(ce.Reason, "status 3") {
		t.Fatalf("reason = %q, want it to mention exit status 3", ce.Reason)
	}
	if ce.Session.Status != StatusClosed {
		t.Fatalf("payload status = %q, want %q", ce.Session.Status, StatusClosed)
	}
	if h.mgr.Count() != 0 {
		t.Fatalf("manager still lists %d sessions", h.mgr.Count())
	}
	if err := h.mgr.Write(sess.ID(), []byte("x")); !errors.Is(err, ErrClosed) {
		t.Fatalf("write after close = %v, want ErrClosed", err)
	}

	// Closing again must not emit a second event.
	_ = sess.Close()
	time.Sleep(50 * time.Millisecond)
	if n := h.countClosed(sess.ID()); n != 1 {
		t.Fatalf("closed events = %d, want exactly 1", n)
	}
}

func (h *harness) countClosed(id string) int {
	h.t.Helper()
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	n := 0
	for _, e := range h.rec.events {
		if e.topic == EventClosed {
			if ce, ok := e.payload.(ClosedEvent); ok && ce.SessionID == id {
				n++
			}
		}
	}
	return n
}

func TestDroppedConnectionReportsConnectionLost(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	h.srv.CloseAllConns()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, ok := h.rec.closed(sess.ID()); ok {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("no closed event after the server hung up\nnotices:\n%s", h.rec.noticeText())
}

func TestOutputIsBatchedNotOneEventPerChunk(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	h.send(sess, "spam 4000\r")
	h.expectOutput(sess, "line 004000")

	n := h.rec.countDataEvents()
	if n > 400 {
		t.Fatalf("%d data events for ~280KB of output: batching is not working", n)
	}
	if n < 2 {
		t.Fatalf("%d data events: output arrived in one piece, which means the pump is not streaming", n)
	}
}

func TestConcurrentSessionsStaySeparate(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	const sessions = 5

	ids := map[string]string{}
	for i := 0; i < sessions; i++ {
		sess := h.connect(h.config())
		h.expectOutput(sess, readyMarker)
		marker := fmt.Sprintf("marker-%d", i)
		h.send(sess, "echo "+marker+"\r")
		h.waitFor(sess, marker, func() bool { return bytes.Contains(h.rec.output(sess.ID()), []byte(marker)) })
		ids[sess.ID()] = marker
	}
	if h.mgr.Count() != sessions {
		t.Fatalf("manager holds %d sessions, want %d", h.mgr.Count(), sessions)
	}
	for id, marker := range ids {
		out := h.rec.output(id)
		if !bytes.Contains(out, []byte(marker)) {
			t.Fatalf("session %s lost its own output", id)
		}
		for otherID, otherMarker := range ids {
			if otherID != id && bytes.Contains(out, []byte(otherMarker)) {
				t.Fatalf("session %s received %s, which belongs to %s", id, otherMarker, otherID)
			}
		}
	}
}

func TestCloseAllEndsSessions(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	first := h.connect(h.config())
	second := h.connect(h.config())
	h.mgr.CloseAll()

	for _, s := range []*Session{first, second} {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, ok := h.rec.closed(s.ID()); ok {
				break
			}
			time.Sleep(5 * time.Millisecond)
		}
		if _, ok := h.rec.closed(s.ID()); !ok {
			t.Fatalf("session %s did not report closure", s.ID())
		}
	}
	if h.mgr.Count() != 0 {
		t.Fatalf("%d sessions still live after CloseAll", h.mgr.Count())
	}
}

func TestPublicKeyAuthFromKeyFile(t *testing.T) {
	dir := testutil.TempDir(t)
	keyPath := filepath.Join(dir, "id_ed25519")
	pub, err := writePKCS8Key(t, keyPath)
	if err != nil {
		t.Fatalf("write key: %v", err)
	}

	h := newHarness(t, sshtest.Options{AuthorizedKeys: []ssh.PublicKey{pub}})
	cfg := h.config()
	cfg.Password = ""
	cfg.AuthType = AuthKey
	cfg.KeyPath = keyPath

	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	h.send(sess, "echo keyed-in\r")
	h.expectOutput(sess, "keyed-in")
}

func TestEncryptedKeyNeedsAndUsesPassphrase(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("ssh-keygen not installed")
	}
	dir := testutil.TempDir(t)
	keyPath := filepath.Join(dir, "id_enc")
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "s3cret", "-C", "goterm-test", "-f", keyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Skipf("ssh-keygen failed: %v: %s", err, out)
	}

	h := newHarness(t, sshtest.Options{AuthorizedKeys: authorizedKeysFromFile(t, keyPath+".pub")})
	cfg := h.config()
	cfg.Password = ""
	cfg.AuthType = AuthKey
	cfg.KeyPath = keyPath

	if _, err := h.mgr.Connect(cfg); err == nil {
		t.Fatal("an encrypted key connected without its passphrase")
	} else if !strings.Contains(err.Error(), "passphrase") {
		t.Fatalf("error = %v, want it to ask for a passphrase", err)
	}

	cfg.Passphrase = "s3cret"
	sess := h.connect(cfg)
	h.expectOutput(sess, readyMarker)
	h.send(sess, "echo unlocked\r")
	h.expectOutput(sess, "unlocked")
}

func TestConfigNormalizeDefaults(t *testing.T) {
	c := Config{Host: "example.com", User: "root", Password: "pw"}
	if err := c.Normalize(); err != nil {
		t.Fatalf("normalize: %v", err)
	}
	if c.Port != DefaultPort || c.Cols != DefaultCols || c.Rows != DefaultRows || c.Term != DefaultTerm {
		t.Fatalf("defaults not filled: %+v", c)
	}
	if c.AuthType != AuthPassword || c.HostKeyPolicy != PolicyTOFU {
		t.Fatalf("auth/policy = %q/%q", c.AuthType, c.HostKeyPolicy)
	}
	if c.Address() != "example.com:22" {
		t.Fatalf("address = %q", c.Address())
	}
	if strings.Contains(c.Redacted(), "pw") {
		t.Fatal("Redacted leaked the password")
	}

	v6 := Config{Host: "fe80::1", User: "root", Password: "x"}
	if err := v6.Normalize(); err != nil {
		t.Fatalf("normalize v6: %v", err)
	}
	if v6.Address() != "[fe80::1]:22" {
		t.Fatalf("ipv6 address = %q", v6.Address())
	}

	bad := Config{Host: "h", User: "u", Password: "p", Port: 70000}
	if err := bad.Normalize(); err == nil {
		t.Fatal("port 70000 accepted")
	}
	empty := Config{User: "u", Password: "p"}
	if err := empty.Normalize(); err == nil {
		t.Fatal("empty host accepted")
	}
	noCreds := Config{Host: "h", User: "u", AuthType: AuthPassword}
	if err := noCreds.Normalize(); err == nil {
		t.Fatal("password auth without a password accepted")
	}
}

func TestHostKeyStoreForgetAndList(t *testing.T) {
	path := filepath.Join(testutil.TempDir(t), "kh.json")
	store := NewHostKeyStore(path)
	key := newHostKey(t).PublicKey()

	note, err := store.Check("127.0.0.1:2222", key, PolicyTOFU)
	if err != nil || note == "" {
		t.Fatalf("first check: note=%q err=%v", note, err)
	}
	if note, err := store.Check("127.0.0.1:2222", key, PolicyTOFU); err != nil || note != "" {
		t.Fatalf("second check should be silent: note=%q err=%v", note, err)
	}
	rec, ok := store.FingerprintOf("127.0.0.1:2222")
	if !ok || rec.Fingerprint != ssh.FingerprintSHA256(key) {
		t.Fatalf("record = %+v", rec)
	}
	if got := store.List(); len(got) != 1 {
		t.Fatalf("list = %+v", got)
	}
	if !store.Forget("127.0.0.1:2222") {
		t.Fatal("forget reported nothing removed")
	}
	if _, ok := store.FingerprintOf("127.0.0.1:2222"); ok {
		t.Fatal("entry survived Forget")
	}

	// The store must survive a restart, or every launch is a new TOFU.
	store2 := NewHostKeyStore(path)
	if _, err := store2.Check("10.0.0.1:22", key, PolicyTOFU); err != nil {
		t.Fatalf("check after reload: %v", err)
	}
	if len(store2.List()) != 1 {
		t.Fatalf("reload lost entries: %+v", store2.List())
	}
}

func TestDialErrorMessages(t *testing.T) {
	cases := []struct {
		raw     string
		want    string
		timeout time.Duration
	}{
		{"dial tcp 127.0.0.1:1: connectex: No connection could be made because the target machine actively refused it.", "is sshd listening", 0},
		{"dial tcp 10.0.0.1:22: connect: connection refused", "is sshd listening", 0},
		{"dial tcp: lookup nope.invalid: no such host", "cannot resolve", 0},
		{"dial tcp 10.0.0.1:22: i/o timeout", "no answer", 20 * time.Second},
		{"ssh: unable to authenticate, attempted methods [none password], no supported methods remained", "authentication failed", 0},
	}
	for _, tc := range cases {
		got := dialError(Config{Host: "h", Port: 22, User: "u", AuthType: AuthPassword}, []string{"password"}, 20*time.Second, errors.New(tc.raw))
		if !strings.Contains(got.Error(), tc.want) {
			t.Errorf("dialError(%q) = %q, want it to contain %q", tc.raw, got, tc.want)
		}
	}
}

func TestUnreachableHostFailsFast(t *testing.T) {
	// Port 1 on loopback is refused immediately on every OS we ship on.
	mgr := NewManager(NewHostKeyStore(filepath.Join(testutil.TempDir(t), "kh.json")))
	start := time.Now()
	_, err := mgr.Connect(Config{Host: "127.0.0.1", Port: 1, User: "u", Password: "p"})
	if err == nil {
		t.Fatal("connect to a closed port succeeded")
	}
	if !strings.Contains(err.Error(), "refused") {
		t.Fatalf("error = %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatalf("took %s to fail", time.Since(start))
	}
}

// helpers

// netHostPort splits "127.0.0.1:52123" into its parts.
func netHostPort(addr string) (string, int, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, err
	}
	n, err := strconv.Atoi(port)
	if err != nil {
		return host, 0, fmt.Errorf("port %q: %w", port, err)
	}
	return host, n, nil
}

func newHostKey(t *testing.T) ssh.Signer {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519: %v", err)
	}
	signer, err := ssh.NewSignerFromKey(priv)
	if err != nil {
		t.Fatalf("signer: %v", err)
	}
	return signer
}

func writePKCS8Key(t *testing.T, path string) (ssh.PublicKey, error) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	der, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(path, pemBytes, 0o600); err != nil {
		return nil, err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, err
	}
	return sshPub, nil
}

func publicKeyFromFile(t *testing.T, path string) ssh.PublicKey {
	t.Helper()
	key, _, _, _, err := ssh.ParseAuthorizedKey(readFile(t, path))
	if err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	return key
}

func authorizedKeysFromFile(t *testing.T, path string) []ssh.PublicKey {
	t.Helper()
	return []ssh.PublicKey{publicKeyFromFile(t, path)}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return b
}

func portOf(t *testing.T, addr string) int {
	t.Helper()
	_, port, err := netHostPort(addr)
	if err != nil {
		t.Fatalf("addr %q: %v", addr, err)
	}
	return port
}

// printable escapes control bytes so a failing terminal dump is readable in
// test output.
func printable(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c == '\r':
			sb.WriteString("\\r")
		case c == '\n':
			sb.WriteString("\\n")
		case c == 0x1b:
			sb.WriteString("^[")
		case c < 0x20 || c == 0x7f:
			fmt.Fprintf(&sb, "\\x%02x", c)
		default:
			sb.WriteByte(c)
		}
	}
	return sb.String()
}
