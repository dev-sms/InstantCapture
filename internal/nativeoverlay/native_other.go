//go:build !windows

package nativeoverlay

import (
	"context"
	"fmt"
	"image"

	"instantcapture/internal/overlay"
)

func selectNative(context.Context, image.Image, overlay.Options) (Selection, bool, error) {
	return Selection{}, false, fmt.Errorf("%w: non-Windows platform", ErrUnavailable)
}
