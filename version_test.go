package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

// TestVersionsAgree keeps the two places a version is written from drifting
// apart. app.go is what the status bar shows; mygo.json is what the release
// workflow reads and stamps into the executable's version resource. A tag that
// matches one but not the other has already failed a release once.
func TestVersionsAgree(t *testing.T) {
	raw, err := os.ReadFile("mygo.json")
	if err != nil {
		t.Fatalf("read mygo.json: %v", err)
	}
	var cfg struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil {
		t.Fatalf("parse mygo.json: %v", err)
	}
	if cfg.Version == "" {
		t.Fatal("mygo.json has no version")
	}
	if strings.TrimSpace(Version) != strings.TrimSpace(cfg.Version) {
		t.Fatalf("version mismatch: app.go has %q, mygo.json has %q; "+
			"bump both together before tagging", Version, cfg.Version)
	}
	if strings.HasPrefix(cfg.Version, "v") {
		t.Fatalf("mygo.json version %q carries a %q prefix; the workflow compares it "+
			"against the tag with the prefix stripped", cfg.Version, "v")
	}
	_ = fmt.Sprint
}
