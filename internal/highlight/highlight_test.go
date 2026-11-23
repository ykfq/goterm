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

func TestAlternateScreenThatColoursIsLeftAlone(t *testing.T) {
	h := New()
	if got := feed(h, "\x1b[?1049h"); got != "\x1b[?1049h" {
		t.Fatalf("enter sequence changed: %q", got)
	}
	// vim paints its cells with palette colours; from then on the screen is its.
	if got := feed(h, "\x1b[1;32m"); got != "\x1b[1;32m" {
		t.Fatalf("style sequence changed: %q", got)
	}
	for _, in := range []string{
		"\x1b[1;1Hfunc main() { error := 1 }",
		"\x1b[2;1H\x1b[0mFATAL: out of memory",
	} {
		if got := feed(h, in); got != in {
			t.Fatalf("self-coloured editor screen was painted: %q", got)
		}
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
	feed(h, "\x1b[31mred\x1b[0m\n")
	if got := feed(h, "\x1b[10;1H", "error: status line"); got != "\x1b[10;1Herror: status line" {
		t.Fatalf("screen fragment of a self-coloured program was painted: %q", got)
	}
	feed(h, "\n")
	if got := feed(h, "error: ordinary line\n"); strip(got) != "error: ordinary line\n" || got == "error: ordinary line\n" {
		t.Fatalf("ordinary output was not painted: %q", got)
	}
}

// less opens the alternate screen and redraws each row with an absolute cursor
// move. A file of plain text has no colour of its own, so those rows are lines
// of output and get the same treatment cat would give them.
func TestPagerOnPlainTextIsHighlighted(t *testing.T) {
	h := New()
	feed(h, "\x1b[?1049h", "\x1b[H\x1b[2J")
	row := "\x1b[3;1HNov 10 12:00:00 web-1 app[42]: ERROR: disk full"
	out := feed(h, row)
	if out == row {
		t.Fatalf("pager row was not painted: %q", out)
	}
	if got := strip(out); got != strip(row) {
		t.Fatalf("bytes changed:\n got %q\nwant %q", got, strip(row))
	}
	if !strings.Contains(out, brightRed) {
		t.Fatalf("ERROR keyword not red in pager row: %q", out)
	}
	// Bold is not colour: a pager that only bolds its status line is still
	// showing plain text, and the next row is still painted.
	out = feed(h, "\x1b[1m", "\x1b[24;1H", "FATAL: out of memory")
	if !strings.Contains(out, brightRed) {
		t.Fatalf("row after a bold-only status line was not painted: %q", out)
	}
	if !strings.Contains(out, "\x1b[1m") {
		t.Fatalf("program's bold was not replayed: %q", out)
	}
}

// less marks a search hit with reverse video. That is a mark on one span, not a
// palette for the screen: the hit's own row stays untouched, and every other row
// keeps being painted — a pager must not lose its colours because it was
// searched.
func TestAPagerWithASearchHitKeepsPainting(t *testing.T) {
	h := New()
	feed(h, "\x1b[?1049h")
	hit := "\x1b[7mERROR\x1b[m: this row is the hit\r\n"
	if got := feed(h, hit); got != hit {
		t.Fatalf("the program's own marked row was painted: %q", got)
	}
	row := "FATAL: the next row is not the hit\r\n"
	out := feed(h, row)
	if !strings.Contains(out, brightRed) {
		t.Fatalf("row after a search hit was not painted: %q", out)
	}
	if got := strip(out); got != row {
		t.Fatalf("the row's text changed: %q", got)
	}
}

// The bytes below are the shape of a real `less -R` session, captured with
// script(1) on a 200-column pty: enter the alternate screen, print rows that end
// with a bare SGR reset, page with `\x1b[H` + `\x1bM` (cursor home, reverse
// index), and leave. less picks no colour of its own here, so every row is a line
// of text and has to be painted like cat would paint it.
func TestTheShapeOfARealLessSessionIsPainted(t *testing.T) {
	const trace = "\x1b[?1049h\x1b[?1h\x1b=" +
		"Nov 10 12:01:00 web-1 app[1]: ERROR: disk full on /dev/sda1 from 10.0.0.1\x1b[m\r\n" +
		"Nov 10 12:02:00 web-1 app[2]: ERROR: disk full on /dev/sda1 from 10.0.0.2\x1b[m\r\n" +
		"\x1b[H\x1bMNov 10 12:31:00 web-1 app[151]: ERROR: disk full on /dev/sda1 from 10.0.0.151\x1b[m\r\n" +
		"\x1b[?1049l\x1b[?1l\x1b>"

	h := New()
	var out strings.Builder
	// The chunks cut the stream the way the network does: inside the enter
	// sequence, inside a row, and around a cursor move.
	cuts := []int{7, 120, len(trace) - 40, len(trace)}
	at := 0
	for _, cut := range cuts {
		out.Write(h.Process([]byte(trace[at:cut])))
		at = cut
	}
	got := out.String()

	if !strings.Contains(got, "\x1b[?1049h") || !strings.Contains(got, "\x1b[?1049l") {
		t.Fatalf("the screen's own sequences were disturbed: %q", got)
	}
	if n := strings.Count(got, "\x1b[1;31mERROR"); n != 3 {
		t.Fatalf("expected 3 painted ERROR keywords, got %d: %q", n, got)
	}
	if plain, want := strip(got), strip(trace); plain != want {
		t.Fatalf("decorating changed the text:\n got %q\nwant %q", plain, want)
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
	// An editor's screen: it colours, so the line is its own.
	feed(h, "\x1b[1;32m")
	in := "error: hidden in the editor\n"
	if got := feed(h, in); got != in {
		t.Fatalf("painted inside a self-coloured alternate screen: %q", got)
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
