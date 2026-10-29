//go:build windows

package windows

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

var (
	shell32                = xwindows.NewLazySystemDLL("shell32.dll")
	procSHFileOperationW   = shell32.NewProc("SHFileOperationW")
	recyclePaths           = recycleFilesNative
	invokeSHFileOperationW = callSHFileOperationW
)

const (
	fileOperationDelete         = 0x0003
	fileOperationSilent         = 0x0004
	fileOperationNoConfirmation = 0x0010
	fileOperationAllowUndo      = 0x0040
	fileOperationNoConfirmMkdir = 0x0200
	fileOperationNoErrorUI      = 0x0400
	recycleOperationFlags       = fileOperationSilent | fileOperationNoConfirmation | fileOperationAllowUndo | fileOperationNoConfirmMkdir | fileOperationNoErrorUI
)

type shFileOperation struct {
	hwnd                  uintptr
	wFunc                 uint32
	pFrom                 *uint16
	pTo                   *uint16
	fFlags                uint16
	fAnyOperationsAborted int32
	hNameMappings         uintptr
	lpszProgressTitle     *uint16
}

func MoveFileToRecycleBin(path string) error {
	return MoveFilesToRecycleBin([]string{path})
}

func MoveFilesToRecycleBin(paths []string) error {
	normalized, err := normalizeRecyclePaths(paths)
	if err != nil {
		return err
	}
	for _, path := range normalized {
		info, err := os.Stat(path)
		if err != nil {
			return fmt.Errorf("stat recycle file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("only regular files can be moved to the recycle bin")
		}
	}
	if len(normalized) == 0 {
		return nil
	}
	if err := recyclePaths(normalized); err != nil {
		return fmt.Errorf("move file to recycle bin: %w", err)
	}
	return nil
}

func recycleFileNative(path string) error {
	return recycleFilesNative([]string{path})
}

func recycleFilesNative(input []string) error {
	paths, err := normalizeRecyclePaths(input)
	if err != nil {
		return err
	}
	if len(paths) == 0 {
		return nil
	}
	from, err := doubleNullTerminatedPaths(paths)
	if err != nil {
		return err
	}
	operation := shFileOperation{
		wFunc:  fileOperationDelete,
		pFrom:  &from[0],
		fFlags: recycleOperationFlags,
	}
	result, err := invokeSHFileOperationW(&operation)
	runtime.KeepAlive(from)
	if err != nil {
		return err
	}
	if result != 0 {
		return fmt.Errorf("SHFileOperationW failed with code 0x%08x", uint32(result))
	}
	if operation.fAnyOperationsAborted != 0 {
		return errors.New("SHFileOperationW was aborted")
	}
	return nil
}

func normalizeRecyclePaths(paths []string) ([]string, error) {
	normalized := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		absPath, err := filepath.Abs(path)
		if err != nil {
			return nil, fmt.Errorf("resolve recycle path: %w", err)
		}
		absPath = filepath.Clean(absPath)
		key := strings.ToLower(absPath)
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		normalized = append(normalized, absPath)
	}
	return normalized, nil
}

func doubleNullTerminatedPaths(paths []string) ([]uint16, error) {
	encoded := make([]uint16, 0)
	for _, path := range paths {
		utf16Path, err := xwindows.UTF16FromString(path)
		if err != nil {
			return nil, fmt.Errorf("encode recycle path: %w", err)
		}
		encoded = append(encoded, utf16Path...)
	}
	return append(encoded, 0), nil
}

func callSHFileOperationW(operation *shFileOperation) (int32, error) {
	if err := procSHFileOperationW.Find(); err != nil {
		return 0, fmt.Errorf("load SHFileOperationW: %w", err)
	}
	result, _, _ := procSHFileOperationW.Call(uintptr(unsafe.Pointer(operation)))
	return int32(result), nil
}
