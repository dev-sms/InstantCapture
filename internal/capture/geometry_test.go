package capture

import (
	"image"
	"testing"
)

func TestUnionDisplaysSupportsNegativeMonitorOrigins(t *testing.T) {
	displays := []image.Rectangle{
		image.Rect(0, 0, 1920, 1080),
		image.Rect(-1280, 120, 0, 840),
	}

	got := UnionDisplays(displays)
	want := image.Rect(-1280, 0, 1920, 1080)
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}
