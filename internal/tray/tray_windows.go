//go:build windows

package tray

import (
	"fmt"
	"runtime"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type Event int

const (
	EventShow Event = iota + 1
	EventQuit
)

const (
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmCommand       = 0x0111
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wmTrayCallback  = 0x8000 + 1

	nimAdd        = 0x00000000
	nimModify     = 0x00000001
	nimDelete     = 0x00000002
	nimSetVersion = 0x00000004
	nifMessage    = 0x00000001
	nifIcon       = 0x00000002
	nifTip        = 0x00000004
	nifInfo       = 0x00000010
	nifShowTip    = 0x00000080
	notifyVersion = 4
	niifInfo      = 0x00000001

	mfString       = 0x00000000
	tmpRightButton = 0x0002

	trayCommandOpen = 1001
	trayCommandQuit = 1002
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW    = user32.NewProc("RegisterClassExW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procDestroyMenu         = user32.NewProc("DestroyMenu")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procPostMessageW        = user32.NewProc("PostMessageW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procCreateIcon          = user32.NewProc("CreateIcon")
	procLoadImageW          = user32.NewProc("LoadImageW")
	procCopyIcon            = user32.NewProc("CopyIcon")
	procDestroyIcon         = user32.NewProc("DestroyIcon")

	procShellNotifyIconW = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW = kernel32.NewProc("GetModuleHandleW")

	wndProcCallback = syscall.NewCallback(wndProc)

	activeMu sync.Mutex
	active   *Tray
)

type Tray struct {
	hwnd   uintptr
	hicon  uintptr
	events chan<- Event
	ready  chan error
	stop   chan struct{}
}

type wndClassEx struct {
	CbSize        uint32
	Style         uint32
	LpfnWndProc   uintptr
	CbClsExtra    int32
	CbWndExtra    int32
	HInstance     uintptr
	HIcon         uintptr
	HCursor       uintptr
	HbrBackground uintptr
	LpszMenuName  *uint16
	LpszClassName *uint16
	HIconSm       uintptr
}

type point struct {
	X int32
	Y int32
}

type msg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       point
	LPrivate uint32
}

type guid struct {
	Data1 uint32
	Data2 uint16
	Data3 uint16
	Data4 [8]byte
}

type notifyIconData struct {
	CbSize           uint32
	HWnd             uintptr
	UID              uint32
	UFlags           uint32
	UCallbackMessage uint32
	HIcon            uintptr
	SzTip            [128]uint16
	DwState          uint32
	DwStateMask      uint32
	SzInfo           [256]uint16
	UVersion         uint32
	SzInfoTitle      [64]uint16
	DwInfoFlags      uint32
	GuidItem         guid
	HBalloonIcon     uintptr
}

func Start(events chan<- Event) (*Tray, error) {
	t := &Tray{events: events, ready: make(chan error, 1), stop: make(chan struct{})}
	go t.run()
	if err := <-t.ready; err != nil {
		return nil, err
	}
	return t, nil
}

func (t *Tray) Stop() {
	select {
	case <-t.stop:
		return
	default:
		close(t.stop)
	}
	if t.hwnd != 0 {
		procPostMessageW.Call(t.hwnd, uintptr(wmClose), 0, 0)
	}
}

func (t *Tray) Notify(title, message string) error {
	if t == nil || t.hwnd == 0 {
		return fmt.Errorf("tray is not ready")
	}
	data := notifyIconData{
		CbSize:      uint32(unsafe.Sizeof(notifyIconData{})),
		HWnd:        t.hwnd,
		UID:         1,
		UFlags:      nifInfo,
		DwInfoFlags: niifInfo,
	}
	copyNotificationText(data.SzInfoTitle[:], title)
	copyNotificationText(data.SzInfo[:], message)
	ret, _, err := procShellNotifyIconW.Call(uintptr(nimModify), uintptr(unsafe.Pointer(&data)))
	if ret == 0 {
		return fmt.Errorf("show tray notification: %v", err)
	}
	return nil
}

func copyNotificationText(dst []uint16, text string) {
	if len(dst) == 0 {
		return
	}
	encoded := windows.StringToUTF16(text)
	if len(encoded) > len(dst) {
		encoded = encoded[:len(dst)-1]
	}
	n := copy(dst, encoded)
	if n == len(dst) {
		n--
	}
	dst[n] = 0
}

func (t *Tray) run() {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	hwnd, err := createMessageWindow()
	if err != nil {
		t.ready <- err
		return
	}
	t.hwnd = hwnd

	activeMu.Lock()
	active = t
	activeMu.Unlock()
	defer func() {
		activeMu.Lock()
		if active == t {
			active = nil
		}
		activeMu.Unlock()
	}()

	hicon, err := addIcon(hwnd)
	if err != nil {
		t.ready <- err
		procDestroyWindow.Call(hwnd)
		return
	}
	t.hicon = hicon
	t.ready <- nil

	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if ret == 0 || int32(ret) == -1 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func createMessageWindow() (uintptr, error) {
	instance, _, err := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return 0, fmt.Errorf("get module handle: %v", err)
	}
	className, _ := windows.UTF16PtrFromString("InstantCaptureTrayWindow")
	windowName, _ := windows.UTF16PtrFromString("InstantCapture Tray")
	wc := wndClassEx{CbSize: uint32(unsafe.Sizeof(wndClassEx{})), LpfnWndProc: wndProcCallback, HInstance: instance, LpszClassName: className}
	atom, _, regErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && regErr != syscall.Errno(1410) {
		return 0, fmt.Errorf("register tray class: %v", regErr)
	}
	hwnd, _, createErr := procCreateWindowExW.Call(0, uintptr(unsafe.Pointer(className)), uintptr(unsafe.Pointer(windowName)), 0, 0, 0, 0, 0, 0, 0, instance, 0)
	if hwnd == 0 {
		return 0, fmt.Errorf("create tray window: %v", createErr)
	}
	return hwnd, nil
}

func addIcon(hwnd uintptr) (uintptr, error) {
	hicon := createTrayIcon()
	data := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: hwnd, UID: 1, UFlags: nifMessage | nifIcon | nifTip | nifShowTip, UCallbackMessage: wmTrayCallback, HIcon: hicon, UVersion: notifyVersion}
	copy(data.SzTip[:], windows.StringToUTF16("InstantCapture - 화면 캡처"))
	ret, _, err := procShellNotifyIconW.Call(uintptr(nimAdd), uintptr(unsafe.Pointer(&data)))
	if ret == 0 {
		if hicon != 0 {
			procDestroyIcon.Call(hicon)
		}
		return 0, fmt.Errorf("add tray icon: %v", err)
	}
	procShellNotifyIconW.Call(uintptr(nimSetVersion), uintptr(unsafe.Pointer(&data)))
	return hicon, nil
}

func deleteIcon(hwnd uintptr) {
	data := notifyIconData{CbSize: uint32(unsafe.Sizeof(notifyIconData{})), HWnd: hwnd, UID: 1}
	procShellNotifyIconW.Call(uintptr(nimDelete), uintptr(unsafe.Pointer(&data)))
}

func wndProc(hwnd uintptr, message uint32, wParam, lParam uintptr) uintptr {
	switch message {
	case wmTrayCallback:
		callback := lowWord(lParam)
		if callback == wmLButtonUp || callback == wmLButtonDblClk {
			if t := current(); t != nil {
				t.emit(EventShow)
			}
			return 0
		}
		if callback == wmRButtonUp {
			showContextMenu(hwnd)
			return 0
		}
	case wmCommand:
		switch lowWord(wParam) {
		case trayCommandOpen:
			if t := current(); t != nil {
				t.emit(EventShow)
			}
			return 0
		case trayCommandQuit:
			if t := current(); t != nil {
				t.emit(EventQuit)
			}
			return 0
		}
	case wmClose:
		deleteIcon(hwnd)
		if t := current(); t != nil && t.hicon != 0 {
			procDestroyIcon.Call(t.hicon)
			t.hicon = 0
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(message), wParam, lParam)
	return ret
}

func (t *Tray) emit(event Event) {
	select {
	case t.events <- event:
	default:
	}
}

func showContextMenu(hwnd uintptr) {
	menu, _, _ := procCreatePopupMenu.Call()
	if menu == 0 {
		return
	}
	defer procDestroyMenu.Call(menu)
	openText, _ := windows.UTF16PtrFromString("Open")
	quitText, _ := windows.UTF16PtrFromString("Quit")
	procAppendMenuW.Call(menu, uintptr(mfString), uintptr(trayCommandOpen), uintptr(unsafe.Pointer(openText)))
	procAppendMenuW.Call(menu, uintptr(mfString), uintptr(trayCommandQuit), uintptr(unsafe.Pointer(quitText)))
	var cursor point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&cursor)))
	procSetForegroundWindow.Call(hwnd)
	procTrackPopupMenu.Call(menu, uintptr(tmpRightButton), uintptr(uint32(cursor.X)), uintptr(uint32(cursor.Y)), 0, hwnd, 0)
}

func createTrayIcon() uintptr {
	instance, _, _ := procGetModuleHandleW.Call(0)
	if instance != 0 {
		const imageIcon = 1
		resource, _, _ := procLoadImageW.Call(instance, 1, imageIcon, 32, 32, 0)
		if resource != 0 {
			if icon, _, _ := procCopyIcon.Call(resource); icon != 0 {
				return icon
			}
		}
	}
	return createFallbackTrayIcon()
}

func createFallbackTrayIcon() uintptr {
	andMask := make([]byte, 32*32/8)
	xorMask := make([]byte, 32*32/8)
	for i := range andMask {
		andMask[i] = 0x00
	}
	setIconRect(xorMask, 5, 5, 26, 26)
	setIconRect(xorMask, 9, 9, 12, 22)
	setIconRect(xorMask, 15, 9, 22, 12)
	setIconRect(xorMask, 18, 12, 22, 22)
	hicon, _, _ := procCreateIcon.Call(0, 32, 32, 1, 1, uintptr(unsafe.Pointer(&andMask[0])), uintptr(unsafe.Pointer(&xorMask[0])))
	return hicon
}

func setIconRect(mask []byte, minX, minY, maxX, maxY int) {
	for y := minY; y <= maxY; y++ {
		for x := minX; x <= maxX; x++ {
			setIconBit(mask, x, y)
		}
	}
}

func setIconBit(mask []byte, x, y int) {
	if x < 0 || x >= 32 || y < 0 || y >= 32 {
		return
	}
	idx := y*4 + x/8
	mask[idx] |= 0x80 >> uint(x%8)
}

func lowWord(v uintptr) uint32 {
	return uint32(v & 0xffff)
}

func current() *Tray {
	activeMu.Lock()
	defer activeMu.Unlock()
	return active
}
