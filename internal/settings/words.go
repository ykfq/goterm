package settings

import (
	"strings"
)

// The custom keyword field is one rule per line:
//
//	RED: error|fatal|fail
//	GREEN: info|healthy|pass
//	#ff8800: deploy|release
//	oom
//
// A colour names the words after the first colon. A line without a colour is a
// list of words in the default colour, which is what the field held before
// colours existed, so an old list keeps working untouched. Words are separated
// by "|" and matched case-insensitively.

// KeywordColours are the colour names a rule may start with, in the order the
// settings dialog lists them. "#RRGGBB" is accepted as well.
var KeywordColours = []string{
	"RED", "GREEN", "YELLOW", "BLUE", "MAGENTA", "CYAN", "WHITE", "GREY", "BLACK",
}

// WordRule is one parsed line of the keyword field: a colour, and the words it
// colours. Colour is empty for the default colour.
type WordRule struct {
	Colour string
	Words  []string
}

// Line writes the rule back as the field's text, in canonical form: the colour
// name as the dialog lists it, one space after the colon, words joined by "|".
func (r WordRule) Line() string {
	body := strings.Join(r.Words, "|")
	if r.Colour == "" {
		return body
	}
	return r.Colour + ": " + body
}

// ParseWordRules reads the keyword field's text into rules. Limits are applied
// here, once, so the highlighter and the stored file can never disagree about
// how many words there are: words longer than MaxHighlightWordLen are dropped,
// and counting stops at MaxHighlightWords.
//
// A line whose first colon is not preceded by a colour is a keyword list in the
// default colour, colon and all — that is what it meant before colours existed.
func ParseWordRules(text string) []WordRule {
	rules := make([]WordRule, 0, 8)
	budget := MaxHighlightWords
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		colour, body := "", line
		if i := strings.IndexByte(line, ':'); i >= 0 {
			if c, ok := canonicalColour(line[:i]); ok {
				colour, body = c, line[i+1:]
			}
		}
		words := splitWords(body, colour == "")
		if len(words) == 0 {
			continue
		}
		if len(words) > budget {
			words = words[:budget]
		}
		budget -= len(words)
		rules = append(rules, WordRule{Colour: colour, Words: words})
		if budget == 0 {
			break
		}
	}
	return rules
}

// splitWords cuts one rule's body into words. "|" always separates. The other
// two separators are the ones the field used before colours, kept only for a
// line without a colour: "RED: a,b" is one word, because a comma is part of a
// phrase somebody wants painted red.
func splitWords(body string, legacy bool) []string {
	fields := strings.FieldsFunc(body, func(r rune) bool {
		if r == '|' {
			return true
		}
		return legacy && (r == ',' || r == ';')
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" || len(f) > MaxHighlightWordLen {
			continue
		}
		out = append(out, f)
	}
	return out
}

// canonicalColour recognises a rule's colour token and returns it in canonical
// form: colour names uppercase, hex lowercase. "GRAY" is accepted as "GREY".
func canonicalColour(token string) (string, bool) {
	t := strings.TrimSpace(token)
	if t == "" {
		return "", false
	}
	if t[0] == '#' {
		hex := t[1:]
		if len(hex) != 3 && len(hex) != 6 {
			return "", false
		}
		for i := 0; i < len(hex); i++ {
			if !isHex(hex[i]) {
				return "", false
			}
		}
		return "#" + strings.ToLower(hex), true
	}
	up := strings.ToUpper(t)
	if up == "GRAY" {
		up = "GREY"
	}
	for _, name := range KeywordColours {
		if up == name {
			return name, true
		}
	}
	return "", false
}

func isHex(b byte) bool {
	return ('0' <= b && b <= '9') || ('a' <= b && b <= 'f') || ('A' <= b && b <= 'F')
}

// ParseHighlightWords turns the field's text into the lines that are stored:
// canonical, trimmed, without blanks, and without a line repeated in a
// different case. It is the field's "commit" — what the dialog saves when the
// text changes.
func ParseHighlightWords(text string) []string {
	rules := ParseWordRules(text)
	if len(rules) == 0 {
		return nil
	}
	out := make([]string, 0, len(rules))
	seen := make(map[string]bool, len(rules))
	for _, r := range rules {
		line := r.Line()
		key := strings.ToLower(line)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, line)
	}
	return out
}

// clampWords repairs stored rules the way ParseHighlightWords repairs typed
// ones, so a hand-edited settings.json cannot smuggle in a blank line, an
// oversized word or a thousand rules.
func clampWords(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return ParseHighlightWords(strings.Join(in, "\n"))
}

// UnknownColours lists colour names the field will not recognise, so the
// dialog can say so instead of letting a typo silently change nothing. Only
// tokens that look like an attempt at a colour are reported: an uppercase word
// or a "#" prefix. A lowercase phrase such as "queue: full" is a keyword with a
// colon in it, not a mistake.
func UnknownColours(text string) []string {
	var out []string
	seen := make(map[string]bool)
	for _, raw := range strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n") {
		line := strings.TrimSpace(raw)
		i := strings.IndexByte(line, ':')
		if i <= 0 {
			continue
		}
		token := strings.TrimSpace(line[:i])
		if token == "" || seen[token] {
			continue
		}
		if _, ok := canonicalColour(token); ok {
			continue
		}
		if !looksLikeColourToken(token) {
			continue
		}
		seen[token] = true
		out = append(out, token)
	}
	return out
}

// looksLikeColourToken reports whether a token someone put before a colon reads
// as an attempt at a colour: a "#" something, or an uppercase word.
func looksLikeColourToken(token string) bool {
	if token[0] == '#' {
		return true
	}
	letters := 0
	for i := 0; i < len(token); i++ {
		c := token[i]
		switch {
		case c == '_' || c == ' ':
		case 'A' <= c && c <= 'Z':
			letters++
		default:
			return false
		}
	}
	return letters > 0
}
