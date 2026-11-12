package main

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/hoststore"
)

// Two-factor logins: the code is typed for every connect, never stored, and a
// reconnect must ask again instead of dialing with the expired one.

func twoFactorHost(t *testing.T, app *App) hoststore.Host {
	t.Helper()
	h, err := app.hosts.Save(hoststore.Host{
		Name: "bastion", Host: "127.0.0.1", Port: 22, User: "tester",
		AuthType: hoststore.AuthPassword, TwoFactor: true,
	})
	if err != nil {
		t.Fatalf("save host: %v", err)
	}
	if err := app.hosts.SetSecret(h.ID, "saved-password"); err != nil {
		t.Fatalf("save secret: %v", err)
	}
	return h
}

// A reconnect on a two-factor host must not carry the previous login's code:
// it lived in a 30-second window and the server would refuse it, which is the
// "reconnect just errors" report. The code is dropped, and the prompt that
// asks for a fresh one opens.
func TestReconnectOnATwoFactorHostAsksForAFreshCode(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h := twoFactorHost(t, app)

	tab := &Tab{ID: "t1", Title: "bastion", HostID: h.ID, Status: StatusConnected,
		OV: hoststore.ConnectOverrides{TwoFactorCode: "123456"}}
	app.tabs = []*Tab{tab}
	app.active = 0

	app.reconnect(tab)
	if tab.OV.TwoFactorCode != "" {
		t.Fatalf("the reconnect kept the expired code %q", tab.OV.TwoFactorCode)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if app.prompt != nil && app.prompt.Kind == promptOTP {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("no one-time code prompt after the reconnect (status %q, err %q)", tab.Status, tab.Err)
}

// The missing code is its own sentinel, so the UI can ask instead of showing
// the generic failure.
func TestTwoFactorIsItsOwnSentinel(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	h := twoFactorHost(t, app)

	_, err = app.hosts.SSHConfigWith(h.ID, 80, 24, hoststore.ConnectOverrides{})
	if err == nil {
		t.Fatal("a two-factor host configured without a code")
	}
	if !strings.Contains(err.Error(), "two-factor") {
		t.Fatalf("error does not name the second factor: %v", err)
	}
	if !errors.Is(err, hoststore.ErrTwoFactorRequired) {
		t.Fatalf("error is not ErrTwoFactorRequired: %v", err)
	}
}

func TestDigitsOnlyKeepsCodesFromPastes(t *testing.T) {
	cases := map[string]string{
		"123456":           "123456",
		" 123 456 ":        "123456",
		"123-456":          "123456",
		"code: 123456":     "123456",
		"12345678":         "12345678",
		"１２３４５６":           "",           // full-width digits are not what a TOTP generates
		"12a34b56c78d90":   "1234567890", // the field's cap trims, not this
		"\x1b[1;32m123456": "123456",
	}
	for in, want := range cases {
		if got := digitsOnly(in); got != want {
			t.Errorf("digitsOnly(%q) = %q, want %q", in, got, want)
		}
	}
}

// The field itself refuses letters and stops at eight digits, which is what the
// user meets when they type or paste a code.
func TestOneTimeCodeFieldTakesEightDigitsAtMost(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.prompt = &Prompt{Open: true, Kind: promptOTP, Title: "One-time code", Hint: "test"}

	tt := ui.NewTester(app.view, 900, 600)
	tt.Frame()
	tt.Type("ab12cd34567890")
	tt.Frame()
	if got := app.prompt.Value; got != "12345678" {
		t.Fatalf("field holds %q, want 12345678", got)
	}
}
