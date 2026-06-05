//go:build !windows

package windows

import "image"

func NativeVirtualScreenBounds() image.Rectangle {
	return image.Rectangle{}
}
