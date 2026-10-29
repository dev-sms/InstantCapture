package ui

import (
	"image"
	"image/color"
	"testing"
)

func TestBoxDownsampleAveragesBlocksAndPartialEdges(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 5, 3))
	for y := range 3 {
		for x := range 5 {
			src.SetRGBA(x, y, color.RGBA{R: uint8(x * 40), G: uint8(y * 50), B: 7, A: 255})
		}
	}
	got := boxDownsampleRGBA(src, 2)
	if got.Bounds().Size() != image.Pt(3, 2) {
		t.Fatalf("size = %v, want 3x2", got.Bounds().Size())
	}
	checks := map[image.Point]color.RGBA{
		{0, 0}: {R: 20, G: 25, B: 7, A: 255},  // x 0,40 · y 0,50
		{2, 0}: {R: 160, G: 25, B: 7, A: 255}, // partial column x=4
		{1, 1}: {R: 100, G: 100, B: 7, A: 255},
		{2, 1}: {R: 160, G: 100, B: 7, A: 255}, // partial corner
	}
	for p, want := range checks {
		if c := got.RGBAAt(p.X, p.Y); c != want {
			t.Fatalf("pixel %v = %v, want %v", p, c, want)
		}
	}
}

func TestBoxDownsampleKeepsThinLinesVisible(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 400, 40))
	for y := range 40 {
		src.SetRGBA(201, y, color.RGBA{A: 255})
	}
	got := boxDownsampleRGBA(src, 4)
	if got.RGBAAt(50, 5).A == 0 {
		t.Fatal("a 1px line must still contribute to the reduced image")
	}
}

func TestBoxDownsampleSubImage(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 8, 8))
	src.SetRGBA(4, 4, color.RGBA{R: 255, A: 255})
	sub := src.SubImage(image.Rect(4, 4, 8, 8)).(*image.RGBA)
	got := boxDownsampleRGBA(sub, 4)
	if c := got.RGBAAt(0, 0); c.R != 16 || c.A != 16 {
		t.Fatalf("sub-image corner = %v, want one of 16 pixels lit", c)
	}
}

func TestBoxDownsampleNRGBAIgnoresHiddenColor(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 2, 1))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	src.SetNRGBA(1, 0, color.NRGBA{G: 255, A: 0})
	got := boxDownsampleNRGBA(src, 2).RGBAAt(0, 0)
	if want := (color.RGBA{R: 128, A: 128}); got != want {
		t.Fatalf("pixel = %v, want premultiplied %v", got, want)
	}
}

func TestCanvasMipFactor(t *testing.T) {
	for _, tc := range []struct {
		scale float64
		want  int
	}{{1, 1}, {0.75, 1}, {0.5, 2}, {0.34, 2}, {0.33, 3}, {0.01, maxCanvasMipFactor}} {
		if got := canvasMipFactor(tc.scale); got != tc.want {
			t.Fatalf("canvasMipFactor(%v) = %d, want %d", tc.scale, got, tc.want)
		}
	}
}

func TestCanvasMipOperationCachesPerImageAndFactor(t *testing.T) {
	u := &windowUI{}
	img := image.NewRGBA(image.Rect(0, 0, 100, 60))
	if _, _, ok := u.canvasMipOperation(img, 0.8); ok {
		t.Fatal("no mip needed above half size")
	}
	_, factor, ok := u.canvasMipOperation(img, 0.4)
	if !ok || factor != 2 || u.canvasMip.img.Bounds().Size() != image.Pt(50, 30) {
		t.Fatalf("factor %d ok %v size %v", factor, ok, u.canvasMip.img.Bounds().Size())
	}
	first := u.canvasMip.img
	u.canvasMipOperation(img, 0.45)
	if u.canvasMip.img != first {
		t.Fatal("same image and factor should reuse the reduced copy")
	}
	u.releaseTransientImageCaches()
	if u.canvasMip.img != nil || u.canvasMip.src != nil {
		t.Fatal("hiding must release the reduced copy and its source reference")
	}
}

func TestResizeHighQualityFitsWithoutEnlarging(t *testing.T) {
	big := resizeHighQuality(image.NewRGBA(image.Rect(0, 0, 3840, 2160)), image.Pt(320, 320))
	if big.Bounds().Size() != image.Pt(320, 180) {
		t.Fatalf("size = %v", big.Bounds().Size())
	}
	small := resizeHighQuality(image.NewNRGBA(image.Rect(0, 0, 50, 40)), image.Pt(320, 320))
	if small.Bounds().Size() != image.Pt(50, 40) {
		t.Fatalf("small size = %v", small.Bounds().Size())
	}
}

func BenchmarkBoxDownsample4K(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	for b.Loop() {
		boxDownsampleRGBA(src, 2)
	}
}

func BenchmarkResizeHighQuality4KToHover(b *testing.B) {
	src := image.NewNRGBA(image.Rect(0, 0, 3840, 2160))
	for b.Loop() {
		resizeHighQuality(src, image.Pt(1080, 720))
	}
}

func BenchmarkResizeHighQualityRGBA4KToHover(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	for b.Loop() {
		resizeHighQuality(src, image.Pt(1080, 720))
	}
}

func BenchmarkResizeHighQuality4KToThumb(b *testing.B) {
	src := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	for b.Loop() {
		resizeHighQuality(src, image.Pt(192, 156))
	}
}
