package coords

import (
	"image"
	"math"
)

type Viewport struct {
	Source image.Rectangle
	View   image.Rectangle
	Scale  float64
}

func FitContain(source, bounds image.Rectangle) Viewport {
	source = source.Canon()
	bounds = bounds.Canon()
	if source.Empty() || bounds.Empty() {
		return Viewport{Source: source, View: image.Rectangle{}, Scale: 1}
	}

	scaleX := float64(bounds.Dx()) / float64(source.Dx())
	scaleY := float64(bounds.Dy()) / float64(source.Dy())
	scale := math.Min(scaleX, scaleY)
	if scale <= 0 || math.IsInf(scale, 0) || math.IsNaN(scale) {
		scale = 1
	}

	viewW := max(1, int(math.Round(float64(source.Dx())*scale)))
	viewH := max(1, int(math.Round(float64(source.Dy())*scale)))
	viewMin := image.Pt(
		bounds.Min.X+(bounds.Dx()-viewW)/2,
		bounds.Min.Y+(bounds.Dy()-viewH)/2,
	)

	return Viewport{
		Source: source,
		View:   image.Rectangle{Min: viewMin, Max: viewMin.Add(image.Pt(viewW, viewH))},
		Scale:  scale,
	}
}

func (v Viewport) ViewToSource(p image.Point) (image.Point, bool) {
	if v.Source.Empty() || v.View.Empty() || v.Scale <= 0 || !pointInInclusive(p, v.View) {
		return image.Point{}, false
	}

	p = clampPoint(p, v.View)
	x := v.Source.Min.X + int(math.Floor(float64(p.X-v.View.Min.X)/v.Scale))
	y := v.Source.Min.Y + int(math.Floor(float64(p.Y-v.View.Min.Y)/v.Scale))
	return clampPoint(image.Pt(x, y), v.Source), true
}

func (v Viewport) SourceToView(p image.Point) image.Point {
	if v.Scale <= 0 {
		return v.View.Min
	}
	return image.Pt(
		v.View.Min.X+int(math.Round(float64(p.X-v.Source.Min.X)*v.Scale)),
		v.View.Min.Y+int(math.Round(float64(p.Y-v.Source.Min.Y)*v.Scale)),
	)
}

func (v Viewport) ViewRectToSource(r image.Rectangle) image.Rectangle {
	min, _ := v.ViewToSource(r.Min)
	max, _ := v.ViewToSource(r.Max)
	return image.Rectangle{Min: min, Max: max}.Canon().Intersect(v.Source)
}

func pointInInclusive(p image.Point, r image.Rectangle) bool {
	return p.X >= r.Min.X && p.Y >= r.Min.Y && p.X <= r.Max.X && p.Y <= r.Max.Y
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
