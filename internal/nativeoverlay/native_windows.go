//go:build windows

package nativeoverlay

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"instantcapture/internal/overlay"
	platformwindows "instantcapture/internal/platform/windows"
)

const (
	wsPopup   = 0x80000000
	wsVisible = 0x10000000

	wsExTopMost     = 0x00000008
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	wsExNoActivate  = 0x08000000
	gwlExStyle      = -20

	wmDestroy        = 0x0002
	wmSetCursor      = 0x0020
	wmClose          = 0x0010
	wmEraseBkgnd     = 0x0014
	wmPaint          = 0x000F
	wmKeyDown        = 0x0100
	wmTimer          = 0x0113
	wmLButtonDown    = 0x0201
	wmLButtonUp      = 0x0202
	wmRButtonDown    = 0x0204
	wmRButtonUp      = 0x0205
	wmMouseMove      = 0x0200
	wmMouseWheel     = 0x020A
	wmScrollObserved = 0x8001
	whMouseLL        = 14
	hcAction         = 0

	vkEscape   = 0x1B
	vkLButton  = 0x01
	vkLeft     = 0x25
	vkUp       = 0x26
	vkRight    = 0x27
	vkDown     = 0x28
	wheelDelta = 120
	idcCross   = 32515

	scrollEdgeThreshold              = 36
	scrollStepPixels                 = 360
	scrollTickInterval               = 100 * time.Millisecond
	scrollTimerID                    = 1
	scrollTimerInterval              = 50
	scrollScoreTopSkipMax            = 96
	scrollScoreBottomSkipMax         = 48
	scrollScoreUnchangedRowThreshold = 2
	scrollScoreMinSamples            = 16
	scrollScoreMaxAccepted           = 24
	scrollScoreAlternativeGap        = 4
	scrollScoreStaticMax             = 1
	smtoAbortIfHung                  = 0x0002
	wheelMessageTimeout              = 250
	cwpSkipInvisible                 = 0x0001
	cwpSkipDisabled                  = 0x0002
	cwpSkipTransparent               = 0x0004
	inputMouse                       = 0
	mouseEventWheel                  = 0x0800
	scrollSyntheticInputTag          = 0x49435343

	swHide           = 0
	swShowNoActivate = 4
	swShow           = 5
	swpNoActivate    = 0x0010
	swpNoSize        = 0x0001
	swpNoMove        = 0x0002
	swpNoZOrder      = 0x0004
	swpFrameChanged  = 0x0020
	swpShowWindow    = 0x0040

	lwaAlpha              = 0x00000002
	wdaExcludeFromCapture = 0x00000011

	dibRGBColors = 0
	biRGB        = 0
	srcCopy      = 0x00CC0020

	psSolid               = 0
	nullBrush             = 5
	transparentBackground = 1
	dtCenter              = 0x00000001
	dtVCenter             = 0x00000004
	dtSingleLine          = 0x00000020
	dtNoPrefix            = 0x00000800
)

var (
	nativeUser32   = syscall.NewLazyDLL("user32.dll")
	nativeGDI32    = syscall.NewLazyDLL("gdi32.dll")
	nativeKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetModuleHandleW   = nativeKernel32.NewProc("GetModuleHandleW")
	procGetCurrentThreadID = nativeKernel32.NewProc("GetCurrentThreadId")

	procRegisterClassExW             = nativeUser32.NewProc("RegisterClassExW")
	procCreateWindowExW              = nativeUser32.NewProc("CreateWindowExW")
	procDefWindowProcW               = nativeUser32.NewProc("DefWindowProcW")
	procGetWindowLongPtrW            = nativeUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW            = nativeUser32.NewProc("SetWindowLongPtrW")
	procFillRect                     = nativeUser32.NewProc("FillRect")
	procDestroyWindow                = nativeUser32.NewProc("DestroyWindow")
	procShowWindow                   = nativeUser32.NewProc("ShowWindow")
	procUpdateWindow                 = nativeUser32.NewProc("UpdateWindow")
	procGetMessageW                  = nativeUser32.NewProc("GetMessageW")
	procTranslateMessage             = nativeUser32.NewProc("TranslateMessage")
	procDispatchMessageW             = nativeUser32.NewProc("DispatchMessageW")
	procPostQuitMessage              = nativeUser32.NewProc("PostQuitMessage")
	procPostMessageW                 = nativeUser32.NewProc("PostMessageW")
	procSendMessageTimeoutW          = nativeUser32.NewProc("SendMessageTimeoutW")
	procBeginPaint                   = nativeUser32.NewProc("BeginPaint")
	procEndPaint                     = nativeUser32.NewProc("EndPaint")
	procInvalidateRect               = nativeUser32.NewProc("InvalidateRect")
	procSetWindowPos                 = nativeUser32.NewProc("SetWindowPos")
	procSetLayeredWindowAttribute    = nativeUser32.NewProc("SetLayeredWindowAttributes")
	procSetWindowDisplayAffinity     = nativeUser32.NewProc("SetWindowDisplayAffinity")
	procSetThreadDPIAwarenessContext = nativeUser32.NewProc("SetThreadDpiAwarenessContext")
	procSetCapture                   = nativeUser32.NewProc("SetCapture")
	procReleaseCapture               = nativeUser32.NewProc("ReleaseCapture")
	procSetTimer                     = nativeUser32.NewProc("SetTimer")
	procKillTimer                    = nativeUser32.NewProc("KillTimer")
	procSetForegroundWindow          = nativeUser32.NewProc("SetForegroundWindow")
	procGetWindowThreadProcessID     = nativeUser32.NewProc("GetWindowThreadProcessId")
	procAttachThreadInput            = nativeUser32.NewProc("AttachThreadInput")
	procGetGUIThreadInfo             = nativeUser32.NewProc("GetGUIThreadInfo")
	procGetCursorPos                 = nativeUser32.NewProc("GetCursorPos")
	procLoadCursorW                  = nativeUser32.NewProc("LoadCursorW")
	procSetCursor                    = nativeUser32.NewProc("SetCursor")
	procSetCursorPos                 = nativeUser32.NewProc("SetCursorPos")
	procSendInput                    = nativeUser32.NewProc("SendInput")
	procMouseEvent                   = nativeUser32.NewProc("mouse_event")
	procGetMessageExtraInfo          = nativeUser32.NewProc("GetMessageExtraInfo")
	procGetAsyncKeyState             = nativeUser32.NewProc("GetAsyncKeyState")
	procSetWindowsHookExW            = nativeUser32.NewProc("SetWindowsHookExW")
	procUnhookWindowsHookEx          = nativeUser32.NewProc("UnhookWindowsHookEx")
	procCallNextHookEx               = nativeUser32.NewProc("CallNextHookEx")
	procGetDC                        = nativeUser32.NewProc("GetDC")
	procReleaseDC                    = nativeUser32.NewProc("ReleaseDC")
	procGetClientRect                = nativeUser32.NewProc("GetClientRect")
	procWindowFromPoint              = nativeUser32.NewProc("WindowFromPoint")
	procScreenToClient               = nativeUser32.NewProc("ScreenToClient")
	procClientToScreen               = nativeUser32.NewProc("ClientToScreen")
	procChildWindowFromPointEx       = nativeUser32.NewProc("ChildWindowFromPointEx")
	procDrawTextW                    = nativeUser32.NewProc("DrawTextW")

	procStretchDIBits          = nativeGDI32.NewProc("StretchDIBits")
	procCreateCompatibleDC     = nativeGDI32.NewProc("CreateCompatibleDC")
	procDeleteDC               = nativeGDI32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = nativeGDI32.NewProc("CreateCompatibleBitmap")
	procBitBlt                 = nativeGDI32.NewProc("BitBlt")
	procGetDIBits              = nativeGDI32.NewProc("GetDIBits")
	procCreateSolidBrush       = nativeGDI32.NewProc("CreateSolidBrush")
	procCreatePen              = nativeGDI32.NewProc("CreatePen")
	procSelectObject           = nativeGDI32.NewProc("SelectObject")
	procSaveDC                 = nativeGDI32.NewProc("SaveDC")
	procRestoreDC              = nativeGDI32.NewProc("RestoreDC")
	procExcludeClipRect        = nativeGDI32.NewProc("ExcludeClipRect")
	procRectangle              = nativeGDI32.NewProc("Rectangle")
	procMoveToEx               = nativeGDI32.NewProc("MoveToEx")
	procLineTo                 = nativeGDI32.NewProc("LineTo")
	procDeleteObject           = nativeGDI32.NewProc("DeleteObject")
	procGetStockObject         = nativeGDI32.NewProc("GetStockObject")
	procSetBkMode              = nativeGDI32.NewProc("SetBkMode")
	procSetTextColor           = nativeGDI32.NewProc("SetTextColor")

	wndProcCallback   = syscall.NewCallback(nativeWndProc)
	mouseHookCallback = syscall.NewCallback(nativeMouseHookProc)

	activeMu      sync.Mutex
	activeSession *nativeSession
	overlayCursor uintptr
)

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

type nativePoint struct {
	X int32
	Y int32
}

type nativeRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

type nativeMsg struct {
	Hwnd     uintptr
	Message  uint32
	WParam   uintptr
	LParam   uintptr
	Time     uint32
	Pt       nativePoint
	LPrivate uint32
}

type nativeMouseInput struct {
	Dx          int32
	Dy          int32
	MouseData   uint32
	DwFlags     uint32
	Time        uint32
	DwExtraInfo uintptr
}

type lowLevelMouseHook struct {
	Pt          nativePoint
	MouseData   uint32
	Flags       uint32
	Time        uint32
	DwExtraInfo uintptr
}

type nativeInput struct {
	Type uint32
	Mi   nativeMouseInput
}

type guiThreadInfo struct {
	CbSize        uint32
	Flags         uint32
	HwndActive    uintptr
	HwndFocus     uintptr
	HwndCapture   uintptr
	HwndMenuOwner uintptr
	HwndMoveSize  uintptr
	HwndCaret     uintptr
	RcCaret       nativeRect
}

type paintStruct struct {
	Hdc         uintptr
	FErase      int32
	RcPaint     nativeRect
	FRestore    int32
	FIncUpdate  int32
	RgbReserved [32]byte
}

type bitmapInfoHeader struct {
	BiSize          uint32
	BiWidth         int32
	BiHeight        int32
	BiPlanes        uint16
	BiBitCount      uint16
	BiCompression   uint32
	BiSizeImage     uint32
	BiXPelsPerMeter int32
	BiYPelsPerMeter int32
	BiClrUsed       uint32
	BiClrImportant  uint32
}

type bitmapInfo struct {
	Header bitmapInfoHeader
	Colors [3]uint32
}

type nativeResult struct {
	selection Selection
	ok        bool
	err       error
}

type nativeSession struct {
	hwnd                  uintptr
	result                chan nativeResult
	finished              bool
	imgBounds             image.Rectangle
	virtualMin            image.Point
	winWidth              int
	winHeight             int
	bgra                  []byte
	original              []byte
	highlight             []byte
	highlightHover        int
	highlightValid        bool
	bmi                   bitmapInfo
	hasCursor             bool
	cursor                image.Point
	selecting             bool
	start                 image.Point
	current               image.Point
	mode                  string
	liveBackground        bool
	displays              []image.Rectangle
	windows               []nativeWindow
	hover                 int
	scrollWin             int
	scrollOffsetY         int
	scrollWheelSteps      int
	scrollWheelUnits      int
	scrollStartContent    image.Point
	scrollCurrentContent  image.Point
	scrollMaxContentY     int
	scrollMaxOffsetY      int
	scrollMaxWheelSteps   int
	scrollRangeEndViewY   int
	lastScroll            time.Time
	scrollFallbackStep    int
	scrollLastDelta       int
	scrollLastMeasured    bool
	scrollNoMotion        bool
	scrollWheelPoint      image.Point
	scrollOriginSample    scrollSample
	scrollPassiveSample   scrollSample
	scrollSampleRect      image.Rectangle
	scrollRestoring       bool
	scrollOriginalExStyle uintptr
	scrollPassthrough     bool
	scrollWheelSender     func(uintptr, int, image.Point) bool
	scrollSampleCapture   func(image.Rectangle) scrollSample
	scrollInitialPosition *platformwindows.ScrollPosition
	scrollPositionQuery   func(platformwindows.WindowTarget, image.Point) (platformwindows.ScrollPosition, error)
	scrollPositionRestore func(platformwindows.WindowTarget, platformwindows.ScrollPosition) error
	scrollHookWheelCount  int
	scrollHandledWheels   int
	scrollSentWheels      int
	mouseHook             uintptr
	polygon               []image.Point
	magnifierDC           uintptr
	magnifierBitmap       uintptr
	magnifierOldBitmap    uintptr
	selectionColor        uint32
}

type nativeWindow struct {
	handle       uintptr
	rect         image.Rectangle
	visibleRects []image.Rectangle
}

type scrollSample struct {
	width  int
	height int
	luma   []byte
}

type scrollDeltaCandidate struct {
	delta int
	score int64
}

type scrollMotionResult uint8

const (
	scrollMotionNone scrollMotionResult = iota
	scrollMotionMeasured
	scrollMotionEstimated
)

func selectNative(ctx context.Context, img image.Image, opt overlay.Options) (Selection, bool, error) {
	if img == nil {
		return Selection{}, false, fmt.Errorf("%w: nil image", ErrUnavailable)
	}
	virtualScreen := opt.VirtualScreen
	if virtualScreen.Empty() {
		virtualScreen = img.Bounds()
	}
	if virtualScreen.Empty() {
		return Selection{}, false, fmt.Errorf("%w: empty virtual screen", ErrUnavailable)
	}

	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	previousDPIContext, _, _ := procSetThreadDPIAwarenessContext.Call(^uintptr(3))
	if previousDPIContext != 0 {
		defer procSetThreadDPIAwarenessContext.Call(previousDPIContext)
	}
	virtualScreen = platformwindows.PreferMatchingNativeVirtualScreen(
		platformwindows.NativeVirtualScreenBounds(),
		virtualScreen,
		img.Bounds(),
	)

	instance, _, err := procGetModuleHandleW.Call(0)
	if instance == 0 {
		return Selection{}, false, fmt.Errorf("%w: get module handle: %v", ErrUnavailable, err)
	}

	className, _ := windows.UTF16PtrFromString("InstantCaptureNativeOverlay")
	windowName, _ := windows.UTF16PtrFromString("InstantCapture Native Overlay")
	if err := registerOverlayClass(instance, className); err != nil {
		return Selection{}, false, err
	}

	session, err := newNativeSession(img, virtualScreen, opt.DimAlpha)
	if err != nil {
		return Selection{}, false, err
	}
	session.mode = opt.CaptureMode
	session.selectionColor = colorRef(opt.SelectionColor.R, opt.SelectionColor.G, opt.SelectionColor.B)
	session.liveBackground = opt.LiveBackground
	for _, display := range opt.Displays {
		imageRect := display.Sub(virtualScreen.Min).Intersect(session.imgBounds)
		if !imageRect.Empty() {
			session.displays = append(session.displays, imageRect)
		}
	}
	if len(opt.WindowRegions) > 0 {
		for _, region := range opt.WindowRegions {
			imageRect := region.Rect.Sub(virtualScreen.Min).Intersect(session.imgBounds)
			if imageRect.Empty() {
				continue
			}
			visibleRects := make([]image.Rectangle, 0, len(region.VisibleRects))
			for _, visible := range region.VisibleRects {
				visibleRect := visible.Sub(virtualScreen.Min).Intersect(session.imgBounds)
				if !visibleRect.Empty() {
					visibleRects = append(visibleRects, visibleRect)
				}
			}
			if len(visibleRects) == 0 {
				continue
			}
			session.windows = append(session.windows, nativeWindow{handle: region.Handle, rect: imageRect, visibleRects: visibleRects})
		}
	} else {
		for _, rect := range opt.WindowRects {
			imageRect := rect.Sub(virtualScreen.Min).Intersect(session.imgBounds)
			if !imageRect.Empty() {
				session.windows = append(session.windows, nativeWindow{rect: imageRect, visibleRects: []image.Rectangle{imageRect}})
			}
		}
	}

	activeMu.Lock()
	activeSession = session
	activeMu.Unlock()
	defer func() {
		activeMu.Lock()
		if activeSession == session {
			activeSession = nil
		}
		activeMu.Unlock()
	}()

	exStyle := uintptr(wsExTopMost | wsExLayered | wsExToolWindow)
	if session.mode == "Scroll" {
		exStyle |= uintptr(wsExTransparent | wsExNoActivate)
	}
	hwnd, _, callErr := procCreateWindowExW.Call(
		exStyle,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(wsPopup),
		intArg(virtualScreen.Min.X),
		intArg(virtualScreen.Min.Y),
		uintptr(virtualScreen.Dx()),
		uintptr(virtualScreen.Dy()),
		0,
		0,
		instance,
		0,
	)
	if hwnd == 0 {
		return Selection{}, false, fmt.Errorf("%w: create window: %v", ErrUnavailable, callErr)
	}
	session.hwnd = hwnd
	if session.mode == "Scroll" {
		procSetWindowDisplayAffinity.Call(hwnd, uintptr(wdaExcludeFromCapture))
		hook, _, hookErr := procSetWindowsHookExW.Call(uintptr(whMouseLL), mouseHookCallback, instance, 0)
		if hook == 0 {
			procDestroyWindow.Call(hwnd)
			return Selection{}, false, fmt.Errorf("%w: install scroll mouse hook: %v", ErrUnavailable, hookErr)
		}
		session.mouseHook = hook
		defer procUnhookWindowsHookEx.Call(hook)
	}

	alpha := uintptr(245)
	if opt.LiveBackground {
		alpha = 105
	}
	procSetLayeredWindowAttribute.Call(hwnd, 0, alpha, uintptr(lwaAlpha))
	positionFlags := uintptr(swpShowWindow)
	showCommand := uintptr(swShow)
	if session.mode == "Scroll" {
		positionFlags |= uintptr(swpNoActivate)
		showCommand = uintptr(swShowNoActivate)
	}
	procSetWindowPos.Call(hwnd, ^uintptr(0), intArg(virtualScreen.Min.X), intArg(virtualScreen.Min.Y), uintptr(virtualScreen.Dx()), uintptr(virtualScreen.Dy()), positionFlags)
	procShowWindow.Call(hwnd, showCommand)
	procSetCursor.Call(overlayCursor)
	procUpdateWindow.Call(hwnd)

	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			procPostMessageW.Call(hwnd, uintptr(wmClose), 0, 0)
		case <-done:
		}
	}()

	var msg nativeMsg
	for {
		ret, _, msgErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) == -1 {
			procDestroyWindow.Call(hwnd)
			return Selection{}, false, fmt.Errorf("get message: %v", msgErr)
		}
		if ret == 0 {
			break
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}

	select {
	case result := <-session.result:
		return result.selection, result.ok, result.err
	default:
		return Selection{}, false, nil
	}
}

func registerOverlayClass(instance uintptr, className *uint16) error {
	cursor, _, cursorErr := procLoadCursorW.Call(0, uintptr(idcCross))
	if cursor == 0 {
		return fmt.Errorf("%w: load crosshair cursor: %v", ErrUnavailable, cursorErr)
	}
	overlayCursor = cursor
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProcCallback,
		HInstance:     instance,
		HCursor:       cursor,
		LpszClassName: className,
	}
	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && err != syscall.Errno(1410) {
		return fmt.Errorf("%w: register window class: %v", ErrUnavailable, err)
	}
	return nil
}

func newNativeSession(img image.Image, virtualScreen image.Rectangle, dimAlpha uint8) (*nativeSession, error) {
	bounds := img.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	bgra := make([]byte, width*height*4)
	original := make([]byte, len(bgra))
	dim := 1 - (float64(dimAlpha)/255.0)*0.65
	if dim < 0.2 {
		dim = 0.2
	}
	if rgba, ok := img.(*image.RGBA); ok {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				src := rgba.PixOffset(bounds.Min.X+x, bounds.Min.Y+y)
				dst := (y*width + x) * 4
				original[dst+0] = rgba.Pix[src+2]
				original[dst+1] = rgba.Pix[src+1]
				original[dst+2] = rgba.Pix[src+0]
				original[dst+3] = rgba.Pix[src+3]
			}
		}
	} else {
		for y := 0; y < height; y++ {
			for x := 0; x < width; x++ {
				pixel := color.RGBAModel.Convert(img.At(bounds.Min.X+x, bounds.Min.Y+y)).(color.RGBA)
				dst := (y*width + x) * 4
				original[dst+0] = pixel.B
				original[dst+1] = pixel.G
				original[dst+2] = pixel.R
				original[dst+3] = pixel.A
			}
		}
	}
	for dst := 0; dst < len(original); dst += 4 {
		bgra[dst+0] = uint8(float64(original[dst+0]) * dim)
		bgra[dst+1] = uint8(float64(original[dst+1]) * dim)
		bgra[dst+2] = uint8(float64(original[dst+2]) * dim)
		bgra[dst+3] = original[dst+3]
	}
	bmi := bitmapInfo{}
	bmi.Header = bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(width),
		BiHeight:      -int32(height),
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: biRGB,
	}
	return &nativeSession{
		result:     make(chan nativeResult, 1),
		imgBounds:  image.Rect(0, 0, width, height),
		virtualMin: virtualScreen.Min,
		winWidth:   virtualScreen.Dx(),
		winHeight:  virtualScreen.Dy(),
		bgra:       bgra,
		original:   original,
		highlight:  make([]byte, len(bgra)),
		bmi:        bmi,
		hover:      -1,
		scrollWin:  -1,
	}, nil
}

func nativeWndProc(hwnd uintptr, msg uint32, wParam, lParam uintptr) uintptr {
	s := currentSession()
	switch msg {
	case wmEraseBkgnd:
		return 1
	case wmSetCursor:
		if s != nil && overlayCursor != 0 {
			procSetCursor.Call(overlayCursor)
			return 1
		}
	case wmPaint:
		if s != nil {
			s.paint(hwnd)
			return 0
		}
	case wmLButtonDown:
		if s != nil {
			if s.mode == "Window" {
				if s.hover >= 0 && s.hover < len(s.windows) {
					s.finish(Selection{Rect: s.windows[s.hover].rect}, true, nil)
					procDestroyWindow.Call(hwnd)
				}
				return 0
			}
			if s.mode == "Scroll" {
				s.cursor = s.clientToImage(lParamPoint(lParam))
				s.hasCursor = true
				s.hover = s.windowAt(s.cursor)
				if s.hover < 0 || s.hover >= len(s.windows) {
					return 0
				}
				s.scrollWin = s.hover
				s.selecting = true
				s.start = s.cursor
				s.current = s.cursor
				s.beginScrollSelection()
				procSetTimer.Call(hwnd, uintptr(scrollTimerID), uintptr(scrollTimerInterval), 0)
				procInvalidateRect.Call(hwnd, 0, 0)
				return 0
			}
			s.selecting = true
			s.cursor = s.clientToImage(lParamPoint(lParam))
			s.hasCursor = true
			s.start = s.cursor
			s.current = s.start
			s.polygon = []image.Point{s.cursor}
			procSetCapture.Call(hwnd)
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case wmMouseMove:
		if s != nil {
			if s.mode == "Scroll" {
				return 0
			}
			clientW, clientH := s.currentClientSize()
			before := s.dynamicClientRects(clientW, clientH)
			oldHover := s.hover
			s.cursor = s.clientToImage(lParamPoint(lParam))
			s.hasCursor = true
			if s.mode == "Window" || (s.mode == "Scroll" && !s.selecting) {
				s.hover = s.windowAt(s.cursor)
			}
			if s.selecting {
				s.current = s.cursor
				if s.mode == "Freeform" {
					s.polygon = appendPointIfFar(s.polygon, s.cursor)
				} else if s.mode == "Scroll" {
					s.updateScrollSelectionCurrent()
					s.maybeAutoScrollSelection()
				}
			}
			after := s.dynamicClientRects(clientW, clientH)
			if oldHover != s.hover {
				after = append(after, s.windowClientRects(oldHover, clientW, clientH)...)
				after = append(after, s.windowClientRects(s.hover, clientW, clientH)...)
			}
			invalidateClientRects(hwnd, append(before, after...))
			return 0
		}
	case wmMouseWheel:
		if s != nil && s.mode == "Scroll" {
			// Scrolling begins only after selection, under helper control.
			return 0
		}
	case wmScrollObserved:
		if s != nil && s.mode == "Scroll" && s.selecting {
			s.scrollHandledWheels++
			delta := int(int32(uint32(wParam)))
			s.handleObservedPhysicalWheel(delta)
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case wmTimer:
		if s != nil && s.mode == "Scroll" && wParam == uintptr(scrollTimerID) {
			s.pollScrollInteraction(hwnd)
			return 0
		}
	case wmLButtonUp:
		if s != nil && s.selecting {
			if s.mode == "Scroll" {
				s.completeObservedScrollSelection(hwnd)
				return 0
			}
			s.cursor = s.clientToImage(lParamPoint(lParam))
			s.current = s.cursor
			s.hasCursor = true
			if s.mode == "Scroll" {
				s.updateScrollSelectionCurrent()
			}
			s.selecting = false
			procReleaseCapture.Call()
			if s.mode == "Freeform" {
				s.polygon = appendPointIfFar(s.polygon, s.current)
				rect := polygonBounds(s.polygon).Intersect(s.imgBounds)
				s.finish(Selection{Rect: rect, Polygon: append([]image.Point(nil), s.polygon...)}, !rect.Empty() && len(s.polygon) >= 3, nil)
			} else if s.mode == "Scroll" && s.scrollWin >= 0 && s.scrollWin < len(s.windows) {
				windowRect := s.windows[s.scrollWin].rect
				scrollRange := s.scrollRangeSelection(windowRect)
				if !s.restoreAutoScroll() {
					s.endScrollPassthrough()
					s.finish(Selection{}, false, fmt.Errorf("원래 스크롤 위치로 복원하지 못했습니다"))
					procDestroyWindow.Call(hwnd)
					return 0
				}
				if scrollRange == nil {
					s.finish(Selection{Rect: windowRect}, true, nil)
				} else {
					s.finish(Selection{Rect: windowRect, ScrollRange: scrollRange}, true, nil)
				}
				s.endScrollPassthrough()
			} else {
				rect := image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(s.imgBounds)
				s.finish(Selection{Rect: rect}, !rect.Empty(), nil)
			}
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case wmRButtonUp:
		if s != nil {
			s.restoreAutoScroll()
			s.endScrollPassthrough()
			s.finish(Selection{}, false, nil)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmKeyDown:
		if wParam == vkEscape {
			if s != nil {
				s.restoreAutoScroll()
				s.endScrollPassthrough()
				s.finish(Selection{}, false, nil)
			}
			procDestroyWindow.Call(hwnd)
			return 0
		}
		if s != nil && s.supportsArrowCursorNudge() {
			if delta, ok := arrowKeyDelta(wParam); ok {
				s.nudgeSelectionCursor(hwnd, delta)
				return 0
			}
		}
	case wmClose:
		if s != nil {
			s.restoreAutoScroll()
			s.endScrollPassthrough()
			s.finish(Selection{}, false, nil)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if s != nil {
			s.restoreAutoScroll()
			s.endScrollPassthrough()
			s.releasePaintResources()
			s.finish(Selection{}, false, nil)
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func scrollPositionQueryPoint(start image.Point, windowRect image.Rectangle) image.Point {
	return image.Pt(clamp(start.X, windowRect.Min.X, windowRect.Max.X), windowRect.Min.Y+windowRect.Dy()/2)
}

func (s *nativeSession) supportsArrowCursorNudge() bool {
	return s.mode == "Region"
}

func arrowKeyDelta(key uintptr) (image.Point, bool) {
	switch key {
	case vkLeft:
		return image.Pt(-1, 0), true
	case vkUp:
		return image.Pt(0, -1), true
	case vkRight:
		return image.Pt(1, 0), true
	case vkDown:
		return image.Pt(0, 1), true
	default:
		return image.Point{}, false
	}
}

func (s *nativeSession) nudgeSelectionCursor(hwnd uintptr, delta image.Point) bool {
	clientW, clientH := s.currentClientSize()
	if !s.hasCursor {
		cursor, ok := s.systemCursorImagePoint(hwnd, clientW, clientH)
		if !ok {
			return false
		}
		s.cursor = cursor
		s.current = cursor
		s.hasCursor = true
	}
	next, changed := nudgedPoint(s.cursor, delta, s.imgBounds)
	if !changed {
		return false
	}
	before := s.dynamicClientRects(clientW, clientH)
	actual, ok := s.moveSystemCursorToImagePoint(hwnd, next, clientW, clientH)
	if !ok || actual == s.cursor {
		return false
	}
	s.cursor = actual
	s.current = actual
	s.hasCursor = true
	after := s.dynamicClientRects(clientW, clientH)
	invalidateClientRects(hwnd, append(before, after...))
	return true
}

func nudgedPoint(point, delta image.Point, bounds image.Rectangle) (image.Point, bool) {
	if bounds.Empty() {
		return point, false
	}
	next := image.Pt(
		clamp(point.X+delta.X, bounds.Min.X, bounds.Max.X-1),
		clamp(point.Y+delta.Y, bounds.Min.Y, bounds.Max.Y-1),
	)
	return next, next != point
}

func (s *nativeSession) moveSystemCursorToImagePoint(hwnd uintptr, point image.Point, clientW, clientH int) (image.Point, bool) {
	clientPoint := nativePoint{
		X: int32((point.X - s.imgBounds.Min.X) * clientW / max(1, s.imgBounds.Dx())),
		Y: int32((point.Y - s.imgBounds.Min.Y) * clientH / max(1, s.imgBounds.Dy())),
	}
	if ok, _, _ := procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&clientPoint))); ok == 0 {
		return s.cursor, false
	}
	if ok, _, _ := procSetCursorPos.Call(intArg(int(clientPoint.X)), intArg(int(clientPoint.Y))); ok == 0 {
		return s.cursor, false
	}
	return s.systemCursorImagePoint(hwnd, clientW, clientH)
}

func (s *nativeSession) systemCursorImagePoint(hwnd uintptr, clientW, clientH int) (image.Point, bool) {
	var actual nativePoint
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&actual))); ok == 0 {
		return s.cursor, false
	}
	if ok, _, _ := procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&actual))); ok == 0 {
		return s.cursor, false
	}
	result := clientPointToImage(image.Pt(int(actual.X), int(actual.Y)), clientW, clientH, s.imgBounds)
	result.X = clamp(result.X, s.imgBounds.Min.X, s.imgBounds.Max.X-1)
	result.Y = clamp(result.Y, s.imgBounds.Min.Y, s.imgBounds.Max.Y-1)
	return result, true
}

func currentSession() *nativeSession {
	activeMu.Lock()
	defer activeMu.Unlock()
	return activeSession
}

func nativeMouseHookProc(code, wParam uintptr, mouse *lowLevelMouseHook) uintptr {
	if int32(code) == hcAction {
		s := currentSession()
		if s != nil && s.mode == "Scroll" && mouse != nil {
			if uint32(wParam) == wmMouseWheel && s.selecting && mouse.DwExtraInfo != uintptr(scrollSyntheticInputTag) {
				return 1
			}
			if message, point, swallow := scrollHookMessage(s, uint32(wParam), *mouse); swallow {
				buttonState := uintptr(0)
				if message == wmLButtonDown {
					buttonState = 1
				}
				procPostMessageW.Call(s.hwnd, uintptr(message), buttonState, screenPointLParam(point))
				return 1
			}
		}
	}
	ret, _, _ := procCallNextHookEx.Call(0, code, wParam, uintptr(unsafe.Pointer(mouse)))
	return ret
}

func scrollHookMessage(s *nativeSession, message uint32, mouse lowLevelMouseHook) (uint32, image.Point, bool) {
	if s == nil || s.mode != "Scroll" || mouse.DwExtraInfo == uintptr(scrollSyntheticInputTag) {
		return 0, image.Point{}, false
	}
	if message != wmLButtonDown && message != wmLButtonUp && message != wmRButtonDown && message != wmRButtonUp {
		return 0, image.Point{}, false
	}
	point := screenToImagePoint(image.Pt(int(mouse.Pt.X), int(mouse.Pt.Y)), s.virtualMin, s.imgBounds)
	return message, point, true
}

func screenToImagePoint(point, virtualMin image.Point, bounds image.Rectangle) image.Point {
	point = point.Sub(virtualMin)
	point.X = clamp(point.X, bounds.Min.X, bounds.Max.X)
	point.Y = clamp(point.Y, bounds.Min.Y, bounds.Max.Y)
	return point
}

func (s *nativeSession) finish(selection Selection, ok bool, err error) {
	if s.finished {
		return
	}
	s.finished = true
	s.result <- nativeResult{selection: selection, ok: ok, err: err}
}

func (s *nativeSession) paint(hwnd uintptr) {
	var ps paintStruct
	hdc, _, _ := procBeginPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))
	if hdc == 0 {
		return
	}
	defer procEndPaint.Call(hwnd, uintptr(unsafe.Pointer(&ps)))

	client := s.clientRect(hwnd)
	w := int(client.Right - client.Left)
	h := int(client.Bottom - client.Top)
	if w <= 0 || h <= 0 || (!s.liveBackground && len(s.bgra) == 0) {
		return
	}
	if s.hasCursor && !s.liveBackground {
		s.drawMagnifier(hdc, w, h)
	}
	if s.selecting && !s.scrollRestoring && s.mode == "Region" {
		s.drawSelectionOverlay(hdc, w, h)
	}
	exclusions := make([]image.Rectangle, 0, 5)
	if s.hasCursor && !s.liveBackground {
		magnifier := s.magnifierDestinationRect(w, h)
		if !magnifier.Empty() {
			exclusions = append(exclusions, magnifier)
		}
	}
	if s.mode != "Scroll" {
		exclusions = append(exclusions, s.selectionClientRects(w, h, 1)...)
	}
	savedDC := uintptr(0)
	if len(exclusions) > 0 {
		savedDC, _, _ = procSaveDC.Call(hdc)
		if savedDC != 0 {
			for _, rect := range exclusions {
				procExcludeClipRect.Call(hdc, uintptr(rect.Min.X), uintptr(rect.Min.Y), uintptr(rect.Max.X), uintptr(rect.Max.Y))
			}
		}
	}
	if s.liveBackground && s.mode == "Scroll" {
		s.drawLiveDim(hdc, w, h)
	} else {
		bgra := s.paintBuffer()
		if len(bgra) == 0 {
			return
		}
		procStretchDIBits.Call(
			hdc,
			0, 0, uintptr(w), uintptr(h),
			0, 0, uintptr(s.imgBounds.Dx()), uintptr(s.imgBounds.Dy()),
			uintptr(unsafe.Pointer(&bgra[0])),
			uintptr(unsafe.Pointer(&s.bmi)),
			uintptr(dibRGBColors),
			uintptr(srcCopy),
		)
	}
	if savedDC != 0 {
		procRestoreDC.Call(hdc, savedDC)
	}
	if s.hasCursor {
		s.drawCrosshair(hdc, w, h)
		if !s.liveBackground {
			s.drawMagnifier(hdc, w, h)
		}
	}
	s.drawSelectionOverlay(hdc, w, h)
}

func (s *nativeSession) drawSelectionOverlay(hdc uintptr, clientW, clientH int) {
	if !s.selecting || s.scrollRestoring {
		return
	}
	if s.mode == "Freeform" {
		s.drawFreeform(hdc, clientW, clientH)
	} else {
		s.drawSelection(hdc, clientW, clientH)
	}
}

func (s *nativeSession) drawLiveDim(hdc uintptr, clientW, clientH int) {
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorRef(0, 0, 0)))
	if brush == 0 {
		return
	}
	defer procDeleteObject.Call(brush)
	rect := nativeRect{Right: int32(clientW), Bottom: int32(clientH)}
	procFillRect.Call(hdc, uintptr(unsafe.Pointer(&rect)), brush)
}

func (s *nativeSession) paintBuffer() []byte {
	if len(s.original) != len(s.bgra) || len(s.highlight) != len(s.bgra) {
		return s.bgra
	}
	if s.mode == "Region" && s.selecting {
		copy(s.highlight, s.bgra)
		s.copyOriginalRect(s.highlight, image.Rectangle{Min: s.start, Max: s.current}.Canon())
		return s.highlight
	}
	if (s.mode != "Window" && s.mode != "Scroll") || s.hover < 0 || s.hover >= len(s.windows) {
		return s.bgra
	}
	if s.highlightValid && s.highlightHover == s.hover {
		return s.highlight
	}
	copy(s.highlight, s.bgra)
	for _, rect := range s.windows[s.hover].visibleRects {
		s.copyOriginalRect(s.highlight, rect)
	}
	s.highlightHover = s.hover
	s.highlightValid = true
	return s.highlight
}

func (s *nativeSession) copyOriginalRect(dst []byte, rect image.Rectangle) {
	rect = rect.Intersect(s.imgBounds)
	if rect.Empty() {
		return
	}
	stride := max(1, s.imgBounds.Dx()) * 4
	rowBytes := rect.Dx() * 4
	for y := rect.Min.Y; y < rect.Max.Y; y++ {
		start := y*stride + rect.Min.X*4
		copy(dst[start:start+rowBytes], s.original[start:start+rowBytes])
	}
}

func (s *nativeSession) dynamicClientRects(clientW, clientH int) []image.Rectangle {
	clientBounds := image.Rect(0, 0, clientW, clientH)
	if clientBounds.Empty() {
		return nil
	}
	rects := make([]image.Rectangle, 0, 12)
	if s.hasCursor {
		x := s.cursor.X * clientW / max(1, s.imgBounds.Dx())
		y := s.cursor.Y * clientH / max(1, s.imgBounds.Dy())
		rects = appendClippedRects(rects, clientBounds,
			image.Rect(0, y-2, clientW, y+3),
			image.Rect(x-2, 0, x+3, clientH),
		)
		if !s.liveBackground {
			rects = appendClippedRects(rects, clientBounds, s.magnifierDestinationRect(clientW, clientH).Inset(-2))
		}
	}
	if rect := s.selectionDirtyClientRect(clientW, clientH); !rect.Empty() {
		rects = appendClippedRects(rects, clientBounds, rect)
	}
	return append(rects, s.selectionClientRects(clientW, clientH, 4)...)
}

func (s *nativeSession) selectionDirtyClientRect(clientW, clientH int) image.Rectangle {
	if !s.selecting || s.scrollRestoring || s.mode != "Region" {
		return image.Rectangle{}
	}
	selection := image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(s.imgBounds)
	if selection.Empty() {
		return image.Rectangle{}
	}
	left, top, right, bottom := s.clientRectForImageRect(selection, clientW, clientH)
	return image.Rect(left, top, right, bottom).Inset(-4)
}

func (s *nativeSession) selectionClientRects(clientW, clientH, padding int) []image.Rectangle {
	if !s.selecting || s.scrollRestoring || s.mode == "Scroll" {
		return nil
	}
	clientBounds := image.Rect(0, 0, clientW, clientH)
	rects := make([]image.Rectangle, 0, 4)
	if s.mode == "Freeform" {
		if padding <= 1 {
			return nil
		}
		bounds := polygonBounds(s.polygon).Intersect(s.imgBounds)
		if !bounds.Empty() {
			left, top, right, bottom := s.clientRectForImageRect(bounds, clientW, clientH)
			rects = appendClippedRects(rects, clientBounds, image.Rect(left-padding, top-padding, right+padding, bottom+padding))
		}
		return rects
	}
	selection := image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(s.imgBounds)
	if selection.Empty() {
		return rects
	}
	left, top, right, bottom := s.clientRectForImageRect(selection, clientW, clientH)
	return appendBorderDirtyRects(rects, image.Rect(left, top, right, bottom), clientBounds, padding)
}

func (s *nativeSession) windowClientRects(index, clientW, clientH int) []image.Rectangle {
	if index < 0 || index >= len(s.windows) {
		return nil
	}
	clientBounds := image.Rect(0, 0, clientW, clientH)
	rects := make([]image.Rectangle, 0, len(s.windows[index].visibleRects))
	for _, rect := range s.windows[index].visibleRects {
		left, top, right, bottom := s.clientRectForImageRect(rect, clientW, clientH)
		rects = appendClippedRects(rects, clientBounds, image.Rect(left, top, right, bottom))
	}
	return rects
}

func appendBorderDirtyRects(rects []image.Rectangle, rect, bounds image.Rectangle, width int) []image.Rectangle {
	if rect.Empty() {
		return rects
	}
	return appendClippedRects(rects, bounds,
		image.Rect(rect.Min.X-width, rect.Min.Y-width, rect.Max.X+width, rect.Min.Y+width+1),
		image.Rect(rect.Min.X-width, rect.Max.Y-width-1, rect.Max.X+width, rect.Max.Y+width),
		image.Rect(rect.Min.X-width, rect.Min.Y-width, rect.Min.X+width+1, rect.Max.Y+width),
		image.Rect(rect.Max.X-width-1, rect.Min.Y-width, rect.Max.X+width, rect.Max.Y+width),
	)
}

func appendClippedRects(rects []image.Rectangle, bounds image.Rectangle, candidates ...image.Rectangle) []image.Rectangle {
	for _, rect := range candidates {
		if rect = rect.Intersect(bounds); !rect.Empty() {
			rects = append(rects, rect)
		}
	}
	return rects
}

func invalidateClientRects(hwnd uintptr, rects []image.Rectangle) {
	for _, rect := range rects {
		if rect.Empty() {
			continue
		}
		native := nativeRect{Left: int32(rect.Min.X), Top: int32(rect.Min.Y), Right: int32(rect.Max.X), Bottom: int32(rect.Max.Y)}
		procInvalidateRect.Call(hwnd, uintptr(unsafe.Pointer(&native)), 0)
	}
}

func (s *nativeSession) clientRect(hwnd uintptr) nativeRect {
	var rect nativeRect
	ret, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 || rect.Right <= rect.Left || rect.Bottom <= rect.Top {
		return nativeRect{Right: int32(s.winWidth), Bottom: int32(s.winHeight)}
	}
	return rect
}

func (s *nativeSession) drawSelection(hdc uintptr, clientW, clientH int) {
	rect := image.Rectangle{Min: s.start, Max: s.current}.Canon()
	if s.mode == "Scroll" && s.scrollWin >= 0 && s.scrollWin < len(s.windows) {
		windowRect := s.windows[s.scrollWin].rect
		var clippedTop, clippedBottom bool
		rect, clippedTop, clippedBottom = s.scrollSelectionVisibleRect(windowRect)
		if rect.Empty() {
			return
		}
		borderColor := colorRef(45, 212, 191)
		if s.scrollNoMotion {
			borderColor = colorRef(255, 214, 10)
		} else if s.scrollLastDelta != 0 && !s.scrollLastMeasured {
			borderColor = colorRef(255, 159, 10)
		}
		s.drawScrollRect(hdc, clientW, clientH, rect, borderColor, 3, clippedTop, clippedBottom)
		endpoint := image.Pt(s.scrollCurrentContent.X, s.scrollCurrentContent.Y-s.scrollOffsetY)
		endpoint = scrollViewportPoint(endpoint, windowRect)
		marker := image.Rect(endpoint.X-4, endpoint.Y-4, endpoint.X+5, endpoint.Y+5).Intersect(windowRect)
		if !marker.Empty() {
			s.drawRect(hdc, clientW, clientH, marker, borderColor, 3)
		}
		if clippedTop {
			s.drawRect(hdc, clientW, clientH, image.Rect(rect.Min.X, rect.Min.Y, min(rect.Max.X, rect.Min.X+28), rect.Min.Y+5), borderColor, 3)
		}
		if clippedBottom {
			s.drawRect(hdc, clientW, clientH, image.Rect(max(rect.Min.X, rect.Max.X-28), rect.Max.Y-5, rect.Max.X, rect.Max.Y), borderColor, 3)
		}
		s.drawScrollStatus(hdc, clientW, clientH, rect, borderColor)
		return
	}
	rect = rect.Intersect(s.imgBounds)
	if rect.Empty() {
		return
	}
	borderColor := s.selectionColor
	if borderColor == 0 {
		borderColor = colorRef(255, 64, 64)
	}
	s.drawRect(hdc, clientW, clientH, rect, borderColor, 3)
}

func (s *nativeSession) scrollStatusLabel() string {
	selectionHeight := absInt(s.scrollCurrentContent.Y - s.scrollStartContent.Y)
	if s.scrollNoMotion {
		return "페이지 이동 없음"
	}
	if s.scrollLastDelta != 0 && !s.scrollLastMeasured {
		return fmt.Sprintf("선택 %d px · 이동량 추정", selectionHeight)
	}
	if s.scrollLastMeasured {
		return fmt.Sprintf("선택 %d px · 실제 이동 감지", selectionHeight)
	}
	return fmt.Sprintf("선택 %d px", selectionHeight)
}

func (s *nativeSession) drawScrollRect(hdc uintptr, clientW, clientH int, rect image.Rectangle, c uint32, width int, clippedTop, clippedBottom bool) {
	s.drawLine(hdc, clientW, clientH, rect.Min, image.Pt(rect.Min.X, rect.Max.Y), c, width)
	s.drawLine(hdc, clientW, clientH, image.Pt(rect.Max.X, rect.Min.Y), rect.Max, c, width)
	if !clippedTop {
		s.drawLine(hdc, clientW, clientH, rect.Min, image.Pt(rect.Max.X, rect.Min.Y), c, width)
	}
	if !clippedBottom {
		s.drawLine(hdc, clientW, clientH, image.Pt(rect.Min.X, rect.Max.Y), rect.Max, c, width)
	}
}

func (s *nativeSession) drawLine(hdc uintptr, clientW, clientH int, from, to image.Point, c uint32, width int) {
	fromX := from.X * clientW / max(1, s.imgBounds.Dx())
	fromY := from.Y * clientH / max(1, s.imgBounds.Dy())
	toX := to.X * clientW / max(1, s.imgBounds.Dx())
	toY := to.Y * clientH / max(1, s.imgBounds.Dy())
	pen, _, _ := procCreatePen.Call(uintptr(psSolid), uintptr(width), uintptr(c))
	if pen == 0 {
		return
	}
	defer procDeleteObject.Call(pen)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procMoveToEx.Call(hdc, uintptr(fromX), uintptr(fromY), 0)
	procLineTo.Call(hdc, uintptr(toX), uintptr(toY))
	if oldPen != 0 {
		procSelectObject.Call(hdc, oldPen)
	}
}

func (s *nativeSession) drawScrollStatus(hdc uintptr, clientW, clientH int, rect image.Rectangle, textColor uint32) {
	label, err := windows.UTF16PtrFromString(s.scrollStatusLabel())
	if err != nil {
		return
	}
	left, top, _, _ := s.clientRectForImageRect(rect, clientW, clientH)
	statusRect := nativeRect{Left: int32(left + 8), Top: int32(top + 8), Right: int32(min(clientW-8, left+238)), Bottom: int32(top + 36)}
	if statusRect.Right <= statusRect.Left || statusRect.Bottom <= statusRect.Top {
		return
	}
	brush, _, _ := procCreateSolidBrush.Call(uintptr(colorRef(24, 24, 27)))
	if brush != 0 {
		procFillRect.Call(hdc, uintptr(unsafe.Pointer(&statusRect)), brush)
		procDeleteObject.Call(brush)
	}
	procSetBkMode.Call(hdc, transparentBackground)
	procSetTextColor.Call(hdc, uintptr(textColor))
	procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(label)), ^uintptr(0), uintptr(unsafe.Pointer(&statusRect)), dtCenter|dtVCenter|dtSingleLine|dtNoPrefix)
}

func (s *nativeSession) drawRect(hdc uintptr, clientW, clientH int, rect image.Rectangle, c uint32, width int) {
	left, top, right, bottom := s.clientRectForImageRect(rect, clientW, clientH)
	pen, _, _ := procCreatePen.Call(uintptr(psSolid), uintptr(width), uintptr(c))
	if pen == 0 {
		return
	}
	defer procDeleteObject.Call(pen)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	brush, _, _ := procGetStockObject.Call(uintptr(nullBrush))
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	procRectangle.Call(hdc, uintptr(left), uintptr(top), uintptr(right), uintptr(bottom))
	if oldBrush != 0 {
		procSelectObject.Call(hdc, oldBrush)
	}
	if oldPen != 0 {
		procSelectObject.Call(hdc, oldPen)
	}
}

func (s *nativeSession) clientRectForImageRect(rect image.Rectangle, clientW, clientH int) (int, int, int, int) {
	left := rect.Min.X * clientW / max(1, s.imgBounds.Dx())
	top := rect.Min.Y * clientH / max(1, s.imgBounds.Dy())
	right := rect.Max.X * clientW / max(1, s.imgBounds.Dx())
	bottom := rect.Max.Y * clientH / max(1, s.imgBounds.Dy())
	return left, top, right, bottom
}

func (s *nativeSession) drawFreeform(hdc uintptr, clientW, clientH int) {
	if len(s.polygon) < 2 {
		return
	}
	pen, _, _ := procCreatePen.Call(uintptr(psSolid), uintptr(3), uintptr(colorRef(255, 64, 64)))
	if pen == 0 {
		return
	}
	defer procDeleteObject.Call(pen)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	first := true
	for _, p := range s.polygon {
		x := p.X * clientW / max(1, s.imgBounds.Dx())
		y := p.Y * clientH / max(1, s.imgBounds.Dy())
		if first {
			procMoveToEx.Call(hdc, uintptr(x), uintptr(y), 0)
			first = false
		} else {
			procLineTo.Call(hdc, uintptr(x), uintptr(y))
		}
	}
	if oldPen != 0 {
		procSelectObject.Call(hdc, oldPen)
	}
}

func (s *nativeSession) windowAt(p image.Point) int {
	for i, window := range s.windows {
		for _, rect := range window.visibleRects {
			if p.In(rect) {
				return i
			}
		}
	}
	return -1
}

func (s *nativeSession) beginScrollSelection() {
	s.scrollOffsetY = 0
	s.scrollWheelSteps = 0
	s.scrollWheelUnits = 0
	s.scrollStartContent = s.scrollDocumentPoint(s.start)
	s.scrollCurrentContent = s.scrollStartContent
	s.scrollMaxContentY = s.scrollStartContent.Y
	s.scrollMaxOffsetY = 0
	s.scrollMaxWheelSteps = 0
	s.scrollRangeEndViewY = s.start.Y
	s.lastScroll = time.Time{}
	s.scrollFallbackStep = 0
	s.scrollLastDelta = 0
	s.scrollLastMeasured = false
	s.scrollNoMotion = false
	s.scrollWheelPoint = s.start
	s.scrollOriginSample = scrollSample{}
	s.scrollPassiveSample = scrollSample{}
	s.scrollInitialPosition = nil
	s.scrollSampleRect = image.Rectangle{}
}

func (s *nativeSession) pollScrollInteraction(hwnd uintptr) {
	s.updateScrollCursorFromPhysical()
	escape, _, _ := procGetAsyncKeyState.Call(uintptr(vkEscape))
	if uint16(escape)&0x8000 != 0 {
		procKillTimer.Call(hwnd, uintptr(scrollTimerID))
		s.restoreAutoScroll()
		s.finish(Selection{}, false, nil)
		procDestroyWindow.Call(hwnd)
		return
	}
	if !s.selecting {
		s.hover = s.windowAt(s.cursor)
	}
	procInvalidateRect.Call(hwnd, 0, 0)
}

func (s *nativeSession) updateScrollCursorFromPhysical() {
	var point nativePoint
	if ok, _, _ := procGetCursorPos.Call(uintptr(unsafe.Pointer(&point))); ok == 0 {
		return
	}
	cursor := screenToImagePoint(image.Pt(int(point.X), int(point.Y)), s.virtualMin, s.imgBounds)
	s.cursor = cursor
	s.current = cursor
	s.hasCursor = true
	if s.selecting {
		s.updateScrollSelectionCurrent()
	}
}

func (s *nativeSession) observePassiveScroll() {
	if s.scrollWin < 0 || s.scrollWin >= len(s.windows) {
		return
	}
	measurementRect := s.scrollSampleRect
	if measurementRect.Empty() {
		measurementRect = s.scrollMeasurementRect(s.windows[s.scrollWin].rect)
	}
	sample := s.captureScrollSample(measurementRect)
	if !validScrollSample(sample) {
		return
	}
	if !validScrollSample(s.scrollPassiveSample) {
		s.scrollPassiveSample = sample
		s.scrollOriginSample = sample
		return
	}
	previous := s.scrollPassiveSample
	s.scrollPassiveSample = sample
	if scrollSamplesNearStatic(previous, sample) {
		return
	}
	maxAbs := min(sample.height-scrollScoreMinSamples, max(120, sample.height*3/4))
	delta, measured := measureScrollDelta(previous, sample, maxAbs)
	if !measured || delta == 0 {
		return
	}
	s.scrollOffsetY += delta
	if s.scrollOffsetY < 0 {
		s.scrollOffsetY = 0
	}
	stepEstimate := s.scrollFallbackStep
	if stepEstimate <= 0 {
		stepEstimate = absInt(delta)
	}
	steps := max(1, int(math.Round(float64(absInt(delta))/float64(max(1, stepEstimate)))))
	wheelInput := -steps * wheelDelta
	if delta < 0 {
		wheelInput = steps * wheelDelta
	}
	s.recordScrollWheelInput(wheelInput)
	s.scrollFallbackStep = max(1, absInt(delta)/steps)
	s.scrollLastDelta = delta
	s.scrollLastMeasured = true
	s.scrollNoMotion = false
	s.updateScrollSelectionCurrent()
}

func (s *nativeSession) completeObservedScrollSelection(hwnd uintptr) {
	procKillTimer.Call(hwnd, uintptr(scrollTimerID))
	s.updateScrollCursorFromPhysical()
	s.updateScrollSelectionCurrent()
	s.selecting = false
	if s.scrollWin < 0 || s.scrollWin >= len(s.windows) {
		s.endScrollPassthrough()
		s.finish(Selection{}, false, nil)
		procDestroyWindow.Call(hwnd)
		return
	}
	windowRect := s.windows[s.scrollWin].rect
	if s.scrollInitialPosition == nil {
		window := s.windows[s.scrollWin]
		target := platformwindows.WindowTarget{Handle: window.handle, Rect: window.rect.Add(s.virtualMin)}
		selection := image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(windowRect)
		point := selection.Min.Add(selection.Size().Div(2)).Add(s.virtualMin)
		query := s.scrollPositionQuery
		if query == nil {
			query = platformwindows.QueryScrollPosition
		}
		if position, queryErr := query(target, point); queryErr == nil {
			s.scrollInitialPosition = &position
		}
	}
	scrollRange := s.scrollRangeSelection(windowRect)
	if scrollRange == nil {
		s.finish(Selection{Rect: windowRect}, true, nil)
	} else {
		s.finish(Selection{Rect: windowRect, ScrollRange: scrollRange}, true, nil)
	}
	s.endScrollPassthrough()
	procDestroyWindow.Call(hwnd)
}

func shouldDeferScrollRestore(scrollRange *ScrollRangeSelection) bool {
	return scrollRange != nil && (scrollRange.InitialScrollPercent != nil || scrollRange.InitialWheelUnits > 0)
}

func (s *nativeSession) updateScrollSelectionCurrent() {
	s.scrollCurrentContent = s.scrollDocumentPoint(s.current)
	if s.scrollCurrentContent.Y > s.scrollMaxContentY {
		s.scrollMaxContentY = s.scrollCurrentContent.Y
		s.scrollMaxOffsetY = s.scrollOffsetY
		s.scrollMaxWheelSteps = s.scrollWheelSteps
		s.scrollRangeEndViewY = s.current.Y
	}
}

func (s *nativeSession) scrollDocumentPoint(p image.Point) image.Point {
	if s.scrollWin >= 0 && s.scrollWin < len(s.windows) {
		p = scrollViewportPoint(p, s.windows[s.scrollWin].rect)
	}
	return image.Pt(p.X, p.Y+s.scrollOffsetY)
}

func scrollViewportPoint(p image.Point, viewport image.Rectangle) image.Point {
	return image.Pt(
		clamp(p.X, viewport.Min.X, viewport.Max.X),
		clamp(p.Y, viewport.Min.Y, viewport.Max.Y),
	)
}

func (s *nativeSession) maybeAutoScrollSelection() {
	if s.scrollWin < 0 || s.scrollWin >= len(s.windows) {
		return
	}
	window := s.windows[s.scrollWin]
	if window.handle == 0 || time.Since(s.lastScroll) < scrollTickInterval {
		return
	}
	direction := 0
	if s.current.Y >= window.rect.Max.Y-scrollEdgeThreshold {
		direction = 1
	} else if s.current.Y <= window.rect.Min.Y+scrollEdgeThreshold && s.scrollOffsetY > 0 {
		direction = -1
	}
	if direction == 0 {
		return
	}
	s.lastScroll = time.Now()
	wheelInput := -direction * wheelDelta
	if direction < 0 {
		wheelInput = scrollSelectionReverseWheelDelta(wheelDelta, s.scrollOffsetY, s.scrollFallbackStep)
		if wheelInput == 0 {
			return
		}
	}
	wheelPoint := scrollViewportPoint(s.current, window.rect)
	if !s.sendMouseWheel(window.handle, wheelInput, wheelPoint) {
		return
	}
	s.handleObservedPhysicalWheel(wheelInput)
}

func (s *nativeSession) handleScrollWheel(delta int) {
	if delta == 0 {
		return
	}
	target := s.hover
	if s.selecting && s.scrollWin >= 0 {
		target = s.scrollWin
	}
	if target < 0 || target >= len(s.windows) {
		return
	}
	window := s.windows[target]
	if window.handle == 0 {
		return
	}
	if s.selecting && delta > 0 {
		delta = scrollSelectionReverseWheelDelta(delta, s.scrollOffsetY, s.scrollFallbackStep)
		if delta == 0 {
			return
		}
	}
	steps := absInt(delta) / wheelDelta
	if steps == 0 {
		steps = 1
	}
	if s.selecting && delta > 0 {
		if s.scrollWheelSteps > 0 && steps > s.scrollWheelSteps {
			steps = s.scrollWheelSteps
			delta = steps * wheelDelta
		}
	}
	point := s.cursor
	if !point.In(window.rect) {
		point = image.Pt(window.rect.Min.X+window.rect.Dx()/2, window.rect.Min.Y+window.rect.Dy()/2)
	}
	if !s.sendMouseWheel(window.handle, delta, point) {
		return
	}
	s.scrollSentWheels++
	if s.selecting {
		s.handleObservedPhysicalWheel(delta)
	}
}

func (s *nativeSession) handleObservedPhysicalWheel(delta int) {
	if delta == 0 || s.scrollWin < 0 || s.scrollWin >= len(s.windows) {
		return
	}
	s.recordScrollWheelInput(delta)
	estimated := max(1, scrollStepPixels*absInt(delta)/wheelDelta)
	if delta > 0 {
		estimated = -estimated
	}
	s.scrollOffsetY = max(0, s.scrollOffsetY+estimated)
	s.scrollFallbackStep = scrollStepPixels
	s.scrollLastDelta = estimated
	s.scrollLastMeasured = false
	s.scrollNoMotion = false
	s.scrollWheelPoint = s.cursor
	s.updateScrollSelectionCurrent()
}

func (s *nativeSession) recordScrollWheelInput(delta int) {
	s.scrollWheelUnits -= delta
	if s.scrollWheelUnits < 0 {
		s.scrollWheelUnits = 0
	}
	s.scrollWheelSteps = (s.scrollWheelUnits + wheelDelta - 1) / wheelDelta
}

func (s *nativeSession) captureSettledScrollSample(rect image.Rectangle, before scrollSample, expected int) scrollSample {
	_ = expected
	last := s.captureScrollSample(rect)
	for attempt := 0; attempt < 4; attempt++ {
		if !validScrollSample(before) || !validScrollSample(last) || !scrollSamplesNearStatic(before, last) {
			return last
		}
		if attempt == 3 {
			break
		}
		time.Sleep(25 * time.Millisecond)
		last = s.captureScrollSample(rect)
	}
	return last
}

func (s *nativeSession) captureStableScrollSample(rect image.Rectangle) scrollSample {
	last := s.captureScrollSample(rect)
	stableFrames := 0
	for attempt := 0; attempt < 10; attempt++ {
		time.Sleep(30 * time.Millisecond)
		next := s.captureScrollSample(rect)
		if validScrollSample(last) && validScrollSample(next) && scrollSamplesNearStatic(last, next) {
			stableFrames++
			if stableFrames >= 2 {
				return next
			}
		} else {
			stableFrames = 0
		}
		last = next
	}
	return last
}

func scrollSampleHasExpectedMotion(before, after scrollSample, expected, maxAbs int) bool {
	delta, ok := measureScrollDelta(before, after, maxAbs)
	if !ok || delta == 0 {
		return false
	}
	return expected == 0 || (delta > 0) == (expected > 0)
}

func (s *nativeSession) scrollMeasurementRect(windowRect image.Rectangle) image.Rectangle {
	left := clamp(min(s.start.X, s.current.X)-16, windowRect.Min.X, windowRect.Max.X)
	right := clamp(max(s.start.X, s.current.X)+16, windowRect.Min.X, windowRect.Max.X)
	if right-left >= 64 {
		return image.Rect(left, windowRect.Min.Y, right, windowRect.Max.Y)
	}
	center := clamp(s.current.X, windowRect.Min.X, windowRect.Max.X)
	focusWidth := min(windowRect.Dx(), max(160, min(320, windowRect.Dx()/2)))
	left = clamp(center-focusWidth/2, windowRect.Min.X, max(windowRect.Min.X, windowRect.Max.X-focusWidth))
	return image.Rect(left, windowRect.Min.Y, left+focusWidth, windowRect.Max.Y)
}

func scrollSelectionReverseWheelDelta(requested, remaining, stepEstimate int) int {
	if requested <= 0 || remaining <= 0 {
		return 0
	}
	if stepEstimate <= 0 {
		stepEstimate = scrollStepPixels
	}
	correction := scrollRestoreWheelDelta(remaining, stepEstimate)
	return min(requested, correction)
}

func scrollExpectedPixelDelta(wheelInput int) int {
	if wheelInput == 0 {
		return 0
	}
	magnitude := max(1, int(math.Round(float64(scrollStepPixels*absInt(wheelInput))/wheelDelta)))
	if wheelInput > 0 {
		return -magnitude
	}
	return magnitude
}

func (s *nativeSession) restoreAutoScroll() bool {
	if s.scrollWin < 0 || s.scrollWin >= len(s.windows) || s.scrollOffsetY <= 0 {
		return true
	}
	window := s.windows[s.scrollWin]
	if window.handle == 0 {
		return false
	}
	s.scrollRestoring = true
	defer func() { s.scrollRestoring = false }()
	if s.scrollInitialPosition != nil {
		target := platformwindows.WindowTarget{Handle: window.handle, Rect: window.rect.Add(s.virtualMin)}
		restore := s.scrollPositionRestore
		if restore == nil {
			restore = platformwindows.RestoreScrollPosition
		}
		if err := restore(target, *s.scrollInitialPosition); err == nil {
			s.scrollOffsetY = 0
			s.scrollWheelSteps = 0
			s.scrollWheelUnits = 0
			return true
		}
	}
	point := s.scrollRestorePoint(window.rect)
	wheelUnits := s.scrollWheelUnits
	for wheelUnits > 0 {
		inputDelta := min(wheelDelta, wheelUnits)
		if s.scrollWheelSender != nil {
			if !s.scrollWheelSender(window.handle, inputDelta, point) {
				return false
			}
		} else {
			if !activateWheelTarget(window.handle) {
				return false
			}
			procMouseEvent.Call(uintptr(mouseEventWheel), 0, 0, uintptr(uint32(int32(inputDelta))), 0)
			time.Sleep(80 * time.Millisecond)
		}
		wheelUnits -= inputDelta
	}
	s.scrollOffsetY = 0
	s.scrollWheelSteps = 0
	s.scrollWheelUnits = 0
	return true
}

func scrollRestoreWheelDelta(remaining, stepEstimate int) int {
	if remaining == 0 {
		return 0
	}
	stepEstimate = max(1, stepEstimate)
	amount := wheelDelta
	if absInt(remaining) < stepEstimate {
		amount = max(1, int(math.Round(float64(wheelDelta*absInt(remaining))/float64(stepEstimate))))
	}
	if remaining < 0 {
		return -amount
	}
	return amount
}

func (s *nativeSession) scrollRestorePoint(windowRect image.Rectangle) image.Point {
	if s.scrollWheelPoint.In(windowRect) {
		return s.scrollWheelPoint
	}
	return image.Pt(windowRect.Min.X+windowRect.Dx()/2, windowRect.Min.Y+windowRect.Dy()/2)
}

func (s *nativeSession) applyObservedScrollDelta(before, after scrollSample, fallback int) scrollMotionResult {
	if s.scrollOffsetY == 0 && !validScrollSample(s.scrollOriginSample) && validScrollSample(before) {
		s.scrollOriginSample = before
	}
	s.scrollLastDelta = 0
	s.scrollLastMeasured = false
	s.scrollNoMotion = false
	delta, ok := measureScrollDelta(before, after, max(80, absInt(fallback)+120))
	if ok && fallback != 0 && delta != 0 && (delta > 0) != (fallback > 0) {
		s.scrollNoMotion = true
		return scrollMotionNone
	}
	if ok {
		s.scrollOffsetY += delta
		steps := max(1, int(math.Round(float64(absInt(fallback))/scrollStepPixels)))
		s.scrollFallbackStep = max(1, absInt(delta)/steps)
		s.scrollLastDelta = delta
		s.scrollLastMeasured = true
	} else {
		if scrollSamplesNearStatic(before, after) {
			s.scrollNoMotion = true
			return scrollMotionNone
		}
		delta = fallback
		if s.scrollFallbackStep > 0 && fallback != 0 {
			steps := max(1, int(math.Round(float64(absInt(fallback))/scrollStepPixels)))
			delta = s.scrollFallbackStep * steps
			if fallback < 0 {
				delta = -delta
			}
		}
		s.scrollOffsetY += delta
		s.scrollLastDelta = delta
	}
	if s.scrollOffsetY < 0 {
		s.scrollOffsetY = 0
	}
	if fallback < 0 {
		s.reconcileScrollOrigin(after)
	}
	if ok {
		return scrollMotionMeasured
	}
	return scrollMotionEstimated
}

func validScrollSample(sample scrollSample) bool {
	return sample.width > 0 && sample.height > 0 && len(sample.luma) >= sample.width*sample.height
}

func (s *nativeSession) measuredScrollOriginDelta(sample scrollSample, expected int) (int, bool) {
	if !validScrollSample(s.scrollOriginSample) || !validScrollSample(sample) || s.scrollOriginSample.width != sample.width || s.scrollOriginSample.height != sample.height {
		return 0, false
	}
	maxAbs := min(sample.height-scrollScoreMinSamples, max(80, absInt(expected)+64))
	if maxAbs <= 0 {
		return 0, false
	}
	delta, ok := measureScrollPositionDelta(s.scrollOriginSample, sample, maxAbs, true)
	if !ok {
		return 0, false
	}
	tolerance := max(24, min(sample.height/3, max(64, s.scrollFallbackStep*2)))
	if absInt(delta-expected) > tolerance {
		return 0, false
	}
	return delta, true
}

func (s *nativeSession) reconcileScrollOrigin(sample scrollSample) bool {
	nearOrigin := max(64, s.scrollFallbackStep*2)
	if s.scrollOffsetY <= nearOrigin && validScrollSample(s.scrollOriginSample) && validScrollSample(sample) &&
		s.scrollOriginSample.width == sample.width && s.scrollOriginSample.height == sample.height {
		if score, ok := scrollDeltaScore(s.scrollOriginSample, sample, 0); ok && score <= 8 {
			s.scrollOffsetY = 0
			return true
		}
	}
	offset, ok := s.measuredScrollOriginDelta(sample, s.scrollOffsetY)
	if !ok || offset != 0 {
		return false
	}
	s.scrollOffsetY = offset
	return true
}

func (s *nativeSession) captureScrollSample(rect image.Rectangle) scrollSample {
	if s.scrollSampleCapture != nil {
		return s.scrollSampleCapture(rect)
	}
	rect = rect.Intersect(s.imgBounds)
	if rect.Empty() {
		return scrollSample{}
	}
	marginX := min(24, max(0, rect.Dx()*12/1000))
	rect.Max.X -= marginX
	if rect.Dx() < 8 || rect.Dy() < 8 {
		return scrollSample{}
	}
	screenRect := rect.Add(s.virtualMin)
	w, h := screenRect.Dx(), screenRect.Dy()
	screenDC, _, _ := procGetDC.Call(0)
	if screenDC == 0 {
		return scrollSample{}
	}
	defer procReleaseDC.Call(0, screenDC)
	memDC, _, _ := procCreateCompatibleDC.Call(screenDC)
	if memDC == 0 {
		return scrollSample{}
	}
	defer procDeleteDC.Call(memDC)
	bmp, _, _ := procCreateCompatibleBitmap.Call(screenDC, uintptr(w), uintptr(h))
	if bmp == 0 {
		return scrollSample{}
	}
	defer procDeleteObject.Call(bmp)
	old, _, _ := procSelectObject.Call(memDC, bmp)
	if ret, _, _ := procBitBlt.Call(memDC, 0, 0, uintptr(w), uintptr(h), screenDC, intArg(screenRect.Min.X), intArg(screenRect.Min.Y), uintptr(srcCopy)); ret == 0 {
		if old != 0 {
			procSelectObject.Call(memDC, old)
		}
		return scrollSample{}
	}
	if old != 0 {
		procSelectObject.Call(memDC, old)
	}
	bmi := bitmapInfo{}
	bmi.Header.BiSize = uint32(unsafe.Sizeof(bitmapInfoHeader{}))
	bmi.Header.BiWidth = int32(w)
	bmi.Header.BiHeight = -int32(h)
	bmi.Header.BiPlanes = 1
	bmi.Header.BiBitCount = 32
	bmi.Header.BiCompression = biRGB
	bgra := make([]byte, w*h*4)
	ret, _, _ := procGetDIBits.Call(memDC, bmp, 0, uintptr(h), uintptr(unsafe.Pointer(&bgra[0])), uintptr(unsafe.Pointer(&bmi)), uintptr(dibRGBColors))
	if ret == 0 {
		return scrollSample{}
	}
	luma := make([]byte, w*h)
	for i := 0; i < w*h; i++ {
		p := i * 4
		luma[i] = byte((29*int(bgra[p]) + 150*int(bgra[p+1]) + 77*int(bgra[p+2])) >> 8)
	}
	return scrollSample{width: w, height: h, luma: luma}
}

func measureScrollDelta(before, after scrollSample, maxAbs int) (int, bool) {
	return measureScrollPositionDelta(before, after, maxAbs, false)
}

func measureScrollPositionDelta(before, after scrollSample, maxAbs int, allowZero bool) (int, bool) {
	if before.width <= 0 || before.height <= 0 || before.width != after.width || before.height != after.height || len(before.luma) != before.width*before.height || len(after.luma) != after.width*after.height {
		return 0, false
	}
	maxAbs = min(maxAbs, before.height-4)
	if maxAbs <= 0 {
		return 0, false
	}
	candidates := make([]scrollDeltaCandidate, 0, maxAbs*2+1)
	for delta := -maxAbs; delta <= maxAbs; delta++ {
		score, ok := scrollDeltaScore(before, after, delta)
		if !ok {
			continue
		}
		candidates = append(candidates, scrollDeltaCandidate{delta: delta, score: score})
	}
	if len(candidates) == 0 {
		return 0, false
	}
	best := candidates[0]
	for _, candidate := range candidates[1:] {
		if candidate.score < best.score {
			best = candidate
		}
	}
	if (!allowZero && best.delta == 0) || best.score > scrollScoreMaxAccepted {
		return 0, false
	}
	alternativeScore := int64(1<<62 - 1)
	for _, candidate := range candidates {
		if absInt(candidate.delta-best.delta) < scrollScoreAlternativeGap {
			continue
		}
		if candidate.score < alternativeScore {
			alternativeScore = candidate.score
		}
	}
	minimumGap := int64(max(2, int(best.score/8)+1))
	if alternativeScore != int64(1<<62-1) && alternativeScore-best.score < minimumGap {
		return 0, false
	}
	return best.delta, true
}

func scrollSamplesNearStatic(before, after scrollSample) bool {
	score, ok := scrollDeltaScoreWithFilter(before, after, 0, false)
	return ok && score <= scrollScoreStaticMax
}

func scrollDeltaScore(before, after scrollSample, delta int) (int64, bool) {
	if score, ok := scrollDeltaScoreWithFilter(before, after, delta, true); ok {
		return score, true
	}
	return scrollDeltaScoreWithFilter(before, after, delta, false)
}

func scrollDeltaScoreWithFilter(before, after scrollSample, delta int, filterStickyRows bool) (int64, bool) {
	w, h := before.width, before.height
	yBefore, yAfter := 0, 0
	overlap := h
	if delta > 0 {
		yBefore = delta
		overlap = h - delta
	} else if delta < 0 {
		yAfter = -delta
		overlap = h + delta
	}
	if overlap < max(8, h/5) {
		return 0, false
	}
	rowStep := max(1, overlap/48)
	colStep := max(1, w/96)
	topSkip := min(scrollScoreTopSkipMax, max(24, h/8))
	bottomSkip := min(scrollScoreBottomSkipMax, max(12, h/16))
	var total int64
	var count int64
	for y := 0; y < overlap; y += rowStep {
		py := yBefore + y
		ay := yAfter + y
		if filterStickyRows {
			if py < topSkip || ay < topSkip || py >= h-bottomSkip || ay >= h-bottomSkip {
				continue
			}
			if rowMeanAbsDiff(before, after, ay, colStep) <= scrollScoreUnchangedRowThreshold {
				continue
			}
		}
		bi := (yBefore + y) * w
		ai := (yAfter + y) * w
		for x := 0; x < w; x += colStep {
			d := int(before.luma[bi+x]) - int(after.luma[ai+x])
			if d < 0 {
				d = -d
			}
			total += int64(d)
			count++
		}
	}
	if count < scrollScoreMinSamples {
		return 0, false
	}
	return total / count, true
}

func rowMeanAbsDiff(before, after scrollSample, y, colStep int) int {
	if y < 0 || y >= before.height || y >= after.height || before.width != after.width {
		return 255
	}
	w := before.width
	if colStep <= 0 {
		colStep = 1
	}
	var total int
	var count int
	bi := y * w
	ai := y * w
	for x := 0; x < w; x += colStep {
		d := int(before.luma[bi+x]) - int(after.luma[ai+x])
		if d < 0 {
			d = -d
		}
		total += d
		count++
	}
	if count == 0 {
		return 255
	}
	return total / count
}

func (s *nativeSession) scrollSelectionContentRect(windowRect image.Rectangle) image.Rectangle {
	rect := s.scrollSelectionCurrentContentRect()
	if s.scrollMaxContentY > rect.Max.Y {
		rect.Max.Y = s.scrollMaxContentY
	}
	rect.Min.X = clamp(rect.Min.X, windowRect.Min.X, windowRect.Max.X)
	rect.Max.X = clamp(rect.Max.X, windowRect.Min.X, windowRect.Max.X)
	if rect.Min.Y < windowRect.Min.Y {
		rect.Min.Y = windowRect.Min.Y
	}
	if rect.Max.Y < windowRect.Min.Y {
		rect.Max.Y = windowRect.Min.Y
	}
	return rect
}

func (s *nativeSession) scrollRangeSelection(windowRect image.Rectangle) *ScrollRangeSelection {
	cropRect := s.scrollSelectionContentRect(windowRect)
	endViewY := s.scrollRangeEndViewY
	if endViewY == 0 {
		endViewY = s.current.Y
	}
	viewEnd := image.Pt(s.scrollCurrentContent.X, endViewY)
	viewRect := image.Rectangle{Min: s.start, Max: viewEnd}.Canon().Intersect(windowRect)
	if cropRect.Max.Y <= cropRect.Min.Y || cropRect.Dx() <= 0 {
		return nil
	}
	scrollSteps := s.scrollMaxWheelSteps
	scrollPixels := s.scrollMaxOffsetY
	if scrollPixels == 0 {
		scrollPixels = s.scrollOffsetY
	}
	if scrollSteps == 0 {
		scrollSteps = s.scrollWheelSteps
	}
	if scrollSteps == 0 && scrollPixels > 0 {
		scrollSteps = 1
	}
	var initialPercent *float64
	initialPoint := image.Point{}
	if s.scrollInitialPosition != nil {
		percent := s.scrollInitialPosition.Percent
		initialPercent = &percent
		initialPoint = s.scrollInitialPosition.WheelPoint.Sub(s.virtualMin)
	} else {
		initialPoint = s.scrollWheelPoint
	}
	return &ScrollRangeSelection{
		WindowRect:           windowRect,
		CropRect:             cropRect,
		ViewRect:             viewRect,
		ScrollSteps:          scrollSteps,
		ScrollPixels:         scrollPixels,
		InitialScrollPercent: initialPercent,
		InitialScrollPoint:   initialPoint,
		InitialWheelUnits:    s.scrollWheelUnits,
	}
}

func (s *nativeSession) scrollSelectionCurrentContentRect() image.Rectangle {
	start := s.scrollStartContent
	if start == (image.Point{}) {
		start = s.start
	}
	current := s.scrollCurrentContent
	if current == (image.Point{}) {
		current = s.scrollDocumentPoint(s.current)
	}
	return image.Rectangle{Min: start, Max: current}.Canon()
}

func (s *nativeSession) scrollSelectionPreviewRect(windowRect image.Rectangle) image.Rectangle {
	rect := s.scrollSelectionContentRect(windowRect)
	rect.Min.Y -= s.scrollOffsetY
	rect.Max.Y -= s.scrollOffsetY
	rect.Min.X = clamp(rect.Min.X, windowRect.Min.X, windowRect.Max.X)
	rect.Max.X = clamp(rect.Max.X, windowRect.Min.X, windowRect.Max.X)
	return rect
}

func (s *nativeSession) scrollSelectionVisibleRect(windowRect image.Rectangle) (image.Rectangle, bool, bool) {
	preview := s.scrollSelectionPreviewRect(windowRect)
	clippedTop := preview.Min.Y < windowRect.Min.Y
	clippedBottom := preview.Max.Y > windowRect.Max.Y
	visible := preview.Intersect(windowRect)
	if visible.Empty() {
		return image.Rectangle{}, clippedTop, clippedBottom
	}
	return visible, clippedTop, clippedBottom
}

func (s *nativeSession) sendMouseWheel(hwnd uintptr, delta int, p image.Point) bool {
	if s.scrollWheelSender != nil {
		return s.scrollWheelSender(hwnd, delta, p)
	}
	if hwnd == 0 {
		return false
	}
	screenPoint := p.Add(s.virtualMin)
	procSetForegroundWindow.Call(hwnd)
	var original nativePoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&original)))
	procSetCursorPos.Call(intArg(screenPoint.X), intArg(screenPoint.Y))
	sent := uintptr(0)
	if s.scrollRestoring {
		procMouseEvent.Call(uintptr(mouseEventWheel), 0, 0, uintptr(uint32(int32(delta))), 0)
		sent = 1
	} else {
		input := scrollWheelInput(delta)
		sent, _, _ = procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	}
	time.Sleep(100 * time.Millisecond)
	procSetCursorPos.Call(intArg(int(original.X)), intArg(int(original.Y)))
	return sent == 1
}

func scrollWheelInput(delta int) nativeInput {
	return nativeInput{
		Type: inputMouse,
		Mi: nativeMouseInput{
			MouseData:   uint32(int32(delta)),
			DwFlags:     mouseEventWheel,
			DwExtraInfo: uintptr(scrollSyntheticInputTag),
		},
	}
}

func activateWheelTarget(hwnd uintptr) bool {
	currentThread, _, _ := procGetCurrentThreadID.Call()
	targetThread, _, _ := procGetWindowThreadProcessID.Call(hwnd, 0)
	attached := currentThread != 0 && targetThread != 0 && currentThread != targetThread
	if attached {
		if ok, _, _ := procAttachThreadInput.Call(currentThread, targetThread, 1); ok == 0 {
			return false
		}
		defer procAttachThreadInput.Call(currentThread, targetThread, 0)
	}
	ok, _, _ := procSetForegroundWindow.Call(hwnd)
	return ok != 0
}

func (s *nativeSession) beginScrollPassthrough(hwnd uintptr) bool {
	if hwnd == 0 || !activateWheelTarget(hwnd) {
		return false
	}
	if s.hwnd == 0 || s.scrollPassthrough {
		return true
	}
	style, _, _ := procGetWindowLongPtrW.Call(s.hwnd, intArg(gwlExStyle))
	if style == 0 {
		return false
	}
	procSetWindowLongPtrW.Call(s.hwnd, intArg(gwlExStyle), style|uintptr(wsExTransparent))
	procSetWindowPos.Call(s.hwnd, 0, 0, 0, 0, uintptr(swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged))
	s.scrollOriginalExStyle = style
	s.scrollPassthrough = true
	return true
}

func (s *nativeSession) endScrollPassthrough() {
	if !s.scrollPassthrough || s.hwnd == 0 {
		return
	}
	procSetWindowLongPtrW.Call(s.hwnd, intArg(gwlExStyle), s.scrollOriginalExStyle)
	procSetWindowPos.Call(s.hwnd, 0, 0, 0, 0, uintptr(swpNoSize|swpNoMove|swpNoZOrder|swpNoActivate|swpFrameChanged))
	s.scrollPassthrough = false
}

func focusedWheelTarget(hwnd uintptr) uintptr {
	threadID, _, _ := procGetWindowThreadProcessID.Call(hwnd, 0)
	if threadID == 0 {
		return hwnd
	}
	info := guiThreadInfo{CbSize: uint32(unsafe.Sizeof(guiThreadInfo{}))}
	if ok, _, _ := procGetGUIThreadInfo.Call(threadID, uintptr(unsafe.Pointer(&info))); ok != 0 && info.HwndFocus != 0 {
		return info.HwndFocus
	}
	return hwnd
}

func wheelMessageWParam(delta int) uintptr {
	return uintptr(uint32(uint16(int16(delta))) << 16)
}

func screenPointLParam(point image.Point) uintptr {
	return uintptr(uint32(uint16(int16(point.X))) | uint32(uint16(int16(point.Y)))<<16)
}

func appendPointIfFar(points []image.Point, p image.Point) []image.Point {
	if len(points) == 0 {
		return append(points, p)
	}
	last := points[len(points)-1]
	dx := p.X - last.X
	dy := p.Y - last.Y
	if dx*dx+dy*dy >= 16 {
		return append(points, p)
	}
	return points
}

func polygonBounds(points []image.Point) image.Rectangle {
	if len(points) == 0 {
		return image.Rectangle{}
	}
	minX, minY := points[0].X, points[0].Y
	maxX, maxY := minX, minY
	for _, p := range points[1:] {
		if p.X < minX {
			minX = p.X
		}
		if p.Y < minY {
			minY = p.Y
		}
		if p.X > maxX {
			maxX = p.X
		}
		if p.Y > maxY {
			maxY = p.Y
		}
	}
	return image.Rect(minX, minY, maxX+1, maxY+1)
}

func (s *nativeSession) drawCrosshair(hdc uintptr, clientW, clientH int) {
	x := s.cursor.X * clientW / max(1, s.imgBounds.Dx())
	y := s.cursor.Y * clientH / max(1, s.imgBounds.Dy())
	pen, _, _ := procCreatePen.Call(uintptr(psSolid), uintptr(1), uintptr(colorRef(255, 255, 255)))
	if pen == 0 {
		return
	}
	defer procDeleteObject.Call(pen)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	procMoveToEx.Call(hdc, 0, uintptr(y), 0)
	procLineTo.Call(hdc, uintptr(clientW), uintptr(y))
	procMoveToEx.Call(hdc, uintptr(x), 0, 0)
	procLineTo.Call(hdc, uintptr(x), uintptr(clientH))
	if oldPen != 0 {
		procSelectObject.Call(hdc, oldPen)
	}
}

func (s *nativeSession) drawMagnifier(hdc uintptr, clientW, clientH int) {
	const zoom = 4
	destination := s.magnifierDestinationRect(clientW, clientH)
	if destination.Empty() {
		return
	}
	boxWidth, boxHeight := destination.Dx(), destination.Dy()

	srcSize := max(1, min(boxWidth, boxHeight)/zoom)
	src := magnifierSourceRect(s.cursor, srcSize, s.imgBounds)
	buffer := s.original
	if len(buffer) == 0 {
		buffer = s.bgra
	}
	if src.Empty() || len(buffer) == 0 {
		return
	}
	target := hdc
	drawX, drawY := destination.Min.X, destination.Min.Y
	buffered := s.ensureMagnifierBuffer(hdc)
	if buffered {
		target = s.magnifierDC
		drawX, drawY = 0, 0
	}
	procStretchDIBits.Call(
		target,
		uintptr(drawX), uintptr(drawY), uintptr(boxWidth), uintptr(boxHeight),
		uintptr(src.Min.X), uintptr(magnifierDIBSourceY(src, s.imgBounds)), uintptr(src.Dx()), uintptr(src.Dy()),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&s.bmi)),
		uintptr(dibRGBColors),
		uintptr(srcCopy),
	)
	pen, _, _ := procCreatePen.Call(uintptr(psSolid), uintptr(2), uintptr(colorRef(255, 64, 64)))
	if pen != 0 {
		oldPen, _, _ := procSelectObject.Call(target, pen)
		brush, _, _ := procGetStockObject.Call(uintptr(nullBrush))
		oldBrush, _, _ := procSelectObject.Call(target, brush)
		procRectangle.Call(target, uintptr(drawX), uintptr(drawY), uintptr(drawX+boxWidth), uintptr(drawY+boxHeight))
		centerX := drawX + boxWidth/2
		centerY := drawY + boxHeight/2
		markerRadius := min(10, min(boxWidth, boxHeight)/2)
		procMoveToEx.Call(target, uintptr(centerX-markerRadius), uintptr(centerY), 0)
		procLineTo.Call(target, uintptr(centerX+markerRadius), uintptr(centerY))
		procMoveToEx.Call(target, uintptr(centerX), uintptr(centerY-markerRadius), 0)
		procLineTo.Call(target, uintptr(centerX), uintptr(centerY+markerRadius))
		if oldBrush != 0 {
			procSelectObject.Call(target, oldBrush)
		}
		if oldPen != 0 {
			procSelectObject.Call(target, oldPen)
		}
		procDeleteObject.Call(pen)
	}
	if buffered {
		procBitBlt.Call(
			hdc,
			uintptr(destination.Min.X), uintptr(destination.Min.Y), uintptr(boxWidth), uintptr(boxHeight),
			s.magnifierDC,
			0, 0,
			uintptr(srcCopy),
		)
	}
}

func magnifierDestinationRect(cursor image.Point, clientW, clientH int, bounds image.Rectangle) image.Rectangle {
	return magnifierDestinationRectForDisplays(cursor, clientW, clientH, bounds, nil)
}

func (s *nativeSession) magnifierDestinationRect(clientW, clientH int) image.Rectangle {
	return magnifierDestinationRectForDisplays(s.cursor, clientW, clientH, s.imgBounds, s.displays)
}

func magnifierDestinationRectForDisplays(cursor image.Point, clientW, clientH int, bounds image.Rectangle, displays []image.Rectangle) image.Rectangle {
	const (
		boxSize = 160
		offset  = 24
	)
	if clientW <= 0 || clientH <= 0 || bounds.Empty() {
		return image.Rectangle{}
	}
	cursorX := (cursor.X - bounds.Min.X) * clientW / max(1, bounds.Dx())
	cursorY := (cursor.Y - bounds.Min.Y) * clientH / max(1, bounds.Dy())
	limit := image.Rect(0, 0, clientW, clientH)
	cursorPixel := image.Pt(
		clamp(cursor.X, bounds.Min.X, bounds.Max.X-1),
		clamp(cursor.Y, bounds.Min.Y, bounds.Max.Y-1),
	)
	for _, display := range displays {
		display = display.Canon().Intersect(bounds)
		if display.Empty() || !cursorPixel.In(display) {
			continue
		}
		limit = image.Rect(
			(display.Min.X-bounds.Min.X)*clientW/max(1, bounds.Dx()),
			(display.Min.Y-bounds.Min.Y)*clientH/max(1, bounds.Dy()),
			(display.Max.X-bounds.Min.X)*clientW/max(1, bounds.Dx()),
			(display.Max.Y-bounds.Min.Y)*clientH/max(1, bounds.Dy()),
		).Intersect(limit)
		break
	}
	if limit.Empty() {
		return image.Rectangle{}
	}
	size := min(boxSize, min(limit.Dx(), limit.Dy()))
	dstX, dstY := cursorX+offset, cursorY+offset
	if dstX+size > limit.Max.X {
		dstX = cursorX - size - offset
	}
	if dstY+size > limit.Max.Y {
		dstY = cursorY - size - offset
	}
	dstX = clamp(dstX, limit.Min.X, max(limit.Min.X, limit.Max.X-size))
	dstY = clamp(dstY, limit.Min.Y, max(limit.Min.Y, limit.Max.Y-size))
	return image.Rect(dstX, dstY, dstX+size, dstY+size)
}

func (s *nativeSession) ensureMagnifierBuffer(hdc uintptr) bool {
	if s.magnifierDC != 0 && s.magnifierBitmap != 0 {
		return true
	}
	dc, _, _ := procCreateCompatibleDC.Call(hdc)
	if dc == 0 {
		return false
	}
	bitmap, _, _ := procCreateCompatibleBitmap.Call(hdc, 160, 160)
	if bitmap == 0 {
		procDeleteDC.Call(dc)
		return false
	}
	oldBitmap, _, _ := procSelectObject.Call(dc, bitmap)
	if oldBitmap == 0 {
		procDeleteObject.Call(bitmap)
		procDeleteDC.Call(dc)
		return false
	}
	s.magnifierDC = dc
	s.magnifierBitmap = bitmap
	s.magnifierOldBitmap = oldBitmap
	return true
}

func (s *nativeSession) releasePaintResources() {
	if s.magnifierDC != 0 && s.magnifierOldBitmap != 0 {
		procSelectObject.Call(s.magnifierDC, s.magnifierOldBitmap)
	}
	if s.magnifierBitmap != 0 {
		procDeleteObject.Call(s.magnifierBitmap)
	}
	if s.magnifierDC != 0 {
		procDeleteDC.Call(s.magnifierDC)
	}
	s.magnifierDC = 0
	s.magnifierBitmap = 0
	s.magnifierOldBitmap = 0
}

func (s *nativeSession) clientToImage(p image.Point) image.Point {
	clientW, clientH := s.currentClientSize()
	return clientPointToImage(p, clientW, clientH, s.imgBounds)
}

func (s *nativeSession) currentClientSize() (int, int) {
	if s.hwnd != 0 {
		client := s.clientRect(s.hwnd)
		w := int(client.Right - client.Left)
		h := int(client.Bottom - client.Top)
		if w > 0 && h > 0 {
			return w, h
		}
	}
	return s.winWidth, s.winHeight
}

func clientPointToImage(p image.Point, clientW, clientH int, bounds image.Rectangle) image.Point {
	clientW = max(1, clientW)
	clientH = max(1, clientH)
	p.X = clamp(p.X, 0, clientW)
	p.Y = clamp(p.Y, 0, clientH)
	return image.Pt(
		clamp(bounds.Min.X+p.X*bounds.Dx()/clientW, bounds.Min.X, bounds.Max.X),
		clamp(bounds.Min.Y+p.Y*bounds.Dy()/clientH, bounds.Min.Y, bounds.Max.Y),
	)
}

func magnifierSourceRect(cursor image.Point, size int, bounds image.Rectangle) image.Rectangle {
	if bounds.Empty() || size <= 0 {
		return image.Rectangle{}
	}
	size = min(size, min(bounds.Dx(), bounds.Dy()))
	x := clamp(cursor.X-size/2, bounds.Min.X, max(bounds.Min.X, bounds.Max.X-size))
	y := clamp(cursor.Y-size/2, bounds.Min.Y, max(bounds.Min.Y, bounds.Max.Y-size))
	return image.Rect(x, y, x+size, y+size)
}

func magnifierDIBSourceY(src, bounds image.Rectangle) int {
	if src.Empty() || bounds.Empty() {
		return 0
	}
	return clamp(bounds.Min.Y+bounds.Max.Y-src.Max.Y, bounds.Min.Y, max(bounds.Min.Y, bounds.Max.Y-src.Dy()))
}

func lParamPoint(lParam uintptr) image.Point {
	x := int(int16(uint16(lParam & 0xffff)))
	y := int(int16(uint16((lParam >> 16) & 0xffff)))
	return image.Pt(x, y)
}

func intArg(v int) uintptr {
	return uintptr(uint32(int32(v)))
}

func colorRef(r, g, b byte) uint32 {
	return uint32(r) | uint32(g)<<8 | uint32(b)<<16
}

func clamp(v, minValue, maxValue int) int {
	if v < minValue {
		return minValue
	}
	if v > maxValue {
		return maxValue
	}
	return v
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
