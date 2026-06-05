package capture

import (
	"fmt"
	"image"

	"github.com/kbinani/screenshot"
)

type ScreenCapturer interface {
	Capture(rect image.Rectangle) (image.Image, error)
	Displays() []image.Rectangle
}

type ScreenshotCapturer struct{}

func (ScreenshotCapturer) Capture(rect image.Rectangle) (image.Image, error) {
	if rect.Empty() {
		return nil, fmt.Errorf("capture rect is empty")
	}
	return screenshot.CaptureRect(rect)
}

func (ScreenshotCapturer) Displays() []image.Rectangle {
	count := screenshot.NumActiveDisplays()
	displays := make([]image.Rectangle, 0, count)
	for i := 0; i < count; i++ {
		displays = append(displays, screenshot.GetDisplayBounds(i))
	}
	return displays
}
