package highlight

import "io"

// Reader decorates a byte stream on its way to the terminal. It can be switched
// off while a session runs; switching never rebuilds the terminal or the SSH
// connection.
type Reader struct {
	src io.Reader
	h   *Highlighter

	// out is decorated bytes not yet handed to the terminal.
	out []byte
	// pendingErr is the source error to report after the remaining out bytes.
	pendingErr error
	buf        []byte
}

// NewReader wraps src with every built-in rule set enabled.
func NewReader(src io.Reader) *Reader {
	return &Reader{src: src, h: New(), buf: make([]byte, 32*1024)}
}

// SetEnabled turns decoration on or off mid-stream. The stream itself is never
// interrupted; only what gets inserted into it changes.
func (r *Reader) SetEnabled(on bool) { r.h.SetEnabled(on) }

func (r *Reader) Read(p []byte) (int, error) {
	for len(r.out) == 0 {
		if r.pendingErr != nil {
			err := r.pendingErr
			r.pendingErr = nil
			return 0, err
		}
		n, err := r.src.Read(r.buf)
		if n > 0 {
			r.out = append(r.out, r.h.Process(r.buf[:n])...)
		}
		if err != nil {
			if len(r.out) == 0 {
				return 0, err
			}
			r.pendingErr = err
			break
		}
		if n == 0 {
			return 0, nil
		}
	}
	n := copy(p, r.out)
	r.out = r.out[n:]
	if len(r.out) == 0 {
		r.out = nil
	}
	return n, nil
}
