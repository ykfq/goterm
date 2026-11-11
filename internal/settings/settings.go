// Package settings persists GoTerm's preferences as JSON.
//
// 1.x kept these in the webview's localStorage, which 2.x has none of. The
// file lives next to hosts.json so a user's configuration is one directory.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/ykfq/goterm/internal/atomicfile"
)

// Default scrollback is smaller than Ghostty's 10 MB because a terminal tab is
// not the only thing in memory; 8 MB holds roughly 100k lines of ordinary
// shell output.
const (
	DefaultScrollbackMB = 8
	MinScrollbackMB     = 1
	MaxScrollbackMB     = 64
	MinFontSize         = 9
	MaxFontSize         = 32
	MinIdleHours        = 1
	MaxIdleHours        = 744
)

// Settings is the whole preference file. Zero values are repaired by Clamp, so
// an older or hand-edited file cannot produce a terminal with no scrollback or
// a font of size 0.
type Settings struct {
	Theme            string  `json:"theme"` // system | light | dark
	FontFamily       string  `json:"fontFamily"`
	FontSize         int     `json:"fontSize"`
	ScrollbackMB     int     `json:"scrollbackMB"`
	SessionLogging   bool    `json:"sessionLogging"`
	Highlight        bool    `json:"highlight"`
	Encoding         string  `json:"encoding"`
	CopyOnSelect     bool    `json:"copyOnSelect"`
	IdleTimeoutHours int     `json:"idleTimeoutHours"`
	SidebarWidth     float32 `json:"sidebarWidth"`
	// LastUploadDir is where the next upload dialog opens.
	LastUploadDir string `json:"lastUploadDir,omitempty"`
	// LastHostID reopens the sidebar on the host used last.
	LastHostID string `json:"lastHostId,omitempty"`
}

// Default is what a fresh install starts with: the 1.x defaults, translated to
// the few units that changed (scrollback is MB, not lines).
func Default() Settings {
	return Settings{
		Theme:            "system",
		FontFamily:       "",
		FontSize:         14,
		ScrollbackMB:     DefaultScrollbackMB,
		SessionLogging:   false,
		Highlight:        true,
		Encoding:         "utf-8",
		CopyOnSelect:     true,
		IdleTimeoutHours: 24,
		SidebarWidth:     268,
	}
}

// Clamp repairs values that are missing or out of range.
func (s *Settings) Clamp() {
	if s.Theme != "light" && s.Theme != "dark" {
		s.Theme = "system"
	}
	if s.FontSize < MinFontSize || s.FontSize > MaxFontSize {
		s.FontSize = Default().FontSize
	}
	if s.ScrollbackMB < MinScrollbackMB || s.ScrollbackMB > MaxScrollbackMB {
		s.ScrollbackMB = DefaultScrollbackMB
	}
	if s.IdleTimeoutHours < MinIdleHours || s.IdleTimeoutHours > MaxIdleHours {
		s.IdleTimeoutHours = Default().IdleTimeoutHours
	}
	if s.SidebarWidth < 180 || s.SidebarWidth > 520 {
		s.SidebarWidth = Default().SidebarWidth
	}
	switch s.Encoding {
	case "utf-8", "gb18030", "big5", "shift_jis":
	default:
		s.Encoding = "utf-8"
	}
}

// Load reads path, or returns the defaults when it is missing or unreadable.
// A malformed file is not an error a user can act on: starting with defaults
// beats refusing to start.
func Load(path string) Settings {
	s := Default()
	data, err := os.ReadFile(path)
	if err != nil {
		return s
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Default()
	}
	s.Clamp()
	return s
}

// Save writes the settings atomically, creating the directory if needed.
func (s Settings) Save(path string) error {
	s.Clamp()
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicfile.Write(path, append(data, '\n'), 0o600)
}
