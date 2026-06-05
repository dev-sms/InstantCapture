package overlay

import (
	"image"
	"testing"

	"instantcapture/internal/coords"
)

func TestSelectionStateMapsViewDragToSourceRect(t *testing.T) {
	viewport := coords.FitContain(image.Rect(0, 0, 100, 50), image.Rect(0, 0, 200, 120))
	state := NewSelectionState(viewport)

	if !state.Begin(image.Pt(50, 35)) {
		t.Fatal("expected begin inside viewport")
	}
	if !state.Update(image.Pt(150, 85)) {
		t.Fatal("expected update inside viewport")
	}

	rect, ok := state.Commit()
	if !ok {
		t.Fatal("expected committed selection")
	}
	want := image.Rect(25, 12, 75, 37)
	if rect != want {
		t.Fatalf("got %v, want %v", rect, want)
	}
}

func TestSelectionStateRejectsOutsideStart(t *testing.T) {
	viewport := coords.FitContain(image.Rect(0, 0, 100, 50), image.Rect(0, 0, 200, 100))
	state := NewSelectionState(viewport)

	if state.Begin(image.Pt(250, 10)) {
		t.Fatal("outside start should be rejected")
	}
}

func TestSelectionStateClampsDragOutsideViewport(t *testing.T) {
	viewport := coords.FitContain(image.Rect(0, 0, 100, 50), image.Rect(0, 0, 200, 100))
	state := NewSelectionState(viewport)

	if !state.Begin(image.Pt(50, 25)) {
		t.Fatal("expected begin inside viewport")
	}
	if !state.Update(image.Pt(300, 160)) {
		t.Fatal("outside drag should clamp and update")
	}

	rect, ok := state.Commit()
	if !ok {
		t.Fatal("expected committed selection")
	}
	want := image.Rect(25, 12, 100, 50)
	if rect != want {
		t.Fatalf("got %v, want %v", rect, want)
	}
}
