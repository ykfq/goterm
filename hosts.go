package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"

	"github.com/ykfq/goterm/internal/hoststore"
)

var (
	authLabels = []string{"Password", "Private key", "ssh-agent"}
	authValues = []string{"password", "key", "agent"}
)

// HostEditor is the state behind the new/edit host dialog. Ports are numbers
// because the dialog offers a numeric input; the fields stay strings so a
// half-typed name never gets lost.
type HostEditor struct {
	Open    bool
	ID      string
	Editing bool

	Name      string
	Group     string
	Host      string
	Port      float64
	User      string
	AuthIdx   int
	KeyPath   string
	Password  string
	Remember  bool
	TwoFactor bool
	Notes     string

	ProxyEnabled  bool
	ProxyHost     string
	ProxyPort     float64
	ProxyUser     string
	ProxyAuthIdx  int
	ProxyKeyPath  string
	ProxyPassword string

	Err string
}

func (a *App) openNewHost() {
	a.editor = HostEditor{
		Open:      true,
		Port:      22,
		ProxyPort: 22,
		Remember:  true,
	}
	a.showHostEditor = true
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) openEditHost(id string) {
	h, ok := a.hosts.Get(id)
	if !ok {
		return
	}
	e := HostEditor{
		Open:      true,
		ID:        h.ID,
		Editing:   true,
		Name:      h.Name,
		Group:     h.Group,
		Host:      h.Host,
		Port:      float64(h.Port),
		User:      h.User,
		AuthIdx:   authIndex(h.AuthType),
		KeyPath:   h.KeyPath,
		TwoFactor: h.TwoFactor,
		Notes:     h.Notes,
	}
	if h.ProxyHost != "" {
		e.ProxyEnabled = true
		e.ProxyHost = h.ProxyHost
		e.ProxyPort = float64(h.ProxyPort)
		e.ProxyUser = h.ProxyUser
		e.ProxyAuthIdx = authIndex(h.ProxyAuthType)
		e.ProxyKeyPath = h.ProxyKeyPath
	}
	if h.HasSecret {
		e.Remember = true
	}
	a.editor = e
	a.showHostEditor = true
	if a.win != nil {
		a.win.Invalidate()
	}
}

func authIndex(v string) int {
	for i, s := range authValues {
		if s == v {
			return i
		}
	}
	return 0
}

func (a *App) saveHostEditor() {
	e := &a.editor
	e.Err = ""
	e.Name = strings.TrimSpace(e.Name)
	e.Host = strings.TrimSpace(e.Host)
	e.User = strings.TrimSpace(e.User)
	e.Group = strings.TrimSpace(e.Group)
	if e.Host == "" {
		e.Err = "Host is required."
		return
	}
	if e.User == "" {
		e.Err = "User is required."
		return
	}
	if e.Name == "" {
		e.Name = e.Host
	}
	port := int(e.Port)
	if port <= 0 || port > 65535 {
		e.Err = "Port must be between 1 and 65535."
		return
	}
	h := hoststore.Host{
		ID:        e.ID,
		Name:      e.Name,
		Group:     e.Group,
		Host:      e.Host,
		Port:      port,
		User:      e.User,
		AuthType:  authValues[e.AuthIdx],
		KeyPath:   strings.TrimSpace(e.KeyPath),
		Notes:     e.Notes,
		TwoFactor: e.TwoFactor,
	}
	if e.ProxyEnabled {
		pp := int(e.ProxyPort)
		if pp <= 0 || pp > 65535 {
			e.Err = "Jump host port must be between 1 and 65535."
			return
		}
		if strings.TrimSpace(e.ProxyHost) == "" {
			e.Err = "Jump host needs an address."
			return
		}
		h.ProxyHost = strings.TrimSpace(e.ProxyHost)
		h.ProxyPort = pp
		h.ProxyUser = strings.TrimSpace(e.ProxyUser)
		h.ProxyAuthType = authValues[e.ProxyAuthIdx]
		h.ProxyKeyPath = strings.TrimSpace(e.ProxyKeyPath)
	}
	saved, err := a.hosts.Save(h)
	if err != nil {
		e.Err = err.Error()
		return
	}
	if pw := e.Password; pw != "" && e.Remember {
		if err := a.hosts.SetSecret(saved.ID, pw); err != nil {
			e.Err = "Host saved, but the credential store refused the password: " + err.Error()
			return
		}
	}
	if pw := e.ProxyPassword; pw != "" && e.Remember && e.ProxyEnabled {
		if err := a.hosts.SetProxySecret(saved.ID, pw); err != nil {
			e.Err = "Host saved, but the credential store refused the jump host password: " + err.Error()
			return
		}
	}
	e.Open = false
	a.showHostEditor = false
	a.selectedHost = saved.ID
	a.prefs.LastHostID = saved.ID
	a.savePrefs()
	a.reloadHosts()
}

func (a *App) deleteHost(id string) {
	h, ok := a.hosts.Get(id)
	if !ok {
		return
	}
	a.confirm = &Confirm{
		Open:   true,
		Title:  "Delete " + h.Name + "?",
		Body:   "This removes the saved host and its password. Open tabs stay connected.",
		OK:     "Delete",
		Danger: true,
		Run: func() {
			if err := a.hosts.Delete(id); err != nil {
				a.status = "Could not delete the host: " + err.Error()
				return
			}
			if a.selectedHost == id {
				a.selectedHost = ""
			}
			a.reloadHosts()
		},
	}
}

// hostSummary is the sidebar's second line.
func hostSummary(h hoststore.Host) string {
	port := h.Port
	if port == 0 {
		port = 22
	}
	s := fmt.Sprintf("%s@%s", h.User, h.Host)
	if port != 22 {
		s += fmt.Sprintf(":%d", port)
	}
	if h.ProxyHost != "" {
		s += "  via " + h.ProxyHost
	}
	return s
}

// pickKeyInto opens a native file dialog and writes the choice back into the
// editor, on the main thread.
func (a *App) pickKeyInto(proxy bool) {
	go func() {
		path, err := a.pickFile("Choose a private key", []mygo.FileFilter{{
			Name:       "SSH keys",
			Extensions: []string{"pem", "key", "ppk", "pub"},
		}, {Name: "All files", Extensions: []string{"*"}}})
		if err != nil || path == "" {
			return
		}
		a.post(func() {
			if proxy {
				a.editor.ProxyKeyPath = path
			} else {
				a.editor.KeyPath = path
			}
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}()
}
