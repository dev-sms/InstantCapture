//go:build windows

package windows

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"unicode/utf16"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

var (
	commonDialog32           = syscall.NewLazyDLL("comdlg32.dll")
	procGetOpenFileNameW     = commonDialog32.NewProc("GetOpenFileNameW")
	procGetSaveFileNameW     = commonDialog32.NewProc("GetSaveFileNameW")
	procCommDlgExtendedError = commonDialog32.NewProc("CommDlgExtendedError")
)

type openFileName struct {
	lStructSize       uint32
	hwndOwner         uintptr
	hInstance         uintptr
	lpstrFilter       *uint16
	lpstrCustomFilter *uint16
	nMaxCustFilter    uint32
	nFilterIndex      uint32
	lpstrFile         *uint16
	nMaxFile          uint32
	lpstrFileTitle    *uint16
	nMaxFileTitle     uint32
	lpstrInitialDir   *uint16
	lpstrTitle        *uint16
	flags             uint32
	nFileOffset       uint16
	nFileExtension    uint16
	lpstrDefExt       *uint16
	lCustData         uintptr
	lpfnHook          uintptr
	lpTemplateName    *uint16
	pvReserved        uintptr
	dwReserved        uint32
	flagsEx           uint32
}

const (
	openFileNameExplorer        = 0x00080000
	openFileNameFileMustExist   = 0x00001000
	openFileNamePathMustExist   = 0x00000800
	openFileNameHideReadOnly    = 0x00000004
	openFileNameNoChangeDir     = 0x00000008
	openFileNameOverwritePrompt = 0x00000002
)

func PickImageFile(initialDir string) (string, bool, error) {
	pathBuffer := make([]uint16, xwindows.MAX_PATH*16)
	filter := doubleNullUTF16("Image Files (*.png;*.jpg;*.jpeg;*.gif;*.bmp;*.webp)", "*.png;*.jpg;*.jpeg;*.gif;*.bmp;*.webp", "All Files (*.*)", "*.*")
	title, err := xwindows.UTF16PtrFromString("Choose image to edit in InstantCapture")
	if err != nil {
		return "", false, fmt.Errorf("prepare image dialog title: %w", err)
	}

	var initialDirPtr *uint16
	if initialDir != "" {
		if info, statErr := os.Stat(initialDir); statErr == nil && info.IsDir() {
			initialDirPtr, err = xwindows.UTF16PtrFromString(initialDir)
			if err != nil {
				return "", false, fmt.Errorf("prepare image dialog initial dir: %w", err)
			}
		}
	}

	ofn := openFileName{
		lStructSize:     uint32(unsafe.Sizeof(openFileName{})),
		lpstrFilter:     &filter[0],
		nFilterIndex:    1,
		lpstrFile:       &pathBuffer[0],
		nMaxFile:        uint32(len(pathBuffer)),
		lpstrInitialDir: initialDirPtr,
		lpstrTitle:      title,
		flags:           openFileNameExplorer | openFileNameFileMustExist | openFileNamePathMustExist | openFileNameHideReadOnly | openFileNameNoChangeDir,
	}

	ret, _, callErr := procGetOpenFileNameW.Call(uintptr(unsafe.Pointer(&ofn)))
	if ret == 0 {
		extended, _, _ := procCommDlgExtendedError.Call()
		if extended == 0 {
			return "", false, nil
		}
		if callErr != syscall.Errno(0) {
			return "", false, fmt.Errorf("pick image file: %w", callErr)
		}
		return "", false, fmt.Errorf("pick image file: common dialog error 0x%04x", extended)
	}

	path := xwindows.UTF16ToString(pathBuffer)
	if path == "" {
		return "", false, nil
	}
	return path, true, nil
}

func PickImageSavePath(initialPath string) (string, bool, error) {
	pathBuffer := make([]uint16, 32768)
	filter := doubleNullUTF16("Image Files (*.png;*.jpg;*.jpeg;*.bmp;*.gif)", "*.png;*.jpg;*.jpeg;*.bmp;*.gif")
	title, err := xwindows.UTF16PtrFromString("Save image as")
	if err != nil {
		return "", false, fmt.Errorf("prepare save dialog title: %w", err)
	}

	initialPath = strings.TrimSpace(initialPath)
	if initialPath != "" {
		encoded, encodeErr := xwindows.UTF16FromString(initialPath)
		if encodeErr != nil {
			return "", false, fmt.Errorf("prepare initial save path: %w", encodeErr)
		}
		if len(encoded) > len(pathBuffer) {
			return "", false, fmt.Errorf("initial save path is too long")
		}
		copy(pathBuffer, encoded)
	}

	var initialDirPtr *uint16
	if dir := filepath.Dir(initialPath); dir != "." && dir != "" {
		if info, statErr := os.Stat(dir); statErr == nil && info.IsDir() {
			initialDirPtr, err = xwindows.UTF16PtrFromString(dir)
			if err != nil {
				return "", false, fmt.Errorf("prepare save dialog initial dir: %w", err)
			}
		}
	}

	defaultExtension := saveDialogDefaultExtension(initialPath)
	defaultExtensionPtr, err := xwindows.UTF16PtrFromString(defaultExtension)
	if err != nil {
		return "", false, fmt.Errorf("prepare default save extension: %w", err)
	}
	of := openFileName{
		lStructSize:     uint32(unsafe.Sizeof(openFileName{})),
		lpstrFilter:     &filter[0],
		nFilterIndex:    1,
		lpstrFile:       &pathBuffer[0],
		nMaxFile:        uint32(len(pathBuffer)),
		lpstrInitialDir: initialDirPtr,
		lpstrTitle:      title,
		lpstrDefExt:     defaultExtensionPtr,
		flags:           openFileNameExplorer | openFileNamePathMustExist | openFileNameHideReadOnly | openFileNameNoChangeDir | openFileNameOverwritePrompt,
	}

	ret, _, _ := procGetSaveFileNameW.Call(uintptr(unsafe.Pointer(&of)))
	if ret == 0 {
		extended, _, _ := procCommDlgExtendedError.Call()
		if extended == 0 {
			return "", false, nil
		}
		return "", false, fmt.Errorf("save image file: common dialog error 0x%04x", extended)
	}

	path := xwindows.UTF16ToString(pathBuffer)
	if path == "" {
		return "", false, nil
	}
	return path, true, nil
}

func saveDialogDefaultExtension(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg":
		return "jpg"
	case ".bmp":
		return "bmp"
	case ".gif":
		return "gif"
	default:
		return "png"
	}
}

func doubleNullUTF16(parts ...string) []uint16 {
	encoded := make([]uint16, 0)
	for _, part := range parts {
		encoded = append(encoded, utf16.Encode([]rune(part))...)
		encoded = append(encoded, 0)
	}
	return append(encoded, 0)
}
