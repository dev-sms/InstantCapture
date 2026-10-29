package ui

import (
	"image"
	"math"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"instantcapture/internal/coords"
	"instantcapture/internal/workflow"
)

func TestRotationGestureZoomPanHistory(t *testing.T) {
	for _, scale := range []float64{0.5, 1, 2.5} {
		c := workflow.NewController(nil)
		src := image.NewRGBA(image.Rect(0, 0, 240, 120))
		if err := c.CommitImportedImage(src); err != nil {
			t.Fatal(err)
		}
		u := &windowUI{controller: c, canvasZoom: scale, canvasPan: image.Pt(-30, 15), canvasSourceSize: src.Bounds().Size()}
		before := u.captureCanvasView()
		viewport := coords.Viewport{Source: src.Bounds(), View: image.Rect(-30, 50, -30+int(240*scale), 50+int(120*scale)), Scale: scale}
		rect := rotationHandleRect(viewport, c.VisibleCropRect())
		start := f32.Pt(float32(rect.Min.X+9), float32(rect.Min.Y+9))
		router := new(input.Router)
		gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}}
		if !u.handleRotationPointer(gtx, pointer.Event{Kind: pointer.Move, Position: start}, viewport) || !u.rotation.Hover {
			t.Fatal("hover not detected")
		}
		if !u.handleRotationPointer(gtx, pointer.Event{Kind: pointer.Press, PointerID: 1, Buttons: pointer.ButtonPrimary, Position: start}, viewport) {
			t.Fatal("press not handled")
		}
		if !u.rotation.Active || u.cropDragging || u.dragging {
			t.Fatal("conflicting gesture")
		}
		center := u.rotation.Center
		end := f32.Pt(center.X+(center.Y-start.Y), center.Y)
		for i := 0; i < 10; i++ {
			u.handleRotationPointer(gtx, pointer.Event{Kind: pointer.Drag, PointerID: 1, Position: end}, viewport)
		}
		if u.rotation.Degrees != 90 || c.CapturedImage != src || len(u.undoStack) != 0 {
			t.Fatal("preview mutated history or wrong angle")
		}
		u.handleRotationPointer(gtx, pointer.Event{Kind: pointer.Release, PointerID: 1, Position: end}, viewport)
		if u.rotation.Active || c.OutputSize() != image.Pt(120, 240) || len(u.undoStack) != 1 {
			t.Fatal("commit should create one entry")
		}
		if !u.undoLastEdit() || c.CapturedImage != src || u.captureCanvasView() != before {
			t.Fatal("undo did not restore view and source")
		}
		if !u.redoLastEdit() || c.OutputSize() != image.Pt(120, 240) {
			t.Fatal("redo failed")
		}
	}
}

func TestRotationPointerRoutingReleaseOutsideImage(t *testing.T) {
	c := workflow.NewController(nil)
	src := image.NewRGBA(image.Rect(0, 0, 120, 60))
	if err := c.CommitImportedImage(src); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{controller: c}
	canvas := image.Rect(0, 0, 600, 400)
	viewport := coords.Viewport{Source: src.Bounds(), View: image.Rect(80, 80, 200, 140), Scale: 1}
	router := new(input.Router)
	gtx := layout.Context{Ops: new(op.Ops), Source: router.Source(), Constraints: layout.Exact(canvas.Size())}
	clipping := clip.Rect(canvas).Push(gtx.Ops)
	event.Op(gtx.Ops, &u.canvasTag)
	clipping.Pop()
	u.handleCanvasPointer(gtx, nil, src.Bounds(), canvas, viewport)
	router.Frame(gtx.Ops)
	gtx.Ops.Reset()
	handle := rotationHandleRect(viewport, c.VisibleCropRect())
	start := f32.Pt(float32(handle.Min.X+9), float32(handle.Min.Y+9))
	router.Queue(
		pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, PointerID: 1, Buttons: pointer.ButtonPrimary, Position: start},
		pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, PointerID: 1, Buttons: pointer.ButtonPrimary, Position: f32.Pt(300, 110)},
		pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, PointerID: 1, Position: f32.Pt(300, 110)},
	)
	u.handleCanvasPointer(gtx, nil, src.Bounds(), canvas, viewport)
	if u.rotation.Active || u.cropDragging || u.dragging || len(u.undoStack) != 1 || c.OutputSize() != image.Pt(60, 120) {
		t.Fatalf("routed gesture failed: rotation=%+v history=%d size=%v", u.rotation, len(u.undoStack), c.OutputSize())
	}
}

func TestRotationCancellationReplacementAndCopy(t *testing.T) {
	c := workflow.NewController(nil)
	src := image.NewRGBA(image.Rect(0, 0, 40, 20))
	if err := c.CommitImportedImage(src); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{controller: c}
	for _, action := range []string{"escape", "cancel", "secondary", "copy", "replace", "releaseCaches"} {
		u.rotation = rotationGesture{Active: true, Degrees: 37, ID: 1}
		switch action {
		case "escape":
			u.handleShortcutKey(key.Event{Name: key.NameEscape})
		case "cancel":
			u.handleRotationPointer(layout.Context{}, pointer.Event{Kind: pointer.Cancel}, coords.Viewport{})
		case "secondary":
			u.handleRotationPointer(layout.Context{}, pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonSecondary}, coords.Viewport{})
		case "copy":
			u.copyPNG = func() error {
				if c.CapturedImage != src {
					t.Fatal("copy used uncommitted preview")
				}
				return nil
			}
			if err := u.copyPNGCurrentImage(); err != nil {
				t.Fatal(err)
			}
		case "replace":
			if err := u.commitLoadedImage(src); err != nil {
				t.Fatal(err)
			}
		case "releaseCaches":
			u.releaseTransientImageCaches()
		}
		if u.rotation.Active || len(u.undoStack) != 0 || c.CapturedImage != src {
			t.Fatalf("%s retained gesture or mutated image", action)
		}
	}
}

func TestRotationQuantizationAndCropTargetSeparation(t *testing.T) {
	for _, angle := range []float64{-179.7, -90.4, -1.2, 0, 1.4, 36.7, 179.4} {
		center := f32.Pt(100, 100)
		start := -math.Pi / 2
		rad := start + angle*math.Pi/180
		p := f32.Pt(100+float32(math.Cos(rad)*80), 100+float32(math.Sin(rad)*80))
		want := (int(math.Round(angle))+540)%360 - 180
		if got := rotationDelta(start, center, p); got != want {
			t.Fatalf("angle %f = %d, want %d", angle, got, want)
		}
	}
	for _, y := range []int{0, 50} {
		viewport := coords.Viewport{Source: image.Rect(0, 0, 100, 80), View: image.Rect(0, y, 100, 80+y), Scale: 1}
		handle := rotationHandleRect(viewport, viewport.Source)
		top := viewport.SourceToView(image.Pt(50, 0))
		if top.In(handle) || handle.Intersect(image.Rect(top.X-4, top.Y-4, top.X+5, top.Y+5)) != (image.Rectangle{}) {
			t.Fatal("rotation target overlaps crop target")
		}
	}
}
