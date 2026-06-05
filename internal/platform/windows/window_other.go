//go:build !windows

package windows

import "image"

type WindowTarget struct {
	Handle uintptr
	Title  string
	Rect   image.Rectangle
}

func HideWindowByTitle(string) error                        { return nil }
func RestoreWindowByTitle(string) error                     { return nil }
func ForegroundIsWindowTitle(string) bool                   { return true }
func WindowVisibleByTitle(string) bool                      { return true }
func ForegroundWindowRect(string) (image.Rectangle, error)  { return image.Rectangle{}, nil }
func VisibleWindowRects(string) []image.Rectangle           { return nil }
func VisibleWindowTargets(string) []WindowTarget            { return nil }
func CaptureWindowTarget(WindowTarget) (image.Image, error) { return nil, nil }
func CursorPosition() image.Point                           { return image.Point{} }
