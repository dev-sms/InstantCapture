package workflow

import (
	"image"
	"image/color"
	"image/png"
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

func TestResetEditingSessionClearsCurrentImage(t *testing.T) {
	controller := NewController(nil)
	if err := controller.CommitImportedImage(image.NewRGBA(image.Rect(0, 0, 20, 10))); err != nil {
		t.Fatal(err)
	}
	controller.ResetEditingSession()
	if controller.Mode != ModeIdle || controller.CapturedImage != nil || controller.FullScreenImage != nil {
		t.Fatalf("mode=%s captured=%v fullscreen=%v", controller.Mode, controller.CapturedImage, controller.FullScreenImage)
	}
	if controller.Editor == nil {
		t.Fatal("editor was not reset")
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

func TestControllerSavePNGCreatesCustomDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "custom", "captures")
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 40, 30)},
		img:      image.NewRGBA(image.Rect(0, 0, 40, 30)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 40, 30)); err != nil {
		t.Fatal(err)
	}

	path, err := controller.SavePNG(dir)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(path) != dir {
		t.Fatalf("saved dir = %q, want %q", filepath.Dir(path), dir)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("saved file missing: %v", err)
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
	if controller.FullScreenImage != nil {
		t.Fatal("expected fullscreen image to be released after selection commit")
	}
}

func TestControllerFullScreenSelectionReusesCapturedBuffer(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 100, 60)},
		img:      image.NewRGBA(image.Rect(0, 0, 100, 60)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	fullScreen := controller.FullScreenImage
	if err := controller.CommitFullScreenSelection(); err != nil {
		t.Fatal(err)
	}
	if controller.CapturedImage != fullScreen {
		t.Fatal("full-screen selection should transfer the existing image buffer")
	}
	if controller.FullScreenImage != nil {
		t.Fatal("full-screen image reference should be released")
	}
}

func TestControllerCommitImportedImageStartsEditing(t *testing.T) {
	controller := NewController(nil)
	img := image.NewRGBA(image.Rect(10, 20, 90, 70))

	if err := controller.CommitImportedImage(img); err != nil {
		t.Fatal(err)
	}

	if controller.Mode != ModeEditing {
		t.Fatalf("mode = %s, want %s", controller.Mode, ModeEditing)
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 80, 50) {
		t.Fatalf("captured bounds = %v, want 80x50", got)
	}
	if got := controller.VisibleCropRect(); got != image.Rect(0, 0, 80, 50) {
		t.Fatalf("crop = %v, want full imported image", got)
	}
}

func TestPreviewImageReusesCommittedRenderUntilEdit(t *testing.T) {
	controller := NewController(nil)
	base := image.NewRGBA(image.Rect(0, 0, 80, 50))
	if err := controller.CommitImportedImage(base); err != nil {
		t.Fatal(err)
	}

	first, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	second, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	if first != base || second != first {
		t.Fatal("unchanged preview should reuse the captured image")
	}

	if err := controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(editor.Point{X: 5, Y: 5}, editor.Point{X: 30, Y: 25})); err != nil {
		t.Fatal(err)
	}
	third, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	fourth, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	if third == first || fourth != third {
		t.Fatal("edited preview should render once and then be reused")
	}

	if !controller.UndoEdit() {
		t.Fatal("expected undo")
	}
	afterUndo, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	if afterUndo != base {
		t.Fatal("undo should invalidate the edited preview")
	}
}

func TestNormalizeImageCopiesRGBAWithLargerBackingStore(t *testing.T) {
	parent := image.NewRGBA(image.Rect(0, 0, 100, 100))
	subImage := parent.SubImage(image.Rect(0, 0, 20, 20)).(*image.RGBA)

	normalized := normalizeImage(subImage)

	if normalized == subImage {
		t.Fatal("subimage should not retain its larger backing store")
	}
	if got, want := len(normalized.Pix), 20*20*4; got != want {
		t.Fatalf("normalized pixel buffer = %d bytes, want %d", got, want)
	}
	tight := image.NewRGBA(image.Rect(0, 0, 20, 20))
	if normalizeImage(tight) != tight {
		t.Fatal("tightly packed owned RGBA should be reused")
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
	if controller.FullScreenImage == nil {
		t.Fatal("expected fullscreen image before external commit")
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
	if controller.FullScreenImage != nil {
		t.Fatal("expected fullscreen image to be released after external commit")
	}
}

func TestControllerRenderedImageAppliesOutputBorder(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 20, 20)},
		img:      image.NewRGBA(image.Rect(0, 0, 20, 20)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 10, 8)); err != nil {
		t.Fatal(err)
	}
	controller.SetOutputBorder(true, 3, [4]uint8{1, 2, 3, 255})

	img, err := controller.RenderedImage()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.Bounds(), image.Rect(0, 0, 16, 14); got != want {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Fatalf("border pixel = %#v", got)
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 10, 8) {
		t.Fatalf("source bounds changed to %v", got)
	}
}

func TestControllerSaveOriginalPNGExcludesOutputBorder(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 20, 20)},
		img:      image.NewUniform(color.NRGBA{R: 40, G: 50, B: 60, A: 255}),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(2, 3, 12, 11)); err != nil {
		t.Fatal(err)
	}
	controller.SetOutputBorder(true, 3, [4]uint8{1, 2, 3, 255})

	path, err := controller.SaveOriginalPNG(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := saved.Bounds(), image.Rect(0, 0, 10, 8); got != want {
		t.Fatalf("saved bounds = %v, want %v", got, want)
	}
	if got := color.NRGBAModel.Convert(saved.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 40, G: 50, B: 60, A: 255}) {
		t.Fatalf("saved source pixel = %#v", got)
	}
}

func TestControllerDiscardSelectionCaptureReleasesImageAndIdles(t *testing.T) {
	controller := NewController(&fakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 100, 100)},
		img:      image.NewRGBA(image.Rect(0, 0, 100, 100)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if controller.Mode != ModeSelecting || controller.FullScreenImage == nil {
		t.Fatalf("unexpected pre-discard state: mode=%s image=%v", controller.Mode, controller.FullScreenImage)
	}

	controller.DiscardSelectionCapture()

	if controller.Mode != ModeIdle {
		t.Fatalf("mode = %s, want %s", controller.Mode, ModeIdle)
	}
	if controller.FullScreenImage != nil {
		t.Fatal("expected fullscreen image to be released")
	}
}

func TestControllerCapsEditHistory(t *testing.T) {
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

	for i := 0; i < MaxEditHistoryEntries+5; i++ {
		rect := editor.NewRect(editor.Point{X: i, Y: i}, editor.Point{X: i + 10, Y: i + 10})
		if err := controller.AddAnnotation(editor.ToolRectangle, nil, rect); err != nil {
			t.Fatal(err)
		}
	}

	if len(controller.undoStack) != MaxEditHistoryEntries {
		t.Fatalf("undo history length = %d, want %d", len(controller.undoStack), MaxEditHistoryEntries)
	}
	undos := 0
	for controller.UndoEdit() {
		undos++
	}
	if undos != MaxEditHistoryEntries {
		t.Fatalf("undo count = %d, want %d", undos, MaxEditHistoryEntries)
	}
	if len(controller.redoStack) != MaxEditHistoryEntries {
		t.Fatalf("redo history length = %d, want %d", len(controller.redoStack), MaxEditHistoryEntries)
	}
	if got := len(controller.Editor.Annotations); got != 5 {
		t.Fatalf("remaining annotations = %d, want 5", got)
	}
}

func TestControllerCropCapturedImageKeepsSourceAndOffsetsOutput(t *testing.T) {
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

	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 120, 80) {
		t.Fatalf("source image bounds changed: %v", got)
	}
	if got := controller.VisibleCropRect(); got != image.Rect(10, 5, 90, 55) {
		t.Fatalf("unexpected crop rect: %v", got)
	}
	if got := controller.SelectedImageRect; got != image.Rect(30, 15, 110, 65) {
		t.Fatalf("unexpected selected image rect: %v", got)
	}
	if got := controller.SelectedScreen; got != image.Rect(30, 15, 110, 65) {
		t.Fatalf("unexpected selected screen rect: %v", got)
	}
	commands := controller.Editor.RenderCommands()
	wantRect := editor.NewRect(editor.Point{X: 30, Y: 20}, editor.Point{X: 80, Y: 60})
	if commands[0].Rect != wantRect {
		t.Fatalf("annotation was shifted: got %#v want %#v", commands[0].Rect, wantRect)
	}
	rendered, err := controller.RenderedImage()
	if err != nil {
		t.Fatal(err)
	}
	if got := rendered.Bounds(); got != image.Rect(0, 0, 80, 50) {
		t.Fatalf("unexpected rendered crop bounds: %v", got)
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
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 120, 80) {
		t.Fatalf("source image bounds changed: %v", got)
	}
	if got := controller.VisibleCropRect(); got != image.Rect(10, 5, 90, 55) {
		t.Fatalf("unexpected crop rect: %v", got)
	}
	if !controller.UndoEdit() {
		t.Fatal("expected crop undo")
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 120, 80) {
		t.Fatalf("unexpected undo bounds: %v", got)
	}
	if got := controller.VisibleCropRect(); got != image.Rect(0, 0, 120, 80) {
		t.Fatalf("unexpected undo crop rect: %v", got)
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
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 120, 80) {
		t.Fatalf("source image bounds changed after redo: %v", got)
	}
	if got := controller.VisibleCropRect(); got != image.Rect(10, 5, 90, 55) {
		t.Fatalf("unexpected redo crop rect: %v", got)
	}
	if got := controller.SelectedScreen; got != image.Rect(30, 15, 110, 65) {
		t.Fatalf("unexpected redo selected screen: %v", got)
	}
	if got := controller.Editor.Annotations[0].Rect; got != editor.NewRect(editor.Point{X: 30, Y: 20}, editor.Point{X: 80, Y: 60}) {
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

func TestControllerNormalizesShapeAnnotationRects(t *testing.T) {
	controller := NewController(&fakeCapturer{})
	controller.Mode = ModeEditing
	controller.Editor.State = editor.StateEditing
	rect := editor.Rect{Min: editor.Point{X: 40, Y: 30}, Max: editor.Point{X: 10, Y: 5}}
	for _, tool := range []editor.Tool{editor.ToolRectangle, editor.ToolTriangle, editor.ToolCircle} {
		if err := controller.AddAnnotation(tool, nil, rect); err != nil {
			t.Fatalf("AddAnnotation(%s): %v", tool, err)
		}
		got := controller.Editor.Annotations[len(controller.Editor.Annotations)-1].Rect
		if got != rect.Normalize() {
			t.Fatalf("%s rect = %#v, want %#v", tool, got, rect.Normalize())
		}
	}
}

func TestControllerAddsUpdatesAndRemovesTextAnnotationWithHistory(t *testing.T) {
	controller := NewController(&fakeCapturer{})
	controller.Mode = ModeEditing
	controller.CapturedImage = image.NewRGBA(image.Rect(0, 0, 200, 120))
	controller.CropRect = controller.CapturedImage.Bounds()
	controller.Editor.State = editor.StateEditing
	style := editor.DefaultTextStyle()

	id, err := controller.AddTextAnnotation("처음 Text", editor.NewRect(editor.Point{X: 10, Y: 15}, editor.Point{X: 150, Y: 70}), style)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := controller.UpdateTextAnnotation(id, "수정 Text", editor.NewRect(editor.Point{X: 20, Y: 25}, editor.Point{X: 170, Y: 90}), style)
	if err != nil || !changed {
		t.Fatalf("update = %v, %v", changed, err)
	}
	if !controller.UndoEdit() {
		t.Fatal("expected update undo")
	}
	annotation, ok := controller.Editor.AnnotationByID(id)
	if !ok || annotation.Text != "처음 Text" {
		t.Fatalf("undo restored %#v", annotation)
	}
	if !controller.RedoEdit() {
		t.Fatal("expected update redo")
	}
	annotation, _ = controller.Editor.AnnotationByID(id)
	if annotation.Text != "수정 Text" {
		t.Fatalf("redo restored %#v", annotation)
	}
	removed, err := controller.RemoveTextAnnotation(id)
	if err != nil || !removed {
		t.Fatalf("remove = %v, %v", removed, err)
	}
	if !controller.UndoEdit() {
		t.Fatal("expected removal undo")
	}
	if _, ok := controller.Editor.AnnotationByID(id); !ok {
		t.Fatal("removal undo did not restore text")
	}
}

func TestPreviewReplacingTextPreservesAnnotationStackingOrder(t *testing.T) {
	controller := NewController(&fakeCapturer{})
	controller.Mode = ModeEditing
	controller.CapturedImage = image.NewRGBA(image.Rect(0, 0, 100, 100))
	controller.Editor.State = editor.StateEditing
	backgroundStyle := func(r, g, b uint8) editor.TextStyle {
		style := editor.DefaultTextStyle()
		style.BackgroundEnabled = true
		style.BackgroundRGBA = [4]uint8{r, g, b, 255}
		return style
	}
	controller.Editor.Annotations = []editor.Annotation{
		{
			ID:        "editing",
			Tool:      editor.ToolText,
			Rect:      editor.NewRect(editor.Point{}, editor.Point{X: 90, Y: 90}),
			Text:      "first",
			TextStyle: backgroundStyle(255, 0, 0),
		},
		{
			ID:        "above",
			Tool:      editor.ToolText,
			Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 80, Y: 80}),
			Text:      "second",
			TextStyle: backgroundStyle(0, 255, 0),
		},
	}
	preview, err := controller.PreviewImageReplacingAnnotation("editing", editor.RenderCommand{
		Tool:      editor.ToolText,
		Rect:      controller.Editor.Annotations[0].Rect,
		Text:      "pending",
		TextStyle: backgroundStyle(0, 0, 255),
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(preview.At(15, 15)).(color.NRGBA); got.G < 200 || got.R > 50 || got.B > 50 {
		t.Fatalf("pending text moved above the later annotation: %#v", got)
	}
}

func TestControllerRichTextRunsAreCopiedAndUndoable(t *testing.T) {
	controller := NewController(&fakeCapturer{})
	controller.Mode = ModeEditing
	controller.Editor.State = editor.StateEditing
	style := editor.DefaultTextStyle()
	rect := editor.NewRect(editor.Point{X: 10, Y: 15}, editor.Point{X: 150, Y: 70})
	runs := []editor.TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 3, Bold: false}}

	id, err := controller.AddRichTextAnnotation("A한🙂", runs, rect, style)
	if err != nil {
		t.Fatal(err)
	}
	runs[0].Bold = false
	annotation, _ := controller.Editor.AnnotationByID(id)
	if !annotation.TextRuns[0].Bold {
		t.Fatal("added annotation shares caller's text runs")
	}
	snapshot := controller.snapshotEditState()
	snapshot.Editor.Annotations[0].TextRuns[0].Bold = false
	if !controller.Editor.Annotations[0].TextRuns[0].Bold {
		t.Fatal("workflow snapshot shares text runs with live editor")
	}

	updatedRuns := []editor.TextRun{{Start: 0, End: 2, Bold: false}, {Start: 2, End: 4, Bold: true}}
	changed, err := controller.UpdateRichTextAnnotation(id, "A한글🙂", updatedRuns, rect, style)
	if err != nil || !changed {
		t.Fatalf("rich update = %v, %v", changed, err)
	}
	updatedRuns[1].Bold = false
	annotation, _ = controller.Editor.AnnotationByID(id)
	if !annotation.TextRuns[1].Bold {
		t.Fatal("updated annotation shares caller's text runs")
	}

	if !controller.UndoEdit() {
		t.Fatal("expected rich-text update undo")
	}
	annotation, _ = controller.Editor.AnnotationByID(id)
	if annotation.Text != "A한🙂" || len(annotation.TextRuns) != 2 || !annotation.TextRuns[0].Bold {
		t.Fatalf("undo restored %#v", annotation)
	}
	annotation.TextRuns[0].Bold = false
	if !controller.RedoEdit() {
		t.Fatal("expected rich-text update redo")
	}
	annotation, _ = controller.Editor.AnnotationByID(id)
	if annotation.Text != "A한글🙂" || len(annotation.TextRuns) != 2 || !annotation.TextRuns[1].Bold {
		t.Fatalf("redo restored %#v", annotation)
	}
}

func TestLegacyTextUpdateTransformsExplicitRuns(t *testing.T) {
	controller := NewController(&fakeCapturer{})
	controller.Mode = ModeEditing
	controller.Editor.State = editor.StateEditing
	style := editor.DefaultTextStyle()
	rect := editor.NewRect(editor.Point{}, editor.Point{X: 100, Y: 40})
	id, err := controller.AddRichTextAnnotation("한🙂B", []editor.TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 3, Bold: true}}, rect, style)
	if err != nil {
		t.Fatal(err)
	}
	changed, err := controller.UpdateTextAnnotation(id, "한🙂글B", rect, style)
	if err != nil || !changed {
		t.Fatalf("legacy update = %v, %v", changed, err)
	}
	annotation, _ := controller.Editor.AnnotationByID(id)
	want := []editor.TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 4, Bold: true}}
	if len(annotation.TextRuns) != len(want) || annotation.TextRuns[0] != want[0] || annotation.TextRuns[1] != want[1] {
		t.Fatalf("transformed runs = %#v, want %#v", annotation.TextRuns, want)
	}
}
