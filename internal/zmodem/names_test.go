package zmodem

import (
	"bytes"
	"testing"
)

// TestSplitNamesKeepsTheName pins the ZFILE name parse: the name is what comes
// before the first NUL, not the last.
func TestSplitNamesKeepsTheName(t *testing.T) {
	got := splitNames([]byte("hello.txt\x000 0 0 0 0 0"))
	if len(got) != 1 || got[0] != "hello.txt" {
		t.Fatalf("splitNames = %q", got)
	}
}

// TestHexHeaderDecodesName checks the header survives a round trip with a NUL
// in the middle, which is exactly the ZFILE shape.
func TestHexHeaderDecodesName(t *testing.T) {
	args := []byte("hello.txt\x000 0 0 0 0 0")
	f, err := NewReader(bytes.NewReader(hexHeader(ZFILE, args))).Next()
	if err != nil {
		t.Fatal(err)
	}
	if got := splitNames(f.Args); len(got) != 1 || got[0] != "hello.txt" {
		t.Fatalf("name = %q (args %q)", got, f.Args)
	}
}
