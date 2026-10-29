package workflow

import (
	"fmt"
	"image"
	"image/color"
	"math"
)

const maxTransformPixels = 64_000_000

type ImageTransform struct {
	Degrees        int
	FlipHorizontal bool
	FlipVertical   bool
}

func (t ImageTransform) NormalizedDegrees() int {
	return (t.Degrees%360 + 360) % 360
}

func (t ImageTransform) IsIdentity() bool {
	return t.NormalizedDegrees() == 0 && !t.FlipHorizontal && !t.FlipVertical
}

func transformGeometry(size image.Point, degrees int) (image.Point, float64, float64, error) {
	sin, cos := math.Sincos(float64(degrees) * math.Pi / 180)
	switch degrees {
	case 0:
		sin, cos = 0, 1
	case 90:
		sin, cos = 1, 0
	case 180:
		sin, cos = 0, -1
	case 270:
		sin, cos = -1, 0
	}
	width := math.Ceil(math.Abs(float64(size.X)*cos) + math.Abs(float64(size.Y)*sin))
	height := math.Ceil(math.Abs(float64(size.X)*sin) + math.Abs(float64(size.Y)*cos))
	if width < 1 || height < 1 || width > maxTransformPixels || height > maxTransformPixels || width*height > maxTransformPixels {
		return image.Point{}, 0, 0, fmt.Errorf("변환 결과는 최대 64,000,000 픽셀까지 지원합니다")
	}
	return image.Pt(int(width), int(height)), sin, cos, nil
}

func (c *Controller) TransformImage(t ImageTransform) error {
	if c.Mode != ModeEditing || c.CapturedImage == nil {
		return fmt.Errorf("no image to transform")
	}
	if t.IsIdentity() {
		return nil
	}
	crop := c.VisibleCropRect()
	degrees := t.NormalizedDegrees()
	size, sin, cos, err := transformGeometry(crop.Size(), degrees)
	if err != nil {
		return err
	}
	preview, err := c.PreviewImage()
	if err != nil {
		return err
	}
	src := preview.SubImage(crop).(*image.RGBA)
	dst := image.NewRGBA(image.Rectangle{Max: size})
	for y := 0; y < size.Y; y++ {
		for x := 0; x < size.X; x++ {
			dx, dy := float64(x)+0.5-float64(size.X)/2, float64(y)+0.5-float64(size.Y)/2
			sx, sy := cos*dx+sin*dy, -sin*dx+cos*dy
			if t.FlipHorizontal {
				sx = -sx
			}
			if t.FlipVertical {
				sy = -sy
			}
			sx += float64(crop.Min.X) + float64(crop.Dx())/2 - 0.5
			sy += float64(crop.Min.Y) + float64(crop.Dy())/2 - 0.5
			if degrees%90 == 0 {
				dst.SetRGBA(x, y, src.RGBAAt(int(math.Round(sx)), int(math.Round(sy))))
			} else {
				dst.SetRGBA(x, y, sampleTransformPixel(src, sx, sy))
			}
		}
	}
	c.pushEditHistory()
	c.CapturedImage = dst
	c.CapturedImageRect = image.Rectangle{Min: c.CapturedImageRect.Min, Max: c.CapturedImageRect.Min.Add(size)}
	c.CropRect = dst.Bounds()
	c.Editor.Annotations = nil
	c.Editor.RedoStack = nil
	c.Editor.Selection = rectToEditorRect(c.CropRect)
	c.updateSelectedRectsFromCrop()
	c.invalidatePreviewImage()
	return nil
}

func sampleTransformPixel(src *image.RGBA, x, y float64) color.RGBA {
	x0, y0 := int(math.Floor(x)), int(math.Floor(y))
	fx, fy := x-float64(x0), y-float64(y0)
	var channels [4]float64
	for j := 0; j < 2; j++ {
		wy := 1 - fy
		if j == 1 {
			wy = fy
		}
		for i := 0; i < 2; i++ {
			wx := 1 - fx
			if i == 1 {
				wx = fx
			}
			p := src.RGBAAt(x0+i, y0+j)
			// Freeform capture masks can leave RGB behind in transparent pixels.
			p.R, p.G, p.B = min(p.R, p.A), min(p.G, p.A), min(p.B, p.A)
			for k, v := range [4]uint8{p.R, p.G, p.B, p.A} {
				channels[k] += float64(v) * wx * wy
			}
		}
	}
	return color.RGBA{uint8(math.Round(channels[0])), uint8(math.Round(channels[1])), uint8(math.Round(channels[2])), uint8(math.Round(channels[3]))}
}
