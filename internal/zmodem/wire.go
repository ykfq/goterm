package zmodem

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

// offsetBytes is a header word as lrzsz writes it: four bytes, big endian.
func offsetBytes(off uint32) []byte {
	return []byte{byte(off >> 24), byte(off >> 16), byte(off >> 8), byte(off)}
}

// readOffset is the same word coming the other way.
func readOffset(b []byte) uint32 {
	if len(b) < 4 {
		return 0
	}
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

// dataFrame renders one binary data frame in the form lrzsz writes:
//
//	PAD ZDLE A <type> <offset:4> <hdr crc:2>
//	<escaped payload> DLE <kind> <crc:2>
//	CR LF
//
// Two things here matter to anything but our own code. The four offset bytes
// are part of every binary frame — in a ZDATA frame they say where in the file
// the payload belongs — so a frame written without them reads to a peer as a
// file whose data begins four bytes early. And the header carries its own
// checksum before the payload starts, so a peer that does not find one there
// takes the first two payload bytes for it and calls the frame damaged.
//
// Both checksums cover the bytes themselves, not the escaped form: lrzsz
// updates its checksum with each byte as it picks it up, and only then decides
// whether the character has to be doubled with a ZDLE. A sender that signs the
// escaped stream disagrees with every lrzsz build it meets.
func dataFrame(typ byte, offset uint32, data []byte, kind byte) []byte {
	word := escapeForCRC(offsetBytes(offset))
	body := escapeForCRC(data)
	headCRC := CRC16(offsetBytes(offset))
	dataCRC := CRC16(data)

	out := make([]byte, 0, len(word)+len(body)+20)
	out = append(out, ZPAD, ZDLE, ZBIN, typ)
	out = append(out, word...)
	out = append(out, byte(headCRC>>8), byte(headCRC))
	out = append(out, body...)
	out = append(out, ZDLE, kind, byte(dataCRC>>8), byte(dataCRC), '\r', '\n')
	return out
}

// escapeForCRC is the transmitted form of a run of bytes: the DLEs a peer has
// to read, whether or not they take part in a checksum.
func escapeForCRC(body []byte) []byte {
	out := make([]byte, 0, len(body))
	for _, b := range body {
		if escapes(b) {
			out = append(out, ZDLE, b^0x40)
			continue
		}
		out = append(out, b)
	}
	return out
}
