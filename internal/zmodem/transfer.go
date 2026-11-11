package zmodem

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// File is one file to send.
type File struct {
	Name string
	Data []byte
}

// ReadPaths reads files, and directories recursively, into send order.
func ReadPaths(paths []string) ([]File, error) {
	var out []File
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			return nil, err
		}
		if !st.IsDir() {
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, err
			}
			out = append(out, File{Name: filepath.Base(p), Data: data})
			continue
		}
		root := filepath.Dir(p)
		err = filepath.Walk(p, func(q string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() {
				return err
			}
			data, err := os.ReadFile(q)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, q)
			if err != nil {
				rel = filepath.Base(q)
			}
			out = append(out, File{Name: filepath.ToSlash(rel), Data: data})
			return nil
		})
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

// Scan finds the first frame start in b, or -1. A frame start is PAD ZDLE
// <bin|hex>, and the common double-PAD form is accepted too.
func Scan(b []byte) int {
	for i := 0; i+3 < len(b); i++ {
		if b[i] != ZPAD {
			continue
		}
		if b[i+1] == ZPAD && b[i+2] == ZDLE && (b[i+3] == ZHEX || b[i+3] == ZBIN || b[i+3] == ZBIN32) {
			return i
		}
		if i+2 < len(b) && b[i+1] == ZDLE && (b[i+2] == ZBIN || b[i+2] == ZBIN32) {
			return i
		}
	}
	return -1
}

// PrefixLen reports how many trailing bytes of b could still begin a frame.
func PrefixLen(b []byte) int {
	n := len(b)
	switch {
	case n >= 3 && b[n-3] == ZPAD && b[n-2] == ZPAD && b[n-1] == ZDLE:
		return 3
	case n >= 2 && b[n-2] == ZPAD && b[n-1] == ZPAD:
		return 2
	case n >= 2 && b[n-2] == ZPAD && b[n-1] == ZDLE:
		return 2
	case n >= 1 && b[n-1] == ZPAD:
		return 1
	}
	return 0
}

const chunkSize = 8192

// Sender drives the peer that runs `sz`: it writes the file, frame by frame.
type Sender struct {
	In     io.Reader
	Out    io.Writer
	On     Progress
	Window int // bytes per ZDATA frame
}

// Send runs one sz session.
func (s *Sender) Send(files []File) error {
	return s.start(files, s.handshake)
}

// SendAfterAsk opens the session by asking the peer for files (a ZRQINIT of
// our own) and then expects the peer to be an `rz`. A remote `sz` answers that
// ask with a bare ZRQINIT of its own, which is reported as
// ErrPeerWantsDownload so the caller can switch to receiving instead.
func (s *Sender) SendAfterAsk(files []File) error {
	if _, err := s.Out.Write(hexHeader(ZRQINIT, nil)); err != nil {
		return err
	}
	return s.start(files, s.handshakeAfterAsk)
}

func (s *Sender) start(files []File, shake func(*Reader) error) error {
	if len(files) == 0 {
		return errors.New("zmodem: nothing to send")
	}
	if s.On == nil {
		s.On = noProgress
	}
	window := s.Window
	if window <= 0 {
		window = chunkSize * 4
	}
	r := NewReader(s.In)
	if err := shake(r); err != nil {
		return err
	}
	for i, f := range files {
		if err := s.sendFile(r, f, i, len(files), window); err != nil {
			return err
		}
	}
	return s.close(r)
}

// close finishes the session with ZFIN and takes the peer's answer.
func (s *Sender) close(r *Reader) error {
	if _, err := s.Out.Write(hexHeader(ZFIN, nil)); err != nil {
		return err
	}
	for {
		f, err := r.Next()
		if err != nil {
			return err
		}
		if f.Type == ZFIN {
			return nil
		}
	}
}

// waitFor reads frames until one of want arrives.
func (s *Sender) waitFor(r *Reader, timeout time.Duration, want ...byte) error {
	for {
		f, err := r.Next()
		if err != nil {
			return err
		}
		for _, w := range want {
			if f.Type == w {
				return nil
			}
		}
		if f.Type == ZFIN || f.Type == ZABORT {
			return ErrCanceled
		}
	}
}

// Receiver drives the peer that runs `rz`: it announces ZRINIT, accepts files
// into Dir, and closes with ZFIN.
type Receiver struct {
	In  io.Reader
	Out io.Writer
	Dir string
	On  Progress
}

// Receive runs one rz session and returns the names it saved. The caller has
// already seen and consumed the peer's opening frame (see lrzszPeer.accept),
// so nothing is announced here: the receiver answers the ZFIN/ZRQINIT that
// follows and waits for the ZFILE that comes with it.
func (rcv *Receiver) Receive() ([]string, error) {
	if rcv.On == nil {
		rcv.On = noProgress
	}
	r := NewReader(rcv.In)
	var saved []string
	// Answer the announcement that opened the transfer: the peer is waiting for
	// a ZRINIT before it names the first file.
	if _, err := rcv.Out.Write(hexHeader(ZRINIT, rzRinitArgs())); err != nil {
		return nil, err
	}
	for {
		f, err := r.Next()
		if err != nil {
			if len(saved) > 0 && !errors.Is(err, ErrCanceled) {
				return saved, nil
			}
			return saved, err
		}
		switch f.Type {
		case ZRQINIT:
			if _, err := rcv.Out.Write(hexHeader(ZRINIT, rinitArgs())); err != nil {
				return saved, err
			}
		case ZFILE:
			names, err := rcv.acceptFile(r, f)
			if err != nil {
				return saved, err
			}
			saved = append(saved, names...)
		case ZFIN:
			_, _ = rcv.Out.Write(hexHeader(ZFIN, nil))
			return saved, nil
		case ZABORT, ZNAK2:
			return saved, ErrCanceled
		}
	}
}

// acceptFile reads one ZFILE and everything that follows it.
func (rcv *Receiver) acceptFile(r *Reader, f *Frame) ([]string, error) {
	names := splitNames(f.Data)
	name := ""
	if len(names) > 0 {
		name = names[0]
	}
	if _, err := rcv.Out.Write(hexHeader(ZRPOS, []byte{0, 0, 0, 0})); err != nil {
		return nil, err
	}
	var data []byte
	var saved []string
	for {
		next, err := r.Next()
		if err != nil {
			return saved, err
		}
		switch next.Type {
		case ZDATA:
			data = append(data, next.Data...)
			rcv.On(name, int64(len(data)), -1, 1, 1)
		case ZEOF:
			if err := os.MkdirAll(rcv.Dir, 0o755); err != nil {
				return saved, err
			}
			target := filepath.Join(rcv.Dir, filepath.Base(sanitize(name)))
			if name == "" {
				target = filepath.Join(rcv.Dir, "received")
			}
			if err := os.WriteFile(target, data, 0o644); err != nil {
				return saved, err
			}
			saved = append(saved, target)
			_, err := rcv.Out.Write(hexHeader(ZRINIT, rinitArgs()))
			return saved, err
		case ZFIN, ZABORT:
			return saved, ErrCanceled
		}
	}
}

// rinitArgs is close enough to lrzsz's ZRINIT: no CAN, a 1 KiB receive
// buffer, and a 1 KiB frame length.
func rinitArgs() []byte { return []byte{0, 0, 1, 0, 0, 1} }

// rzRinitArgs answers a peer that announced itself: the same flags, with the
// "this side is the receiver" bit lrzsz looks for. Without it a peer that also
// sent a bare ZRQINIT cannot tell us from another `sz` and both sides wait.
func rzRinitArgs() []byte { return []byte{0, 1, 1, 0, 0, 1} }

// splitNames splits a ZFILE argument string into the names it carries. The
// name runs up to the first NUL; everything after it is lrzsz's metadata.
func splitNames(data []byte) []string {
	s := string(data)
	if i := strings.IndexByte(s, 0); i >= 0 {
		s = s[:i]
	}
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	return []string{s}
}

// sanitize keeps a remote name from escaping the download directory.
func sanitize(name string) string {
	name = strings.ReplaceAll(name, "\\", "_")
	name = strings.ReplaceAll(name, "/", "_")
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "received"
	}
	return name
}
