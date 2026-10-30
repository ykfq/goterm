//go:build windows

package wintheme

import (
	"fmt"
	"syscall"
	"unsafe"
)

// DWM window attributes. 19/20 are "use immersive dark mode" (19 before the
// 20H1 build, 20 after); 34/35/36 are the Windows 11 colours.
const (
	attrBorderColour       = 34
	attrCaptionColour      = 35
	attrTextColour         = 36
	attrImmersiveDarkMode  = 20
	attrImmersiveDarkMode9 = 19
)

var (
	dwmapi                    = syscall.NewLazyDLL("dwmapi.dll")
	user32                    = syscall.NewLazyDLL("user32.dll")
	procDwmSetWindowAttribute = dwmapi.NewProc("DwmSetWindowAttribute")
	procDwmGetWindowAttribute = dwmapi.NewProc("DwmGetWindowAttribute")
	procSetWindowPos          = user32.NewProc("SetWindowPos")
)

const (
	swpNoSize       = 0x0001
	swpNoMove       = 0x0002
	swpNoZOrder     = 0x0004
	swpNoActivate   = 0x0010
	swpFrameChanged = 0x0020
)

// Apply repaints the frame of hwnd in the app theme.
//
// It returns the first failure, but the callers are allowed to ignore it: a
// frame that could not be coloured is a cosmetic miss, never a reason to fail a
// theme switch. Windows 10, where the three colours do not exist, always reports
// one — that is expected, and the dark-mode flag has already done the useful
// part by then.
func Apply(hwnd uintptr, dark bool) error {
	if hwnd == 0 {
		return ErrNoWindow
	}
	frame, err := For(dark).Resolve()
	if err != nil {
		return err
	}

	var firstErr error
	note := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}

	var on uint32
	if dark {
		on = 1
	}
	// 20 first: every build that understands 19 also understands 20 except the
	// 1809-era ones, which reject 20 outright — hence the fallback rather than a
	// version check that would need the build number.
	if err := setAttribute(hwnd, attrImmersiveDarkMode, on); err != nil {
		note(setAttribute(hwnd, attrImmersiveDarkMode9, on))
	}
	note(setAttribute(hwnd, attrCaptionColour, frame.Caption))
	note(setAttribute(hwnd, attrTextColour, frame.Text))
	note(setAttribute(hwnd, attrBorderColour, frame.Border))

	// The non-client area is cached; without this the new colours only appear
	// the next time something else forces DWM to redraw the frame (a resize, an
	// un-maximise) — which looks exactly like "the setting did nothing".
	procSetWindowPos.Call(hwnd, 0, 0, 0, 0, 0,
		swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged)

	return firstErr
}

func setAttribute(hwnd uintptr, attribute int32, value uint32) error {
	ret, _, _ := procDwmSetWindowAttribute.Call(
		hwnd,
		uintptr(attribute),
		uintptr(unsafe.Pointer(&value)),
		unsafe.Sizeof(value),
	)
	if ret != 0 {
		return fmt.Errorf("wintheme: dwm attribute %d on window %#x failed: hresult %#x", attribute, hwnd, uint32(ret))
	}
	return nil
}

// readAttribute is what the test uses to see the frame as Windows does, rather
// than trusting that a call that returned no error had an effect.
func readAttribute(hwnd uintptr, attribute int32) (uint32, error) {
	var value uint32
	ret, _, _ := procDwmGetWindowAttribute.Call(
		hwnd,
		uintptr(attribute),
		uintptr(unsafe.Pointer(&value)),
		unsafe.Sizeof(value),
	)
	if ret != 0 {
		return 0, fmt.Errorf("wintheme: dwm get attribute %d on window %#x failed: hresult %#x", attribute, hwnd, uint32(ret))
	}
	return value, nil
}
