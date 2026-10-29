package ui

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"math"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"
	appclipboard "golang.design/x/clipboard"
	_ "golang.org/x/image/bmp"
	_ "golang.org/x/image/webp"

	"instantcapture/internal/capture"
	"instantcapture/internal/config"
	"instantcapture/internal/coords"
	"instantcapture/internal/editor"
	"instantcapture/internal/exporter"
	"instantcapture/internal/hotkey"
	"instantcapture/internal/nativeoverlay"
	"instantcapture/internal/overlay"
	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/tray"
	"instantcapture/internal/workflow"
)

const windowTitle = "InstantCapture"

const (
	minCanvasZoom             = 0.005
	maxCanvasZoom             = 8.0
	canvasFitMargin           = 40
	cropHandleScreenTolerance = 4
	maxHotkeyCaptureKeys      = 3
	maxEditorImageFileBytes   = 256 << 20
	maxEditorImagePixels      = 64_000_000
	toolbarSingleRowMinWidth  = 1040
	toastBriefDuration        = 1800 * time.Millisecond
	toastDetailDuration       = 2400 * time.Millisecond
	toastErrorDuration        = 3500 * time.Millisecond
	textBackgroundTranslucent = 128
)

type cropHandle uint8

const (
	cropHandleNone cropHandle = 0
	cropHandleLeft cropHandle = 1 << iota
	cropHandleRight
	cropHandleTop
	cropHandleBottom
)

type textTransformMode uint8

const (
	textTransformNone textTransformMode = iota
	textTransformMove
	textTransformResize
)

type textHandleGeometry struct {
	Visual image.Rectangle
	Hit    image.Rectangle
}

type textEditSnapshot struct {
	Text           string
	Runs           []editor.TextRun
	Style          editor.TextStyle
	Rect           editor.Rect
	SelectionStart int
	SelectionEnd   int
}

type inputTag struct {
	id byte
}

type cropMagnifier struct {
	Source      image.Rectangle
	Destination image.Rectangle
	Target      image.Point
}

type cropMagnifierFrame struct {
	Image        paint.ImageOp
	SourcePoint  image.Point
	SourceBounds image.Rectangle
	PointerView  image.Point
	Visible      bool
}

type uiEditHistoryKind uint8

const (
	uiEditHistoryController uiEditHistoryKind = iota
	uiEditHistoryCanvasView
)

type canvasViewState struct {
	Zoom       float64
	Pan        image.Point
	SourceSize image.Point
}

type uiEditHistoryEntry struct {
	Kind uiEditHistoryKind
	From canvasViewState
	To   canvasViewState
}

type overlayResult struct {
	mode           string
	rememberRegion bool
	selection      nativeoverlay.Selection
	ok             bool
	err            error
}

type captureStartResult struct {
	mode           string
	rememberRegion bool
	useSavedRegion bool
	snapshot       workflow.CaptureSnapshot
	windowTargets  []platformwindows.WindowTarget
	err            error
}

type scrollCaptureResult struct {
	target     platformwindows.WindowTarget
	img        image.Image
	screenRect image.Rectangle
	err        error
}

type imageLoadResult struct {
	generation  uint64
	path        string
	displayName string
	img         image.Image
	err         error
}

type imageDropSetupResult struct {
	target io.Closer
	err    error
}

type saveDirBrowseResult struct {
	dir string
	ok  bool
	err error
}

type mosaicHoverCacheKey struct {
	controller      *workflow.Controller
	point           editor.Point
	width           float64
	blockSize       int
	previewRevision uint64
}

func penModeLabel(mode editor.PenMode) string {
	switch mode {
	case editor.PenModeFreehand:
		return "자유형"
	case editor.PenModeRectangle:
		return "네모"
	default:
		return "직선"
	}
}

func nextPenMode(mode editor.PenMode) editor.PenMode {
	switch mode {
	case editor.PenModeStraight:
		return editor.PenModeFreehand
	case editor.PenModeFreehand:
		return editor.PenModeRectangle
	default:
		return editor.PenModeStraight
	}
}

func mosaicModeLabel(mode editor.MosaicMode) string {
	switch mode {
	case editor.MosaicModeFreehand:
		return "자유형"
	case editor.MosaicModeRectangle:
		return "네모"
	default:
		return "직선"
	}
}

func nextMosaicMode(mode editor.MosaicMode) editor.MosaicMode {
	switch mode {
	case editor.MosaicModeStraight:
		return editor.MosaicModeFreehand
	case editor.MosaicModeFreehand:
		return editor.MosaicModeRectangle
	default:
		return editor.MosaicModeStraight
	}
}

type textPreviewCache struct {
	base       *image.RGBA
	baseOp     paint.ImageOp
	layer      *image.RGBA
	layerOp    paint.ImageOp
	layerKey   editor.RenderCommand
	layerValid bool
}

type windowEventItem struct {
	event event.Event
	ack   chan struct{}
}

type windowUI struct {
	controller    *workflow.Controller
	theme         *material.Theme
	ctx           context.Context
	defaultShaper *text.Shaper
	shaperFont    editor.TextFontFamily
	shaperBold    bool

	newCaptureButton          widget.Clickable
	loadImageButton           widget.Clickable
	emptyCaptureButton        widget.Clickable
	emptyLoadImageButton      widget.Clickable
	penButton                 widget.Clickable
	penModeButton             widget.Clickable
	rectButton                widget.Clickable
	arrowButton               widget.Clickable
	mosaicButton              widget.Clickable
	mosaicModeButton          widget.Clickable
	highlightButton           widget.Clickable
	highlightModeButton       widget.Clickable
	textButton                widget.Clickable
	textBoldButton            widget.Clickable
	textAlignButton           widget.Clickable
	textColorButton           widget.Clickable
	textBackgroundButton      widget.Clickable
	textBackgroundColorButton widget.Clickable
	textBackgroundAlphaButton widget.Clickable
	textBorderButton          widget.Clickable
	textBorderColorButton     widget.Clickable
	textFontButton            widget.Clickable
	textFontItems             [5]widget.Clickable
	textSizeEditor            widget.Editor
	outputBorderButton        widget.Clickable
	copyButton                widget.Clickable
	saveButton                widget.Clickable
	settingsButton            widget.Clickable
	settingsCloseButton       widget.Clickable
	captureModeButton         widget.Clickable
	captureModeItems          [5]widget.Clickable
	hotkeyButton              widget.Clickable
	hotkeyActionButton        widget.Clickable
	rememberRegionButton      widget.Clickable
	savedRegionButton         widget.Clickable
	savedRegionSelectButtons  []widget.Clickable
	savedRegionDeleteButtons  []widget.Clickable
	editAfterCaptureButton    widget.Clickable
	hideMinButton             widget.Clickable
	hideFocusButton           widget.Clickable
	autoSaveButton            widget.Clickable
	saveDirEditor             widget.Editor
	saveDirApplyButton        widget.Clickable
	saveDirBrowseButton       widget.Clickable
	strokeColorButton         widget.Clickable
	strokeWidthButton         widget.Clickable
	paletteButtons            [18]widget.Clickable
	borderPaletteButtons      [18]widget.Clickable
	rgbEditor                 widget.Editor
	rgbApplyButton            widget.Clickable
	borderRGBEditor           widget.Editor
	borderRGBApplyButton      widget.Clickable
	strokePickerButton        widget.Clickable
	borderPickerButton        widget.Clickable
	colorPickerCloseButton    widget.Clickable
	colorPickerButtons        [48]widget.Clickable
	strokeWidthSlider         widget.Float
	borderWidthSlider         widget.Float
	highlightWidthSlider      widget.Float
	highlightAlphaSlider      widget.Float
	mosaicBlockSizeSlider     widget.Float
	mosaicWidthSlider         widget.Float
	settingsList              widget.List
	strokeSettingsDirty       bool
	borderSettingsDirty       bool
	highlightSettingsDirty    bool
	mosaicSettingsDirty       bool
	rgbInitialized            bool
	borderRGBInitialized      bool
	colorPickerOpen           bool
	colorPickerTarget         string
	saveDirInitialized        bool

	canvasTag           inputTag
	keyTag              inputTag
	colorPickerBlockTag inputTag
	settingsBlockTag    inputTag
	textMoveTag         inputTag
	textResizeTag       inputTag

	status                  string
	toastTitle              string
	toastMessage            string
	toastUntil              time.Time
	autoCopyPending         bool
	appConfig               config.Config
	configPath              string
	showSettings            bool
	showModeDropdown        bool
	hotkeyCaptureMode       bool
	hotkeyCapturePressed    []string
	hotkeyCaptureEventKeys  []string
	hotkeyCaptureNativeKeys []string
	hotkeyCaptureCandidate  hotkey.Binding
	hotkeyCaptureHasValue   bool
	hotkeyCaptureInvalid    bool
	hotkeyCaptureSuppressed atomic.Bool
	hotkeyEvents            chan struct{}
	registrar               *hotkey.GlobalRegistrar
	captureHotkey           func()
	rebindHotkey            func(hotkey.Binding, func()) error
	saveConfig              func(config.Config) error
	copyPNG                 func() error
	trayHidden              bool
	hasFocused              bool
	trayAvailable           bool
	suppressFocusLossUntil  time.Time
	hideToTrayOnEscape      func()

	overlayActive           bool
	overlayResults          chan overlayResult
	captureActive           bool
	captureResults          chan captureStartResult
	scrollResults           chan scrollCaptureResult
	imageLoadActive         bool
	imageLoadGeneration     uint64
	imageLoadResults        chan imageLoadResult
	pendingCapture          bool
	pendingUseSavedRegion   bool
	pendingImageLoadPath    string
	saveDirBrowseActive     bool
	saveDirBrowseResults    chan saveDirBrowseResult
	dropHover               bool
	readClipboardImage      func() ([]byte, error)
	pickFolderPath          func(string, string) (string, bool, error)
	pickImageSavePath       func(string) (string, bool, error)
	restoreWindow           func(string) error
	hideWindow              func(string) error
	windowVisible           func(string) bool
	flushDesktop            func()
	captureSnapshot         func() (workflow.CaptureSnapshot, error)
	sourceImagePath         string
	fileDropEvents          chan platformwindows.FileDropEvent
	imageDropSetupResults   chan imageDropSetupResult
	imageDropTarget         io.Closer
	imageDropAttempted      bool
	registerImageDropTarget func(*app.Window, uintptr, chan<- platformwindows.FileDropEvent) (io.Closer, error)
	freeOSMemory            func()
	notify                  func(string, string) error
	filmstrip               *filmstripState
	windowTargets           []platformwindows.WindowTarget

	dragging                 bool
	dragStart                editor.Point
	dragNow                  editor.Point
	penPoints                []editor.Point
	activeTool               editor.Tool
	penMode                  editor.PenMode
	mosaicMode               editor.MosaicMode
	highlightStraight        bool
	textEditor               widget.Editor
	textEditing              bool
	textEditingID            string
	textEditingRect          editor.Rect
	textEditingStyle         editor.TextStyle
	textEditingRuns          []editor.TextRun
	textEditUndo             []textEditSnapshot
	textEditRedo             []textEditSnapshot
	textEditCoalescing       bool
	textEditCoalesceStart    int
	textEditCoalesceEnd      int
	textSizeWheel            float32
	textEditorNeedsFocus     bool
	textFontDropdownOpen     bool
	textTransforming         bool
	textTransformMode        textTransformMode
	textTransformStart       f32.Point
	textTransformStartScale  float64
	textTransformStartRect   editor.Rect
	textTransformPreviewRect editor.Rect
	canvasPointer            editor.Point
	canvasPointerView        editor.Point
	canvasPointerOver        bool
	canvasPointerIn          bool
	mosaicHoverKey           mosaicHoverCacheKey
	mosaicHoverImage         *image.RGBA
	canvasImage              *image.RGBA
	canvasImageOp            paint.ImageOp
	canvasMip                canvasMipCache
	textPreview              textPreviewCache

	canvasZoom       float64
	canvasPan        image.Point
	canvasSourceSize image.Point
	// Canvas size the current zoom was auto-fitted for; zero once the user zooms or pans.
	canvasFitBounds  image.Point
	canvasPanning    bool
	brushAdjust      brushAdjustState
	canvasPanStart   image.Point
	canvasPanPointer editor.Point
	canvasPanCenter  image.Point
	canvasPanSize    image.Point
	undoStack        []uiEditHistoryEntry
	redoStack        []uiEditHistoryEntry

	cropDragging              bool
	rotation                  rotationGesture
	transformButtons          [6]widget.Clickable
	transformCounterclockwise bool
	cropHoverHandle           cropHandle
	cropDragHandle            cropHandle
	cropStartRect             image.Rectangle
	cropPreviewRect           image.Rectangle
	cropMagnifierFrame        cropMagnifierFrame
}

func RunMainWindow(ctx context.Context) error {
	w := new(app.Window)
	w.Option(
		app.Title(windowTitle),
		app.Size(unit.Dp(1100), unit.Dp(760)),
		app.MinSize(unit.Dp(900), unit.Dp(620)),
	)

	cfg, cfgErr := config.Load()
	cfgPath, _ := config.Path()
	if binding, ok := hotkey.ParseBinding(cfg.Hotkey); !ok {
		cfg.Hotkey = hotkey.DefaultBinding().Name
	} else {
		cfg.Hotkey = binding.Name
	}
	if !validCaptureMode(cfg.CaptureMode) {
		cfg.CaptureMode = "Region"
	}
	initialStatus := fmt.Sprintf("%s 또는 지금 캡처를 눌러 시작하세요.", cfg.Hotkey)
	if cfg.HotkeyOpensWindow {
		initialStatus = fmt.Sprintf("%s로 프로그램 화면을 열거나 지금 캡처를 눌러 시작하세요.", cfg.Hotkey)
	}

	theme := newTheme()
	ui := &windowUI{
		controller:             NewDefaultController(),
		theme:                  theme,
		ctx:                    ctx,
		defaultShaper:          theme.Shaper,
		status:                 initialStatus,
		appConfig:              cfg,
		configPath:             cfgPath,
		saveConfig:             config.Save,
		suppressFocusLossUntil: time.Now().Add(2 * time.Second),
		overlayResults:         make(chan overlayResult, 1),
		captureResults:         make(chan captureStartResult, 1),
		scrollResults:          make(chan scrollCaptureResult, 1),
		imageLoadResults:       make(chan imageLoadResult, 1),
		saveDirBrowseResults:   make(chan saveDirBrowseResult, 1),
		fileDropEvents:         make(chan platformwindows.FileDropEvent, 8),
		imageDropSetupResults:  make(chan imageDropSetupResult, 1),
		freeOSMemory:           debug.FreeOSMemory,
		activeTool:             editor.ToolRectangle,
		highlightStraight:      true,
		shaperFont:             editor.TextFontMalgun,
	}
	ui.textEditor.MaxLen = 4000
	ui.textEditor.WrapPolicy = text.WrapGraphemes
	ui.configureTextSizeEditor()
	ui.filmstrip = newFilmstripState(ctx)
	ui.requestFilmstripRefresh()
	ui.applyStrokeStyle()
	ui.strokeWidthSlider.Value = widthToSlider(ui.appConfig.StrokeWidth)
	ui.borderWidthSlider.Value = outputBorderWidthToSlider(ui.appConfig.OutputBorderWidth)
	ui.highlightWidthSlider.Value = highlighterWidthToSlider(ui.appConfig.HighlighterWidth)
	ui.highlightAlphaSlider.Value = highlighterAlphaToSlider(ui.appConfig.HighlighterAlpha)
	ui.mosaicBlockSizeSlider.Value = mosaicBlockSizeToSlider(ui.appConfig.MosaicBlockSize)
	ui.mosaicWidthSlider.Value = mosaicWidthToSlider(ui.appConfig.MosaicWidth)
	ui.settingsList.List.Axis = layout.Vertical
	if cfgErr != nil {
		ui.status = "설정 로드 실패, 기본값을 사용합니다: " + cfgErr.Error()
	}

	hotkeyEvents := make(chan struct{}, 1)
	ui.hotkeyEvents = hotkeyEvents
	focusLossEvents := make(chan struct{}, 1)
	rawTrayEvents := make(chan tray.Event, 4)
	trayEvents := make(chan tray.Event, 4)
	trayIcon, trayErr := tray.Start(rawTrayEvents)
	if trayErr != nil {
		ui.status = "트레이 초기화 실패: " + trayErr.Error()
	} else {
		ui.trayAvailable = true
		ui.notify = trayIcon.Notify
		ui.hideToTrayOnEscape = func() {
			ui.hideToTray("Esc 키로 트레이에 숨겼습니다.", w)
		}
		defer trayIcon.Stop()
		go func() {
			for event := range rawTrayEvents {
				if event == tray.EventShow {
					externalRestoreWindow()
				}
				select {
				case trayEvents <- event:
				default:
				}
				w.Invalidate()
			}
		}()
	}
	go func() {
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				select {
				case focusLossEvents <- struct{}{}:
				default:
				}
			}
		}
	}()

	registrar := hotkey.NewGlobalRegistrar()
	defer registrar.Close()
	ui.registrar = registrar
	ui.rebindHotkey = registrar.RebindCapture
	ui.captureHotkey = func() {
		if ui.hotkeyCaptureSuppressed.Load() {
			return
		}
		select {
		case hotkeyEvents <- struct{}{}:
		default:
		}
		w.Invalidate()
	}
	binding, _ := hotkey.ParseBinding(ui.appConfig.Hotkey)
	if err := registrar.RegisterCaptureBinding(binding, ui.captureHotkey); err != nil {
		ui.status = "전역 단축키 등록 실패: " + err.Error()
	}
	windowEvents := forwardWindowEvents(ctx, w)
	var ops op.Ops
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hotkeyEvents:
			if ui.textEditing && !ui.appConfig.HotkeyOpensWindow {
				ui.colorPickerOpen = false
				ui.pendingCapture = true
				_, ui.pendingUseSavedRegion = ui.activeSavedRegion()
				w.Invalidate()
			} else {
				ui.handleGlobalHotkey(w)
			}
		case result := <-ui.captureResults:
			ui.handleCaptureStartResult(result, w)
		case result := <-ui.scrollResults:
			ui.handleScrollCaptureResult(result, w)
		case result := <-ui.imageLoadResults:
			ui.handleImageLoadResult(result, w)
		case result := <-ui.saveDirBrowseResults:
			ui.handleSaveDirBrowseResult(result)
			w.Invalidate()
		case dropEvent := <-ui.fileDropEvents:
			ui.handleFileDropEvent(dropEvent)
			w.Invalidate()
		case result := <-ui.imageDropSetupResults:
			if result.err != nil {
				ui.status = "파일 드롭 초기화 실패: " + result.err.Error()
			} else {
				ui.imageDropTarget = result.target
			}
			w.Invalidate()
		case result := <-ui.filmstrip.scanResults:
			ui.filmstrip.applyScanResult(result)
			if result.generation == ui.filmstrip.generation && result.err != nil {
				ui.status = "이전 캡처 목록을 읽지 못했습니다: " + result.err.Error()
			}
			w.Invalidate()
		case result := <-ui.filmstrip.thumbResults:
			ui.filmstrip.applyThumbnailResult(result)
			w.Invalidate()
		case result := <-ui.filmstrip.hoverResults:
			ui.filmstrip.applyHoverPreviewResult(result)
			w.Invalidate()
		case result := <-ui.filmstrip.recycleResults:
			ui.handleFilmstripRecycleResult(result)
			w.Invalidate()
		case result := <-ui.filmstrip.folderResults:
			ui.handleFilmstripFolderResult(result)
			w.Invalidate()
		case <-focusLossEvents:
			if uShouldHideForFocusLoss(ui) {
				ui.hideToTray("포커스를 잃어 트레이로 숨겼습니다.", w)
			}
		case event := <-trayEvents:
			switch event {
			case tray.EventShow:
				ui.restoreFromTray(w)
			case tray.EventQuit:
				return nil
			}
		case result := <-ui.overlayResults:
			ui.handleOverlayResult(result, w)
		case item := <-windowEvents:
			shouldReturn, err := ui.handleWindowEvent(item.event, w, &ops)
			close(item.ack)
			if shouldReturn {
				return err
			}
		}
	}
}

func forwardWindowEvents(ctx context.Context, w *app.Window) <-chan windowEventItem {
	events := make(chan windowEventItem, 1)
	go func() {
		for {
			e := w.Event()
			item := windowEventItem{event: e, ack: make(chan struct{})}
			select {
			case events <- item:
			case <-ctx.Done():
				return
			}
			select {
			case <-item.ack:
			case <-ctx.Done():
				return
			}
			if _, ok := e.(app.DestroyEvent); ok {
				return
			}
		}
	}()
	return events
}

func (u *windowUI) handleWindowEvent(e event.Event, w *app.Window, ops *op.Ops) (bool, error) {
	if !u.imageDropAttempted {
		if hwnd := platformwindows.NativeWindowHandle(e); hwnd != 0 {
			u.imageDropAttempted = true
			u.beginImageDropSetup(w, hwnd)
		}
	}
	switch e := e.(type) {
	case app.DestroyEvent:
		return true, e.Err
	case app.ConfigEvent:
		u.handleConfigEvent(e, w)
	case app.FrameEvent:
		gtx := app.NewContext(ops, e)
		u.layout(gtx, w)
		e.Frame(gtx.Ops)
	}
	return false, nil
}

func (u *windowUI) beginImageDropSetup(w *app.Window, hwnd uintptr) {
	register := u.registerImageDropTarget
	if register == nil {
		register = platformwindows.RegisterImageDropTarget
	}
	go func() {
		target, err := register(w, hwnd, u.fileDropEvents)
		u.imageDropSetupResults <- imageDropSetupResult{target: target, err: err}
	}()
}

func newTheme() *material.Theme {
	theme := material.NewTheme()
	theme.Face = font.Typeface("Malgun Gothic")
	theme.Shaper = koreanTextShaper(editor.TextFontMalgun, false)
	theme.Palette.Bg = color.NRGBA{R: 17, G: 17, B: 20, A: 255}
	theme.Palette.Fg = color.NRGBA{R: 245, G: 245, B: 247, A: 255}
	theme.Palette.ContrastBg = color.NRGBA{R: 10, G: 132, B: 255, A: 255}
	theme.Palette.ContrastFg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	return theme
}

func koreanTextShaper(family editor.TextFontFamily, bold bool) *text.Shaper {
	faces := append([]font.FontFace{}, gofont.Collection()...)
	for _, path := range koreanFontPaths(family, bold) {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		parsed, err := opentype.ParseCollection(data)
		if err != nil {
			continue
		}
		faces = append(faces, parsed...)
	}
	return text.NewShaper(text.WithCollection(faces))
}

func koreanFontPaths(family editor.TextFontFamily, bold bool) []string {
	windir := os.Getenv("WINDIR")
	if windir == "" {
		windir = `C:\Windows`
	}
	fonts := filepath.Join(windir, "Fonts")
	paths := []string{filepath.Join(fonts, "malgun.ttf")}
	switch editor.NormalizeTextFontFamily(family) {
	case editor.TextFontGulim, editor.TextFontDotum:
		paths = append(paths, filepath.Join(fonts, "gulim.ttc"))
	case editor.TextFontBatang, editor.TextFontGungsuh:
		paths = append(paths, filepath.Join(fonts, "batang.ttc"))
	case editor.TextFontMalgun:
		if bold {
			paths = append(paths, filepath.Join(fonts, "malgunbd.ttf"))
		}
	}
	return paths
}

func NewDefaultController() *workflow.Controller {
	return workflow.NewController(capture.ScreenshotCapturer{})
}

func (u *windowUI) layout(gtx layout.Context, w *app.Window) layout.Dimensions {
	u.ensureTextShaper()
	paint.Fill(gtx.Ops, color.NRGBA{R: 10, G: 10, B: 12, A: 255})
	u.cropMagnifierFrame = cropMagnifierFrame{}
	if u.colorPickerOpen {
		event.Op(gtx.Ops, &u.keyTag)
		gtx.Execute(key.FocusCmd{Tag: &u.keyTag})
		for {
			e, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Name: key.NameEscape})
			if !ok {
				break
			}
			if ke, ok := e.(key.Event); ok && ke.State == key.Press {
				u.colorPickerOpen = false
				u.textEditorNeedsFocus = u.textEditing
				u.hideForEscape()
			}
		}
	} else if u.hotkeyCaptureMode {
		event.Op(gtx.Ops, &u.keyTag)
		gtx.Execute(key.FocusCmd{Tag: &u.keyTag})
		u.handleHotkeyCapture(gtx)
		if u.hotkeyCaptureMode {
			u.syncNativeHotkeyCaptureKeys(platformwindows.PressedSpecialShortcutKeys())
		}
		if u.hotkeyCaptureMode {
			gtx.Execute(op.InvalidateCmd{At: time.Now().Add(16 * time.Millisecond)})
		}
	} else if u.showSettings {
		event.Op(gtx.Ops, &u.keyTag)
		gtx.Execute(key.FocusCmd{Tag: &u.keyTag})
		for {
			e, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Name: key.NameEscape})
			if !ok {
				break
			}
			if ke, ok := e.(key.Event); ok && ke.State == key.Press {
				u.showSettings = false
				u.hideForEscape()
			}
		}
	} else if u.textEditing {
		u.handleTextEditingKeys(gtx)
	} else if !u.showModeDropdown {
		event.Op(gtx.Ops, &u.keyTag)
		gtx.Execute(key.FocusCmd{Tag: &u.keyTag})
		u.handleShortcuts(gtx)
	}
	u.handlePendingExternalActions(w)
	u.handleButtons(gtx, w)

	toolbarHeight := 0
	dropdownHeight := 0
	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			dims := u.layoutToolbar(gtx)
			toolbarHeight = dims.Size.Y
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !u.showModeDropdown {
				return layout.Dimensions{}
			}
			dims := u.layoutModeDropdown(gtx)
			dropdownHeight = dims.Size.Y
			return dims
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.layoutWorkspace(gtx, w)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutTransformControls(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutStatusBar(gtx)
		}),
	)
	u.layoutCropMagnifierOverlay(gtx, toolbarHeight, toolbarHeight+dropdownHeight)
	u.layoutFilmstripHoverPreview(gtx)
	u.layoutToast(gtx)
	u.layoutSettingsModal(gtx)
	u.layoutColorPickerModal(gtx)
	u.layoutDropOverlay(gtx)
	return dims
}

func (u *windowUI) ensureTextShaper() {
	if u.theme == nil {
		return
	}
	if u.defaultShaper == nil && (u.shaperFont == "" || u.shaperFont == editor.TextFontMalgun) && !u.shaperBold {
		u.defaultShaper = u.theme.Shaper
	}
	family := editor.TextFontMalgun
	bold := false
	if u.activeTool == editor.ToolText {
		family = editor.EffectiveRichTextFontFamily(u.textEditingStyle, u.textEditingRuns)
		bold = editor.TextRunsUseBold(u.textEditingStyle.Bold, u.textEditingRuns)
	}
	if u.shaperFont == "" && family == editor.TextFontMalgun && !bold {
		u.shaperFont = editor.TextFontMalgun
		return
	}
	if u.shaperFont == family && u.shaperBold == bold {
		return
	}
	if family == editor.TextFontMalgun && !bold && u.defaultShaper != nil {
		u.theme.Shaper = u.defaultShaper
		u.shaperFont = family
		u.shaperBold = false
		return
	}
	u.theme.Shaper = koreanTextShaper(family, bold)
	u.shaperFont = family
	u.shaperBold = bold
}

func (u *windowUI) handleFileDropEvent(event platformwindows.FileDropEvent) {
	switch event.Kind {
	case platformwindows.FileDropEnter:
		u.dropHover = true
	case platformwindows.FileDropLeave:
		u.dropHover = false
	case platformwindows.FileDropFiles:
		u.dropHover = false
		if len(event.Paths) > 0 {
			if u.textEditing {
				u.colorPickerOpen = false
				u.pendingImageLoadPath = event.Paths[0]
			} else {
				u.startImageLoad(event.Paths[0])
			}
		}
	}
}

func (u *windowUI) handlePendingExternalActions(w *app.Window) {
	if u.pendingCapture {
		u.pendingCapture = false
		useSavedRegion := u.pendingUseSavedRegion
		u.pendingUseSavedRegion = false
		u.beginCaptureRequest(w, u.appConfig.CaptureMode, false, useSavedRegion)
	}
	if u.pendingImageLoadPath != "" {
		path := u.pendingImageLoadPath
		u.pendingImageLoadPath = ""
		u.startImageLoad(path)
	}
}

func (u *windowUI) layoutDropOverlay(gtx layout.Context) {
	if !u.dropHover {
		return
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{R: 2, G: 12, B: 24, A: 218}, clip.Rect{Max: size}.Op())
	layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		cardSize := image.Pt(min(size.X-32, gtx.Dp(unit.Dp(460))), min(size.Y-32, gtx.Dp(unit.Dp(190))))
		gtx.Constraints = layout.Exact(cardSize)
		card := image.Rectangle{Max: cardSize}
		paint.FillShape(gtx.Ops, color.NRGBA{R: 18, G: 35, B: 55, A: 255}, clip.RRect{Rect: card, SE: 22, SW: 22, NW: 22, NE: 22}.Op(gtx.Ops))
		drawIconRect(gtx.Ops, image.Rect(1, 1, cardSize.X-1, cardSize.Y-1), color.NRGBA{R: 56, G: 189, B: 248, A: 255}, 3)
		return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.H5(u.theme, "이미지를 여기에 놓으세요")
					label.TextSize = unit.Sp(22)
					label.Color = color.NRGBA{R: 240, G: 249, B: 255, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body2(u.theme, "파일을 놓으면 바로 편집 대상으로 불러옵니다")
					label.TextSize = unit.Sp(13)
					label.Color = color.NRGBA{R: 125, G: 211, B: 252, A: 255}
					return label.Layout(gtx)
				}),
			)
		})
	})
}

func (u *windowUI) layoutToolbar(gtx layout.Context) layout.Dimensions {
	singleRow := gtx.Constraints.Max.X >= gtx.Dp(unit.Dp(toolbarSingleRowMinWidth))
	barHeight := toolbarHeight(gtx.Metric, singleRow)
	gtx.Constraints.Min.Y = barHeight
	gtx.Constraints.Max.Y = barHeight
	paint.FillShape(gtx.Ops, color.NRGBA{R: 24, G: 24, B: 28, A: 250}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 18}, clip.Rect{Min: image.Pt(0, barHeight-1), Max: image.Pt(gtx.Constraints.Max.X, barHeight)}.Op())
	return layout.Inset{Top: unit.Dp(4), Bottom: unit.Dp(4), Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		if singleRow {
			return u.layoutSingleRowToolbar(gtx)
		}
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(4))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutPrimaryToolbar(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutMarkupToolbar(gtx)
			}),
		)
	})
}

func toolbarHeight(metric unit.Metric, singleRow bool) int {
	pxPerDp := metric.PxPerDp
	if pxPerDp <= 0 {
		pxPerDp = 1
	}
	pxPerSp := metric.PxPerSp
	if pxPerSp <= 0 {
		pxPerSp = pxPerDp
	}
	dp := func(value float32) int { return int(math.Ceil(float64(value * pxPerDp))) }
	textLine := int(math.Ceil(float64(13 * pxPerSp * 1.5)))
	rowHeight := max(dp(48), textLine+dp(20))
	if singleRow {
		return rowHeight + dp(8)
	}
	return rowHeight*2 + dp(12)
}

func (u *windowUI) layoutSingleRowToolbar(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(6))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.captureModeButton, modeLabel(u.appConfig.CaptureMode)+"  ▾", u.showModeDropdown).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return accentButton(u.theme, &u.newCaptureButton, "새 캡처").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return neutralButton(u.theme, &u.loadImageButton, "이미지 열기").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.rectButton, u.shapeButtonTool())
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.iconToolButton(gtx, &u.penButton, editor.ToolPen) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.activeTool != editor.ToolPen {
				return layout.Dimensions{}
			}
			return toggleButton(u.theme, &u.penModeButton, penModeLabel(u.penMode), u.penMode != editor.PenModeStraight).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.arrowButton, editor.ToolArrow)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.mosaicButton, editor.ToolMosaic)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.activeTool != editor.ToolMosaic {
				return layout.Dimensions{}
			}
			return toggleButton(u.theme, &u.mosaicModeButton, mosaicModeLabel(u.mosaicMode), u.mosaicMode != editor.MosaicModeStraight).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.highlightButton, editor.ToolHighlight)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.activeTool != editor.ToolHighlight {
				return layout.Dimensions{}
			}
			return toggleButton(u.theme, &u.highlightModeButton, drawingModeLabel(u.highlightStraight), u.highlightStraight).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutTextToolButton(gtx)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.outputBorderButton, "외곽선 "+onOff(u.appConfig.OutputBorderEnabled), u.appConfig.OutputBorderEnabled).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.autoSaveButton, "자동 저장 "+onOff(u.appConfig.AutoSave), u.appConfig.AutoSave).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.layoutCopyButton(gtx) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.layoutSaveButton(gtx) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.settingsButton, "설정", u.showSettings).Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutPrimaryToolbar(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.captureModeButton, modeLabel(u.appConfig.CaptureMode)+"  ▾", u.showModeDropdown).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return accentButton(u.theme, &u.newCaptureButton, "새 캡처").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return neutralButton(u.theme, &u.loadImageButton, "이미지 열기").Layout(gtx)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutCopyButton(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutSaveButton(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.settingsButton, "설정", u.showSettings).Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutCopyButton(gtx layout.Context) layout.Dimensions {
	editing := u.controller != nil && u.controller.Mode == workflow.ModeEditing
	return enabledWidget(gtx, editing, func(gtx layout.Context) layout.Dimensions {
		style := neutralButton(u.theme, &u.copyButton, "복사")
		if !editing {
			style = disabledButton(u.theme, &u.copyButton, "복사")
		}
		return style.Layout(gtx)
	})
}

func (u *windowUI) layoutSaveButton(gtx layout.Context) layout.Dimensions {
	editing := u.controller != nil && u.controller.Mode == workflow.ModeEditing
	return enabledWidget(gtx, editing, func(gtx layout.Context) layout.Dimensions {
		style := neutralButton(u.theme, &u.saveButton, "저장")
		if !editing {
			style = disabledButton(u.theme, &u.saveButton, "저장")
		}
		return style.Layout(gtx)
	})
}

func (u *windowUI) layoutMarkupToolbar(gtx layout.Context) layout.Dimensions {
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(7))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Right: unit.Dp(3)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, "마크업")
				label.TextSize = unit.Sp(12)
				label.Color = color.NRGBA{R: 160, G: 160, B: 170, A: 255}
				return label.Layout(gtx)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.rectButton, u.shapeButtonTool())
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.iconToolButton(gtx, &u.penButton, editor.ToolPen) }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.activeTool != editor.ToolPen {
				return layout.Dimensions{}
			}
			return toggleButton(u.theme, &u.penModeButton, penModeLabel(u.penMode), u.penMode != editor.PenModeStraight).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.arrowButton, editor.ToolArrow)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.mosaicButton, editor.ToolMosaic)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.activeTool != editor.ToolMosaic {
				return layout.Dimensions{}
			}
			return toggleButton(u.theme, &u.mosaicModeButton, mosaicModeLabel(u.mosaicMode), u.mosaicMode != editor.MosaicModeStraight).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.iconToolButton(gtx, &u.highlightButton, editor.ToolHighlight)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if u.activeTool != editor.ToolHighlight {
				return layout.Dimensions{}
			}
			return toggleButton(u.theme, &u.highlightModeButton, drawingModeLabel(u.highlightStraight), u.highlightStraight).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutTextToolButton(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Inset{Left: unit.Dp(5)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
				return layoutStatusPill(gtx, u.theme, u.activeToolLabel())
			})
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions { return layout.Dimensions{} }),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.outputBorderButton, "결과 외곽선 "+onOff(u.appConfig.OutputBorderEnabled), u.appConfig.OutputBorderEnabled).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.autoSaveButton, "자동 저장 "+onOff(u.appConfig.AutoSave), u.appConfig.AutoSave).Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutStatusBar(gtx layout.Context) layout.Dimensions {
	height := statusBarHeight(gtx.Metric)
	gtx.Constraints.Min.Y = height
	gtx.Constraints.Max.Y = height
	paint.FillShape(gtx.Ops, color.NRGBA{R: 20, G: 20, B: 24, A: 252}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 16}, clip.Rect{Max: image.Pt(gtx.Constraints.Max.X, 1)}.Op())
	return layout.Inset{Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				dot := gtx.Dp(unit.Dp(7))
				paint.FillShape(gtx.Ops, statusIndicatorColor(u.status), clip.RRect{Rect: image.Rect(0, 0, dot, dot), SE: dot, SW: dot, NW: dot, NE: dot}.Op(gtx.Ops))
				return layout.Dimensions{Size: image.Pt(dot, dot)}
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, u.status)
				label.TextSize = unit.Sp(12)
				label.MaxLines = 1
				label.Color = color.NRGBA{R: 205, G: 205, B: 214, A: 255}
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				text := u.appConfig.Hotkey + "  " + u.hotkeyActionValueLabel()
				if u.controller != nil && u.controller.Mode == workflow.ModeEditing {
					text = fmt.Sprintf("%s  ·  %.0f%%  ·  Ctrl+휠 확대  ·  가운데 드래그 이동  ·  Alt+우클릭 드래그 브러시 조절", u.activeToolLabel(), u.canvasZoom*100)
				}
				label := material.Body2(u.theme, text)
				label.TextSize = unit.Sp(11)
				label.Color = color.NRGBA{R: 126, G: 126, B: 138, A: 255}
				return label.Layout(gtx)
			}),
		)
	})
}

func statusBarHeight(metric unit.Metric) int {
	pxPerDp := metric.PxPerDp
	if pxPerDp <= 0 {
		pxPerDp = 1
	}
	pxPerSp := metric.PxPerSp
	if pxPerSp <= 0 {
		pxPerSp = pxPerDp
	}
	return max(int(math.Ceil(float64(36*pxPerDp))), int(math.Ceil(float64(12*pxPerSp*1.5)))+int(math.Ceil(float64(14*pxPerDp))))
}

func statusIndicatorColor(status string) color.NRGBA {
	if strings.Contains(status, "실패") || strings.Contains(status, "오류") {
		return color.NRGBA{R: 255, G: 69, B: 58, A: 255}
	}
	if strings.Contains(status, "중") || strings.Contains(status, "진행") {
		return color.NRGBA{R: 255, G: 214, B: 10, A: 255}
	}
	return color.NRGBA{R: 48, G: 209, B: 88, A: 255}
}

func (u *windowUI) layoutToast(gtx layout.Context) {
	if u.toastMessage == "" || time.Now().After(u.toastUntil) {
		return
	}
	gtx.Execute(op.InvalidateCmd{At: u.toastUntil})
	detailed := u.toastTitle != "" && u.toastTitle != u.toastMessage
	toastSize := compactToastSize(gtx, detailed)
	width := toastSize.X
	height := toastSize.Y
	x := gtx.Constraints.Max.X - width - gtx.Dp(unit.Dp(12))
	if x < 0 {
		x = 0
	}
	y := gtx.Constraints.Max.Y - height - gtx.Dp(unit.Dp(48))
	if y < gtx.Dp(unit.Dp(8)) {
		y = gtx.Dp(unit.Dp(8))
	}
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: image.Pt(width, height)}
	shadow := image.Rect(0, gtx.Dp(unit.Dp(3)), width, height+gtx.Dp(unit.Dp(3)))
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 90}, clip.RRect{Rect: shadow, SE: 12, SW: 12, NW: 12, NE: 12}.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, color.NRGBA{R: 38, G: 38, B: 44, A: 248}, clip.RRect{Rect: rect, SE: 11, SW: 11, NW: 11, NE: 11}.Op(gtx.Ops))
	accent := color.NRGBA{R: 10, G: 132, B: 255, A: 255}
	if toastIsError(u.toastTitle, u.toastMessage) {
		accent = color.NRGBA{R: 255, G: 69, B: 58, A: 255}
	}
	paint.FillShape(gtx.Ops, accent, clip.RRect{Rect: image.Rect(0, 0, gtx.Dp(unit.Dp(4)), height), SE: 2, SW: 2, NW: 2, NE: 2}.Op(gtx.Ops))
	layout.Inset{Top: unit.Dp(9), Bottom: unit.Dp(9), Left: unit.Dp(12), Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = width - gtx.Dp(unit.Dp(22))
		if !detailed {
			label := material.Body2(u.theme, u.toastMessage)
			label.TextSize = unit.Sp(12)
			label.MaxLines = 1
			label.Color = color.NRGBA{R: 245, G: 245, B: 247, A: 255}
			return label.Layout(gtx)
		}
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(2))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, u.toastTitle)
				label.TextSize = unit.Sp(12)
				label.MaxLines = 1
				label.Color = color.NRGBA{R: 248, G: 248, B: 250, A: 255}
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, u.toastMessage)
				label.TextSize = unit.Sp(10)
				label.MaxLines = 1
				label.Color = color.NRGBA{R: 180, G: 180, B: 190, A: 255}
				return label.Layout(gtx)
			}),
		)
	})
}

func compactToastSize(gtx layout.Context, detailed bool) image.Point {
	height := gtx.Dp(unit.Dp(44))
	if detailed {
		height = gtx.Dp(unit.Dp(64))
	}
	return image.Pt(min(gtx.Dp(unit.Dp(320)), gtx.Constraints.Max.X-gtx.Dp(unit.Dp(24))), height)
}

func (u *windowUI) showToast(message string) {
	u.toastTitle = ""
	u.toastMessage = message
	u.toastUntil = time.Now().Add(toastBriefDuration)
}

func (u *windowUI) showToastDetail(title, message string) {
	u.toastTitle = title
	u.toastMessage = message
	duration := toastDetailDuration
	if toastIsError(title, message) {
		duration = toastErrorDuration
	}
	u.toastUntil = time.Now().Add(duration)
}

func toastIsError(title, message string) bool {
	text := title + " " + message
	return strings.Contains(text, "실패") || strings.Contains(text, "오류")
}

func (u *windowUI) openColorPicker(target string) {
	u.colorPickerTarget = target
	u.colorPickerOpen = true
}

func (u *windowUI) layoutColorPickerModal(gtx layout.Context) {
	if !u.colorPickerOpen {
		return
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 175}, clip.Rect{Max: size}.Op())
	block := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &u.colorPickerBlockTag)
	block.Pop()
	for {
		_, ok := gtx.Event(pointer.Filter{
			Target: &u.colorPickerBlockTag,
			Kinds:  pointer.Press | pointer.Release | pointer.Drag | pointer.Move | pointer.Scroll,
		})
		if !ok {
			break
		}
	}
	card := image.Pt(min(size.X-48, 520), min(size.Y-48, 430))
	if card.X < 320 {
		card.X = 320
	}
	if card.Y < 260 {
		card.Y = 260
	}
	pos := image.Pt((size.X-card.X)/2, (size.Y-card.Y)/2)
	defer op.Offset(pos).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: card}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 31, G: 31, B: 36, A: 255}, clip.RRect{Rect: rect, SE: 20, SW: 20, NW: 20, NE: 20}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, image.Rect(1, 1, card.X-1, card.Y-1), color.NRGBA{R: 255, G: 255, B: 255, A: 28}, 1)
	gtx.Constraints.Min = card
	gtx.Constraints.Max = card
	layout.UniformInset(unit.Dp(18)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		title := "선 색상 선택"
		if u.colorPickerTarget == "border" {
			title = "결과 외곽선 색 선택"
		} else if u.colorPickerTarget == "text" {
			title = "텍스트 색상 선택"
		} else if u.colorPickerTarget == "text-background" {
			title = "텍스트 배경색 선택"
		} else if u.colorPickerTarget == "text-border" {
			title = "텍스트 외곽선 색 선택"
		}
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(12))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						label := material.H6(u.theme, title)
						label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
						return label.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return neutralButton(u.theme, &u.colorPickerCloseButton, "닫기").Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, "색을 클릭하면 바로 적용됩니다. RGB 숫자 입력 없이 선택할 수 있습니다.")
				label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutColorPickerGrid(gtx)
			}),
		)
	})
}

func (u *windowUI) layoutColorPickerGrid(gtx layout.Context) layout.Dimensions {
	palette := colorPickerPalette()
	rows := make([]layout.FlexChild, 0, 6)
	for row := 0; row < 6; row++ {
		row := row
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			children := make([]layout.FlexChild, 0, 8)
			for col := 0; col < 8; col++ {
				idx := row*8 + col
				if idx >= len(palette) {
					break
				}
				buttonIdx := idx
				children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.colorPickerSwatch(gtx, &u.colorPickerButtons[buttonIdx], palette[buttonIdx].rgba)
				}))
			}
			return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx, children...)
		}))
	}
	return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx, rows...)
}

func (u *windowUI) colorPickerSwatch(gtx layout.Context, button *widget.Clickable, rgba [4]uint8) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(48)), gtx.Dp(unit.Dp(40)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		semantic.LabelOp(fmt.Sprintf("색상 #%02X%02X%02X", rgba[0], rgba[1], rgba[2])).Add(gtx.Ops)
		inset := 2
		if button.Pressed() {
			inset = 4
		}
		rect := image.Rect(inset, inset, size.X-inset, size.Y-inset)
		paint.FillShape(gtx.Ops, color.NRGBA{R: rgba[0], G: rgba[1], B: rgba[2], A: rgba[3]}, clip.RRect{Rect: rect, SE: 9, SW: 9, NW: 9, NE: 9}.Op(gtx.Ops))
		drawIconRect(gtx.Ops, rect, color.NRGBA{R: 255, G: 255, B: 255, A: 45}, 1)
		current := strokeColor(u.appConfig.StrokeColor)
		if u.colorPickerTarget == "border" {
			current = strokeColor(u.appConfig.OutputBorderColor)
		} else if u.colorPickerTarget == "text" {
			current = u.textEditingStyle.ColorRGBA
		} else if u.colorPickerTarget == "text-background" {
			current = u.textEditingStyle.BackgroundRGBA
		} else if u.colorPickerTarget == "text-border" {
			current = u.textEditingStyle.BorderRGBA
		}
		if colorPickerRGBASelected(u.colorPickerTarget, current, rgba) {
			semantic.SelectedOp(true).Add(gtx.Ops)
			drawIconRect(gtx.Ops, image.Rect(1, 1, size.X-1, size.Y-1), color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 2.5)
		}
		return layout.Dimensions{Size: size}
	})
}

func colorPickerRGBASelected(target string, current, candidate [4]uint8) bool {
	if target == "text-background" {
		return current[0] == candidate[0] && current[1] == candidate[1] && current[2] == candidate[2]
	}
	return current == candidate
}

func (u *windowUI) layoutModeDropdown(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, color.NRGBA{R: 28, G: 28, B: 33, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(180), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		modes := []string{"Region", "Window", "Scroll", "Freeform", "Fullscreen"}
		children := make([]layout.FlexChild, 0, len(modes))
		for i := range modes {
			idx := i
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.captureModeItems[idx], modeLabel(modes[idx]), u.appConfig.CaptureMode == modes[idx]).Layout(gtx)
			}))
		}
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx, children...)
	})
}

func (u *windowUI) layoutSettingsModal(gtx layout.Context) {
	if !u.showSettings {
		return
	}
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 180}, clip.Rect{Max: size}.Op())
	block := clip.Rect{Max: size}.Push(gtx.Ops)
	event.Op(gtx.Ops, &u.settingsBlockTag)
	block.Pop()
	for {
		_, ok := gtx.Event(pointer.Filter{Target: &u.settingsBlockTag, Kinds: pointer.Press | pointer.Release | pointer.Drag | pointer.Move | pointer.Scroll})
		if !ok {
			break
		}
	}
	card := settingsModalCardSize(gtx)
	position := image.Pt((size.X-card.X)/2, (size.Y-card.Y)/2)
	defer op.Offset(position).Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 30, G: 30, B: 36, A: 255}, clip.RRect{Rect: image.Rectangle{Max: card}, SE: 22, SW: 22, NW: 22, NE: 22}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, image.Rect(1, 1, card.X-1, card.Y-1), color.NRGBA{R: 255, G: 255, B: 255, A: 30}, 1)
	gtx.Constraints = layout.Exact(card)
	layout.UniformInset(unit.Dp(18)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(14))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle}.Layout(gtx,
					layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
						label := material.H6(u.theme, "설정")
						label.TextSize = unit.Sp(20)
						label.Color = color.NRGBA{R: 245, G: 245, B: 247, A: 255}
						return label.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return neutralButton(u.theme, &u.settingsCloseButton, "닫기").Layout(gtx)
					}),
				)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				return u.layoutSettings(gtx)
			}),
		)
	})
}

func settingsModalCardSize(gtx layout.Context) image.Point {
	margin := gtx.Dp(unit.Dp(24))
	available := image.Pt(max(0, gtx.Constraints.Max.X-margin*2), max(0, gtx.Constraints.Max.Y-margin*2))
	return image.Pt(min(available.X, gtx.Dp(unit.Dp(980))), min(available.Y, gtx.Dp(unit.Dp(540))))
}

func (u *windowUI) layoutSettings(gtx layout.Context) layout.Dimensions {
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
	return material.List(u.theme, &u.settingsList).Layout(gtx, len(sections), func(gtx layout.Context, index int) layout.Dimensions {
		section := sections[index]
		return layout.Inset{Bottom: unit.Dp(10), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return u.layoutSettingsSection(gtx, section.title, section.content)
		})
	})
}

func (u *windowUI) layoutCaptureSettings(gtx layout.Context) layout.Dimensions {
	u.ensureSavedRegionButtons()
	controls := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return neutralButton(u.theme, &u.rememberRegionButton, "화면 좌표 추가").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return enabledWidget(gtx, len(u.appConfig.SavedRegions) > 0, func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.savedRegionButton, "기억 좌표 사용: "+onOff(u.appConfig.SavedRegionEnabled), u.appConfig.SavedRegionEnabled).Layout(gtx)
			})
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.editAfterCaptureButton, "캡처 후 편집: "+onOff(u.appConfig.EditAfterCapture), u.appConfig.EditAfterCapture).Layout(gtx)
		}),
	}
	axis := layout.Horizontal
	if settingsPreviewStacked(gtx) {
		axis = layout.Vertical
	}
	return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: axis, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx, controls...)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if len(u.appConfig.SavedRegions) == 0 {
				label := material.Body2(u.theme, "저장된 좌표 없음")
				label.TextSize = unit.Sp(12)
				label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
				return label.Layout(gtx)
			}
			rows := make([]layout.FlexChild, 0, len(u.appConfig.SavedRegions))
			for i := range u.appConfig.SavedRegions {
				index := i
				rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					preset := u.appConfig.SavedRegions[index]
					active := preset.ID == u.appConfig.ActiveSavedRegionID
					return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
						layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
							return toggleButton(u.theme, &u.savedRegionSelectButtons[index], preset.Name+" · "+savedRegionLabel(preset.Region), active).Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return neutralButton(u.theme, &u.savedRegionDeleteButtons[index], "삭제").Layout(gtx)
						}),
					)
				}))
			}
			return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(6))}.Layout(gtx, rows...)
		}),
	)
}

func (u *windowUI) ensureSavedRegionButtons() {
	count := len(u.appConfig.SavedRegions)
	if len(u.savedRegionSelectButtons) != count {
		u.savedRegionSelectButtons = make([]widget.Clickable, count)
		u.savedRegionDeleteButtons = make([]widget.Clickable, count)
	}
}

func savedRegionLabel(region config.ScreenRegion) string {
	rect := region.Rect()
	if rect.Empty() {
		return "기억된 좌표 없음"
	}
	return fmt.Sprintf("(%d, %d) · %dx%d", rect.Min.X, rect.Min.Y, rect.Dx(), rect.Dy())
}

func (u *windowUI) activeSavedRegion() (config.SavedRegionPreset, bool) {
	if !u.appConfig.SavedRegionEnabled {
		return config.SavedRegionPreset{}, false
	}
	for _, preset := range u.appConfig.SavedRegions {
		if preset.ID == u.appConfig.ActiveSavedRegionID && !preset.Region.Rect().Empty() {
			return preset, true
		}
	}
	return config.SavedRegionPreset{}, false
}

func (u *windowUI) layoutSettingsSection(gtx layout.Context, title string, content layout.Widget) layout.Dimensions {
	recording := op.Record(gtx.Ops)
	dims := layout.UniformInset(unit.Dp(14)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(u.theme, title)
				label.TextSize = unit.Sp(14)
				label.Font.Weight = font.SemiBold
				label.Color = color.NRGBA{R: 235, G: 235, B: 240, A: 255}
				return label.Layout(gtx)
			}),
			layout.Rigid(content),
		)
	})
	call := recording.Stop()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 38, G: 38, B: 44, A: 255}, clip.RRect{Rect: image.Rectangle{Max: dims.Size}, SE: 14, SW: 14, NW: 14, NE: 14}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, image.Rect(1, 1, dims.Size.X-1, dims.Size.Y-1), color.NRGBA{R: 255, G: 255, B: 255, A: 20}, 1)
	call.Add(gtx.Ops)
	return dims
}

func (u *windowUI) layoutGeneralSettings(gtx layout.Context) layout.Dimensions {
	if settingsPreviewStacked(gtx) {
		return layout.Flex{Axis: layout.Vertical, Alignment: layout.Start, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.hotkeyButton, u.hotkeyButtonLabel(), true).Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.hotkeyActionButton, u.hotkeyActionLabel(), u.appConfig.HotkeyOpensWindow).Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.hideMinButton, "최소화 시 트레이: "+onOff(u.appConfig.HideOnMinimize), u.appConfig.HideOnMinimize).Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.hideFocusButton, "자동 숨기기: "+onOff(u.appConfig.HideOnFocusLoss), u.appConfig.HideOnFocusLoss).Layout(gtx)
			}),
		)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.hotkeyButton, u.hotkeyButtonLabel(), true).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.hotkeyActionButton, u.hotkeyActionLabel(), u.appConfig.HotkeyOpensWindow).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.hideMinButton, "최소화 시 트레이: "+onOff(u.appConfig.HideOnMinimize), u.appConfig.HideOnMinimize).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.hideFocusButton, "자동 숨기기: "+onOff(u.appConfig.HideOnFocusLoss), u.appConfig.HideOnFocusLoss).Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutSaveSettings(gtx layout.Context) layout.Dimensions {
	if !u.saveDirInitialized {
		u.saveDirEditor.SingleLine = true
		u.saveDirEditor.SetText(u.configuredSaveDir())
		u.saveDirInitialized = true
	}
	if settingsPreviewStacked(gtx) {
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return material.Editor(u.theme, &u.saveDirEditor, "저장 폴더").Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return neutralButton(u.theme, &u.saveDirApplyButton, "적용").Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return neutralButton(u.theme, &u.saveDirBrowseButton, "찾아보기...").Layout(gtx)
					}),
				)
			}),
		)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return material.Editor(u.theme, &u.saveDirEditor, "저장 폴더").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return neutralButton(u.theme, &u.saveDirApplyButton, "적용").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return neutralButton(u.theme, &u.saveDirBrowseButton, "찾아보기...").Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutSettingsControlLabel(gtx layout.Context, text string) layout.Dimensions {
	widthDp := float32(72)
	if settingsTextScale(gtx) > 1.3 {
		widthDp = 120
	}
	width := gtx.Dp(unit.Dp(widthDp))
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width
	label := material.Body2(u.theme, text)
	label.TextSize = unit.Sp(12)
	label.MaxLines = 1
	label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	return label.Layout(gtx)
}

func (u *windowUI) layoutSettingsValueLabel(gtx layout.Context, text string) layout.Dimensions {
	widthDp := float32(76)
	if settingsTextScale(gtx) > 1.3 {
		widthDp = 100
	}
	width := gtx.Dp(unit.Dp(widthDp))
	gtx.Constraints.Min.X = width
	gtx.Constraints.Max.X = width
	label := material.Body2(u.theme, text)
	label.TextSize = unit.Sp(12)
	label.MaxLines = 1
	label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
	return label.Layout(gtx)
}

func (u *windowUI) layoutStyleSettings(gtx layout.Context) layout.Dimensions {
	return u.layoutSettingsWithPreview(gtx, u.layoutStyleSettingsControls, u.layoutStrokeSettingsPreview)
}

func (u *windowUI) layoutStyleSettingsControls(gtx layout.Context) layout.Dimensions {
	if !u.rgbInitialized {
		u.rgbEditor.SingleLine = true
		u.rgbEditor.SetText(rgbText(strokeColor(u.appConfig.StrokeColor)))
		u.rgbInitialized = true
	}

	oldWidth := u.appConfig.StrokeWidth
	dims := layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "색상")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.layoutPalette(gtx) }),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "굵기")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(140))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(140))
					dims := material.Slider(u.theme, &u.strokeWidthSlider).Layout(gtx)
					u.appConfig.StrokeWidth = sliderToWidth(u.strokeWidthSlider.Value)
					if u.appConfig.StrokeWidth != oldWidth {
						u.applyStrokeStyle()
						u.strokeSettingsDirty = true
					}
					return dims
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsValueLabel(gtx, fmt.Sprintf("%dpx", u.appConfig.StrokeWidth))
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(90))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(90))
					return material.Editor(u.theme, &u.rgbEditor, "R,G,B").Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return neutralButton(u.theme, &u.rgbApplyButton, "RGB 적용").Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return neutralButton(u.theme, &u.strokePickerButton, "색 선택...").Layout(gtx)
				}),
			)
		}),
	)
	if u.strokeSettingsDirty && !u.strokeWidthSlider.Dragging() {
		u.persistConfig()
		u.strokeSettingsDirty = false
	}
	return dims
}

func (u *windowUI) layoutOutputBorderSettings(gtx layout.Context) layout.Dimensions {
	if !u.borderRGBInitialized {
		u.borderRGBEditor.SingleLine = true
		u.borderRGBEditor.SetText(rgbText(strokeColor(u.appConfig.OutputBorderColor)))
		u.borderRGBInitialized = true
	}
	oldWidth := u.appConfig.OutputBorderWidth
	dims := layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "색상")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.layoutBorderPalette(gtx) }),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "굵기")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(140))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(140))
					dims := material.Slider(u.theme, &u.borderWidthSlider).Layout(gtx)
					u.appConfig.OutputBorderWidth = outputBorderSliderToWidth(u.borderWidthSlider.Value)
					if u.appConfig.OutputBorderWidth != oldWidth {
						u.applyStrokeStyle()
						u.borderSettingsDirty = true
					}
					return dims
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsValueLabel(gtx, fmt.Sprintf("%dpx", u.appConfig.OutputBorderWidth))
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(90))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(90))
					return material.Editor(u.theme, &u.borderRGBEditor, "R,G,B").Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return neutralButton(u.theme, &u.borderRGBApplyButton, "RGB 적용").Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return neutralButton(u.theme, &u.borderPickerButton, "색 선택...").Layout(gtx)
				}),
			)
		}),
	)
	if u.borderSettingsDirty && !u.borderWidthSlider.Dragging() {
		u.persistConfig()
		u.borderSettingsDirty = false
	}
	return dims
}

func (u *windowUI) layoutHighlighterSettings(gtx layout.Context) layout.Dimensions {
	return u.layoutSettingsWithPreview(gtx, u.layoutHighlighterSettingsControls, u.layoutHighlighterSettingsPreview)
}

func (u *windowUI) layoutHighlighterSettingsControls(gtx layout.Context) layout.Dimensions {
	oldWidth := u.appConfig.HighlighterWidth
	oldAlpha := u.appConfig.HighlighterAlpha
	dims := layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "두께")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(180))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(180))
					dims := material.Slider(u.theme, &u.highlightWidthSlider).Layout(gtx)
					u.appConfig.HighlighterWidth = highlighterSliderToWidth(u.highlightWidthSlider.Value)
					if u.appConfig.HighlighterWidth != oldWidth {
						u.applyStrokeStyle()
						u.highlightSettingsDirty = true
					}
					return dims
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsValueLabel(gtx, fmt.Sprintf("%dpx", u.appConfig.HighlighterWidth))
				}),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "불투명도")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(180))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(180))
					dims := material.Slider(u.theme, &u.highlightAlphaSlider).Layout(gtx)
					u.appConfig.HighlighterAlpha = highlighterSliderToAlpha(u.highlightAlphaSlider.Value)
					if u.appConfig.HighlighterAlpha != oldAlpha {
						u.applyStrokeStyle()
						u.highlightSettingsDirty = true
					}
					return dims
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsValueLabel(gtx, fmt.Sprintf("%d%%", u.appConfig.HighlighterAlpha*100/255))
				}),
			)
		}),
	)
	if u.highlightSettingsDirty && !u.highlightWidthSlider.Dragging() && !u.highlightAlphaSlider.Dragging() {
		u.persistConfig()
		u.highlightSettingsDirty = false
	}
	return dims
}

func (u *windowUI) layoutMosaicSettings(gtx layout.Context) layout.Dimensions {
	return u.layoutSettingsWithPreview(gtx, u.layoutMosaicSettingsControls, u.layoutMosaicSettingsPreview)
}

func (u *windowUI) layoutMosaicSettingsControls(gtx layout.Context) layout.Dimensions {
	oldBlockSize := u.appConfig.MosaicBlockSize
	oldWidth := u.appConfig.MosaicWidth
	dims := layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "블록 크기")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(180))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(180))
					dims := material.Slider(u.theme, &u.mosaicBlockSizeSlider).Layout(gtx)
					u.appConfig.MosaicBlockSize = mosaicSliderToBlockSize(u.mosaicBlockSizeSlider.Value)
					if u.appConfig.MosaicBlockSize != oldBlockSize {
						u.applyStrokeStyle()
						u.mosaicSettingsDirty = true
					}
					return dims
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsValueLabel(gtx, fmt.Sprintf("%dpx", u.appConfig.MosaicBlockSize))
				}),
			)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(8))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsControlLabel(gtx, "펜 굵기")
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					gtx.Constraints.Min.X = gtx.Dp(unit.Dp(180))
					gtx.Constraints.Max.X = gtx.Dp(unit.Dp(180))
					dims := material.Slider(u.theme, &u.mosaicWidthSlider).Layout(gtx)
					u.appConfig.MosaicWidth = mosaicSliderToWidth(u.mosaicWidthSlider.Value)
					if u.appConfig.MosaicWidth != oldWidth {
						u.applyStrokeStyle()
						u.mosaicSettingsDirty = true
					}
					return dims
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return u.layoutSettingsValueLabel(gtx, fmt.Sprintf("%dpx", u.appConfig.MosaicWidth))
				}),
			)
		}),
	)
	if u.mosaicSettingsDirty && !u.mosaicBlockSizeSlider.Dragging() && !u.mosaicWidthSlider.Dragging() {
		u.persistConfig()
		u.mosaicSettingsDirty = false
	}
	return dims
}

func (u *windowUI) layoutSettingsWithPreview(gtx layout.Context, controls, preview layout.Widget) layout.Dimensions {
	if settingsPreviewStacked(gtx) {
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(controls),
			layout.Rigid(preview),
		)
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(12))}.Layout(gtx,
		layout.Rigid(controls),
		layout.Flexed(1, func(layout.Context) layout.Dimensions { return layout.Dimensions{} }),
		layout.Rigid(preview),
	)
}

func settingsPreviewStacked(gtx layout.Context) bool {
	return gtx.Constraints.Max.X < gtx.Dp(unit.Dp(720)) || settingsTextScale(gtx) > 1.3
}

func settingsTextScale(gtx layout.Context) float32 {
	pxPerDp := gtx.Metric.PxPerDp
	if pxPerDp <= 0 {
		pxPerDp = 1
	}
	pxPerSp := gtx.Metric.PxPerSp
	if pxPerSp <= 0 {
		pxPerSp = pxPerDp
	}
	return pxPerSp / pxPerDp
}

func (u *windowUI) layoutSettingsPreviewCard(gtx layout.Context, drawPreview func(*op.Ops, image.Rectangle)) layout.Dimensions {
	size := image.Pt(min(gtx.Dp(unit.Dp(160)), gtx.Constraints.Max.X), gtx.Dp(unit.Dp(112)))
	if size.X <= 0 {
		return layout.Dimensions{}
	}
	gtx.Constraints = layout.Exact(size)
	rect := image.Rectangle{Max: size}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 20, G: 24, B: 33, A: 255}, clip.RRect{Rect: rect, SE: 14, SW: 14, NW: 14, NE: 14}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, image.Rect(1, 1, size.X-1, size.Y-1), color.NRGBA{R: 148, G: 163, B: 184, A: 65}, 1)

	labelOffset := op.Offset(image.Pt(gtx.Dp(unit.Dp(10)), gtx.Dp(unit.Dp(7)))).Push(gtx.Ops)
	label := material.Body2(u.theme, "미리보기")
	label.TextSize = unit.Sp(10)
	label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
	labelGtx := gtx
	labelGtx.Constraints.Min = image.Point{}
	labelGtx.Constraints.Max = image.Pt(size.X-gtx.Dp(unit.Dp(20)), gtx.Dp(unit.Dp(18)))
	label.Layout(labelGtx)
	labelOffset.Pop()

	preview := image.Rect(
		gtx.Dp(unit.Dp(10)),
		gtx.Dp(unit.Dp(30)),
		size.X-gtx.Dp(unit.Dp(10)),
		size.Y-gtx.Dp(unit.Dp(9)),
	)
	drawPreview(gtx.Ops, preview)
	return layout.Dimensions{Size: size}
}

func (u *windowUI) layoutStrokeSettingsPreview(gtx layout.Context) layout.Dimensions {
	return u.layoutSettingsPreviewCard(gtx, func(ops *op.Ops, bounds image.Rectangle) {
		style := u.controller.AnnotationStyle
		stroke := color.NRGBA{R: style.ColorRGBA[0], G: style.ColorRGBA[1], B: style.ColorRGBA[2], A: 255}
		y := float32((bounds.Min.Y + bounds.Max.Y) / 2)
		from := f32.Pt(float32(bounds.Min.X+10), y)
		to := f32.Pt(float32(bounds.Max.X-10), y)
		drawIconLine(ops, from, to, color.NRGBA{R: 255, G: 255, B: 255, A: 70}, float32(style.Width)+2)
		drawIconLine(ops, from, to, stroke, float32(style.Width))
	})
}

func (u *windowUI) layoutHighlighterSettingsPreview(gtx layout.Context) layout.Dimensions {
	return u.layoutSettingsPreviewCard(gtx, func(ops *op.Ops, bounds image.Rectangle) {
		paper := color.NRGBA{R: 241, G: 245, B: 249, A: 255}
		paint.FillShape(ops, paper, clip.RRect{Rect: bounds, SE: 7, SW: 7, NW: 7, NE: 7}.Op(ops))
		style := u.controller.HighlighterStyle
		highlight := color.NRGBA{R: style.ColorRGBA[0], G: style.ColorRGBA[1], B: style.ColorRGBA[2], A: style.ColorRGBA[3]}
		y := float32((bounds.Min.Y + bounds.Max.Y) / 2)
		drawIconLine(ops, f32.Pt(float32(bounds.Min.X+8), y), f32.Pt(float32(bounds.Max.X-8), y), highlight, float32(style.Width))

		textOffset := op.Offset(bounds.Min).Push(ops)
		textGtx := gtx
		textGtx.Constraints = layout.Exact(bounds.Size())
		layout.Center.Layout(textGtx, func(gtx layout.Context) layout.Dimensions {
			label := material.H6(u.theme, "A a  B b  C c")
			label.TextSize = unit.Sp(22)
			label.Color = color.NRGBA{R: 30, G: 41, B: 59, A: 255}
			return label.Layout(gtx)
		})
		textOffset.Pop()
	})
}

func (u *windowUI) layoutMosaicSettingsPreview(gtx layout.Context) layout.Dimensions {
	return u.layoutSettingsPreviewCard(gtx, func(ops *op.Ops, bounds image.Rectangle) {
		baseColors := []color.NRGBA{
			{R: 30, G: 64, B: 175, A: 255},
			{R: 14, G: 165, B: 233, A: 255},
			{R: 244, G: 63, B: 94, A: 255},
			{R: 250, G: 204, B: 21, A: 255},
		}
		stripe := max(3, gtx.Dp(unit.Dp(6)))
		for x := bounds.Min.X; x < bounds.Max.X; x += stripe {
			shade := baseColors[(x-bounds.Min.X)/stripe%len(baseColors)]
			paint.FillShape(ops, shade, clip.Rect(image.Rect(x, bounds.Min.Y, min(x+stripe, bounds.Max.X), bounds.Max.Y)).Op())
		}

		width := max(1, u.appConfig.MosaicWidth)
		center := image.Pt((bounds.Min.X+bounds.Max.X)/2, (bounds.Min.Y+bounds.Max.Y)/2)
		mask := clip.Ellipse(cursorEllipse(editor.Point{X: center.X, Y: center.Y}, float32(width)/2)).Push(ops)
		block := max(1, u.appConfig.MosaicBlockSize)
		for y := bounds.Min.Y; y < bounds.Max.Y; y += block {
			for x := bounds.Min.X; x < bounds.Max.X; x += block {
				shade := baseColors[((x-bounds.Min.X)/block+(y-bounds.Min.Y)/block)%len(baseColors)]
				shade.R = uint8((uint16(shade.R) + 90) / 2)
				shade.G = uint8((uint16(shade.G) + 90) / 2)
				shade.B = uint8((uint16(shade.B) + 90) / 2)
				paint.FillShape(ops, shade, clip.Rect(image.Rect(x, y, min(x+block, bounds.Max.X), min(y+block, bounds.Max.Y))).Op())
			}
		}
		mask.Pop()
		outline := clip.Ellipse(cursorEllipse(editor.Point{X: center.X, Y: center.Y}, float32(width)/2)).Path(ops)
		paint.FillShape(ops, color.NRGBA{R: 224, G: 231, B: 255, A: 230}, clip.Stroke{Path: outline, Width: 1.5}.Op())
	})
}

func (u *windowUI) layoutPalette(gtx layout.Context) layout.Dimensions {
	presets := visibleStrokePresets()
	children := make([]layout.FlexChild, 0, len(presets))
	for i := range presets {
		idx := i
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.colorSwatch(gtx, &u.paletteButtons[idx], presets[idx].name, presets[idx].rgba)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(6))}.Layout(gtx, children...)
}

func (u *windowUI) layoutBorderPalette(gtx layout.Context) layout.Dimensions {
	presets := visibleStrokePresets()
	children := make([]layout.FlexChild, 0, len(presets))
	for i := range presets {
		idx := i
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.borderColorSwatch(gtx, &u.borderPaletteButtons[idx], presets[idx].name, presets[idx].rgba)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(4))}.Layout(gtx, children...)
}

func (u *windowUI) colorSwatch(gtx layout.Context, button *widget.Clickable, name string, rgba [4]uint8) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(40)), gtx.Dp(unit.Dp(40)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		semantic.LabelOp("선 색상 " + colorLabel(name)).Add(gtx.Ops)
		inset := 4
		if button.Pressed() {
			inset = 6
		}
		paint.FillShape(gtx.Ops, color.NRGBA{R: rgba[0], G: rgba[1], B: rgba[2], A: rgba[3]}, clip.RRect{Rect: image.Rect(inset, inset, size.X-inset, size.Y-inset), SE: 9, SW: 9, NW: 9, NE: 9}.Op(gtx.Ops))
		if u.appConfig.StrokeColor == name {
			semantic.SelectedOp(true).Add(gtx.Ops)
			drawIconRect(gtx.Ops, image.Rect(1, 1, size.X-1, size.Y-1), color.NRGBA{R: 10, G: 132, B: 255, A: 255}, 2.5)
		}
		return layout.Dimensions{Size: size}
	})
}

func (u *windowUI) borderColorSwatch(gtx layout.Context, button *widget.Clickable, name string, rgba [4]uint8) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(40)), gtx.Dp(unit.Dp(40)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		semantic.LabelOp("결과 외곽선 색상 " + colorLabel(name)).Add(gtx.Ops)
		inset := 4
		if button.Pressed() {
			inset = 6
		}
		paint.FillShape(gtx.Ops, color.NRGBA{R: rgba[0], G: rgba[1], B: rgba[2], A: rgba[3]}, clip.RRect{Rect: image.Rect(inset, inset, size.X-inset, size.Y-inset), SE: 9, SW: 9, NW: 9, NE: 9}.Op(gtx.Ops))
		if u.appConfig.OutputBorderColor == name {
			semantic.SelectedOp(true).Add(gtx.Ops)
			drawIconRect(gtx.Ops, image.Rect(1, 1, size.X-1, size.Y-1), color.NRGBA{R: 10, G: 132, B: 255, A: 255}, 2.5)
		}
		return layout.Dimensions{Size: size}
	})
}

func (u *windowUI) handleConfigEvent(e app.ConfigEvent, w *app.Window) {
	wasFocused := u.hasFocused
	if e.Config.Focused {
		u.hasFocused = true
		if !wasFocused && u.filmstrip != nil {
			u.requestFilmstripRefresh()
		}
	} else {
		u.hasFocused = false
		if u.hotkeyCaptureMode {
			u.cancelHotkeyCapture("창 포커스가 해제되어 단축키 지정을 취소했습니다.")
		}
	}
	if u.overlayActive || u.trayHidden || !u.trayAvailable {
		return
	}
	if u.appConfig.HideOnMinimize && e.Config.Mode == app.Minimized {
		u.hideToTray("최소화되어 트레이로 숨겼습니다.", w)
	}
}

func uShouldHideForFocusLoss(u *windowUI) bool {
	return u.appConfig.HideOnFocusLoss &&
		u.trayAvailable &&
		!u.trayHidden &&
		!u.captureActive &&
		!u.overlayActive &&
		!u.saveDirBrowseActive &&
		time.Now().After(u.suppressFocusLossUntil) &&
		platformwindows.WindowVisibleByTitle(windowTitle) &&
		!platformwindows.ForegroundIsWindowTitle(windowTitle)
}

func (u *windowUI) hideToTray(message string, w *app.Window) {
	u.releaseTransientImageCaches()
	if err := platformwindows.HideWindowByTitle(windowTitle); err != nil {
		u.status = "트레이로 숨기기 실패: " + err.Error()
		return
	}
	u.trayHidden = true
	u.hasFocused = false
	u.status = message
	w.Invalidate()
	u.scheduleOSMemoryRelease(100 * time.Millisecond)
}

func (u *windowUI) restoreFromTray(w *app.Window) {
	u.suppressFocusLossUntil = time.Now().Add(2 * time.Second)
	restore := u.restoreWindow
	if restore == nil {
		restore = platformwindows.RestoreWindowByTitle
	}
	if err := restore(windowTitle); err != nil {
		u.hasFocused = false
	} else {
		u.trayHidden = false
		u.hasFocused = false
	}
	if w != nil {
		w.Invalidate()
	}
}

func (u *windowUI) handleGlobalHotkey(w *app.Window) {
	if u.hotkeyCaptureMode {
		u.status = "단축키 지정 중에는 기존 단축키 동작을 실행하지 않습니다."
		return
	}
	if !u.appConfig.HotkeyOpensWindow {
		_, useSavedRegion := u.activeSavedRegion()
		u.beginCaptureRequest(w, u.appConfig.CaptureMode, false, useSavedRegion)
		return
	}
	if u.overlayActive || u.captureActive {
		u.status = "캡처가 끝난 뒤 프로그램 화면을 열 수 있습니다."
		return
	}
	u.suppressFocusLossUntil = time.Now().Add(2 * time.Second)
	restore := u.restoreWindow
	if restore == nil {
		restore = platformwindows.RestoreWindowByTitle
	}
	if err := restore(windowTitle); err != nil {
		u.status = "프로그램 열기 실패: " + err.Error()
	} else {
		u.trayHidden = false
		u.hasFocused = false
		u.status = "단축키로 프로그램 화면을 열었습니다."
	}
	if w != nil {
		w.Invalidate()
	}
}

func externalRestoreWindow() {
	_ = platformwindows.RestoreWindowByTitle(windowTitle)
}

func accentButton(th *material.Theme, button *widget.Clickable, text string) material.ButtonStyle {
	style := material.Button(th, button, text)
	style.Background = color.NRGBA{R: 10, G: 132, B: 255, A: 255}
	style.Color = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	style.TextSize = unit.Sp(13)
	style.CornerRadius = unit.Dp(11)
	style.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(16), Right: unit.Dp(16)}
	return style
}

func neutralButton(th *material.Theme, button *widget.Clickable, text string) material.ButtonStyle {
	style := material.Button(th, button, text)
	style.Background = color.NRGBA{R: 48, G: 48, B: 54, A: 255}
	style.Color = color.NRGBA{R: 245, G: 245, B: 247, A: 255}
	style.TextSize = unit.Sp(13)
	style.CornerRadius = unit.Dp(11)
	style.Inset = layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(14), Right: unit.Dp(14)}
	return style
}

func disabledButton(th *material.Theme, button *widget.Clickable, text string) material.ButtonStyle {
	style := neutralButton(th, button, text)
	style.Background = color.NRGBA{R: 36, G: 36, B: 41, A: 255}
	style.Color = color.NRGBA{R: 110, G: 110, B: 120, A: 255}
	return style
}

func toggleButton(th *material.Theme, button *widget.Clickable, text string, enabled bool) material.ButtonStyle {
	style := neutralButton(th, button, text)
	if enabled {
		style.Background = color.NRGBA{R: 36, G: 92, B: 154, A: 255}
	}
	return style
}

func enabledWidget(gtx layout.Context, enabled bool, widget layout.Widget) layout.Dimensions {
	if !enabled {
		gtx = gtx.Disabled()
	}
	return widget(gtx)
}

func layoutStatusPill(gtx layout.Context, th *material.Theme, text string) layout.Dimensions {
	recording := op.Record(gtx.Ops)
	dims := layout.Inset{Top: unit.Dp(7), Bottom: unit.Dp(7), Left: unit.Dp(12), Right: unit.Dp(12)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		label := material.Body2(th, text)
		label.TextSize = unit.Sp(12)
		label.Color = color.NRGBA{R: 205, G: 205, B: 214, A: 255}
		return label.Layout(gtx)
	})
	content := recording.Stop()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 39, G: 39, B: 45, A: 255}, clip.RRect{Rect: image.Rectangle{Max: dims.Size}, SE: 10, SW: 10, NW: 10, NE: 10}.Op(gtx.Ops))
	content.Add(gtx.Ops)
	return dims
}

func (u *windowUI) iconToolButton(gtx layout.Context, button *widget.Clickable, tool editor.Tool) layout.Dimensions {
	active := u.activeTool == tool
	size := image.Pt(gtx.Dp(unit.Dp(44)), gtx.Dp(unit.Dp(44)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		shortcut := toolShortcutLabel(tool)
		semantic.LabelOp(toolLabel(tool) + " 단축키 " + shortcut).Add(gtx.Ops)
		semantic.SelectedOp(active).Add(gtx.Ops)
		bg := color.NRGBA{R: 48, G: 48, B: 54, A: 255}
		fg := color.NRGBA{R: 235, G: 235, B: 240, A: 255}
		if button.Hovered() {
			bg = color.NRGBA{R: 60, G: 60, B: 68, A: 255}
		}
		if button.Pressed() {
			bg = color.NRGBA{R: 72, G: 72, B: 82, A: 255}
		}
		if active {
			bg = color.NRGBA{R: 10, G: 132, B: 255, A: 255}
			fg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
			if tool == editor.ToolHighlight {
				if u.highlightStraight {
					bg = color.NRGBA{R: 14, G: 165, B: 233, A: 255}
				} else {
					bg = color.NRGBA{R: 250, G: 204, B: 21, A: 255}
				}
			}
			if tool == editor.ToolMosaic {
				bg = color.NRGBA{R: 99, G: 102, B: 241, A: 255}
			}
		}
		inset := 0
		if button.Pressed() {
			inset = 2
		}
		paint.FillShape(gtx.Ops, bg, clip.RRect{Rect: image.Rect(inset, inset, size.X-inset, size.Y-inset), SE: 11, SW: 11, NW: 11, NE: 11}.Op(gtx.Ops))
		iconOffset := op.Offset(image.Pt(0, -3)).Push(gtx.Ops)
		drawToolIcon(gtx.Ops, tool, size, fg)
		iconOffset.Pop()
		drawToolShortcutLabel(gtx, u.theme, shortcut, size, fg)
		return layout.Dimensions{Size: size}
	})
}

func (u *windowUI) layoutTextToolButton(gtx layout.Context) layout.Dimensions {
	dims := u.iconToolButton(gtx, &u.textButton, editor.ToolText)
	if !u.textEditing {
		return dims
	}
	recording := op.Record(gtx.Ops)
	width := gtx.Dp(unit.Dp(430))
	offset := op.Offset(image.Pt(-(width-dims.Size.X)/2, dims.Size.Y+gtx.Dp(unit.Dp(8)))).Push(gtx.Ops)
	popoverGTX := gtx
	height := gtx.Dp(unit.Dp(94))
	textScale := textControlsScale(gtx.Metric)
	expanded := textScale > 1.25
	if expanded {
		height = max(height, gtx.Dp(unit.Dp(16))+4*(gtx.Dp(unit.Dp(16))+gtx.Sp(unit.Sp(11)))+3*gtx.Dp(unit.Dp(5)))
	}
	if u.textFontDropdownOpen {
		dropdownHeight := max(gtx.Dp(unit.Dp(174)), gtx.Dp(unit.Dp(82))+5*gtx.Sp(unit.Sp(12)))
		height += dropdownHeight
	}
	popoverGTX.Constraints = layout.Exact(image.Pt(width, height))
	u.layoutTextToolControls(popoverGTX)
	offset.Pop()
	op.Defer(gtx.Ops, recording.Stop())
	return dims
}

func (u *windowUI) layoutTextToolControls(gtx layout.Context) layout.Dimensions {
	if !u.textEditing {
		return layout.Dimensions{}
	}
	size := gtx.Constraints.Max
	background := color.NRGBA{R: 30, G: 30, B: 36, A: 252}
	var pointerPath clip.Path
	pointerPath.Begin(gtx.Ops)
	pointerPath.MoveTo(f32.Pt(float32(size.X/2-7), 0))
	pointerPath.LineTo(f32.Pt(float32(size.X/2), -7))
	pointerPath.LineTo(f32.Pt(float32(size.X/2+7), 0))
	pointerPath.LineTo(f32.Pt(float32(size.X/2-7), 0))
	paint.FillShape(gtx.Ops, background, clip.Outline{Path: pointerPath.End()}.Op())
	paint.FillShape(gtx.Ops, background, clip.RRect{Rect: image.Rectangle{Max: size}, SE: 12, SW: 12, NW: 12, NE: 12}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, image.Rectangle{Max: size}.Inset(1), color.NRGBA{R: 255, G: 255, B: 255, A: 32}, 1)
	button := func(clickable *widget.Clickable, label string, active bool) layout.Widget {
		return func(gtx layout.Context) layout.Dimensions {
			style := toggleButton(u.theme, clickable, label, active)
			style.TextSize = unit.Sp(11)
			style.Inset = layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(9), Right: unit.Dp(9)}
			return style.Layout(gtx)
		}
	}
	return layout.UniformInset(unit.Dp(8)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		expanded := textControlsScale(gtx.Metric) > 1.25
		rows := make([]layout.FlexChild, 0, 4)
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(button(&u.textFontButton, textFontFamilyLabel(u.textEditingStyle.FontFamily)+" ▾", u.textFontDropdownOpen)),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body2(u.theme, "크기")
					label.TextSize = unit.Sp(11)
					label.Color = color.NRGBA{R: 205, G: 205, B: 214, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions { return u.layoutTextSizeInput(gtx) }),
			}
			if !expanded {
				children = append(children,
					layout.Rigid(button(&u.textBoldButton, "B", u.selectedTextBold())),
					layout.Rigid(button(&u.textAlignButton, textAlignmentLabel(u.textEditingStyle.Alignment), false)),
					layout.Rigid(button(&u.textColorButton, "색", false)),
				)
			}
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx, children...)
		}))
		if expanded {
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx,
					layout.Rigid(button(&u.textBoldButton, "B", u.selectedTextBold())),
					layout.Rigid(button(&u.textAlignButton, textAlignmentLabel(u.textEditingStyle.Alignment), false)),
					layout.Rigid(button(&u.textColorButton, "색", false)),
				)
			}))
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			children := []layout.FlexChild{
				layout.Rigid(button(&u.textBackgroundButton, "배경 "+onOff(u.textEditingStyle.BackgroundEnabled), u.textEditingStyle.BackgroundEnabled)),
				layout.Rigid(button(&u.textBackgroundColorButton, "배경색", false)),
				layout.Rigid(button(&u.textBackgroundAlphaButton, textBackgroundAlphaLabel(u.textEditingStyle.BackgroundRGBA[3]), u.textEditingStyle.BackgroundRGBA[3] == 255)),
			}
			if !expanded {
				children = append(children,
					layout.Rigid(button(&u.textBorderButton, "외곽선 "+onOff(u.textEditingStyle.BorderEnabled), u.textEditingStyle.BorderEnabled)),
					layout.Rigid(button(&u.textBorderColorButton, "외곽선색", false)),
				)
			}
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx, children...)
		}))
		if expanded {
			rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx,
					layout.Rigid(button(&u.textBorderButton, "외곽선 "+onOff(u.textEditingStyle.BorderEnabled), u.textEditingStyle.BorderEnabled)),
					layout.Rigid(button(&u.textBorderColorButton, "외곽선색", false)),
				)
			}))
		}
		rows = append(rows, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !u.textFontDropdownOpen {
				return layout.Dimensions{}
			}
			return u.layoutTextFontDropdown(gtx)
		}))
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx, rows...)
	})
}

func textControlsScale(metric unit.Metric) float32 {
	if metric.PxPerDp <= 0 || metric.PxPerSp <= 0 {
		return 1
	}
	return metric.PxPerSp / metric.PxPerDp
}

func textFontFamilies() []editor.TextFontFamily {
	return []editor.TextFontFamily{editor.TextFontMalgun, editor.TextFontGulim, editor.TextFontDotum, editor.TextFontBatang, editor.TextFontGungsuh}
}

func textBackgroundAlphaLabel(alpha uint8) string {
	if alpha == 255 {
		return "불투명"
	}
	return "반투명 50%"
}

func (u *windowUI) toggleTextBackgroundAlpha() {
	u.pushTextEditUndo(u.captureTextEditSnapshot())
	u.textEditingStyle.BackgroundEnabled = true
	if u.textEditingStyle.BackgroundRGBA[3] == 255 {
		u.textEditingStyle.BackgroundRGBA[3] = textBackgroundTranslucent
		u.status = "텍스트 배경: 반투명 50%"
		return
	}
	u.textEditingStyle.BackgroundRGBA[3] = 255
	u.status = "텍스트 배경: 불투명"
}

func (u *windowUI) setTextBackgroundColor(rgba [4]uint8) {
	alpha := u.textEditingStyle.BackgroundRGBA[3]
	if alpha == 0 {
		alpha = 255
	}
	rgba[3] = alpha
	if u.textEditingStyle.BackgroundEnabled && u.textEditingStyle.BackgroundRGBA == rgba {
		return
	}
	u.pushTextEditUndo(u.captureTextEditSnapshot())
	u.textEditingStyle.BackgroundRGBA = rgba
	u.textEditingStyle.BackgroundEnabled = true
}

func textFontFamilyLabel(family editor.TextFontFamily) string {
	switch editor.NormalizeTextFontFamily(family) {
	case editor.TextFontGulim:
		return "굴림"
	case editor.TextFontDotum:
		return "돋움"
	case editor.TextFontBatang:
		return "바탕"
	case editor.TextFontGungsuh:
		return "궁서"
	default:
		return "맑은 고딕"
	}
}

func (u *windowUI) layoutTextFontDropdown(gtx layout.Context) layout.Dimensions {
	items := textFontFamilies()
	children := make([]layout.FlexChild, 0, len(items))
	for i, family := range items {
		i, family := i, family
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			style := toggleButton(u.theme, &u.textFontItems[i], textFontFamilyLabel(family), editor.NormalizeTextFontFamily(u.textEditingStyle.FontFamily) == family)
			style.TextSize = unit.Sp(12)
			style.Inset = layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(10), Right: unit.Dp(10)}
			return style.Layout(gtx)
		}))
	}
	return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(3))}.Layout(gtx, children...)
}

func (u *windowUI) layoutTextSizeInput(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(48)), gtx.Dp(unit.Dp(34)))
	gtx.Constraints = layout.Exact(size)
	_, valid := parseTextFontSize(u.textSizeEditor.Text())
	border := color.NRGBA{R: 82, G: 82, B: 92, A: 255}
	if !valid {
		border = color.NRGBA{R: 239, G: 68, B: 68, A: 255}
	}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 18, G: 18, B: 22, A: 255}, clip.RRect{Rect: image.Rectangle{Max: size}, SE: 8, SW: 8, NW: 8, NE: 8}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, image.Rectangle{Max: size}.Inset(1), border, 1)
	inputArea := clip.RRect{Rect: image.Rectangle{Max: size}, SE: 8, SW: 8, NW: 8, NE: 8}.Push(gtx.Ops)
	pointer.CursorText.Add(gtx.Ops)
	event.Op(gtx.Ops, &u.textSizeEditor)
	inputArea.Pop()
	dims := layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(5), Left: unit.Dp(7), Right: unit.Dp(5)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		style := material.Editor(u.theme, &u.textSizeEditor, "32")
		style.TextSize = unit.Sp(12)
		style.Color = color.NRGBA{R: 245, G: 245, B: 248, A: 255}
		return style.Layout(gtx)
	})
	u.applyTextSizeInput()
	return dims
}

func (u *windowUI) configureTextSizeEditor() {
	u.textSizeEditor = widget.Editor{SingleLine: true, MaxLen: 3, Filter: "0123456789", InputHint: key.HintNumeric}
}

func parseTextFontSize(value string) (float64, bool) {
	size, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || size < 1 || size > 512 {
		return 0, false
	}
	return float64(size), true
}

func (u *windowUI) applyTextSizeInput() bool {
	size, ok := parseTextFontSize(u.textSizeEditor.Text())
	if ok && size != u.textEditingStyle.FontSize {
		u.pushTextEditUndo(u.captureTextEditSnapshot())
		u.textEditingStyle.FontSize = size
		u.status = fmt.Sprintf("텍스트 크기: %.0fpx", size)
	}
	return ok
}

func (u *windowUI) selectedTextBold() bool {
	textValue := u.textEditor.Text()
	start, end := u.textEditor.Selection()
	if start > end {
		start, end = end, start
	}
	if start == end {
		if start > 0 {
			start--
		}
		return editor.BoldAtRuneOffset(textValue, u.textEditingStyle.Bold, u.textEditingRuns, start)
	}
	for i := start; i < end; i++ {
		if !editor.BoldAtRuneOffset(textValue, u.textEditingStyle.Bold, u.textEditingRuns, i) {
			return false
		}
	}
	return true
}

func drawToolShortcutLabel(gtx layout.Context, th *material.Theme, shortcut string, size image.Point, c color.NRGBA) {
	recording := op.Record(gtx.Ops)
	label := material.Label(th, unit.Sp(8), shortcut)
	label.Color = c
	gtx.Constraints.Min = image.Point{}
	gtx.Constraints.Max = size
	dims := label.Layout(gtx)
	call := recording.Stop()
	offset := op.Offset(image.Pt((size.X-dims.Size.X)/2, size.Y-dims.Size.Y-1)).Push(gtx.Ops)
	call.Add(gtx.Ops)
	offset.Pop()
}

func drawToolIcon(ops *op.Ops, tool editor.Tool, size image.Point, c color.NRGBA) {
	cx := float32(size.X) / 2
	cy := float32(size.Y) / 2
	switch tool {
	case editor.ToolRectangle:
		drawIconRect(ops, image.Rect(int(cx-11), int(cy-8), int(cx+11), int(cy+8)), c, 2.5)
	case editor.ToolTriangle:
		drawIconPolyline(ops, []f32.Point{
			f32.Pt(cx, cy-11),
			f32.Pt(cx+12, cy+9),
			f32.Pt(cx-12, cy+9),
			f32.Pt(cx, cy-11),
		}, c, 2.5)
	case editor.ToolCircle:
		path := clip.Ellipse(image.Rect(int(cx-11), int(cy-10), int(cx+11), int(cy+10))).Path(ops)
		paint.FillShape(ops, c, clip.Stroke{Path: path, Width: 2.5}.Op())
	case editor.ToolPen:
		drawIconLine(ops, f32.Pt(cx-11, cy+9), f32.Pt(cx+9, cy-11), c, 3)
		drawIconLine(ops, f32.Pt(cx+5, cy-12), f32.Pt(cx+11, cy-6), c, 3)
	case editor.ToolArrow:
		drawIconLine(ops, f32.Pt(cx-12, cy+8), f32.Pt(cx+11, cy-8), c, 3)
		drawIconLine(ops, f32.Pt(cx+11, cy-8), f32.Pt(cx+4, cy-9), c, 3)
		drawIconLine(ops, f32.Pt(cx+11, cy-8), f32.Pt(cx+8, cy-1), c, 3)
	case editor.ToolLine:
		drawIconLine(ops, f32.Pt(cx-12, cy+8), f32.Pt(cx+12, cy-8), c, 3)
	case editor.ToolMosaic:
		block := 7
		startX, startY := int(cx)-11, int(cy)-11
		for row := 0; row < 3; row++ {
			for col := 0; col < 3; col++ {
				shade := c
				if (row+col)%2 == 0 {
					shade.A = 115
				}
				rect := image.Rect(startX+col*block, startY+row*block, startX+(col+1)*block-1, startY+(row+1)*block-1)
				paint.FillShape(ops, shade, clip.Rect(rect).Op())
			}
		}
	case editor.ToolHighlight:
		drawIconLine(ops, f32.Pt(cx-12, cy+6), f32.Pt(cx+12, cy+6), color.NRGBA{R: 250, G: 204, B: 21, A: 255}, 7)
		drawIconLine(ops, f32.Pt(cx-9, cy-6), f32.Pt(cx+7, cy-10), c, 3)
	case editor.ToolText:
		drawIconLine(ops, f32.Pt(cx-10, cy-10), f32.Pt(cx+10, cy-10), c, 2.5)
		drawIconLine(ops, f32.Pt(cx, cy-10), f32.Pt(cx, cy+10), c, 2.5)
		drawIconLine(ops, f32.Pt(cx-6, cy+10), f32.Pt(cx+6, cy+10), c, 2.5)
	}
}

func drawIconRect(ops *op.Ops, rect image.Rectangle, c color.NRGBA, width float32) {
	points := []f32.Point{
		f32.Pt(float32(rect.Min.X), float32(rect.Min.Y)),
		f32.Pt(float32(rect.Max.X), float32(rect.Min.Y)),
		f32.Pt(float32(rect.Max.X), float32(rect.Max.Y)),
		f32.Pt(float32(rect.Min.X), float32(rect.Max.Y)),
		f32.Pt(float32(rect.Min.X), float32(rect.Min.Y)),
	}
	drawIconPolyline(ops, points, c, width)
}

func drawIconLine(ops *op.Ops, from, to f32.Point, c color.NRGBA, width float32) {
	drawIconPolyline(ops, []f32.Point{from, to}, c, width)
}

func drawIconPolyline(ops *op.Ops, points []f32.Point, c color.NRGBA, width float32) {
	if len(points) < 2 {
		return
	}
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(points[0])
	for _, point := range points[1:] {
		path.LineTo(point)
	}
	paint.FillShape(ops, c, clip.Stroke{Path: path.End(), Width: width}.Op())
}

func onOff(enabled bool) string {
	if enabled {
		return "켬"
	}
	return "끔"
}

func drawingModeLabel(straight bool) string {
	if straight {
		return "직선"
	}
	return "자유형"
}

func textAlignmentLabel(alignment editor.TextAlignment) string {
	switch alignment {
	case editor.TextAlignCenter:
		return "가운데"
	case editor.TextAlignRight:
		return "오른쪽"
	default:
		return "왼쪽"
	}
}

func nextTextAlignment(alignment editor.TextAlignment) editor.TextAlignment {
	switch alignment {
	case editor.TextAlignLeft:
		return editor.TextAlignCenter
	case editor.TextAlignCenter:
		return editor.TextAlignRight
	default:
		return editor.TextAlignLeft
	}
}

func modeLabel(mode string) string {
	switch mode {
	case "Region":
		return "영역"
	case "Window":
		return "창"
	case "Scroll":
		return "스크롤"
	case "Freeform":
		return "자유형"
	case "Fullscreen":
		return "전체화면"
	default:
		return mode
	}
}

func toolLabel(tool editor.Tool) string {
	switch tool {
	case editor.ToolPen:
		return "펜"
	case editor.ToolRectangle:
		return "사각형"
	case editor.ToolTriangle:
		return "삼각형"
	case editor.ToolCircle:
		return "원"
	case editor.ToolArrow:
		return "화살표"
	case editor.ToolLine:
		return "선"
	case editor.ToolHighlight:
		return "형광펜"
	case editor.ToolMosaic:
		return "모자이크"
	case editor.ToolText:
		return "텍스트"
	default:
		return string(tool)
	}
}

func toolShortcutLabel(tool editor.Tool) string {
	switch tool {
	case editor.ToolRectangle, editor.ToolTriangle, editor.ToolCircle:
		return "1"
	case editor.ToolPen:
		return "2"
	case editor.ToolArrow:
		return "3"
	case editor.ToolMosaic:
		return "4"
	case editor.ToolHighlight:
		return "5"
	case editor.ToolText:
		return "6"
	default:
		return ""
	}
}

func (u *windowUI) activeToolLabel() string {
	if u.activeTool == editor.ToolPen {
		return "펜(" + penModeLabel(u.penMode) + ")"
	}
	if u.activeTool == editor.ToolMosaic {
		return "모자이크(" + mosaicModeLabel(u.mosaicMode) + ")"
	}
	if u.activeTool == editor.ToolHighlight {
		return "형광펜(" + drawingModeLabel(u.highlightStraight) + ")"
	}
	return toolLabel(u.activeTool)
}

func (u *windowUI) highlighterPoints() []editor.Point {
	return u.drawingPoints(u.highlightStraight)
}

func (u *windowUI) drawingPoints(straight bool) []editor.Point {
	if straight {
		return []editor.Point{u.dragStart, u.dragNow}
	}
	return u.penPoints
}

func (u *windowUI) isFreehandTool() bool {
	return (u.activeTool == editor.ToolPen && u.penMode == editor.PenModeFreehand) ||
		(u.activeTool == editor.ToolMosaic && u.mosaicMode == editor.MosaicModeFreehand) ||
		(u.activeTool == editor.ToolHighlight && !u.highlightStraight)
}

func (u *windowUI) penRenderCommand() editor.RenderCommand {
	command := editor.RenderCommand{
		Tool:    editor.ToolPen,
		Style:   u.controller.AnnotationStyle,
		PenMode: u.penMode,
	}
	if u.penMode == editor.PenModeRectangle {
		command.Rect = editor.NewRect(u.dragStart, u.dragNow)
	} else {
		command.Points = u.drawingPoints(u.penMode == editor.PenModeStraight)
	}
	return command
}

func (u *windowUI) mosaicRenderCommand() editor.RenderCommand {
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Style:           u.controller.MosaicStyle,
		MosaicMode:      u.mosaicMode,
		MosaicBlockSize: u.controller.MosaicBlockSize,
	}
	if u.mosaicMode == editor.MosaicModeRectangle {
		command.Rect = editor.NewRect(u.dragStart, u.dragNow)
	} else {
		command.Points = u.drawingPoints(u.mosaicMode == editor.MosaicModeStraight)
	}
	return command
}

func colorLabel(name string) string {
	switch name {
	case "Black":
		return "검정"
	case "Red":
		return "빨강"
	case "Rose":
		return "장미"
	case "Orange":
		return "주황"
	case "Cyan":
		return "하늘"
	case "Yellow":
		return "노랑"
	case "Lime":
		return "라임"
	case "Green":
		return "초록"
	case "Emerald":
		return "에메랄드"
	case "Teal":
		return "청록"
	case "Sky":
		return "하늘"
	case "Blue":
		return "파랑"
	case "Indigo":
		return "남색"
	case "Violet":
		return "보라"
	case "Fuchsia":
		return "자홍"
	case "Gray":
		return "회색"
	case "Slate":
		return "슬레이트"
	case "White":
		return "흰색"
	default:
		return name
	}
}

func (u *windowUI) applyStrokeStyle() {
	u.controller.AnnotationStyle = editor.StrokeStyle{
		ColorRGBA: strokeColor(u.appConfig.StrokeColor),
		Width:     float64(u.appConfig.StrokeWidth),
	}
	u.controller.HighlighterStyle = editor.StrokeStyle{
		ColorRGBA: [4]uint8{250, 204, 21, uint8(u.appConfig.HighlighterAlpha)},
		Width:     float64(u.appConfig.HighlighterWidth),
	}
	u.controller.MosaicStyle = editor.StrokeStyle{Width: float64(u.appConfig.MosaicWidth)}
	u.controller.MosaicBlockSize = u.appConfig.MosaicBlockSize
	u.controller.SetOutputBorder(u.appConfig.OutputBorderEnabled, u.appConfig.OutputBorderWidth, strokeColor(u.appConfig.OutputBorderColor))
}

func (u *windowUI) setStrokeColor(name string) {
	u.appConfig.StrokeColor = name
	u.rgbEditor.SetText(rgbText(strokeColor(name)))
	u.applyStrokeStyle()
}

func (u *windowUI) setOutputBorderColor(name string) {
	u.appConfig.OutputBorderColor = name
	u.borderRGBEditor.SetText(rgbText(strokeColor(name)))
	u.applyStrokeStyle()
}

type strokePreset struct {
	name string
	rgba [4]uint8
}

func strokePresets() []strokePreset {
	return []strokePreset{
		{name: "Black", rgba: [4]uint8{0, 0, 0, 255}},
		{name: "Red", rgba: [4]uint8{255, 64, 64, 255}},
		{name: "Rose", rgba: [4]uint8{244, 63, 94, 255}},
		{name: "Orange", rgba: [4]uint8{249, 115, 22, 255}},
		{name: "Cyan", rgba: [4]uint8{34, 211, 238, 255}},
		{name: "Yellow", rgba: [4]uint8{250, 204, 21, 255}},
		{name: "Lime", rgba: [4]uint8{132, 204, 22, 255}},
		{name: "Green", rgba: [4]uint8{34, 197, 94, 255}},
		{name: "Emerald", rgba: [4]uint8{16, 185, 129, 255}},
		{name: "Teal", rgba: [4]uint8{20, 184, 166, 255}},
		{name: "Sky", rgba: [4]uint8{14, 165, 233, 255}},
		{name: "Blue", rgba: [4]uint8{59, 130, 246, 255}},
		{name: "Indigo", rgba: [4]uint8{99, 102, 241, 255}},
		{name: "Violet", rgba: [4]uint8{139, 92, 246, 255}},
		{name: "Fuchsia", rgba: [4]uint8{217, 70, 239, 255}},
		{name: "Gray", rgba: [4]uint8{148, 163, 184, 255}},
		{name: "Slate", rgba: [4]uint8{51, 65, 85, 255}},
		{name: "White", rgba: [4]uint8{255, 255, 255, 255}},
	}
}

func visibleStrokePresets() []strokePreset {
	return []strokePreset{
		{name: "Black", rgba: [4]uint8{0, 0, 0, 255}},
		{name: "Red", rgba: [4]uint8{255, 64, 64, 255}},
		{name: "Yellow", rgba: [4]uint8{250, 204, 21, 255}},
		{name: "White", rgba: [4]uint8{255, 255, 255, 255}},
	}
}

func colorPickerPalette() []strokePreset {
	hues := []float64{0, 24, 45, 72, 120, 170, 200, 220, 245, 270, 300, 330}
	levels := []struct {
		s float64
		v float64
	}{
		{s: 0.95, v: 0.95},
		{s: 0.75, v: 0.95},
		{s: 0.55, v: 0.90},
	}
	palette := make([]strokePreset, 0, 48)
	for _, level := range levels {
		for _, hue := range hues {
			palette = append(palette, strokePreset{name: "", rgba: hsvColor(hue, level.s, level.v)})
		}
	}
	for _, gray := range [][4]uint8{
		{0, 0, 0, 255}, {31, 41, 55, 255}, {75, 85, 99, 255}, {107, 114, 128, 255},
		{156, 163, 175, 255}, {209, 213, 219, 255}, {243, 244, 246, 255}, {255, 255, 255, 255},
		{255, 64, 64, 255}, {34, 211, 238, 255}, {250, 204, 21, 255}, {34, 197, 94, 255},
	} {
		palette = append(palette, strokePreset{name: "", rgba: gray})
	}
	return palette
}

func hsvColor(h, s, v float64) [4]uint8 {
	h = math.Mod(h, 360)
	if h < 0 {
		h += 360
	}
	c := v * s
	x := c * (1 - math.Abs(math.Mod(h/60, 2)-1))
	m := v - c
	var r, g, b float64
	switch {
	case h < 60:
		r, g, b = c, x, 0
	case h < 120:
		r, g, b = x, c, 0
	case h < 180:
		r, g, b = 0, c, x
	case h < 240:
		r, g, b = 0, x, c
	case h < 300:
		r, g, b = x, 0, c
	default:
		r, g, b = c, 0, x
	}
	return [4]uint8{uint8(math.Round((r + m) * 255)), uint8(math.Round((g + m) * 255)), uint8(math.Round((b + m) * 255)), 255}
}

func strokeColor(name string) [4]uint8 {
	if strings.HasPrefix(name, "#") && len(name) == 7 {
		if r, errR := strconv.ParseUint(name[1:3], 16, 8); errR == nil {
			if g, errG := strconv.ParseUint(name[3:5], 16, 8); errG == nil {
				if b, errB := strconv.ParseUint(name[5:7], 16, 8); errB == nil {
					return [4]uint8{uint8(r), uint8(g), uint8(b), 255}
				}
			}
		}
	}
	switch name {
	case "Black":
		return [4]uint8{0, 0, 0, 255}
	case "Cyan":
		return [4]uint8{34, 211, 238, 255}
	case "Orange":
		return [4]uint8{249, 115, 22, 255}
	case "Rose":
		return [4]uint8{244, 63, 94, 255}
	case "Yellow":
		return [4]uint8{250, 204, 21, 255}
	case "Lime":
		return [4]uint8{132, 204, 22, 255}
	case "Green":
		return [4]uint8{34, 197, 94, 255}
	case "Emerald":
		return [4]uint8{16, 185, 129, 255}
	case "Teal":
		return [4]uint8{20, 184, 166, 255}
	case "Sky":
		return [4]uint8{14, 165, 233, 255}
	case "Blue":
		return [4]uint8{59, 130, 246, 255}
	case "Indigo":
		return [4]uint8{99, 102, 241, 255}
	case "Violet":
		return [4]uint8{139, 92, 246, 255}
	case "Fuchsia":
		return [4]uint8{217, 70, 239, 255}
	case "Gray":
		return [4]uint8{148, 163, 184, 255}
	case "Slate":
		return [4]uint8{51, 65, 85, 255}
	case "White":
		return [4]uint8{255, 255, 255, 255}
	default:
		return [4]uint8{255, 64, 64, 255}
	}
}

func rgbText(rgba [4]uint8) string {
	return fmt.Sprintf("%d,%d,%d", rgba[0], rgba[1], rgba[2])
}

func rgbName(rgba [4]uint8) string {
	return fmt.Sprintf("#%02X%02X%02X", rgba[0], rgba[1], rgba[2])
}

func parseRGBText(text string) ([4]uint8, bool) {
	parts := strings.Split(text, ",")
	if len(parts) != 3 {
		return [4]uint8{}, false
	}
	var rgb [4]uint8
	for i, part := range parts {
		value, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || value < 0 || value > 255 {
			return [4]uint8{}, false
		}
		rgb[i] = uint8(value)
	}
	rgb[3] = 255
	return rgb, true
}

func widthToSlider(width int) float32 {
	if width < 1 {
		width = 1
	}
	if width > 12 {
		width = 12
	}
	return float32(width-1) / 11
}

func sliderToWidth(value float32) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return 1 + int(math.Round(float64(value*11)))
}

func outputBorderWidthToSlider(width int) float32 {
	if width < 1 {
		width = 1
	}
	if width > 32 {
		width = 32
	}
	return float32(width-1) / 31
}

func outputBorderSliderToWidth(value float32) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return 1 + int(math.Round(float64(value*31)))
}

func highlighterWidthToSlider(width int) float32 {
	if width < 4 {
		width = 4
	}
	if width > 32 {
		width = 32
	}
	return float32(width-4) / 28
}

func highlighterSliderToWidth(value float32) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return 4 + int(math.Round(float64(value*28)))
}

func highlighterAlphaToSlider(alpha int) float32 {
	if alpha < 40 {
		alpha = 40
	}
	if alpha > 255 {
		alpha = 255
	}
	return float32(alpha-40) / 215
}

func highlighterSliderToAlpha(value float32) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return 40 + int(math.Round(float64(value*215)))
}

func mosaicBlockSizeToSlider(size int) float32 {
	if size < 4 {
		size = 4
	}
	if size > 40 {
		size = 40
	}
	return float32(size-4) / 36
}

func mosaicSliderToBlockSize(value float32) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return 4 + int(math.Round(float64(value*36)))
}

func mosaicWidthToSlider(width int) float32 {
	if width < 8 {
		width = 8
	}
	if width > 80 {
		width = 80
	}
	return float32(width-8) / 72
}

func mosaicSliderToWidth(value float32) int {
	if value < 0 {
		value = 0
	}
	if value > 1 {
		value = 1
	}
	return 8 + int(math.Round(float64(value*72)))
}

func nextStrokeColor(current string) string {
	presets := []string{"Red", "Cyan", "Yellow", "Green", "White"}
	for i, preset := range presets {
		if preset == current {
			return presets[(i+1)%len(presets)]
		}
	}
	return presets[0]
}

func nextStrokeWidth(current int) int {
	presets := []int{2, 3, 5, 8, 12}
	for i, preset := range presets {
		if preset == current {
			return presets[(i+1)%len(presets)]
		}
	}
	return 3
}

func validCaptureMode(mode string) bool {
	switch mode {
	case "Region", "Window", "Scroll", "Freeform", "Fullscreen":
		return true
	default:
		return false
	}
}

func nextCaptureMode(current string) string {
	modes := []string{"Region", "Window", "Scroll", "Freeform", "Fullscreen"}
	for i, mode := range modes {
		if mode == current {
			return modes[(i+1)%len(modes)]
		}
	}
	return modes[0]
}

func toolButton(th *material.Theme, button *widget.Clickable, text string, active bool) material.ButtonStyle {
	style := material.Button(th, button, text)
	if active {
		style.Background = color.NRGBA{R: 245, G: 96, B: 76, A: 255}
	} else {
		style.Background = color.NRGBA{R: 42, G: 49, B: 64, A: 255}
	}
	return style
}

func (u *windowUI) layoutCanvas(gtx layout.Context, w *app.Window) layout.Dimensions {
	size := gtx.Constraints.Max
	canvasBounds := image.Rectangle{Max: size}
	defer clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 10, G: 10, B: 12, A: 255}, clip.Rect{Max: size}.Op())

	var source image.Image
	switch u.controller.Mode {
	case workflow.ModeSelecting:
		source = u.controller.FullScreenImage
	case workflow.ModeEditing:
		source = u.controller.CapturedImage
	}

	if source == nil {
		u.clearCanvasImageOp()
		return u.layoutEmptyCanvas(gtx, size)
	}

	sourceRect := image.Rectangle{Max: source.Bounds().Size()}
	viewport := u.canvasViewport(sourceRect, canvasBounds)
	if viewport.View.Empty() {
		return layout.Dimensions{Size: size}
	}
	u.syncCanvasPointer(viewport, sourceRect)

	u.addCanvasCursor(gtx.Ops)
	event.Op(gtx.Ops, &u.canvasTag)
	u.handleCanvasPointer(gtx, w, sourceRect, canvasBounds, viewport)
	if u.controller.Mode == workflow.ModeEditing {
		source = u.controller.CapturedImage
		sourceRect = image.Rectangle{Max: source.Bounds().Size()}
	}
	viewport = u.canvasViewport(sourceRect, canvasBounds)
	if viewport.View.Empty() {
		return layout.Dimensions{Size: size}
	}
	u.syncCanvasPointer(viewport, sourceRect)

	img := source
	var textLayerOp paint.ImageOp
	var textLayerRect image.Rectangle
	textLayerVisible := false
	if u.controller.Mode == workflow.ModeEditing {
		if u.activeTool != editor.ToolMosaic && u.mosaicHoverImage != nil {
			u.clearMosaicHoverCache()
		}
		var rendered *image.RGBA
		var err error
		if u.textEditing {
			previewStyle := textEditingPreviewStyle(u.textEditingStyle)
			command := editor.RenderCommand{
				Tool:      editor.ToolText,
				Rect:      u.textEditingRect,
				Text:      u.textEditor.Text(),
				TextStyle: previewStyle,
				TextRuns:  u.textEditingRuns,
			}
			var fast bool
			fast, err = u.prepareTextPreview(command)
			if fast && err == nil {
				img = u.textPreview.base
				textLayerOp = u.textPreview.layerOp
				textLayerRect = command.Rect.Normalize().ImageRect()
				textLayerVisible = true
			}
			if !textLayerVisible && err == nil {
				rendered, err = u.controller.PreviewImageReplacingAnnotation(u.textEditingID, command)
			}
		} else if u.rotation.Active {
			rendered, err = u.controller.PreviewImage()
		} else if u.dragging && u.activeTool == editor.ToolPen && u.penMode == editor.PenModeRectangle {
			rendered, err = u.controller.PreviewImageWithPending(u.penRenderCommand())
		} else if u.dragging && u.activeTool == editor.ToolHighlight {
			rendered, err = u.controller.PreviewImageWithPending(editor.RenderCommand{
				Tool:   editor.ToolHighlight,
				Points: u.highlighterPoints(),
				Style:  u.controller.HighlighterStyle,
			})
		} else if u.dragging && u.activeTool == editor.ToolMosaic {
			rendered, err = u.controller.PreviewImageWithPending(u.mosaicRenderCommand())
		} else if command, ok := u.mosaicHoverCommand(); ok {
			rendered, err = u.previewMosaicHover(command)
		} else {
			rendered, err = u.controller.PreviewImage()
		}
		if err != nil {
			u.status = "렌더링 실패: " + err.Error()
			img = u.controller.CapturedImage
		} else if !textLayerVisible {
			img = rendered
		}
	}

	if u.rotation.Active {
		u.drawRotationPreview(gtx, img, viewport)
		return layout.Dimensions{Size: size}
	}

	viewportOffset := op.Offset(viewport.View.Min).Push(gtx.Ops)
	scale := float32(viewport.Scale)
	viewportScale := op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(gtx.Ops)
	sourceClip := clip.Rect(sourceRect).Push(gtx.Ops)

	var imageOp paint.ImageOp
	if textLayerVisible {
		imageOp = u.textPreview.baseOp
	} else {
		imageOp = u.canvasImageOperation(img)
	}
	if mipOp, factor, ok := u.canvasMipOperation(img, viewport.Scale); ok {
		mipScale := op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(float32(factor), float32(factor)))).Push(gtx.Ops)
		mipOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		mipScale.Pop()
	} else {
		drawOp := imageOp
		if pixelAlignedImageScale(viewport.Scale) {
			drawOp.Filter = paint.FilterNearest
		}
		drawOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
	}
	if textLayerVisible {
		layerOffset := op.Offset(textLayerRect.Min).Push(gtx.Ops)
		layerClip := clip.Rect{Max: textLayerRect.Size()}.Push(gtx.Ops)
		textLayerOp.Add(gtx.Ops)
		paint.PaintOp{}.Add(gtx.Ops)
		layerClip.Pop()
		layerOffset.Pop()
	}
	u.drawCropFrame(gtx.Ops, sourceRect, viewport.Scale)
	u.drawGesturePreview(gtx.Ops, viewport.Scale)
	u.drawToolCursorPreview(gtx.Ops, viewport.Scale)
	sourceClip.Pop()
	viewportScale.Pop()
	viewportOffset.Pop()
	u.drawRotationHandle(gtx, viewport)
	u.layoutTextEditor(gtx, viewport)
	u.layoutActiveTextTransformCursor(gtx, size)

	if u.cropDragging {
		u.cropMagnifierFrame = cropMagnifierFrame{
			Image:        imageOp,
			SourcePoint:  image.Pt(u.canvasPointer.X, u.canvasPointer.Y),
			SourceBounds: sourceRect,
			PointerView:  image.Pt(u.canvasPointerView.X, u.canvasPointerView.Y),
			Visible:      true,
		}
	}

	return layout.Dimensions{Size: size}
}

func (u *windowUI) layoutTextEditor(gtx layout.Context, viewport coords.Viewport) {
	if !u.textEditing {
		return
	}
	u.handleTextTransformEvents(gtx, viewport)
	viewRect := textEditorViewRect(u.textEditingRect, viewport)
	if viewRect.Empty() {
		return
	}
	defer op.Offset(viewRect.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(viewRect.Size())
	drawIconRect(gtx.Ops, image.Rectangle{Max: viewRect.Size()}.Inset(1), color.NRGBA{R: 56, G: 189, B: 248, A: 255}, 1)
	if !u.colorPickerOpen && u.textEditorNeedsFocus {
		gtx.Execute(key.FocusCmd{Tag: &u.textEditor})
		u.textEditorNeedsFocus = false
	}
	u.textEditor.Alignment = gioTextAlignment(u.textEditingStyle.Alignment)
	padding := max(0, int(math.Round(editor.EffectiveTextPadding(u.textEditingStyle)*viewport.Scale)))
	innerSize := viewRect.Size().Sub(image.Pt(padding*2, padding*2))
	if innerSize.X < 1 {
		innerSize.X = 1
	}
	if innerSize.Y < 1 {
		innerSize.Y = 1
	}
	innerOffset := op.Offset(image.Pt(padding, padding)).Push(gtx.Ops)
	innerGTX := gtx
	innerGTX.Constraints = layout.Exact(innerSize)
	style := material.Editor(u.theme, &u.textEditor, "텍스트를 입력하세요")
	style.Font.Typeface = font.Typeface(string(editor.EffectiveRichTextFontFamily(u.textEditingStyle, u.textEditingRuns)) + ", Malgun Gothic, sans-serif")
	style.Color = color.NRGBA{}
	style.HintColor = color.NRGBA{R: 100, G: 116, B: 139, A: 230}
	style.SelectionColor = color.NRGBA{R: 10, G: 132, B: 255, A: 105}
	pxPerSp := gtx.Metric.PxPerSp
	if pxPerSp <= 0 {
		pxPerSp = 1
	}
	textSize := float32(u.textEditingStyle.FontSize * viewport.Scale / float64(pxPerSp))
	style.TextSize = unit.Sp(maxFloat32(textSize, 1))
	style.LineHeight = unit.Sp(maxFloat32(textSize*float32(editor.EffectiveTextLineHeight(u.textEditingStyle)), 1))
	style.LineHeightScale = 1
	pass := pointer.PassOp{}.Push(innerGTX.Ops)
	style.Layout(innerGTX)
	pass.Pop()
	if innerGTX.Focused(&u.textEditor) {
		start, end := u.textEditor.Selection()
		if start == end {
			caret := u.textEditor.CaretCoords()
			x := int(math.Round(float64(caret.X)))
			baseline := int(math.Round(float64(caret.Y)))
			height := max(1, int(math.Round(float64(textSize))))
			caretRect := image.Rect(x, baseline-height, x+max(1, gtx.Dp(unit.Dp(1))), baseline+max(1, height/5)).Intersect(image.Rectangle{Max: innerSize})
			if !caretRect.Empty() {
				paint.FillShape(gtx.Ops, color.NRGBA{R: 14, G: 165, B: 233, A: 255}, clip.Rect(caretRect).Op())
			}
		}
	}
	innerOffset.Pop()
	u.layoutTextTransformHandles(gtx, viewRect, viewport)
}

func textEditorBackground(textColor [4]uint8) color.NRGBA {
	luminance := 0.2126*float64(textColor[0]) + 0.7152*float64(textColor[1]) + 0.0722*float64(textColor[2])
	if luminance < 110 {
		return color.NRGBA{R: 248, G: 250, B: 252, A: 62}
	}
	return color.NRGBA{R: 15, G: 23, B: 42, A: 68}
}

func textEditingPreviewStyle(style editor.TextStyle) editor.TextStyle {
	if style.BackgroundEnabled {
		return style
	}
	background := textEditorBackground(style.ColorRGBA)
	style.BackgroundEnabled = true
	style.BackgroundRGBA = [4]uint8{background.R, background.G, background.B, background.A}
	return style
}

func (u *windowUI) layoutTextTransformHandles(gtx layout.Context, viewRect image.Rectangle, viewport coords.Viewport) {
	localRect := image.Rectangle{Max: viewRect.Size()}
	move, resize := textTransformHandleGeometry(localRect, gtx.Dp(unit.Dp(16)), gtx.Dp(unit.Dp(14)), gtx.Dp(unit.Dp(34)))
	u.layoutTextTransformHandle(gtx, move, &u.textMoveTag, pointer.CursorGrab, true)
	u.layoutTextTransformHandle(gtx, resize, &u.textResizeTag, pointer.CursorSouthEastResize, false)
	if u.textTransforming {
		preview := textEditorViewRect(u.textTransformPreviewRect, viewport)
		preview.Min = preview.Min.Sub(viewRect.Min)
		preview.Max = preview.Max.Sub(viewRect.Min)
		drawIconRect(gtx.Ops, preview, color.NRGBA{R: 14, G: 165, B: 233, A: 230}, 2)
	}
}

func centeredSquare(center image.Point, side int) image.Rectangle {
	side = max(1, side)
	minPoint := image.Pt(center.X-side/2, center.Y-side/2)
	return image.Rectangle{Min: minPoint, Max: minPoint.Add(image.Pt(side, side))}
}

func textTransformHandleGeometry(rect image.Rectangle, moveVisual, resizeVisual, hitSize int) (textHandleGeometry, textHandleGeometry) {
	moveCenter := image.Pt(rect.Min.X+rect.Dx()/2, rect.Min.Y)
	resizeCenter := rect.Max
	hitSize = max(hitSize, moveVisual, resizeVisual)
	move := textHandleGeometry{Visual: centeredSquare(moveCenter, moveVisual), Hit: centeredSquare(moveCenter, hitSize)}
	resize := textHandleGeometry{Visual: centeredSquare(resizeCenter, resizeVisual), Hit: centeredSquare(resizeCenter, hitSize)}
	if move.Hit.Overlaps(resize.Hit) {
		dx := absInt(moveCenter.X - resizeCenter.X)
		dy := absInt(moveCenter.Y - resizeCenter.Y)
		if dy >= dx {
			boundary := (moveCenter.Y + resizeCenter.Y) / 2
			if moveCenter.Y <= resizeCenter.Y {
				boundary = max(boundary, moveCenter.Y+1)
				boundary = min(boundary, resizeCenter.Y)
				move.Hit.Max.Y = min(move.Hit.Max.Y, boundary)
				resize.Hit.Min.Y = max(resize.Hit.Min.Y, boundary)
			} else {
				boundary = min(boundary, moveCenter.Y)
				boundary = max(boundary, resizeCenter.Y+1)
				move.Hit.Min.Y = max(move.Hit.Min.Y, boundary)
				resize.Hit.Max.Y = min(resize.Hit.Max.Y, boundary)
			}
		} else {
			boundary := (moveCenter.X + resizeCenter.X) / 2
			if moveCenter.X <= resizeCenter.X {
				boundary = max(boundary, moveCenter.X+1)
				boundary = min(boundary, resizeCenter.X)
				move.Hit.Max.X = min(move.Hit.Max.X, boundary)
				resize.Hit.Min.X = max(resize.Hit.Min.X, boundary)
			} else {
				boundary = min(boundary, moveCenter.X)
				boundary = max(boundary, resizeCenter.X+1)
				move.Hit.Min.X = max(move.Hit.Min.X, boundary)
				resize.Hit.Max.X = min(resize.Hit.Max.X, boundary)
			}
		}
	}
	return move, resize
}

func textTransformHandlesContain(point f32.Point, viewRect image.Rectangle, moveVisual, resizeVisual, hitSize int) bool {
	if viewRect.Empty() {
		return false
	}
	localPoint := f32.Pt(point.X-float32(viewRect.Min.X), point.Y-float32(viewRect.Min.Y))
	move, resize := textTransformHandleGeometry(image.Rectangle{Max: viewRect.Size()}, moveVisual, resizeVisual, hitSize)
	return floatPointInRect(localPoint, move.Hit) || floatPointInRect(localPoint, resize.Hit)
}

func floatPointInRect(point f32.Point, rect image.Rectangle) bool {
	return point.X >= float32(rect.Min.X) && point.X < float32(rect.Max.X) && point.Y >= float32(rect.Min.Y) && point.Y < float32(rect.Max.Y)
}

func (u *windowUI) layoutTextTransformHandle(gtx layout.Context, geometry textHandleGeometry, tag event.Tag, cursor pointer.Cursor, move bool) {
	area := clip.Rect(geometry.Hit).Push(gtx.Ops)
	cursor.Add(gtx.Ops)
	event.Op(gtx.Ops, tag)
	area.Pop()
	fill := color.NRGBA{R: 14, G: 165, B: 233, A: 255}
	paint.FillShape(gtx.Ops, fill, clip.Ellipse(geometry.Visual).Op(gtx.Ops))
	center := f32.Pt(float32(geometry.Visual.Min.X+geometry.Visual.Dx()/2), float32(geometry.Visual.Min.Y+geometry.Visual.Dy()/2))
	if move {
		drawIconLine(gtx.Ops, f32.Pt(center.X-4, center.Y), f32.Pt(center.X+4, center.Y), color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 1.5)
		drawIconLine(gtx.Ops, f32.Pt(center.X, center.Y-4), f32.Pt(center.X, center.Y+4), color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 1.5)
	} else {
		drawIconLine(gtx.Ops, f32.Pt(center.X-3, center.Y+3), f32.Pt(center.X+3, center.Y-3), color.NRGBA{R: 255, G: 255, B: 255, A: 255}, 1.5)
	}
}

func (u *windowUI) layoutActiveTextTransformCursor(gtx layout.Context, size image.Point) {
	if !u.textTransforming {
		return
	}
	area := clip.Rect{Max: size}.Push(gtx.Ops)
	if u.textTransformMode == textTransformMove {
		pointer.CursorGrabbing.Add(gtx.Ops)
	} else {
		pointer.CursorSouthEastResize.Add(gtx.Ops)
	}
	area.Pop()
}

func (u *windowUI) handleTextTransformEvents(gtx layout.Context, viewport coords.Viewport) {
	u.handleTextTransformTag(gtx, viewport, &u.textMoveTag, textTransformMove)
	u.handleTextTransformTag(gtx, viewport, &u.textResizeTag, textTransformResize)
}

func (u *windowUI) handleTextTransformTag(gtx layout.Context, viewport coords.Viewport, tag event.Tag, mode textTransformMode) {
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: tag, Kinds: pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel})
		if !ok {
			return
		}
		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		switch pe.Kind {
		case pointer.Press:
			u.textTransforming = true
			u.textTransformMode = mode
			u.textTransformStart = pe.Position
			u.textTransformStartScale = viewport.Scale
			u.textTransformStartRect = u.textEditingRect.Normalize()
			u.textTransformPreviewRect = u.textTransformStartRect
			gtx.Execute(pointer.GrabCmd{Tag: tag, ID: pe.PointerID})
		case pointer.Drag, pointer.Release:
			if !u.textTransforming || u.textTransformMode != mode {
				continue
			}
			delta := textTransformSourceDelta(u.textTransformStart, pe.Position, u.textTransformStartScale)
			u.textTransformPreviewRect = transformTextRect(u.textTransformStartRect, delta, mode, viewport.Source)
			if pe.Kind == pointer.Release {
				if u.textEditingRect != u.textTransformPreviewRect {
					u.pushTextEditUndo(u.captureTextEditSnapshot())
				}
				u.textEditingRect = u.textTransformPreviewRect
				u.textTransforming = false
				u.textTransformMode = textTransformNone
				u.status = "텍스트 상자를 조정했습니다."
			}
		case pointer.Cancel:
			u.textTransforming = false
			u.textTransformMode = textTransformNone
		}
	}
}

func textTransformSourceDelta(start, current f32.Point, scale float64) editor.Point {
	if scale <= 0 {
		scale = 1
	}
	return editor.Point{
		X: int(math.Round(float64(current.X-start.X) / scale)),
		Y: int(math.Round(float64(current.Y-start.Y) / scale)),
	}
}

func transformTextRect(rect editor.Rect, delta editor.Point, mode textTransformMode, bounds image.Rectangle) editor.Rect {
	r := rect.Normalize().ImageRect()
	switch mode {
	case textTransformMove:
		x := min(max(bounds.Min.X, r.Min.X+delta.X), bounds.Max.X-r.Dx())
		y := min(max(bounds.Min.Y, r.Min.Y+delta.Y), bounds.Max.Y-r.Dy())
		r = image.Rect(x, y, x+r.Dx(), y+r.Dy())
	case textTransformResize:
		maxX := min(bounds.Max.X, max(r.Min.X+48, r.Max.X+delta.X))
		maxY := min(bounds.Max.Y, max(r.Min.Y+32, r.Max.Y+delta.Y))
		r.Max = image.Pt(maxX, maxY)
	}
	return rectToEditorRect(r)
}

func textEditorViewRect(rect editor.Rect, viewport coords.Viewport) image.Rectangle {
	r := rect.Normalize().ImageRect()
	return image.Rectangle{Min: viewport.SourceToView(r.Min), Max: viewport.SourceToView(r.Max)}.Canon()
}

func gioTextAlignment(alignment editor.TextAlignment) text.Alignment {
	switch alignment {
	case editor.TextAlignCenter:
		return text.Middle
	case editor.TextAlignRight:
		return text.End
	default:
		return text.Start
	}
}

func maxFloat32(a, b float32) float32 {
	if a > b {
		return a
	}
	return b
}

func (u *windowUI) handleTextEditingKeys(gtx layout.Context) {
	u.handleRichTextShortcuts(gtx)
	u.handleTextSizeAdjustments(gtx)
	u.syncTextEditorEvents(gtx)
	u.flushTextSizeEditorEvents(gtx)
	u.applyTextSizeInput()
	filters := []event.Filter{
		key.Filter{Focus: &u.textEditor, Name: key.NameEscape},
		key.Filter{Focus: &u.textEditor, Name: key.NameReturn, Required: key.ModShortcut},
		key.Filter{Focus: &u.textEditor, Name: key.NameEnter, Required: key.ModShortcut},
		key.Filter{Focus: &u.textSizeEditor, Name: key.NameEscape},
		key.Filter{Focus: &u.textSizeEditor, Name: key.NameReturn, Required: key.ModShortcut},
		key.Filter{Focus: &u.textSizeEditor, Name: key.NameEnter, Required: key.ModShortcut},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		if ke.Name == key.NameEscape {
			u.cancelTextEditing("텍스트 편집을 취소했습니다.")
			u.hideForEscape()
			return
		}
		if ke.Modifiers.Contain(key.ModShortcut) {
			u.commitTextEditing()
			return
		}
	}
}

func (u *windowUI) handleRichTextShortcuts(gtx layout.Context) {
	filters := []event.Filter{
		key.Filter{Focus: &u.textEditor, Name: "B", Required: key.ModShortcut},
		key.Filter{Focus: &u.textEditor, Name: "b", Required: key.ModShortcut},
		key.Filter{Focus: &u.textEditor, Name: "Z", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Focus: &u.textEditor, Name: "z", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Focus: &u.textEditor, Name: "Y", Required: key.ModShortcut},
		key.Filter{Focus: &u.textEditor, Name: "y", Required: key.ModShortcut},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			return
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		switch strings.ToUpper(string(ke.Name)) {
		case "B":
			u.toggleSelectedTextBold()
		case "Z":
			if ke.Modifiers.Contain(key.ModShift) {
				u.redoTextEdit()
			} else {
				u.undoTextEdit()
			}
		case "Y":
			u.redoTextEdit()
		}
	}
}

func (u *windowUI) handleTextSizeAdjustments(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(
			key.Filter{Focus: &u.textSizeEditor, Name: key.NameUpArrow, Optional: key.ModShift},
			key.Filter{Focus: &u.textSizeEditor, Name: key.NameDownArrow, Optional: key.ModShift},
		)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		delta := 1
		if ke.Name == key.NameDownArrow {
			delta = -1
		}
		if ke.Modifiers.Contain(key.ModShift) {
			delta *= 10
		}
		u.adjustTextFontSize(delta)
	}
	for {
		ev, ok := gtx.Event(pointer.Filter{Target: &u.textSizeEditor, Kinds: pointer.Scroll, ScrollY: pointer.ScrollRange{Min: -1000000, Max: 1000000}})
		if !ok {
			break
		}
		pe, ok := ev.(pointer.Event)
		if !ok || pe.Scroll.Y == 0 {
			continue
		}
		u.applyTextSizeWheel(pe.Scroll.Y)
	}
}

func (u *windowUI) applyTextSizeWheel(scrollY float32) {
	u.textSizeWheel += scrollY
	for math.Abs(float64(u.textSizeWheel)) >= 40 {
		delta := 1
		if u.textSizeWheel > 0 {
			delta = -1
			u.textSizeWheel -= 40
		} else {
			u.textSizeWheel += 40
		}
		u.adjustTextFontSize(delta)
	}
}

func (u *windowUI) adjustTextFontSize(delta int) {
	current := int(math.Round(u.textEditingStyle.FontSize))
	if parsed, ok := parseTextFontSize(u.textSizeEditor.Text()); ok {
		current = int(math.Round(parsed))
	}
	current = max(1, min(512, current+delta))
	if float64(current) != u.textEditingStyle.FontSize {
		u.pushTextEditUndo(u.captureTextEditSnapshot())
	}
	u.textSizeEditor.SetText(strconv.Itoa(current))
	u.textSizeEditor.SetCaret(u.textSizeEditor.Len(), u.textSizeEditor.Len())
	u.textEditingStyle.FontSize = float64(current)
	u.status = fmt.Sprintf("텍스트 크기: %dpx", current)
}

func (u *windowUI) syncTextEditorEvents(gtx layout.Context) {
	before := u.captureTextEditSnapshot()
	changed := false
	for {
		ev, ok := u.textEditor.Update(gtx)
		if !ok {
			break
		}
		if _, ok := ev.(widget.ChangeEvent); ok {
			changed = true
		}
	}
	if !changed || before.Text == u.textEditor.Text() {
		return
	}
	changeStart, oldEnd, newEnd := changedTextRange(before.Text, u.textEditor.Text())
	if !u.textEditCoalescing || changeStart != u.textEditCoalesceStart || oldEnd != u.textEditCoalesceEnd {
		u.pushTextEditUndo(before)
	}
	u.textEditCoalescing = true
	u.textEditCoalesceStart = changeStart
	u.textEditCoalesceEnd = newEnd
	newCaret, _ := u.textEditor.Selection()
	u.textEditingRuns = editor.TransformTextRunsForEdit(before.Text, u.textEditor.Text(), u.textEditingStyle.Bold, before.Runs, before.SelectionStart, before.SelectionEnd, newCaret)
}

func changedTextRange(oldText, newText string) (start, oldEnd, newEnd int) {
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	for start < len(oldRunes) && start < len(newRunes) && oldRunes[start] == newRunes[start] {
		start++
	}
	oldEnd = len(oldRunes)
	newEnd = len(newRunes)
	for oldEnd > start && newEnd > start && oldRunes[oldEnd-1] == newRunes[newEnd-1] {
		oldEnd--
		newEnd--
	}
	return start, oldEnd, newEnd
}

func (u *windowUI) captureTextEditSnapshot() textEditSnapshot {
	start, end := u.textEditor.Selection()
	return textEditSnapshot{Text: u.textEditor.Text(), Runs: append([]editor.TextRun(nil), u.textEditingRuns...), Style: u.textEditingStyle, Rect: u.textEditingRect, SelectionStart: start, SelectionEnd: end}
}

func (u *windowUI) pushTextEditUndo(snapshot textEditSnapshot) {
	u.textEditCoalescing = false
	u.textEditUndo = append(u.textEditUndo, snapshot)
	if len(u.textEditUndo) > workflow.MaxEditHistoryEntries {
		u.textEditUndo = append([]textEditSnapshot(nil), u.textEditUndo[len(u.textEditUndo)-workflow.MaxEditHistoryEntries:]...)
	}
	u.textEditRedo = nil
}

func (u *windowUI) restoreTextEditSnapshot(snapshot textEditSnapshot) {
	u.textEditCoalescing = false
	u.textEditor.SetText(snapshot.Text)
	u.textEditor.SetCaret(snapshot.SelectionStart, snapshot.SelectionEnd)
	u.textEditingRuns = append([]editor.TextRun(nil), snapshot.Runs...)
	u.textEditingStyle = snapshot.Style
	u.textEditingRect = snapshot.Rect
	u.textSizeEditor.SetText(strconv.Itoa(int(math.Round(snapshot.Style.FontSize))))
	u.textEditorNeedsFocus = true
}

func (u *windowUI) undoTextEdit() {
	if len(u.textEditUndo) == 0 {
		u.status = "텍스트에서 실행 취소할 항목이 없습니다."
		return
	}
	current := u.captureTextEditSnapshot()
	previous := u.textEditUndo[len(u.textEditUndo)-1]
	u.textEditUndo = u.textEditUndo[:len(u.textEditUndo)-1]
	u.textEditRedo = append(u.textEditRedo, current)
	u.restoreTextEditSnapshot(previous)
	u.status = "텍스트 편집을 실행 취소했습니다."
}

func (u *windowUI) redoTextEdit() {
	if len(u.textEditRedo) == 0 {
		u.status = "텍스트에서 다시 실행할 항목이 없습니다."
		return
	}
	current := u.captureTextEditSnapshot()
	next := u.textEditRedo[len(u.textEditRedo)-1]
	u.textEditRedo = u.textEditRedo[:len(u.textEditRedo)-1]
	u.textEditUndo = append(u.textEditUndo, current)
	u.restoreTextEditSnapshot(next)
	u.status = "텍스트 편집을 다시 실행했습니다."
}

func (u *windowUI) toggleSelectedTextBold() bool {
	start, end := u.textEditor.Selection()
	if start == end {
		u.status = "굵게 적용할 텍스트를 선택하세요."
		return false
	}
	u.pushTextEditUndo(u.captureTextEditSnapshot())
	u.textEditingRuns = editor.ToggleBoldTextRuns(u.textEditor.Text(), u.textEditingStyle.Bold, u.textEditingRuns, start, end)
	u.textEditor.SetCaret(start, end)
	u.textEditorNeedsFocus = true
	u.status = "선택 영역의 굵기를 변경했습니다."
	return true
}

func (u *windowUI) flushTextSizeEditorEvents(gtx layout.Context) {
	for {
		if _, ok := u.textSizeEditor.Update(gtx); !ok {
			return
		}
	}
}

func (u *windowUI) beginTextEditing(annotation editor.Annotation) {
	u.resetTextPreview()
	u.textEditor = widget.Editor{MaxLen: 4000, WrapPolicy: text.WrapGraphemes}
	u.textEditor.SetText(annotation.Text)
	u.textEditor.SetCaret(u.textEditor.Len(), u.textEditor.Len())
	u.configureTextSizeEditor()
	u.textSizeEditor.SetText(strconv.Itoa(int(math.Round(annotation.TextStyle.FontSize))))
	u.textEditing = true
	u.textEditingID = annotation.ID
	u.textEditingRect = annotation.Rect.Normalize()
	u.textEditingStyle = annotation.TextStyle
	u.textEditingStyle.FontFamily = editor.NormalizeTextFontFamily(u.textEditingStyle.FontFamily)
	u.textEditingRuns = editor.CanonicalizeTextRuns(annotation.Text, u.textEditingStyle.Bold, annotation.TextRuns)
	u.textEditUndo = nil
	u.textEditRedo = nil
	u.textEditCoalescing = false
	u.textSizeWheel = 0
	u.textEditorNeedsFocus = true
	u.textFontDropdownOpen = false
	u.dragging = false
	u.status = "텍스트를 편집합니다. Ctrl+Enter로 완료, Esc로 취소합니다."
}

func (u *windowUI) beginNewTextEditing(start, end editor.Point, bounds image.Rectangle) {
	rect := newTextEditingRect(start, end, bounds)
	style := editor.DefaultTextStyle()
	u.beginTextEditing(editor.Annotation{Tool: editor.ToolText, Rect: rect, TextStyle: style})
}

func newTextEditingRect(start, end editor.Point, bounds image.Rectangle) editor.Rect {
	r := editor.NewRect(start, end).Normalize().ImageRect()
	if r.Dx() < 12 && r.Dy() < 12 {
		width := min(320, bounds.Dx())
		height := min(140, bounds.Dy())
		x := min(max(bounds.Min.X, start.X), bounds.Max.X-width)
		y := min(max(bounds.Min.Y, start.Y), bounds.Max.Y-height)
		r = image.Rect(x, y, x+width, y+height)
	} else {
		minWidth := min(120, bounds.Dx())
		minHeight := min(64, bounds.Dy())
		if r.Dx() < minWidth {
			r.Max.X = r.Min.X + minWidth
		}
		if r.Dy() < minHeight {
			r.Max.Y = r.Min.Y + minHeight
		}
		r = r.Intersect(bounds)
	}
	return rectToEditorRect(r)
}

func (u *windowUI) textAnnotationAt(point editor.Point) (editor.Annotation, bool) {
	if u.controller == nil || u.controller.Editor == nil {
		return editor.Annotation{}, false
	}
	p := image.Pt(point.X, point.Y)
	for i := len(u.controller.Editor.Annotations) - 1; i >= 0; i-- {
		annotation := u.controller.Editor.Annotations[i]
		if annotation.Tool == editor.ToolText && p.In(annotation.Rect.Normalize().ImageRect()) {
			return annotation, true
		}
	}
	return editor.Annotation{}, false
}

func (u *windowUI) commitTextEditing() bool {
	if !u.textEditing {
		return true
	}
	if !u.applyTextSizeInput() {
		u.status = "글자 크기는 1부터 512 사이의 숫자로 입력하세요."
		u.showToastDetail("글자 크기 입력 오류", u.status)
		return false
	}
	value := u.textEditor.Text()
	view := u.captureCanvasView()
	changed := false
	var err error
	if strings.TrimSpace(value) == "" {
		if u.textEditingID != "" {
			changed, err = u.controller.RemoveTextAnnotation(u.textEditingID)
		}
	} else if u.textEditingID == "" {
		_, err = u.controller.AddRichTextAnnotation(value, u.textEditingRuns, u.textEditingRect, u.textEditingStyle)
		changed = err == nil
	} else {
		changed, err = u.controller.UpdateRichTextAnnotation(u.textEditingID, value, u.textEditingRuns, u.textEditingRect, u.textEditingStyle)
	}
	if err != nil {
		u.status = "텍스트 편집 실패: " + err.Error()
		u.showToastDetail("텍스트 편집 실패", err.Error())
		return false
	}
	u.resetTextEditing()
	if !changed {
		u.status = "텍스트 편집을 종료했습니다."
		return true
	}
	u.recordControllerEdit(view, view)
	if u.autoCopyPending {
		u.autoCopyPending = false
		if err := u.copyPNGCurrentImage(); err != nil {
			u.status = "텍스트를 적용했습니다. 자동 복사 실패: " + err.Error()
			u.showToast("자동 복사 실패")
			return true
		}
		u.status = "텍스트를 적용하고 클립보드에 복사했습니다."
		u.showToast("첫 텍스트 후 복사 완료")
		return true
	}
	u.status = "텍스트를 적용했습니다."
	return true
}

func (u *windowUI) cancelTextEditing(status string) {
	if !u.textEditing {
		return
	}
	u.resetTextEditing()
	u.status = status
}

func (u *windowUI) resetTextEditing() {
	u.resetTextPreview()
	u.textEditing = false
	u.textEditingID = ""
	u.textEditingRect = editor.Rect{}
	u.textEditingStyle = editor.TextStyle{}
	u.textEditingRuns = nil
	u.textEditUndo = nil
	u.textEditRedo = nil
	u.textEditCoalescing = false
	u.textSizeWheel = 0
	u.textEditor = widget.Editor{MaxLen: 4000, WrapPolicy: text.WrapGraphemes}
	u.configureTextSizeEditor()
	u.textEditorNeedsFocus = false
	u.textFontDropdownOpen = false
	u.textTransforming = false
	u.textTransformMode = textTransformNone
}

func (u *windowUI) resetTextPreview() {
	u.textPreview = textPreviewCache{}
}

func (u *windowUI) prepareTextPreview(command editor.RenderCommand) (bool, error) {
	if u.cropDragging || !u.textPreviewFastPathEligible() {
		return false, nil
	}
	if u.textPreview.base == nil {
		var base *image.RGBA
		var err error
		if u.textEditingID == "" {
			base, err = u.controller.PreviewImage()
		} else {
			base, err = u.controller.PreviewImageWithoutAnnotation(u.textEditingID)
		}
		if err != nil {
			return true, err
		}
		u.textPreview.base = base
		u.textPreview.baseOp = u.canvasImageOperation(base)
	}
	if !u.textPreview.layerValid || !sameTextPreviewCommand(u.textPreview.layerKey, command) {
		layer, err := exporter.RenderTextLayer(command)
		if err != nil {
			return true, err
		}
		u.textPreview.layer = layer
		u.textPreview.layerOp = paint.NewImageOp(layer)
		u.textPreview.layerKey = command
		u.textPreview.layerKey.TextRuns = append([]editor.TextRun(nil), command.TextRuns...)
		u.textPreview.layerValid = true
	}
	return true, nil
}

func (u *windowUI) textPreviewFastPathEligible() bool {
	if u.controller == nil || u.controller.Editor == nil {
		return false
	}
	if u.textEditingID == "" {
		return true
	}
	annotations := u.controller.Editor.Annotations
	return len(annotations) > 0 && annotations[len(annotations)-1].ID == u.textEditingID
}

func sameTextPreviewCommand(a, b editor.RenderCommand) bool {
	return a.Tool == b.Tool &&
		a.Rect == b.Rect &&
		a.Text == b.Text &&
		a.TextStyle == b.TextStyle &&
		slices.Equal(a.TextRuns, b.TextRuns)
}

func (u *windowUI) layoutEmptyCanvas(gtx layout.Context, size image.Point) layout.Dimensions {
	if size.Y < gtx.Dp(unit.Dp(270)) {
		return u.layoutCompactEmptyCanvas(gtx, size)
	}
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		cardSize := image.Pt(
			min(size.X-gtx.Dp(unit.Dp(64)), gtx.Dp(unit.Dp(700))),
			min(size.Y-gtx.Dp(unit.Dp(48)), emptyCanvasCardHeight(gtx.Metric)),
		)
		if cardSize.X < gtx.Dp(unit.Dp(320)) {
			cardSize.X = max(0, size.X-gtx.Dp(unit.Dp(24)))
		}
		if cardSize.Y < gtx.Dp(unit.Dp(260)) {
			cardSize.Y = max(0, size.Y-gtx.Dp(unit.Dp(24)))
		}
		gtx.Constraints.Min = cardSize
		gtx.Constraints.Max = cardSize
		shadowRect := image.Rect(0, gtx.Dp(unit.Dp(8)), cardSize.X, cardSize.Y+gtx.Dp(unit.Dp(8)))
		paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 80}, clip.RRect{Rect: shadowRect, SE: 28, SW: 28, NW: 28, NE: 28}.Op(gtx.Ops))
		cardRect := image.Rectangle{Max: cardSize}
		paint.FillShape(gtx.Ops, color.NRGBA{R: 27, G: 27, B: 31, A: 255}, clip.RRect{Rect: cardRect, SE: 24, SW: 24, NW: 24, NE: 24}.Op(gtx.Ops))
		drawIconRect(gtx.Ops, image.Rect(1, 1, cardSize.X-1, cardSize.Y-1), color.NRGBA{R: 255, G: 255, B: 255, A: 24}, 1)
		return layout.UniformInset(unit.Dp(30)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(9))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutCaptureGlyph(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body2(u.theme, "빠르고 정확한 화면 캡처")
					label.TextSize = unit.Sp(12)
					label.Color = color.NRGBA{R: 100, G: 180, B: 255, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.H5(u.theme, "캡처하고, 바로 다듬으세요")
					label.TextSize = unit.Sp(25)
					label.Color = color.NRGBA{R: 250, G: 250, B: 252, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body1(u.theme, "캡처하거나 이미지 파일을 드롭하세요. 클립보드 이미지는 Ctrl+V로 바로 열 수 있습니다.")
					label.TextSize = unit.Sp(14)
					label.Color = color.NRGBA{R: 174, G: 174, B: 184, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(7)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(9))}.Layout(gtx,
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return accentButton(u.theme, &u.emptyCaptureButton, "새 캡처").Layout(gtx)
							}),
							layout.Rigid(func(gtx layout.Context) layout.Dimensions {
								return neutralButton(u.theme, &u.emptyLoadImageButton, "이미지 열기").Layout(gtx)
							}),
						)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.Body2(u.theme, u.appConfig.Hotkey+" "+u.hotkeyActionValueLabel()+"  ·  파일 드롭  ·  Ctrl+V 붙여넣기")
					label.TextSize = unit.Sp(11)
					label.Color = color.NRGBA{R: 126, G: 126, B: 138, A: 255}
					return label.Layout(gtx)
				}),
			)
		})
	})
}

func emptyCanvasCardHeight(metric unit.Metric) int {
	pxPerDp := metric.PxPerDp
	if pxPerDp <= 0 {
		pxPerDp = 1
	}
	pxPerSp := metric.PxPerSp
	if pxPerSp <= 0 {
		pxPerSp = pxPerDp
	}
	textScale := max(1.0, float64(pxPerSp/pxPerDp))
	dpHeight := 330.0 + (textScale-1.0)*100.0
	return int(math.Ceil(dpHeight * float64(pxPerDp)))
}

func (u *windowUI) layoutCompactEmptyCanvas(gtx layout.Context, size image.Point) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		cardSize := image.Pt(
			min(size.X-gtx.Dp(unit.Dp(32)), gtx.Dp(unit.Dp(620))),
			max(0, size.Y-gtx.Dp(unit.Dp(24))),
		)
		if cardSize.X <= 0 || cardSize.Y <= 0 {
			return layout.Dimensions{Size: size}
		}
		gtx.Constraints.Min = cardSize
		gtx.Constraints.Max = cardSize
		paint.FillShape(gtx.Ops, color.NRGBA{R: 27, G: 27, B: 31, A: 255}, clip.RRect{Rect: image.Rectangle{Max: cardSize}, SE: 18, SW: 18, NW: 18, NE: 18}.Op(gtx.Ops))
		return layout.UniformInset(unit.Dp(18)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(14))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutCaptureGlyph(gtx)
				}),
				layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
					return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(4))}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							label := material.H6(u.theme, "캡처할 준비가 되었습니다")
							label.TextSize = unit.Sp(17)
							label.Color = color.NRGBA{R: 250, G: 250, B: 252, A: 255}
							return label.Layout(gtx)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							label := material.Body2(u.theme, u.appConfig.Hotkey+" "+u.hotkeyActionValueLabel()+", 파일 드롭 또는 Ctrl+V로 시작하세요.")
							label.TextSize = unit.Sp(12)
							label.Color = color.NRGBA{R: 160, G: 160, B: 170, A: 255}
							return label.Layout(gtx)
						}),
					)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return accentButton(u.theme, &u.emptyCaptureButton, "새 캡처").Layout(gtx)
				}),
			)
		})
	})
}

func layoutCaptureGlyph(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(52)), gtx.Dp(unit.Dp(40)))
	body := image.Rect(2, 7, size.X-2, size.Y-2)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 10, G: 132, B: 255, A: 38}, clip.RRect{Rect: body, SE: 11, SW: 11, NW: 11, NE: 11}.Op(gtx.Ops))
	drawIconRect(gtx.Ops, body, color.NRGBA{R: 100, G: 180, B: 255, A: 255}, 2)
	lens := gtx.Dp(unit.Dp(14))
	lensRect := image.Rect((size.X-lens)/2, (size.Y-lens)/2+3, (size.X+lens)/2, (size.Y+lens)/2+3)
	drawIconRect(gtx.Ops, lensRect, color.NRGBA{R: 100, G: 180, B: 255, A: 255}, 2)
	paint.FillShape(gtx.Ops, color.NRGBA{R: 100, G: 180, B: 255, A: 255}, clip.RRect{Rect: image.Rect(15, 2, size.X-15, 10), SE: 4, SW: 4, NW: 4, NE: 4}.Op(gtx.Ops))
	return layout.Dimensions{Size: size}
}

func (u *windowUI) handleButtons(gtx layout.Context, w *app.Window) {
	if u.textEditing {
		u.syncTextEditorEvents(gtx)
		u.flushTextSizeEditorEvents(gtx)
		u.applyTextSizeInput()
	}
	if u.colorPickerOpen {
		u.handleColorPickerButtons(gtx)
		return
	}
	u.handleFilmstripButtons(gtx)
	u.handleTransformButtons(gtx)
	for u.newCaptureButton.Clicked(gtx) {
		u.beginCapture(w)
	}
	for u.emptyCaptureButton.Clicked(gtx) {
		u.beginCapture(w)
	}
	for u.loadImageButton.Clicked(gtx) {
		u.chooseImageFile()
	}
	for u.emptyLoadImageButton.Clicked(gtx) {
		u.chooseImageFile()
	}
	for u.penButton.Clicked(gtx) {
		if !u.commitTextEditing() {
			continue
		}
		u.activeTool = editor.ToolPen
		_ = u.controller.Editor.SetActiveTool(editor.ToolPen)
		u.status = "도구: " + u.activeToolLabel()
	}
	for u.penModeButton.Clicked(gtx) {
		u.penMode = nextPenMode(u.penMode)
		u.status = "펜 방식: " + penModeLabel(u.penMode)
	}
	for u.rectButton.Clicked(gtx) {
		u.selectNextShapeTool()
	}
	for u.arrowButton.Clicked(gtx) {
		if !u.commitTextEditing() {
			continue
		}
		u.activeTool = editor.ToolArrow
		_ = u.controller.Editor.SetActiveTool(editor.ToolArrow)
		u.status = "도구: 화살표"
	}
	for u.mosaicButton.Clicked(gtx) {
		if !u.commitTextEditing() {
			continue
		}
		u.activeTool = editor.ToolMosaic
		_ = u.controller.Editor.SetActiveTool(editor.ToolMosaic)
		u.status = "도구: " + u.activeToolLabel()
	}
	for u.mosaicModeButton.Clicked(gtx) {
		u.mosaicMode = nextMosaicMode(u.mosaicMode)
		u.clearMosaicHoverCache()
		u.status = "모자이크 방식: " + mosaicModeLabel(u.mosaicMode)
	}
	for u.highlightButton.Clicked(gtx) {
		if !u.commitTextEditing() {
			continue
		}
		u.activeTool = editor.ToolHighlight
		_ = u.controller.Editor.SetActiveTool(editor.ToolHighlight)
		u.status = "도구: " + u.activeToolLabel()
	}
	for u.highlightModeButton.Clicked(gtx) {
		u.highlightStraight = !u.highlightStraight
		u.status = "형광펜 방식: " + u.activeToolLabel()
	}
	for u.textButton.Clicked(gtx) {
		u.activeTool = editor.ToolText
		_ = u.controller.Editor.SetActiveTool(editor.ToolText)
		u.status = "도구: 텍스트"
	}
	for u.textBoldButton.Clicked(gtx) {
		u.toggleSelectedTextBold()
	}
	for u.textFontButton.Clicked(gtx) {
		u.textFontDropdownOpen = !u.textFontDropdownOpen
	}
	for i, family := range textFontFamilies() {
		for u.textFontItems[i].Clicked(gtx) {
			if u.textEditingStyle.FontFamily != family {
				u.pushTextEditUndo(u.captureTextEditSnapshot())
			}
			u.textEditingStyle.FontFamily = family
			u.textFontDropdownOpen = false
			u.status = "텍스트 글꼴: " + textFontFamilyLabel(family)
			u.textEditorNeedsFocus = true
		}
	}
	for u.textAlignButton.Clicked(gtx) {
		u.pushTextEditUndo(u.captureTextEditSnapshot())
		u.textEditingStyle.Alignment = nextTextAlignment(u.textEditingStyle.Alignment)
		u.status = "텍스트 정렬: " + textAlignmentLabel(u.textEditingStyle.Alignment)
	}
	for u.textColorButton.Clicked(gtx) {
		u.openColorPicker("text")
	}
	for u.textBackgroundButton.Clicked(gtx) {
		u.pushTextEditUndo(u.captureTextEditSnapshot())
		u.textEditingStyle.BackgroundEnabled = !u.textEditingStyle.BackgroundEnabled
		if u.textEditingStyle.BackgroundEnabled && u.textEditingStyle.BackgroundRGBA[3] == 0 {
			u.textEditingStyle.BackgroundRGBA = editor.DefaultTextStyle().BackgroundRGBA
		}
		u.status = "텍스트 배경: " + onOff(u.textEditingStyle.BackgroundEnabled)
	}
	for u.textBackgroundColorButton.Clicked(gtx) {
		u.openColorPicker("text-background")
	}
	for u.textBackgroundAlphaButton.Clicked(gtx) {
		u.toggleTextBackgroundAlpha()
	}
	for u.textBorderButton.Clicked(gtx) {
		u.pushTextEditUndo(u.captureTextEditSnapshot())
		u.textEditingStyle.BorderEnabled = !u.textEditingStyle.BorderEnabled
		if u.textEditingStyle.BorderEnabled {
			defaults := editor.DefaultTextStyle()
			if u.textEditingStyle.BorderRGBA[3] == 0 {
				u.textEditingStyle.BorderRGBA = defaults.BorderRGBA
			}
			if u.textEditingStyle.BorderWidth <= 0 {
				u.textEditingStyle.BorderWidth = defaults.BorderWidth
			}
		}
		u.status = "텍스트 외곽선: " + onOff(u.textEditingStyle.BorderEnabled)
	}
	for u.textBorderColorButton.Clicked(gtx) {
		u.openColorPicker("text-border")
	}
	for u.outputBorderButton.Clicked(gtx) {
		u.appConfig.OutputBorderEnabled = !u.appConfig.OutputBorderEnabled
		u.applyStrokeStyle()
		u.saveConfigStatus("결과 외곽선: " + onOff(u.appConfig.OutputBorderEnabled))
	}
	for u.copyButton.Clicked(gtx) {
		if u.commitTextEditing() {
			u.copyCurrentImage()
		}
	}
	for u.saveButton.Clicked(gtx) {
		if u.commitTextEditing() {
			u.saveCurrentImageAs()
		}
	}
	for u.settingsButton.Clicked(gtx) {
		u.showSettings = !u.showSettings
		if u.showSettings {
			u.showModeDropdown = false
		} else {
			u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
		}
	}
	for u.settingsCloseButton.Clicked(gtx) {
		u.showSettings = false
		u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
	}
	for u.captureModeButton.Clicked(gtx) {
		u.showModeDropdown = !u.showModeDropdown
		if u.showModeDropdown {
			u.showSettings = false
			u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
		}
	}
	for i, mode := range []string{"Region", "Window", "Scroll", "Freeform", "Fullscreen"} {
		for u.captureModeItems[i].Clicked(gtx) {
			u.appConfig.CaptureMode = mode
			u.showModeDropdown = false
			u.saveConfigStatus("캡처 모드 변경: " + modeLabel(mode))
		}
	}
	for u.hotkeyButton.Clicked(gtx) {
		u.beginHotkeyCapture()
		w.Invalidate()
	}
	for u.hotkeyActionButton.Clicked(gtx) {
		u.appConfig.HotkeyOpensWindow = !u.appConfig.HotkeyOpensWindow
		u.saveConfigStatus("단축키 동작: " + u.hotkeyActionValueLabel())
	}
	for u.rememberRegionButton.Clicked(gtx) {
		u.showSettings = false
		u.beginCaptureRequest(w, "Region", true, false)
	}
	for u.savedRegionButton.Clicked(gtx) {
		if len(u.appConfig.SavedRegions) == 0 {
			u.status = "먼저 화면 좌표를 기억해 주세요."
			continue
		}
		if u.appConfig.ActiveSavedRegionID == "" {
			u.appConfig.ActiveSavedRegionID = u.appConfig.SavedRegions[0].ID
		}
		u.appConfig.SavedRegionEnabled = !u.appConfig.SavedRegionEnabled
		u.saveConfigStatus("기억 좌표 사용: " + onOff(u.appConfig.SavedRegionEnabled))
	}
	for i := range min(len(u.savedRegionSelectButtons), len(u.appConfig.SavedRegions)) {
		for u.savedRegionSelectButtons[i].Clicked(gtx) {
			u.appConfig.ActiveSavedRegionID = u.appConfig.SavedRegions[i].ID
			u.appConfig.SavedRegionEnabled = true
			u.saveConfigStatus("활성 캡처 좌표: " + u.appConfig.SavedRegions[i].Name)
		}
	}
	for i := range min(len(u.savedRegionDeleteButtons), len(u.appConfig.SavedRegions)) {
		deleted := false
		for u.savedRegionDeleteButtons[i].Clicked(gtx) {
			current := u.appConfig
			next := current
			name := current.SavedRegions[i].Name
			id := current.SavedRegions[i].ID
			next.SavedRegions = append([]config.SavedRegionPreset(nil), current.SavedRegions[:i]...)
			next.SavedRegions = append(next.SavedRegions, current.SavedRegions[i+1:]...)
			if next.ActiveSavedRegionID == id {
				next.ActiveSavedRegionID = ""
				if len(next.SavedRegions) > 0 {
					next.ActiveSavedRegionID = next.SavedRegions[0].ID
				}
			}
			if len(next.SavedRegions) == 0 {
				next.SavedRegionEnabled = false
			}
			if !u.saveConfigCandidate(next, "저장 좌표 삭제: "+name) {
				break
			}
			u.appConfig = next
			u.ensureSavedRegionButtons()
			deleted = true
			break
		}
		if deleted {
			break
		}
	}
	for u.editAfterCaptureButton.Clicked(gtx) {
		u.appConfig.EditAfterCapture = !u.appConfig.EditAfterCapture
		u.saveConfigStatus("캡처 후 편집: " + onOff(u.appConfig.EditAfterCapture))
	}
	for u.hideMinButton.Clicked(gtx) {
		u.appConfig.HideOnMinimize = !u.appConfig.HideOnMinimize
		u.saveConfigStatus("최소화 시 트레이 설정: " + onOff(u.appConfig.HideOnMinimize))
	}
	for u.hideFocusButton.Clicked(gtx) {
		u.appConfig.HideOnFocusLoss = !u.appConfig.HideOnFocusLoss
		u.saveConfigStatus("자동 숨기기 설정: " + onOff(u.appConfig.HideOnFocusLoss))
	}
	for u.autoSaveButton.Clicked(gtx) {
		u.appConfig.AutoSave = !u.appConfig.AutoSave
		u.saveConfigStatus("자동 저장 설정: " + onOff(u.appConfig.AutoSave))
	}
	for u.saveDirApplyButton.Clicked(gtx) {
		u.applySaveDirText()
	}
	for u.saveDirBrowseButton.Clicked(gtx) {
		u.chooseSaveDir()
	}
	for i, preset := range visibleStrokePresets() {
		for u.paletteButtons[i].Clicked(gtx) {
			u.setStrokeColor(preset.name)
			u.saveConfigStatus("선 색상 변경: " + colorLabel(preset.name))
		}
		for u.borderPaletteButtons[i].Clicked(gtx) {
			u.setOutputBorderColor(preset.name)
			u.saveConfigStatus("결과 외곽선 색상 변경: " + colorLabel(preset.name))
		}
	}
	for u.rgbApplyButton.Clicked(gtx) {
		if rgb, ok := parseRGBText(u.rgbEditor.Text()); ok {
			u.setStrokeColor(rgbName(rgb))
			u.saveConfigStatus("선 색상 RGB 변경: " + u.appConfig.StrokeColor)
		} else {
			u.status = "RGB 형식은 255,64,64처럼 R,G,B로 입력하세요."
			u.showToastDetail("RGB 입력 오류", u.status)
		}
	}
	for u.strokePickerButton.Clicked(gtx) {
		u.openColorPicker("stroke")
	}
	for u.borderPickerButton.Clicked(gtx) {
		u.openColorPicker("border")
	}
	for u.borderRGBApplyButton.Clicked(gtx) {
		if rgb, ok := parseRGBText(u.borderRGBEditor.Text()); ok {
			u.setOutputBorderColor(rgbName(rgb))
			u.saveConfigStatus("결과 외곽선 RGB 변경: " + u.appConfig.OutputBorderColor)
		} else {
			u.status = "RGB 형식은 0,0,0처럼 R,G,B로 입력하세요."
			u.showToastDetail("RGB 입력 오류", u.status)
		}
	}
}

func (u *windowUI) handleColorPickerButtons(gtx layout.Context) {
	for u.colorPickerCloseButton.Clicked(gtx) {
		u.colorPickerOpen = false
		u.textEditorNeedsFocus = u.textEditing
	}
	for i, preset := range colorPickerPalette() {
		for u.colorPickerButtons[i].Clicked(gtx) {
			name := rgbName(preset.rgba)
			if u.colorPickerTarget == "border" {
				u.setOutputBorderColor(name)
				u.saveConfigStatus("결과 외곽선 색상 변경: " + name)
			} else if u.colorPickerTarget == "text" {
				u.pushTextEditUndo(u.captureTextEditSnapshot())
				u.textEditingStyle.ColorRGBA = preset.rgba
				u.status = "텍스트 색상 변경: " + name
			} else if u.colorPickerTarget == "text-background" {
				u.setTextBackgroundColor(preset.rgba)
				u.status = "텍스트 배경색 변경: " + name
			} else if u.colorPickerTarget == "text-border" {
				u.pushTextEditUndo(u.captureTextEditSnapshot())
				u.textEditingStyle.BorderRGBA = preset.rgba
				u.textEditingStyle.BorderEnabled = true
				u.status = "텍스트 외곽선 색 변경: " + name
			} else {
				u.setStrokeColor(name)
				u.saveConfigStatus("선 색상 변경: " + name)
			}
			u.colorPickerOpen = false
			u.textEditorNeedsFocus = u.textEditing
		}
	}
}

func (u *windowUI) handleShortcuts(gtx layout.Context) {
	filters := []event.Filter{
		key.Filter{Name: key.NameEscape},
		key.Filter{Name: "1"},
		key.Filter{Name: "2"},
		key.Filter{Name: "3"},
		key.Filter{Name: "4"},
		key.Filter{Name: "5"},
		key.Filter{Name: "6"},
		key.Filter{Name: "S", Required: key.ModShortcut},
		key.Filter{Name: "s", Required: key.ModShortcut},
		key.Filter{Name: "C", Required: key.ModShortcut},
		key.Filter{Name: "c", Required: key.ModShortcut},
		key.Filter{Name: "V", Required: key.ModShortcut},
		key.Filter{Name: "v", Required: key.ModShortcut},
		key.Filter{Name: "Z", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Name: "z", Required: key.ModShortcut, Optional: key.ModShift},
		key.Filter{Name: "Y", Required: key.ModShortcut},
		key.Filter{Name: "y", Required: key.ModShortcut},
		key.Filter{Name: "", Required: key.ModShortcut, Optional: key.ModShift},
	}
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok || ke.State != key.Press {
			continue
		}
		u.handleShortcutKey(ke)
	}
}

func (u *windowUI) handleHotkeyCapture(gtx layout.Context) {
	filters := hotkeyCaptureFilters(&u.keyTag)
	for {
		ev, ok := gtx.Event(filters...)
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if !ok {
			continue
		}
		u.handleShortcutKey(ke)
	}
}

func hotkeyCaptureFilters(tag event.Tag) []event.Filter {
	return []event.Filter{
		key.FocusFilter{Target: tag},
		key.Filter{Focus: tag, Name: key.NameEscape, Optional: allShortcutModifiers()},
		key.Filter{Focus: tag, Name: "", Optional: allShortcutModifiers()},
	}
}

func (u *windowUI) handleShortcutKey(ke key.Event) bool {
	if u.rotation.Active {
		if ke.Name == key.NameEscape {
			u.cancelRotation()
			u.status = "회전을 취소했습니다."
		}
		return true
	}
	if u.hotkeyCaptureMode {
		return u.handleHotkeyCaptureKey(ke)
	}
	switch strings.ToUpper(string(ke.Name)) {
	case string(key.NameEscape):
		u.dragging = false
		u.cropDragging = false
		u.hideForEscape()
		return true
	case "1":
		if ke.Modifiers == 0 {
			u.selectNextShapeTool()
			return true
		}
	case "2":
		if ke.Modifiers == 0 {
			return u.handleToolShortcut(editor.ToolPen)
		}
	case "3":
		if ke.Modifiers == 0 {
			return u.handleToolShortcut(editor.ToolArrow)
		}
	case "4":
		if ke.Modifiers == 0 {
			return u.handleToolShortcut(editor.ToolMosaic)
		}
	case "5":
		if ke.Modifiers == 0 {
			return u.handleToolShortcut(editor.ToolHighlight)
		}
	case "6":
		if ke.Modifiers == 0 {
			return u.handleToolShortcut(editor.ToolText)
		}
	case "S":
		if ke.Modifiers.Contain(key.ModShortcut) {
			if ke.Modifiers.Contain(key.ModShift) {
				u.saveCurrentImage()
			} else {
				u.saveCurrentImageAs()
			}
			return true
		}
	case "C":
		if ke.Modifiers.Contain(key.ModShortcut) {
			u.copyCurrentImage()
			return true
		}
	case "V":
		if ke.Modifiers.Contain(key.ModShortcut) {
			u.startClipboardImageLoad()
			return true
		}
	case "Z":
		if ke.Modifiers.Contain(key.ModShortcut) {
			if ke.Modifiers.Contain(key.ModShift) {
				u.redoEdit()
			} else {
				u.undoEdit()
			}
			return true
		}
	case "Y":
		if ke.Modifiers.Contain(key.ModShortcut) {
			u.redoEdit()
			return true
		}
	}
	return false
}

func (u *windowUI) handleToolShortcut(tool editor.Tool) bool {
	if u.activeTool == tool {
		switch tool {
		case editor.ToolPen:
			u.penMode = nextPenMode(u.penMode)
			u.status = "펜 방식: " + penModeLabel(u.penMode)
		case editor.ToolMosaic:
			u.mosaicMode = nextMosaicMode(u.mosaicMode)
			u.clearMosaicHoverCache()
			u.status = "모자이크 방식: " + mosaicModeLabel(u.mosaicMode)
		case editor.ToolHighlight:
			u.highlightStraight = !u.highlightStraight
			u.status = "형광펜 방식: " + drawingModeLabel(u.highlightStraight)
		}
		return true
	}
	if !u.commitTextEditing() {
		return true
	}
	u.activeTool = tool
	_ = u.controller.Editor.SetActiveTool(tool)
	u.status = "도구: " + u.activeToolLabel()
	return true
}

func isShapeTool(tool editor.Tool) bool {
	return tool == editor.ToolRectangle || tool == editor.ToolTriangle || tool == editor.ToolCircle
}

func nextShapeTool(tool editor.Tool) editor.Tool {
	switch tool {
	case editor.ToolRectangle:
		return editor.ToolTriangle
	case editor.ToolTriangle:
		return editor.ToolCircle
	case editor.ToolCircle:
		return editor.ToolRectangle
	default:
		return editor.ToolRectangle
	}
}

func (u *windowUI) shapeButtonTool() editor.Tool {
	if isShapeTool(u.activeTool) {
		return u.activeTool
	}
	return editor.ToolRectangle
}

func (u *windowUI) selectNextShapeTool() {
	if !u.commitTextEditing() {
		return
	}
	u.activeTool = nextShapeTool(u.activeTool)
	_ = u.controller.Editor.SetActiveTool(u.activeTool)
	u.status = "도구: " + u.activeToolLabel()
}

func (u *windowUI) handleHotkeyCaptureKey(ke key.Event) bool {
	if ke.Name == key.NameEscape && ke.State == key.Press && ke.Modifiers == 0 {
		u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
		u.hideForEscape()
		return true
	}
	switch ke.State {
	case key.Press:
		u.recordHotkeyCapturePress(ke)
	case key.Release:
		u.recordHotkeyCaptureRelease(ke)
	default:
		return false
	}
	return true
}

func (u *windowUI) hideForEscape() {
	if u.hideToTrayOnEscape != nil {
		u.hideToTrayOnEscape()
		return
	}
	u.status = "현재 동작을 취소했습니다."
}

func (u *windowUI) recordHotkeyCapturePress(ke key.Event) {
	for _, modifier := range hotkeyCaptureModifierNames(ke.Modifiers) {
		u.hotkeyCaptureEventKeys = appendUniqueKey(u.hotkeyCaptureEventKeys, modifier)
	}
	name := hotkeyCaptureEventKeyName(ke.Name)
	if name != "" {
		u.hotkeyCaptureEventKeys = appendUniqueKey(u.hotkeyCaptureEventKeys, name)
	}
	u.refreshHotkeyCapturePressed()
	u.updateHotkeyCapturePreview()
}

func (u *windowUI) recordHotkeyCaptureRelease(ke key.Event) {
	if name := hotkeyCaptureEventKeyName(ke.Name); name != "" {
		u.hotkeyCaptureEventKeys = removeCapturedKey(u.hotkeyCaptureEventKeys, name)
	}
	u.refreshHotkeyCapturePressed()
	u.finishHotkeyCaptureInput()
}

func (u *windowUI) saveCurrentImage() {
	if u.sourceImagePath == "" || !exporter.SupportsDirectSavePath(u.sourceImagePath) {
		u.saveCurrentImageAs()
		return
	}
	u.saveCurrentImageToPath(u.sourceImagePath)
}

func (u *windowUI) saveCurrentImageAs() {
	picker := u.pickImageSavePath
	if picker == nil {
		picker = platformwindows.PickImageSavePath
	}
	path, ok, err := picker(u.suggestedSavePath())
	if err != nil {
		u.status = "저장 위치 선택 실패: " + err.Error()
		u.showToastDetail("저장 위치 선택 실패", err.Error())
		return
	}
	if !ok {
		u.status = "저장을 취소했습니다."
		return
	}
	if u.saveCurrentImageToPath(path) {
		u.sourceImagePath = filepath.Clean(path)
	}
}

func (u *windowUI) suggestedSavePath() string {
	if u.sourceImagePath != "" {
		if exporter.SupportsDirectSavePath(u.sourceImagePath) {
			return u.sourceImagePath
		}
		return strings.TrimSuffix(u.sourceImagePath, filepath.Ext(u.sourceImagePath)) + ".png"
	}
	return filepath.Join(u.configuredSaveDir(), "InstantCapture_"+time.Now().Format("20060102_150405")+".png")
}

func (u *windowUI) saveCurrentImageToPath(path string) bool {
	u.cancelRotation()
	path = filepath.Clean(path)
	err := u.controller.SaveImage(path)
	if err != nil {
		u.status = "저장 실패: " + err.Error()
		u.showToastDetail("저장 실패", err.Error())
		return false
	}
	u.requestFilmstripRefresh()
	u.showToastDetail("이미지 저장 완료", toastSavePath(path))
	u.status = "저장 완료: " + path
	return true
}

func (u *windowUI) autoSaveCurrentImage() {
	if !u.appConfig.AutoSave {
		return
	}
	dir := u.configuredSaveDir()
	path, err := u.controller.SaveOriginalPNG(dir)
	if err != nil {
		u.status = "원본 자동 저장 실패: " + err.Error()
		u.showToastDetail("원본 자동 저장 실패", err.Error())
		return
	}
	u.requestFilmstripRefresh()
	u.showToastDetail("원본 PNG 자동 저장 완료", toastSavePath(path))
	u.status = "원본 자동 저장 완료: " + path
}

func (u *windowUI) configuredSaveDir() string {
	return resolveSaveDir(u.appConfig, defaultSaveDir())
}

func resolveSaveDir(cfg config.Config, fallback string) string {
	dir := config.NormalizeSaveDir(cfg.SaveDir)
	if dir != "" {
		return dir
	}
	return fallback
}

func (u *windowUI) applySaveDirText() {
	dir := config.NormalizeSaveDir(u.saveDirEditor.Text())
	u.appConfig.SaveDir = dir
	if dir == "" {
		u.saveDirEditor.SetText(defaultSaveDir())
		u.saveConfigStatus("저장 폴더를 기본값으로 되돌렸습니다.")
	} else {
		u.saveDirEditor.SetText(dir)
		u.saveConfigStatus("저장 폴더 변경: " + dir)
	}
	u.requestFilmstripRefresh()
}

func (u *windowUI) chooseSaveDir() {
	if u.saveDirBrowseActive {
		u.status = "폴더 선택 창이 이미 열려 있습니다."
		return
	}
	picker := u.pickFolderPath
	if picker == nil {
		picker = platformwindows.PickFolder
	}
	if u.saveDirBrowseResults == nil {
		u.saveDirBrowseResults = make(chan saveDirBrowseResult, 1)
	}
	u.saveDirBrowseActive = true
	u.status = "저장 폴더를 선택하세요."
	initialDir := u.configuredSaveDir()
	results := u.saveDirBrowseResults
	go func() {
		dir, ok, err := picker(initialDir, windowTitle)
		results <- saveDirBrowseResult{dir: dir, ok: ok, err: err}
	}()
}

func (u *windowUI) handleSaveDirBrowseResult(result saveDirBrowseResult) {
	u.saveDirBrowseActive = false
	if result.err != nil {
		u.status = "폴더 선택 실패: " + result.err.Error()
		u.showToastDetail("폴더 선택 실패", result.err.Error())
		return
	}
	if !result.ok {
		u.status = "폴더 선택을 취소했습니다."
		return
	}
	dir := config.NormalizeSaveDir(result.dir)
	u.appConfig.SaveDir = dir
	u.saveDirEditor.SetText(dir)
	u.saveConfigStatus("저장 폴더 변경: " + dir)
	u.requestFilmstripRefresh()
}

func (u *windowUI) chooseImageFile() {
	if u.imageLoadActive {
		u.status = "이미 사진을 불러오는 중입니다."
		return
	}
	path, ok, err := platformwindows.PickImageFile(u.configuredSaveDir())
	if err != nil {
		u.status = "사진 선택 실패: " + err.Error()
		u.showToastDetail("사진 선택 실패", err.Error())
		return
	}
	if !ok {
		u.status = "사진 불러오기를 취소했습니다."
		return
	}
	u.startImageLoad(path)
}

func (u *windowUI) startImageLoad(path string) {
	u.beginImageLoad(path, filepath.Base(path), func() (image.Image, error) {
		return decodeImageFile(path)
	})
}

func (u *windowUI) startClipboardImageLoad() {
	read := u.readClipboardImage
	if read == nil {
		read = readClipboardPNG
	}
	u.beginImageLoad("", "클립보드 이미지", func() (image.Image, error) {
		data, err := read()
		if err != nil {
			return nil, err
		}
		return decodeImageBytes(data)
	})
}

func (u *windowUI) beginImageLoad(path, displayName string, load func() (image.Image, error)) {
	if !u.commitTextEditing() {
		return
	}
	if u.captureActive || u.overlayActive {
		u.status = "캡처가 끝난 뒤 이미지를 불러올 수 있습니다."
		return
	}
	if u.imageLoadActive {
		u.status = "이미 사진을 불러오는 중입니다."
		return
	}
	u.imageLoadActive = true
	u.imageLoadGeneration++
	generation := u.imageLoadGeneration
	u.status = "사진을 불러오는 중입니다: " + displayName
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		img, err := load()
		select {
		case u.imageLoadResults <- imageLoadResult{generation: generation, path: path, displayName: displayName, img: img, err: err}:
		case <-ctx.Done():
		}
	}()
}

func (u *windowUI) handleImageLoadResult(result imageLoadResult, w *app.Window) {
	if result.generation != u.imageLoadGeneration {
		return
	}
	if w != nil {
		defer w.Invalidate()
	}
	u.imageLoadActive = false
	if result.err != nil {
		u.status = "사진 불러오기 실패: " + result.err.Error()
		u.showToastDetail("사진 불러오기 실패", result.err.Error())
		return
	}
	if err := u.commitLoadedImage(result.img); err != nil {
		u.status = "사진 불러오기 실패: " + err.Error()
		u.showToastDetail("사진 불러오기 실패", err.Error())
		return
	}
	u.sourceImagePath = result.path
	if u.sourceImagePath != "" {
		u.sourceImagePath = filepath.Clean(u.sourceImagePath)
	}
	if u.filmstrip != nil {
		u.filmstrip.selected = result.path
	}
	size := u.controller.CapturedImage.Bounds().Size()
	u.status = fmt.Sprintf("%dx%d 사진을 편집 중입니다.", size.X, size.Y)
	displayName := result.displayName
	if displayName == "" {
		displayName = filepath.Base(result.path)
	}
	u.showToastDetail("사진 불러오기 완료", displayName)
}

func decodeImageFile(path string) (image.Image, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open image: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat image: %w", err)
	}
	if !info.Mode().IsRegular() || info.Size() > maxEditorImageFileBytes {
		return nil, fmt.Errorf("image file is too large")
	}
	return decodeImageReader(file, info.Size())
}

func readClipboardPNG() ([]byte, error) {
	if err := appclipboard.Init(); err != nil {
		return nil, fmt.Errorf("클립보드 초기화 실패: %w", err)
	}
	data := appclipboard.Read(appclipboard.FmtImage)
	if len(data) == 0 {
		return nil, fmt.Errorf("클립보드에 이미지가 없습니다")
	}
	return append([]byte(nil), data...), nil
}

func decodeImageBytes(data []byte) (image.Image, error) {
	return decodeImageReader(bytes.NewReader(data), int64(len(data)))
}

func decodeImageReader(reader io.ReadSeeker, size int64) (image.Image, error) {
	if size <= 0 || size > maxEditorImageFileBytes {
		return nil, fmt.Errorf("image file is too large")
	}
	decodedConfig, _, err := image.DecodeConfig(reader)
	if err != nil {
		return nil, fmt.Errorf("read image size: %w", err)
	}
	if !validImageDimensions(decodedConfig.Width, decodedConfig.Height, maxEditorImagePixels) {
		return nil, fmt.Errorf("image dimensions are too large")
	}
	if _, err := reader.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind image: %w", err)
	}
	img, _, err := image.Decode(reader)
	if err != nil {
		return nil, fmt.Errorf("decode image: %w", err)
	}
	return normalizeDecodedImage(img), nil
}

func validImageDimensions(width, height int, maxPixels int64) bool {
	return width > 0 && height > 0 && int64(width) <= maxPixels/int64(height)
}

func normalizeDecodedImage(src image.Image) *image.RGBA {
	if rgba, ok := src.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) {
		return rgba
	}
	rect := src.Bounds()
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

func (u *windowUI) commitLoadedImage(img image.Image) error {
	if err := u.controller.CommitImportedImage(img); err != nil {
		return err
	}
	u.resetCanvasView()
	u.clearEditHistory()
	u.autoCopyPending = false
	u.showModeDropdown = false
	u.showSettings = false
	u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
	return nil
}

func toastSavePath(path string) string {
	dir := filepath.Dir(path)
	name := filepath.Base(path)
	if dir == "." || dir == "" {
		return name
	}
	return name + "  |  " + dir
}

func defaultSaveDir() string {
	return platformwindows.DefaultScreenshotDir()
}

func (u *windowUI) copyCurrentImage() {
	if err := u.copyPNGCurrentImage(); err != nil {
		u.status = "복사 실패: " + err.Error()
		u.showToastDetail("복사 실패", err.Error())
	} else {
		u.status = "PNG를 클립보드에 복사했습니다."
		u.showToastDetail("PNG 복사 완료", "이미지가 클립보드에 들어갔습니다")
	}
}

func (u *windowUI) copyPNGCurrentImage() error {
	u.cancelRotation()
	if u.copyPNG != nil {
		return u.copyPNG()
	}
	return u.controller.CopyPNG()
}

func (u *windowUI) finishCaptureReadyWithInitialCopy(status string) {
	u.sourceImagePath = ""
	if u.filmstrip != nil {
		u.filmstrip.selected = ""
	}
	u.autoCopyPending = true
	if err := u.copyPNGCurrentImage(); err != nil {
		u.status = status + " 최초 복사 실패: " + err.Error()
		u.showToastDetail("최초 복사 실패", err.Error())
		return
	}
	u.status = status + " 첫 캡처를 클립보드에 복사했습니다."
	u.showToastDetail("캡처 복사 완료", "첫 캡처 이미지를 클립보드에 복사했습니다")
}

func (u *windowUI) undoEdit() {
	if u.undoLastEdit() {
		u.status = "실행 취소했습니다."
		u.showToast("실행 취소")
	} else {
		u.showToast("실행 취소할 항목이 없습니다")
	}
}

func (u *windowUI) redoEdit() {
	if u.redoLastEdit() {
		u.status = "다시 실행했습니다."
		u.showToast("다시 실행")
	} else {
		u.showToast("다시 실행할 항목이 없습니다")
	}
}

func (u *windowUI) undoLastEdit() bool {
	u.cancelRotation()
	if len(u.undoStack) == 0 {
		return false
	}
	entry := u.undoStack[len(u.undoStack)-1]
	u.undoStack = u.undoStack[:len(u.undoStack)-1]
	switch entry.Kind {
	case uiEditHistoryController:
		if !u.controller.UndoEdit() {
			return false
		}
		u.restoreCanvasView(entry.From)
	case uiEditHistoryCanvasView:
		u.restoreCanvasView(entry.From)
	default:
		return false
	}
	u.clearMosaicHoverCache()
	u.redoStack = appendBoundedUIHistory(u.redoStack, entry)
	return true
}

func (u *windowUI) redoLastEdit() bool {
	u.cancelRotation()
	if len(u.redoStack) == 0 {
		return false
	}
	entry := u.redoStack[len(u.redoStack)-1]
	u.redoStack = u.redoStack[:len(u.redoStack)-1]
	switch entry.Kind {
	case uiEditHistoryController:
		if !u.controller.RedoEdit() {
			return false
		}
		u.restoreCanvasView(entry.To)
	case uiEditHistoryCanvasView:
		u.restoreCanvasView(entry.To)
	default:
		return false
	}
	u.clearMosaicHoverCache()
	u.undoStack = appendBoundedUIHistory(u.undoStack, entry)
	return true
}

func (u *windowUI) recordControllerEdit(from, to canvasViewState) {
	u.undoStack = appendBoundedUIHistory(u.undoStack, uiEditHistoryEntry{Kind: uiEditHistoryController, From: from, To: to})
	u.redoStack = nil
}

func (u *windowUI) recordCanvasViewEdit(from, to canvasViewState) {
	if from == to {
		return
	}
	u.undoStack = appendBoundedUIHistory(u.undoStack, uiEditHistoryEntry{Kind: uiEditHistoryCanvasView, From: from, To: to})
	u.redoStack = nil
}

func (u *windowUI) clearEditHistory() {
	u.undoStack = nil
	u.redoStack = nil
}

func appendBoundedUIHistory(stack []uiEditHistoryEntry, entry uiEditHistoryEntry) []uiEditHistoryEntry {
	stack = append(stack, entry)
	if len(stack) > workflow.MaxEditHistoryEntries {
		copy(stack, stack[len(stack)-workflow.MaxEditHistoryEntries:])
		stack = stack[:workflow.MaxEditHistoryEntries]
	}
	return stack
}

func (u *windowUI) captureCanvasView() canvasViewState {
	zoom := u.canvasZoom
	if zoom <= 0 {
		zoom = 1
	}
	return canvasViewState{Zoom: zoom, Pan: u.canvasPan, SourceSize: u.canvasSourceSize}
}

func (u *windowUI) restoreCanvasView(state canvasViewState) {
	u.cancelRotation()
	if state.Zoom <= 0 {
		state.Zoom = 1
	}
	u.canvasZoom = state.Zoom
	u.canvasPan = state.Pan
	u.canvasSourceSize = state.SourceSize
	u.canvasFitBounds = image.Point{}
	u.canvasPanning = false
	u.cropDragging = false
	u.cropHoverHandle = cropHandleNone
	u.cropDragHandle = cropHandleNone
	u.cropStartRect = image.Rectangle{}
	u.cropPreviewRect = image.Rectangle{}
}

func (u *windowUI) canvasViewport(sourceRect, canvasBounds image.Rectangle) coords.Viewport {
	base := coords.FitContain(sourceRect, canvasBounds)
	if sourceRect.Empty() || canvasBounds.Empty() || base.View.Empty() {
		return base
	}
	autoFitResized := u.canvasFitBounds != image.Point{} && u.canvasFitBounds != canvasBounds.Size()
	if u.canvasZoom <= 0 || u.canvasSourceSize != sourceRect.Size() || autoFitResized {
		u.canvasZoom = initialCanvasZoom(sourceRect, canvasBounds, base.Scale)
		u.canvasPan = image.Point{}
		u.canvasSourceSize = sourceRect.Size()
		u.canvasFitBounds = canvasBounds.Size()
	}

	scale := u.canvasZoom
	viewSize := image.Pt(
		max(1, int(math.Round(float64(sourceRect.Dx())*scale))),
		max(1, int(math.Round(float64(sourceRect.Dy())*scale))),
	)
	centeredMin := image.Pt(
		canvasBounds.Min.X+(canvasBounds.Dx()-viewSize.X)/2,
		canvasBounds.Min.Y+(canvasBounds.Dy()-viewSize.Y)/2,
	)
	u.canvasPan = clampCanvasPan(u.canvasPan, centeredMin, viewSize, canvasBounds)
	viewMin := centeredMin.Add(u.canvasPan)
	return coords.Viewport{Source: sourceRect, View: image.Rectangle{Min: viewMin, Max: viewMin.Add(viewSize)}, Scale: scale}
}

// Small captures are enlarged to fill the canvas, leaving room for the crop
// and rotation handles that sit just outside the image edge.
func initialCanvasZoom(sourceRect, canvasBounds image.Rectangle, fitScale float64) float64 {
	if fitScale <= 1 {
		return fitScale
	}
	inset := canvasBounds.Inset(canvasFitMargin)
	if inset.Empty() {
		return 1
	}
	return math.Max(1, math.Min(maxCanvasZoom, coords.FitContain(sourceRect, inset).Scale))
}

func clampCanvasPan(pan, centeredMin, viewSize image.Point, bounds image.Rectangle) image.Point {
	pan.X = clampPanAxis(pan.X, centeredMin.X, viewSize.X, bounds.Min.X, bounds.Max.X)
	pan.Y = clampPanAxis(pan.Y, centeredMin.Y, viewSize.Y, bounds.Min.Y, bounds.Max.Y)
	return pan
}

func clampPanAxis(pan, centeredMin, viewSize, minBound, maxBound int) int {
	if viewSize <= maxBound-minBound {
		return 0
	}
	minPan := maxBound - (centeredMin + viewSize)
	maxPan := minBound - centeredMin
	if pan < minPan {
		return minPan
	}
	if pan > maxPan {
		return maxPan
	}
	return pan
}

func (u *windowUI) zoomCanvas(scrollY float32, anchor editor.Point, canvasBounds image.Rectangle, viewport coords.Viewport) {
	if scrollY == 0 || viewport.Scale <= 0 || viewport.Source.Empty() || canvasBounds.Empty() {
		return
	}
	if u.canvasZoom <= 0 {
		u.canvasZoom = 1
	}
	before := u.captureCanvasView()
	oldZoom := u.canvasZoom
	factor := math.Pow(1.0015, -float64(scrollY))
	nextZoom := math.Max(minCanvasZoom, math.Min(maxCanvasZoom, u.canvasZoom*factor))
	if math.Abs(nextZoom-1) < 0.025 {
		nextZoom = 1
	}
	u.canvasZoom = nextZoom
	if u.canvasZoom == oldZoom {
		return
	}
	u.canvasFitBounds = image.Point{}

	newScale := u.canvasZoom
	viewSize := image.Pt(
		max(1, int(math.Round(float64(viewport.Source.Dx())*newScale))),
		max(1, int(math.Round(float64(viewport.Source.Dy())*newScale))),
	)
	centeredMin := image.Pt(
		canvasBounds.Min.X+(canvasBounds.Dx()-viewSize.X)/2,
		canvasBounds.Min.Y+(canvasBounds.Dy()-viewSize.Y)/2,
	)
	anchorSource := image.Pt(anchor.X, anchor.Y)
	anchorView := viewport.SourceToView(anchorSource)
	desiredMin := image.Pt(
		anchorView.X-int(math.Round(float64(anchorSource.X-viewport.Source.Min.X)*newScale)),
		anchorView.Y-int(math.Round(float64(anchorSource.Y-viewport.Source.Min.Y)*newScale)),
	)
	u.canvasPan = clampCanvasPan(desiredMin.Sub(centeredMin), centeredMin, viewSize, canvasBounds)
	u.recordCanvasViewEdit(before, u.captureCanvasView())
	u.status = fmt.Sprintf("확대: %.0f%%", u.canvasZoom*100)
}

func pixelAlignedImageScale(scale float64) bool {
	return scale >= 1 && math.Abs(scale-math.Round(scale)) < 0.000001
}

func (u *windowUI) addCanvasCursor(ops *op.Ops) {
	if u.rotation.Active || u.rotation.Hover {
		pointer.CursorGrab.Add(ops)
		return
	}
	if u.canvasPanning {
		pointer.CursorAllScroll.Add(ops)
		return
	}
	handle := u.cropHoverHandle
	if u.cropDragging {
		handle = u.cropDragHandle
	}
	switch handle {
	case cropHandleLeft | cropHandleTop, cropHandleRight | cropHandleBottom:
		pointer.CursorNorthWestSouthEastResize.Add(ops)
	case cropHandleRight | cropHandleTop, cropHandleLeft | cropHandleBottom:
		pointer.CursorNorthEastSouthWestResize.Add(ops)
	case cropHandleLeft, cropHandleRight:
		pointer.CursorEastWestResize.Add(ops)
	case cropHandleTop, cropHandleBottom:
		pointer.CursorNorthSouthResize.Add(ops)
	default:
		pointer.CursorCrosshair.Add(ops)
	}
}

type toolCursorSpec struct {
	Tool   editor.Tool
	Center editor.Point
	Color  color.NRGBA
	Width  float32
}

func (u *windowUI) currentToolCursor() (toolCursorSpec, bool) {
	if u.rotation.Active || u.rotation.Hover {
		return toolCursorSpec{}, false
	}
	if u.controller == nil || u.controller.Mode != workflow.ModeEditing || u.textEditing || !u.canvasPointerIn || u.canvasPanning || u.cropDragging || u.cropHoverHandle != cropHandleNone {
		return toolCursorSpec{}, false
	}

	style := u.controller.AnnotationStyle
	spec := toolCursorSpec{Tool: u.activeTool, Center: u.canvasPointer}
	switch u.activeTool {
	case editor.ToolPen:
		if u.penMode == editor.PenModeRectangle {
			return toolCursorSpec{}, false
		}
	case editor.ToolHighlight:
		style = u.controller.HighlighterStyle
	case editor.ToolMosaic:
		if u.mosaicMode == editor.MosaicModeRectangle {
			return toolCursorSpec{}, false
		}
		style = u.controller.MosaicStyle
		spec.Color = color.NRGBA{R: 129, G: 140, B: 248, A: 255}
	case editor.ToolLine, editor.ToolRectangle, editor.ToolTriangle, editor.ToolCircle, editor.ToolArrow, editor.ToolText:
	default:
		return toolCursorSpec{}, false
	}
	if spec.Color.A == 0 {
		spec.Color = color.NRGBA{R: style.ColorRGBA[0], G: style.ColorRGBA[1], B: style.ColorRGBA[2], A: style.ColorRGBA[3]}
	}
	spec.Width = float32(style.Width)
	return spec, spec.Width > 0
}

func (u *windowUI) mosaicHoverCommand() (editor.RenderCommand, bool) {
	if u.controller == nil || u.controller.Mode != workflow.ModeEditing || u.activeTool != editor.ToolMosaic || u.dragging || u.mosaicMode == editor.MosaicModeRectangle {
		return editor.RenderCommand{}, false
	}
	if _, ok := u.currentToolCursor(); !ok {
		return editor.RenderCommand{}, false
	}
	return editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Points:          []editor.Point{u.canvasPointer, u.canvasPointer},
		Style:           u.controller.MosaicStyle,
		MosaicMode:      u.mosaicMode,
		MosaicBlockSize: u.controller.MosaicBlockSize,
	}, true
}

func (u *windowUI) previewMosaicHover(command editor.RenderCommand) (*image.RGBA, error) {
	key := mosaicHoverCacheKey{
		controller:      u.controller,
		point:           command.Points[0],
		width:           command.Style.Width,
		blockSize:       command.MosaicBlockSize,
		previewRevision: u.controller.PreviewRevision(),
	}
	if u.mosaicHoverImage != nil && u.mosaicHoverKey == key {
		return u.mosaicHoverImage, nil
	}
	rendered, err := u.controller.PreviewImageWithPending(command)
	if err != nil {
		return nil, err
	}
	u.mosaicHoverKey = key
	u.mosaicHoverImage = rendered
	return rendered, nil
}

func (u *windowUI) clearMosaicHoverCache() {
	u.mosaicHoverKey = mosaicHoverCacheKey{}
	u.mosaicHoverImage = nil
}

func (u *windowUI) canvasImageOperation(img image.Image) paint.ImageOp {
	if rgba, ok := img.(*image.RGBA); ok {
		if u.canvasImage == rgba {
			return u.canvasImageOp
		}
		u.canvasImage = rgba
		u.canvasImageOp = paint.NewImageOp(rgba)
		return u.canvasImageOp
	}
	u.clearCanvasImageOp()
	return paint.NewImageOp(img)
}

func (u *windowUI) clearCanvasImageOp() {
	u.canvasImage = nil
	u.canvasImageOp = paint.ImageOp{}
	u.clearCanvasMip()
}

func (u *windowUI) releaseTransientImageCaches() {
	u.cancelRotation()
	if u.controller != nil {
		u.controller.ReleasePreviewCache()
	}
	u.clearMosaicHoverCache()
	u.resetTextPreview()
	u.clearCanvasImageOp()
	if u.filmstrip != nil {
		u.filmstrip.releaseHoverPreview()
	}
	if u.theme != nil && u.defaultShaper != nil {
		u.theme.Shaper = u.defaultShaper
		u.shaperFont = editor.TextFontMalgun
		u.shaperBold = false
	}
}

func (u *windowUI) scheduleOSMemoryRelease(delay time.Duration) {
	free := u.freeOSMemory
	if free == nil {
		return
	}
	go func() {
		if delay > 0 {
			time.Sleep(delay)
		}
		free()
	}()
}

func (u *windowUI) schedulePostCaptureMemoryCleanup() {
	u.releaseTransientImageCaches()
	u.scheduleOSMemoryRelease(250 * time.Millisecond)
}

func (u *windowUI) syncCanvasPointer(viewport coords.Viewport, bounds image.Rectangle) {
	if !u.canvasPointerOver {
		u.canvasPointerIn = false
		return
	}
	point, inside := canvasViewToSourcePoint(u.canvasPointerView, viewport)
	u.canvasPointer = clampEditorPoint(point, bounds)
	u.canvasPointerIn = inside && u.controller.Mode == workflow.ModeEditing
}

func (u *windowUI) drawToolCursorPreview(ops *op.Ops, scale float64) {
	spec, ok := u.currentToolCursor()
	if !ok {
		return
	}
	if scale <= 0 {
		scale = 1
	}
	screenUnit := float32(1 / scale)
	white := color.NRGBA{R: 255, G: 255, B: 255, A: 210}

	switch spec.Tool {
	case editor.ToolPen, editor.ToolLine, editor.ToolHighlight, editor.ToolMosaic:
		radius := spec.Width / 2
		fill := spec.Color
		if spec.Tool == editor.ToolHighlight {
			// Show the real opacity while it is being tuned.
			if fill.A > 110 && !u.brushAdjust.Active {
				fill.A = 110
			}
		} else {
			fill.A = 45
		}
		if spec.Tool != editor.ToolMosaic {
			paint.FillShape(ops, fill, clip.Ellipse(cursorEllipse(spec.Center, radius)).Op(ops))
		}
		outlineRadius := float32(math.Max(float64(radius), float64(4*screenUnit)))
		outline := clip.Ellipse(cursorEllipse(spec.Center, outlineRadius)).Path(ops)
		outlineColor := spec.Color
		outlineColor.A = 255
		paint.FillShape(ops, white, clip.Stroke{Path: outline, Width: 2 * screenUnit}.Op())
		paint.FillShape(ops, outlineColor, clip.Stroke{Path: outline, Width: screenUnit}.Op())
	case editor.ToolRectangle:
		half := float32(math.Max(float64(9*screenUnit), float64(spec.Width*2)))
		rect := editor.Rect{
			Min: editor.Point{X: int(math.Round(float64(spec.Center.X) - float64(half))), Y: int(math.Round(float64(spec.Center.Y) - float64(half)))},
			Max: editor.Point{X: int(math.Round(float64(spec.Center.X) + float64(half))), Y: int(math.Round(float64(spec.Center.Y) + float64(half)))},
		}
		drawStrokeRect(ops, rect, white, spec.Width+2*screenUnit)
		drawStrokeRect(ops, rect, spec.Color, spec.Width)
	case editor.ToolTriangle:
		half := float32(math.Max(float64(9*screenUnit), float64(spec.Width*2)))
		rect := editor.Rect{
			Min: editor.Point{X: int(math.Round(float64(spec.Center.X) - float64(half))), Y: int(math.Round(float64(spec.Center.Y) - float64(half)))},
			Max: editor.Point{X: int(math.Round(float64(spec.Center.X) + float64(half))), Y: int(math.Round(float64(spec.Center.Y) + float64(half)))},
		}
		drawStrokeTriangle(ops, rect, white, spec.Width+2*screenUnit)
		drawStrokeTriangle(ops, rect, spec.Color, spec.Width)
	case editor.ToolCircle:
		radius := float32(math.Max(float64(9*screenUnit), float64(spec.Width*2)))
		rect := cursorEllipse(spec.Center, radius)
		drawStrokeEllipse(ops, rect, white, spec.Width+2*screenUnit)
		drawStrokeEllipse(ops, rect, spec.Color, spec.Width)
	case editor.ToolArrow:
		extent := float32(math.Max(float64(12*screenUnit), float64(spec.Width*3)))
		start := editor.Point{X: int(math.Round(float64(spec.Center.X) - float64(extent))), Y: int(math.Round(float64(spec.Center.Y) + float64(extent)*0.65))}
		end := editor.Point{X: int(math.Round(float64(spec.Center.X) + float64(extent))), Y: int(math.Round(float64(spec.Center.Y) - float64(extent)*0.65))}
		drawArrowWithHeadLength(ops, start, end, white, spec.Width+2*screenUnit, 8/scale)
		drawArrowWithHeadLength(ops, start, end, spec.Color, spec.Width, 8/scale)
	case editor.ToolText:
		extent := float32(math.Max(float64(10*screenUnit), float64(spec.Width*2)))
		drawIconLine(ops, f32.Pt(float32(spec.Center.X)-extent, float32(spec.Center.Y)-extent), f32.Pt(float32(spec.Center.X)+extent, float32(spec.Center.Y)-extent), spec.Color, 2*screenUnit)
		drawIconLine(ops, f32.Pt(float32(spec.Center.X), float32(spec.Center.Y)-extent), f32.Pt(float32(spec.Center.X), float32(spec.Center.Y)+extent), spec.Color, 2*screenUnit)
	}
}

func cursorEllipse(center editor.Point, radius float32) image.Rectangle {
	radius = float32(math.Max(float64(radius), 0.5))
	minX := int(math.Floor(float64(center.X) - float64(radius)))
	minY := int(math.Floor(float64(center.Y) - float64(radius)))
	maxX := int(math.Ceil(float64(center.X) + float64(radius)))
	maxY := int(math.Ceil(float64(center.Y) + float64(radius)))
	if maxX <= minX {
		maxX = minX + 1
	}
	if maxY <= minY {
		maxY = minY + 1
	}
	return image.Rect(minX, minY, maxX, maxY)
}

func isCanvasPanButton(buttons pointer.Buttons) bool {
	return buttons.Contain(pointer.ButtonTertiary)
}

func (u *windowUI) beginCanvasPan(pointer editor.Point, viewport coords.Viewport) {
	u.canvasPanning = true
	u.canvasPanStart = u.canvasPan
	u.canvasPanPointer = pointer
	u.canvasPanCenter = viewport.View.Min.Sub(u.canvasPan)
	u.canvasPanSize = viewport.View.Size()
}

func (u *windowUI) panCanvasToViewPoint(pointer editor.Point, canvasBounds image.Rectangle) bool {
	if u.canvasPanSize.X <= 0 || u.canvasPanSize.Y <= 0 || canvasBounds.Empty() {
		return false
	}
	delta := image.Pt(pointer.X-u.canvasPanPointer.X, pointer.Y-u.canvasPanPointer.Y)
	next := clampCanvasPan(u.canvasPanStart.Add(delta), u.canvasPanCenter, u.canvasPanSize, canvasBounds)
	if next == u.canvasPan {
		return false
	}
	u.canvasPan = next
	u.canvasFitBounds = image.Point{}
	return true
}

func canvasViewToSourcePoint(viewPoint editor.Point, viewport coords.Viewport) (editor.Point, bool) {
	view := image.Pt(viewPoint.X, viewPoint.Y)
	source, inside := viewport.ViewToSource(view)
	if inside {
		return editor.Point{X: source.X, Y: source.Y}, true
	}
	view.X = max(viewport.View.Min.X, min(view.X, viewport.View.Max.X))
	view.Y = max(viewport.View.Min.Y, min(view.Y, viewport.View.Max.Y))
	source, _ = viewport.ViewToSource(view)
	return editor.Point{X: source.X, Y: source.Y}, false
}

func (u *windowUI) hitCropHandle(point editor.Point, bounds image.Rectangle, scale float64) cropHandle {
	if u.controller.Mode != workflow.ModeEditing || scale <= 0 || bounds.Empty() {
		return cropHandleNone
	}
	tolerance := cropHandleSourceTolerance(scale)
	x, y := point.X, point.Y
	withinVerticalSpan := y >= bounds.Min.Y-tolerance && y <= bounds.Max.Y+tolerance
	withinHorizontalSpan := x >= bounds.Min.X-tolerance && x <= bounds.Max.X+tolerance
	nearLeft := withinVerticalSpan && absInt(x-bounds.Min.X) <= tolerance
	nearRight := withinVerticalSpan && absInt(x-bounds.Max.X) <= tolerance
	nearTop := withinHorizontalSpan && absInt(y-bounds.Min.Y) <= tolerance
	nearBottom := withinHorizontalSpan && absInt(y-bounds.Max.Y) <= tolerance
	var handle cropHandle
	if nearLeft {
		handle |= cropHandleLeft
	} else if nearRight {
		handle |= cropHandleRight
	}
	if nearTop {
		handle |= cropHandleTop
	} else if nearBottom {
		handle |= cropHandleBottom
	}
	return handle
}

func cropHandleSourceTolerance(scale float64) int {
	if scale <= 0 {
		return 0
	}
	return max(0, int(math.Floor(cropHandleScreenTolerance/scale)))
}

func (u *windowUI) updateCropPreview(point editor.Point, bounds image.Rectangle) {
	r := u.cropStartRect
	minSize := minCropSize(bounds)
	if u.cropDragHandle&cropHandleLeft != 0 {
		r.Min.X = min(point.X, r.Max.X-minSize)
	}
	if u.cropDragHandle&cropHandleRight != 0 {
		r.Max.X = max(point.X, r.Min.X+minSize)
	}
	if u.cropDragHandle&cropHandleTop != 0 {
		r.Min.Y = min(point.Y, r.Max.Y-minSize)
	}
	if u.cropDragHandle&cropHandleBottom != 0 {
		r.Max.Y = max(point.Y, r.Min.Y+minSize)
	}
	u.cropPreviewRect = r.Canon().Intersect(bounds)
}

func minCropSize(bounds image.Rectangle) int {
	return max(1, min(8, min(bounds.Dx(), bounds.Dy())))
}

func cropMagnifierSourceRect(center image.Point, size int, bounds image.Rectangle) image.Rectangle {
	return cropMagnifierSourceRectSize(center, image.Pt(size, size), bounds)
}

func cropMagnifierSourceRectSize(center, size image.Point, bounds image.Rectangle) image.Rectangle {
	if bounds.Empty() || size.X <= 0 || size.Y <= 0 {
		return image.Rectangle{}
	}
	size.X = min(size.X, bounds.Dx())
	size.Y = min(size.Y, bounds.Dy())
	x := max(bounds.Min.X, min(center.X-size.X/2, bounds.Max.X-size.X))
	y := max(bounds.Min.Y, min(center.Y-size.Y/2, bounds.Max.Y-size.Y))
	return image.Rect(x, y, x+size.X, y+size.Y)
}

func cropMagnifierDestination(windowBounds image.Rectangle, workspaceTop int, pointer image.Point, size image.Point, gap int) (image.Rectangle, bool) {
	windowBounds = windowBounds.Canon()
	if windowBounds.Empty() || workspaceTop < 0 || size.X <= 0 || size.Y <= 0 {
		return image.Rectangle{}, false
	}
	edge := max(2, gap/4)
	height := min(size.Y, windowBounds.Max.Y-workspaceTop-edge*2)
	width := min(size.X, max(0, windowBounds.Dx()/2-edge*2))
	if width < 64 || height < 32 {
		return image.Rectangle{}, false
	}
	pointerAvoid := image.Rect(pointer.X-gap, pointer.Y-gap, pointer.X+gap+1, pointer.Y+gap+1)
	y := max(windowBounds.Min.Y+workspaceTop+edge, min(windowBounds.Min.Y+workspaceTop+edge, windowBounds.Max.Y-height-edge))
	candidates := []image.Rectangle{
		image.Rect(windowBounds.Max.X-edge-width, y, windowBounds.Max.X-edge, y+height),
		image.Rect(windowBounds.Min.X+edge, y, windowBounds.Min.X+edge+width, y+height),
	}
	for _, candidate := range candidates {
		if candidate.In(windowBounds) && !candidate.Overlaps(pointerAvoid) {
			return candidate, true
		}
	}
	return image.Rectangle{}, false
}

func cropMagnifierForDestination(point image.Point, sourceBounds, destination image.Rectangle) (cropMagnifier, bool) {
	if sourceBounds.Empty() || destination.Empty() {
		return cropMagnifier{}, false
	}
	sourceSize := image.Pt(max(1, destination.Dx()/4), max(1, destination.Dy()/4))
	source := cropMagnifierSourceRectSize(point, sourceSize, sourceBounds)
	if source.Empty() {
		return cropMagnifier{}, false
	}
	target := image.Pt(
		destination.Min.X+(point.X-source.Min.X)*destination.Dx()/max(1, source.Dx()),
		destination.Min.Y+(point.Y-source.Min.Y)*destination.Dy()/max(1, source.Dy()),
	)
	target.X = max(destination.Min.X+3, min(target.X, destination.Max.X-3))
	target.Y = max(destination.Min.Y+3, min(target.Y, destination.Max.Y-3))
	return cropMagnifier{Source: source, Destination: destination, Target: target}, true
}

func (u *windowUI) layoutCropMagnifierOverlay(gtx layout.Context, _ int, workspaceTop int) {
	frame := u.cropMagnifierFrame
	if !frame.Visible {
		return
	}
	pointer := image.Pt(frame.PointerView.X, workspaceTop+frame.PointerView.Y)
	destination, ok := cropMagnifierDestination(
		image.Rectangle{Max: gtx.Constraints.Max},
		workspaceTop,
		pointer,
		image.Pt(gtx.Dp(unit.Dp(160)), gtx.Dp(unit.Dp(160))),
		gtx.Dp(unit.Dp(16)),
	)
	if !ok {
		return
	}
	magnifier, ok := cropMagnifierForDestination(frame.SourcePoint, frame.SourceBounds, destination)
	if ok {
		drawCropMagnifier(gtx.Ops, frame.Image, magnifier)
	}
}

func drawCropMagnifier(ops *op.Ops, imageOp paint.ImageOp, magnifier cropMagnifier) {
	if magnifier.Source.Empty() || magnifier.Destination.Empty() {
		return
	}
	destination := magnifier.Destination
	shadow := destination.Add(image.Pt(0, 5)).Inset(-5)
	paint.FillShape(ops, color.NRGBA{A: 110}, clip.RRect{Rect: shadow, SE: 16, SW: 16, NW: 16, NE: 16}.Op(ops))

	destinationClip := clip.RRect{Rect: destination, SE: 11, SW: 11, NW: 11, NE: 11}.Push(ops)
	destinationOffset := op.Offset(destination.Min).Push(ops)
	scaleX := float32(destination.Dx()) / float32(magnifier.Source.Dx())
	scaleY := float32(destination.Dy()) / float32(magnifier.Source.Dy())
	imageScale := op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scaleX, scaleY))).Push(ops)
	sourceOffset := op.Offset(image.Pt(-magnifier.Source.Min.X, -magnifier.Source.Min.Y)).Push(ops)
	imageOp.Filter = paint.FilterNearest
	imageOp.Add(ops)
	paint.PaintOp{}.Add(ops)
	sourceOffset.Pop()
	imageScale.Pop()
	destinationOffset.Pop()
	destinationClip.Pop()

	drawIconRect(ops, destination, color.NRGBA{R: 15, G: 23, B: 42, A: 245}, 5)
	drawIconRect(ops, destination.Inset(2), color.NRGBA{R: 125, G: 211, B: 252, A: 255}, 2)
	target := magnifier.Target
	crosshair := color.NRGBA{R: 255, G: 255, B: 255, A: 235}
	crosshairClip := clip.Rect(destination.Inset(3)).Push(ops)
	paint.FillShape(ops, crosshair, clip.Rect(image.Rect(target.X-11, target.Y-1, target.X+12, target.Y+1)).Op())
	paint.FillShape(ops, crosshair, clip.Rect(image.Rect(target.X-1, target.Y-11, target.X+1, target.Y+12)).Op())
	paint.FillShape(ops, color.NRGBA{R: 56, G: 189, B: 248, A: 255}, clip.Rect(image.Rect(target.X-2, target.Y-2, target.X+3, target.Y+3)).Op())
	crosshairClip.Pop()
}

func (u *windowUI) commitCropPreview() {
	if u.cropPreviewRect.Empty() || u.cropPreviewRect.Eq(u.cropStartRect) {
		u.status = "자르기를 취소했습니다."
		return
	}
	viewBefore := u.captureCanvasView()
	if err := u.controller.CropCapturedImage(u.cropPreviewRect); err != nil {
		u.status = "자르기 실패: " + err.Error()
		u.showToast(u.status)
		return
	}
	croppedSize := u.cropPreviewRect.Size()
	u.resetCanvasView()
	u.recordControllerEdit(viewBefore, u.captureCanvasView())
	status := fmt.Sprintf("%dx%d 크기로 잘랐습니다.", croppedSize.X, croppedSize.Y)
	if u.autoCopyPending {
		u.autoCopyPending = false
		if err := u.copyPNGCurrentImage(); err != nil {
			u.status = status + " 자동 복사 실패: " + err.Error()
			u.showToast("자동 복사 실패")
			return
		}
		u.status = status + " 클립보드에 복사했습니다."
		u.showToast("첫 변경 후 복사 완료")
		return
	}
	u.status = status
	u.showToast("이미지를 잘랐습니다")
}

func (u *windowUI) resetCanvasView() {
	u.cancelRotation()
	u.resetTextEditing()
	u.canvasZoom = 1
	u.canvasPan = image.Point{}
	u.canvasSourceSize = image.Point{}
	u.canvasFitBounds = image.Point{}
	u.brushAdjust = brushAdjustState{}
	u.canvasPointerOver = false
	u.canvasPointerIn = false
	u.clearMosaicHoverCache()
	u.canvasPanning = false
	u.cropDragging = false
	u.cropHoverHandle = cropHandleNone
	u.cropDragHandle = cropHandleNone
	u.cropStartRect = image.Rectangle{}
	u.cropPreviewRect = image.Rectangle{}
}

func (u *windowUI) drawCropFrame(ops *op.Ops, sourceRect image.Rectangle, scale float64) {
	if u.controller.Mode != workflow.ModeEditing || sourceRect.Empty() {
		return
	}
	if scale <= 0 {
		scale = 1
	}
	rect := u.controller.VisibleCropRect()
	if rect.Empty() {
		rect = sourceRect
	}
	if u.cropDragging && !u.cropPreviewRect.Empty() {
		rect = u.cropPreviewRect
	}
	drawCropDim(ops, sourceRect, rect)
	u.drawOutputBorderPreview(ops, sourceRect, rect)
	width := float32(max(1, int(math.Round(2/scale))))
	drawStrokeRect(ops, rectToEditorRect(rect), color.NRGBA{R: 56, G: 189, B: 248, A: 230}, width)

	handleSize := max(4, int(math.Round(8/scale)))
	for _, center := range cropHandleCenters(rect) {
		drawHandleSquare(ops, center, handleSize)
	}
}

func (u *windowUI) drawOutputBorderPreview(ops *op.Ops, sourceRect, cropRect image.Rectangle) {
	border := u.controller.RenderOptions.OutputBorder
	if !border.Enabled || border.Width <= 0 || border.Color.A == 0 {
		return
	}
	for _, rect := range outputBorderPreviewRects(sourceRect, cropRect, border.Width) {
		paint.FillShape(ops, border.Color, clip.Rect(rect).Op())
	}
}

func outputBorderPreviewRects(sourceRect, cropRect image.Rectangle, width int) []image.Rectangle {
	cropRect = cropRect.Canon().Intersect(sourceRect)
	if cropRect.Empty() || width <= 0 {
		return nil
	}

	topMin := cropRect.Min.Y - width
	if topMin < sourceRect.Min.Y {
		topMin = cropRect.Min.Y
	}
	topMax := topMin + min(width, cropRect.Dy())

	bottomMax := cropRect.Max.Y + width
	if bottomMax > sourceRect.Max.Y {
		bottomMax = cropRect.Max.Y
	}
	bottomMin := bottomMax - min(width, cropRect.Dy())

	leftMin := cropRect.Min.X - width
	if leftMin < sourceRect.Min.X {
		leftMin = cropRect.Min.X
	}
	leftMax := leftMin + min(width, cropRect.Dx())

	rightMax := cropRect.Max.X + width
	if rightMax > sourceRect.Max.X {
		rightMax = cropRect.Max.X
	}
	rightMin := rightMax - min(width, cropRect.Dx())

	return []image.Rectangle{
		image.Rect(cropRect.Min.X, topMin, cropRect.Max.X, topMax),
		image.Rect(cropRect.Min.X, bottomMin, cropRect.Max.X, bottomMax),
		image.Rect(leftMin, cropRect.Min.Y, leftMax, cropRect.Max.Y),
		image.Rect(rightMin, cropRect.Min.Y, rightMax, cropRect.Max.Y),
	}
}

func drawCropDim(ops *op.Ops, sourceRect, cropRect image.Rectangle) {
	cropRect = cropRect.Canon().Intersect(sourceRect)
	if cropRect.Empty() || cropRect.Eq(sourceRect) {
		return
	}
	c := color.NRGBA{R: 2, G: 6, B: 23, A: 120}
	fillRect := func(r image.Rectangle) {
		if !r.Empty() {
			paint.FillShape(ops, c, clip.Rect(r).Op())
		}
	}
	fillRect(image.Rect(sourceRect.Min.X, sourceRect.Min.Y, sourceRect.Max.X, cropRect.Min.Y))
	fillRect(image.Rect(sourceRect.Min.X, cropRect.Max.Y, sourceRect.Max.X, sourceRect.Max.Y))
	fillRect(image.Rect(sourceRect.Min.X, cropRect.Min.Y, cropRect.Min.X, cropRect.Max.Y))
	fillRect(image.Rect(cropRect.Max.X, cropRect.Min.Y, sourceRect.Max.X, cropRect.Max.Y))
}

func rectToEditorRect(r image.Rectangle) editor.Rect {
	return editor.Rect{Min: editor.Point{X: r.Min.X, Y: r.Min.Y}, Max: editor.Point{X: r.Max.X, Y: r.Max.Y}}
}

func cropHandleCenters(r image.Rectangle) []editor.Point {
	midX := (r.Min.X + r.Max.X) / 2
	midY := (r.Min.Y + r.Max.Y) / 2
	return []editor.Point{
		{X: r.Min.X, Y: r.Min.Y}, {X: midX, Y: r.Min.Y}, {X: r.Max.X, Y: r.Min.Y},
		{X: r.Max.X, Y: midY}, {X: r.Max.X, Y: r.Max.Y}, {X: midX, Y: r.Max.Y},
		{X: r.Min.X, Y: r.Max.Y}, {X: r.Min.X, Y: midY},
	}
}

func drawHandleSquare(ops *op.Ops, center editor.Point, size int) {
	half := max(1, size/2)
	r := image.Rect(center.X-half, center.Y-half, center.X+half, center.Y+half)
	paint.FillShape(ops, color.NRGBA{R: 15, G: 23, B: 42, A: 230}, clip.Rect(r).Op())
	drawStrokeRect(ops, rectToEditorRect(r), color.NRGBA{R: 125, G: 211, B: 252, A: 255}, 1)
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func (u *windowUI) cycleHotkey() {
	binding := hotkey.NextPreset(u.appConfig.Hotkey)
	u.applyHotkeyBinding(binding)
}

func (u *windowUI) hotkeyButtonLabel() string {
	if u.hotkeyCaptureMode {
		if u.hotkeyCaptureHasValue {
			return "단축키 입력 중: " + u.hotkeyCaptureCandidate.Name
		}
		if combo := strings.Join(u.hotkeyCapturePressed, "+"); combo != "" {
			return "단축키 입력 중: " + combo
		}
		return "단축키 입력 중..."
	}
	return "단축키: " + u.appConfig.Hotkey
}

func (u *windowUI) hotkeyActionLabel() string {
	return "단축키 동작: " + u.hotkeyActionValueLabel()
}

func (u *windowUI) hotkeyActionValueLabel() string {
	if u.appConfig.HotkeyOpensWindow {
		return "프로그램 열기"
	}
	return "즉시 캡처"
}

func (u *windowUI) beginHotkeyCapture() {
	u.hotkeyCaptureSuppressed.Store(true)
	for u.hotkeyEvents != nil {
		select {
		case <-u.hotkeyEvents:
		default:
			goto drained
		}
	}
drained:
	u.hotkeyCaptureMode = true
	u.resetHotkeyCaptureInput()
	u.status = "새 단축키를 눌러주세요. Esc로 취소할 수 있습니다."
	u.showToast("새 단축키를 눌러주세요")
}

func (u *windowUI) cancelHotkeyCapture(message string) {
	if !u.hotkeyCaptureMode {
		return
	}
	u.hotkeyCaptureMode = false
	u.resetHotkeyCaptureInput()
	u.hotkeyCaptureSuppressed.Store(false)
	u.status = message
}

func (u *windowUI) applyHotkeyBinding(binding hotkey.Binding) {
	current, currentOK := hotkey.ParseBinding(u.appConfig.Hotkey)
	if currentOK && binding.Name == current.Name {
		u.hotkeyCaptureMode = false
		u.resetHotkeyCaptureInput()
		u.hotkeyCaptureSuppressed.Store(false)
		u.status = "단축키 유지: " + binding.Name
		return
	}
	rebind := u.rebindHotkey
	if rebind == nil && u.registrar != nil {
		rebind = u.registrar.RebindCapture
	}
	if rebind != nil {
		if err := rebind(binding, u.captureHotkey); err != nil {
			u.status = "단축키 변경 실패: " + err.Error()
			return
		}
	}
	nextConfig := u.appConfig
	nextConfig.Hotkey = binding.Name
	save := u.saveConfig
	if save == nil {
		save = config.Save
	}
	if err := save(nextConfig); err != nil {
		rollbackMessage := ""
		if rebind != nil && currentOK {
			if rollbackErr := rebind(current, u.captureHotkey); rollbackErr != nil {
				rollbackMessage = " · 기존 단축키 복구 실패: " + rollbackErr.Error()
			}
		}
		u.status = "단축키 설정 저장 실패: " + err.Error() + rollbackMessage
		u.showToastDetail("단축키 설정 저장 실패", err.Error()+rollbackMessage)
		return
	}
	u.hotkeyCaptureMode = false
	u.resetHotkeyCaptureInput()
	u.hotkeyCaptureSuppressed.Store(false)
	u.appConfig = nextConfig
	u.status = "단축키 변경: " + binding.Name
	u.showToast(u.status)
}

func (u *windowUI) saveConfigStatus(success string) {
	save := u.saveConfig
	if save == nil {
		save = config.Save
	}
	if err := save(u.appConfig); err != nil {
		u.status = success + " (설정 저장 실패: " + err.Error() + ")"
		u.showToastDetail("설정 저장 실패", err.Error())
		return
	}
	u.status = success
	u.showToast(success)
}

func (u *windowUI) saveConfigCandidate(next config.Config, success string) bool {
	save := u.saveConfig
	if save == nil {
		save = config.Save
	}
	if err := save(next); err != nil {
		u.status = success + " 실패: " + err.Error()
		u.showToastDetail("설정 저장 실패", err.Error())
		return false
	}
	u.status = success
	u.showToast(success)
	return true
}

func (u *windowUI) persistConfig() {
	save := u.saveConfig
	if save == nil {
		save = config.Save
	}
	if err := save(u.appConfig); err != nil {
		u.status = "설정 저장 실패: " + err.Error()
	}
}

func hotkeyBindingFromKeyEvent(ke key.Event) (hotkey.Binding, bool) {
	keyName := hotkeyCaptureKeyName(ke.Name)
	if keyName == "" {
		return hotkey.Binding{}, false
	}
	return hotkey.BindingFromNames(hotkeyCaptureModifierNames(ke.Modifiers), keyName)
}

func appendUniqueKey(keys []string, name string) []string {
	if name == "" {
		return keys
	}
	for _, keyName := range keys {
		if keyName == name {
			return keys
		}
	}
	return append(keys, name)
}

func removeCapturedKey(keys []string, name string) []string {
	for i, keyName := range keys {
		if keyName == name {
			return append(keys[:i], keys[i+1:]...)
		}
	}
	return keys
}

func (u *windowUI) resetHotkeyCaptureInput() {
	u.hotkeyCapturePressed = nil
	u.hotkeyCaptureEventKeys = nil
	u.hotkeyCaptureNativeKeys = nil
	u.hotkeyCaptureCandidate = hotkey.Binding{}
	u.hotkeyCaptureHasValue = false
	u.hotkeyCaptureInvalid = false
}

func (u *windowUI) refreshHotkeyCapturePressed() {
	pressed := make([]string, 0, len(u.hotkeyCaptureEventKeys)+len(u.hotkeyCaptureNativeKeys))
	for _, name := range u.hotkeyCaptureEventKeys {
		pressed = appendUniqueKey(pressed, name)
	}
	for _, name := range u.hotkeyCaptureNativeKeys {
		pressed = appendUniqueKey(pressed, name)
	}
	u.hotkeyCapturePressed = pressed
}

func (u *windowUI) updateHotkeyCapturePreview() {
	if invalidCapturedKeys(u.hotkeyCapturePressed) {
		u.hotkeyCaptureInvalid = true
		u.hotkeyCaptureCandidate = hotkey.Binding{}
		u.hotkeyCaptureHasValue = false
		u.status = fmt.Sprintf("단축키는 보조키 포함 최대 %d개와 일반 키 1개만 사용할 수 있습니다.", maxHotkeyCaptureKeys)
		return
	}
	if u.hotkeyCaptureInvalid {
		u.hotkeyCaptureCandidate = hotkey.Binding{}
		u.hotkeyCaptureHasValue = false
		u.status = "잘못된 키 조합입니다. 모든 키를 놓고 다시 입력해주세요."
		return
	}
	if binding, ok := bindingFromCapturedKeys(u.hotkeyCapturePressed); ok {
		u.hotkeyCaptureCandidate = binding
		u.hotkeyCaptureHasValue = true
		u.status = "단축키 입력 중: " + binding.Name
		return
	}
	if combo := strings.Join(u.hotkeyCapturePressed, "+"); combo != "" {
		u.status = "단축키 입력 중: " + combo
		return
	}
	u.status = "새 단축키를 눌러주세요. Esc로 취소할 수 있습니다."
}

func invalidCapturedKeys(keys []string) bool {
	if len(keys) > maxHotkeyCaptureKeys {
		return true
	}
	mainKeys := 0
	for _, name := range keys {
		if !isHotkeyModifierName(name) {
			mainKeys++
		}
	}
	return mainKeys > 1
}

func (u *windowUI) finishHotkeyCaptureInput() {
	if len(u.hotkeyCapturePressed) != 0 {
		u.updateHotkeyCapturePreview()
		return
	}
	if u.hotkeyCaptureInvalid {
		u.resetHotkeyCaptureInput()
		u.status = "잘못된 조합은 저장하지 않았습니다. 새 단축키를 다시 눌러주세요."
		return
	}
	if u.hotkeyCaptureHasValue {
		u.applyHotkeyBinding(u.hotkeyCaptureCandidate)
		return
	}
	u.status = "Ctrl/Alt/Shift와 함께 사용할 키를 눌러주세요. Esc로 취소할 수 있습니다."
}

func (u *windowUI) syncNativeHotkeyCaptureKeys(keys []string) {
	native := make([]string, 0, len(keys))
	for _, name := range keys {
		if !isNativeHotkeyCaptureKey(name) {
			continue
		}
		native = appendUniqueKey(native, name)
	}
	if slices.Equal(native, u.hotkeyCaptureNativeKeys) {
		return
	}
	for _, name := range nativeHotkeyCaptureKeys() {
		if !slices.Contains(native, name) {
			u.hotkeyCaptureEventKeys = removeCapturedKey(u.hotkeyCaptureEventKeys, name)
		}
	}
	u.hotkeyCaptureNativeKeys = native
	u.refreshHotkeyCapturePressed()
	u.finishHotkeyCaptureInput()
}

func nativeHotkeyCaptureKeys() []string {
	return []string{"Ctrl", "Alt", "Shift", "PrintScreen", "Insert", "Pause", "Backspace"}
}

func isNativeHotkeyCaptureKey(name string) bool {
	for _, keyName := range nativeHotkeyCaptureKeys() {
		if name == keyName {
			return true
		}
	}
	return false
}

func bindingFromCapturedKeys(keys []string) (hotkey.Binding, bool) {
	modifiers := make([]string, 0, len(keys))
	var keyName string
	for _, name := range keys {
		if isHotkeyModifierName(name) {
			modifiers = append(modifiers, name)
			continue
		}
		if keyName != "" {
			return hotkey.Binding{}, false
		}
		keyName = name
	}
	if keyName == "" {
		return hotkey.Binding{}, false
	}
	return hotkey.BindingFromNames(modifiers, keyName)
}

func isHotkeyModifierName(name string) bool {
	switch name {
	case "Ctrl", "Alt", "Shift":
		return true
	default:
		return false
	}
}

func hotkeyCaptureEventKeyName(name key.Name) string {
	switch name {
	case key.NameCtrl:
		return "Ctrl"
	case key.NameShift:
		return "Shift"
	case key.NameAlt:
		return "Alt"
	case key.NameSuper, key.NameCommand:
		return ""
	default:
		return hotkeyCaptureKeyName(name)
	}
}

func hotkeyCaptureModifierNames(modifiers key.Modifiers) []string {
	names := make([]string, 0, 4)
	if modifiers.Contain(key.ModCtrl) {
		names = append(names, "Ctrl")
	}
	if modifiers.Contain(key.ModAlt) {
		names = append(names, "Alt")
	}
	if modifiers.Contain(key.ModShift) {
		names = append(names, "Shift")
	}
	return names
}

func hotkeyCaptureKeyName(name key.Name) string {
	switch name {
	case key.NameCtrl, key.NameShift, key.NameAlt, key.NameSuper, key.NameCommand:
		return ""
	case key.NameSpace:
		return "Space"
	case key.NameReturn, key.NameEnter:
		return "Enter"
	case key.NameEscape:
		return "Escape"
	case key.NameHome:
		return "Home"
	case key.NameEnd:
		return "End"
	case key.NamePageUp:
		return "PageUp"
	case key.NamePageDown:
		return "PageDown"
	case key.NameDeleteBackward:
		return "Backspace"
	case key.NameDeleteForward:
		return "Delete"
	case key.NameTab:
		return "Tab"
	case key.NameLeftArrow:
		return "Left"
	case key.NameRightArrow:
		return "Right"
	case key.NameUpArrow:
		return "Up"
	case key.NameDownArrow:
		return "Down"
	default:
		return strings.TrimSpace(string(name))
	}
}

func allShortcutModifiers() key.Modifiers {
	return key.ModCtrl | key.ModAlt | key.ModShift | key.ModSuper | key.ModCommand
}

func (u *windowUI) handleCanvasPointer(gtx layout.Context, w *app.Window, bounds, canvasBounds image.Rectangle, viewport coords.Viewport) {
	for {
		ev, ok := gtx.Event(pointer.Filter{
			Target:  &u.canvasTag,
			Kinds:   pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Move | pointer.Enter | pointer.Leave | pointer.Scroll,
			ScrollX: pointer.ScrollRange{Min: -1000000, Max: 1000000},
			ScrollY: pointer.ScrollRange{Min: -1000000, Max: 1000000},
		})
		if !ok {
			break
		}

		pe, ok := ev.(pointer.Event)
		if !ok {
			continue
		}
		viewPoint := editor.Point{X: int(math.Round(float64(pe.Position.X))), Y: int(math.Round(float64(pe.Position.Y)))}
		if u.handleRotationPointer(gtx, pe, viewport) {
			if w != nil {
				w.Invalidate()
			}
			continue
		}
		point, insideImage := canvasViewToSourcePoint(viewPoint, viewport)
		point = clampEditorPoint(point, bounds)
		cropRect := bounds
		if u.controller.Mode == workflow.ModeEditing {
			cropRect = u.controller.VisibleCropRect()
			if cropRect.Empty() {
				cropRect = bounds
			}
		}
		if u.textEditing {
			if pe.Kind == pointer.Press && textTransformHandlesContain(
				pe.Position,
				textEditorViewRect(u.textEditingRect, viewport),
				gtx.Dp(unit.Dp(16)),
				gtx.Dp(unit.Dp(14)),
				gtx.Dp(unit.Dp(34)),
			) {
				continue
			}
			if pe.Kind == pointer.Press && !image.Pt(point.X, point.Y).In(u.textEditingRect.Normalize().ImageRect()) {
				if u.commitTextEditing() && u.activeTool == editor.ToolText {
					if annotation, ok := u.textAnnotationAt(point); ok {
						u.beginTextEditing(annotation)
					}
				}
				w.Invalidate()
			}
			continue
		}
		if u.handleBrushAdjustPointer(pe, float32(gtx.Dp(unit.Dp(brushAdjustRangeDp))), point, insideImage) {
			w.Invalidate()
			continue
		}
		switch pe.Kind {
		case pointer.Enter, pointer.Move, pointer.Press, pointer.Drag, pointer.Release:
			u.canvasPointerView = viewPoint
			u.canvasPointerOver = true
			u.canvasPointer = point
			u.canvasPointerIn = insideImage && u.controller.Mode == workflow.ModeEditing
		case pointer.Leave, pointer.Cancel:
			u.canvasPointerOver = false
			u.canvasPointerIn = false
			u.clearMosaicHoverCache()
		}

		switch pe.Kind {
		case pointer.Scroll:
			if pe.Modifiers.Contain(key.ModCtrl) && insideImage {
				u.zoomCanvas(pe.Scroll.Y, point, canvasBounds, viewport)
				w.Invalidate()
			}
		case pointer.Enter, pointer.Move:
			if insideImage {
				u.cropHoverHandle = u.hitCropHandle(point, cropRect, viewport.Scale)
			} else {
				u.cropHoverHandle = cropHandleNone
			}
			w.Invalidate()
		case pointer.Leave:
			u.cropHoverHandle = cropHandleNone
			w.Invalidate()
		case pointer.Press:
			if u.controller.Mode == workflow.ModeEditing && isCanvasPanButton(pe.Buttons) {
				u.beginCanvasPan(viewPoint, viewport)
				u.dragging = false
				u.cropDragging = false
				u.status = "캔버스를 이동합니다."
				w.Invalidate()
				continue
			}
			if u.controller.Mode == workflow.ModeEditing && insideImage {
				handle := u.hitCropHandle(point, cropRect, viewport.Scale)
				if handle != cropHandleNone {
					u.cropDragging = true
					u.cropDragHandle = handle
					u.cropStartRect = cropRect
					u.cropPreviewRect = cropRect
					u.dragging = false
					u.status = "이미지 테두리를 드래그해 자르세요."
					w.Invalidate()
					continue
				}
			}
			if u.controller.Mode == workflow.ModeEditing && insideImage && u.activeTool == editor.ToolText {
				if annotation, ok := u.textAnnotationAt(point); ok {
					u.beginTextEditing(annotation)
					w.Invalidate()
					continue
				}
			}
			if !insideImage {
				continue
			}
			u.dragging = true
			u.dragStart = point
			u.dragNow = point
			u.penPoints = []editor.Point{point}
		case pointer.Drag:
			if u.canvasPanning {
				u.panCanvasToViewPoint(viewPoint, canvasBounds)
				w.Invalidate()
				continue
			}
			if u.cropDragging {
				u.updateCropPreview(point, bounds)
				w.Invalidate()
				continue
			}
			if !u.dragging {
				continue
			}
			u.dragNow = point
			if u.controller.Mode == workflow.ModeEditing && u.isFreehandTool() {
				u.penPoints = append(u.penPoints, point)
			}
			w.Invalidate()
		case pointer.Release:
			if u.canvasPanning {
				u.canvasPanning = false
				w.Invalidate()
				continue
			}
			if u.cropDragging {
				u.updateCropPreview(point, bounds)
				u.commitCropPreview()
				u.cropDragging = false
				w.Invalidate()
				continue
			}
			if !u.dragging {
				continue
			}
			u.dragNow = point
			u.commitGesture()
			u.dragging = false
			w.Invalidate()
		case pointer.Cancel:
			u.dragging = false
			u.cropDragging = false
			u.canvasPanning = false
		}
	}
}

func (u *windowUI) beginCapture(w *app.Window) {
	u.beginCaptureRequest(w, u.appConfig.CaptureMode, false, false)
}

func (u *windowUI) beginCaptureRequest(w *app.Window, mode string, rememberRegion, useSavedRegion bool) {
	if !u.commitTextEditing() {
		return
	}
	if u.overlayActive || u.captureActive {
		u.status = "이미 캡처가 진행 중입니다."
		return
	}

	u.releaseTransientImageCaches()
	u.imageLoadGeneration++
	u.imageLoadActive = false
	u.dragging = false
	u.resetCanvasView()
	u.clearEditHistory()
	u.autoCopyPending = false
	u.captureActive = true
	u.suppressFocusLossUntil = time.Now().Add(10 * time.Second)
	u.status = "프로그램을 숨기고 화면을 캡처하는 중..."
	if rememberRegion || useSavedRegion {
		mode = "Region"
	}
	go func() {
		done := make(chan captureStartResult, 1)
		go func() {
			snapshot, err := u.captureSnapshotAfterHiding()
			var windowTargets []platformwindows.WindowTarget
			if err == nil && (mode == "Window" || mode == "Scroll") {
				windowTargets = platformwindows.VisibleWindowTargets(windowTitle)
			}
			done <- captureStartResult{mode: mode, rememberRegion: rememberRegion, useSavedRegion: useSavedRegion, snapshot: snapshot, windowTargets: windowTargets, err: err}
		}()
		select {
		case result := <-done:
			u.captureResults <- result
		case <-time.After(8 * time.Second):
			u.captureResults <- captureStartResult{mode: mode, rememberRegion: rememberRegion, useSavedRegion: useSavedRegion, err: fmt.Errorf("화면 캡처 시간이 초과되었습니다")}
		case <-u.ctx.Done():
			return
		}
		if w != nil {
			w.Invalidate()
		}
	}()
	if w != nil {
		w.Invalidate()
	}
}

func (u *windowUI) captureSnapshotAfterHiding() (workflow.CaptureSnapshot, error) {
	hide := u.hideWindow
	if hide == nil {
		hide = platformwindows.HideWindowByTitle
	}
	if err := hide(windowTitle); err != nil {
		return workflow.CaptureSnapshot{}, fmt.Errorf("프로그램 숨기기: %w", err)
	}
	visible := u.windowVisible
	if visible == nil {
		visible = platformwindows.WindowVisibleByTitle
	}
	deadline := time.Now().Add(750 * time.Millisecond)
	for visible(windowTitle) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if visible(windowTitle) {
		return workflow.CaptureSnapshot{}, fmt.Errorf("프로그램 창이 숨겨지지 않았습니다")
	}
	flush := u.flushDesktop
	if flush == nil {
		flush = platformwindows.FlushDesktopComposition
	}
	flush()
	time.Sleep(100 * time.Millisecond)
	return u.captureSnapshotExternal()
}

func (u *windowUI) captureSnapshotExternal() (workflow.CaptureSnapshot, error) {
	if u.captureSnapshot != nil {
		return u.captureSnapshot()
	}
	return workflow.CaptureScreen(u.controller.Capturer)
}

func (u *windowUI) handleCaptureStartResult(result captureStartResult, w *app.Window) {
	u.captureActive = false
	if result.err != nil {
		u.status = "캡처 실패: " + result.err.Error()
		u.restoreFromTray(w)
		u.schedulePostCaptureMemoryCleanup()
		return
	}
	u.trayHidden = true
	if err := u.controller.BeginCaptureFromSnapshot(result.snapshot); err != nil {
		u.status = "캡처 실패: " + err.Error()
		u.restoreFromTray(w)
		u.schedulePostCaptureMemoryCleanup()
		return
	}
	if result.useSavedRegion {
		preset, ok := u.activeSavedRegion()
		if !ok {
			u.status = "활성화된 저장 좌표가 없습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			u.schedulePostCaptureMemoryCleanup()
			return
		}
		screenRect := preset.Region.Rect()
		if _, ok := savedRegionImageRect(screenRect, u.controller.VirtualScreen, result.snapshot.Displays); !ok {
			u.status = "저장된 캡처 좌표가 현재 화면 범위를 벗어났습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			u.showToast(u.status)
			u.schedulePostCaptureMemoryCleanup()
			return
		}
		if err := u.controller.CommitScreenSelection(screenRect); err != nil {
			u.status = "저장 좌표 캡처 실패: " + err.Error()
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			u.schedulePostCaptureMemoryCleanup()
			return
		}
		u.completeCapturedImage(w, fmt.Sprintf("%s %dx%d 캡처", preset.Name, u.controller.OutputSize().X, u.controller.OutputSize().Y))
		u.schedulePostCaptureMemoryCleanup()
		return
	}

	mode := result.mode
	switch mode {
	case "Fullscreen":
		monitor := currentMonitorRect(u.controller.VirtualScreen, result.snapshot.Displays)
		if err := u.controller.CommitScreenSelection(monitor); err != nil {
			u.status = "전체화면 캡처 실패: " + err.Error()
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			u.schedulePostCaptureMemoryCleanup()
			return
		}
		u.resetCanvasView()
		u.clearEditHistory()
		size := u.controller.OutputSize()
		u.completeCapturedImage(w, fmt.Sprintf("%dx%d 모니터 캡처", size.X, size.Y))
		u.schedulePostCaptureMemoryCleanup()
		return
	case "Freeform":
		u.status = "자유형 모드: 대상 영역 둘레를 드래그하세요."
	case "Window":
		u.status = "창 모드: 창 위에 마우스를 올리고 클릭하세요."
		if len(result.windowTargets) == 0 {
			u.status = "창 모드: 선택할 수 있는 창이 없습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			u.schedulePostCaptureMemoryCleanup()
			return
		}
	case "Scroll":
		u.status = "스크롤 모드: 자동으로 이어서 캡처할 영역을 창 안에서 드래그하세요."
		if len(result.windowTargets) == 0 {
			u.status = "스크롤 모드: 선택할 수 있는 창이 없습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			u.schedulePostCaptureMemoryCleanup()
			return
		}
	default:
		u.status = fmt.Sprintf("오버레이에서 영역을 드래그하세요. 가상 화면: %v", u.controller.VirtualScreen)
	}

	u.overlayActive = true

	img := u.controller.FullScreenImage
	virtualScreen := u.controller.VirtualScreen
	dimAlpha := uint8(80)
	var windowRects []image.Rectangle
	var windowRegions []overlay.WindowRegion
	if mode == "Window" || mode == "Scroll" {
		dimAlpha = 130
		windowTargets := result.windowTargets
		u.windowTargets = windowTargets
		windowRects = make([]image.Rectangle, 0, len(windowTargets))
		windowRegions = make([]overlay.WindowRegion, 0, len(windowTargets))
		for _, target := range windowTargets {
			windowRects = append(windowRects, target.Rect)
			visibleRects := target.VisibleRects
			if len(visibleRects) == 0 {
				visibleRects = []image.Rectangle{target.Rect}
			}
			windowRegions = append(windowRegions, overlay.WindowRegion{Handle: target.Handle, Rect: target.Rect, VisibleRects: visibleRects})
		}
	} else {
		u.windowTargets = nil
	}
	go func() {
		selectionColor := color.NRGBA{R: 255, G: 64, B: 64, A: 255}
		hint := ""
		if result.rememberRegion {
			selectionColor = color.NRGBA{R: 48, G: 209, B: 88, A: 255}
			hint = "좌표로 기억할 영역을 드래그하세요. Esc 또는 우클릭으로 취소합니다."
		}
		selection, ok, err := nativeoverlay.SelectDetailed(u.ctx, img, overlay.Options{
			VirtualScreen:  virtualScreen,
			Displays:       append([]image.Rectangle(nil), result.snapshot.Displays...),
			TopMostHint:    true,
			DimAlpha:       dimAlpha,
			CaptureMode:    mode,
			WindowRects:    windowRects,
			WindowRegions:  windowRegions,
			LiveBackground: mode == "Scroll",
			SelectionColor: selectionColor,
			Hint:           hint,
		})
		u.overlayResults <- overlayResult{mode: mode, rememberRegion: result.rememberRegion, selection: selection, ok: ok, err: err}
		if w != nil {
			w.Invalidate()
		}
	}()

	if w != nil {
		w.Invalidate()
	}
}

func savedRegionImageRect(screenRect, virtualScreen image.Rectangle, displays []image.Rectangle) (image.Rectangle, bool) {
	screenRect = screenRect.Canon()
	if screenRect.Empty() || !screenRect.Intersect(virtualScreen).Eq(screenRect) || !rectangleCoveredByDisplays(screenRect, displays) {
		return image.Rectangle{}, false
	}
	return screenRect.Sub(virtualScreen.Min), true
}

func rectangleCoveredByDisplays(rect image.Rectangle, displays []image.Rectangle) bool {
	remaining := []image.Rectangle{rect}
	for _, display := range displays {
		next := make([]image.Rectangle, 0, len(remaining)*4)
		for _, candidate := range remaining {
			intersection := candidate.Intersect(display)
			if intersection.Empty() {
				next = append(next, candidate)
				continue
			}
			for _, fragment := range []image.Rectangle{
				image.Rect(candidate.Min.X, candidate.Min.Y, candidate.Max.X, intersection.Min.Y),
				image.Rect(candidate.Min.X, intersection.Max.Y, candidate.Max.X, candidate.Max.Y),
				image.Rect(candidate.Min.X, intersection.Min.Y, intersection.Min.X, intersection.Max.Y),
				image.Rect(intersection.Max.X, intersection.Min.Y, candidate.Max.X, intersection.Max.Y),
			} {
				if !fragment.Empty() {
					next = append(next, fragment)
				}
			}
		}
		remaining = next
		if len(remaining) == 0 {
			return true
		}
	}
	return false
}

func (u *windowUI) handleOverlayResult(result overlayResult, w *app.Window) {
	defer u.schedulePostCaptureMemoryCleanup()
	u.overlayActive = false
	u.dragging = false
	if result.err != nil {
		u.status = "오버레이 오류: " + result.err.Error()
		u.controller.DiscardSelectionCapture()
		u.restoreFromTray(w)
		return
	}
	if !result.ok {
		u.status = "선택을 취소했습니다. 언제든 다시 캡처할 수 있습니다."
		u.controller.DiscardSelectionCapture()
		u.restoreFromTray(w)
		return
	}
	if result.rememberRegion {
		screenRect := result.selection.Rect.Add(u.controller.VirtualScreen.Min)
		if screenRect.Empty() {
			u.status = "기억할 좌표가 비어 있습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			return
		}
		preset := config.SavedRegionPreset{
			ID:     fmt.Sprintf("region-%d", time.Now().UnixNano()),
			Name:   nextSavedRegionName(u.appConfig.SavedRegions),
			Region: config.ScreenRegionFromRect(screenRect),
		}
		u.appConfig.SavedRegions = append(u.appConfig.SavedRegions, preset)
		u.appConfig.ActiveSavedRegionID = preset.ID
		u.appConfig.SavedRegionEnabled = true
		u.controller.DiscardSelectionCapture()
		u.restoreFromTray(w)
		u.ensureSavedRegionButtons()
		u.saveConfigStatus("캡처 좌표를 추가했습니다: " + preset.Name + " · " + savedRegionLabel(preset.Region))
		return
	}
	var err error
	if result.mode == "Window" {
		err = u.commitWindowSelection(result.selection.Rect)
	} else if result.mode == "Scroll" {
		u.controller.DiscardSelectionCapture()
		u.startScrollCapture(result.selection, w)
		return
	} else if len(result.selection.Polygon) >= 3 {
		err = u.controller.CommitFreeformSelection(result.selection.Rect, result.selection.Polygon)
	} else {
		err = u.controller.CommitImageSelection(result.selection.Rect)
	}
	if err != nil {
		u.status = "선택 실패: " + err.Error()
		u.controller.DiscardSelectionCapture()
		u.restoreFromTray(w)
		return
	}
	u.resetCanvasView()
	u.clearEditHistory()
	size := u.controller.OutputSize()
	u.completeCapturedImage(w, fmt.Sprintf("%dx%d 캡처", size.X, size.Y))
}

func nextSavedRegionName(regions []config.SavedRegionPreset) string {
	used := make(map[string]bool, len(regions))
	for _, preset := range regions {
		used[preset.Name] = true
	}
	for number := 1; ; number++ {
		name := fmt.Sprintf("영역 %d", number)
		if !used[name] {
			return name
		}
	}
}

func (u *windowUI) completeCapturedImage(w *app.Window, label string) {
	u.resetCanvasView()
	u.clearEditHistory()
	if u.appConfig.EditAfterCapture {
		u.restoreFromTray(w)
		u.finishCaptureReadyWithInitialCopy(label + "를 편집 중입니다.")
		u.autoSaveCurrentImage()
		return
	}

	copyErr := u.copyPNGCurrentImage()
	var savePath string
	var saveErr error
	if u.appConfig.AutoSave {
		savePath, saveErr = u.controller.SaveOriginalPNG(u.configuredSaveDir())
		if saveErr == nil {
			u.requestFilmstripRefresh()
		}
	}
	title := "캡처 완료"
	message := label + "를 클립보드에 복사했습니다."
	if savePath != "" {
		message = label + "를 복사하고 " + filepath.Base(savePath) + "로 저장했습니다."
	}
	if copyErr != nil || saveErr != nil {
		title = "캡처 후 처리 실패"
		parts := make([]string, 0, 2)
		if copyErr != nil {
			parts = append(parts, "클립보드: "+copyErr.Error())
		}
		if saveErr != nil {
			parts = append(parts, "자동 저장: "+saveErr.Error())
		}
		message = strings.Join(parts, " · ")
	}
	u.status = title + ": " + message
	if copyErr != nil && savePath == "" {
		if u.notify != nil {
			_ = u.notify(title, message)
		}
		u.status += " (결과를 보존하기 위해 편집 화면을 열었습니다.)"
		u.restoreFromTray(w)
		u.showToast(u.status)
		return
	}
	if u.notify == nil {
		u.status += " (트레이 알림을 사용할 수 없어 편집 화면을 열었습니다.)"
		u.restoreFromTray(w)
		u.showToast(u.status)
		return
	}
	if err := u.notify(title, message); err != nil {
		u.status += " (알림 실패로 편집 화면을 열었습니다: " + err.Error() + ")"
		u.restoreFromTray(w)
		u.showToast(u.status)
		return
	}
	u.autoCopyPending = false
	u.controller.ResetEditingSession()
	u.releaseTransientImageCaches()
	u.trayHidden = true
}

func (u *windowUI) commitWindowSelection(imageRect image.Rectangle) error {
	screenRect := imageRect.Add(u.controller.VirtualScreen.Min)
	target, ok := u.findWindowTarget(screenRect)
	if ok {
		if img, err := platformwindows.CaptureWindowTarget(target); err == nil {
			return u.controller.CommitExternalImage(target.Rect, img)
		}
	}
	return u.controller.CommitScreenCapture(screenRect)
}

func (u *windowUI) startScrollCapture(selection nativeoverlay.Selection, w *app.Window) {
	screenRect := selection.Rect.Add(u.controller.VirtualScreen.Min)
	target, ok := u.findWindowTarget(screenRect)
	if !ok {
		u.status = "스크롤 대상을 찾지 못했습니다."
		u.showToastDetail("스크롤 캡처 실패", u.status)
		u.restoreFromTray(w)
		return
	}
	u.captureActive = true
	u.status = "스크롤 캡처 진행 중..."
	u.showToastDetail("스크롤 캡처", "스크롤 가능한 내용을 캡처하는 중...")
	rangeRequest, hasRange := scrollCaptureRangeFromSelection(selection, u.controller.VirtualScreen.Min)
	if !hasRange && !selection.Rect.Empty() {
		selectedScreen := selection.Rect.Add(u.controller.VirtualScreen.Min).Intersect(target.Rect)
		if !selectedScreen.Empty() {
			local := selectedScreen.Sub(target.Rect.Min)
			rangeRequest = platformwindows.ScrollCaptureRange{
				Crop:               local,
				View:               local,
				InitialScrollPoint: local.Min.Add(local.Size().Div(2)),
			}
			hasRange = true
		}
	}
	go func() {
		var img image.Image
		var err error
		resultRect := image.Rectangle{}
		if hasRange {
			img, err = platformwindows.CaptureScrollRange(target, rangeRequest)
			resultRect = image.Rectangle{Min: target.Rect.Min.Add(rangeRequest.Crop.Min), Max: target.Rect.Min.Add(rangeRequest.Crop.Max)}
		} else {
			img, err = platformwindows.CaptureScrollTarget(target)
		}
		u.scrollResults <- scrollCaptureResult{target: target, img: img, screenRect: resultRect, err: err}
		w.Invalidate()
	}()
}

func scrollCaptureRangeFromSelection(selection nativeoverlay.Selection, virtualMin image.Point) (platformwindows.ScrollCaptureRange, bool) {
	if selection.ScrollRange == nil || selection.ScrollRange.CropRect.Empty() || selection.ScrollRange.WindowRect.Empty() {
		return platformwindows.ScrollCaptureRange{}, false
	}
	windowRect := selection.ScrollRange.WindowRect.Add(virtualMin)
	cropRect := selection.ScrollRange.CropRect.Add(virtualMin)
	viewRect := selection.ScrollRange.ViewRect.Add(virtualMin)
	crop := image.Rectangle{
		Min: image.Pt(cropRect.Min.X-windowRect.Min.X, cropRect.Min.Y-windowRect.Min.Y),
		Max: image.Pt(cropRect.Max.X-windowRect.Min.X, cropRect.Max.Y-windowRect.Min.Y),
	}.Canon()
	view := image.Rectangle{
		Min: image.Pt(viewRect.Min.X-windowRect.Min.X, viewRect.Min.Y-windowRect.Min.Y),
		Max: image.Pt(viewRect.Max.X-windowRect.Min.X, viewRect.Max.Y-windowRect.Min.Y),
	}.Canon()
	if crop.Empty() {
		return platformwindows.ScrollCaptureRange{}, false
	}
	initialPoint := selection.ScrollRange.InitialScrollPoint.Add(virtualMin).Sub(windowRect.Min)
	return platformwindows.ScrollCaptureRange{
		Crop:                 crop,
		View:                 view,
		ScrollSteps:          selection.ScrollRange.ScrollSteps,
		ScrollPixels:         selection.ScrollRange.ScrollPixels,
		InitialScrollPercent: selection.ScrollRange.InitialScrollPercent,
		InitialScrollPoint:   initialPoint,
		InitialWheelUnits:    selection.ScrollRange.InitialWheelUnits,
	}, true
}

func (u *windowUI) handleScrollCaptureResult(result scrollCaptureResult, w *app.Window) {
	u.captureActive = false
	if result.err != nil {
		u.restoreFromTray(w)
		u.status = "스크롤 캡처 실패: " + result.err.Error()
		u.showToastDetail("스크롤 캡처 실패", result.err.Error())
		return
	}
	if result.img == nil || result.img.Bounds().Empty() {
		u.restoreFromTray(w)
		u.status = "스크롤 캡처 실패: 빈 이미지입니다."
		u.showToastDetail("스크롤 캡처 실패", "빈 이미지")
		return
	}
	screenRect := result.screenRect
	if screenRect.Empty() {
		screenRect = scrollExternalRect(result.target, result.img)
	} else {
		screenRect.Max = screenRect.Min.Add(result.img.Bounds().Size())
	}
	if err := u.controller.CommitExternalImage(screenRect, result.img); err != nil {
		u.restoreFromTray(w)
		u.status = "스크롤 캡처 실패: " + err.Error()
		u.showToastDetail("스크롤 캡처 실패", err.Error())
		return
	}
	size := u.controller.OutputSize()
	u.completeCapturedImage(w, fmt.Sprintf("%dx%d 스크롤 캡처", size.X, size.Y))
	u.schedulePostCaptureMemoryCleanup()
}

func scrollExternalRect(target platformwindows.WindowTarget, img image.Image) image.Rectangle {
	bounds := img.Bounds()
	return image.Rectangle{Min: target.Rect.Min, Max: target.Rect.Min.Add(bounds.Size())}
}

func (u *windowUI) findWindowTarget(screenRect image.Rectangle) (platformwindows.WindowTarget, bool) {
	for _, target := range u.windowTargets {
		if target.Rect.Eq(screenRect) {
			return target, true
		}
	}
	best := platformwindows.WindowTarget{}
	bestArea := 0
	for _, target := range u.windowTargets {
		intersection := target.Rect.Intersect(screenRect)
		area := intersection.Dx() * intersection.Dy()
		if area > bestArea {
			best = target
			bestArea = area
		}
	}
	return best, bestArea > 0
}

func (u *windowUI) commitGesture() {
	switch u.controller.Mode {
	case workflow.ModeSelecting:
		selection := editor.NewRect(u.dragStart, u.dragNow).ImageRect()
		if err := u.controller.CommitImageSelection(selection); err != nil {
			u.status = "선택 실패: " + err.Error()
			return
		}
		size := u.controller.OutputSize()
		u.finishCaptureReadyWithInitialCopy(fmt.Sprintf("%dx%d 캡처를 편집 중입니다.", size.X, size.Y))
		u.autoSaveCurrentImage()
		u.schedulePostCaptureMemoryCleanup()
	case workflow.ModeEditing:
		if u.activeTool == editor.ToolText {
			u.beginNewTextEditing(u.dragStart, u.dragNow, u.controller.CapturedImageBounds())
			return
		}
		var err error
		switch u.activeTool {
		case editor.ToolPen:
			command := u.penRenderCommand()
			err = u.controller.AddPenAnnotation(command.PenMode, command.Points, command.Rect)
		case editor.ToolRectangle:
			err = u.controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(u.dragStart, u.dragNow))
		case editor.ToolTriangle:
			err = u.controller.AddAnnotation(editor.ToolTriangle, nil, editor.NewRect(u.dragStart, u.dragNow))
		case editor.ToolCircle:
			err = u.controller.AddAnnotation(editor.ToolCircle, nil, editor.NewRect(u.dragStart, u.dragNow))
		case editor.ToolArrow:
			err = u.controller.AddAnnotation(editor.ToolArrow, nil, editor.Rect{Min: u.dragStart, Max: u.dragNow})
		case editor.ToolLine:
			err = u.controller.AddAnnotation(editor.ToolLine, nil, editor.Rect{Min: u.dragStart, Max: u.dragNow})
		case editor.ToolHighlight:
			err = u.controller.AddAnnotation(editor.ToolHighlight, u.highlighterPoints(), editor.Rect{})
		case editor.ToolMosaic:
			command := u.mosaicRenderCommand()
			err = u.controller.AddMosaicAnnotation(command.MosaicMode, command.Points, command.Rect)
		}
		if err != nil {
			u.status = "주석 추가 실패: " + err.Error()
			return
		}
		u.clearMosaicHoverCache()
		view := u.captureCanvasView()
		u.recordControllerEdit(view, view)
		if u.autoCopyPending {
			u.autoCopyPending = false
			if err := u.copyPNGCurrentImage(); err != nil {
				u.status = fmt.Sprintf("%s 주석을 추가했습니다. 자동 복사 실패: %s", u.activeToolLabel(), err.Error())
				u.showToast("자동 복사 실패")
			} else {
				u.status = fmt.Sprintf("%s 주석을 추가하고 클립보드에 복사했습니다.", u.activeToolLabel())
				u.showToast("첫 주석 후 복사 완료")
			}
		} else {
			u.status = fmt.Sprintf("%s 주석을 추가했습니다.", u.activeToolLabel())
		}
	}
}

func (u *windowUI) drawGesturePreview(ops *op.Ops, scale float64) {
	if !u.dragging {
		return
	}
	if u.controller.Mode == workflow.ModeEditing {
		if u.activeTool == editor.ToolPen && u.penMode == editor.PenModeRectangle {
			return
		}
		if u.activeTool == editor.ToolHighlight {
			return
		}
		if u.activeTool == editor.ToolMosaic {
			if u.mosaicMode == editor.MosaicModeRectangle {
				if scale <= 0 {
					scale = 1
				}
				drawStrokeRect(ops, editor.NewRect(u.dragStart, u.dragNow), color.NRGBA{R: 255, G: 64, B: 64, A: 255}, float32(2/scale))
			}
			return
		}
	}

	style := u.controller.AnnotationStyle
	c := color.NRGBA{R: style.ColorRGBA[0], G: style.ColorRGBA[1], B: style.ColorRGBA[2], A: style.ColorRGBA[3]}
	width := float32(style.Width)

	if u.controller.Mode == workflow.ModeSelecting {
		drawStrokeRect(ops, editor.NewRect(u.dragStart, u.dragNow), c, width)
		return
	}
	if u.activeTool == editor.ToolRectangle {
		drawStrokeRect(ops, editor.NewRect(u.dragStart, u.dragNow), c, width)
		return
	}
	if u.activeTool == editor.ToolTriangle {
		drawStrokeTriangle(ops, editor.NewRect(u.dragStart, u.dragNow), c, width)
		return
	}
	if u.activeTool == editor.ToolCircle {
		drawStrokeEllipse(ops, editor.NewRect(u.dragStart, u.dragNow).ImageRect(), c, width)
		return
	}
	if u.activeTool == editor.ToolText {
		drawStrokeRect(ops, editor.NewRect(u.dragStart, u.dragNow), c, width)
		return
	}
	if u.activeTool == editor.ToolArrow {
		drawArrow(ops, u.dragStart, u.dragNow, c, width)
		return
	}
	if u.activeTool == editor.ToolLine {
		drawPolyline(ops, []editor.Point{u.dragStart, u.dragNow}, c, width)
		return
	}
	if u.activeTool == editor.ToolPen && u.penMode == editor.PenModeStraight {
		drawPolyline(ops, []editor.Point{u.dragStart, u.dragNow}, c, width)
		return
	}
	if u.activeTool == editor.ToolHighlight {
		highlight := u.controller.HighlighterStyle
		c = color.NRGBA{R: highlight.ColorRGBA[0], G: highlight.ColorRGBA[1], B: highlight.ColorRGBA[2], A: highlight.ColorRGBA[3]}
		width = float32(highlight.Width)
		if u.highlightStraight {
			drawPolyline(ops, []editor.Point{u.dragStart, u.dragNow}, c, width)
			return
		}
	}
	drawPolyline(ops, u.penPoints, c, width)
}

func clampEditorPoint(p editor.Point, bounds image.Rectangle) editor.Point {
	if p.X < bounds.Min.X {
		p.X = bounds.Min.X
	}
	if p.Y < bounds.Min.Y {
		p.Y = bounds.Min.Y
	}
	if p.X > bounds.Max.X {
		p.X = bounds.Max.X
	}
	if p.Y > bounds.Max.Y {
		p.Y = bounds.Max.Y
	}
	return p
}

func drawStrokeRect(ops *op.Ops, rect editor.Rect, c color.NRGBA, width float32) {
	r := rect.Normalize()
	points := []editor.Point{
		r.Min,
		{X: r.Max.X, Y: r.Min.Y},
		r.Max,
		{X: r.Min.X, Y: r.Max.Y},
		r.Min,
	}
	drawPolyline(ops, points, c, width)
}

func drawStrokeTriangle(ops *op.Ops, rect editor.Rect, c color.NRGBA, width float32) {
	r := rect.Normalize()
	drawPolyline(ops, []editor.Point{
		{X: r.Min.X + r.Width()/2, Y: r.Min.Y},
		{X: r.Max.X, Y: r.Max.Y},
		{X: r.Min.X, Y: r.Max.Y},
		{X: r.Min.X + r.Width()/2, Y: r.Min.Y},
	}, c, width)
}

func drawStrokeEllipse(ops *op.Ops, rect image.Rectangle, c color.NRGBA, width float32) {
	path := clip.Ellipse(rect.Canon()).Path(ops)
	paint.FillShape(ops, c, clip.Stroke{Path: path, Width: width}.Op())
}

func drawPolyline(ops *op.Ops, points []editor.Point, c color.NRGBA, width float32) {
	if len(points) < 2 {
		return
	}
	var path clip.Path
	path.Begin(ops)
	path.MoveTo(f32.Pt(float32(points[0].X), float32(points[0].Y)))
	for _, point := range points[1:] {
		path.LineTo(f32.Pt(float32(point.X), float32(point.Y)))
	}
	paint.FillShape(ops, c, clip.Stroke{Path: path.End(), Width: width}.Op())
}

func drawArrow(ops *op.Ops, start, end editor.Point, c color.NRGBA, width float32) {
	drawArrowWithHeadLength(ops, start, end, c, width, 18)
}

func drawArrowWithHeadLength(ops *op.Ops, start, end editor.Point, c color.NRGBA, width float32, headLen float64) {
	drawPolyline(ops, []editor.Point{start, end}, c, width)
	angle := math.Atan2(float64(end.Y-start.Y), float64(end.X-start.X))
	spread := math.Pi / 7
	for _, sign := range []float64{-1, 1} {
		x := float64(end.X) - headLen*math.Cos(angle+sign*spread)
		y := float64(end.Y) - headLen*math.Sin(angle+sign*spread)
		drawPolyline(ops, []editor.Point{end, {X: int(x), Y: int(y)}}, c, width)
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func currentMonitorRect(fallback image.Rectangle, displays []image.Rectangle) image.Rectangle {
	cursor := platformwindows.CursorPosition()
	for _, display := range displays {
		if cursor.In(display) {
			return display
		}
	}
	if len(displays) > 0 {
		return displays[0]
	}
	return fallback
}
