package main

import (
	"testing"

	"github.com/egoist/mygo/ui"
)

func TestMoveTabReordersAndKeepsTheSameTabActive(t *testing.T) {
	app := &App{
		tabs: []*Tab{
			{ID: "a", Title: "a"},
			{ID: "b", Title: "b"},
			{ID: "c", Title: "c"},
		},
		active: 1,
	}
	app.moveTab(1, 2)
	if got := []string{app.tabs[0].ID, app.tabs[1].ID, app.tabs[2].ID}; got[0] != "a" || got[1] != "c" || got[2] != "b" {
		t.Fatalf("after moving b right: %v", got)
	}
	if app.current() == nil || app.current().ID != "b" {
		t.Fatalf("active tab changed to %+v", app.current())
	}

	app.moveTab(2, 0)
	if got := []string{app.tabs[0].ID, app.tabs[1].ID, app.tabs[2].ID}; got[0] != "b" || got[1] != "a" || got[2] != "c" {
		t.Fatalf("after moving b to the front: %v", got)
	}
	if app.current() == nil || app.current().ID != "b" {
		t.Fatal("active tab was lost when the active tab moved")
	}
	if app.active != 0 {
		t.Fatalf("active index %d, want 0", app.active)
	}

	// Out-of-range moves are no-ops, not panics.
	app.moveTab(-1, 0)
	app.moveTab(0, 9)
	if len(app.tabs) != 3 || app.tabs[0].ID != "b" {
		t.Fatalf("an invalid move changed the order: %+v", app.tabs)
	}
}

func TestMoveTabStopsDragTracking(t *testing.T) {
	app := &App{tabs: []*Tab{{ID: "a"}, {ID: "b"}}, active: 0, dragFrom: 0}
	app.moveTab(0, 1)
	// moveTab itself does not own drag state; the view resets it after the
	// move. This test pins the invariant that the active tab follows.
	if app.current() == nil || app.current().ID != "a" {
		t.Fatal("the dragged tab stopped being active")
	}
	if app.active != 1 {
		t.Fatalf("active index %d, want 1", app.active)
	}
}

// The tab you are looking at has to look like the one you are looking at. The
// strip and the selected chip used to share one colour, so every tab looked
// identical and a click changed nothing visible — which also made a drag-reorder
// impossible to follow.
func TestTheSelectedTabLooksDifferentFromTheOthers(t *testing.T) {
	for _, th := range []*ui.Theme{ui.LightTheme(), ui.DarkTheme()} {
		activeBG, activeFG, activeBold := tabLook(th, true, false, false)
		plainBG, plainFG, plainBold := tabLook(th, false, false, false)

		if activeBG == th.Surface {
			t.Errorf("dark=%v: the selected tab is painted the strip's own colour", th.Dark)
		}
		if activeBG == plainBG {
			t.Errorf("dark=%v: the selected tab and the others share a background", th.Dark)
		}
		if activeFG == plainFG {
			t.Errorf("dark=%v: the selected tab's text is the same colour as the others'", th.Dark)
		}
		if !activeBold || plainBold {
			t.Errorf("dark=%v: bold = %v/%v, want the selected tab bold and the others not", th.Dark, activeBold, plainBold)
		}
		// Hovering an unselected tab must not make it look selected.
		hoverBG, _, _ := tabLook(th, false, false, true)
		if hoverBG == activeBG {
			t.Errorf("dark=%v: a hovered tab looks like the selected one", th.Dark)
		}
		// The tab being dragged is findable, and distinct from both.
		dragBG, _, _ := tabLook(th, false, true, false)
		if dragBG == activeBG || dragBG == hoverBG || dragBG == plainBG {
			t.Errorf("dark=%v: the dragged tab is not distinguishable (%v)", th.Dark, dragBG)
		}
	}
}
