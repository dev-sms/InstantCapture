//go:build windows

package windows

import (
	"errors"
	"fmt"
	"sync"

	xwindows "golang.org/x/sys/windows"
)

func AcquireSingleInstance(name string) (func(), bool, error) {
	namePtr, err := xwindows.UTF16PtrFromString(name)
	if err != nil {
		return nil, false, fmt.Errorf("encode instance name: %w", err)
	}
	handle, err := xwindows.CreateMutex(nil, false, namePtr)
	if errors.Is(err, xwindows.ERROR_ALREADY_EXISTS) {
		if handle != 0 {
			xwindows.CloseHandle(handle)
		}
		return func() {}, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("create instance mutex: %w", err)
	}
	var once sync.Once
	return func() {
		once.Do(func() {
			xwindows.CloseHandle(handle)
		})
	}, false, nil
}
