package highlight

import (
	"regexp"
	"strings"
	"testing"
)

var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func strip(s string) string { return ansi.ReplaceAllString(s, "") }

func feed(h *Highlighter, chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.Write(h.Process([]byte(chunk)))
	}
	return out.String()
}

func TestPlainErrorLineIsHighlightedWithoutLosingBytes(t *testing.T) {
	in := "Nov 10 12:00:00 web-1 app[42]: ERROR: disk full\n"
	out := feed(New(), in)
	if out == in {
		t.Fatalf("expected colour in %q", out)
	}
	if got := strip(out); got != in {
		t.Fatalf("bytes changed:\n got %q\nwant %q", got, in)
	}
	if !strings.Contains(out, brightRed) {
		t.Fatalf("error keyword not red: %q", out)
	}
}

func TestProgramColourWins(t *testing.T) {
	in := "\x1b[32merror: already coloured\x1b[0m\n"
	if got := feed(New(), in); got != in {
		t.Fatalf("highlighter touched a tinted line:\n got %q\nwant %q", got, in)
	}
}

func TestAlternateScreenIsLeftAlone(t *testing.T) {
	h := New()
	if got := feed(h, "\x1b[?1049h"); got != "\x1b[?1049h" {
		t.Fatalf("enter sequence changed: %q", got)
	}
	in := "func main() { error := 1 }\n"
	if got := feed(h, in); got != in {
		t.Fatalf("editor output was painted: %q", got)
	}
	if got := feed(h, "\x1b[?1049l"); got != "\x1b[?1049l" {
		t.Fatalf("leave sequence changed: %q", got)
	}
	plain := "error: back to the shell\n"
	if got := feed(h, plain); got == plain {
		t.Fatalf("highlighting did not resume after leaving the alternate screen")
	}
}

func TestCursorAddressedFragmentIsLeftAlone(t *testing.T) {
	h := New()
	if got := feed(h, "\x1b[10;1H", "error: status line"); got != "\x1b[10;1Herror: status line" {
		t.Fatalf("screen fragment was painted: %q", got)
	}
	feed(h, "\n")
	if got := feed(h, "error: ordinary line\n"); strip(got) != "error: ordinary line\n" || got == "error: ordinary line\n" {
		t.Fatalf("ordinary output was not painted: %q", got)
	}
}

func TestEscapeSequenceSplitAcrossChunksSurvives(t *testing.T) {
	h := New()
	if got := feed(h, "\x1b[?10"); got != "" {
		t.Fatalf("incomplete sequence leaked early: %q", got)
	}
	if got := feed(h, "49h"); got != "\x1b[?1049h" {
		t.Fatalf("sequence was corrupted: %q", got)
	}
	in := "error: hidden in the editor\n"
	if got := feed(h, in); got != in {
		t.Fatalf("painted inside the alternate screen: %q", got)
	}
}

func TestBinaryAndControlLinesAreLeftAlone(t *testing.T) {
	for _, in := range []string{
		"er\x00ror: binary\n",
		"bad \xff error\n",
		"bell \a error\n",
	} {
		if got := feed(New(), in); got != in {
			t.Fatalf("binary line was painted: %q -> %q", in, got)
		}
	}
}

func TestVeryLongLinesAreLeftAlone(t *testing.T) {
	in := strings.Repeat("a", maxLineBytes+1) + " error\n"
	if got := feed(New(), in); got != in {
		t.Fatalf("long line was scanned")
	}
}

func TestShellCommandLineGetsVocabularyColours(t *testing.T) {
	in := "ls -la /var/log\n"
	out := feed(New(), in)
	if strip(out) != in {
		t.Fatalf("bytes changed: %q", strip(out))
	}
	if !strings.Contains(out, brightCyan) || !strings.Contains(out, yellow) {
		t.Fatalf("command and option not coloured: %q", out)
	}
}

func TestNginxAccessLineKeepsShape(t *testing.T) {
	in := `127.0.0.1 - - [10/Nov/2026:12:00:00 +0800] "GET /x HTTP/1.1" 200 12 "-" "curl"` + "\n"
	out := feed(New(), in)
	if strip(out) != in {
		t.Fatalf("bytes changed: %q", strip(out))
	}
	if !strings.Contains(out, green) {
		t.Fatalf("status code was not coloured: %q", out)
	}
}

func TestDisabledHighlighterPassesEverythingThrough(t *testing.T) {
	r := NewReader(strings.NewReader("error: quiet\n"))
	r.SetEnabled(false)
	buf := make([]byte, 64)
	n, err := r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) != "error: quiet\n" {
		t.Fatalf("disabled reader decorated output: %q", buf[:n])
	}

	r = NewReader(strings.NewReader("error: loud\n"))
	buf = make([]byte, 64)
	n, err = r.Read(buf)
	if err != nil {
		t.Fatal(err)
	}
	if string(buf[:n]) == "error: loud\n" {
		t.Fatalf("enabled reader did not decorate output")
	}
}

func TestReaderPreservesBytesAcrossSmallReads(t *testing.T) {
	in := "plain text with 10.0.0.8 and https://example.com/x\n"
	r := NewReader(strings.NewReader(in))
	var got strings.Builder
	buf := make([]byte, 7)
	for {
		n, err := r.Read(buf)
		got.Write(buf[:n])
		if err != nil {
			break
		}
	}
	if strip(got.String()) != in {
		t.Fatalf("reader lost or reordered bytes:\n got %q\nwant %q", strip(got.String()), in)
	}
}

func TestBoldIsRestoredAfterHighlight(t *testing.T) {
	in := "\x1b[1mERROR: bold line\x1b[0m\n"
	out := feed(New(), in)
	if strip(out) != strip(in) {
		t.Fatalf("bytes changed: %q -> %q", strip(in), strip(out))
	}
	if !strings.Contains(out, "\x1b[1m") {
		t.Fatalf("caller's bold was not replayed: %q", out)
	}
}
