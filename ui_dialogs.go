package main

import (
	"fmt"
	"strings"

	"github.com/egoist/mygo"
	"github.com/egoist/mygo/ui"

	"github.com/ykfq/goterm/internal/appdir"
	"github.com/ykfq/goterm/internal/encode"
	"github.com/ykfq/goterm/internal/settings"
)

func (a *App) settingsDialog(c *ui.Context) {
	ui.Modal(c, &a.showSettings, func() {
		ui.Column(c).Width(560).MaxHeight(640).Gap(12).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Settings").FontSize(18).Bold().Grow(1)
				ui.Button(c, "Done").OnClick(func() { a.showSettings = false })
			})
			page := a.settingsPage
			ui.Tabs(c, &page, "Appearance", "Behaviour", "Storage")
			a.settingsPage = page
			switch page {
			case 0:
				a.settingsAppearance(c)
			case 1:
				a.settingsBehaviour(c)
			default:
				a.settingsStorage(c)
			}
		})
	})
}

func (a *App) settingsAppearance(c *ui.Context) {
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			// Theme
			ui.Field(c, "Theme", func() {
				labels := []string{"System", "Dark", "Light"}
				choice := "System"
				switch a.prefs.Theme {
				case "dark":
					choice = "Dark"
				case "light":
					choice = "Light"
				}
				if ui.Select(c, &choice, labels).Changed() {
					a.prefs.Theme = strings.ToLower(choice)
					a.applySettings()
				}
			})

			// Font family
			ui.Field(c, "Font", func() {
				options := append([]string{"System monospace"}, a.fonts...)
				choice := a.prefs.FontFamily
				if choice == "" {
					choice = "System monospace"
				}
				if ui.Select(c, &choice, options).Changed() {
					if choice == "System monospace" {
						choice = ""
					}
					a.prefs.FontFamily = choice
					a.applySettings()
				}
			})

			// Font size
			ui.Field(c, "Font size", func() {
				size := float64(a.prefs.FontSize)
				ui.NumberInput(c, &size, settings.MinFontSize, settings.MaxFontSize, 1)
				if int(size) != a.prefs.FontSize {
					a.prefs.FontSize = int(size)
					a.applySettings()
				}
			})

			// Scrollback
			ui.Field(c, fmt.Sprintf("Scrollback (MB, %d–%d)", settings.MinScrollbackMB, settings.MaxScrollbackMB), func() {
				mb := float64(a.prefs.ScrollbackMB)
				ui.NumberInput(c, &mb, settings.MinScrollbackMB, settings.MaxScrollbackMB, 1)
				if int(mb) != a.prefs.ScrollbackMB {
					a.prefs.ScrollbackMB = int(mb)
					a.applySettings()
					// The terminal reads its scrollback limit when it is created;
					// existing tabs keep theirs until they reconnect, which the
					// note below says.
				}
			})
			ui.Text(c, "Scrollback applies to new terminals; reconnect a tab to shrink an existing one.").
				FontSize(11).TextColor(c.Theme().TextMuted)

			ui.Field(c, "Selecting text copies it", func() {
				if ui.Switch(c, &a.prefs.CopyOnSelect).Changed() {
					a.applySettings()
				}
			})
		})
	})
}

func (a *App) settingsBehaviour(c *ui.Context) {
	ui.Scroll(c).Grow(1).Children(func() {
		ui.Column(c).Gap(12).Children(func() {
			ui.Field(c, "Record every session to a log file", func() {
				if ui.Switch(c, &a.prefs.SessionLogging).Changed() {
					a.applySettings()
				}
			})
			ui.Text(c, "Logs go to "+a.logDir+". The file holds the server's bytes, before decoding.").
				FontSize(11).TextColor(c.Theme().TextMuted)

			ui.Field(c, "Transfers", func() {
				ui.Text(c, "tsz downloads save to "+a.downloadDir+". trz and rz ask for the files to upload.").
					FontSize(11).TextColor(c.Theme().TextMuted)
				if ui.Button(c, "Open download folder").Clicked() {
					_ = mygo.Shell.OpenPath(a.downloadDir)
				}
			})

			ui.Field(c, "Remote text encoding", func() {
				names := encode.Names()
				labels := make([]string, len(names))
				choice := encode.UTF8.Label()
				for i, n := range names {
					labels[i] = n.Label()
					if string(n) == a.prefs.Encoding {
						choice = n.Label()
					}
				}
				if ui.Select(c, &choice, labels).Changed() {
					for _, n := range names {
						if n.Label() == choice {
							a.prefs.Encoding = string(n)
						}
					}
					a.applySettings()
				}
			})
			ui.Text(c, "Applies to open terminals immediately. Input is always sent as UTF-8.").
				FontSize(11).TextColor(c.Theme().TextMuted)

			ui.Field(c, "Keepalive stops after idle hours", func() {
				hours := float64(a.prefs.IdleTimeoutHours)
				ui.NumberInput(c, &hours, settings.MinIdleHours, settings.MaxIdleHours, 1)
				if int(hours) != a.prefs.IdleTimeoutHours {
					a.prefs.IdleTimeoutHours = int(hours)
					a.applySettings()
				}
			})
			ui.Text(c, "Only real input resets the timer; a keepalive never does.").
				FontSize(11).TextColor(c.Theme().TextMuted)
		})
	})
}

func (a *App) settingsStorage(c *ui.Context) {
	ui.Column(c).Gap(12).Children(func() {
		ui.Field(c, "Data directory", func() {
			ui.Text(c, a.dir).Selectable().FontSize(12)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Open").Clicked() {
					_ = mygo.Shell.OpenPath(a.dir)
				}
				if ui.Button(c, "Change…").Clicked() {
					go a.pickStorageDir()
				}
			})
		})
		ui.Text(c, "Changing the directory takes effect at the next start; files are not moved for you.").
			FontSize(11).TextColor(c.Theme().TextMuted)
		ui.Field(c, "Settings file", func() {
			ui.Text(c, a.configPath).Selectable().FontSize(12)
		})
		ui.Field(c, "Session logs", func() {
			ui.Text(c, a.logDir).Selectable().FontSize(12)
			if ui.Button(c, "Open logs").Clicked() {
				_ = mygo.Shell.OpenPath(a.logDir)
			}
		})
		ui.Field(c, "Credential store", func() {
			ui.Text(c, a.secretBackend()).FontSize(12)
		})
		ui.Divider(c)
		ui.Field(c, "Host list", func() {
			ui.Text(c, "Move hosts and their passwords to another machine as one sealed file.").
				FontSize(11).TextColor(c.Theme().TextMuted)
			ui.Row(c).Gap(8).Children(func() {
				if ui.Button(c, "Export…").Clicked() {
					a.showSettings = false
					a.openExport()
				}
				if ui.Button(c, "Import…").Clicked() {
					a.showSettings = false
					a.openImport()
				}
			})
		})
	})
}

func (a *App) pickStorageDir() {
	dirs, err := mygo.Dialog.Open(mygo.OpenDialogOptions{
		Parent:    a.win,
		Title:     "Choose the GoTerm data directory",
		Directory: true,
	})
	if err != nil || len(dirs) == 0 {
		return
	}
	if err := appdir.SetDir(dirs[0]); err != nil {
		a.post(func() { a.status = "Could not set the data directory: " + err.Error() })
		return
	}
	a.post(func() {
		a.status = "Data directory set to " + dirs[0] + ". Restart GoTerm to use it."
		if a.win != nil {
			a.win.Invalidate()
		}
	})
}

func (a *App) hostEditorDialog(c *ui.Context) {
	ui.Modal(c, &a.showHostEditor, func() {
		e := &a.editor
		ui.Column(c).Width(560).MaxHeight(680).Gap(10).Children(func() {
			title := "New host"
			if e.Editing {
				title = "Edit host"
			}
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, title).FontSize(18).Bold().Grow(1)
				ui.Button(c, "Cancel").OnClick(func() { a.showHostEditor = false })
			})
			if e.Err != "" {
				ui.Text(c, e.Err).FontSize(12).TextColor(c.Theme().Danger)
			}
			ui.Scroll(c).Grow(1).Children(func() {
				ui.Column(c).Gap(10).Children(func() {
					ui.Field(c, "Name", func() {
						ui.TextInput(c, &e.Name).Placeholder("Production web")
					})
					ui.Field(c, "Group", func() {
						ui.TextInput(c, &e.Group).Placeholder("Optional")
					})
					ui.Row(c).Gap(10).Children(func() {
						ui.Field(c, "Host", func() {
							ui.TextInput(c, &e.Host).Placeholder("server.example.com").Grow(1)
						})
						ui.Field(c, "Port", func() {
							ui.NumberInput(c, &e.Port, 1, 65535, 1).Width(110)
						})
					})
					ui.Field(c, "User", func() {
						ui.TextInput(c, &e.User).Placeholder("root")
					})
					ui.Field(c, "Authentication", func() {
						label := authLabels[e.AuthIdx]
						if ui.Select(c, &label, authLabels).Changed() {
							for i, l := range authLabels {
								if l == label {
									e.AuthIdx = i
								}
							}
						}
					})
					if authValues[e.AuthIdx] == "key" {
						ui.Field(c, "Private key", func() {
							ui.Row(c).Gap(8).Children(func() {
								ui.TextInput(c, &e.KeyPath).Placeholder(`C:\Users\me\.ssh\id_ed25519`).Grow(1)
								if ui.Button(c, "Choose…").Clicked() {
									a.pickKeyInto(false)
								}
							})
						})
					}
					if authValues[e.AuthIdx] == "password" {
						ui.Field(c, "Password", func() {
							ui.TextInput(c, &e.Password).Password().Placeholder("Leave empty to keep the saved one")
						})
					}
					ui.Field(c, "Second factor (append a one-time code)", func() {
						ui.Checkbox(c, &e.TwoFactor, "Ask for a dynamic code at connect time")
					})

					ui.Divider(c)
					ui.Field(c, "Jump host", func() {
						ui.Checkbox(c, &e.ProxyEnabled, "Connect through another SSH server")
					})
					if e.ProxyEnabled {
						ui.Row(c).Gap(10).Children(func() {
							ui.Field(c, "Jump host address", func() {
								ui.TextInput(c, &e.ProxyHost).Placeholder("bastion.example.com").Grow(1)
							})
							ui.Field(c, "Port", func() {
								ui.NumberInput(c, &e.ProxyPort, 1, 65535, 1).Width(110)
							})
						})
						ui.Field(c, "Jump host user", func() {
							ui.TextInput(c, &e.ProxyUser).Placeholder("Same as the target when empty")
						})
						ui.Field(c, "Jump host authentication", func() {
							label := authLabels[e.ProxyAuthIdx]
							if ui.Select(c, &label, authLabels).Changed() {
								for i, l := range authLabels {
									if l == label {
										e.ProxyAuthIdx = i
									}
								}
							}
						})
						if authValues[e.ProxyAuthIdx] == "key" {
							ui.Field(c, "Jump host private key", func() {
								ui.Row(c).Gap(8).Children(func() {
									ui.TextInput(c, &e.ProxyKeyPath).Grow(1)
									if ui.Button(c, "Choose…").Clicked() {
										a.pickKeyInto(true)
									}
								})
							})
						}
						if authValues[e.ProxyAuthIdx] == "password" {
							ui.Field(c, "Jump host password", func() {
								ui.TextInput(c, &e.ProxyPassword).Password().Placeholder("Leave empty to keep the saved one")
							})
						}
					}

					ui.Divider(c)
					ui.Checkbox(c, &e.Remember, "Store passwords in the system credential store")
					ui.Field(c, "Notes", func() {
						ui.TextInput(c, &e.Notes).Placeholder("Optional")
					})
				})
			})
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if e.Editing {
					ui.Button(c, "Delete…").OnClick(func() { a.post(func() { a.deleteHost(e.ID) }) })
				}
				if ui.PrimaryButton(c, "Save").Clicked() {
					a.saveHostEditor()
				}
			})
		})
	})
}

func (a *App) promptDialog(c *ui.Context) {
	if a.prompt == nil {
		return
	}
	p := a.prompt
	open := p.Open
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, p.Title).FontSize(17).Bold()
			ui.Text(c, p.Hint).FontSize(12).TextColor(c.Theme().TextMuted)
			var input ui.Element
			if p.Kind == promptOTP {
				input = ui.TextInput(c, &p.Value).Placeholder("6-digit code").AutoFocus()
			} else {
				input = ui.TextInput(c, &p.Value).Password().AutoFocus()
			}
			if input.Submitted() {
				a.submitPrompt()
			}
			if p.Err != "" {
				ui.Text(c, p.Err).FontSize(12).TextColor(c.Theme().Danger)
			}
			if p.Rememberable {
				ui.Checkbox(c, &p.Remember, "Remember in the system credential store")
			}
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					a.cancelPrompt()
				}
				if ui.PrimaryButton(c, "Connect").Clicked() {
					a.submitPrompt()
				}
			})
		})
	})
	p.Open = open
	if !open {
		a.prompt = nil
	}
}

func (a *App) confirmDialog(c *ui.Context) {
	if a.confirm == nil {
		return
	}
	cf := a.confirm
	open := cf.Open
	ui.Modal(c, &open, func() {
		ui.Column(c).Width(460).Gap(12).Children(func() {
			ui.Text(c, cf.Title).FontSize(17).Bold()
			ui.Text(c, cf.Body).FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.Button(c, "Cancel").Clicked() {
					cf.Open = false
					a.confirm = nil
				}
				btn := ui.PrimaryButton(c, cf.OK)
				if cf.Danger {
					btn = ui.Button(c, cf.OK).TextColor(c.Theme().Danger)
				}
				if btn.Clicked() {
					run := cf.Run
					cf.Open = false
					a.confirm = nil
					if run != nil {
						a.post(run)
					}
				}
			})
		})
	})
	cf.Open = open
	if !open && a.confirm == cf {
		a.confirm = nil
	}
}

func (a *App) knownHostsDialog(c *ui.Context) {
	ui.Modal(c, &a.showKnownHosts, func() {
		ui.Column(c).Width(620).MaxHeight(560).Gap(10).Children(func() {
			ui.Row(c).Gap(10).AlignItems(ui.Center).Children(func() {
				ui.Text(c, "Known hosts").FontSize(18).Bold().Grow(1)
				ui.Button(c, "Done").OnClick(func() { a.showKnownHosts = false })
			})
			ui.Text(c, "First contact is trusted and remembered. A changed key is always refused.").
				FontSize(11).TextColor(c.Theme().TextMuted)
			records := a.mgr.HostKeys().List()
			if len(records) == 0 {
				ui.Text(c, "Nothing on record yet.").FontSize(12).TextColor(c.Theme().TextMuted)
				return
			}
			ui.Scroll(c).Grow(1).Children(func() {
				for _, r := range records {
					addr := r.Address
					ui.Row(c).Padding(6, 8).Gap(10).Border(1, c.Theme().Border).Radius(6).Children(func() {
						ui.Column(c).Grow(1).Gap(2).Children(func() {
							ui.Text(c, addr).FontSize(12).Bold()
							ui.Text(c, r.Type+" · "+r.Fingerprint).FontSize(10).TextColor(c.Theme().TextMuted)
						})
						if ui.Button(c, "Forget").Clicked() {
							a.post(func() {
								a.mgr.HostKeys().Forget(addr)
								if a.win != nil {
									a.win.Invalidate()
								}
							})
						}
					})
				}
			})
		})
	})
}

func (a *App) aboutDialog(c *ui.Context) {
	ui.Modal(c, &a.showAbout, func() {
		ui.Column(c).Width(440).Gap(10).Children(func() {
			ui.Text(c, "GoTerm "+Version).FontSize(20).Bold()
			ui.Text(c, "An SSH terminal in pure Go, drawn by mygo (no WebView2).").
				FontSize(12).TextColor(c.Theme().TextMuted)
			ui.Divider(c)
			ui.Text(c, "Data: "+a.dir).FontSize(11).Selectable()
			ui.Text(c, "Credentials: "+a.secretBackend()).FontSize(11)
			ui.Text(c, "Terminal: Ghostty libghostty-vt").FontSize(11)
			ui.Row(c).Gap(8).Justify(ui.End).Children(func() {
				if ui.PrimaryButton(c, "Close").Clicked() {
					a.showAbout = false
				}
			})
		})
	})
}
