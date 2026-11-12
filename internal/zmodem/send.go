package zmodem

import (
	"errors"
	"fmt"
	"time"
)

// handshake gets the peer ready: it answers a ZRQINIT with ZRINIT, sends its
// own ZRQINIT when the peer waits for one, and returns once a ZRINIT arrives.
// It is also what `sz` opens with: the announcer sends ZRQINIT and waits, so a
// peer that answers with ZRINIT is ready and a peer that answers with a bare
// ZRQINIT is itself an `sz` asking to send.
func (s *Sender) handshake(r *Reader) error {
	return s.shake(r, true)
}

// handshakeAfterAsk opens an upload that starts by asking: the caller sends
// ZRQINIT first, so a peer that answers bare is announcing a download.
func (s *Sender) handshakeAfterAsk(r *Reader) error {
	return s.shake(r, false)
}

func (s *Sender) shake(r *Reader, announce bool) error {
	if announce {
		if _, err := s.Out.Write(hexHeader(ZRQINIT, nil)); err != nil {
			return err
		}
	}
	for {
		f, err := r.Next()
		if err != nil {
			return err
		}
		switch f.Type {
		case ZRINIT:
			return nil
		case ZRQINIT:
			// Announcing: this is `rz` waiting, so answer and continue. Asking:
			// this is `sz` announcing, so there is nothing to upload to.
			withBanner := len(f.Args) > 0
			if !announce && !withBanner {
				return ErrPeerWantsDownload
			}
			// The answer says which side we are: real lrzsz peers look at the ZRINIT
			// args to tell "this peer wants to send" from "this peer wants to
			// receive", and two peers that both send bare ZRINITs wait forever.
			args := rinitArgs()
			if !announce {
				args = rzRinitArgs()
			}
			if _, err := s.Out.Write(hexHeader(ZRINIT, args)); err != nil {
				return err
			}
		case ZFIN, ZABORT:
			return ErrCanceled
		}
	}
}

// unused keeps the import list stable while probing.
var _ = time.Second

// sendFile announces one file, waits for the offset the peer wants, streams
// the bytes and closes with the count it actually sent.
//
// The descriptor is what lrzsz plans from: mode in hex, the byte count in
// decimal, then the file number and mtime. "0 0" tells a real rz that the file
// is empty, and a ZEOF that contradicts the count it counted is a file rz
// throws away.
func (s *Sender) sendFile(r *Reader, f File, index, count, window int) error {
	meta := make([]byte, 0, len(f.Name)+32)
	meta = append(meta, f.Name...)
	meta = append(meta, 0)
	meta = append(meta, fmt.Sprintf("600 %d 1 0 0 0", len(f.Data))...)
	if _, err := s.Out.Write(hexHeader(ZFILE, meta)); err != nil {
		return err
	}
	pos, err := s.wantPosition(r, f)
	if err != nil {
		return err
	}
	// ZCRCE ends the data, ZEOF says how much there was, and the peer answers
	// with either the next invitation or a request to send the file again from
	// an offset it names. lrzsz's rz does restart the last window when a frame
	// comes through damaged, so the request is honoured a few times instead of
	// turning into a failed transfer.
	for attempt := 0; ; attempt++ {
		if err := s.stream(r, f, index, count, window, pos); err != nil {
			return err
		}
		if _, err := s.Out.Write(hexHeader(ZEOF, offsetBytes(uint32(len(f.Data))))); err != nil {
			return err
		}
		next, err := s.fileAnswer(r)
		if err != nil {
			return err
		}
		if !next.restart {
			return nil
		}
		if attempt >= 2 || next.offset >= uint32(len(f.Data)) {
			return errors.New("zmodem: the peer would not take the file")
		}
		pos = next.offset
	}
}

// fileAnswer is what a peer says about a file that just finished: an invitation
// to the next one, or a position to send this one again from.
type answer struct {
	restart bool
	offset  uint32
}

func (s *Sender) fileAnswer(r *Reader) (answer, error) {
	for {
		frame, err := r.Next()
		if err != nil {
			return answer{}, err
		}
		switch frame.Type {
		case ZRINIT, ZFIN, ZCLOSE, ZSKIP:
			return answer{}, nil
		case ZRPOS:
			return answer{restart: true, offset: frame.Offset}, nil
		case ZABORT:
			return answer{}, ErrCanceled
		}
	}
}

// wantPosition waits for the offset the peer wants the file to start at. A peer
// that answers ZSKIP has the file already and does not want it.
func (s *Sender) wantPosition(r *Reader, f File) (uint32, error) {
	for {
		frame, err := r.Next()
		if err != nil {
			return 0, err
		}
		switch frame.Type {
		case ZRPOS:
			return frame.Offset, nil
		case ZSKIP:
			return 0, errSkipped
		case ZNAK, ZNAK2:
			// Announce again: lrzsz does this after a header it could not read.
			if _, err := s.Out.Write(hexHeader(ZFILE, append(append([]byte{}, f.Name...), 0))); err != nil {
				return 0, err
			}
		case ZFIN, ZABORT:
			return 0, ErrCanceled
		}
	}
}

// stream writes the file in windows. Every window but the last asks to be
// acknowledged (ZCRCQ) and waits for the peer's ZCRCR; the last one closes the
// data (ZCRCE) and waits for the peer to invite the next file.
//
// ZCRCG and silence are not an option here: lrzsz's rz stops after a ZCRCQ
// window until the ZCRCR arrives, and a sender that waits for a ZACK it was
// never going to get leaves the transfer standing still with both sides
// convinced the other has the floor.
func (s *Sender) stream(r *Reader, f File, index, count, window int, from uint32) error {
	size := int64(len(f.Data))
	if int64(from) > size {
		from = uint32(size)
	}
	off := int(from)
	for {
		end := off + window
		if end > len(f.Data) {
			end = len(f.Data)
		}
		chunk := f.Data[off:end]
		last := end == len(f.Data)
		kind := byte(ZCRCQ)
		if last {
			kind = ZCRCE
		}
		if err := s.sendWindow(ZDATA, uint32(off), chunk, kind); err != nil {
			return err
		}
		s.On(f.Name, int64(end), size, index+1, count)
		if last {
			return nil
		}
		if err := s.waitWindow(r, uint32(off), chunk, kind); err != nil {
			return err
		}
		off = end
	}
}

// sendWindow writes one window. lrzsz repeats a window that came back with a
// NAK, so one retry is built in rather than left to the user.
func (s *Sender) sendWindow(typ byte, off uint32, data []byte, kind byte) error {
	_, err := s.Out.Write(dataFrame(typ, off, data, kind))
	return err
}

// waitWindow waits for the peer to take the window. ZCRCR and ZACK both mean
// "go on" — builds of rz differ in which one they use — and a NAK earns the
// window one more try before the transfer is given up.
func (s *Sender) waitWindow(r *Reader, off uint32, data []byte, kind byte) error {
	tried := false
	for {
		frame, err := r.Next()
		if err != nil {
			return err
		}
		switch frame.Type {
		case ZCRCR, ZACK, ZCRC:
			return nil
		case ZNAK, ZNAK2:
			if tried {
				return errors.New("zmodem: the peer rejected the data")
			}
			tried = true
			if err := s.sendWindow(ZDATA, off, data, kind); err != nil {
				return err
			}
		case ZFIN, ZABORT:
			return ErrCanceled
		}
	}
}
