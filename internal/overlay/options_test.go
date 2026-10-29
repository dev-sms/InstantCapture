package overlay

import (
	"image"
	"image/color"
	"testing"
)

func TestNormalizeOptionsDefaultsToImageBounds(t *testing.T) {
	imageBounds := image.Rect(0, 0, 1920, 1080)

	got := NormalizeOptions(imageBounds)
	if got.VirtualScreen != imageBounds {
		t.Fatalf("got %v, want %v", got.VirtualScreen, imageBounds)
	}
	if !got.TopMostHint {
		t.Fatal("topmost hint should default to true")
	}
	if got.DimAlpha == 0 {
		t.Fatal("dim alpha should default to non-zero")
	}
	if got.SelectionColor != (color.NRGBA{R: 255, G: 64, B: 64, A: 255}) {
		t.Fatalf("unexpected default selection color: %#v", got.SelectionColor)
	}
}

func TestNormalizeOptionsUsesSelectionAppearance(t *testing.T) {
	want := color.NRGBA{R: 48, G: 209, B: 88, A: 255}
	got := NormalizeOptions(image.Rect(0, 0, 100, 80), Options{SelectionColor: want, Hint: "좌표 기억"})
	if got.SelectionColor != want || got.Hint != "좌표 기억" {
		t.Fatalf("selection appearance = %#v %q", got.SelectionColor, got.Hint)
	}
}

func TestNormalizeOptionsUsesVirtualScreen(t *testing.T) {
	virtualScreen := image.Rect(-1280, 0, 1920, 1080)
	displays := []image.Rectangle{image.Rect(-1280, 0, 0, 1024), image.Rect(0, 0, 1920, 1080)}

	got := NormalizeOptions(image.Rect(0, 0, 3200, 1080), Options{VirtualScreen: virtualScreen, Displays: displays, DimAlpha: 120})
	if got.VirtualScreen != virtualScreen {
		t.Fatalf("got %v, want %v", got.VirtualScreen, virtualScreen)
	}
	if got.TopMostHint {
		t.Fatal("explicit zero-value topmost hint should be preserved")
	}
	if got.DimAlpha != 120 {
		t.Fatalf("got dim alpha %d, want 120", got.DimAlpha)
	}
	if len(got.Displays) != 2 || got.Displays[0] != displays[0] || got.Displays[1] != displays[1] {
		t.Fatalf("got displays %v, want %v", got.Displays, displays)
	}
	displays[0] = image.Rectangle{}
	if got.Displays[0].Empty() {
		t.Fatal("normalized options share the caller's display slice")
	}
}
