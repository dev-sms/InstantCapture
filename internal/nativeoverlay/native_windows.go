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
	"unsafe"

	"golang.org/x/sys/windows"

	"instantcapture/internal/overlay"
)

const (
	wsPopup   = 0x80000000
	wsVisible = 0x10000000

	wsExTopMost    = 0x00000008
	wsExToolWindow = 0x00000080
	wsExLayered    = 0x00080000

	wmDestroy     = 0x0002
	wmClose       = 0x0010
	wmEraseBkgnd  = 0x0014
	wmPaint       = 0x000F
	wmKeyDown     = 0x0100
	wmLButtonDown = 0x0201
	wmLButtonUp   = 0x0202
	wmMouseMove   = 0x0200

	vkEscape = 0x1B

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
	procDestroyWindow             = nativeUser32.NewProc("DestroyWindow")
	procShowWindow                = nativeUser32.NewProc("ShowWindow")
	procUpdateWindow              = nativeUser32.NewProc("UpdateWindow")
	procGetMessageW               = nativeUser32.NewProc("GetMessageW")
	procTranslateMessage          = nativeUser32.NewProc("TranslateMessage")
	procDispatchMessageW          = nativeUser32.NewProc("DispatchMessageW")
	procPostQuitMessage           = nativeUser32.NewProc("PostQuitMessage")
	procPostMessageW              = nativeUser32.NewProc("PostMessageW")
	procBeginPaint                = nativeUser32.NewProc("BeginPaint")
	procEndPaint                  = nativeUser32.NewProc("EndPaint")
	procInvalidateRect            = nativeUser32.NewProc("InvalidateRect")
	procSetWindowPos              = nativeUser32.NewProc("SetWindowPos")
	procSetLayeredWindowAttribute = nativeUser32.NewProc("SetLayeredWindowAttributes")
	procSetCapture                = nativeUser32.NewProc("SetCapture")
	procReleaseCapture            = nativeUser32.NewProc("ReleaseCapture")
	procGetClientRect             = nativeUser32.NewProc("GetClientRect")

	procStretchDIBits  = nativeGDI32.NewProc("StretchDIBits")
	procCreatePen      = nativeGDI32.NewProc("CreatePen")
	procSelectObject   = nativeGDI32.NewProc("SelectObject")
	procRectangle      = nativeGDI32.NewProc("Rectangle")
	procMoveToEx       = nativeGDI32.NewProc("MoveToEx")
	procLineTo         = nativeGDI32.NewProc("LineTo")
	procDeleteObject   = nativeGDI32.NewProc("DeleteObject")
	procGetStockObject = nativeGDI32.NewProc("GetStockObject")

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
	hwnd      uintptr
	result    chan nativeResult
	finished  bool
	imgBounds image.Rectangle
	winWidth  int
	winHeight int
	bgra      []byte
	original  []byte
	highlight []byte
	bmi       bitmapInfo
	hasCursor bool
	cursor    image.Point
	selecting bool
	start     image.Point
	current   image.Point
	mode      string
	windows   []nativeWindow
	hover     int
	polygon   []image.Point
}

type nativeWindow struct {
	rect         image.Rectangle
	visibleRects []image.Rectangle
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
			session.windows = append(session.windows, nativeWindow{rect: imageRect, visibleRects: visibleRects})
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
		result:    make(chan nativeResult, 1),
		imgBounds: image.Rect(0, 0, rgba.Bounds().Dx(), rgba.Bounds().Dy()),
		winWidth:  virtualScreen.Dx(),
		winHeight: virtualScreen.Dy(),
		bgra:      bgra,
		original:  original,
		highlight: make([]byte, len(bgra)),
		bmi:       bmi,
		hover:     -1,
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
			if s.mode == "Window" {
				s.hover = s.windowAt(s.cursor)
			}
			if s.selecting {
				s.current = s.cursor
				if s.mode == "Freeform" {
					s.polygon = appendPointIfFar(s.polygon, s.cursor)
				}
			}
			procInvalidateRect.Call(hwnd, 0, 0)
			return 0
		}
	case wmLButtonUp:
		if s != nil && s.selecting {
			s.cursor = s.clientToImage(lParamPoint(lParam))
			s.current = s.cursor
			s.hasCursor = true
			s.selecting = false
			procReleaseCapture.Call()
			if s.mode == "Freeform" {
				s.polygon = appendPointIfFar(s.polygon, s.current)
				rect := polygonBounds(s.polygon).Intersect(s.imgBounds)
				s.finish(Selection{Rect: rect, Polygon: append([]image.Point(nil), s.polygon...)}, !rect.Empty() && len(s.polygon) >= 3, nil)
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
				s.finish(Selection{}, false, nil)
			}
			procDestroyWindow.Call(hwnd)
			return 0
		}
	case wmClose:
		if s != nil {
			s.finish(Selection{}, false, nil)
		}
		procDestroyWindow.Call(hwnd)
		return 0
	case wmDestroy:
		if s != nil {
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
	if w <= 0 || h <= 0 || len(s.bgra) == 0 {
		return
	}
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
	if s.hasCursor {
		s.drawCrosshair(hdc, w, h)
		s.drawMagnifier(hdc, w, h)
	}
	if s.selecting {
		if s.mode == "Freeform" {
			s.drawFreeform(hdc, w, h)
		} else {
			s.drawSelection(hdc, w, h)
		}
	}
}

func (s *nativeSession) paintBuffer() []byte {
	if s.mode != "Window" || s.hover < 0 || s.hover >= len(s.windows) || len(s.original) != len(s.bgra) || len(s.highlight) != len(s.bgra) {
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
	rect := image.Rectangle{Min: s.start, Max: s.current}.Canon().Intersect(s.imgBounds)
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
	srcX := clamp(s.cursor.X-srcSize/2, 0, max(0, s.imgBounds.Dx()-srcSize))
	srcY := clamp(s.cursor.Y-srcSize/2, 0, max(0, s.imgBounds.Dy()-srcSize))
	procStretchDIBits.Call(
		hdc,
		uintptr(dstX), uintptr(dstY), uintptr(boxSize), uintptr(boxSize),
		uintptr(srcX), uintptr(srcY), uintptr(srcSize), uintptr(srcSize),
		uintptr(unsafe.Pointer(&s.bgra[0])),
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
	p.X = clamp(p.X, 0, max(1, s.winWidth))
	p.Y = clamp(p.Y, 0, max(1, s.winHeight))
	return image.Pt(
		clamp(p.X*s.imgBounds.Dx()/max(1, s.winWidth), s.imgBounds.Min.X, s.imgBounds.Max.X),
		clamp(p.Y*s.imgBounds.Dy()/max(1, s.winHeight), s.imgBounds.Min.Y, s.imgBounds.Max.Y),
	)
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
