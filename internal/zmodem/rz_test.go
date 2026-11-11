package zmodem

import (
	"os"
	"path/filepath"
	"sync"
	"testing"
)

// pipeStream is an unbounded FIFO behind a Reader and a Writer, so neither end
// of a session waits for the other's pipe semantics.
type pipeStream struct {
	mu  sync.Mutex
	buf []byte
	ch  chan struct{}
}

func newPipeStream() *pipeStream { return &pipeStream{ch: make(chan struct{}, 1)} }

func (p *pipeStream) Write(b []byte) (int, error) {
	p.mu.Lock()
	p.buf = append(p.buf, b...)
	p.mu.Unlock()
	select {
	case p.ch <- struct{}{}:
	default:
	}
	return len(b), nil
}

func (p *pipeStream) Read(dst []byte) (int, error) {
	for {
		p.mu.Lock()
		if len(p.buf) > 0 {
			n := copy(dst, p.buf)
			p.buf = p.buf[n:]
			p.mu.Unlock()
			return n, nil
		}
		p.mu.Unlock()
		<-p.ch
	}
}

// peerRZ is the remote `rz` half: it answers the sender and stores the file.
func peerRZ(r *Reader, out *pipeStream, dir string) {
	name := ""
	var data []byte
	for {
		f, err := r.Next()
		if err != nil {
			return
		}
		switch f.Type {
		case ZRQINIT:
			out.Write(hexHeader(ZRINIT, rinitArgs()))
		case ZFILE:
			names := splitNames(f.Data)
			name = ""
			if len(names) > 0 {
				name = names[0]
			}
			data = nil
			out.Write(hexHeader(ZRPOS, []byte{0, 0, 0, 0}))
		case ZDATA:
			data = append(data, f.Data...)
			out.Write(hexHeader(ZACK, []byte{}))
		case ZEOF:
			os.MkdirAll(dir, 0o755)
			os.WriteFile(filepath.Join(dir, name), data, 0o644)
		case ZFIN:
			out.Write(hexHeader(ZFIN, nil))
			return
		}
	}
}

// TestRzRoundTrip is the lrzsz upload contract end to end: ZFILE with its
// name, the data, ZEOF, and a file on the peer's disk.
func TestRzRoundTrip(t *testing.T) {
	toPeer := newPipeStream()
	toClient := newPipeStream()
	dir := t.TempDir()
	go peerRZ(NewReader(toPeer), toClient, dir)

	body := []byte("hello zmodem\nsecond line\n")
	s := &Sender{In: toClient, Out: toPeer}
	done := make(chan error, 1)
	go func() { done <- s.Send([]File{{Name: "hello.txt", Data: body}}) }()
	if err := <-done; err != nil {
		t.Fatalf("send: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "hello.txt"))
	if err != nil {
		t.Fatalf("peer missed the file: %v", err)
	}
	if string(got) != string(body) {
		t.Fatalf("file content %q", got)
	}
}
