package overlay

import (
	"image"
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
}

func TestNormalizeOptionsUsesVirtualScreen(t *testing.T) {
	virtualScreen := image.Rect(-1280, 0, 1920, 1080)

	got := NormalizeOptions(image.Rect(0, 0, 3200, 1080), Options{VirtualScreen: virtualScreen, DimAlpha: 120})
	if got.VirtualScreen != virtualScreen {
		t.Fatalf("got %v, want %v", got.VirtualScreen, virtualScreen)
	}
	if got.TopMostHint {
		t.Fatal("explicit zero-value topmost hint should be preserved")
	}
	if got.DimAlpha != 120 {
		t.Fatalf("got dim alpha %d, want 120", got.DimAlpha)
	}
}
