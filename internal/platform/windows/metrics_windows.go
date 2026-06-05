//go:build windows

package windows

import (
	"image"
	"syscall"
)

const (
	smXVirtualScreen  = 76
	smYVirtualScreen  = 77
	smCXVirtualScreen = 78
	smCYVirtualScreen = 79
)

var (
	metricsUser32          = syscall.NewLazyDLL("user32.dll")
	metricsGetSystemMetric = metricsUser32.NewProc("GetSystemMetrics")
)

func NativeVirtualScreenBounds() (rect image.Rectangle) {
	x := getSystemMetric(smXVirtualScreen)
	y := getSystemMetric(smYVirtualScreen)
	w := getSystemMetric(smCXVirtualScreen)
	h := getSystemMetric(smCYVirtualScreen)
	return BuildVirtualScreenRect(x, y, w, h)
}

func getSystemMetric(index int) int {
	ret, _, _ := metricsGetSystemMetric.Call(uintptr(index))
	return int(int32(ret))
}
