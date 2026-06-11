//go:build windows

package nativeoverlay

import (
	"context"
	"image"
	"os"
	"testing"
	"time"

	"instantcapture/internal/capture"
	"instantcapture/internal/overlay"
	platformwindows "instantcapture/internal/platform/windows"
)

func TestWindowAtUsesVisibleFragmentsInZOrder(t *testing.T) {
	s := &nativeSession{
		windows: []nativeWindow{
			{rect: image.Rect(0, 0, 100, 100), visibleRects: []image.Rectangle{image.Rect(80, 0, 100, 100)}},
			{rect: image.Rect(50, 0, 150, 100), visibleRects: []image.Rectangle{image.Rect(50, 0, 150, 100)}},
		},
	}
	if got := s.windowAt(image.Pt(90, 50)); got != 0 {
		t.Fatalf("expected top visible fragment, got %d", got)
	}
	if got := s.windowAt(image.Pt(60, 50)); got != 1 {
		t.Fatalf("expected second window visible fragment, got %d", got)
	}
	if got := s.windowAt(image.Pt(25, 50)); got != -1 {
		t.Fatalf("expected no hit on hidden area, got %d", got)
	}
}

func TestScrollSelectionContentAnchorSurvivesReverseScroll(t *testing.T) {
	s := &nativeSession{
		start:   image.Pt(20, 100),
		current: image.Pt(80, 100),
	}
	s.beginScrollSelection()

	s.scrollOffsetY = 720
	s.current = image.Pt(80, 300)
	s.updateScrollSelectionCurrent()

	s.scrollOffsetY = 360
	s.current = image.Pt(80, 220)
	s.updateScrollSelectionCurrent()

	got := s.scrollSelectionContentRect(image.Rect(0, 0, 200, 1200))
	want := image.Rect(20, 100, 80, 1020)
	if got != want {
		t.Fatalf("content rect = %v, want %v", got, want)
	}
	if s.scrollMaxWheelSteps != 0 {
		t.Fatalf("manual offset setup should not infer wheel steps, got %d", s.scrollMaxWheelSteps)
	}
}

func TestScrollSelectionPreservesMaxWheelStepForReplay(t *testing.T) {
	s := &nativeSession{
		start:   image.Pt(20, 100),
		current: image.Pt(80, 100),
	}
	s.beginScrollSelection()
	s.scrollWheelSteps = 2
	s.scrollOffsetY = 720
	s.current = image.Pt(80, 300)
	s.updateScrollSelectionCurrent()

	s.scrollWheelSteps = 1
	s.scrollOffsetY = 360
	s.current = image.Pt(80, 220)
	s.updateScrollSelectionCurrent()

	if s.scrollMaxWheelSteps != 2 {
		t.Fatalf("max wheel steps = %d, want 2", s.scrollMaxWheelSteps)
	}
	if s.scrollRangeEndViewY != 300 {
		t.Fatalf("range end view y = %d, want 300", s.scrollRangeEndViewY)
	}
}

func TestScrollSelectionKeepsEarlierReplayStepOnEqualContentY(t *testing.T) {
	s := &nativeSession{
		start:   image.Pt(20, 100),
		current: image.Pt(80, 100),
	}
	s.beginScrollSelection()
	s.scrollWheelSteps = 2
	s.scrollOffsetY = 720
	s.current = image.Pt(80, 300)
	s.updateScrollSelectionCurrent()

	s.scrollWheelSteps = 1
	s.scrollOffsetY = 360
	s.current = image.Pt(80, 660)
	s.updateScrollSelectionCurrent()

	if s.scrollMaxWheelSteps != 2 {
		t.Fatalf("max wheel steps = %d, want 2", s.scrollMaxWheelSteps)
	}
	if s.scrollRangeEndViewY != 300 {
		t.Fatalf("range end view y = %d, want 300", s.scrollRangeEndViewY)
	}
}

func TestScrollSelectionPreviewProjectsVirtualContentPosition(t *testing.T) {
	s := &nativeSession{
		start:   image.Pt(20, 100),
		current: image.Pt(80, 100),
	}
	s.beginScrollSelection()
	s.scrollOffsetY = 360
	s.current = image.Pt(80, 300)
	s.updateScrollSelectionCurrent()

	got := s.scrollSelectionPreviewRect(image.Rect(0, 0, 200, 400))
	want := image.Rect(20, -260, 80, 300)
	if got != want {
		t.Fatalf("preview rect = %v, want %v", got, want)
	}
}

func TestScrollSelectionPreviewAfterReverseScrollUsesCurrentOffset(t *testing.T) {
	s := &nativeSession{
		start:   image.Pt(20, 100),
		current: image.Pt(80, 100),
	}
	s.beginScrollSelection()
	s.scrollOffsetY = 720
	s.current = image.Pt(80, 300)
	s.updateScrollSelectionCurrent()
	s.scrollOffsetY = 360
	s.current = image.Pt(80, 220)
	s.updateScrollSelectionCurrent()

	got := s.scrollSelectionPreviewRect(image.Rect(0, 0, 200, 400))
	want := image.Rect(20, -260, 80, 220)
	if got != want {
		t.Fatalf("preview rect after reverse = %v, want %v", got, want)
	}
}

func TestMeasureScrollDeltaDetectsDownwardMovement(t *testing.T) {
	before := buildScrollSample(32, 80, 0)
	after := buildScrollSample(32, 80, 17)
	delta, ok := measureScrollDelta(before, after, 40)
	if !ok {
		t.Fatal("expected measured delta")
	}
	if delta != 17 {
		t.Fatalf("delta = %d, want 17", delta)
	}
}

func TestMeasureScrollDeltaDetectsUpwardMovement(t *testing.T) {
	before := buildScrollSample(32, 80, 17)
	after := buildScrollSample(32, 80, 0)
	delta, ok := measureScrollDelta(before, after, 40)
	if !ok {
		t.Fatal("expected measured delta")
	}
	if delta != -17 {
		t.Fatalf("delta = %d, want -17", delta)
	}
}

func TestMeasureScrollDeltaIgnoresStickyHeader(t *testing.T) {
	before, after := buildStickyScrollSamples(40, 120, 40, 17)
	delta, ok := measureScrollDelta(before, after, 50)
	if !ok {
		t.Fatal("expected measured delta with sticky header")
	}
	if delta != 17 {
		t.Fatalf("delta = %d, want 17", delta)
	}
}

func TestMeasureScrollDeltaIgnoresStickyFooter(t *testing.T) {
	before, after := buildStickyFooterScrollSamples(40, 120, 28, 19)
	delta, ok := measureScrollDelta(before, after, 50)
	if !ok {
		t.Fatal("expected measured delta with sticky footer")
	}
	if delta != 19 {
		t.Fatalf("delta = %d, want 19", delta)
	}
}

func TestMeasureScrollDeltaRejectsStaticSamples(t *testing.T) {
	sample := buildScrollSample(32, 80, 0)
	if delta, ok := measureScrollDelta(sample, sample, 40); ok {
		t.Fatalf("static samples measured delta = %d", delta)
	}
}

func buildScrollSample(width, height, offset int) scrollSample {
	luma := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			absoluteY := y + offset
			v := (absoluteY*absoluteY*17 + absoluteY*31 + x*x*11 + x*7 + (absoluteY^x)*19) % 251
			luma[y*width+x] = byte(v)
		}
	}
	return scrollSample{width: width, height: height, luma: luma}
}

func buildStickyScrollSamples(width, height, stickyHeight, offset int) (scrollSample, scrollSample) {
	before := buildScrollSample(width, height, 0)
	after := buildScrollSample(width, height, offset)
	for y := 0; y < stickyHeight && y < height; y++ {
		copy(after.luma[y*width:(y+1)*width], before.luma[y*width:(y+1)*width])
	}
	return before, after
}

func buildStickyFooterScrollSamples(width, height, footerHeight, offset int) (scrollSample, scrollSample) {
	before := buildScrollSample(width, height, 0)
	after := buildScrollSample(width, height, offset)
	for y := max(0, height-footerHeight); y < height; y++ {
		copy(after.luma[y*width:(y+1)*width], before.luma[y*width:(y+1)*width])
	}
	return before, after
}

func TestScrollModeUsesWindowHighlightBuffer(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 2, 1),
		mode:      "Scroll",
		hover:     0,
		windows:   []nativeWindow{{rect: image.Rect(0, 0, 2, 1), visibleRects: []image.Rectangle{image.Rect(0, 0, 1, 1)}}},
		bgra:      []byte{10, 10, 10, 255, 10, 10, 10, 255},
		original:  []byte{200, 200, 200, 255, 200, 200, 200, 255},
		highlight: make([]byte, 8),
	}
	got := s.paintBuffer()
	if got[0] != 200 || got[4] != 10 {
		t.Fatalf("Scroll highlight buffer not applied correctly: %#v", got)
	}
}

func TestPaintBufferHighlightsVisibleFragments(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 4, 2),
		mode:      "Window",
		hover:     0,
		windows: []nativeWindow{{
			rect:         image.Rect(0, 0, 4, 2),
			visibleRects: []image.Rectangle{image.Rect(1, 0, 3, 1)},
		}},
	}
	s.bgra = make([]byte, 4*2*4)
	s.original = make([]byte, len(s.bgra))
	s.highlight = make([]byte, len(s.bgra))
	for i := range s.bgra {
		s.bgra[i] = 10
		s.original[i] = 200
	}

	got := s.paintBuffer()
	if len(got) != len(s.bgra) {
		t.Fatalf("unexpected buffer length: %d", len(got))
	}
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			off := (y*4 + x) * 4
			want := byte(10)
			if y == 0 && x >= 1 && x < 3 {
				want = 200
			}
			if got[off] != want {
				t.Fatalf("pixel %d,%d = %d, want %d", x, y, got[off], want)
			}
		}
	}
}

func TestClientPointToImageUsesActualClientSize(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	got := clientPointToImage(image.Pt(480, 270), 960, 540, bounds)
	want := image.Pt(960, 540)
	if got != want {
		t.Fatalf("client point = %v, want %v", got, want)
	}

	got = clientPointToImage(image.Pt(2000, -20), 960, 540, bounds)
	want = image.Pt(1920, 0)
	if got != want {
		t.Fatalf("clamped client point = %v, want %v", got, want)
	}
}

func TestMagnifierSourceRectCentersOnCursorAndClamps(t *testing.T) {
	bounds := image.Rect(0, 0, 200, 120)
	if got := magnifierSourceRect(image.Pt(100, 60), 40, bounds); got != image.Rect(80, 40, 120, 80) {
		t.Fatalf("centered source rect = %v", got)
	}
	if got := magnifierSourceRect(image.Pt(5, 5), 40, bounds); got != image.Rect(0, 0, 40, 40) {
		t.Fatalf("top-left source rect = %v", got)
	}
	if got := magnifierSourceRect(image.Pt(199, 119), 40, bounds); got != image.Rect(160, 80, 200, 120) {
		t.Fatalf("bottom-right source rect = %v", got)
	}
}

func TestMagnifierDIBSourceYUsesBottomOriginForPartialStretch(t *testing.T) {
	bounds := image.Rect(0, 0, 200, 120)
	if got := magnifierDIBSourceY(image.Rect(0, 0, 40, 40), bounds); got != 80 {
		t.Fatalf("top source DIB Y = %d, want 80", got)
	}
	if got := magnifierDIBSourceY(image.Rect(80, 40, 120, 80), bounds); got != 40 {
		t.Fatalf("middle source DIB Y = %d, want 40", got)
	}
	if got := magnifierDIBSourceY(image.Rect(160, 80, 200, 120), bounds); got != 0 {
		t.Fatalf("bottom source DIB Y = %d, want 0", got)
	}
}

func TestSelectNativeWindowModeSmoke(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 240, 160))
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	_, _, err := selectNative(ctx, img, overlay.Options{
		VirtualScreen: image.Rect(0, 0, 240, 160),
		CaptureMode:   "Window",
		WindowRegions: []overlay.WindowRegion{{
			Rect:         image.Rect(20, 20, 180, 120),
			VisibleRects: []image.Rectangle{image.Rect(20, 20, 180, 120)},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelectNativeWindowModeRealScreenSmoke(t *testing.T) {
	if os.Getenv("INSTANTCAPTURE_NATIVE_SMOKE") != "1" {
		t.Skip("set INSTANTCAPTURE_NATIVE_SMOKE=1 to open the native overlay")
	}
	displays := capture.ScreenshotCapturer{}.Displays()
	virtualScreen := capture.UnionDisplays(displays)
	if virtualScreen.Empty() {
		t.Fatal("no displays")
	}
	img := image.NewRGBA(image.Rect(0, 0, virtualScreen.Dx(), virtualScreen.Dy()))
	targets := platformwindows.VisibleWindowTargets("InstantCapture")
	regions := make([]overlay.WindowRegion, 0, len(targets))
	for _, target := range targets {
		regions = append(regions, overlay.WindowRegion{Rect: target.Rect, VisibleRects: target.VisibleRects})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	_, _, err := selectNative(ctx, img, overlay.Options{
		VirtualScreen:  virtualScreen,
		CaptureMode:    "Window",
		WindowRegions:  regions,
		TopMostHint:    true,
		DimAlpha:       80,
		LiveBackground: false,
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestSelectNativeWindowModeClickSmoke(t *testing.T) {
	if os.Getenv("INSTANTCAPTURE_NATIVE_SMOKE") != "1" {
		t.Skip("set INSTANTCAPTURE_NATIVE_SMOKE=1 to open the native overlay")
	}
	img := image.NewRGBA(image.Rect(0, 0, 240, 160))
	region := overlay.WindowRegion{
		Rect:         image.Rect(20, 20, 180, 120),
		VisibleRects: []image.Rectangle{image.Rect(20, 20, 180, 120)},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			activeMu.Lock()
			s := activeSession
			var hwnd uintptr
			if s != nil {
				hwnd = s.hwnd
			}
			activeMu.Unlock()
			if hwnd != 0 {
				lp := uintptr(uint32(40) | uint32(40)<<16)
				procPostMessageW.Call(hwnd, uintptr(wmMouseMove), 0, lp)
				procPostMessageW.Call(hwnd, uintptr(wmLButtonDown), 0, lp)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	selection, ok, err := selectNative(ctx, img, overlay.Options{
		VirtualScreen:  image.Rect(0, 0, 240, 160),
		CaptureMode:    "Window",
		WindowRegions:  []overlay.WindowRegion{region},
		TopMostHint:    true,
		DimAlpha:       80,
		LiveBackground: false,
	})
	<-done
	if err != nil {
		t.Fatal(err)
	}
	if !ok || selection.Rect != region.Rect {
		t.Fatalf("selection = %#v ok=%v, want %#v", selection, ok, region.Rect)
	}
}
