//go:build !windows

package wintheme

// Apply does nothing here on purpose.
//
// macOS draws the title bar from the window's own appearance (and follows the
// system, which is also what its users expect); the Linux ports paint their own
// decorations or defer to the desktop's theme. Neither takes a colour from the
// app, so there is nothing to apply — and nothing to report, either: the frame
// on those systems is not something this app got wrong.
func Apply(_ uintptr, _ bool) error {
	return nil
}
