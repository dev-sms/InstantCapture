package coords

import (
	"image"
	"testing"
)

func TestFitContainCentersSource(t *testing.T) {
	viewport := FitContain(image.Rect(0, 0, 1920, 1080), image.Rect(0, 0, 1000, 600))

	if viewport.View.Dx() != 1000 || viewport.View.Dy() != 563 {
		t.Fatalf("unexpected fitted size: %v", viewport.View.Size())
	}
	if viewport.View.Min.Y != 18 {
		t.Fatalf("expected vertical centering, got %v", viewport.View)
	}
}

func TestViewportMapsBetweenViewAndSource(t *testing.T) {
	viewport := FitContain(image.Rect(-1920, 0, 1920, 1080), image.Rect(0, 0, 1920, 540))

	p, ok := viewport.ViewToSource(image.Pt(960, 270))
	if !ok {
		t.Fatal("expected point inside viewport")
	}
	if p != (image.Point{X: 0, Y: 540}) {
		t.Fatalf("unexpected source point: %v", p)
	}

	view := viewport.SourceToView(image.Pt(0, 540))
	if view != (image.Point{X: 960, Y: 270}) {
		t.Fatalf("unexpected view point: %v", view)
	}
}

func TestViewportMapsMaxEdgeForRectSelection(t *testing.T) {
	viewport := FitContain(image.Rect(0, 0, 100, 50), image.Rect(0, 0, 200, 100))

	got := viewport.ViewRectToSource(image.Rect(0, 0, 200, 100))
	want := image.Rect(0, 0, 100, 50)
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
