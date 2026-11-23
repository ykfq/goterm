package selection

import (
	"strings"
	"testing"
	"unicode/utf8"
)

var both = Options{Links: true, URLTrim: true}

func pick(t *testing.T, line, word string, col int, o Options) string {
	t.Helper()
	span, widened := Pick(line, col, word, o)
	if !widened {
		return word
	}
	return span.Text()
}

// The report that started this: a setting line cut at its `=`.
func TestAKeyValueLosesItsKey(t *testing.T) {
	line := "ST=Guangdong"
	// Clicked inside `Guangdong`, however the emulator cut it.
	for _, word := range []string{"Guangdong", "ST=Guangdong", ""} {
		for col := 3; col < len([]rune(line)); col++ {
			if got := pick(t, line, word, col, both); got != "Guangdong" {
				t.Fatalf("col %d, emulator word %q, selected %q", col, word, got)
			}
		}
	}
	// Clicked on the key, where the emulator had nothing or had `ST`: the
	// value is still the word that was meant.
	for _, word := range []string{"", "ST", "ST=Guangdong"} {
		if got := pick(t, line, word, 1, both); got != "Guangdong" {
			t.Fatalf("clicked on the key, emulator word %q, selected %q", word, got)
		}
	}
}

func TestAFlagKeepsItsDashes(t *testing.T) {
	if got := pick(t, "java -Dproxy=true", "java", 3, both); got != "java" {
		t.Fatalf("got %q", got)
	}
	span, _ := Pick("--mode=--verbose", 8, "--mode=--verbose", both)
	if span.Text() != "--verbose" {
		t.Fatalf("got %q", span.Text())
	}
}

func TestAValueStopsAtItsFirstSpace(t *testing.T) {
	// The emulator took `note=a` whole; the next word is its own and the value
	// ends where the emulator's word did.
	if got := pick(t, "note=a b", "note=a", 2, both); got != "a" {
		t.Fatalf("got %q", got)
	}
	// `key=value` with no space after the `=`.
	if got := pick(t, "ST=Guangdong", "ST=Guangdong", 2, both); got != "Guangdong" {
		t.Fatalf("got %q", got)
	}
}

// The other report: a URL cut at its slashes.
func TestAURLCutAtItsSlashesComesOutWhole(t *testing.T) {
	line := "pip3 install -i https://mirrors.tencent.com/pypi/simple"
	for _, word := range []string{"mirrors.tencent.com", "//mirrors.tencent.com", "mirrors", "tencent"} {
		if got := pick(t, line, word, 20, both); got != "https://mirrors.tencent.com/pypi/simple" {
			t.Fatalf("word %q gave %q", word, got)
		}
	}
}

func TestAURLKeepsItsQueryAndFragment(t *testing.T) {
	line := "see https://example.com/a?x=1&y=2#top, then stop"
	span, _ := Pick(line, strings.Index(line, "example")+2, "https://example.com/a", both)
	// The comma is not part of it.
	if got := span.Text(); got != "https://example.com/a?x=1&y=2#top" {
		t.Fatalf("got %q", got)
	}
}

func TestAnSSHAddressIsOneWord(t *testing.T) {
	line := "git clone git@github.com:ykfq/goterm.git"
	if got := pick(t, line, "github.com", 15, both); got != "git@github.com:ykfq/goterm.git" {
		t.Fatalf("got %q", got)
	}
}

func TestAValueWithAURLIsTheURL(t *testing.T) {
	line := "registry=https://mirrors.tencent.com/go"
	if got := pick(t, line, "registry=https://mirrors.tencent.com/go", 20, both); got != "https://mirrors.tencent.com/go" {
		t.Fatalf("got %q", got)
	}
}

// Everything a double click already got right stays exactly as it was.
func TestOrdinaryWordsAreNotWidened(t *testing.T) {
	cases := []struct{ line, word string }{
		{"see d:x:1:1", "d:x:1:1"},
		{"note app.go:12", "app.go:12"},
		{"errCode_and_more", "errCode_and_more"},
		{"Asia/Shanghai", "Asia/Shanghai"},
		{"log-2026.txt", "log-2026.txt"},
		{"plain words here", "words"},
		{"export FOO=$HOME/x", "FOO"},
	}
	for _, c := range cases {
		at := strings.Index(c.line, c.word)
		if at < 0 {
			t.Fatalf("%q is not in %q: the case is wrong, not the code", c.word, c.line)
		}
		if got := pick(t, c.line, c.word, utf8.RuneCountInString(c.line[:at])+1, both); got != c.word {
			t.Fatalf("%q in %q became %q", c.word, c.line, got)
		}
	}
}

func TestALocalPathInAValueIsNotAURL(t *testing.T) {
	// No scheme, no `//`: nothing here is a link. The value rule still
	// applies, and the URL rule adds nothing to it.
	if got := pick(t, "ST=/var/log", "/var/log", 4, both); got != "/var/log" {
		t.Fatalf("got %q", got)
	}
	if got := pick(t, "path=/a/b", "/a/b", 6, both); got != "/a/b" {
		t.Fatalf("got %q", got)
	}
}

// Turning a rule off leaves the emulator's word alone.
func TestEachRuleIsItsOwnSwitch(t *testing.T) {
	// The URL rule on, the value rule off: the emulator's word stays.
	if got := pick(t, "ST=abc", "", 1, Options{Links: true}); got != "ST" {
		t.Fatalf("the value rule trimmed a value: %q", got)
	}
	// Both rules off: the emulator's word, whatever it was.
	if got := pick(t, "ST/Guangdong", "ST/Guangdong", 4, Options{}); got != "ST/Guangdong" {
		t.Fatalf("got %q", got)
	}
}

// The emulator's word appears twice: the one clicked is widened.
func TestARepeatedWordIsWidenedWhereItWasClicked(t *testing.T) {
	line := "iso=a then iso=b"
	span, widened := Pick(line, strings.Index(line, "iso=b")+2, "iso", both)
	if !widened || span.Text() != "b" {
		t.Fatalf("got %q (widened %v)", span.Text(), widened)
	}
	// And the first one is widened at the first one.
	span, _ = Pick(line, 0, "iso", both)
	if span.Text() != "a" {
		t.Fatalf("the first value is %q", span.Text())
	}
}

func TestAnEmptyWordIsNothingToWiden(t *testing.T) {
	if _, widened := Pick("   ", 1, "", both); widened {
		t.Fatal("blank space was widened")
	}
}

func TestClickingTheValueOfAKeyValueLeavesIt(t *testing.T) {
	// Clicked inside `Guangdong`, the emulator already gave that word.
	if got := pick(t, "ST=Guangdong", "Guangdong", 5, both); got != "Guangdong" {
		t.Fatalf("got %q", got)
	}
}

func TestSkipLeavesGesturesTheEmulatorOwns(t *testing.T) {
	base := Press{Clicks: 2, Button: 0, Pointer: Mouse}
	if both.Skip(base) {
		t.Fatal("a plain double click with both rules on was skipped")
	}
	for name, ev := range map[string]Press{
		"triple click": {Clicks: 3, Pointer: Mouse},
		"single click": {Clicks: 1, Pointer: Mouse},
		"secondary":    {Clicks: 2, Button: 1, Pointer: Mouse},
		"middle":       {Clicks: 2, Button: 2, Pointer: Mouse},
		"shift-click":  {Clicks: 2, Mods: true, Pointer: Mouse},
		"pen":          {Clicks: 2, Pointer: Pen},
		"finger":       {Clicks: 2, Pointer: Touch},
	} {
		if !(Options{Links: true, URLTrim: true}).Skip(ev) {
			t.Fatalf("%s was not skipped", name)
		}
	}
	if !(Options{}).Skip(base) {
		t.Fatal("a double click with every rule off was not skipped")
	}
}

func TestLineFromTextFindsTheClickedRow(t *testing.T) {
	text := "one\r\ntwo\r\nline https://a.b/c and ST=Guangdong\r\n"
	line, x, ok := LineFromText(text, 5, 2)
	if !ok || line != "line https://a.b/c and ST=Guangdong" || x != 5 {
		t.Fatalf("got %q at %d (ok %v)", line, x, ok)
	}
	// A row past the end has nothing.
	if _, _, ok := LineFromText(text, 0, 9); ok {
		t.Fatal("a row past the end answered")
	}
	// A click past the last character still has a line.
	if line, x, ok := LineFromText(text, 200, 2); !ok || x != len([]rune(line)) {
		t.Fatalf("got %q at %d (ok %v)", line, x, ok)
	}
}

// Nothing a user did not ask for moves: every rule is anchored on what the
// emulator had, and turning both off hands the click back to it.
func TestTurningBothRulesOffLeavesTheEmulatorAlone(t *testing.T) {
	for _, line := range []string{"ST=Guangdong", "https://mirrors.tencent.com/x"} {
		left, right := wordRun(line, 5)
		word := line[left:right]
		span, widened := Pick(line, 5, word, Options{})
		if widened || span.Text() != word {
			t.Fatalf("%q: got %q (widened %v)", line, span.Text(), widened)
		}
	}
}

func TestAChineseValueKeepsItsCharacters(t *testing.T) {
	line := "城市=广东 今日"
	if got := pick(t, line, "广东", 4, both); got != "广东" {
		t.Fatalf("got %q", got)
	}
	// The emulator's `城市=广东` loses the key, counting runes, not bytes.
	if got := pick(t, line, "城市=广东", 3, both); got != "广东" {
		t.Fatalf("got %q", got)
	}
}
