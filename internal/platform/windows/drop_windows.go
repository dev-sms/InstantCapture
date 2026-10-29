//go:build windows

package windows

import (
	"fmt"
	"io"
	"runtime"
	"sync"
	"sync/atomic"
	"syscall"
	"unsafe"

	"gioui.org/app"
	"gioui.org/io/event"
	xwindows "golang.org/x/sys/windows"
)

type FileDropKind uint8

const (
	FileDropEnter FileDropKind = iota + 1
	FileDropLeave
	FileDropFiles
)

type FileDropEvent struct {
	Kind  FileDropKind
	Paths []string
}

const (
	cfHDrop         = 15
	dvAspectContent = 1
	tymedHGlobal    = 1
	dropEffectNone  = 0
	dropEffectCopy  = 1

	sOK          = 0
	sFalse       = 1
	eNoInterface = 0x80004002
)

var (
	dropOle32              = syscall.NewLazyDLL("ole32.dll")
	dropShell32            = syscall.NewLazyDLL("shell32.dll")
	procOleInitialize      = dropOle32.NewProc("OleInitialize")
	procOleUninitialize    = dropOle32.NewProc("OleUninitialize")
	procRegisterDragDrop   = dropOle32.NewProc("RegisterDragDrop")
	procRevokeDragDrop     = dropOle32.NewProc("RevokeDragDrop")
	procReleaseStgMedium   = dropOle32.NewProc("ReleaseStgMedium")
	procDragQueryFileW     = dropShell32.NewProc("DragQueryFileW")
	iidIUnknown            = xwindows.GUID{Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	iidIDropTarget         = xwindows.GUID{Data1: 0x00000122, Data4: [8]byte{0xC0, 0, 0, 0, 0, 0, 0, 0x46}}
	nativeDropTargetVTable = iDropTargetVTable{
		QueryInterface: syscall.NewCallback(dropQueryInterface),
		AddRef:         syscall.NewCallback(dropAddRef),
		Release:        syscall.NewCallback(dropRelease),
		DragEnter:      syscall.NewCallback(dropDragEnter),
		DragOver:       syscall.NewCallback(dropDragOver),
		DragLeave:      syscall.NewCallback(dropDragLeave),
		Drop:           syscall.NewCallback(dropFiles),
	}
)

type iDropTargetVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	DragEnter      uintptr
	DragOver       uintptr
	DragLeave      uintptr
	Drop           uintptr
}

type nativeDropTarget struct {
	VTable *iDropTargetVTable
	refs   uint32
	events chan<- FileDropEvent
	active bool
}

type iDataObject struct {
	VTable *iDataObjectVTable
}

type iDataObjectVTable struct {
	QueryInterface uintptr
	AddRef         uintptr
	Release        uintptr
	GetData        uintptr
	GetDataHere    uintptr
	QueryGetData   uintptr
}

type formatETC struct {
	CfFormat uint16
	Ptd      uintptr
	DwAspect uint32
	Lindex   int32
	Tymed    uint32
}

type stgMedium struct {
	Tymed          uint32
	Data           uintptr
	PUnkForRelease uintptr
}

type imageDropRegistration struct {
	window     *app.Window
	hwnd       uintptr
	target     *nativeDropTarget
	oleStarted bool
	closeOnce  sync.Once
}

func NativeWindowHandle(e event.Event) uintptr {
	view, ok := e.(app.Win32ViewEvent)
	if !ok || !view.Valid() {
		return 0
	}
	return view.HWND
}

func RegisterImageDropTarget(window *app.Window, hwnd uintptr, events chan<- FileDropEvent) (io.Closer, error) {
	if window == nil || hwnd == 0 || events == nil {
		return nil, fmt.Errorf("invalid image drop target")
	}
	registration := &imageDropRegistration{window: window, hwnd: hwnd}
	var registerErr error
	window.Run(func() {
		hr, _, _ := procOleInitialize.Call(0)
		if hr != sOK && hr != sFalse {
			registerErr = fmt.Errorf("initialize OLE drag and drop: HRESULT %#x", uint32(hr))
			return
		}
		registration.oleStarted = true
		target := &nativeDropTarget{VTable: &nativeDropTargetVTable, refs: 1, events: events}
		hr, _, _ = procRegisterDragDrop.Call(hwnd, uintptr(unsafe.Pointer(target)))
		if hr != sOK {
			procOleUninitialize.Call()
			registration.oleStarted = false
			registerErr = fmt.Errorf("register image drop target: HRESULT %#x", uint32(hr))
			return
		}
		registration.target = target
	})
	if registerErr != nil {
		return nil, registerErr
	}
	return registration, nil
}

func (r *imageDropRegistration) Close() error {
	r.closeOnce.Do(func() {
		r.window.Run(func() {
			if r.target != nil {
				procRevokeDragDrop.Call(r.hwnd)
				r.target = nil
			}
			if r.oleStarted {
				procOleUninitialize.Call()
				r.oleStarted = false
			}
		})
	})
	return nil
}

func dropQueryInterface(this *nativeDropTarget, iidPtr *xwindows.GUID, objectPtr *uintptr) uintptr {
	if objectPtr == nil {
		return uintptr(eNoInterface)
	}
	*objectPtr = 0
	if this == nil || iidPtr == nil {
		return uintptr(eNoInterface)
	}
	iid := *iidPtr
	if iid != iidIUnknown && iid != iidIDropTarget {
		return uintptr(eNoInterface)
	}
	*objectPtr = uintptr(unsafe.Pointer(this))
	dropAddRef(this)
	return sOK
}

func dropAddRef(this *nativeDropTarget) uintptr {
	return uintptr(atomic.AddUint32(&this.refs, 1))
}

func dropRelease(this *nativeDropTarget) uintptr {
	return uintptr(atomic.AddUint32(&this.refs, ^uint32(0)))
}

func dropDragEnter(target *nativeDropTarget, dataObject *iDataObject, _keyState, _point uintptr, effectPtr *uint32) uintptr {
	target.active = dataObjectSupportsFiles(dataObject)
	setDropEffect(effectPtr, target.active)
	if target.active {
		target.emit(FileDropEvent{Kind: FileDropEnter})
	}
	return sOK
}

func dropDragOver(target *nativeDropTarget, _keyState, _point uintptr, effectPtr *uint32) uintptr {
	setDropEffect(effectPtr, target.active)
	return sOK
}

func dropDragLeave(target *nativeDropTarget) uintptr {
	if target.active {
		target.emit(FileDropEvent{Kind: FileDropLeave})
	}
	target.active = false
	return sOK
}

func dropFiles(target *nativeDropTarget, dataObject *iDataObject, _keyState, _point uintptr, effectPtr *uint32) uintptr {
	paths := dataObjectFilePaths(dataObject)
	setDropEffect(effectPtr, len(paths) > 0)
	if len(paths) > 0 {
		target.emit(FileDropEvent{Kind: FileDropFiles, Paths: paths})
	} else if target.active {
		target.emit(FileDropEvent{Kind: FileDropLeave})
	}
	target.active = false
	return sOK
}

func (t *nativeDropTarget) emit(event FileDropEvent) {
	select {
	case t.events <- event:
	default:
	}
}

func setDropEffect(effectPtr *uint32, accepted bool) {
	if effectPtr == nil {
		return
	}
	effect := uint32(dropEffectNone)
	if accepted {
		effect = dropEffectCopy
	}
	*effectPtr = effect
}

func dataObjectSupportsFiles(dataObject *iDataObject) bool {
	if dataObject == nil || dataObject.VTable == nil || dataObject.VTable.QueryGetData == 0 {
		return false
	}
	format := fileDropFormat()
	hr, _, _ := syscall.SyscallN(dataObject.VTable.QueryGetData, uintptr(unsafe.Pointer(dataObject)), uintptr(unsafe.Pointer(&format)))
	runtime.KeepAlive(format)
	return hr == sOK
}

func dataObjectFilePaths(dataObject *iDataObject) []string {
	if dataObject == nil || dataObject.VTable == nil || dataObject.VTable.GetData == 0 {
		return nil
	}
	format := fileDropFormat()
	medium := stgMedium{}
	hr, _, _ := syscall.SyscallN(dataObject.VTable.GetData, uintptr(unsafe.Pointer(dataObject)), uintptr(unsafe.Pointer(&format)), uintptr(unsafe.Pointer(&medium)))
	if hr != sOK || medium.Tymed != tymedHGlobal || medium.Data == 0 {
		return nil
	}
	defer procReleaseStgMedium.Call(uintptr(unsafe.Pointer(&medium)))

	count, _, _ := procDragQueryFileW.Call(medium.Data, ^uintptr(0), 0, 0)
	paths := make([]string, 0, int(count))
	for index := uintptr(0); index < count; index++ {
		length, _, _ := procDragQueryFileW.Call(medium.Data, index, 0, 0)
		if length == 0 {
			continue
		}
		buffer := make([]uint16, int(length)+1)
		procDragQueryFileW.Call(medium.Data, index, uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)))
		paths = append(paths, syscall.UTF16ToString(buffer))
	}
	return paths
}

func fileDropFormat() formatETC {
	return formatETC{CfFormat: cfHDrop, DwAspect: dvAspectContent, Lindex: -1, Tymed: tymedHGlobal}
}
