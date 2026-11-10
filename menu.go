package main

import "github.com/egoist/mygo"

// installMenu gives the window the app's commands. The accelerators avoid the
// combinations a shell needs (Ctrl+W, Ctrl+K, Ctrl+T alone), as 1.x did: the
// shifted variants are the app's.
func (a *App) installMenu() {
	menu := mygo.NewMenu([]*mygo.MenuItem{
		{Label: "File", Submenu: []*mygo.MenuItem{
			{Label: "New Host…", Accelerator: "CmdOrCtrl+Shift+T", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(a.openNewHost)
			}},
			{Label: "Connect Selected Host", Accelerator: "CmdOrCtrl+Shift+N", Click: func(*mygo.MenuItem, *mygo.Window) {
				host := a.selectedHost
				if host != "" {
					a.post(func() { a.connectHost(host) })
				}
			}},
			{Label: "Close Tab", Accelerator: "CmdOrCtrl+Shift+W", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(func() { a.closeTab(a.active) })
			}},
			mygo.Separator(),
			{Label: "Settings…", Accelerator: "CmdOrCtrl+,", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(func() { a.showSettings = true })
			}},
			mygo.Separator(),
			{Role: mygo.RoleQuit},
		}},
		{Role: mygo.RoleEditMenu},
		{Label: "View", Submenu: []*mygo.MenuItem{
			{Label: "Find in Terminal…", Accelerator: "CmdOrCtrl+Shift+F", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(a.openFind)
			}},
			{Label: "Next Tab", Accelerator: "Ctrl+Tab", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(func() { a.selectTab((a.active + 1) % max(len(a.tabs), 1)) })
			}},
			{Label: "Previous Tab", Accelerator: "Ctrl+Shift+Tab", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(func() {
					if len(a.tabs) == 0 {
						return
					}
					a.selectTab((a.active - 1 + len(a.tabs)) % len(a.tabs))
				})
			}},
			mygo.Separator(),
			{Role: mygo.RoleToggleFullScreen},
		}},
		{Label: "Help", Submenu: []*mygo.MenuItem{
			{Label: "About GoTerm", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(func() { a.showAbout = true })
			}},
			{Label: "GoTerm on GitHub", Click: func(*mygo.MenuItem, *mygo.Window) {
				a.post(func() { a.openURL("https://github.com/ykfq/goterm") })
			}},
		}},
	})
	mygo.App.SetMenu(menu)
}
