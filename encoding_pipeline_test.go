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

func TestASplitChineseLineReachesTheTerminalByteForByte(t *testing.T) {
	const screen = "\x1b[?1049h\x1b[H\x1b[2J" +
		"if [ \"${rc}\" -eq 0 ]; then\r\n" +
		"    echo \"cms 全部迁移任务执行完成\"\r\n" +
		"else\r\n" +
		"    echo \"cms 存在失败的迁移任务，详见各 cmsN.log\"\r\n" +
		"fi\r\n"

	r := highlight.NewReader(encode.Reader(&oneByteReader{data: []byte(screen)}, encode.UTF8))
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != screen {
		t.Fatalf("the pipeline changed a vim-style screen:\n got %q\nwant %q", out, screen)
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
