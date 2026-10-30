//go:build !windows

package fonts

// List has no portable equivalent: fontconfig (Linux) and CoreText (macOS) each
// have their own enumeration API, and shipping a half-tested one for both is
// worse than being honest. The settings screen still needs a usable list, so it
// gets the families that ship with these systems or with the tools developers
// install anyway. Everything here is monospace.
func List() ([]Family, error) {
	names := []string{
		"DejaVu Sans Mono",
		"Liberation Mono",
		"Menlo",
		"Monaco",
		"Noto Sans Mono",
		"SF Mono",
		"Ubuntu Mono",
	}
	out := make([]Family, 0, len(names))
	for _, name := range names {
		out = append(out, Family{Name: name, Mono: true})
	}
	return out, nil
}
