// The state directory has sources, one rule each: GOTERM_DIR wins; a pointer
// the user set wins over the default; the default is the `data` folder beside
// the executable when that is writable, and the per-user configuration folder
// otherwise — and the pointer always lives in the per-user folder, because the
// executable may move with the portable copy it belongs to.
package appdir

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// sandbox points the "user config" lookup at a temp dir and clears GOTERM_DIR,
// so a test never reads or writes the real %APPDATA%\goterm. It returns the
// per-user folder that would be used.
func sandbox(t *testing.T) string {
	t.Helper()
	dir := tempDir(t)
	if os.PathSeparator == '\\' {
		t.Setenv("APPDATA", dir)
	} else {
		t.Setenv("XDG_CONFIG_HOME", dir)
	}
	t.Setenv("GOTERM_DIR", "")
	Reset()
	t.Cleanup(Reset)
	return filepath.Join(dir, "goterm")
}

// tempDir is t.TempDir with a shove: on Windows a directory that was just
// scanned can refuse to be removed for a few milliseconds, and testing's own
// cleanup reports that as "directory is not empty". Retrying here keeps a
// timing artefact from failing an otherwise correct test.
func tempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Cleanup(func() {
		for i := 0; i < 20; i++ {
			if _, err := os.Stat(dir); os.IsNotExist(err) {
				return
			}
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	})
	return dir
}

func TestDefaultPrefersTheExecutableFolder(t *testing.T) {
	sandbox(t)
	got, err := DefaultDir()
	if err != nil {
		t.Fatalf("DefaultDir: %v", err)
	}
	if PortableDir() == "" {
		t.Skip("the executable's folder is not writable here")
	}
	exe, _ := os.Executable()
	// PortableDir resolves symlinks and, on Windows, 8.3 short names (the
	// runner's %TEMP% can hold "JIANGZ~1" while the real path says
	// "jiangzhouling"). Compare against the same normalised form rather than
	// the raw executable path.
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	if !strings.EqualFold(filepath.Dir(got), filepath.Dir(exe)) {
		t.Fatalf("DefaultDir = %q, want the executable's folder beside %q", got, exe)
	}
	if filepath.Base(got) != "data" {
		t.Fatalf("DefaultDir = %q, want a data folder", got)
	}
}

func TestDefaultFallsBackToTheUserFolder(t *testing.T) {
	user := sandbox(t)
	if PortableDir() != "" {
		t.Skip("this test needs a host whose executable folder is not writable")
	}
	got, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	if got != user {
		t.Fatalf("DefaultDir = %q, want %q", got, user)
	}
}

func TestDirCreatesWhateverItPicks(t *testing.T) {
	sandbox(t)
	got, err := Dir()
	if err != nil {
		t.Fatalf("Dir: %v", err)
	}
	if info, err := os.Stat(got); err != nil || !info.IsDir() {
		t.Fatalf("Dir must exist afterwards: %v", err)
	}
}

func TestSettingDecidesTheNextDir(t *testing.T) {
	user := sandbox(t)
	chosen := filepath.Join(tempDir(t), "state-on-another-disk")

	if err := SetDir(chosen); err != nil {
		t.Fatalf("SetDir: %v", err)
	}
	// The pointer sits in the per-user folder, never in the folder it names.
	if _, err := os.Stat(filepath.Join(user, prefsFile)); err != nil {
		t.Fatalf("pointer not written to %s: %v", user, err)
	}
	if _, err := os.Stat(filepath.Join(chosen, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("pointer must not be duplicated in the data dir (err = %v)", err)
	}
	if got, _ := Configured(); got != chosen {
		t.Fatalf("Configured = %q, want %q", got, chosen)
	}
	Reset()
	if got, err := Dir(); err != nil || got != chosen {
		t.Fatalf("Dir = %q, %v; want the chosen %q", got, err, chosen)
	}
}

func TestEmptySettingGoesBackToTheDefault(t *testing.T) {
	user := sandbox(t)
	chosen := filepath.Join(tempDir(t), "elsewhere")
	if err := SetDir(chosen); err != nil {
		t.Fatal(err)
	}
	if err := SetDir("  "); err != nil {
		t.Fatalf("SetDir(\"  \"): %v", err)
	}
	if _, err := os.Stat(filepath.Join(user, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("pointer should be gone, stat err = %v", err)
	}
	Reset()
	if got, _ := Configured(); got != "" {
		t.Fatalf("Configured = %q, want empty", got)
	}
	if err := SetDir(""); err != nil {
		t.Fatalf("second clear: %v", err)
	}
}

func TestChoosingTheDefaultClearsThePointer(t *testing.T) {
	user := sandbox(t)
	if err := SetDir(filepath.Join(tempDir(t), "x")); err != nil {
		t.Fatal(err)
	}
	dflt, err := DefaultDir()
	if err != nil {
		t.Fatal(err)
	}
	spelled := dflt
	if os.PathSeparator == '\\' {
		spelled = strings.ToUpper(dflt)
	}
	if err := SetDir(spelled + string(os.PathSeparator)); err != nil {
		t.Fatalf("SetDir(default): %v", err)
	}
	if got, _ := Configured(); got != "" {
		t.Fatalf("Configured = %q, want empty", got)
	}
	if _, err := os.Stat(filepath.Join(user, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("pointer should be gone, stat err = %v", err)
	}
}

func TestEnvVarBeatsTheSetting(t *testing.T) {
	sandbox(t)
	chosen := filepath.Join(tempDir(t), "chosen")
	if err := SetDir(chosen); err != nil {
		t.Fatal(err)
	}
	env := filepath.Join(tempDir(t), "from-env")
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
	if err := SetDir(filepath.Join(tempDir(t), "ignored")); err != ErrEnvOverride {
		t.Fatalf("SetDir with GOTERM_DIR set = %v, want ErrEnvOverride", err)
	}
	if got, _ := Configured(); got != chosen {
		t.Fatalf("a refused SetDir must not touch the pointer, got %q", got)
	}
}

func TestCorruptPointerFallsBackToTheDefault(t *testing.T) {
	user := sandbox(t)
	if err := os.MkdirAll(user, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(user, prefsFile), []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, _ := Configured(); got != "" {
		t.Fatalf("Configured = %q, want empty for unreadable JSON", got)
	}
	want, _ := DefaultDir()
	if got, err := Dir(); err != nil || got != want {
		t.Fatalf("Dir = %q, %v; want the default %q", got, err, want)
	}
}

func TestSetDirRefusesAnUnusablePath(t *testing.T) {
	user := sandbox(t)
	// A regular file cannot be a parent directory.
	file := filepath.Join(tempDir(t), "a-file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := SetDir(filepath.Join(file, "child")); err == nil {
		t.Fatal("SetDir under a regular file must fail")
	}
	if _, err := os.Stat(filepath.Join(user, prefsFile)); !os.IsNotExist(err) {
		t.Fatalf("a failed SetDir must leave the pointer alone (err = %v)", err)
	}
}

// TestPathsStayTogetherUnderTheChosenDir pins the layout the settings dialog
// promises: hosts, secrets, settings and the log folder all under one folder.
func TestPathsStayTogetherUnderTheChosenDir(t *testing.T) {
	sandbox(t)
	chosen := tempDir(t)
	if err := SetDir(chosen); err != nil {
		t.Fatal(err)
	}
	Reset()
	logs, err := Path("logs")
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(logs) != chosen || filepath.Base(logs) != "logs" {
		t.Fatalf("Path(logs) = %q, want %q", logs, filepath.Join(chosen, "logs"))
	}
	if dir, err := Dir(); err != nil || dir != chosen {
		t.Fatalf("Dir = %q, %v", dir, err)
	}
}
