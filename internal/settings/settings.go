// Package settings persists GoTerm's preferences as JSON.
//
// 1.x kept these in the webview's localStorage, which 2.x has none of. The
// file lives next to hosts.json so a user's configuration is one directory.
package settings

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

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
	// Idle nudge bounds, in seconds. The floor is 15 s so a hand-edited file
	// cannot turn the keystroke into a flood; the ceiling is half an hour,
	// past which a bastion's timer would have fired first anyway.
	MinNudgeSeconds = 15
	MaxNudgeSeconds = 1800
	// DefaultNudgeSeconds is one minute: half the shortest input timeout anyone
	// configures (two minutes), so one missed tick still leaves another in
	// time. It is the cadence the user asked for.
	DefaultNudgeSeconds = 60
	// The custom highlight keywords are the user's, but they still need a
	// limit: every word is matched against every line of output, and a file
	// full of one-letter words would slow the terminal without colouring
	// anything useful.
	MaxHighlightWords   = 64
	MaxHighlightWordLen = 64
)

// SchemaVersion is stamped into every file this package writes.
//
// It exists for one reason a bool cannot serve: telling a choice from an old
// default. Before schema 2 the idle keystroke was opt-in and off, so every
// settings.json in the wild says "idleNudge": false — written by the app, not
// chosen by anyone. Reading that as a choice is how the feature the user asked
// for ran on no machine. A file without a version was written by a build whose
// false meant nothing, and gets the new default once.
//
// Schema 3 is the window dropping to one minute. Files at 2 were written by the
// builds whose keystroke was too slow or never fired, so their 120 s is the old
// default too, not a preference.
const SchemaVersion = 3

// Settings is the whole preference file. Zero values are repaired by Clamp, so
// an older or hand-edited file cannot produce a terminal with no scrollback or
// a font of size 0.
type Settings struct {
	// Version is the schema that wrote this file (see SchemaVersion).
	Version          int     `json:"version,omitempty"`
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
	// HighlightWords are the user's own keywords, coloured like the built-in
	// ones whenever a line of plain output contains one. Matching ignores
	// ASCII case; an all-ASCII word matches whole words only, so "ERR" cannot
	// colour part of "error" — a keyword with any non-ASCII character (CJK
	// included) matches as a substring, because those scripts have no spaces
	// to bound a word with.
	HighlightWords []string `json:"highlightWords,omitempty"`

	// IdleNudge turns the idle keystroke on (see internal/sshx/nudge.go). It is
	// on by default, because the sessions it defends — a shell with TMOUT, a
	// bastion that watches typed input — are the common case and the failure
	// is silent: the user just gets logged out. A quiet window with no output
	// is the only thing that triggers it, and a session that is being used is
	// never touched.
	IdleNudge bool `json:"idleNudge"`
	// IdleNudgeSeconds is the window between keystrokes. Zero means the default
	// of one minute.
	IdleNudgeSeconds int `json:"idleNudgeSeconds,omitempty"`
	// IdleNudgeSubmit lets the empty-line keystroke become a space when an
	// Enter would be unsafe (for instance mid-transfer). Off by default: a
	// shell that only counts completed lines needs a real Enter.
	IdleNudgeSubmit bool `json:"idleNudgeSubmit,omitempty"`
}

// Default is what a fresh install starts with: the 1.x defaults, translated to
// the few units that changed (scrollback is MB, not lines).
func Default() Settings {
	return Settings{
		Version:          SchemaVersion,
		IdleNudge:        true,
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
		IdleNudgeSeconds: DefaultNudgeSeconds,
	}
}

// Clamp repairs values that are missing or out of range.
func (s *Settings) Clamp() {
	if s.Version < SchemaVersion {
		s.Version = SchemaVersion
	}
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
	if s.IdleNudgeSeconds == 0 {
		s.IdleNudgeSeconds = Default().IdleNudgeSeconds
	}
	if s.IdleNudgeSeconds < MinNudgeSeconds || s.IdleNudgeSeconds > MaxNudgeSeconds {
		s.IdleNudgeSeconds = Default().IdleNudgeSeconds
	}
	if s.SidebarWidth < 180 || s.SidebarWidth > 520 {
		s.SidebarWidth = Default().SidebarWidth
	}
	switch s.Encoding {
	case "utf-8", "gb18030", "big5", "shift_jis":
	default:
		s.Encoding = "utf-8"
	}
	s.HighlightWords = clampWords(s.HighlightWords)
}

// clampWords drops blank, oversized and duplicate words and applies the cap.
// Order is kept, so the list a user typed stays recognisable.
func clampWords(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, 0, len(in))
	seen := make(map[string]bool, len(in))
	for _, w := range in {
		w = strings.TrimSpace(w)
		if w == "" || len(w) > MaxHighlightWordLen {
			continue
		}
		k := strings.ToLower(w)
		if seen[k] {
			continue
		}
		seen[k] = true
		out = append(out, w)
		if len(out) == MaxHighlightWords {
			break
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// ParseHighlightWords reads the settings field's text: one keyword per line,
// with commas and semicolons also accepted as separators so a paste of a
// comma-separated list works without editing it first.
func ParseHighlightWords(text string) []string {
	fields := strings.FieldsFunc(text, func(r rune) bool {
		return r == '\n' || r == '\r' || r == ',' || r == ';'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		// A run of separators can leave a field of nothing but spaces:
		// FieldsFunc splits on the separators, not on what surrounds them.
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
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
	// Read the version on its own first: unmarshalling into Default() leaves a
	// missing key holding the new default, which would make every old file look
	// current.
	var header struct {
		Version int `json:"version"`
	}
	if err := json.Unmarshal(data, &header); err != nil {
		return Default()
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return Default()
	}
	if header.Version < SchemaVersion {
		// An older file, and every version before this one was written by a
		// build whose keystroke did not actually keep a session alive: off, or
		// two minutes late, or silent. So neither the switch nor the window in
		// it is a decision to honour — both take the current default, once, and
		// the file is rewritten so the next start reads a choice.
		s.IdleNudge = true
		s.IdleNudgeSeconds = DefaultNudgeSeconds
		s.Clamp()
		_ = s.Save(path)
		return s
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
