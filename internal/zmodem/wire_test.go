package zmodem

import (
	"bytes"
	"testing"
)

// TestBinaryFrameIsTheShapeLrzszWrites pins the frame layout, because every
// byte of it is something a peer checks: the four-byte offset that says where
// the payload belongs, the header's own checksum ahead of the payload, the
// escaping, and a subframe checksum taken over the bytes rather than over
// their escaped spelling.
func TestBinaryFrameIsTheShapeLrzszWrites(t *testing.T) {
	payload := []byte{0x01, ZDLE, 0x0d}
	frame := dataFrame(ZDATA, 1, payload, ZCRCQ)

	if frame[0] != ZPAD || frame[1] != ZDLE || frame[2] != ZBIN || frame[3] != ZDATA {
		t.Fatalf("frame does not open with PAD ZDLE A ZDATA: %q", frame[:4])
	}
	word := frame[4:8]
	if !bytes.Equal(word, []byte{0, 0, 0, 1}) {
		t.Fatalf("the header word is the offset, big endian: %v", word)
	}
	if got, want := uint16(frame[8])<<8|uint16(frame[9]), CRC16(word); got != want {
		t.Fatalf("header checksum %04x, want %04x — a peer reads the first two payload bytes as its checksum without one", got, want)
	}
	body := frame[10:]
	want := []byte{0x01, ZDLE, ZDLE ^ 0x40, ZDLE, 0x0d ^ 0x40}
	if !bytes.HasPrefix(body, want) {
		t.Fatalf("payload is escaped as %v, got %v", want, body[:len(want)])
	}
	if body[len(want)] != ZDLE || body[len(want)+1] != ZCRCQ {
		t.Fatalf("the subframe trailer does not follow the payload: %q", body[len(want):len(want)+2])
	}
	tail := body[len(want)+2:]
	if got, want := uint16(tail[0])<<8|uint16(tail[1]), CRC16(payload); got != want {
		t.Fatalf("subframe checksum %04x, want %04x: lrzsz folds each byte in before escaping it", got, want)
	}
	if tail[2] != '\r' || tail[3] != '\n' {
		t.Fatalf("frame does not end with CR LF: %q", tail[2:])
	}

	// The same bytes coming back: offset, trailer and payload are each read
	// where a peer puts them.
	parsed, err := NewReader(bytes.NewReader(frame)).Next()
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if parsed.Type != ZDATA || parsed.Offset != 1 || parsed.Kind != ZCRCQ {
		t.Fatalf("parsed type=%d offset=%d kind=%d", parsed.Type, parsed.Offset, parsed.Kind)
	}
	if !bytes.Equal(parsed.Data, payload) {
		t.Fatalf("payload %v, want %v", parsed.Data, payload)
	}
}

// TestHeaderCarriesItsPositionForBothForms: a position travels as a hex header
// (ZEOF's count, ZRPOS's restart point) or a binary one, and both must arrive
// as the same number.
func TestHeaderCarriesItsPositionForBothForms(t *testing.T) {
	hexed, err := NewReader(bytes.NewReader(hexHeader(ZEOF, offsetBytes(4096)))).Next()
	if err != nil {
		t.Fatal(err)
	}
	if hexed.Offset != 4096 {
		t.Fatalf("ZEOF from a hex header reads %d, want 4096 — a short file looks complete when it is zero", hexed.Offset)
	}
	binary, err := NewReader(bytes.NewReader(dataFrame(ZEOF, 4096, nil, ZCRCE))).Next()
	if err != nil {
		t.Fatal(err)
	}
	if binary.Offset != 4096 {
		t.Fatalf("ZEOF from a binary header reads %d, want 4096", binary.Offset)
	}
}
