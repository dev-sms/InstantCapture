package windows

import "image"

func BuildVirtualScreenRect(x, y, width, height int) image.Rectangle {
	if width <= 0 || height <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(x, y, x+width, y+height)
}

func PreferNativeVirtualScreen(native, fallback image.Rectangle) image.Rectangle {
	if !native.Empty() {
		return native
	}
	return fallback
}

func PreferMatchingNativeVirtualScreen(native, fallback, imageBounds image.Rectangle) image.Rectangle {
	if native.Empty() {
		return fallback
	}
	if native.Dx() == imageBounds.Dx() && native.Dy() == imageBounds.Dy() {
		return native
	}
	return fallback
}
