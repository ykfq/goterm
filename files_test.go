package main

import (
	"encoding/base64"
	"github.com/ykfq/goterm/internal/testutil"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newGrantedBridge(t *testing.T) (*FileBridge, string) {
	t.Helper()
	dir := testutil.TempDir(t)
	bridge := NewFileBridge()
	bridge.grant(dir)
	return bridge, dir
}

func TestFileBridgeRefusesPathsOutsideGrantedRoots(t *testing.T) {
	bridge := NewFileBridge()
	target := filepath.Join(testutil.TempDir(t), "secret.txt")

	operations := map[string]func() error{
		"stat":  func() error { _, err := bridge.Stat(target); return err },
		"list":  func() error { _, err := bridge.List(filepath.Dir(target)); return err },
		"mkdir": func() error { return bridge.Mkdir(filepath.Join(filepath.Dir(target), "x")) },
		"read":  func() error { _, err := bridge.ReadRange(target, 0, 16); return err },
		"write": func() error {
			return bridge.WriteChunk(target, 0, base64.StdEncoding.EncodeToString([]byte("nope")))
		},
	}
	for name, run := range operations {
		if err := run(); err == nil {
			t.Fatalf("%s was allowed without the user picking a folder", name)
		} else if !strings.Contains(err.Error(), "outside") {
			t.Fatalf("%s error = %v, want it to explain the scope", name, err)
		}
	}
	if _, err := os.Stat(target); !os.IsNotExist(err) {
		t.Fatal("the refused write still created a file")
	}
}

func TestFileBridgeWriteReadRoundTrip(t *testing.T) {
	bridge, dir := newGrantedBridge(t)
	target := filepath.Join(dir, "download.bin")

	// What trzsz does: sequential chunks, starting at offset 0.
	first := []byte("hello ")
	second := make([]byte, 300000)
	for i := range second {
		second[i] = byte(i * 13)
	}
	if err := bridge.WriteChunk(target, 0, base64.StdEncoding.EncodeToString(first)); err != nil {
		t.Fatalf("first chunk: %v", err)
	}
	if err := bridge.WriteChunk(target, int64(len(first)), base64.StdEncoding.EncodeToString(second)); err != nil {
		t.Fatalf("second chunk: %v", err)
	}

	written, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	want := append(append([]byte(nil), first...), second...)
	if len(written) != len(want) {
		t.Fatalf("file is %d bytes, want %d", len(written), len(want))
	}
	for i := range want {
		if written[i] != want[i] {
			t.Fatalf("byte %d = %d, want %d", i, written[i], want[i])
		}
	}

	info, err := bridge.Stat(target)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size != int64(len(want)) || info.Dir || info.Name != "download.bin" {
		t.Fatalf("stat = %+v", info)
	}

	// Offset-based reads are the upload path.
	got, err := bridge.ReadRange(target, int64(len(first)), 1024)
	if err != nil {
		t.Fatalf("read range: %v", err)
	}
	raw, err := base64.StdEncoding.DecodeString(got)
	if err != nil {
		t.Fatalf("read range is not base64: %v", err)
	}
	if string(raw) != string(second[:1024]) {
		t.Fatal("read range returned the wrong bytes")
	}

	// Re-writing at offset 0 must truncate, not append.
	if err := bridge.WriteChunk(target, 0, base64.StdEncoding.EncodeToString([]byte("short"))); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	again, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read after overwrite: %v", err)
	}
	if string(again) != "short" {
		t.Fatalf("after offset-0 write the file is %q, want %q", again, "short")
	}
}

func TestFileBridgeListingAndMkdir(t *testing.T) {
	bridge, dir := newGrantedBridge(t)

	if err := bridge.Mkdir(filepath.Join(dir, "sub", "deeper")); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := bridge.WriteChunk(filepath.Join(dir, "a.txt"), 0, base64.StdEncoding.EncodeToString([]byte("x"))); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	entries, err := bridge.List(dir)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]FileEntry{}
	for _, entry := range entries {
		seen[entry.Name] = entry
	}
	if !seen["sub"].Dir {
		t.Fatalf("sub/ missing or not a directory: %+v", entries)
	}
	if seen["a.txt"].Dir || seen["a.txt"].Size != 1 {
		t.Fatalf("a.txt = %+v", seen["a.txt"])
	}
}

func TestFileBridgeRejectsTraversal(t *testing.T) {
	bridge, dir := newGrantedBridge(t)
	outside := filepath.Join(filepath.Dir(dir), "escaped.txt")

	// This is what a hostile server can put in a transfer's NAME frame.
	err := bridge.WriteChunk(outside, 0, base64.StdEncoding.EncodeToString([]byte("pwn")))
	if err == nil {
		t.Fatal("a path dot-dotting out of the granted folder was written")
	}
	if _, statErr := os.Stat(outside); !os.IsNotExist(statErr) {
		t.Fatal("the traversal write actually created the file")
	}
}

func TestFileBridgeCapsChunkSizes(t *testing.T) {
	bridge, dir := newGrantedBridge(t)
	target := filepath.Join(dir, "big.bin")
	huge := make([]byte, maxChunk+1)

	if err := bridge.WriteChunk(target, 0, base64.StdEncoding.EncodeToString(huge)); err == nil {
		t.Fatal("an oversized chunk was accepted")
	}
	if err := bridge.WriteChunk(target, 0, "not base64 !!"); err == nil {
		t.Fatal("invalid base64 was accepted")
	}
	if err := bridge.WriteChunk(target, -1, ""); err == nil {
		t.Fatal("a negative offset was accepted")
	}

	// Reading near EOF returns what exists rather than an error.
	if err := bridge.WriteChunk(target, 0, base64.StdEncoding.EncodeToString([]byte("abc"))); err != nil {
		t.Fatalf("seed: %v", err)
	}
	got, err := bridge.ReadRange(target, 1, 100)
	if err != nil {
		t.Fatalf("read past EOF: %v", err)
	}
	raw, _ := base64.StdEncoding.DecodeString(got)
	if string(raw) != "bc" {
		t.Fatalf("read past EOF = %q, want %q", raw, "bc")
	}
}

func TestFileBridgeNeedsAWindowForDialogs(t *testing.T) {
	bridge := NewFileBridge()
	if _, err := bridge.ChooseDirectory("x"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("ChooseDirectory without a context = %v", err)
	}
	if _, err := bridge.ChooseFiles("x"); err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Fatalf("ChooseFiles without a context = %v", err)
	}
}

func TestWithinHandlesCaseAndTrailingSeparators(t *testing.T) {
	root := filepath.Join(testutil.TempDir(t), "Downloads")
	if !within(root, filepath.Join(root, "a.txt")) {
		t.Fatal("a direct child is not considered inside")
	}
	if !within(root+string(filepath.Separator), root) {
		t.Fatal("a trailing separator broke containment")
	}
	if within(root, root+"-evil") {
		t.Fatal("a sibling with a shared prefix was treated as a child")
	}
	if runtime.GOOS == "windows" && !within(root, strings.ToUpper(filepath.Join(root, "A.TXT"))) {
		t.Fatal("Windows paths should compare case-insensitively")
	}
}
