package workflow

import (
	"image"
	"image/color"
	"os"
	"path/filepath"
	"testing"
	"time"

	"instantcapture/internal/editor"
)

type fakeCapturer struct {
	displays []image.Rectangle
	img      image.Image
	gotRect  image.Rectangle
}

func TestControllerPreservesArrowDirection(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 100, 100)},
		img:      image.NewRGBA(image.Rect(0, 0, 100, 100)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 100, 100)); err != nil {
		t.Fatal(err)
	}

	arrow := editor.Rect{Min: editor.Point{X: 90, Y: 80}, Max: editor.Point{X: 10, Y: 20}}
	if err := controller.AddAnnotation(editor.ToolArrow, nil, arrow); err != nil {
		t.Fatal(err)
	}

	commands := controller.Editor.RenderCommands()
	if commands[0].Rect != arrow {
		t.Fatalf("arrow direction was not preserved: %#v", commands[0].Rect)
	}
}

func TestNextSavePathAvoidsClobber(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 6, 5, 12, 30, 45, 0, time.Local)
	existing := filepath.Join(dir, "InstantCapture_20260605_123045.png")
	if err := os.WriteFile(existing, []byte("existing"), 0o644); err != nil {
		t.Fatal(err)
	}

	got := nextSavePath(dir, now)
	want := filepath.Join(dir, "InstantCapture_20260605_123045_01.png")
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestControllerCommitScreenSelection(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(-100, 0, 220, 200)},
		img:      image.NewRGBA(image.Rect(0, 0, 320, 200)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitScreenSelection(image.Rect(-90, 20, 10, 80)); err != nil {
		t.Fatal(err)
	}
	if controller.CapturedImage.Bounds() != image.Rect(0, 0, 100, 60) {
		t.Fatalf("unexpected captured bounds: %v", controller.CapturedImage.Bounds())
	}
}

func TestControllerCommitFreeformSelectionAppliesAlphaMask(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 100, 100)},
		img:      image.NewRGBA(image.Rect(0, 0, 100, 100)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	poly := []image.Point{image.Pt(10, 10), image.Pt(80, 10), image.Pt(10, 80)}
	if err := controller.CommitFreeformSelection(image.Rect(10, 10, 80, 80), poly); err != nil {
		t.Fatal(err)
	}
	rgba := controller.CapturedImage.(*image.RGBA)
	_, _, _, a := rgba.At(65, 65).RGBA()
	if a != 0 {
		t.Fatal("expected outside polygon pixel to be transparent")
	}
}

func TestControllerCommitExternalImage(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 100, 100)},
		img:      image.NewRGBA(image.Rect(0, 0, 100, 100)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	external := image.NewRGBA(image.Rect(0, 0, 40, 30))
	if err := controller.CommitExternalImage(image.Rect(10, 20, 50, 50), external); err != nil {
		t.Fatal(err)
	}
	if controller.CapturedImage.Bounds() != image.Rect(0, 0, 40, 30) {
		t.Fatalf("unexpected bounds: %v", controller.CapturedImage.Bounds())
	}
	if controller.SelectedScreen != image.Rect(10, 20, 50, 50) {
		t.Fatalf("unexpected selected screen: %v", controller.SelectedScreen)
	}
}

func TestControllerCropCapturedImageShiftsAnnotations(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 200, 120)},
		img:      image.NewRGBA(image.Rect(0, 0, 200, 120)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(20, 10, 140, 90)); err != nil {
		t.Fatal(err)
	}
	if err := controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(editor.Point{X: 30, Y: 20}, editor.Point{X: 80, Y: 60})); err != nil {
		t.Fatal(err)
	}

	if err := controller.CropCapturedImage(image.Rect(10, 5, 90, 55)); err != nil {
		t.Fatal(err)
	}

	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 80, 50) {
		t.Fatalf("unexpected cropped bounds: %v", got)
	}
	if got := controller.SelectedImageRect; got != image.Rect(30, 15, 110, 65) {
		t.Fatalf("unexpected selected image rect: %v", got)
	}
	if got := controller.SelectedScreen; got != image.Rect(30, 15, 110, 65) {
		t.Fatalf("unexpected selected screen rect: %v", got)
	}
	commands := controller.Editor.RenderCommands()
	wantRect := editor.NewRect(editor.Point{X: 20, Y: 15}, editor.Point{X: 70, Y: 55})
	if commands[0].Rect != wantRect {
		t.Fatalf("annotation was not shifted: got %#v want %#v", commands[0].Rect, wantRect)
	}
}

func TestControllerUndoRedoAnnotationAndCrop(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 200, 120)},
		img:      image.NewRGBA(image.Rect(0, 0, 200, 120)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(20, 10, 140, 90)); err != nil {
		t.Fatal(err)
	}
	if err := controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(editor.Point{X: 30, Y: 20}, editor.Point{X: 80, Y: 60})); err != nil {
		t.Fatal(err)
	}
	if len(controller.Editor.Annotations) != 1 {
		t.Fatalf("expected annotation to be added")
	}

	if !controller.UndoEdit() {
		t.Fatal("expected annotation undo")
	}
	if len(controller.Editor.Annotations) != 0 {
		t.Fatalf("expected annotation undo, got %d annotations", len(controller.Editor.Annotations))
	}
	if !controller.RedoEdit() {
		t.Fatal("expected annotation redo")
	}
	if len(controller.Editor.Annotations) != 1 {
		t.Fatalf("expected annotation redo, got %d annotations", len(controller.Editor.Annotations))
	}

	if err := controller.CropCapturedImage(image.Rect(10, 5, 90, 55)); err != nil {
		t.Fatal(err)
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 80, 50) {
		t.Fatalf("unexpected cropped bounds: %v", got)
	}
	if !controller.UndoEdit() {
		t.Fatal("expected crop undo")
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 120, 80) {
		t.Fatalf("unexpected undo bounds: %v", got)
	}
	if got := controller.SelectedScreen; got != image.Rect(20, 10, 140, 90) {
		t.Fatalf("unexpected undo selected screen: %v", got)
	}
	if got := controller.Editor.Annotations[0].Rect; got != editor.NewRect(editor.Point{X: 30, Y: 20}, editor.Point{X: 80, Y: 60}) {
		t.Fatalf("unexpected undo annotation rect: %#v", got)
	}
	if !controller.RedoEdit() {
		t.Fatal("expected crop redo")
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 80, 50) {
		t.Fatalf("unexpected redo bounds: %v", got)
	}
	if got := controller.SelectedScreen; got != image.Rect(30, 15, 110, 65) {
		t.Fatalf("unexpected redo selected screen: %v", got)
	}
	if got := controller.Editor.Annotations[0].Rect; got != editor.NewRect(editor.Point{X: 20, Y: 15}, editor.Point{X: 70, Y: 55}) {
		t.Fatalf("unexpected redo annotation rect: %#v", got)
	}
}

func (f *fakeCapturer) Capture(rect image.Rectangle) (image.Image, error) {
	f.gotRect = rect
	return f.img, nil
}

func (f *fakeCapturer) Displays() []image.Rectangle {
	return f.displays
}

func TestControllerCaptureSelectionAnnotationFlow(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 320, 200))
	base.Set(30, 20, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	capturer := &fakeCapturer{
		displays: []image.Rectangle{image.Rect(-100, 0, 220, 200)},
		img:      base,
	}

	controller := NewController(capturer)
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if capturer.gotRect != image.Rect(-100, 0, 220, 200) {
		t.Fatalf("unexpected capture rect: %v", capturer.gotRect)
	}

	if err := controller.CommitImageSelection(image.Rect(10, 20, 110, 80)); err != nil {
		t.Fatal(err)
	}
	if controller.Mode != ModeEditing {
		t.Fatalf("unexpected mode: %s", controller.Mode)
	}
	if controller.CapturedImage.Bounds() != image.Rect(0, 0, 100, 60) {
		t.Fatalf("unexpected captured bounds: %v", controller.CapturedImage.Bounds())
	}
	if controller.SelectedScreen != image.Rect(-90, 20, 10, 80) {
		t.Fatalf("unexpected screen selection: %v", controller.SelectedScreen)
	}

	err := controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(editor.Point{X: 5, Y: 5}, editor.Point{X: 50, Y: 40}))
	if err != nil {
		t.Fatal(err)
	}

	rendered, err := controller.RenderedImage()
	if err != nil {
		t.Fatal(err)
	}
	if rendered.Bounds() != image.Rect(0, 0, 100, 60) {
		t.Fatalf("unexpected rendered bounds: %v", rendered.Bounds())
	}
}
