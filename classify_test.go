package main

import (
	"bytes"
	"io"
	"testing"

	"github.com/ykfq/goterm/internal/zmodem"
)

// TestAcceptPicksTheDirection pins the opening-frame decision. Both `rz` and
// `sz` open with ZRQINIT, so args are what tells a real `rz` banner from a
// bare announcement, and a ZRINIT means the peer already answered as receiver.
func TestAcceptPicksTheDirection(t *testing.T) {
	cases := []struct {
		name   string
		frame  []byte
		upload bool
	}{
		{"sz bare ZRQINIT", frameOf(t, zmodem.ZRQINIT, nil), false},
		{"rz ZRQINIT with banner", frameOf(t, zmodem.ZRQINIT, []byte("rz\r\n\x00")), true},
		{"ZRINIT", frameOf(t, zmodem.ZRINIT, []byte{0, 0, 1, 0, 0, 1}), true},
		{"noise", []byte("$ ls\r\n"), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &lrzszPeer{stream: nopCloser{bytes.NewReader(nil)}}
			if got := p.accept(c.frame); got != c.upload {
				t.Fatalf("accept(%q) = %v, want %v", c.frame, got, c.upload)
			}
		})
	}
}

// TestAcceptKeepsTheStreamWhole checks that the peek does not eat the frames
// that follow: the peer must still see everything after the first.
func TestAcceptKeepsTheStreamWhole(t *testing.T) {
	tail := frameOf(t, zmodem.ZFILE, []byte("file\x000 0 0 0 0 0"))
	p := &lrzszPeer{stream: nopCloser{bytes.NewReader(tail)}}
	if p.accept(frameOf(t, zmodem.ZRQINIT, nil)) {
		t.Fatal("a bare ZRQINIT was classified as an upload")
	}
	got, err := io.ReadAll(p.stream)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, tail) {
		t.Fatalf("stream after accept = %q, want %q", got, tail)
	}
}

// TestSendAfterAskSwitchesToDownload checks the other half: when an upload was
// chosen but the peer turns out to be an `sz`, the sender reports the mismatch
// instead of forcing ZRINIT at it.
func TestSendAfterAskSwitchesToDownload(t *testing.T) {
	in := bytes.NewReader(frameOf(t, zmodem.ZRQINIT, nil))
	var out bytes.Buffer
	s := &zmodem.Sender{In: in, Out: &out}
	err := s.SendAfterAsk([]zmodem.File{{Name: "x", Data: []byte("x")}})
	if err != zmodem.ErrPeerWantsDownload {
		t.Fatalf("SendAfterAsk err = %v, want ErrPeerWantsDownload", err)
	}
	if out.Len() == 0 {
		t.Fatal("SendAfterAsk sent nothing")
	}
}

// frameOf builds a hex header without depending on the package's encoder:
// sz announces and rz answers with the same shape this peer writes, so the
// test drives the real wire format instead of a copy of it.
func frameOf(t *testing.T, typ byte, args []byte) []byte {
	t.Helper()
	b := []byte{'*', '*', 0x18, 'B'}
	body := append([]byte{typ}, args...)
	for _, c := range body {
		b = append(b, "0123456789abcdef"[c>>4], "0123456789abcdef"[c&0x0f])
	}
	crc := zmodem.CRC16(body)
	for _, c := range []byte{byte(crc >> 8), byte(crc)} {
		b = append(b, "0123456789abcdef"[c>>4], "0123456789abcdef"[c&0x0f])
	}
	return append(b, '\r', '\n')
}

type nopCloser struct{ io.Reader }

func (nopCloser) Close() error { return nil }
