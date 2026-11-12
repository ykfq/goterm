#!/usr/bin/env bash
# GoTerm checks on the Linux dev box — the mirror of what CI runs on Windows
# (`go vet ./...` + `go test ./...`), with one platform guard.
#
# mygo loads two native stacks in one process: the Ghostty terminal library
# and pango for text. On Linux the two collide (Ghostty's build statically
# links its own copy of the shaping libraries), and a test binary that uses
# both dies with a SIGSEGV inside pango, in whichever UI test happens to run
# after the first real terminal opens. Windows and macOS are unaffected —
# that is where the terminal plugin ships and where CI proves it.
#
# So here the terminal-backed tests self-skip (they already do when the engine
# cannot load) and MYGO_GHOSTTY_VT pins that outcome deterministically,
# instead of letting the first run race a library download. Set
# MYGO_GHOSTTY_VT=/path/to/libghostty-vt.so to opt the terminal tests back in
# if you are debugging the plugin itself on Linux.
set -euo pipefail
if [[ "$(uname -s)" == Linux ]]; then
	export MYGO_GHOSTTY_VT="${MYGO_GHOSTTY_VT:-/nonexistent}"
fi
cd "$(dirname "$0")/.."
go vet ./...
go test ./... "$@"
