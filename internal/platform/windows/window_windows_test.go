//go:build windows

package windows

import (
	"image"
	"os"
	"testing"
)

func TestVisibleFragmentsExcludeFullyCoveredWindow(t *testing.T) {
	got := visibleFragments(image.Rect(0, 0, 100, 100), []image.Rectangle{image.Rect(0, 0, 100, 100)})
	if len(got) != 0 {
		t.Fatalf("expected no visible fragments, got %#v", got)
	}
}

func TestVisibleFragmentsKeepPartialWindow(t *testing.T) {
	got := visibleFragments(image.Rect(0, 0, 100, 100), []image.Rectangle{image.Rect(25, 0, 100, 100)})
	want := []image.Rectangle{image.Rect(0, 0, 25, 100)}
	if len(got) != len(want) || got[0] != want[0] {
		t.Fatalf("got %#v, want %#v", got, want)
	}
}

func TestVisibleWindowTargetsDoesNotCrash(t *testing.T) {
	_ = VisibleWindowTargets("InstantCapture")
}

func TestCaptureFirstVisibleWindowTargetSmoke(t *testing.T) {
	if os.Getenv("INSTANTCAPTURE_WGC_SMOKE") != "1" {
		t.Skip("set INSTANTCAPTURE_WGC_SMOKE=1 to invoke the WGC helper")
	}
	targets := VisibleWindowTargets("InstantCapture")
	if len(targets) == 0 {
		t.Fatal("no visible window targets")
	}
	img, err := CaptureWindowTarget(targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if img == nil || img.Bounds().Empty() {
		t.Fatalf("empty captured image: %#v", img)
	}
}
