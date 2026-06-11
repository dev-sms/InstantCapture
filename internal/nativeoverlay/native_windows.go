//go:build windows

package nativeoverlay

import (
	"context"
	"fmt"
	"image"
	"image/draw"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"

	"instantcapture/internal/overlay"
)

const (
	wsPopup   = 0x80000000
	wsVisible = 0x10000000

	wsExTopMost     = 0x00000008
	wsExTransparent = 0x00000020
	wsExToolWindow  = 0x00000080
	wsExLayered     = 0x00080000
	gwlExStyle      = -20

	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmEraseBkgnd  = 0x0014
	wmPaint       = 0x000F
	wmKeyDown     = 0x0100
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmMouseMove   = 0x0200
	wmMouseWheel  = 0x020A

	vkEscape   = 0x1B
	wheelDelta = 120

	scrollEdgeThreshold              = 36
	scrollStepPixels                 = 360
	scrollTickInterval               = 90 * time.Millisecond
	scrollScoreTopSkipMax            = 96
	scrollScoreBottomSkipMax         = 48
	scrollScoreUnchangedRowThreshold = 2
	scrollScoreMinSamples            = 16
	smtoAbortIfHung                  = 0x0002
	wheelMessageTimeout              = 80
	cwpSkipInvisible                 = 0x0001
	cwpSkipDisabled                  = 0x0002
	cwpSkipTransparent               = 0x0004
	inputMouse                       = 0
	mouseEventWheel                  = 0x0800

	swHide        = 0
	swShow        = 5
	swpShowWindow = 0x0040

	lwaAlpha = 0x00000002

	dibRGBColors = 0
	biRGB        = 0
	srcCopy      = 0x00CC0020

	psSolid   = 0
	nullBrush = 5
)

var (
	nativeUser32   = syscall.NewLazyDLL("user32.dll")
	nativeGDI32    = syscall.NewLazyDLL("gdi32.dll")
	nativeKernel32 = syscall.NewLazyDLL("kernel32.dll")

	procGetModuleHandleW = nativeKernel32.NewProc("GetModuleHandleW")

	procRegisterClassExW          = nativeUser32.NewProc("RegisterClassExW")
	procCreateWindowExW           = nativeUser32.NewProc("CreateWindowExW")
	procDefWindowProcW            = nativeUser32.NewProc("DefWindowProcW")
	procGetWindowLongPtrW         = nativeUser32.NewProc("GetWindowLongPtrW")
	procSetWindowLongPtrW         = nativeUser32.NewProc("SetWindowLongPtrW")
	procFillRect                  = nativeUser32.NewProc("FillRect")
	procDestroyWindow             = nativeUser32.NewProc("DestroyWindow")
	procShowWindow                = nativeUser32.NewProc("ShowWindow")
	procUpdateWindow              = nativeUser32.NewProc("UpdateWindow")
	procGetMessageW               = nativeUser32.NewProc("GetMessageW")
	procTranslateMessage          = nativeUser32.NewProc("TranslateMessage")
	procDispatchMessageW          = nativeUser32.NewProc("DispatchMessageW")
	procPostQuitMessage           = nativeUser32.NewProc("PostQuitMessage")
	procPostMessageW              = nativeUser32.NewProc("PostMessageW")
	procSendMessageTimeoutW       = nativeUser32.NewProc("SendMessageTimeoutW")
	procBeginPaint                = nativeUser32.NewProc("BeginPaint")
	procEndPaint                  = nativeUser32.NewProc("EndPaint")
	procInvalidateRect            = nativeUser32.NewProc("InvalidateRect")
	procSetWindowPos              = nativeUser32.NewProc("SetWindowPos")
	procSetLayeredWindowAttribute = nativeUser32.NewProc("SetLayeredWindowAttributes")
	procSetCapture                = nativeUser32.NewProc("SetCapture")
	procReleaseCapture            = nativeUser32.NewProc("ReleaseCapture")
	procSetForegroundWindow       = nativeUser32.NewProc("SetForegroundWindow")
	procGetCursorPos              = nativeUser32.NewProc("GetCursorPos")
	procSetCursorPos              = nativeUser32.NewProc("SetCursorPos")
	procSendInput                 = nativeUser32.NewProc("SendInput")
	procGetDC                     = nativeUser32.NewProc("GetDC")
	procReleaseDC                 = nativeUser32.NewProc("ReleaseDC")
	procGetClientRect             = nativeUser32.NewProc("GetClientRect")
	procScreenToClient            = nativeUser32.NewProc("ScreenToClient")
	procClientToScreen            = nativeUser32.NewProc("ClientToScreen")
	procChildWindowFromPointEx    = nativeUser32.NewProc("ChildWindowFromPointEx")

	procStretchDIBits          = nativeGDI32.NewProc("StretchDIBits")
	procCreateCompatibleDC     = nativeGDI32.NewProc("CreateCompatibleDC")
	procDeleteDC               = nativeGDI32.NewProc("DeleteDC")
	procCreateCompatibleBitmap = nativeGDI32.NewProc("CreateCompatibleBitmap")
	procBitBlt                 = nativeGDI32.NewProc("BitBlt")
	procGetDIBits              = nativeGDI32.NewProc("GetDIBits")
	procCreateSolidBrush       = nativeGDI32.NewProc("CreateSolidBrush")
	procCreatePen              = nativeGDI32.NewProc("CreatePen")
	procSelectObject           = nativeGDI32.NewProc("SelectObject")
	procRectangle              = nativeGDI32.NewProc("Rectangle")
	procMoveToEx               = nativeGDI32.NewProc("MoveToEx")
	procLineTo                 = nativeGDI32.NewProc("LineTo")
	procDeleteObject           = nativeGDI32.NewProc("DeleteObject")
	procGetStockObject         = nativeGDI32.NewProc("GetStockObject")

	wndProcCallback = syscall.NewCallback(nativeWndProc)

	activeMu      sync.Mutex
	activeSession *nativeSession
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

type nativeInput struct {
	Type uint32
	Mi   nativeMouseInput
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
	hwnd                 uintptr
	result               chan nativeResult
	finished             bool
	imgBounds            image.Rectangle
	virtualMin           image.Point
	winWidth             int
	winHeight            int
	bgra                 []byte
	original             []byte
	highlight            []byte
	bmi                  bitmapInfo
	hasCursor            bool
	cursor               image.Point
	selecting            bool
	start                image.Point
	current              image.Point
	mode                 string
	liveBackground       bool
	windows              []nativeWindow
	hover                int
	scrollWin            int
	scrollOffsetY        int
	scrollWheelSteps     int
	scrollMaxWheelSteps  int
	scrollStartContent   image.Point
	scrollCurrentContent image.Point
	scrollMaxContentY    int
	scrollRangeEndViewY  int
	lastScroll           time.Time
	polygon              []image.Point
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
	session.liveBackground = opt.LiveBackground
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
	hwnd, _, callErr := procCreateWindowExW.Call(
		exStyle,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(windowName)),
		uintptr(wsPopup|wsVisible),
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

	alpha := uintptr(245)
	if opt.LiveBackground {
		alpha = 105
	}
	procSetLayeredWindowAttribute.Call(hwnd, 0, alpha, uintptr(lwaAlpha))
	procSetWindowPos.Call(hwnd, ^uintptr(0), intArg(virtualScreen.Min.X), intArg(virtualScreen.Min.Y), uintptr(virtualScreen.Dx()), uintptr(virtualScreen.Dy()), uintptr(swpShowWindow))
	procShowWindow.Call(hwnd, uintptr(swShow))
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
	wc := wndClassEx{
		CbSize:        uint32(unsafe.Sizeof(wndClassEx{})),
		LpfnWndProc:   wndProcCallback,
		HInstance:     instance,
		LpszClassName: className,
	}
	atom, _, err := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
	if atom == 0 && err != syscall.Errno(1410) {
		return fmt.Errorf("%w: register window class: %v", ErrUnavailable, err)
	}
	return nil
}

func newNativeSession(img image.Image, virtualScreen image.Rectangle, dimAlpha uint8) (*nativeSession, error) {
	rgba := image.NewRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(rgba, rgba.Bounds(), img, img.Bounds().Min, draw.Src)
	bgra := make([]byte, rgba.Bounds().Dx()*rgba.Bounds().Dy()*4)
	original := make([]byte, len(bgra))
	dim := 1 - (float64(dimAlpha)/255.0)*0.65
	if dim < 0.2 {
		dim = 0.2
	}
	for y := 0; y < rgba.Bounds().Dy(); y++ {
		for x := 0; x < rgba.Bounds().Dx(); x++ {
			src := rgba.PixOffset(x, y)
			dst := (y*rgba.Bounds().Dx() + x) * 4
			original[dst+0] = rgba.Pix[src+2]
			original[dst+1] = rgba.Pix[src+1]
			original[dst+2] = rgba.Pix[src+0]
			original[dst+3] = rgba.Pix[src+3]
			bgra[dst+0] = uint8(float64(original[dst+0]) * dim)
			bgra[dst+1] = uint8(float64(original[dst+1]) * dim)
			bgra[dst+2] = uint8(float64(original[dst+2]) * dim)
			bgra[dst+3] = original[dst+3]
		}
	}
	bmi := bitmapInfo{}
	bmi.Header = bitmapInfoHeader{
		BiSize:        uint32(unsafe.Sizeof(bitmapInfoHeader{})),
		BiWidth:       int32(rgba.Bounds().Dx()),
		BiHeight:      -int32(rgba.Bounds().Dy()),
		BiPlanes:      1,
		BiBitCount:    32,
		BiCompression: biRGB,
	}
	return &nativeSession{
		result:     make(chan nativeResult, 1),
		imgBounds:  image.Rect(0, 0, rgba.Bounds().Dx(), rgba.Bounds().Dy()),
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
				s.current = s.start
				s.beginScrollSelection()
				procSetCapture.Call(hwnd)
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
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case wmMouseWheel:
		if s != nil && s.mode == "Scroll" {
			delta := int(int16(uint16((wParam >> 16) & 0xffff)))
			s.handleScrollWheel(delta)
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case wmLButtonUp:
		if s != nil && s.selecting {
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
				cropRect := s.scrollSelectionContentRect(windowRect)
				viewEnd := image.Pt(s.current.X, s.scrollRangeEndViewY)
				viewRect := image.Rectangle{Min: s.start, Max: viewEnd}.Canon()
				cropRect.Min.X = clamp(cropRect.Min.X, windowRect.Min.X, windowRect.Max.X)
				cropRect.Max.X = clamp(cropRect.Max.X, windowRect.Min.X, windowRect.Max.X)
				cropRect.Min.Y = max(cropRect.Min.Y, windowRect.Min.Y)
				viewRect.Min.X = clamp(viewRect.Min.X, windowRect.Min.X, windowRect.Max.X)
				viewRect.Max.X = clamp(viewRect.Max.X, windowRect.Min.X, windowRect.Max.X)
				viewRect.Min.Y = clamp(viewRect.Min.Y, windowRect.Min.Y, windowRect.Max.Y)
				viewRect.Max.Y = clamp(viewRect.Max.Y, windowRect.Min.Y, windowRect.Max.Y)
				scrollSteps := s.scrollMaxWheelSteps
				s.restoreAutoScroll()
				if cropRect.Max.Y <= cropRect.Min.Y || cropRect.Dx() <= 0 {
					s.finish(Selection{Rect: windowRect}, true, nil)
				} else {
					s.finish(Selection{Rect: windowRect, ScrollRange: &ScrollRangeSelection{WindowRect: windowRect, CropRect: cropRect, ViewRect: viewRect, ScrollSteps: scrollSteps}}, true, nil)
				}
			} else {
				rect := image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(s.imgBounds)
				s.finish(Selection{Rect: rect}, !rect.Empty(), nil)
			}
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case wmKeyDown:
		if wParam == vkEscape {
			if s != nil {
				s.restoreAutoScroll()
				s.finish(Selection{}, false, nil)
			}
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case wmClose:
		if s != nil {
			s.restoreAutoScroll()
			s.finish(Selection{}, false, nil)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if s != nil {
			s.restoreAutoScroll()
			s.finish(Selection{}, false, nil)
		}
		procPostQuitMessage.Call(0)
		return 0
	}
	ret, _, _ := procDefWindowProcW.Call(hwnd, uintptr(msg), wParam, lParam)
	return ret
}

func currentSession() *nativeSession {
	activeMu.Lock()
	defer activeMu.Unlock()
	return activeSession
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
	if s.hasCursor {
		s.drawCrosshair(hdc, w, h)
		if !s.liveBackground {
			s.drawMagnifier(hdc, w, h)
		}
	}
	if s.selecting {
		if s.mode == "Freeform" {
			s.drawFreeform(hdc, w, h)
		} else {
			s.drawSelection(hdc, w, h)
		}
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
	if (s.mode != "Window" && s.mode != "Scroll") || s.hover < 0 || s.hover >= len(s.windows) || len(s.original) != len(s.bgra) || len(s.highlight) != len(s.bgra) {
		return s.bgra
	}
	copy(s.highlight, s.bgra)
	stride := max(1, s.imgBounds.Dx()) * 4
	for _, rect := range s.windows[s.hover].visibleRects {
		rect = rect.Intersect(s.imgBounds)
		if rect.Empty() {
			continue
		}
		rowBytes := rect.Dx() * 4
		for y := rect.Min.Y; y < rect.Max.Y; y++ {
			start := y*stride + rect.Min.X*4
			copy(s.highlight[start:start+rowBytes], s.original[start:start+rowBytes])
		}
	}
	return s.highlight
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
		rect = s.scrollSelectionPreviewRect(windowRect)
		if rect.Empty() || rect.Max.Y <= windowRect.Min.Y || rect.Min.Y >= windowRect.Max.Y {
			return
		}
		s.drawRect(hdc, clientW, clientH, rect, colorRef(255, 64, 64), 3)
		return
	}
	rect = rect.Intersect(s.imgBounds)
	if rect.Empty() {
		return
	}
	s.drawRect(hdc, clientW, clientH, rect, colorRef(255, 64, 64), 3)
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
	s.scrollMaxWheelSteps = 0
	s.scrollStartContent = s.scrollContentPoint(s.start)
	s.scrollCurrentContent = s.scrollStartContent
	s.scrollMaxContentY = s.scrollStartContent.Y
	s.scrollRangeEndViewY = s.start.Y
	s.lastScroll = time.Time{}
}

func (s *nativeSession) updateScrollSelectionCurrent() {
	s.scrollCurrentContent = s.scrollContentPoint(s.current)
	if s.scrollCurrentContent.Y > s.scrollMaxContentY {
		s.scrollMaxContentY = s.scrollCurrentContent.Y
		s.scrollMaxWheelSteps = s.scrollWheelSteps
		s.scrollRangeEndViewY = s.current.Y
	}
}

func (s *nativeSession) scrollContentPoint(p image.Point) image.Point {
	return image.Pt(p.X, p.Y+s.scrollOffsetY)
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
	} else if s.current.Y <= window.rect.Min.Y+scrollEdgeThreshold && s.scrollWheelSteps > 0 {
		direction = -1
	}
	if direction == 0 {
		return
	}
	s.lastScroll = time.Now()
	before := s.captureScrollSample(window.rect)
	if !s.sendMouseWheel(window.handle, -direction*wheelDelta, s.current) {
		return
	}
	s.scrollWheelSteps += direction
	if s.scrollWheelSteps < 0 {
		s.scrollWheelSteps = 0
	}
	after := s.captureScrollSample(window.rect)
	if !s.applyMeasuredScrollDelta(before, after, direction*scrollStepPixels) {
		s.scrollOffsetY = s.scrollWheelSteps * scrollStepPixels
	}
	s.updateScrollSelectionCurrent()
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
	steps := absInt(delta) / wheelDelta
	if steps == 0 {
		steps = 1
	}
	if s.selecting && delta > 0 {
		if s.scrollWheelSteps <= 0 {
			return
		}
		if steps > s.scrollWheelSteps {
			steps = s.scrollWheelSteps
			delta = steps * wheelDelta
		}
	}
	point := s.cursor
	if !point.In(window.rect) {
		point = image.Pt(window.rect.Min.X+window.rect.Dx()/2, window.rect.Min.Y+window.rect.Dy()/2)
	}
	before := s.captureScrollSample(window.rect)
	if !s.sendMouseWheel(window.handle, delta, point) {
		return
	}
	if s.selecting {
		expectedDelta := 0
		if delta < 0 {
			s.scrollWheelSteps += steps
			expectedDelta = steps * scrollStepPixels
		} else {
			s.scrollWheelSteps -= steps
			if s.scrollWheelSteps < 0 {
				s.scrollWheelSteps = 0
			}
			expectedDelta = -steps * scrollStepPixels
		}
		after := s.captureScrollSample(window.rect)
		if !s.applyMeasuredScrollDelta(before, after, expectedDelta) {
			s.scrollOffsetY = s.scrollWheelSteps * scrollStepPixels
		}
		s.updateScrollSelectionCurrent()
	}
}

func (s *nativeSession) restoreAutoScroll() {
	if s.scrollWin < 0 || s.scrollWin >= len(s.windows) || s.scrollWheelSteps <= 0 {
		return
	}
	window := s.windows[s.scrollWin]
	if window.handle == 0 {
		return
	}
	point := image.Pt(window.rect.Min.X+window.rect.Dx()/2, window.rect.Min.Y+window.rect.Dy()/2)
	for i := 0; i < s.scrollWheelSteps; i++ {
		s.sendMouseWheel(window.handle, wheelDelta, point)
	}
	s.scrollWheelSteps = 0
	time.Sleep(160 * time.Millisecond)
}

func (s *nativeSession) applyMeasuredScrollDelta(before, after scrollSample, fallback int) bool {
	delta, ok := measureScrollDelta(before, after, max(80, absInt(fallback)+240))
	if !ok {
		return false
	}
	if fallback != 0 && delta != 0 && (delta > 0) != (fallback > 0) {
		return false
	}
	s.scrollOffsetY += delta
	if s.scrollOffsetY < 0 {
		s.scrollOffsetY = 0
	}
	return true
}

func (s *nativeSession) captureScrollSample(rect image.Rectangle) scrollSample {
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
	if before.width <= 0 || before.height <= 0 || before.width != after.width || before.height != after.height || len(before.luma) != before.width*before.height || len(after.luma) != after.width*after.height {
		return 0, false
	}
	maxAbs = min(maxAbs, before.height-4)
	if maxAbs <= 0 {
		return 0, false
	}
	bestDelta := 0
	bestScore := int64(1<<62 - 1)
	secondScore := int64(1<<62 - 1)
	for delta := -maxAbs; delta <= maxAbs; delta++ {
		score, ok := scrollDeltaScore(before, after, delta)
		if !ok {
			continue
		}
		if score < bestScore {
			secondScore = bestScore
			bestScore = score
			bestDelta = delta
		} else if score < secondScore {
			secondScore = score
		}
	}
	if bestDelta == 0 || bestScore == int64(1<<62-1) {
		return 0, false
	}
	minGap := int64(50)
	if bestScore/20 > minGap {
		minGap = bestScore / 20
	}
	if secondScore != int64(1<<62-1) && secondScore-bestScore < minGap {
		return 0, false
	}
	return bestDelta, true
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
	if rect.Min.Y < windowRect.Min.Y {
		rect.Min.Y = windowRect.Min.Y
	}
	if rect.Max.Y < windowRect.Min.Y {
		rect.Max.Y = windowRect.Min.Y
	}
	return rect
}

func (s *nativeSession) scrollSelectionCurrentContentRect() image.Rectangle {
	start := s.scrollStartContent
	if start == (image.Point{}) {
		start = s.start
	}
	current := s.scrollCurrentContent
	if current == (image.Point{}) {
		current = s.scrollContentPoint(s.current)
	}
	return image.Rectangle{Min: start, Max: current}.Canon()
}

func (s *nativeSession) scrollSelectionPreviewRect(windowRect image.Rectangle) image.Rectangle {
	rect := s.scrollSelectionCurrentContentRect()
	rect.Min.Y -= s.scrollOffsetY
	rect.Max.Y -= s.scrollOffsetY
	rect.Min.X = clamp(rect.Min.X, windowRect.Min.X, windowRect.Max.X)
	rect.Max.X = clamp(rect.Max.X, windowRect.Min.X, windowRect.Max.X)
	return rect
}

func (s *nativeSession) sendMouseWheel(hwnd uintptr, delta int, p image.Point) bool {
	_, screenPoint := s.scrollMessageTarget(hwnd, p)
	var original nativePoint
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&original)))
	wasSelecting := s.selecting
	if wasSelecting {
		procReleaseCapture.Call()
	}
	oldExStyle := uintptr(0)
	if s.hwnd != 0 {
		oldExStyle, _, _ = procGetWindowLongPtrW.Call(s.hwnd, intArg(gwlExStyle))
		procSetWindowLongPtrW.Call(s.hwnd, intArg(gwlExStyle), oldExStyle|uintptr(wsExTransparent))
	}
	procSetForegroundWindow.Call(hwnd)
	procSetCursorPos.Call(intArg(screenPoint.X), intArg(screenPoint.Y))
	input := nativeInput{
		Type: inputMouse,
		Mi: nativeMouseInput{
			MouseData: uint32(int32(delta)),
			DwFlags:   mouseEventWheel,
		},
	}
	sent, _, _ := procSendInput.Call(1, uintptr(unsafe.Pointer(&input)), unsafe.Sizeof(input))
	time.Sleep(70 * time.Millisecond)
	procSetCursorPos.Call(intArg(int(original.X)), intArg(int(original.Y)))
	if s.hwnd != 0 {
		procSetWindowLongPtrW.Call(s.hwnd, intArg(gwlExStyle), oldExStyle)
		procInvalidateRect.Call(s.hwnd, 0, 0)
	}
	if s.hwnd != 0 && wasSelecting {
		procSetCapture.Call(s.hwnd)
	}
	return sent == 1
}

func (s *nativeSession) scrollMessageTarget(hwnd uintptr, p image.Point) (uintptr, image.Point) {
	screen := p.Add(s.virtualMin)
	if hwnd == 0 {
		return 0, screen
	}
	client := nativePoint{X: int32(screen.X), Y: int32(screen.Y)}
	if ret, _, _ := procScreenToClient.Call(hwnd, uintptr(unsafe.Pointer(&client))); ret == 0 {
		return hwnd, screen
	}
	var rect nativeRect
	if ret, _, _ := procGetClientRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ret != 0 && rect.Right > rect.Left && rect.Bottom > rect.Top {
		client.X = int32(clamp(int(client.X), int(rect.Left), int(rect.Right-1)))
		client.Y = int32(clamp(int(client.Y), int(rect.Top), int(rect.Bottom-1)))
	}
	clampedScreen := client
	if ret, _, _ := procClientToScreen.Call(hwnd, uintptr(unsafe.Pointer(&clampedScreen))); ret != 0 {
		screen = image.Pt(int(clampedScreen.X), int(clampedScreen.Y))
	}
	child, _, _ := procChildWindowFromPointEx.Call(
		hwnd,
		uintptr(uint64(uint32(client.X))|uint64(uint32(client.Y))<<32),
		uintptr(cwpSkipInvisible|cwpSkipDisabled|cwpSkipTransparent),
	)
	if child != 0 {
		return child, screen
	}
	return hwnd, screen
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
	const (
		boxSize = 160
		zoom    = 4
	)
	dstX := s.cursor.X*clientW/max(1, s.imgBounds.Dx()) + 24
	dstY := s.cursor.Y*clientH/max(1, s.imgBounds.Dy()) + 24
	if dstX+boxSize > clientW {
		dstX = dstX - boxSize - 48
	}
	if dstY+boxSize > clientH {
		dstY = dstY - boxSize - 48
	}
	dstX = clamp(dstX, 0, max(0, clientW-boxSize))
	dstY = clamp(dstY, 0, max(0, clientH-boxSize))

	srcSize := boxSize / zoom
	src := magnifierSourceRect(s.cursor, srcSize, s.imgBounds)
	buffer := s.original
	if len(buffer) == 0 {
		buffer = s.bgra
	}
	if src.Empty() || len(buffer) == 0 {
		return
	}
	procStretchDIBits.Call(
		hdc,
		uintptr(dstX), uintptr(dstY), uintptr(boxSize), uintptr(boxSize),
		uintptr(src.Min.X), uintptr(magnifierDIBSourceY(src, s.imgBounds)), uintptr(src.Dx()), uintptr(src.Dy()),
		uintptr(unsafe.Pointer(&buffer[0])),
		uintptr(unsafe.Pointer(&s.bmi)),
		uintptr(dibRGBColors),
		uintptr(srcCopy),
	)
	pen, _, _ := procCreatePen.Call(uintptr(psSolid), uintptr(2), uintptr(colorRef(255, 64, 64)))
	if pen == 0 {
		return
	}
	defer procDeleteObject.Call(pen)
	oldPen, _, _ := procSelectObject.Call(hdc, pen)
	brush, _, _ := procGetStockObject.Call(uintptr(nullBrush))
	oldBrush, _, _ := procSelectObject.Call(hdc, brush)
	procRectangle.Call(hdc, uintptr(dstX), uintptr(dstY), uintptr(dstX+boxSize), uintptr(dstY+boxSize))
	centerX := dstX + boxSize/2
	centerY := dstY + boxSize/2
	procMoveToEx.Call(hdc, uintptr(centerX-10), uintptr(centerY), 0)
	procLineTo.Call(hdc, uintptr(centerX+10), uintptr(centerY))
	procMoveToEx.Call(hdc, uintptr(centerX), uintptr(centerY-10), 0)
	procLineTo.Call(hdc, uintptr(centerX), uintptr(centerY+10))
	if oldBrush != 0 {
		procSelectObject.Call(hdc, oldBrush)
	}
	if oldPen != 0 {
		procSelectObject.Call(hdc, oldPen)
	}
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
