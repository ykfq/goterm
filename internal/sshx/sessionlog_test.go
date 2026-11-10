package sshx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ykfq/goterm/internal/sshtest"
)

// The log is a copy of what the terminal received, byte for byte. Colour
// sequences included: a log that quietly differs from the screen is worse than
// one a pager has to interpret.
func TestSessionLogRecordsExactlyWhatTheTerminalGot(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	dir := filepath.Join(t.TempDir(), "logs")

	info, err := h.mgr.ConfigureSessionLogging(true, dir)
	if err != nil {
		t.Fatalf("enable logging: %v", err)
	}
	if !info.Enabled || info.Dir != dir {
		t.Fatalf("SessionLogging() = %+v, want enabled in %s", info, dir)
	}

	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)
	h.send(sess, "echo logged line\r")
	h.expectOutput(sess, "logged line")

	path := sess.Info().LogPath
	if path == "" {
		t.Fatal("session has no log path")
	}
	if !strings.HasPrefix(path, dir) {
		t.Fatalf("log path %q is outside %q", path, dir)
	}
	if !strings.Contains(h.rec.noticeText(), path) {
		t.Fatalf("no notice naming the log file; notices were:\n%s", h.rec.noticeText())
	}

	// Close the session and wait for the file to match what was emitted: both
	// sides settle on the same pump, so equality is the honest assertion.
	if err := h.mgr.Close(sess.ID()); err != nil {
		t.Fatalf("close: %v", err)
	}
	h.waitFor(sess, "log file to match the terminal", func() bool {
		raw, err := os.ReadFile(path)
		return err == nil && bytes.Equal(raw, h.rec.output(sess.ID()))
	})

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(raw, []byte("logged line")) {
		t.Fatalf("log does not contain the command's output:\n%s", printable(raw))
	}
	// The login banner is colourised by the test server; the escape sequence in
	// the file proves this is the raw stream, not a re-rendered text copy.
	if !bytes.Contains(raw, []byte("\x1b[")) {
		t.Fatal("log has no escape sequences; it is not the raw terminal stream")
	}
}

func TestSessionLogIsOffByDefault(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	if got := sess.Info().LogPath; got != "" {
		t.Fatalf("LogPath = %q with logging never enabled", got)
	}
	if info := h.mgr.SessionLogging(); info.Enabled {
		t.Fatalf("SessionLogging() = %+v, want disabled", info)
	}
}

// Switching recording off mid-session closes the file there and then: a user
// who turns it off is not asking for the rest of the session to be recorded
// quietly.
func TestSessionLogStopsWhenDisabled(t *testing.T) {
	// The temp dir is created before the harness on purpose: Go runs cleanups
	// last-in-first-out, so CloseAll (which closes the log file) runs before
	// the directory is removed.
	dir := t.TempDir()
	h := newHarness(t, sshtest.Options{})
	if _, err := h.mgr.ConfigureSessionLogging(true, dir); err != nil {
		t.Fatalf("enable logging: %v", err)
	}
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	h.send(sess, "echo before-switch\r")
	h.expectOutput(sess, "before-switch")
	path := sess.Info().LogPath

	if _, err := h.mgr.ConfigureSessionLogging(false, dir); err != nil {
		t.Fatalf("disable logging: %v", err)
	}
	if got := sess.Info().LogPath; got != "" {
		t.Fatalf("LogPath = %q after disabling", got)
	}

	h.send(sess, "echo after-switch\r")
	h.expectOutput(sess, "after-switch")

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if !bytes.Contains(raw, []byte("before-switch")) {
		t.Fatal("the part recorded before switching off is missing")
	}
	if bytes.Contains(raw, []byte("after-switch")) {
		t.Fatal("output after switching off was recorded anyway")
	}
}

// Two sessions get two files: a tab duplicated onto the same connection is a
// second terminal, and its output belongs in its own log.
func TestSessionLogIsPerSession(t *testing.T) {
	dir := t.TempDir()
	h := newHarness(t, sshtest.Options{})
	if _, err := h.mgr.ConfigureSessionLogging(true, dir); err != nil {
		t.Fatalf("enable logging: %v", err)
	}
	first := h.connect(h.config())
	h.expectOutput(first, readyMarker)

	second, err := h.mgr.Duplicate(first.ID())
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	h.expectOutput(second, readyMarker)

	if first.Info().LogPath == second.Info().LogPath {
		t.Fatalf("both sessions log to %q", first.Info().LogPath)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read log dir: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("log dir has %d files, want 2", len(entries))
	}
}

func TestSessionLogPathIsSafe(t *testing.T) {
	started := time.Date(2026, 11, 10, 15, 4, 5, 0, time.Local)
	got := sessionLogPath("C:/logs", `tester@[::1]:2222`, started)
	if strings.ContainsAny(filepath.Base(got), `@:[]`) {
		t.Fatalf("file name kept characters Windows rejects: %q", got)
	}
	if !strings.HasPrefix(filepath.Base(got), "goterm-") || !strings.HasSuffix(got, ".log") {
		t.Fatalf("unexpected log name %q", got)
	}
}
