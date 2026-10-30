package wintheme

import (
	"errors"
	"regexp"
	"testing"
)

// The byte order is the whole risk in this package: Windows wants 0x00BBGGRR,
// every web colour is #rrggbb, and a swap looks plausible on screen. These are
// the real values the app uses.
func TestColrefPacksBGR(t *testing.T) {
	cases := []struct {
		in   string
		want uint32
	}{
		{"#0b0f14", 0x00140f0b}, // dark caption
		{"#f6f8fa", 0x00faf8f6}, // light caption
		{"#d7e0ea", 0x00eae0d7}, // dark text
		{"#222c38", 0x00382c22}, // dark border
		{"#ffffff", 0x00ffffff},
		{"#000000", 0x00000000},
		{"#ff0000", 0x000000ff}, // pure red stays in the low byte
	}
	for _, tc := range cases {
		got, err := colref(tc.in)
		if err != nil {
			t.Fatalf("colref(%q): %v", tc.in, err)
		}
		if got != tc.want {
			t.Errorf("colref(%q) = %#08x, want %#08x", tc.in, got, tc.want)
		}
	}
}

func TestColrefRefusesAnythingElse(t *testing.T) {
	for _, in := range []string{"", "#", "#fff", "0b0f14", "#0b0f1", "#gggggg", "#0b0f14ff", "rgb(1,2,3)"} {
		if _, err := colref(in); !errors.Is(err, ErrBadColour) {
			t.Errorf("colref(%q) error = %v, want ErrBadColour", in, err)
		}
	}
}

func TestForPicksTheThemeFrame(t *testing.T) {
	if For(true) != Dark {
		t.Errorf("For(true) = %+v, want Dark", For(true))
	}
	if For(false) != Light {
		t.Errorf("For(false) = %+v, want Light", For(false))
	}
}

// Both palettes have to be usable colours, or every Apply fails at runtime on a
// machine nobody re-tested on.
func TestPalettesResolve(t *testing.T) {
	hex := regexp.MustCompile(`^#[0-9a-f]{6}$`)
	for name, p := range map[string]Palette{"dark": Dark, "light": Light} {
		for _, c := range []struct{ field, value string }{
			{"caption", p.Caption},
			{"text", p.Text},
			{"border", p.Border},
		} {
			if !hex.MatchString(c.value) {
				t.Errorf("%s %s = %q, not #rrggbb", name, c.field, c.value)
			}
		}
		frame, err := p.Resolve()
		if err != nil {
			t.Fatalf("%s palette does not resolve: %v", name, err)
		}
		if want, _ := colref(p.Caption); frame.Caption != want {
			t.Errorf("%s frame caption = %#08x, want %#08x", name, frame.Caption, want)
		}
		if want, _ := colref(p.Border); frame.Border != want {
			t.Errorf("%s frame border = %#08x, want %#08x", name, frame.Border, want)
		}
	}
}

// The zero-handle case is tested in apply_windows_test.go: off Windows, Apply
// has nothing to do with a handle at all.
