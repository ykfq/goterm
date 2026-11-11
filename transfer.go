package main

import (
	"io"
	"sync"
	"time"

	"fmt"

	"strings"

	"github.com/trzsz/trzsz-go/trzsz"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/highlight"
	"github.com/ykfq/goterm/internal/sshx"
	"github.com/ykfq/goterm/internal/zmodem"
)

// transferConn sits between the terminal and the SSH session and gives it two
// transfer protocols:
//
//	terminal writes 鈬?inW/inR 鈬?trzsz filter 鈬?zmodem peer 鈬?SSH
//	SSH 鈬?zmodem peer 鈬?trzsz filter 鈬?outW/outR 鈬?decode 鈬?highlight 鈬?terminal
//
// trzsz (trz/tsz) is trzsz-go's own filter: it sniffs the banner, speaks the
// protocol and draws its progress bar into the stream. lrzsz (rz/sz) is
// GoTerm's own ZMODEM peer (internal/zmodem), because trzsz-go's ZMODEM
// support shells out to a local rz/sz binary that a pure-Go build does not
// have. Both ask the same Chooser for folders and files, so a server that has
// only lrzsz gets the same transfer experience as one that has trzsz.
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

	// tick is told when output flows, so the tab bar can mark other tabs.
	tick func()

	chooser Chooser
	// notice reports a transfer problem to the status bar.
	notice func(string)

	closeOnce sync.Once
}

// Chooser is the dialog layer transfers need. The app implements it with
// native dialogs; tests implement it with fixed answers.
type Chooser interface {
	// ChooseDownloadFolder returns the folder a download lands in. suggested
	// is where the dialog should open.
	ChooseDownloadFolder(suggested string) (string, error)
	// ChooseUpload returns local paths for an upload. dirOnly asks for a
	// folder instead of files (a recursive send).
	ChooseUpload(dirOnly bool) ([]string, error)
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

// lrzszPeer adapts the session to internal/zmodem: it is the server's side of
// the stream as that package sees it, and it renders progress into the
// terminal through the same pipe the trzsz filter writes to.
type lrzszPeer struct {
	c      *transferConn
	z      *zmodemPeer
	dir    string
	uiOnce sync.Once
}

func newTransferConn(s *sshx.Session, enc encode.Name, chooser Chooser) (*transferConn, error) {
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
		sess:    s,
		raw:     raw,
		inR:     inR,
		inW:     inW,
		outR:    outR,
		outW:    outW,
		chooser: chooser,
	}
	z := newZmodemPeer(c)
	c.dec = newDecodingReader(outR, enc)
	c.hl = highlight.NewReader(c.dec)
	c.filter = trzsz.NewTrzszFilter(inR, outW, sessionIn{s}, z, trzsz.TrzszOptions{
		TerminalColumns: 100,
	})
	if chooser != nil {
		if dir, err := chooser.ChooseDownloadFolder(""); err == nil && dir != "" {
			c.filter.SetDefaultDownloadPath(dir)
		}
	}
	return c, nil
}

func (c *transferConn) Read(p []byte) (int, error) {
	n, err := c.hl.Read(p)
	if n > 0 && c.tick != nil {
		c.tick()
	}
	return n, err
}

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

// zmodemPeer is the trzsz filter's view of the server's output. It passes the
// stream through until a ZMODEM frame arrives, then hands the conversation to
// internal/zmodem and blocks that stream until the transfer ends 鈥?so protocol
// bytes never reach the terminal and terminal painting never reaches the wire.
type zmodemPeer struct {
	c *transferConn

	mu      sync.Mutex
	carry   []byte // a possible frame start, held back
	relay   []byte // bytes for the terminal, in order
	pending []byte // protocol bytes read ahead of the peer's own reads
	active  bool
	closed  bool
	err     error
	stream  *peerStream // the session as the lrzsz package sees it
	done    chan struct{} // closed whenever a transfer ends


	// progressLine is the last line drawn, for in-place redraw.
	progressLen int
}

func newZmodemPeer(c *transferConn) *zmodemPeer {
	return &zmodemPeer{c: c, done: make(chan struct{})}
}

// Read is called by the trzsz filter. It never returns protocol bytes.
func (p *zmodemPeer) Read(b []byte) (int, error) {
	for {
		p.mu.Lock()
		if !p.active && len(p.relay) > 0 {
			n := copy(b, p.relay)
			p.relay = p.relay[n:]
			p.mu.Unlock()
			return n, nil
		}
		if p.active {
			// The transfer owns the stream until it ends, so the terminal is
			// told to wait rather than served a byte the sender needs. The
			// peer signal is closed when the transfer finishes.
			p.mu.Unlock()
			select {
			case <-p.done:
			case <-time.After(20 * time.Millisecond):
			}
			continue
		}
		if p.closed {
			err := p.err
			p.mu.Unlock()
			return 0, err
		}
		p.mu.Unlock()

		buf := make([]byte, 32*1024)
		n, err := p.c.raw.Read(buf)
		if err == nil && n > 0 {
		}
		if n > 0 {
			p.feed(buf[:n])
			if err != nil {
				p.mu.Lock()
				p.err, p.closed = err, true
				p.mu.Unlock()
			}
			continue
		}
		if err != nil {
			p.mu.Lock()
			p.err, p.closed = err, true
			left := p.relay
			p.relay = nil
			p.mu.Unlock()
			if len(left) > 0 {
				n := copy(b, left)
				p.mu.Lock()
				p.relay = append(left[n:], p.relay...)
				p.mu.Unlock()
				return n, nil
			}
			return 0, err
		}
	}
}

// feed splits one chunk into terminal bytes and protocol bytes. Once a frame
// starts, everything after it belongs to the transfer: the peer's writes go out
// through the session and its reads come straight from p.c.raw, so no later
// byte may be handed to the terminal or it would never reach the sender.
func (p *zmodemPeer) feed(chunk []byte) {
	p.mu.Lock()
	joined := append(append([]byte{}, p.carry...), chunk...)
	p.carry = nil
	p.mu.Unlock()

	at := zmodem.Scan(joined)
	if at < 0 {
		keep := zmodem.PrefixLen(joined)
		cut := len(joined) - keep
		p.mu.Lock()
		p.relay = append(p.relay, joined[:cut]...)
		p.carry = append([]byte{}, joined[cut:]...)
		p.mu.Unlock()
		return
	}
	p.mu.Lock()
	p.relay = append(p.relay, joined[:at]...)
	p.active = true
	p.mu.Unlock()
	go p.run(joined[at:])
}

// run performs the transfer and then resumes pass-through.
func (p *zmodemPeer) run(header []byte) {
	defer func() {
		p.mu.Lock()
		p.active = false
		p.pending = nil
		close(p.done)
		p.done = make(chan struct{})
		p.mu.Unlock()
	}()
	p.mu.Lock()
	p.stream = &peerStream{p: p, header: header}
	p.mu.Unlock()
	var err error
	if len(header) > 4 && header[4] == '0' {
		err = p.send()
	} else {
		err = p.receive()
	}
	if err == nil || err == zmodem.ErrCanceled {
		return
	}
	p.notice("lrzsz: " + err.Error())
}

// stream is the lrzsz peer's view of the server: reads come from bytes already
// buffered at the frame start and then from the session; writes go to SSH.
type peerStream struct {
	p      *zmodemPeer
	header []byte
}

func (s *peerStream) Read(b []byte) (int, error) {
	if len(s.header) > 0 {
		n := copy(b, s.header)
		s.header = s.header[n:]
		return n, nil
	}
	return s.p.c.raw.Read(b)
}

func (s *peerStream) Write(b []byte) (int, error) {
	if err := s.p.c.sess.Write(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

func (p *zmodemPeer) send() error {
	paths, err := p.c.chooser.ChooseUpload(false)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return zmodem.ErrCanceled
	}
	files, err := zmodem.ReadPaths(paths)
	if err != nil {
		return err
	}
	p.println("\x1b[36mlrzsz\x1b[0m sending " + fmt.Sprintf("%d file(s)", len(files)))
	err = (&zmodem.Sender{In: p.stream, Out: p.stream, On: p.progress("-> ")}).Send(files)
	if err == nil {
		p.println("\x1b[36mlrzsz\x1b[0m upload complete")
	}
	return err
}

func (p *zmodemPeer) receive() error {
	dir, err := p.c.chooser.ChooseDownloadFolder("")
	if err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return zmodem.ErrCanceled
	}
	saved, err := (&zmodem.Receiver{In: p.stream, Out: p.stream, Dir: dir, On: p.progress("<- ")}).Receive()
	if err == nil {
		p.println("\x1b[36mlrzsz\x1b[0m saved " + fmt.Sprintf("%d file(s) to ", len(saved)) + dir)
	}
	return err
}

// progress draws one line in place, like the trzsz progress bar.
func (p *zmodemPeer) progress(arrow string) zmodem.Progress {
	return func(name string, done, size int64, index, count int) {
		pct := 0
		if size > 0 {
			pct = int(done * 100 / size)
		}
		line := fmt.Sprintf("%s %s %3d%% (%s/%s)", arrow, name, pct, humanSize(done), humanSize(size))
		if len([]rune(line)) > 100 {
			line = string([]rune(line)[:100])
		}
		p.mu.Lock()
		erase := p.progressLen
		p.progressLen = len([]rune(line))
		p.mu.Unlock()
		pad := ""
		if erase > p.progressLen {
			pad = strings.Repeat(" ", erase-p.progressLen)
		}
		_, _ = io.WriteString(p.c.outW, "\r\x1b[K"+line+pad)
	}
}

func (p *zmodemPeer) println(s string) {
	_, _ = io.WriteString(p.c.outW, "\r\x1b[K"+s+"\r\n")
	p.mu.Lock()
	p.progressLen = 0
	p.mu.Unlock()
}

func (p *zmodemPeer) notice(s string) {
	if p.c.notice != nil {
		p.c.notice(s)
	}
}

// Close stops a transfer in flight.
func (p *zmodemPeer) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}
