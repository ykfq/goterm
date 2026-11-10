package main

import (
	"io"
	"strings"
	"testing"
	"time"

	"github.com/egoist/mygo/plugins/terminal"

	"github.com/ykfq/goterm/internal/highlight"
)

type highlightTestConn struct {
	*highlight.Reader
	pw *io.PipeWriter
}

func (c *highlightTestConn) Write(p []byte) (int, error) { return len(p), nil }
func (c *highlightTestConn) Resize(int, int) error       { return nil }
func (c *highlightTestConn) Close() error                { return c.pw.Close() }

// The highlighter inserts SGR sequences into the SSH stream. This test runs the
// decorated bytes through the real Ghostty-backed terminal, so a sequence that
// broke parsing would show up as missing or mangled text.
func TestHighlightedOutputSurvivesTheTerminal(t *testing.T) {
	pr, pw := io.Pipe()
	conn := &highlightTestConn{Reader: highlight.NewReader(pr), pw: pw}
	term, err := terminal.New(terminal.Options{Conn: conn})
	if err != nil {
		t.Skipf("terminal engine unavailable: %v", err)
	}
	defer term.Close()

	const line = "error: disk full on /dev/sda1"
	go func() {
		_, _ = io.WriteString(pw, line+"\r\n")
	}()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(term.Text(), line) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("terminal did not show the plain line; text=%q", term.Text())
}
