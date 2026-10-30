//go:build windows

package fonts

import (
	"fmt"
	"sort"
	"strings"
	"syscall"
	"unsafe"
)

// GDI enumeration rather than the font registry: the registry lists files, and
// answers "Consolas (TrueType)" — it says nothing about whether a face is
// fixed-pitch. EnumFontFamiliesEx hands back each LOGFONT, whose lfPitchAndFamily
// carries exactly that bit, so the list the settings screen shows is the set of
// fonts that can actually render a terminal grid.

var (
	gdi32  = syscall.NewLazyDLL("gdi32.dll")
	user32 = syscall.NewLazyDLL("user32.dll")

	procGetDC              = user32.NewProc("GetDC")
	procReleaseDC          = user32.NewProc("ReleaseDC")
	procEnumFontFamiliesEx = gdi32.NewProc("EnumFontFamiliesExW")
)

const (
	defaultCharSet = 1  // DEFAULT_CHARSET: all of them, we dedupe by name
	fixedPitch     = 1  // FIXED_PITCH bit of lfPitchAndFamily
	pitchMask      = 3  // low two bits — VARIABLE_PITCH is 2
	rasterFontType = 1  // RASTER_FONTTYPE: bitmap, does not scale
	lfFaceSize     = 32 // LF_FACESIZE
)

type logFont struct {
	Height         int32
	Width          int32
	Escapement     int32
	Orientation    int32
	Weight         int32
	Italic         byte
	Underline      byte
	StrikeOut      byte
	CharSet        byte
	OutPrecision   byte
	ClipPrecision  byte
	Quality        byte
	PitchAndFamily byte
	FaceName       [lfFaceSize]uint16
}

// enumLogFontEx mirrors ENUMLOGFONTEXW: the LOGFONT, then the full name and the
// script. Only the LOGFONT is read, but the struct has to be the right size.
type enumLogFontEx struct {
	LogFont  logFont
	FullName [64]uint16
	Script   [32]uint16
}

// List returns every installed family, with the fixed-pitch flag set on those a
// terminal can use.
func List() ([]Family, error) {
	hdc, _, callErr := procGetDC.Call(0)
	if hdc == 0 {
		return nil, fmt.Errorf("GetDC: %w", callErr)
	}
	defer func() { _, _, _ = procReleaseDC.Call(0, hdc) }()

	seen := map[string]Family{}

	// The C prototype is (ENUMLOGFONTEX*, NEWTEXTMETRICEX*, DWORD, LPARAM); all
	// four parameters must appear or the stack frame is wrong.
	callback := syscall.NewCallback(func(elf *enumLogFontEx, _ uintptr, fontType uint32, _ uintptr) uintptr {
		if elf == nil {
			return 1
		}
		// Bitmaps cannot be scaled and PostScript faces are unusable here.
		if fontType&rasterFontType != 0 {
			return 1
		}
		name := syscall.UTF16ToString(elf.LogFont.FaceName[:])
		if name == "" || strings.HasPrefix(name, "@") {
			// "@family" is the vertical-writing twin of the same face.
			return 1
		}
		mono := elf.LogFont.PitchAndFamily&pitchMask == fixedPitch
		if existing, ok := seen[name]; ok {
			// The same family arrives once per character set; a single
			// fixed-pitch instance is enough to prove it is monospace.
			seen[name] = Family{Name: name, Mono: existing.Mono || mono}
			return 1
		}
		seen[name] = Family{Name: name, Mono: mono}
		return 1
	})

	var query logFont
	query.CharSet = defaultCharSet
	// The callback is handed as a plain uintptr because that is what the Windows
	// calling convention takes; `go vet` cannot see through NewProc.Call.
	ret, _, err := procEnumFontFamiliesEx.Call(
		hdc,
		uintptr(unsafe.Pointer(&query)),
		callback,
		0,
		0,
	)
	if ret == 0 {
		return nil, fmt.Errorf("EnumFontFamiliesEx: %w", err)
	}

	out := make([]Family, 0, len(seen))
	for _, family := range seen {
		out = append(out, family)
	}
	sort.Slice(out, func(i, j int) bool { return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name) })
	return out, nil
}
