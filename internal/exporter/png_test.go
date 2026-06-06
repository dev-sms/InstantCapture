package exporter

import (
	"image"
	"image/color"
	"testing"

	"instantcapture/internal/editor"
)

func TestRenderAnnotations(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 100, 80))
	commands := []editor.RenderCommand{
		{
			Tool:  editor.ToolRectangle,
			Rect:  editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 50, Y: 40}),
			Style: editor.StrokeStyle{ColorRGBA: [4]uint8{255, 0, 0, 255}, Width: 2},
		},
	}

	img, err := RenderAnnotations(base, commands)
	if err != nil {
		t.Fatal(err)
	}

	if img.Bounds() != base.Bounds() {
		t.Fatalf("unexpected bounds: %v", img.Bounds())
	}

	if img.At(10, 10) == (color.RGBA{}) {
		t.Fatal("expected annotation to draw onto image")
	}
}

func TestRenderAnnotationsCroppedOffsetsBaseAndCommands(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 100, 80))
	base.Set(20, 10, color.NRGBA{R: 10, G: 20, B: 30, A: 255})
	commands := []editor.RenderCommand{
		{
			Tool:  editor.ToolRectangle,
			Rect:  editor.NewRect(editor.Point{X: 25, Y: 15}, editor.Point{X: 45, Y: 30}),
			Style: editor.StrokeStyle{ColorRGBA: [4]uint8{255, 0, 0, 255}, Width: 2},
		},
	}

	img, err := RenderAnnotationsCropped(base, commands, image.Rect(20, 10, 70, 50))
	if err != nil {
		t.Fatal(err)
	}
	if got := img.Bounds(); got != image.Rect(0, 0, 50, 40) {
		t.Fatalf("unexpected bounds: %v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("unexpected copied pixel: %#v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(5, 5)).(color.NRGBA); got.A == 0 {
		t.Fatalf("expected shifted annotation at crop-local point, got %#v", got)
	}
}

func TestEncodePNGRejectsNil(t *testing.T) {
	if _, err := EncodePNG(nil); err == nil {
		t.Fatal("expected nil image error")
	}
}
