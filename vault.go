package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/hostport"
)

// Vault is the passphrase dialog for exporting or importing the host list.
// It is a modal of its own so the two fields (passphrase, confirmation) are
// not squeezed into the connection prompt.
type Vault struct {
	Open    bool
	Mode    string // "export" | "import"
	Path    string // chosen file, kept while a wrong passphrase is retried
	Pass    string
	Confirm string
	Err     string
}

func (a *App) openExport() {
	a.vault = &Vault{Open: true, Mode: "export"}
	if a.win != nil {
		a.win.Invalidate()
	}
}

func (a *App) openImport() {
	a.vault = &Vault{Open: true, Mode: "import"}
	if a.win != nil {
		a.win.Invalidate()
	}
}

// submitVault runs the file work off the main thread: native dialogs block,
// and sealing 64 MiB of scrypt should not freeze the window.
func (a *App) submitVault() {
	v := a.vault
	if v == nil {
		return
	}
	pass := strings.TrimSpace(v.Pass)
	if pass == "" {
		v.Err = "Enter a passphrase."
		return
	}
	if v.Mode == "export" && pass != v.Confirm {
		v.Err = "The two passphrases do not match."
		return
	}
	mode, path := v.Mode, v.Path
	v.Open = false
	a.vault = nil

	go func() {
		var err error
		var message string
		switch mode {
		case "export":
			if path == "" {
				path, err = mygo.Dialog.Save(mygo.SaveDialogOptions{
					Parent:      a.win,
					Title:       "Export hosts",
					DefaultPath: "goterm-hosts-" + time.Now().Format("2006-01-02") + ".json",
					Filters:     []mygo.FileFilter{{Name: "GoTerm host export", Extensions: []string{"json"}}},
				})
				if err != nil || path == "" {
					return
				}
			}
			data, count, e := a.exportHosts(pass)
			if e != nil {
				err = e
				break
			}
			if e := os.WriteFile(path, data, 0o600); e != nil {
				err = e
				break
			}
			message = fmt.Sprintf("Exported %d host(s) to %s", count, filepath.Base(path))
		case "import":
			if path == "" {
				paths, e := mygo.Dialog.Open(mygo.OpenDialogOptions{
					Parent:  a.win,
					Title:   "Import hosts",
					Filters: []mygo.FileFilter{{Name: "GoTerm host export", Extensions: []string{"json"}}},
				})
				if e != nil || len(paths) == 0 {
					return
				}
				path = paths[0]
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				err = e
				break
			}
			bundle, e := hostport.Unseal(raw, pass)
			if e != nil {
				if errors.Is(e, hostport.ErrWrongPassphrase) {
					a.post(func() {
						a.vault = &Vault{Open: true, Mode: "import", Path: path, Err: "Wrong passphrase. Try again."}
					})
					return
				}
				err = e
				break
			}
			sum := a.hosts.Import(bundle.Hosts, bundle.Secrets, bundle.ProxySecrets)
			a.post(func() { a.reloadHosts() })
			message = fmt.Sprintf("Imported %d, updated %d, skipped %d (%d secret(s)).",
				sum.Added, sum.Updated, sum.Skipped, sum.Secrets)
		}
		a.post(func() {
			if err != nil {
				a.status = "Hosts: " + friendlyError(err)
				if a.vault == nil && mode == "import" {
					a.vault = &Vault{Open: true, Mode: mode, Path: path, Err: err.Error()}
				}
				return
			}
			a.status = message
			if a.win != nil {
				a.win.Invalidate()
			}
		})
	}()
}

// exportHosts collects the hosts and their key-ring secrets into one sealed
// bundle. A host without a stored secret is exported without one; the import
// asks again rather than inventing a password.
func (a *App) exportHosts(pass string) ([]byte, int, error) {
	hosts := a.hosts.List()
	bundle := hostport.Bundle{
		Format:       hostport.Format,
		Version:      hostport.Version,
		ExportedAt:   time.Now().UTC(),
		Hosts:        hosts,
		Secrets:      map[string]string{},
		ProxySecrets: map[string]string{},
	}
	for _, h := range hosts {
		if v, err := a.hosts.Secret(h.ID); err == nil && v != "" {
			bundle.Secrets[h.ID] = v
		}
		if v, err := a.hosts.ProxySecret(h.ID); err == nil && v != "" {
			bundle.ProxySecrets[h.ID] = v
		}
	}
	raw, err := hostport.Seal(bundle, pass)
	if err != nil {
		return nil, 0, err
	}
	return raw, len(hosts), nil
}

// vaultDialog is the modal itself.
func (a *App) vaultDialog(c *ui.Context) {
	if a.vault == nil {
		return
	}
	v := a.vault
	ui.Modal(c, &v.Open, func() {
		title := "Export hosts"
		body := "The file is sealed with AES-256-GCM. It holds every saved password; treat it like a password."
		action := "Export"
		if v.Mode == "import" {
			title = "Import hosts"
			body = "Enter the passphrase the export was made with. Nothing local is deleted; existing hosts are matched by id, then by address and user."
			action = "Import"
		}
		ui.Column(c).Width(460).Gap(10).Children(func() {
			ui.Text(c, title).FontSize(17).Bold()
			ui.Text(c, body).FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Field(c, "Passphrase", func() {
				ui.TextInput(c, &v.Pass).Password().AutoFocus()
			})
			if v.Mode == "export" {
				ui.Field(c, "Confirm", func() {
					ui.TextInput(c, &v.Confirm).Password()
				})
			}
			if v.Path != "" {
				ui.Text(c, filepath.Base(v.Path)).FontSize(11).TextColor(c.Theme().TextMuted)
			}
			if v.Err != "" {
				ui.Text(c, v.Err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.vault = nil
					v.Open = false
				}
				if ui.PrimaryButton(c, action).Clicked() {
					a.submitVault()
				}
			})
		})
	})
	if a.vault == v && !v.Open {
		a.vault = nil
	}
}
