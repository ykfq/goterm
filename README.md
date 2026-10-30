# GoTerm

**A desktop SSH terminal written in Go + [Wails v3](https://wails.io) + [xterm.js](https://xtermjs.org):**
- `rz`/`sz` friendly: transfers use [`trz`/`tsz`](https://github.com/trzsz/trzsz.js) by default, and the
  ZMODEM `rz`/`sz` of a plain lrzsz install works too;
- session keepalive: an SSH session can be kept alive for up to 24 h (configurable);
- session duplication: need another shell on the same connection? Right-click a tab → `Duplicate`;
- built-in keyword highlighting for the usual log and command output;
- full list: [jump to “Features”](#features)

![GoTerm's welcome screen](docs/screenshot.png)

**English** · [简体中文](README.zh-CN.md) · [需求台账 / plan](plan.md) · [实现与决策记录](docs/DECISIONS.md)

---

## 🤖 Continuing this project with an AI

**This repository is maintained to be handed to an AI and continued.** [`plan.md`](plan.md) is the
interface: a requirements ledger — what was asked for, where each item stands, which red lines must
never be crossed, what is left, and how to verify a change.

Hand the repository to any AI coding assistant with one line:

> Read `plan.md` and keep developing from the ledger — fix / improve / add xx, then run the tests and
> update the ledger.

---

## Features

**Terminal**
- Real SSH: password, private key (with passphrase prompts at connect time), or ssh-agent.
- Multi-tab, each with its own xterm, SSH session and transfer filter. Drag to reorder.
  *Duplicate* opens another shell on the **same** connection — no second login.
- GPU (WebGL) rendering with automatic DOM fallback.
- `Ctrl+Shift+F` find-in-tab (`@xterm/addon-search`); the count is real buffer matches, not what fits
  on screen.
- Copy on select (mouse-release only), multi-line paste confirmation with an editable box.
- Remote text encoding: UTF-8 (default) or GB18030 / GBK / GB2312 / Big5 / Shift_JIS, streamed so a
  multi-byte character split across a 64 KiB chunk is still decoded correctly.

**Transfers**
- Native `trz`/`tsz` with the real trzsz progress bar; drag files onto the terminal to upload.
- `rz`/`sz` for lrzsz-only servers, with **the same progress line** (upstream trzsz `TextProgressBar`,
  vendored verbatim) drawn in the terminal.
- Write paths are confined to the folder you actually picked in the native file dialog.
- Cancel is silent — no thrown protocol exception, no half-written file claimed as complete.

**Hosts and secrets**
- Saved hosts, groups, search, jump host (`ssh -J`, single hop), two-factor as "code appended to the
  password" (the JumpServer style).
- Host keys pinned on first use; a changed key refuses to connect. *Known hosts* panel to forget one.
- Passwords live in Windows Credential Manager (pure syscall — no console window flash); platforms
  without a keyring fall back to a sealed file. **No secret ever enters the webview.**
- Export/import: hosts with their secrets in one opaque file — scrypt + AES-256-GCM.

**Staying connected**
- SSH keepalives (`keepalive@openssh.com`) — never an Enter, so nothing on the far side can run early.
- Anti-idle for `TMOUT` / bastion activity timers, off by default: a space and a backspace (invisible),
  an empty line, or a bare space for the one case the first two cannot reach — a TUI.
- **Every connection has a hard lifetime** (default 12 h, 1–24 h configurable, enforced in Go).
  Keepalives prove a link is alive; they are not a way to renew a login forever.

**The rest**
- Dark and light themes; the window frame follows the theme (DWM). Keyword colouring with per-line
  vocabularies and `.msyn` import, five built-in rule sets.
- Settings apply to every open tab instantly and persist.
- Config folder defaults to `%USERPROFILE%\AppData\Roaming\goterm` and is configurable
  (Settings → Behaviour → Storage).

---

## Install

Download `goterm-v1.0.0-windows-x64.exe` (and the matching `.md5` / `.sha256`) from the repository's
[**Releases**](https://github.com/ykfq/goterm/releases) page. The binaries there are built by GitHub
Actions from the tag ([`.github/workflows/release.yml`](.github/workflows/release.yml)), never
committed to the repository.

It is a **single portable executable** — no installer. WebView2 (the browser engine Wails embeds) ships
with Windows 10/11; on Windows Server or an LTSC image you may need the WebView2 Runtime installed once.
Put the exe anywhere and run it.

Requirements: **Windows 10/11 or Windows Server, x64**. This is a Windows application; the Wails
project can be built for macOS/Linux, but only the Windows path is exercised here.

## Build from source

```powershell
# prerequisites: Go >= 1.26, Node >= 20, and the Wails v3 CLI.
# Go 1.26 is required, not 1.25: wails3 v3.0.0 declares `go 1.26.0`.
go install github.com/wailsapp/wails/v3/cmd/wails3@latest

git clone https://github.com/ykfq/goterm
cd goterm
wails3 build          # frontend deps + bindings + vite bundle + binary -> bin\goterm.exe
```

Behind a slow or filtered network a Go module mirror helps:
`go env -w GOPROXY=https://goproxy.cn,direct`. If `wails3` is not found, put Go's bin directory on
`PATH`.

Regenerate the TypeScript bindings after changing a bound method:

```powershell
wails3 generate bindings -ts -d frontend/bindings -clean
```

`wails3 dev` runs the Vite server with a hot-reloading webview; `npm run dev` inside `frontend/` gives
you the UI alone (it will report "No Go backend" — there is no bridge).

## Tests

```powershell
go test ./...                        # Go: SSH stack, host store, secrets, app bindings
npm --prefix frontend test           # 373 vitest cases in 38 files
npm --prefix frontend run typecheck  # vue-tsc, strict
```

`internal/sshtest` is a real SSH server used by the Go tests, and `cmd/sshtest` exposes it for manual
work — run it and connect to `127.0.0.1:2222` with `tester` / `goterm-dev` instead of a real host:

```powershell
go build -o build\bin\sshtest.exe .\cmd\sshtest
.\build\bin\sshtest.exe -addr 127.0.0.1:2222 -hostkey "$env:TEMP\sshtest_key.pem"
#   -2fa                password auth also demands a trailing one-time code
#   -allow-forwarding   the server can act as a jump host (direct-tcpip)
#   -shell trzsz        serves tsz/trz transfers
```

The GUI smoke scripts drive the built `bin\goterm.exe` with synthetic mouse and keyboard input, so they
need an interactive desktop session:

```powershell
.\tools\gui-demo.ps1           # -> build\gui-dialog.png, gui-session.png, gui-typed.png
.\tools\gui-check.ps1          # window renders at all
.\tools\frame-check.ps1        # asks DWM what the window frame actually is
.\tools\gui-transfer.ps1 -Direction download   # drives a real tsz/trz transfer, checks bytes
```

Those captures are **local artefacts and git-ignored on purpose**: a screenshot carries whatever the
desktop or a screen-capture policy stamps onto it. `docs/screenshot.png` is the one curated image.

---

## Keys and mouse

| Action | Binding |
| --- | --- |
| Copy selection | `Ctrl+Shift+C` |
| Paste | `Ctrl+Shift+V` |
| Find in this tab | `Ctrl+Shift+F`, or right-click → *Find in terminal…* |
| Quick connect | `Ctrl+Shift+T` (`Ctrl+T` when the terminal is not focused) |
| Close tab | `Ctrl+Shift+W` (`Ctrl+W` outside the terminal), or middle-click the tab |
| Toggle sidebar | `Ctrl+Shift+B` (`Ctrl+B` outside the terminal) |
| Clear buffer | `Ctrl+Shift+K` (`Ctrl+K` outside the terminal) |
| Next / previous tab | `Ctrl+Tab` / `Ctrl+Shift+Tab` |
| Font size | `Ctrl+=`, `Ctrl+-`, `Ctrl+0`, or `Ctrl+wheel` |
| Context menu / tab menu | right-click the terminal / right-click a tab |
| Settings | `Ctrl+,` |

**Some control keys belong to the shell, not to the app.** While the keyboard is inside a terminal,
`Ctrl+W` deletes a word, `Ctrl+K` kills to end of line, `Ctrl+T` transposes, `Ctrl+B` is the tmux prefix
and `Ctrl+F` is readline's forward-char — so the app's commands live on the `Shift`ed forms, and the
unshifted ones still work when focus is in the sidebar or a dialog. (This is not politeness: `Ctrl+W`
used to close the tab *and* rub out a word, so three typos cost you the session.)

**When a full-screen program owns the mouse** (`htop`, `tmux`): right-click goes to the program, which
draws its own menu; `Shift+right-click` opens GoTerm's. `Shift+drag` selects locally, and copy-on-select
still copies it on release. `Ctrl+wheel` still zooms.

**Pasting several lines** opens an editable confirmation box first — a block of text is a script nobody
has read. One line (with or without its newline) goes straight through.

## Settings worth knowing

| Section | What it does |
| --- | --- |
| Appearance | Terminal font (monospace only, enumerated from the system), **Block and box characters**, **Remote text encoding**, terminal background (six presets or any colour) |
| Behaviour | Word separator (with a live preview of what double-click will select), copy on select, **Idle sessions** (anti-idle + which nudge), **Session lifetime** (1–24 h), **Storage** (where the files live) |
| Highlighting | Five built-in keyword sets, custom keywords/regex with a colour, `.msyn` import |

The light/dark theme is the sun/moon icon in the status bar; `Ctrl+=` / `Ctrl+-` / `Ctrl+0` change the
font size.

**Block and box characters** decides who draws `█`,`│`,`─`: xterm (fills the whole cell, which fuses
`dust`'s bar chart into one block) or the font (keeps the line spacing, which can leave a hairline gap in
a TUI's box borders). Off by default; both are pictured in
[`docs/DECISIONS.md`](docs/DECISIONS.md) and the trade-off is real — there is no third option in xterm.

**Storage** shows the folder that holds hosts, pinned host keys and any sealed secrets, and lets you move
it. A move takes effect on the next start and nothing is copied for you — the old folder keeps its files
until you move them, and a fresh folder simply starts empty. A `GOTERM_DIR` environment variable, when
set, always wins (that is also how a portable copy is pointed at a folder beside the exe).

Appearance and Behaviour apply instantly to every open tab and persist; switching panel tabs only
shows/hides sections, so a half-written custom rule is never lost.

## State on disk

The folder Settings → Storage shows (default `%USERPROFILE%\AppData\Roaming\goterm`, or
`$XDG_CONFIG_HOME/goterm`):

| File | Contents |
| --- | --- |
| `hosts.json` | saved servers, no secrets |
| `known_hosts.json` | pinned fingerprints |
| `prefs.json` | the storage-folder pointer, always in the default folder |
| `secrets.enc` + `master.key` | only when the sealed-file backend is in use |

Credential-Manager secrets are generic credentials named `goterm/host/<id>/secret`.

---

## How it is put together

```
main.go, app.go      Wails application + bound methods (thin: every method is a few lines)
files.go             native file pickers and the scoped file bridge
selftest.go          in-app self-test against internal/sshtest
cmd/sshtest          a real SSH server for development and tests
internal/sshx        SSH: connect, auth, host keys, keepalive, jump host, sessions, lifetime
internal/hoststore   hosts.json, export/import, error markers the UI reacts to
internal/secrets     Windows Credential Manager, sealed-file fallback
internal/appdir      where state lives: GOTERM_DIR > the Settings pointer > the default
internal/{atomicfile,hostport,fonts,wintheme,sshtest,testutil}

frontend/src/lib/
  terminal.ts    the byte pipe: base64 in, ordered writes to xterm, anti-idle, find, encoding
  bytes.ts       base64 / UTF-8 / latin1 — the whole encoding story
  zmodem.ts      lrzsz scan/transfer, driving the vendored progress bar
  store.ts       app state: tabs, notices, settings, connect state machine
frontend/src/vendor/trzsz/progress.ts   trzsz's progress bar, vendored verbatim
frontend/src/components/                Vue 3 + Tailwind 4
```

`app.go` does bindings only; `internal/**` never imports Wails; dependencies point one way
(`main → hoststore → sshx / secrets`). Full design notes, including the parts that took several
attempts, are in [`docs/DECISIONS.md`](docs/DECISIONS.md).

## Limits

- The **remote** still needs `trz`/`tsz` (`pip install trzsz`, or drop the single binary in
  `/usr/local/bin`) or `lrzsz`. GoTerm speaks the protocols; it cannot install them.
- Jump hosts are a single hop (`ssh -J` style, no `-J a,b`), and the bastion must allow TCP forwarding.
- Two-factor is the "code appended to the password" kind. There is no separate
  keyboard-interactive challenge and no TOTP generation inside GoTerm.
- Anti-idle cannot reset a timer that only counts *completed lines* unless you pick the empty-line nudge;
  the bare-space nudge is the only one that reaches a TUI, and it leaves a space behind.
- Session lifetime is a hard cap: at 12 h (or whatever you set) the connection closes and says why.
- Keepalives prove the link is alive; they cannot detect a frozen server that still answers TCP
  (an unanswered probe is noticed within ~45 s × 3).
- Moving the storage folder applies on the next start, and GoTerm never copies your files for you.
- No installer (NSIS), no port forwarding, no `~/.ssh/config` parsing, no session logging, no per-host
  profiles. Linux/macOS builds are untested and use a short built-in font list.
- Output is decoded per your encoding setting; **input is always sent as UTF-8** (browsers have no GBK
  encoder). On a GBK-only host, `export LANG=zh_CN.UTF-8` is the way out.

## License

[Apache License 2.0](LICENSE) © GoTerm contributors. Third-party notices are in [NOTICE](NOTICE):
bundled code keeps its own licence — Wails, xterm.js, Vue, trzsz, zmodem.js, lucide.
