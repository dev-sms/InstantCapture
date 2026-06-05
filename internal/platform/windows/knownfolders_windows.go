//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

var (
	knownFolderShell32              = syscall.NewLazyDLL("shell32.dll")
	knownFolderOle32                = syscall.NewLazyDLL("ole32.dll")
	knownFolderSHGetKnownFolderPath = knownFolderShell32.NewProc("SHGetKnownFolderPath")
	knownFolderCoTaskMemFree        = knownFolderOle32.NewProc("CoTaskMemFree")
)

var (
	folderIDScreenshots = xwindows.GUID{Data1: 0xB7BEDE81, Data2: 0xDF94, Data3: 0x4682, Data4: [8]byte{0xA7, 0xD8, 0x57, 0xA5, 0x26, 0x20, 0xB8, 0x6F}}
	folderIDPictures    = xwindows.GUID{Data1: 0x33E28130, Data2: 0x4E1E, Data3: 0x4676, Data4: [8]byte{0x83, 0x5A, 0x98, 0x39, 0x5C, 0x3B, 0xC3, 0xBB}}
)

func DefaultScreenshotDir() string {
	if dir, err := knownFolderPath(folderIDScreenshots); err == nil && ensureDir(dir) == nil {
		return dir
	}
	if pictures, err := knownFolderPath(folderIDPictures); err == nil && pictures != "" {
		screenshots := filepath.Join(pictures, "Screenshots")
		if ensureDir(screenshots) == nil {
			return screenshots
		}
		if ensureDir(pictures) == nil {
			return pictures
		}
	}
	return fallbackScreenshotDir()
}

func knownFolderPath(id xwindows.GUID) (string, error) {
	var path *uint16
	hr, _, callErr := knownFolderSHGetKnownFolderPath.Call(
		uintptr(unsafe.Pointer(&id)),
		0,
		0,
		uintptr(unsafe.Pointer(&path)),
	)
	if hr != 0 {
		return "", fmt.Errorf("SHGetKnownFolderPath %s: HRESULT 0x%08X (%v)", id.String(), uint32(hr), callErr)
	}
	if path == nil {
		return "", fmt.Errorf("SHGetKnownFolderPath %s returned nil", id.String())
	}
	defer knownFolderCoTaskMemFree.Call(uintptr(unsafe.Pointer(path)))
	return xwindows.UTF16PtrToString(path), nil
}

func ensureDir(dir string) error {
	if dir == "" {
		return fmt.Errorf("empty directory")
	}
	return os.MkdirAll(dir, 0o755)
}

func fallbackScreenshotDir() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		screenshots := filepath.Join(home, "Pictures", "Screenshots")
		if ensureDir(screenshots) == nil {
			return screenshots
		}
		pictures := filepath.Join(home, "Pictures")
		if ensureDir(pictures) == nil {
			return pictures
		}
	}
	wd, err := os.Getwd()
	if err == nil {
		return wd
	}
	return "."
}
