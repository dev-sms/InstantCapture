//go:build windows

package windows

import (
	"fmt"
	"image"
	"strings"
	"syscall"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

type WindowTarget struct {
	Handle uintptr
	Title  string
	Rect   image.Rectangle
}

type windowRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

const (
	swHide    = 0
	swShow    = 5
	swRestore = 9
)

var (
	windowUser32               = syscall.NewLazyDLL("user32.dll")
	windowFindWindowW          = windowUser32.NewProc("FindWindowW")
	windowEnumWindows          = windowUser32.NewProc("EnumWindows")
	windowGetForegroundWindow  = windowUser32.NewProc("GetForegroundWindow")
	windowGetCursorPos         = windowUser32.NewProc("GetCursorPos")
	windowGetWindowRect        = windowUser32.NewProc("GetWindowRect")
	windowGetWindowTextLengthW = windowUser32.NewProc("GetWindowTextLengthW")
	windowGetWindowTextW       = windowUser32.NewProc("GetWindowTextW")
	windowIsWindowVisible      = windowUser32.NewProc("IsWindowVisible")
	windowShowWindow           = windowUser32.NewProc("ShowWindow")
	windowShowWindowAsync      = windowUser32.NewProc("ShowWindowAsync")
	windowSetForegroundWindow  = windowUser32.NewProc("SetForegroundWindow")
)

func HideWindowByTitle(title string) error {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return err
	}
	windowShowWindowAsync.Call(hwnd, uintptr(swHide))
	return nil
}

func RestoreWindowByTitle(title string) error {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return err
	}
	windowShowWindowAsync.Call(hwnd, uintptr(swRestore))
	windowShowWindowAsync.Call(hwnd, uintptr(swShow))
	windowSetForegroundWindow.Call(hwnd)
	return nil
}

func ForegroundIsWindowTitle(title string) bool {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return false
	}
	foreground, _, _ := windowGetForegroundWindow.Call()
	return foreground != 0 && foreground == hwnd
}

func ForegroundWindowRect(excludeTitle string) (image.Rectangle, error) {
	foreground, _, _ := windowGetForegroundWindow.Call()
	if foreground == 0 {
		return image.Rectangle{}, fmt.Errorf("no foreground window")
	}
	if hwnd, err := findWindowByTitle(excludeTitle); err == nil && hwnd == foreground {
		return image.Rectangle{}, fmt.Errorf("foreground window is %q", excludeTitle)
	}
	visible, _, _ := windowIsWindowVisible.Call(foreground)
	if visible == 0 {
		return image.Rectangle{}, fmt.Errorf("foreground window is not visible")
	}
	var rect windowRect
	ret, _, callErr := windowGetWindowRect.Call(foreground, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return image.Rectangle{}, fmt.Errorf("get foreground window rect: %v", callErr)
	}
	result := image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom))
	if result.Empty() {
		return image.Rectangle{}, fmt.Errorf("foreground window rect is empty")
	}
	return result, nil
}

func VisibleWindowRects(excludeTitle string) []image.Rectangle {
	targets := VisibleWindowTargets(excludeTitle)
	rects := make([]image.Rectangle, 0, len(targets))
	for _, target := range targets {
		rects = append(rects, target.Rect)
	}
	return rects
}

func VisibleWindowTargets(excludeTitle string) []WindowTarget {
	return visibleWindowTargetsRaw(excludeTitle)
}

func visibleWindowTargetsRaw(excludeTitle string) []WindowTarget {
	excluded, _ := findWindowByTitle(excludeTitle)
	var targets []WindowTarget
	callback := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		if hwnd == 0 || hwnd == excluded {
			return 1
		}
		visible, _, _ := windowIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
		title := windowTitle(hwnd)
		if !usableWindowTitle(title, excludeTitle) {
			return 1
		}
		var wr windowRect
		ret, _, _ := windowGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		if ret == 0 {
			return 1
		}
		rect := image.Rect(int(wr.Left), int(wr.Top), int(wr.Right), int(wr.Bottom))
		if usableWindowRect(rect) {
			targets = append(targets, WindowTarget{Handle: hwnd, Title: title, Rect: rect})
		}
		return 1
	})
	windowEnumWindows.Call(callback, 0)
	return targets
}

func CaptureWindowTarget(target WindowTarget) (image.Image, error) {
	return nil, fmt.Errorf("direct window capture disabled; using frozen snapshot crop")
}

func usableWindowTitle(title, excludeTitle string) bool {
	trimmed := strings.TrimSpace(title)
	return trimmed != "" && trimmed != excludeTitle && trimmed != "InstantCapture Native Overlay"
}

func usableWindowRect(rect image.Rectangle) bool {
	return rect.Dx() >= 40 && rect.Dy() >= 40
}

func CursorPosition() image.Point {
	var p point
	windowGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return image.Pt(int(p.X), int(p.Y))
}

type point struct{ X, Y int32 }

func windowTitle(hwnd uintptr) string {
	length, _, _ := windowGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}
	buf := make([]uint16, int(length)+1)
	windowGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func WindowVisibleByTitle(title string) bool {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return false
	}
	visible, _, _ := windowIsWindowVisible.Call(hwnd)
	return visible != 0
}

func findWindowByTitle(title string) (uintptr, error) {
	windowTitle, err := xwindows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	hwnd, _, callErr := windowFindWindowW.Call(0, uintptr(unsafe.Pointer(windowTitle)))
	if hwnd == 0 {
		return 0, fmt.Errorf("find window %q: %v", title, callErr)
	}
	return hwnd, nil
}
