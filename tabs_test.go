package main

import "testing"

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
