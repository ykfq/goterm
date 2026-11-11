package main

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/sftpx"
	"github.com/ykfq/goterm/internal/sshx"
)

// SFTPPanel is one tab's remote file browser. It rides the same SSH
// connection as the shell, so opening it never asks for a password again.
type SFTPPanel struct {
	Open     bool
	Path     string
	Entries  []sftpx.Entry
	Selected string
	Loading  bool
	Busy     bool
	Err      string
	Progress string

	NewFolder bool
	Renaming  bool
	Draft     string
}

func (t *Tab) sftp() *SFTPPanel {
	if t.SFTP == nil {
		t.SFTP = &SFTPPanel{}
	}
	return t.SFTP
}

// defaultFilesWidth is how wide the file panel opens before anyone drags it.
const defaultFilesWidth = 360

// clampFilesWidth keeps the panel within [240 DIP, half the window]: the
// terminal it shares the window with must keep at least half. A tab that has
// never been dragged takes the default, which is also clamped for a narrow
// window.
func (a *App) clampFilesWidth(t *Tab, c *ui.Context) {
	if t == nil || c == nil {
		return
	}
	w, _ := c.Size()
	if w <= 0 {
		return
	}
	half := w / 2
	minW := float32(240)
	if minW > half {
		minW = half
	}
	if t.FilesWidth <= 0 {
		t.FilesWidth = defaultFilesWidth
	}
	if t.FilesWidth > half {
		t.FilesWidth = half
	}
	if t.FilesWidth < minW {
		t.FilesWidth = minW
	}
}

func (a *App) toggleSFTP(t *Tab) {
	if t == nil || t.Sess == nil {
		a.status = "Connect first; the file panel uses the session's own SFTP channel."
		return
	}
	st := t.sftp()
	st.Open = !st.Open
	if st.Open && st.Path == "" && !st.Loading {
		a.sftpList(t, "")
	}
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) sftpList(t *Tab, dir string) {
	if t == nil || t.Sess == nil {
		return
	}
	st := t.sftp()
	st.Loading = true
	st.Err = ""
	id := t.Sess.ID()
	go func() {
		listing, err := a.mgr.SFTPList(id, dir)
		a.post(func() {
			st.Loading = false
			if err != nil {
				st.Err = friendlyError(err)
			} else {
				st.Path = listing.Path
				st.Entries = listing.Entries
				st.Selected = ""
			}
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}()
}

// sftpUploadPaths sends local files to the panel's current directory (the
// remote home when the panel has not listed anything yet).
func (a *App) sftpUploadPaths(t *Tab, files []string) {
	if t == nil || t.Sess == nil || len(files) == 0 {
		return
	}
	st := t.sftp()
	if !st.Open {
		st.Open = true
	}
	st.Busy = true
	st.Err = ""
	st.Progress = "uploading…"
	id, dir := t.Sess.ID(), st.Path
	go func() {
		names, err := a.mgr.SFTPUpload(id, files, dir)
		a.post(func() {
			st.Busy = false
			st.Progress = ""
			if err != nil {
				st.Err = friendlyError(err)
			} else {
				a.status = fmt.Sprintf("Uploaded %d file(s) to %s", len(names), dir)
			}
			a.sftpList(t, st.Path)
		})
	}()
}

func (a *App) sftpPickUpload(t *Tab) {
	if t == nil || t.Sess == nil {
		return
	}
	go func() {
		paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
			Parent:   a.win,
			Title:    "Upload to " + sftpDir(t),
			Multiple: true,
		})
		if err != nil || len(paths) == 0 {
			return
		}
		a.post(func() { a.sftpUploadPaths(t, paths) })
	}()
}

func (a *App) sftpPickDownload(t *Tab, e sftpx.Entry) {
	if t == nil || t.Sess == nil {
		return
	}
	go func() {
		// A download goes where the user points: the dialog starts at the
		// configured download folder, which is where tsz saves without asking.
		target, err := mygo.Dialog.Save(mygo.SaveDialogOptions{
			Parent:      a.win,
			Title:       "Download " + e.Name,
			DefaultPath: filepath.Join(a.downloadDir, e.Name),
		})
		if err != nil || target == "" {
			return
		}
		a.post(func() {
			st := t.sftp()
			st.Busy = true
			st.Err = ""
			st.Progress = "downloading " + e.Name + "…"
			id := t.Sess.ID()
			remote := e.Path
			go func() {
				err := a.mgr.SFTPDownload(id, remote, target)
				a.post(func() {
					st.Busy = false
					st.Progress = ""
					if err != nil {
						st.Err = friendlyError(err)
					} else {
						a.status = "Downloaded " + e.Name
					}
					if a.win != nil {
						a.win.Invalidate()
					}
				})
			}()
		})
	}()
}

func (a *App) sftpMkdir(t *Tab) {
	st := t.sftp()
	name := strings.TrimSpace(st.Draft)
	if name == "" || t.Sess == nil {
		return
	}
	if strings.ContainsAny(name, `/\`) {
		st.Err = "A folder name cannot contain a slash."
		return
	}
	dir := path.Join(sftpDir(t), name)
	id := t.Sess.ID()
	st.Busy = true
	go func() {
		err := a.mgr.SFTPMkdir(id, dir)
		a.post(func() {
			st.Busy = false
			st.NewFolder = false
			st.Draft = ""
			if err != nil {
				st.Err = friendlyError(err)
			}
			a.sftpList(t, st.Path)
		})
	}()
}

func (a *App) sftpRename(t *Tab, e sftpx.Entry) {
	st := t.sftp()
	name := strings.TrimSpace(st.Draft)
	if name == "" || t.Sess == nil || name == e.Name {
		st.Renaming = false
		return
	}
	if strings.ContainsAny(name, `/\`) {
		st.Err = "A name cannot contain a slash."
		return
	}
	id := t.Sess.ID()
	st.Busy = true
	go func() {
		err := a.mgr.SFTPRename(id, e.Path, path.Join(path.Dir(e.Path), name))
		a.post(func() {
			st.Busy = false
			st.Renaming = false
			st.Draft = ""
			if err != nil {
				st.Err = friendlyError(err)
			}
			a.sftpList(t, st.Path)
		})
	}()
}

func (a *App) sftpRemove(t *Tab, e sftpx.Entry) {
	if t == nil || t.Sess == nil {
		return
	}
	a.confirm = &Confirm{
		Open:   true,
		Title:  "Delete " + e.Name + "?",
		Body:   "This deletes it on the server. Directories must be empty.",
		OK:     "Delete",
		Danger: true,
		Run: func() {
			st := t.sftp()
			id := t.Sess.ID()
			st.Busy = true
			go func() {
				err := a.mgr.SFTPRemove(id, e.Path, e.Dir)
				a.post(func() {
					st.Busy = false
					if err != nil {
						st.Err = friendlyError(err)
					}
					a.sftpList(t, st.Path)
				})
			}()
		},
	}
}

func sftpDir(t *Tab) string {
	if t != nil && t.SFTP != nil && t.SFTP.Path != "" {
		return t.SFTP.Path
	}
	return ""
}

// sftpView draws the panel.
// sftpView draws the panel. Its width is the tab's own setting, dragged with
// the splitter beside it and capped at half the window so the terminal never
// loses the room it needs.
func (a *App) sftpView(c *ui.Context, t *Tab) {
	st := t.SFTP
	if st == nil || !st.Open {
		return
	}
	ui.Column(c).FillHeight().Background(c.Theme().Surface).Border(1, c.Theme().Border).Children(func() {
		ui.Row(c).Padding(8, 10).Gap(8).Children(func() {
			ui.Text(c, "Files").Bold().Grow(1)
			ui.Button(c, "✕").Label("Close files").BorderWidth(0).Background(ui.Transparent).
				OnClick(func() { a.post(func() { st.Open = false }) })
		})
		ui.Divider(c)
		ui.Row(c).Padding(6, 8).Gap(4).Children(func() {
			if ui.Button(c, "↑").Label("Parent folder").Tooltip("Parent folder").Disabled(st.Path == "" || st.Path == "/").
				Clicked() {
				a.sftpList(t, path.Dir(st.Path))
			}
			entry := ui.TextInput(c, &st.Path).Grow(1)
			if entry.Submitted() {
				a.sftpList(t, st.Path)
			}
			if ui.Button(c, "Go").Clicked() {
				a.sftpList(t, st.Path)
			}
			if ui.Button(c, "⟳").Label("Refresh").Tooltip("Refresh").Clicked() {
				a.sftpList(t, st.Path)
			}
		})
		ui.Row(c).Padding(0, 8, 6).Gap(4).Children(func() {
			ui.Button(c, "Upload…").Disabled(st.Busy).OnClick(func() { a.sftpPickUpload(t) })
			ui.Button(c, "Folder").Disabled(st.Busy).OnClick(func() {
				st.NewFolder = true
				st.Draft = ""
			})
			ui.Button(c, "Download").Disabled(st.Busy || st.Selected == "").OnClick(func() {
				if e, ok := selectedEntry(st); ok && !e.Dir {
					a.sftpPickDownload(t, e)
				}
			})
			ui.Button(c, "Rename").Disabled(st.Busy || st.Selected == "").OnClick(func() {
				if e, ok := selectedEntry(st); ok {
					st.Renaming = true
					st.Draft = e.Name
				}
			})
			ui.Button(c, "Delete").Disabled(st.Busy || st.Selected == "").OnClick(func() {
				if e, ok := selectedEntry(st); ok {
					a.post(func() { a.sftpRemove(t, e) })
				}
			})
		})
		if st.NewFolder || st.Renaming {
			ui.Row(c).Padding(6, 8).Gap(6).Children(func() {
				input := ui.TextInput(c, &st.Draft).AutoFocus().Grow(1)
				label := "Create"
				if st.Renaming {
					label = "Rename"
				}
				if ui.Button(c, label).Disabled(st.Busy).Clicked() {
					if st.Renaming {
						if e, ok := selectedEntry(st); ok {
							a.sftpRename(t, e)
						}
					} else {
						a.sftpMkdir(t)
					}
				}
				if ui.Button(c, "Cancel").Clicked() {
					st.NewFolder, st.Renaming = false, false
					st.Draft = ""
				}
				_ = input
			})
		}
		if st.Err != "" {
			ui.Text(c, st.Err).FontSize(11).TextColor(c.Theme().Danger).Padding(4, 10)
		}
		ui.Scroll(c).Grow(1).Children(func() {
			if st.Loading {
				ui.Row(c).Padding(12).Gap(8).Children(func() {
					ui.Spinner(c)
					ui.Text(c, "Listing…").FontSize(12).TextColor(c.Theme().TextMuted)
				})
				return
			}
			if len(st.Entries) == 0 {
				ui.Text(c, "Empty folder.").FontSize(12).TextColor(c.Theme().TextMuted).Padding(12)
				return
			}
			for _, e := range st.Entries {
				a.sftpRow(c, t, st, e)
			}
		})
		footer := st.Progress
		if footer == "" && st.Path != "" {
			footer = st.Path
		}
		ui.Text(c, footer).FontSize(10).TextColor(c.Theme().TextMuted).Padding(4, 10).MaxLines(1)
	})
}

func (a *App) sftpRow(c *ui.Context, t *Tab, st *SFTPPanel, e sftpx.Entry) {
	row := ui.Row(c.Key("sftp-"+e.Path)).Padding(4, 8).Gap(6).Radius(4).Focusable()
	if st.Selected == e.Path {
		row.Background(c.Theme().SurfacePressed)
	} else if row.Hovered() {
		row.Background(c.Theme().SurfaceHover)
	}
	row.Children(func() {
		icon := "▪"
		colour := c.Theme().TextMuted
		if e.Dir {
			icon = "▸"
			colour = c.Theme().Accent
		}
		ui.Text(c, icon).FontSize(11).TextColor(colour).Width(12)
		ui.Text(c, e.Name).FontSize(12).Grow(1).MaxLines(1)
		if !e.Dir {
			ui.Text(c, humanSize(e.Size)).FontSize(10).TextColor(c.Theme().TextMuted)
		}
	})
	if row.Clicked() {
		st.Selected = e.Path
	}
	if row.DoubleClicked() {
		if e.Dir {
			a.post(func() { a.sftpList(t, e.Path) })
		} else {
			a.sftpPickDownload(t, e)
		}
	}
	row.ContextMenu(func(m *ui.Menu) {
		if !e.Dir && m.Item("Download…").Chosen() {
			a.post(func() { a.sftpPickDownload(t, e) })
		}
		if m.Item("Rename…").Chosen() {
			a.post(func() {
				st.Selected = e.Path
				st.Renaming = true
				st.Draft = e.Name
			})
		}
		m.Separator()
		if m.Item("Delete…").Danger().Chosen() {
			a.post(func() { a.sftpRemove(t, e) })
		}
	})
}

func selectedEntry(st *SFTPPanel) (sftpx.Entry, bool) {
	for _, e := range st.Entries {
		if e.Path == st.Selected {
			return e, true
		}
	}
	return sftpx.Entry{}, false
}

func sftpProgressText(ev sshx.SFTPProgressEvent) string {
	pct := 0
	if ev.Total > 0 {
		pct = int(ev.Done * 100 / ev.Total)
	}
	done := humanSize(ev.Done)
	if ev.Total > 0 {
		done += " / " + humanSize(ev.Total)
	}
	return fmt.Sprintf("%s %s — %d%% (%s)", ev.Direction, ev.Name, pct, done)
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for m := n / unit; m >= unit && exp < 3; m /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
