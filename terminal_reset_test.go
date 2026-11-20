package main

import (
	"strings"
	"testing"
)

// A full-screen program can leave the emulator holding state a shell cannot use:
// the cursor hidden, the alternate screen up, the kitty keyboard flags pushed —
// which is what turns Ctrl+C into `\x1b[99;5u` and makes the shell echo `9;5u`
// back. Closing the tab used to be the only way out; this is the way out.
func TestTheResetSequenceUndoesWhatAFullScreenProgramLeaves(t *testing.T) {
	for _, part := range []struct{ what, seq string }{
		{"the cursor visible again", "\x1b[?25h"},
		{"the alternate screen left", "\x1b[?1049l"},
		{"mouse tracking off", "\x1b[?1000l"},
		{"focus reporting off", "\x1b[?1004l"},
		{"bracketed paste off", "\x1b[?2004l"},
		{"the kitty keyboard flags cleared", "\x1b[=0;1u"},
		{"a soft reset for the rest", "\x1b[!p"},
	} {
		if !strings.Contains(terminalReset, part.seq) {
			t.Errorf("the reset does not do %s (%q missing)", part.what, part.seq)
		}
	}
	// And it must not throw anything away: a reset that clears the history is a
	// worse trade than the state it fixes.
	for _, clears := range []string{"\x1b[2J", "\x1b[3J", "\x1bc"} {
		if strings.Contains(terminalReset, clears) {
			t.Errorf("the reset clears the screen or the scrollback (%q)", clears)
		}
	}
}

func TestResetTerminalIsACommandWithAKey(t *testing.T) {
	app := &App{}
	var found *appCommand
	for _, group := range app.menus() {
		for i := range group.cmds {
			if group.cmds[i].label == "Reset Terminal" {
				found = &group.cmds[i]
				if group.label != "View" {
					t.Fatalf("Reset Terminal lives in %q, want View", group.label)
				}
			}
		}
	}
	if found == nil {
		t.Fatal("there is no Reset Terminal command")
	}
	if found.accel != "CmdOrCtrl+Shift+K" {
		t.Fatalf("Reset Terminal has accelerator %q", found.accel)
	}
	if found.run == nil {
		t.Fatal("Reset Terminal does nothing")
	}
}

// A tab that never got a terminal (a failed dial) must not be able to panic the
// command, which is reachable from the menu at any time.
func TestResetTerminalWithoutATerminalIsHarmless(t *testing.T) {
	app := &App{}
	app.resetTerminal(nil)
	app.resetTerminal(&Tab{})
}
