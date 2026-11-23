package highlight

import (
	"strings"
	"testing"
)

// The user's own keywords ride the same rules as the built-ins: they colour
// plain text only, never the program's colour, and they must not turn one
// word into three colours by matching inside other words.

func paintWith(t *testing.T, words []string, line string) string {
	t.Helper()
	h := New()
	h.SetWords(words)
	got := h.paintLine(line)
	// The bytes must survive in order: colour is inserted, text is not edited.
	if plain := stripSGR(got); plain != line {
		t.Fatalf("text changed: %q -> %q", line, plain)
	}
	return got
}

func stripSGR(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '[' {
			j := i + 2
			for j < len(s) && (s[j] == ';' || (s[j] >= '0' && s[j] <= '9')) {
				j++
			}
			if j < len(s) && s[j] == 'm' {
				i = j + 1
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

func TestCustomWordIsColouredCaseInsensitively(t *testing.T) {
	for _, line := range []string{"queue: Out of Cheese: refilling", "queue: OUT OF CHEESE", "queue: out of cheese"} {
		got := paintWith(t, []string{"out of cheese"}, line)
		// The span keeps the text's own case; the colour is what must move.
		if strings.Count(got, brightMagenta) != 1 {
			t.Fatalf("want one coloured span in %q, got %q", line, got)
		}
	}
}

func TestCustomASCIIWordMatchesWholeWordsOnly(t *testing.T) {
	got := paintWith(t, []string{"zrk"}, "PZRK: zrk, (zrk!), Zrkzzz")
	// Only the two isolated "zrk" colour: PZRK and Zrkzzz have word bytes
	// touching the match.
	if n := strings.Count(got, brightMagenta); n != 2 {
		t.Fatalf("coloured %d spans, want 2 (bare err and (err!)): %q", n, got)
	}
	if strings.Contains(got, brightMagenta+"ZRK") || strings.Contains(got, brightMagenta+"Zrk") {
		t.Fatalf("matched inside a longer word: %q", got)
	}
}

func TestCustomCJKWordMatchesInsideText(t *testing.T) {
	// Chinese has no spaces; whole-word rules would make every multi-character
	// keyword dead, so non-ASCII keywords match as substrings.
	got := paintWith(t, []string{"内存不足"}, "java: 系统内存不足，进程被终止")
	if !strings.Contains(got, brightMagenta+"内存不足"+reset) {
		t.Fatalf("CJK keyword not coloured: %q", got)
	}
}

func TestCustomWordYieldsToAlreadyColouredSpans(t *testing.T) {
	// "ERROR" is a built-in; a user keyword covering the same bytes must not
	// stack a second colour on it.
	h := New()
	h.SetWords([]string{"ERROR"})
	got := h.paintLine("ERROR bad gateway")
	if strings.Contains(got, brightMagenta) {
		t.Fatalf("custom word overpainted the built-in: %q", got)
	}
	if !strings.Contains(got, red) && !strings.Contains(got, brightRed) {
		t.Fatalf("built-in colour lost: %q", got)
	}
}

func TestLongerWordWinsOverShorterInsideIt(t *testing.T) {
	got := paintWith(t, []string{"widget", "blue widget"}, "order a blue widget today")
	if strings.Count(got, brightMagenta) != 1 {
		t.Fatalf("expected one span, got %q", got)
	}
	if !strings.Contains(got, brightMagenta+"blue widget") {
		t.Fatalf("shorter word won: %q", got)
	}
}

func TestSetWordsFromEmptyToSomethingTakesEffectMidStream(t *testing.T) {
	h := New()
	if strings.Contains(h.paintLine("nothing configured\n"), brightMagenta) {
		t.Fatal("colour appeared with no words set")
	}
	h.SetWords([]string{"midstream"})
	if !strings.Contains(h.paintLine("found midstream marker"), brightMagenta+"midstream") {
		t.Fatal("SetWords did not take effect on the next line")
	}
	h.SetWords(nil)
	if strings.Contains(h.paintLine("found midstream marker"), brightMagenta) {
		t.Fatal("clearing the words left them coloured")
	}
}

// The keyword field carries a colour per line now. The words must come out in
// the colour the user typed, not in the default one.
func TestEachRulePaintsItsOwnColour(t *testing.T) {
	h := New()
	// Words the built-in sets do not claim, so what is measured is the rule's
	// colour and not which rule won: built-ins outrank the user's list.
	h.SetWords([]string{"GREEN: healthy|passed", "RED: exploded|doomed"})
	got := h.paintLine("healthy check, then exploded")
	for _, want := range []string{brightGreen + "healthy", brightRed + "exploded"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in %q", want, got)
		}
	}
	if strings.Contains(got, brightMagenta+"healthy") || strings.Contains(got, brightMagenta+"exploded") {
		t.Fatalf("a coloured rule fell back to the default colour: %q", got)
	}
}

func TestARGBRulePaintsInThatRGB(t *testing.T) {
	got := paintWith(t, []string{"#ff8800: deploy"}, "deploy finished")
	if !strings.Contains(got, "\x1b[38;2;255;136;0mdeploy") {
		t.Fatalf("24-bit colour not used: %q", got)
	}
}

func TestColouredWordsStillBowToTheProgram(t *testing.T) {
	h := New()
	h.SetWords([]string{"RED: error"})
	got := string(h.Process([]byte("\x1b[42merror: already green\x1b[0m\n")))
	if strings.Contains(got, brightRed) {
		t.Fatalf("a user colour overpainted the program's: %q", got)
	}
}
