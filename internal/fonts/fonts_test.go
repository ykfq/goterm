package fonts

import (
	"runtime"
	"strings"
	"testing"
)

// This test is the proof that the list is real: it asks the operating system and
// checks that a face everybody has is present, and that a face everybody knows is
// proportional is not called monospace.
func TestListReportsInstalledFamilies(t *testing.T) {
	list, err := List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) < 5 {
		t.Fatalf("only %d families reported; the enumeration is not working", len(list))
	}

	mono := Monospace(list)
	if len(mono) == 0 {
		t.Fatal("no monospace family at all")
	}
	for _, family := range mono {
		if family.Name == "" {
			t.Fatal("a family with no name")
		}
	}

	if runtime.GOOS != "windows" {
		return // the assertions below are about fonts Windows ships
	}

	byName := map[string]Family{}
	for _, family := range list {
		byName[family.Name] = family
	}
	if consolas, ok := byName["Consolas"]; !ok {
		t.Fatalf("Consolas is missing from %d families; the enumeration is not reading faces", len(list))
	} else if !consolas.Mono {
		t.Fatal("Consolas reported as proportional")
	}
	if arial, ok := byName["Arial"]; ok && arial.Mono {
		t.Fatal("Arial reported as fixed-pitch; the pitch bit is being read wrong")
	}
}

func TestMonospaceKeepsOnlyFixedPitch(t *testing.T) {
	list := []Family{
		{Name: "zeta", Mono: true},
		{Name: "Alpha", Mono: false},
		{Name: "beta", Mono: true},
	}
	got := Monospace(list)
	if len(got) != 2 {
		t.Fatalf("got %d families, want 2", len(got))
	}
	// Sorted case-insensitively.
	if !strings.EqualFold(got[0].Name, "beta") || !strings.EqualFold(got[1].Name, "zeta") {
		t.Fatalf("order = %v", got)
	}
}
