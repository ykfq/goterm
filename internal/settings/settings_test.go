package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileGivesDefaults(t *testing.T) {
	got := Load(filepath.Join(t.TempDir(), "settings.json"))
	if got != Default() {
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
	if err := want.Save(path); err != nil {
		t.Fatal(err)
	}
	got := Load(path)
	if got != want {
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
	if got := Load(path); got != Default() {
		t.Fatalf("got %+v", got)
	}
}

func TestBoundariesStayAsGiven(t *testing.T) {
	s := Settings{FontSize: MinFontSize, ScrollbackMB: MaxScrollbackMB, IdleTimeoutHours: MaxIdleHours, SidebarWidth: 180}
	s.Clamp()
	if s.FontSize != MinFontSize || s.ScrollbackMB != MaxScrollbackMB || s.IdleTimeoutHours != MaxIdleHours || s.SidebarWidth != 180 {
		t.Fatalf("boundary values were changed: %+v", s)
	}
}
