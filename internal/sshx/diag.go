package sshx

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// The keepalive diary.
//
// Every "it still does not work" report this feature has had was answered by
// reading code and guessing, because from outside the process there is no way
// to tell "the switch is off" from "the keystroke was suppressed" from "the
// server hung up anyway". Four rounds of that is enough: the app writes what it
// decided and what it sent to one small file, and a report can carry evidence
// instead of a guess.
//
// It records, per session: what was configured at connect, every keystroke that
// went out, every keystroke that was suppressed and why, every probe that went
// unanswered, and how the session ended.
type diagLog struct {
	mu sync.Mutex
	w  io.Writer
}

func (d *diagLog) set(w io.Writer) {
	d.mu.Lock()
	d.w = w
	d.mu.Unlock()
}

func (d *diagLog) note(format string, args ...any) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.w == nil {
		return
	}
	_, _ = fmt.Fprintf(d.w, "%s %s\n", time.Now().Format("2006-01-02 15:04:05"), fmt.Sprintf(format, args...))
}
