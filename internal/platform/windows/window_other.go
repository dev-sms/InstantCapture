//go:build !windows

package windows

import (
	"image"
	"os"
	"path/filepath"
)

type WindowTarget struct {
	Handle       uintptr
	Title        string
	Rect         image.Rectangle
	VisibleRects []image.Rectangle
}

func HideWindowByTitle(string) error                        { return nil }
func RestoreWindowByTitle(string) error                     { return nil }
func ForegroundIsWindowTitle(string) bool                   { return true }
func WindowVisibleByTitle(string) bool                      { return true }
func ForegroundWindowRect(string) (image.Rectangle, error)  { return image.Rectangle{}, nil }
func VisibleWindowRects(string) []image.Rectangle           { return nil }
func VisibleWindowTargets(string) []WindowTarget            { return nil }
func CaptureWindowTarget(WindowTarget) (image.Image, error) { return nil, nil }
func CaptureScrollTarget(WindowTarget) (image.Image, error) { return nil, nil }
func CursorPosition() image.Point                           { return image.Point{} }
func PickFolder(string) (string, bool, error)               { return "", false, nil }

func DefaultScreenshotDir() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		screenshots := filepath.Join(home, "Pictures", "Screenshots")
		if err := os.MkdirAll(screenshots, 0o755); err == nil {
			return screenshots
		}
	}
	wd, err := os.Getwd()
	if err == nil {
		return wd
	}
	return "."
}
