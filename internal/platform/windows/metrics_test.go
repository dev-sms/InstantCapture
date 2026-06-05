package windows

import (
	"image"
	"testing"
)

func TestBuildVirtualScreenRect(t *testing.T) {
	got := BuildVirtualScreenRect(-1920, -120, 3840, 1200)
	want := image.Rect(-1920, -120, 1920, 1080)
	if got != want {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBuildVirtualScreenRectRejectsInvalidSize(t *testing.T) {
	if got := BuildVirtualScreenRect(0, 0, 0, 100); !got.Empty() {
		t.Fatalf("expected empty rect, got %v", got)
	}
}

func TestPreferNativeVirtualScreen(t *testing.T) {
	fallback := image.Rect(0, 0, 1920, 1080)
	native := image.Rect(-1280, 0, 1920, 1080)

	if got := PreferNativeVirtualScreen(native, fallback); got != native {
		t.Fatalf("got %v, want native %v", got, native)
	}
	if got := PreferNativeVirtualScreen(image.Rectangle{}, fallback); got != fallback {
		t.Fatalf("got %v, want fallback %v", got, fallback)
	}
}

func TestPreferMatchingNativeVirtualScreen(t *testing.T) {
	fallback := image.Rect(-100, 0, 1820, 1080)
	native := image.Rect(-100, 0, 1820, 1080)
	imageBounds := image.Rect(0, 0, 1920, 1080)

	if got := PreferMatchingNativeVirtualScreen(native, fallback, imageBounds); got != native {
		t.Fatalf("got %v, want native %v", got, native)
	}

	mismatchedNative := image.Rect(-100, 0, 1700, 1000)
	if got := PreferMatchingNativeVirtualScreen(mismatchedNative, fallback, imageBounds); got != fallback {
		t.Fatalf("got %v, want fallback %v", got, fallback)
	}
}
