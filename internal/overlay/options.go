package overlay

import "image"

type Options struct {
	VirtualScreen  image.Rectangle
	TopMostHint    bool
	DimAlpha       uint8
	CaptureMode    string
	WindowRects    []image.Rectangle
	LiveBackground bool
}

func DefaultOptions(imageBounds image.Rectangle) Options {
	return Options{
		VirtualScreen: imageBounds,
		TopMostHint:   true,
		DimAlpha:      80,
		CaptureMode:   "Region",
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
	normalized.TopMostHint = provided.TopMostHint
	if provided.DimAlpha > 0 {
		normalized.DimAlpha = provided.DimAlpha
	}
	if provided.CaptureMode != "" {
		normalized.CaptureMode = provided.CaptureMode
	}
	normalized.WindowRects = append([]image.Rectangle(nil), provided.WindowRects...)
	normalized.LiveBackground = provided.LiveBackground
	return normalized
}
