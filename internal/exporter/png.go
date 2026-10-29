package exporter

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/gif"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fogleman/gg"
	"github.com/go-text/typesetting/segmenter"
	"golang.design/x/clipboard"
	"golang.org/x/image/bmp"
	"golang.org/x/image/font"
	"golang.org/x/image/font/gofont/gobold"
	"golang.org/x/image/font/gofont/goregular"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/font/sfnt"

	"instantcapture/internal/editor"
)

type BorderOptions struct {
	Enabled bool
	Width   int
	Color   color.NRGBA
}

type mosaicSegment struct {
	from editor.Point
	to   editor.Point
}

func RenderAnnotations(base image.Image, commands []editor.RenderCommand) (*image.RGBA, error) {
	return RenderAnnotationsCropped(base, commands, baseBounds(base))
}

func RenderTextLayer(command editor.RenderCommand) (*image.RGBA, error) {
	if command.Tool != editor.ToolText {
		return nil, fmt.Errorf("render command is not text")
	}
	r := command.Rect.Normalize().ImageRect()
	if r.Empty() {
		return nil, fmt.Errorf("text rectangle is empty")
	}
	layer := image.NewRGBA(image.Rectangle{Max: r.Size()})
	ctx := gg.NewContextForRGBA(layer)
	ctx.Translate(float64(-r.Min.X), float64(-r.Min.Y))
	if err := drawTextCommand(ctx, command); err != nil {
		return nil, err
	}
	return layer, nil
}

func RenderAnnotationsCropped(base image.Image, commands []editor.RenderCommand, crop image.Rectangle) (*image.RGBA, error) {
	return RenderAnnotationsCroppedWithBorder(base, commands, crop, BorderOptions{})
}

func RenderAnnotationsCroppedWithBorder(base image.Image, commands []editor.RenderCommand, crop image.Rectangle, border BorderOptions) (*image.RGBA, error) {
	if base == nil {
		return nil, fmt.Errorf("base image is nil")
	}

	bounds := base.Bounds()
	crop = crop.Canon().Intersect(bounds)
	if crop.Empty() {
		return nil, fmt.Errorf("crop rectangle is empty")
	}
	canvas := image.NewRGBA(image.Rect(0, 0, bounds.Dx(), bounds.Dy()))
	draw.Draw(canvas, canvas.Bounds(), base, bounds.Min, draw.Src)

	ctx := gg.NewContextForRGBA(canvas)
	ctx.Translate(float64(-bounds.Min.X), float64(-bounds.Min.Y))
	for _, command := range commands {
		if command.Tool == editor.ToolMosaic {
			if err := drawMosaic(canvas, command, bounds.Min); err != nil {
				return nil, err
			}
			continue
		}
		if command.Tool == editor.ToolHighlight {
			if err := drawHighlightMultiply(canvas, command, bounds.Min); err != nil {
				return nil, err
			}
			continue
		}
		if command.Tool == editor.ToolText {
			if err := drawTextCommand(ctx, command); err != nil {
				return nil, err
			}
			continue
		}
		passes := 1
		if command.Style.ColorRGBA[3] >= 255 {
			passes = 2
		}
		for range passes {
			applyStyle(ctx, command.Style)
			if err := drawCommand(ctx, command); err != nil {
				return nil, err
			}
		}
	}
	if !crop.Eq(bounds) {
		cropped := image.NewRGBA(image.Rect(0, 0, crop.Dx(), crop.Dy()))
		draw.Draw(cropped, cropped.Bounds(), canvas, crop.Min.Sub(bounds.Min), draw.Src)
		canvas = cropped
	}

	if !border.Enabled || border.Width <= 0 || border.Color.A == 0 {
		return canvas, nil
	}
	return addOutputBorder(canvas, border), nil
}

func drawMosaic(canvas *image.RGBA, command editor.RenderCommand, cropMin image.Point) error {
	if command.MosaicBlockSize <= 0 {
		return fmt.Errorf("mosaic block size must be positive")
	}
	if command.MosaicMode == editor.MosaicModeRectangle {
		return drawRectangleMosaic(canvas, command, cropMin)
	}
	if len(command.Points) < 2 || command.Style.Width <= 0 {
		return nil
	}
	blocks := make(map[image.Point][]mosaicSegment)
	padding := int(math.Ceil(command.Style.Width/2)) + 2
	for i := 1; i < len(command.Points); i++ {
		item := mosaicSegment{from: command.Points[i-1], to: command.Points[i]}
		for _, origin := range mosaicBlockOrigins(item, command.MosaicBlockSize, padding) {
			blocks[origin] = append(blocks[origin], item)
		}
	}

	for blockOrigin, segments := range blocks {
		block := image.Rect(
			blockOrigin.X-cropMin.X,
			blockOrigin.Y-cropMin.Y,
			blockOrigin.X-cropMin.X+command.MosaicBlockSize,
			blockOrigin.Y-cropMin.Y+command.MosaicBlockSize,
		).Intersect(canvas.Bounds())
		if block.Empty() {
			continue
		}
		pixelated := averageColor(canvas, block)
		radiusSquared := command.Style.Width * command.Style.Width / 4
		for y := block.Min.Y; y < block.Max.Y; y++ {
			for x := block.Min.X; x < block.Max.X; x++ {
				globalX := float64(x+cropMin.X) + 0.5
				globalY := float64(y+cropMin.Y) + 0.5
				covered := false
				for _, item := range segments {
					if pointSegmentDistanceSquared(globalX, globalY, item) <= radiusSquared {
						covered = true
						break
					}
				}
				if !covered {
					continue
				}
				canvas.Set(x, y, pixelated)
			}
		}
	}
	return nil
}

func drawRectangleMosaic(canvas *image.RGBA, command editor.RenderCommand, cropMin image.Point) error {
	selected := command.Rect.Normalize().ImageRect().Sub(cropMin).Intersect(canvas.Bounds())
	if selected.Empty() {
		return nil
	}
	global := command.Rect.Normalize().ImageRect()
	startX := floorToBlock(global.Min.X, command.MosaicBlockSize)
	startY := floorToBlock(global.Min.Y, command.MosaicBlockSize)
	for blockY := startY; blockY < global.Max.Y; blockY += command.MosaicBlockSize {
		for blockX := startX; blockX < global.Max.X; blockX += command.MosaicBlockSize {
			block := image.Rect(
				blockX-cropMin.X,
				blockY-cropMin.Y,
				blockX-cropMin.X+command.MosaicBlockSize,
				blockY-cropMin.Y+command.MosaicBlockSize,
			).Intersect(canvas.Bounds())
			if block.Empty() {
				continue
			}
			pixelated := averageColor(canvas, block)
			fill := block.Intersect(selected)
			for y := fill.Min.Y; y < fill.Max.Y; y++ {
				for x := fill.Min.X; x < fill.Max.X; x++ {
					canvas.Set(x, y, pixelated)
				}
			}
		}
	}
	return nil
}

func pointSegmentDistanceSquared(x, y float64, segment mosaicSegment) float64 {
	fromX, fromY := float64(segment.from.X), float64(segment.from.Y)
	dx := float64(segment.to.X - segment.from.X)
	dy := float64(segment.to.Y - segment.from.Y)
	lengthSquared := dx*dx + dy*dy
	if lengthSquared == 0 {
		deltaX, deltaY := x-fromX, y-fromY
		return deltaX*deltaX + deltaY*deltaY
	}
	t := ((x-fromX)*dx + (y-fromY)*dy) / lengthSquared
	t = math.Max(0, math.Min(1, t))
	closestX := fromX + t*dx
	closestY := fromY + t*dy
	deltaX, deltaY := x-closestX, y-closestY
	return deltaX*deltaX + deltaY*deltaY
}

func mosaicBlockOrigins(segment mosaicSegment, blockSize, padding int) []image.Point {
	dx := float64(segment.to.X - segment.from.X)
	dy := float64(segment.to.Y - segment.from.Y)
	distance := math.Hypot(dx, dy)
	step := math.Max(1, float64(blockSize)/2)
	samples := max(1, int(math.Ceil(distance/step)))
	radius := padding/blockSize + 2
	origins := make(map[image.Point]struct{}, samples*(radius*2+1))
	for sample := 0; sample <= samples; sample++ {
		t := float64(sample) / float64(samples)
		x := int(math.Round(float64(segment.from.X) + dx*t))
		y := int(math.Round(float64(segment.from.Y) + dy*t))
		centerX := floorToBlock(x, blockSize)
		centerY := floorToBlock(y, blockSize)
		for row := -radius; row <= radius; row++ {
			for col := -radius; col <= radius; col++ {
				origins[image.Pt(centerX+col*blockSize, centerY+row*blockSize)] = struct{}{}
			}
		}
	}
	result := make([]image.Point, 0, len(origins))
	for origin := range origins {
		result = append(result, origin)
	}
	return result
}

func floorToBlock(value, blockSize int) int {
	if value >= 0 {
		return value / blockSize * blockSize
	}
	return -((-value + blockSize - 1) / blockSize * blockSize)
}

func averageColor(img image.Image, bounds image.Rectangle) color.NRGBA {
	if bounds.Empty() {
		return color.NRGBA{}
	}
	var red, green, blue, alpha uint64
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			pixel := color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA)
			red += uint64(pixel.R)
			green += uint64(pixel.G)
			blue += uint64(pixel.B)
			alpha += uint64(pixel.A)
		}
	}
	count := uint64(bounds.Dx() * bounds.Dy())
	return color.NRGBA{R: uint8(red / count), G: uint8(green / count), B: uint8(blue / count), A: uint8(alpha / count)}
}

func drawHighlightMultiply(canvas *image.RGBA, command editor.RenderCommand, cropMin image.Point) error {
	bounds := highlightBounds(command, cropMin).Intersect(canvas.Bounds())
	if bounds.Empty() {
		return nil
	}
	layer := image.NewRGBA(image.Rectangle{Max: bounds.Size()})
	ctx := gg.NewContextForRGBA(layer)
	ctx.Translate(float64(-cropMin.X-bounds.Min.X), float64(-cropMin.Y-bounds.Min.Y))
	applyStyle(ctx, command.Style)
	if err := drawCommand(ctx, command); err != nil {
		return err
	}

	for y := layer.Bounds().Min.Y; y < layer.Bounds().Max.Y; y++ {
		for x := layer.Bounds().Min.X; x < layer.Bounds().Max.X; x++ {
			highlight := color.NRGBAModel.Convert(layer.At(x, y)).(color.NRGBA)
			if highlight.A == 0 {
				continue
			}
			canvasX, canvasY := x+bounds.Min.X, y+bounds.Min.Y
			base := color.NRGBAModel.Convert(canvas.At(canvasX, canvasY)).(color.NRGBA)
			if base.A == 0 {
				continue
			}
			base.R = multiplyChannel(base.R, highlight.R, highlight.A)
			base.G = multiplyChannel(base.G, highlight.G, highlight.A)
			base.B = multiplyChannel(base.B, highlight.B, highlight.A)
			canvas.Set(canvasX, canvasY, base)
		}
	}
	return nil
}

func highlightBounds(command editor.RenderCommand, cropMin image.Point) image.Rectangle {
	if len(command.Points) == 0 {
		return image.Rectangle{}
	}
	minX, minY := command.Points[0].X, command.Points[0].Y
	maxX, maxY := minX, minY
	for _, point := range command.Points[1:] {
		minX = min(minX, point.X)
		minY = min(minY, point.Y)
		maxX = max(maxX, point.X)
		maxY = max(maxY, point.Y)
	}
	padding := int(math.Ceil(command.Style.Width/2)) + 2
	return image.Rect(
		minX-cropMin.X-padding,
		minY-cropMin.Y-padding,
		maxX-cropMin.X+padding+1,
		maxY-cropMin.Y+padding+1,
	)
}

func multiplyChannel(base, highlight, alpha uint8) uint8 {
	multiplied := int(base) * int(highlight) / 255
	return uint8((multiplied*int(alpha) + int(base)*(255-int(alpha)) + 127) / 255)
}

func addOutputBorder(src *image.RGBA, border BorderOptions) *image.RGBA {
	w := border.Width
	if w <= 0 {
		return src
	}
	bounds := src.Bounds()
	out := image.NewRGBA(image.Rect(0, 0, bounds.Dx()+2*w, bounds.Dy()+2*w))
	draw.Draw(out, out.Bounds(), &image.Uniform{C: border.Color}, image.Point{}, draw.Src)
	draw.Draw(out, image.Rect(w, w, w+bounds.Dx(), w+bounds.Dy()), src, bounds.Min, draw.Src)
	return out
}

func baseBounds(base image.Image) image.Rectangle {
	if base == nil {
		return image.Rectangle{}
	}
	return base.Bounds()
}

func drawCommand(ctx *gg.Context, command editor.RenderCommand) error {
	switch command.Tool {
	case editor.ToolPen:
		if command.PenMode == editor.PenModeRectangle {
			r := command.Rect.Normalize()
			ctx.DrawRectangle(float64(r.Min.X), float64(r.Min.Y), float64(r.Width()), float64(r.Height()))
			ctx.Fill()
		} else {
			drawPen(ctx, command.Points)
		}
	case editor.ToolHighlight, editor.ToolMosaic:
		drawPen(ctx, command.Points)
	case editor.ToolRectangle:
		r := command.Rect.Normalize()
		ctx.DrawRectangle(float64(r.Min.X), float64(r.Min.Y), float64(r.Width()), float64(r.Height()))
		ctx.Stroke()
	case editor.ToolTriangle:
		drawTriangle(ctx, command.Rect)
	case editor.ToolCircle:
		r := command.Rect.Normalize()
		ctx.DrawEllipse(
			float64(r.Min.X+r.Width()/2),
			float64(r.Min.Y+r.Height()/2),
			float64(r.Width())/2,
			float64(r.Height())/2,
		)
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
	return writeFileAtomic(path, data, 0o644)
}

func SupportsSavePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp":
		return true
	default:
		return false
	}
}

func SupportsDirectSavePath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png", ".jpg", ".jpeg", ".bmp":
		return true
	default:
		return false
	}
}

func SaveImage(path string, img image.Image) error {
	if img == nil {
		return fmt.Errorf("image is nil")
	}
	var buf bytes.Buffer
	switch strings.ToLower(filepath.Ext(path)) {
	case ".png":
		if err := png.Encode(&buf, img); err != nil {
			return fmt.Errorf("encode png: %w", err)
		}
	case ".jpg", ".jpeg":
		background := image.NewRGBA(img.Bounds())
		draw.Draw(background, background.Bounds(), &image.Uniform{C: color.White}, image.Point{}, draw.Src)
		draw.Draw(background, background.Bounds(), img, img.Bounds().Min, draw.Over)
		if err := jpeg.Encode(&buf, background, &jpeg.Options{Quality: 95}); err != nil {
			return fmt.Errorf("encode jpeg: %w", err)
		}
	case ".gif":
		if err := gif.Encode(&buf, img, nil); err != nil {
			return fmt.Errorf("encode gif: %w", err)
		}
	case ".bmp":
		if err := bmp.Encode(&buf, img); err != nil {
			return fmt.Errorf("encode bmp: %w", err)
		}
	default:
		return fmt.Errorf("unsupported image format %q", filepath.Ext(path))
	}
	return writeFileAtomic(path, buf.Bytes(), 0o644)
}

func writeFileAtomic(path string, data []byte, perm os.FileMode) (err error) {
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+"-*.tmp")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(perm); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := temp.Sync(); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return replaceFile(tempPath, path)
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

func drawTriangle(ctx *gg.Context, rect editor.Rect) {
	r := rect.Normalize()
	ctx.MoveTo(float64(r.Min.X+r.Width()/2), float64(r.Min.Y))
	ctx.LineTo(float64(r.Max.X), float64(r.Max.Y))
	ctx.LineTo(float64(r.Min.X), float64(r.Max.Y))
	ctx.ClosePath()
	ctx.Stroke()
}

func drawTextCommand(ctx *gg.Context, command editor.RenderCommand) error {
	fontStyle := command.TextStyle
	fontStyle.FontFamily = editor.EffectiveRichTextFontFamily(command.TextStyle, command.TextRuns)
	regular, err := textFontFaceForWeight(fontStyle, command.Text, false)
	if err != nil {
		return err
	}
	defer regular.Close()
	bold := regular
	if editor.TextRunsUseBold(command.TextStyle.Bold, command.TextRuns) {
		bold, err = textFontFaceForWeight(fontStyle, command.Text, true)
		if err != nil {
			return err
		}
		defer bold.Close()
	}
	for _, r := range command.Text {
		if isHangulRune(r) {
			if _, ok := regular.GlyphAdvance(r); !ok {
				return fmt.Errorf("text font does not support Korean glyph %q", r)
			}
		}
	}

	r := command.Rect.Normalize()
	style := command.TextStyle
	if style.BackgroundEnabled {
		setContextRGBA(ctx, style.BackgroundRGBA)
		ctx.DrawRoundedRectangle(float64(r.Min.X), float64(r.Min.Y), float64(r.Width()), float64(r.Height()), math.Min(style.CornerRadius, float64(min(r.Width(), r.Height()))/2))
		ctx.Fill()
	}
	if style.BorderEnabled && style.BorderWidth > 0 {
		setContextRGBA(ctx, style.BorderRGBA)
		inset := style.BorderWidth / 2
		ctx.SetLineWidth(style.BorderWidth)
		ctx.DrawRoundedRectangle(float64(r.Min.X)+inset, float64(r.Min.Y)+inset, math.Max(0, float64(r.Width())-style.BorderWidth), math.Max(0, float64(r.Height())-style.BorderWidth), math.Max(0, math.Min(style.CornerRadius-inset, float64(min(r.Width(), r.Height()))/2)))
		ctx.Stroke()
	}
	padding := editor.EffectiveTextPadding(style)
	content := image.Rect(
		r.Min.X+int(math.Round(padding)),
		r.Min.Y+int(math.Round(padding)),
		r.Max.X-int(math.Round(padding)),
		r.Max.Y-int(math.Round(padding)),
	)
	if content.Empty() {
		return nil
	}
	c := command.TextStyle.ColorRGBA
	ctx.SetRGBA255(int(c[0]), int(c[1]), int(c[2]), int(c[3]))
	regularMetrics := regular.Metrics()
	boldMetrics := bold.Metrics()
	ascent := float64(max(regularMetrics.Ascent.Ceil(), boldMetrics.Ascent.Ceil()))
	descent := float64(max(regularMetrics.Descent.Ceil(), boldMetrics.Descent.Ceil()))
	lineHeight := math.Max(command.TextStyle.FontSize*editor.EffectiveTextLineHeight(style), ascent+descent)
	y := float64(content.Min.Y) + ascent
	lines := wrapStyledTextLines(command.Text, command.TextRuns, command.TextStyle.Bold, regular, max(1, float64(content.Dx())))
	ctx.Push()
	ctx.DrawRectangle(float64(content.Min.X), float64(content.Min.Y), float64(content.Dx()), float64(content.Dy()))
	ctx.Clip()
	defer ctx.Pop()
	for _, line := range lines {
		if y+descent > float64(content.Max.Y) {
			break
		}
		x := float64(content.Min.X)
		switch command.TextStyle.Alignment {
		case editor.TextAlignCenter:
			x += (float64(content.Dx()) - line.width) / 2
		case editor.TextAlignRight:
			x += float64(content.Dx()) - line.width
		}
		for _, run := range line.runs {
			face := regular
			if run.bold {
				face = bold
			}
			ctx.SetFontFace(face)
			ctx.DrawString(run.text, x, y)
			x += float64(font.MeasureString(regular, run.text)) / 64
		}
		y += lineHeight
	}
	return nil
}

func setContextRGBA(ctx *gg.Context, rgba [4]uint8) {
	ctx.SetRGBA255(int(rgba[0]), int(rgba[1]), int(rgba[2]), int(rgba[3]))
}

type styledTextToken struct {
	text    string
	bold    bool
	newline bool
}

type styledTextRun struct {
	text string
	bold bool
}

type styledTextLine struct {
	runs  []styledTextRun
	width float64
}

func wrapStyledTextLines(value string, runs []editor.TextRun, baseBold bool, regular font.Face, maxWidth float64) []styledTextLine {
	tokens := styledTextTokens(value, runs, baseBold)
	lines := make([]styledTextLine, 0, strings.Count(value, "\n")+1)
	current := make([]styledTextToken, 0, len(tokens))
	appendLine := func(tokens []styledTextToken) {
		lines = append(lines, styledTextLine{runs: mergeStyledTextTokens(tokens), width: measureStyledTextTokens(tokens, regular)})
	}
	for _, token := range tokens {
		if token.newline {
			appendLine(current)
			current = current[:0]
			continue
		}
		candidate := append(current, token)
		if len(current) > 0 && measureStyledTextTokens(candidate, regular) > maxWidth {
			appendLine(current)
			current = append(current[:0], token)
		} else {
			current = candidate
		}
	}
	appendLine(current)
	return lines
}

func styledTextTokens(value string, runs []editor.TextRun, baseBold bool) []styledTextToken {
	tokens := make([]styledTextToken, 0, len([]rune(value)))
	runeOffset := 0
	for _, grapheme := range textGraphemes(value) {
		count := len([]rune(grapheme))
		tokens = append(tokens, styledTextToken{
			text:    grapheme,
			bold:    textBoldAt(runs, baseBold, runeOffset),
			newline: grapheme == "\n",
		})
		runeOffset += count
	}
	return tokens
}

func textBoldAt(runs []editor.TextRun, base bool, runeOffset int) bool {
	for _, run := range runs {
		if runeOffset >= run.Start && runeOffset < run.End {
			return run.Bold
		}
	}
	return base
}

func measureStyledTextTokens(tokens []styledTextToken, regular font.Face) float64 {
	width := 0.0
	for _, run := range mergeStyledTextTokens(tokens) {
		advance := font.MeasureString(regular, run.text)
		width += float64(advance) / 64
	}
	return width
}

func mergeStyledTextTokens(tokens []styledTextToken) []styledTextRun {
	runs := make([]styledTextRun, 0, len(tokens))
	for _, token := range tokens {
		if token.newline {
			continue
		}
		if len(runs) > 0 && runs[len(runs)-1].bold == token.bold {
			runs[len(runs)-1].text += token.text
			continue
		}
		runs = append(runs, styledTextRun{text: token.text, bold: token.bold})
	}
	return runs
}

func textGraphemes(value string) []string {
	var seg segmenter.Segmenter
	seg.InitWithString(value)
	iter := seg.GraphemeIterator()
	graphemes := make([]string, 0, len([]rune(value)))
	for iter.Next() {
		graphemes = append(graphemes, string(iter.Grapheme().Text))
	}
	return graphemes
}

func isHangulRune(r rune) bool {
	return r >= 0x1100 && r <= 0x11ff ||
		r >= 0x3130 && r <= 0x318f ||
		r >= 0xa960 && r <= 0xa97f ||
		r >= 0xac00 && r <= 0xd7af ||
		r >= 0xd7b0 && r <= 0xd7ff
}

type textFontCandidate struct {
	Family editor.TextFontFamily
	File   string
}

type parsedTextFontEntry struct {
	font *sfnt.Font
	err  error
}

var parsedTextFonts sync.Map

func textFontFace(style editor.TextStyle, value string) (font.Face, error) {
	return textFontFaceForWeight(style, value, style.Bold)
}

func textFontFaceForWeight(style editor.TextStyle, value string, bold bool) (font.Face, error) {
	windir := os.Getenv("WINDIR")
	if windir == "" {
		windir = `C:\Windows`
	}
	fonts := filepath.Join(windir, "Fonts")
	family := editor.NormalizeTextFontFamily(style.FontFamily)
	candidates := textFontCandidates(fonts, family, bold)
	for _, candidate := range candidates {
		face, err := loadTextFontFace(candidate, style.FontSize)
		if err != nil {
			continue
		}
		if textFaceSupportsHangul(face, value) {
			return face, nil
		}
		face.Close()
	}
	if strings.IndexFunc(value, isHangulRune) >= 0 {
		return nil, fmt.Errorf("no installed Korean font supports the requested text")
	}
	fallback := goregular.TTF
	if bold {
		fallback = gobold.TTF
	}
	parsed, err := opentype.Parse(fallback)
	if err != nil {
		return nil, fmt.Errorf("parse fallback text font: %w", err)
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: style.FontSize, DPI: 72, Hinting: font.HintingFull})
}

func textFontCandidates(fonts string, family editor.TextFontFamily, bold bool) []textFontCandidate {
	selected := textFontCandidate{Family: family}
	if bold && family != editor.TextFontMalgun {
		selected = textFontCandidate{Family: editor.TextFontMalgun, File: filepath.Join(fonts, "malgunbd.ttf")}
	}
	switch family {
	case editor.TextFontGulim, editor.TextFontDotum:
		if selected.File == "" {
			selected.File = filepath.Join(fonts, "gulim.ttc")
		}
	case editor.TextFontBatang, editor.TextFontGungsuh:
		if selected.File == "" {
			selected.File = filepath.Join(fonts, "batang.ttc")
		}
	default:
		selected.File = filepath.Join(fonts, "malgun.ttf")
		if bold {
			selected.File = filepath.Join(fonts, "malgunbd.ttf")
		}
	}
	candidates := []textFontCandidate{selected}
	if family == editor.TextFontMalgun && bold {
		candidates = append(candidates, textFontCandidate{Family: editor.TextFontMalgun, File: filepath.Join(fonts, "malgun.ttf")})
	}
	for _, fallback := range []textFontCandidate{
		{Family: editor.TextFontMalgun, File: filepath.Join(fonts, "malgun.ttf")},
		{Family: editor.TextFontGulim, File: filepath.Join(fonts, "gulim.ttc")},
		{Family: editor.TextFontBatang, File: filepath.Join(fonts, "batang.ttc")},
	} {
		if fallback != selected {
			candidates = append(candidates, fallback)
		}
	}
	return candidates
}

func loadTextFontFace(candidate textFontCandidate, size float64) (font.Face, error) {
	parsed, err := loadParsedTextFont(candidate)
	if err != nil {
		return nil, err
	}
	return opentype.NewFace(parsed, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
}

func loadParsedTextFont(candidate textFontCandidate) (*sfnt.Font, error) {
	if cached, ok := parsedTextFonts.Load(candidate); ok {
		entry := cached.(parsedTextFontEntry)
		return entry.font, entry.err
	}
	data, err := os.ReadFile(candidate.File)
	if err != nil {
		parsedTextFonts.Store(candidate, parsedTextFontEntry{err: err})
		return nil, err
	}
	collection, err := opentype.ParseCollection(data)
	if err != nil {
		parsedTextFonts.Store(candidate, parsedTextFontEntry{err: err})
		return nil, err
	}
	for i := 0; i < collection.NumFonts(); i++ {
		parsed, err := collection.Font(i)
		if err != nil || !fontFamilyMatches(parsed, candidate.Family) {
			continue
		}
		parsedTextFonts.Store(candidate, parsedTextFontEntry{font: parsed})
		return parsed, nil
	}
	err = fmt.Errorf("font family %q not found in %s", candidate.Family, candidate.File)
	parsedTextFonts.Store(candidate, parsedTextFontEntry{err: err})
	return nil, err
}

func fontFamilyMatches(parsed *sfnt.Font, family editor.TextFontFamily) bool {
	var buffer sfnt.Buffer
	for _, nameID := range []sfnt.NameID{sfnt.NameIDTypographicFamily, sfnt.NameIDFamily} {
		name, err := parsed.Name(&buffer, nameID)
		if err == nil && strings.EqualFold(strings.TrimSpace(name), string(family)) {
			return true
		}
	}
	return false
}

func textFaceSupportsHangul(face font.Face, value string) bool {
	for _, r := range value {
		if isHangulRune(r) {
			if _, ok := face.GlyphAdvance(r); !ok {
				return false
			}
		}
	}
	return true
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
