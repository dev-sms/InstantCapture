package main

import (
	"encoding/binary"
	"image"
	"image/color"
	"image/draw"
	"os"
)

func main() {
	img := image.NewRGBA(image.Rect(0, 0, 32, 32))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 8, G: 13, B: 26, A: 255}}, image.Point{}, draw.Src)
	fillRect(img, 4, 4, 28, 28, color.RGBA{R: 20, G: 184, B: 166, A: 255})
	fillRect(img, 8, 8, 24, 24, color.RGBA{R: 15, G: 23, B: 42, A: 255})
	fillRect(img, 10, 10, 13, 22, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	fillRect(img, 15, 10, 22, 13, color.RGBA{R: 255, G: 255, B: 255, A: 255})
	fillRect(img, 19, 13, 22, 22, color.RGBA{R: 255, G: 255, B: 255, A: 255})

	data := encodeICO(img)
	if err := os.WriteFile("assets/app.ico", data, 0o644); err != nil {
		panic(err)
	}
}

func fillRect(img *image.RGBA, x0, y0, x1, y1 int, c color.RGBA) {
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			img.SetRGBA(x, y, c)
		}
	}
}

func encodeICO(img *image.RGBA) []byte {
	const size = 32
	andMaskBytes := size * size / 8
	pixelBytes := size * size * 4
	bmpSize := 40 + pixelBytes + andMaskBytes
	out := make([]byte, 6+16+bmpSize)
	binary.LittleEndian.PutUint16(out[0:], 0)
	binary.LittleEndian.PutUint16(out[2:], 1)
	binary.LittleEndian.PutUint16(out[4:], 1)
	out[6] = size
	out[7] = size
	out[8] = 0
	out[9] = 0
	binary.LittleEndian.PutUint16(out[10:], 1)
	binary.LittleEndian.PutUint16(out[12:], 32)
	binary.LittleEndian.PutUint32(out[14:], uint32(bmpSize))
	binary.LittleEndian.PutUint32(out[18:], 22)

	bmp := out[22:]
	binary.LittleEndian.PutUint32(bmp[0:], 40)
	binary.LittleEndian.PutUint32(bmp[4:], size)
	binary.LittleEndian.PutUint32(bmp[8:], size*2)
	binary.LittleEndian.PutUint16(bmp[12:], 1)
	binary.LittleEndian.PutUint16(bmp[14:], 32)
	binary.LittleEndian.PutUint32(bmp[16:], 0)
	binary.LittleEndian.PutUint32(bmp[20:], uint32(pixelBytes))

	pixels := bmp[40:]
	for y := 0; y < size; y++ {
		for x := 0; x < size; x++ {
			r, g, b, a := img.At(x, size-1-y).RGBA()
			i := (y*size + x) * 4
			pixels[i+0] = byte(b >> 8)
			pixels[i+1] = byte(g >> 8)
			pixels[i+2] = byte(r >> 8)
			pixels[i+3] = byte(a >> 8)
		}
	}
	return out
}
