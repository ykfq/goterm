package main

import (
	"io"
	"sync"

	"github.com/trzsz/trzsz-go/trzsz"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/highlight"
	"github.com/ykfq/goterm/internal/sshx"
)

// transferConn puts trzsz's filter between the terminal and the SSH session.
// That filter is the same code the `trzsz` command line runs: it recognises the
// `trz`/`tsz` protocol on the wire, and (with EnableZmodem) the lrzsz `rz`/`sz`
// handshake too, draws its progress bar into the terminal stream, and leaves
// ordinary output and keystrokes untouched.
//
//	terminal writes → inW → inR → filter → serverIn → SSH
//	SSH → serverOut → filter → outW → outR → decode → terminal reads
type transferConn struct {
	sess *sshx.Session
	raw  io.ReadCloser

	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter

	dec    *decodingReader
	hl     *highlight.Reader
	filter *trzsz.TrzszFilter

	closeOnce sync.Once
}

// sessionIn is the filter's view of the remote PTY's input. Closing it must not
// end the session: the terminal owns that decision.
type sessionIn struct{ sess *sshx.Session }

func (w sessionIn) Write(p []byte) (int, error) {
	if err := w.sess.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (sessionIn) Close() error { return nil }

func newTransferConn(s *sshx.Session, enc encode.Name, downloadDir string) (*transferConn, error) {
	// We are not a Windows console: the bytes on this pipe are the raw PTY
	// stream, so trzsz must use its plain line protocol. Without this the
	// library applies console echo/newline fixups meant for a local ConPTY.
	trzsz.SetAffectedByWindows(false)
	raw, err := s.AttachOutput()
	if err != nil {
		return nil, err
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &transferConn{
		sess: s,
		raw:  raw,
		inR:  inR,
		inW:  inW,
		outR: outR,
		outW: outW,
	}
	c.dec = newDecodingReader(outR, enc)
	c.hl = highlight.NewReader(c.dec)
	c.filter = trzsz.NewTrzszFilter(inR, outW, sessionIn{s}, raw, trzsz.TrzszOptions{
		TerminalColumns: 100,
		EnableZmodem:    true,
	})
	if downloadDir != "" {
		c.filter.SetDefaultDownloadPath(downloadDir)
	}
	return c, nil
}

func (c *transferConn) Read(p []byte) (int, error) { return c.hl.Read(p) }

// SetHighlight turns keyword colouring on or off for this tab without
// rebuilding the terminal or the SSH session.
func (c *transferConn) SetHighlight(on bool) { c.hl.SetEnabled(on) }

// SetEncoding switches the remote-output decoder without rebuilding the
// terminal. A character split across the switch can be lost; the next one
// decodes with the new setting.
func (c *transferConn) SetEncoding(name encode.Name) { c.dec.SetEncoding(name) }

// decodingReader is an io.Reader whose decoder can be replaced while the
// session runs.
type decodingReader struct {
	mu   sync.Mutex
	src  io.Reader
	cur  io.Reader
	name encode.Name
}

func newDecodingReader(src io.Reader, name encode.Name) *decodingReader {
	return &decodingReader{src: src, cur: encode.Reader(src, name), name: name}
}

func (d *decodingReader) Read(p []byte) (int, error) {
	d.mu.Lock()
	r := d.cur
	d.mu.Unlock()
	return r.Read(p)
}

func (d *decodingReader) SetEncoding(name encode.Name) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.name == name {
		return
	}
	d.name = name
	d.cur = encode.Reader(d.src, name)
}
func (c *transferConn) Write(p []byte) (int, error) { return c.inW.Write(p) }

func (c *transferConn) Resize(cols, rows int) error {
	c.filter.SetTerminalColumns(int32(cols))
	return c.sess.Resize(cols, rows)
}

func (c *transferConn) Close() error {
	var err error
	c.closeOnce.Do(func() {
		_ = c.inW.Close()
		c.filter.Close()
		_ = c.raw.Close()
		_ = c.outW.Close()
		err = c.sess.Close()
	})
	return err
}
