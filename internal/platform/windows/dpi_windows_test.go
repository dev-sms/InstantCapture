//go:build windows

package windows

import (
	"testing"

	"instantcapture/internal/capture"
)

func TestDPIAwareVirtualScreenMatchesScreenshotBounds(t *testing.T) {
	if err := EnableDPIAwareness(); err != nil {
		t.Fatal(err)
	}
	native := NativeVirtualScreenBounds()
	screenshotBounds := capture.UnionDisplays(capture.ScreenshotCapturer{}.Displays())
	if native != screenshotBounds {
		t.Fatalf("native virtual screen = %v, screenshot bounds = %v", native, screenshotBounds)
	}
}
