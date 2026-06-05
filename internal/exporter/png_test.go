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

func TestEncodePNGRejectsNil(t *testing.T) {
	if _, err := EncodePNG(nil); err == nil {
		t.Fatal("expected nil image error")
	}
}
