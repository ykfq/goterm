package zmodem

import "time"

// handshake gets the peer ready: it answers a ZRQINIT with ZRINIT, sends its
// own ZRQINIT when the peer waits for one, and returns once a ZRINIT arrives.
func (s *Sender) handshake(r *Reader) error {
	if _, err := s.Out.Write(hexHeader(ZRQINIT, nil)); err != nil {
		return err
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
			if _, err := s.Out.Write(hexHeader(ZRINIT, rinitArgs())); err != nil {
				return err
			}
		case ZFIN, ZABORT:
			return ErrCanceled
		}
	}
}

// unused keeps the import list stable while probing.
var _ = time.Second

// sendFile sends ZFILE, waits for ZRPOS, and closes the file with ZEOF.
func (s *Sender) sendFile(r *Reader, f File, index, count, window int) error {
	meta := make([]byte, 0, len(f.Name)+16)
	meta = append(meta, f.Name...)
	meta = append(meta, 0)
	meta = append(meta, "0 0 0 0 0 0"...)
	if _, err := s.Out.Write(hexHeader(ZFILE, meta)); err != nil {
		return err
	}
	if err := s.waitFor(r, 30*time.Second, ZRPOS, ZSKIP); err != nil {
		return err
	}
	if err := s.stream(r, f, index, count, window); err != nil {
		return err
	}
	_, err := s.Out.Write(hexHeader(ZEOF, []byte{0, 0, 0, 0}))
	return err
}

// stream writes the file in ZDATA frames, acknowledging every burst but the
// last, which carries ZCRCE and the file end.
func (s *Sender) stream(r *Reader, f File, index, count, window int) error {
	size := int64(len(f.Data))
	off := 0
	for {
		end := off + window
		if end > len(f.Data) {
			end = len(f.Data)
		}
		chunk := f.Data[off:end]
		last := end == len(f.Data)
		kind := byte(ZCRCG)
		if last {
			kind = ZCRCE
		}
		if _, err := s.Out.Write(binFrame(ZDATA, chunk, kind)); err != nil {
			return err
		}
		s.On(f.Name, int64(end), size, index+1, count)
		if last {
			return nil
		}
		if err := s.waitFor(r, 30*time.Second, ZACK); err != nil {
			return err
		}
		off = end
	}
}

