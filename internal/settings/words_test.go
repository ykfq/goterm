package settings

import (
	"strings"
	"testing"
)

// One rule per line: a colour, a colon, words joined by "|". The user asked
// for exactly this shape, so the examples from their request are pinned here.
func TestTheKeywordFieldReadsOneColouredRulePerLine(t *testing.T) {
	rules := ParseWordRules("GREEN: info|healthy|pass\nRED: error|fatal|fail\n")
	if len(rules) != 2 {
		t.Fatalf("parsed %d rules: %+v", len(rules), rules)
	}
	if rules[0].Colour != "GREEN" || strings.Join(rules[0].Words, ",") != "info,healthy,pass" {
		t.Fatalf("first rule: %+v", rules[0])
	}
	if rules[1].Colour != "RED" || strings.Join(rules[1].Words, ",") != "error,fatal,fail" {
		t.Fatalf("second rule: %+v", rules[1])
	}
}

func TestAColourIsCaseInsensitiveAndSpacingIsFree(t *testing.T) {
	rules := ParseWordRules("  green :  INFO | healthy  \r\n")
	if len(rules) != 1 {
		t.Fatalf("parsed %+v", rules)
	}
	if rules[0].Colour != "GREEN" {
		t.Fatalf("colour kept as %q", rules[0].Colour)
	}
	if len(rules[0].Words) != 2 || rules[0].Words[0] != "INFO" || rules[0].Words[1] != "healthy" {
		t.Fatalf("words: %q", rules[0].Words)
	}
}

func TestARGBColourIsAccepted(t *testing.T) {
	rules := ParseWordRules("#FF8800: deploy|release\n")
	if len(rules) != 1 || rules[0].Colour != "#ff8800" {
		t.Fatalf("parsed %+v", rules)
	}
	// Short form and a broken value.
	if r := ParseWordRules("#f80: a"); len(r) != 1 || r[0].Colour != "#f80" {
		t.Fatalf("short hex: %+v", r)
	}
	if r := ParseWordRules("#ff88: a"); len(r) != 1 || r[0].Colour != "" {
		t.Fatalf("five hex digits were read as a colour: %+v", r)
	}
}

// The field held a bare list of words before colours existed. Those lines must
// keep meaning the same thing, including the commas and colons people typed.
func TestALineWithoutAColourIsStillAPlainList(t *testing.T) {
	rules := ParseWordRules("OOM\nbackup failed\nqueue: full\n")
	if len(rules) != 3 {
		t.Fatalf("parsed %+v", rules)
	}
	for _, r := range rules {
		if r.Colour != "" {
			t.Fatalf("a plain line got colour %q: %+v", r.Colour, r)
		}
	}
	if got := strings.Join(rules[1].Words, "|"); got != "backup failed" {
		t.Fatalf("a space split the word: %q", got)
	}
	if got := strings.Join(rules[2].Words, "|"); got != "queue: full" {
		t.Fatalf("a phrase with a colon was cut: %q", got)
	}
}

func TestAWordAfterAColourKeepsItsCommas(t *testing.T) {
	rules := ParseWordRules("RED: a,b\n")
	if len(rules) != 1 || len(rules[0].Words) != 1 || rules[0].Words[0] != "a,b" {
		t.Fatalf("parsed %+v", rules)
	}
}

func TestRulesRoundTripThroughTheirOwnText(t *testing.T) {
	for _, line := range []string{"RED: error|fatal", "#ff8800: deploy", "oom|out of memory"} {
		rules := ParseWordRules(line)
		if len(rules) != 1 {
			t.Fatalf("%q parsed to %+v", line, rules)
		}
		if got := rules[0].Line(); got != line {
			t.Fatalf("round trip changed %q to %q", line, got)
		}
	}
}

func TestTheFieldKeepsOneRulePerLine(t *testing.T) {
	got := ParseHighlightWords("RED: error\n\n   \nred: error\nGREEN: ok\n")
	if strings.Join(got, " / ") != "RED: error / GREEN: ok" {
		t.Fatalf("stored %+v", got)
	}
}

func TestATypoedColourIsReported(t *testing.T) {
	got := UnknownColours("GRREN: error\n#zz: x\nqueue: full\nred: fine\n")
	if strings.Join(got, ",") != "GRREN,#zz" {
		t.Fatalf("reported %+v", got)
	}
}

func TestNoColourWordsMeansNoRules(t *testing.T) {
	if rules := ParseWordRules(" \n\n\t\nRED:\n"); len(rules) != 0 {
		// "RED:" with nothing after it colours nothing; keeping the rule would
		// only add a line the highlighter has to skip.
		t.Fatalf("parsed %+v", rules)
	}
}
