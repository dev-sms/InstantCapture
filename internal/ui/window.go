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
	"gioui.org/io/event"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
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
	cropHandleScreenTolerance = 12
)

type cropHandle uint8

const (
	cropHandleNone cropHandle = 0
	cropHandleLeft cropHandle = 1 << iota
	cropHandleRight
	cropHandleTop
	cropHandleBottom
)

type overlayResult struct {
	selection nativeoverlay.Selection
	ok        bool
	err       error
}

type captureStartResult struct {
	mode     string
	snapshot workflow.CaptureSnapshot
	err      error
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
	captureModeItems     [4]widget.Clickable
	hotkeyButton         widget.Clickable
	hideMinButton        widget.Clickable
	hideFocusButton      widget.Clickable
	strokeColorButton    widget.Clickable
	strokeWidthButton    widget.Clickable
	paletteButtons       [5]widget.Clickable
	rgbEditor            widget.Editor
	rgbApplyButton       widget.Clickable
	strokeWidthSlider    widget.Float
	highlightWidthSlider widget.Float
	highlightAlphaSlider widget.Float
	rgbInitialized       bool

	canvasTag struct{}
	keyTag    struct{}

	status                 string
	toastMessage           string
	toastUntil             time.Time
	autoCopyPending        bool
	appConfig              config.Config
	configPath             string
	showSettings           bool
	showModeDropdown       bool
	registrar              *hotkey.GlobalRegistrar
	captureHotkey          func()
	trayHidden             bool
	hasFocused             bool
	trayAvailable          bool
	suppressFocusLossUntil time.Time

	overlayActive  bool
	overlayResults chan overlayResult
	captureActive  bool
	captureResults chan captureStartResult
	windowTargets  []platformwindows.WindowTarget

	dragging   bool
	dragStart  editor.Point
	dragNow    editor.Point
	penPoints  []editor.Point
	activeTool editor.Tool

	canvasZoom       float64
	canvasPan        image.Point
	canvasSourceSize image.Point

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
		status:                 fmt.Sprintf("Press %s or Capture Now to start.", cfg.Hotkey),
		appConfig:              cfg,
		configPath:             cfgPath,
		suppressFocusLossUntil: time.Now().Add(2 * time.Second),
		overlayResults:         make(chan overlayResult, 1),
		captureResults:         make(chan captureStartResult, 1),
		activeTool:             editor.ToolRectangle,
	}
	ui.applyStrokeStyle()
	ui.strokeWidthSlider.Value = widthToSlider(ui.appConfig.StrokeWidth)
	ui.highlightWidthSlider.Value = highlighterWidthToSlider(ui.appConfig.HighlighterWidth)
	ui.highlightAlphaSlider.Value = highlighterAlphaToSlider(ui.appConfig.HighlighterAlpha)
	if cfgErr != nil {
		ui.status = "Config load failed; using defaults: " + cfgErr.Error()
	}

	hotkeyEvents := make(chan struct{}, 1)
	focusLossEvents := make(chan struct{}, 1)
	rawTrayEvents := make(chan tray.Event, 4)
	trayEvents := make(chan tray.Event, 4)
	trayIcon, trayErr := tray.Start(rawTrayEvents)
	if trayErr != nil {
		ui.status = "Tray init failed: " + trayErr.Error()
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
	ui.captureHotkey = func() {
		select {
		case hotkeyEvents <- struct{}{}:
		default:
		}
		w.Invalidate()
	}
	binding, _ := hotkey.ParseBinding(ui.appConfig.Hotkey)
	if err := registrar.RegisterCaptureBinding(binding, ui.captureHotkey); err != nil {
		ui.status = "Global hotkey registration failed: " + err.Error()
	} else {
		defer registrar.Close()
	}
	var ops op.Ops
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-hotkeyEvents:
			ui.beginCapture(w)
		case result := <-ui.captureResults:
			ui.handleCaptureStartResult(result, w)
		case <-focusLossEvents:
			if uShouldHideForFocusLoss(ui) {
				ui.hideToTray("Hidden to tray after focus loss.", w)
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
		default:
		}

		e := w.Event()
		switch e := e.(type) {
		case app.DestroyEvent:
			return e.Err
		case app.ConfigEvent:
			ui.handleConfigEvent(e, w)
		case app.FrameEvent:
			gtx := app.NewContext(&ops, e)
			ui.layout(gtx, w)
			e.Frame(gtx.Ops)
		}
	}
}

func newTheme() *material.Theme {
	theme := material.NewTheme()
	theme.Palette.Bg = color.NRGBA{R: 10, G: 15, B: 28, A: 255}
	theme.Palette.Fg = color.NRGBA{R: 235, G: 241, B: 250, A: 255}
	theme.Palette.ContrastBg = color.NRGBA{R: 45, G: 212, B: 191, A: 255}
	theme.Palette.ContrastFg = color.NRGBA{R: 255, G: 255, B: 255, A: 255}
	return theme
}

func NewDefaultController() *workflow.Controller {
	return workflow.NewController(capture.ScreenshotCapturer{})
}

func (u *windowUI) layout(gtx layout.Context, w *app.Window) layout.Dimensions {
	paint.Fill(gtx.Ops, color.NRGBA{R: 8, G: 13, B: 26, A: 255})
	if !u.showSettings && !u.showModeDropdown {
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
	barHeight := gtx.Dp(unit.Dp(60))
	gtx.Constraints.Min.Y = barHeight
	gtx.Constraints.Max.Y = barHeight
	paint.FillShape(gtx.Ops, color.NRGBA{R: 13, G: 20, B: 38, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(12), Left: unit.Dp(14), Right: unit.Dp(14)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return layout.Inset{Right: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					label := material.H6(u.theme, "InstantCapture")
					label.Color = color.NRGBA{R: 245, G: 248, B: 255, A: 255}
					return label.Layout(gtx)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.captureModeButton, "Mode: "+u.appConfig.CaptureMode, true).Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return accentButton(u.theme, &u.newCaptureButton, "Capture Now").Layout(gtx)
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
				return neutralButton(u.theme, &u.copyButton, "Copy").Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return neutralButton(u.theme, &u.saveButton, "Save").Layout(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return neutralButton(u.theme, &u.settingsButton, "Settings").Layout(gtx)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, "Tool: "+string(u.activeTool))
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
	width := gtx.Dp(unit.Dp(360))
	height := gtx.Dp(unit.Dp(76))
	x := gtx.Constraints.Max.X - width - gtx.Dp(unit.Dp(18))
	if x < 0 {
		x = 0
	}
	y := gtx.Dp(unit.Dp(74))
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	rect := image.Rectangle{Max: image.Pt(width, height)}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 15, G: 23, B: 42, A: 245}, clip.RRect{Rect: rect, SE: 12, SW: 12, NW: 12, NE: 12}.Op(gtx.Ops))
	layout.UniformInset(unit.Dp(14)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max = rect.Size().Sub(image.Pt(gtx.Dp(unit.Dp(28)), gtx.Dp(unit.Dp(28))))
		label := material.Body2(u.theme, u.toastMessage)
		label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
		return label.Layout(gtx)
	})
}

func (u *windowUI) showToast(message string) {
	u.toastMessage = message
	u.toastUntil = time.Now().Add(3 * time.Second)
}

func (u *windowUI) layoutModeDropdown(gtx layout.Context) layout.Dimensions {
	paint.FillShape(gtx.Ops, color.NRGBA{R: 11, G: 18, B: 34, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	return layout.Inset{Top: unit.Dp(8), Bottom: unit.Dp(8), Left: unit.Dp(150), Right: unit.Dp(16)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		modes := []string{"Region", "Window", "Freeform", "Fullscreen"}
		children := make([]layout.FlexChild, 0, len(modes))
		for i := range modes {
			idx := i
			children = append(children, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return toggleButton(u.theme, &u.captureModeItems[idx], modes[idx], u.appConfig.CaptureMode == modes[idx]).Layout(gtx)
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
						label := material.Body1(u.theme, "Settings")
						label.Color = color.NRGBA{R: 226, G: 232, B: 240, A: 255}
						return label.Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return toggleButton(u.theme, &u.hotkeyButton, "Hotkey: "+u.appConfig.Hotkey, true).Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return toggleButton(u.theme, &u.hideMinButton, "Minimize to tray: "+onOff(u.appConfig.HideOnMinimize), u.appConfig.HideOnMinimize).Layout(gtx)
					}),
					layout.Rigid(func(gtx layout.Context) layout.Dimensions {
						return toggleButton(u.theme, &u.hideFocusButton, "Focus-loss tray: "+onOff(u.appConfig.HideOnFocusLoss), u.appConfig.HideOnFocusLoss).Layout(gtx)
					}),
				)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutStyleSettings(gtx)
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutHighlighterSettings(gtx)
			}),
		)
	})
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
			label := material.Body2(u.theme, "Border")
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
			return neutralButton(u.theme, &u.rgbApplyButton, "Apply RGB").Layout(gtx)
		}),
	)
}

func (u *windowUI) layoutHighlighterSettings(gtx layout.Context) layout.Dimensions {
	oldWidth := u.appConfig.HighlighterWidth
	oldAlpha := u.appConfig.HighlighterAlpha
	return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, "Highlighter")
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
			label := material.Body2(u.theme, fmt.Sprintf("Width %dpx", u.appConfig.HighlighterWidth))
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
			label := material.Body2(u.theme, fmt.Sprintf("Opacity %d%%", u.appConfig.HighlighterAlpha*100/255))
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
		u.hideToTray("Hidden to tray after minimize.", w)
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
		u.status = "Hide to tray failed: " + err.Error()
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
		return "ON"
	}
	return "OFF"
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
	case "Region", "Window", "Freeform", "Fullscreen":
		return true
	default:
		return false
	}
}

func nextCaptureMode(current string) string {
	modes := []string{"Region", "Window", "Freeform", "Fullscreen"}
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
		rendered, err := u.controller.RenderedImage()
		if err != nil {
			u.status = "Render failed: " + err.Error()
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
					label := material.H5(u.theme, "Ready to capture")
					label.Color = color.NRGBA{R: 240, G: 249, B: 255, A: 255}
					return label.Layout(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(10)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						label := material.Body1(u.theme, "Press the global hotkey or Capture Now, then drag the region to annotate.")
						label.Color = color.NRGBA{R: 148, G: 163, B: 184, A: 255}
						return label.Layout(gtx)
					})
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layout.Inset{Top: unit.Dp(18)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
						return accentButton(u.theme, &u.newCaptureButton, "Capture Now").Layout(gtx)
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
		u.status = "Tool: Pen"
	}
	for u.rectButton.Clicked(gtx) {
		u.activeTool = editor.ToolRectangle
		_ = u.controller.Editor.SetActiveTool(editor.ToolRectangle)
		u.status = "Tool: Rectangle"
	}
	for u.arrowButton.Clicked(gtx) {
		u.activeTool = editor.ToolArrow
		_ = u.controller.Editor.SetActiveTool(editor.ToolArrow)
		u.status = "Tool: Arrow"
	}
	for u.lineButton.Clicked(gtx) {
		u.activeTool = editor.ToolLine
		_ = u.controller.Editor.SetActiveTool(editor.ToolLine)
		u.status = "Tool: Line"
	}
	for u.highlightButton.Clicked(gtx) {
		u.activeTool = editor.ToolHighlight
		_ = u.controller.Editor.SetActiveTool(editor.ToolHighlight)
		u.status = "Tool: Highlighter"
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
		}
	}
	for u.captureModeButton.Clicked(gtx) {
		u.showModeDropdown = !u.showModeDropdown
		if u.showModeDropdown {
			u.showSettings = false
		}
	}
	for i, mode := range []string{"Region", "Window", "Freeform", "Fullscreen"} {
		for u.captureModeItems[i].Clicked(gtx) {
			u.appConfig.CaptureMode = mode
			u.showModeDropdown = false
			u.saveConfigStatus("Capture mode changed: " + mode)
		}
	}
	for u.hotkeyButton.Clicked(gtx) {
		u.cycleHotkey()
	}
	for u.hideMinButton.Clicked(gtx) {
		u.appConfig.HideOnMinimize = !u.appConfig.HideOnMinimize
		u.saveConfigStatus("Minimize-to-tray changed: " + onOff(u.appConfig.HideOnMinimize))
	}
	for u.hideFocusButton.Clicked(gtx) {
		u.appConfig.HideOnFocusLoss = !u.appConfig.HideOnFocusLoss
		u.saveConfigStatus("Focus-loss tray changed: " + onOff(u.appConfig.HideOnFocusLoss))
	}
	for i, preset := range strokePresets() {
		for u.paletteButtons[i].Clicked(gtx) {
			u.setStrokeColor(preset.name)
			u.saveConfigStatus("Border color changed: " + preset.name)
		}
	}
	for u.rgbApplyButton.Clicked(gtx) {
		if rgb, ok := parseRGBText(u.rgbEditor.Text()); ok {
			u.setStrokeColor(rgbName(rgb))
			u.saveConfigStatus("Border RGB changed: " + u.appConfig.StrokeColor)
		} else {
			u.status = "RGB format must be R,G,B such as 255,64,64."
		}
	}
}

func (u *windowUI) handleShortcuts(gtx layout.Context) {
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Name: key.NameEscape})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			u.dragging = false
			u.cropDragging = false
			u.status = "Current gesture canceled."
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Required: key.ModCtrl, Name: key.Name("S")})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			u.saveCurrentImage()
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Required: key.ModCtrl, Name: key.Name("C")})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			u.copyCurrentImage()
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Required: key.ModCtrl, Name: key.Name("Z")})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			u.undoAnnotation()
		}
	}
	for {
		ev, ok := gtx.Event(key.Filter{Focus: &u.keyTag, Required: key.ModCtrl, Name: key.Name("Y")})
		if !ok {
			break
		}
		ke, ok := ev.(key.Event)
		if ok && ke.State == key.Press {
			u.redoAnnotation()
		}
	}
}

func (u *windowUI) saveCurrentImage() {
	dir := defaultSaveDir()
	path, err := u.controller.SavePNG(dir)
	if err != nil {
		u.status = "Save failed: " + err.Error()
		u.showToast(u.status)
	} else {
		u.status = "Saved: " + path
		u.showToast("Saved PNG")
	}
}

func defaultSaveDir() string {
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		screenshots := filepath.Join(home, "Pictures", "Screenshots")
		if err := os.MkdirAll(screenshots, 0o755); err == nil {
			return screenshots
		}
		pictures := filepath.Join(home, "Pictures")
		if err := os.MkdirAll(pictures, 0o755); err == nil {
			return pictures
		}
	}
	wd, err := os.Getwd()
	if err == nil {
		return wd
	}
	return "."
}

func (u *windowUI) copyCurrentImage() {
	if err := u.controller.CopyPNG(); err != nil {
		u.status = "Copy failed: " + err.Error()
		u.showToast(u.status)
	} else {
		u.status = "Copied PNG to clipboard."
		u.showToast("Copied PNG to clipboard")
	}
}

func (u *windowUI) undoAnnotation() {
	if u.controller.UndoAnnotation() {
		u.status = "Undo annotation."
		u.showToast("Undo")
	} else {
		u.showToast("Nothing to undo")
	}
}

func (u *windowUI) redoAnnotation() {
	if u.controller.RedoAnnotation() {
		u.status = "Redo annotation."
		u.showToast("Redo")
	} else {
		u.showToast("Nothing to redo")
	}
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
	u.status = fmt.Sprintf("Zoom: %.0f%%", u.canvasZoom*100)
}

func (u *windowUI) addCanvasCursor(ops *op.Ops) {
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

func (u *windowUI) hitCropHandle(point editor.Point, bounds image.Rectangle, scale float64) cropHandle {
	if u.controller.Mode != workflow.ModeEditing || scale <= 0 || bounds.Empty() {
		return cropHandleNone
	}
	tolerance := max(2, int(math.Ceil(cropHandleScreenTolerance/scale)))
	x, y := point.X, point.Y
	nearLeft := absInt(x-bounds.Min.X) <= tolerance
	nearRight := absInt(x-bounds.Max.X) <= tolerance
	nearTop := absInt(y-bounds.Min.Y) <= tolerance
	nearBottom := absInt(y-bounds.Max.Y) <= tolerance
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
		u.status = "Crop canceled."
		return
	}
	if err := u.controller.CropCapturedImage(u.cropPreviewRect); err != nil {
		u.status = "Crop failed: " + err.Error()
		u.showToast(u.status)
		return
	}
	croppedSize := u.cropPreviewRect.Size()
	u.resetCanvasView()
	u.status = fmt.Sprintf("Cropped to %dx%d.", croppedSize.X, croppedSize.Y)
	u.showToast("Image cropped")
}

func (u *windowUI) resetCanvasView() {
	u.canvasZoom = 1
	u.canvasPan = image.Point{}
	u.canvasSourceSize = image.Point{}
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
	rect := sourceRect
	if u.cropDragging && !u.cropPreviewRect.Empty() {
		rect = u.cropPreviewRect
	}
	width := float32(max(1, int(math.Round(2/scale))))
	drawStrokeRect(ops, rectToEditorRect(rect), color.NRGBA{R: 56, G: 189, B: 248, A: 230}, width)

	handleSize := max(4, int(math.Round(8/scale)))
	for _, center := range cropHandleCenters(rect) {
		drawHandleSquare(ops, center, handleSize)
	}
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
	if err := u.registrar.RebindCapture(binding, u.captureHotkey); err != nil {
		u.status = "Hotkey change failed: " + err.Error()
		return
	}
	u.appConfig.Hotkey = binding.Name
	u.saveConfigStatus("Hotkey changed: " + binding.Name)
}

func (u *windowUI) saveConfigStatus(success string) {
	if err := config.Save(u.appConfig); err != nil {
		u.status = success + " (config save failed: " + err.Error() + ")"
		return
	}
	u.status = success
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

		switch pe.Kind {
		case pointer.Scroll:
			if pe.Modifiers.Contain(key.ModCtrl) {
				u.zoomCanvas(pe.Scroll.Y, point, canvasBounds, viewport)
				w.Invalidate()
			}
		case pointer.Move:
			u.cropHoverHandle = u.hitCropHandle(point, bounds, viewport.Scale)
			w.Invalidate()
		case pointer.Press:
			if u.controller.Mode == workflow.ModeEditing {
				handle := u.hitCropHandle(point, bounds, viewport.Scale)
				if handle != cropHandleNone {
					u.cropDragging = true
					u.cropDragHandle = handle
					u.cropStartRect = bounds
					u.cropPreviewRect = bounds
					u.dragging = false
					u.status = "Drag image border to crop."
					w.Invalidate()
					continue
				}
			}
			u.dragging = true
			u.dragStart = point
			u.dragNow = point
			u.penPoints = []editor.Point{point}
		case pointer.Drag:
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
		}
	}
}

func (u *windowUI) beginCapture(w *app.Window) {
	if u.overlayActive || u.captureActive {
		u.status = "Capture is already in progress."
		return
	}

	u.dragging = false
	u.resetCanvasView()
	u.autoCopyPending = false
	u.captureActive = true
	u.suppressFocusLossUntil = time.Now().Add(10 * time.Second)
	u.status = "Capturing screen..."
	mode := u.appConfig.CaptureMode
	go func() {
		done := make(chan captureStartResult, 1)
		go func() {
			snapshot, err := u.captureSnapshotExternal()
			done <- captureStartResult{mode: mode, snapshot: snapshot, err: err}
		}()
		select {
		case result := <-done:
			u.captureResults <- result
		case <-time.After(8 * time.Second):
			u.captureResults <- captureStartResult{mode: mode, err: fmt.Errorf("screen capture timed out")}
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
		u.status = "Capture failed: " + result.err.Error()
		return
	}
	if err := u.controller.BeginCaptureFromSnapshot(result.snapshot); err != nil {
		u.status = "Capture failed: " + err.Error()
		return
	}

	mode := result.mode
	switch mode {
	case "Fullscreen":
		monitor := currentMonitorRect(u.controller.VirtualScreen, result.snapshot.Displays)
		if err := u.controller.CommitScreenSelection(monitor); err != nil {
			u.status = "Fullscreen capture failed: " + err.Error()
			return
		}
		u.resetCanvasView()
		u.restoreFromTray(w)
		u.autoCopyPending = true
		u.status = fmt.Sprintf("Editing monitor %dx%d capture.", u.controller.CapturedImage.Bounds().Dx(), u.controller.CapturedImage.Bounds().Dy())
		return
	case "Freeform":
		u.status = "Freeform mode: drag around the target area."
	case "Window":
		u.status = "Window mode: hover a window and click it."
	default:
		u.status = fmt.Sprintf("Drag a region in the overlay. Virtual screen: %v", u.controller.VirtualScreen)
	}

	u.overlayActive = true

	img := u.controller.FullScreenImage
	virtualScreen := u.controller.VirtualScreen
	var windowRects []image.Rectangle
	if mode == "Window" {
		windowTargets := platformwindows.VisibleWindowTargets(windowTitle)
		u.windowTargets = windowTargets
		windowRects = make([]image.Rectangle, 0, len(windowTargets))
		for _, target := range windowTargets {
			windowRects = append(windowRects, target.Rect)
		}
	} else {
		u.windowTargets = nil
	}
	go func() {
		selection, ok, err := nativeoverlay.SelectDetailed(u.ctx, img, overlay.Options{
			VirtualScreen: virtualScreen,
			TopMostHint:   true,
			DimAlpha:      80,
			CaptureMode:   mode,
			WindowRects:   windowRects,
		})
		u.overlayResults <- overlayResult{selection: selection, ok: ok, err: err}
		w.Invalidate()
	}()

	w.Invalidate()
}

func (u *windowUI) handleOverlayResult(result overlayResult, w *app.Window) {
	u.overlayActive = false
	u.dragging = false
	if result.err != nil {
		u.status = "Overlay error: " + result.err.Error()
		return
	}
	if !result.ok {
		u.status = "Selection canceled. You can start capture again anytime."
		return
	}
	var err error
	if u.appConfig.CaptureMode == "Window" {
		err = u.controller.CommitImageSelection(result.selection.Rect)
	} else if len(result.selection.Polygon) >= 3 {
		err = u.controller.CommitFreeformSelection(result.selection.Rect, result.selection.Polygon)
	} else {
		err = u.controller.CommitImageSelection(result.selection.Rect)
	}
	if err != nil {
		u.status = "Selection failed: " + err.Error()
		return
	}
	u.resetCanvasView()
	u.restoreFromTray(w)
	u.autoCopyPending = true
	u.status = fmt.Sprintf("Editing %dx%d capture. Annotate, then Copy or Save.", u.controller.CapturedImage.Bounds().Dx(), u.controller.CapturedImage.Bounds().Dy())
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
			u.status = "Selection failed: " + err.Error()
			return
		}
		u.autoCopyPending = true
		u.status = fmt.Sprintf("Editing %dx%d capture. Annotate, then Copy or Save.", u.controller.CapturedImage.Bounds().Dx(), u.controller.CapturedImage.Bounds().Dy())
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
			u.status = "Annotation failed: " + err.Error()
		} else if u.autoCopyPending {
			u.autoCopyPending = false
			if err := u.controller.CopyPNG(); err != nil {
				u.status = fmt.Sprintf("%s annotation added. Auto-copy failed: %s", u.activeTool, err.Error())
				u.showToast("Auto-copy failed")
			} else {
				u.status = fmt.Sprintf("%s annotation added and copied to clipboard.", u.activeTool)
				u.showToast("Copied after first annotation")
			}
		} else {
			u.status = fmt.Sprintf("%s annotation added.", u.activeTool)
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
