package zmodem

import (
	"bytes"
	"testing"
)

// TestFrameDataCoversBothForms pins the frame contract: a caller reads a
// frame's content from Data whether it arrived as a binary payload or as a
// hex header's arguments. ZFILE's name list is the case that matters.
func TestFrameDataCoversBothForms(t *testing.T) {
	args := []byte("hello.txt\x000 0 0 0 0 0")

	header, err := NewReader(bytes.NewReader(hexHeader(ZFILE, args))).Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(header.Data, args) {
		t.Fatalf("hex header payload %q, want %q", header.Data, args)
	}

	binary, err := NewReader(bytes.NewReader(binFrame(ZFILE, args, ZCRCE))).Next()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(binary.Data, args) {
		t.Fatalf("binary payload %q, want %q", binary.Data, args)
	}

	for _, f := range []*Frame{header, binary} {
		if got := splitNames(f.Data); len(got) != 1 || got[0] != "hello.txt" {
			t.Fatalf("name %q from %v frame", got, f.Hex)
		}
	}
}
