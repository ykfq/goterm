package settings

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	got := Load(filepath.Join(t.TempDir(), "settings.json"))
	if !reflect.DeepEqual(got, Default()) {
		t.Fatalf("got %+v, want defaults %+v", got, Default())
	}
}

func TestSaveThenLoadRoundTrips(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "settings.json")
	want := Default()
	want.Theme = "light"
	want.FontSize = 16
	want.ScrollbackMB = 16
	want.SessionLogging = true
	want.Encoding = "gb18030"
	want.CopyOnSelect = false
	want.HighlightWords = []string{"OOM", "内存不足", "backup failed"}
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestClampRepairsImpossibleValues(t *testing.T) {
	s := Settings{FontSize: 0, ScrollbackMB: 9999, IdleTimeoutHours: 0, SidebarWidth: 10, Theme: "neon", Encoding: "latin1"}
	s.Clamp()
	d := Default()
	if s.FontSize != d.FontSize || s.ScrollbackMB != d.ScrollbackMB ||
		s.IdleTimeoutHours != d.IdleTimeoutHours || s.SidebarWidth != d.SidebarWidth ||
		s.Theme != "system" || s.Encoding != "utf-8" {
		t.Fatalf("clamp left %+v", s)
	}
}

func TestMalformedFileFallsBackToDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := Load(path); !reflect.DeepEqual(got, Default()) {
		t.Fatalf("got %+v", got)
	}
}

// The keyword list is the one preference a user pastes in, so its parsing and
// its limits are pinned here rather than left to whatever the dialog sends.
func TestHighlightWordsAreParsedTrimmedAndCapped(t *testing.T) {
	got := ParseHighlightWords(" OOM \n, backup failed ;  ;内存不足\nOOM\n")
	if strings.Join(got, "|") != "OOM|backup failed|内存不足|OOM" {
		t.Fatalf("parsed %+v", got)
	}

	s := Default()
	s.HighlightWords = append(append(make([]string, 0, MaxHighlightWords+5),
		strings.Repeat("x", MaxHighlightWordLen+1), "", " dup ", "DUP"),
		repeatWord("w", MaxHighlightWords+4)...)
	s.Clamp()
	if len(s.HighlightWords) > MaxHighlightWords {
		t.Fatalf("kept %d words, the cap is %d", len(s.HighlightWords), MaxHighlightWords)
	}
	seen := map[string]bool{}
	for _, w := range s.HighlightWords {
		k := strings.ToLower(w)
		if seen[k] {
			t.Fatalf("duplicate survived the clamp: %q", w)
		}
		seen[k] = true
		if len(w) > MaxHighlightWordLen {
			t.Fatalf("oversized word survived: %d bytes", len(w))
		}
	}
	if !seen["dup"] {
		t.Fatalf("clamp dropped a valid word: %+v", s.HighlightWords)
	}
}

func TestAllBlankWordsMeansNoWords(t *testing.T) {
	s := Default()
	s.HighlightWords = []string{" ", "", "\t"}
	s.Clamp()
	if s.HighlightWords != nil {
		t.Fatalf("blank list kept as %+v", s.HighlightWords)
	}
}

func repeatWord(w string, n int) []string {
	out := make([]string, n)
	for i := range out {
		out[i] = w + string(rune('a'+i%26)) + strings.Repeat("z", i/26)
	}
	return out
}

func TestBoundariesStayAsGiven(t *testing.T) {
	s := Settings{FontSize: MinFontSize, ScrollbackMB: MaxScrollbackMB, IdleTimeoutHours: MaxIdleHours, SidebarWidth: 180}
	s.Clamp()
	if s.FontSize != MinFontSize || s.ScrollbackMB != MaxScrollbackMB || s.IdleTimeoutHours != MaxIdleHours || s.SidebarWidth != 180 {
		t.Fatalf("boundary values were changed: %+v", s)
	}
}

// The idle keystroke is what keeps a shell with TMOUT, and a bastion that
// watches typed input, from logging the session out. It must be armed on a
// fresh install: a feature behind a switch nobody knows to flip is a feature
// that does not exist.
func TestAFreshInstallArmsTheIdleKeystroke(t *testing.T) {
	d := Default()
	if !d.IdleNudge {
		t.Fatal("default settings leave the idle keystroke off")
	}
	if d.IdleNudgeSeconds != DefaultNudgeSeconds {
		t.Fatalf("default window is %ds, want %ds", d.IdleNudgeSeconds, DefaultNudgeSeconds)
	}
	if d.Version != SchemaVersion {
		t.Fatalf("default version is %d, want %d", d.Version, SchemaVersion)
	}
	got := Load(filepath.Join(t.TempDir(), "settings.json"))
	if !got.IdleNudge {
		t.Fatal("a missing settings file loaded with the keystroke off")
	}
}

// Before schema 2 every file the app wrote said "idleNudge": false, because
// that was the default. Reading it as a choice is exactly how the keystroke
// came to run on nobody's machine.
func TestALegacyFileGetsTheIdleKeystrokeOnceAndSaysSo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	legacy := `{"theme":"dark","fontSize":15,"scrollbackMB":16,"idleNudge":false}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if !got.IdleNudge {
		t.Fatal("a file written before schema 2 left the keystroke off")
	}
	if got.Version != SchemaVersion {
		t.Fatalf("migrated file says version %d, want %d", got.Version, SchemaVersion)
	}
	// The rest of the file is the user's and must survive the migration.
	if got.Theme != "dark" || got.FontSize != 15 || got.ScrollbackMB != 16 {
		t.Fatalf("migration changed other settings: %+v", got)
	}
	// Rewritten, so the next start reads a decision rather than an absence.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), `"version"`) == false {
		t.Fatalf("migrated file was not stamped: %s", raw)
	}
}

// After the migration the switch is the user's again: turning it off has to
// stick, or the only way to silence it would be deleting the file.
func TestTurningTheIdleKeystrokeOffSticks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	want := Default()
	want.IdleNudge = false
	want.IdleNudgeSeconds = 45
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got.IdleNudge {
		t.Fatal("turning the keystroke off did not survive a save and load")
	}
	if got.IdleNudgeSeconds != 45 {
		t.Fatalf("window came back as %ds, want 45s", got.IdleNudgeSeconds)
	}
}

func TestAFileFromTheFutureIsNotDowngraded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"version":99,"idleNudge":false}`), 0o600); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got.IdleNudge {
		t.Fatal("a newer file's explicit \"off\" was overridden")
	}
	if got.Version != 99 {
		t.Fatalf("version rewritten to %d", got.Version)
	}
}
