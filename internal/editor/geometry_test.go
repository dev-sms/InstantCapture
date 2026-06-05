package editor

import "testing"

func TestRectNormalize(t *testing.T) {
	rect := NewRect(Point{X: 20, Y: 30}, Point{X: 5, Y: 10})

	if rect.Min != (Point{X: 5, Y: 10}) || rect.Max != (Point{X: 20, Y: 30}) {
		t.Fatalf("unexpected normalized rect: %#v", rect)
	}

	if rect.Width() != 15 || rect.Height() != 20 {
		t.Fatalf("unexpected size: %dx%d", rect.Width(), rect.Height())
	}
}

func TestRectEmpty(t *testing.T) {
	if !NewRect(Point{X: 1, Y: 1}, Point{X: 1, Y: 10}).Empty() {
		t.Fatal("zero width rect should be empty")
	}
}
