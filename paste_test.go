package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/egoist/mygo/transfer"
	"github.com/egoist/mygo/ui"
)

// The paste path: what reaches the program, and when the user is asked.

func TestStripPasteControlRemovesEscapeSequences(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain text; with semicolons", "plain text; with semicolons"},
		// SGR from copied coloured output: the reported trailing garbage was
		// the parameters of sequences like this, echoed by the shell.
		{"\x1b[1;32mok\x1b[0m done", "ok done"},
		{"tail\x1b[38;5;214m", "tail"},
		// Cursor movement, erase, and a title change.
		{"a\x1b[2Jb\x1b[12;40Hc", "abc"},
		{"\x1b]0;window title\x07visible", "visible"},
		{"\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\", "link"},
		// Charset designators are three bytes.
		{"\x1b(Bascii", "ascii"},
		// Tabs and line breaks survive: they are the program's business.
		{"a\tb\r\nc\nd", "a\tb\r\nc\nd"},
		// A sequence cut off at the end is dropped, not pasted half-formed.
		{"text\x1b[1;3", "text"},
		{"text\x1b]0;unterminated", "text"},
		// Other control characters go; multi-byte text stays byte for byte.
		{"a\x00\x07b\x7fc 内存不足", "abc 内存不足"},
	}
	for _, tc := range cases {
		if got := stripPasteControl(tc.in); got != tc.want {
			t.Errorf("stripPasteControl(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestCleanSingleLinePasteTakesTheTerminalsOwnPath(t *testing.T) {
	a := &App{}
	tab := &Tab{}
	if took := a.pasteText(tab, transfer.TextData("ls -l")); took {
		t.Fatal("a clean single line was taken; it needs no handling")
	}
	if a.confirm != nil {
		t.Fatal("a dialog appeared for a clean single line")
	}
}

func TestMultiLinePasteAsksFirst(t *testing.T) {
	a := &App{}
	tab := &Tab{}
	if took := a.pasteText(tab, transfer.TextData("one\ntwo\nthree")); !took {
		t.Fatal("a multi-line paste went straight to the program")
	}
	if a.pasteAsk == nil || !a.pasteAsk.Open {
		t.Fatal("no confirmation for a multi-line paste")
	}
	// The dialog gets the whole text. It used to carry a preview of the first
	// line, which asked the user to approve three lines while showing one.
	if a.pasteAsk.Text != "one\ntwo\nthree" {
		t.Fatalf("the dialog holds %q, want the whole paste", a.pasteAsk.Text)
	}
	if !a.pasteAsk.TrimEnd {
		t.Fatal("trailing whitespace is left in by default")
	}
}

func TestTrimPasteEnd(t *testing.T) {
	cases := []struct{ in, want string }{
		{"ls -l", "ls -l"},
		// The paste that runs one more empty command than the user meant.
		{"ls -l\n", "ls -l"},
		{"ls -l\r\n\r\n", "ls -l"},
		{"echo hi   ", "echo hi"},
		{"echo hi\t \n\n   \n", "echo hi"},
		// Inner blank lines and leading text are what was copied: they stay.
		{"first\n\nsecond", "first\r\rsecond"},
		{"   indented\n", "   indented"},
		// The spaces people actually paste: no-break and full width.
		{"end\u00a0\u3000", "end"},
		{"\n\n", ""},
		// A space copied in front of every break lands in the command.
		{"make -j4   \nmake test\t\n", "make -j4\rmake test"},
	}
	for _, tc := range cases {
		if got := trimPasteEnd(tc.in); got != tc.want {
			t.Errorf("trimPasteEnd(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The bug the user hit: a Windows clipboard carries CRLF, and sending it as it
// is runs two lines per line — the CR ends the command and the LF ends an empty
// one, so every line gets a blank line after it.
func TestPasteLineEndingsMakeOneLinePerLine(t *testing.T) {
	cases := []struct{ in, want string }{
		{"one", "one"},
		{"one\ntwo", "one\rtwo"},
		{"one\r\ntwo", "one\rtwo"},
		{"one\r\r\ntwo", "one\r\rtwo"}, // a blank line the user copied stays
		{"one\rtwo", "one\rtwo"},
		{"one\n\ntwo\r\nthree\n", "one\r\rtwo\rthree\r"},
	}
	for _, tc := range cases {
		if got := pasteLineEndings(tc.in); got != tc.want {
			t.Errorf("pasteLineEndings(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// What the dialog actually sends, both ways the checkbox can be set.
func TestPasteSendsWhatTheDialogPromises(t *testing.T) {
	const crlf = "hostname\r\ncd /tmp  \r\nls\r\n\r\n"
	if got, want := pasteSends(crlf, true), "hostname\rcd /tmp\rls"; got != want {
		t.Errorf("trimmed: %q, want %q", got, want)
	}
	// Untrimmed keeps the blank line the user pasted, and still sends one
	// break per line rather than two.
	if got, want := pasteSends(crlf, false), "hostname\rcd /tmp  \rls\r\r"; got != want {
		t.Errorf("untrimmed: %q, want %q", got, want)
	}
}

// TestPasteDialogEditsAndPastesOnEnter drives the dialog the way a keyboard
// user does: no mouse, Enter, done. The text box is editable and holds the whole
// paste, the button holds the keyboard, and the trailing blank line does not
// reach the shell.
func TestPasteDialogEditsAndPastesOnEnter(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()

	got := make(chan string, 1)
	app.pasteAsk = &PasteAsk{
		Open:    true,
		Text:    "make deploy\nssh host\t",
		TrimEnd: true,
		Run: func(text string) {
			got <- text
		},
	}

	tt := ui.NewTester(app.view, 1100, 760)
	tt.Frame()

	field, ok := tt.Access("Paste text")
	if !ok {
		t.Fatalf("no editable text area in the paste dialog: %q", tt.Texts())
	}
	if !field.Multiline {
		t.Fatal("the paste box is not multiline")
	}
	if !strings.Contains(field.Value, "ssh host") {
		t.Fatalf("the text area holds %q, want the whole paste", field.Value)
	}
	if !tt.Focused("Paste") {
		t.Fatal("Enter would not paste: the Paste button does not hold the keyboard")
	}

	tt.KeyDown(ui.KeyEnter)
	tt.Frame()
	select {
	case text := <-got:
		// One CR per line — the byte a terminal reads as Enter — never CRLF,
		// which runs an empty line between every two.
		if text != "make deploy\rssh host" {
			t.Fatalf("pasted %q, want the lines separated by CR with the trailing tab trimmed", text)
		}
	default:
		t.Fatal("Enter did not paste")
	}
	if app.pasteAsk != nil {
		t.Fatal("the dialog stayed open after the paste")
	}
}

func TestEscapeOnlyPasteSendsNothing(t *testing.T) {
	a := &App{}
	tab := &Tab{}
	if took := a.pasteText(tab, transfer.TextData("\x1b[0m\x1b[2J")); !took {
		t.Fatal("an escape-only paste was left to the terminal")
	}
	if a.confirm != nil || a.pasteAsk != nil {
		t.Fatal("an escape-only paste asked a question")
	}
}

// The sidebar folding away must not move the window's chrome: the tab strip
// stays at the top and the status bar at the bottom, sidebar or no sidebar.
func TestCollapsedSidebarKeepsChromeAtTheEdges(t *testing.T) {
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	defer app.Close()
	app.tabs = []*Tab{{ID: "t1", Title: "deploy", Status: StatusConnecting}}
	app.active = 0

	statusText := fmt.Sprintf("GoTerm %s · %s · %s", Version, platformName(), app.secretBackend())
	const height = 800

	for _, hidden := range []bool{false, true} {
		app.sidebarHidden = hidden
		tt := ui.NewTester(app.view, 1200, height)
		tt.Frame()
		tab, ok := tt.Find("deploy")
		if !ok {
			t.Fatalf("hidden=%v: the tab strip did not render: %q", hidden, tt.Texts())
		}
		if tab.Y > 80 {
			t.Errorf("hidden=%v: the tab strip sits at y=%.0f, not at the top", hidden, tab.Y)
		}
		// The strip starts on the sidebar's right edge, so it lines up with the
		// terminal below it instead of beginning at the window's left edge. The
		// chip's own box is the measure; its label sits inside its padding.
		chip, ok := tt.Find("Tab deploy")
		if !ok {
			t.Fatalf("hidden=%v: no tab chip to measure", hidden)
		}
		if !hidden {
			want := app.prefs.SidebarWidth
			if chip.X < want-4 || chip.X > want+16 {
				t.Errorf("hidden=%v: the tab strip starts at x=%.0f, want about %.0f (the sidebar's edge)", hidden, chip.X, want)
			}
		}
		if hidden && chip.X < railWidth {
			t.Errorf("with the sidebar folded away the tab strip starts at x=%.0f, over the rail", chip.X)
		}
		status, ok := tt.Find(statusText)
		if !ok {
			t.Fatalf("hidden=%v: the status bar did not render: %q", hidden, tt.Texts())
		}
		if status.Y+status.H < height-60 {
			t.Errorf("hidden=%v: the status bar sits at y=%.0f of %d, not at the bottom", hidden, status.Y, height)
		}
	}
}
