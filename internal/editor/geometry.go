package editor

import "image"

type Point struct {
	X int
	Y int
}

type Rect struct {
	Min Point
	Max Point
}

func NewRect(a, b Point) Rect {
	return Rect{Min: a, Max: b}.Normalize()
}

func (r Rect) Normalize() Rect {
	minX, maxX := ordered(r.Min.X, r.Max.X)
	minY, maxY := ordered(r.Min.Y, r.Max.Y)
	return Rect{Min: Point{X: minX, Y: minY}, Max: Point{X: maxX, Y: maxY}}
}

func (r Rect) Width() int {
	r = r.Normalize()
	return r.Max.X - r.Min.X
}

func (r Rect) Height() int {
	r = r.Normalize()
	return r.Max.Y - r.Min.Y
}

func (r Rect) Empty() bool {
	return r.Width() <= 0 || r.Height() <= 0
}

func (r Rect) ImageRect() image.Rectangle {
	r = r.Normalize()
	return image.Rect(r.Min.X, r.Min.Y, r.Max.X, r.Max.Y)
}

func ordered(a, b int) (int, int) {
	if a <= b {
		return a, b
	}
	return b, a
}
