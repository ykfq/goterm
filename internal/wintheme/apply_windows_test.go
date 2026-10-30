//go:build windows

package wintheme

import (
	"errors"
	"syscall"
	"testing"
	"unsafe"
)

var (
	procCreateWindowExW = user32.NewProc("CreateWindowExW")
	procDestroyWindow   = user32.NewProc("DestroyWindow")
)

const (
	wsOverlappedWindow = 0x00cf0000
	// wsVisible is deliberately left out: the frame is painted for a window that
	// exists, and a test that flashes a 200x200 window on the desktop is a test
	// people disable.
)

// newWindow makes a real top-level window with the built-in STATIC class, so the
// DWM attributes are exercised against an actual hwnd instead of a mock. The
// class does not matter to DWM; what matters is that this is a real window.
func newWindow(t *testing.T) uintptr {
	t.Helper()
	class, err := syscall.UTF16PtrFromString("STATIC")
	if err != nil {
		t.Fatal(err)
	}
	title, err := syscall.UTF16PtrFromString("goterm wintheme test")
	if err != nil {
		t.Fatal(err)
	}
	hwnd, _, callErr := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(class)),
		uintptr(unsafe.Pointer(title)),
		wsOverlappedWindow,
		0, 0, 200, 200,
		0, 0, 0, 0,
	)
	if hwnd == 0 {
		t.Fatalf("CreateWindowExW: %v", callErr)
	}
	t.Cleanup(func() { procDestroyWindow.Call(hwnd) })
	return hwnd
}

func TestApplyRepaintsTheFrame(t *testing.T) {
	hwnd := newWindow(t)

	// Dark first.
	applyErr := Apply(hwnd, true)
	if got, err := readAttribute(hwnd, attrImmersiveDarkMode); err != nil {
		t.Fatalf("reading immersive dark mode back: %v", err)
	} else if got != 1 {
		t.Fatalf("immersive dark mode = %d, want 1", got)
	}
	if applyErr != nil {
		// Windows 10 has no caption/text/border colours; the flag above is the
		// part it does have, and Apply is documented to report the rest.
		t.Logf("Apply(dark) reported: %v", applyErr)
	}

	caption, captionErr := readAttribute(hwnd, attrCaptionColour)
	if captionErr != nil {
		t.Skipf("this Windows cannot colour the caption (pre-11): %v", captionErr)
	}
	wantCaption := mustColref(t, Dark.Caption)
	if caption != wantCaption {
		t.Errorf("dark caption = %#08x, want %#08x", caption, wantCaption)
	}
	if got, err := readAttribute(hwnd, attrTextColour); err != nil || got != mustColref(t, Dark.Text) {
		t.Errorf("dark text = %#08x (%v), want %#08x", got, err, mustColref(t, Dark.Text))
	}
	if got, err := readAttribute(hwnd, attrBorderColour); err != nil || got != mustColref(t, Dark.Border) {
		t.Errorf("dark border = %#08x (%v), want %#08x", got, err, mustColref(t, Dark.Border))
	}

	// Then light, on the same window: the switch has to undo the dark frame and
	// not just pile another colour on top of it.
	if err := Apply(hwnd, false); err != nil {
		t.Logf("Apply(light) reported: %v", err)
	}
	if got, err := readAttribute(hwnd, attrImmersiveDarkMode); err != nil {
		t.Fatalf("reading immersive dark mode back: %v", err)
	} else if got != 0 {
		t.Fatalf("immersive dark mode after light = %d, want 0", got)
	}
	if got, err := readAttribute(hwnd, attrCaptionColour); err != nil || got != mustColref(t, Light.Caption) {
		t.Errorf("light caption = %#08x (%v), want %#08x", got, err, mustColref(t, Light.Caption))
	}
}

func mustColref(t *testing.T, value string) uint32 {
	t.Helper()
	got, err := colref(value)
	if err != nil {
		t.Fatalf("colref(%q): %v", value, err)
	}
	return got
}

func TestApplyWithoutAWindow(t *testing.T) {
	if err := Apply(0, true); !errors.Is(err, ErrNoWindow) {
		t.Fatalf("Apply(0) error = %v, want ErrNoWindow", err)
	}
}
