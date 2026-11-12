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
