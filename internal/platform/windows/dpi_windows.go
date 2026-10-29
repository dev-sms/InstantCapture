//go:build windows

package windows

import (
	"fmt"

	xwindows "golang.org/x/sys/windows"
)

var (
	user32                        = xwindows.NewLazySystemDLL("user32.dll")
	setProcessDPIAware            = user32.NewProc("SetProcessDPIAware")
	setProcessDPIAwarenessContext = user32.NewProc("SetProcessDpiAwarenessContext")
)

const dpiAwarenessContextPerMonitorAwareV2 = ^uintptr(3)

func EnableDPIAwareness() error {
	if err := user32.Load(); err != nil {
		return fmt.Errorf("load user32.dll: %w", err)
	}
	if err := setProcessDPIAwarenessContext.Find(); err == nil {
		result, _, callErr := setProcessDPIAwarenessContext.Call(dpiAwarenessContextPerMonitorAwareV2)
		if result != 0 || callErr == xwindows.ERROR_ACCESS_DENIED {
			return nil
		}
	}

	result, _, callErr := setProcessDPIAware.Call()
	if result == 0 && callErr != xwindows.ERROR_SUCCESS && callErr != xwindows.ERROR_ACCESS_DENIED {
		return fmt.Errorf("set process DPI aware: %w", callErr)
	}

	return nil
}
