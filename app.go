package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/appdir"
	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/fonts"
	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/secrets"
	"github.com/ykfq/goterm/internal/settings"
	"github.com/ykfq/goterm/internal/sshx"
)

// Version is what the status bar and About show.
const Version = "2.0.0-dev"

// App is GoTerm's state. In 1.x this type was a Wails service and the frontend
// called it over IPC; now it is an ordinary Go value that the view reads on the
// main thread and background work updates through win.Update.
type App struct {
	win *mygo.Window

	mgr    *sshx.Manager
	hosts  *hoststore.Store
	secs   *secrets.Store
	dir    string
	prefs  settings.Settings
	prefOK bool

	fonts []string

	tabs   []*Tab
	active int

	// startHost is --connect: a saved host to open once the window exists.
	startHost string

	query        string
	selectedHost string
	openGroups   map[string]bool

	showSettings   bool
	showKnownHosts bool
	showHostEditor bool
	showAbout      bool
	settingsPage   int

	editor HostEditor

	prompt  *Prompt
	confirm *Confirm
	status  string

	// logDir and configPath are resolved once; the settings dialog shows them.
	logDir     string
	configPath string

	mu sync.Mutex
}

// Tab is one terminal tab: a remote PTY, its emulator and what the tab bar
// shows.
type Tab struct {
	ID     string
	HostID string
	Title  string
	Status string // connecting | connected | closed | failed
	Err    string

	Sess  *sshx.Session
	Term  *terminal.Terminal
	Conn  io.Closer
	Focus ui.Handle

	Cfg sshx.Config
	OV  hoststore.ConnectOverrides

	LogPath string
	Via     string

	SearchOpen    bool
	SearchQuery   string
	SearchTotal   int
	SearchCurrent int
	Searching     bool

	Started time.Time
	titleMu sync.Mutex

	// AuthFailures counts refused secret prompts, so the dialog can stop at
	// three instead of looping forever.
	AuthFailures int
}

const (
	StatusConnecting = "connecting"
	StatusConnected  = "connected"
	StatusClosed     = "closed"
	StatusFailed     = "failed"
)

// NewApp opens the state directory, the host store and the SSH manager.
func NewApp() (*App, error) {
	dir, err := appdir.Dir()
	if err != nil {
		return nil, fmt.Errorf("locate data directory: %w", err)
	}
	secs, err := secrets.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("open credential store: %w", err)
	}
	hosts, err := hoststore.Open(dir, secs)
	if err != nil {
		return nil, fmt.Errorf("open host store: %w", err)
	}
	hostKeys := sshx.NewHostKeyStore(filepath.Join(dir, "known_hosts.json"))
	mgr := sshx.NewManager(hostKeys)

	prefs := settings.Load(filepath.Join(dir, "settings.json"))
	mgr.SetIdleTimeout(sshx.IdleTimeout(prefs.IdleTimeoutHours))
	logDir, err := appdir.Path("logs")
	if err != nil {
		return nil, fmt.Errorf("locate log directory: %w", err)
	}
	if _, err := mgr.ConfigureSessionLogging(prefs.SessionLogging, logDir); err != nil {
		log.Printf("goterm: session logging: %v", err)
	}

	a := &App{
		mgr:        mgr,
		hosts:      hosts,
		secs:       secs,
		dir:        dir,
		prefs:      prefs,
		prefOK:     true,
		openGroups: map[string]bool{},
		logDir:     logDir,
		configPath: filepath.Join(dir, "settings.json"),
	}
	if list, err := fonts.List(); err == nil {
		for _, f := range fonts.Monospace(list) {
			a.fonts = append(a.fonts, f.Name)
		}
	}
	mgr.SetEmitter(func(topic string, payload any) {
		switch topic {
		case sshx.EventClosed:
			ev, ok := payload.(sshx.ClosedEvent)
			if !ok {
				return
			}
			a.post(func() { a.sessionClosed(ev) })
		case sshx.EventNotice:
			ev, ok := payload.(sshx.NoticeEvent)
			if !ok {
				return
			}
			a.post(func() {
				if ev.Level == "warn" || ev.Level == "error" {
					a.status = ev.Message
					if a.win != nil {
						a.win.Invalidate()
					}
				}
			})
		}
	})
	return a, nil
}

// post runs fn on the UI thread when there is a window, and directly before
// one exists (which is only during start-up).
func (a *App) post(fn func()) {
	if a.win == nil {
		fn()
		return
	}
	a.win.Update(fn)
}

// Open creates the main window and the menu.
func (a *App) Open() {
	a.applyAppearance()
	a.win = mygo.NewWindow(mygo.WindowOptions{
		Title:     "GoTerm",
		Width:     1200,
		Height:    800,
		MinWidth:  760,
		MinHeight: 480,
		StateKey:  "main",
		Content:   ui.View(a.view),
	})
	a.installMenu()
	a.win.OnClosed(func() { a.Close() })

	if hosts := a.hosts.List(); len(hosts) > 0 && a.selectedHost == "" {
		if h, ok := a.hosts.Get(a.prefs.LastHostID); ok {
			a.selectedHost = h.ID
		}
	}
	if a.startHost != "" {
		a.connectHost(a.startHost)
	}
}

// Close releases every session and persists what changed.
func (a *App) Close() {
	if a.mgr != nil {
		// CloseAll ends every session, which announces itself to the window;
		// doing that on its own goroutine keeps quit from waiting on a frame.
		go a.mgr.CloseAll()
	}
	a.savePrefs()
}

func (a *App) savePrefs() {
	a.prefs.LastHostID = a.selectedHost
	if err := a.prefs.Save(a.configPath); err != nil {
		log.Printf("goterm: save settings: %v", err)
	}
}

// applyAppearance makes mygo and every open terminal follow the theme setting.
func (a *App) applyAppearance() {
	switch a.prefs.Theme {
	case "light":
		mygo.Theme.SetSource(mygo.ThemeLight)
	case "dark":
		mygo.Theme.SetSource(mygo.ThemeDark)
	default:
		mygo.Theme.SetSource(mygo.ThemeSystem)
	}
}

func (a *App) terminalTheme() *terminal.Theme {
	if a.prefs.Theme == "light" {
		return terminal.LightTheme()
	}
	if a.prefs.Theme == "dark" {
		return terminal.DarkTheme()
	}
	return nil // follows the window
}

// current returns the active tab, or nil.
func (a *App) current() *Tab {
	if a.active < 0 || a.active >= len(a.tabs) {
		return nil
	}
	return a.tabs[a.active]
}

// newTab appends an empty tab and focuses it.
func (a *App) newTab(title, hostID string) *Tab {
	t := &Tab{
		ID:      fmt.Sprintf("tab-%d", time.Now().UnixNano()),
		HostID:  hostID,
		Title:   title,
		Status:  StatusConnecting,
		Started: time.Now(),
	}
	a.tabs = append(a.tabs, t)
	a.active = len(a.tabs) - 1
	return t
}

// selectTab switches tabs and gives the terminal the keyboard. This is the
// whole of requirement 1: the request waits while the pane is hidden and
// lands the moment it is shown, so the user can type without clicking again.
func (a *App) selectTab(i int) {
	if i < 0 || i >= len(a.tabs) || i == a.active {
		return
	}
	a.active = i
	if t := a.current(); t != nil {
		t.Focus.Focus()
	}
}

// focusTerminal brings the keyboard back to the active terminal, unless a
// dialog or a text field owns it.
func (a *App) focusTerminal() {
	if t := a.current(); t != nil && t.Term != nil {
		t.Focus.Focus()
	}
}

// closeTab closes one terminal and its session.
func (a *App) closeTab(i int) {
	if i < 0 || i >= len(a.tabs) {
		return
	}
	t := a.tabs[i]
	a.tabs = append(a.tabs[:i], a.tabs[i+1:]...)
	if a.active >= len(a.tabs) {
		a.active = len(a.tabs) - 1
	}
	a.active = max(a.active, 0)
	go func() {
		if t.Term != nil {
			_ = t.Term.Close()
		}
		if t.Sess != nil {
			_ = t.Sess.Close()
		}
	}()
	a.focusTerminal()
}

// sessionClosed updates the tab when the remote side hangs up.
func (a *App) sessionClosed(ev sshx.ClosedEvent) {
	for _, t := range a.tabs {
		if t.Sess != nil && t.Sess.ID() == ev.SessionID {
			t.Status = StatusClosed
			t.Err = ev.Reason
			break
		}
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// connectHost dials a saved host into a new tab.
func (a *App) connectHost(hostID string) {
	h, ok := a.hosts.Get(hostID)
	if !ok {
		// --connect accepts a name too, as a person types it.
		for _, candidate := range a.hosts.List() {
			if strings.EqualFold(candidate.Name, hostID) {
				h, ok = candidate, true
				break
			}
		}
	}
	if !ok {
		a.status = "That host is gone."
		return
	}
	hostID = h.ID
	a.selectedHost = hostID
	a.savePrefs()
	a.connect(hostID, h.Name, hoststore.ConnectOverrides{}, nil)
}

func (a *App) connect(hostID, title string, ov hoststore.ConnectOverrides, existing *Tab) {
	t := existing
	if t == nil {
		t = a.newTab(title, hostID)
	}
	t.Status = StatusConnecting
	t.Err = ""
	t.OV = ov
	if h, ok := a.hosts.Get(hostID); ok && h.TwoFactor && t.OV.TwoFactorCode == "" {
		a.askOTP(t, h)
		return
	}
	go a.dial(t)
}

// dial opens the SSH session and, on success, the terminal. Errors that mean
// "the UI must ask something" turn into native prompts and a retry; everything
// else becomes a tab status the user can act on.
func (a *App) dial(t *Tab) {
	cfg, err := a.hosts.SSHConfigWith(t.HostID, 100, 30, t.OV)
	if err != nil {
		a.connectError(t, err)
		return
	}
	t.Cfg = cfg
	sess, err := a.mgr.Connect(cfg)
	if err != nil {
		a.connectError(t, err)
		return
	}
	a.post(func() {
		t.Sess = sess
		t.Status = StatusConnected
		t.Via = viaText(cfg)
		if info := sess.Info(); info.LogPath != "" {
			t.LogPath = info.LogPath
		}
		a.startTerminal(t)
	})
}

// connectError classifies a failure and, where the user can fix it, asks.
func (a *App) connectError(t *Tab, err error) {
	msg := err.Error()
	proxy := strings.Contains(msg, "(proxy)")
	switch {
	case errors.Is(err, hoststore.ErrPasswordRequired):
		a.askSecret(t, promptPassword, proxy)
	case errors.Is(err, sshx.ErrPassphraseRequired):
		a.askSecret(t, promptPassphrase, proxy)
	case isAuthFailure(err) && a.hostUsesPassword(t):
		// A saved password can be wrong, and the SSH stack has no sentinel for
		// that. Ask again, but stop after three refusals instead of looping.
		t.AuthFailures++
		if t.AuthFailures <= 3 {
			a.askSecret(t, promptPassword, proxy)
			if a.prompt != nil {
				a.prompt.Err = authFailureText(t.AuthFailures)
			}
			return
		}
		a.post(func() {
			t.Status = StatusFailed
			t.Err = friendlyError(err)
			a.status = friendlyError(err)
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	default:
		a.post(func() {
			t.Status = StatusFailed
			t.Err = friendlyError(err)
			a.status = friendlyError(err)
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}
}

// hostUsesPassword tells whether another password attempt makes sense: a
// key-only host would just fail the same way.
func (a *App) hostUsesPassword(t *Tab) bool {
	h, ok := a.hosts.Get(t.HostID)
	if !ok {
		return false
	}
	auth := h.AuthType
	if auth == "" {
		auth = t.Cfg.AuthType
	}
	return auth == "password" || h.TwoFactor
}

func friendlyError(err error) string {
	msg := err.Error()
	switch {
	case strings.Contains(msg, "unable to authenticate"):
		return "Authentication failed. Check the password, the key, or the account."
	case strings.Contains(msg, "connection refused"):
		return "The server refused the connection on that port."
	case strings.Contains(msg, "i/o timeout"), strings.Contains(msg, "deadline exceeded"):
		return "The connection timed out. Check the address and any jump host."
	case strings.Contains(msg, "host key"):
		return msg
	default:
		return msg
	}
}

// startTerminal builds the emulator for a connected tab. It runs on the main
// thread because creating a terminal sets up the view-side resources.
func (a *App) startTerminal(t *Tab) {
	conn, err := newSessionConn(t.Sess, encode.Parse(a.prefs.Encoding))
	if err != nil {
		t.Status = StatusFailed
		t.Err = err.Error()
		return
	}
	term, err := terminal.New(terminal.Options{
		Conn:       conn,
		Font:       terminal.Font{Family: a.prefs.FontFamily, Size: float32(a.prefs.FontSize)},
		Scrollback: a.prefs.ScrollbackMB << 20,
		Theme:      a.terminalTheme(),
		CopyOnSelect: func() terminal.CopyOnSelect {
			if a.prefs.CopyOnSelect {
				return terminal.CopyOnSelectClipboard
			}
			return terminal.CopyOnSelectNone
		}(),
		OnTitle: func(title string) {
			a.post(func() {
				if title != "" {
					t.Title = title
					if a.win != nil {
						a.win.Invalidate()
					}
				}
			})
		},
		OnSearch: func(s terminal.SearchStatus) {
			a.post(func() {
				t.SearchTotal = s.Total
				t.SearchCurrent = s.Selected
				t.Searching = s.Searching
				if a.win != nil {
					a.win.Invalidate()
				}
			})
		},
	})
	if err != nil {
		t.Status = StatusFailed
		t.Err = "Terminal engine: " + err.Error()
		return
	}
	t.Term = term
	t.Conn = conn
	t.Focus.Focus()
	if a.win != nil {
		a.win.Invalidate()
	}
}

// sessionConn adapts a session to terminal.Options.Conn: reads are decoded,
// writes go to the remote PTY, and Close ends both.
type sessionConn struct {
	sess  *sshx.Session
	out   io.ReadCloser
	read  io.Reader
	close sync.Once
}

func newSessionConn(s *sshx.Session, enc encode.Name) (*sessionConn, error) {
	out, err := s.AttachOutput()
	if err != nil {
		return nil, err
	}
	return &sessionConn{sess: s, out: out, read: encode.Reader(out, enc)}, nil
}

func (c *sessionConn) Read(p []byte) (int, error) { return c.read.Read(p) }
func (c *sessionConn) Write(p []byte) (int, error) {
	if err := c.sess.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (c *sessionConn) Resize(cols, rows int) error { return c.sess.Resize(cols, rows) }
func (c *sessionConn) Close() error {
	var err error
	c.close.Do(func() {
		_ = c.out.Close()
		err = c.sess.Close()
	})
	return err
}

func viaText(cfg sshx.Config) string {
	if cfg.Proxy == nil {
		return ""
	}
	return cfg.Proxy.Target()
}

// applySettings persists and applies the settings dialog's changes.
func (a *App) applySettings() {
	a.prefs.Clamp()
	a.applyAppearance()
	a.mgr.SetIdleTimeout(sshx.IdleTimeout(a.prefs.IdleTimeoutHours))
	if _, err := a.mgr.ConfigureSessionLogging(a.prefs.SessionLogging, a.logDir); err != nil {
		a.status = "Session logging: " + err.Error()
	}
	theme := a.terminalTheme()
	for _, t := range a.tabs {
		if t.Term == nil {
			continue
		}
		t.Term.SetFont(terminal.Font{Family: a.prefs.FontFamily, Size: float32(a.prefs.FontSize)})
		if theme != nil {
			t.Term.SetTheme(theme, theme)
		}
		if a.prefs.CopyOnSelect {
			t.Term.SetCopyOnSelect(terminal.CopyOnSelectClipboard)
		} else {
			t.Term.SetCopyOnSelect(terminal.CopyOnSelectNone)
		}
	}
	if err := a.prefs.Save(a.configPath); err != nil {
		a.status = "Could not save settings: " + err.Error()
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// reloadHosts refreshes the sidebar after a host change.
func (a *App) reloadHosts() {
	if a.win != nil {
		a.win.Invalidate()
	}
}

// hostGroups returns the host list grouped by the host's Group field, with
// the empty group last as "Ungrouped".
func (a *App) hostGroups() map[string][]hoststore.Host {
	out := map[string][]hoststore.Host{}
	for _, h := range a.hosts.List() {
		if a.query != "" {
			q := strings.ToLower(a.query)
			if !strings.Contains(strings.ToLower(h.Name), q) &&
				!strings.Contains(strings.ToLower(h.Host), q) &&
				!strings.Contains(strings.ToLower(h.User), q) {
				continue
			}
		}
		out[h.Group] = append(out[h.Group], h)
	}
	return out
}

// platformName is the status bar's platform column.
func platformName() string {
	switch runtime.GOOS {
	case "windows":
		return "Windows"
	case "darwin":
		return "macOS"
	default:
		return runtime.GOOS
	}
}

// secretBackend names the credential store honestly.
func (a *App) secretBackend() string {
	if a.secs == nil {
		return "none"
	}
	return string(a.secs.Backend())
}

// openURL opens a link with the system's browser.
func (a *App) openURL(url string) {
	if err := mygo.Shell.OpenExternal(url); err != nil {
		a.status = "Could not open link: " + err.Error()
	}
}

// exitDir is where the user's data lives, shown in settings.
func (a *App) exitDir() string { return a.dir }

func (a *App) pickFile(title string, filters []mygo.FileFilter) (string, error) {
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:  a.win,
		Title:   title,
		Filters: filters,
	})
	if err != nil || len(paths) == 0 {
		return "", err
	}
	return paths[0], nil
}

func (a *App) saveFile(title, defaultName string, filters []mygo.FileFilter) (string, error) {
	return mygo.Dialog.Save(mygo.SaveDialogOptions{
		Parent:      a.win,
		Title:       title,
		DefaultPath: defaultName,
		Filters:     filters,
	})
}
