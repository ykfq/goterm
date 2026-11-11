package zmodem

import (
	"bytes"
	"testing"
)

// TestHexHeaderCarriesItsArguments pins the header bytes down: the type, the
// arguments and the CRC must all land on the wire, in that order.
func TestHexHeaderCarriesItsArguments(t *testing.T) {
	args := []byte("hello.txt\x000 0 0 0 0 0")
	meta := make([]byte, 0, len("hello.txt")+16)
	meta = append(meta, "hello.txt"...)
	meta = append(meta, 0)
	meta = append(meta, "0 0 0 0 0 0"...)
	frame := hexHeader(ZFILE, meta)
	wantDigits := 2 * (1 + len(args) + 2)
	if got := len(frame) - 6; got != wantDigits {
		t.Fatalf("header carries %d digits, want %d (frame % X)", got, wantDigits, frame)
	}
	r := NewReader(bytes.NewReader(frame))
	f, err := r.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if f.Type != ZFILE {
		t.Fatalf("type %d", f.Type)
	}
	if !bytes.Equal(f.Args, args) {
		t.Fatalf("args %q, want %q", f.Args, args)
	}
}
