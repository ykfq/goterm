package sshx

import (
	"bufio"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Session logging
//
// Every byte the terminal receives is also written to a file, when the user has
// asked for it. The log lives in Go rather than in the frontend for two
// reasons: the bytes are already here, exactly as the server sent them, and a
// `cat huge.log` must not travel the webview twice (once to the screen, once
// back to disk through an IPC call per chunk).
//
// What lands in the file is the *raw* PTY stream, colour sequences included —
// the same thing `script(1)` records. Re-encoding it as plain text would mean
// parsing escape sequences in the hot path, and a log that silently differs
// from what the user saw is worse than one that needs `cat -v` or a pager that
// understands ANSI.

// SessionLogInfo is what the settings screen shows: whether sessions are being
// recorded, and where the files go.
type SessionLogInfo struct {
	Enabled bool   `json:"enabled"`
	Dir     string `json:"dir"`
}

// unsafeLogChars matches everything that is not safe in a Windows file name.
var unsafeLogChars = regexp.MustCompile(`[^A-Za-z0-9._-]+`)

// sessionLogPath names one session's file. The target is in the name because a
// folder of logs is read by eye, and a timestamp alone would not say which
// server a file belongs to.
func sessionLogPath(dir, target string, started time.Time) string {
	name := unsafeLogChars.ReplaceAllString(target, "_")
	name = strings.Trim(name, "_")
	if name == "" {
		name = "session"
	}
	stamp := started.Local().Format("20060102-150405")
	return filepath.Join(dir, fmt.Sprintf("goterm-%s-%s.log", stamp, name))
}

// sessionLog is one open log file. Only the session's pump writes to it; the
// mutex exists for the other direction — the user switching logging off while
// output is still arriving.
type sessionLog struct {
	mu     sync.Mutex
	file   *os.File
	buf    *bufio.Writer
	path   string
	closed bool
	// broken is set after a write error; the session keeps running, the file
	// simply stops growing, and the user is told once.
	broken bool
	// onError is called once with the first write failure.
	onError func(error)
}

func openSessionLog(dir, target string, started time.Time, onError func(error)) (*sessionLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create log directory %s: %w", dir, err)
	}
	path := sessionLogPath(dir, target, started)
	// Two sessions can start in the same second (a duplicated tab does): the
	// second one gets a numbered sibling rather than appending to the first
	// session's file.
	for attempt := 1; attempt <= 100; attempt++ {
		f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o600)
		if err == nil {
			return &sessionLog{
				file:    f,
				buf:     bufio.NewWriterSize(f, 64*1024),
				path:    path,
				onError: onError,
			}, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return nil, fmt.Errorf("open session log %s: %w", path, err)
		}
		path = numberedLogPath(sessionLogPath(dir, target, started), attempt+1)
	}
	return nil, fmt.Errorf("could not find an unused session log name in %s", dir)
}

// numberedLogPath turns `goterm-….log` into `goterm-…-2.log`.
func numberedLogPath(path string, n int) string {
	base := strings.TrimSuffix(path, ".log")
	return fmt.Sprintf("%s-%d.log", base, n)
}

// write appends one run of server bytes.
func (l *sessionLog) write(p []byte) {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.broken {
		return
	}
	if _, err := l.buf.Write(p); err != nil {
		l.fail(err)
	}
}

// flush pushes what is buffered to the file. Called from the pump's own ticker,
// so a file is never more than a tick behind what the user is watching.
func (l *sessionLog) flush() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed || l.broken {
		return
	}
	if err := l.buf.Flush(); err != nil {
		l.fail(err)
	}
}

func (l *sessionLog) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return
	}
	l.closed = true
	if !l.broken {
		_ = l.buf.Flush()
	}
	_ = l.file.Close()
}

// fail marks the file unusable and reports it once. Called with the mutex held.
func (l *sessionLog) fail(err error) {
	if l.broken {
		return
	}
	l.broken = true
	if l.onError != nil {
		l.onError(err)
	}
}

// attachLog installs (or, with nil, removes) the log a session's output is
// copied into. Safe while the pump is running: the pump re-reads the field for
// every chunk.
func (s *Session) attachLog(l *sessionLog) *sessionLog {
	s.logMu.Lock()
	previous := s.log
	s.log = l
	s.logMu.Unlock()
	if previous != nil && previous != l {
		previous.close()
	}
	return l
}

// attachLogOnce installs a log only if the session does not already have one.
// Two settings changes arriving together must not leave one of the files open
// with nobody writing to it.
func (s *Session) attachLogOnce(l *sessionLog) bool {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	if s.log != nil {
		return false
	}
	s.log = l
	return true
}

// currentLog is the pump's per-chunk view of the log.
func (s *Session) currentLog() *sessionLog {
	s.logMu.Lock()
	defer s.logMu.Unlock()
	return s.log
}

// ConfigureSessionLogging turns session recording on or off for the whole
// manager: sessions that are already open start or stop recording too, so the
// switch does what it says rather than applying next time.
//
// An empty dir disables recording; the manager never invents a location. The
// caller (app.go) resolves the state directory, which keeps this package free
// of UI policy.
func (m *Manager) ConfigureSessionLogging(enabled bool, dir string) (SessionLogInfo, error) {
	if enabled && strings.TrimSpace(dir) == "" {
		return SessionLogInfo{}, fmt.Errorf("session logging needs a directory")
	}
	m.logMu.Lock()
	m.logEnabled = enabled
	m.logDir = dir
	m.logMu.Unlock()

	// The session map has its own lock; the log setting does not own it.
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.RUnlock()

	for _, s := range sessions {
		if enabled {
			m.openLogFor(s)
		} else {
			s.attachLog(nil)
			s.setLogPath("")
		}
	}
	return m.SessionLogging(), nil
}

// SessionLogging reports the current setting.
func (m *Manager) SessionLogging() SessionLogInfo {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	return SessionLogInfo{Enabled: m.logEnabled, Dir: m.logDir}
}

// loggingSettings is the pump's cheap read of the manager-wide setting.
func (m *Manager) loggingSettings() (bool, string) {
	m.logMu.Lock()
	defer m.logMu.Unlock()
	return m.logEnabled, m.logDir
}

// openLogFor starts a file for one session, reporting a failure where the user
// can see it instead of losing the recording quietly.
func (m *Manager) openLogFor(s *Session) {
	enabled, dir := m.loggingSettings()
	if !enabled || dir == "" {
		return
	}
	if s.currentLog() != nil || s.closed() {
		return
	}
	started := s.Info().CreatedAt
	if started.IsZero() {
		started = time.Now()
	}
	target := s.Info().Target
	log, err := openSessionLog(dir, target, started, func(err error) {
		m.notice(s.id, "warn", fmt.Sprintf("%s: session logging stopped: %v", target, err))
	})
	if err != nil {
		m.notice(s.id, "warn", fmt.Sprintf("could not record this session: %v", err))
		return
	}
	if !s.attachLogOnce(log) {
		// Another settings change opened one first; this file would be orphaned.
		log.close()
		return
	}
	s.setLogPath(log.path)
	m.notice(s.id, "info", fmt.Sprintf("Recording this session to %s", log.path))
}

// setLogPath keeps the session summary in step with the file being written.
func (s *Session) setLogPath(path string) {
	s.infoMu.Lock()
	s.info.LogPath = path
	s.infoMu.Unlock()
}

// closeLog is called when a session's pump exits: the last bytes are on disk
// even if the connection died mid-line.
func (s *Session) closeLog() {
	s.attachLog(nil)
}
