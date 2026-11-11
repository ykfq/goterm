package zmodem

import "io"

// The writer side: frames exactly as lrzsz expects them.

// escapes reports whether a data byte must be written as DLE <b^0x40>.
func escapes(b byte) bool {
	switch b {
	case ZDLE, 0x10, 0x11, 0x13, 0x0d, 0x8d, 0x90, 0x91, 0x93:
		return true
	}
	return false
}

// hexChars are the digits a hex header is built from.
const hexChars = "0123456789abcdef"

// hexHeader renders PAD PAD ZDLE B <type, args, CRC as hex> CR LF. The
// checksum covers the type and the arguments.
func hexHeader(typ byte, args []byte) []byte {
	body := make([]byte, 0, 1+len(args))
	body = append(body, typ)
	body = append(body, args...)
	crc := CRC16(body)
	digits := make([]byte, 0, 2*len(body)+4)
	for _, b := range body {
		digits = append(digits, hexChars[b>>4], hexChars[b&0x0f])
	}
	digits = append(digits,
		hexChars[byte(crc>>12)],
		hexChars[byte(crc>>8)&0x0f],
		hexChars[byte(crc>>4)&0x0f],
		hexChars[byte(crc)&0x0f],
	)
	out := make([]byte, 0, 4+len(digits)+2)
	out = append(out, ZPAD, ZPAD, ZDLE, ZHEX)
	out = append(out, digits...)
	out = append(out, '\r', '\n')
	return out
}

// binFrame renders PAD ZDLE A <type> <escaped data> DLE <kind> DLE h DLE l
// CR LF, where the CRC covers the unescaped payload.
func binFrame(typ byte, data []byte, kind byte) []byte {
	out := make([]byte, 0, len(data)+16)
	out = append(out, ZPAD, ZDLE, ZBIN, typ)
	for _, b := range data {
		if escapes(b) {
			out = append(out, ZDLE, b^0x40)
			continue
		}
		out = append(out, b)
	}
	out = append(out, ZDLE, kind)
	crc := CRC16(data)
	out = append(out, ZDLE, byte(crc>>8)^0x40, ZDLE, byte(crc)^0x40, '\r', '\n')
	return out
}

// writeFrame writes one binary header frame carrying its arguments.
func writeFrame(w io.Writer, typ byte, args ...byte) error {
	_, err := w.Write(binFrame(typ, args, ZCRCE))
	return err
}
