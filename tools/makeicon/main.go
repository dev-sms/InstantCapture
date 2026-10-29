package main

import (
	"encoding/binary"
	"image"
	"image/png"
	"os"

	"github.com/fogleman/gg"
	xdraw "golang.org/x/image/draw"
)

var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 128, 256}

func main() {
	icon := drawAppIcon()
	pngFile, err := os.Create("assets/app.png")
	if err != nil {
		panic(err)
	}
	if err := png.Encode(pngFile, icon); err != nil {
		pngFile.Close()
		panic(err)
	}
	if err := pngFile.Close(); err != nil {
		panic(err)
	}
	data, err := encodeICO(icon, iconSizes)
	if err != nil {
		panic(err)
	}
	if err := os.WriteFile("assets/app.ico", data, 0o644); err != nil {
		panic(err)
	}
}

func drawAppIcon() image.Image {
	const size = 256
	ctx := gg.NewContext(size, size)
	ctx.SetRGBA(0, 0, 0, 0)
	ctx.Clear()

	ctx.SetHexColor("#07111F")
	ctx.DrawRoundedRectangle(8, 8, 240, 240, 54)
	ctx.Fill()
	ctx.SetHexColor("#0A84FF")
	ctx.DrawRoundedRectangle(28, 28, 200, 200, 44)
	ctx.Fill()
	ctx.SetHexColor("#0F2438")
	ctx.DrawRoundedRectangle(43, 72, 170, 130, 28)
	ctx.Fill()

	ctx.SetHexColor("#E0F2FE")
	ctx.DrawRoundedRectangle(74, 55, 66, 34, 12)
	ctx.Fill()
	ctx.SetHexColor("#38BDF8")
	ctx.DrawCircle(184, 96, 10)
	ctx.Fill()

	ctx.SetHexColor("#F8FAFC")
	ctx.DrawCircle(128, 139, 50)
	ctx.Fill()
	ctx.SetHexColor("#082F49")
	ctx.DrawCircle(128, 139, 36)
	ctx.Fill()
	ctx.SetHexColor("#38BDF8")
	ctx.DrawCircle(128, 139, 20)
	ctx.Fill()
	ctx.SetHexColor("#BAE6FD")
	ctx.DrawCircle(120, 131, 7)
	ctx.Fill()

	return ctx.Image()
}

func encodeICO(src image.Image, sizes []int) ([]byte, error) {
	images := make([][]byte, len(sizes))
	for i, size := range sizes {
		scaled := image.NewRGBA(image.Rect(0, 0, size, size))
		xdraw.CatmullRom.Scale(scaled, scaled.Bounds(), src, src.Bounds(), xdraw.Over, nil)
		images[i] = encodeIconDIB(scaled)
	}

	headerSize := 6 + len(images)*16
	totalSize := headerSize
	for _, data := range images {
		totalSize += len(data)
	}
	out := make([]byte, totalSize)
	binary.LittleEndian.PutUint16(out[2:], 1)
	binary.LittleEndian.PutUint16(out[4:], uint16(len(images)))
	offset := headerSize
	for i, data := range images {
		entry := out[6+i*16:]
		size := sizes[i]
		if size < 256 {
			entry[0] = byte(size)
			entry[1] = byte(size)
		}
		binary.LittleEndian.PutUint16(entry[4:], 1)
		binary.LittleEndian.PutUint16(entry[6:], 32)
		binary.LittleEndian.PutUint32(entry[8:], uint32(len(data)))
		binary.LittleEndian.PutUint32(entry[12:], uint32(offset))
		copy(out[offset:], data)
		offset += len(data)
	}
	return out, nil
}

func encodeIconDIB(img *image.RGBA) []byte {
	size := img.Bounds().Dx()
	maskStride := (size + 31) / 32 * 4
	pixelBytes := size * size * 4
	data := make([]byte, 40+pixelBytes+maskStride*size)
	binary.LittleEndian.PutUint32(data[0:], 40)
	binary.LittleEndian.PutUint32(data[4:], uint32(size))
	binary.LittleEndian.PutUint32(data[8:], uint32(size*2))
	binary.LittleEndian.PutUint16(data[12:], 1)
	binary.LittleEndian.PutUint16(data[14:], 32)
	binary.LittleEndian.PutUint32(data[20:], uint32(pixelBytes))

	pixels := data[40 : 40+pixelBytes]
	mask := data[40+pixelBytes:]
	for y := 0; y < size; y++ {
		sourceY := size - 1 - y
		for x := 0; x < size; x++ {
			c := img.RGBAAt(x, sourceY)
			offset := (y*size + x) * 4
			pixels[offset+0] = c.B
			pixels[offset+1] = c.G
			pixels[offset+2] = c.R
			pixels[offset+3] = c.A
			if c.A == 0 {
				mask[y*maskStride+x/8] |= 0x80 >> uint(x%8)
			}
		}
	}
	return data
}
