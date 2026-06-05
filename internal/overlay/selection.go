package overlay

import (
	"image"

	"instantcapture/internal/coords"
)

type SelectionState struct {
	viewport coords.Viewport
	dragging bool
	start    image.Point
	current  image.Point
}

func NewSelectionState(viewport coords.Viewport) SelectionState {
	return SelectionState{viewport: viewport}
}

func (s *SelectionState) Begin(viewPoint image.Point) bool {
	sourcePoint, ok := s.viewport.ViewToSource(viewPoint)
	if !ok {
		return false
	}
	s.dragging = true
	s.start = sourcePoint
	s.current = sourcePoint
	return true
}

func (s *SelectionState) Update(viewPoint image.Point) bool {
	if !s.dragging {
		return false
	}
	viewPoint = clampPoint(viewPoint, s.viewport.View)
	sourcePoint, ok := s.viewport.ViewToSource(viewPoint)
	if !ok {
		return false
	}
	s.current = sourcePoint
	return true
}

func (s *SelectionState) Cancel() {
	s.dragging = false
}

func (s SelectionState) Dragging() bool {
	return s.dragging
}

func (s SelectionState) Rect() image.Rectangle {
	return image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(s.viewport.Source)
}

func (s SelectionState) ViewRect() image.Rectangle {
	return image.Rectangle{
		Min: s.viewport.SourceToView(s.Rect().Min),
		Max: s.viewport.SourceToView(s.Rect().Max),
	}.Canon()
}

func (s SelectionState) Commit() (image.Rectangle, bool) {
	if !s.dragging {
		return image.Rectangle{}, false
	}
	rect := s.Rect()
	return rect, !rect.Empty()
}

func clampPoint(p image.Point, r image.Rectangle) image.Point {
	if p.X < r.Min.X {
		p.X = r.Min.X
	}
	if p.Y < r.Min.Y {
		p.Y = r.Min.Y
	}
	if p.X > r.Max.X {
		p.X = r.Max.X
	}
	if p.Y > r.Max.Y {
		p.Y = r.Max.Y
	}
	return p
}
