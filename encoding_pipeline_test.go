package main

/*
 * The bytes between the remote shell and the terminal.
 *
 * A UTF-8 character can arrive split across two reads — vim repaints a screen a
 * character at a time, and a screen is not written in one packet. Every stage
 * between the session and the terminal has to pass those bytes through
 * untouched; the terminal itself is what puts a split character back together.
 *
 * These tests pin the Go half of that, because "vim shows Chinese as garbage
 * while cat shows it fine" is only diagnosable if this half is known to be
 * innocent.
 */
import (
	"io"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/highlight"
)

// oneByteReader hands out exactly one byte per Read, which is the worst case a
// byte stream can present to a decoder.
type oneByteReader struct {
	data []byte
}

func (r *oneByteReader) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, io.EOF
	}
	p[0] = r.data[0]
	r.data = r.data[1:]
	return 1, nil
}

func TestASplitChineseLineReachesTheTerminalUnchanged(t *testing.T) {
	const screen = "\x1b[?1049h\x1b[H\x1b[2J" +
		"if [ \"${rc}\" -eq 0 ]; then\r\n" +
		"    echo \"cms 全部迁移任务执行完成\"\r\n" +
		"else\r\n" +
		"    echo \"cms 存在失败的迁移任务，详见各 cmsN.log\"\r\n" +
		"fi\r\n"

	// The transcoder alone is byte-for-byte, split rune and all.
	encoded, err := io.ReadAll(encode.Reader(&oneByteReader{data: []byte(screen)}, encode.UTF8))
	if err != nil {
		t.Fatal(err)
	}
	if string(encoded) != screen {
		t.Fatalf("the transcoder changed a vim-style screen:\n got %q\nwant %q", encoded, screen)
	}

	// Through the highlighter, colour may be added — a pager showing plain
	// text is painted the way cat is — but not one byte of text may change, and
	// a rune that arrived in pieces is still one rune.
	r := highlight.NewReader(encode.Reader(&oneByteReader{data: []byte(screen)}, encode.UTF8))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(out) {
		t.Fatalf("the decorated screen is not valid UTF-8: %q", out)
	}
	if !strings.HasPrefix(string(out), "\x1b[?1049h\x1b[H\x1b[2J") {
		t.Fatalf("the screen's own control sequences were disturbed: %q", out)
	}
	if got, want := stripSGR(string(out)), stripSGR(screen); got != want {
		t.Fatalf("the pipeline changed the text of the screen:\n got %q\nwant %q", got, want)
	}
}

// A screen that brings its own colours is an editor's, not a pager's: vim
// paints every cell and anything we insert would collide with its redraws, so
// the bytes must come out exactly as they went in.
func TestAVimScreenIsNotPainted(t *testing.T) {
	const screen = "\x1b[?1049h\x1b[H\x1b[2J" +
		"\x1b[1;32mif\x1b[0m \x1b[1;31merror\x1b[0m: disk full\r\n" +
		"\x1b[7m~\x1b[0m\r\n"

	r := highlight.NewReader(encode.Reader(&oneByteReader{data: []byte(screen)}, encode.UTF8))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != screen {
		t.Fatalf("an editor's coloured screen was painted:\n got %q\nwant %q", out, screen)
	}
}

// With decoration on, the plain lines of `cat` gain colour — what they must not
// gain is a broken character. A rune that arrived in pieces is still one rune.
func TestHighlightingNeverBreaksACharacter(t *testing.T) {
	const text = "cms 全部迁移任务执行完成\r\n"
	r := highlight.NewReader(encode.Reader(&oneByteReader{data: []byte(text)}, encode.UTF8))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !utf8.Valid(out) {
		t.Fatalf("the decorated stream is not valid UTF-8: %q", out)
	}
	// Every character the file had is still there, in order; only SGR runs may
	// sit between them.
	plain := stripSGR(string(out))
	if plain != text {
		t.Fatalf("decorating changed the text:\n got %q\nwant %q", plain, text)
	}
}

// stripSGR removes the escape sequences the highlighter inserts, so what is
// left can be compared with the input.
func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
				j++
			}
			if j < len(s) {
				j++
			}
			i = j
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}
