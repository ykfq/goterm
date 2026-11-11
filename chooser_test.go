package main

import (
	"bytes"
	"errors"
	"sync"
)

// fixedChooser answers every dialog the way a user who already picked files and
// a folder would, so a transfer test never opens a window.
type fixedChooser struct {
	dir    string
	upload []string
}

func (f fixedChooser) ChooseDownloadFolder(string) (string, error) { return f.dir, nil }

func (f fixedChooser) DefaultDownloadFolder() (string, error) { return f.dir, nil }

func (f fixedChooser) ChooseUpload(bool) ([]string, error) {
	if len(f.upload) == 0 {
		return nil, errors.New("upload not scripted")
	}
	return f.upload, nil
}

// syncBuffer is a bytes.Buffer safe for a drain goroutine and the test.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.buf.Write(p)
}

func (s *syncBuffer) Buffer() *bytes.Buffer { return &s.buf }
