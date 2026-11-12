// App icon: the artwork lives in resources/icon.png, which mygo bakes into the
// executable (window, taskbar and file icon). The same bytes are embedded here
// so a build made without the tool — a plain `go build` — still shows the icon
// on the window it creates.
package main

import _ "embed"

//go:embed resources/icon.png
var appIconPNG []byte
