//go:build windows

package nativeoverlay

import (
	"context"
	"image"
	"image/color"
	"os"
	"runtime"
	"slices"
	"strconv"
	"testing"
	"time"
	"unsafe"

	"instantcapture/internal/capture"
	"instantcapture/internal/overlay"
	platformwindows "instantcapture/internal/platform/windows"
)

func TestNewNativeSessionConvertsRGBADirectlyFromSourceBounds(t *testing.T) {
	parent := image.NewRGBA(image.Rect(0, 0, 4, 4))
	parent.SetRGBA(1, 1, color.RGBA{R: 10, G: 20, B: 30, A: 40})
	source := parent.SubImage(image.Rect(1, 1, 3, 3))

	session, err := newNativeSession(source, image.Rect(0, 0, 2, 2), 80)
	if err != nil {
		t.Fatal(err)
	}
	if session.imgBounds != image.Rect(0, 0, 2, 2) {
		t.Fatalf("image bounds = %v", session.imgBounds)
	}
	if got, want := session.original[:4], []byte{30, 20, 10, 40}; !slices.Equal(got, want) {
		t.Fatalf("first BGRA pixel = %v, want %v", got, want)
	}
}

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
	s.scrollWheelSteps = 2
	s.current = image.Pt(80, 300)
	s.updateScrollSelectionCurrent()

	s.scrollOffsetY = 360
	s.scrollWheelSteps = 1
	s.current = image.Pt(80, 220)
	s.updateScrollSelectionCurrent()

	got := s.scrollSelectionContentRect(image.Rect(0, 0, 200, 1200))
	want := image.Rect(20, 100, 80, 1020)
	if got != want {
		t.Fatalf("content rect = %v, want %v", got, want)
	}
	rangeSelection := s.scrollRangeSelection(image.Rect(0, 0, 200, 1200))
	if rangeSelection == nil || rangeSelection.ScrollSteps != 2 || rangeSelection.ScrollPixels != 720 {
		t.Fatalf("preserved replay range = %#v, want 2 steps/720 px", rangeSelection)
	}
}

func TestScrollSelectionKeepsViewportAndDocumentCoordinatesSeparate(t *testing.T) {
	s := &nativeSession{
		start:     image.Pt(20, 100),
		current:   image.Pt(80, 100),
		scrollWin: 0,
		windows:   []nativeWindow{{rect: image.Rect(0, 0, 200, 400)}},
	}
	s.beginScrollSelection()
	s.scrollOffsetY = 360
	s.current = image.Pt(80, 900)
	s.updateScrollSelectionCurrent()

	if s.scrollCurrentContent != image.Pt(80, 760) {
		t.Fatalf("document focus = %v, want (80,760)", s.scrollCurrentContent)
	}
	if got := s.scrollSelectionPreviewRect(image.Rect(0, 0, 200, 400)); got != image.Rect(20, -260, 80, 400) {
		t.Fatalf("viewport projection = %v", got)
	}
}

func TestScrollSelectionBottomProjectionStaysAtViewportEdge(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	s := &nativeSession{
		start:     image.Pt(20, 100),
		current:   image.Pt(80, 900),
		scrollWin: 0,
		windows:   []nativeWindow{{rect: viewport}},
	}
	s.beginScrollSelection()

	for _, offset := range []int{120, 360, 740} {
		s.scrollOffsetY = offset
		s.updateScrollSelectionCurrent()
		wantDocumentY := viewport.Max.Y + offset
		if s.scrollCurrentContent.Y != wantDocumentY {
			t.Fatalf("offset %d document y = %d, want %d", offset, s.scrollCurrentContent.Y, wantDocumentY)
		}
		preview := s.scrollSelectionPreviewRect(viewport)
		if preview.Max.Y != viewport.Max.Y {
			t.Fatalf("offset %d projected bottom = %d, want %d", offset, preview.Max.Y, viewport.Max.Y)
		}
	}
}

func TestScrollSelectionTopProjectionStaysAtViewportEdge(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	s := &nativeSession{
		start:     image.Pt(20, 300),
		current:   image.Pt(80, -500),
		scrollWin: 0,
		windows:   []nativeWindow{{rect: viewport}},
	}
	s.beginScrollSelection()
	s.scrollOffsetY = 120
	s.updateScrollSelectionCurrent()

	if s.scrollCurrentContent.Y != 120 {
		t.Fatalf("document focus y = %d, want 120", s.scrollCurrentContent.Y)
	}
	preview := s.scrollSelectionPreviewRect(viewport)
	if preview.Min.Y != viewport.Min.Y {
		t.Fatalf("projected top = %d, want %d", preview.Min.Y, viewport.Min.Y)
	}
}

func TestScrollRangeSelectionExportsAbsoluteDocumentRange(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	s := &nativeSession{
		start:            image.Pt(20, 100),
		current:          image.Pt(80, 900),
		scrollWin:        0,
		windows:          []nativeWindow{{rect: viewport}},
		scrollOffsetY:    360,
		scrollWheelSteps: 2,
	}
	s.scrollStartContent = s.start
	s.updateScrollSelectionCurrent()

	got := s.scrollRangeSelection(viewport)
	if got == nil {
		t.Fatal("expected scroll range")
	}
	if got.CropRect != image.Rect(20, 100, 80, 760) {
		t.Fatalf("document crop = %v", got.CropRect)
	}
	if got.ViewRect != image.Rect(20, 100, 80, 400) {
		t.Fatalf("viewport range = %v", got.ViewRect)
	}
	if got.ScrollSteps != 2 || got.ScrollPixels != 360 {
		t.Fatalf("steps/pixels = %d/%d", got.ScrollSteps, got.ScrollPixels)
	}
}

func TestScrollWheelUnitsRoundPartialReverseUpForReplay(t *testing.T) {
	s := &nativeSession{}
	s.recordScrollWheelInput(-wheelDelta * 2)
	s.recordScrollWheelInput(48)
	if s.scrollWheelUnits != 192 {
		t.Fatalf("wheel units = %d, want 192", s.scrollWheelUnits)
	}
	if s.scrollWheelSteps != 2 {
		t.Fatalf("replay steps = %d, want 2", s.scrollWheelSteps)
	}
}

func TestScrollWheelInputDoesNotMovePhysicalCursor(t *testing.T) {
	input := scrollWheelInput(-wheelDelta)
	if input.Type != inputMouse || input.Mi.DwFlags != mouseEventWheel {
		t.Fatalf("unexpected wheel input: %#v", input)
	}
	if input.Mi.Dx != 0 || input.Mi.Dy != 0 || input.Mi.DwFlags&0x0001 != 0 {
		t.Fatalf("wheel input contains cursor movement: %#v", input.Mi)
	}
	if got := int(int32(input.Mi.MouseData)); got != -wheelDelta {
		t.Fatalf("wheel delta = %d, want %d", got, -wheelDelta)
	}
}

func TestScrollHookCapturesClicksButLeavesPhysicalWheelNative(t *testing.T) {
	s := &nativeSession{mode: "Scroll", selecting: true, virtualMin: image.Pt(-100, 50), imgBounds: image.Rect(0, 0, 800, 600)}
	mouse := lowLevelMouseHook{Pt: nativePoint{X: 120, Y: 210}}
	message, point, swallow := scrollHookMessage(s, wmLButtonDown, mouse)
	if !swallow || message != wmLButtonDown || point != image.Pt(220, 160) {
		t.Fatalf("click dispatch = message %#x point %v swallow %v", message, point, swallow)
	}
	message, point, swallow = scrollHookMessage(s, wmRButtonUp, mouse)
	if !swallow || message != wmRButtonUp || point != image.Pt(220, 160) {
		t.Fatalf("right click dispatch = message %#x point %v swallow %v", message, point, swallow)
	}
	if _, _, swallow := scrollHookMessage(s, wmMouseWheel, mouse); swallow {
		t.Fatal("physical wheel input was swallowed instead of using the native Windows route")
	}
	mouse.DwExtraInfo = uintptr(scrollSyntheticInputTag)
	if _, _, swallow := scrollHookMessage(s, wmLButtonUp, mouse); swallow {
		t.Fatal("synthetic restoration input re-entered the selection hook")
	}
	s.selecting = false
	mouse.DwExtraInfo = 0
	if _, _, swallow := scrollHookMessage(s, wmMouseWheel, mouse); swallow {
		t.Fatal("wheel input outside an active selection was swallowed")
	}
}

func TestScrollRangeSelectionKeepsReplayForAsymmetricReverseMotion(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	s := &nativeSession{
		start:                image.Pt(20, 100),
		current:              image.Pt(80, 300),
		scrollWin:            0,
		windows:              []nativeWindow{{rect: viewport}},
		scrollOffsetY:        240,
		scrollWheelSteps:     0,
		scrollStartContent:   image.Pt(20, 100),
		scrollCurrentContent: image.Pt(80, 540),
	}

	got := s.scrollRangeSelection(viewport)
	if got == nil {
		t.Fatal("expected scroll range")
	}
	if got.ScrollSteps != 1 || got.ScrollPixels != 240 {
		t.Fatalf("steps/pixels = %d/%d, want 1/240", got.ScrollSteps, got.ScrollPixels)
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

func TestScrollSelectionPreviewAfterReverseScrollKeepsMaximumRange(t *testing.T) {
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
	want := image.Rect(20, -260, 80, 660)
	if got != want {
		t.Fatalf("preview rect after reverse = %v, want %v", got, want)
	}
}

func TestScrollSelectionVisibleRectKeepsClosedBorderOnscreen(t *testing.T) {
	s := &nativeSession{
		start:   image.Pt(20, 100),
		current: image.Pt(80, 300),
	}
	s.beginScrollSelection()
	s.scrollOffsetY = 360
	s.updateScrollSelectionCurrent()

	got, clippedTop, clippedBottom := s.scrollSelectionVisibleRect(image.Rect(0, 0, 200, 400))
	want := image.Rect(20, 0, 80, 300)
	if got != want || !clippedTop || clippedBottom {
		t.Fatalf("visible = %v, top = %v, bottom = %v, want %v,true,false", got, clippedTop, clippedBottom, want)
	}
}

func TestScrollSelectionVisibleRectKeepsMaximumRangeAfterReverse(t *testing.T) {
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

	got, clippedTop, clippedBottom := s.scrollSelectionVisibleRect(image.Rect(0, 0, 200, 400))
	want := image.Rect(20, 0, 80, 400)
	if got != want || !clippedTop || !clippedBottom {
		t.Fatalf("visible = %v, top = %v, bottom = %v, want %v,true,true", got, clippedTop, clippedBottom, want)
	}
}

func TestScrollStatusLabelReportsMotionConfidence(t *testing.T) {
	s := &nativeSession{scrollStartContent: image.Pt(0, 100), scrollCurrentContent: image.Pt(0, 517), scrollLastDelta: 73, scrollLastMeasured: true}
	if got := s.scrollStatusLabel(); got != "선택 417 px · 실제 이동 감지" {
		t.Fatalf("measured label = %q", got)
	}
	s.scrollLastMeasured = false
	if got := s.scrollStatusLabel(); got != "선택 417 px · 이동량 추정" {
		t.Fatalf("estimated label = %q", got)
	}
	s.scrollNoMotion = true
	if got := s.scrollStatusLabel(); got != "페이지 이동 없음" {
		t.Fatalf("no-motion label = %q", got)
	}
}

func TestScrollRestorePointUsesSelectedScrollablePane(t *testing.T) {
	windowRect := image.Rect(100, 200, 900, 800)
	s := &nativeSession{scrollWheelPoint: image.Pt(720, 410)}
	if got := s.scrollRestorePoint(windowRect); got != s.scrollWheelPoint {
		t.Fatalf("restore point = %v, want selected point %v", got, s.scrollWheelPoint)
	}
	s.scrollWheelPoint = image.Pt(0, 0)
	if got := s.scrollRestorePoint(windowRect); got != image.Pt(500, 500) {
		t.Fatalf("fallback restore point = %v, want window center", got)
	}
}

func TestScrollRestoreWheelDeltaCorrectsOvershoot(t *testing.T) {
	if got := scrollRestoreWheelDelta(40, 100); got != 48 {
		t.Fatalf("forward correction delta = %d, want 48", got)
	}
	if got := scrollRestoreWheelDelta(-60, 100); got != -72 {
		t.Fatalf("overshoot correction delta = %d, want -72", got)
	}
	if got := scrollRestoreWheelDelta(200, 100); got != wheelDelta {
		t.Fatalf("full correction delta = %d, want %d", got, wheelDelta)
	}
}

func TestScrollSelectionAllowsFineCorrectionAtZeroWheelSteps(t *testing.T) {
	if got := scrollSelectionReverseWheelDelta(wheelDelta, 40, 100); got != 48 {
		t.Fatalf("fine reverse delta = %d, want 48", got)
	}
	if got := scrollSelectionReverseWheelDelta(wheelDelta, 0, 100); got != 0 {
		t.Fatalf("origin reverse delta = %d, want 0", got)
	}
	if got := scrollExpectedPixelDelta(48); got != -144 {
		t.Fatalf("expected reverse pixels = %d, want -144", got)
	}
	if got := scrollExpectedPixelDelta(-60); got != 180 {
		t.Fatalf("expected downward pixels = %d, want 180", got)
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

func TestScrollSampleExpectedMotionWaitsForDelayedFrame(t *testing.T) {
	origin := buildScrollSample(48, 180, 0)
	static := buildScrollSample(48, 180, 0)
	moved := buildScrollSample(48, 180, 37)
	if scrollSampleHasExpectedMotion(origin, static, scrollStepPixels, 100) {
		t.Fatal("static frame was accepted as motion")
	}
	if !scrollSampleHasExpectedMotion(origin, moved, scrollStepPixels, 100) {
		t.Fatal("delayed moved frame was not accepted")
	}
	if scrollSampleHasExpectedMotion(origin, moved, -scrollStepPixels, 100) {
		t.Fatal("wrong-direction frame was accepted")
	}
}

func TestScrollMeasurementRectFocusesSelectedPane(t *testing.T) {
	windowRect := image.Rect(0, 0, 1000, 700)
	s := &nativeSession{start: image.Pt(220, 100), current: image.Pt(610, 650)}
	if got := s.scrollMeasurementRect(windowRect); got != image.Rect(204, 0, 626, 700) {
		t.Fatalf("selected measurement rect = %v", got)
	}
	s.start.X = 500
	s.current.X = 500
	if got := s.scrollMeasurementRect(windowRect); got != image.Rect(340, 0, 660, 700) {
		t.Fatalf("focused measurement rect = %v", got)
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

func TestMeasureScrollDeltaDetectsVariableWheelDistance(t *testing.T) {
	before := buildScrollSample(48, 180, 0)
	after := buildScrollSample(48, 180, 73)
	delta, ok := measureScrollDelta(before, after, 120)
	if !ok || delta != 73 {
		t.Fatalf("delta = %d, ok = %v, want 73", delta, ok)
	}
}

func TestMeasureScrollDeltaToleratesSmallDynamicRegion(t *testing.T) {
	before := buildScrollSample(48, 160, 0)
	after := buildScrollSample(48, 160, 31)
	for y := 70; y < 78; y++ {
		for x := 8; x < 20; x++ {
			after.luma[y*after.width+x] = byte((x*37 + y*13) % 255)
		}
	}
	delta, ok := measureScrollDelta(before, after, 80)
	if !ok || delta != 31 {
		t.Fatalf("delta = %d, ok = %v, want 31", delta, ok)
	}
}

func TestMeasureScrollDeltaRejectsRepeatingAmbiguousContent(t *testing.T) {
	before := buildRepeatingScrollSample(40, 160, 0, 12)
	after := buildRepeatingScrollSample(40, 160, 7, 12)
	if delta, ok := measureScrollDelta(before, after, 80); ok {
		t.Fatalf("ambiguous repeating content measured delta = %d", delta)
	}
}

func TestMeasureScrollDeltaRejectsUnrelatedPageChange(t *testing.T) {
	before := buildScrollSample(40, 140, 0)
	after := buildUnrelatedScrollSample(40, 140)
	if delta, ok := measureScrollDelta(before, after, 70); ok {
		t.Fatalf("unrelated page change measured delta = %d", delta)
	}
}

func TestMeasureScrollDeltaIgnoresStickyHeaderAndFooterTogether(t *testing.T) {
	before := buildScrollSample(48, 180, 0)
	after := buildScrollSample(48, 180, 29)
	for y := 0; y < 44; y++ {
		copy(after.luma[y*after.width:(y+1)*after.width], before.luma[y*before.width:(y+1)*before.width])
	}
	for y := 150; y < 180; y++ {
		copy(after.luma[y*after.width:(y+1)*after.width], before.luma[y*before.width:(y+1)*before.width])
	}
	delta, ok := measureScrollDelta(before, after, 80)
	if !ok || delta != 29 {
		t.Fatalf("delta = %d, ok = %v, want 29", delta, ok)
	}
}

func TestApplyObservedScrollDeltaAddsFallbackWithoutResettingHistory(t *testing.T) {
	s := &nativeSession{scrollOffsetY: 240}
	before := buildRepeatingScrollSample(40, 160, 0, 12)
	after := buildRepeatingScrollSample(40, 160, 7, 12)

	result := s.applyObservedScrollDelta(before, after, scrollStepPixels)

	if result != scrollMotionEstimated {
		t.Fatalf("motion result = %v, want estimated", result)
	}
	if s.scrollOffsetY != 600 {
		t.Fatalf("scroll offset = %d, want 600", s.scrollOffsetY)
	}
}

func TestApplyObservedScrollDeltaUsesMeasuredHistoryForAmbiguousStep(t *testing.T) {
	s := &nativeSession{}
	firstBefore := buildScrollSample(40, 160, 0)
	firstAfter := buildScrollSample(40, 160, 23)
	if result := s.applyObservedScrollDelta(firstBefore, firstAfter, scrollStepPixels); result != scrollMotionMeasured {
		t.Fatalf("first motion result = %v, want measured", result)
	}
	ambiguousBefore := buildRepeatingScrollSample(40, 160, 0, 12)
	ambiguousAfter := buildRepeatingScrollSample(40, 160, 7, 12)

	result := s.applyObservedScrollDelta(ambiguousBefore, ambiguousAfter, scrollStepPixels)

	if result != scrollMotionEstimated {
		t.Fatalf("second motion result = %v, want estimated", result)
	}
	if s.scrollOffsetY != 46 {
		t.Fatalf("scroll offset = %d, want 46", s.scrollOffsetY)
	}
}

func TestApplyObservedScrollDeltaDoesNotAdvanceStaticPage(t *testing.T) {
	s := &nativeSession{scrollOffsetY: 120}
	sample := buildScrollSample(40, 160, 0)

	result := s.applyObservedScrollDelta(sample, sample, scrollStepPixels)

	if result != scrollMotionNone || !s.scrollNoMotion {
		t.Fatalf("motion result = %v, noMotion = %v", result, s.scrollNoMotion)
	}
	if s.scrollOffsetY != 120 {
		t.Fatalf("scroll offset = %d, want 120", s.scrollOffsetY)
	}
}

func TestApplyObservedScrollDeltaRejectsWrongDirection(t *testing.T) {
	s := &nativeSession{scrollOffsetY: 120}
	before := buildScrollSample(40, 160, 30)
	after := buildScrollSample(40, 160, 0)

	result := s.applyObservedScrollDelta(before, after, scrollStepPixels)

	if result != scrollMotionNone {
		t.Fatalf("motion result = %v, want none", result)
	}
	if s.scrollOffsetY != 120 {
		t.Fatalf("scroll offset = %d, want 120", s.scrollOffsetY)
	}
}

func TestScrollOriginKeyframeRestoresAnchorAfterRoundTrip(t *testing.T) {
	origin := buildScrollSample(48, 180, 0)
	away := buildScrollSample(48, 180, 73)
	s := &nativeSession{
		start:                image.Pt(20, 100),
		current:              image.Pt(80, 300),
		scrollStartContent:   image.Pt(20, 100),
		scrollCurrentContent: image.Pt(80, 300),
	}

	if result := s.applyObservedScrollDelta(origin, away, scrollStepPixels); result != scrollMotionMeasured {
		t.Fatalf("downward motion = %v", result)
	}
	if s.scrollOffsetY != 73 {
		t.Fatalf("away offset = %d, want 73", s.scrollOffsetY)
	}

	s.scrollOffsetY = 81
	if result := s.applyObservedScrollDelta(away, origin, -scrollStepPixels); result != scrollMotionMeasured {
		t.Fatalf("return motion = %v", result)
	}
	if s.scrollOffsetY != 0 {
		t.Fatalf("round-trip offset = %d, want 0", s.scrollOffsetY)
	}
	preview := s.scrollSelectionPreviewRect(image.Rect(0, 0, 200, 400))
	if preview.Min.Y != s.scrollStartContent.Y {
		t.Fatalf("restored anchor y = %d, want %d", preview.Min.Y, s.scrollStartContent.Y)
	}
}

func TestScrollSelectionInputRoundTripPreservesMaximumRangeAndOrigin(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	origin := buildScrollSample(48, 180, 0)
	first := buildScrollSample(48, 180, 73)
	second := buildScrollSample(48, 180, 146)
	s := &nativeSession{
		start:     image.Pt(20, 100),
		current:   image.Pt(80, 300),
		scrollWin: 0,
		windows:   []nativeWindow{{rect: viewport}},
	}
	s.beginScrollSelection()

	steps := []struct {
		before scrollSample
		after  scrollSample
		wheel  int
	}{
		{origin, first, -wheelDelta},
		{first, second, -wheelDelta},
		{second, first, wheelDelta},
		{first, origin, wheelDelta},
	}
	for index, step := range steps {
		if result := s.applyObservedScrollDelta(step.before, step.after, scrollExpectedPixelDelta(step.wheel)); result != scrollMotionMeasured {
			t.Fatalf("step %d motion result = %v", index, result)
		}
		s.recordScrollWheelInput(step.wheel)
		if index >= 2 {
			s.current = image.Pt(80, 220)
		}
		s.updateScrollSelectionCurrent()
	}

	if s.scrollOffsetY != 0 || s.scrollWheelSteps != 0 {
		t.Fatalf("round-trip position = %d px/%d steps, want origin", s.scrollOffsetY, s.scrollWheelSteps)
	}
	selection := s.scrollRangeSelection(viewport)
	if selection == nil {
		t.Fatal("maximum range was lost after returning to origin")
	}
	if selection.CropRect.Min.Y != 100 || selection.CropRect.Max.Y != 446 {
		t.Fatalf("preserved range = %v, want y=100..446", selection.CropRect)
	}
	if selection.ScrollSteps != 2 || selection.ScrollPixels != 146 {
		t.Fatalf("preserved replay = %d steps/%d px, want 2/146", selection.ScrollSteps, selection.ScrollPixels)
	}
}

func TestRestoreAutoScrollReplaysSimulatedInputToExactOrigin(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	position := 146
	var sent []int
	s := &nativeSession{
		hwnd:               1,
		imgBounds:          viewport,
		scrollWin:          0,
		windows:            []nativeWindow{{handle: 2, rect: viewport}},
		scrollOffsetY:      position,
		scrollWheelUnits:   wheelDelta * 2,
		scrollWheelSteps:   2,
		scrollFallbackStep: 73,
		scrollOriginSample: buildScrollSample(48, 180, 0),
		scrollSampleRect:   viewport,
	}
	s.scrollSampleCapture = func(image.Rectangle) scrollSample {
		return buildScrollSample(48, 180, position)
	}
	s.scrollWheelSender = func(_ uintptr, delta int, _ image.Point) bool {
		sent = append(sent, delta)
		if delta > 0 {
			position = max(0, position-73)
		} else if delta < 0 {
			position += 73
		}
		return true
	}

	if !s.restoreAutoScroll() {
		t.Fatal("simulated restoration did not reach the origin")
	}
	if position != 0 || s.scrollOffsetY != 0 {
		t.Fatalf("restored position = model %d/session %d, want 0/0", position, s.scrollOffsetY)
	}
	if want := []int{wheelDelta, wheelDelta}; !slices.Equal(sent, want) {
		t.Fatalf("restore inputs = %v, want %v", sent, want)
	}
}

func TestPassiveWheelSelectionDetectsMovementExportsRangeAndRestoresOrigin(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	position := 0
	var restored []int
	s := &nativeSession{
		imgBounds: viewport,
		start:     image.Pt(20, 100),
		current:   image.Pt(80, 300),
		scrollWin: 0,
		windows:   []nativeWindow{{handle: 2, rect: viewport}},
	}
	s.scrollSampleCapture = func(image.Rectangle) scrollSample {
		return buildScrollSample(48, 180, position)
	}
	s.scrollWheelSender = func(_ uintptr, delta int, _ image.Point) bool {
		restored = append(restored, delta)
		if delta > 0 {
			position = max(0, position-73)
		}
		return true
	}
	s.beginScrollSelection()
	s.scrollSampleRect = viewport
	s.observePassiveScroll()
	position = 73
	s.observePassiveScroll()
	position = 146
	s.observePassiveScroll()

	selection := s.scrollRangeSelection(viewport)
	if selection == nil || selection.CropRect != image.Rect(20, 100, 80, 446) {
		t.Fatalf("detected selection = %#v", selection)
	}
	if selection.ScrollSteps != 2 || selection.ScrollPixels != 146 {
		t.Fatalf("detected replay = %d steps/%d px", selection.ScrollSteps, selection.ScrollPixels)
	}
	if !s.restoreAutoScroll() {
		t.Fatal("passively detected movement did not restore")
	}
	if position != 0 || s.scrollOffsetY != 0 || !slices.Equal(restored, []int{wheelDelta, wheelDelta}) {
		t.Fatalf("restoration = position %d offset %d inputs %v", position, s.scrollOffsetY, restored)
	}
}

func TestObservedPhysicalWheelSequenceTracksAndRestoresOrigin(t *testing.T) {
	viewport := image.Rect(0, 0, 200, 400)
	position := 0
	var inputs []int
	captures := 0
	s := &nativeSession{
		imgBounds: viewport,
		start:     image.Pt(20, 100),
		current:   image.Pt(80, 300),
		cursor:    image.Pt(80, 300),
		hover:     0,
		scrollWin: 0,
		selecting: true,
		windows:   []nativeWindow{{handle: 2, rect: viewport}},
	}
	s.scrollSampleCapture = func(image.Rectangle) scrollSample {
		captures++
		return buildScrollSample(48, 180, position)
	}
	s.scrollWheelSender = func(_ uintptr, delta int, _ image.Point) bool {
		inputs = append(inputs, delta)
		if delta < 0 {
			position += 73
		} else if delta > 0 {
			position = max(0, position-73)
		}
		return true
	}
	s.beginScrollSelection()
	position = 73
	s.handleObservedPhysicalWheel(-wheelDelta)
	position = 146
	s.handleObservedPhysicalWheel(-wheelDelta)

	if position != 146 || s.scrollOffsetY != 720 || s.scrollWheelSteps != 2 {
		t.Fatalf("continuous wheel result = model %d offset %d steps %d", position, s.scrollOffsetY, s.scrollWheelSteps)
	}
	selection := s.scrollRangeSelection(viewport)
	if selection == nil || selection.CropRect != image.Rect(20, 100, 80, 1020) {
		t.Fatalf("handled wheel selection = %#v", selection)
	}
	if !s.restoreAutoScroll() {
		t.Fatal("handled wheel sequence did not restore")
	}
	if position != 0 || s.scrollOffsetY != 0 {
		t.Fatalf("handled wheel restoration = model %d offset %d", position, s.scrollOffsetY)
	}
	if want := []int{wheelDelta, wheelDelta}; !slices.Equal(inputs, want) {
		t.Fatalf("wheel routing inputs = %v, want %v", inputs, want)
	}
	if captures != 0 {
		t.Fatalf("selection performed %d screen captures; want none", captures)
	}
}

func TestMixedDPIScrollSelectionUsesPhysicalCoordinatesWithoutLiveCapture(t *testing.T) {
	virtualScreen := image.Rect(-1600, -200, 3840, 1600)
	imageBounds := image.Rect(0, 0, virtualScreen.Dx(), virtualScreen.Dy())
	targetScreen := image.Rect(1280, 0, 3840, 1440)
	targetImage := targetScreen.Sub(virtualScreen.Min)
	startScreen := image.Pt(2560, 160)
	endScreen := image.Pt(2880, 1320)
	startImage := screenToImagePoint(startScreen, virtualScreen.Min, imageBounds)
	endImage := screenToImagePoint(endScreen, virtualScreen.Min, imageBounds)

	captures := 0
	uiaRestored := false
	s := &nativeSession{
		imgBounds:  imageBounds,
		virtualMin: virtualScreen.Min,
		start:      startImage,
		current:    endImage,
		cursor:     endImage,
		scrollWin:  0,
		selecting:  true,
		windows:    []nativeWindow{{handle: 2, rect: targetImage}},
	}
	s.scrollSampleCapture = func(image.Rectangle) scrollSample {
		captures++
		return scrollSample{}
	}
	s.scrollWheelSender = func(_ uintptr, _ int, _ image.Point) bool {
		t.Fatal("wheel fallback ran despite an available UI Automation position")
		return false
	}
	s.scrollPositionRestore = func(target platformwindows.WindowTarget, position platformwindows.ScrollPosition) error {
		if target.Handle != 2 || target.Rect != targetScreen {
			t.Fatalf("restore target = %#v", target)
		}
		if position.Percent != 37.5 || position.WheelPoint != endScreen {
			t.Fatalf("restore position = %#v", position)
		}
		uiaRestored = true
		return nil
	}
	s.beginScrollSelection()
	position := platformwindows.ScrollPosition{Percent: 37.5, WheelPoint: endScreen}
	s.scrollInitialPosition = &position
	s.current = endImage
	s.cursor = endImage
	for range 5 {
		s.handleObservedPhysicalWheel(-wheelDelta)
	}

	if s.start != startImage || s.current != endImage || s.scrollWheelPoint != endImage {
		t.Fatalf("selection coordinates = start %v current %v wheel %v", s.start, s.current, s.scrollWheelPoint)
	}
	if s.scrollOffsetY != scrollStepPixels*5 || s.scrollWheelSteps != 5 {
		t.Fatalf("selection range = %d px/%d steps", s.scrollOffsetY, s.scrollWheelSteps)
	}
	selection := s.scrollRangeSelection(targetImage)
	if selection == nil || selection.WindowRect != targetImage {
		t.Fatalf("mixed-DPI selection = %#v", selection)
	}
	if selection.InitialWheelUnits != wheelDelta*5 {
		t.Fatalf("deferred wheel units = %d", selection.InitialWheelUnits)
	}
	if !s.restoreAutoScroll() {
		t.Fatal("recorded wheel replay failed")
	}
	if !uiaRestored {
		t.Fatal("UI Automation position was not restored")
	}
	if captures != 0 {
		t.Fatalf("selection performed %d live screen captures", captures)
	}
}

func TestUIAScrollOriginDefersRestoreToCaptureHelper(t *testing.T) {
	percent := 42.5
	if !shouldDeferScrollRestore(&ScrollRangeSelection{InitialScrollPercent: &percent}) {
		t.Fatal("UI Automation origin should defer restoration to capture helper")
	}
	if shouldDeferScrollRestore(&ScrollRangeSelection{}) {
		t.Fatal("selection without a UI Automation origin cannot defer restoration")
	}
	if !shouldDeferScrollRestore(&ScrollRangeSelection{InitialWheelUnits: wheelDelta * 2}) {
		t.Fatal("wheel fallback origin should defer restoration to capture helper")
	}
}

func TestScrollPositionQueryUsesSelectedColumnAndViewportCenter(t *testing.T) {
	windowRect := image.Rect(2880, 0, 5440, 1392)
	if got := scrollPositionQueryPoint(image.Pt(4160, 120), windowRect); got != image.Pt(4160, 696) {
		t.Fatalf("query point = %v", got)
	}
}

func TestScrollOriginKeyframeToleratesSmallDynamicRegion(t *testing.T) {
	origin := buildScrollSample(48, 180, 0)
	current := buildScrollSample(48, 180, 0)
	for y := 70; y < 76; y++ {
		for x := 8; x < 18; x++ {
			current.luma[y*current.width+x] = byte((x*41 + y*17) % 251)
		}
	}
	s := &nativeSession{scrollOriginSample: origin, scrollOffsetY: 19}
	if !s.reconcileScrollOrigin(current) {
		t.Fatal("expected origin reconciliation")
	}
	if s.scrollOffsetY != 0 {
		t.Fatalf("dynamic origin offset = %d, want 0", s.scrollOffsetY)
	}
}

func TestScrollOriginKeyframeSnapsNearbyRepeatingViewport(t *testing.T) {
	origin := buildRepeatingScrollSample(48, 180, 0, 12)
	s := &nativeSession{scrollOriginSample: origin, scrollOffsetY: 19}
	if !s.reconcileScrollOrigin(origin) {
		t.Fatal("nearby repeating origin should snap when viewport pixels match")
	}
	if s.scrollOffsetY != 0 {
		t.Fatalf("nearby repeating origin offset = %d, want 0", s.scrollOffsetY)
	}
}

func TestScrollOriginKeyframeRejectsDistantDuplicateViewport(t *testing.T) {
	origin := buildScrollSample(48, 180, 0)
	s := &nativeSession{scrollOriginSample: origin, scrollOffsetY: 900, scrollFallbackStep: 73}
	if s.reconcileScrollOrigin(origin) {
		t.Fatal("distant duplicate viewport should not snap to origin")
	}
	if s.scrollOffsetY != 900 {
		t.Fatalf("distant duplicate changed offset to %d", s.scrollOffsetY)
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

func buildRepeatingScrollSample(width, height, offset, period int) scrollSample {
	luma := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			absoluteY := (y + offset) % period
			luma[y*width+x] = byte((absoluteY*23 + x*11) % 251)
		}
	}
	return scrollSample{width: width, height: height, luma: luma}
}

func buildUnrelatedScrollSample(width, height int) scrollSample {
	luma := make([]byte, width*height)
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			luma[y*width+x] = byte((y*43 + x*97 + (y*x)%89 + 61) % 251)
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

func TestPaintBufferReusesHighlightUntilHoveredWindowChanges(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 2, 1),
		mode:      "Window",
		hover:     0,
		windows: []nativeWindow{
			{visibleRects: []image.Rectangle{image.Rect(0, 0, 1, 1)}},
			{visibleRects: []image.Rectangle{image.Rect(1, 0, 2, 1)}},
		},
		bgra:      []byte{10, 10, 10, 255, 10, 10, 10, 255},
		original:  []byte{200, 200, 200, 255, 200, 200, 200, 255},
		highlight: make([]byte, 8),
	}

	first := s.paintBuffer()
	first[0] = 77
	if got := s.paintBuffer()[0]; got != 77 {
		t.Fatalf("unchanged hover rebuilt highlight buffer: got %d", got)
	}

	s.hover = 1
	second := s.paintBuffer()
	if second[0] != 10 || second[4] != 200 {
		t.Fatalf("changed hover did not rebuild highlight buffer: %#v", second)
	}
}

func TestDynamicClientRectsAvoidFullScreenInvalidation(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 1920, 1080),
		mode:      "Region",
		hasCursor: true,
		cursor:    image.Pt(960, 540),
		selecting: true,
		start:     image.Pt(300, 200),
		current:   image.Pt(1200, 800),
	}
	clientBounds := image.Rect(0, 0, 1920, 1080)
	rects := s.dynamicClientRects(clientBounds.Dx(), clientBounds.Dy())
	if len(rects) < 7 {
		t.Fatalf("dynamic dirty rect count = %d, want crosshair, magnifier, and selection borders", len(rects))
	}
	for _, rect := range rects {
		if rect == clientBounds {
			t.Fatal("dynamic repaint invalidated the full client area")
		}
		if !rect.In(clientBounds) {
			t.Fatalf("dirty rect escaped client bounds: %v", rect)
		}
	}
}

func TestRegionSelectionInvalidatesBrightInterior(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 200, 100),
		mode:      "Region",
		selecting: true,
		start:     image.Pt(20, 10),
		current:   image.Pt(80, 60),
	}
	got := s.selectionDirtyClientRect(400, 200)
	want := image.Rect(36, 16, 164, 124)
	if got != want {
		t.Fatalf("selection dirty rect = %v, want %v", got, want)
	}
	s.current = image.Pt(40, 30)
	shrunk := s.selectionDirtyClientRect(400, 200)
	if !got.Overlaps(shrunk) || got == shrunk {
		t.Fatalf("selection resize dirty rects = %v then %v", got, shrunk)
	}
	s.mode = "Freeform"
	if got := s.selectionDirtyClientRect(400, 200); !got.Empty() {
		t.Fatalf("freeform dirty interior = %v", got)
	}
}

func TestSelectionExclusionMatchesLineWithoutKeepingDirtyPadding(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 100, 80),
		mode:      "Region",
		selecting: true,
		start:     image.Pt(10, 10),
		current:   image.Pt(70, 60),
	}
	exclusions := s.selectionClientRects(100, 80, 1)
	dirty := s.selectionClientRects(100, 80, 4)
	if len(exclusions) != 4 || len(dirty) != 4 {
		t.Fatalf("selection rect counts = %d/%d, want 4/4", len(exclusions), len(dirty))
	}
	for i := range exclusions {
		if !exclusions[i].In(dirty[i]) || exclusions[i] == dirty[i] {
			t.Fatalf("exclusion %v should fit strictly inside dirty rect %v", exclusions[i], dirty[i])
		}
	}
	s.mode = "Freeform"
	s.polygon = []image.Point{{X: 10, Y: 10}, {X: 70, Y: 60}}
	if got := s.selectionClientRects(100, 80, 1); len(got) != 0 {
		t.Fatalf("freeform bounding box must not be excluded from background: %v", got)
	}
}

func TestWindowClientRectsIncludeVisibleFragments(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 200, 100),
		mode:      "Window",
		hover:     0,
		windows: []nativeWindow{{visibleRects: []image.Rectangle{
			image.Rect(10, 20, 40, 50),
			image.Rect(60, 20, 90, 50),
		}}},
	}
	rects := s.windowClientRects(0, 400, 200)
	want := []image.Rectangle{image.Rect(20, 40, 80, 100), image.Rect(120, 40, 180, 100)}
	if !slices.Equal(rects, want) {
		t.Fatalf("hover dirty rects = %v, want %v", rects, want)
	}
}

func TestRegionPaintBufferRestoresSelectionBrightness(t *testing.T) {
	s := &nativeSession{
		imgBounds: image.Rect(0, 0, 2, 2),
		mode:      "Region",
		selecting: true,
		start:     image.Pt(0, 0),
		current:   image.Pt(1, 1),
		bgra:      make([]byte, 16),
		original:  []byte{80, 81, 82, 255, 90, 91, 92, 255, 100, 101, 102, 255, 110, 111, 112, 255},
		highlight: make([]byte, 16),
	}
	for i := range s.bgra {
		s.bgra[i] = 10
	}
	got := s.paintBuffer()
	if !slices.Equal(got[:4], s.original[:4]) {
		t.Fatalf("selected pixel = %v, want %v", got[:4], s.original[:4])
	}
	if !slices.Equal(got[4:8], s.bgra[4:8]) {
		t.Fatalf("outside pixel = %v, want dimmed %v", got[4:8], s.bgra[4:8])
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

func TestArrowKeyDeltaMovesOnePixel(t *testing.T) {
	for _, tc := range []struct {
		key  uintptr
		want image.Point
	}{
		{key: vkLeft, want: image.Pt(-1, 0)},
		{key: vkUp, want: image.Pt(0, -1)},
		{key: vkRight, want: image.Pt(1, 0)},
		{key: vkDown, want: image.Pt(0, 1)},
	} {
		got, ok := arrowKeyDelta(tc.key)
		if !ok || got != tc.want {
			t.Fatalf("arrowKeyDelta(%d) = %v, %v; want %v, true", tc.key, got, ok, tc.want)
		}
	}
	if _, ok := arrowKeyDelta(vkEscape); ok {
		t.Fatal("Escape should not be treated as an arrow key")
	}
}

func TestRegionSupportsArrowCursorNudgeBeforeSelection(t *testing.T) {
	session := nativeSession{mode: "Region", selecting: false}
	if !session.supportsArrowCursorNudge() {
		t.Fatal("region cursor nudge should work before the first click")
	}
	session.selecting = true
	if !session.supportsArrowCursorNudge() {
		t.Fatal("region cursor nudge should continue while selecting")
	}
	session.mode = "Window"
	if session.supportsArrowCursorNudge() {
		t.Fatal("window mode should not nudge the region cursor")
	}
}

func TestNudgedPointClampsToImagePixels(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 80)
	if got, changed := nudgedPoint(image.Pt(50, 40), image.Pt(1, 0), bounds); !changed || got != image.Pt(51, 40) {
		t.Fatalf("right nudge = %v, %v", got, changed)
	}
	if got, changed := nudgedPoint(image.Pt(0, 0), image.Pt(-1, 0), bounds); changed || got != image.Pt(0, 0) {
		t.Fatalf("left edge nudge = %v, %v", got, changed)
	}
	if got, changed := nudgedPoint(image.Pt(99, 79), image.Pt(1, 1), bounds); changed || got != image.Pt(99, 79) {
		t.Fatalf("bottom-right edge nudge = %v, %v", got, changed)
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

func TestMagnifierDestinationTracksCursorAndAvoidsEdges(t *testing.T) {
	bounds := image.Rect(0, 0, 1920, 1080)
	if got := magnifierDestinationRect(image.Pt(960, 540), 1920, 1080, bounds); got != image.Rect(984, 564, 1144, 724) {
		t.Fatalf("center destination = %v", got)
	}
	if got := magnifierDestinationRect(image.Pt(961, 540), 1920, 1080, bounds); got != image.Rect(985, 564, 1145, 724) {
		t.Fatalf("adjacent movement did not track cursor: %v", got)
	}
	if got := magnifierDestinationRect(image.Pt(1919, 1079), 1920, 1080, bounds); got != image.Rect(1735, 895, 1895, 1055) {
		t.Fatalf("edge destination = %v", got)
	}
	if got := magnifierDestinationRect(image.Pt(50, 40), 100, 80, image.Rect(0, 0, 100, 80)); got.Size() != image.Pt(80, 80) || !got.In(image.Rect(0, 0, 100, 80)) {
		t.Fatalf("small-client destination = %v", got)
	}
}

func TestMagnifierDestinationStaysInsideCursorDisplay(t *testing.T) {
	bounds := image.Rect(0, 0, 3840, 2160)
	displays := []image.Rectangle{
		image.Rect(0, 0, 1920, 1080),
		image.Rect(1920, 1080, 3840, 2160),
	}
	firstDisplay := image.Rect(0, 0, 1920, 1080)
	if got := magnifierDestinationRectForDisplays(image.Pt(1850, 1020), 3840, 2160, bounds, displays); !got.In(firstDisplay) {
		t.Fatalf("first-display destination escaped its monitor: %v", got)
	}
	secondDisplay := image.Rect(1920, 1080, 3840, 2160)
	if got := magnifierDestinationRectForDisplays(image.Pt(1950, 1110), 3840, 2160, bounds, displays); !got.In(secondDisplay) {
		t.Fatalf("second-display destination escaped its monitor: %v", got)
	}
}

func TestMagnifierDisplayBoundsScaleWithOverlayClient(t *testing.T) {
	bounds := image.Rect(0, 0, 3840, 1080)
	displays := []image.Rectangle{
		image.Rect(0, 0, 1920, 1080),
		image.Rect(1920, 0, 3840, 1080),
	}
	leftClientDisplay := image.Rect(0, 0, 1920, 2160)
	if got := magnifierDestinationRectForDisplays(image.Pt(1919, 1079), 3840, 2160, bounds, displays); !got.In(leftClientDisplay) {
		t.Fatalf("scaled destination escaped the cursor display: %v", got)
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
	geometry := make(chan image.Rectangle, 1)
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		previousDPIContext, _, _ := procSetThreadDPIAwarenessContext.Call(^uintptr(3))
		if previousDPIContext != 0 {
			defer procSetThreadDPIAwarenessContext.Call(previousDPIContext)
		}
		getWindowRect := nativeUser32.NewProc("GetWindowRect")
		for attempt := 0; attempt < 100; attempt++ {
			activeMu.Lock()
			s := activeSession
			var hwnd uintptr
			if s != nil {
				hwnd = s.hwnd
			}
			activeMu.Unlock()
			if hwnd != 0 {
				var rect nativeRect
				if ok, _, _ := getWindowRect.Call(hwnd, uintptr(unsafe.Pointer(&rect))); ok != 0 {
					geometry <- image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom))
					return
				}
			}
			time.Sleep(2 * time.Millisecond)
		}
		geometry <- image.Rectangle{}
	}()
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
	if got := <-geometry; got != virtualScreen {
		t.Fatalf("overlay window rect = %v, virtual screen = %v", got, virtualScreen)
	}
}

func TestSelectNativeScrollBrowserSmoke(t *testing.T) {
	type smokeState struct {
		start      image.Point
		current    image.Point
		wheelPoint image.Point
		noMotion   bool
		offset     int
		steps      int
		hooked     int
		handled    int
		sent       int
		selecting  bool
		uiaOrigin  bool
		foreground uintptr
	}
	handleText := os.Getenv("INSTANTCAPTURE_BROWSER_HWND")
	if handleText == "" {
		t.Skip("set INSTANTCAPTURE_BROWSER_HWND to a visible browser HWND")
	}
	if err := platformwindows.EnableDPIAwareness(); err != nil {
		t.Fatal(err)
	}
	handle, err := strconv.ParseUint(handleText, 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	displays := capture.ScreenshotCapturer{}.Displays()
	virtualScreen := capture.UnionDisplays(displays)
	if virtualScreen.Empty() {
		t.Fatal("no displays")
	}
	var target platformwindows.WindowTarget
	for _, candidate := range platformwindows.VisibleWindowTargets("InstantCapture") {
		if candidate.Handle == uintptr(handle) {
			target = candidate
			break
		}
	}
	if target.Handle == 0 {
		var rect nativeRect
		getWindowRect := nativeUser32.NewProc("GetWindowRect")
		if ok, _, _ := getWindowRect.Call(uintptr(handle), uintptr(unsafe.Pointer(&rect))); ok == 0 {
			t.Fatalf("browser HWND %d has no window rectangle", handle)
		}
		targetRect := image.Rect(int(rect.Left), int(rect.Top), int(rect.Right), int(rect.Bottom)).Intersect(virtualScreen)
		if targetRect.Empty() {
			t.Fatalf("browser HWND %d is outside the virtual screen", handle)
		}
		target = platformwindows.WindowTarget{Handle: uintptr(handle), Rect: targetRect, VisibleRects: []image.Rectangle{targetRect}}
	}
	targetCenter := target.Rect.Min.Add(target.Rect.Size().Div(2))
	secondaryDisplay := false
	for _, display := range displays {
		if targetCenter.In(display) && display.Min != virtualScreen.Min {
			secondaryDisplay = true
			break
		}
	}
	if !secondaryDisplay {
		t.Fatalf("browser target %v is not on a secondary display; mixed-DPI smoke requires one", target.Rect)
	}
	t.Logf("browser target=%v virtual=%v", target.Rect, virtualScreen)
	focusPoint := image.Pt(target.Rect.Min.X+target.Rect.Dx()/2, target.Rect.Min.Y+min(160, target.Rect.Dy()/3))
	procSetCursorPos.Call(intArg(focusPoint.X), intArg(focusPoint.Y))
	focusDown := nativeInput{Type: inputMouse, Mi: nativeMouseInput{DwFlags: 0x0002}}
	focusUp := nativeInput{Type: inputMouse, Mi: nativeMouseInput{DwFlags: 0x0004}}
	procSendInput.Call(1, uintptr(unsafe.Pointer(&focusDown)), unsafe.Sizeof(focusDown))
	procSendInput.Call(1, uintptr(unsafe.Pointer(&focusUp)), unsafe.Sizeof(focusUp))
	time.Sleep(200 * time.Millisecond)
	sampler := &nativeSession{imgBounds: image.Rect(0, 0, virtualScreen.Dx(), virtualScreen.Dy()), virtualMin: virtualScreen.Min}
	sampleRect := target.Rect.Sub(virtualScreen.Min)
	originSample := sampler.captureScrollSample(sampleRect)
	if !validScrollSample(originSample) {
		t.Fatal("could not capture browser origin sample")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	done := make(chan struct{})
	states := make(chan smokeState, 1)
	go func() {
		defer close(done)
		var originalCursor nativePoint
		procGetCursorPos.Call(uintptr(unsafe.Pointer(&originalCursor)))
		defer procSetCursorPos.Call(intArg(int(originalCursor.X)), intArg(int(originalCursor.Y)))
		for attempt := 0; attempt < 200; attempt++ {
			activeMu.Lock()
			s := activeSession
			var overlayHWND uintptr
			if s != nil {
				overlayHWND = s.hwnd
			}
			activeMu.Unlock()
			if overlayHWND == 0 {
				time.Sleep(10 * time.Millisecond)
				continue
			}
			time.Sleep(200 * time.Millisecond)
			startScreen := image.Pt(target.Rect.Min.X+target.Rect.Dx()/2, target.Rect.Min.Y+min(120, target.Rect.Dy()/3))
			endScreen := image.Pt(startScreen.X+max(80, min(320, target.Rect.Dx()/3)), target.Rect.Max.Y-80)
			procSetCursorPos.Call(intArg(startScreen.X), intArg(startScreen.Y))
			down := nativeInput{Type: inputMouse, Mi: nativeMouseInput{DwFlags: 0x0002}}
			procSendInput.Call(1, uintptr(unsafe.Pointer(&down)), unsafe.Sizeof(down))
			time.Sleep(150 * time.Millisecond)
			procSetCursorPos.Call(intArg(endScreen.X), intArg(endScreen.Y))
			for range 5 {
				procSetCursorPos.Call(intArg(endScreen.X), intArg(endScreen.Y))
				delta := -wheelDelta
				wheel := nativeInput{Type: inputMouse, Mi: nativeMouseInput{MouseData: uint32(int32(delta)), DwFlags: mouseEventWheel}}
				procSendInput.Call(1, uintptr(unsafe.Pointer(&wheel)), unsafe.Sizeof(wheel))
				time.Sleep(180 * time.Millisecond)
			}
			procSetCursorPos.Call(intArg(endScreen.X), intArg(endScreen.Y))
			time.Sleep(100 * time.Millisecond)
			activeMu.Lock()
			state := smokeState{}
			if activeSession != nil {
				state.start = activeSession.start
				state.current = activeSession.current
				state.wheelPoint = activeSession.scrollWheelPoint
				state.noMotion = activeSession.scrollNoMotion
				state.offset = activeSession.scrollOffsetY
				state.steps = activeSession.scrollWheelSteps
				state.hooked = activeSession.scrollHookWheelCount
				state.handled = activeSession.scrollHandledWheels
				state.sent = activeSession.scrollSentWheels
				state.selecting = activeSession.selecting
				state.uiaOrigin = activeSession.scrollInitialPosition != nil
			}
			state.foreground, _, _ = nativeUser32.NewProc("GetForegroundWindow").Call()
			activeMu.Unlock()
			states <- state
			up := nativeInput{Type: inputMouse, Mi: nativeMouseInput{DwFlags: 0x0004}}
			procSendInput.Call(1, uintptr(unsafe.Pointer(&up)), unsafe.Sizeof(up))
			time.Sleep(500 * time.Millisecond)
			return
		}
	}()

	img := image.NewRGBA(image.Rect(0, 0, virtualScreen.Dx(), virtualScreen.Dy()))
	selectionStarted := time.Now()
	selection, ok, err := selectNative(ctx, img, overlay.Options{
		VirtualScreen:  virtualScreen,
		CaptureMode:    "Scroll",
		WindowRegions:  []overlay.WindowRegion{{Handle: target.Handle, Rect: target.Rect, VisibleRects: target.VisibleRects}},
		TopMostHint:    true,
		DimAlpha:       130,
		LiveBackground: true,
	})
	t.Logf("selection duration: %v", time.Since(selectionStarted))
	<-done
	observedState := <-states
	t.Logf("stationary-edge state: %+v", observedState)
	if err != nil {
		restoredSample := sampler.captureStableScrollSample(sampleRect)
		maxAbs := min(restoredSample.height-scrollScoreMinSamples, max(120, restoredSample.height/2))
		bestDelta := 0
		bestScore := int64(1<<62 - 1)
		for delta := -maxAbs; delta <= maxAbs; delta++ {
			score, ok := scrollDeltaScore(originSample, restoredSample, delta)
			if ok && score < bestScore {
				bestDelta = delta
				bestScore = score
			}
		}
		t.Fatalf("%v; browser remained at delta=%d score=%d", err, bestDelta, bestScore)
	}
	if !observedState.selecting || observedState.hooked != 5 || observedState.handled != 5 || observedState.noMotion {
		t.Fatalf("wheel delivery was not continuous: %+v", observedState)
	}
	expectedStart := image.Pt(target.Rect.Min.X+target.Rect.Dx()/2, target.Rect.Min.Y+min(120, target.Rect.Dy()/3)).Sub(virtualScreen.Min)
	expectedEnd := image.Pt(expectedStart.X+max(80, min(320, target.Rect.Dx()/3)), target.Rect.Max.Y-80-virtualScreen.Min.Y)
	if absInt(observedState.start.X-expectedStart.X) > 1 || absInt(observedState.start.Y-expectedStart.Y) > 1 {
		t.Fatalf("selection start image point = %v, physical-to-image expected %v", observedState.start, expectedStart)
	}
	if absInt(observedState.current.X-expectedEnd.X) > 1 || absInt(observedState.current.Y-expectedEnd.Y) > 1 {
		t.Fatalf("selection end image point = %v, physical-to-image expected %v", observedState.current, expectedEnd)
	}
	if absInt(observedState.wheelPoint.X-expectedEnd.X) > 1 || absInt(observedState.wheelPoint.Y-expectedEnd.Y) > 1 {
		t.Fatalf("wheel target image point = %v, expected %v", observedState.wheelPoint, expectedEnd)
	}
	if !ok || selection.ScrollRange == nil {
		t.Fatalf("scroll selection failed: ok=%v range=%#v", ok, selection.ScrollRange)
	}
	expectedWindowRect := target.Rect.Sub(virtualScreen.Min)
	if selection.ScrollRange.WindowRect != expectedWindowRect {
		t.Fatalf("selection window rect = %v, expected %v", selection.ScrollRange.WindowRect, expectedWindowRect)
	}
	if selection.ScrollRange.ScrollPixels <= 0 || selection.ScrollRange.ScrollSteps != 5 {
		t.Fatalf("browser did not scroll: pixels=%d steps=%d", selection.ScrollRange.ScrollPixels, selection.ScrollRange.ScrollSteps)
	}
	if selection.ScrollRange.InitialScrollPercent == nil {
		t.Fatal("Chrome selection did not preserve its UI Automation origin")
	}
	maxAbs := min(originSample.height-scrollScoreMinSamples, max(120, originSample.height/2))
	windowScreen := selection.ScrollRange.WindowRect.Add(virtualScreen.Min)
	cropScreen := selection.ScrollRange.CropRect.Add(virtualScreen.Min)
	viewScreen := selection.ScrollRange.ViewRect.Add(virtualScreen.Min)
	rangeRequest := platformwindows.ScrollCaptureRange{
		Crop: image.Rectangle{
			Min: cropScreen.Min.Sub(windowScreen.Min),
			Max: cropScreen.Max.Sub(windowScreen.Min),
		}.Canon(),
		View: image.Rectangle{
			Min: viewScreen.Min.Sub(windowScreen.Min),
			Max: viewScreen.Max.Sub(windowScreen.Min),
		}.Canon(),
		ScrollSteps:          selection.ScrollRange.ScrollSteps,
		ScrollPixels:         selection.ScrollRange.ScrollPixels,
		InitialScrollPercent: selection.ScrollRange.InitialScrollPercent,
		InitialScrollPoint:   selection.ScrollRange.InitialScrollPoint.Add(virtualScreen.Min).Sub(windowScreen.Min),
		InitialWheelUnits:    selection.ScrollRange.InitialWheelUnits,
	}
	captureStarted := time.Now()
	captured, captureErr := platformwindows.CaptureScrollRange(target, rangeRequest)
	t.Logf("helper capture duration: %v", time.Since(captureStarted))
	if captureErr != nil {
		t.Fatalf("end-to-end scroll capture failed: %v", captureErr)
	}
	if captured.Bounds().Dx() != rangeRequest.Crop.Dx() || captured.Bounds().Dy() <= rangeRequest.View.Dy() {
		t.Fatalf("end-to-end image bounds = %v, crop=%v view=%v", captured.Bounds(), rangeRequest.Crop, rangeRequest.View)
	}
	afterCapture := sampler.captureStableScrollSample(sampleRect)
	bestAfterCapture := 0
	bestAfterScore := int64(1<<62 - 1)
	for delta := -maxAbs; delta <= maxAbs; delta++ {
		score, ok := scrollDeltaScore(originSample, afterCapture, delta)
		if ok && score < bestAfterScore {
			bestAfterCapture = delta
			bestAfterScore = score
		}
	}
	if absInt(bestAfterCapture) > 4 {
		t.Fatalf("helper did not restore origin: delta=%d score=%d", bestAfterCapture, bestAfterScore)
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
