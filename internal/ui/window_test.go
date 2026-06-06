package ui

import (
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/io/key"

	"instantcapture/internal/config"
	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/workflow"
)

type uiFakeCapturer struct {
	displays []image.Rectangle
	img      image.Image
}

func TestWindowUICanvasViewUndoRedo(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	u.canvasPan = image.Pt(12, -8)
	u.canvasSourceSize = image.Pt(800, 600)
	after := u.captureCanvasView()
	u.recordCanvasViewEdit(before, after)

	if !u.undoLastEdit() {
		t.Fatal("expected zoom undo")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("unexpected undo view: %#v", got)
	}
	if !u.redoLastEdit() {
		t.Fatal("expected zoom redo")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("unexpected redo view: %#v", got)
	}
}

func TestWindowUICanvasViewEditClearsRedo(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	first := u.captureCanvasView()
	u.recordCanvasViewEdit(before, first)
	if !u.undoLastEdit() {
		t.Fatal("expected undo")
	}
	u.canvasZoom = 3
	second := u.captureCanvasView()
	u.recordCanvasViewEdit(before, second)
	if u.redoLastEdit() {
		t.Fatal("redo should be cleared after a new zoom edit")
	}
}

func TestWindowUIControllerUndoRestoresCanvasView(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 200, 120)},
		img:      image.NewRGBA(image.Rect(0, 0, 200, 120)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 100, 80)); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{controller: controller, canvasZoom: 2, canvasPan: image.Pt(20, -10), canvasSourceSize: image.Pt(100, 80)}
	before := u.captureCanvasView()
	if err := controller.CropCapturedImage(image.Rect(10, 5, 80, 60)); err != nil {
		t.Fatal(err)
	}
	u.resetCanvasView()
	u.recordControllerEdit(before, u.captureCanvasView())

	if !u.undoLastEdit() {
		t.Fatal("expected crop undo")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("unexpected restored view: %#v", got)
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 100, 80) {
		t.Fatalf("unexpected restored image bounds: %v", got)
	}
}

func TestWindowUIToastDetailPersistsLongEnough(t *testing.T) {
	u := &windowUI{}
	u.showToastDetail("Saved PNG", "file.png")

	if u.toastTitle != "Saved PNG" {
		t.Fatalf("unexpected toast title: %q", u.toastTitle)
	}
	if u.toastMessage != "file.png" {
		t.Fatalf("unexpected toast message: %q", u.toastMessage)
	}
	if time.Until(u.toastUntil) < 4*time.Second {
		t.Fatalf("toast duration too short: %s", time.Until(u.toastUntil))
	}
}

func TestScrollCaptureModeIsValidAndCycled(t *testing.T) {
	if !validCaptureMode("Scroll") {
		t.Fatal("Scroll should be a valid capture mode")
	}
	if got := nextCaptureMode("Window"); got != "Scroll" {
		t.Fatalf("next mode after Window = %q, want Scroll", got)
	}
	if got := nextCaptureMode("Scroll"); got != "Freeform" {
		t.Fatalf("next mode after Scroll = %q, want Freeform", got)
	}
}

func TestScrollExternalRectUsesCapturedImageSize(t *testing.T) {
	target := platformwindows.WindowTarget{Rect: image.Rect(100, 200, 500, 600)}
	img := image.NewRGBA(image.Rect(0, 0, 400, 1200))
	got := scrollExternalRect(target, img)
	want := image.Rect(100, 200, 500, 1400)
	if got != want {
		t.Fatalf("scroll rect = %v, want %v", got, want)
	}
}

func TestShortcutKeysUndoRedoCanvasView(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	after := u.captureCanvasView()
	u.recordCanvasViewEdit(before, after)

	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+Z to be handled")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("Ctrl+Z did not undo: %#v", got)
	}
	if !u.handleShortcutKey(key.Event{Name: "Y", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+Y to be handled")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("Ctrl+Y did not redo: %#v", got)
	}
	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected second Ctrl+Z to be handled")
	}
	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut | key.ModShift, State: key.Press}) {
		t.Fatal("expected Ctrl+Shift+Z to be handled")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("Ctrl+Shift+Z did not redo: %#v", got)
	}
}

func TestShortcutEscapeCancelsGestures(t *testing.T) {
	u := &windowUI{dragging: true, cropDragging: true}
	if !u.handleShortcutKey(key.Event{Name: key.NameEscape, State: key.Press}) {
		t.Fatal("expected Escape to be handled")
	}
	if u.dragging || u.cropDragging {
		t.Fatal("Escape did not cancel gestures")
	}
}

func TestToastSavePathIncludesFileAndDirectory(t *testing.T) {
	got := toastSavePath(`C:\Users\someone\Pictures\Screenshots\capture.png`)
	if !strings.Contains(got, "capture.png") || !strings.Contains(got, "Screenshots") {
		t.Fatalf("unexpected toast path: %q", got)
	}
}

func TestResolveSaveDirUsesCustomOrFallback(t *testing.T) {
	fallback := `C:\Users\someone\Pictures\Screenshots`
	custom := `C:\Captures`
	if got := resolveSaveDir(config.Config{SaveDir: custom}, fallback); got != custom {
		t.Fatalf("custom dir = %q, want %q", got, custom)
	}
	if got := resolveSaveDir(config.Config{}, fallback); got != fallback {
		t.Fatalf("fallback dir = %q, want %q", got, fallback)
	}
}

func (f *uiFakeCapturer) Capture(rect image.Rectangle) (image.Image, error) {
	return f.img, nil
}

func (f *uiFakeCapturer) Displays() []image.Rectangle {
	return f.displays
}
