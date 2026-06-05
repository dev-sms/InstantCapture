package exporter

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	"image/png"
	"math"
	"os"

	"github.com/fogleman/gg"
	"golang.design/x/clipboard"

	"instantcapture/internal/editor"
)

func RenderAnnotations(base image.Image, commands []editor.RenderCommand) (*image.RGBA, error) {
	if base == nil {
		return nil, fmt.Errorf("base image is nil")
	}

	bounds := base.Bounds()
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), base, bounds.Min, draw.Src)

	ctx := gg.NewContextForRGBA(canvas)
	for _, command := range commands {
		passes := 1
		if command.Tool != editor.ToolHighlight && command.Style.ColorRGBA[3] >= 255 {
			passes = 2
		}
		for range passes {
			applyStyle(ctx, command.Style)
			if err := drawCommand(ctx, command); err != nil {
				return nil, err
			}
		}
	}

	return canvas, nil
}

func drawCommand(ctx *gg.Context, command editor.RenderCommand) error {
	switch command.Tool {
	case editor.ToolPen, editor.ToolHighlight:
		drawPen(ctx, command.Points)
	case editor.ToolRectangle:
		r := command.Rect.Normalize()
		ctx.DrawRectangle(float64(r.Min.X), float64(r.Min.Y), float64(r.Width()), float64(r.Height()))
		ctx.Stroke()
	case editor.ToolArrow:
		drawArrow(ctx, command.Rect)
	case editor.ToolLine:
		drawLine(ctx, command.Rect)
	default:
		return fmt.Errorf("unsupported render tool %q", command.Tool)
	}
	return nil
}

func CopyPNG(img image.Image) error {
	data, err := EncodePNG(img)
	if err != nil {
		return err
	}

	if err := clipboard.Init(); err != nil {
		return fmt.Errorf("initialize clipboard: %w", err)
	}
	clipboard.Write(clipboard.FmtImage, data)
	return nil
}

func SavePNG(path string, img image.Image) error {
	data, err := EncodePNG(img)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func EncodePNG(img image.Image) ([]byte, error) {
	if img == nil {
		return nil, fmt.Errorf("image is nil")
	}

	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("encode png: %w", err)
	}
	return buf.Bytes(), nil
}

func applyStyle(ctx *gg.Context, style editor.StrokeStyle) {
	if style.Width <= 0 {
		style = editor.DefaultStrokeStyle()
	}
	color := style.ColorRGBA
	ctx.SetRGBA255(int(color[0]), int(color[1]), int(color[2]), int(color[3]))
	ctx.SetLineWidth(style.Width)
	ctx.SetLineCapRound()
	ctx.SetLineJoinRound()
}

func drawPen(ctx *gg.Context, points []editor.Point) {
	if len(points) < 2 {
		return
	}
	ctx.MoveTo(float64(points[0].X), float64(points[0].Y))
	for _, point := range points[1:] {
		ctx.LineTo(float64(point.X), float64(point.Y))
	}
	ctx.Stroke()
}

func drawLine(ctx *gg.Context, rect editor.Rect) {
	ctx.DrawLine(float64(rect.Min.X), float64(rect.Min.Y), float64(rect.Max.X), float64(rect.Max.Y))
	ctx.Stroke()
}

func drawArrow(ctx *gg.Context, rect editor.Rect) {
	startX := float64(rect.Min.X)
	startY := float64(rect.Min.Y)
	endX := float64(rect.Max.X)
	endY := float64(rect.Max.Y)

	ctx.DrawLine(startX, startY, endX, endY)
	ctx.Stroke()

	angle := math.Atan2(endY-startY, endX-startX)
	headLen := 16.0
	spread := math.Pi / 7
	for _, sign := range []float64{-1, 1} {
		x := endX - headLen*math.Cos(angle+sign*spread)
		y := endY - headLen*math.Sin(angle+sign*spread)
		ctx.DrawLine(endX, endY, x, y)
		ctx.Stroke()
	}
}
