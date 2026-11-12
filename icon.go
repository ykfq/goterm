// App icon: the artwork lives in resources/, and mygo bakes resources/icon.png
// into the executable as the file's own icon (window, taskbar and Explorer).
//
// The window icon is a different rendering of the same mark on purpose. The
// wordmark is 2.2:1, which leaves a 16-pixel title bar showing a thin band of
// letter between two half-rows of white; the G with its terminal badge is
// square, so it fills that 16 pixels instead. Explorer keeps the full mark,
// the running window shows the monogram — each form is used where it reads.
package main

import _ "embed"

//go:embed resources/icon.png
var appIconPNG []byte

//go:embed resources/icon-small.png
var appSmallIconPNG []byte
