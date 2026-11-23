// Package selection decides what a double click selects.
//
// libghostty-vt picks the word under the pointer by rules of its own, and
// nothing on this side of it can change them: the terminal's settings have no
// selection-word-chars. GoTerm therefore lets the emulator select as it always
// has and then widens that to what the user meant, reading the line outward from
// the click:
//
//   - a run that is a key=value gives its value: `ST=Guangdong` leaves
//     `Guangdong`;
//   - a run that is an address gives the whole address: `https://a.b/c?x=1`,
//     `//mirrors.tencent.com`, `mirrors.tencent.com` all come out as one URL;
//   - anything else is the word the emulator had, untouched.
//
// `errCode` keeps its underscore and `log-2026.txt` its dots: a rule that fired
// where nothing was asked would make an ordinary double click worse than it was.
package selection

import (
	"strings"
	"unicode/utf8"
)

// Options is what the user asked for in Settings.
type Options struct {
	// Links widens an address under the pointer to the whole address.
	Links bool
	// URLTrim narrows a key=value to its value.
	URLTrim bool
}

// Pointer is the device that pressed.
type Pointer uint8

const (
	Mouse Pointer = iota
	Pen
	Touch
)

// Press is the click being judged.
type Press struct {
	Clicks  int
	Button  int
	Mods    bool
	Pointer Pointer
}

// Skip reports whether a press needs no help at all — a middle or secondary
// button, a modified click, a triple click (a line), a pen, a finger — so the
// emulator's own gesture is left alone.
func (o Options) Skip(ev Press) bool {
	if ev.Button != 0 || ev.Mods || ev.Clicks != 2 {
		return true
	}
	if ev.Pointer == Pen || ev.Pointer == Touch {
		return true
	}
	return !o.Links && !o.URLTrim
}

// Span is what a double click selects: runes start up to end (exclusive) of
// one line's text.
type Span struct {
	Line       string
	Start, End int
}

// Text is the selected runes.
func (s Span) Text() string { return s.Line[s.Start:s.End] }

// Cell returns the cell a point in the view lands on: column and row of the
// grid, from the point in DIPs and the geometry of the view — the padding above
// and left of the grid, the scale, and a cell.
func Cell(ax, ay, padLeft, padTop, scale, cellW, cellH float32) (col, row int, ok bool) {
	if cellW <= 0 || cellH <= 0 || scale <= 0 {
		return 0, 0, false
	}
	col = int((ax*scale - padLeft) / cellW)
	row = int((ay*scale - padTop) / cellH)
	if col < 0 || row < 0 {
		return 0, 0, false
	}
	return col, row, true
}

// LineFromText returns the line of text that holds row, and the rune offset in
// it of cell col — what the visible screen shows, as Terminal.Text shows it.
func LineFromText(text string, col, row int) (line string, x int, ok bool) {
	if row < 0 || col < 0 {
		return "", 0, false
	}
	start := 0
	for r := 0; r < row; r++ {
		i := strings.IndexByte(text[start:], '\n')
		if i < 0 {
			return "", 0, false
		}
		start += i + 1
	}
	line = text[start:]
	if i := strings.IndexByte(line, '\n'); i >= 0 {
		line = line[:i]
	}
	line = strings.TrimSuffix(line, "\r")
	if n := utf8.RuneCountInString(line); col > n {
		col = n
	}
	return line, col, true
}

// Pick returns what a double click at rune offset x of line selects, given what
// the emulator itself selected on that line, and whether that differs from the
// emulator's own answer.
func Pick(line string, x int, word string, o Options) (Span, bool) {
	if !utf8.ValidString(line) || x < 0 || x > utf8.RuneCountInString(line) {
		return Span{}, false
	}
	own := Span{}
	if word != "" {
		if pos := locate(line, word, x); pos >= 0 {
			own = Span{Line: line, Start: pos, End: pos + len(word)}
		}
	}
	span, ok := choose(line, x, o)
	if !ok || span.End <= span.Start {
		return own, false
	}
	return span, span != own
}

// choose is the rules in order over one line: the value of a key=value, the
// whole address, and failing both the word the emulator had.
func choose(line string, x int, o Options) (Span, bool) {
	left, right := wordRun(line, x)
	word := Span{Line: line, Start: left, End: right}

	// A key=value gives its value, wherever in the pair the click was. An
	// address's `=` is in its query: the address rule has it.
	if o.URLTrim && !isURLRun(line[left:right]) {
		if eq, ok := equalsIn(line, left, right); ok {
			if v, ok := valueSpan(line, eq); ok {
				return v, true
			}
		}
	}
	// An address comes out whole, from its scheme to its query.
	if o.Links {
		if link, ok := urlSpan(line, left, right); ok {
			return link, true
		}
	}
	return word, word.End > word.Start
}

// wordRun is the run of word characters around a click: a letter, a digit, and
// the punctuation a name, a path or a setting is made of. An `=` is not one, and
// neither is a URL's query: those are the rules' business.
func wordRun(line string, x int) (int, int) {
	// A click past the end of the line lands on its last character.
	if line != "" && x >= utf8.RuneCountInString(line) {
		x = utf8.RuneCountInString(line) - 1
	}
	left := byteOffset(line, x)
	right := left
	// An address first: from its scheme, through its path and its query.
	if start, ok := addressLeft(line, left); ok {
		end := right
		for end < len(line) && (isWordByte(line[end]) || strings.IndexByte("?&#%=+~", line[end]) >= 0) {
			end++
		}
		for end > start && strings.IndexByte(".,;", line[end-1]) >= 0 {
			end--
		}
		if looksLikeURL(line[start:]) || looksLikeURL(hostOnly(line[start:end])) {
			return start, end
		}
	}
	// Otherwise the word the emulator would see.
	for right < len(line) && isWordByte(line[right]) && line[right] != '=' {
		right++
	}
	for left > 0 && isWordByte(line[left-1]) && line[left-1] != '=' {
		left--
	}
	return left, right
}

// hostOnly is a URL without its query or its fragment: enough to judge whether
// the address is whole.
func hostOnly(s string) string {
	if i := strings.IndexAny(s, "?#"); i >= 0 {
		return s[:i]
	}
	return s
}

// addressLeft finds where the address a click is in begins: its scheme and
// host, from the `://` before or after the click, or the `//` of a host that
// lost its scheme.
func addressLeft(line string, at int) (int, bool) {
	// The nearest `://` at or before the click.
	if i := strings.LastIndex(line[:min(at+1, len(line))], "://"); i >= 0 {
		start := i
		for start > 0 && (isAlnum(line[start-1]) || strings.IndexByte("+-.", line[start-1]) >= 0) {
			start--
		}
		if hasScheme(line[start:i]) {
			return start, true
		}
	}
	// A `//` just before the click: `//mirrors.tencent.com`.
	start := at
	for start > 0 && isWordByte(line[start-1]) {
		start--
	}
	if start >= 2 && line[start-2:start] == "//" {
		s := start - 2
		for s > 0 && (isWordByte(line[s-1]) || line[s-1] == ':') {
			s--
		}
		if hasScheme(line[s : start-2]) {
			return s, true
		}
		return start - 2, true
	}
	return 0, false
}

// equalsIn finds the `=` that makes the run a key=value: inside it, or just past
// its right edge when the emulator's word was the key.
func equalsIn(line string, left, right int) (int, bool) {
	if i := strings.IndexByte(line[left:right], '='); i >= 0 {
		return left + i, true
	}
	if right < len(line) && line[right] == '=' {
		return right, true
	}
	return 0, false
}

// valueSpan is the value after the `=` at eq: spaces after the `=` skipped, the
// value ending at the first space or quote, and its dashes kept when it is a
// flag. A value that holds a path or a host is not a setting's value — the
// address rule has that one — and is left to the word rule.
func valueSpan(line string, eq int) (Span, bool) {
	// The key must be one word on this side of the `=`.
	key := eq
	for key > 0 && isWordByte(line[key-1]) && line[key-1] != '=' && line[key-1] != '/' {
		key--
	}
	if key == eq {
		return Span{}, false
	}
	lead := eq + 1
	for lead < len(line) && line[lead] == ' ' {
		lead++
	}
	if lead >= len(line) {
		return Span{}, false
	}
	end := lead
	for end < len(line) && !isSpace(line[end]) && !isDelim(line[end]) {
		end++
	}
	value := line[lead:end]
	if value == "" || (value[0] == '-' && len(value) == 1) || strings.ContainsAny(value, "\\/:") {
		return Span{}, false
	}
	return Span{Line: line, Start: lead, End: end}, true
}

// urlSpan is the whole address around a click: the run through its query when
// it is one, or the host the emulator cut out of one.
func urlSpan(line string, left, right int) (Span, bool) {
	if isURLRun(line[left:right]) && looksLikeURL(line[left:right]) {
		return Span{Line: line, Start: left, End: right}, true
	}
	// Cut before its scheme: `//mirrors.tencent.com` in `https://…`.
	if start, ok := schemeLeft(line, left); ok {
		return Span{Line: line, Start: start, End: right}, true
	}
	// Cut at its host: `mirrors.tencent.com` in `https://mirrors…`.
	if start, ok := hostLeft(line, left); ok {
		return Span{Line: line, Start: start, End: right}, true
	}
	// user@host:path, as ssh reads it.
	if at := strings.LastIndexByte(line[left:right], '@'); at > 0 {
		start := left
		for start > 0 && isWordByte(line[start-1]) && line[start-1] != '=' {
			start--
		}
		if whole := line[start:right]; looksLikeURL(whole) {
			return Span{Line: line, Start: start, End: right}, true
		}
	}
	return Span{}, false
}

// schemeLeft finds the scheme a click just missed: the `//` right before the
// word, and the scheme before that.
func schemeLeft(line string, left int) (int, bool) {
	if left < 2 || line[left-2:left] != "//" {
		return 0, false
	}
	start := left - 2
	for start > 0 && (isWordByte(line[start-1]) || line[start-1] == ':') && line[start-1] != '=' {
		start--
	}
	return start, hasScheme(line[start : left-2])
}

// hostLeft finds the scheme and host a click inside an address's host or path
// belongs to: `https` of `https://mirrors`.
func hostLeft(line string, left int) (int, bool) {
	i := strings.LastIndex(line[:left], "://")
	if i < 0 {
		return 0, false
	}
	start := i
	for start > 0 && isWordByte(line[start-1]) && line[start-1] != '=' {
		start--
	}
	return start, true
}

// locate finds the emulator's word in line, nearest rune offset x. A word that
// appears twice is the one that was clicked.
func locate(line, word string, x int) int {
	best, bestDist := -1, 0
	for at := 0; at < len(line); {
		i := strings.Index(line[at:], word)
		if i < 0 {
			break
		}
		pos := at + i
		runePos := utf8.RuneCountInString(line[:pos])
		if runePos <= x && x <= runePos+utf8.RuneCountInString(word) {
			return pos
		}
		dist := runePos - x
		if dist < 0 {
			dist = -dist
		}
		if best < 0 || dist < bestDist {
			best, bestDist = pos, dist
		}
		at = pos + 1
	}
	return best
}

// looksLikeURL reports whether s is a whole address: a scheme and a host, a
// host behind `//`, or the user@host:path ssh reads.
func looksLikeURL(s string) bool {
	if i := strings.Index(s, "://"); i >= 0 {
		return hasScheme(s[:i]) && looksLikeHost(hostOf(s[i+3:]))
	}
	if strings.HasPrefix(s, "//") {
		return looksLikeHost(hostOf(s[2:]))
	}
	if at := strings.LastIndexByte(s, '@'); at > 0 {
		rest := s[at+1:]
		if i := strings.IndexAny(rest, "/:"); i > 0 {
			return looksLikeHost(rest[:i])
		}
	}
	return false
}

// hostOf cuts a URL's rest at its path, its query and its user.
func hostOf(rest string) string {
	if i := strings.IndexAny(rest, "/?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndexByte(rest, '@'); i >= 0 {
		rest = rest[i+1:]
	}
	if i := strings.LastIndexByte(rest, ':'); i >= 0 && allDigits(rest[i+1:]) {
		rest = rest[:i]
	}
	return rest
}

// looksLikeHost reports whether s reads as a host: a dotted name, a name with a
// port, or a bracketed address.
func looksLikeHost(s string) bool {
	if s == "" || strings.ContainsAny(s, "/?#") {
		return false
	}
	if strings.HasPrefix(s, "[") {
		return strings.Contains(s, "]")
	}
	if i := strings.LastIndexByte(s, ':'); i >= 0 {
		return i > 0 && allDigits(s[i+1:])
	}
	dot := strings.IndexByte(s, '.')
	return dot > 0 && dot < len(s)-1
}

// urlStart returns where the address in run begins: past a bracket that only
// preceded it, and at the scheme rather than a word before it.
func urlStart(run string) int {
	i := 0
	for i < len(run) && strings.IndexByte("(\"'`<>[{~", run[i]) >= 0 {
		i++
	}
	if j := strings.Index(run[i:], "://"); j >= 0 {
		k := i + j
		for k > i && (isAlnum(run[k-1]) || strings.IndexByte("+-.", run[k-1]) >= 0) {
			k--
		}
		if hasScheme(run[k : i+j]) {
			return k
		}
	}
	if j := strings.Index(run[i:], "//"); j > 0 && run[i-1] != '/' {
		return i + j
	}
	return i
}

// isURLRun reports whether s is, or holds, an address.
func isURLRun(s string) bool {
	return strings.Contains(s, "://") || strings.HasPrefix(s, "//")
}

// hasScheme reports whether s is a scheme name, as `https` or `git+ssh`.
func hasScheme(s string) bool {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '+' || c == '-' || c == '.' {
			continue
		}
		return false
	}
	return true
}

// isWordByte is a byte a word is made of: a letter, a digit, and the
// punctuation a name, a path or a setting carries.
func isWordByte(c byte) bool {
	return isAlnum(c) || strings.IndexByte(".-_@:/", c) >= 0
}

func isAlnum(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c >= 0x80
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

func isDelim(c byte) bool {
	return strings.IndexByte("\"'`()[]{}<>,;", c) >= 0
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// byteOffset is the byte position of rune x in s.
func byteOffset(s string, x int) int {
	for i := range s {
		if x == 0 {
			return i
		}
		x--
	}
	return len(s)
}
