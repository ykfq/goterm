package main

import (
	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"
)

// The app's commands, written once.
//
// One table backs the native menu bar (Alt or F10) and the menu the top-left
// mark opens, so a command added to one cannot go missing from the other. The
// Edit menu is the platform's role in the bar, with cut, copy and paste as the
// focused control understands them; the terminal's own keys cover it in the
// mark's menu, where no text widget is in focus.

type appCommand struct {
	label string
	accel string
	sep   bool
	run   func()
}

type appMenu struct {
	label string
	cmds  []appCommand
}

// menus is the whole command set, in the order the bar shows it.
func (a *App) menus() []appMenu {
	return []appMenu{
		{label: "File", cmds: []appCommand{
			{label: "New Host…", accel: "CmdOrCtrl+Shift+T", run: a.openNewHost},
			{label: "Connect Selected Host", accel: "CmdOrCtrl+Shift+N", run: func() {
				if host := a.selectedHost; host != "" {
					a.connectHost(host)
				}
			}},
			{label: "Close Tab", accel: "CmdOrCtrl+Shift+W", run: func() { a.closeTab(a.active) }},
			{sep: true},
			{label: "Settings…", accel: "CmdOrCtrl+,", run: func() { a.openSettings() }},
			{sep: true},
			{label: "Export Hosts…", run: a.openExport},
			{label: "Import Hosts…", run: a.openImport},
		}},
		{label: "View", cmds: []appCommand{
			{label: "Find in Terminal…", accel: "CmdOrCtrl+Shift+F", run: a.openFind},
			{label: "File Panel", accel: "CmdOrCtrl+Shift+E", run: func() { a.toggleSFTP(a.current()) }},
			{label: "Reset Terminal", accel: "CmdOrCtrl+Shift+K", run: func() { a.resetTerminal(a.current()) }},
			{label: "Next Tab", accel: "Ctrl+Tab", run: func() {
				a.selectTab((a.active + 1) % max(len(a.tabs), 1))
			}},
			{label: "Previous Tab", accel: "Ctrl+Shift+Tab", run: func() {
				if len(a.tabs) == 0 {
					return
				}
				a.selectTab((a.active - 1 + len(a.tabs)) % len(a.tabs))
			}},
		}},
		{label: "Help", cmds: []appCommand{
			{label: "About GoTerm", run: func() { a.showAbout = true }},
			{label: "GoTerm on GitHub", run: func() { a.openURL("https://github.com/ykfq/goterm") }},
		}},
	}
}

// installMenu gives the window the app's commands. The accelerators avoid the
// combinations a shell needs (Ctrl+W, Ctrl+K, Ctrl+T alone), as 1.x did: the
// shifted variants are the app's.
func (a *App) installMenu() {
	var items []*mygo.MenuItem
	for _, g := range a.menus() {
		var sub []*mygo.MenuItem
		for _, c := range g.cmds {
			if c.sep {
				sub = append(sub, mygo.Separator())
				continue
			}
			run := c.run
			sub = append(sub, &mygo.MenuItem{Label: c.label, Accelerator: c.accel,
				Click: func(*mygo.MenuItem, *mygo.Window) { a.post(run) }})
		}
		switch g.label {
		case "File":
			sub = append(sub, mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleQuit})
		case "View":
			sub = append(sub, mygo.Separator(), &mygo.MenuItem{Role: mygo.RoleToggleFullScreen})
		}
		items = append(items, &mygo.MenuItem{Label: g.label, Submenu: sub})
		// The bar's own order is File, Edit, View, Help.
		if g.label == "File" {
			items = append(items, &mygo.MenuItem{Role: mygo.RoleEditMenu})
		}
	}
	mygo.App.SetMenu(mygo.NewMenu(items))
}

// markMenu opens the same commands from the app's mark. The bar hides
// (AutoHideMenuBar), so this is the pointer's way in.
func (a *App) markMenu(m *ui.Menu) {
	for _, g := range a.menus() {
		m.Submenu(g.label, func(sub *ui.Menu) {
			for _, c := range g.cmds {
				if c.sep {
					sub.Separator()
					continue
				}
				run := c.run
				if sub.Item(c.label).Chosen() {
					a.post(run)
				}
			}
			// The two roles the bar carries, in the same places.
			switch g.label {
			case "File":
				sub.Separator()
				if sub.Item("Quit").Chosen() {
					a.post(mygo.App.Quit)
				}
			case "View":
				sub.Separator()
				if sub.Item("Toggle Full Screen").Chosen() {
					a.post(func() {
						if a.win != nil {
							a.win.ToggleFullScreen()
						}
					})
				}
			}
		})
	}
}
