// App icon: the artwork lives in resources/icon.png, and mygo bakes it into
// the executable as the file's own icon (Explorer, Start, and the running
// window's taskbar entry). The same bytes are embedded here so the window
// Open creates carries it too, icon and all, when the build was a plain
// `go build` rather than mygo's.
//
// The design is one compact mark — the G with its terminal badge and speed
// lines — so a single rendering serves every size: at 16 px the G still fills
// most of the frame, which is why the earlier wordmark's separate small
// rendering is gone.
package main

import _ "embed"

//go:embed resources/icon.png
var appIconPNG []byte
