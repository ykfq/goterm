// Package appdir resolves on-disk locations for GoTerm's own state
// (host list, sealed secrets, known hosts).
//
// Three sources, in this order:
//
//  1. the GOTERM_DIR environment variable — tests use it to stay inside a temp
//     dir, and a portable copy can point it at a folder beside the exe;
//  2. a pointer the user set in Settings → Storage, stored in prefs.json in the
//     *default* directory so it can always be found again;
//  3. the default directory: a `data` folder beside the executable when that
//     folder is writable (a portable copy keeps everything together), else
//     %APPDATA%\goterm ($XDG_CONFIG_HOME/goterm).
//
// The executable's own folder is preferred because GoTerm is shipped as a
// two-file portable folder; keeping hosts.json, settings.json and logs/ next
// to the binary is what makes copying that folder a full migration. Only when
// the program sits somewhere it cannot write (Program Files, a read-only
// share) does the default fall back to the per-user configuration folder.
//
// Direction matters: the setting says where the *data* lives, and lives itself
// somewhere that never moves. Otherwise a bad path would make the setting
// unreachable — the app would forget where it keeps its own note.
package appdir

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"

	"github.com/ykfq/goterm/internal/atomicfile"
)

// ErrEnvOverride says the setting cannot take effect because GOTERM_DIR is set.
var ErrEnvOverride = errors.New("GOTERM_DIR is set, so it decides where state lives")

// prefsFile is the pointer to a custom data directory. It always sits in the
// default directory, never in the directory it names.
const prefsFile = "prefs.json"

type prefs struct {
	// DataDir is empty when the default location is in use.
	DataDir string `json:"dataDir,omitempty"`
}

var (
	once sync.Once
	dir  string
	err  error
)

// PortableDir is the `data` folder beside the running executable, or "" when
// the executable cannot be located or that folder cannot be created.
func PortableDir() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir := filepath.Join(filepath.Dir(exe), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return ""
	}
	if err := probeWritable(dir); err != nil {
		return ""
	}
	return dir
}

// UserConfigDir is %APPDATA%\goterm (or $XDG_CONFIG_HOME/goterm). It is the
// fallback for a copy that cannot write beside itself, and the place the
// pointer to a chosen directory always lives.
func UserConfigDir() (string, error) {
	base, e := os.UserConfigDir()
	if e != nil {
		return "", fmt.Errorf("locate user config dir: %w", e)
	}
	return filepath.Join(base, "goterm"), nil
}

// DefaultDir is where state lives when nothing says otherwise. It is not
// created here except for the portable candidate, which has to be probed to
// know whether it is usable.
func DefaultDir() (string, error) {
	if p := PortableDir(); p != "" {
		return p, nil
	}
	return UserConfigDir()
}

// EnvDir returns GOTERM_DIR as set, or "".
func EnvDir() string { return os.Getenv("GOTERM_DIR") }

// Configured returns the directory the user chose in Settings, or "" for the
// default. It reads the pointer file every time, so it also answers "what
// changes on the next start". The pointer lives in the user configuration
// folder when there is one, because the default may be beside an executable
// that moves.
func Configured() (string, error) {
	dflt, err := DefaultDir()
	if err != nil {
		return "", err
	}
	chosen, err := readConfigured(dflt)
	if err != nil || chosen != "" {
		return chosen, err
	}
	if user, uerr := UserConfigDir(); uerr == nil && !samePath(user, dflt) {
		return readConfigured(user)
	}
	return "", nil
}

func readConfigured(dflt string) (string, error) {
	raw, err := os.ReadFile(filepath.Join(dflt, prefsFile))
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read %s: %w", prefsFile, err)
	}
	var p prefs
	if err := json.Unmarshal(raw, &p); err != nil {
		// A hand-edited or truncated pointer is not worth failing the whole app
		// over: the default directory is always a correct answer.
		return "", nil
	}
	return strings.TrimSpace(p.DataDir), nil
}

// PendingDir is the directory the next start would use: GOTERM_DIR, else the
// setting, else the default. No directories are created.
func PendingDir() (string, error) {
	if env := EnvDir(); env != "" {
		return filepath.Clean(env), nil
	}
	chosen, err := Configured()
	if err != nil {
		return "", err
	}
	if chosen != "" {
		return filepath.Clean(chosen), nil
	}
	return DefaultDir()
}

// Dir returns the directory GoTerm stores its state in, creating it if needed.
func Dir() (string, error) {
	once.Do(func() {
		target, e := PendingDir()
		if e != nil {
			err = e
			return
		}
		if e := os.MkdirAll(target, 0o700); e != nil {
			err = fmt.Errorf("create %s: %w", target, e)
			return
		}
		dir = target
	})
	return dir, err
}

// Path joins name onto Dir().
func Path(name string) (string, error) {
	d, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(d, name), nil
}

// SetDir records where state should live from the next start on. An empty
// string goes back to the default. It refuses while GOTERM_DIR is set, because
// that would quietly do nothing.
//
// The directory is created and probed here rather than at the next start: a
// path that cannot be written has to be reported while the person who picked it
// is still looking at the dialog.
func SetDir(path string) error {
	if EnvDir() != "" {
		return ErrEnvOverride
	}
	dflt, err := DefaultDir()
	if err != nil {
		return err
	}
	pointer, err := UserConfigDir()
	if err != nil {
		pointer = dflt
	}
	path = strings.TrimSpace(path)
	if path == "" || samePath(path, dflt) {
		for _, dir := range []string{dflt, pointer} {
			if err := os.Remove(filepath.Join(dir, prefsFile)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("clear %s: %w", prefsFile, err)
			}
		}
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("%q: %w", path, err)
	}
	if err := os.MkdirAll(abs, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", abs, err)
	}
	if err := probeWritable(abs); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(prefs{DataDir: abs}, "", "  ")
	if err != nil {
		return err
	}
	return atomicfile.Write(filepath.Join(pointer, prefsFile), append(raw, '\n'), 0o600)
}

// probeWritable proves the directory accepts a file, which MkdirAll alone does
// not (an existing read-only folder, a network share with no create right).
func probeWritable(dir string) error {
	f, err := os.CreateTemp(dir, ".goterm-write-test-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		os.Remove(name)
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	if err := os.Remove(name); err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	return nil
}

func samePath(a, b string) bool {
	ca, err := filepath.Abs(filepath.Clean(a))
	if err != nil {
		return false
	}
	cb, err := filepath.Abs(filepath.Clean(b))
	if err != nil {
		return false
	}
	if runtime.GOOS == "windows" {
		return strings.EqualFold(ca, cb)
	}
	return ca == cb
}

// Same reports whether two paths name the same directory, case-insensitively on
// Windows. The UI uses it to decide whether a change needs a restart.
func Same(a, b string) bool {
	if a == "" || b == "" {
		return a == b
	}
	return samePath(a, b)
}

// Reset forgets the resolved directory. Test-only helper.
func Reset() {
	once = sync.Once{}
	dir, err = "", nil
}
