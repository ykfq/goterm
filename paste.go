package main

import (
	"strings"
	"unicode/utf8"

	"github.com/egoist/mygo/transfer"
)

// Paste.
//
// Pasting into a terminal sends the clipboard's bytes to the remote program
// unchanged, and two things in a clipboard are worth handling before that:
//
//   - Escape sequences. Text copied from a coloured terminal, a log viewer or
//     a web page carries SGR runs (`ESC[1;32m`), cursor moves and the like.
//     A shell does not interpret them; it echoes their pieces, and what the
//     user sees is a trail of characters with semicolons in it stuck to the
//     pasted text.
//   - Many lines. Every one of them runs as soon as it lands, which is how a
//     paste turns into a batch job nobody chose to start.
//
// So pastes are cleaned of terminal control sequences, and a multi-line paste
// asks first — the same bargain Windows Terminal strikes, without a dialog for
// the ordinary single line.

// stripPasteControl removes terminal escape sequences from pasted text and
// leaves everything a program can use: printing characters in any script,
// tabs, line breaks. A sequence cut off at the end of the text is dropped with
// it rather than pasted half-formed.
func stripPasteControl(s string) string {
	if !strings.ContainsAny(s, "\x1b\x00\x01\x02\x03\x04\x05\x06\x07\x08\x0b\x0c\x0e\x0f") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = skipEscape(s, i)
		case c == '\t' || c == '\n' || c == '\r':
			b.WriteByte(c)
			i++
		case c < 0x20 || c == 0x7f:
			i++ // other C0 controls and DEL: no use to a program, plenty of noise
		default:
			b.WriteByte(c)
			i++
		}
	}
	return b.String()
}

// skipEscape returns the index just past the escape sequence starting at i.
// It covers CSI (ESC [ … final), OSC (ESC ] … BEL or ST), the two- and
// three-byte sequences, and the string forms (DCS, SOS, PM, APC), which end
// with ST; an unterminated sequence ends the text.
func skipEscape(s string, i int) int {
	i++ // ESC
	if i >= len(s) {
		return i
	}
	switch s[i] {
	case '[': // CSI: parameters and intermediates, then a final byte 0x40-0x7e
		i++
		for i < len(s) && s[i] >= 0x20 && s[i] <= 0x3f {
			i++
		}
		if i < len(s) && s[i] >= 0x40 && s[i] <= 0x7e {
			i++
		}
		return i
	case ']', 'P', 'X', '^', '_': // OSC and the string forms: up to BEL or ST
		i++
		for i < len(s) {
			if s[i] == 0x07 { // BEL terminates OSC
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' { // ST
				return i + 2
			}
			i++
		}
		return i
	default:
		// Two-byte escapes (ESC c, ESC 7, ESC =, …); charset designators carry
		// one more byte (ESC ( B).
		c := s[i]
		i++
		if (c == '(' || c == ')' || c == '*' || c == '+' || c == '-' || c == '.' || c == '/') && i < len(s) {
			i++
		}
		return i
	}
}

// pasteText decides what a paste sends. It reports whether this app took the
// paste (the terminal's own path is faster and does nothing extra when the
// text is already clean).
func (a *App) pasteText(t *Tab, d transfer.Data) bool {
	raw, err := d.Read(transfer.Text)
	if err != nil || len(raw) == 0 {
		return false // files or images: nothing this app pastes itself
	}
	text := string(raw)
	clean := stripPasteControl(text)
	multiline := strings.ContainsAny(clean, "\r\n")
	if clean == text && !multiline {
		return false // the ordinary single line: the terminal's own path
	}
	if clean == "" {
		return true // escapes and nothing else: paste nothing
	}
	if !multiline {
		term := t.Term
		if term == nil {
			return false // nowhere to send it: let the terminal's path decide
		}
		term.Paste(clean)
		return true
	}
	a.pasteAsk = &PasteAsk{
		Open:    true,
		Text:    clean,
		TrimEnd: true,
		Run: func(text string) {
			if t.Term != nil {
				t.Term.Paste(text)
			}
		},
	}
	if a.win != nil {
		a.win.Invalidate()
	}
	return true
}

// trimPasteEnd drops the whitespace at the end of a paste: the trailing spaces
// nobody typed on purpose and the blank lines a clipboard likes to carry. A
// trailing blank line is the one that bites — a shell runs it as an empty
// command, and a REPL takes it as one more submit. Only the end is touched:
// inner blank lines and leading text are what the user copied.
func trimPasteEnd(s string) string {
	return strings.TrimRight(s, " \t\r\n\u00a0\u3000")
}

// digitsOnly keeps a one-time code to what TOTP and HOTP put in it: its
// digits. Spaces and hyphens are what people type a code with when they read
// it off a phone; every other character would only make authentication fail.
func digitsOnly(s string) string {
	// Escape sequences carry digits of their own ("1;32" is a colour), so they
	// go first; a code pasted from a terminal would otherwise turn into one.
	s = stripPasteControl(s)
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		r, n := utf8.DecodeRuneInString(s[i:])
		if r >= '0' && r <= '9' {
			b.WriteByte(byte(r))
		}
		i += n
	}
	return b.String()
}
