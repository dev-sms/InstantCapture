package ui

import (
	"image"
	"runtime"
	"sync"

	"gioui.org/op/paint"
	xdraw "golang.org/x/image/draw"
)

// Gio samples textures bilinearly without mipmaps, so drawing a large image
// below half size skips source pixels and text turns jagged. Zoomed-out
// canvases instead draw a copy box-averaged by an integer factor, which the
// GPU then only has to shrink by less than 2x.
const maxCanvasMipFactor = 64

type canvasMipCache struct {
	src    *image.RGBA
	factor int
	img    *image.RGBA
	op     paint.ImageOp
}

// canvasMipFactor returns the integer reduction for drawing at scale, or 1
// when the GPU can sample the source directly without skipping pixels.
func canvasMipFactor(scale float64) int {
	if scale <= 0 || scale > 0.5 {
		return 1
	}
	return min(maxCanvasMipFactor, int(1/scale))
}

func (u *windowUI) canvasMipOperation(img image.Image, scale float64) (paint.ImageOp, int, bool) {
	src, ok := img.(*image.RGBA)
	factor := canvasMipFactor(scale)
	if !ok || src == nil || factor < 2 {
		return paint.ImageOp{}, 1, false
	}
	if u.canvasMip.src != src || u.canvasMip.factor != factor {
		reduced := boxDownsampleRGBA(src, factor)
		u.canvasMip = canvasMipCache{src: src, factor: factor, img: reduced, op: paint.NewImageOp(reduced)}
	}
	return u.canvasMip.op, factor, true
}

func (u *windowUI) clearCanvasMip() {
	u.canvasMip = canvasMipCache{}
}

// boxDownsampleRGBA averages each factor×factor block (partial blocks at the
// right and bottom edges average what they cover). RGBA is premultiplied, so a
// plain channel mean is the correct blend.
func boxDownsampleRGBA(src *image.RGBA, factor int) *image.RGBA {
	return boxDownsamplePix(src.Pix, src.Stride, src.Bounds().Size(), factor, false)
}

// boxDownsampleNRGBA premultiplies while averaging so transparent pixels do
// not bleed their hidden color into the result.
func boxDownsampleNRGBA(src *image.NRGBA, factor int) *image.RGBA {
	return boxDownsamplePix(src.Pix, src.Stride, src.Bounds().Size(), factor, true)
}

func boxDownsamplePix(pix []byte, stride int, size image.Point, factor int, premultiply bool) *image.RGBA {
	factor = min(max(factor, 1), maxCanvasMipFactor)
	b := image.Rectangle{Max: size}
	dw := (b.Dx() + factor - 1) / factor
	dh := (b.Dy() + factor - 1) / factor
	dst := image.NewRGBA(image.Rect(0, 0, dw, dh))
	if dw == 0 || dh == 0 {
		return dst
	}
	colorScale := uint32(1)
	if premultiply {
		colorScale = 255
	}
	workers := min(runtime.GOMAXPROCS(0), dh)
	rowsPerWorker := (dh + workers - 1) / workers
	var wg sync.WaitGroup
	for start := 0; start < dh; start += rowsPerWorker {
		end := min(dh, start+rowsPerWorker)
		wg.Add(1)
		go func(start, end int) {
			defer wg.Done()
			sums := make([]uint32, dw*4)
			for dy := start; dy < end; dy++ {
				clear(sums)
				y0 := dy * factor
				y1 := min(b.Dy(), y0+factor)
				for y := y0; y < y1; y++ {
					row := pix[y*stride : y*stride+b.Dx()*4]
					for dx := range dw {
						s := dx * 4
						var r, g, bl, a uint32
						for i := dx * factor * 4; i < min(b.Dx(), (dx+1)*factor)*4; i += 4 {
							p := row[i : i+4 : i+4]
							if premultiply {
								pa := uint32(p[3])
								r += uint32(p[0]) * pa
								g += uint32(p[1]) * pa
								bl += uint32(p[2]) * pa
								a += pa
								continue
							}
							r += uint32(p[0])
							g += uint32(p[1])
							bl += uint32(p[2])
							a += uint32(p[3])
						}
						sums[s] += r
						sums[s+1] += g
						sums[s+2] += bl
						sums[s+3] += a
					}
				}
				out := dst.Pix[dy*dst.Stride : dy*dst.Stride+dw*4]
				rows := uint32(y1 - y0)
				for dx := range dw {
					cols := uint32(min(b.Dx(), (dx+1)*factor) - dx*factor)
					n := rows * cols
					cn := n * colorScale
					s := dx * 4
					out[s] = uint8((sums[s] + cn/2) / cn)
					out[s+1] = uint8((sums[s+1] + cn/2) / cn)
					out[s+2] = uint8((sums[s+2] + cn/2) / cn)
					out[s+3] = uint8((sums[s+3] + n/2) / n)
				}
			}
		}(start, end)
	}
	wg.Wait()
	return dst
}

// resizeHighQuality fits src inside bounds without enlarging it. Unlike
// ApproxBiLinear, the Catmull-Rom kernel widens with the reduction so every
// source pixel contributes. A box pre-pass handles the whole-number part of the
// reduction, leaving the kernel less than 2x to cover, which keeps it fast.
func resizeHighQuality(src image.Image, bounds image.Point) *image.RGBA {
	size := fittedThumbnailSize(src.Bounds().Size(), bounds)
	if factor := min(src.Bounds().Dx()/size.X, src.Bounds().Dy()/size.Y); factor >= 2 {
		switch typed := src.(type) {
		case *image.RGBA:
			src = boxDownsampleRGBA(typed, factor)
		case *image.NRGBA:
			src = boxDownsampleNRGBA(typed, factor)
		}
	}
	dst := image.NewRGBA(image.Rectangle{Max: size})
	xdraw.CatmullRom.Scale(dst, dst.Bounds(), src, src.Bounds(), xdraw.Src, nil)
	return dst
}
