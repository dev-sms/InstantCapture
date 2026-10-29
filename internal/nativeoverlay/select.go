package nativeoverlay

import (
	"context"
	"errors"
	"image"

	"instantcapture/internal/overlay"
)

var ErrUnavailable = errors.New("native overlay unavailable")

type Selection struct {
	Rect        image.Rectangle
	Polygon     []image.Point
	ScrollRange *ScrollRangeSelection
}

type ScrollRangeSelection struct {
	WindowRect           image.Rectangle
	CropRect             image.Rectangle
	ViewRect             image.Rectangle
	ScrollSteps          int
	ScrollPixels         int
	InitialScrollPercent *float64
	InitialScrollPoint   image.Point
	InitialWheelUnits    int
}

func Select(ctx context.Context, img image.Image, options ...overlay.Options) (image.Rectangle, bool, error) {
	selection, ok, err := SelectDetailed(ctx, img, options...)
	return selection.Rect, ok, err
}

func SelectDetailed(ctx context.Context, img image.Image, options ...overlay.Options) (Selection, bool, error) {
	opt := overlay.NormalizeOptions(img.Bounds(), options...)
	selection, ok, err := selectNative(ctx, img, opt)
	if err == nil {
		return selection, ok, nil
	}
	if errors.Is(err, ErrUnavailable) {
		rect, ok, err := overlay.Select(ctx, img, opt)
		return Selection{Rect: rect}, ok, err
	}
	return selection, ok, err
}
