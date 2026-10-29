package overlay

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"instantcapture/internal/coords"
)

type selector struct {
	image     image.Image
	theme     *material.Theme
	canvasTag struct{}
	keyTag    struct{}
	selection SelectionState
	viewport  coords.Viewport
	options   Options
	hasCursor bool
	cursor    image.Point
	status    string
}

func Select(ctx context.Context, img image.Image, options ...Options) (image.Rectangle, bool, error) {
	if img == nil {
		return image.Rectangle{}, false, fmt.Errorf("overlay image is nil")
	}
	opt := NormalizeOptions(img.Bounds(), options...)

	w := new(app.Window)
	windowOptions := []app.Option{
		app.Title("InstantCapture Selection"),
		app.Decorated(false),
		app.Fullscreen.Option(),
	}
	if opt.TopMostHint {
		windowOptions = append(windowOptions, app.TopMost(true))
	}
	w.Option(windowOptions...)
	w.Perform(system.ActionRaise)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			w.Perform(system.ActionClose)
		case <-done:
		}
	}()

	selector := &selector{
		image:   img,
		theme:   material.NewTheme(),
		options: opt,
		status:  fmt.Sprintf("드래그로 영역 선택, Esc 또는 우클릭으로 취소 | Virtual: %v", opt.VirtualScreen),
	}
	if opt.Hint != "" {
		selector.status = opt.Hint
	}

	var ops op.Ops
	for {
		e := w.Event()
		switch e := e.(type) {
		case app.DestroyEvent:
			return image.Rectangle{}, false, e.Err
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			result, done := selector.layout(gtx)
			e.Frame(gtx.Ops)
			if done {
				w.Perform(system.ActionClose)
				return result, !result.Empty(), nil
			}
		}
	}
}

func (s *selector) layout(gtx layout.Context) (image.Rectangle, bool) {
	size := gtx.Constraints.Max
	bounds := image.Rectangle{Max: size}
	s.viewport = coords.FitContain(s.image.Bounds(), bounds)
	if !s.viewport.View.Empty() {
		s.selection.viewport = s.viewport
	}

	paint.Fill(gtx.Ops, color.NRGBA{R: 4, G: 4, B: 6, A: 255})
	event.Op(gtx.Ops, &s.keyTag)
	gtx.Execute(key.FocusCmd{Tag: &s.keyTag})

	if rect, done := s.handleKeys(gtx); done {
		return rect, true
	}

	clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	event.Op(gtx.Ops, &s.canvasTag)
	pointer.CursorCrosshair.Add(gtx.Ops)
	if rect, done := s.handlePointer(gtx); done {
		return rect, true
	}

	s.drawImage(gtx.Ops)
	s.drawDim(gtx.Ops)
	s.drawCrosshair(gtx.Ops)
	s.drawSelection(gtx.Ops)
	s.drawHint(gtx)
	return image.Rectangle{}, false
}

func (s *selector) handleKeys(gtx layout.Context) (image.Rectangle, bool) {
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &s.keyTag, Name: key.NameEscape})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			return image.Rectangle{}, true
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &s.keyTag, Name: key.NameReturn})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			if rect, ok := s.selection.Commit(); ok {
				return rect, true
			}
		}
	}
	return image.Rectangle{}, false
}

func (s *selector) handlePointer(gtx layout.Context) (image.Rectangle, bool) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target: &s.canvasTag,
			Kinds:  pointer.Move | pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel,
		})
		if !ok {
			break
		}

		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		p := image.Pt(int(math.Round(float64(pe.Position.X))), int(math.Round(float64(pe.Position.Y))))

		switch pe.Kind {
		case pointer.Move:
			if source, ok := s.viewport.ViewToSource(p); ok {
				s.cursor = source
				s.hasCursor = true
			}
		case pointer.Press:
			if isCancelPointerPress(pe) {
				s.selection.Cancel()
				return image.Rectangle{}, true
			}
			if source, ok := s.viewport.ViewToSource(p); ok {
				s.cursor = source
				s.hasCursor = true
			}
			s.selection = NewSelectionState(s.viewport)
			s.selection.Begin(p)
		case pointer.Drag:
			if source, ok := s.viewport.ViewToSource(p); ok {
				s.cursor = source
				s.hasCursor = true
			}
			s.selection.Update(p)
		case pointer.Release:
			if source, ok := s.viewport.ViewToSource(p); ok {
				s.cursor = source
				s.hasCursor = true
			}
			s.selection.Update(p)
			return s.selection.Commit()
		case pointer.Cancel:
			s.selection.Cancel()
		}
	}
	return image.Rectangle{}, false
}

func isCancelPointerPress(ev pointer.Event) bool {
	return ev.Kind == pointer.Press && ev.Buttons.Contain(pointer.ButtonSecondary)
}

func (s *selector) drawImage(ops *op.Ops) {
	if s.viewport.View.Empty() || s.image == nil {
		return
	}
	source := s.image.Bounds()
	scale := float32(s.viewport.View.Dx()) / float32(source.Dx())
	defer clip.Rect(s.viewport.View).Push(ops).Pop()
	defer op.Offset(s.viewport.View.Min).Push(ops).Pop()
	defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(ops).Pop()
	paint.NewImageOp(s.image).Add(ops)
	paint.PaintOp{}.Add(ops)
}

func (s *selector) drawDim(ops *op.Ops) {
	if s.viewport.View.Empty() || s.options.DimAlpha == 0 {
		return
	}
	for _, rect := range dimRects(s.viewport.View, s.selection.ViewRect(), s.selection.Dragging()) {
		paint.FillShape(ops, color.NRGBA{A: s.options.DimAlpha}, clip.Rect(rect).Op())
	}
}

func dimRects(bounds, selection image.Rectangle, selecting bool) []image.Rectangle {
	selection = selection.Intersect(bounds)
	if !selecting || selection.Empty() {
		return []image.Rectangle{bounds}
	}
	return []image.Rectangle{
		image.Rect(bounds.Min.X, bounds.Min.Y, bounds.Max.X, selection.Min.Y),
		image.Rect(bounds.Min.X, selection.Max.Y, bounds.Max.X, bounds.Max.Y),
		image.Rect(bounds.Min.X, selection.Min.Y, selection.Min.X, selection.Max.Y),
		image.Rect(selection.Max.X, selection.Min.Y, bounds.Max.X, selection.Max.Y),
	}
}

func (s *selector) drawCrosshair(ops *op.Ops) {
	if !s.hasCursor || s.viewport.View.Empty() {
		return
	}
	p := s.viewport.SourceToView(s.cursor)
	c := color.NRGBA{R: 255, G: 255, B: 255, A: 180}
	drawPolyline(ops, []image.Point{{X: s.viewport.View.Min.X, Y: p.Y}, {X: s.viewport.View.Max.X, Y: p.Y}}, c, 1)
	drawPolyline(ops, []image.Point{{X: p.X, Y: s.viewport.View.Min.Y}, {X: p.X, Y: s.viewport.View.Max.Y}}, c, 1)
}

func (s *selector) drawSelection(ops *op.Ops) {
	if !s.selection.Dragging() {
		return
	}
	rect := s.selection.ViewRect()
	if rect.Empty() {
		return
	}
	drawStrokeRect(ops, rect, s.options.SelectionColor, 3)
}

func (s *selector) drawHint(gtx layout.Context) {
	inset := layout.Inset{Top: unit.Dp(18), Left: unit.Dp(18)}
	inset.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		label := material.Body1(s.theme, s.status)
		label.Color = color.NRGBA{R: 245, G: 245, B: 250, A: 255}
		return label.Layout(gtx)
	})
}

func drawStrokeRect(ops *op.Ops, rect image.Rectangle, c color.NRGBA, width float32) {
	points := []image.Point{
		rect.Min,
		{X: rect.Max.X, Y: rect.Min.Y},
		rect.Max,
		{X: rect.Min.X, Y: rect.Max.Y},
		rect.Min,
	}
	drawPolyline(ops, points, c, width)
}

func drawPolyline(ops *op.Ops, points []image.Point, c color.NRGBA, width float32) {
	if len(points) < 2 {
		return
	}
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(f32.Pt(float32(points[0].X), float32(points[0].Y)))
	for _, point := range points[1:] {
		path.LineTo(f32.Pt(float32(point.X), float32(point.Y)))
	}
	paint.FillShape(ops, c, clip.Stroke{Path: path.End(), Width: width}.Op())
}
