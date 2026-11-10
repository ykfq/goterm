package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/egoist/mygo/plugins/terminal"
	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/hoststore"
	"github.com/ykfq/goterm/internal/sshx"
)

// view is the window's content: sidebar beside tabs, terminal and status bar,
// with every dialog layered over it.
func (a *App) view(c *ui.Context) {
	if a.status != "" {
		c.Toast(a.status)
		a.status = ""
	}
	// The commands that must work while a terminal has the keyboard. They are
	// registered on the window, so the terminal never sees these combinations.
	if c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyF) {
		a.openFind()
	}
	if c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyT) {
		a.openNewHost()
	}
	if c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyW) {
		a.post(func() { a.closeTab(a.active) })
	}
	if c.Shortcut(ui.Ctrl|ui.Shift, ui.KeyE) {
		a.toggleSFTP(a.current())
	}

	ui.Row(c).Fill().Children(func() {
		split := ui.Split(c, &a.prefs.SidebarWidth,
			func() { a.sidebar(c) },
			func() { a.main(c) },
		).Fill()
		if split.Changed() {
			a.prefs.Save(a.configPath)
		}
	})

	a.settingsDialog(c)
	a.hostEditorDialog(c)
	a.promptDialog(c)
	a.confirmDialog(c)
	a.vaultDialog(c)
	a.knownHostsDialog(c)
	a.aboutDialog(c)
}

func (a *App) sidebar(c *ui.Context) {
	ui.Column(c).Fill().Background(c.Theme().Surface).Children(func() {
		ui.Row(c).Padding(10).Gap(6).Children(func() {
			ui.SearchField(c, &a.query).Placeholder("Search hosts").Grow(1)
			ui.Button(c, "+").Label("New host").Tooltip("New host (Ctrl+Shift+T)").OnClick(a.openNewHost)
		})
		ui.Divider(c)
		ui.Scroll(c).Grow(1).Children(func() {
			hosts := a.hosts.List()
			all := a.hostGroups()
			if len(hosts) == 0 {
				ui.Column(c).Padding(20).Gap(10).Center().Children(func() {
					ui.Text(c, "No hosts yet").Bold()
					ui.Text(c, "Add a server to start a session.").FontSize(12).TextColor(c.Theme().TextMuted)
					if ui.Button(c, "Add host").Clicked() {
						a.openNewHost()
					}
				})
				return
			}
			for _, group := range orderedGroups(all) {
				a.groupView(c, group, all[group])
			}
		})
		ui.Divider(c)
		ui.Row(c).Padding(8).Gap(6).Children(func() {
			if ui.Button(c, "Settings").Clicked() {
				a.showSettings = true
			}
			if ui.Button(c, "Known hosts").Clicked() {
				a.showKnownHosts = true
			}
		})
	})
}

// orderedGroups lists group names, sorted, with Ungrouped first so the
// default never moves.
func orderedGroups(groups map[string][]hoststore.Host) []string {
	out := []string{}
	if g, ok := groups[""]; ok && len(g) > 0 {
		out = append(out, "")
	}
	names := make([]string, 0, len(groups))
	for name := range groups {
		if name != "" {
			names = append(names, name)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		return strings.ToLower(names[i]) < strings.ToLower(names[j])
	})
	return append(out, names...)
}

func (a *App) groupView(c *ui.Context, group string, list []hoststore.Host) {
	if len(list) == 0 {
		return
	}
	open, seen := a.openGroups[group]
	if !seen {
		open = true
	}
	title := group
	if title == "" {
		title = "Ungrouped"
	}
	header := ui.Row(c).Padding(10, 10, 4).Gap(6).Focusable()
	header.Children(func() {
		ui.Text(c, strings.ToUpper(title)).FontSize(10).Bold().TextColor(c.Theme().TextMuted)
		ui.Text(c, fmt.Sprint(len(list))).FontSize(10).TextColor(c.Theme().TextMuted)
	})
	if header.Clicked() {
		a.openGroups[group] = !open
	}
	if !open && a.query == "" {
		return
	}
	for _, h := range list {
		a.hostRow(c, h)
	}
}

func (a *App) hostRow(c *ui.Context, h hoststore.Host) {
	selected := h.ID == a.selectedHost
	row := ui.Row(c.Key("host-"+h.ID)).Padding(7, 10).Gap(8).Radius(6).Focusable()
	if selected {
		row.Background(c.Theme().SurfacePressed)
	} else if row.Hovered() {
		row.Background(c.Theme().SurfaceHover)
	}
	row.Children(func() {
		ui.Box(c).Size(7, 7).Radius(4).Background(c.Theme().Accent).Margin(0, 4, 0, 0)
		ui.Column(c).Grow(1).Gap(1).Children(func() {
			ui.Text(c, h.Name).MaxLines(1)
			ui.Text(c, hostSummary(h)).FontSize(10).TextColor(c.Theme().TextMuted).MaxLines(1)
		})
	})
	if row.Clicked() {
		a.selectedHost = h.ID
		a.prefs.LastHostID = h.ID
	}
	if row.DoubleClicked() {
		a.post(func() { a.connectHost(h.ID) })
	}
	row.Tooltip(fmt.Sprintf("%s@%s:%d", h.User, h.Host, h.Port))
	row.ContextMenu(func(m *ui.Menu) {
		if m.Item("Connect").Chosen() {
			a.post(func() { a.connectHost(h.ID) })
		}
		if m.Item("Edit…").Chosen() {
			a.post(func() { a.openEditHost(h.ID) })
		}
		m.Separator()
		if m.Item("Delete…").Danger().Chosen() {
			a.post(func() { a.deleteHost(h.ID) })
		}
	})
}

func (a *App) main(c *ui.Context) {
	col := ui.Column(c).Fill().Background(c.Theme().Background)
	col.Children(func() {
		a.tabBar(c)
		t := a.current()
		if t == nil {
			a.welcome(c)
			a.statusBar(c)
			return
		}
		if t.Status != StatusConnected || t.Err != "" {
			a.sessionBanner(c, t)
		}
		if t.Term != nil && t.SearchOpen {
			a.findBar(c, t)
		}
		body := ui.Row(c).Grow(1).AlignItems(ui.Stretch)
		body.Children(func() {
			pane := ui.Column(c).Grow(1)
			pane.Children(func() {
				if t.Term != nil {
					terminal.View(c, t.Term).Bind(&t.Focus).Label("Terminal " + tabTitle(t)).Fill()
					return
				}
				ui.Column(c).Fill().Center().Gap(10).Children(func() {
					ui.Spinner(c)
					ui.Text(c, t.Title).Bold()
					switch t.Status {
					case StatusFailed:
						ui.Text(c, t.Err).FontSize(12).TextColor(c.Theme().Danger)
					case StatusClosed:
						ui.Text(c, "Session closed.").FontSize(12).TextColor(c.Theme().TextMuted)
					default:
						ui.Text(c, "Connecting…").FontSize(12).TextColor(c.Theme().TextMuted)
					}
					if t.Status == StatusFailed || t.Status == StatusClosed {
						ui.Row(c).Gap(8).Children(func() {
							if ui.PrimaryButton(c, "Reconnect").Clicked() {
								a.post(func() { a.reconnect(t) })
							}
							if ui.Button(c, "Close tab").Clicked() {
								a.post(func() { a.closeTab(indexOfTab(a.tabs, t)) })
							}
						})
					}
				})
			})
			// Dropping a file anywhere on the terminal uploads it to the file
			// panel's directory (the remote home before it ever listed one).
			if files := pane.DroppedFiles(); len(files) > 0 && t.Sess != nil {
				a.post(func() { a.sftpUploadPaths(t, files) })
			}
			if pane.FileDragOver() {
				pane.Border(2, c.Theme().Accent)
			}
			a.sftpView(c, t)
		})
		a.statusBar(c)
	})
}

func (a *App) tabBar(c *ui.Context) {
	ui.Column(c).Children(func() {
		ui.Row(c).Height(38).Padding(6, 8).Gap(4).Children(func() {
			ui.ScrollHorizontal(c).Grow(1).Children(func() {
				ui.Row(c).Gap(3).Children(func() {
					for i, t := range a.tabs {
						a.tabChip(c, i, t)
					}
				})
			})
			ui.Button(c, "+").Label("New host").Tooltip("New host (Ctrl+Shift+T)").OnClick(a.openNewHost)
		})
		ui.Divider(c)
	})
}

func (a *App) tabChip(c *ui.Context, i int, t *Tab) {
	chip := ui.Row(c.Key(t.ID)).Height(28).Padding(0, 9).Gap(6).Radius(7).Focusable().Label("Tab " + tabTitle(t))
	active := i == a.active
	if active {
		chip.Background(c.Theme().Surface)
	} else if chip.Hovered() {
		chip.Background(c.Theme().SurfaceHover)
	}
	chip.Children(func() {
		status := c.Theme().Success
		switch t.Status {
		case StatusConnecting:
			status = c.Theme().Warning
		case StatusFailed:
			status = c.Theme().Danger
		case StatusClosed:
			status = c.Theme().TextMuted
		}
		ui.Box(c).Size(6, 6).Radius(3).Background(status)
		ui.Text(c, tabTitle(t)).MaxLines(1).FontSize(12)
		ui.Button(c, "✕").Label("Close "+tabTitle(t)).FontSize(9).BorderWidth(0).
			Background(ui.Transparent).Padding(2, 4).Tooltip("Close tab").
			OnClick(func() { a.post(func() { a.closeTab(indexOfTab(a.tabs, t)) }) })
	})
	if chip.Clicked() {
		a.selectTab(i)
	}
	chip.ContextMenu(func(m *ui.Menu) {
		if m.Item("Duplicate").Chosen() {
			a.post(func() { a.duplicate(t) })
		}
		if m.Item("Reconnect").Chosen() {
			a.post(func() { a.reconnect(t) })
		}
		if m.Item("Find in terminal…").Shortcut(ui.Ctrl|ui.Shift, ui.KeyF).Chosen() {
			a.post(func() {
				a.selectTab(indexOfTab(a.tabs, t))
				a.openFind()
			})
		}
		if m.Item("File panel").Checked(t.SFTP != nil && t.SFTP.Open).Chosen() {
			a.post(func() {
				a.selectTab(indexOfTab(a.tabs, t))
				a.toggleSFTP(t)
			})
		}
		m.Separator()
		if m.Item("Close tab").Shortcut(ui.Ctrl|ui.Shift, ui.KeyW).Chosen() {
			a.post(func() { a.closeTab(indexOfTab(a.tabs, t)) })
		}
	})
}

func tabTitle(t *Tab) string {
	if t.Title == "" {
		return "Session"
	}
	return t.Title
}

func (a *App) sessionBanner(c *ui.Context, t *Tab) {
	msg := t.Err
	color := c.Theme().Warning
	if msg == "" {
		msg = "Connecting…"
	}
	if t.Status == StatusFailed {
		color = c.Theme().Danger
	}
	ui.Row(c).Padding(7, 12).Gap(10).Background(c.Theme().Surface).Children(func() {
		ui.Box(c).Size(7, 7).Radius(4).Background(color)
		ui.Text(c, msg).FontSize(12).TextColor(color).Grow(1)
		if t.Status == StatusFailed || t.Status == StatusClosed {
			if ui.Button(c, "Reconnect").Clicked() {
				a.post(func() { a.reconnect(t) })
			}
		}
	})
}

func (a *App) findBar(c *ui.Context, t *Tab) {
	ui.Row(c).Padding(6, 10).Gap(8).Background(c.Theme().Surface).Children(func() {
		input := ui.TextInput(c.Key("find-"+t.ID), &t.SearchQuery).Placeholder("Find in terminal").Width(280).AutoFocus()
		if input.Changed() {
			t.Term.Search(t.SearchQuery)
		}
		if input.Submitted() {
			t.Term.SearchNext()
		}
		count := "0 / 0"
		if t.SearchTotal > 0 {
			count = fmt.Sprintf("%d / %d", t.SearchCurrent, t.SearchTotal)
			if t.Searching {
				count += "…"
			}
		}
		ui.Text(c, count).FontSize(11).TextColor(c.Theme().TextMuted).Width(90)
		if ui.Button(c, "Previous").Clicked() {
			t.Term.SearchPrevious()
		}
		if ui.Button(c, "Next").Clicked() {
			t.Term.SearchNext()
		}
		ui.Button(c, "Done").OnClick(func() {
			t.SearchOpen = false
			t.Term.EndSearch()
			a.focusTerminal()
		})
	})
}

func (a *App) welcome(c *ui.Context) {
	ui.Column(c).Fill().Center().Gap(14).Children(func() {
		ui.Text(c, "GoTerm").FontSize(28).Bold()
		ui.Text(c, "Native SSH terminal — no webview.").FontSize(13).TextColor(c.Theme().TextMuted)
		ui.Row(c).Gap(8).Children(func() {
			if ui.PrimaryButton(c, "Add a host").Clicked() {
				a.openNewHost()
			}
			if ui.Button(c, "Settings").Clicked() {
				a.showSettings = true
			}
		})
		if hosts := a.hosts.List(); len(hosts) > 0 {
			ui.Text(c, "Or double-click a host in the sidebar.").FontSize(11).TextColor(c.Theme().TextMuted)
		}
	})
}

func (a *App) statusBar(c *ui.Context) {
	t := a.current()
	left := fmt.Sprintf("GoTerm %s · %s · %s", Version, platformName(), a.secretBackend())
	var info sshx.SessionInfo
	if t != nil {
		if t.Sess != nil {
			info = t.Sess.Info()
		}
		if t.Via != "" {
			left += " · via " + t.Via
		}
		if info.LogPath != "" {
			left += " · log: " + shortPath(info.LogPath)
		}
	}
	ui.Row(c).Height(24).Padding(0, 10).Gap(12).Background(c.Theme().Surface).Children(func() {
		ui.Text(c, left).FontSize(10).TextColor(c.Theme().TextMuted).Grow(1).MaxLines(1)
		if t != nil && t.Sess != nil {
			ui.Text(c, fmt.Sprintf("%d bytes in · %d out", info.BytesIn, info.BytesOut)).
				FontSize(10).TextColor(c.Theme().TextMuted)
		}
	})
}

func shortPath(p string) string {
	if len(p) <= 48 {
		return p
	}
	return "…" + p[len(p)-46:]
}

func (a *App) openFind() {
	t := a.current()
	if t == nil || t.Term == nil {
		return
	}
	t.SearchOpen = true
	if a.win != nil {
		a.win.Invalidate()
	}
}

// duplicate opens a second shell on the same connection, without a second
// login.
func (a *App) duplicate(src *Tab) {
	if src == nil || src.Sess == nil {
		return
	}
	if src.HostID == "" {
		a.status = "This session was not started from a saved host; it cannot be duplicated."
		return
	}
	sess, err := a.mgr.Duplicate(src.Sess.ID())
	if err != nil {
		a.status = "Duplicate failed: " + err.Error()
		return
	}
	t := a.newTab(src.Title+" (2)", src.HostID)
	t.Sess = sess
	t.Cfg = src.Cfg
	t.Status = StatusConnected
	a.startTerminal(t)
}

// reconnect closes the current attempt and dials the host again with the same
// answers the user already gave.
func (a *App) reconnect(t *Tab) {
	if t == nil {
		return
	}
	if t.HostID == "" {
		a.status = "This session was not started from a saved host; use the host list to connect again."
		return
	}
	term, sess := t.Term, t.Sess
	t.Term, t.Sess = nil, nil
	t.LogPath = ""
	t.SearchOpen = false
	t.Status = StatusConnecting
	t.Err = ""
	go func() {
		if term != nil {
			_ = term.Close()
		}
		if sess != nil {
			_ = sess.Close()
		}
	}()
	go a.dial(t)
}
