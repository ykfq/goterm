// Package fonts asks the operating system which font families are installed,
// and which of them are fixed-pitch.
//
// A terminal can only use a monospace font: cells are a fixed rectangle, and a
// proportional face renders "il1W" at different widths, which breaks the grid.
// So the settings screen offers a list rather than a free-text field — and the
// list comes from the machine, not from a guess at what the user happens to have.
package fonts

import (
	"sort"
	"strings"
)

// Family is one installed font family.
type Family struct {
	Name string `json:"name"`
	// Mono is true when the face is fixed-pitch, i.e. usable in a terminal.
	Mono bool `json:"mono"`
}

// ByName sorts families the way a font menu does it: case-insensitive, with the
// original spelling breaking ties.
func ByName(list []Family) []Family {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := strings.ToLower(list[i].Name), strings.ToLower(list[j].Name)
		if a != b {
			return a < b
		}
		return list[i].Name < list[j].Name
	})
	return list
}

// Monospace returns only the fixed-pitch families, sorted.
func Monospace(list []Family) []Family {
	var out []Family
	for _, family := range list {
		if family.Mono {
			out = append(out, family)
		}
	}
	return ByName(out)
}
