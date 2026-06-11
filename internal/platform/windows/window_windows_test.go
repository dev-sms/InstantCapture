//go:build windows

package windows

import (
	"image"
	"os"
	"path/filepath"
	"strings"
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

func TestCaptureFirstScrollableWindowTargetSmoke(t *testing.T) {
	if os.Getenv("INSTANTCAPTURE_SCROLL_SMOKE") != "1" {
		t.Skip("set INSTANTCAPTURE_SCROLL_SMOKE=1 to invoke the scroll helper")
	}
	targets := VisibleWindowTargets("InstantCapture")
	if len(targets) == 0 {
		t.Fatal("no visible window targets")
	}
	img, err := CaptureScrollTarget(targets[0])
	if err != nil {
		t.Fatal(err)
	}
	if img == nil || img.Bounds().Empty() {
		t.Fatalf("empty captured image: %#v", img)
	}
}

func TestCaptureScrollRangeRejectsEmptyCrop(t *testing.T) {
	img, err := CaptureScrollRange(WindowTarget{
		Handle: 1,
		Rect:   image.Rect(0, 0, 100, 100),
	}, ScrollCaptureRange{})
	if err == nil {
		t.Fatal("expected empty crop error")
	}
	if img != nil {
		t.Fatalf("expected nil image, got %#v", img)
	}
	if !strings.Contains(err.Error(), "scroll range crop is empty") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFindWindowCaptureHelperCachesEnvPath(t *testing.T) {
	resetWindowCaptureHelperCacheForTest()
	defer resetWindowCaptureHelperCacheForTest()

	helper := filepath.Join(t.TempDir(), "helper.exe")
	if err := os.WriteFile(helper, []byte("helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("INSTANTCAPTURE_WINDOW_HELPER", helper)

	got, err := findWindowCaptureHelper()
	if err != nil {
		t.Fatal(err)
	}
	if got != helper {
		t.Fatalf("got %q, want %q", got, helper)
	}
	if err := os.Remove(helper); err != nil {
		t.Fatal(err)
	}
	got, err = findWindowCaptureHelper()
	if err != nil {
		t.Fatal(err)
	}
	if got != helper {
		t.Fatalf("cached path = %q, want %q", got, helper)
	}

	resetWindowCaptureHelperCacheForTest()
	if _, err := findWindowCaptureHelper(); err == nil {
		t.Fatal("expected missing env helper to fail after cache reset")
	}
}

func TestFindDevelopmentRootFromNestedDirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "tools", "window-capture-helper"), 0o755); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "platform", "windows")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	if got := findDevelopmentRoot(nested); got != root {
		t.Fatalf("got %q, want %q", got, root)
	}
}
