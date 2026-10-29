//go:build windows

package windows

import (
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	xwindows "golang.org/x/sys/windows"
)

type WindowTarget struct {
	Handle       uintptr
	Title        string
	Rect         image.Rectangle
	VisibleRects []image.Rectangle
}

type ScrollCaptureRange struct {
	Crop                 image.Rectangle
	View                 image.Rectangle
	ScrollSteps          int
	ScrollPixels         int
	InitialScrollPercent *float64
	InitialScrollPoint   image.Point
	InitialWheelUnits    int
}

type windowRect struct {
	Left   int32
	Top    int32
	Right  int32
	Bottom int32
}

const (
	swHide    = 0
	swShow    = 5
	swRestore = 9

	dwmwaExtendedFrameBounds = 9
	dwmwaCloaked             = 14

	maxVisibilityGridLines = 96
	maxVisibleFragments    = 256
)

var (
	windowUser32               = syscall.NewLazyDLL("user32.dll")
	windowFindWindowW          = windowUser32.NewProc("FindWindowW")
	windowEnumWindows          = windowUser32.NewProc("EnumWindows")
	windowGetForegroundWindow  = windowUser32.NewProc("GetForegroundWindow")
	windowGetCursorPos         = windowUser32.NewProc("GetCursorPos")
	windowGetWindowRect        = windowUser32.NewProc("GetWindowRect")
	windowGetWindowTextLengthW = windowUser32.NewProc("GetWindowTextLengthW")
	windowGetWindowTextW       = windowUser32.NewProc("GetWindowTextW")
	windowIsWindowVisible      = windowUser32.NewProc("IsWindowVisible")
	windowShowWindow           = windowUser32.NewProc("ShowWindow")
	windowShowWindowAsync      = windowUser32.NewProc("ShowWindowAsync")
	windowSetForegroundWindow  = windowUser32.NewProc("SetForegroundWindow")

	windowDwmapi                = syscall.NewLazyDLL("dwmapi.dll")
	windowDwmGetWindowAttribute = windowDwmapi.NewProc("DwmGetWindowAttribute")
	windowDwmFlush              = windowDwmapi.NewProc("DwmFlush")

	windowHelperCache struct {
		once sync.Once
		path string
		err  error
	}
)

func HideWindowByTitle(title string) error {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return err
	}
	windowShowWindowAsync.Call(hwnd, uintptr(swHide))
	return nil
}

func FlushDesktopComposition() {
	windowDwmFlush.Call()
}

func RestoreWindowByTitle(title string) error {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return err
	}
	windowShowWindowAsync.Call(hwnd, uintptr(swRestore))
	windowShowWindowAsync.Call(hwnd, uintptr(swShow))
	windowSetForegroundWindow.Call(hwnd)
	return nil
}

func ForegroundIsWindowTitle(title string) bool {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return false
	}
	foreground, _, _ := windowGetForegroundWindow.Call()
	return foreground != 0 && foreground == hwnd
}

func ForegroundWindowRect(excludeTitle string) (image.Rectangle, error) {
	foreground, _, _ := windowGetForegroundWindow.Call()
	if foreground == 0 {
		return image.Rectangle{}, fmt.Errorf("no foreground window")
	}
	if hwnd, err := findWindowByTitle(excludeTitle); err == nil && hwnd == foreground {
		return image.Rectangle{}, fmt.Errorf("foreground window is %q", excludeTitle)
	}
	visible, _, _ := windowIsWindowVisible.Call(foreground)
	if visible == 0 {
		return image.Rectangle{}, fmt.Errorf("foreground window is not visible")
	}
	var rect windowRect
	ret, _, callErr := windowGetWindowRect.Call(foreground, uintptr(unsafe.Pointer(&rect)))
	if ret == 0 {
		return image.Rectangle{}, fmt.Errorf("get foreground window rect: %v", callErr)
	}
	result := image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom))
	if result.Empty() {
		return image.Rectangle{}, fmt.Errorf("foreground window rect is empty")
	}
	return result, nil
}

func VisibleWindowRects(excludeTitle string) []image.Rectangle {
	targets := VisibleWindowTargets(excludeTitle)
	rects := make([]image.Rectangle, 0, len(targets))
	for _, target := range targets {
		rects = append(rects, target.Rect)
	}
	return rects
}

func VisibleWindowTargets(excludeTitle string) []WindowTarget {
	return visibleWindowTargetsRaw(excludeTitle)
}

func visibleWindowTargetsRaw(excludeTitle string) []WindowTarget {
	excluded, _ := findWindowByTitle(excludeTitle)
	var targets []WindowTarget
	var occluders []image.Rectangle
	callback := syscall.NewCallback(func(hwnd uintptr, lparam uintptr) uintptr {
		if hwnd == 0 {
			return 1
		}
		visible, _, _ := windowIsWindowVisible.Call(hwnd)
		if visible == 0 {
			return 1
		}
		if hwndCloaked(hwnd) {
			return 1
		}
		title := windowTitle(hwnd)
		rect, ok := hwndWindowRect(hwnd)
		if !ok || !usableWindowRect(rect) {
			return 1
		}
		visibleRects := visibleFragments(rect, occluders)
		if len(visibleRects) > 0 && hwnd != excluded && usableWindowTitle(title, excludeTitle) {
			targets = append(targets, WindowTarget{Handle: hwnd, Title: title, Rect: rect, VisibleRects: visibleRects})
		}
		occluders = append(occluders, rect)
		return 1
	})
	windowEnumWindows.Call(callback, 0)
	return targets
}

func hwndCloaked(hwnd uintptr) bool {
	var cloaked uint32
	ret, _, _ := windowDwmGetWindowAttribute.Call(
		hwnd,
		uintptr(dwmwaCloaked),
		uintptr(unsafe.Pointer(&cloaked)),
		unsafe.Sizeof(cloaked),
	)
	return ret == 0 && cloaked != 0
}

func hwndWindowRect(hwnd uintptr) (image.Rectangle, bool) {
	var wr windowRect
	ret, _, _ := windowDwmGetWindowAttribute.Call(
		hwnd,
		uintptr(dwmwaExtendedFrameBounds),
		uintptr(unsafe.Pointer(&wr)),
		unsafe.Sizeof(wr),
	)
	if ret != 0 {
		ret, _, _ = windowGetWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&wr)))
		if ret == 0 {
			return image.Rectangle{}, false
		}
	}
	return image.Rect(int(wr.Left), int(wr.Top), int(wr.Right), int(wr.Bottom)), true
}

func visibleFragments(rect image.Rectangle, occluders []image.Rectangle) []image.Rectangle {
	if rect.Empty() {
		return nil
	}
	clipped := make([]image.Rectangle, 0, len(occluders))
	xs := []int{rect.Min.X, rect.Max.X}
	ys := []int{rect.Min.Y, rect.Max.Y}
	for _, occluder := range occluders {
		cut := occluder.Intersect(rect)
		if cut.Empty() {
			continue
		}
		if cut.Eq(rect) {
			return nil
		}
		clipped = append(clipped, cut)
		xs = append(xs, cut.Min.X, cut.Max.X)
		ys = append(ys, cut.Min.Y, cut.Max.Y)
	}
	if len(clipped) == 0 {
		return []image.Rectangle{rect}
	}
	xs = compactSortedInts(xs)
	ys = compactSortedInts(ys)
	if len(xs) > maxVisibilityGridLines || len(ys) > maxVisibilityGridLines {
		return sampledVisibleFragments(rect, clipped)
	}

	fragments := make([]image.Rectangle, 0, minInt(maxVisibleFragments, (len(xs)-1)*(len(ys)-1)))
	for yi := 0; yi < len(ys)-1; yi++ {
		for xi := 0; xi < len(xs)-1; xi++ {
			cell := image.Rect(xs[xi], ys[yi], xs[xi+1], ys[yi+1])
			if cell.Empty() || coveredByAny(cell, clipped) {
				continue
			}
			fragments = append(fragments, cell)
			if len(fragments) >= maxVisibleFragments {
				return fragments
			}
		}
	}
	return fragments
}

func compactSortedInts(values []int) []int {
	sort.Ints(values)
	write := 0
	for _, value := range values {
		if write == 0 || values[write-1] != value {
			values[write] = value
			write++
		}
	}
	return values[:write]
}

func sampledVisibleFragments(rect image.Rectangle, occluders []image.Rectangle) []image.Rectangle {
	const samplesPerAxis = 12
	cellW := maxInt(1, rect.Dx()/samplesPerAxis)
	cellH := maxInt(1, rect.Dy()/samplesPerAxis)
	var fragments []image.Rectangle
	for y := rect.Min.Y; y < rect.Max.Y; y += cellH {
		for x := rect.Min.X; x < rect.Max.X; x += cellW {
			cell := image.Rect(x, y, minInt(x+cellW, rect.Max.X), minInt(y+cellH, rect.Max.Y))
			if cell.Empty() || coveredByAny(cell, occluders) {
				continue
			}
			fragments = append(fragments, cell)
			if len(fragments) >= maxVisibleFragments {
				return fragments
			}
		}
	}
	return fragments
}

func coveredByAny(rect image.Rectangle, occluders []image.Rectangle) bool {
	for _, occluder := range occluders {
		if rect.In(occluder) {
			return true
		}
	}
	return false
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func CaptureWindowTarget(target WindowTarget) (image.Image, error) {
	return captureTargetWithHelper(target, "capture-window", 5*time.Second,
		"--method", "wgc",
		"--timeout-ms", "2500",
	)
}

func CaptureScrollTarget(target WindowTarget) (image.Image, error) {
	return captureTargetWithHelper(target, "capture-scroll", 10*time.Second,
		"--method", "wgc",
		"--timeout-ms", "2500",
		"--max-frames", "8",
		"--delay-ms", "100",
		"--overlap-px", "96",
	)
}

type ScrollPosition struct {
	Percent    float64
	WheelPoint image.Point
}

func QueryScrollPosition(target WindowTarget, wheelPoint image.Point) (ScrollPosition, error) {
	result, err := runScrollPositionHelper(target, "query-scroll", wheelPoint)
	if err != nil {
		return ScrollPosition{}, err
	}
	return ScrollPosition{Percent: result.ScrollPercent, WheelPoint: wheelPoint}, nil
}

func RestoreScrollPosition(target WindowTarget, position ScrollPosition) error {
	_, err := runScrollPositionHelper(target, "restore-scroll", position.WheelPoint,
		"--scroll-percent", strconv.FormatFloat(position.Percent, 'f', 6, 64),
	)
	return err
}

func runScrollPositionHelper(target WindowTarget, command string, wheelPoint image.Point, extraArgs ...string) (windowHelperResult, error) {
	helper, err := findWindowCaptureHelper()
	if err != nil {
		return windowHelperResult{}, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	args := []string{
		command,
		"--hwnd", fmt.Sprintf("0x%X", target.Handle),
		"--json",
		"--wheel-x", strconv.Itoa(wheelPoint.X),
		"--wheel-y", strconv.Itoa(wheelPoint.Y),
	}
	args = append(args, extraArgs...)
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return windowHelperResult{}, fmt.Errorf("window helper timed out during %s", command)
	}
	if err != nil {
		if message := helperFailureMessage(stdout); message != "" {
			return windowHelperResult{}, fmt.Errorf("window helper failed: %s%s", message, helperStderr(stderr.String()))
		}
		return windowHelperResult{}, fmt.Errorf("window helper failed: %w%s", err, helperStderr(stderr.String()))
	}
	var result windowHelperResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return windowHelperResult{}, fmt.Errorf("parse window helper JSON: %w", err)
	}
	if !result.OK {
		return windowHelperResult{}, fmt.Errorf("window helper failed: %s", result.Message)
	}
	return result, nil
}

func CaptureScrollRange(target WindowTarget, r ScrollCaptureRange) (image.Image, error) {
	crop := r.Crop.Canon()
	if crop.Empty() {
		return nil, fmt.Errorf("scroll range crop is empty")
	}
	if r.ScrollSteps > 64 {
		return nil, fmt.Errorf("scroll range exceeds 64 wheel steps")
	}
	rangeSteps := 0
	maxFrames := 96
	wheelPoint := scrollRangeWheelPoint(target, r)
	args := []string{
		"--method", "wgc",
		"--timeout-ms", "2500",
		"--max-frames", strconv.Itoa(maxFrames),
		"--delay-ms", "100",
		"--overlap-px", "96",
		"--crop-x", strconv.Itoa(crop.Min.X),
		"--crop-y", strconv.Itoa(crop.Min.Y),
		"--crop-w", strconv.Itoa(crop.Dx()),
		"--crop-h", strconv.Itoa(crop.Dy()),
		"--range-steps", strconv.Itoa(rangeSteps),
		"--range-distance-px", "0",
		"--range-end-y", "0",
		"--wheel-x", strconv.Itoa(wheelPoint.X),
		"--wheel-y", strconv.Itoa(wheelPoint.Y),
	}
	if r.InitialScrollPercent != nil {
		args = append(args, "--initial-scroll-percent", strconv.FormatFloat(*r.InitialScrollPercent, 'f', 6, 64))
	}
	return captureTargetWithHelper(target, "capture-scroll", scrollRangeCaptureTimeout(maxFrames, rangeSteps), args...)
}

func scrollRangeCaptureFrames(rangeSteps int, crop image.Rectangle, viewportHeight int) int {
	if rangeSteps == 0 {
		return 1
	}
	frames := rangeSteps + 4
	if estimated := scrollRangeMaxFrames(crop, viewportHeight); estimated > frames {
		frames = estimated
	}
	if frames > 96 {
		return 96
	}
	return frames
}

func scrollRangeCaptureTimeout(maxFrames, rangeSteps int) time.Duration {
	return 4*time.Second + time.Duration(maxFrames)*2750*time.Millisecond + time.Duration(rangeSteps)*200*time.Millisecond
}

func scrollRangeWheelPoint(target WindowTarget, r ScrollCaptureRange) image.Point {
	localBounds := image.Rect(0, 0, target.Rect.Dx(), target.Rect.Dy())
	if (r.InitialScrollPercent != nil || r.InitialWheelUnits > 0) && r.InitialScrollPoint.In(localBounds) {
		return target.Rect.Min.Add(r.InitialScrollPoint)
	}
	view := r.View.Canon()
	if view.Empty() {
		view = r.Crop.Canon()
	}
	if view.Empty() {
		return target.Rect.Min.Add(target.Rect.Size().Div(2))
	}
	point := image.Pt(view.Min.X+view.Dx()/2, view.Min.Y+view.Dy()/2)
	point.X = clampInt(point.X, localBounds.Min.X, localBounds.Max.X-1)
	point.Y = clampInt(point.Y, localBounds.Min.Y, localBounds.Max.Y-1)
	return target.Rect.Min.Add(point)
}

func clampInt(value, minimum, maximum int) int {
	if value < minimum {
		return minimum
	}
	if value > maximum {
		return maximum
	}
	return value
}

func cappedScrollRangeSteps(v int) int {
	if v < 0 {
		return 0
	}
	if v > 64 {
		return 64
	}
	return v
}

func nonNegative(v int) int {
	if v < 0 {
		return 0
	}
	return v
}

func scrollRangeMaxFrames(crop image.Rectangle, viewportHeight int) int {
	if viewportHeight <= 0 {
		return 8
	}
	needed := crop.Max.Y
	frames := needed/viewportHeight + 3
	if needed%viewportHeight != 0 {
		frames++
	}
	if frames < 2 {
		return 2
	}
	if frames > 96 {
		return 96
	}
	return frames
}

func captureTargetWithHelper(target WindowTarget, command string, timeout time.Duration, extraArgs ...string) (image.Image, error) {
	helper, err := findWindowCaptureHelper()
	if err != nil {
		return nil, err
	}

	tmp, err := os.CreateTemp("", "instantcapture-window-*.png")
	if err != nil {
		return nil, fmt.Errorf("create helper output file: %w", err)
	}
	outPath := tmp.Name()
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("close helper output file: %w", err)
	}
	defer os.Remove(outPath)

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	args := []string{
		command,
		"--hwnd", fmt.Sprintf("0x%X", target.Handle),
		"--out", outPath,
		"--json",
	}
	args = append(args, extraArgs...)
	cmd := exec.CommandContext(ctx, helper, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdout, err := cmd.Output()
	if ctx.Err() == context.DeadlineExceeded {
		return nil, fmt.Errorf("window helper timed out during %s (%s)", command, helper)
	}
	if err != nil {
		if message := helperFailureMessage(stdout); message != "" {
			return nil, fmt.Errorf("window helper capture failed (%s): %s%s", helper, message, helperStderr(stderr.String()))
		}
		return nil, fmt.Errorf("window helper failed (%s): %w%s", helper, err, helperStderr(stderr.String()))
	}

	var result windowHelperResult
	if err := json.Unmarshal(stdout, &result); err != nil {
		return nil, fmt.Errorf("parse window helper JSON: %w", err)
	}
	if !result.OK {
		message := result.Message
		if message == "" {
			message = result.ErrorCode
		}
		return nil, fmt.Errorf("window helper capture failed (%s): %s", helper, message)
	}

	file, err := os.Open(outPath)
	if err != nil {
		return nil, fmt.Errorf("open helper PNG: %w", err)
	}
	defer file.Close()
	img, err := png.Decode(file)
	if err != nil {
		return nil, fmt.Errorf("decode helper PNG: %w", err)
	}
	if img.Bounds().Empty() {
		return nil, fmt.Errorf("helper PNG is empty")
	}
	return img, nil
}

type windowHelperResult struct {
	OK            bool    `json:"ok"`
	Method        string  `json:"method"`
	OutputPath    string  `json:"outputPath"`
	Width         int     `json:"width"`
	Height        int     `json:"height"`
	ErrorCode     string  `json:"errorCode"`
	Message       string  `json:"message"`
	ScrollPercent float64 `json:"scrollPercent"`
}

func findWindowCaptureHelper() (string, error) {
	windowHelperCache.once.Do(func() {
		windowHelperCache.path, windowHelperCache.err = resolveWindowCaptureHelper()
	})
	return windowHelperCache.path, windowHelperCache.err
}

func resolveWindowCaptureHelper() (string, error) {
	const helperName = "instantcapture-window-helper.exe"
	if helper := os.Getenv("INSTANTCAPTURE_WINDOW_HELPER"); helper != "" {
		info, err := os.Stat(helper)
		if err == nil && !info.IsDir() {
			return helper, nil
		}
		return "", fmt.Errorf("window capture helper from INSTANTCAPTURE_WINDOW_HELPER not found: %s", helper)
	}
	var candidates []string
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		candidates = append(candidates,
			filepath.Join(dir, helperName),
			filepath.Join(dir, "tools", "window-capture-helper", helperName),
		)
	}
	if wd, err := os.Getwd(); err == nil {
		devRoot := findDevelopmentRoot(wd)
		if devRoot != "" {
			candidates = append(candidates,
				filepath.Join(devRoot, helperName),
				filepath.Join(devRoot, "tools", "window-capture-helper", helperName),
				filepath.Join(devRoot, "build", "window-capture-helper", helperName),
				filepath.Join(devRoot, "build", "window-capture-helper", "Release", helperName),
				filepath.Join(devRoot, "build", "window-capture-helper", "Debug", helperName),
			)
		}
	}
	for _, candidate := range candidates {
		if candidate == "" {
			continue
		}
		info, err := os.Stat(candidate)
		if err == nil && !info.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("window capture helper not found")
}

func findDevelopmentRoot(start string) string {
	for dir := filepath.Clean(start); ; dir = filepath.Dir(dir) {
		if isDevelopmentRoot(dir) {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
	}
}

func isDevelopmentRoot(dir string) bool {
	if info, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil || info.IsDir() {
		return false
	}
	if info, err := os.Stat(filepath.Join(dir, "tools", "window-capture-helper")); err != nil || !info.IsDir() {
		return false
	}
	return true
}

func resetWindowCaptureHelperCacheForTest() {
	windowHelperCache = struct {
		once sync.Once
		path string
		err  error
	}{}
}

func helperStderr(stderr string) string {
	stderr = strings.TrimSpace(stderr)
	if stderr == "" {
		return ""
	}
	return ": " + stderr
}

func helperFailureMessage(stdout []byte) string {
	var result windowHelperResult
	if err := json.Unmarshal(stdout, &result); err != nil || result.OK {
		return ""
	}
	if result.Message != "" {
		return result.Message
	}
	return result.ErrorCode
}

func usableWindowTitle(title, excludeTitle string) bool {
	trimmed := strings.TrimSpace(title)
	return trimmed != "" && trimmed != excludeTitle && trimmed != "InstantCapture Native Overlay"
}

func usableWindowRect(rect image.Rectangle) bool {
	return rect.Dx() >= 40 && rect.Dy() >= 40
}

func CursorPosition() image.Point {
	var p point
	windowGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	return image.Pt(int(p.X), int(p.Y))
}

type point struct{ X, Y int32 }

func windowTitle(hwnd uintptr) string {
	length, _, _ := windowGetWindowTextLengthW.Call(hwnd)
	if length == 0 {
		return ""
	}
	buf := make([]uint16, int(length)+1)
	windowGetWindowTextW.Call(hwnd, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	return syscall.UTF16ToString(buf)
}

func WindowVisibleByTitle(title string) bool {
	hwnd, err := findWindowByTitle(title)
	if err != nil {
		return false
	}
	visible, _, _ := windowIsWindowVisible.Call(hwnd)
	return visible != 0
}

func findWindowByTitle(title string) (uintptr, error) {
	windowTitle, err := xwindows.UTF16PtrFromString(title)
	if err != nil {
		return 0, err
	}
	hwnd, _, callErr := windowFindWindowW.Call(0, uintptr(unsafe.Pointer(windowTitle)))
	if hwnd == 0 {
		return 0, fmt.Errorf("find window %q: %v", title, callErr)
	}
	return hwnd, nil
}
