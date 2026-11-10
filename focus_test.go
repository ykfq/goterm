package main

import (
	"io"
	"sync"
	"testing"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"
)

// idleConn is a terminal Conn with nothing to say: enough to build a real
// terminal view without a program.
type idleConn struct {
	once   sync.Once
	closed chan struct{}
}

func newIdleConn() *idleConn { return &idleConn{closed: make(chan struct{})} }

func (c *idleConn) Read(p []byte) (int, error) {
	<-c.closed
	return 0, io.EOF
}
func (c *idleConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *idleConn) Close() error                { c.once.Do(func() { close(c.closed) }); return nil }
func (c *idleConn) Resize(cols, rows int) error { return nil }

// TestSwitchingTabsPutsTheKeyboardInTheTerminal is requirement 1 from the
// migration: changing tabs must hand the keyboard to the new terminal, so the
// user can type without clicking the pane first.
func TestSwitchingTabsPutsTheKeyboardInTheTerminal(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.win = nil

	mkTab := func(title string) *Tab {
		term, err := terminal.New(terminal.Options{Conn: newIdleConn()})
		if err != nil {
			t.Skipf("terminal engine unavailable: %v", err)
		}
		return &Tab{ID: "id-" + title, Title: title, Status: StatusConnected, Term: term}
	}
	first, second := mkTab("one"), mkTab("two")
	t.Cleanup(func() {
		_ = first.Term.Close()
		_ = second.Term.Close()
	})
	app.tabs = []*Tab{first, second}
	app.active = 0

	tt := ui.NewTester(app.view, 1000, 700)
	first.Focus.Focus()
	tt.Frame()
	if !tt.Focused("Terminal one") {
		t.Fatal("the first terminal did not take the keyboard")
	}

	if err := tt.Click("Tab two"); err != nil {
		t.Fatalf("click tab two: %v", err)
	}
	tt.Frame()
	if app.active != 1 {
		t.Fatalf("active tab is %d, want 1", app.active)
	}
	if !tt.Focused("Terminal two") {
		t.Fatal("switching tabs left the keyboard outside the terminal")
	}

	// Going back must hand it back, not leave it nowhere.
	if err := tt.Click("Tab one"); err != nil {
		t.Fatalf("click tab one: %v", err)
	}
	tt.Frame()
	if !tt.Focused("Terminal one") {
		t.Fatal("switching back did not return the keyboard to the first terminal")
	}
}

// TestFocusRequestWaitsForAHiddenTerminal covers the ordering that made the
// 1.x bug subtle: the tab is switched before its pane is laid out. Focus must
// be remembered, not dropped.
func TestFocusRequestWaitsForAHiddenTerminal(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	term, err := terminal.New(terminal.Options{Conn: newIdleConn()})
	if err != nil {
		t.Skipf("terminal engine unavailable: %v", err)
	}
	defer term.Close()
	tab := &Tab{ID: "only", Title: "only", Status: StatusConnected, Term: term}
	app.tabs = []*Tab{tab}
	app.active = 0

	tt := ui.NewTester(app.view, 900, 600)
	// Ask before the view ever built the terminal: the request must survive.
	tab.Focus.Focus()
	tt.Frame()
	if !tt.Focused("Terminal only") {
		t.Fatal("a focus request made before the pane existed was dropped")
	}
}
