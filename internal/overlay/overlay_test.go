package overlay

import (
	"image"
	"slices"
	"testing"

	"gioui.org/io/pointer"
)

func TestSecondaryPointerPressCancelsSelection(t *testing.T) {
	if !isCancelPointerPress(pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonSecondary}) {
		t.Fatal("secondary press should cancel capture")
	}
	if isCancelPointerPress(pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary}) {
		t.Fatal("primary press should continue capture")
	}
	if isCancelPointerPress(pointer.Event{Kind: pointer.Release, Buttons: pointer.ButtonSecondary}) {
		t.Fatal("secondary release should not be classified as the cancel press")
	}
}

func TestDimRectsLeaveSelectionUndimmed(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 80)
	selection := image.Rect(20, 10, 70, 60)
	want := []image.Rectangle{
		image.Rect(0, 0, 100, 10),
		image.Rect(0, 60, 100, 80),
		image.Rect(0, 10, 20, 60),
		image.Rect(70, 10, 100, 60),
	}
	if got := dimRects(bounds, selection, true); !slices.Equal(got, want) {
		t.Fatalf("dim rects = %v, want %v", got, want)
	}
	if got := dimRects(bounds, selection, false); !slices.Equal(got, []image.Rectangle{bounds}) {
		t.Fatalf("inactive dim rects = %v", got)
	}
}
