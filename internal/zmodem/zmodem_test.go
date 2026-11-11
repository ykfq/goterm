package zmodem

import (
	"bytes"
	"testing"
)

// TestHeaderRoundTrip checks the frame shapes the two halves rely on.
func TestHeaderRoundTrip(t *testing.T) {
	frame := hexHeader(ZRINIT, []byte{0, 0, 1, 0, 0, 1})
	r := NewReader(bytes.NewReader(frame))
	f, err := r.Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if f.Type != ZRINIT || len(f.Args) != 6 || f.Args[2] != 1 {
		t.Fatalf("got type=%d args=% X", f.Type, f.Args)
	}
}

// TestBinaryFrameRoundTrip carries every escape byte and both CRC look-alikes.
func TestBinaryFrameRoundTrip(t *testing.T) {
	data := make([]byte, 0, 512)
	for i := 0; i < 256; i++ {
		data = append(data, byte(i))
	}
	for _, n := range []int{0, 1, 2, 57, 128, 256} {
		r := NewReader(bytes.NewReader(binFrame(ZDATA, data[:n], ZCRCE)))
		f, err := r.Next()
		if err != nil {
			t.Fatalf("n=%d: %v", n, err)
		}
		if f.Type != ZDATA || !bytes.Equal(f.Data, data[:n]) {
			t.Fatalf("n=%d: payload differs", n)
		}
	}
}

// TestNoiseIsSkipped feeds line noise ahead of a frame.
func TestNoiseIsSkipped(t *testing.T) {
	frame := append([]byte("$ rz -b\r\nnoise"), hexHeader(ZRINIT, nil)...)
	f, err := NewReader(bytes.NewReader(frame)).Next()
	if err != nil {
		t.Fatalf("next: %v", err)
	}
	if f.Type != ZRINIT {
		t.Fatalf("type %d", f.Type)
	}
}

