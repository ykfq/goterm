package main

import (
	"math"
	"time"

	"github.com/egoist/mygo/ui"
)

// The tab dot's slow blink while hidden output arrived. It is driven from the
// view with a timer, because the alternative — an animation on every frame —
// costs a frame budget for a 6 DIP square.

// pulsePeriod is one full breath: slow enough to read as "something happened
// here", quick enough to notice.
const pulsePeriod = 1800 * time.Millisecond

// pulseFrame is how often the dot is repainted while it blinks.
const pulseFrame = 40 * time.Millisecond

// pulseLevel is the dot's brightness at a given moment, between 0 (its state
// colour) and 1 (blue). It is a cosine so the ends meet without a jump.
func pulseLevel(now time.Time) float32 {
	phase := float64(now.UnixNano()%int64(pulsePeriod)) / float64(pulsePeriod)
	return float32((math.Cos(phase*2*math.Pi) + 1) / 2)
}

// mixColor blends a towards b by k, ignoring alpha, which tab dots always use
// fully.
func mixColor(a, b ui.Color, k float32) ui.Color {
	return ui.RGBA(
		uint8(float32(a.R)*(1-k)+float32(b.R)*k),
		uint8(float32(a.G)*(1-k)+float32(b.G)*k),
		uint8(float32(a.B)*(1-k)+float32(b.B)*k),
		1,
	)
}

// pulsing reports whether any tab currently flickers, which is when the view
// has to keep frames coming.
func (a *App) pulsing() bool {
	for _, t := range a.tabs {
		if t.Inactive.Load() && a.current() != t {
			return true
		}
	}
	return false
}

// animate asks for the next frame while a dot is blinking, and does not when
// the window has nothing moving.
func (a *App) animate(c *ui.Context) {
	if a.pulsing() {
		c.After(pulseFrame)
	}
}
