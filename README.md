# GoTerm

**A desktop SSH terminal written in pure Go**, with a native user interface drawn by
[mygo](https://mygo.egoist.dev) and [Ghostty's terminal emulator](https://ghostty.org)
(libghostty-vt, loaded at runtime).

No WebView2, no Chromium, no Node, no frontend build: the window, the tabs, the settings and the
terminal are all Go. That is also why memory stays bounded when a session prints a hundred megabytes
(4 MB of output queue + an 8 MB scrollback, then SSH backpressure).

**English** · [简体中文](README.zh-CN.md) · [Migration ledger](plan2.md) · [1.x ledger](plan.md)

---

## Status: 2.0 preview

This branch (`mygo-native`) replaces the 1.4.0 Wails/Vue/xterm frontend. What works today:

- SSH with password, private key and ssh-agent authentication; jump hosts; 2FA one-time codes;
  host-key trust on first use (a changed key is refused); keepalive with an idle window.
- Native host list with groups and search; host editor (host, port, user, auth, jump host, 2FA).
- Tabs: several sessions, duplicate without a second login, reconnect, close.
- **Switching tabs puts the keyboard in the terminal** — type immediately, no second click.
- Real terminal: colours, mouse reporting, scrollback, selection, copy/paste, search
  (Ctrl+Shift+F), IME composition at the cursor, OSC 52 copy.
- Session logging to `%APPDATA%\goterm\logs` (off by default, switchable at runtime).
- **SFTP file panel** (Ctrl+Shift+E, tab menu, or `--files`): browse, upload, download, make
  folders, rename, delete, refresh, with progress. Drop files on the terminal to upload them to the
  panel's directory. It rides the session's own SSH connection, so no second login.
- **Host import/export**: one sealed file (scrypt + AES-256-GCM) with hosts and their passwords,
  through native file dialogs.
- Settings in `settings.json`: theme, font family/size, scrollback (MB), logging, remote encoding
  (UTF-8/GB18030/Big5/Shift_JIS), select-to-copy, idle hours, data directory.

Not ported yet (tracked in [plan2.md](plan2.md)): trzsz/ZMODEM transfers (SFTP covers file transfer),
keyword highlighting (a VT emulator has no line hook), tab drag-reordering, and the known-hosts
review beyond listing/forgetting.

## Build

Requirements: Go 1.27.3 or later. Nothing else.

```powershell
go build -o bin\goterm.exe .
.\bin\goterm.exe
```

A program not built by the mygo CLI downloads `libghostty-vt` once into
`%LOCALAPPDATA%\mygo\natives` and verifies its hash. To package the app the way releases are made:

```powershell
go tool mygo build -platform windows/amd64 -o out -skip-installer
# out\windows-amd64\GoTerm.exe + ghostty-vt.dll
```

The release workflow builds a zip with both files, the licence and checksums.

## Using it

- **New host** (Ctrl+Shift+T), fill in the address and pick an authentication method, then
  double-click the host in the sidebar.
- **Find** in the terminal: Ctrl+Shift+F. **File panel**: Ctrl+Shift+E. **Close tab**:
  Ctrl+Shift+W. **Settings**: Ctrl+,.
- **Duplicate / Reconnect**: right-click a tab. Drop files on the terminal to upload them.
- `goterm.exe --connect <host-id>` opens a saved host at start-up; add `--files` to bring up its
  file panel too.

Passwords live in Windows Credential Manager (or a sealed file where there is no key ring) and are
never written to `hosts.json`. Session logs record the server's bytes before decoding, so they match
what SSH delivered.

## License

Apache License 2.0 — see [LICENSE](LICENSE) and [NOTICE](NOTICE).
