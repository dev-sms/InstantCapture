package ui

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/io/event"
	"gioui.org/io/input"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/unit"

	"instantcapture/internal/config"
	"instantcapture/internal/coords"
	"instantcapture/internal/editor"
	"instantcapture/internal/hotkey"
	"instantcapture/internal/nativeoverlay"
	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/workflow"
)

func TestCtrlVReadsClipboardImageAsynchronously(t *testing.T) {
	var encoded bytes.Buffer
	if err := png.Encode(&encoded, image.NewRGBA(image.Rect(0, 0, 7, 5))); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		ctx:              context.Background(),
		imageLoadResults: make(chan imageLoadResult, 1),
		readClipboardImage: func() ([]byte, error) {
			return encoded.Bytes(), nil
		},
	}
	if !u.handleShortcutKey(key.Event{Name: "V", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("Ctrl+V should be handled")
	}
	select {
	case result := <-u.imageLoadResults:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got, want := result.img.Bounds().Size(), image.Pt(7, 5); got != want {
			t.Fatalf("clipboard image size = %v, want %v", got, want)
		}
		if result.path != "" || result.displayName != "클립보드 이미지" {
			t.Fatalf("clipboard result metadata = path %q name %q", result.path, result.displayName)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for clipboard image decode")
	}
}

func TestLoadedImageRetainsSourcePathForSaving(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dropped.png")
	u := &windowUI{
		controller:          workflow.NewController(&uiFakeCapturer{}),
		imageLoadGeneration: 3,
	}
	u.handleImageLoadResult(imageLoadResult{
		generation:  3,
		path:        path,
		displayName: filepath.Base(path),
		img:         image.NewRGBA(image.Rect(0, 0, 9, 6)),
	}, nil)
	if u.sourceImagePath != path {
		t.Fatalf("source path = %q, want %q", u.sourceImagePath, path)
	}
	u.handleImageLoadResult(imageLoadResult{
		generation:  3,
		displayName: "클립보드 이미지",
		img:         image.NewRGBA(image.Rect(0, 0, 7, 5)),
	}, nil)
	if u.sourceImagePath != "" {
		t.Fatalf("clipboard image retained stale source path %q", u.sourceImagePath)
	}
}

func TestFileDropEventsToggleOverlayAndLoadImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dropped.png")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, image.NewRGBA(image.Rect(0, 0, 9, 6))); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{ctx: context.Background(), imageLoadResults: make(chan imageLoadResult, 1)}

	u.handleFileDropEvent(platformwindows.FileDropEvent{Kind: platformwindows.FileDropEnter})
	if !u.dropHover {
		t.Fatal("drag enter should show the drop overlay")
	}
	u.handleFileDropEvent(platformwindows.FileDropEvent{Kind: platformwindows.FileDropLeave})
	if u.dropHover {
		t.Fatal("drag leave should hide the drop overlay")
	}
	u.handleFileDropEvent(platformwindows.FileDropEvent{Kind: platformwindows.FileDropFiles, Paths: []string{path}})
	select {
	case result := <-u.imageLoadResults:
		if result.err != nil {
			t.Fatal(result.err)
		}
		if got, want := result.img.Bounds().Size(), image.Pt(9, 6); got != want {
			t.Fatalf("dropped image size = %v, want %v", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for dropped image decode")
	}
}

func TestFileDropDefersImageLoadWhileTextInputIsPending(t *testing.T) {
	path := filepath.Join(t.TempDir(), "dropped.png")
	u := &windowUI{textEditing: true, colorPickerOpen: true}
	u.handleFileDropEvent(platformwindows.FileDropEvent{Kind: platformwindows.FileDropFiles, Paths: []string{path}})
	if got := u.pendingImageLoadPath; got != path {
		t.Fatalf("pending image path = %q, want %q", got, path)
	}
	if u.imageLoadActive {
		t.Fatal("image load started before pending text input could be flushed")
	}
	if u.colorPickerOpen {
		t.Fatal("file drop should close the text color picker")
	}
}

func TestImageDropSetupDoesNotBlockWindowEventHandling(t *testing.T) {
	registrationStarted := make(chan struct{})
	releaseRegistration := make(chan struct{})
	u := &windowUI{
		fileDropEvents:        make(chan platformwindows.FileDropEvent, 1),
		imageDropSetupResults: make(chan imageDropSetupResult, 1),
		registerImageDropTarget: func(*app.Window, uintptr, chan<- platformwindows.FileDropEvent) (io.Closer, error) {
			close(registrationStarted)
			<-releaseRegistration
			return nil, nil
		},
	}

	u.beginImageDropSetup(nil, 1)
	select {
	case <-registrationStarted:
	case <-time.After(time.Second):
		t.Fatal("drop registration did not start")
	}
	select {
	case <-u.imageDropSetupResults:
		t.Fatal("registration unexpectedly completed while blocked")
	default:
	}
	close(releaseRegistration)
	select {
	case <-u.imageDropSetupResults:
	case <-time.After(time.Second):
		t.Fatal("drop registration result was not delivered")
	}
}

type uiFakeCapturer struct {
	displays []image.Rectangle
	img      image.Image
}

func TestWindowUILayoutFitsMinimumWindow(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 900, 620)},
		img:      image.NewRGBA(image.Rect(0, 0, 900, 620)),
	})
	u := &windowUI{
		controller: controller,
		theme:      newTheme(),
		status:     "캡처할 준비가 되었습니다.",
		appConfig: config.Config{
			Hotkey:      "Ctrl+Shift+C",
			CaptureMode: "Region",
		},
		canvasZoom: 1,
	}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(900, 620)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}

	dims := u.layout(gtx, nil)

	if got, want := dims.Size, image.Pt(900, 620); got != want {
		t.Fatalf("layout size = %v, want %v", got, want)
	}
}

func TestWindowUIToolbarAdaptsBetweenOneAndTwoRows(t *testing.T) {
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		theme:      newTheme(),
		appConfig:  config.Config{Hotkey: "Ctrl+Shift+C", CaptureMode: "Region"},
		canvasZoom: 1,
	}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(1100, 52)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}

	if got := u.layoutToolbar(gtx).Size.Y; got != 56 {
		t.Fatalf("wide toolbar height = %d, want 56", got)
	}

	ops.Reset()
	gtx.Constraints = layout.Exact(image.Pt(900, 92))
	if got := u.layoutToolbar(gtx).Size.Y; got != 108 {
		t.Fatalf("narrow toolbar height = %d, want 108", got)
	}

	ops.Reset()
	gtx.Constraints = layout.Exact(image.Pt(1100, 36))
	if got := u.layoutStatusBar(gtx).Size.Y; got != 36 {
		t.Fatalf("status bar height = %d, want 36", got)
	}
}

func TestMarkupToolShortcutLabelsFollowToolbarOrder(t *testing.T) {
	for _, tc := range []struct {
		tool editor.Tool
		want string
	}{
		{tool: editor.ToolRectangle, want: "1"},
		{tool: editor.ToolTriangle, want: "1"},
		{tool: editor.ToolCircle, want: "1"},
		{tool: editor.ToolPen, want: "2"},
		{tool: editor.ToolArrow, want: "3"},
		{tool: editor.ToolMosaic, want: "4"},
		{tool: editor.ToolHighlight, want: "5"},
		{tool: editor.ToolText, want: "6"},
	} {
		if got := toolShortcutLabel(tc.tool); got != tc.want {
			t.Fatalf("toolShortcutLabel(%v) = %q, want %q", tc.tool, got, tc.want)
		}
	}
}

func TestToolbarAndEmptyCardGrowWithSystemTextScale(t *testing.T) {
	normal := unit.Metric{PxPerDp: 1, PxPerSp: 1}
	largeText := unit.Metric{PxPerDp: 1, PxPerSp: 2}
	if toolbarHeight(largeText, true) <= toolbarHeight(normal, true) {
		t.Fatal("single-row toolbar did not grow with text scale")
	}
	if toolbarHeight(largeText, false) <= toolbarHeight(normal, false) {
		t.Fatal("two-row toolbar did not grow with text scale")
	}
	if statusBarHeight(largeText) <= statusBarHeight(normal) {
		t.Fatal("status bar did not grow with text scale")
	}
	if emptyCanvasCardHeight(largeText) <= emptyCanvasCardHeight(normal) {
		t.Fatal("empty card did not grow with text scale")
	}

	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		theme:      newTheme(),
		appConfig:  config.Config{Hotkey: "Ctrl+Shift+C", CaptureMode: "Region"},
		canvasZoom: 1,
	}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Constraints{Max: image.Pt(1100, 240)},
		Metric:      largeText,
		Now:         time.Now(),
		Ops:         &ops,
	}
	if got, want := u.layoutToolbar(gtx).Size.Y, toolbarHeight(largeText, true); got != want {
		t.Fatalf("large-text toolbar height = %d, want %d", got, want)
	}

	ops.Reset()
	gtx.Constraints = layout.Constraints{Max: image.Pt(900, 500)}
	dims := u.layoutEmptyCanvas(gtx, image.Pt(900, 500))
	if dims.Size.X > 900 || dims.Size.Y > 500 {
		t.Fatalf("large-text empty canvas overflowed: %v", dims.Size)
	}
}

func TestTextEditingKeepsWideToolbarOnOneRow(t *testing.T) {
	u := &windowUI{
		controller:       workflow.NewController(&uiFakeCapturer{}),
		theme:            newTheme(),
		textEditing:      true,
		textEditingStyle: editor.DefaultTextStyle(),
	}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(1100, 56)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}
	if got := u.layoutToolbar(gtx).Size.Y; got != 56 {
		t.Fatalf("text editing toolbar height = %d, want 56", got)
	}
}

func TestCompactEmptyCanvasStaysWithinAvailableSpace(t *testing.T) {
	u := &windowUI{
		theme:     newTheme(),
		appConfig: config.Config{Hotkey: "Ctrl+Shift+C"},
	}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Constraints{Max: image.Pt(900, 200)},
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}

	dims := u.layoutEmptyCanvas(gtx, image.Pt(900, 200))

	if dims.Size.X > 900 || dims.Size.Y > 200 {
		t.Fatalf("compact empty canvas overflowed: %v", dims.Size)
	}
}

func TestHighlighterReselectionDoesNotChangeMode(t *testing.T) {
	u := &windowUI{
		controller:        workflow.NewController(&uiFakeCapturer{}),
		activeTool:        editor.ToolHighlight,
		highlightStraight: true,
	}
	u.highlightButton.Click()
	var ops op.Ops
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(), Ops: &ops}

	u.handleButtons(gtx, nil)

	if !u.highlightStraight {
		t.Fatal("reselecting the highlighter should not change its mode")
	}

	u.highlightModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.highlightStraight {
		t.Fatal("the explicit highlighter mode control should change its mode")
	}
}

func TestPenAndMosaicUseExplicitModeToggles(t *testing.T) {
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		activeTool: editor.ToolPen,
	}
	var ops op.Ops
	gtx := layout.Context{Metric: unit.Metric{PxPerDp: 1, PxPerSp: 1}, Now: time.Now(), Ops: &ops}

	u.penButton.Click()
	u.handleButtons(gtx, nil)
	if u.penMode != editor.PenModeStraight {
		t.Fatal("reselecting the pen should not change its mode")
	}
	u.penModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.penMode != editor.PenModeFreehand {
		t.Fatal("pen mode button should switch to freehand")
	}
	u.penModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.penMode != editor.PenModeRectangle {
		t.Fatal("pen mode button should switch to rectangle")
	}
	u.penModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.penMode != editor.PenModeStraight {
		t.Fatal("pen mode button should cycle back to straight")
	}

	u.mosaicButton.Click()
	u.handleButtons(gtx, nil)
	if u.activeTool != editor.ToolMosaic || u.mosaicMode != editor.MosaicModeStraight {
		t.Fatalf("mosaic selection changed mode: tool=%v mode=%v", u.activeTool, u.mosaicMode)
	}
	u.mosaicModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.mosaicMode != editor.MosaicModeFreehand {
		t.Fatal("mosaic mode button should switch to freehand")
	}
	u.mosaicModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.mosaicMode != editor.MosaicModeRectangle {
		t.Fatal("mosaic mode button should switch to rectangle")
	}
	u.mosaicModeButton.Click()
	u.handleButtons(gtx, nil)
	if u.mosaicMode != editor.MosaicModeStraight {
		t.Fatal("mosaic mode button should cycle back to straight")
	}
}

func TestKoreanFontPathsLoadOnlyRequestedFamily(t *testing.T) {
	for _, test := range []struct {
		name   string
		family editor.TextFontFamily
		bold   bool
		want   string
	}{
		{name: "default", family: editor.TextFontMalgun, want: "malgun.ttf"},
		{name: "malgun bold", family: editor.TextFontMalgun, bold: true, want: "malgun.ttf,malgunbd.ttf"},
		{name: "gulim", family: editor.TextFontGulim, want: "malgun.ttf,gulim.ttc"},
		{name: "dotum", family: editor.TextFontDotum, want: "malgun.ttf,gulim.ttc"},
		{name: "batang", family: editor.TextFontBatang, want: "malgun.ttf,batang.ttc"},
		{name: "gungsuh", family: editor.TextFontGungsuh, want: "malgun.ttf,batang.ttc"},
	} {
		t.Run(test.name, func(t *testing.T) {
			paths := koreanFontPaths(test.family, test.bold)
			names := make([]string, len(paths))
			for i, path := range paths {
				names[i] = filepath.Base(path)
			}
			if got := strings.Join(names, ","); got != test.want {
				t.Fatalf("font paths = %q, want %q", got, test.want)
			}
		})
	}
}

func TestSettingsModalCardStaysInsideMinimumWindow(t *testing.T) {
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(900, 620)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
	}
	card := settingsModalCardSize(gtx)
	if card.X > 852 || card.Y > 572 || card.X <= 0 || card.Y <= 0 {
		t.Fatalf("settings card escaped viewport: %v", card)
	}
}

func TestSettingsModalCloseButtonClosesModal(t *testing.T) {
	u := &windowUI{showSettings: true}
	u.settingsCloseButton.Click()
	u.handleButtons(layout.Context{Ops: new(op.Ops)}, nil)
	if u.showSettings {
		t.Fatal("settings close button should close the modal")
	}
}

func TestChooseSaveDirUsesOwnedFolderPickerWithoutBlockingUI(t *testing.T) {
	oldDir := t.TempDir()
	newDir := t.TempDir()
	saved := 0
	called := make(chan [2]string, 1)
	release := make(chan struct{})
	u := &windowUI{
		appConfig: config.Config{SaveDir: oldDir},
		pickFolderPath: func(initial, ownerTitle string) (string, bool, error) {
			called <- [2]string{initial, ownerTitle}
			<-release
			return newDir, true, nil
		},
		saveConfig: func(cfg config.Config) error {
			saved++
			if cfg.SaveDir != newDir {
				t.Fatalf("saved dir = %q", cfg.SaveDir)
			}
			return nil
		},
	}
	u.chooseSaveDir()
	if !u.saveDirBrowseActive || u.appConfig.SaveDir != oldDir || saved != 0 {
		t.Fatalf("picker blocked or changed state early: active=%v dir=%q saves=%d", u.saveDirBrowseActive, u.appConfig.SaveDir, saved)
	}
	args := <-called
	if args != [2]string{oldDir, windowTitle} {
		t.Fatalf("picker args = %q", args)
	}
	close(release)
	result := <-u.saveDirBrowseResults
	u.handleSaveDirBrowseResult(result)
	if u.appConfig.SaveDir != newDir || saved != 1 {
		t.Fatalf("save dir=%q saves=%d", u.appConfig.SaveDir, saved)
	}
}

func TestFolderPickerPreventsFocusLossHide(t *testing.T) {
	u := &windowUI{
		appConfig:           config.Config{HideOnFocusLoss: true},
		trayAvailable:       true,
		saveDirBrowseActive: true,
	}
	if uShouldHideForFocusLoss(u) {
		t.Fatal("folder picker owner must stay visible")
	}
}

func TestHotkeyActionTogglePersistsWindowOpeningChoice(t *testing.T) {
	saved := 0
	u := &windowUI{saveConfig: func(cfg config.Config) error {
		saved++
		if !cfg.HotkeyOpensWindow {
			t.Fatal("window-opening choice was not persisted")
		}
		return nil
	}}
	u.hotkeyActionButton.Click()
	u.handleButtons(layout.Context{Ops: new(op.Ops)}, nil)
	if !u.appConfig.HotkeyOpensWindow || saved != 1 {
		t.Fatalf("opensWindow=%v saves=%d", u.appConfig.HotkeyOpensWindow, saved)
	}
	if got := u.hotkeyActionLabel(); got != "단축키 동작: 프로그램 열기" {
		t.Fatalf("unexpected action label: %q", got)
	}
}

func TestGlobalHotkeyWindowActionRestoresInsteadOfCapturing(t *testing.T) {
	restored := 0
	u := &windowUI{
		appConfig: config.Config{HotkeyOpensWindow: true},
		restoreWindow: func(title string) error {
			restored++
			if title != windowTitle {
				t.Fatalf("restore title = %q", title)
			}
			return nil
		},
	}
	u.handleGlobalHotkey(nil)
	if restored != 1 || u.captureActive {
		t.Fatalf("restores=%d captureActive=%v", restored, u.captureActive)
	}
	if !strings.Contains(u.status, "프로그램 화면") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestGlobalHotkeyWindowActionDoesNotInterruptCapture(t *testing.T) {
	restored := false
	u := &windowUI{
		appConfig:     config.Config{HotkeyOpensWindow: true},
		overlayActive: true,
		restoreWindow: func(string) error {
			restored = true
			return nil
		},
	}
	u.handleGlobalHotkey(nil)
	if restored {
		t.Fatal("active capture overlay should not be interrupted")
	}
}

func TestGlobalHotkeyDoesNotFireWhileAssigningShortcut(t *testing.T) {
	restored := false
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{HotkeyOpensWindow: true},
		restoreWindow: func(string) error {
			restored = true
			return nil
		},
	}
	u.handleGlobalHotkey(nil)
	if restored || u.captureActive {
		t.Fatal("existing global shortcut fired while assigning a shortcut")
	}
}

func TestCaptureSnapshotWaitsUntilWindowIsHidden(t *testing.T) {
	hidden := false
	flushed := false
	captured := false
	u := &windowUI{
		hideWindow: func(title string) error {
			if title != windowTitle {
				t.Fatalf("hide title = %q", title)
			}
			hidden = true
			return nil
		},
		windowVisible: func(string) bool { return !hidden },
		flushDesktop: func() {
			if !hidden {
				t.Fatal("desktop composition flushed before the window was hidden")
			}
			flushed = true
		},
		captureSnapshot: func() (workflow.CaptureSnapshot, error) {
			if !hidden {
				t.Fatal("captured before the application window was hidden")
			}
			if !flushed {
				t.Fatal("captured before desktop composition was flushed")
			}
			captured = true
			return workflow.CaptureSnapshot{}, nil
		},
	}
	if _, err := u.captureSnapshotAfterHiding(); err != nil {
		t.Fatal(err)
	}
	if !captured {
		t.Fatal("screen capture was not started")
	}
}

func TestSavedRegionImageRectPreservesNegativeScreenCoordinates(t *testing.T) {
	virtualScreen := image.Rect(-1920, -200, 1920, 1080)
	screenRect := image.Rect(-1800, -100, -1000, 500)
	displays := []image.Rectangle{virtualScreen}
	got, ok := savedRegionImageRect(screenRect, virtualScreen, displays)
	if !ok || got != image.Rect(120, 100, 920, 700) {
		t.Fatalf("saved image rect = %v, ok=%v", got, ok)
	}
	if _, ok := savedRegionImageRect(image.Rect(-2000, 0, -1000, 500), virtualScreen, displays); ok {
		t.Fatal("partially unavailable saved region should be rejected")
	}
}

func TestSavedRegionRejectsGapBetweenDisplays(t *testing.T) {
	virtualScreen := image.Rect(0, 0, 300, 200)
	displays := []image.Rectangle{image.Rect(0, 0, 100, 100), image.Rect(200, 100, 300, 200)}
	if _, ok := savedRegionImageRect(image.Rect(50, 50, 250, 150), virtualScreen, displays); ok {
		t.Fatal("region crossing an uncovered monitor gap should be rejected")
	}
	if _, ok := savedRegionImageRect(image.Rect(10, 10, 90, 90), virtualScreen, displays); !ok {
		t.Fatal("region fully covered by one display should be accepted")
	}
}

func TestRememberRegionStoresAbsoluteCoordinatesAndEnablesIt(t *testing.T) {
	controller := workflow.NewController(nil)
	snapshot := workflow.CaptureSnapshot{
		VirtualScreen: image.Rect(-1200, 0, 800, 1000),
		Image:         image.NewRGBA(image.Rect(0, 0, 2000, 1000)),
	}
	if err := controller.BeginCaptureFromSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	saves := 0
	restores := 0
	u := &windowUI{
		controller:    controller,
		overlayActive: true,
		saveConfig: func(config.Config) error {
			saves++
			return nil
		},
		restoreWindow: func(string) error {
			restores++
			return nil
		},
		freeOSMemory: func() {},
	}
	u.handleOverlayResult(overlayResult{
		mode:           "Region",
		rememberRegion: true,
		selection:      nativeoverlay.Selection{Rect: image.Rect(100, 50, 900, 650)},
		ok:             true,
	}, nil)

	want := image.Rect(-1100, 50, -300, 650)
	if len(u.appConfig.SavedRegions) != 1 {
		t.Fatalf("saved region count = %d", len(u.appConfig.SavedRegions))
	}
	preset := u.appConfig.SavedRegions[0]
	if got := preset.Region.Rect(); got != want || !u.appConfig.SavedRegionEnabled || u.appConfig.ActiveSavedRegionID != preset.ID {
		t.Fatalf("saved region = %v enabled=%v active=%q, want %v", got, u.appConfig.SavedRegionEnabled, u.appConfig.ActiveSavedRegionID, want)
	}
	if saves != 1 || restores != 1 || controller.Mode != workflow.ModeIdle {
		t.Fatalf("saves=%d restores=%d mode=%v", saves, restores, controller.Mode)
	}
}

func TestSavedRegionCaptureWithoutEditingCopiesNotifiesAndStaysHidden(t *testing.T) {
	controller := workflow.NewController(nil)
	want := image.Rect(-900, 120, -300, 520)
	var copiedRect image.Rectangle
	notifications := 0
	restores := 0
	u := &windowUI{
		controller: controller,
		appConfig: config.Config{
			EditAfterCapture:    false,
			SavedRegionEnabled:  true,
			ActiveSavedRegionID: "region-1",
			SavedRegions: []config.SavedRegionPreset{{
				ID: "region-1", Name: "영역 1", Region: config.ScreenRegionFromRect(want),
			}},
		},
		copyPNG: func() error {
			copiedRect = controller.SelectedScreen
			return nil
		},
		notify: func(title, message string) error {
			notifications++
			if title != "캡처 완료" || !strings.Contains(message, "클립보드") {
				t.Fatalf("notification = %q %q", title, message)
			}
			return nil
		},
		restoreWindow: func(string) error {
			restores++
			return nil
		},
		freeOSMemory: func() {},
	}
	u.handleCaptureStartResult(captureStartResult{
		mode:           "Region",
		useSavedRegion: true,
		snapshot: workflow.CaptureSnapshot{
			VirtualScreen: image.Rect(-1200, 0, 800, 1000),
			Displays:      []image.Rectangle{image.Rect(-1200, 0, 800, 1000)},
			Image:         image.NewRGBA(image.Rect(0, 0, 2000, 1000)),
		},
	}, nil)

	if copiedRect != want {
		t.Fatalf("copied screen rect = %v, want %v", copiedRect, want)
	}
	if notifications != 1 || restores != 0 || !u.trayHidden {
		t.Fatalf("notifications=%d restores=%d hidden=%v", notifications, restores, u.trayHidden)
	}
	if controller.Mode != workflow.ModeIdle || controller.CapturedImage != nil {
		t.Fatalf("capture memory was retained: mode=%v image=%T", controller.Mode, controller.CapturedImage)
	}
}

func TestNextSavedRegionNameReusesFirstAvailableNumber(t *testing.T) {
	regions := []config.SavedRegionPreset{{Name: "영역 1"}, {Name: "영역 3"}}
	if got := nextSavedRegionName(regions); got != "영역 2" {
		t.Fatalf("next region name = %q", got)
	}
}

func TestActiveSavedRegionUsesSelectedPreset(t *testing.T) {
	u := &windowUI{appConfig: config.Config{
		SavedRegionEnabled:  true,
		ActiveSavedRegionID: "b",
		SavedRegions: []config.SavedRegionPreset{
			{ID: "a", Name: "영역 1", Region: config.ScreenRegionFromRect(image.Rect(0, 0, 10, 10))},
			{ID: "b", Name: "영역 2", Region: config.ScreenRegionFromRect(image.Rect(20, 20, 40, 40))},
		},
	}}
	preset, ok := u.activeSavedRegion()
	if !ok || preset.ID != "b" || preset.Region.Rect() != image.Rect(20, 20, 40, 40) {
		t.Fatalf("active preset = %#v, ok=%v", preset, ok)
	}
	u.appConfig.SavedRegionEnabled = false
	if _, ok := u.activeSavedRegion(); ok {
		t.Fatal("disabled saved regions should not return an active preset")
	}
}

func TestSavedRegionButtonsSelectAndDeleteActivePreset(t *testing.T) {
	u := &windowUI{
		appConfig: config.Config{
			SavedRegionEnabled:  true,
			ActiveSavedRegionID: "a",
			SavedRegions: []config.SavedRegionPreset{
				{ID: "a", Name: "영역 1", Region: config.ScreenRegionFromRect(image.Rect(0, 0, 10, 10))},
				{ID: "b", Name: "영역 2", Region: config.ScreenRegionFromRect(image.Rect(20, 20, 40, 40))},
			},
		},
		saveConfig: func(config.Config) error { return nil },
	}
	u.ensureSavedRegionButtons()
	u.savedRegionSelectButtons[1].Click()
	u.handleButtons(layout.Context{Ops: new(op.Ops)}, nil)
	if u.appConfig.ActiveSavedRegionID != "b" {
		t.Fatalf("active preset = %q, want b", u.appConfig.ActiveSavedRegionID)
	}

	u.savedRegionDeleteButtons[1].Click()
	u.handleButtons(layout.Context{Ops: new(op.Ops)}, nil)
	if len(u.appConfig.SavedRegions) != 1 || u.appConfig.SavedRegions[0].ID != "a" || u.appConfig.ActiveSavedRegionID != "a" {
		t.Fatalf("presets after delete = %#v active=%q", u.appConfig.SavedRegions, u.appConfig.ActiveSavedRegionID)
	}
}

func TestSavedRegionDeleteRollsBackWhenSaveFails(t *testing.T) {
	u := &windowUI{
		appConfig: config.Config{
			SavedRegionEnabled:  true,
			ActiveSavedRegionID: "a",
			SavedRegions: []config.SavedRegionPreset{{
				ID: "a", Name: "영역 1", Region: config.ScreenRegionFromRect(image.Rect(0, 0, 10, 10)),
			}},
		},
		saveConfig: func(config.Config) error { return errors.New("disk full") },
	}
	u.ensureSavedRegionButtons()
	u.savedRegionDeleteButtons[0].Click()
	u.handleButtons(layout.Context{Ops: new(op.Ops)}, nil)
	if len(u.appConfig.SavedRegions) != 1 || u.appConfig.ActiveSavedRegionID != "a" || !u.appConfig.SavedRegionEnabled {
		t.Fatalf("failed delete changed config: %#v", u.appConfig)
	}
}

func TestCaptureWithoutEditingRestoresWhenNotificationFails(t *testing.T) {
	controller := workflow.NewController(nil)
	if err := controller.CommitImportedImage(image.NewRGBA(image.Rect(0, 0, 80, 60))); err != nil {
		t.Fatal(err)
	}
	restores := 0
	u := &windowUI{
		controller: controller,
		appConfig:  config.Config{EditAfterCapture: false},
		copyPNG:    func() error { return nil },
		notify:     func(string, string) error { return errors.New("tray unavailable") },
		restoreWindow: func(string) error {
			restores++
			return nil
		},
	}
	u.completeCapturedImage(nil, "80x60 캡처")
	if restores != 1 || u.trayHidden || controller.Mode != workflow.ModeEditing || controller.CapturedImage == nil {
		t.Fatalf("restores=%d hidden=%v mode=%v image=%T", restores, u.trayHidden, controller.Mode, controller.CapturedImage)
	}
}

func TestCaptureWithoutEditingRestoresWhenNoOutputSucceeds(t *testing.T) {
	controller := workflow.NewController(nil)
	if err := controller.CommitImportedImage(image.NewRGBA(image.Rect(0, 0, 80, 60))); err != nil {
		t.Fatal(err)
	}
	restores := 0
	u := &windowUI{
		controller: controller,
		appConfig:  config.Config{EditAfterCapture: false},
		copyPNG:    func() error { return errors.New("clipboard busy") },
		notify:     func(string, string) error { return nil },
		restoreWindow: func(string) error {
			restores++
			return nil
		},
	}
	u.completeCapturedImage(nil, "80x60 캡처")
	if restores != 1 || controller.Mode != workflow.ModeEditing || controller.CapturedImage == nil {
		t.Fatalf("restores=%d mode=%v image=%T", restores, controller.Mode, controller.CapturedImage)
	}
}

func TestMosaicSliderRanges(t *testing.T) {
	if got := mosaicSliderToBlockSize(0); got != 4 {
		t.Fatalf("minimum block size = %d, want 4", got)
	}
	if got := mosaicSliderToBlockSize(1); got != 40 {
		t.Fatalf("maximum block size = %d, want 40", got)
	}
	if got := mosaicSliderToWidth(0); got != 8 {
		t.Fatalf("minimum mosaic width = %d, want 8", got)
	}
	if got := mosaicSliderToWidth(1); got != 80 {
		t.Fatalf("maximum mosaic width = %d, want 80", got)
	}
}

func TestCurrentToolCursorReflectsSelectedToolStyle(t *testing.T) {
	controller := &workflow.Controller{
		Mode:             workflow.ModeEditing,
		AnnotationStyle:  editor.StrokeStyle{ColorRGBA: [4]uint8{12, 34, 56, 255}, Width: 7},
		HighlighterStyle: editor.StrokeStyle{ColorRGBA: [4]uint8{250, 204, 21, 90}, Width: 19},
		MosaicStyle:      editor.StrokeStyle{Width: 42},
	}
	for _, test := range []struct {
		tool      editor.Tool
		wantWidth float32
		wantColor color.NRGBA
	}{
		{tool: editor.ToolPen, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
		{tool: editor.ToolLine, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
		{tool: editor.ToolRectangle, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
		{tool: editor.ToolTriangle, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
		{tool: editor.ToolCircle, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
		{tool: editor.ToolArrow, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
		{tool: editor.ToolHighlight, wantWidth: 19, wantColor: color.NRGBA{R: 250, G: 204, B: 21, A: 90}},
		{tool: editor.ToolMosaic, wantWidth: 42, wantColor: color.NRGBA{R: 129, G: 140, B: 248, A: 255}},
		{tool: editor.ToolText, wantWidth: 7, wantColor: color.NRGBA{R: 12, G: 34, B: 56, A: 255}},
	} {
		t.Run(string(test.tool), func(t *testing.T) {
			u := &windowUI{
				controller:      controller,
				activeTool:      test.tool,
				canvasPointer:   editor.Point{X: 31, Y: 47},
				canvasPointerIn: true,
			}
			spec, ok := u.currentToolCursor()
			if !ok {
				t.Fatal("expected a cursor preview")
			}
			if spec.Tool != test.tool || spec.Center != u.canvasPointer || spec.Width != test.wantWidth || spec.Color != test.wantColor {
				t.Fatalf("cursor spec = %#v", spec)
			}
		})
	}
}

func TestCurrentToolCursorHidesOutsideImageAndDuringCrop(t *testing.T) {
	u := &windowUI{
		controller: &workflow.Controller{
			Mode:            workflow.ModeEditing,
			AnnotationStyle: editor.StrokeStyle{ColorRGBA: [4]uint8{255, 0, 0, 255}, Width: 4},
		},
		activeTool: editor.ToolPen,
	}
	if _, ok := u.currentToolCursor(); ok {
		t.Fatal("cursor should be hidden outside the image")
	}
	u.canvasPointerIn = true
	u.cropHoverHandle = cropHandleLeft
	if _, ok := u.currentToolCursor(); ok {
		t.Fatal("cursor should yield to a crop handle")
	}
	u.cropHoverHandle = cropHandleNone
	u.activeTool = editor.ToolText
	u.textEditing = true
	if _, ok := u.currentToolCursor(); ok {
		t.Fatal("text tool cursor preview should be hidden while editing text")
	}
}

func TestMosaicHoverCommandUsesCurrentPointerAndSettings(t *testing.T) {
	u := &windowUI{
		controller: &workflow.Controller{
			Mode:            workflow.ModeEditing,
			MosaicStyle:     editor.StrokeStyle{Width: 36},
			MosaicBlockSize: 14,
		},
		activeTool:      editor.ToolMosaic,
		canvasPointer:   editor.Point{X: 52, Y: 28},
		canvasPointerIn: true,
	}
	command, ok := u.mosaicHoverCommand()
	if !ok {
		t.Fatal("expected a mosaic hover command")
	}
	if command.Tool != editor.ToolMosaic || command.Style.Width != 36 || command.MosaicBlockSize != 14 {
		t.Fatalf("mosaic hover command = %#v", command)
	}
	if len(command.Points) != 2 || command.Points[0] != u.canvasPointer || command.Points[1] != u.canvasPointer {
		t.Fatalf("mosaic hover points = %#v", command.Points)
	}
	u.dragging = true
	if _, ok := u.mosaicHoverCommand(); ok {
		t.Fatal("drag preview should replace the hover command")
	}
}

func TestMosaicHoverPreviewRendersPixelsAndReusesUnchangedFrame(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 32, 32))
	for y := 0; y < 32; y++ {
		for x := 0; x < 32; x++ {
			base.Set(x, y, color.NRGBA{R: uint8(x * 7), G: uint8(y * 7), B: uint8((x + y) * 3), A: 255})
		}
	}
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{base.Bounds()},
		img:      base,
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(base.Bounds()); err != nil {
		t.Fatal(err)
	}
	controller.MosaicStyle = editor.StrokeStyle{Width: 16}
	controller.MosaicBlockSize = 8
	textStyle := editor.DefaultTextStyle()
	textID, err := controller.AddTextAnnotation("A", editor.NewRect(editor.Point{X: 1, Y: 1}, editor.Point{X: 12, Y: 12}), textStyle)
	if err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		controller:      controller,
		activeTool:      editor.ToolMosaic,
		canvasPointer:   editor.Point{X: 16, Y: 16},
		canvasPointerIn: true,
	}
	command, ok := u.mosaicHoverCommand()
	if !ok {
		t.Fatal("expected hover command")
	}
	first, err := u.previewMosaicHover(command)
	if err != nil {
		t.Fatal(err)
	}
	if got, original := first.RGBAAt(16, 16), base.RGBAAt(16, 16); got == original {
		t.Fatalf("center pixel was not mosaicked: %#v", got)
	}
	if got, original := first.RGBAAt(0, 0), base.RGBAAt(0, 0); got != original {
		t.Fatalf("outside pixel changed: got %#v want %#v", got, original)
	}
	second, err := u.previewMosaicHover(command)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatal("unchanged hover preview should reuse the rendered image")
	}
	if changed, err := controller.UpdateTextAnnotation(textID, "B", editor.NewRect(editor.Point{X: 1, Y: 1}, editor.Point{X: 12, Y: 12}), textStyle); err != nil || !changed {
		t.Fatalf("update text annotation: changed=%v err=%v", changed, err)
	}
	third, err := u.previewMosaicHover(command)
	if err != nil {
		t.Fatal(err)
	}
	if third == first {
		t.Fatal("editing an annotation should invalidate the hover preview")
	}

	u.canvasPointer = editor.Point{X: 8, Y: 8}
	command, _ = u.mosaicHoverCommand()
	fourth, err := u.previewMosaicHover(command)
	if err != nil {
		t.Fatal(err)
	}
	if fourth == third {
		t.Fatal("moving the pointer should invalidate the hover preview")
	}
}

func TestReleaseTransientImageCachesPreservesEditingSession(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 40, 30))
	controller := workflow.NewController(nil)
	if err := controller.CommitImportedImage(base); err != nil {
		t.Fatal(err)
	}
	if err := controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(editor.Point{X: 2, Y: 3}, editor.Point{X: 20, Y: 18})); err != nil {
		t.Fatal(err)
	}
	preview, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		controller:       controller,
		mosaicHoverImage: image.NewRGBA(base.Bounds()),
		canvasImage:      preview,
		textPreview: textPreviewCache{
			base:  image.NewRGBA(base.Bounds()),
			layer: image.NewRGBA(image.Rect(0, 0, 10, 10)),
		},
	}

	u.releaseTransientImageCaches()

	if controller.CapturedImage != base || len(controller.Editor.Annotations) != 1 {
		t.Fatal("cache cleanup should preserve the captured image and annotations")
	}
	if u.mosaicHoverImage != nil || u.canvasImage != nil || u.textPreview.base != nil || u.textPreview.layer != nil {
		t.Fatal("transient image caches were not released")
	}
	refreshed, err := controller.PreviewImage()
	if err != nil {
		t.Fatal(err)
	}
	if refreshed == preview {
		t.Fatal("committed preview cache was not released")
	}
}

func TestScheduleOSMemoryReleaseInvokesConfiguredCleanup(t *testing.T) {
	called := make(chan struct{})
	u := &windowUI{freeOSMemory: func() { close(called) }}
	u.scheduleOSMemoryRelease(0)
	select {
	case <-called:
	case <-time.After(time.Second):
		t.Fatal("OS memory cleanup was not invoked")
	}
}

func TestLayoutCanvasRendersToolCursorsAndMosaicHoverEndToEnd(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 120, 80))
	for y := 0; y < 80; y++ {
		for x := 0; x < 120; x++ {
			base.Set(x, y, color.NRGBA{R: uint8(x * 2), G: uint8(y * 3), B: uint8((x + y) % 255), A: 255})
		}
	}
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{base.Bounds()},
		img:      base,
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(base.Bounds()); err != nil {
		t.Fatal(err)
	}
	controller.AnnotationStyle = editor.StrokeStyle{ColorRGBA: [4]uint8{255, 64, 64, 255}, Width: 7}
	controller.HighlighterStyle = editor.StrokeStyle{ColorRGBA: [4]uint8{250, 204, 21, 90}, Width: 18}
	controller.MosaicStyle = editor.StrokeStyle{Width: 28}
	controller.MosaicBlockSize = 10

	u := &windowUI{
		controller:        controller,
		theme:             newTheme(),
		canvasZoom:        1,
		canvasPointerOver: true,
		canvasPointerView: editor.Point{X: 160, Y: 120},
	}
	for _, tool := range []editor.Tool{
		editor.ToolPen,
		editor.ToolLine,
		editor.ToolRectangle,
		editor.ToolTriangle,
		editor.ToolCircle,
		editor.ToolArrow,
		editor.ToolHighlight,
		editor.ToolMosaic,
		editor.ToolText,
	} {
		t.Run(string(tool), func(t *testing.T) {
			u.activeTool = tool
			var ops op.Ops
			gtx := layout.Context{
				Constraints: layout.Exact(image.Pt(320, 240)),
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
				Now:         time.Now(),
				Ops:         &ops,
			}
			if got := u.layoutCanvas(gtx, nil).Size; got != image.Pt(320, 240) {
				t.Fatalf("canvas size = %v", got)
			}
			if _, ok := u.currentToolCursor(); !ok {
				t.Fatal("tool cursor was not active after canvas layout")
			}
			if tool == editor.ToolMosaic && u.mosaicHoverImage == nil {
				t.Fatal("mosaic hover image was not rendered")
			}
		})
	}

	if u.mosaicHoverImage != nil {
		t.Fatal("leaving the mosaic tool should release its hover image")
	}
	u.activeTool = editor.ToolMosaic
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(320, 240)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}
	u.layoutCanvas(gtx, nil)
	first := u.mosaicHoverImage
	if first == nil {
		t.Fatal("mosaic hover image was not recreated")
	}
	ops.Reset()
	u.layoutCanvas(gtx, nil)
	if u.mosaicHoverImage != first {
		t.Fatal("unchanged end-to-end mosaic hover should reuse its cached frame")
	}
}

func TestSyncCanvasPointerReprojectsAfterPan(t *testing.T) {
	u := &windowUI{
		controller:        &workflow.Controller{Mode: workflow.ModeEditing},
		canvasPointerOver: true,
		canvasPointerView: editor.Point{X: 100, Y: 80},
	}
	bounds := image.Rect(0, 0, 200, 160)
	u.syncCanvasPointer(coords.Viewport{Source: bounds, View: bounds, Scale: 1}, bounds)
	if got := u.canvasPointer; got != (editor.Point{X: 100, Y: 80}) {
		t.Fatalf("initial pointer = %#v", got)
	}
	pannedView := image.Rect(40, 20, 240, 180)
	u.syncCanvasPointer(coords.Viewport{Source: bounds, View: pannedView, Scale: 1}, bounds)
	if got := u.canvasPointer; got != (editor.Point{X: 60, Y: 60}) {
		t.Fatalf("reprojected pointer = %#v, want 60,60", got)
	}
}

func TestSettingsPreviewStacksForNarrowOrLargeText(t *testing.T) {
	normal := layout.Context{
		Constraints: layout.Constraints{Max: image.Pt(816, 200)},
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
	}
	if settingsPreviewStacked(normal) {
		t.Fatal("minimum-window settings should keep the preview beside controls")
	}
	normal.Constraints.Max.X = 600
	if !settingsPreviewStacked(normal) {
		t.Fatal("narrow settings should stack the preview")
	}
	normal.Constraints.Max.X = 816
	normal.Metric.PxPerSp = 2
	if !settingsPreviewStacked(normal) {
		t.Fatal("large text should stack the preview")
	}
}

func TestSettingsControlsUpdateBeforeSidePreview(t *testing.T) {
	u := &windowUI{}
	order := make([]string, 0, 2)
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Constraints{Max: image.Pt(816, 160)},
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}
	u.layoutSettingsWithPreview(gtx,
		func(layout.Context) layout.Dimensions {
			order = append(order, "controls")
			return layout.Dimensions{Size: image.Pt(600, 80)}
		},
		func(layout.Context) layout.Dimensions {
			order = append(order, "preview")
			return layout.Dimensions{Size: image.Pt(160, 112)}
		},
	)
	if got := strings.Join(order, ","); got != "controls,preview" {
		t.Fatalf("layout order = %q", got)
	}
}

func TestSettingsPreviewCardsFitTheirColumn(t *testing.T) {
	u := &windowUI{
		controller: &workflow.Controller{
			AnnotationStyle:  editor.StrokeStyle{ColorRGBA: [4]uint8{255, 64, 64, 255}, Width: 5},
			HighlighterStyle: editor.StrokeStyle{ColorRGBA: [4]uint8{250, 204, 21, 100}, Width: 18},
		},
		theme:     newTheme(),
		appConfig: config.Config{MosaicBlockSize: 12, MosaicWidth: 32},
	}
	for _, preview := range []layout.Widget{
		u.layoutStrokeSettingsPreview,
		u.layoutHighlighterSettingsPreview,
		u.layoutMosaicSettingsPreview,
	} {
		var ops op.Ops
		gtx := layout.Context{
			Constraints: layout.Constraints{Max: image.Pt(160, 112)},
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Now:         time.Now(),
			Ops:         &ops,
		}
		if got := preview(gtx).Size; got != image.Pt(160, 112) {
			t.Fatalf("preview size = %v, want 160x112", got)
		}
	}
}

func TestPreviewSettingsSectionsFitMinimumModalWidth(t *testing.T) {
	cfg := config.Default()
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		theme:      newTheme(),
		appConfig:  cfg,
		saveConfig: func(config.Config) error { return nil },
	}
	u.applyStrokeStyle()
	u.strokeWidthSlider.Value = widthToSlider(cfg.StrokeWidth)
	u.highlightWidthSlider.Value = highlighterWidthToSlider(cfg.HighlighterWidth)
	u.highlightAlphaSlider.Value = highlighterAlphaToSlider(cfg.HighlighterAlpha)
	u.mosaicBlockSizeSlider.Value = mosaicBlockSizeToSlider(cfg.MosaicBlockSize)
	u.mosaicWidthSlider.Value = mosaicWidthToSlider(cfg.MosaicWidth)

	for _, metric := range []unit.Metric{
		{PxPerDp: 1, PxPerSp: 1},
		{PxPerDp: 1, PxPerSp: 2},
	} {
		for _, section := range []layout.Widget{
			u.layoutStyleSettings,
			u.layoutHighlighterSettings,
			u.layoutMosaicSettings,
		} {
			var ops op.Ops
			gtx := layout.Context{
				Constraints: layout.Constraints{Max: image.Pt(816, 320)},
				Metric:      metric,
				Now:         time.Now(),
				Ops:         &ops,
			}
			dims := section(gtx)
			if dims.Size.X > 816 || dims.Size.Y > 320 {
				t.Fatalf("settings section overflowed at metric %#v: %v", metric, dims.Size)
			}
		}
	}
}

func TestAlignedSettingsCardsFitMinimumModalBody(t *testing.T) {
	cfg := config.Default()
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		theme:      newTheme(),
		appConfig:  cfg,
		saveConfig: func(config.Config) error { return nil },
	}
	u.applyStrokeStyle()
	u.strokeWidthSlider.Value = widthToSlider(cfg.StrokeWidth)
	u.borderWidthSlider.Value = outputBorderWidthToSlider(cfg.OutputBorderWidth)
	u.highlightWidthSlider.Value = highlighterWidthToSlider(cfg.HighlighterWidth)
	u.highlightAlphaSlider.Value = highlighterAlphaToSlider(cfg.HighlighterAlpha)
	u.mosaicBlockSizeSlider.Value = mosaicBlockSizeToSlider(cfg.MosaicBlockSize)
	u.mosaicWidthSlider.Value = mosaicWidthToSlider(cfg.MosaicWidth)

	sections := []struct {
		title   string
		content layout.Widget
	}{
		{title: "일반", content: u.layoutGeneralSettings},
		{title: "캡처", content: u.layoutCaptureSettings},
		{title: "그리기", content: u.layoutStyleSettings},
		{title: "결과 외곽선", content: u.layoutOutputBorderSettings},
		{title: "저장 위치", content: u.layoutSaveSettings},
		{title: "형광펜", content: u.layoutHighlighterSettings},
		{title: "모자이크", content: u.layoutMosaicSettings},
	}
	for _, metric := range []unit.Metric{{PxPerDp: 1, PxPerSp: 1}, {PxPerDp: 1, PxPerSp: 2}} {
		for _, section := range sections {
			var ops op.Ops
			gtx := layout.Context{
				Constraints: layout.Constraints{Max: image.Pt(808, 420)},
				Metric:      metric,
				Now:         time.Now(),
				Ops:         &ops,
			}
			dims := u.layoutSettingsSection(gtx, section.title, section.content)
			if dims.Size.X > 808 || dims.Size.Y > 420 {
				t.Fatalf("section %q overflowed at metric %#v: %v", section.title, metric, dims.Size)
			}
		}
	}
}

func TestMosaicSettingsApplyAndPersistTogether(t *testing.T) {
	saved := 0
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		theme:      newTheme(),
		appConfig:  config.Default(),
		saveConfig: func(config.Config) error {
			saved++
			return nil
		},
	}
	u.mosaicBlockSizeSlider.Value = mosaicBlockSizeToSlider(20)
	u.mosaicWidthSlider.Value = mosaicWidthToSlider(48)
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Constraints{Max: image.Pt(900, 80)},
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}

	u.layoutMosaicSettings(gtx)

	if u.appConfig.MosaicBlockSize != 20 || u.appConfig.MosaicWidth != 48 {
		t.Fatalf("mosaic config = block %d width %d", u.appConfig.MosaicBlockSize, u.appConfig.MosaicWidth)
	}
	if u.controller.MosaicBlockSize != 20 || u.controller.MosaicStyle.Width != 48 {
		t.Fatalf("controller mosaic = block %d style %#v", u.controller.MosaicBlockSize, u.controller.MosaicStyle)
	}
	if saved != 1 {
		t.Fatalf("settings save count = %d, want 1", saved)
	}
}

func TestStatusIndicatorReflectsFeedbackKind(t *testing.T) {
	if got := statusIndicatorColor("저장 실패: 디스크 오류"); got.R != 255 || got.G != 69 {
		t.Fatalf("error status color = %#v", got)
	}
	if got := statusIndicatorColor("화면을 캡처하는 중..."); got.R != 255 || got.G != 214 {
		t.Fatalf("progress status color = %#v", got)
	}
	if got := statusIndicatorColor("저장 완료"); got.G != 209 {
		t.Fatalf("success status color = %#v", got)
	}
}

func TestPersistConfigSurfacesSaveFailure(t *testing.T) {
	u := &windowUI{saveConfig: func(config.Config) error { return errors.New("disk full") }}

	u.persistConfig()

	if !strings.Contains(u.status, "설정 저장 실패") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestWindowUICanvasViewUndoRedo(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	before := u.captureCanvasView()
	u.canvasZoom = 2
	u.canvasPan = image.Pt(12, -8)
	u.canvasSourceSize = image.Pt(800, 600)
	after := u.captureCanvasView()
	u.recordCanvasViewEdit(before, after)
	u.mosaicHoverImage = image.NewRGBA(image.Rect(0, 0, 2, 2))

	if !u.undoLastEdit() {
		t.Fatal("expected zoom undo")
	}
	if u.mosaicHoverImage != nil {
		t.Fatal("undo should clear the mosaic hover cache")
	}
	if got := u.captureCanvasView(); got != before {
		t.Fatalf("unexpected undo view: %#v", got)
	}
	u.mosaicHoverImage = image.NewRGBA(image.Rect(0, 0, 2, 2))
	if !u.redoLastEdit() {
		t.Fatal("expected zoom redo")
	}
	if u.mosaicHoverImage != nil {
		t.Fatal("redo should clear the mosaic hover cache")
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

func TestOutputBorderPreviewStaysVisibleAtSourceEdges(t *testing.T) {
	source := image.Rect(0, 0, 100, 80)
	rects := outputBorderPreviewRects(source, source, 4)
	want := []image.Rectangle{
		image.Rect(0, 0, 100, 4),
		image.Rect(0, 76, 100, 80),
		image.Rect(0, 0, 4, 80),
		image.Rect(96, 0, 100, 80),
	}
	if len(rects) != len(want) {
		t.Fatalf("preview rect count = %d, want %d", len(rects), len(want))
	}
	for i := range want {
		if rects[i] != want[i] {
			t.Fatalf("preview rect %d = %v, want %v", i, rects[i], want[i])
		}
	}
}

func TestOutputBorderPreviewUsesOutsideOfInternalCrop(t *testing.T) {
	source := image.Rect(0, 0, 100, 80)
	crop := image.Rect(20, 10, 70, 60)
	rects := outputBorderPreviewRects(source, crop, 3)
	want := []image.Rectangle{
		image.Rect(20, 7, 70, 10),
		image.Rect(20, 60, 70, 63),
		image.Rect(17, 10, 20, 60),
		image.Rect(70, 10, 73, 60),
	}
	for i := range want {
		if rects[i] != want[i] {
			t.Fatalf("preview rect %d = %v, want %v", i, rects[i], want[i])
		}
	}
}

func TestAutoSaveCurrentImagePreservesOriginalWithoutBorder(t *testing.T) {
	dir := t.TempDir()
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 20, 20)},
		img:      image.NewUniform(color.NRGBA{R: 70, G: 80, B: 90, A: 255}),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 10, 8)); err != nil {
		t.Fatal(err)
	}
	controller.SetOutputBorder(true, 3, [4]uint8{1, 2, 3, 255})
	u := &windowUI{
		controller: controller,
		appConfig:  config.Config{AutoSave: true, SaveDir: dir},
	}

	u.autoSaveCurrentImage()

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("saved file count = %d, want 1", len(entries))
	}
	file, err := os.Open(dir + string(os.PathSeparator) + entries[0].Name())
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	saved, err := png.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := saved.Bounds(), image.Rect(0, 0, 10, 8); got != want {
		t.Fatalf("auto-saved bounds = %v, want %v", got, want)
	}
	if got := color.NRGBAModel.Convert(saved.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 70, G: 80, B: 90, A: 255}) {
		t.Fatalf("auto-saved source pixel = %#v", got)
	}
}

func TestCommitGestureStraightHighlighterUsesEndpoints(t *testing.T) {
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
	u := &windowUI{
		controller:        controller,
		activeTool:        editor.ToolHighlight,
		highlightStraight: true,
		dragStart:         editor.Point{X: 5, Y: 10},
		dragNow:           editor.Point{X: 70, Y: 20},
		penPoints:         []editor.Point{{X: 5, Y: 10}, {X: 20, Y: 60}, {X: 70, Y: 20}},
		canvasZoom:        1,
	}

	u.commitGesture()

	commands := controller.Editor.RenderCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(commands))
	}
	want := []editor.Point{{X: 5, Y: 10}, {X: 70, Y: 20}}
	if got := commands[0].Points; len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("highlight points = %#v, want %#v", got, want)
	}
}

func TestCommitGestureFreeHighlighterUsesFreehandPoints(t *testing.T) {
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
	points := []editor.Point{{X: 5, Y: 10}, {X: 20, Y: 60}, {X: 70, Y: 20}}
	u := &windowUI{
		controller:        controller,
		activeTool:        editor.ToolHighlight,
		highlightStraight: false,
		dragStart:         points[0],
		dragNow:           points[2],
		penPoints:         points,
		canvasZoom:        1,
	}

	u.commitGesture()

	commands := controller.Editor.RenderCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(commands))
	}
	if got := commands[0].Points; len(got) != len(points) || got[1] != points[1] {
		t.Fatalf("highlight points = %#v, want %#v", got, points)
	}
}

func TestCommitGesturePenAndMosaicRespectDrawingMode(t *testing.T) {
	points := []editor.Point{{X: 5, Y: 10}, {X: 20, Y: 60}, {X: 70, Y: 20}}
	for _, test := range []struct {
		name     string
		tool     editor.Tool
		straight bool
	}{
		{name: "straight pen", tool: editor.ToolPen, straight: true},
		{name: "free pen", tool: editor.ToolPen, straight: false},
		{name: "straight mosaic", tool: editor.ToolMosaic, straight: true},
		{name: "free mosaic", tool: editor.ToolMosaic, straight: false},
	} {
		t.Run(test.name, func(t *testing.T) {
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
			controller.MosaicStyle = editor.StrokeStyle{Width: 30}
			controller.MosaicBlockSize = 9
			u := &windowUI{
				controller: controller,
				activeTool: test.tool,
				dragStart:  points[0],
				dragNow:    points[2],
				penPoints:  points,
				canvasZoom: 1,
			}
			if test.tool == editor.ToolPen && !test.straight {
				u.penMode = editor.PenModeFreehand
			}
			if test.tool == editor.ToolMosaic && !test.straight {
				u.mosaicMode = editor.MosaicModeFreehand
			}

			u.commitGesture()
			commands := controller.Editor.RenderCommands()
			if len(commands) != 1 || commands[0].Tool != test.tool {
				t.Fatalf("commands = %#v", commands)
			}
			wantLength := len(points)
			if test.straight {
				wantLength = 2
			}
			if got := len(commands[0].Points); got != wantLength {
				t.Fatalf("point count = %d, want %d", got, wantLength)
			}
			if test.tool == editor.ToolMosaic && (commands[0].MosaicBlockSize != 9 || commands[0].Style.Width != 30) {
				t.Fatalf("mosaic style = %#v", commands[0])
			}
		})
	}
}

func TestCommitGestureRectanglePenUsesDraggedRect(t *testing.T) {
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
	u := &windowUI{
		controller: controller,
		activeTool: editor.ToolPen,
		penMode:    editor.PenModeRectangle,
		dragStart:  editor.Point{X: 70, Y: 60},
		dragNow:    editor.Point{X: 10, Y: 20},
		canvasZoom: 1,
	}

	u.commitGesture()

	commands := controller.Editor.RenderCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
	command := commands[0]
	if command.PenMode != editor.PenModeRectangle || command.Rect != editor.NewRect(u.dragStart, u.dragNow) || len(command.Points) != 0 {
		t.Fatalf("rectangle pen command = %#v", command)
	}
}

func TestCommitGestureRectangleMosaicUsesDraggedRect(t *testing.T) {
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
	controller.MosaicStyle = editor.StrokeStyle{Width: 30}
	controller.MosaicBlockSize = 9
	u := &windowUI{
		controller: controller,
		activeTool: editor.ToolMosaic,
		mosaicMode: editor.MosaicModeRectangle,
		dragStart:  editor.Point{X: 70, Y: 60},
		dragNow:    editor.Point{X: 10, Y: 20},
		canvasZoom: 1,
	}

	u.commitGesture()

	commands := controller.Editor.RenderCommands()
	if len(commands) != 1 {
		t.Fatalf("commands = %#v", commands)
	}
	command := commands[0]
	if command.MosaicMode != editor.MosaicModeRectangle || command.Rect != editor.NewRect(u.dragStart, u.dragNow) || len(command.Points) != 0 {
		t.Fatalf("rectangle mosaic command = %#v", command)
	}
	if command.MosaicBlockSize != 9 || command.Style.Width != 30 {
		t.Fatalf("rectangle mosaic style = %#v", command)
	}
}

func TestWindowUIPanCanvasTracksViewDeltaAtEveryZoom(t *testing.T) {
	for _, scale := range []float64{0.5, 1, 2, 4} {
		u := &windowUI{canvasPan: image.Pt(-50, -100)}
		viewport := coords.Viewport{
			Source: image.Rect(0, 0, 1000, 1000),
			View:   image.Rect(-250, -350, 750, 650),
			Scale:  scale,
		}
		u.beginCanvasPan(editor.Point{X: 100, Y: 100}, viewport)
		if !u.panCanvasToViewPoint(editor.Point{X: 130, Y: 80}, image.Rect(0, 0, 500, 300)) {
			t.Fatalf("scale %v: expected pan to change", scale)
		}
		if got, want := u.canvasPan, image.Pt(-20, -120); got != want {
			t.Fatalf("scale %v: canvas pan = %v, want %v", scale, got, want)
		}
	}
}

func TestWindowUIPanCanvasByViewDeltaClamps(t *testing.T) {
	u := &windowUI{canvasPan: image.Pt(250, 350)}
	viewport := coords.Viewport{
		Source: image.Rect(0, 0, 1000, 1000),
		View:   image.Rect(0, 0, 1000, 1000),
		Scale:  1,
	}
	u.beginCanvasPan(editor.Point{X: 100, Y: 100}, viewport)
	if u.panCanvasToViewPoint(editor.Point{X: 1000, Y: 1000}, image.Rect(0, 0, 500, 300)) {
		t.Fatal("pan should remain clamped at the maximum bound")
	}
}

func TestWindowUIPanCanvasIgnoresStationaryPointer(t *testing.T) {
	u := &windowUI{canvasPan: image.Pt(-50, -100)}
	viewport := coords.Viewport{View: image.Rect(-250, -350, 750, 650), Scale: 2}
	point := editor.Point{X: 220, Y: 180}
	u.beginCanvasPan(point, viewport)

	if u.panCanvasToViewPoint(point, image.Rect(0, 0, 500, 300)) {
		t.Fatal("stationary pointer should not change canvas pan")
	}
	if got, want := u.canvasPan, image.Pt(-50, -100); got != want {
		t.Fatalf("canvas pan = %v, want %v", got, want)
	}
}

func TestWindowUIPanCanvasUsesPressOriginAcrossMultipleEvents(t *testing.T) {
	u := &windowUI{canvasPan: image.Pt(-50, -100)}
	viewport := coords.Viewport{View: image.Rect(-250, -350, 750, 650), Scale: 2}
	bounds := image.Rect(0, 0, 500, 300)
	u.beginCanvasPan(editor.Point{X: 100, Y: 100}, viewport)

	u.panCanvasToViewPoint(editor.Point{X: 130, Y: 80}, bounds)
	u.panCanvasToViewPoint(editor.Point{X: 160, Y: 60}, bounds)

	if got, want := u.canvasPan, image.Pt(10, -140); got != want {
		t.Fatalf("canvas pan = %v, want %v", got, want)
	}
}

func TestWindowUIPanCanvasDoesNotOscillateAtClampBoundary(t *testing.T) {
	u := &windowUI{canvasPan: image.Pt(200, 200)}
	viewport := coords.Viewport{View: image.Rect(0, 0, 1000, 1000), Scale: 2}
	bounds := image.Rect(0, 0, 500, 300)
	u.beginCanvasPan(editor.Point{X: 100, Y: 100}, viewport)

	u.panCanvasToViewPoint(editor.Point{X: 300, Y: 300}, bounds)
	first := u.canvasPan
	u.panCanvasToViewPoint(editor.Point{X: 301, Y: 301}, bounds)

	if u.canvasPan != first {
		t.Fatalf("pan moved at clamp boundary: %v -> %v", first, u.canvasPan)
	}
}

func TestCanvasViewToSourcePointKeepsViewCoordinatesSeparate(t *testing.T) {
	viewport := coords.Viewport{
		Source: image.Rect(0, 0, 1000, 500),
		View:   image.Rect(-200, -100, 1800, 900),
		Scale:  2,
	}

	point, inside := canvasViewToSourcePoint(editor.Point{X: 300, Y: 300}, viewport)

	if !inside || point != (editor.Point{X: 250, Y: 200}) {
		t.Fatalf("source point = %#v, inside = %v", point, inside)
	}
}

func TestCanvasViewportInitialFitReportsActualScale(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	source := image.Rect(0, 0, 2000, 1000)
	bounds := image.Rect(0, 0, 1000, 700)

	viewport := u.canvasViewport(source, bounds)

	if viewport.Scale != 0.5 || u.canvasZoom != 0.5 {
		t.Fatalf("scale = %v, zoom = %v, want 0.5", viewport.Scale, u.canvasZoom)
	}
}

func TestCanvasViewportInitialFitEnlargesSmallCapture(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	source := image.Rect(0, 0, 200, 100)
	bounds := image.Rect(0, 0, 1080, 700)

	viewport := u.canvasViewport(source, bounds)

	if got, want := viewport.Scale, 5.0; got != want {
		t.Fatalf("scale = %v, want %v", got, want)
	}
	if !viewport.View.In(bounds.Inset(canvasFitMargin)) {
		t.Fatalf("view %v should keep handle margin inside %v", viewport.View, bounds)
	}
}

func TestCanvasViewportRefitsOnResizeUntilUserZooms(t *testing.T) {
	u := &windowUI{canvasZoom: 1}
	source := image.Rect(0, 0, 200, 100)

	u.canvasViewport(source, image.Rect(0, 0, 480, 400))
	if u.canvasZoom != 2 {
		t.Fatalf("initial zoom = %v, want 2", u.canvasZoom)
	}
	viewport := u.canvasViewport(source, image.Rect(0, 0, 1080, 700))
	if viewport.Scale != 5 {
		t.Fatalf("resized zoom = %v, want 5", viewport.Scale)
	}

	u.zoomCanvas(-200, editor.Point{X: 100, Y: 50}, image.Rect(0, 0, 1080, 700), viewport)
	zoomed := u.canvasZoom
	if u.canvasViewport(source, image.Rect(0, 0, 480, 400)).Scale != zoomed {
		t.Fatal("manual zoom should survive window resize")
	}
}

func TestCanvasViewportAt100PercentUsesSourcePixels(t *testing.T) {
	source := image.Rect(0, 0, 2000, 1000)
	u := &windowUI{canvasZoom: 1, canvasSourceSize: source.Size()}

	viewport := u.canvasViewport(source, image.Rect(0, 0, 1000, 700))

	if viewport.Scale != 1 {
		t.Fatalf("scale = %v, want 1", viewport.Scale)
	}
	if got, want := viewport.View.Size(), source.Size(); got != want {
		t.Fatalf("view size = %v, want %v", got, want)
	}
}

func TestPixelAlignedImageScaleUsesNearestAt100Percent(t *testing.T) {
	if !pixelAlignedImageScale(1) || !pixelAlignedImageScale(2) {
		t.Fatal("integer image scales should be pixel aligned")
	}
	if pixelAlignedImageScale(0.5) || pixelAlignedImageScale(0.999) {
		t.Fatal("fractional image scales should use interpolation")
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

func TestWindowUIToastDetailIsBrief(t *testing.T) {
	u := &windowUI{}
	u.showToastDetail("Saved PNG", "file.png")

	if u.toastTitle != "Saved PNG" {
		t.Fatalf("unexpected toast title: %q", u.toastTitle)
	}
	if u.toastMessage != "file.png" {
		t.Fatalf("unexpected toast message: %q", u.toastMessage)
	}
	remaining := time.Until(u.toastUntil)
	if remaining < 2*time.Second || remaining > 3*time.Second {
		t.Fatalf("unexpected toast duration: %s", remaining)
	}
}

func TestCompactToastUsesSmallNonBlockingSizes(t *testing.T) {
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(1100, 760)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
	}
	if got, want := compactToastSize(gtx, false), image.Pt(320, 44); got != want {
		t.Fatalf("brief toast size = %v, want %v", got, want)
	}
	if got, want := compactToastSize(gtx, true), image.Pt(320, 64); got != want {
		t.Fatalf("detail toast size = %v, want %v", got, want)
	}
}

func TestErrorToastStaysLongerThanCompletion(t *testing.T) {
	u := &windowUI{}
	u.showToastDetail("저장 실패", "disk busy")

	remaining := time.Until(u.toastUntil)
	if remaining < 3*time.Second || remaining > 4*time.Second {
		t.Fatalf("unexpected error toast duration: %s", remaining)
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

func TestScrollCaptureRangePreservesMeasuredPixels(t *testing.T) {
	percent := 37.5
	selection := nativeoverlay.Selection{ScrollRange: &nativeoverlay.ScrollRangeSelection{
		WindowRect:           image.Rect(100, 50, 500, 450),
		CropRect:             image.Rect(120, 90, 480, 770),
		ViewRect:             image.Rect(120, 90, 480, 390),
		ScrollSteps:          3,
		ScrollPixels:         517,
		InitialScrollPercent: &percent,
		InitialScrollPoint:   image.Pt(200, 160),
		InitialWheelUnits:    360,
	}}

	got, ok := scrollCaptureRangeFromSelection(selection, image.Pt(-200, -100))
	if !ok {
		t.Fatal("expected scroll range")
	}
	if got.Crop != image.Rect(20, 40, 380, 720) || got.View != image.Rect(20, 40, 380, 340) {
		t.Fatalf("crop/view = %v/%v", got.Crop, got.View)
	}
	if got.ScrollSteps != 3 || got.ScrollPixels != 517 {
		t.Fatalf("steps/pixels = %d/%d, want 3/517", got.ScrollSteps, got.ScrollPixels)
	}
	if got.InitialScrollPercent == nil || *got.InitialScrollPercent != percent || got.InitialScrollPoint != image.Pt(100, 110) {
		t.Fatalf("initial scroll origin = %v/%v", got.InitialScrollPercent, got.InitialScrollPoint)
	}
	if got.InitialWheelUnits != 360 {
		t.Fatalf("initial wheel units = %d", got.InitialWheelUnits)
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

func TestNumberShortcutsSelectMarkupTools(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{})
	u := &windowUI{controller: controller, activeTool: editor.ToolPen}

	for _, tc := range []struct {
		key  key.Name
		tool editor.Tool
	}{
		{key: "1", tool: editor.ToolRectangle},
		{key: "2", tool: editor.ToolPen},
		{key: "3", tool: editor.ToolArrow},
		{key: "4", tool: editor.ToolMosaic},
		{key: "5", tool: editor.ToolHighlight},
		{key: "6", tool: editor.ToolText},
	} {
		if !u.handleShortcutKey(key.Event{Name: tc.key, State: key.Press}) {
			t.Fatalf("expected %s to be handled", tc.key)
		}
		if u.activeTool != tc.tool || controller.Editor.ActiveTool != tc.tool {
			t.Fatalf("shortcut %s selected UI/editor tools %v/%v, want %v", tc.key, u.activeTool, controller.Editor.ActiveTool, tc.tool)
		}
	}

	if u.handleShortcutKey(key.Event{Name: "1", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("modified number key should not select a tool")
	}
}

func TestRepeatedNumberShortcutTogglesSelectedToolMode(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{})
	u := &windowUI{
		controller:        controller,
		activeTool:        editor.ToolPen,
		highlightStraight: true,
	}

	for _, tc := range []struct {
		key      key.Name
		tool     editor.Tool
		straight *bool
	}{
		{key: "5", tool: editor.ToolHighlight, straight: &u.highlightStraight},
	} {
		u.activeTool = tc.tool
		if !u.handleShortcutKey(key.Event{Name: tc.key, State: key.Press}) {
			t.Fatalf("expected %s to be handled", tc.key)
		}
		if *tc.straight {
			t.Fatalf("shortcut %s did not toggle %v mode", tc.key, tc.tool)
		}
	}
	u.activeTool = editor.ToolPen
	for _, want := range []editor.PenMode{editor.PenModeFreehand, editor.PenModeRectangle, editor.PenModeStraight} {
		if !u.handleShortcutKey(key.Event{Name: "2", State: key.Press}) {
			t.Fatal("expected 2 to be handled")
		}
		if u.penMode != want {
			t.Fatalf("pen shortcut mode = %v, want %v", u.penMode, want)
		}
	}
	u.activeTool = editor.ToolMosaic
	for _, want := range []editor.MosaicMode{editor.MosaicModeFreehand, editor.MosaicModeRectangle, editor.MosaicModeStraight} {
		if !u.handleShortcutKey(key.Event{Name: "4", State: key.Press}) {
			t.Fatal("expected 4 to be handled")
		}
		if u.mosaicMode != want {
			t.Fatalf("mosaic shortcut mode = %v, want %v", u.mosaicMode, want)
		}
	}
}

func TestShapeShortcutCyclesRectangleTriangleCircle(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{})
	u := &windowUI{controller: controller, activeTool: editor.ToolPen}

	for _, want := range []editor.Tool{
		editor.ToolRectangle,
		editor.ToolTriangle,
		editor.ToolCircle,
		editor.ToolRectangle,
	} {
		if !u.handleShortcutKey(key.Event{Name: "1", State: key.Press}) {
			t.Fatal("expected 1 to be handled")
		}
		if u.activeTool != want || controller.Editor.ActiveTool != want {
			t.Fatalf("shape shortcut selected UI/editor tools %v/%v, want %v", u.activeTool, controller.Editor.ActiveTool, want)
		}
		if u.shapeButtonTool() != want {
			t.Fatalf("shape button tool = %v, want %v", u.shapeButtonTool(), want)
		}
	}
}

func TestTextEditingCreatesReeditsAndUndoesOneAnnotation(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 320, 180))
	controller := workflow.NewController(&uiFakeCapturer{displays: []image.Rectangle{base.Bounds()}, img: base})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(base.Bounds()); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		controller: controller,
		appConfig:  config.Config{StrokeColor: "Red"},
		canvasZoom: 1,
		activeTool: editor.ToolText,
	}

	u.beginNewTextEditing(editor.Point{X: 20, Y: 25}, editor.Point{X: 220, Y: 100}, base.Bounds())
	u.textEditor.SetText("한글 English")
	if !u.commitTextEditing() {
		t.Fatal("new text commit failed")
	}
	if len(controller.Editor.Annotations) != 1 {
		t.Fatalf("annotation count = %d", len(controller.Editor.Annotations))
	}
	original := controller.Editor.Annotations[0]
	if original.Tool != editor.ToolText || original.Text != "한글 English" {
		t.Fatalf("created annotation = %#v", original)
	}
	if got, want := original.TextStyle.ColorRGBA, ([4]uint8{0, 0, 0, 255}); got != want {
		t.Fatalf("default text color = %#v, want %#v", got, want)
	}

	u.beginTextEditing(original)
	u.textEditor.SetText("수정된 Text")
	u.textEditingStyle.Bold = true
	if !u.commitTextEditing() {
		t.Fatal("text update failed")
	}
	updated := controller.Editor.Annotations[0]
	if updated.ID != original.ID || updated.Text != "수정된 Text" || !updated.TextStyle.Bold {
		t.Fatalf("updated annotation = %#v", updated)
	}
	if !u.undoLastEdit() {
		t.Fatal("expected text update undo")
	}
	restored := controller.Editor.Annotations[0]
	if restored.Text != original.Text || restored.TextStyle != original.TextStyle {
		t.Fatalf("undo restored %#v, want %#v", restored, original)
	}
}

func TestTextEditingCancelLeavesExistingAnnotationUnchanged(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{})
	controller.Mode = workflow.ModeEditing
	controller.Editor.State = editor.StateEditing
	annotation := editor.Annotation{
		ID:        "annotation-1",
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 150, Y: 70}),
		Text:      "원본",
		TextStyle: editor.DefaultTextStyle(),
	}
	if err := controller.Editor.AddAnnotation(annotation); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{controller: controller}
	u.beginTextEditing(annotation)
	u.textEditor.SetText("취소할 변경")
	u.cancelTextEditing("취소")
	got, _ := controller.Editor.AnnotationByID(annotation.ID)
	if got.Text != annotation.Text {
		t.Fatalf("cancel changed text to %q", got.Text)
	}
}

func TestTextAnnotationHitTestingUsesTopmostAnnotation(t *testing.T) {
	style := editor.DefaultTextStyle()
	u := &windowUI{controller: &workflow.Controller{Editor: &editor.Editor{
		Annotations: []editor.Annotation{
			{ID: "back", Tool: editor.ToolText, Rect: editor.NewRect(editor.Point{X: 0, Y: 0}, editor.Point{X: 100, Y: 80}), Text: "뒤", TextStyle: style},
			{ID: "front", Tool: editor.ToolText, Rect: editor.NewRect(editor.Point{X: 20, Y: 20}, editor.Point{X: 120, Y: 90}), Text: "앞", TextStyle: style},
		},
	}}}
	annotation, ok := u.textAnnotationAt(editor.Point{X: 50, Y: 50})
	if !ok || annotation.ID != "front" {
		t.Fatalf("hit annotation = %#v, %v", annotation, ok)
	}
}

func TestTextEditorViewRectTracksViewport(t *testing.T) {
	viewport := coords.Viewport{
		Source: image.Rect(0, 0, 200, 100),
		View:   image.Rect(50, 30, 450, 230),
		Scale:  2,
	}
	rect := editor.NewRect(editor.Point{X: 10, Y: 15}, editor.Point{X: 80, Y: 50})
	if got, want := textEditorViewRect(rect, viewport), image.Rect(70, 60, 210, 130); got != want {
		t.Fatalf("text editor view rect = %v, want %v", got, want)
	}
}

func TestNewTextEditingRectProvidesUsableClickBox(t *testing.T) {
	bounds := image.Rect(0, 0, 200, 100)
	got := newTextEditingRect(editor.Point{X: 190, Y: 95}, editor.Point{X: 190, Y: 95}, bounds).ImageRect()
	if got != bounds {
		t.Fatalf("edge click text box = %v, want %v", got, bounds)
	}
	if nextTextAlignment(editor.TextAlignRight) != editor.TextAlignLeft {
		t.Fatal("text alignment control did not cycle")
	}
}

func TestParseTextFontSize(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  float64
		ok    bool
	}{
		{value: "1", want: 1, ok: true},
		{value: "36", want: 36, ok: true},
		{value: "512", want: 512, ok: true},
		{value: "", ok: false},
		{value: "0", ok: false},
		{value: "513", ok: false},
	} {
		got, ok := parseTextFontSize(tc.value)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("parseTextFontSize(%q) = %v, %v; want %v, %v", tc.value, got, ok, tc.want, tc.ok)
		}
	}
}

func TestTextSizeInputUpdatesEditingStyle(t *testing.T) {
	u := &windowUI{textEditingStyle: editor.DefaultTextStyle()}
	u.configureTextSizeEditor()
	u.textSizeEditor.SetText("47")
	if !u.applyTextSizeInput() {
		t.Fatal("expected valid text size")
	}
	if got := u.textEditingStyle.FontSize; got != 47 {
		t.Fatalf("font size = %v, want 47", got)
	}
}

func TestTextSizeAdjustmentClampsAndUpdatesEditor(t *testing.T) {
	u := &windowUI{textEditingStyle: editor.DefaultTextStyle()}
	u.configureTextSizeEditor()
	u.textSizeEditor.SetText("32")
	u.adjustTextFontSize(1)
	if got := u.textSizeEditor.Text(); got != "33" || u.textEditingStyle.FontSize != 33 {
		t.Fatalf("incremented size = %q, %v", got, u.textEditingStyle.FontSize)
	}
	u.adjustTextFontSize(-1000)
	if got := u.textSizeEditor.Text(); got != "1" || u.textEditingStyle.FontSize != 1 {
		t.Fatalf("clamped size = %q, %v", got, u.textEditingStyle.FontSize)
	}
}

func TestTextSizeWheelAccumulatesHighResolutionInput(t *testing.T) {
	u := &windowUI{textEditingStyle: editor.DefaultTextStyle()}
	u.configureTextSizeEditor()
	u.textSizeEditor.SetText("32")
	for range 3 {
		u.applyTextSizeWheel(-10)
	}
	if got := u.textEditingStyle.FontSize; got != 32 {
		t.Fatalf("font size changed before wheel threshold: %v", got)
	}
	u.applyTextSizeWheel(-10)
	if got := u.textEditingStyle.FontSize; got != 33 {
		t.Fatalf("font size after accumulated wheel input = %v, want 33", got)
	}
	u.applyTextSizeWheel(80)
	if got := u.textEditingStyle.FontSize; got != 31 {
		t.Fatalf("font size after two reverse wheel steps = %v, want 31", got)
	}
}

func TestTextEditingPreviewBackgroundIsTemporaryAndBehindText(t *testing.T) {
	for _, textColor := range [][4]uint8{{0, 0, 0, 255}, {255, 255, 255, 255}} {
		style := editor.DefaultTextStyle()
		style.ColorRGBA = textColor
		style.BackgroundEnabled = false
		preview := textEditingPreviewStyle(style)
		if !preview.BackgroundEnabled || preview.BackgroundRGBA[3] >= 80 {
			t.Fatalf("preview background = %#v, want a highly translucent rendered background", preview.BackgroundRGBA)
		}
		if preview.ColorRGBA != textColor {
			t.Fatalf("preview background changed text color alpha: %#v", preview.ColorRGBA)
		}
		if style.BackgroundEnabled {
			t.Fatal("preview style mutated the committed text style")
		}
	}
	style := editor.DefaultTextStyle()
	style.BackgroundEnabled = true
	style.BackgroundRGBA = [4]uint8{37, 99, 235, 128}
	if preview := textEditingPreviewStyle(style); preview != style {
		t.Fatalf("enabled background changed in preview: %#v", preview)
	}
}

func TestTextPreviewFastPathReusesBaseAndUnchangedLayer(t *testing.T) {
	controller := workflow.NewController(nil)
	controller.Mode = workflow.ModeEditing
	controller.CapturedImage = image.NewRGBA(image.Rect(0, 0, 320, 180))
	controller.CropRect = controller.CapturedImage.Bounds()
	controller.Editor.State = editor.StateEditing
	u := &windowUI{controller: controller}
	u.beginNewTextEditing(editor.Point{X: 20, Y: 25}, editor.Point{X: 220, Y: 100}, controller.CapturedImage.Bounds())
	command := editor.RenderCommand{
		Tool:      editor.ToolText,
		Rect:      u.textEditingRect,
		Text:      "first",
		TextStyle: textEditingPreviewStyle(u.textEditingStyle),
	}

	fast, err := u.prepareTextPreview(command)
	if err != nil || !fast {
		t.Fatalf("first preview = fast %v, err %v", fast, err)
	}
	base, layer := u.textPreview.base, u.textPreview.layer
	fast, err = u.prepareTextPreview(command)
	if err != nil || !fast {
		t.Fatalf("unchanged preview = fast %v, err %v", fast, err)
	}
	if u.textPreview.base != base || u.textPreview.layer != layer {
		t.Fatal("unchanged text preview did not reuse its cached images")
	}

	command.Text = "second"
	fast, err = u.prepareTextPreview(command)
	if err != nil || !fast {
		t.Fatalf("changed preview = fast %v, err %v", fast, err)
	}
	if u.textPreview.base != base {
		t.Fatal("text change rerendered the full base image")
	}
	if u.textPreview.layer == layer {
		t.Fatal("text change did not rerender the text layer")
	}
	changedLayer := u.textPreview.layer
	command.TextRuns = []editor.TextRun{{Start: 0, End: 3, Bold: true}}
	if fast, err = u.prepareTextPreview(command); err != nil || !fast {
		t.Fatalf("run change preview = fast %v, err %v", fast, err)
	}
	if u.textPreview.base != base || u.textPreview.layer == changedLayer {
		t.Fatal("run change should rerender only the text layer")
	}
	changedLayer = u.textPreview.layer
	command.TextStyle.BackgroundEnabled = true
	command.TextStyle.BackgroundRGBA = [4]uint8{37, 99, 235, 128}
	if fast, err = u.prepareTextPreview(command); err != nil || !fast {
		t.Fatalf("style change preview = fast %v, err %v", fast, err)
	}
	if u.textPreview.base != base || u.textPreview.layer == changedLayer {
		t.Fatal("style change should rerender only the text layer")
	}
	changedLayer = u.textPreview.layer
	command.Rect = editor.NewRect(editor.Point{X: 30, Y: 35}, editor.Point{X: 230, Y: 110})
	if fast, err = u.prepareTextPreview(command); err != nil || !fast {
		t.Fatalf("rect change preview = fast %v, err %v", fast, err)
	}
	if u.textPreview.base != base || u.textPreview.layer == changedLayer {
		t.Fatal("rect change should rerender only the text layer")
	}
	stableLayer := u.textPreview.layer
	u.canvasZoom = 1.75
	u.canvasPan = image.Pt(13, -7)
	if fast, err = u.prepareTextPreview(command); err != nil || !fast {
		t.Fatalf("view change preview = fast %v, err %v", fast, err)
	}
	if u.textPreview.base != base || u.textPreview.layer != stableLayer {
		t.Fatal("zoom or pan invalidated source-space preview rasters")
	}
	u.resetTextPreview()
	if u.textPreview.base != nil || u.textPreview.layer != nil || u.textPreview.layerValid {
		t.Fatal("text preview reset retained cached rasters")
	}
}

func TestTextPreviewFastPathRequiresExistingTextToBeTopmost(t *testing.T) {
	controller := workflow.NewController(nil)
	controller.Mode = workflow.ModeEditing
	controller.CapturedImage = image.NewRGBA(image.Rect(0, 0, 200, 120))
	controller.Editor.State = editor.StateEditing
	style := editor.DefaultTextStyle()
	controller.Editor.Annotations = []editor.Annotation{
		{ID: "editing", Tool: editor.ToolText, Rect: editor.NewRect(editor.Point{}, editor.Point{X: 100, Y: 60}), Text: "text", TextStyle: style},
		{ID: "above", Tool: editor.ToolRectangle, Rect: editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 80, Y: 50})},
	}
	u := &windowUI{controller: controller, textEditing: true, textEditingID: "editing"}
	if u.textPreviewFastPathEligible() {
		t.Fatal("non-topmost text must retain the full stacking-order renderer")
	}
	controller.Editor.Annotations = controller.Editor.Annotations[:1]
	if !u.textPreviewFastPathEligible() {
		t.Fatal("topmost text should use the cached layer renderer")
	}
}

func TestTextPreviewFastPathYieldsToCropMagnifierComposition(t *testing.T) {
	controller := workflow.NewController(nil)
	controller.Mode = workflow.ModeEditing
	controller.CapturedImage = image.NewRGBA(image.Rect(0, 0, 200, 120))
	controller.Editor.State = editor.StateEditing
	u := &windowUI{
		controller:   controller,
		textEditing:  true,
		cropDragging: true,
	}
	fast, err := u.prepareTextPreview(editor.RenderCommand{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{}, editor.Point{X: 100, Y: 60}),
		Text:      "crop",
		TextStyle: editor.DefaultTextStyle(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if fast || u.textPreview.base != nil || u.textPreview.layer != nil {
		t.Fatal("crop magnifier must use the fully composed fallback preview")
	}
}

func TestSelectedTextBoldToggleUndoAndRedo(t *testing.T) {
	u := &windowUI{textEditing: true, textEditingStyle: editor.DefaultTextStyle()}
	u.textEditor.SetText("가나다라")
	u.textEditor.SetCaret(1, 3)
	if !u.toggleSelectedTextBold() {
		t.Fatal("selected bold toggle was not applied")
	}
	for i, want := range []bool{false, true, true, false} {
		if got := editor.BoldAtRuneOffset(u.textEditor.Text(), false, u.textEditingRuns, i); got != want {
			t.Fatalf("bold at %d = %v, want %v", i, got, want)
		}
	}
	u.undoTextEdit()
	if len(u.textEditingRuns) != 0 {
		t.Fatalf("undo left runs: %#v", u.textEditingRuns)
	}
	u.redoTextEdit()
	if !editor.BoldAtRuneOffset(u.textEditor.Text(), false, u.textEditingRuns, 1) {
		t.Fatal("redo did not restore selected bold")
	}
}

type richTextInputHarness struct {
	router *input.Router
	gtx    layout.Context
}

func newRichTextInputHarness(u *windowUI) *richTextInputHarness {
	router := new(input.Router)
	gtx := layout.Context{
		Ops:         new(op.Ops),
		Source:      router.Source(),
		Constraints: layout.Exact(image.Pt(320, 120)),
		Locale:      system.Locale{Language: "ko", Direction: system.LTR},
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
	}
	u.handleRichTextShortcuts(gtx)
	u.syncTextEditorEvents(gtx)
	event.Op(gtx.Ops, &u.textEditor)
	gtx.Execute(key.FocusCmd{Tag: &u.textEditor})
	router.Frame(gtx.Ops)
	gtx.Ops.Reset()
	return &richTextInputHarness{router: router, gtx: gtx}
}

func (h *richTextInputHarness) process(u *windowUI, events ...event.Event) {
	h.router.Queue(events...)
	u.handleRichTextShortcuts(h.gtx)
	u.syncTextEditorEvents(h.gtx)
	event.Op(h.gtx.Ops, &u.textEditor)
	h.router.Frame(h.gtx.Ops)
	h.gtx.Ops.Reset()
}

func TestRichTextShortcutsRouteThroughGioKeyFilters(t *testing.T) {
	u := &windowUI{}
	u.beginTextEditing(editor.Annotation{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{}, editor.Point{X: 240, Y: 100}),
		Text:      "가나다라",
		TextStyle: editor.DefaultTextStyle(),
	})
	u.textEditor.SetCaret(1, 3)
	harness := newRichTextInputHarness(u)

	harness.process(u, key.Event{Name: "B", Modifiers: key.ModShortcut, State: key.Press})
	if !editor.BoldAtRuneOffset(u.textEditor.Text(), false, u.textEditingRuns, 1) || !editor.BoldAtRuneOffset(u.textEditor.Text(), false, u.textEditingRuns, 2) {
		t.Fatalf("Ctrl+B did not bold the selected run: %#v", u.textEditingRuns)
	}
	harness.process(u, key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press})
	if u.textEditingRuns != nil {
		t.Fatalf("Ctrl+Z did not undo selected bold: %#v", u.textEditingRuns)
	}
	harness.process(u, key.Event{Name: "Y", Modifiers: key.ModShortcut, State: key.Press})
	if !editor.BoldAtRuneOffset(u.textEditor.Text(), false, u.textEditingRuns, 1) {
		t.Fatalf("Ctrl+Y did not redo selected bold: %#v", u.textEditingRuns)
	}
	harness.process(u, key.Event{Name: "Z", Modifiers: key.ModShortcut, State: key.Press})
	if u.textEditingRuns != nil {
		t.Fatalf("second Ctrl+Z did not prepare Shift+Z redo: %#v", u.textEditingRuns)
	}
	harness.process(u, key.Event{Name: "Z", Modifiers: key.ModShortcut | key.ModShift, State: key.Press})
	if !editor.BoldAtRuneOffset(u.textEditor.Text(), false, u.textEditingRuns, 1) {
		t.Fatalf("Ctrl+Shift+Z should redo after Ctrl+Z, runs: %#v", u.textEditingRuns)
	}
}

func TestKoreanIMECompositionRoutesThroughGioEditEvents(t *testing.T) {
	style := editor.DefaultTextStyle()
	u := &windowUI{}
	u.beginTextEditing(editor.Annotation{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{}, editor.Point{X: 240, Y: 100}),
		Text:      "가나",
		TextStyle: style,
		TextRuns: []editor.TextRun{
			{Start: 0, End: 1, Bold: true},
			{Start: 1, End: 2, Bold: false},
		},
	})
	u.textEditor.SetCaret(1, 1)
	harness := newRichTextInputHarness(u)

	harness.process(u,
		key.EditEvent{Range: key.Range{Start: 1, End: 1}, Text: "ㅎ"},
		key.SelectionEvent{Start: 2, End: 2},
	)
	harness.process(u,
		key.EditEvent{Range: key.Range{Start: 1, End: 2}, Text: "하"},
		key.SelectionEvent{Start: 2, End: 2},
	)
	harness.process(u,
		key.EditEvent{Range: key.Range{Start: 1, End: 2}, Text: "한"},
		key.SelectionEvent{Start: 2, End: 2},
	)
	if got := u.textEditor.Text(); got != "가한나" {
		t.Fatalf("IME final composition text = %q, want %q", got, "가한나")
	}
	if !editor.BoldAtRuneOffset("가한나", false, u.textEditingRuns, 0) || !editor.BoldAtRuneOffset("가한나", false, u.textEditingRuns, 1) || editor.BoldAtRuneOffset("가한나", false, u.textEditingRuns, 2) {
		t.Fatalf("IME composition crossed the partial-bold boundary: %#v", u.textEditingRuns)
	}
	if got := len(u.textEditUndo); got != 1 {
		t.Fatalf("IME composition undo entries = %d, want one composition unit", got)
	}
	u.undoTextEdit()
	if got := u.textEditor.Text(); got != "가나" {
		t.Fatalf("IME undo text = %q, want %q", got, "가나")
	}
	u.redoTextEdit()
	if got := u.textEditor.Text(); got != "가한나" {
		t.Fatalf("IME redo lost final composition character: %q", got)
	}
}

func TestTextEditUndoRestoresStyleAndBox(t *testing.T) {
	style := editor.DefaultTextStyle()
	rect := editor.NewRect(editor.Point{X: 10, Y: 20}, editor.Point{X: 180, Y: 90})
	u := &windowUI{textEditing: true, textEditingStyle: style, textEditingRect: rect}
	u.textEditor.SetText("스타일")
	u.pushTextEditUndo(u.captureTextEditSnapshot())
	u.textEditingStyle.FontSize = 48
	u.textEditingStyle.BackgroundEnabled = true
	u.textEditingStyle.BorderEnabled = true
	u.textEditingRect = editor.NewRect(editor.Point{X: 40, Y: 50}, editor.Point{X: 250, Y: 150})

	u.undoTextEdit()
	if u.textEditingStyle != style || u.textEditingRect != rect {
		t.Fatalf("undo restored style/rect %#v/%#v, want %#v/%#v", u.textEditingStyle, u.textEditingRect, style, rect)
	}
	u.redoTextEdit()
	if u.textEditingStyle.FontSize != 48 || !u.textEditingStyle.BackgroundEnabled || !u.textEditingStyle.BorderEnabled {
		t.Fatalf("redo did not restore style changes: %#v", u.textEditingStyle)
	}
}

func TestTextBackgroundOpacityTogglePersistsAcrossColorChanges(t *testing.T) {
	style := editor.DefaultTextStyle()
	u := &windowUI{textEditing: true, textEditingStyle: style}
	u.textEditor.SetText("배경")
	u.toggleTextBackgroundAlpha()
	if !u.textEditingStyle.BackgroundEnabled || u.textEditingStyle.BackgroundRGBA[3] != textBackgroundTranslucent {
		t.Fatalf("translucent background style = %#v", u.textEditingStyle)
	}
	if got := textBackgroundAlphaLabel(u.textEditingStyle.BackgroundRGBA[3]); got != "반투명 50%" {
		t.Fatalf("translucent label = %q", got)
	}
	u.setTextBackgroundColor([4]uint8{37, 99, 235, 255})
	if got := u.textEditingStyle.BackgroundRGBA; got != ([4]uint8{37, 99, 235, textBackgroundTranslucent}) {
		t.Fatalf("color change reset background opacity: %#v", got)
	}
	undoEntries := len(u.textEditUndo)
	u.setTextBackgroundColor([4]uint8{37, 99, 235, 255})
	if len(u.textEditUndo) != undoEntries {
		t.Fatal("selecting the current background color added a no-op undo entry")
	}
	if !colorPickerRGBASelected("text-background", u.textEditingStyle.BackgroundRGBA, [4]uint8{37, 99, 235, 255}) {
		t.Fatal("translucent background color was not selected by RGB")
	}
	if colorPickerRGBASelected("text", u.textEditingStyle.BackgroundRGBA, [4]uint8{37, 99, 235, 255}) {
		t.Fatal("non-background color selection ignored alpha")
	}
	u.toggleTextBackgroundAlpha()
	if u.textEditingStyle.BackgroundRGBA[3] != 255 || textBackgroundAlphaLabel(u.textEditingStyle.BackgroundRGBA[3]) != "불투명" {
		t.Fatalf("opaque background style = %#v", u.textEditingStyle)
	}
	u.undoTextEdit()
	if u.textEditingStyle.BackgroundRGBA[3] != textBackgroundTranslucent {
		t.Fatalf("undo did not restore translucent background: %#v", u.textEditingStyle)
	}
}

func TestTextControlsExpandForIncreasedSystemTextScale(t *testing.T) {
	if got := textControlsScale(unit.Metric{PxPerDp: 1, PxPerSp: 1}); got != 1 {
		t.Fatalf("normal text scale = %v", got)
	}
	if got := textControlsScale(unit.Metric{PxPerDp: 1, PxPerSp: 2}); got <= 1.25 {
		t.Fatalf("increased text scale = %v, expanded rows would not activate", got)
	}
}

func TestBeginTextEditingPreservesTransparentSquareBoxStyle(t *testing.T) {
	style := editor.DefaultTextStyle()
	style.BackgroundRGBA = [4]uint8{}
	style.BorderRGBA = [4]uint8{}
	style.BorderWidth = 0
	style.CornerRadius = 0
	annotation := editor.Annotation{
		ID:        "text-1",
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 180, Y: 90}),
		Text:      "투명",
		TextStyle: style,
	}
	u := &windowUI{}
	u.beginTextEditing(annotation)
	if u.textEditingStyle != style {
		t.Fatalf("begin editing changed box style: %#v, want %#v", u.textEditingStyle, style)
	}
	u.textSizeWheel = 30
	u.resetTextEditing()
	if u.textSizeWheel != 0 {
		t.Fatalf("reset left wheel accumulator at %v", u.textSizeWheel)
	}
}

func TestWindowInputTagsHaveUniqueAddresses(t *testing.T) {
	u := &windowUI{}
	tags := []*inputTag{
		&u.canvasTag,
		&u.keyTag,
		&u.colorPickerBlockTag,
		&u.settingsBlockTag,
		&u.textMoveTag,
		&u.textResizeTag,
	}
	for i := range tags {
		for j := i + 1; j < len(tags); j++ {
			if tags[i] == tags[j] {
				t.Fatalf("input tags %d and %d share an address", i, j)
			}
		}
	}
}

func TestInvalidTextSizePreventsCommit(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{})
	controller.Mode = workflow.ModeEditing
	controller.Editor.State = editor.StateEditing
	u := &windowUI{controller: controller}
	u.beginTextEditing(editor.Annotation{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 160, Y: 80}),
		TextStyle: editor.DefaultTextStyle(),
	})
	u.textEditor.SetText("크기")
	u.textSizeEditor.SetText("")
	if u.commitTextEditing() {
		t.Fatal("invalid font size should prevent commit")
	}
	if !u.textEditing {
		t.Fatal("invalid font size should keep text editing active")
	}
	if len(controller.Editor.Annotations) != 0 {
		t.Fatal("invalid font size committed an annotation")
	}
}

func TestTransformTextRectMovesAndResizesWithinBounds(t *testing.T) {
	bounds := image.Rect(0, 0, 300, 200)
	start := editor.NewRect(editor.Point{X: 50, Y: 40}, editor.Point{X: 150, Y: 100})
	if got, want := transformTextRect(start, editor.Point{X: 200, Y: 150}, textTransformMove, bounds).ImageRect(), image.Rect(200, 140, 300, 200); got != want {
		t.Fatalf("moved rect = %v, want %v", got, want)
	}
	if got, want := transformTextRect(start, editor.Point{X: 80, Y: 70}, textTransformResize, bounds).ImageRect(), image.Rect(50, 40, 230, 170); got != want {
		t.Fatalf("resized rect = %v, want %v", got, want)
	}
	if got, want := transformTextRect(start, editor.Point{X: -200, Y: -200}, textTransformResize, bounds).ImageRect(), image.Rect(50, 40, 98, 72); got != want {
		t.Fatalf("minimum resized rect = %v, want %v", got, want)
	}
}

func TestTextTransformHandleGeometryAlignsVisualAndHitAreas(t *testing.T) {
	move, resize := textTransformHandleGeometry(image.Rect(0, 0, 200, 100), 16, 14, 34)
	for name, geometry := range map[string]textHandleGeometry{"move": move, "resize": resize} {
		if got := geometry.Hit.Size(); got != image.Pt(34, 34) {
			t.Fatalf("%s hit size = %v, want 34x34", name, got)
		}
		if !geometry.Visual.In(geometry.Hit) {
			t.Fatalf("%s visual %v is not contained by hit area %v", name, geometry.Visual, geometry.Hit)
		}
		if geometry.Visual.Min.Add(geometry.Visual.Max).Div(2) != geometry.Hit.Min.Add(geometry.Hit.Max).Div(2) {
			t.Fatalf("%s visual and hit centers differ", name)
		}
	}
	if center := move.Hit.Min.Add(move.Hit.Max).Div(2); center != image.Pt(100, 0) {
		t.Fatalf("move handle center = %v", center)
	}
	if center := resize.Hit.Min.Add(resize.Hit.Max).Div(2); center != image.Pt(200, 100) {
		t.Fatalf("resize handle center = %v", center)
	}
}

func TestTextTransformHandleHitAreasAreCenteredBeyondBoxEdges(t *testing.T) {
	viewRect := image.Rect(100, 80, 300, 180)
	for name, point := range map[string]f32.Point{
		"move center":          f32.Pt(200, 80),
		"move above":           f32.Pt(200, 64),
		"move below":           f32.Pt(200, 96),
		"move fractional edge": f32.Pt(216.75, 80),
		"resize center":        f32.Pt(300, 180),
		"resize upper left":    f32.Pt(285, 165),
		"resize lower right":   f32.Pt(315, 195),
	} {
		if !textTransformHandlesContain(point, viewRect, 16, 14, 34) {
			t.Fatalf("%s point %v was outside the handle hit area", name, point)
		}
	}
	for name, point := range map[string]f32.Point{
		"above move hit area":  f32.Pt(200, 62),
		"beside move hit area": f32.Pt(182, 80),
		"at move maximum edge": f32.Pt(217, 80),
		"past resize hit area": f32.Pt(318, 198),
		"plain top edge":       f32.Pt(130, 80),
	} {
		if textTransformHandlesContain(point, viewRect, 16, 14, 34) {
			t.Fatalf("%s point %v unexpectedly hit a handle", name, point)
		}
	}
}

func TestTextTransformHandleHitAreasDoNotOverlapOnSmallBoxes(t *testing.T) {
	for _, size := range []image.Point{image.Pt(48, 32), image.Pt(24, 16), image.Pt(2, 1), image.Pt(1, 1)} {
		move, resize := textTransformHandleGeometry(image.Rectangle{Max: size}, 16, 14, 34)
		if move.Hit.Overlaps(resize.Hit) {
			t.Fatalf("%v box has overlapping handle hits: %v and %v", size, move.Hit, resize.Hit)
		}
		moveCenter := image.Pt(size.X/2, 0)
		resizeCenter := size
		if !moveCenter.In(move.Hit) {
			t.Fatalf("%v box move center %v is outside %v", size, moveCenter, move.Hit)
		}
		if !resizeCenter.In(resize.Hit) {
			t.Fatalf("%v box resize center %v is outside %v", size, resizeCenter, resize.Hit)
		}
	}
}

func TestCanvasPressAboveTextBoxDefersToMoveHandle(t *testing.T) {
	bounds := image.Rect(0, 0, 400, 300)
	controller := workflow.NewController(nil)
	controller.Mode = workflow.ModeEditing
	controller.CapturedImage = image.NewRGBA(bounds)
	controller.CropRect = bounds
	controller.Editor.State = editor.StateEditing
	u := &windowUI{controller: controller, activeTool: editor.ToolText}
	u.beginTextEditing(editor.Annotation{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 100, Y: 80}, editor.Point{X: 300, Y: 180}),
		Text:      "handle",
		TextStyle: editor.DefaultTextStyle(),
	})
	router := new(input.Router)
	gtx := layout.Context{
		Ops:         new(op.Ops),
		Source:      router.Source(),
		Constraints: layout.Exact(bounds.Size()),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
	}
	viewport := coords.Viewport{Source: bounds, View: bounds, Scale: 1}
	canvasClip := clip.Rect(bounds).Push(gtx.Ops)
	event.Op(gtx.Ops, &u.canvasTag)
	canvasClip.Pop()
	u.handleCanvasPointer(gtx, nil, bounds, bounds, viewport)
	viewRect := textEditorViewRect(u.textEditingRect, viewport)
	handleOffset := op.Offset(viewRect.Min).Push(gtx.Ops)
	move, _ := textTransformHandleGeometry(image.Rectangle{Max: viewRect.Size()}, 16, 14, 34)
	u.layoutTextTransformHandle(gtx, move, &u.textMoveTag, pointer.CursorGrab, true)
	handleOffset.Pop()
	u.handleTextTransformEvents(gtx, viewport)
	router.Frame(gtx.Ops)
	gtx.Ops.Reset()
	router.Queue(pointer.Event{
		Kind:      pointer.Press,
		Source:    pointer.Mouse,
		PointerID: 1,
		Buttons:   pointer.ButtonPrimary,
		Position:  f32.Pt(200, 64),
	})
	u.handleCanvasPointer(gtx, nil, bounds, bounds, viewport)
	u.handleTextTransformEvents(gtx, viewport)
	if !u.textEditing {
		t.Fatal("pressing the upper half of the move handle ended text editing")
	}
	if !u.textTransforming || u.textTransformMode != textTransformMove {
		t.Fatal("pressing the upper half of the move handle did not start the handle drag")
	}
}

func TestTextTransformHandlesCompleteGesturesAtZoomedViewport(t *testing.T) {
	sourceBounds := image.Rect(0, 0, 240, 120)
	canvasBounds := image.Rect(0, 0, 600, 340)
	viewport := coords.Viewport{Source: sourceBounds, View: image.Rect(50, 40, 530, 280), Scale: 2}
	original := editor.NewRect(editor.Point{X: 60, Y: 30}, editor.Point{X: 140, Y: 70})
	for _, test := range []struct {
		name  string
		press f32.Point
		drag  f32.Point
		want  editor.Rect
	}{
		{
			name:  "move upper half",
			press: f32.Pt(250, 84),
			drag:  f32.Pt(270, 94),
			want:  editor.NewRect(editor.Point{X: 70, Y: 35}, editor.Point{X: 150, Y: 75}),
		},
		{
			name:  "resize center",
			press: f32.Pt(330, 180),
			drag:  f32.Pt(350, 200),
			want:  editor.NewRect(editor.Point{X: 60, Y: 30}, editor.Point{X: 150, Y: 80}),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			controller := workflow.NewController(nil)
			controller.Mode = workflow.ModeEditing
			controller.CapturedImage = image.NewRGBA(sourceBounds)
			controller.CropRect = sourceBounds
			controller.Editor.State = editor.StateEditing
			u := &windowUI{controller: controller, activeTool: editor.ToolText}
			u.beginTextEditing(editor.Annotation{Tool: editor.ToolText, Rect: original, Text: "handle", TextStyle: editor.DefaultTextStyle()})
			router := new(input.Router)
			gtx := layout.Context{
				Ops:         new(op.Ops),
				Source:      router.Source(),
				Constraints: layout.Exact(canvasBounds.Size()),
				Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			}
			canvasClip := clip.Rect(canvasBounds).Push(gtx.Ops)
			event.Op(gtx.Ops, &u.canvasTag)
			canvasClip.Pop()
			u.handleCanvasPointer(gtx, nil, sourceBounds, canvasBounds, viewport)
			viewRect := textEditorViewRect(original, viewport)
			handleOffset := op.Offset(viewRect.Min).Push(gtx.Ops)
			move, resize := textTransformHandleGeometry(image.Rectangle{Max: viewRect.Size()}, 16, 14, 34)
			u.layoutTextTransformHandle(gtx, move, &u.textMoveTag, pointer.CursorGrab, true)
			u.layoutTextTransformHandle(gtx, resize, &u.textResizeTag, pointer.CursorSouthEastResize, false)
			handleOffset.Pop()
			u.handleTextTransformEvents(gtx, viewport)
			router.Frame(gtx.Ops)
			gtx.Ops.Reset()
			router.Queue(
				pointer.Event{Kind: pointer.Press, Source: pointer.Mouse, PointerID: 1, Buttons: pointer.ButtonPrimary, Position: test.press},
				pointer.Event{Kind: pointer.Move, Source: pointer.Mouse, PointerID: 1, Buttons: pointer.ButtonPrimary, Position: test.drag},
				pointer.Event{Kind: pointer.Release, Source: pointer.Mouse, PointerID: 1, Position: test.drag},
			)
			u.handleCanvasPointer(gtx, nil, sourceBounds, canvasBounds, viewport)
			u.handleTextTransformEvents(gtx, viewport)
			if !u.textEditing {
				t.Fatal("handle gesture ended text editing")
			}
			if u.textTransforming {
				t.Fatal("handle gesture remained active after release")
			}
			if u.textEditingRect != test.want {
				t.Fatalf("transformed rect = %#v, want %#v", u.textEditingRect, test.want)
			}
			if len(u.textEditUndo) != 1 {
				t.Fatalf("handle gesture undo entries = %d, want 1", len(u.textEditUndo))
			}
			u.undoTextEdit()
			if u.textEditingRect != original {
				t.Fatalf("undo rect = %#v, want %#v", u.textEditingRect, original)
			}
		})
	}
}

func TestTextTransformSourceDeltaUsesViewportScale(t *testing.T) {
	got := textTransformSourceDelta(f32.Pt(10, 20), f32.Pt(35, 5), 2.5)
	if want := (editor.Point{X: 10, Y: -6}); got != want {
		t.Fatalf("source delta = %v, want %v", got, want)
	}
}

func TestTextFontFamiliesHaveLabelsAndNormalize(t *testing.T) {
	families := textFontFamilies()
	if len(families) != len(windowUI{}.textFontItems) {
		t.Fatalf("font families = %d, buttons = %d", len(families), len(windowUI{}.textFontItems))
	}
	for _, family := range families {
		if textFontFamilyLabel(family) == "" || editor.NormalizeTextFontFamily(family) != family {
			t.Fatalf("invalid text font family %q", family)
		}
	}
}

func TestTextColorSelectionPersistsOnCommit(t *testing.T) {
	controller := workflow.NewController(&uiFakeCapturer{})
	controller.Mode = workflow.ModeEditing
	controller.Editor.State = editor.StateEditing
	u := &windowUI{controller: controller}
	u.beginTextEditing(editor.Annotation{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 160, Y: 80}),
		TextStyle: editor.DefaultTextStyle(),
	})
	u.textEditor.SetText("색상")
	u.textEditingStyle.ColorRGBA = [4]uint8{37, 99, 235, 255}
	if !u.commitTextEditing() {
		t.Fatal("text commit failed")
	}
	if got := controller.Editor.Annotations[0].TextStyle.ColorRGBA; got != ([4]uint8{37, 99, 235, 255}) {
		t.Fatalf("committed text color = %#v", got)
	}
}

func TestShortcutCtrlShiftSSavesToImportedSourcePath(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.png")
	if err := os.WriteFile(sourcePath, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 120, 80)},
		img:      image.NewRGBA(image.Rect(0, 0, 120, 80)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 120, 80)); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		controller:      controller,
		sourceImagePath: sourcePath,
		pickImageSavePath: func(string) (string, bool, error) {
			t.Fatal("Ctrl+S should not open Save As for a supported source file")
			return "", false, nil
		},
	}
	shortcut := key.Event{Name: "S", Modifiers: key.ModShortcut | key.ModShift, State: key.Press}
	if !u.handleShortcutKey(shortcut) {
		t.Fatal("expected Ctrl+Shift+S to be handled")
	}
	file, err := os.Open(sourcePath)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if _, err := png.Decode(file); err != nil {
		t.Fatalf("source was not replaced with a valid PNG: %v", err)
	}
	if !strings.Contains(u.status, "저장 완료") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestShortcutCtrlSWithoutSourceUsesSaveAs(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clipboard.png")
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 20, 10)},
		img:      image.NewRGBA(image.Rect(0, 0, 20, 10)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 20, 10)); err != nil {
		t.Fatal(err)
	}
	pickerCalls := 0
	u := &windowUI{
		controller: controller,
		appConfig:  config.Config{SaveDir: dir},
		pickImageSavePath: func(initial string) (string, bool, error) {
			pickerCalls++
			if filepath.Dir(initial) != dir || filepath.Ext(initial) != ".png" {
				t.Fatalf("unexpected initial path: %q", initial)
			}
			return path, true, nil
		},
	}
	if !u.handleShortcutKey(key.Event{Name: "S", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+S to be handled")
	}
	if pickerCalls != 1 || u.sourceImagePath != path {
		t.Fatalf("save dialog calls=%d source=%q", pickerCalls, u.sourceImagePath)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestShortcutCtrlSUsesSaveAsWithImportedSource(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.png")
	targetPath := filepath.Join(dir, "copy.png")
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 20, 10)},
		img:      image.NewRGBA(image.Rect(0, 0, 20, 10)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 20, 10)); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		controller:      controller,
		sourceImagePath: sourcePath,
		pickImageSavePath: func(initial string) (string, bool, error) {
			if initial != sourcePath {
				t.Fatalf("initial path = %q, want %q", initial, sourcePath)
			}
			return targetPath, true, nil
		},
	}
	if !u.handleShortcutKey(key.Event{Name: "S", Modifiers: key.ModShortcut, State: key.Press}) {
		t.Fatal("expected Ctrl+S to be handled")
	}
	if u.sourceImagePath != targetPath {
		t.Fatalf("source path after Save As = %q", u.sourceImagePath)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatal(err)
	}
}

func TestSaveButtonActionAlwaysUsesSaveAs(t *testing.T) {
	dir := t.TempDir()
	sourcePath := filepath.Join(dir, "source.jpg")
	targetPath := filepath.Join(dir, "copy.jpg")
	controller := workflow.NewController(&uiFakeCapturer{
		displays: []image.Rectangle{image.Rect(0, 0, 12, 8)},
		img:      image.NewRGBA(image.Rect(0, 0, 12, 8)),
	})
	if err := controller.BeginCapture(); err != nil {
		t.Fatal(err)
	}
	if err := controller.CommitImageSelection(image.Rect(0, 0, 12, 8)); err != nil {
		t.Fatal(err)
	}
	u := &windowUI{
		controller:      controller,
		sourceImagePath: sourcePath,
		pickImageSavePath: func(initial string) (string, bool, error) {
			if initial != sourcePath {
				t.Fatalf("initial path = %q, want %q", initial, sourcePath)
			}
			return targetPath, true, nil
		},
	}
	u.saveCurrentImageAs()
	if u.sourceImagePath != targetPath {
		t.Fatalf("source path after Save As = %q", u.sourceImagePath)
	}
	if _, err := os.Stat(targetPath); err != nil {
		t.Fatal(err)
	}
}

func TestShortcutEscapeCancelsGestures(t *testing.T) {
	hidden := false
	u := &windowUI{
		dragging:     true,
		cropDragging: true,
		hideToTrayOnEscape: func() {
			hidden = true
		},
	}
	if !u.handleShortcutKey(key.Event{Name: key.NameEscape, State: key.Press}) {
		t.Fatal("expected Escape to be handled")
	}
	if u.dragging || u.cropDragging {
		t.Fatal("Escape did not cancel gestures")
	}
	if !hidden {
		t.Fatal("Escape did not hide the window to the tray")
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
	u.handleShortcutKey(key.Event{Name: key.NameAlt, State: key.Release})
	u.syncNativeHotkeyCaptureKeys(nil)
	if u.hotkeyCaptureMode || u.appConfig.Hotkey != "Alt+PrintScreen" {
		t.Fatalf("unexpected final state: capture=%v hotkey=%q", u.hotkeyCaptureMode, u.appConfig.Hotkey)
	}
}

func TestHotkeyCaptureRejectsFourthSimultaneousKey(t *testing.T) {
	saved := 0
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig: func(config.Config) error {
			saved++
			return nil
		},
		rebindHotkey: func(hotkey.Binding, func()) error { return nil },
	}
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
	if got := u.hotkeyButtonLabel(); got != "단축키 입력 중: Ctrl+Alt+K+Shift" {
		t.Fatalf("invalid chord should be shown as entered, label = %q", got)
	}
	u.handleShortcutKey(key.Event{Name: key.NameShift, State: key.Release})
	u.handleShortcutKey(key.Event{Name: "K", State: key.Release})
	u.handleShortcutKey(key.Event{Name: key.NameAlt, State: key.Release})
	u.handleShortcutKey(key.Event{Name: key.NameCtrl, State: key.Release})
	if saved != 0 || u.appConfig.Hotkey != "Ctrl+Shift+C" || !u.hotkeyCaptureMode {
		t.Fatalf("invalid chord was saved: saved=%d hotkey=%q capture=%v", saved, u.appConfig.Hotkey, u.hotkeyCaptureMode)
	}
}

func TestHotkeyCaptureRejectsTwoMainKeysWithoutSavingPartialChord(t *testing.T) {
	saved := 0
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig: func(config.Config) error {
			saved++
			return nil
		},
		rebindHotkey: func(hotkey.Binding, func()) error { return nil },
	}
	u.handleShortcutKey(key.Event{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Press})
	u.handleShortcutKey(key.Event{Name: "K", Modifiers: key.ModCtrl, State: key.Press})
	u.handleShortcutKey(key.Event{Name: "L", Modifiers: key.ModCtrl, State: key.Press})
	u.handleShortcutKey(key.Event{Name: "L", Modifiers: key.ModCtrl, State: key.Release})
	u.handleShortcutKey(key.Event{Name: "K", Modifiers: key.ModCtrl, State: key.Release})
	u.handleShortcutKey(key.Event{Name: key.NameCtrl, State: key.Release})
	if saved != 0 || u.appConfig.Hotkey != "Ctrl+Shift+C" || !u.hotkeyCaptureMode {
		t.Fatalf("partial chord was saved: saved=%d hotkey=%q capture=%v", saved, u.appConfig.Hotkey, u.hotkeyCaptureMode)
	}
	if !strings.Contains(u.status, "저장하지 않았습니다") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestHotkeyCaptureAllowsModifiedEscape(t *testing.T) {
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig:        func(config.Config) error { return nil },
		rebindHotkey:      func(hotkey.Binding, func()) error { return nil },
	}
	u.handleShortcutKey(key.Event{Name: key.NameCtrl, Modifiers: key.ModCtrl, State: key.Press})
	u.handleShortcutKey(key.Event{Name: key.NameEscape, Modifiers: key.ModCtrl, State: key.Press})
	u.handleShortcutKey(key.Event{Name: key.NameEscape, Modifiers: key.ModCtrl, State: key.Release})
	u.handleShortcutKey(key.Event{Name: key.NameCtrl, State: key.Release})
	if u.hotkeyCaptureMode || u.appConfig.Hotkey != "Ctrl+Escape" {
		t.Fatalf("modified Escape was not assigned: capture=%v hotkey=%q", u.hotkeyCaptureMode, u.appConfig.Hotkey)
	}
}

func TestHotkeySaveFailureRestoresPreviousRegistration(t *testing.T) {
	var rebound []string
	u := &windowUI{
		hotkeyCaptureMode: true,
		appConfig:         config.Config{Hotkey: "Ctrl+Shift+C"},
		saveConfig: func(config.Config) error {
			return errors.New("disk full")
		},
		rebindHotkey: func(binding hotkey.Binding, _ func()) error {
			rebound = append(rebound, binding.Name)
			return nil
		},
	}
	binding, ok := hotkey.ParseBinding("Alt+D")
	if !ok {
		t.Fatal("test binding should parse")
	}
	u.applyHotkeyBinding(binding)
	if got := strings.Join(rebound, ","); got != "Alt+D,Ctrl+Shift+C" {
		t.Fatalf("rebind order = %q", got)
	}
	if u.appConfig.Hotkey != "Ctrl+Shift+C" || !u.hotkeyCaptureMode {
		t.Fatalf("failed save changed state: hotkey=%q capture=%v", u.appConfig.Hotkey, u.hotkeyCaptureMode)
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

func TestCropMagnifierSourceRectClampsAtImageEdges(t *testing.T) {
	bounds := image.Rect(0, 0, 100, 80)
	if got := cropMagnifierSourceRect(image.Pt(0, 0), 40, bounds); got != image.Rect(0, 0, 40, 40) {
		t.Fatalf("top-left source = %v", got)
	}
	if got := cropMagnifierSourceRect(image.Pt(100, 80), 40, bounds); got != image.Rect(60, 40, 100, 80) {
		t.Fatalf("bottom-right source = %v", got)
	}
	if got := cropMagnifierSourceRect(image.Pt(50, 40), 40, bounds); got != image.Rect(30, 20, 70, 60) {
		t.Fatalf("center source = %v", got)
	}
}

func TestCropMagnifierUsesSquareWorkspaceOverlay(t *testing.T) {
	window := image.Rect(0, 0, 1000, 700)
	destination, ok := cropMagnifierDestination(window, 56, image.Pt(500, 300), image.Pt(160, 160), 16)
	if !ok {
		t.Fatal("expected workspace magnifier placement")
	}
	if destination.Min.Y < 56 {
		t.Fatalf("magnifier escaped workspace: %v", destination)
	}
	if destination.Size() != image.Pt(160, 160) {
		t.Fatalf("unexpected magnifier size: %v", destination.Size())
	}
}

func TestCropMagnifierKeepsSizeBelowTwoRowToolbar(t *testing.T) {
	window := image.Rect(0, 0, 900, 700)
	destination, ok := cropMagnifierDestination(window, 108, image.Pt(450, 350), image.Pt(160, 160), 16)
	if !ok {
		t.Fatal("expected workspace placement")
	}
	if destination.Size() != image.Pt(160, 160) || destination.Min.Y < 108 {
		t.Fatalf("unexpected magnifier: %v", destination)
	}
}

func TestCropMagnifierMovesAwayFromPointer(t *testing.T) {
	window := image.Rect(0, 0, 1000, 700)
	pointer := image.Pt(920, 100)
	destination, ok := cropMagnifierDestination(window, 56, pointer, image.Pt(160, 160), 16)
	if !ok {
		t.Fatal("expected alternate toolbar placement")
	}
	if destination.Min.X != 4 {
		t.Fatalf("magnifier did not move to opposite toolbar side: %v", destination)
	}
	pointerAvoid := image.Rect(pointer.X-16, pointer.Y-16, pointer.X+17, pointer.Y+17)
	if destination.Overlaps(pointerAvoid) {
		t.Fatalf("magnifier covers active pointer: %v", destination)
	}
}

func TestCropMagnifierSpecTracksCropEdgeInSquareOverlay(t *testing.T) {
	source := image.Rect(0, 0, 400, 300)
	destination := image.Rect(832, 60, 992, 220)
	magnifier, ok := cropMagnifierForDestination(image.Pt(400, 300), source, destination)
	if !ok {
		t.Fatal("expected crop magnifier spec")
	}
	if magnifier.Source.Max != source.Max {
		t.Fatalf("source edge = %v, want %v", magnifier.Source.Max, source.Max)
	}
	if !magnifier.Target.In(magnifier.Destination) {
		t.Fatalf("target %v is outside destination %v", magnifier.Target, magnifier.Destination)
	}
	if magnifier.Source.Size() != image.Pt(40, 40) {
		t.Fatalf("source size = %v", magnifier.Source.Size())
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
