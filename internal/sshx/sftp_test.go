package sshx

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ykfq/goterm/internal/sshtest"
)

// newSFTPHarness starts a server whose sftp subsystem is rooted at a fresh
// directory, connects a session and returns both.
func newSFTPHarness(t *testing.T) (*harness, *Session, string) {
	t.Helper()
	root := t.TempDir()
	h := newHarness(t, sshtest.Options{SFTPRoot: root})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)
	return h, sess, root
}

func sftpProgressEvents(r *recorder) []SFTPProgressEvent {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]SFTPProgressEvent, 0, len(r.events))
	for _, e := range r.events {
		if pe, ok := e.payload.(SFTPProgressEvent); ok && e.topic == EventSFTPProgress {
			out = append(out, pe)
		}
	}
	return out
}

func TestSFTPListsTheLoginDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "notes.txt"), []byte("hello"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatalf("mkdir fixture: %v", err)
	}

	h := newHarness(t, sshtest.Options{SFTPRoot: root})
	sess := h.connect(h.config())
	h.expectOutput(sess, readyMarker)

	listing, err := h.mgr.SFTPList(sess.ID(), "")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	byName := map[string]struct {
		dir  bool
		size int64
	}{}
	for _, entry := range listing.Entries {
		byName[entry.Name] = struct {
			dir  bool
			size int64
		}{entry.Dir, entry.Size}
		if !strings.HasPrefix(entry.Path, filepath.ToSlash(listing.Path)) && entry.Path != listing.Path {
			// Listing paths are absolute and joined onto the resolved
			// directory; a relative one would make the panel's next request
			// depend on the server's cwd.
			t.Fatalf("entry %q has path %q, want a child of %q", entry.Name, entry.Path, listing.Path)
		}
	}
	if got, ok := byName["notes.txt"]; !ok || got.dir || got.size != 5 {
		t.Fatalf("notes.txt entry = %+v", got)
	}
	if got, ok := byName["sub"]; !ok || !got.dir {
		t.Fatalf("sub entry = %+v", got)
	}
	// Directories come first: a home directory with many files stays usable.
	if len(listing.Entries) < 2 || !listing.Entries[0].Dir {
		t.Fatalf("entries = %+v, want the directory first", listing.Entries)
	}
}

func TestSFTPUploadAndDownloadRoundTrip(t *testing.T) {
	h, sess, root := newSFTPHarness(t)

	local := filepath.Join(t.TempDir(), "payload.bin")
	payload := bytes.Repeat([]byte("GoTerm-sftp-"), 5000) // ~60 KB, several chunks
	if err := os.WriteFile(local, payload, 0o644); err != nil {
		t.Fatalf("write local: %v", err)
	}

	written, err := h.mgr.SFTPUpload(sess.ID(), []string{local}, filepath.ToSlash(root))
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	if len(written) != 1 || !strings.HasSuffix(written[0], "payload.bin") {
		t.Fatalf("uploaded = %v", written)
	}
	onDisk, err := os.ReadFile(filepath.Join(root, "payload.bin"))
	if err != nil {
		t.Fatalf("read uploaded file: %v", err)
	}
	if !bytes.Equal(onDisk, payload) {
		t.Fatalf("uploaded %d bytes, want %d bytes identical", len(onDisk), len(payload))
	}

	progress := sftpProgressEvents(h.rec)
	if len(progress) == 0 {
		t.Fatal("upload emitted no progress events")
	}
	last := progress[len(progress)-1]
	if last.Direction != "upload" || last.Done != int64(len(payload)) || last.Total != int64(len(payload)) {
		t.Fatalf("last progress = %+v, want a completed upload of %d bytes", last, len(payload))
	}

	back := filepath.Join(t.TempDir(), "back.bin")
	if err := h.mgr.SFTPDownload(sess.ID(), filepath.ToSlash(filepath.Join(root, "payload.bin")), back); err != nil {
		t.Fatalf("download: %v", err)
	}
	roundTrip, err := os.ReadFile(back)
	if err != nil {
		t.Fatalf("read downloaded file: %v", err)
	}
	if !bytes.Equal(roundTrip, payload) {
		t.Fatalf("download changed the bytes: %d vs %d", len(roundTrip), len(payload))
	}

	// The shell on the same connection is untouched by the subsystem channel.
	h.send(sess, "echo after-sftp\r")
	h.expectOutput(sess, "after-sftp")
}

func TestSFTPMkdirRenameAndRemove(t *testing.T) {
	h, sess, root := newSFTPHarness(t)

	dir := filepath.ToSlash(filepath.Join(root, "docs"))
	if err := h.mgr.SFTPMkdir(sess.ID(), dir); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if info, err := os.Stat(filepath.Join(root, "docs")); err != nil || !info.IsDir() {
		t.Fatalf("mkdir did not create a directory: %v", err)
	}

	source := filepath.Join(root, "old.txt")
	if err := os.WriteFile(source, []byte("move me"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	target := filepath.ToSlash(filepath.Join(root, "docs", "new.txt"))
	if err := h.mgr.SFTPRename(sess.ID(), filepath.ToSlash(source), target); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs", "new.txt")); err != nil {
		t.Fatalf("renamed file is not where it should be: %v", err)
	}
	if _, err := os.Stat(source); !os.IsNotExist(err) {
		t.Fatalf("the old name still exists: %v", err)
	}

	if err := h.mgr.SFTPRemove(sess.ID(), target, false); err != nil {
		t.Fatalf("remove file: %v", err)
	}
	// A directory delete only works when empty — a recursive deletion behind a
	// mis-click would be far too expensive.
	if err := h.mgr.SFTPRemove(sess.ID(), dir, true); err != nil {
		t.Fatalf("remove empty directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "docs")); !os.IsNotExist(err) {
		t.Fatalf("directory still exists: %v", err)
	}
}

func TestSFTPRejectsUnknownSession(t *testing.T) {
	h := newHarness(t, sshtest.Options{})
	if _, err := h.mgr.SFTPList("no-such-session", ""); err == nil {
		t.Fatal("listing through a dead session id reported success")
	}
}
