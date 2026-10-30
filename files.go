package main

// FileBridge is the native half of trzsz's file transfer.
//
// trzsz.js was written for browsers, where a download means
// window.showDirectoryPicker() and an upload means window.showOpenFilePicker().
// Inside a webview those APIs are unreliable (they need a secure context *and*
// transient user activation, and the transfer banner arrives asynchronously,
// long after the keystroke that caused it), which is why `tsz` looked like it
// did nothing.
//
// So the frontend installs shims for those two functions that instead call
// through here: the user gets a real Explorer dialog, and the byte movement
// goes through the chunked ReadRange/WriteChunk methods below. Nothing may read
// or write outside a folder the user actually picked in this session — a
// malicious server can put any path it likes in a transfer's NAME frame, and
// that must not become arbitrary file access.

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/wailsapp/wails/v3/pkg/application"
)

// maxChunk bounds one ReadRange/WriteChunk payload. trzsz negotiates its own
// chunk size; the frontend caps it below this so a single IPC message stays a
// sane size instead of 10 MB of base64.
const maxChunk = 4 << 20

// FileEntry is what the frontend's directory-handle shim iterates over.
type FileEntry struct {
	Name string `json:"name"`
	Dir  bool   `json:"dir"`
	Size int64  `json:"size"`
}

// FileBridge exposes folder/file operations scoped to user-chosen roots.
type FileBridge struct {
	mu    sync.Mutex
	ready bool
	roots []string
}

// NewFileBridge returns a bridge with no granted roots.
func NewFileBridge() *FileBridge { return &FileBridge{} }

// ServiceStartup implements Wails v3's service lifecycle. Before this runs
// there is no application object to hang a dialog off, so pickers refuse to
// open instead of panicking.
func (f *FileBridge) ServiceStartup(_ context.Context, _ application.ServiceOptions) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ready = true
	return nil
}

// dialogs returns the v3 dialog manager, or an error when the app is not up
// (which is exactly the case in unit tests).
func (f *FileBridge) dialogs() (*application.DialogManager, error) {
	f.mu.Lock()
	ready := f.ready
	f.mu.Unlock()
	app := application.Get()
	if !ready || app == nil {
		return nil, errors.New("the window is not ready yet")
	}
	return app.Dialog, nil
}

// --- picking --------------------------------------------------------------

// ChooseDirectory shows the native folder picker and grants access to whatever
// the user selected. An empty result means the user cancelled.
func (f *FileBridge) ChooseDirectory(title string) (string, error) {
	dialogs, err := f.dialogs()
	if err != nil {
		return "", err
	}
	if title == "" {
		title = "Choose a folder"
	}
	dir, err := dialogs.OpenFile().
		SetTitle(title).
		CanChooseFiles(false).
		CanChooseDirectories(true).
		CanCreateDirectories(true).
		PromptForSingleSelection()
	if err != nil {
		return "", err
	}
	if dir == "" {
		return "", nil
	}
	f.grant(dir)
	return filepath.Clean(dir), nil
}

// ChooseFiles shows the native multi-file picker and grants access to each
// file's parent folder. Empty means the user cancelled.
func (f *FileBridge) ChooseFiles(title string) ([]string, error) {
	dialogs, err := f.dialogs()
	if err != nil {
		return nil, err
	}
	if title == "" {
		title = "Choose files"
	}
	paths, err := dialogs.OpenFile().
		SetTitle(title).
		CanChooseFiles(true).
		CanChooseDirectories(false).
		CanCreateDirectories(true).
		PromptForMultipleSelection()
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(paths))
	for _, path := range paths {
		if path == "" {
			continue
		}
		clean := filepath.Clean(path)
		f.grant(filepath.Dir(clean))
		out = append(out, clean)
	}
	return out, nil
}

// grant makes dir (and everything under it) reachable by the methods below.
func (f *FileBridge) grant(dir string) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.roots {
		if existing == abs {
			return
		}
	}
	f.roots = append(f.roots, abs)
}

// GrantedRoots reports the folders this session may touch (About popover, and a
// cheap way to see why an operation was refused).
func (f *FileBridge) GrantedRoots() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := append([]string(nil), f.roots...)
	sort.Strings(out)
	return out
}

// guard resolves path and refuses it unless it sits inside a granted root.
func (f *FileBridge) guard(path string) (string, error) {
	if strings.TrimSpace(path) == "" {
		return "", errors.New("empty path")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", path, err)
	}
	abs = filepath.Clean(abs)

	f.mu.Lock()
	defer f.mu.Unlock()
	for _, root := range f.roots {
		if within(root, abs) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("%s is outside every folder you chose in this session", path)
}

func within(root, path string) bool {
	r, p := normalizeForCompare(root), normalizeForCompare(path)
	if r == p {
		return true
	}
	sep := string(filepath.Separator)
	if !strings.HasSuffix(r, sep) {
		r += sep
	}
	return strings.HasPrefix(p, r)
}

func normalizeForCompare(path string) string {
	path = filepath.Clean(path)
	if runtime.GOOS == "windows" {
		path = strings.ToLower(path)
	}
	return path
}

// --- file operations ------------------------------------------------------

// Stat returns one entry's metadata.
func (f *FileBridge) Stat(path string) (FileEntry, error) {
	abs, err := f.guard(path)
	if err != nil {
		return FileEntry{}, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return FileEntry{}, err
	}
	return entryOf(info), nil
}

// List enumerates a granted directory.
func (f *FileBridge) List(dir string) ([]FileEntry, error) {
	abs, err := f.guard(dir)
	if err != nil {
		return nil, err
	}
	items, err := os.ReadDir(abs)
	if err != nil {
		return nil, err
	}
	out := make([]FileEntry, 0, len(items))
	for _, item := range items {
		info, err := item.Info()
		if err != nil {
			// A racing delete or a permission edge must not fail the listing:
			// trzsz only uses it to pick a free name.
			out = append(out, FileEntry{Name: item.Name(), Dir: item.IsDir()})
			continue
		}
		out = append(out, entryOf(info))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func entryOf(info fs.FileInfo) FileEntry {
	return FileEntry{Name: info.Name(), Dir: info.IsDir(), Size: info.Size()}
}

// CreateFile makes sure a file exists without touching its contents. trzsz
// asks for a handle with {create:true} before it starts writing; that must not
// truncate anything on its own, because the writer decides when to overwrite.
func (f *FileBridge) CreateFile(path string) error {
	abs, err := f.guard(path)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", abs, err)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", abs, err)
	}
	return file.Close()
}

// Mkdir creates a directory inside a granted root (trzsz walks directory
// transfers this way).
func (f *FileBridge) Mkdir(dir string) error {
	abs, err := f.guard(dir)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", abs, err)
	}
	return nil
}

// ReadRange returns base64 of up to length bytes at offset, for uploads. The
// file is never loaded whole into the webview.
func (f *FileBridge) ReadRange(path string, offset int64, length int) (string, error) {
	abs, err := f.guard(path)
	if err != nil {
		return "", err
	}
	if offset < 0 {
		return "", fmt.Errorf("negative offset %d", offset)
	}
	if length <= 0 {
		return "", nil
	}
	if length > maxChunk {
		length = maxChunk
	}

	file, err := os.Open(abs)
	if err != nil {
		return "", err
	}
	defer file.Close()

	buffer := make([]byte, length)
	n, err := file.ReadAt(buffer, offset)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", fmt.Errorf("read %s@%d: %w", abs, offset, err)
	}
	return base64.StdEncoding.EncodeToString(buffer[:n]), nil
}

// WriteChunk writes one chunk of a download. Offset 0 truncates, so the first
// chunk establishes the file and later ones append at their exact position.
func (f *FileBridge) WriteChunk(path string, offset int64, dataB64 string) error {
	abs, err := f.guard(path)
	if err != nil {
		return err
	}
	if offset < 0 {
		return fmt.Errorf("negative offset %d", offset)
	}
	data, err := base64.StdEncoding.DecodeString(dataB64)
	if err != nil {
		return fmt.Errorf("chunk is not valid base64: %w", err)
	}
	if len(data) > maxChunk {
		return fmt.Errorf("chunk of %d bytes exceeds the %d byte limit", len(data), maxChunk)
	}

	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return fmt.Errorf("create parent of %s: %w", abs, err)
	}
	file, err := os.OpenFile(abs, os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open %s: %w", abs, err)
	}
	defer file.Close()

	if offset == 0 {
		if err := file.Truncate(0); err != nil {
			return fmt.Errorf("truncate %s: %w", abs, err)
		}
	}
	if _, err := file.WriteAt(data, offset); err != nil {
		return fmt.Errorf("write %s@%d: %w", abs, offset, err)
	}
	return nil
}
