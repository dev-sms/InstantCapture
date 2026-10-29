package ui

import (
	"fmt"
	"image"
	"image/color"
	"math"

	"gioui.org/f32"
	"gioui.org/io/pointer"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget/material"

	"instantcapture/internal/coords"
	"instantcapture/internal/workflow"
)

type rotationGesture struct {
	Active  bool
	Hover   bool
	ID      pointer.ID
	Center  f32.Point
	Start   float64
	Degrees int
}

func rotationHandleRect(viewport coords.Viewport, crop image.Rectangle) image.Rectangle {
	top := viewport.SourceToView(image.Pt((crop.Min.X+crop.Max.X)/2, crop.Min.Y))
	// Keep a separate target when the image touches the top of the canvas.
	y := top.Y - 24
	if y < 10 {
		y = top.Y + 18
	}
	return image.Rect(top.X-9, y-9, top.X+9, y+9)
}

func rotationAngle(center, point f32.Point) float64 {
	return math.Atan2(float64(point.Y-center.Y), float64(point.X-center.X))
}

func rotationDelta(start float64, center, point f32.Point) int {
	degrees := int(math.Round((rotationAngle(center, point) - start) * 180 / math.Pi))
	return (degrees%360+540)%360 - 180
}

func (u *windowUI) cancelRotation() {
	u.rotation = rotationGesture{}
}

func (u *windowUI) handleRotationPointer(gtx layout.Context, pe pointer.Event, viewport coords.Viewport) bool {
	if u.rotation.Active {
		if pe.Kind == pointer.Cancel || (pe.Kind == pointer.Press && pe.Buttons != pointer.ButtonPrimary) {
			u.cancelRotation()
			u.status = "회전을 취소했습니다."
			return true
		}
		if pe.PointerID != u.rotation.ID {
			return true
		}
		switch pe.Kind {
		case pointer.Drag, pointer.Release:
			u.rotation.Degrees = rotationDelta(u.rotation.Start, u.rotation.Center, pe.Position)
			u.status = fmt.Sprintf("회전 %d° · 놓아서 적용 · Esc 취소", u.rotation.Degrees)
			if pe.Kind == pointer.Release {
				degrees := u.rotation.Degrees
				u.cancelRotation()
				u.applyImageTransform(workflow.ImageTransform{Degrees: degrees})
			}
		}
		return true
	}
	if u.controller.Mode != workflow.ModeEditing || u.textEditing || u.dragging || u.cropDragging || u.canvasPanning {
		u.rotation.Hover = false
		return false
	}
	hit := image.Pt(int(pe.Position.X), int(pe.Position.Y)).In(rotationHandleRect(viewport, u.controller.VisibleCropRect()))
	switch pe.Kind {
	case pointer.Enter, pointer.Move:
		u.rotation.Hover = hit
		if hit {
			u.cropHoverHandle = cropHandleNone
			u.clearMosaicHoverCache()
			u.status = "회전 핸들 · 드래그로 1°씩 회전 · 현재 크롭과 주석을 합칩니다"
		}
		return hit
	case pointer.Leave, pointer.Cancel:
		u.rotation.Hover = false
	case pointer.Press:
		if hit && pe.Buttons == pointer.ButtonPrimary {
			crop := u.controller.VisibleCropRect().Sub(viewport.Source.Min)
			p := f32.Pt(
				float32(float64(viewport.View.Min.X)+float64(crop.Min.X+crop.Max.X)*viewport.Scale/2),
				float32(float64(viewport.View.Min.Y)+float64(crop.Min.Y+crop.Max.Y)*viewport.Scale/2),
			)
			u.rotation = rotationGesture{Active: true, Hover: true, ID: pe.PointerID, Center: p, Start: rotationAngle(p, pe.Position)}
			u.cropHoverHandle = cropHandleNone
			u.clearMosaicHoverCache()
			gtx.Execute(pointer.GrabCmd{Tag: &u.canvasTag, ID: pe.PointerID})
			return true
		}
	}
	return false
}

func (u *windowUI) drawRotationHandle(gtx layout.Context, viewport coords.Viewport) {
	if u.controller.Mode != workflow.ModeEditing || u.textEditing || u.cropDragging {
		return
	}
	rect := rotationHandleRect(viewport, u.controller.VisibleCropRect())
	c := color.NRGBA{R: 125, G: 211, B: 252, A: 255}
	if u.rotation.Hover {
		c = color.NRGBA{R: 255, G: 210, B: 80, A: 255}
	}
	paint.FillShape(gtx.Ops, color.NRGBA{R: 15, G: 23, B: 42, A: 255}, clip.Ellipse(rect).Op(gtx.Ops))
	defer op.Offset(rect.Min).Push(gtx.Ops).Pop()
	gtx.Constraints = layout.Exact(rect.Size())
	label := material.Body2(u.theme, "↻")
	label.Color = c
	label.TextSize = unit.Sp(16)
	layout.Center.Layout(gtx, label.Layout)
}

func (u *windowUI) drawRotationPreview(gtx layout.Context, img image.Image, viewport coords.Viewport) {
	crop := u.controller.VisibleCropRect()
	offset := op.Offset(viewport.View.Min).Push(gtx.Ops)
	scale := float32(viewport.Scale)
	scaling := op.Affine(f32.AffineId().Scale(f32.Point{}, f32.Pt(scale, scale))).Push(gtx.Ops)
	center := f32.Pt(float32(crop.Min.X+crop.Max.X)/2, float32(crop.Min.Y+crop.Max.Y)/2)
	rotation := op.Affine(f32.AffineId().Rotate(center, float32(u.rotation.Degrees)*math.Pi/180)).Push(gtx.Ops)
	cropping := clip.Rect(crop).Push(gtx.Ops)
	u.canvasImageOperation(img).Add(gtx.Ops)
	paint.PaintOp{}.Add(gtx.Ops)
	cropping.Pop()
	rotation.Pop()
	scaling.Pop()
	offset.Pop()
}

func (u *windowUI) layoutTransformControls(gtx layout.Context) layout.Dimensions {
	if u.controller.Mode != workflow.ModeEditing {
		return layout.Dimensions{}
	}
	return layout.UniformInset(unit.Dp(6)).Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return enabledWidget(gtx, !u.rotation.Active, func(gtx layout.Context) layout.Dimensions {
					direction := "시계 방향"
					if u.transformCounterclockwise {
						direction = "반시계 방향"
					}
					labels := []string{direction, "90°", "180°", "270°", "좌우 반전", "상하 반전"}
					children := make([]layout.FlexChild, len(labels))
					for i, label := range labels {
						children[i] = layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return neutralButton(u.theme, &u.transformButtons[i], label).Layout(gtx)
						})
					}
					return layout.Flex{Gap: gtx.Dp(unit.Dp(5)), Alignment: layout.Middle}.Layout(gtx, children...)
				})
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				label := material.Body2(u.theme, "회전·반전: 현재 크롭과 주석을 합칩니다. 외곽선은 출력 때 적용 · Ctrl+Z로 복원")
				label.TextSize = unit.Sp(11)
				return label.Layout(gtx)
			}),
		)
	})
}

func (u *windowUI) handleTransformButtons(gtx layout.Context) {
	for i := range u.transformButtons {
		for u.transformButtons[i].Clicked(gtx) {
			if u.rotation.Active || u.controller.Mode != workflow.ModeEditing {
				continue
			}
			if i == 0 {
				u.transformCounterclockwise = !u.transformCounterclockwise
				continue
			}
			t := workflow.ImageTransform{}
			if i <= 3 {
				t.Degrees = i * 90
				if u.transformCounterclockwise {
					t.Degrees = -t.Degrees
				}
			} else {
				t.FlipHorizontal = i == 4
				t.FlipVertical = i == 5
			}
			u.applyImageTransform(t)
		}
	}
}

func (u *windowUI) applyImageTransform(t workflow.ImageTransform) {
	if t.IsIdentity() || !u.commitTextEditing() {
		return
	}
	before := u.captureCanvasView()
	if err := u.controller.TransformImage(t); err != nil {
		u.status = "회전·반전 실패: " + err.Error()
		u.showToast(u.status)
		return
	}
	u.dragging = false
	u.resetCanvasView()
	u.releaseTransientImageCaches()
	u.recordControllerEdit(before, u.captureCanvasView())
	u.status = "회전·반전을 적용했습니다. 주석과 크롭은 Ctrl+Z로 복원할 수 있습니다."
	if u.autoCopyPending {
		u.autoCopyPending = false
		u.copyCurrentImage()
	}
}
