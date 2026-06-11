package ui

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/f32"
	"gioui.org/font"
	"gioui.org/font/gofont"
	"gioui.org/font/opentype"
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	"instantcapture/internal/capture"
	"instantcapture/internal/config"
	"instantcapture/internal/coords"
	"instantcapture/internal/editor"
	"instantcapture/internal/hotkey"
	"instantcapture/internal/nativeoverlay"
	"instantcapture/internal/overlay"
	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/tray"
	"instantcapture/internal/workflow"
)

const windowTitle = "InstantCapture"

const (
	minCanvasZoom             = 0.25
	maxCanvasZoom             = 8.0
	cropHandleScreenTolerance = 4
	maxHotkeyCaptureKeys      = 3
)

type cropHandle uint8

const (
	cropHandleNone cropHandle = 0
	cropHandleLeft cropHandle = 1 << iota
	cropHandleRight
	cropHandleTop
	cropHandleBottom
)

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
	mode      string
	selection nativeoverlay.Selection
	ok        bool
	err       error
}

type captureStartResult struct {
	mode          string
	snapshot      workflow.CaptureSnapshot
	windowTargets []platformwindows.WindowTarget
	err           error
}

type scrollCaptureResult struct {
	target     platformwindows.WindowTarget
	img        image.Image
	screenRect image.Rectangle
	err        error
}

type windowEventItem struct {
	event event.Event
	ack   chan struct{}
}

type windowUI struct {
	controller *workflow.Controller
	theme      *material.Theme
	ctx        context.Context

	newCaptureButton     widget.Clickable
	penButton            widget.Clickable
	rectButton           widget.Clickable
	arrowButton          widget.Clickable
	lineButton           widget.Clickable
	highlightButton      widget.Clickable
	copyButton           widget.Clickable
	saveButton           widget.Clickable
	settingsButton       widget.Clickable
	captureModeButton    widget.Clickable
	captureModeItems     [5]widget.Clickable
	hotkeyButton         widget.Clickable
	hideMinButton        widget.Clickable
	hideFocusButton      widget.Clickable
	autoSaveButton       widget.Clickable
	saveDirEditor        widget.Editor
	saveDirApplyButton   widget.Clickable
	saveDirBrowseButton  widget.Clickable
	strokeColorButton    widget.Clickable
	strokeWidthButton    widget.Clickable
	paletteButtons       [5]widget.Clickable
	rgbEditor            widget.Editor
	rgbApplyButton       widget.Clickable
	strokeWidthSlider    widget.Float
	highlightWidthSlider widget.Float
	highlightAlphaSlider widget.Float
	rgbInitialized       bool
	saveDirInitialized   bool

	canvasTag struct{}
	keyTag    struct{}

	status                 string
	toastTitle             string
	toastMessage           string
	toastUntil             time.Time
	autoCopyPending        bool
	appConfig              config.Config
	configPath             string
	showSettings           bool
	showModeDropdown       bool
	hotkeyCaptureMode      bool
	hotkeyCapturePressed   []string
	hotkeyCaptureCandidate hotkey.Binding
	hotkeyCaptureHasValue  bool
	registrar              *hotkey.GlobalRegistrar
	captureHotkey          func()
	rebindHotkey           func(hotkey.Binding, func()) error
	saveConfig             func(config.Config) error
	copyPNG                func() error
	trayHidden             bool
	hasFocused             bool
	trayAvailable          bool
	suppressFocusLossUntil time.Time

	overlayActive  bool
	overlayResults chan overlayResult
	captureActive  bool
	captureResults chan captureStartResult
	scrollResults  chan scrollCaptureResult
	windowTargets  []platformwindows.WindowTarget

	dragging   bool
	dragStart  editor.Point
	dragNow    editor.Point
	penPoints  []editor.Point
	activeTool editor.Tool

	canvasZoom       float64
	canvasPan        image.Point
	canvasSourceSize image.Point
	canvasPanning    bool
	canvasPanLast    editor.Point
	undoStack        []uiEditHistoryEntry
	redoStack        []uiEditHistoryEntry

	cropDragging    bool
	cropHoverHandle cropHandle
	cropDragHandle  cropHandle
	cropStartRect   image.Rectangle
	cropPreviewRect image.Rectangle
}

func RunMainWindow(ctx context.Context) error {
	w := new(app.Window)
	w.Option(
		app.Title(windowTitle),
		app.Size(unit.Dp(1100), unit.Dp(760)),
	)

	cfg, cfgErr := config.Load()
	cfgPath, _ := config.Path()
	if _, ok := hotkey.ParseBinding(cfg.Hotkey); !ok {
		cfg.Hotkey = hotkey.DefaultBinding().Name
	}
	if !validCaptureMode(cfg.CaptureMode) {
		cfg.CaptureMode = "Region"
	}

	ui := &windowUI{
		controller:             NewDefaultController(),
		theme:                  newTheme(),
		ctx:                    ctx,
		status:                 fmt.Sprintf("%s 또는 지금 캡처를 눌러 시작하세요.", cfg.Hotkey),
		appConfig:              cfg,
		configPath:             cfgPath,
		saveConfig:             config.Save,
		suppressFocusLossUntil: time.Now().Add(2 * time.Second),
		overlayResults:         make(chan overlayResult, 1),
		captureResults:         make(chan captureStartResult, 1),
		scrollResults:          make(chan scrollCaptureResult, 1),
		activeTool:             editor.ToolRectangle,
	}
	ui.applyStrokeStyle()
	ui.strokeWidthSlider.Value = widthToSlider(ui.appConfig.StrokeWidth)
	ui.highlightWidthSlider.Value = highlighterWidthToSlider(ui.appConfig.HighlighterWidth)
	ui.highlightAlphaSlider.Value = highlighterAlphaToSlider(ui.appConfig.HighlighterAlpha)
	if cfgErr != nil {
		ui.status = "설정 로드 실패, 기본값을 사용합니다: " + cfgErr.Error()
	}

	hotkeyEvents := make(chan struct{}, 1)
	focusLossEvents := make(chan struct{}, 1)
	rawTrayEvents := make(chan tray.Event, 4)
	trayEvents := make(chan tray.Event, 4)
	trayIcon, trayErr := tray.Start(rawTrayEvents)
	if trayErr != nil {
		ui.status = "트레이 초기화 실패: " + trayErr.Error()
	} else {
		ui.trayAvailable = true
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
				w.Invalidate()
			}
		}
	}()

	registrar := hotkey.NewGlobalRegistrar()
	ui.registrar = registrar
	ui.rebindHotkey = registrar.RebindCapture
	ui.captureHotkey = func() {
		select {
		case hotkeyEvents <- struct{}{}:
		default:
		}
		w.Invalidate()
	}
	binding, _ := hotkey.ParseBinding(ui.appConfig.Hotkey)
	if err := registrar.RegisterCaptureBinding(binding, ui.captureHotkey); err != nil {
		ui.status = "전역 단축키 등록 실패: " + err.Error()
	} else {
		defer registrar.Close()
	}
	windowEvents := forwardWindowEvents(ctx, w)
	var ops op.Ops
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hotkeyEvents:
			ui.beginCapture(w)
		case result := <-ui.captureResults:
			ui.handleCaptureStartResult(result, w)
		case result := <-ui.scrollResults:
			ui.handleScrollCaptureResult(result, w)
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

func newTheme() *material.Theme {
	theme := material.NewTheme()
	theme.Face = font.Typeface("Malgun Gothic")
	theme.Shaper = koreanTextShaper()
	theme.Palette.Bg = color.NRGBA{R: 10, G: 15, B: 28, A: 255}
	theme.Palette.Fg = color.NRGBA{R: 235, G: 241, B: 250, A: 255}
	theme.Palette.ContrastBg = color.NRGBA{R: 45, G: 212, B: 191, A: 255}
	theme.Palette.ContrastFg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	return theme
}

func koreanTextShaper() *text.Shaper {
	faces := append([]font.FontFace{}, gofont.Collection()...)
	for _, path := range koreanFontPaths() {
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

func koreanFontPaths() []string {
	windir := os.Getenv("WINDIR")
	if windir == "" {
		windir = `C:\Windows`
	}
	fonts := filepath.Join(windir, "Fonts")
	return []string{
		filepath.Join(fonts, "malgun.ttf"),
		filepath.Join(fonts, "malgunbd.ttf"),
		filepath.Join(fonts, "malgunsl.ttf"),
	}
}

func NewDefaultController() *workflow.Controller {
	return workflow.NewController(capture.ScreenshotCapturer{})
}

func (u *windowUI) layout(gtx layout.Context, w *app.Window) layout.Dimensions {
	paint.Fill(gtx.Ops, color.NRGBA{R: 8, G: 13, B: 26, A: 255})
	if u.hotkeyCaptureMode {
		event.Op(gtx.Ops, &u.keyTag)
		gtx.Execute(key.FocusCmd{Tag: &u.keyTag})
		u.handleHotkeyCapture(gtx)
		if u.hotkeyCaptureMode {
			u.syncNativeHotkeyCaptureKeys(platformwindows.PressedSpecialShortcutKeys())
		}
		if u.hotkeyCaptureMode {
			gtx.Execute(op.InvalidateCmd{At: time.Now().Add(16 * time.Millisecond)})
		}
	} else if !u.showSettings && !u.showModeDropdown {
		event.Op(gtx.Ops, &u.keyTag)
		gtx.Execute(key.FocusCmd{Tag: &u.keyTag})
		u.handleShortcuts(gtx)
	}
	u.handleButtons(gtx, w)

	dims := layout.Flex{Axis: layout.Vertical}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutToolbar(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !u.showModeDropdown {
				return layout.Dimensions{}
			}
			return u.layoutModeDropdown(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			if !u.showSettings {
				return layout.Dimensions{}
			}
			return u.layoutSettings(gtx)
		}),
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.layoutCanvas(gtx, w)
		}),
	)
	u.layoutToast(gtx)
	return dims
}

func (u *windowUI) layoutToolbar(gtx layout.Context) layout.Dimensions {
	barHeight := gtx.Dp(unit.Dp(68))
	gtx.Constraints.Min.Y = barHeight
	gtx.Constraints.Max.Y = barHeight
	paint.FillShape(gtx.Ops, color.NRGBA{R: 13, G: 20, B: 38, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(10), Bottom: unit.Dp(10), Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					label := material.H6(u.theme, "InstantCapture")
					label.Color = color.NRGBA{R: 245, G: 248, B: 255, A: 255}
					return label.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.captureModeButton, "모드: "+modeLabel(u.appConfig.CaptureMode), true).Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return accentButton(u.theme, &u.newCaptureButton, "지금 캡처").Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.iconToolButton(gtx, &u.rectButton, editor.ToolRectangle)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.iconToolButton(gtx, &u.penButton, editor.ToolPen)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.iconToolButton(gtx, &u.arrowButton, editor.ToolArrow)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.iconToolButton(gtx, &u.lineButton, editor.ToolLine)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.iconToolButton(gtx, &u.highlightButton, editor.ToolHighlight)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return neutralButton(u.theme, &u.copyButton, "복사").Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return neutralButton(u.theme, &u.saveButton, "저장").Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return neutralButton(u.theme, &u.settingsButton, "설정").Layout(gtx)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, "도구: "+toolLabel(u.activeTool))
				label.Color = color.NRGBA{R: 171, G: 184, B: 205, A: 255}
				return label.Layout(gtx)
			}),
		)
	})
}

func (u *windowUI) layoutToast(gtx layout.Context) {
	if u.toastMessage == "" || time.Now().After(u.toastUntil) {
		return
	}
	gtx.Execute(op.InvalidateCmd{At: u.toastUntil})
	width := gtx.Dp(unit.Dp(430))
	height := gtx.Dp(unit.Dp(96))
	x := gtx.Constraints.Max.X - width - gtx.Dp(unit.Dp(18))
	if x < 0 {
		x = 0
	}
	y := gtx.Dp(unit.Dp(82))
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: image.Pt(width, height)}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 15, G: 23, B: 42, A: 252}, clip.RRect{Rect: rect, SE: 14, SW: 14, NW: 14, NE: 14}.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, color.NRGBA{R: 34, G: 211, B: 238, A: 255}, clip.Rect{Max: image.Pt(gtx.Dp(unit.Dp(5)), height)}.Op())
	layout.UniformInset(unit.Dp(16)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max = rect.Size().Sub(image.Pt(gtx.Dp(unit.Dp(32)), gtx.Dp(unit.Dp(32))))
		title := u.toastTitle
		if title == "" {
			title = u.toastMessage
		}
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(4))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body1(u.theme, title)
				label.Color = color.NRGBA{R: 248, G: 250, B: 252, A: 255}
				return label.Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, u.toastMessage)
				label.Color = color.NRGBA{R: 203, G: 213, B: 225, A: 255}
				return label.Layout(gtx)
			}),
		)
	})
}

func (u *windowUI) showToast(message string) {
	u.toastTitle = ""
	u.toastMessage = message
	u.toastUntil = time.Now().Add(4 * time.Second)
}

func (u *windowUI) showToastDetail(title, message string) {
	u.toastTitle = title
	u.toastMessage = message
	u.toastUntil = time.Now().Add(5 * time.Second)
}

func (u *windowUI) layoutModeDropdown(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, color.NRGBA{R: 11, G: 18, B: 34, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(150), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
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

func (u *windowUI) layoutSettings(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, color.NRGBA{R: 15, G: 23, B: 42, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(12), Left: unit.Dp(16), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(12))}.Layout(gtx,
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						label := material.Body1(u.theme, "설정")
						label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
						return label.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return toggleButton(u.theme, &u.hotkeyButton, u.hotkeyButtonLabel(), true).Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return toggleButton(u.theme, &u.hideMinButton, "최소화 시 트레이: "+onOff(u.appConfig.HideOnMinimize), u.appConfig.HideOnMinimize).Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return toggleButton(u.theme, &u.hideFocusButton, "포커스 해제 시 트레이: "+onOff(u.appConfig.HideOnFocusLoss), u.appConfig.HideOnFocusLoss).Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutStyleSettings(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutSaveSettings(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutHighlighterSettings(gtx)
			}),
		)
	})
}

func (u *windowUI) layoutSaveSettings(gtx layout.Context) layout.Dimensions {
	if !u.saveDirInitialized {
		u.saveDirEditor.SingleLine = true
		u.saveDirEditor.SetText(u.configuredSaveDir())
		u.saveDirInitialized = true
	}
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, "저장")
			label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return toggleButton(u.theme, &u.autoSaveButton, "자동 저장: "+onOff(u.appConfig.AutoSave), u.appConfig.AutoSave).Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(430))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(430))
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

func (u *windowUI) layoutStyleSettings(gtx layout.Context) layout.Dimensions {
	if !u.rgbInitialized {
		u.rgbEditor.SingleLine = true
		u.rgbEditor.SetText(rgbText(strokeColor(u.appConfig.StrokeColor)))
		u.rgbInitialized = true
	}

	oldWidth := u.appConfig.StrokeWidth
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, "테두리")
			label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.layoutPalette(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(160))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(160))
			dims := material.Slider(u.theme, &u.strokeWidthSlider).Layout(gtx)
			u.appConfig.StrokeWidth = sliderToWidth(u.strokeWidthSlider.Value)
			if u.appConfig.StrokeWidth != oldWidth {
				u.applyStrokeStyle()
				_ = config.Save(u.appConfig)
			}
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, fmt.Sprintf("%dpx", u.appConfig.StrokeWidth))
			label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(110))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(110))
			return material.Editor(u.theme, &u.rgbEditor, "R,G,B").Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return neutralButton(u.theme, &u.rgbApplyButton, "RGB 적용").Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutHighlighterSettings(gtx layout.Context) layout.Dimensions {
	oldWidth := u.appConfig.HighlighterWidth
	oldAlpha := u.appConfig.HighlighterAlpha
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, "형광펜")
			label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(160))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(160))
			dims := material.Slider(u.theme, &u.highlightWidthSlider).Layout(gtx)
			u.appConfig.HighlighterWidth = highlighterSliderToWidth(u.highlightWidthSlider.Value)
			if u.appConfig.HighlighterWidth != oldWidth {
				u.applyStrokeStyle()
				_ = config.Save(u.appConfig)
			}
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, fmt.Sprintf("두께 %dpx", u.appConfig.HighlighterWidth))
			label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = gtx.Dp(unit.Dp(160))
			gtx.Constraints.Max.X = gtx.Dp(unit.Dp(160))
			dims := material.Slider(u.theme, &u.highlightAlphaSlider).Layout(gtx)
			u.appConfig.HighlighterAlpha = highlighterSliderToAlpha(u.highlightAlphaSlider.Value)
			if u.appConfig.HighlighterAlpha != oldAlpha {
				u.applyStrokeStyle()
				_ = config.Save(u.appConfig)
			}
			return dims
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, fmt.Sprintf("불투명도 %d%%", u.appConfig.HighlighterAlpha*100/255))
			label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
			return label.Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutPalette(gtx layout.Context) layout.Dimensions {
	presets := strokePresets()
	children := make([]layout.FlexChild, 0, len(presets))
	for i := range presets {
		idx := i
		children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return u.colorSwatch(gtx, &u.paletteButtons[idx], presets[idx].name, presets[idx].rgba)
		}))
	}
	return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(6))}.Layout(gtx, children...)
}

func (u *windowUI) colorSwatch(gtx layout.Context, button *widget.Clickable, name string, rgba [4]uint8) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(28)), gtx.Dp(unit.Dp(28)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		paint.FillShape(gtx.Ops, color.NRGBA{R: rgba[0], G: rgba[1], B: rgba[2], A: rgba[3]}, clip.RRect{Rect: image.Rectangle{Max: size}, SE: 8, SW: 8, NW: 8, NE: 8}.Op(gtx.Ops))
		if u.appConfig.StrokeColor == name {
			drawIconRect(gtx.Ops, image.Rect(3, 3, size.X-3, size.Y-3), color.NRGBA{R: 15, G: 23, B: 42, A: 255}, 3)
		}
		return layout.Dimensions{Size: size}
	})
}

func (u *windowUI) handleConfigEvent(e app.ConfigEvent, w *app.Window) {
	if e.Config.Focused {
		u.hasFocused = true
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
		time.Now().After(u.suppressFocusLossUntil) &&
		platformwindows.WindowVisibleByTitle(windowTitle) &&
		!platformwindows.ForegroundIsWindowTitle(windowTitle)
}

func (u *windowUI) hideToTray(message string, w *app.Window) {
	if err := platformwindows.HideWindowByTitle(windowTitle); err != nil {
		u.status = "트레이로 숨기기 실패: " + err.Error()
		return
	}
	u.trayHidden = true
	u.hasFocused = false
	u.status = message
	w.Invalidate()
}

func (u *windowUI) restoreFromTray(w *app.Window) {
	u.suppressFocusLossUntil = time.Now().Add(2 * time.Second)
	if err := platformwindows.RestoreWindowByTitle(windowTitle); err != nil {
		u.hasFocused = false
	} else {
		u.trayHidden = false
		u.hasFocused = false
	}
	w.Invalidate()
}

func externalRestoreWindow() {
	_ = platformwindows.RestoreWindowByTitle(windowTitle)
}

func accentButton(th *material.Theme, button *widget.Clickable, text string) material.ButtonStyle {
	style := material.Button(th, button, text)
	style.Background = color.NRGBA{R: 20, G: 184, B: 166, A: 255}
	style.Color = color.NRGBA{R: 4, G: 12, B: 20, A: 255}
	return style
}

func neutralButton(th *material.Theme, button *widget.Clickable, text string) material.ButtonStyle {
	style := material.Button(th, button, text)
	style.Background = color.NRGBA{R: 30, G: 41, B: 59, A: 255}
	style.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
	return style
}

func toggleButton(th *material.Theme, button *widget.Clickable, text string, enabled bool) material.ButtonStyle {
	style := neutralButton(th, button, text)
	if enabled {
		style.Background = color.NRGBA{R: 21, G: 128, B: 120, A: 255}
	}
	return style
}

func (u *windowUI) iconToolButton(gtx layout.Context, button *widget.Clickable, tool editor.Tool) layout.Dimensions {
	active := u.activeTool == tool
	size := image.Pt(gtx.Dp(unit.Dp(42)), gtx.Dp(unit.Dp(36)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	return button.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		bg := color.NRGBA{R: 30, G: 41, B: 59, A: 255}
		fg := color.NRGBA{R: 226, G: 232, B: 240, A: 255}
		if active {
			bg = color.NRGBA{R: 245, G: 96, B: 76, A: 255}
			fg = color.NRGBA{R: 4, G: 12, B: 20, A: 255}
		}
		paint.FillShape(gtx.Ops, bg, clip.RRect{Rect: image.Rectangle{Max: size}, SE: 10, SW: 10, NW: 10, NE: 10}.Op(gtx.Ops))
		drawToolIcon(gtx.Ops, tool, size, fg)
		return layout.Dimensions{Size: size}
	})
}

func drawToolIcon(ops *op.Ops, tool editor.Tool, size image.Point, c color.NRGBA) {
	cx := float32(size.X) / 2
	cy := float32(size.Y) / 2
	switch tool {
	case editor.ToolRectangle:
		drawIconRect(ops, image.Rect(int(cx-11), int(cy-8), int(cx+11), int(cy+8)), c, 2.5)
	case editor.ToolPen:
		drawIconLine(ops, f32.Pt(cx-11, cy+9), f32.Pt(cx+9, cy-11), c, 3)
		drawIconLine(ops, f32.Pt(cx+5, cy-12), f32.Pt(cx+11, cy-6), c, 3)
	case editor.ToolArrow:
		drawIconLine(ops, f32.Pt(cx-12, cy+8), f32.Pt(cx+11, cy-8), c, 3)
		drawIconLine(ops, f32.Pt(cx+11, cy-8), f32.Pt(cx+4, cy-9), c, 3)
		drawIconLine(ops, f32.Pt(cx+11, cy-8), f32.Pt(cx+8, cy-1), c, 3)
	case editor.ToolLine:
		drawIconLine(ops, f32.Pt(cx-12, cy+8), f32.Pt(cx+12, cy-8), c, 3)
	case editor.ToolHighlight:
		drawIconLine(ops, f32.Pt(cx-12, cy+6), f32.Pt(cx+12, cy+6), color.NRGBA{R: 250, G: 204, B: 21, A: 255}, 7)
		drawIconLine(ops, f32.Pt(cx-9, cy-6), f32.Pt(cx+7, cy-10), c, 3)
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
	case editor.ToolArrow:
		return "화살표"
	case editor.ToolLine:
		return "선"
	case editor.ToolHighlight:
		return "형광펜"
	default:
		return string(tool)
	}
}

func colorLabel(name string) string {
	switch name {
	case "Red":
		return "빨강"
	case "Cyan":
		return "하늘"
	case "Yellow":
		return "노랑"
	case "Green":
		return "초록"
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
}

func (u *windowUI) setStrokeColor(name string) {
	u.appConfig.StrokeColor = name
	u.rgbEditor.SetText(rgbText(strokeColor(name)))
	u.applyStrokeStyle()
}

type strokePreset struct {
	name string
	rgba [4]uint8
}

func strokePresets() []strokePreset {
	return []strokePreset{
		{name: "Red", rgba: [4]uint8{255, 64, 64, 255}},
		{name: "Cyan", rgba: [4]uint8{34, 211, 238, 255}},
		{name: "Yellow", rgba: [4]uint8{250, 204, 21, 255}},
		{name: "Green", rgba: [4]uint8{34, 197, 94, 255}},
		{name: "White", rgba: [4]uint8{255, 255, 255, 255}},
	}
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
	case "Cyan":
		return [4]uint8{34, 211, 238, 255}
	case "Yellow":
		return [4]uint8{250, 204, 21, 255}
	case "Green":
		return [4]uint8{34, 197, 94, 255}
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
	clip.Rect{Max: size}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 5, G: 10, B: 24, A: 255}, clip.Rect{Max: size}.Op())

	var img image.Image
	switch u.controller.Mode {
	case workflow.ModeSelecting:
		img = u.controller.FullScreenImage
	case workflow.ModeEditing:
		rendered, err := u.controller.PreviewImage()
		if err != nil {
			u.status = "렌더링 실패: " + err.Error()
			img = u.controller.CapturedImage
		} else {
			img = rendered
		}
	}

	if img == nil {
		return u.layoutEmptyCanvas(gtx, size)
	}

	sourceRect := image.Rectangle{Max: img.Bounds().Size()}
	viewport := u.canvasViewport(sourceRect, image.Rectangle{Max: size})
	if viewport.View.Empty() {
		return layout.Dimensions{Size: size}
	}

	defer op.Offset(viewport.View.Min).Push(gtx.Ops).Pop()
	scale := float32(viewport.Scale)
	defer op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(gtx.Ops).Pop()
	defer clip.Rect(sourceRect).Push(gtx.Ops).Pop()
	u.addCanvasCursor(gtx.Ops)
	event.Op(gtx.Ops, &u.canvasTag)
	u.handleCanvasPointer(gtx, w, sourceRect, image.Rectangle{Max: size}, viewport)

	paint.NewImageOp(img).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	u.drawCropFrame(gtx.Ops, sourceRect, viewport.Scale)
	u.drawGesturePreview(gtx.Ops)

	return layout.Dimensions{Size: size}
}

func (u *windowUI) layoutEmptyCanvas(gtx layout.Context, size image.Point) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		cardSize := image.Pt(min(size.X-48, 620), 190)
		if cardSize.X < 320 {
			cardSize.X = 320
		}
		gtx.Constraints.Min = cardSize
		gtx.Constraints.Max = cardSize
		paint.FillShape(gtx.Ops, color.NRGBA{R: 15, G: 23, B: 42, A: 255}, clip.RRect{Rect: image.Rectangle{Max: cardSize}, SE: 16, SW: 16, NW: 16, NE: 16}.Op(gtx.Ops))
		return layout.UniformInset(unit.Dp(24)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					label := material.H5(u.theme, "캡처 준비 완료")
					label.Color = color.NRGBA{R: 240, G: 249, B: 255, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						label := material.Body1(u.theme, "전역 단축키나 지금 캡처를 누른 뒤 영역을 드래그해 주석을 추가하세요.")
						label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
						return label.Layout(gtx)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(18)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return accentButton(u.theme, &u.newCaptureButton, "지금 캡처").Layout(gtx)
					})
				}),
			)
		})
	})
}

func (u *windowUI) handleButtons(gtx layout.Context, w *app.Window) {
	for u.newCaptureButton.Clicked(gtx) {
		u.beginCapture(w)
	}
	for u.penButton.Clicked(gtx) {
		u.activeTool = editor.ToolPen
		_ = u.controller.Editor.SetActiveTool(editor.ToolPen)
		u.status = "도구: 펜"
	}
	for u.rectButton.Clicked(gtx) {
		u.activeTool = editor.ToolRectangle
		_ = u.controller.Editor.SetActiveTool(editor.ToolRectangle)
		u.status = "도구: 사각형"
	}
	for u.arrowButton.Clicked(gtx) {
		u.activeTool = editor.ToolArrow
		_ = u.controller.Editor.SetActiveTool(editor.ToolArrow)
		u.status = "도구: 화살표"
	}
	for u.lineButton.Clicked(gtx) {
		u.activeTool = editor.ToolLine
		_ = u.controller.Editor.SetActiveTool(editor.ToolLine)
		u.status = "도구: 선"
	}
	for u.highlightButton.Clicked(gtx) {
		u.activeTool = editor.ToolHighlight
		_ = u.controller.Editor.SetActiveTool(editor.ToolHighlight)
		u.status = "도구: 형광펜"
	}
	for u.copyButton.Clicked(gtx) {
		u.copyCurrentImage()
	}
	for u.saveButton.Clicked(gtx) {
		u.saveCurrentImage()
	}
	for u.settingsButton.Clicked(gtx) {
		u.showSettings = !u.showSettings
		if u.showSettings {
			u.showModeDropdown = false
		} else {
			u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
		}
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
	for u.hideMinButton.Clicked(gtx) {
		u.appConfig.HideOnMinimize = !u.appConfig.HideOnMinimize
		u.saveConfigStatus("최소화 시 트레이 설정: " + onOff(u.appConfig.HideOnMinimize))
	}
	for u.hideFocusButton.Clicked(gtx) {
		u.appConfig.HideOnFocusLoss = !u.appConfig.HideOnFocusLoss
		u.saveConfigStatus("포커스 해제 시 트레이 설정: " + onOff(u.appConfig.HideOnFocusLoss))
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
	for i, preset := range strokePresets() {
		for u.paletteButtons[i].Clicked(gtx) {
			u.setStrokeColor(preset.name)
			u.saveConfigStatus("테두리 색상 변경: " + colorLabel(preset.name))
		}
	}
	for u.rgbApplyButton.Clicked(gtx) {
		if rgb, ok := parseRGBText(u.rgbEditor.Text()); ok {
			u.setStrokeColor(rgbName(rgb))
			u.saveConfigStatus("테두리 RGB 변경: " + u.appConfig.StrokeColor)
		} else {
			u.status = "RGB 형식은 255,64,64처럼 R,G,B로 입력하세요."
		}
	}
}

func (u *windowUI) handleShortcuts(gtx layout.Context) {
	filters := []event.Filter{
		key.Filter{Name: key.NameEscape},
		key.Filter{Name: "S", Required: key.ModShortcut},
		key.Filter{Name: "s", Required: key.ModShortcut},
		key.Filter{Name: "C", Required: key.ModShortcut},
		key.Filter{Name: "c", Required: key.ModShortcut},
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
	if u.hotkeyCaptureMode {
		return u.handleHotkeyCaptureKey(ke)
	}
	switch strings.ToUpper(string(ke.Name)) {
	case string(key.NameEscape):
		u.dragging = false
		u.cropDragging = false
		u.status = "현재 동작을 취소했습니다."
		return true
	case "S":
		if ke.Modifiers.Contain(key.ModShortcut) {
			u.saveCurrentImage()
			return true
		}
	case "C":
		if ke.Modifiers.Contain(key.ModShortcut) {
			u.copyCurrentImage()
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

func (u *windowUI) handleHotkeyCaptureKey(ke key.Event) bool {
	if ke.Name == key.NameEscape && ke.State == key.Press {
		u.cancelHotkeyCapture("단축키 지정을 취소했습니다.")
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

func (u *windowUI) recordHotkeyCapturePress(ke key.Event) {
	for _, modifier := range hotkeyCaptureModifierNames(ke.Modifiers) {
		if !u.addHotkeyCaptureKey(modifier) {
			u.status = fmt.Sprintf("단축키는 최대 %d개 키까지 입력할 수 있습니다.", maxHotkeyCaptureKeys)
			return
		}
	}
	name := hotkeyCaptureEventKeyName(ke.Name)
	if name != "" {
		if !u.addHotkeyCaptureKey(name) {
			u.status = fmt.Sprintf("단축키는 최대 %d개 키까지 입력할 수 있습니다.", maxHotkeyCaptureKeys)
			return
		}
	}
	u.updateHotkeyCapturePreview()
}

func (u *windowUI) recordHotkeyCaptureRelease(ke key.Event) {
	if name := hotkeyCaptureEventKeyName(ke.Name); name != "" {
		u.removeHotkeyCaptureKey(name)
	}
	if len(u.hotkeyCapturePressed) == 0 {
		if u.hotkeyCaptureHasValue {
			u.applyHotkeyBinding(u.hotkeyCaptureCandidate)
			return
		}
		u.status = "Ctrl/Alt/Shift와 함께 사용할 키를 눌러주세요. Esc로 취소할 수 있습니다."
		return
	}
	u.updateHotkeyCapturePreview()
}

func (u *windowUI) saveCurrentImage() {
	path, err := u.saveCurrentImageWithStatus("PNG 저장 완료")
	if err == nil {
		u.status = "저장 완료: " + path
	}
}

func (u *windowUI) saveCurrentImageWithStatus(toastTitle string) (string, error) {
	dir := u.configuredSaveDir()
	path, err := u.controller.SavePNG(dir)
	if err != nil {
		u.status = "저장 실패: " + err.Error()
		u.showToastDetail("저장 실패", err.Error())
		return "", err
	}
	u.showToastDetail(toastTitle, toastSavePath(path))
	return path, nil
}

func (u *windowUI) autoSaveCurrentImage() {
	if !u.appConfig.AutoSave {
		return
	}
	path, err := u.saveCurrentImageWithStatus("PNG 자동 저장 완료")
	if err != nil {
		return
	}
	u.status = "자동 저장 완료: " + path
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
}

func (u *windowUI) chooseSaveDir() {
	dir, ok, err := platformwindows.PickFolder(u.configuredSaveDir())
	if err != nil {
		u.status = "폴더 선택 실패: " + err.Error()
		u.showToastDetail("폴더 선택 실패", err.Error())
		return
	}
	if !ok {
		u.status = "폴더 선택을 취소했습니다."
		return
	}
	dir = config.NormalizeSaveDir(dir)
	u.appConfig.SaveDir = dir
	u.saveDirEditor.SetText(dir)
	u.saveConfigStatus("저장 폴더 변경: " + dir)
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
	if u.copyPNG != nil {
		return u.copyPNG()
	}
	return u.controller.CopyPNG()
}

func (u *windowUI) finishCaptureReadyWithInitialCopy(status string) {
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
	u.redoStack = appendBoundedUIHistory(u.redoStack, entry)
	return true
}

func (u *windowUI) redoLastEdit() bool {
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
	if state.Zoom <= 0 {
		state.Zoom = 1
	}
	u.canvasZoom = state.Zoom
	u.canvasPan = state.Pan
	u.canvasSourceSize = state.SourceSize
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
	if u.canvasZoom <= 0 || u.canvasSourceSize != sourceRect.Size() {
		u.canvasZoom = 1
		u.canvasPan = image.Point{}
		u.canvasSourceSize = sourceRect.Size()
	}
	if u.canvasZoom == 1 && u.canvasPan == (image.Point{}) {
		return base
	}

	scale := base.Scale * u.canvasZoom
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
	u.canvasZoom = math.Max(minCanvasZoom, math.Min(maxCanvasZoom, u.canvasZoom*factor))
	if u.canvasZoom == oldZoom {
		return
	}

	baseScale := viewport.Scale / oldZoom
	newScale := baseScale * u.canvasZoom
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

func (u *windowUI) addCanvasCursor(ops *op.Ops) {
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

func isCanvasPanButton(buttons pointer.Buttons) bool {
	return buttons.Contain(pointer.ButtonTertiary)
}

func (u *windowUI) panCanvasBySourceDelta(from, to editor.Point, canvasBounds image.Rectangle, viewport coords.Viewport) bool {
	if viewport.Scale <= 0 || viewport.View.Empty() || canvasBounds.Empty() {
		return false
	}
	delta := image.Pt(
		int(math.Round(float64(to.X-from.X)*viewport.Scale)),
		int(math.Round(float64(to.Y-from.Y)*viewport.Scale)),
	)
	if delta == (image.Point{}) {
		return false
	}
	centeredMin := viewport.View.Min.Sub(u.canvasPan)
	next := clampCanvasPan(u.canvasPan.Add(delta), centeredMin, viewport.View.Size(), canvasBounds)
	if next == u.canvasPan {
		return false
	}
	u.canvasPan = next
	return true
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
	u.canvasZoom = 1
	u.canvasPan = image.Point{}
	u.canvasSourceSize = image.Point{}
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
	width := float32(max(1, int(math.Round(2/scale))))
	drawStrokeRect(ops, rectToEditorRect(rect), color.NRGBA{R: 56, G: 189, B: 248, A: 230}, width)

	handleSize := max(4, int(math.Round(8/scale)))
	for _, center := range cropHandleCenters(rect) {
		drawHandleSquare(ops, center, handleSize)
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

func (u *windowUI) beginHotkeyCapture() {
	u.hotkeyCaptureMode = true
	u.hotkeyCapturePressed = nil
	u.hotkeyCaptureCandidate = hotkey.Binding{}
	u.hotkeyCaptureHasValue = false
	u.status = "새 단축키를 눌러주세요. Esc로 취소할 수 있습니다."
	u.showToast("새 단축키를 눌러주세요")
}

func (u *windowUI) cancelHotkeyCapture(message string) {
	if !u.hotkeyCaptureMode {
		return
	}
	u.hotkeyCaptureMode = false
	u.hotkeyCapturePressed = nil
	u.hotkeyCaptureCandidate = hotkey.Binding{}
	u.hotkeyCaptureHasValue = false
	u.status = message
}

func (u *windowUI) applyHotkeyBinding(binding hotkey.Binding) {
	if binding.Name == u.appConfig.Hotkey {
		u.hotkeyCaptureMode = false
		u.hotkeyCapturePressed = nil
		u.hotkeyCaptureCandidate = hotkey.Binding{}
		u.hotkeyCaptureHasValue = false
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
	u.hotkeyCaptureMode = false
	u.hotkeyCapturePressed = nil
	u.hotkeyCaptureCandidate = hotkey.Binding{}
	u.hotkeyCaptureHasValue = false
	u.appConfig.Hotkey = binding.Name
	u.saveConfigStatus("단축키 변경: " + binding.Name)
}

func (u *windowUI) saveConfigStatus(success string) {
	save := u.saveConfig
	if save == nil {
		save = config.Save
	}
	if err := save(u.appConfig); err != nil {
		u.status = success + " (설정 저장 실패: " + err.Error() + ")"
		return
	}
	u.status = success
}

func hotkeyBindingFromKeyEvent(ke key.Event) (hotkey.Binding, bool) {
	keyName := hotkeyCaptureKeyName(ke.Name)
	if keyName == "" {
		return hotkey.Binding{}, false
	}
	return hotkey.BindingFromNames(hotkeyCaptureModifierNames(ke.Modifiers), keyName)
}

func (u *windowUI) addHotkeyCaptureKey(name string) bool {
	if name == "" {
		return true
	}
	for _, pressed := range u.hotkeyCapturePressed {
		if pressed == name {
			return true
		}
	}
	if len(u.hotkeyCapturePressed) >= maxHotkeyCaptureKeys {
		return false
	}
	u.hotkeyCapturePressed = append(u.hotkeyCapturePressed, name)
	return true
}

func (u *windowUI) removeHotkeyCaptureKey(name string) {
	for i, pressed := range u.hotkeyCapturePressed {
		if pressed == name {
			u.hotkeyCapturePressed = append(u.hotkeyCapturePressed[:i], u.hotkeyCapturePressed[i+1:]...)
			return
		}
	}
}

func (u *windowUI) updateHotkeyCapturePreview() {
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

func (u *windowUI) syncNativeHotkeyCaptureKeys(keys []string) {
	current := make(map[string]bool, len(keys))
	changed := false
	for _, name := range keys {
		if !isNativeHotkeyCaptureKey(name) {
			continue
		}
		current[name] = true
		if !u.addHotkeyCaptureKey(name) {
			u.status = fmt.Sprintf("단축키는 최대 %d개 키까지 입력할 수 있습니다.", maxHotkeyCaptureKeys)
			return
		}
		changed = true
	}
	for _, name := range nativeHotkeyCaptureKeys() {
		if !current[name] && u.hotkeyCaptureKeyPressed(name) {
			u.removeHotkeyCaptureKey(name)
			changed = true
		}
	}
	if !changed {
		return
	}
	if len(u.hotkeyCapturePressed) == 0 {
		if u.hotkeyCaptureHasValue {
			u.applyHotkeyBinding(u.hotkeyCaptureCandidate)
			return
		}
		u.status = "Ctrl/Alt/Shift와 함께 사용할 키를 눌러주세요. Esc로 취소할 수 있습니다."
		return
	}
	u.updateHotkeyCapturePreview()
}

func (u *windowUI) hotkeyCaptureKeyPressed(name string) bool {
	for _, pressed := range u.hotkeyCapturePressed {
		if pressed == name {
			return true
		}
	}
	return false
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
			Kinds:   pointer.Press | pointer.Drag | pointer.Release | pointer.Cancel | pointer.Move | pointer.Scroll,
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
		point := clampEditorPoint(editor.Point{X: int(pe.Position.X), Y: int(pe.Position.Y)}, bounds)
		cropRect := bounds
		if u.controller.Mode == workflow.ModeEditing {
			cropRect = u.controller.VisibleCropRect()
			if cropRect.Empty() {
				cropRect = bounds
			}
		}

		switch pe.Kind {
		case pointer.Scroll:
			if pe.Modifiers.Contain(key.ModCtrl) {
				u.zoomCanvas(pe.Scroll.Y, point, canvasBounds, viewport)
				w.Invalidate()
			}
		case pointer.Move:
			u.cropHoverHandle = u.hitCropHandle(point, cropRect, viewport.Scale)
			w.Invalidate()
		case pointer.Press:
			if u.controller.Mode == workflow.ModeEditing && isCanvasPanButton(pe.Buttons) {
				u.canvasPanning = true
				u.canvasPanLast = point
				u.dragging = false
				u.cropDragging = false
				u.status = "캔버스를 이동합니다."
				w.Invalidate()
				continue
			}
			if u.controller.Mode == workflow.ModeEditing {
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
			u.dragging = true
			u.dragStart = point
			u.dragNow = point
			u.penPoints = []editor.Point{point}
		case pointer.Drag:
			if u.canvasPanning {
				u.panCanvasBySourceDelta(u.canvasPanLast, point, canvasBounds, viewport)
				u.canvasPanLast = point
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
			if u.controller.Mode == workflow.ModeEditing && (u.activeTool == editor.ToolPen || u.activeTool == editor.ToolHighlight) {
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
	if u.overlayActive || u.captureActive {
		u.status = "이미 캡처가 진행 중입니다."
		return
	}

	u.dragging = false
	u.resetCanvasView()
	u.clearEditHistory()
	u.autoCopyPending = false
	u.captureActive = true
	u.suppressFocusLossUntil = time.Now().Add(10 * time.Second)
	u.status = "화면을 캡처하는 중..."
	mode := u.appConfig.CaptureMode
	go func() {
		done := make(chan captureStartResult, 1)
		go func() {
			snapshot, err := u.captureSnapshotExternal()
			var windowTargets []platformwindows.WindowTarget
			if err == nil && (mode == "Window" || mode == "Scroll") {
				windowTargets = platformwindows.VisibleWindowTargets(windowTitle)
			}
			done <- captureStartResult{mode: mode, snapshot: snapshot, windowTargets: windowTargets, err: err}
		}()
		select {
		case result := <-done:
			u.captureResults <- result
		case <-time.After(8 * time.Second):
			u.captureResults <- captureStartResult{mode: mode, err: fmt.Errorf("화면 캡처 시간이 초과되었습니다")}
		case <-u.ctx.Done():
			return
		}
		w.Invalidate()
	}()
	w.Invalidate()
}

func (u *windowUI) captureSnapshotExternal() (workflow.CaptureSnapshot, error) {
	return workflow.CaptureScreen(u.controller.Capturer)
}

func (u *windowUI) handleCaptureStartResult(result captureStartResult, w *app.Window) {
	u.captureActive = false
	if result.err != nil {
		u.status = "캡처 실패: " + result.err.Error()
		return
	}
	if err := u.controller.BeginCaptureFromSnapshot(result.snapshot); err != nil {
		u.status = "캡처 실패: " + err.Error()
		return
	}

	mode := result.mode
	switch mode {
	case "Fullscreen":
		monitor := currentMonitorRect(u.controller.VirtualScreen, result.snapshot.Displays)
		if err := u.controller.CommitScreenSelection(monitor); err != nil {
			u.status = "전체화면 캡처 실패: " + err.Error()
			return
		}
		u.resetCanvasView()
		u.clearEditHistory()
		u.restoreFromTray(w)
		size := u.controller.OutputSize()
		u.finishCaptureReadyWithInitialCopy(fmt.Sprintf("%dx%d 모니터 캡처를 편집 중입니다.", size.X, size.Y))
		u.autoSaveCurrentImage()
		return
	case "Freeform":
		u.status = "자유형 모드: 대상 영역 둘레를 드래그하세요."
	case "Window":
		u.status = "창 모드: 창 위에 마우스를 올리고 클릭하세요."
		if len(result.windowTargets) == 0 {
			u.status = "창 모드: 선택할 수 있는 창이 없습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
			return
		}
	case "Scroll":
		u.status = "스크롤 모드: 창 안에서 드래그하세요. 아래/위 가장자리로 끌면 스크롤됩니다."
		if len(result.windowTargets) == 0 {
			u.status = "스크롤 모드: 선택할 수 있는 창이 없습니다."
			u.controller.DiscardSelectionCapture()
			u.restoreFromTray(w)
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
		selection, ok, err := nativeoverlay.SelectDetailed(u.ctx, img, overlay.Options{
			VirtualScreen:  virtualScreen,
			TopMostHint:    true,
			DimAlpha:       dimAlpha,
			CaptureMode:    mode,
			WindowRects:    windowRects,
			WindowRegions:  windowRegions,
			LiveBackground: mode == "Scroll",
		})
		u.overlayResults <- overlayResult{mode: mode, selection: selection, ok: ok, err: err}
		w.Invalidate()
	}()

	w.Invalidate()
}

func (u *windowUI) handleOverlayResult(result overlayResult, w *app.Window) {
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
	u.restoreFromTray(w)
	size := u.controller.OutputSize()
	u.finishCaptureReadyWithInitialCopy(fmt.Sprintf("%dx%d 캡처를 편집 중입니다.", size.X, size.Y))
	u.autoSaveCurrentImage()
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
	return platformwindows.ScrollCaptureRange{Crop: crop, View: view, ScrollSteps: selection.ScrollRange.ScrollSteps}, true
}

func (u *windowUI) handleScrollCaptureResult(result scrollCaptureResult, w *app.Window) {
	u.captureActive = false
	u.restoreFromTray(w)
	if result.err != nil {
		u.status = "스크롤 캡처 실패: " + result.err.Error()
		u.showToastDetail("스크롤 캡처 실패", result.err.Error())
		return
	}
	if result.img == nil || result.img.Bounds().Empty() {
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
		u.status = "스크롤 캡처 실패: " + err.Error()
		u.showToastDetail("스크롤 캡처 실패", err.Error())
		return
	}
	u.resetCanvasView()
	u.clearEditHistory()
	size := u.controller.OutputSize()
	u.finishCaptureReadyWithInitialCopy(fmt.Sprintf("%dx%d 스크롤 캡처를 편집 중입니다. 휠 버튼 드래그로 이동하고 테두리로 범위를 자를 수 있습니다.", size.X, size.Y))
	u.autoSaveCurrentImage()
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
	case workflow.ModeEditing:
		var err error
		switch u.activeTool {
		case editor.ToolPen:
			err = u.controller.AddAnnotation(editor.ToolPen, u.penPoints, editor.Rect{})
		case editor.ToolRectangle:
			err = u.controller.AddAnnotation(editor.ToolRectangle, nil, editor.NewRect(u.dragStart, u.dragNow))
		case editor.ToolArrow:
			err = u.controller.AddAnnotation(editor.ToolArrow, nil, editor.Rect{Min: u.dragStart, Max: u.dragNow})
		case editor.ToolLine:
			err = u.controller.AddAnnotation(editor.ToolLine, nil, editor.Rect{Min: u.dragStart, Max: u.dragNow})
		case editor.ToolHighlight:
			err = u.controller.AddAnnotation(editor.ToolHighlight, u.penPoints, editor.Rect{})
		}
		if err != nil {
			u.status = "주석 추가 실패: " + err.Error()
			return
		}
		view := u.captureCanvasView()
		u.recordControllerEdit(view, view)
		if u.autoCopyPending {
			u.autoCopyPending = false
			if err := u.copyPNGCurrentImage(); err != nil {
				u.status = fmt.Sprintf("%s 주석을 추가했습니다. 자동 복사 실패: %s", toolLabel(u.activeTool), err.Error())
				u.showToast("자동 복사 실패")
			} else {
				u.status = fmt.Sprintf("%s 주석을 추가하고 클립보드에 복사했습니다.", toolLabel(u.activeTool))
				u.showToast("첫 주석 후 복사 완료")
			}
		} else {
			u.status = fmt.Sprintf("%s 주석을 추가했습니다.", toolLabel(u.activeTool))
		}
	}
}

func (u *windowUI) drawGesturePreview(ops *op.Ops) {
	if !u.dragging {
		return
	}

	style := u.controller.AnnotationStyle
	c := color.NRGBA{R: style.ColorRGBA[0], G: style.ColorRGBA[1], B: style.ColorRGBA[2], A: style.ColorRGBA[3]}
	width := float32(style.Width)

	if u.controller.Mode == workflow.ModeSelecting || u.activeTool == editor.ToolRectangle {
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
	if u.activeTool == editor.ToolHighlight {
		highlight := u.controller.HighlighterStyle
		c = color.NRGBA{R: highlight.ColorRGBA[0], G: highlight.ColorRGBA[1], B: highlight.ColorRGBA[2], A: highlight.ColorRGBA[3]}
		width = float32(highlight.Width)
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
	drawPolyline(ops, []editor.Point{start, end}, c, width)
	angle := math.Atan2(float64(end.Y-start.Y), float64(end.X-start.X))
	headLen := 18.0
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
