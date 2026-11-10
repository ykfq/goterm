// GoTerm 2.0 — an SSH terminal drawn by mygo in native UI: no webview, no
// JavaScript, no frontend build.
package main

import (
	"flag"
	"log"

	"github.com/egoist/mygo"
)

func main() {
	connect := flag.String("connect", "", "host ID to connect to once the window opens")
	flag.Parse()

	app, err := NewApp()
	if err != nil {
		log.Fatalf("goterm: %v", err)
	}
	app.startHost = *connect
	mygo.App.WhenReady(app.Open)
	mygo.App.OnBeforeQuit(func(*mygo.QuitEvent) { app.Close() })
	if err := mygo.App.Run(); err != nil {
		log.Fatalf("goterm: %v", err)
	}
}
