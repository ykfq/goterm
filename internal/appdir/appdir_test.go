// The state directory has three sources and one rule: the pointer to a custom
// directory must live where it can always be found, so a bad path cannot make
// the setting unreachable. These tests pin the precedence and the refusals.
package appdir

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// sandbox points the "user config" lookup at a temp dir and clears GOTERM_DIR,
// so a test never reads or writes the real %APPDATA%\goterm. It returns the
// default state directory that would be used.
func sandbox(t *testing.T) string {
	t.Helper()
	if runtime.GOOS != "windows" && runtime.GOOS != "linux" {
		t.Skipf("UserConfigDir is not redirectable on %s", runtime.GOOS)
	}
	base := t.TempDir()
	if runtime.GOOS == "windows" {
		t.Setenv("APPDATA", base)
	} else {
		t.Setenv("XDG_CONFIG_HOME", base)
	}
	t.Setenv("GOTERM_DIR", "")
	Reset()
	t.Cleanup(Reset)
	return filepath.Join(base, "goterm")
}

func TestDefaultDirFollowsTheConfigBase(t *testing.T) {
	dflt := sandbox(t)
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if got != dflt {
		t.Fatalf("DefaultDir = %q, want %q", got, dflt)
	}
	if _, err := os.Stat(got); !os.IsNotExist(err) {
		t.Fatalf("DefaultDir must not create anything, stat err = %v", err)
	}
}

func TestDirDefaultsToTheConfigFolder(t *testing.T) {
	dflt := sandbox(t)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != dflt {
		t.Fatalf("Dir = %q, want the default %q", got, dflt)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("Dir must exist afterwards: %v", err)
	}
}

func TestSettingDecidesTheNextDir(t *testing.T) {
	dflt := sandbox(t)
	chosen := filepath.Join(t.TempDir(), "state-on-another-disk")

	if err := SetDir(chosen); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	// The pointer sits in the default folder, never in the folder it names.
	if _, err := os.Stat(filepath.Join(dflt, prefsFile)); err != nil {
		t.Fatalf("pointer not written to %s: %v", dflt, err)
	}
	if _, err := os.Stat(filepath.Join(chosen, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("pointer must not be duplicated in the data dir (err = %v)", err)
	}
	if got, _ := Configured(); got != chosen {
		t.Fatalf("Configured = %q, want %q", got, chosen)
	}

	Reset()
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir after SetDir: %v", err)
	}
	if got != chosen {
		t.Fatalf("Dir = %q, want the chosen %q", got, chosen)
	}
	if info, err := os.Stat(chosen); err != nil || !info.IsDir() {
		t.Fatalf("chosen dir not usable: %v", err)
	}
}

func TestEmptySettingGoesBackToTheDefault(t *testing.T) {
	dflt := sandbox(t)
	chosen := filepath.Join(t.TempDir(), "elsewhere")
	if err := SetDir(chosen); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	if err := SetDir("  "); err != nil {
		t.Fatalf("SetDir(\"  \"): %v", err)
	}
	if _, err := os.Stat(filepath.Join(dflt, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("pointer should be gone, stat err = %v", err)
	}
	Reset()
	if got, _ := Dir(); got != dflt {
		t.Fatalf("Dir = %q, want %q", got, dflt)
	}
	// Clearing when nothing was set is not an error either.
	if err := SetDir(""); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

func TestChoosingTheDefaultClearsThePointer(t *testing.T) {
	dflt := sandbox(t)
	if err := SetDir(filepath.Join(t.TempDir(), "x")); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	// Same folder, different spelling: Windows is case-insensitive.
	spelled := dflt
	if runtime.GOOS == "windows" {
		spelled = strings.ToUpper(dflt)
	}
	if err := SetDir(spelled + string(os.PathSeparator)); err != nil {
		t.Fatalf("SetDir(default): %v", err)
	}
	if got, _ := Configured(); got != "" {
		t.Fatalf("Configured = %q, want empty", got)
	}
}

func TestEnvVarBeatsTheSetting(t *testing.T) {
	dflt := sandbox(t)
	chosen := filepath.Join(t.TempDir(), "chosen")
	if err := SetDir(chosen); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	env := filepath.Join(t.TempDir(), "from-env")
	t.Setenv("GOTERM_DIR", env)
	Reset()

	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if got != env {
		t.Fatalf("Dir = %q, want the environment override %q", got, env)
	}
	if pending, _ := PendingDir(); pending != env {
		t.Fatalf("PendingDir = %q, want %q", pending, env)
	}
	// And the setting itself refuses, instead of writing a value that loses.
	if err := SetDir(filepath.Join(t.TempDir(), "ignored")); err != ErrEnvOverride {
		t.Fatalf("SetDir with GOTERM_DIR set = %v, want ErrEnvOverride", err)
	}
	if got, _ := Configured(); got != chosen {
		t.Fatalf("a refused SetDir must not touch the pointer, got %q", got)
	}
	_ = dflt
}

func TestCorruptPointerFallsBackToTheDefault(t *testing.T) {
	dflt := sandbox(t)
	if err := os.MkdirAll(dflt, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dflt, prefsFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, _ := Configured(); got != "" {
		t.Fatalf("Configured = %q, want empty for unreadable JSON", got)
	}
	if got, err := Dir(); err != nil || got != dflt {
		t.Fatalf("Dir = %q, %v; want the default %q", got, err, dflt)
	}
}

func TestSetDirRefusesAnUnusablePath(t *testing.T) {
	dflt := sandbox(t)
	// A regular file cannot be a parent directory.
	file := filepath.Join(t.TempDir(), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := SetDir(filepath.Join(file, "child")); err == nil {
		t.Fatal("SetDir under a regular file must fail")
	}
	if _, err := os.Stat(filepath.Join(dflt, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("a failed SetDir must leave the pointer alone (err = %v)", err)
	}
}
