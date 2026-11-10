package main

import (
	"strings"

	"github.com/ykfq/goterm/internal/hoststore"
)

// promptKind says what the modal is asking for. All four are things the SSH
// stack refuses to guess: it returns a sentinel error and the UI asks.
type promptKind int

const (
	promptPassword promptKind = iota
	promptPassphrase
	promptOTP
)

// Prompt is the state of a native "we need one secret" dialog.
type Prompt struct {
	Open         bool
	Kind         promptKind
	Tab          *Tab
	Proxy        bool
	Title        string
	Hint         string
	Value        string
	Err          string
	Remember     bool
	Rememberable bool
}

// Confirm is a two-button question (delete a host, discard a tab).
type Confirm struct {
	Open   bool
	Title  string
	Body   string
	OK     string
	Danger bool
	Run    func()
}

func (a *App) askSecret(t *Tab, kind promptKind, proxy bool) {
	target := t.Cfg.Target()
	if target == "" && t.HostID != "" {
		if h, ok := a.hosts.Get(t.HostID); ok {
			target = h.User + "@" + h.Host
		}
	}
	what := "Password"
	if kind == promptPassphrase {
		what = "Key passphrase"
	}
	where := target
	if proxy {
		what = "Jump host password"
		if kind == promptPassphrase {
			what = "Jump host key passphrase"
		}
		if t.Cfg.Proxy != nil {
			where = t.Cfg.Proxy.Target()
		}
	}
	a.prompt = &Prompt{
		Open:         true,
		Kind:         kind,
		Tab:          t,
		Proxy:        proxy,
		Title:        what,
		Hint:         where + " — not stored on disk unless you ask.",
		Rememberable: true,
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) askOTP(t *Tab, h hoststore.Host) {
	a.prompt = &Prompt{
		Open:  true,
		Kind:  promptOTP,
		Tab:   t,
		Title: "One-time code",
		Hint:  h.User + "@" + h.Host + " asks for a second factor (dynamic code).",
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// submitPrompt applies the answer and retries the connection.
func (a *App) submitPrompt() {
	p := a.prompt
	if p == nil || !p.Open {
		return
	}
	value := strings.TrimSpace(p.Value)
	if value == "" {
		p.Err = "Enter a value first."
		return
	}
	t := p.Tab
	if t == nil {
		a.prompt = nil
		return
	}
	switch p.Kind {
	case promptPassword:
		if p.Proxy {
			t.OV.ProxyPassword = value
			if p.Remember {
				if err := a.hosts.SetProxySecret(t.HostID, value); err != nil {
					p.Err = "Could not save to the credential store: " + err.Error()
					return
				}
			}
		} else {
			t.OV.Password = value
			if p.Remember {
				if err := a.hosts.SetSecret(t.HostID, value); err != nil {
					p.Err = "Could not save to the credential store: " + err.Error()
					return
				}
			}
		}
	case promptPassphrase:
		if p.Proxy {
			t.OV.ProxyPassphrase = value
		} else {
			t.OV.Passphrase = value
		}
	case promptOTP:
		t.OV.TwoFactorCode = value
	}
	p.Open = false
	p.Value = ""
	a.prompt = nil
	t.AuthFailures = 0
	go a.dial(t)
}

func (a *App) cancelPrompt() {
	if p := a.prompt; p != nil {
		if t := p.Tab; t != nil && t.Status == StatusConnecting {
			t.Status = StatusFailed
			t.Err = "Cancelled."
		}
	}
	a.prompt = nil
	a.focusTerminal()
}

func indexOfTab(tabs []*Tab, want *Tab) int {
	for i, t := range tabs {
		if t == want {
			return i
		}
	}
	return -1
}

// authFailureText is what the password prompt shows after a rejected retry.
func authFailureText(attempts int) string {
	if attempts >= 3 {
		return "That was refused again. Check the account, the key, or the network."
	}
	return "That was refused. Try again."
}

// isAuthFailure recognizes the SSH stack's "wrong password" outcome, which
// has no sentinel error of its own.
func isAuthFailure(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "unable to authenticate") ||
		strings.Contains(msg, "no supported methods remain")
}
