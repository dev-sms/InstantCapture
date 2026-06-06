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
