package main

import (
	"os"
	"path/filepath"

	"github.com/egoist/mygo"

	"github.com/ykfq/goterm/internal/zmodem"
)

// dialogs is the Chooser the transfer code sees: native dialogs, called from
// the goroutine that runs a transfer. The dialogs are modal to the window, so
// blocking that goroutine is what the user expects; the terminal keeps
// painting because it reads through a different goroutine.
type dialogs struct{ app *App }

func (d dialogs) ChooseDownloadFolder(suggested string) (string, error) {
	if d.app == nil {
		return suggested, nil
	}
	if suggested == "" {
		suggested = d.app.downloadDir
	}
	dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      d.app.win,
		Title:       "Choose the folder to save into",
		Directory:   true,
		DefaultPath: suggested,
	})
	if err != nil || len(dirs) == 0 {
		return "", zmodem.ErrCanceled
	}
	return dirs[0], nil
}

func (d dialogs) ChooseUpload(dirOnly bool) ([]string, error) {
	if d.app == nil {
		return nil, zmodem.ErrCanceled
	}
	title := "Choose files to send"
	if dirOnly {
		title = "Choose a folder to send"
	}
	paths, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:      d.app.win,
		Title:       title,
		Multiple:    !dirOnly,
		Directory:   dirOnly,
		DefaultPath: d.app.lastUploadDir(),
	})
	if err != nil || len(paths) == 0 {
		return nil, zmodem.ErrCanceled
	}
	d.app.rememberUploadDir(paths[0])
	return paths, nil
}

// lastUploadDir is where the upload dialog starts: the config file's folder
// on a first run, and the folder of the last upload after that.
func (a *App) lastUploadDir() string {
	if a.prefs.LastUploadDir != "" {
		if st, err := os.Stat(a.prefs.LastUploadDir); err == nil && st.IsDir() {
			return a.prefs.LastUploadDir
		}
	}
	if home, err := os.UserHomeDir(); err == nil {
		return home
	}
	return ""
}

// rememberUploadDir keeps the chosen file's folder for the next dialog and
// saves the setting, so it survives a restart.
func (a *App) rememberUploadDir(p string) {
	dir := p
	if st, err := os.Stat(p); err == nil && !st.IsDir() {
		dir = filepath.Dir(p)
	}
	if dir == a.prefs.LastUploadDir {
		return
	}
	a.prefs.LastUploadDir = dir
	a.post(func() {
		if err := a.prefs.Save(a.configPath); err != nil && a.win != nil {
			a.status = "Could not save settings: " + err.Error()
		}
	})
}
