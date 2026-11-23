package main

import (
	"net"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/sshtest"
	"github.com/ykfq/goterm/internal/sshx"
)

// sftpSessionApp is an app with one tab connected to a local SSH server whose
// sftp subsystem is rooted at a fresh directory — the shape a real session
// takes, so the list, the panel and an upload all run against a server.
func sftpSessionApp(t *testing.T) (*App, *Tab, string) {
	t.Helper()
	root := t.TempDir()
	srv, err := sshtest.New(sshtest.Options{User: "tester", Password: "files-pass", SFTPRoot: root})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })
	host, portStr, err := net.SplitHostPort(srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}
	cfg := sshx.Config{Host: host, Port: port, User: "tester", Password: "files-pass"}

	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.win = nil

	sess, err := app.mgr.Connect(cfg)
	if err != nil {
		t.Fatal(err)
	}
	tab := &Tab{ID: "sftp-tab", Title: "web", Status: StatusConnected, Sess: sess, Cfg: cfg}
	app.tabs = []*Tab{tab}
	app.active = 0
	return app, tab, root
}

// The list lives where a file browser is looked for: the sidebar, bottom left.
// It names the sessions, and a double click puts that session's files beside its
// terminal.
func TestTheSidebarListsSessionsAndADoubleClickOpensTheFiles(t *testing.T) {
	app, tab, _ := sftpSessionApp(t)
	tt := ui.NewTester(app.view, 1200, 820)
	tt.Frame()

	if !tt.HasText("SFTP sessions") {
		t.Fatalf("no SFTP section in the sidebar: %q", tt.Texts())
	}
	if !tt.HasText("web") {
		t.Fatalf("the session is not listed: %q", tt.Texts())
	}

	row, ok := tt.Find("SFTP session web")
	if !ok {
		t.Fatalf("the session row has no box: %q", tt.Texts())
	}
	// Two presses at one place and one moment are a double click.
	cx, cy := row.X+row.W/2, row.Y+row.H/2
	tt.ClickAt(cx, cy)
	tt.ClickAt(cx, cy)
	tt.Frame()

	if tab.SFTP == nil || !tab.SFTP.Open {
		t.Fatal("a double click on the session did not open its file panel")
	}
	if !tt.HasText("Files") {
		t.Fatalf("the panel is not beside the terminal: %q", tt.Texts())
	}
	if app.active != 0 {
		t.Fatalf("the click left tab %d active, want the session's own", app.active)
	}
}

// A session that has ended cannot browse. Its row says so — the panel may still
// be worth looking at — and opening files from it explains rather than
// pretending.
func TestASessionThatEndedSaysSoInsteadOfBrowsing(t *testing.T) {
	app, tab, _ := sftpSessionApp(t)
	tab.SFTP = &SFTPPanel{Open: true, Path: "/"}
	tab.Sess = nil
	tab.Status = StatusClosed

	tt := ui.NewTester(app.view, 1200, 820)
	tt.Frame()
	if !tt.HasText("Closed — reconnect to browse") {
		t.Fatalf("the closed session does not say what to do: %q", tt.Texts())
	}

	// And a fresh open attempt reports instead of opening an empty panel.
	tab.SFTP.Open = false
	app.openSFTP(tab)
	if tab.SFTP.Open {
		t.Fatal("a closed session opened a panel")
	}
	if app.status == "" {
		t.Fatal("opening a closed session said nothing")
	}
}

// With nothing to browse the section is still there, saying how to get a row.
func TestTheSFTPListSaysHowToFillIt(t *testing.T) {
	app, tab, _ := sftpSessionApp(t)
	tab.Sess = nil
	tab.Status = StatusClosed

	tt := ui.NewTester(app.view, 1200, 820)
	tt.Frame()
	if !tt.HasText("SFTP sessions") {
		t.Fatalf("the section is missing when empty: %q", tt.Texts())
	}
	if !tt.HasText("Connect a host, then double-click it here") {
		t.Fatalf("the empty list does not say what to do: %q", tt.Texts())
	}
}

// Dropping local files on the panel uploads them to the folder it is showing —
// the same thing a drop on the terminal does, but onto the browser itself.
func TestDroppingFilesOnThePanelUploadsThem(t *testing.T) {
	app, tab, root := sftpSessionApp(t)

	// List from the server directly, so the panel opens on a folder that is
	// known before the first frame: the drop then targets a real path rather
	// than wherever the async listing had got to.
	listing, err := app.mgr.SFTPList(tab.Sess.ID(), "")
	if err != nil {
		t.Fatal(err)
	}
	tab.SFTP = &SFTPPanel{Open: true, Path: listing.Path, Entries: listing.Entries}

	local := filepath.Join(t.TempDir(), "report.txt")
	if err := os.WriteFile(local, []byte("hello from the drop"), 0o644); err != nil {
		t.Fatal(err)
	}

	tt := ui.NewTester(app.view, 1200, 820)
	tt.Frame()
	panel, ok := tt.Find("Files")
	if !ok {
		t.Fatalf("the panel is not on screen: %q", tt.Texts())
	}
	if taken := tt.DropFiles(panel.X+panel.W/2, panel.Y+90, local); !taken {
		t.Fatal("the panel refused a drop of local files")
	}

	deadline := time.Now().Add(10 * time.Second)
	for {
		if got, err := os.ReadFile(filepath.Join(root, "report.txt")); err == nil {
			if string(got) != "hello from the drop" {
				t.Fatalf("the file arrived as %q", got)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the dropped file never reached the server: %v", err)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// The panel's width is the splitter's, and it cannot eat the terminal: a
// quarter of the window at least, half at most.
func TestTheFilePanelLeavesTheTerminalItsShare(t *testing.T) {
	app, tab, _ := sftpSessionApp(t)

	// The clamp reads the window's size from the view context, so it runs in
	// one: a frame of the tester is the cheapest way to have a real context.
	var width float32
	tt := ui.NewTester(func(c *ui.Context) {
		app.clampFilesWidth(tab, c)
		width = tab.FilesWidth
		ui.Text(c, "x")
	}, 1000, 700)

	tab.FilesWidth = 0
	tt.Frame()
	if width != float32(defaultFilesWidth) {
		t.Fatalf("a fresh panel opens at %v, want %v", width, defaultFilesWidth)
	}
	tab.FilesWidth = 5000
	tt.Frame()
	if width > 500 {
		t.Fatalf("the panel takes %v of a 1000-wide window; the terminal needs half", width)
	}
	tab.FilesWidth = 10
	tt.Frame()
	if width < 240 {
		t.Fatalf("the panel shrank to %v; it needs room for a listing", width)
	}
}
