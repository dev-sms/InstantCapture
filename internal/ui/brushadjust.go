package ui

import (
	"fmt"

	"gioui.org/f32"
	"gioui.org/io/key"
	"gioui.org/io/pointer"
	"gioui.org/widget"

	"instantcapture/internal/editor"
	"instantcapture/internal/workflow"
)

// Dragging this far (in pixels) sweeps a brush setting across its whole range.
const brushAdjustRangeDp = 200

type brushAdjustState struct {
	Active      bool
	Tool        editor.Tool
	Start       f32.Point
	StartH      float32
	StartV      float32
	RangePixels float32
}

// brushAdjustSliders returns the setting sliders driven by horizontal and
// vertical Alt+right drags; vertical is nil when the tool has no second value.
func (u *windowUI) brushAdjustSliders() (horizontal, vertical *widget.Float) {
	switch u.activeTool {
	case editor.ToolPen, editor.ToolLine, editor.ToolRectangle, editor.ToolTriangle, editor.ToolCircle, editor.ToolArrow:
		return &u.strokeWidthSlider, nil
	case editor.ToolHighlight:
		return &u.highlightWidthSlider, &u.highlightAlphaSlider
	case editor.ToolMosaic:
		return &u.mosaicWidthSlider, &u.mosaicBlockSizeSlider
	default:
		return nil, nil
	}
}

// handleBrushAdjustPointer runs Photoshop-style Alt+right-drag brush tuning:
// left/right changes the width, up/down the tool's second setting.
func (u *windowUI) handleBrushAdjustPointer(pe pointer.Event, rangePixels float32, anchor editor.Point, insideImage bool) bool {
	if !u.brushAdjust.Active {
		if pe.Kind != pointer.Press || !pe.Buttons.Contain(pointer.ButtonSecondary) || !pe.Modifiers.Contain(key.ModAlt) {
			return false
		}
		if u.controller == nil || u.controller.Mode != workflow.ModeEditing {
			return false
		}
		horizontal, vertical := u.brushAdjustSliders()
		if horizontal == nil {
			return false
		}
		if rangePixels <= 0 {
			rangePixels = brushAdjustRangeDp
		}
		u.brushAdjust = brushAdjustState{Active: true, Tool: u.activeTool, Start: pe.Position, StartH: horizontal.Value, RangePixels: rangePixels}
		if vertical != nil {
			u.brushAdjust.StartV = vertical.Value
		}
		u.dragging = false
		u.cropDragging = false
		u.cropHoverHandle = cropHandleNone
		u.canvasPointer = anchor
		u.canvasPointerOver = true
		u.canvasPointerIn = insideImage
		u.status = u.brushAdjustStatus()
		return true
	}

	if u.activeTool != u.brushAdjust.Tool {
		u.brushAdjust = brushAdjustState{}
		u.persistConfig()
		return true
	}
	switch pe.Kind {
	case pointer.Drag:
		u.updateBrushAdjust(pe.Position)
	case pointer.Release:
		u.updateBrushAdjust(pe.Position)
		u.brushAdjust = brushAdjustState{}
		u.persistConfig()
	case pointer.Cancel:
		u.cancelBrushAdjust()
	}
	return true
}

func (u *windowUI) updateBrushAdjust(position f32.Point) {
	horizontal, vertical := u.brushAdjustSliders()
	if horizontal == nil {
		return
	}
	delta := position.Sub(u.brushAdjust.Start)
	horizontal.Value = clampUnit(u.brushAdjust.StartH + delta.X/u.brushAdjust.RangePixels)
	if vertical != nil {
		vertical.Value = clampUnit(u.brushAdjust.StartV - delta.Y/u.brushAdjust.RangePixels)
	}
	u.applyBrushSliders()
	u.clearMosaicHoverCache()
	u.status = u.brushAdjustStatus()
}

func (u *windowUI) cancelBrushAdjust() {
	if !u.brushAdjust.Active {
		return
	}
	horizontal, vertical := u.brushAdjustSliders()
	if horizontal != nil {
		horizontal.Value = u.brushAdjust.StartH
	}
	if vertical != nil {
		vertical.Value = u.brushAdjust.StartV
	}
	u.brushAdjust = brushAdjustState{}
	u.applyBrushSliders()
	u.clearMosaicHoverCache()
	u.status = "브러시 조절을 취소했습니다."
}

func (u *windowUI) applyBrushSliders() {
	switch u.activeTool {
	case editor.ToolHighlight:
		u.appConfig.HighlighterWidth = highlighterSliderToWidth(u.highlightWidthSlider.Value)
		u.appConfig.HighlighterAlpha = highlighterSliderToAlpha(u.highlightAlphaSlider.Value)
	case editor.ToolMosaic:
		u.appConfig.MosaicWidth = mosaicSliderToWidth(u.mosaicWidthSlider.Value)
		u.appConfig.MosaicBlockSize = mosaicSliderToBlockSize(u.mosaicBlockSizeSlider.Value)
	default:
		u.appConfig.StrokeWidth = sliderToWidth(u.strokeWidthSlider.Value)
	}
	u.applyStrokeStyle()
}

func (u *windowUI) brushAdjustStatus() string {
	switch u.activeTool {
	case editor.ToolHighlight:
		return fmt.Sprintf("형광펜 굵기 %dpx · 불투명도 %d%%  (좌우: 굵기, 위아래: 불투명도)", u.appConfig.HighlighterWidth, u.appConfig.HighlighterAlpha*100/255)
	case editor.ToolMosaic:
		return fmt.Sprintf("모자이크 굵기 %dpx · 블록 %dpx  (좌우: 굵기, 위아래: 블록 크기)", u.appConfig.MosaicWidth, u.appConfig.MosaicBlockSize)
	default:
		return fmt.Sprintf("굵기 %dpx  (좌우로 조절)", u.appConfig.StrokeWidth)
	}
}

func clampUnit(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}
