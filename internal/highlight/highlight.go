// Package highlight decorates terminal output with colour, the way MobaXterm
// does: recognised words in the output get a colour, nothing else about the
// stream changes.
//
// Three rules keep this honest:
//
//  1. Only bytes between escape sequences are touched. The scanner walks
//     CSI/OSC/charset sequences whole and never inserts anything inside one, so
//     cursor movement, titles and charset switches survive intact.
//  2. The stream is never edited, only decorated. Existing bytes are copied out
//     unchanged; the only additions are SGR sequences, which are zero-width.
//  3. The program's colour wins. When the remote program picks a colour for a
//     line (grep --color, ls --color, a compiler diagnostic), the highlighter
//     stands down for that line.
//
// Full-screen paint is judged by one question: has this program used colour?
// vim, top and nano have, and inserting colours into a screen they already
// colour fights their own redraws, so the highlighter stands down for as long
// as that screen lasts. A pager showing plain text (less on most files) has
// not, and is painted exactly like cat: its cursor-addressed rows are lines of
// text, and colouring them is the whole point. Plain output from cat, tail,
// head, ps and tee is what this package was built for; less is now included.
package highlight

import (
	"strings"
	"sync"
	"sync/atomic"
	"unicode/utf8"
)

// Anything bigger than this is a paste or a cat of a big file: skip the scan.
const maxScanBytes = 512 * 1024

// A single 64 KiB "line" is a minified JSON blob or a base64 dump, not a log
// entry: scanning every rule over it costs real time and cannot look good.
const maxLineBytes = 8192

// trzsz's own banner: colouring it would only confuse the eye and the parser.
const transferBanner = "::TRZSZ:TRANSFER:"

// Highlighter carries one terminal's stream state: the SGR sequences seen so
// far and whether the alternate screen is showing. One instance per terminal;
// it is not safe for concurrent use (Reader adds the lock).
type Highlighter struct {
	sets       []*ruleSet
	vocabulary []*ruleSet
	always     []*ruleSet

	// style is the SGR state the program has established, replayed after each
	// local highlight so the program's colours continue where ours stop.
	style string
	// lineTinted is set once the program picks a colour on the line being
	// painted. Highlighting on top of that competes with the one thing the
	// remote program was pointing at, so for the rest of that line we keep
	// quiet. It is per line, not per style state: grep colours a match and does
	// not always reset afterwards.
	lineTinted bool
	// altScreen is true while a full-screen program owns the screen.
	altScreen bool
	// screenHint is set once a program positions the cursor absolutely. While
	// it is set, a fragment without a line break is that program's screen
	// painting rather than a line of output.
	screenHint bool
	// colourSeen is set once the program owning the current screen has picked
	// a foreground or background colour. It is what separates vim or top (paint
	// self-styled cells; our colour would collide) from less on a plain file
	// (paints plain text rows; there is no palette to collide with). Reverse
	// video does not set it: less marks a search hit that way, and a pager with
	// one highlight must not lose all the others. Cleared when a different
	// screen regime begins, because the next program has its own palette.
	colourSeen bool
	// carry is an escape sequence the chunk boundary cut in half. It is carried
	// to the next chunk rather than guessed at, so a colour can never be
	// inserted inside a cursor-movement sequence that arrived in two pieces.
	carry string

	// enabled is false while the user has keyword highlighting turned off. The
	// scanner still runs so alternate-screen and style state stay accurate; paint
	// simply copies text through. It is atomic because the settings dialog flips
	// it while the terminal is reading.
	enabled atomic.Bool

	// words are the user's own keywords, compiled by SetWords. The settings
	// dialog writes them while the terminal's read goroutine paints lines,
	// so the slice is swapped whole under wordsMu and read once per line.
	wordsMu sync.Mutex
	words   []wordRule

	// Scratch buffers, reused across lines to keep large outputs allocation
	// free once the terminal has seen its widest line.
	mask  []byte
	edits []edit
}

// New returns a highlighter with every built-in rule set enabled.
func New() *Highlighter {
	h := &Highlighter{}
	h.enabled.Store(true)
	h.sets = allSets()
	for _, set := range h.sets {
		if set.vocabulary {
			h.vocabulary = append(h.vocabulary, set)
		} else {
			h.always = append(h.always, set)
		}
	}
	return h
}

// SetEnabled turns decoration on or off mid-stream. The stream itself is never
// interrupted; only what gets inserted into it changes.
func (h *Highlighter) SetEnabled(on bool) { h.enabled.Store(on) }

// SetWords replaces the user's custom keywords, effective from the next line.
// Like SetEnabled it never interrupts the stream. Built-in rules keep running;
// a custom word that lands where the program or a built-in rule already put
// colour loses, because competing colours on one span read as a bug.
func (h *Highlighter) SetWords(words []string) {
	rules := compileWords(words)
	// No Reset: the stream state (style, screen mode) has nothing to do with
	// the word list, and dropping it would replay a cleared SGR state and let
	// an open program colour leak until the next sequence arrives.
	h.wordsMu.Lock()
	h.words = rules
	h.wordsMu.Unlock()
}

// Reset forgets the stream state after a clear, a reconnect, or a tab swap.
func (h *Highlighter) Reset() {
	h.style = ""
	h.lineTinted = false
	h.altScreen = false
	h.screenHint = false
	h.colourSeen = false
	h.carry = ""
}

// Process decorates one chunk and returns the bytes for the terminal. It never
// loses, reorders or rewrites an input byte; anything it adds is an SGR
// sequence.
func (h *Highlighter) Process(chunk []byte) []byte {
	if len(chunk) == 0 {
		return nil
	}
	if len(chunk) > maxScanBytes {
		tail := h.carry
		h.carry = ""
		if tail == "" {
			return chunk
		}
		return append([]byte(tail), chunk...)
	}

	text := h.carry + string(chunk)
	h.carry = ""
	if text == "" {
		return nil
	}

	var out strings.Builder
	out.Grow(len(text) + len(text)/16)
	runStart := 0
	i := 0
	for i < len(text) {
		if text[i] != 0x1b {
			i++
			continue
		}
		if i > runStart {
			h.paint(text[runStart:i], &out)
		}
		end, complete := escapeEnd(text, i)
		if !complete {
			// Wait for the rest of it, unless it never finishes; the carry cap
			// keeps a truncated stream from swallowing bytes forever.
			if len(text)-i > 32 {
				out.WriteString(text[i:])
			} else {
				h.carry = text[i:]
			}
			return []byte(out.String())
		}
		sequence := text[i:end]
		h.observe(sequence)
		out.WriteString(sequence)
		i = end
		runStart = end
	}
	if runStart < len(text) {
		h.paint(text[runStart:], &out)
	}
	return []byte(out.String())
}

// paint colours one stretch of ordinary text (no escape sequences in it).
func (h *Highlighter) paint(run string, out *strings.Builder) {
	if run == "" {
		return
	}
	if !h.enabled.Load() {
		if strings.Contains(run, "\n") {
			h.lineTinted = false
			h.screenHint = false
		}
		out.WriteString(run)
		return
	}
	if h.altScreen && h.colourSeen {
		out.WriteString(run)
		return
	}
	// A fragment with no line break after an absolute cursor move is a screen
	// repaint, not a line of output. A pager in a plain screen repaints plain
	// text, and a repaint of plain text is a line like any other.
	if h.screenHint && h.colourSeen && !strings.Contains(run, "\n") {
		out.WriteString(run)
		return
	}
	if strings.Contains(run, "\n") {
		h.screenHint = false
	}

	start := 0
	for i := 0; i < len(run); i++ {
		if run[i] != '\n' {
			continue
		}
		h.writeLine(run[start:i+1], out)
		h.lineTinted = false
		start = i + 1
	}
	if start < len(run) {
		h.writeLine(run[start:], out)
	}
}

func (h *Highlighter) writeLine(line string, out *strings.Builder) {
	if h.lineTinted || silent(line) || !plausibleText(line) {
		out.WriteString(line)
		return
	}
	out.WriteString(h.paintLine(line))
}

// silent reports stretches no rule can say anything useful about.
func silent(line string) bool {
	if strings.Contains(line, transferBanner) {
		return true
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') {
			return false
		}
	}
	return true
}

// plausibleText rejects binary and control-laden output. Runs never contain ESC
// (the scanner split those out), but a `cat` of a binary file still carries NUL
// and invalid UTF-8, which no text rule should paint.
func plausibleText(line string) bool {
	if !utf8.ValidString(line) {
		return false
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if c < 0x20 {
			switch c {
			case '\t', '\r', '\n':
			default:
				return false
			}
			continue
		}
		if c == 0x7f {
			return false
		}
	}
	return true
}

func (h *Highlighter) paintLine(line string) string {
	if len(line) > maxLineBytes {
		return line
	}
	if cap(h.mask) < len(line) {
		h.mask = make([]byte, len(line))
	}
	mask := h.mask[:len(line)]
	clear(mask)
	h.edits = h.edits[:0]

	for _, set := range h.setsForLine(line) {
		applySet(line, set.rules, mask, &h.edits)
	}
	for _, set := range h.always {
		applySet(line, set.rules, mask, &h.edits)
	}
	h.wordsMu.Lock()
	words := h.words
	h.wordsMu.Unlock()
	if len(words) > 0 {
		applyWords(line, words, mask, &h.edits)
	}
	if len(h.edits) == 0 {
		return line
	}
	sortEdits(h.edits)

	var out strings.Builder
	out.Grow(len(line) + len(h.edits)*16)
	at := 0
	for _, e := range h.edits {
		out.WriteString(line[at:e.start])
		// Drop the caller's colour, add ours, then put the caller's back.
		out.WriteString(reset)
		out.WriteString(e.sgr)
		out.WriteString(line[e.start:e.end])
		out.WriteString(reset)
		out.WriteString(h.style)
		at = e.end
	}
	out.WriteString(line[at:])
	return out.String()
}

// setsForLine decides which vocabulary a line speaks. Data-shape and keyword
// rules are not included here; they run for every line after the vocabulary
// had its pick. One line, one vocabulary: a syslog line is read with syslog
// rules, not with the shell's.
func (h *Highlighter) setsForLine(line string) []*ruleSet {
	find := func(id string) *ruleSet {
		for _, set := range h.vocabulary {
			if set.id == id {
				return set
			}
		}
		return nil
	}
	if set := find("nginx"); set != nil && set.sniff(line) {
		return []*ruleSet{set}
	}
	if set := find("nginx-conf"); set != nil && set.sniff(line) {
		return []*ruleSet{set}
	}
	if set := find("syslog"); set != nil && set.sniff(line) {
		return []*ruleSet{set}
	}
	if set := find("bash"); set != nil && set.sniff(line) {
		if shell := find("shell"); shell != nil {
			return []*ruleSet{set, shell}
		}
		return []*ruleSet{set}
	}
	if set := find("shell"); set != nil {
		return []*ruleSet{set}
	}
	return nil
}

// observe tracks what the program's own sequences mean.
func (h *Highlighter) observe(sequence string) {
	if len(sequence) < 3 || !strings.HasPrefix(sequence, "\x1b[") {
		return
	}
	body := sequence[2 : len(sequence)-1]
	final := sequence[len(sequence)-1]

	switch final {
	case 'h', 'l':
		code := ""
		if strings.HasPrefix(body, "?") {
			code = body[1:]
		}
		if code == "1049" || code == "1047" || code == "47" {
			h.altScreen = final == 'h'
			// Style belongs to the program; when the program is gone, it goes
			// with it. Otherwise the shell prompt after :q came back tinted with
			// vim's palette.
			h.style = ""
			h.lineTinted = false
			h.screenHint = false
			// A new screen regime: the pager or editor that owned the last
			// one is gone, and this one has to earn its stand-down by
			// painting colour of its own.
			h.colourSeen = false
		}
		return
	case 'H', 'f':
		// Absolute cursor addressing: the program is painting cells, not
		// printing lines.
		h.screenHint = true
		return
	case 'm':
		if body == "" || body == "0" {
			h.style = ""
			return
		}
		if sgrPicksColour(body) {
			h.lineTinted = true
		}
		if sgrPicksPalette(body) {
			h.colourSeen = true
		}
		// A sequence that starts with a reset replaces the accumulated state;
		// anything else adds to it.
		if strings.HasPrefix(body, "0;") {
			h.style = sequence
		} else {
			h.style += sequence
		}
		if len(h.style) > 96 {
			h.style = h.style[len(h.style)-96:]
		}
	}
}

// sgrPicksColour reports whether an SGR body picks an explicit colour.
// Bold, dim, italic and underline are style, not colour, and do not silence a
// highlight; palette, 256-colour, truecolor and reverse video do.
func sgrPicksColour(body string) bool {
	colour, _ := sgrColourKind(body)
	return colour
}

// sgrPicksPalette reports whether an SGR body picks a foreground or background
// colour — the mark of a program with a palette of its own (vim's syntax, top's
// meters, a bastion's menu). Reverse video deliberately does not count: it marks
// a span rather than styling a screen, and `less` marks a search hit that way.
func sgrPicksPalette(body string) bool {
	_, palette := sgrColourKind(body)
	return palette
}

func sgrColourKind(body string) (colour, palette bool) {
	parts := strings.Split(body, ";")
	for i := 0; i < len(parts); i++ {
		raw := parts[i]
		code := 0
		if raw != "" {
			code = atoi(raw)
			if code < 0 {
				continue
			}
		}
		if code == 38 || code == 48 {
			if i+1 >= len(parts) {
				continue
			}
			mode := atoi(parts[i+1])
			if mode == 5 {
				colour, palette = true, true
				i += 2
			} else if mode == 2 {
				colour, palette = true, true
				i += 4
			}
			continue
		}
		switch {
		case code == 7 || code == 109:
			colour = true
		case (code >= 30 && code <= 37) || (code >= 90 && code <= 97),
			(code >= 40 && code <= 47) || (code >= 100 && code <= 107):
			colour, palette = true, true
		}
	}
	return colour, palette
}

func atoi(s string) int {
	n := 0
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return -1
		}
		n = n*10 + int(s[i]-'0')
		if n > 1<<20 {
			return -1
		}
	}
	return n
}

// escapeEnd reports where an escape sequence starting at i ends, and whether it
// ended at all: a sequence cut off by the end of the chunk reports complete
// false so the caller can wait for the rest instead of painting inside it.
func escapeEnd(text string, i int) (end int, complete bool) {
	if i+1 >= len(text) {
		return len(text), false
	}
	switch text[i+1] {
	case '[':
		j := i + 2
		for j < len(text) && text[j] >= 0x30 && text[j] <= 0x3f {
			j++
		}
		for j < len(text) && text[j] >= 0x20 && text[j] <= 0x2f {
			j++
		}
		if j < len(text) {
			return j + 1, true
		}
		return len(text), false
	case ']', 'P', 'X', '^', '_':
		j := i + 2
		for j < len(text) {
			if text[j] == 0x07 {
				return j + 1, true
			}
			if text[j] == 0x1b && j+1 < len(text) && text[j+1] == '\\' {
				return j + 2, true
			}
			j++
		}
		return len(text), false
	case '(', ')', '*', '+':
		if len(text) >= i+3 {
			return i + 3, true
		}
		return len(text), false
	}
	if len(text) >= i+2 {
		return i + 2, true
	}
	return len(text), true
}
