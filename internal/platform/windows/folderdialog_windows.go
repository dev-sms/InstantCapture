//go:build windows

package windows

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"syscall"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

const (
	coinitApartmentThreaded = 0x2
	coinitDisableOLE1DDE    = 0x4
	clsctxInprocServer      = 0x1
	fileOpenOptions         = 0x20 | 0x40 | 0x800 | 0x8
	sigdnFileSystemPath     = 0x80058000
	hresultCancelled        = 0x800704C7
	fileDialogShow          = 3
	fileDialogSetOptions    = 9
	fileDialogGetOptions    = 10
	fileDialogSetFolder     = 12
	fileDialogSetTitle      = 17
	fileDialogGetResult     = 20
	shellItemGetDisplayName = 5
)

var (
	folderDialogOle32               = syscall.NewLazyDLL("ole32.dll")
	folderDialogShell32             = syscall.NewLazyDLL("shell32.dll")
	procFolderCoInitializeEx        = folderDialogOle32.NewProc("CoInitializeEx")
	procFolderCoUninitialize        = folderDialogOle32.NewProc("CoUninitialize")
	procFolderCoCreateInstance      = folderDialogOle32.NewProc("CoCreateInstance")
	procSHCreateItemFromParsingName = folderDialogShell32.NewProc("SHCreateItemFromParsingName")

	clsidFileOpenDialog = xwindows.GUID{Data1: 0xDC1C5A9C, Data2: 0xE88A, Data3: 0x4DDE, Data4: [8]byte{0xA5, 0xA1, 0x60, 0xF8, 0x2A, 0x20, 0xAE, 0xF7}}
	iidIFileOpenDialog  = xwindows.GUID{Data1: 0xD57C7288, Data2: 0xD4AD, Data3: 0x4768, Data4: [8]byte{0xBE, 0x02, 0x9D, 0x96, 0x95, 0x32, 0xD9, 0x60}}
	iidIShellItem       = xwindows.GUID{Data1: 0x43826D1E, Data2: 0xE718, Data3: 0x42EE, Data4: [8]byte{0xBC, 0x55, 0xA1, 0xE2, 0x61, 0xC3, 0x7B, 0xFE}}
)

type folderDialogResult struct {
	path string
	ok   bool
	err  error
}

type comInterface struct {
	vtbl *[30]uintptr
}

func (c *comInterface) call(index int, args ...uintptr) uintptr {
	params := make([]uintptr, 1, len(args)+1)
	params[0] = uintptr(unsafe.Pointer(c))
	params = append(params, args...)
	result, _, _ := syscall.SyscallN(c.vtbl[index], params...)
	return result
}

func (c *comInterface) release() {
	if c != nil {
		c.call(2)
	}
}

func PickFolder(initialDir, ownerTitle string) (string, bool, error) {
	result := make(chan folderDialogResult, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		path, ok, err := pickFolderExplorer(initialDir, ownerTitle)
		result <- folderDialogResult{path: path, ok: ok, err: err}
	}()
	picked := <-result
	return picked.path, picked.ok, picked.err
}

func pickFolderExplorer(initialDir, ownerTitle string) (string, bool, error) {
	hresult, _, _ := procFolderCoInitializeEx.Call(0, coinitApartmentThreaded|coinitDisableOLE1DDE)
	if failedHRESULT(hresult) {
		return "", false, hresultError("initialize folder dialog", hresult)
	}
	defer procFolderCoUninitialize.Call()

	var dialog *comInterface
	hresult, _, _ = procFolderCoCreateInstance.Call(
		uintptr(unsafe.Pointer(&clsidFileOpenDialog)),
		0,
		clsctxInprocServer,
		uintptr(unsafe.Pointer(&iidIFileOpenDialog)),
		uintptr(unsafe.Pointer(&dialog)),
	)
	if failedHRESULT(hresult) {
		return "", false, hresultError("create folder dialog", hresult)
	}
	defer dialog.release()

	var options uint32
	if hresult = dialog.call(fileDialogGetOptions, uintptr(unsafe.Pointer(&options))); failedHRESULT(hresult) {
		return "", false, hresultError("read folder dialog options", hresult)
	}
	if hresult = dialog.call(fileDialogSetOptions, uintptr(options|fileOpenOptions)); failedHRESULT(hresult) {
		return "", false, hresultError("set folder dialog options", hresult)
	}
	title, err := xwindows.UTF16PtrFromString("Choose folder in InstantCapture")
	if err != nil {
		return "", false, fmt.Errorf("prepare folder dialog title: %w", err)
	}
	if hresult = dialog.call(fileDialogSetTitle, uintptr(unsafe.Pointer(title))); failedHRESULT(hresult) {
		return "", false, hresultError("set folder dialog title", hresult)
	}

	initialDir = strings.TrimSpace(initialDir)
	if info, statErr := os.Stat(initialDir); statErr == nil && info.IsDir() {
		initialPath, pathErr := xwindows.UTF16PtrFromString(initialDir)
		if pathErr != nil {
			return "", false, fmt.Errorf("prepare initial folder: %w", pathErr)
		}
		var initialItem *comInterface
		hresult, _, _ = procSHCreateItemFromParsingName.Call(
			uintptr(unsafe.Pointer(initialPath)),
			0,
			uintptr(unsafe.Pointer(&iidIShellItem)),
			uintptr(unsafe.Pointer(&initialItem)),
		)
		if failedHRESULT(hresult) {
			return "", false, hresultError("open initial folder", hresult)
		}
		if hresult = dialog.call(fileDialogSetFolder, uintptr(unsafe.Pointer(initialItem))); failedHRESULT(hresult) {
			initialItem.release()
			return "", false, hresultError("set initial folder", hresult)
		}
		initialItem.release()
	}

	var owner uintptr
	if ownerTitle != "" {
		owner, _ = findWindowByTitle(ownerTitle)
	}
	hresult = dialog.call(fileDialogShow, owner)
	if uint32(hresult) == hresultCancelled {
		return "", false, nil
	}
	if failedHRESULT(hresult) {
		return "", false, hresultError("show folder dialog", hresult)
	}

	var item *comInterface
	if hresult = dialog.call(fileDialogGetResult, uintptr(unsafe.Pointer(&item))); failedHRESULT(hresult) {
		return "", false, hresultError("get selected folder", hresult)
	}
	defer item.release()
	var pathPtr *uint16
	if hresult = item.call(shellItemGetDisplayName, sigdnFileSystemPath, uintptr(unsafe.Pointer(&pathPtr))); failedHRESULT(hresult) {
		return "", false, hresultError("read selected folder path", hresult)
	}
	defer xwindows.CoTaskMemFree(unsafe.Pointer(pathPtr))
	path := xwindows.UTF16PtrToString(pathPtr)
	if path == "" {
		return "", false, fmt.Errorf("selected folder has no file system path")
	}
	return path, true, nil
}

func failedHRESULT(hresult uintptr) bool {
	return int32(uint32(hresult)) < 0
}

func hresultError(action string, hresult uintptr) error {
	return fmt.Errorf("%s: HRESULT 0x%08X", action, uint32(hresult))
}
