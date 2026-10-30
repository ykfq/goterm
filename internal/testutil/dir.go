// Package testutil holds harness helpers shared by tests.
//
// It is imported only from _test.go files. It exists because testing.T.TempDir's
// cleanup is unconditional: on Windows, Defender opens a just-written state file
// for scanning, RemoveAll answers "the directory is not empty" for a few dozen
// milliseconds, and a test that passed fails in teardown. Retrying fixes the
// report without weakening anything the tests actually assert.
package testutil

import (
	"os"
	"testing"
	"time"
)

// TempDir is t.TempDir with a cleanup that forgives a transient Windows hold.
//
// The directory is removed in a few attempts; if it still refuses, it is left
// behind in the OS temp directory rather than failing an otherwise passing test.
func TempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "goterm-"+t.Name()+"-")
	if err != nil {
		t.Fatalf("temp dir: %v", err)
	}
	t.Cleanup(func() {
		for attempt := 0; attempt < 10; attempt++ {
			if err := os.RemoveAll(dir); err == nil {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	})
	return dir
}
