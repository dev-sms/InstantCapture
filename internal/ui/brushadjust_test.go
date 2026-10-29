package ui

import (
	"image"
	"testing"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"

	"instantcapture/internal/config"
	"instantcapture/internal/editor"
	"instantcapture/internal/workflow"
)

func newBrushAdjustTestUI(t *testing.T, tool editor.Tool, saves *int) *windowUI {
	t.Helper()
	c := workflow.NewController(nil)
	if err := c.CommitImportedImage(image.NewRGBA(image.Rect(0, 0, 200, 100))); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.StrokeWidth = 4
	cfg.HighlighterWidth = 12
	cfg.HighlighterAlpha = 120
	cfg.MosaicWidth = 30
	cfg.MosaicBlockSize = 10
	u := &windowUI{controller: c, appConfig: cfg, activeTool: tool, saveConfig: func(config.Config) error {
		*saves++
		return nil
	}}
	u.strokeWidthSlider.Value = widthToSlider(cfg.StrokeWidth)
	u.highlightWidthSlider.Value = highlighterWidthToSlider(cfg.HighlighterWidth)
	u.highlightAlphaSlider.Value = highlighterAlphaToSlider(cfg.HighlighterAlpha)
	u.mosaicWidthSlider.Value = mosaicWidthToSlider(cfg.MosaicWidth)
	u.mosaicBlockSizeSlider.Value = mosaicBlockSizeToSlider(cfg.MosaicBlockSize)
	u.applyStrokeStyle()
	return u
}

func altRightPress(at f32.Point) pointer.Event {
	return pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonSecondary, Modifiers: key.ModAlt, Position: at}
}

func TestBrushAdjustHighlighterWidthAndOpacity(t *testing.T) {
	saves := 0
	u := newBrushAdjustTestUI(t, editor.ToolHighlight, &saves)
	anchor := editor.Point{X: 50, Y: 40}
	if !u.handleBrushAdjustPointer(altRightPress(f32.Pt(100, 100)), 200, anchor, true) {
		t.Fatal("Alt+right press should start brush adjustment")
	}
	if u.dragging {
		t.Fatal("brush adjustment must not start a stroke")
	}

	u.handleBrushAdjustPointer(pointer.Event{Kind: pointer.Drag, Position: f32.Pt(200, 0)}, 200, editor.Point{}, true)
	if u.appConfig.HighlighterWidth <= 12 || u.appConfig.HighlighterAlpha <= 120 {
		t.Fatalf("right+up should grow width and opacity, got width %d alpha %d", u.appConfig.HighlighterWidth, u.appConfig.HighlighterAlpha)
	}
	if u.canvasPointer != anchor {
		t.Fatalf("brush preview should stay at press point, got %v", u.canvasPointer)
	}
	if u.controller.HighlighterStyle.Width != float64(u.appConfig.HighlighterWidth) {
		t.Fatal("highlighter style should follow the adjusted width")
	}

	u.handleBrushAdjustPointer(pointer.Event{Kind: pointer.Release, Position: f32.Pt(0, 200)}, 200, editor.Point{}, true)
	if u.appConfig.HighlighterWidth >= 12 || u.appConfig.HighlighterAlpha >= 120 {
		t.Fatalf("left+down should shrink width and opacity, got width %d alpha %d", u.appConfig.HighlighterWidth, u.appConfig.HighlighterAlpha)
	}
	if u.brushAdjust.Active || saves != 1 {
		t.Fatalf("release should end adjustment and save once, active=%v saves=%d", u.brushAdjust.Active, saves)
	}
}

func TestBrushAdjustMosaicVerticalChangesBlockSize(t *testing.T) {
	saves := 0
	u := newBrushAdjustTestUI(t, editor.ToolMosaic, &saves)
	u.handleBrushAdjustPointer(altRightPress(f32.Pt(100, 100)), 200, editor.Point{}, true)
	u.handleBrushAdjustPointer(pointer.Event{Kind: pointer.Drag, Position: f32.Pt(100, 0)}, 200, editor.Point{}, true)
	if u.appConfig.MosaicWidth != 30 {
		t.Fatalf("vertical drag should keep width, got %d", u.appConfig.MosaicWidth)
	}
	if u.appConfig.MosaicBlockSize <= 10 || u.controller.MosaicBlockSize != u.appConfig.MosaicBlockSize {
		t.Fatalf("upward drag should grow block size, got %d", u.appConfig.MosaicBlockSize)
	}
}

func TestBrushAdjustPenOnlyChangesWidthAndCancelRestores(t *testing.T) {
	saves := 0
	u := newBrushAdjustTestUI(t, editor.ToolPen, &saves)
	u.handleBrushAdjustPointer(altRightPress(f32.Pt(100, 100)), 200, editor.Point{}, true)
	u.handleBrushAdjustPointer(pointer.Event{Kind: pointer.Drag, Position: f32.Pt(300, -300)}, 200, editor.Point{}, true)
	if u.appConfig.StrokeWidth != sliderToWidth(1) {
		t.Fatalf("pen width = %d, want max %d", u.appConfig.StrokeWidth, sliderToWidth(1))
	}
	if u.appConfig.HighlighterAlpha != 120 || u.appConfig.MosaicBlockSize != 10 {
		t.Fatal("pen adjustment must not touch other tool settings")
	}
	u.handleBrushAdjustPointer(pointer.Event{Kind: pointer.Cancel}, 200, editor.Point{}, true)
	if u.appConfig.StrokeWidth != 4 || u.brushAdjust.Active || saves != 0 {
		t.Fatalf("cancel should restore width 4 without saving, got %d active=%v saves=%d", u.appConfig.StrokeWidth, u.brushAdjust.Active, saves)
	}
}

func TestBrushAdjustRequiresAltAndRightButton(t *testing.T) {
	saves := 0
	u := newBrushAdjustTestUI(t, editor.ToolPen, &saves)
	plainRight := pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonSecondary, Position: f32.Pt(1, 1)}
	altLeft := pointer.Event{Kind: pointer.Press, Buttons: pointer.ButtonPrimary, Modifiers: key.ModAlt, Position: f32.Pt(1, 1)}
	if u.handleBrushAdjustPointer(plainRight, 200, editor.Point{}, true) || u.handleBrushAdjustPointer(altLeft, 200, editor.Point{}, true) {
		t.Fatal("only Alt+right press should start brush adjustment")
	}
	u.activeTool = editor.ToolText
	if u.handleBrushAdjustPointer(altRightPress(f32.Pt(1, 1)), 200, editor.Point{}, true) {
		t.Fatal("text tool has no brush to adjust")
	}
}
