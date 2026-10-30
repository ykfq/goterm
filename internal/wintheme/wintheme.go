// Package wintheme paints the native window frame — the title bar, its text and
// the hairline border around the window — in GoTerm's own theme.
//
// The frame is drawn by the OS, and Windows only knows about its own
// "apps use light theme" registry value. A dark GoTerm on a light-mode Windows
// therefore sat under a white title bar with a white line around it: the app
// said dark, the frame said light, and the seam was the first thing visible.
//
// What the OS accepts:
//
//   - Windows 11 (22000+): the caption, caption text and border colours, via
//     DwmSetWindowAttribute attributes 35 / 36 / 34.
//   - Windows 10 18985+ : the older "use immersive dark mode" flag (20; 19 on
//     1809-18985). No custom colours, but the frame goes properly dark.
//   - Anything older: nothing, and Apply reports the failures rather than
//     pretending the frame changed.
//
// The palettes below are the app's own tokens (frontend/src/style.css): the
// frame has to meet the tab strip without a seam, so they are copied from
// --bg / --text / --border rather than invented.
package wintheme

import (
	"errors"
	"fmt"
	"strconv"
)

// Palette is the part of the frame an app may colour. All three are "#rrggbb".
type Palette struct {
	// Caption is the title bar background.
	Caption string
	// Text is the title bar text.
	Text string
	// Border is the hairline around the window.
	Border string
}

// Frame is a Palette with the colours resolved into the 0x00BBGGRR form Windows
// calls a COLREF — the shape both DwmSetWindowAttribute and Wails' window theme
// want.
type Frame struct {
	Caption uint32
	Text    uint32
	Border  uint32
}

// The two frames, one per app theme.
//
// Light keeps the caption on --bg rather than pure white because the tab strip
// below it is --bg; dark likewise.
var (
	Dark  = Palette{Caption: "#0b0f14", Text: "#d7e0ea", Border: "#222c38"}
	Light = Palette{Caption: "#f6f8fa", Text: "#1f2933", Border: "#d6dee6"}
)

// For returns the frame palette for an app theme.
func For(dark bool) Palette {
	if dark {
		return Dark
	}
	return Light
}

// ErrNoWindow is returned when there is no window to paint — no window was ever
// handed over (headless tests, server mode) or it has been destroyed.
var ErrNoWindow = errors.New("wintheme: no window")

// ErrBadColour is returned for a colour that is not "#rrggbb". It can only come
// from a code change: the palettes above are constants.
var ErrBadColour = errors.New("wintheme: colour is not #rrggbb")

// colref converts "#rrggbb" into the 0x00BBGGRR form Windows calls a COLREF.
//
// Getting this backwards is invisible in a screenshot taken from the app itself
// and obvious on screen (a dark blue caption where grey was asked for), so the
// byte order is pinned by a test.
func colref(value string) (uint32, error) {
	if len(value) != 7 || value[0] != '#' {
		return 0, fmt.Errorf("%w: %q", ErrBadColour, value)
	}
	rgb, err := strconv.ParseUint(value[1:], 16, 32)
	if err != nil {
		return 0, fmt.Errorf("%w: %q", ErrBadColour, value)
	}
	r := uint32(rgb) >> 16
	g := (uint32(rgb) >> 8) & 0xff
	b := uint32(rgb) & 0xff
	return r | g<<8 | b<<16, nil
}

// Resolve turns the palette into the numbers Windows wants, and is the only
// place the two can disagree: a palette that is not three #rrggbb strings cannot
// be applied, so the error is reported here rather than swallowed.
func (p Palette) Resolve() (Frame, error) {
	var f Frame
	var err error
	if f.Caption, err = colref(p.Caption); err != nil {
		return Frame{}, err
	}
	if f.Text, err = colref(p.Text); err != nil {
		return Frame{}, err
	}
	if f.Border, err = colref(p.Border); err != nil {
		return Frame{}, err
	}
	return f, nil
}
