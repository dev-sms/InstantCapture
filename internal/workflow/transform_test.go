package workflow

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"testing"

	"instantcapture/internal/editor"
	"instantcapture/internal/exporter"
)

func TestImageTransformExactPixels(t *testing.T) {
	for _, tc := range []struct {
		name      string
		transform ImageTransform
		size      image.Point
		want      []uint8
	}{
		{"clockwise", ImageTransform{Degrees: 90}, image.Pt(3, 2), []uint8{5, 3, 1, 6, 4, 2}},
		{"half", ImageTransform{Degrees: 180}, image.Pt(2, 3), []uint8{6, 5, 4, 3, 2, 1}},
		{"threequarters", ImageTransform{Degrees: 270}, image.Pt(3, 2), []uint8{2, 4, 6, 1, 3, 5}},
		{"counterclockwise", ImageTransform{Degrees: -90}, image.Pt(3, 2), []uint8{2, 4, 6, 1, 3, 5}},
		{"counterclockwiseHalf", ImageTransform{Degrees: -180}, image.Pt(2, 3), []uint8{6, 5, 4, 3, 2, 1}},
		{"counterclockwiseThreequarters", ImageTransform{Degrees: -270}, image.Pt(3, 2), []uint8{5, 3, 1, 6, 4, 2}},
		{"horizontal", ImageTransform{FlipHorizontal: true}, image.Pt(2, 3), []uint8{2, 1, 4, 3, 6, 5}},
		{"vertical", ImageTransform{FlipVertical: true}, image.Pt(2, 3), []uint8{5, 6, 3, 4, 1, 2}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			src := image.NewRGBA(image.Rect(0, 0, 2, 3))
			for i := 0; i < 6; i++ {
				src.SetRGBA(i%2, i/2, color.RGBA{R: uint8(i + 1), A: 255})
			}
			c := NewController(nil)
			if err := c.CommitImportedImage(src); err != nil {
				t.Fatal(err)
			}
			if err := c.TransformImage(tc.transform); err != nil {
				t.Fatal(err)
			}
			if c.OutputSize() != tc.size {
				t.Fatal(c.OutputSize())
			}
			dst := c.CapturedImage.(*image.RGBA)
			for i, v := range tc.want {
				if dst.RGBAAt(i%tc.size.X, i/tc.size.X).R != v {
					t.Fatalf("pixel %d: %v", i, dst.Pix)
				}
			}
			if src.RGBAAt(0, 0).R != 1 {
				t.Fatal("source modified")
			}
			if len(c.undoStack) != 1 {
				t.Fatal("expected one history entry")
			}
			if !c.UndoEdit() || c.CapturedImage != src {
				t.Fatal("undo lost original")
			}
			if !c.RedoEdit() || c.CapturedImage != dst {
				t.Fatal("redo lost result")
			}
		})
	}
}

func TestTransformCropAnnotationsBorderAndSave(t *testing.T) {
	c := NewController(nil)
	src := image.NewRGBA(image.Rect(0, 0, 12, 10))
	if err := c.CommitImportedImage(src); err != nil {
		t.Fatal(err)
	}
	if err := c.AddAnnotation(editor.ToolRectangle, nil, editor.Rect{Min: editor.Point{X: 3, Y: 3}, Max: editor.Point{X: 7, Y: 6}}); err != nil {
		t.Fatal(err)
	}
	crop := image.Rect(2, 2, 9, 8)
	if err := c.CropCapturedImage(crop); err != nil {
		t.Fatal(err)
	}
	c.SetOutputBorder(true, 2, [4]uint8{10, 20, 30, 255})
	before, err := exporter.RenderAnnotationsCropped(src, c.Editor.RenderCommands(), crop)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.TransformImage(ImageTransform{FlipHorizontal: true}); err != nil {
		t.Fatal(err)
	}
	if len(c.Editor.Annotations) != 0 || c.CropRect != image.Rect(0, 0, 7, 6) {
		t.Fatal("not flattened")
	}
	dst := c.CapturedImage.(*image.RGBA)
	for y := 0; y < 6; y++ {
		for x := 0; x < 7; x++ {
			if dst.RGBAAt(x, y) != before.RGBAAt(6-x, y) {
				t.Fatal("composition mismatch")
			}
		}
	}
	rendered, err := c.RenderedImage()
	if err != nil {
		t.Fatal(err)
	}
	expected, err := exporter.RenderAnnotationsCroppedWithBorder(dst, nil, dst.Bounds(), c.RenderOptions.OutputBorder)
	if err != nil || !bytes.Equal(rendered.Pix, expected.Pix) {
		t.Fatal("border should apply once after transform")
	}
	path := filepath.Join(t.TempDir(), "transformed.png")
	if err := c.SaveImage(path); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	saved, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	copyData, err := exporter.EncodePNG(rendered)
	if err != nil {
		t.Fatal(err)
	}
	copyImage, err := png.Decode(bytes.NewReader(copyData))
	if err != nil {
		t.Fatal(err)
	}
	if saved.Bounds() != rendered.Bounds() {
		t.Fatal("saved bounds differ")
	}
	for y := 0; y < rendered.Bounds().Dy(); y++ {
		for x := 0; x < rendered.Bounds().Dx(); x++ {
			if color.NRGBAModel.Convert(saved.At(x, y)) != color.NRGBAModel.Convert(copyImage.At(x, y)) {
				t.Fatal("save differs from copy render")
			}
		}
	}
	if !c.UndoEdit() || c.CapturedImage != src || c.CropRect != crop || len(c.Editor.Annotations) != 1 {
		t.Fatal("undo did not restore layers")
	}
	if !c.RedoEdit() {
		t.Fatal("redo failed")
	}
	if err := c.CommitImportedImage(image.NewRGBA(image.Rect(0, 0, 4, 4))); err != nil {
		t.Fatal(err)
	}
	if c.UndoEdit() || c.RedoEdit() {
		t.Fatal("replacement retained history")
	}
}

func TestTransformArbitraryAndLimits(t *testing.T) {
	masked := image.NewRGBA(image.Rect(0, 0, 2, 2))
	masked.SetRGBA(0, 0, color.RGBA{R: 255})
	if got := sampleTransformPixel(masked, 0.2, 0.2); got != (color.RGBA{}) {
		t.Fatalf("transparent masked RGB leaked into interpolation: %v", got)
	}
	c := NewController(nil)
	src := image.NewRGBA(image.Rect(0, 0, 10, 6))
	for y := 0; y < 6; y++ {
		for x := 0; x < 10; x++ {
			src.SetRGBA(x, y, color.RGBA{R: 80, A: 128})
		}
	}
	if err := c.CommitImportedImage(src); err != nil {
		t.Fatal(err)
	}
	if err := c.TransformImage(ImageTransform{Degrees: 360}); err != nil || len(c.undoStack) != 0 {
		t.Fatal("identity mutated state")
	}
	if err := c.TransformImage(ImageTransform{Degrees: 37}); err != nil {
		t.Fatal(err)
	}
	if c.OutputSize() != image.Pt(12, 11) {
		t.Fatal(c.OutputSize())
	}
	dst := c.CapturedImage.(*image.RGBA)
	if dst.RGBAAt(0, 0).A != 0 {
		t.Fatal("corner should be transparent")
	}
	for i := 0; i < len(dst.Pix); i += 4 {
		if dst.Pix[i] > dst.Pix[i+3] {
			t.Fatal("invalid premultiplied alpha")
		}
	}
	if _, _, _, err := transformGeometry(image.Pt(8000, 8000), 45); err == nil {
		t.Fatal("expanded pixel limit missing")
	}
	for i := 0; i < MaxEditHistoryEntries+3; i++ {
		if err := c.TransformImage(ImageTransform{FlipVertical: true}); err != nil {
			t.Fatal(err)
		}
	}
	if len(c.undoStack) != MaxEditHistoryEntries {
		t.Fatal("unbounded history")
	}
	c.ResetEditingSession()
	if c.CapturedImage != nil || c.previewImage != nil || len(c.undoStack) != 0 {
		t.Fatal("session retained image")
	}
}
