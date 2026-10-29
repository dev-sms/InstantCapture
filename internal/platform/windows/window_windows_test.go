//go:build windows

package windows

import (
	"image"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestVisibleFragmentsExcludeFullyCoveredWindow(t *testing.T) {
	got := visibleFragments(image.Rect(0, 0, 100, 100), []image.Rectangle{image.Rect(0, 0, 100, 100)})
	if len(got) != 0 {
		t.Fatalf("expected no visible fragments, got %#v", got)
	}
}

func TestSaveDialogDefaultExtensionFollowsInitialPath(t *testing.T) {
	for path, want := range map[string]string{
		"capture.png":  "png",
		"capture.jpg":  "jpg",
		"capture.jpeg": "jpg",
		"capture.bmp":  "bmp",
		"capture.gif":  "gif",
	} {
		if got := saveDialogDefaultExtension(path); got != want {
			t.Fatalf("default extension for %q = %q, want %q", path, got, want)
		}
	}
}

func TestFolderDialogHRESULTAndOptions(t *testing.T) {
	if failedHRESULT(0) || failedHRESULT(1) {
		t.Fatal("successful HRESULT was treated as failure")
	}
	if !failedHRESULT(hresultCancelled) {
		t.Fatal("cancel HRESULT should be a failed HRESULT before special handling")
	}
	for _, required := range []uint32{0x20, 0x40, 0x800, 0x8} {
		if fileOpenOptions&required == 0 {
			t.Fatalf("folder dialog options missing 0x%X", required)
		}
	}
}

func TestFolderDialogVTableIndexes(t *testing.T) {
	if fileDialogShow != 3 || fileDialogSetOptions != 9 || fileDialogGetOptions != 10 || fileDialogSetFolder != 12 || fileDialogSetTitle != 17 || fileDialogGetResult != 20 {
		t.Fatal("IFileDialog vtable indexes changed")
	}
	if shellItemGetDisplayName != 5 {
		t.Fatal("IShellItem vtable index changed")
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

func TestScrollRangeCaptureFramesDoesNotScrollZeroStepRange(t *testing.T) {
	if got := scrollRangeCaptureFrames(0, image.Rect(10, 20, 90, 80), 100); got != 1 {
		t.Fatalf("zero-step frame count = %d, want 1", got)
	}
}

func TestScrollRangeCaptureFramesAllowsMeasuredReplaySlack(t *testing.T) {
	if got := scrollRangeCaptureFrames(2, image.Rect(0, 10, 80, 430), 200); got != 6 {
		t.Fatalf("frame count = %d, want 6", got)
	}
}

func TestScrollRangeCaptureFramesSupportsSixtyFourSteps(t *testing.T) {
	if got := scrollRangeCaptureFrames(64, image.Rect(0, 0, 80, 20_000), 800); got != 68 {
		t.Fatalf("frame count = %d, want 68", got)
	}
	if got := cappedScrollRangeSteps(64); got != 64 {
		t.Fatalf("capped steps = %d, want 64", got)
	}
}

func TestCaptureScrollRangeRejectsMoreThanSixtyFourSteps(t *testing.T) {
	_, err := CaptureScrollRange(WindowTarget{Handle: 1, Rect: image.Rect(0, 0, 100, 100)}, ScrollCaptureRange{
		Crop:        image.Rect(0, 0, 100, 1000),
		ScrollSteps: 65,
	})
	if err == nil || !strings.Contains(err.Error(), "exceeds 64 wheel steps") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestScrollRangeCaptureTimeoutCoversLongReplay(t *testing.T) {
	if got := scrollRangeCaptureTimeout(32, 31); got <= 12*time.Second {
		t.Fatalf("long replay timeout = %v, want more than 12s", got)
	}
}

func TestScrollRangeWheelPointUsesSelectedViewCenter(t *testing.T) {
	target := WindowTarget{Rect: image.Rect(100, 200, 500, 600)}
	got := scrollRangeWheelPoint(target, ScrollCaptureRange{View: image.Rect(40, 80, 360, 320)})
	if got != image.Pt(300, 400) {
		t.Fatalf("selected view center = %v, want (300,400)", got)
	}
}

func TestScrollRangeWheelPointUsesSavedUIAOrigin(t *testing.T) {
	target := WindowTarget{Rect: image.Rect(100, 200, 500, 600)}
	got := scrollRangeWheelPoint(target, ScrollCaptureRange{
		InitialWheelUnits:  240,
		InitialScrollPoint: image.Pt(80, 120),
	})
	if got != image.Pt(180, 320) {
		t.Fatalf("saved UIA wheel point = %v, want (180,320)", got)
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
