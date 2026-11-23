package main

import (
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/settings"
	"github.com/ykfq/goterm/internal/testutil"
)

// newSettingsApp opens the app with an empty data directory and the settings
// dialog on the Behaviour page, where the keyword field lives.
func newSettingsApp(t *testing.T, width, height int) (*App, *ui.Tester) {
	t.Helper()
	t.Setenv("GOTERM_DIR", testutil.TempDir(t))
	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(app.Close)
	app.win = nil
	// The data directory is resolved once per process (appdir caches it), so a
	// settings.json an earlier test wrote is loaded here. Start from an empty
	// field instead of inheriting it.
	app.prefs.HighlightWords = nil
	app.prefs.SettingsWidth = settings.DefaultSettingsWidth
	app.prefs.SettingsHeight = settings.DefaultSettingsHeight
	app.settingsPage = 1
	app.openSettings()
	tt := ui.NewTester(app.view, width, height)
	tt.Frame()
	return app, tt
}

// Enter has to end a line in the keyword field. It did not: the field's text
// was rebuilt from the parsed words on every frame, so the newline was gone
// before the next keystroke arrived.
func TestEnterAddsAKeywordRuleLine(t *testing.T) {
	app, tt := newSettingsApp(t, 1100, 780)

	if err := tt.Click("Highlight keywords"); err != nil {
		t.Fatalf("the keyword field is not reachable: %v", err)
	}
	tt.Type("RED: error|fatal")
	tt.Key(0, ui.KeyEnter)
	tt.Type("GREEN: info")
	tt.Frame()

	if want := "RED: error|fatal\nGREEN: info"; app.highlightText != want {
		t.Fatalf("field text is %q, want %q", app.highlightText, want)
	}
	if got := strings.Join(app.prefs.HighlightWords, " / "); got != "RED: error|fatal / GREEN: info" {
		t.Fatalf("stored rules are %q", got)
	}
}

// A rule typed with a colour reaches the terminals: the words are painted in
// that colour, not the default one.
func TestAColouredRuleReachesTheSavedSettings(t *testing.T) {
	app, tt := newSettingsApp(t, 1100, 780)
	if err := tt.Click("Highlight keywords"); err != nil {
		t.Fatalf("the keyword field is not reachable: %v", err)
	}
	tt.Type("GREEN: healthy")
	tt.Frame()

	rules := settings.ParseWordRules(strings.Join(app.prefs.HighlightWords, "\n"))
	if len(rules) != 1 || rules[0].Colour != "GREEN" || rules[0].Words[0] != "healthy" {
		t.Fatalf("saved %+v", rules)
	}
}

// A typo in a colour name used to fail silently. The dialog says so.
func TestAnUnknownColourIsCalledOutInTheDialog(t *testing.T) {
	app, tt := newSettingsApp(t, 1100, 780)
	app.highlightText = "GRREN: error"
	tt.Frame()
	if !tt.HasText("Not a colour") {
		t.Fatalf("no warning for the typo: %q", tt.Texts())
	}
	if !tt.HasText("GRREN") {
		t.Fatalf("the warning does not name the token: %q", tt.Texts())
	}
	app.highlightText = "GREEN: error"
	tt.Frame()
	if tt.HasText("Not a colour") {
		t.Fatalf("the warning stayed after the typo was fixed: %q", tt.Texts())
	}
}

// The dialog is sized by dragging its corner, and the size is kept.
func TestDraggingTheCornerResizesTheDialog(t *testing.T) {
	app, tt := newSettingsApp(t, 1300, 900)
	before := app.prefs.SettingsWidth

	rect, ok := tt.Find("Resize settings")
	if !ok {
		t.Fatalf("no resize grip: %q", tt.Texts())
	}
	cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
	tt.Move(cx, cy)
	tt.Press(cx, cy)
	for _, step := range []float32{18, 40, 80} {
		tt.Move(cx+step, cy+step/2)
	}
	tt.Release(cx+80, cy+40)
	tt.Frame()

	if got := app.prefs.SettingsWidth; got <= before {
		t.Fatalf("width did not grow: %v -> %v", before, got)
	}
	if app.prefs.SettingsWidth > settings.MaxSettingsWidth || app.prefs.SettingsHeight > settings.MaxSettingsHeight {
		t.Fatalf("drag overran the clamp: %vx%v", app.prefs.SettingsWidth, app.prefs.SettingsHeight)
	}
	if app.prefs.SettingsHeight <= float32(settings.DefaultSettingsHeight) {
		t.Fatalf("height did not grow: %v", app.prefs.SettingsHeight)
	}
}

// Dragging past the limits stops at them: the dialog must stay a dialog.
func TestTheResizeGripStopsAtItsLimits(t *testing.T) {
	app, tt := newSettingsApp(t, 1300, 900)
	rect, ok := tt.Find("Resize settings")
	if !ok {
		t.Fatalf("no resize grip: %q", tt.Texts())
	}
	cx, cy := rect.X+rect.W/2, rect.Y+rect.H/2
	tt.Move(cx, cy)
	tt.Press(cx, cy)
	for i := 1; i <= 4; i++ {
		tt.Move(cx-float32(i)*120, cy-float32(i)*100)
	}
	tt.Release(cx-480, cy-400)
	tt.Frame()
	if app.prefs.SettingsWidth != settings.MinSettingsWidth || app.prefs.SettingsHeight != settings.MinSettingsHeight {
		t.Fatalf("shrank to %vx%v, want the minimum %dx%d",
			app.prefs.SettingsWidth, app.prefs.SettingsHeight, settings.MinSettingsWidth, settings.MinSettingsHeight)
	}
}

// mygo draws a scroll container's bar over its content. Without a reserve at the
// right edge, the last few pixels of every input and select in the dialog sat
// under the page's bar, which read as controls overflowing the page.
func TestSettingsControlsStopShortOfThePageScrollBar(t *testing.T) {
	_, tt := newSettingsApp(t, 1100, 800)
	field, ok := tt.Find("Highlight keywords")
	if !ok {
		t.Fatalf("no keyword field: %q", tt.Texts())
	}
	grip, ok := tt.Find("Resize settings")
	if !ok {
		t.Fatalf("no resize grip: %q", tt.Texts())
	}
	// The grip sits in the dialog's bottom-right corner, so its right edge is
	// the edge the bar is drawn against.
	if gap := (grip.X + grip.W) - (field.X + field.W); gap < 6 {
		t.Fatalf("the field ends %.1f DIPs from the dialog edge; the scroll bar is 6 wide and drawn over it", gap)
	}
}

// A keyword list is usually longer than the box, so the box has to show where
// it is scrolled to. The bar is only drawn while hovered unless it is kept, and
// a bar that appears only on hover looks like no bar at all.
func TestTheKeywordBoxKeepsItsScrollBar(t *testing.T) {
	app, tt := newSettingsApp(t, 1100, 800)
	field, ok := tt.Find("Highlight keywords")
	if !ok {
		t.Fatalf("no keyword field: %q", tt.Texts())
	}

	// The thumb is drawn in the theme's scroll bar colour, which is darker than
	// the field's background and border; counting those pixels down the right
	// edge is how "is there a bar" is measurable in a headless frame.
	dark := func() int {
		img := tt.Image()
		n := 0
		for y := int(field.Y) + 2; y < int(field.Y+field.H)-2; y++ {
			for x := int(field.X+field.W) - 10; x < int(field.X+field.W)-2; x++ {
				r, _, _, _ := img.At(x, y).RGBA()
				if r>>8 < 0xd0 {
					n++
				}
			}
		}
		return n
	}

	app.highlightText = "RED: error"
	tt.Frame()
	if n := dark(); n != 0 {
		t.Fatalf("%d bar pixels beside a one-line field: the bar shows with nothing to scroll", n)
	}

	app.highlightText = strings.Repeat("GREEN: info|healthy\n", 20)
	tt.Frame()
	if n := dark(); n == 0 {
		t.Fatal("the field overflows its box but shows no scroll bar")
	}
}

// Shrinking the dialog to its shortest used to make the Storage page spill out
// of the panel and paint over the dimmed backdrop under it: the end of the page
// hung outside its own window, and there was no way to scroll it back.
func TestTheStoragePageScrollsInsteadOfSpilling(t *testing.T) {
	app, tt := newSettingsApp(t, 1100, 820)
	app.prefs.SettingsHeight = settings.MinSettingsHeight
	app.settingsPage = 2
	tt.Frame()

	grip, ok := tt.Find("Resize settings")
	if !ok {
		t.Fatalf("no resize grip: %q", tt.Texts())
	}
	bottom := grip.Y + grip.H

	export, ok := tt.Find("Export…")
	if !ok {
		t.Fatalf("the Storage page has no export button: %q", tt.Texts())
	}
	// A clipped box has no area: nothing of it is painted, whatever its laid
	// out position. Without the scroller it kept its full height out there.
	if export.H != 0 {
		t.Fatalf("content below the fold is painted outside the panel: y=%.0f h=%.0f, panel ends at %.0f",
			export.Y, export.H, bottom)
	}

	// Below the fold, but reachable: the page scrolls to it.
	x, y := grip.X-40, bottom-60
	tt.Move(x, y)
	tt.Frame()
	for i := 0; i < 8; i++ {
		tt.Wheel(x, y, 0, 120)
		tt.Advance(300 * time.Millisecond)
	}
	tt.Frame()

	export, ok = tt.Find("Export…")
	if !ok || export.H == 0 {
		t.Fatalf("the page did not scroll to its own end: y=%.0f h=%.0f", export.Y, export.H)
	}
	if export.Y+export.H > bottom {
		t.Fatalf("the end of the page is still outside the panel: %.0f > %.0f", export.Y+export.H, bottom)
	}
}
