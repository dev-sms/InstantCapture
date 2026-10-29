package overlay

import (
	"image"
	"image/color"
)

type Options struct {
	VirtualScreen  image.Rectangle
	Displays       []image.Rectangle
	TopMostHint    bool
	DimAlpha       uint8
	CaptureMode    string
	WindowRects    []image.Rectangle
	WindowRegions  []WindowRegion
	LiveBackground bool
	SelectionColor color.NRGBA
	Hint           string
}

type WindowRegion struct {
	Handle       uintptr
	Rect         image.Rectangle
	VisibleRects []image.Rectangle
}

func DefaultOptions(imageBounds image.Rectangle) Options {
	return Options{
		VirtualScreen:  imageBounds,
		TopMostHint:    true,
		DimAlpha:       80,
		CaptureMode:    "Region",
		SelectionColor: color.NRGBA{R: 255, G: 64, B: 64, A: 255},
	}
}

func NormalizeOptions(imageBounds image.Rectangle, options ...Options) Options {
	normalized := DefaultOptions(imageBounds)
	if len(options) == 0 {
		return normalized
	}

	provided := options[0]
	if !provided.VirtualScreen.Empty() {
		normalized.VirtualScreen = provided.VirtualScreen
	}
	normalized.Displays = append([]image.Rectangle(nil), provided.Displays...)
	normalized.TopMostHint = provided.TopMostHint
	if provided.DimAlpha > 0 {
		normalized.DimAlpha = provided.DimAlpha
	}
	if provided.CaptureMode != "" {
		normalized.CaptureMode = provided.CaptureMode
	}
	normalized.WindowRects = append([]image.Rectangle(nil), provided.WindowRects...)
	normalized.WindowRegions = cloneWindowRegions(provided.WindowRegions)
	normalized.LiveBackground = provided.LiveBackground
	if provided.SelectionColor.A != 0 {
		normalized.SelectionColor = provided.SelectionColor
	}
	if provided.Hint != "" {
		normalized.Hint = provided.Hint
	}
	return normalized
}

func cloneWindowRegions(src []WindowRegion) []WindowRegion {
	if src == nil {
		return nil
	}
	dst := make([]WindowRegion, len(src))
	for i := range src {
		dst[i] = src[i]
		dst[i].VisibleRects = append([]image.Rectangle(nil), src[i].VisibleRects...)
	}
	return dst
}
