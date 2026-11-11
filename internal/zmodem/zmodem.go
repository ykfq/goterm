// Package zmodem is the lrzsz wire protocol in the two halves a terminal
// needs: a sender that talks to a remote `rz` and a receiver that talks to a
// remote `sz`. It is a small, tolerant implementation: it understands the
// binary and hex header forms, CRC and plain-text frames, and it treats
// anything it cannot parse as line noise between frames.
package zmodem

import (
	"errors"
	"io"
	"time"
)

// Wire bytes.
const (
	ZDLE  = 0x18
	ZPAD  = '*'
	ZHEX  = 'B'
	ZBIN  = 'A'
	ZBIN32 = 'C'
	ZNAK  = 0x15
	ZACK  = 6
)

// Header and frame types, numbered as lrzsz numbers them.
const (
	ZRQINIT = 0
	ZRINIT  = 1
	ZFILE   = 2
	ZDATA   = 3
	ZEOF    = 4
	ZFIN    = 8
	ZRPOS   = 9
	ZSKIP   = 10
	ZNAK2   = 12
	ZABORT  = 13
	ZCLOSE  = 21
	ZCRC    = 22
)

// Datasubframe types.
const (
	ZCRCE = 'h'
	ZCRCG = 'i'
	ZCRCQ = 'j'
	ZCRW  = 'k'
)

// ErrCanceled reports that the peer gave up or the user closed the dialog.
var ErrCanceled = errors.New("zmodem: canceled")

// errNotFrame says the bytes at the front of the stream are not a frame. The
// caller keeps reading; a real peer skips line noise between frames.
var errNotFrame = errors.New("zmodem: not a frame")

// errTimeout reports a silent peer.
var errTimeout = errors.New("zmodem: timed out waiting for the peer")

// Progress reports one file's share of a transfer. count is the number of
// files in the batch and index is the one in flight.
type Progress func(name string, done, size int64, index, count int)

func noProgress(string, int64, int64, int, int) {}

// CRC16 is CRC-16/XMODEM, the checksum ZMODEM headers and frames carry. It is
// table driven: the bit-at-a-time form is what ZMODEM's C code uses, but a
// table keeps the checksum obvious and fast in one pass.
func CRC16(data []byte) uint16 {
	crc := uint16(0)
	for _, b := range data {
		crc = crc<<8 ^ crc16Table[byte(crc>>8)^b]
	}
	return crc
}

// crc16Table is filled once from the polynomial, so the table cannot disagree
// with the definition.
var crc16Table = func() [256]uint16 {
	var t [256]uint16
	for i := range t {
		crc := uint16(i) << 8
		for bit := 0; bit < 8; bit++ {
			if crc&0x8000 != 0 {
				crc = crc<<1 ^ 0x1021
			} else {
				crc <<= 1
			}
		}
		t[i] = crc
	}
	return t
}()

// Reader walks a byte stream frame by frame. A stream without deadlines is
// wrapped by callers that need timeouts; the reader itself only reads.
type Reader struct {
	r   io.Reader
	buf []byte // look-ahead, never dropped
	err error  // the stream's terminal error, once it has one
}

// NewReader returns a Reader over r.
func NewReader(r io.Reader) *Reader { return &Reader{r: r} }

// fill reads until at least n bytes are buffered or the stream ends. A read
// that returns bytes together with io.EOF is not the end of anything: the
// stream is only finished once a read brings no bytes at all.
func (r *Reader) fill(n int) bool {
	for len(r.buf) < n && r.err == nil {
		want := n - len(r.buf)
		if want < 4096 {
			want = 4096
		}
		chunk := make([]byte, want)
		got, err := r.r.Read(chunk)
		r.buf = append(r.buf, chunk[:got]...)
		if err != nil && got == 0 {
			r.err = err
		}
	}
	return len(r.buf) >= n
}

// consume drops n buffered bytes.
func (r *Reader) consume(n int) {
	if n > len(r.buf) {
		n = len(r.buf)
	}
	r.buf = r.buf[n:]
}

// readByte consumes one byte.
func (r *Reader) readByte() (byte, error) {
	if !r.fill(1) {
		if r.err != nil {
			return 0, r.err
		}
		return 0, io.ErrUnexpectedEOF
	}
	b := r.buf[0]
	r.buf = r.buf[1:]
	return b, nil
}

// Frame is one frame read off the wire.
type Frame struct {
	Type byte   // ZRINIT, ZFILE, ZDATA...
	Args []byte // header arguments, when the frame has them
	Data []byte // binary payload, for ZDATA, ZFILE and ZCRCG
	Hex  bool   // a hex header rather than a binary one
}

// Next returns the next frame, skipping line noise. A binary frame's payload
// is read whole, so a ZDATA frame arrives complete. Only a stream that runs
// out with no frame in hand reports an error.
func (r *Reader) Next() (*Frame, error) {
	for {
		f, err := r.tryFrame()
		if errors.Is(err, errNotFrame) {
			continue
		}
		if err != nil && r.err != nil {
			return nil, r.err
		}
		return f, err
	}
}

// tryFrame parses one frame from the front of the stream. Line noise is
// dropped one byte at a time until a frame start is in sight.
func (r *Reader) tryFrame() (*Frame, error) {
	// Hunt for the PAD that starts a frame, dropping the bytes before it.
	pos := -1
	for {
		if !r.fill(1) {
			return nil, r.streamErr("no frame before end of stream")
		}
		for i := 0; i < len(r.buf); i++ {
			if r.buf[i] == ZPAD {
				pos = i
				break
			}
		}
		if pos >= 0 {
			break
		}
		r.consume(len(r.buf))
	}
	r.consume(pos)
	// PAD [PAD] ZDLE <kind>
	if !r.fill(3) {
		return nil, r.streamErr("truncated frame start")
	}
	n := 1
	if r.buf[1] == ZPAD {
		if !r.fill(3) || n+1 >= len(r.buf) {
			return nil, r.streamErr("truncated frame start")
		}
		n = 2
	}
	if r.buf[n] != ZDLE {
		r.consume(1)
		return nil, errNotFrame
	}
	if !r.fill(n + 2) {
		return nil, r.streamErr("truncated frame start")
	}
	kind := r.buf[n+1]
	r.consume(n + 2)
	switch kind {
	case ZHEX:
		return r.hexFrame()
	case ZBIN:
		typ, err := r.readByte()
		if err != nil {
			return nil, err
		}
		data, err := r.subpackets()
		if err != nil {
			return nil, err
		}
		return &Frame{Type: typ, Data: data}, nil
	case ZBIN32:
		typ, err := r.readByte()
		if err != nil {
			return nil, err
		}
		if err := r.skipCRC32(); err != nil {
			return nil, err
		}
		data, err := r.subpackets32()
		if err != nil {
			return nil, err
		}
		return &Frame{Type: typ, Data: data}, nil
	}
	return nil, errNotFrame
}

// hexFrame reads PAD PAD ZDLE B <hex digits> CR [LF]. Each pair of digits is
// one byte: the type, then the arguments, then the two CRC bytes.
func (r *Reader) hexFrame() (*Frame, error) {
	typ, args, want, err := r.readHexHeader()
	if err != nil {
		return nil, err
	}
	body := make([]byte, 0, 1+len(args))
	body = append(body, typ)
	body = append(body, args...)
	if CRC16(body) != want {
		return nil, errNotFrame
	}
	// A header's arguments are also its payload: a caller that reads a frame as
	// data (ZFILE's name list) must not have to know which form arrived.
	return &Frame{Type: typ, Args: args, Data: args, Hex: true}, nil
}

// readHexHeader parses the digits of a hex header without checking the CRC.
func (r *Reader) readHexHeader() (typ byte, args []byte, want uint16, err error) {
	var digits []byte
	for {
		b, err := r.readByte()
		if err != nil {
			return 0, nil, 0, r.streamErr("truncated hex header")
		}
		if b == '\r' {
			if r.fill(1) && r.buf[0] == '\n' {
				r.consume(1)
			}
			break
		}
		v, ok := nibble(b)
		if !ok {
			return 0, nil, 0, errNotFrame
		}
		digits = append(digits, v)
		if len(digits) > 2*256+4 {
			return 0, nil, 0, errNotFrame
		}
	}
	if len(digits)%2 != 0 || len(digits) < 6 {
		return 0, nil, 0, errNotFrame
	}
	n := len(digits) / 2
	raw := make([]byte, n)
	for i := 0; i < n; i++ {
		raw[i] = digits[2*i]<<4 | digits[2*i+1]
	}
	typ = raw[0]
	want = uint16(raw[n-2])<<8 | uint16(raw[n-1])
	args = append([]byte{}, raw[1:n-2]...)
	return typ, args, want, nil
}
// subpackets reads a ZBIN data frame: escaped bytes up to DLE <kind> DLE h
// DLE l, repeated until a kind ends the frame.
func (r *Reader) subpackets() ([]byte, error) {
	var out []byte
	for {
		chunk, kind, err := r.subpacket(false)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if kind == ZCRCE {
			// ZCRCE ends a frame; the terminal CR LF is already behind it.
			return out, r.trailingCR()
		}
	}
}

// subpacket reads escaped data up to and including one DLE <kind> DLE h DLE l.
func (r *Reader) subpacket(crc32 bool) ([]byte, byte, error) {
	var raw []byte
	for {
		b, err := r.readByte()
		if err != nil {
			return nil, 0, r.streamErr("frame ends inside its data")
		}
		if b != ZDLE {
			raw = append(raw, b)
			continue
		}
		// DLE <kind> [DLE hi [DLE lo]], where the CRC bytes are XOR 0x40.
		typ, err := r.readByte()
		if err != nil {
			return nil, 0, r.streamErr("frame ends inside its data")
		}
		if typ == ZDLE {
			// A payload byte 0x18, escaped as DLE DLE.
			raw = append(raw, ZDLE)
			continue
		}
		switch typ {
		case ZCRCE, ZCRCG, ZCRCQ, ZCRW:
			if err := r.checkCRC(raw, crc32); err != nil {
				return nil, 0, err
			}
			return raw, typ, nil
		}
		// An escaped byte: DLE then the value XOR 0x40.
		raw = append(raw, typ^0x40)
	}
}

// checkCRC consumes the CRC that follows DLE <kind>, then the CR LF pair for
// ZCRCE frames.
func (r *Reader) checkCRC(data []byte, _ bool) error {
	hi, err := r.readCRCByte()
	if err != nil {
		return err
	}
	lo, err := r.readCRCByte()
	if err != nil {
		return err
	}
	want := uint16(hi)<<8 | uint16(lo)
	if CRC16(data) != want {
		return errors.New("zmodem: frame CRC mismatch")
	}
	return nil
}

// readCRCByte reads one CRC byte, undoing the DLE escape when present.
func (r *Reader) readCRCByte() (byte, error) {
	b, err := r.readByte()
	if err != nil {
		return 0, r.streamErr("frame ends inside its CRC")
	}
	if b == ZDLE {
		b, err = r.readByte()
		if err != nil {
			return 0, r.streamErr("frame ends inside its CRC")
		}
		b ^= 0x40
	}
	return b, nil
}

// trailingCR eats the CR [LF] that follows a frame terminator, when present.
func (r *Reader) trailingCR() error {
	if !r.fill(1) {
		return nil
	}
	if r.buf[0] == '\r' {
		r.consume(1)
		if r.fill(1) && r.buf[0] == '\n' {
			r.consume(1)
		}
		return nil
	}
	if r.buf[0] == '\n' {
		r.consume(1)
	}
	return nil
}

func (r *Reader) skipCRC32() error {
	for i := 0; i < 4; i++ {
		if _, err := r.readCRCByte(); err != nil {
			return err
		}
	}
	return nil
}

func (r *Reader) subpackets32() ([]byte, error) {
	var out []byte
	for {
		chunk, kind, err := r.subpacket(true)
		if err != nil {
			return nil, err
		}
		out = append(out, chunk...)
		if kind == ZCRCE {
			return out, r.trailingCR()
		}
	}
}

func (r *Reader) streamErr(msg string) error {
	if r.err != nil {
		return r.err
	}
	return errors.New("zmodem: " + msg)
}

func nibble(c byte) (byte, bool) {
	switch {
	case c >= '0' && c <= '9':
		return c - '0', true
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10, true
	case c >= 'A' && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// deadlineReader applies a read timeout to a stream that has no deadline of
// its own, which is how the SSH channel arrives.
type deadlineReader struct {
	r       io.Reader
	timeout time.Duration
}

func (d *deadlineReader) Read(b []byte) (int, error) {
	type result struct {
		n   int
		err error
	}
	ch := make(chan result, 1)
	go func() {
		n, err := d.r.Read(b)
		ch <- result{n, err}
	}()
	if d.timeout <= 0 {
		res := <-ch
		return res.n, res.err
	}
	select {
	case res := <-ch:
		return res.n, res.err
	case <-time.After(d.timeout):
		return 0, errTimeout
	}
}
