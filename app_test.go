package main

// App-level tests drive the exact methods the frontend calls (Connect,
// SendInputB64, ResizePTY, SaveHost, SelfTest...) with a recording emitter in
// place of the webview event bridge. That covers Stage 1's "IPC contract works"
// deliverable and Stage 2's "typing reaches a remote shell" without needing a
// window, a click, or a display.

import (
	"bytes"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/appdir"
	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
	"github.com/ykfq/goterm/internal/testutil"
)

type recordingEmitter struct {
	mu     sync.Mutex
	events []struct {
		topic   string
		payload any
	}
	output map[string]*bytes.Buffer
	closed map[string]string
}

func newRecordingEmitter() *recordingEmitter {
	return &recordingEmitter{output: map[string]*bytes.Buffer{}, closed: map[string]string{}}
}

func (r *recordingEmitter) emit(topic string, payload any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, struct {
		topic   string
		payload any
	}{topic, payload})
	switch p := payload.(type) {
	case sshx.DataEvent:
		buf, ok := r.output[p.SessionID]
		if !ok {
			buf = &bytes.Buffer{}
			r.output[p.SessionID] = buf
		}
		raw, err := base64.StdEncoding.DecodeString(p.Data)
		if err != nil {
			panic("ssh:data payload is not valid base64: " + err.Error())
		}
		buf.Write(raw)
	case sshx.ClosedEvent:
		r.closed[p.SessionID] = p.Reason
	}
}

func (r *recordingEmitter) bytesFor(id string) []byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	if buf, ok := r.output[id]; ok {
		return append([]byte(nil), buf.Bytes()...)
	}
	return nil
}

func (r *recordingEmitter) closedReason(id string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.closed[id]
}

func (r *recordingEmitter) notices() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, event := range r.events {
		if notice, ok := event.payload.(sshx.NoticeEvent); ok && event.topic == sshx.EventNotice {
			out = append(out, notice.Message)
		}
	}
	return out
}

// newTestApp builds an App rooted in a temp dir with the sealed-file secret
// backend, so a test never touches the user's Credential Manager.
func newTestApp(t *testing.T) (*App, *recordingEmitter) {
	t.Helper()
	dir := testutil.TempDir(t)
	t.Setenv("GOTERM_DIR", dir)
	t.Setenv("GOTERM_SECRETS", "file")
	appdir.Reset()
	t.Cleanup(appdir.Reset)

	app, err := NewApp()
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	rec := newRecordingEmitter()
	app.setEmitter(rec.emit)
	app.mgr.SetEmitter(rec.emit)
	t.Cleanup(app.mgr.CloseAll)
	return app, rec
}

func startServer(t *testing.T) *sshtest.Server {
	t.Helper()
	srv, err := sshtest.New(sshtest.Options{
		User:     "tester",
		Password: "correct horse battery staple",
	})
	if err != nil {
		t.Fatalf("start sshtest: %v", err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	return srv
}

func configFor(srv *sshtest.Server) sshx.Config {
	return sshx.Config{
		Host: "127.0.0.1", Port: srv.Port(), User: "tester",
		Password: "correct horse battery staple",
		Cols:     100, Rows: 30,
	}
}

func waitUntil(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// Stage 1 + 2: Connect returns a session id, output arrives as base64 events,
// and typed input reaches the remote shell.
func TestAppConnectStreamsOutputAndAcceptsInput(t *testing.T) {
	app, rec := newTestApp(t)
	srv := startServer(t)

	id, err := app.Connect(configFor(srv))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if !strings.HasPrefix(id, "s_") {
		t.Fatalf("session id %q does not look like one the UI can route on", id)
	}

	waitUntil(t, "shell banner", func() bool {
		return bytes.Contains(rec.bytesFor(id), []byte("ready (user=tester"))
	})

	// The frontend's input path: base64 →bytes →PTY.
	if err := app.SendInputB64(id, base64.StdEncoding.EncodeToString([]byte("echo from-the-ui\r"))); err != nil {
		t.Fatalf("SendInputB64: %v", err)
	}
	waitUntil(t, "echoed input", func() bool {
		return bytes.Contains(rec.bytesFor(id), []byte("from-the-ui"))
	})

	if err := app.ResizePTY(id, 120, 40); err != nil {
		t.Fatalf("ResizePTY: %v", err)
	}
	// Wait for the server to say it saw the new geometry before asking the
	// shell what it thinks the size is; otherwise this asserts on a race.
	waitUntil(t, "server-side resize notification", func() bool {
		return bytes.Contains(rec.bytesFor(id), []byte("[resized cols=120 rows=40]"))
	})
	if err := app.SendInputB64(id, base64.StdEncoding.EncodeToString([]byte("resize\r"))); err != nil {
		t.Fatalf("SendInputB64: %v", err)
	}
	waitUntil(t, "resize acknowledgement", func() bool {
		return bytes.Contains(rec.bytesFor(id), []byte("resize cols=120 rows=40"))
	})

	if sessions := app.ListSessions(); len(sessions) != 1 || sessions[0].ID != id {
		t.Fatalf("ListSessions = %+v", sessions)
	}

	if err := app.CloseSession(id); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	waitUntil(t, "closed event", func() bool { return rec.closedReason(id) != "" })
	if n := len(app.ListSessions()); n != 0 {
		t.Fatalf("%d sessions still listed after CloseSession", n)
	}
}

// A binary payload must survive the whole path: JSON →base64 →Go →PTY.
func TestAppBinaryInputRoundTrip(t *testing.T) {
	app, rec := newTestApp(t)
	srv := startServer(t)

	id, err := app.Connect(configFor(srv))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	waitUntil(t, "banner", func() bool { return bytes.Contains(rec.bytesFor(id), []byte("ready (user=tester")) })

	payload := make([]byte, 2048)
	for i := range payload {
		payload[i] = byte(i * 7)
	}
	command := "b64 " + base64.StdEncoding.EncodeToString(payload) + "\r"
	if err := app.SendInputB64(id, base64.StdEncoding.EncodeToString([]byte(command))); err != nil {
		t.Fatalf("SendInputB64: %v", err)
	}
	waitUntil(t, "binary echo", func() bool {
		return bytes.Contains(rec.bytesFor(id), payload)
	})
}

func TestAppConnectBadPasswordReportsReason(t *testing.T) {
	app, _ := newTestApp(t)
	srv := startServer(t)
	cfg := configFor(srv)
	cfg.Password = "nope"

	if _, err := app.Connect(cfg); err == nil {
		t.Fatal("Connect succeeded with a bad password")
	} else if !strings.Contains(err.Error(), "authentication failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestAppSavedHostConnectKeepsTheSecretInGo(t *testing.T) {
	app, rec := newTestApp(t)
	srv := startServer(t)

	saved, err := app.SaveHost(hoststore.Host{
		Name:     "local-test",
		Host:     "127.0.0.1",
		Port:     srv.Port(),
		User:     "tester",
		AuthType: hoststore.AuthPassword,
	})
	if err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	if saved.ID == "" {
		t.Fatal("SaveHost returned no id")
	}
	if err := app.SetHostSecret(saved.ID, "correct horse battery staple"); err != nil {
		t.Fatalf("SetHostSecret: %v", err)
	}

	// ListHosts is what the sidebar renders; it must never carry the secret.
	hosts := app.ListHosts()
	if len(hosts) != 1 || !hosts[0].HasSecret {
		t.Fatalf("ListHosts = %+v", hosts)
	}
	if strings.Contains(strings.Join([]string{hosts[0].Name, hosts[0].Host, hosts[0].User}, " "), "correct horse") {
		t.Fatal("the host list leaked the password")
	}

	// Connecting by id must resolve the secret inside Go.
	id, err := app.ConnectHost(saved.ID, 90, 25, ConnectAnswers{})
	if err != nil {
		t.Fatalf("ConnectHost: %v", err)
	}
	waitUntil(t, "banner", func() bool { return bytes.Contains(rec.bytesFor(id), []byte("ready (user=tester")) })

	// And a host with no secret asks instead of failing obscurely: the error is
	// the marker the frontend turns into a password prompt.
	bare, err := app.SaveHost(hoststore.Host{Host: "127.0.0.1", Port: 1, User: "x", AuthType: hoststore.AuthPassword})
	if err != nil {
		t.Fatalf("SaveHost bare: %v", err)
	}
	_, err = app.ConnectHost(bare.ID, 80, 24, ConnectAnswers{})
	if err == nil {
		t.Fatal("ConnectHost without a stored secret succeeded")
	}
	if !errors.Is(err, hoststore.ErrPasswordRequired) {
		t.Fatalf("ConnectHost without a stored secret = %v; the frontend matches a marker, not prose", err)
	}
}

func TestAppHostCRUD(t *testing.T) {
	app, _ := newTestApp(t)

	if err := app.SetHostSecret("ghost", "x"); err == nil {
		t.Fatal("SetHostSecret accepted an unknown host")
	}
	if _, err := app.SaveHost(hoststore.Host{Host: "", User: "u", AuthType: hoststore.AuthPassword}); err == nil {
		t.Fatal("SaveHost accepted an empty address")
	}

	saved, err := app.SaveHost(hoststore.Host{Name: "a", Host: "h1", User: "u", AuthType: hoststore.AuthAgent, Group: "lab"})
	if err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	if groups := app.Groups(); len(groups) != 1 || groups[0] != "lab" {
		t.Fatalf("Groups = %v", groups)
	}
	if err := app.DeleteHost(saved.ID); err != nil {
		t.Fatalf("DeleteHost: %v", err)
	}
	if len(app.ListHosts()) != 0 {
		t.Fatal("host survived DeleteHost")
	}
}

func TestAppKnownHostsAndTrust(t *testing.T) {
	app, _ := newTestApp(t)
	srv := startServer(t)

	id, err := app.Connect(configFor(srv))
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer app.CloseSession(id)

	records := app.KnownHosts()
	if len(records) != 1 {
		t.Fatalf("KnownHosts = %+v", records)
	}
	if records[0].Fingerprint != srv.HostKeyFingerprint() {
		t.Fatalf("pinned %s, server is %s", records[0].Fingerprint, srv.HostKeyFingerprint())
	}
	if !app.ForgetHostKey(records[0].Address) {
		t.Fatal("ForgetHostKey reported nothing removed")
	}
	if len(app.KnownHosts()) != 0 {
		t.Fatal("record survived ForgetHostKey")
	}
}

func TestAppInfoDescribesTheSecretBackendHonestly(t *testing.T) {
	app, _ := newTestApp(t)
	info := app.Info()
	if info.Version == "" || info.ConfigDir == "" {
		t.Fatalf("Info = %+v", info)
	}
	if info.SecretBackend != "sealed-file" {
		t.Fatalf("backend = %q; the test pinned GOTERM_SECRETS=file", info.SecretBackend)
	}
	if info.SecretSecure {
		t.Fatal("the sealed-file backend must not be reported as secure")
	}
	if !strings.Contains(info.GoVersion, "go1.") {
		t.Fatalf("GoVersion = %q", info.GoVersion)
	}
}

// Stage 1's smoke test and Stage 5's "verify from inside the app" both use this.
func TestAppPingEmitsAnEvent(t *testing.T) {
	app, rec := newTestApp(t)
	reply := app.Ping()
	if !strings.Contains(reply, "Go #1") {
		t.Fatalf("Ping reply = %q", reply)
	}
	if !strings.Contains(reply, "Go #1 at ") {
		t.Fatalf("Ping reply lacks a timestamp: %q", reply)
	}
	found := false
	for _, message := range rec.notices() {
		if strings.Contains(message, "backend Go #1") {
			found = true
		}
	}
	if !found {
		t.Fatalf("Ping did not emit a notice event; notices: %v", rec.notices())
	}
}

func TestAppSelfTestPasses(t *testing.T) {
	app, _ := newTestApp(t)
	report := app.DebugSelfTest()
	if !strings.HasPrefix(report, "PASS") {
		t.Fatalf("self-test did not pass:\n%s", report)
	}
	for _, want := range []string{"echo round trip", "binary round trip", "resize 132x43", "host key pinned"} {
		if !strings.Contains(report, want) {
			t.Fatalf("self-test report is missing %q:\n%s", want, report)
		}
	}
}

func TestAppShutdownClosesSessions(t *testing.T) {
	app, _ := newTestApp(t)
	srv := startServer(t)
	if _, err := app.Connect(configFor(srv)); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	app.shutdown(nil)
	if n := len(app.ListSessions()); n != 0 {
		t.Fatalf("%d sessions survived shutdown", n)
	}
}

func TestAppStateLivesInGotermDir(t *testing.T) {
	app, _ := newTestApp(t)
	dir := os.Getenv("GOTERM_DIR")
	if _, err := app.SaveHost(hoststore.Host{Name: "x", Host: "h", User: "u", AuthType: hoststore.AuthAgent}); err != nil {
		t.Fatalf("SaveHost: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "hosts.json")); err != nil {
		t.Fatalf("hosts.json not written to GOTERM_DIR: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "known_hosts.json")); err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("unexpected known_hosts error: %v", err)
	}
}

// The storage folder is a setting, and with GOTERM_DIR in the environment the
// setting cannot win — so it says so instead of writing a value that loses.
func TestStorageYieldsToTheEnvironment(t *testing.T) {
	app, _ := newTestApp(t)
	got := app.Storage()
	if !got.EnvOverride {
		t.Fatalf("EnvOverride = false, but GOTERM_DIR is %q", got.EnvDir)
	}
	if got.Dir != got.EnvDir {
		t.Fatalf("Dir = %q, want the env dir %q", got.Dir, got.EnvDir)
	}
	if got.RestartPending {
		t.Fatalf("RestartPending = true, but nothing differs: %+v", got)
	}
	if _, err := app.SetStorageDir(filepath.Join(t.TempDir(), "ignored")); !errors.Is(err, appdir.ErrEnvOverride) {
		t.Fatalf("SetStorageDir with GOTERM_DIR set = %v, want ErrEnvOverride", err)
	}
	if after := app.Storage(); after.RestartPending {
		t.Fatalf("a refused change must not become pending: %+v", after)
	}
}

// Without GOTERM_DIR the setting decides the next start, and the running
// process keeps its own directory until it is restarted.
func TestStorageSettingTakesEffectOnTheNextStart(t *testing.T) {
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skipf("UserConfigDir is not redirectable on %s", runtime.GOOS)
	}
	base := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", base)
	} else {
		t.Setenv("XDG_CONFIG_HOME", base)
	}
	t.Setenv("GOTERM_DIR", "")
	appdir.Reset()
	t.Cleanup(appdir.Reset)

	a := &App{}
	first := a.Storage()
	if first.EnvOverride || first.RestartPending || first.ConfiguredDir != "" {
		t.Fatalf("fresh install should use the default: %+v", first)
	}

	chosen := filepath.Join(t.TempDir(), "state-elsewhere")
	got, err := a.SetStorageDir(chosen)
	if err != nil {
		t.Fatalf("SetStorageDir: %v", err)
	}
	if got.PendingDir != chosen || got.ConfiguredDir != chosen {
		t.Fatalf("setting not recorded: %+v", got)
	}
	if !got.RestartPending {
		t.Fatalf("running process must still report the old dir: %+v", got)
	}
	if got.Dir == chosen {
		t.Fatalf("Dir changed without a restart: %+v", got)
	}

	back, err := a.SetStorageDir("")
	if err != nil {
		t.Fatalf("SetStorageDir(\"\"): %v", err)
	}
	if back.RestartPending || back.PendingDir != first.DefaultDir {
		t.Fatalf("clearing the setting should return to the default: %+v", back)
	}
}
