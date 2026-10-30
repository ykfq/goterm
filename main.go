package main

import (
	"embed"
	"io/fs"
	"log"

	"github.com/wailsapp/wails/v3/pkg/application"

	"github.com/ykfq/goterm/internal/wintheme"
)

// frontend/dist is produced by `npm run build` (the Taskfile runs it for you).
// `all:` keeps dotfiles in the bundle.
//
//go:embed all:frontend/dist
var embedded embed.FS

func main() {
	app, err := NewApp()
	if err != nil {
		log.Fatalf("goterm: %v", err)
	}
	// trzsz's file transfer runs through this: native pickers plus scoped,
	// chunked file IO. See files.go for why the webview's own file pickers are
	// not something to rely on.
	files := NewFileBridge()

	// The embed tree has frontend/ as its root; the asset server needs the
	// dist directory itself, or every URL would be one level too deep.
	assets, err := fs.Sub(embedded, "frontend/dist")
	if err != nil {
		log.Fatalf("goterm: embedded frontend: %v", err)
	}

	// The window frame's palette, resolved once: a malformed colour in
	// internal/wintheme is a programming error, and it should not wait for a
	// theme switch to show up. Only the dark pair is handed to Wails — the light
	// frame is painted at runtime by App.SetWindowTheme, which is the same path
	// the dark one takes once a window exists.
	darkFrame, err := wintheme.Dark.Resolve()
	if err != nil {
		log.Fatalf("goterm: dark window frame: %v", err)
	}

	wailsApp := application.New(application.Options{
		Name:        "GoTerm",
		Description: "SSH terminal with trzsz file transfer",
		Services: []application.Service{
			application.NewService(app),
			application.NewService(files),
		},
		Assets: application.AssetOptions{
			// Also serves the bundled runtime at /wails/runtime.js, so a build
			// without the npm runtime still has one.
			Handler: application.BundledAssetFileServer(assets),
		},
		SingleInstance: &application.SingleInstanceOptions{
			UniqueID: "goterm.desktop.single.instance",
			OnSecondInstanceLaunch: func(application.SecondInstanceData) {
				// The window may be minimised; waking it is the frontend's job.
				log.Println("goterm: a second launch was redirected to this instance")
			},
		},
		Mac: application.MacOptions{
			ApplicationShouldTerminateAfterLastWindowClosed: true,
		},
	})

	win := wailsApp.Window.NewWithOptions(application.WebviewWindowOptions{
		Title:     "GoTerm — SSH terminal",
		Width:     1360,
		Height:    860,
		MinWidth:  880,
		MinHeight: 540,
		// Matches the terminal theme, so the first frame is not a white flash.
		BackgroundColour: application.NewRGB(13, 17, 23),
		URL:              "/",
		Windows: application.WindowsWindow{
			// The frame is part of the look, and GoTerm has its own theme switch:
			// pinning the window theme to dark starts from the palette below and,
			// just as importantly, stops Wails from repainting a system-coloured
			// frame over ours whenever Windows' own theme changes. The frontend
			// then calls SetWindowTheme with whatever theme the user saved (see
			// App.SetWindowTheme).
			Theme: application.Dark,
			// Windows 11 accepts these colours and paints the caption, its text
			// and the border with them; Windows 10 ignores the colours but still
			// gets the dark flag from the theme above. Both states are given the
			// same palette so focus does not change the frame's colour.
			CustomTheme: application.ThemeSettings{
				DarkModeActive:   frameTheme(darkFrame),
				DarkModeInactive: frameTheme(darkFrame),
			},
		},
	})
	app.attachWindow(win)

	if err := wailsApp.Run(); err != nil {
		log.Fatalf("goterm: %v", err)
	}
}

// frameTheme is the shape Wails wants for a window theme: three pointers, or
// nil for "leave that part alone".
func frameTheme(f wintheme.Frame) *application.WindowTheme {
	caption, text, border := f.Caption, f.Text, f.Border
	return &application.WindowTheme{
		TitleBarColour:  &caption,
		TitleTextColour: &text,
		BorderColour:    &border,
	}
}
