package main

import (
	"os"
	"testing"
)

// TestMain gives the whole package one data directory. appdir caches its
// choice for the process, so per-test temp dirs would silently fight over it.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "goterm-tests-")
	if err != nil {
		panic(err)
	}
	_ = os.Setenv("GOTERM_DIR", dir)
	code := m.Run()
	_ = os.RemoveAll(dir)
	os.Exit(code)
}
