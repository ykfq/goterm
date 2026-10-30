// Package main is GoTerm's binding layer: the thin, boring surface the
// frontend calls into. Everything with real logic lives in internal/*, which is
// why this file is testable without a window.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/ykfq/goterm/internal/appdir"
	"github.com/ykfq/goterm/internal/fonts"
	"github.com/ykfq/goterm/internal/hostport"
	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/secrets"
	"github.com/ykfq/goterm/internal/sshx"
	"github.com/ykfq/goterm/internal/wintheme"
)

// Version is stamped by the build; the UI shows it in the status bar.
const Version = "1.0.0"

// App is the object bound to the frontend. Every exported method here is
// callable from TypeScript, so keep signatures additive.
type App struct {
	ctx context.Context

	mu      sync.Mutex
	emit    sshx.EmitFunc
	pings   int
	started time.Time

	mgr     *sshx.Manager
	hosts   *hoststore.Store
	secrets *secrets.Store

	// win is the app window, handed over by main once it exists. Nil in tests,
	// which is why every use of it is guarded.
	win *application.WebviewWindow
}

// NewApp wires up state directories, the secret backend, the host list and the
// SSH manager. It is safe to call before a window exists; events are dropped
// until startup hands us a context.
func NewApp() (*App, error) {
	dir, err := appdir.Dir()
	if err != nil {
		return nil, err
	}
	secretStore, err := secrets.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open secret store: %w", err)
	}
	hostStore, err := hoststore.Open(dir, secretStore)
	if err != nil {
		return nil, fmt.Errorf("open host list: %w", err)
	}
	return &App{
		mgr:     sshx.NewManager(sshx.NewHostKeyStore(hostKeysPath())),
		hosts:   hostStore,
		secrets: secretStore,
		started: time.Now(),
	}, nil
}

func hostKeysPath() string {
	path, err := appdir.Path("known_hosts.json")
	if err != nil {
		// appdir already reported the real problem; fall back to a name that
		// makes the failure obvious in a host-key error message.
		return "known_hosts.json"
	}
	return path
}

// startup is the application lifecycle hook; Wails calls it through
// ServiceStartup once the app (and its event manager) exists.
func (a *App) startup(ctx context.Context) {
	a.ctx = ctx
	a.setEmitter(func(topic string, payload any) {
		// Emitting needs the running application; during teardown it may already
		// be gone, and losing a final "closed" event is better than a panic.
		if app := application.Get(); app != nil {
			app.Event.Emit(topic, payload)
		}
	})
	a.mgr.SetEmitter(a.emitFn())
}

// ServiceStartup implements Wails v3's service lifecycle.
func (a *App) ServiceStartup(ctx context.Context, _ application.ServiceOptions) error {
	a.startup(ctx)
	return nil
}

// ServiceShutdown implements Wails v3's service lifecycle: no remote shell
// outlives the window.
func (a *App) ServiceShutdown() error {
	a.shutdown(nil)
	return nil
}

// setEmitter installs the event sink. Tests replace it with a recorder, which
// is how the whole SSH path is exercised without opening a window.
func (a *App) setEmitter(emit sshx.EmitFunc) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.emit = emit
}

func (a *App) emitFn() sshx.EmitFunc {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.emit == nil {
		return func(string, any) {}
	}
	return a.emit
}

// shutdown is Wails' OnShutdown hook: no remote shell outlives the window.
func (a *App) shutdown(_ context.Context) {
	a.mgr.CloseAll()
}

func (a *App) notify(id string) {
	if id == "" {
		return
	}
	a.hosts.Touch(id)
}

// --- sessions -------------------------------------------------------------

// Connect opens an ad-hoc session from a config typed into the connect dialog.
// Saved hosts should use ConnectHost so their password stays in Go.
func (a *App) Connect(cfg sshx.Config) (string, error) {
	sess, err := a.mgr.Connect(cfg)
	if err != nil {
		// The error is returned to the caller, which is the single place that
		// decides how to show it. Emitting a notice here as well double-toasted
		// every failed connection.
		return "", err
	}
	return sess.ID(), nil
}

// ConnectAnswers carries what the connect-time prompts collected for one
// attempt. Every field is optional — empty means "use what is stored" — and none
// of it is persisted by ConnectHost: a prompt's "remember" tick is a separate
// SetHostSecret call, which is the only way a secret reaches the key ring.
//
// It is a struct rather than five positional strings on purpose. Three of these
// are interchangeable passphrases for different machines, and the project has
// already shipped a bug where a target's answer went to the jump host's slot.
type ConnectAnswers struct {
	// Passphrase unlocks the target's encrypted private key.
	Passphrase string `json:"passphrase"`
	// ProxyPassphrase unlocks the jump host's key.
	ProxyPassphrase string `json:"proxyPassphrase"`
	// Password is typed at the prompt for a host whose password was never stored.
	Password string `json:"password"`
	// ProxyPassword is the same for the jump host's password.
	ProxyPassword string `json:"proxyPassword"`
	// TwoFactorCode is appended to the password, as these servers expect.
	TwoFactorCode string `json:"twoFactorCode"`
}

// ConnectHost connects to a saved host, resolving its secret inside Go so the
// password never crosses into JavaScript — except an answer the user typed into
// a prompt just now, which is used for this attempt only and forgotten.
func (a *App) ConnectHost(hostID string, cols, rows int, answers ConnectAnswers) (string, error) {
	cfg, err := a.hosts.SSHConfigWith(hostID, cols, rows, hoststore.ConnectOverrides{
		Passphrase:      answers.Passphrase,
		ProxyPassphrase: answers.ProxyPassphrase,
		Password:        answers.Password,
		ProxyPassword:   answers.ProxyPassword,
		TwoFactorCode:   answers.TwoFactorCode,
	})
	if err != nil {
		return "", err
	}
	sess, err := a.mgr.Connect(cfg)
	if err != nil {
		return "", err
	}
	a.notify(hostID)
	return sess.ID(), nil
}

// SetSessionLifetime sets the hard cap on one connection, in hours, and returns
// the value actually in force. Go clamps whatever it is told to 1–24 and treats
// 0 as "the default 12", so neither a stale UI nor a hand-edited settings file
// can ask for an endless session.
//
// It reaches sessions that are already open, not only the next one, and the
// deadline counts from when each connection was made — lowering the cap shortens
// a long-running session rather than silently restarting its clock. This is the
// one thing a keepalive is not allowed to defeat: keeping a link alive is about
// noticing it died, not about holding a login open for ever.
func (a *App) SetSessionLifetime(hours int) int {
	d := sshx.SessionLifetime(hours)
	a.mgr.SetMaxLifetime(d)
	return int(d / time.Hour)
}

// SendInput forwards typed text (UTF-8) to a session.
func (a *App) SendInput(sessionID, data string) error {
	return a.mgr.Write(sessionID, []byte(data))
}

// SendInputB64 forwards raw bytes. trzsz builds control strings whose characters
// are byte values, and JSON would re-encode those as UTF-8, so binary input
// travels base64-encoded. This is the path the frontend uses for everything.
func (a *App) SendInputB64(sessionID, dataB64 string) error {
	raw, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("input is not valid base64: %w", err)
	}
	return a.mgr.Write(sessionID, raw)
}

// ResizePTY tells the remote PTY the terminal's new size.
func (a *App) ResizePTY(sessionID string, cols, rows int) error {
	return a.mgr.Resize(sessionID, cols, rows)
}

// CloseSession ends one session.
func (a *App) CloseSession(sessionID string) error {
	return a.mgr.Close(sessionID)
}

// DuplicateSession opens another terminal on a session's existing SSH
// connection. Nothing is re-authenticated: the new tab is a second channel on
// the same connection, which is what makes it instant and what makes it safe to
// close either tab first.
func (a *App) DuplicateSession(sessionID string) (string, error) {
	sess, err := a.mgr.Duplicate(sessionID)
	if err != nil {
		return "", err
	}
	return sess.ID(), nil
}

// ListSessions reports live sessions for the status bar and tab restore.
func (a *App) ListSessions() []sshx.SessionInfo {
	return a.mgr.Sessions()
}

// --- hosts ----------------------------------------------------------------

// ListHosts returns saved servers. Secrets are not part of Host; the list only
// says whether one is stored.
func (a *App) ListHosts() []hoststore.Host {
	return a.hosts.List()
}

// Groups lists the group labels in use.
func (a *App) Groups() []string {
	return a.hosts.Groups()
}

// SaveHost creates or updates a saved host.
func (a *App) SaveHost(host hoststore.Host) (hoststore.Host, error) {
	saved, err := a.hosts.Save(host)
	if err != nil {
		return hoststore.Host{}, err
	}
	return saved, nil
}

// SetHostSecret stores a password or key passphrase ("" removes it).
func (a *App) SetHostSecret(hostID, secret string) error {
	return a.hosts.SetSecret(hostID, secret)
}

// SetHostProxySecret stores the jump host's password or key passphrase.
func (a *App) SetHostProxySecret(hostID, secret string) error {
	return a.hosts.SetProxySecret(hostID, secret)
}

// DeleteHost removes a saved host and its secret.
func (a *App) DeleteHost(hostID string) error {
	return a.hosts.Delete(hostID)
}

// --- host list portability ------------------------------------------------

// HostExport reports a finished export. Path is empty when the user cancelled
// the save dialog, which is not an error.
type HostExport struct {
	Path    string `json:"path"`
	Hosts   int    `json:"hosts"`
	Secrets int    `json:"secrets"`
	// Missing counts hosts whose secret the key ring would not hand over, so a
	// partial export can never pass for a complete one.
	Missing int `json:"missing"`
}

// HostImport reports what an import did to the local list.
type HostImport struct {
	Path    string `json:"path"`
	Added   int    `json:"added"`
	Updated int    `json:"updated"`
	Skipped int    `json:"skipped"`
	Secrets int    `json:"secrets"`
}

// maxImportFile bounds what one import may read from disk.
const maxImportFile = 8 << 20

// ExportHosts writes every saved host — with its secret re-encrypted under
// passphrase — to the file chosen in the native save dialog.
//
// The result is one opaque file: hosts plus passwords, sealed with a key derived
// from the passphrase the user just typed. Nothing but GoTerm can read it back,
// and without that passphrase neither can anyone else.
func (a *App) ExportHosts(passphrase string) (HostExport, error) {
	dialogs, err := a.dialogs()
	if err != nil {
		return HostExport{}, err
	}
	if passphrase == "" {
		return HostExport{}, errors.New("choose an export passphrase first — it is the only thing that unlocks the file")
	}

	// SetOptions is the only way to set a save dialog's title in this Wails
	// version, and it returns nothing, so the builder chain breaks in two here.
	saveDialog := dialogs.SaveFile()
	saveDialog.SetOptions(&application.SaveFileDialogOptions{
		Title:                "Export hosts",
		Message:              "Hosts and their encrypted secrets will be written here.",
		Filename:             suggestedExportName(time.Now()),
		CanCreateDirectories: true,
	})
	path, err := saveDialog.AddFilter("GoTerm hosts", "*.json").PromptForSingleSelection()
	if err != nil {
		return HostExport{}, err
	}
	if path == "" {
		return HostExport{}, nil // cancelled
	}
	return a.exportTo(path, passphrase)
}

// exportTo is ExportHosts without the dialog. The dialog is a thin shell around
// this on purpose: everything that can be wrong — what goes into the bundle,
// what a failed key-ring read does, whether the file is sealed before it is
// written — is exercised by app_test.go without a window.
func (a *App) exportTo(path, passphrase string) (HostExport, error) {
	if passphrase == "" {
		return HostExport{}, errors.New("choose an export passphrase first — it is the only thing that unlocks the file")
	}

	var bundle hostport.Bundle
	missing := 0
	secrets := map[string]string{}
	proxySecrets := map[string]string{}
	for _, host := range a.hosts.List() {
		if host.HasSecret {
			if secret, err := a.hosts.Secret(host.ID); err == nil && secret != "" {
				secrets[host.ID] = secret
			} else {
				missing++
			}
		}
		if host.HasProxySecret {
			if secret, err := a.hosts.ProxySecret(host.ID); err == nil && secret != "" {
				proxySecrets[host.ID] = secret
			} else {
				missing++
			}
		}
		// These two describe *this* machine's key ring, not the host.
		host.HasSecret, host.HasProxySecret = false, false
		bundle.Hosts = append(bundle.Hosts, host)
	}
	bundle.Secrets = secrets
	bundle.ProxySecrets = proxySecrets

	raw, err := hostport.Seal(bundle, passphrase)
	if err != nil {
		return HostExport{}, err
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		return HostExport{}, fmt.Errorf("write %s: %w", path, err)
	}
	return HostExport{Path: path, Hosts: len(bundle.Hosts), Secrets: len(secrets), Missing: missing}, nil
}

// ImportHosts merges a file written by ExportHosts back in: a host that is
// already here is updated, an unknown one is added. Cancelled gives a zero report.
func (a *App) ImportHosts(passphrase string) (HostImport, error) {
	dialogs, err := a.dialogs()
	if err != nil {
		return HostImport{}, err
	}

	path, err := dialogs.OpenFile().
		SetTitle("Import hosts").
		AddFilter("GoTerm hosts", "*.json").
		AddFilter("All files", "*").
		PromptForSingleSelection()
	if err != nil {
		return HostImport{}, err
	}
	if path == "" {
		return HostImport{}, nil // cancelled
	}
	return a.importFrom(path, passphrase)
}

// importFrom is ImportHosts without the dialog — see exportTo.
func (a *App) importFrom(path, passphrase string) (HostImport, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return HostImport{}, fmt.Errorf("read %s: %w", path, err)
	}
	if len(raw) > maxImportFile {
		return HostImport{}, fmt.Errorf("%s is %s — that is not a host list", path, humanBytes(len(raw)))
	}
	bundle, err := hostport.Unseal(raw, passphrase)
	if err != nil {
		return HostImport{}, err
	}

	sum := a.hosts.Import(bundle.Hosts, bundle.Secrets, bundle.ProxySecrets)
	return HostImport{Path: path, Added: sum.Added, Updated: sum.Updated, Skipped: sum.Skipped, Secrets: sum.Secrets}, nil
}

// suggestedExportName is a file name that says what it is and when it was cut.
func suggestedExportName(now time.Time) string {
	return fmt.Sprintf("goterm-hosts-%s.json", now.Format("2006-01-02"))
}

// humanBytes is for error messages, not for the terminal's byte counters.
func humanBytes(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MiB", float64(n)/float64(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KiB", float64(n)/float64(1<<10))
	default:
		return fmt.Sprintf("%d bytes", n)
	}
}

// --- trust ----------------------------------------------------------------

// KnownHosts lists pinned server fingerprints.
func (a *App) KnownHosts() []sshx.HostKeyRecord {
	return a.mgr.HostKeys().List()
}

// ForgetHostKey drops a pin, for when a server's key genuinely changed.
func (a *App) ForgetHostKey(address string) bool {
	return a.mgr.HostKeys().Forget(address)
}

// --- dialogs --------------------------------------------------------------

// dialogs returns the native dialog manager, or an error when the window is not
// up yet — which is exactly the state a unit test runs in.
func (a *App) dialogs() (*application.DialogManager, error) {
	if a.ctx == nil {
		return nil, errors.New("window is not ready yet")
	}
	app := application.Get()
	if app == nil {
		return nil, errors.New("window is not ready yet")
	}
	return app.Dialog, nil
}

// PickKeyFile opens the native file picker for a private key. Wails v3 keeps
// dialogs on the application object, so the frontend asks Go instead of poking
// at window.runtime and hoping.
func (a *App) PickKeyFile() (string, error) {
	dialogs, err := a.dialogs()
	if err != nil {
		return "", err
	}
	path, err := dialogs.OpenFile().
		SetTitle("Choose a private key").
		AddFilter("Private keys", "id_*;*.pem;*.key;*.ppk").
		AddFilter("All files", "*").
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return path, nil
}

// OpenURL hands a link to the real browser, which is what a clicked URL in
// terminal output should do.
func (a *App) OpenURL(url string) error {
	if a.ctx == nil {
		return errors.New("window is not ready yet")
	}
	if !strings.HasPrefix(url, "http://") && !strings.HasPrefix(url, "https://") {
		return fmt.Errorf("refusing to open %q: only http(s) links are allowed", url)
	}
	app := application.Get()
	if app == nil {
		return errors.New("window is not ready yet")
	}
	return app.Browser.OpenURL(url)
}

// --- where state lives ----------------------------------------------------

// StorageInfo is the answer to “where does GoTerm keep its files, and did I
// change that?”. Dir is what this process uses right now; PendingDir is what the
// next start will use. They differ until a restart, which is the honest state
// rather than pretending a move happened instantly — open files and the secret
// ring were resolved at startup.
type StorageInfo struct {
	Dir            string `json:"dir"`
	DefaultDir     string `json:"defaultDir"`
	ConfiguredDir  string `json:"configuredDir"`
	PendingDir     string `json:"pendingDir"`
	EnvDir         string `json:"envDir"`
	EnvOverride    bool   `json:"envOverride"`
	RestartPending bool   `json:"restartPending"`
}

// Storage backs the Settings → Storage section and the self-test report.
func (a *App) Storage() StorageInfo {
	current, _ := appdir.Dir()
	dflt, _ := appdir.DefaultDir()
	configured, _ := appdir.Configured()
	pending, err := appdir.PendingDir()
	if err != nil || pending == "" {
		pending = current
	}
	env := appdir.EnvDir()
	return StorageInfo{
		Dir:            current,
		DefaultDir:     dflt,
		ConfiguredDir:  configured,
		PendingDir:     pending,
		EnvDir:         env,
		EnvOverride:    env != "",
		RestartPending: !appdir.Same(pending, current),
	}
}

// SetStorageDir remembers where the next start should keep state. An empty
// string returns to the default folder. It writes nothing when GOTERM_DIR is
// set, because that variable wins and the setting would silently do nothing.
func (a *App) SetStorageDir(dir string) (StorageInfo, error) {
	if err := appdir.SetDir(dir); err != nil {
		return a.Storage(), err
	}
	return a.Storage(), nil
}

// PickStorageDir opens the native folder picker. Empty means the user cancelled;
// the folder is not adopted until SetStorageDir is called with it.
func (a *App) PickStorageDir() (string, error) {
	dialogs, err := a.dialogs()
	if err != nil {
		return "", err
	}
	dir, err := dialogs.OpenFile().
		SetTitle("Choose where GoTerm keeps its files").
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	return dir, nil
}

// --- app info -------------------------------------------------------------

// Fonts lists the monospace families installed on this machine, for the
// settings screen. A proportional face cannot render a terminal grid, so the
// filtering happens here instead of trusting whatever the form sends back.
func (a *App) Fonts() []fonts.Family {
	list, err := fonts.List()
	if err != nil {
		// No list is a bad day, not a broken app: the terminal keeps its
		// built-in stack and the settings screen shows that.
		return nil
	}
	return fonts.Monospace(list)
}

// attachWindow hands the App the window it may repaint. main calls it right
// after creating the window and before Run.
func (a *App) attachWindow(win *application.WebviewWindow) {
	a.mu.Lock()
	a.win = win
	a.mu.Unlock()
}

// SetWindowTheme paints the native title bar and window border in the app's
// theme, so a dark GoTerm on a light-mode Windows is not sitting under a white
// caption and a white hairline.
//
// The frontend calls this at startup and on every theme switch: the theme lives
// in localStorage, which Go cannot read, so the window follows the UI rather
// than the other way round. It is a no-op where the platform draws its own
// decorations (see internal/wintheme).
func (a *App) SetWindowTheme(dark bool) error {
	a.mu.Lock()
	win := a.win
	a.mu.Unlock()
	if win == nil {
		return nil // headless: tests, and the server build
	}
	hwnd := uintptr(win.NativeWindow())
	if hwnd == 0 {
		return nil
	}
	return wintheme.Apply(hwnd, dark)
}

// AppInfo describes where state lives and how it is protected. The UI prints
// secretBackend verbatim instead of claiming key-ring protection it may not
// have.
type AppInfo struct {
	Version       string `json:"version"`
	GoVersion     string `json:"goVersion"`
	Platform      string `json:"platform"`
	ConfigDir     string `json:"configDir"`
	SecretBackend string `json:"secretBackend"`
	SecretSecure  bool   `json:"secretSecure"`
	OpenSessions  int    `json:"openSessions"`
	UptimeSeconds int    `json:"uptimeSeconds"`
}

// Info backs the status bar and the About popover.
func (a *App) Info() AppInfo {
	dir, _ := appdir.Dir()
	return AppInfo{
		Version:       Version,
		GoVersion:     runtime.Version(),
		Platform:      runtime.GOOS + "/" + runtime.GOARCH,
		ConfigDir:     dir,
		SecretBackend: string(a.secrets.Backend()),
		SecretSecure:  a.secrets.Secure(),
		OpenSessions:  a.mgr.Count(),
		UptimeSeconds: int(time.Since(a.started).Seconds()),
	}
}

// Ping is the Stage-1 IPC smoke test, kept because it stays useful: it proves
// both directions of the bridge in one click (frontend → Go returns a value,
// Go → frontend receives an event).
func (a *App) Ping() string {
	a.mu.Lock()
	a.pings++
	count := a.pings
	a.mu.Unlock()

	stamp := time.Now().Format("15:04:05.000")
	message := fmt.Sprintf("Go #%d at %s (%d sessions open)", count, stamp, a.mgr.Count())
	a.emitFn()(sshx.EventNotice, sshx.NoticeEvent{Level: "info", Message: "backend " + message})
	return message
}

// DebugSelfTest drives the SSH stack against the bundled test server and
// reports what happened, so a broken install can be diagnosed from the UI
// without a terminal. It never touches the user's saved hosts.
func (a *App) DebugSelfTest() string {
	report, err := runSelfTest(a.emitFn())
	if err != nil {
		return "FAIL: " + err.Error()
	}
	return report
}

// EnvDirOverride documents the GOTERM_DIR escape hatch for tests and portable
// installs; the UI shows it in the About popover.
func envDirOverride() string { return os.Getenv("GOTERM_DIR") }
