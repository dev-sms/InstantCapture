//go:build windows

package windows

import (
	"fmt"

	xwindows "golang.org/x/sys/windows"
)

var (
	user32             = xwindows.NewLazySystemDLL("user32.dll")
	setProcessDPIAware = user32.NewProc("SetProcessDPIAware")
)

func EnableDPIAwareness() error {
	if err := user32.Load(); err != nil {
		return fmt.Errorf("load user32.dll: %w", err)
	}

	result, _, callErr := setProcessDPIAware.Call()
	if result == 0 && callErr != xwindows.ERROR_SUCCESS {
		return fmt.Errorf("set process DPI aware: %w", callErr)
	}

	return nil
}
