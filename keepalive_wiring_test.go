package main

/*
 * The idle keystroke, seen from the app.
 *
 * internal/sshx proves the keystroke reaches a shell. What failed in the field
 * was upstream of that: the setting and the manager. A file the app itself had
 * written said "idleNudge": false, nothing on screen said a switch needed
 * flipping, and the sessions died with the code below them working correctly.
 *
 * These two tests are the seam: what NewApp hands the manager on start-up.
 */
import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/settings"
)

// settingsPath is where NewApp reads its file in this package's test data dir
// (main_test.go pins GOTERM_DIR for the whole package).
func settingsPath(t *testing.T) string {
	t.Helper()
	dir := os.Getenv("GOTERM_DIR")
	if dir == "" {
		t.Fatal("GOTERM_DIR is not set; TestMain should have made one")
	}
	return filepath.Join(dir, "settings.json")
}

func TestAFreshStartArmsTheIdleKeystroke(t *testing.T) {
	path := settingsPath(t)
	_ = os.Remove(path) // no file: a fresh install
	t.Cleanup(func() { _ = os.Remove(path) })

	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	if !app.prefs.IdleNudge {
		t.Fatal("a fresh install started with the idle keystroke off")
	}
	got := app.mgr.Nudge()
	if !got.On {
		t.Fatal("NewApp did not hand the idle keystroke to the manager: a session would sit there and be logged out")
	}
	if got.Every != settings.DefaultNudgeSeconds*time.Second {
		t.Fatalf("window handed over as %v, want %v", got.Every, settings.DefaultNudgeSeconds*time.Second)
	}
}

// The exact state on the machine this was reported from: the app's own file,
// written by the build whose default was off.
func TestASettingsFileFromTheOldBuildStillKeepsSessionsAlive(t *testing.T) {
	path := settingsPath(t)
	legacy := `{"theme":"system","fontSize":14,"scrollbackMB":8,"highlight":true,"idleNudge":false,"idleNudgeSeconds":120}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(path) })

	app, err := NewApp()
	if err != nil {
		t.Fatal(err)
	}
	if got := app.mgr.Nudge(); !got.On {
		t.Fatal("an old settings file left the idle keystroke off; the session would be logged out again")
	}
	// And the choice sticks from here on: the migration rewrote the file, so a
	// user who turns it off — or the app that writes the file next — is obeyed
	// instead of being overruled on every start.
	app.prefs.IdleNudge = false
	app.savePrefs()
	if again := settings.Load(path); again.IdleNudge {
		t.Fatal("the off switch was not saved; the migration would force it back on next start")
	}
}
