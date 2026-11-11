package main

import (
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/trzsz/trzsz-go/trzsz"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/highlight"
	"github.com/ykfq/goterm/internal/sshx"
	"github.com/ykfq/goterm/internal/zmodem"
)

// transferConn sits between the terminal and the SSH session and gives it two
// transfer protocols:
//
//	terminal writes -> inW/inR -> trzsz filter -> SSH
//	SSH -> trzsz filter -> outW/outR -> lrzsz peel-off -> decode -> highlight -> terminal
//
// trzsz (trz/tsz) is trzsz-go's own filter: it sniffs the banner, speaks the
// protocol and draws its progress bar into the stream. lrzsz (rz/sz) is
// GoTerm's own ZMODEM peer (internal/zmodem), because trzsz-go's ZMODEM
// support shells out to a local rz/sz binary that a pure-Go build does not
// have — and when that binary is missing the library silently swallows the
// protocol, losing the download. So the filter is left with ZMODEM disabled
// and our receiver peels ZMODEM frames off the filter's output instead.
// Both protocols ask the same Chooser for folders and files, so a server that
// has only lrzsz gets the same transfer experience as one that has trzsz.
type transferConn struct {
	sess *sshx.Session
	raw  io.ReadCloser

	inR  *io.PipeReader
	inW  *io.PipeWriter
	outR *io.PipeReader
	outW *io.PipeWriter

	peer   *lrzszPeer
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

// sessionIn is the filter's view of the remote PTY's input: the normal path
// from typed bytes to the server. It is an io.WriteCloser so the filter can
// close it on shutdown; closing only releases this adapter, not the session.
type sessionIn struct{ sess *sshx.Session }

func (w sessionIn) Write(p []byte) (int, error) {
	if err := w.sess.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (sessionIn) Close() error { return nil }

// serverInput is the transfer's path to the server. During a ZMODEM or trzsz
// conversation the peer writes protocol bytes straight to it, and the filter
// must not close it: the session outlives the transfer.
type serverInput struct{ sess *sshx.Session }

func (w serverInput) Write(p []byte) (int, error) {
	if err := w.sess.Write(p); err != nil {
		return 0, err
	}
	return len(p), nil
}
func (serverInput) Close() error { return nil }

// lrzszPeer peels ZMODEM frames off the terminal-bound stream. Between
// transfers it is a pass-through with a few bytes of look-ahead, so a frame
// start split across two reads is still recognised.
type lrzszPeer struct {
	c *transferConn

	mu     sync.Mutex
	carry  []byte // a possible frame start, held back
	relay  []byte // bytes for the terminal, in order
	active bool
	closed bool
	err    error
	done   chan struct{} // closed when a transfer ends
	ready  chan struct{} // nudges Read after feed, transfer end or close
	// stream is the protocol layer's source while a transfer runs. It starts
	// as the filter's output and is wrapped once the opening frame is peeked
	// at, so the peer continues exactly where the peek stopped.
	stream io.Reader

	// progressLen is the last line drawn, for in-place redraw.
	progressLen int
}

func newLrzszPeer(c *transferConn) *lrzszPeer {
	return &lrzszPeer{c: c, done: make(chan struct{}), ready: make(chan struct{}, 1), stream: c.outR}
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
	c.peer = newLrzszPeer(c)
	c.dec = newDecodingReader(c.peer, enc)
	c.hl = highlight.NewReader(c.dec)
	go c.peer.pump()
	// trzsz-go's documented topology: terminal -> inW -> filter -> SSH, and
	// SSH -> filter -> outW -> outR -> terminal. The session has one reader,
	// the filter; the peer peels ZMODEM frames off before the terminal sees
	// them. ZMODEM stays off in the library: it would run a local rz/sz we do
	// not ship, and swallow the protocol when it is missing. The peer handles
	// lrzsz in Go instead.
	c.filter = trzsz.NewTrzszFilter(inR, outW, sessionIn{s}, raw, trzsz.TrzszOptions{
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

// Write sends terminal input through the trzsz filter, which turns a typed
// `trz`/`tsz` into an upload and otherwise forwards the bytes to SSH.
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
		c.peer.Close()
		err = c.sess.Close()
	})
	return err
}

// Read is the terminal's source. While a transfer runs the terminal waits:
// pump is the only reader of the filter's output while no transfer runs. It
// keeps ownership across transfers instead of stopping: the peer consumes the
// session through p.session(), which serialises against this loop, so there is
// exactly one reader at any moment and no bytes change hands mid-frame.
func (p *lrzszPeer) pump() {
	buf := make([]byte, 32*1024)
	for {
		p.mu.Lock()
		if p.active {
			done := p.done
			p.mu.Unlock()
			<-done
			continue
		}
		p.mu.Unlock()

		n, err := p.c.outR.Read(buf)
		if n > 0 {
			p.feed(buf[:n])
		}
		if err != nil {
			p.mu.Lock()
			if p.err == nil {
				p.err = err
			}
			p.closed = true
			p.signal()
			p.mu.Unlock()
			return
		}
	}
}

// session is the protocol layer's view of the remote stream while a transfer
// runs. The pump pauses on p.active, so this is the only reader then.
func (p *lrzszPeer) session() io.Reader { return p.stream }

// Read serves the terminal. While a transfer runs the terminal waits: the
// protocol bytes belong to the peer, not to the screen.
func (p *lrzszPeer) Read(b []byte) (int, error) {
	for {
		p.mu.Lock()
		if !p.closed && !p.active && len(p.relay) == 0 {
			ready := p.ready
			p.mu.Unlock()
			<-ready
			continue
		}
		if !p.active && len(p.relay) > 0 {
			n := copy(b, p.relay)
			p.relay = p.relay[n:]
			p.mu.Unlock()
			return n, nil
		}
		if p.active {
			done := p.done
			p.mu.Unlock()
			<-done
			continue
		}
		err := p.err
		if err == nil {
			err = io.EOF
		}
		p.mu.Unlock()
		return 0, err
	}
}

// signal wakes the terminal's Read after a state change. Holding p.mu while
// signalling is the caller's job.
func (p *lrzszPeer) signal() {
	if p.ready != nil {
		select {
		case p.ready <- struct{}{}:
		default:
		}
	}
}

// feed splits one chunk into terminal bytes and protocol bytes. It reports
// whether a transfer took over the stream: once a frame start shows up the
// caller must stop reading, because those bytes belong to the peer.
func (p *lrzszPeer) feed(chunk []byte) bool {
	p.mu.Lock()
	if p.active {
		// A transfer owns the session: these bytes are protocol, and reading
		// them here would take them away from the peer that needs them.
		p.mu.Unlock()
		return true
	}
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
		p.signal()
		p.mu.Unlock()
		return false
	}
	p.mu.Lock()
	p.relay = append(p.relay, joined[:at]...)
	p.active = true
	p.signal()
	p.mu.Unlock()
	go p.run(joined[at:])
	return true
}

// run performs the transfer and then resumes pass-through. The first frame is
// already in hand; a download is assumed unless that frame proves an upload
// (see accept).
func (p *lrzszPeer) run(header []byte) {
	upload := p.accept(header)
	defer func() {
		p.mu.Lock()
		p.active = false
		done := p.done
		p.done = make(chan struct{})
		p.signal()
		p.mu.Unlock()
		close(done)
		// The pump was told to resume by clearing active above.
	}()
	var err error
	if upload {
		err = p.send()
	} else {
		err = p.receive()
	}
	if err == nil || errors.Is(err, zmodem.ErrCanceled) {
		return
	}
	p.notice("lrzsz: " + err.Error())
}

// accept decides the direction and gets the session past its opening frame.
//
// `rz` and `sz` both open with ZRQINIT and a peer may answer a ZRQINIT with a
// ZRINIT before we can look, so the opening is read as an exchange rather than
// a single frame: a ZRQINIT with args, or any ZRINIT, means the remote is an
// `rz` (an upload is possible); a bare ZRQINIT that nothing prompted means `sz`
// and we are downloading. Everything read stays buffered for the peer that runs.
func (p *lrzszPeer) accept(start []byte) bool {
	f, head, err := zmodem.ParseFirst(start, p.stream)
	if err != nil {
		return false
	}
	p.stream = &bufferedStream{head: head, rest: p.stream}
	switch f.Type {
	case zmodem.ZRQINIT:
		return len(f.Args) > 0
	case zmodem.ZRINIT:
		// The remote already answered as a receiver: it is an `rz`.
		return true
	}
	return false
}

// bufferedStream replays the classifier's look-ahead, then continues with the
// session.
type bufferedStream struct {
	head []byte
	rest io.Reader
}

func (b *bufferedStream) Read(p []byte) (int, error) {
	if len(b.head) > 0 {
		n := copy(p, b.head)
		b.head = b.head[n:]
		return n, nil
	}
	return b.rest.Read(p)
}
func (b *bufferedStream) Close() error { return nil }

func (p *lrzszPeer) send() error {
	if p.c.chooser == nil {
		return errNoChooser
	}
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
	p.println(fmt.Sprintf("\x1b[36mlrzsz\x1b[0m sending %d file(s)", len(files)))
	err = (&zmodem.Sender{In: p.stream, Out: sessionIn{p.c.sess}, On: p.progress("-> ")}).SendAfterAsk(files)
	if errors.Is(err, zmodem.ErrPeerWantsDownload) {
		// The remote answered our ZRQINIT with a bare one: it is `sz`, not `rz`.
		p.println("\x1b[36mlrzsz\x1b[0m remote wants to send; switching to download")
		return p.receive()
	}
	if err == nil {
		p.println("\x1b[36mlrzsz\x1b[0m upload complete")
	}
	return err
}

func (p *lrzszPeer) receive() error {
	if p.c.chooser == nil {
		return errNoChooser
	}
	dir, err := p.c.chooser.ChooseDownloadFolder("")
	if err != nil {
		return err
	}
	if strings.TrimSpace(dir) == "" {
		return zmodem.ErrCanceled
	}
	saved, err := (&zmodem.Receiver{In: p.stream, Out: sessionIn{p.c.sess}, Dir: dir, On: p.progress("<- ")}).Receive()
	if err == nil {
		p.println(fmt.Sprintf("\x1b[36mlrzsz\x1b[0m saved %d file(s) to %s", len(saved), dir))
	}
	return err
}

var errNoChooser = errors.New("no file dialog available")

// relayWriter hands status lines to the terminal through the peer's queue
// instead of the filter's pipe. Writing the pipe directly would block, because
// the terminal deliberately does not read while a transfer runs.
type relayWriter struct{ p *lrzszPeer }

func (w relayWriter) Write(b []byte) (int, error) {
	w.p.mu.Lock()
	w.p.relay = append(w.p.relay, b...)
	w.p.signal()
	w.p.mu.Unlock()
	return len(b), nil
}

// progress draws one line in place, like the trzsz progress bar.
func (p *lrzszPeer) progress(arrow string) zmodem.Progress {
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
		_, _ = io.WriteString(relayWriter{p}, "\r\x1b[K"+line+pad)
	}
}

func (p *lrzszPeer) println(s string) {
	_, _ = io.WriteString(relayWriter{p}, "\r\x1b[K"+s+"\r\n")
	p.mu.Lock()
	p.progressLen = 0
	p.mu.Unlock()
}

func (p *lrzszPeer) notice(s string) {
	if p.c.notice != nil {
		p.c.notice(s)
	}
}

// Close stops pass-through: the session is gone.
func (p *lrzszPeer) Close() {
	p.mu.Lock()
	p.closed = true
	p.mu.Unlock()
}

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
