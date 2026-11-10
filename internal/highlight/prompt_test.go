package highlight

import (
	"strings"
	"testing"
)

func TestPromptThenCommandOutputIsColoured(t *testing.T) {
	h := New()
	// sshtest's banner and prompt, then `echo error` echoing and its output.
	feed(h, "\x1b[0;32msshtest\x1b[0m ready\r\n")
	feed(h, "\x1b[1;36msshtest\x1b[0m:\x1b[1;34m~\x1b[0m$ ")
	if got := feed(h, "echo error"); got != "echo error" {
		t.Fatalf("typed echo was painted: %q", got)
	}
	feed(h, "\r\n")
	got := feed(h, "error\r\n")
	if !strings.Contains(got, brightRed) {
		t.Fatalf("command output was not highlighted: %q", got)
	}
	if strip(got) != "error\r\n" {
		t.Fatalf("bytes changed: %q", strip(got))
	}
}
