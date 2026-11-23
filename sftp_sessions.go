package main

import (
	"fmt"

	"github.com/egoist/mygo/ui"
)

// The sidebar's SFTP list: one row per session that has files to browse, at the
// bottom left, above the Settings row. A double click opens that session's file
// panel beside its terminal, which is where a file browser belongs — the tab's
// context menu still has "File panel", but nobody finds a feature there.
//
// The list is not a second kind of connection: an SFTP channel rides the tab's
// own SSH connection (internal/sshx), so its rows are tabs, and the panel is a
// state of the tab (`Tab.SFTP`). That is why a closed tab cannot be browsed
// until it reconnects, and why the row says so instead of opening an empty
// panel.

// sftpSessionHeight is the room the list takes at the bottom of the sidebar:
// enough for three rows before it scrolls, small enough that the host list
// above it keeps its own space.
const sftpSessionHeight = 132

// sftpSessions draws the sidebar's SFTP list.
func (a *App) sftpSessions(c *ui.Context) {
	sessions := a.sftpTabs()
	ui.Column(c).Height(sftpSessionHeight).Children(func() {
		ui.Row(c).Padding(8, 10, 4).Gap(6).AlignItems(ui.Center).Children(func() {
			ui.Text(c, "SFTP sessions").Bold().FontSize(12).Grow(1)
			if n := len(sessions); n > 0 {
				ui.Text(c, fmt.Sprintf("%d", n)).FontSize(11).TextColor(c.Theme().TextMuted)
			}
		})
		ui.Scroll(c).Grow(1).Children(func() {
			if len(sessions) == 0 {
				ui.Text(c, "Connect a host, then double-click it here to browse its files.").
					FontSize(11).TextColor(c.Theme().TextMuted).Padding(4, 10)
				return
			}
			for _, t := range sessions {
				a.sftpSessionRow(c, t)
			}
		})
	})
}

// sftpTabs is what the list shows: every tab with a live session, and any tab
// whose panel is still open after its session went.
func (a *App) sftpTabs() []*Tab {
	out := make([]*Tab, 0, len(a.tabs))
	for _, t := range a.tabs {
		if t.Sess != nil || (t.SFTP != nil && t.SFTP.Open) {
			out = append(out, t)
		}
	}
	return out
}

func (a *App) sftpSessionRow(c *ui.Context, t *Tab) {
	st := t.SFTP
	open := st != nil && st.Open
	live := t.Sess != nil

	row := ui.Row(c.Key("sftp-session-"+t.ID)).Padding(4, 10).Gap(8).
		Radius(4).Focusable().Label("SFTP session " + tabTitle(t))
	switch {
	case a.current() == t && open:
		row.Background(c.Theme().SurfacePressed)
	case row.Hovered():
		row.Background(c.Theme().SurfaceHover)
	}
	row.Children(func() {
		dot := ui.Box(c).Width(8).Height(8).Radius(4)
		switch {
		case !live:
			dot.Background(c.Theme().Border)
		case open:
			dot.Background(c.Theme().Accent)
		default:
			dot.Background(c.Theme().Success)
		}
		ui.Column(c).Grow(1).Gap(0).Children(func() {
			ui.Text(c, tabTitle(t)).FontSize(12).MaxLines(1)
			ui.Text(c, a.sftpSessionSubtitle(t)).FontSize(10).
				TextColor(c.Theme().TextMuted).MaxLines(1)
		})
	})
	if row.Clicked() {
		a.post(func() { a.selectTab(indexOfTab(a.tabs, t)) })
	}
	if row.DoubleClicked() {
		a.post(func() { a.openSFTP(t) })
	}
	row.ContextMenu(func(m *ui.Menu) {
		if open {
			if m.Item("Close files").Chosen() {
				a.post(func() { a.toggleSFTP(t) })
			}
		} else if m.Item("Open files").Chosen() {
			a.post(func() { a.openSFTP(t) })
		}
		if m.Item("Go to tab").Chosen() {
			a.post(func() { a.selectTab(indexOfTab(a.tabs, t)) })
		}
		if !live {
			m.Separator()
			if m.Item("Reconnect…").Chosen() {
				a.post(func() { a.reconnect(t) })
			}
		}
	})
}

// sftpSessionSubtitle is the line under a session's name: the folder it shows
// when its panel is open, and what a double click would do otherwise.
func (a *App) sftpSessionSubtitle(t *Tab) string {
	st := t.SFTP
	if t.Sess == nil {
		return "Closed — reconnect to browse"
	}
	if st != nil && st.Open {
		if st.Err != "" {
			return st.Err
		}
		if st.Path != "" {
			return st.Path
		}
		return "Listing…"
	}
	if who := t.Cfg.User + "@" + t.Cfg.Host; t.Cfg.Host != "" {
		return "Double-click to browse " + who
	}
	return "Double-click to browse its files"
}

// openSFTP shows a session's files beside its terminal: the tab comes forward,
// and the panel opens on its home directory the first time. A session that has
// ended cannot browse anything, and says so rather than opening a panel that
// would only fail.
func (a *App) openSFTP(t *Tab) {
	if t == nil {
		return
	}
	a.selectTab(indexOfTab(a.tabs, t))
	if t.Sess == nil {
		a.status = "Reconnect " + tabTitle(t) + " before its files can open."
		return
	}
	st := t.sftp()
	if st.Open {
		return
	}
	st.Open = true
	if st.Path == "" && !st.Loading {
		a.sftpList(t, "")
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}
