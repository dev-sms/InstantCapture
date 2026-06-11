package ui

import (
	"errors"
	"image"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/op"

	"instantcapture/internal/config"
	"instantcapture/internal/coords"
	"instantcapture/internal/editor"
	"instantcapture/internal/hotkey"
	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/workflow"
)

type uiFakeCapturer struct {
	displays []image.Rectangle
	img      image.Image
}

func TestWindowUICanvasViewUndoRedo(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	u.canvasPan = image.Pt(12, -8)
	u.canvasSourceSize = image.Pt(800, 600)
	after := u.captureCanvasView()
	u.recordCanvasViewEdit(before, after)

	if !u.undoLastEdit() {
		t.Fatal("expected zoom undo")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("unexpected undo view: %#v", got)
	}
	if !u.redoLastEdit() {
		t.Fatal("expected zoom redo")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("unexpected redo view: %#v", got)
	}
}

func TestWindowUICanvasViewEditClearsRedo(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	first := u.captureCanvasView()
	u.recordCanvasViewEdit(before, first)
	if !u.undoLastEdit() {
		t.Fatal("expected undo")
	}
	u.canvasZoom = 3
	second := u.captureCanvasView()
	u.recordCanvasViewEdit(before, second)
	if u.redoLastEdit() {
		t.Fatal("redo should be cleared after a new zoom edit")
	}
}

func TestWindowUIControllerUndoRestoresCanvasView(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 200, 120)},
		img:      image.NewRGBA(image.Rect(0, 0, 200, 120)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 100, 80)); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{controller: controller, canvasZoom: 2, canvasPan: image.Pt(20, -10), canvasSourceSize: image.Pt(100, 80)}
	before := u.captureCanvasView()
	if err := controller.CropCapturedImage(image.Rect(10, 5, 80, 60)); err != nil {
		t.Fatal(err)
	}
	u.resetCanvasView()
	u.recordControllerEdit(before, u.captureCanvasView())

	if !u.undoLastEdit() {
		t.Fatal("expected crop undo")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("unexpected restored view: %#v", got)
	}
	if got := controller.CapturedImage.Bounds(); got != image.Rect(0, 0, 100, 80) {
		t.Fatalf("unexpected restored image bounds: %v", got)
	}
}

func TestCommitCropPreviewUsesPendingFirstEditAutoCopy(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 200, 120)},
		img:      image.NewRGBA(image.Rect(0, 0, 200, 120)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 100, 80)); err != nil {
		t.Fatal(err)
	}
	calls := 0
	u := &windowUI{
		controller:      controller,
		canvasZoom:      1,
		cropStartRect:   image.Rect(0, 0, 100, 80),
		cropPreviewRect: image.Rect(10, 5, 80, 60),
		autoCopyPending: true,
		copyPNG: func() error {
			calls++
			return nil
		},
	}

	u.commitCropPreview()

	if calls != 1 {
		t.Fatalf("copy calls = %d, want 1", calls)
	}
	if u.autoCopyPending {
		t.Fatal("pending auto-copy should be consumed by crop edit")
	}
	if !strings.Contains(u.status, "클립보드에 복사했습니다") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestWindowUIPanCanvasBySourceDelta(t *testing.T) {
	u := &windowUI{canvasPan: image.Pt(-50, -100)}
	viewport := coords.Viewport{
		Source: image.Rect(0, 0, 1000, 1000),
		View:   image.Rect(-250, -350, 750, 650),
		Scale:  1,
	}
	if !u.panCanvasBySourceDelta(editor.Point{X: 100, Y: 100}, editor.Point{X: 130, Y: 80}, image.Rect(0, 0, 500, 300), viewport) {
		t.Fatal("expected pan to change")
	}
	if got, want := u.canvasPan, image.Pt(-20, -120); got != want {
		t.Fatalf("canvas pan = %v, want %v", got, want)
	}
}

func TestWindowUIPanCanvasBySourceDeltaClamps(t *testing.T) {
	u := &windowUI{canvasPan: image.Pt(250, 350)}
	viewport := coords.Viewport{
		Source: image.Rect(0, 0, 1000, 1000),
		View:   image.Rect(0, 0, 1000, 1000),
		Scale:  1,
	}
	if u.panCanvasBySourceDelta(editor.Point{X: 100, Y: 100}, editor.Point{X: 1000, Y: 1000}, image.Rect(0, 0, 500, 300), viewport) {
		t.Fatal("pan should remain clamped at the maximum bound")
	}
}

func TestCanvasPanButtonUsesMiddleOnly(t *testing.T) {
	if !isCanvasPanButton(pointer.ButtonTertiary) {
		t.Fatal("middle button should start canvas panning")
	}
	if isCanvasPanButton(pointer.ButtonSecondary) {
		t.Fatal("right button should not start canvas panning")
	}
	if isCanvasPanButton(pointer.ButtonPrimary) {
		t.Fatal("left button should remain reserved for annotations")
	}
}

func TestWindowUIToastDetailPersistsLongEnough(t *testing.T) {
	u := &windowUI{}
	u.showToastDetail("Saved PNG", "file.png")

	if u.toastTitle != "Saved PNG" {
		t.Fatalf("unexpected toast title: %q", u.toastTitle)
	}
	if u.toastMessage != "file.png" {
		t.Fatalf("unexpected toast message: %q", u.toastMessage)
	}
	if time.Until(u.toastUntil) < 4*time.Second {
		t.Fatalf("toast duration too short: %s", time.Until(u.toastUntil))
	}
}

func TestFinishCaptureReadyCopiesImmediatelyAndKeepsPendingEditCopy(t *testing.T) {
	calls := 0
	u := &windowUI{copyPNG: func() error {
		calls++
		return nil
	}}

	u.finishCaptureReadyWithInitialCopy("100x80 캡처를 편집 중입니다.")

	if calls != 1 {
		t.Fatalf("copy calls = %d, want 1", calls)
	}
	if !u.autoCopyPending {
		t.Fatal("first edit auto-copy should remain pending")
	}
	if !strings.Contains(u.status, "첫 캡처를 클립보드에 복사했습니다") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestFinishCaptureReadyReportsInitialCopyFailureButKeepsPendingEditCopy(t *testing.T) {
	u := &windowUI{copyPNG: func() error { return errors.New("clipboard busy") }}

	u.finishCaptureReadyWithInitialCopy("100x80 캡처를 편집 중입니다.")

	if !u.autoCopyPending {
		t.Fatal("first edit auto-copy should remain pending after initial copy failure")
	}
	if !strings.Contains(u.status, "최초 복사 실패") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestHandleWindowEventDestroyReturnsError(t *testing.T) {
	want := errors.New("closed")
	u := &windowUI{}
	shouldReturn, err := u.handleWindowEvent(app.DestroyEvent{Err: want}, nil, &op.Ops{})
	if !shouldReturn {
		t.Fatal("destroy event should stop the window loop")
	}
	if !errors.Is(err, want) {
		t.Fatalf("destroy error = %v, want %v", err, want)
	}
}

func TestScrollCaptureModeIsValidAndCycled(t *testing.T) {
	if !validCaptureMode("Scroll") {
		t.Fatal("Scroll should be a valid capture mode")
	}
	if got := nextCaptureMode("Window"); got != "Scroll" {
		t.Fatalf("next mode after Window = %q, want Scroll", got)
	}
	if got := nextCaptureMode("Scroll"); got != "Freeform" {
		t.Fatalf("next mode after Scroll = %q, want Freeform", got)
	}
}

func TestScrollExternalRectUsesCapturedImageSize(t *testing.T) {
	target := platformwindows.WindowTarget{Rect: image.Rect(100, 200, 500, 600)}
	img := image.NewRGBA(image.Rect(0, 0, 400, 1200))
	got := scrollExternalRect(target, img)
	want := image.Rect(100, 200, 500, 1400)
	if got != want {
		t.Fatalf("scroll rect = %v, want %v", got, want)
	}
}

func TestShortcutKeysUndoRedoCanvasView(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	after := u.captureCanvasView()
	u.recordCanvasViewEdit(before, after)

	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+Z to be handled")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("Ctrl+Z did not undo: %#v", got)
	}
	if !u.handleShortcutKey(key.Event{Name: "Y", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+Y to be handled")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("Ctrl+Y did not redo: %#v", got)
	}
	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected second Ctrl+Z to be handled")
	}
	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut | key.ModShift, State: key.Press}) {
		t.Fatal("expected Ctrl+Shift+Z to be handled")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("Ctrl+Shift+Z did not redo: %#v", got)
	}
}

func TestShortcutEscapeCancelsGestures(t *testing.T) {
	u := &windowUI{dragging: true, cropDragging: true}
	if !u.handleShortcutKey(key.Event{Name: key.NameEscape, State: key.Press}) {
		t.Fatal("expected Escape to be handled")
	}
	if u.dragging || u.cropDragging {
		t.Fatal("Escape did not cancel gestures")
	}
}

func TestHotkeyCaptureBindingFromKeyEvent(t *testing.T) {
	binding, ok := hotkeyBindingFromKeyEvent(key.Event{Name: "K", Modifiers: key.ModCtrl | key.ModAlt, State: key.Press})
	if !ok {
		t.Fatal("expected Ctrl+Alt+K to be a valid capture hotkey")
	}
	if binding.Name != "Ctrl+Alt+K" {
		t.Fatalf("unexpected binding name: %q", binding.Name)
	}
}

func TestHotkeyCaptureSpecialKeyNames(t *testing.T) {
	for _, tc := range []struct {
		name key.Name
		want string
	}{
		{name: key.Name("PrintScreen"), want: "PrintScreen"},
		{name: key.NameHome, want: "Home"},
		{name: key.NameEnd, want: "End"},
		{name: key.NamePageUp, want: "PageUp"},
		{name: key.NamePageDown, want: "PageDown"},
		{name: key.NameDeleteBackward, want: "Backspace"},
	} {
		if got := hotkeyCaptureKeyName(tc.name); got != tc.want {
			t.Fatalf("hotkeyCaptureKeyName(%q) = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestHotkeyCaptureFiltersRegisterFocusableTarget(t *testing.T) {
	tag := new(struct{})
	filters := hotkeyCaptureFilters(tag)
	if len(filters) == 0 {
		t.Fatal("expected capture filters")
	}
	focus, ok := filters[0].(key.FocusFilter)
	if !ok {
		t.Fatalf("first filter should make the capture tag focusable, got %T", filters[0])
	}
	if focus.Target != tag {
		t.Fatal("focus filter should target the capture key tag")
	}
}

func TestHotkeyCaptureIgnoresModifierAndEscapeCancels(t *testing.T) {
	u := &windowUI{}
	u.beginHotkeyCapture()
	if !u.hotkeyCaptureMode {
		t.Fatal("expected hotkey capture mode")
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Press}) {
		t.Fatal("expected modifier press to be handled by capture mode")
	}
	if !u.hotkeyCaptureMode {
		t.Fatal("modifier-only input should keep capture mode active")
	}
	if u.appConfig.Hotkey != "" {
		t.Fatalf("modifier-only input should not assign hotkey, got %q", u.appConfig.Hotkey)
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameCtrl, State: key.Release}) {
		t.Fatal("expected modifier release to be handled by capture mode")
	}
	if !u.hotkeyCaptureMode {
		t.Fatal("modifier-only release should keep capture mode active")
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameEscape, State: key.Press}) {
		t.Fatal("expected Escape to cancel capture mode")
	}
	if u.hotkeyCaptureMode {
		t.Fatal("Escape should cancel hotkey capture mode")
	}
}

func TestShortcutKeysDoNotEditWhileCapturingHotkey(t *testing.T) {
	saved := 0
	u := &windowUI{
		canvasZoom:        1,
		hotkeyCaptureMode: true,
		saveConfig: func(config.Config) error {
			saved++
			return nil
		},
		rebindHotkey: func(hotkey.Binding, func()) error {
			return nil
		},
	}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	after := u.captureCanvasView()
	u.recordCanvasViewEdit(before, after)

	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+Z to be handled by hotkey capture mode")
	}
	if got := u.captureCanvasView(); got != after {
		t.Fatalf("Ctrl+Z should not undo while capturing: %#v", got)
	}
	if u.appConfig.Hotkey != "" || saved != 0 {
		t.Fatalf("hotkey should not save until release, got %q saved %d", u.appConfig.Hotkey, saved)
	}
	if !u.handleShortcutKey(key.Event{Name: "Z", Modifiers: key.ModCtrl, State: key.Release}) {
		t.Fatal("expected Z release to be handled by hotkey capture mode")
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameCtrl, State: key.Release}) {
		t.Fatal("expected Ctrl release to be handled by hotkey capture mode")
	}
	if u.appConfig.Hotkey != "Ctrl+Z" {
		t.Fatalf("expected Ctrl+Z to be assigned after release, got %q", u.appConfig.Hotkey)
	}
}

func TestHotkeyCaptureSameBindingSkipsRebind(t *testing.T) {
	rebound := false
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Alt+K"},
		saveConfig: func(config.Config) error {
			return nil
		},
		rebindHotkey: func(hotkey.Binding, func()) error {
			rebound = true
			return nil
		},
	}

	if !u.handleShortcutKey(key.Event{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Press}) {
		t.Fatal("expected Ctrl press to be handled")
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameAlt, Modifiers: key.ModCtrl | key.ModAlt, State: key.Press}) {
		t.Fatal("expected Alt press to be handled")
	}
	if !u.handleShortcutKey(key.Event{Name: "K", Modifiers: key.ModCtrl | key.ModAlt, State: key.Press}) {
		t.Fatal("expected same binding to be handled")
	}
	if !u.hotkeyCaptureMode {
		t.Fatal("same binding should wait until release")
	}
	if !u.handleShortcutKey(key.Event{Name: "K", Modifiers: key.ModCtrl | key.ModAlt, State: key.Release}) {
		t.Fatal("expected K release to be handled")
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameAlt, Modifiers: key.ModCtrl, State: key.Release}) {
		t.Fatal("expected Alt release to be handled")
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameCtrl, State: key.Release}) {
		t.Fatal("expected Ctrl release to be handled")
	}
	if rebound {
		t.Fatal("same binding should not be re-registered")
	}
	if u.hotkeyCaptureMode {
		t.Fatal("same binding should leave capture mode")
	}
}

func TestHotkeyCaptureSavesAltPrintScreenAfterAllKeysReleased(t *testing.T) {
	saved := 0
	rebound := 0
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig: func(cfg config.Config) error {
			saved++
			if cfg.Hotkey != "Alt+PrintScreen" {
				t.Fatalf("saved hotkey = %q, want Alt+PrintScreen", cfg.Hotkey)
			}
			return nil
		},
		rebindHotkey: func(binding hotkey.Binding, _ func()) error {
			rebound++
			if binding.Name != "Alt+PrintScreen" {
				t.Fatalf("rebound hotkey = %q, want Alt+PrintScreen", binding.Name)
			}
			return nil
		},
	}

	u.handleShortcutKey(key.Event{Name: key.NameAlt, Modifiers: key.ModAlt, State: key.Press})
	if u.appConfig.Hotkey != "Ctrl+Shift+C" || saved != 0 || rebound != 0 {
		t.Fatal("modifier press should not save")
	}
	u.handleShortcutKey(key.Event{Name: "PrintScreen", Modifiers: key.ModAlt, State: key.Press})
	if got := u.hotkeyButtonLabel(); got != "단축키 입력 중: Alt+PrintScreen" {
		t.Fatalf("unexpected capture label: %q", got)
	}
	if u.appConfig.Hotkey != "Ctrl+Shift+C" || saved != 0 || rebound != 0 {
		t.Fatal("combo should not save before release")
	}
	u.handleShortcutKey(key.Event{Name: "PrintScreen", Modifiers: key.ModAlt, State: key.Release})
	if !u.hotkeyCaptureMode || saved != 0 || rebound != 0 {
		t.Fatal("first key release should not save while Alt remains pressed")
	}
	u.handleShortcutKey(key.Event{Name: key.NameAlt, State: key.Release})
	if u.hotkeyCaptureMode {
		t.Fatal("capture mode should end after all keys are released")
	}
	if saved != 1 || rebound != 1 || u.appConfig.Hotkey != "Alt+PrintScreen" {
		t.Fatalf("unexpected save/rebind result: saved=%d rebound=%d hotkey=%q", saved, rebound, u.appConfig.Hotkey)
	}
}

func TestHotkeyCaptureSavesNativePrintScreenAfterRelease(t *testing.T) {
	saved := 0
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig: func(cfg config.Config) error {
			saved++
			if cfg.Hotkey != "PrintScreen" {
				t.Fatalf("saved hotkey = %q, want PrintScreen", cfg.Hotkey)
			}
			return nil
		},
		rebindHotkey: func(binding hotkey.Binding, _ func()) error {
			if binding.Name != "PrintScreen" {
				t.Fatalf("rebound hotkey = %q, want PrintScreen", binding.Name)
			}
			return nil
		},
	}

	u.syncNativeHotkeyCaptureKeys(nil)
	if saved != 0 || u.appConfig.Hotkey != "Ctrl+Shift+C" {
		t.Fatal("initial empty native poll should not save")
	}
	u.syncNativeHotkeyCaptureKeys([]string{"PrintScreen"})
	if got := u.hotkeyButtonLabel(); got != "단축키 입력 중: PrintScreen" {
		t.Fatalf("unexpected label: %q", got)
	}
	if saved != 0 {
		t.Fatal("PrintScreen press should wait for release")
	}
	u.syncNativeHotkeyCaptureKeys(nil)
	if u.hotkeyCaptureMode {
		t.Fatal("capture should finish after native PrintScreen release")
	}
	if saved != 1 || u.appConfig.Hotkey != "PrintScreen" {
		t.Fatalf("unexpected result: saved=%d hotkey=%q", saved, u.appConfig.Hotkey)
	}
}

func TestHotkeyCaptureMergesNativePrintScreenWithModifier(t *testing.T) {
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig: func(cfg config.Config) error {
			if cfg.Hotkey != "Alt+PrintScreen" {
				t.Fatalf("saved hotkey = %q, want Alt+PrintScreen", cfg.Hotkey)
			}
			return nil
		},
		rebindHotkey: func(binding hotkey.Binding, _ func()) error {
			if binding.Name != "Alt+PrintScreen" {
				t.Fatalf("rebound hotkey = %q, want Alt+PrintScreen", binding.Name)
			}
			return nil
		},
	}

	u.handleShortcutKey(key.Event{Name: key.NameAlt, Modifiers: key.ModAlt, State: key.Press})
	u.syncNativeHotkeyCaptureKeys([]string{"Alt", "PrintScreen"})
	if got := u.hotkeyButtonLabel(); got != "단축키 입력 중: Alt+PrintScreen" {
		t.Fatalf("unexpected label: %q", got)
	}
	u.syncNativeHotkeyCaptureKeys([]string{"Alt"})
	if !u.hotkeyCaptureMode {
		t.Fatal("capture should wait while Alt remains pressed")
	}
	u.syncNativeHotkeyCaptureKeys(nil)
	if u.hotkeyCaptureMode || u.appConfig.Hotkey != "Alt+PrintScreen" {
		t.Fatalf("unexpected final state: capture=%v hotkey=%q", u.hotkeyCaptureMode, u.appConfig.Hotkey)
	}
}

func TestHotkeyCaptureRejectsFourthSimultaneousKey(t *testing.T) {
	u := &windowUI{hotkeyCaptureMode: true}
	u.handleShortcutKey(key.Event{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Press})
	u.handleShortcutKey(key.Event{Name: key.NameAlt, Modifiers: key.ModCtrl | key.ModAlt, State: key.Press})
	u.handleShortcutKey(key.Event{Name: "K", Modifiers: key.ModCtrl | key.ModAlt, State: key.Press})
	if got := u.hotkeyButtonLabel(); got != "단축키 입력 중: Ctrl+Alt+K" {
		t.Fatalf("unexpected valid label before overflow: %q", got)
	}
	u.handleShortcutKey(key.Event{Name: key.NameShift, Modifiers: key.ModCtrl | key.ModAlt | key.ModShift, State: key.Press})
	if !strings.Contains(u.status, "최대 3개") {
		t.Fatalf("expected max-3 warning, got %q", u.status)
	}
	if got := u.hotkeyButtonLabel(); got != "단축키 입력 중: Ctrl+Alt+K" {
		t.Fatalf("fourth key should be rejected, label = %q", got)
	}
}

func TestHitCropHandleUsesTightScreenTolerance(t *testing.T) {
	u := &windowUI{controller: &workflow.Controller{Mode: workflow.ModeEditing}}
	bounds := image.Rect(0, 0, 200, 120)

	if got := u.hitCropHandle(editor.Point{X: 4, Y: 60}, bounds, 1); got != cropHandleLeft {
		t.Fatalf("near left handle = %v, want %v", got, cropHandleLeft)
	}
	if got := u.hitCropHandle(editor.Point{X: 5, Y: 60}, bounds, 1); got != cropHandleNone {
		t.Fatalf("inside non-border point = %v, want none", got)
	}
	if got := u.hitCropHandle(editor.Point{X: 1, Y: 60}, bounds, 8); got != cropHandleNone {
		t.Fatalf("high-zoom non-border point = %v, want none", got)
	}
	if got := u.hitCropHandle(editor.Point{X: 0, Y: 60}, bounds, 8); got != cropHandleLeft {
		t.Fatalf("high-zoom border point = %v, want %v", got, cropHandleLeft)
	}
}

func TestHitCropHandleRequiresPointWithinBorderSpan(t *testing.T) {
	u := &windowUI{controller: &workflow.Controller{Mode: workflow.ModeEditing}}
	bounds := image.Rect(50, 50, 150, 120)

	if got := u.hitCropHandle(editor.Point{X: 50, Y: 20}, bounds, 1); got != cropHandleNone {
		t.Fatalf("point outside vertical span = %v, want none", got)
	}
	if got := u.hitCropHandle(editor.Point{X: 80, Y: 50}, bounds, 1); got != cropHandleTop {
		t.Fatalf("top border handle = %v, want %v", got, cropHandleTop)
	}
}

func TestToastSavePathIncludesFileAndDirectory(t *testing.T) {
	got := toastSavePath(`C:\Users\someone\Pictures\Screenshots\capture.png`)
	if !strings.Contains(got, "capture.png") || !strings.Contains(got, "Screenshots") {
		t.Fatalf("unexpected toast path: %q", got)
	}
}

func TestResolveSaveDirUsesCustomOrFallback(t *testing.T) {
	fallback := `C:\Users\someone\Pictures\Screenshots`
	custom := `C:\Captures`
	if got := resolveSaveDir(config.Config{SaveDir: custom}, fallback); got != custom {
		t.Fatalf("custom dir = %q, want %q", got, custom)
	}
	if got := resolveSaveDir(config.Config{}, fallback); got != fallback {
		t.Fatalf("fallback dir = %q, want %q", got, fallback)
	}
}

func (f *uiFakeCapturer) Capture(rect image.Rectangle) (image.Image, error) {
	return f.img, nil
}

func (f *uiFakeCapturer) Displays() []image.Rectangle {
	return f.displays
}
