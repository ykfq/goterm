// Package atomicfile writes small state files (host list, known hosts, sealed
// secrets) so a crash mid-write cannot leave a half-written file behind.
package atomicfile

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// renameAttempts bounds how long we fight a transient denial: five waits of
// 20/40/80/160/320ms, so the worst case is about a second and then we report
// the error instead of hanging the UI.
var renameAttempts = []time.Duration{
	20 * time.Millisecond,
	40 * time.Millisecond,
	80 * time.Millisecond,
	160 * time.Millisecond,
	320 * time.Millisecond,
}

// Write stores data at path atomically: write to a sibling temp file, fsync-ish
// close, then rename over the target.
//
// The rename is retried. On Windows, Defender or the search indexer commonly
// opens a just-created file for scanning within microseconds, and MoveFileEx
// answers ERROR_ACCESS_DENIED until it lets go. That is a few dozen
// milliseconds, not a real failure, and treating it as one made known_hosts
// saves fail intermittently.
func Write(path string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".*.tmp")
	if err != nil {
		return fmt.Errorf("create temp in %s: %w", dir, err)
	}
	tmpName := tmp.Name()
	defer func() {
		if _, statErr := os.Stat(tmpName); statErr == nil {
			_ = os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write %s: %w", tmpName, err)
	}
	if err := tmp.Sync(); err != nil {
		// Some filesystems (and some antivirus filters) refuse FlushFileBuffers
		// on a file that is still open for writing; the data is already handed
		// to the OS, so this is not worth failing the save over.
		_ = err
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close %s: %w", tmpName, err)
	}
	if err := os.Chmod(tmpName, perm); err != nil {
		_ = err // no POSIX permissions on Windows
	}

	last := error(nil)
	for i := 0; i <= len(renameAttempts); i++ {
		if last = os.Rename(tmpName, path); last == nil {
			return nil
		}
		if i == len(renameAttempts) {
			break
		}
		time.Sleep(renameAttempts[i])
	}
	return fmt.Errorf("replace %s: %w", path, last)
}
