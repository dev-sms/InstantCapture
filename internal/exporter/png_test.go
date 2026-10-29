package exporter

import (
	"bytes"
	"image"
	"image/color"
	"image/draw"
	"math"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	giofont "gioui.org/font"
	gioopentype "gioui.org/font/opentype"
	"gioui.org/io/system"
	"gioui.org/layout"
	"gioui.org/op"
	giotext "gioui.org/text"
	"gioui.org/unit"
	"gioui.org/widget"
	xfont "golang.org/x/image/font"

	"instantcapture/internal/editor"
)

func TestRenderTextLayerMatchesFullTextRendering(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 180, 100))
	draw.Draw(base, base.Bounds(), &image.Uniform{C: color.NRGBA{R: 24, G: 32, B: 48, A: 255}}, image.Point{}, draw.Src)
	style := editor.DefaultTextStyle()
	style.BackgroundEnabled = true
	style.BackgroundRGBA = [4]uint8{37, 99, 235, 128}
	style.BorderEnabled = true
	style.BorderRGBA = [4]uint8{255, 255, 255, 255}
	command := editor.RenderCommand{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 23, Y: 17}, editor.Point{X: 153, Y: 83}),
		Text:      "Layer test",
		TextStyle: style,
		TextRuns:  []editor.TextRun{{Start: 0, End: 5, Bold: true}},
	}

	full, err := RenderAnnotations(base, []editor.RenderCommand{command})
	if err != nil {
		t.Fatal(err)
	}
	layer, err := RenderTextLayer(command)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := layer.Bounds(), (image.Rectangle{Max: command.Rect.Normalize().ImageRect().Size()}); got != want {
		t.Fatalf("layer bounds = %v, want %v", got, want)
	}
	composed := image.NewRGBA(base.Bounds())
	draw.Draw(composed, composed.Bounds(), base, base.Bounds().Min, draw.Src)
	draw.Draw(composed, command.Rect.Normalize().ImageRect(), layer, image.Point{}, draw.Over)
	if !bytes.Equal(composed.Pix, full.Pix) {
		maxDelta := 0
		for i := range composed.Pix {
			delta := int(composed.Pix[i]) - int(full.Pix[i])
			if delta < 0 {
				delta = -delta
			}
			maxDelta = max(maxDelta, delta)
		}
		if maxDelta > 1 {
			t.Fatalf("text layer composition differs from full rendering by up to %d", maxDelta)
		}
	}
}

func TestSaveImageUsesPathFormat(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 8, 6))
	for _, extension := range []string{".png", ".jpg", ".jpeg", ".gif", ".bmp"} {
		t.Run(extension, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "saved"+extension)
			if err := SaveImage(path, img); err != nil {
				t.Fatal(err)
			}
			file, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer file.Close()
			decoded, _, err := image.Decode(file)
			if err != nil {
				t.Fatal(err)
			}
			if decoded.Bounds().Size() != img.Bounds().Size() {
				t.Fatalf("saved size = %v, want %v", decoded.Bounds().Size(), img.Bounds().Size())
			}
		})
	}
}

func TestSaveImageRejectsUnsupportedExtension(t *testing.T) {
	path := filepath.Join(t.TempDir(), "saved.webp")
	if SupportsSavePath(path) {
		t.Fatal("webp should not be reported as writable")
	}
	if err := SaveImage(path, image.NewRGBA(image.Rect(0, 0, 1, 1))); err == nil {
		t.Fatal("expected unsupported format error")
	}
}

func TestAnimatedFormatIsNotOverwrittenDirectly(t *testing.T) {
	if SupportsDirectSavePath("animation.gif") {
		t.Fatal("animated GIF sources must use Save As")
	}
	for _, path := range []string{"image.png", "image.jpg", "image.jpeg", "image.bmp"} {
		if !SupportsDirectSavePath(path) {
			t.Fatalf("expected direct save support for %q", path)
		}
	}
}

func TestSaveJPEGFlattensTransparencyOnWhite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "transparent.jpg")
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	if err := SaveImage(path, img); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	decoded, _, err := image.Decode(file)
	if err != nil {
		t.Fatal(err)
	}
	got := color.NRGBAModel.Convert(decoded.At(4, 4)).(color.NRGBA)
	if got.R < 245 || got.G < 245 || got.B < 245 {
		t.Fatalf("transparent JPEG background = %#v, want white", got)
	}
}

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

func TestRenderShapeAnnotations(t *testing.T) {
	for _, tool := range []editor.Tool{editor.ToolTriangle, editor.ToolCircle} {
		t.Run(string(tool), func(t *testing.T) {
			base := image.NewRGBA(image.Rect(0, 0, 80, 60))
			img, err := RenderAnnotations(base, []editor.RenderCommand{{
				Tool:  tool,
				Rect:  editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 60, Y: 45}),
				Style: editor.StrokeStyle{ColorRGBA: [4]uint8{255, 0, 0, 255}, Width: 3},
			}})
			if err != nil {
				t.Fatal(err)
			}
			visible := 0
			for y := 0; y < img.Bounds().Dy(); y++ {
				for x := 0; x < img.Bounds().Dx(); x++ {
					if color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A != 0 {
						visible++
					}
				}
			}
			if visible == 0 {
				t.Fatalf("%s rendered no visible pixels", tool)
			}
		})
	}
}

func TestRenderKoreanAndEnglishTextAnnotation(t *testing.T) {
	style := editor.TextStyle{ColorRGBA: [4]uint8{255, 255, 255, 255}, FontSize: 28, Alignment: editor.TextAlignLeft}
	face, err := textFontFace(style, "한글 입력")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := face.GlyphAdvance('한'); !ok {
		face.Close()
		t.Fatal("selected text font does not contain Korean glyphs")
	}
	face.Close()

	for _, value := range []string{"한글 입력", "English 123"} {
		t.Run(value, func(t *testing.T) {
			base := image.NewRGBA(image.Rect(0, 0, 320, 120))
			img, err := RenderAnnotations(base, []editor.RenderCommand{{
				Tool:      editor.ToolText,
				Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 310, Y: 110}),
				Text:      value,
				TextStyle: style,
			}})
			if err != nil {
				t.Fatal(err)
			}
			visible := 0
			for y := 0; y < img.Bounds().Dy(); y++ {
				for x := 0; x < img.Bounds().Dx(); x++ {
					if color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A != 0 {
						visible++
					}
				}
			}
			if visible == 0 {
				t.Fatalf("text renderer produced no visible pixels for %q", value)
			}
		})
	}
}

func TestWindowsKoreanFontFamiliesLoadSelectedFace(t *testing.T) {
	windir := os.Getenv("WINDIR")
	if windir == "" {
		t.Skip("Windows fonts are unavailable")
	}
	fonts := filepath.Join(windir, "Fonts")
	for _, candidate := range []textFontCandidate{
		{Family: editor.TextFontMalgun, File: filepath.Join(fonts, "malgun.ttf")},
		{Family: editor.TextFontGulim, File: filepath.Join(fonts, "gulim.ttc")},
		{Family: editor.TextFontDotum, File: filepath.Join(fonts, "gulim.ttc")},
		{Family: editor.TextFontBatang, File: filepath.Join(fonts, "batang.ttc")},
		{Family: editor.TextFontGungsuh, File: filepath.Join(fonts, "batang.ttc")},
	} {
		t.Run(string(candidate.Family), func(t *testing.T) {
			face, err := loadTextFontFace(candidate, 28)
			if err != nil {
				t.Fatal(err)
			}
			defer face.Close()
			if _, ok := face.GlyphAdvance('한'); !ok {
				t.Fatalf("font %q lacks Hangul", candidate.Family)
			}
		})
	}
}

func TestLegacyKoreanBoldUsesRealMalgunBoldFallback(t *testing.T) {
	fonts := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	for _, family := range []editor.TextFontFamily{editor.TextFontGulim, editor.TextFontDotum, editor.TextFontBatang, editor.TextFontGungsuh} {
		candidates := textFontCandidates(fonts, family, true)
		if len(candidates) == 0 || candidates[0].Family != editor.TextFontMalgun || filepath.Base(candidates[0].File) != "malgunbd.ttf" {
			t.Fatalf("bold %q candidates = %#v, want Malgun real bold first", family, candidates)
		}
	}
}

func TestBoldRightAlignmentStaysInsideTextBox(t *testing.T) {
	style := editor.DefaultTextStyle()
	style.FontFamily = editor.TextFontGulim
	style.Bold = true
	style.Alignment = editor.TextAlignRight
	base := image.NewRGBA(image.Rect(0, 0, 240, 100))
	textRect := editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 200, Y: 90})
	img, err := RenderAnnotations(base, []editor.RenderCommand{{Tool: editor.ToolText, Rect: textRect, Text: "Bold text", TextStyle: style}})
	if err != nil {
		t.Fatal(err)
	}
	maxX := -1
	for y := 0; y < img.Bounds().Dy(); y++ {
		for x := 0; x < img.Bounds().Dx(); x++ {
			if color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A != 0 {
				maxX = max(maxX, x)
			}
		}
	}
	if maxX >= textRect.Normalize().Max.X {
		t.Fatalf("right-aligned bold reached x=%d outside %d", maxX, textRect.Normalize().Max.X)
	}
}

func TestMixedBoldRunsAndTextBoxStyleRender(t *testing.T) {
	style := editor.DefaultTextStyle()
	style.BackgroundEnabled = true
	style.BackgroundRGBA = [4]uint8{255, 255, 255, 255}
	style.BorderEnabled = true
	style.BorderRGBA = [4]uint8{255, 0, 0, 255}
	style.BorderWidth = 2
	base := image.NewRGBA(image.Rect(0, 0, 280, 100))
	img, err := RenderAnnotations(base, []editor.RenderCommand{{
		Tool:      editor.ToolText,
		Rect:      editor.NewRect(editor.Point{X: 10, Y: 10}, editor.Point{X: 270, Y: 90}),
		Text:      "regular 굵게 regular",
		TextStyle: style,
		TextRuns: []editor.TextRun{
			{Start: 0, End: 8, Bold: false},
			{Start: 8, End: 10, Bold: true},
			{Start: 10, End: 18, Bold: false},
		},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(12, 12)).(color.NRGBA); got.R < 200 || got.A == 0 {
		t.Fatalf("text background was not rendered: %#v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(10, 30)).(color.NRGBA); got.R < 150 || got.G > 100 {
		t.Fatalf("text border was not rendered: %#v", got)
	}
}

func TestMixedBoldGioCaretSelectionWrapAndPNGParity(t *testing.T) {
	style := editor.DefaultTextStyle()
	style.FontSize = 28
	style.Padding = 1
	value := "가나다라마바사"
	runs := []editor.TextRun{
		{Start: 0, End: 2, Bold: false},
		{Start: 2, End: 5, Bold: true},
		{Start: 5, End: 7, Bold: false},
	}
	fonts := filepath.Join(os.Getenv("WINDIR"), "Fonts")
	candidate := textFontCandidates(fonts, editor.EffectiveRichTextFontFamily(style, runs), false)[0]
	regular, err := loadTextFontFace(candidate, style.FontSize)
	if err != nil {
		t.Fatal(err)
	}
	defer regular.Close()
	fontData, err := os.ReadFile(candidate.File)
	if err != nil {
		t.Fatal(err)
	}
	gioFaces, err := gioopentype.ParseCollection(fontData)
	if err != nil {
		t.Fatal(err)
	}
	shaper := giotext.NewShaper(giotext.NoSystemFonts(), giotext.WithCollection(gioFaces))
	threeGlyphs := float64(xfont.MeasureString(regular, "가나다")) / 64
	contentWidth := int(math.Ceil(threeGlyphs))
	exporterLines := wrapStyledTextLines(value, runs, style.Bold, regular, float64(contentWidth))
	exporterStarts := []int{0}
	offset := 0
	for i, line := range exporterLines {
		if i > 0 {
			exporterStarts = append(exporterStarts, offset)
		}
		for _, run := range line.runs {
			offset += len([]rune(run.text))
		}
	}

	gioEditor := widget.Editor{WrapPolicy: giotext.WrapGraphemes}
	gioEditor.SetText(value)
	gtx := layout.Context{
		Ops:         new(op.Ops),
		Constraints: layout.Exact(image.Pt(contentWidth, 500)),
		Locale:      system.Locale{Language: "ko", Direction: system.LTR},
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
	}
	gioEditor.Layout(gtx, shaper, giofont.Font{Typeface: giofont.Typeface(editor.TextFontMalgun)}, unit.Sp(style.FontSize), op.CallOp{}, op.CallOp{})
	gioStarts := []int{0}
	previousY := float32(0)
	for runeOffset := 0; runeOffset <= len([]rune(value)); runeOffset++ {
		gioEditor.SetCaret(runeOffset, runeOffset)
		y := gioEditor.CaretCoords().Y
		if runeOffset == 0 {
			previousY = y
		} else if y != previousY {
			gioStarts = append(gioStarts, runeOffset)
			previousY = y
		}
	}
	if !reflect.DeepEqual(gioStarts, exporterStarts) {
		t.Fatalf("Gio wrap starts = %v, PNG wrap starts = %v", gioStarts, exporterStarts)
	}

	firstLineEnd := len([]rune(value))
	if len(exporterStarts) > 1 {
		firstLineEnd = exporterStarts[1]
	}
	caretX := make(map[int]float64, firstLineEnd)
	for runeOffset := 0; runeOffset < firstLineEnd; runeOffset++ {
		gioEditor.SetCaret(runeOffset, runeOffset)
		gotX := float64(gioEditor.CaretCoords().X)
		caretX[runeOffset] = gotX
		wantX := float64(xfont.MeasureString(regular, string([]rune(value)[:runeOffset]))) / 64
		if math.Abs(gotX-wantX) > 2 {
			t.Fatalf("caret x at rune %d = %.2f, PNG advance = %.2f", runeOffset, gotX, wantX)
		}
	}
	selectionEndWant := min(2, firstLineEnd-1)
	gioEditor.SetCaret(1, selectionEndWant)
	selectionStart, selectionEnd := gioEditor.Selection()
	if selectionStart != 1 || selectionEnd != selectionEndWant {
		t.Fatalf("Gio selection = %d..%d", selectionStart, selectionEnd)
	}
	selectionWidth := caretX[selectionEndWant] - caretX[1]
	wantSelectionWidth := float64(xfont.MeasureString(regular, string([]rune(value)[1:selectionEndWant]))) / 64
	if math.Abs(selectionWidth-wantSelectionWidth) > 2 {
		t.Fatalf("selection highlight width = %.2f, PNG advance = %.2f", selectionWidth, wantSelectionWidth)
	}

	lineHeight := math.Max(style.FontSize*editor.EffectiveTextLineHeight(style), float64(regular.Metrics().Height.Ceil()))
	textRect := editor.NewRect(editor.Point{}, editor.Point{X: contentWidth + 2, Y: int(math.Ceil(lineHeight*float64(len(exporterLines)))) + 2})
	img, err := RenderAnnotations(image.NewRGBA(image.Rect(0, 0, textRect.Width(), textRect.Height())), []editor.RenderCommand{{
		Tool:      editor.ToolText,
		Rect:      textRect,
		Text:      value,
		TextStyle: style,
		TextRuns:  runs,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if got := alphaRowBands(img); got != len(exporterLines) {
		t.Fatalf("PNG ink line bands = %d, Gio/exporter lines = %d", got, len(exporterLines))
	}
	regularImg, err := RenderAnnotations(image.NewRGBA(img.Bounds()), []editor.RenderCommand{{
		Tool:      editor.ToolText,
		Rect:      textRect,
		Text:      value,
		TextStyle: style,
	}})
	if err != nil {
		t.Fatal(err)
	}
	if changed := differingPixels(img, regularImg); changed < 20 {
		t.Fatalf("mixed-bold PNG differs from all-regular PNG at only %d pixels", changed)
	}
}

func alphaRowBands(img image.Image) int {
	bands := 0
	inside := false
	for y := img.Bounds().Min.Y; y < img.Bounds().Max.Y; y++ {
		hasAlpha := false
		for x := img.Bounds().Min.X; x < img.Bounds().Max.X; x++ {
			if color.NRGBAModel.Convert(img.At(x, y)).(color.NRGBA).A != 0 {
				hasAlpha = true
				break
			}
		}
		if hasAlpha && !inside {
			bands++
		}
		inside = hasAlpha
	}
	return bands
}

func differingPixels(a, b image.Image) int {
	bounds := a.Bounds().Intersect(b.Bounds())
	changed := 0
	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			if color.NRGBAModel.Convert(a.At(x, y)).(color.NRGBA) != color.NRGBAModel.Convert(b.At(x, y)).(color.NRGBA) {
				changed++
			}
		}
	}
	return changed
}

func TestTextGraphemesKeepCombiningAndEmojiSequencesTogether(t *testing.T) {
	got := textGraphemes("e\u0301👨‍👩‍👧‍👦한")
	want := []string{"e\u0301", "👨‍👩‍👧‍👦", "한"}
	if len(got) != len(want) {
		t.Fatalf("graphemes = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("graphemes = %#v, want %#v", got, want)
		}
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

func TestRenderAnnotationsCroppedWithBorder(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 10, 8))
	base.Set(2, 2, color.NRGBA{R: 10, G: 20, B: 30, A: 255})

	img, err := RenderAnnotationsCroppedWithBorder(base, nil, image.Rect(2, 2, 7, 6), BorderOptions{
		Enabled: true,
		Width:   2,
		Color:   color.NRGBA{R: 1, G: 2, B: 3, A: 255},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := img.Bounds(), image.Rect(0, 0, 9, 8); got != want {
		t.Fatalf("bounds = %v, want %v", got, want)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 1, G: 2, B: 3, A: 255}) {
		t.Fatalf("border pixel = %#v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(2, 2)).(color.NRGBA); got != (color.NRGBA{R: 10, G: 20, B: 30, A: 255}) {
		t.Fatalf("content pixel = %#v", got)
	}
}

func TestHighlighterMultiplyPreservesDarkText(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 20, 20))
	draw.Draw(base, base.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	base.Set(10, 10, color.Black)
	command := editor.RenderCommand{
		Tool:   editor.ToolHighlight,
		Points: []editor.Point{{X: 2, Y: 10}, {X: 18, Y: 10}},
		Style:  editor.StrokeStyle{ColorRGBA: [4]uint8{250, 204, 21, 180}, Width: 8},
	}

	img, err := RenderAnnotations(base, []editor.RenderCommand{command})
	if err != nil {
		t.Fatal(err)
	}
	if got := color.NRGBAModel.Convert(img.At(10, 10)).(color.NRGBA); got != (color.NRGBA{A: 255}) {
		t.Fatalf("dark text changed under highlighter: %#v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(5, 10)).(color.NRGBA); got.R >= 255 || got.G >= 255 || got.B >= 255 {
		t.Fatalf("light background was not highlighted: %#v", got)
	}
	if got := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); got != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Fatalf("pixel outside highlighter changed: %#v", got)
	}
}

func TestRectanglePenFillsSelectedAreaIndependentOfWidth(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 12, 10))
	draw.Draw(base, base.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	var rendered []*image.RGBA
	for _, width := range []float64{1, 40} {
		img, err := RenderAnnotations(base, []editor.RenderCommand{{
			Tool:    editor.ToolPen,
			Rect:    editor.Rect{Min: editor.Point{X: 10, Y: 8}, Max: editor.Point{X: 2, Y: 2}},
			Style:   editor.StrokeStyle{ColorRGBA: [4]uint8{220, 30, 40, 255}, Width: width},
			PenMode: editor.PenModeRectangle,
		}})
		if err != nil {
			t.Fatal(err)
		}
		rendered = append(rendered, img)
	}
	if got := color.NRGBAModel.Convert(rendered[0].At(4, 4)).(color.NRGBA); got != (color.NRGBA{R: 220, G: 30, B: 40, A: 255}) {
		t.Fatalf("pixel inside rectangle pen = %#v", got)
	}
	if got := color.NRGBAModel.Convert(rendered[0].At(1, 1)).(color.NRGBA); got != (color.NRGBA{R: 255, G: 255, B: 255, A: 255}) {
		t.Fatalf("pixel outside rectangle pen changed: %#v", got)
	}
	if !bytes.Equal(rendered[0].Pix, rendered[1].Pix) {
		t.Fatal("rectangle pen fill changed with stroke width")
	}
}

func TestMosaicPixelatesOnlyInsideStroke(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 12, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 12; x++ {
			value := uint8(x*18 + y)
			base.Set(x, y, color.NRGBA{R: value, G: 255 - value, B: uint8(y * 20), A: 255})
		}
	}
	originalTop := color.NRGBAModel.Convert(base.At(1, 0)).(color.NRGBA)
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Points:          []editor.Point{{X: 1, Y: 4}, {X: 10, Y: 4}},
		Style:           editor.StrokeStyle{Width: 5},
		MosaicBlockSize: 4,
	}

	img, err := RenderAnnotations(base, []editor.RenderCommand{command})
	if err != nil {
		t.Fatal(err)
	}
	first := color.NRGBAModel.Convert(img.At(1, 4)).(color.NRGBA)
	second := color.NRGBAModel.Convert(img.At(2, 4)).(color.NRGBA)
	if first != second {
		t.Fatalf("pixels in the same mosaic block differ: %#v != %#v", first, second)
	}
	if got := color.NRGBAModel.Convert(img.At(1, 0)).(color.NRGBA); got != originalTop {
		t.Fatalf("pixel outside mosaic stroke changed: %#v != %#v", got, originalTop)
	}
}

func TestMosaicCropMatchesFullPreview(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 16, 8))
	for y := 0; y < 8; y++ {
		for x := 0; x < 16; x++ {
			base.Set(x, y, color.NRGBA{R: uint8(x * 15), G: uint8(y * 30), B: uint8((x + y) * 10), A: 255})
		}
	}
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Points:          []editor.Point{{X: 0, Y: 4}, {X: 15, Y: 4}},
		Style:           editor.StrokeStyle{Width: 6},
		MosaicBlockSize: 6,
	}
	full, err := RenderAnnotations(base, []editor.RenderCommand{command})
	if err != nil {
		t.Fatal(err)
	}
	crop := image.Rect(4, 1, 13, 7)
	cropped, err := RenderAnnotationsCropped(base, []editor.RenderCommand{command}, crop)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < cropped.Bounds().Dy(); y++ {
		for x := 0; x < cropped.Bounds().Dx(); x++ {
			got := color.NRGBAModel.Convert(cropped.At(x, y)).(color.NRGBA)
			want := color.NRGBAModel.Convert(full.At(x+crop.Min.X, y+crop.Min.Y)).(color.NRGBA)
			if got != want {
				t.Fatalf("crop pixel (%d,%d) = %#v, want %#v", x, y, got, want)
			}
		}
	}
}

func TestRectangleMosaicPixelatesOnlySelectedArea(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 12, 10))
	for y := 0; y < 10; y++ {
		for x := 0; x < 12; x++ {
			base.Set(x, y, color.NRGBA{R: uint8(x * 18), G: uint8(y * 20), B: uint8((x + y) * 9), A: 255})
		}
	}
	originalOutside := color.NRGBAModel.Convert(base.At(1, 1)).(color.NRGBA)
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Rect:            editor.Rect{Min: editor.Point{X: 10, Y: 8}, Max: editor.Point{X: 2, Y: 2}},
		Style:           editor.StrokeStyle{Width: 20},
		MosaicMode:      editor.MosaicModeRectangle,
		MosaicBlockSize: 4,
	}

	img, err := RenderAnnotations(base, []editor.RenderCommand{command})
	if err != nil {
		t.Fatal(err)
	}
	if first, second := color.NRGBAModel.Convert(img.At(4, 4)).(color.NRGBA), color.NRGBAModel.Convert(img.At(5, 4)).(color.NRGBA); first != second {
		t.Fatalf("pixels in the selected mosaic block differ: %#v != %#v", first, second)
	}
	if got := color.NRGBAModel.Convert(img.At(1, 1)).(color.NRGBA); got != originalOutside {
		t.Fatalf("pixel outside rectangle mosaic changed: %#v != %#v", got, originalOutside)
	}
	if got, original := color.NRGBAModel.Convert(img.At(2, 2)).(color.NRGBA), color.NRGBAModel.Convert(base.At(2, 2)).(color.NRGBA); got == original {
		t.Fatalf("pixel inside rectangle mosaic did not change: %#v", got)
	}
}

func TestRectangleMosaicCropMatchesFullPreview(t *testing.T) {
	base := image.NewRGBA(image.Rect(0, 0, 16, 12))
	for y := 0; y < 12; y++ {
		for x := 0; x < 16; x++ {
			base.Set(x, y, color.NRGBA{R: uint8(x * 15), G: uint8(y * 20), B: uint8((x + y) * 8), A: 255})
		}
	}
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Rect:            editor.NewRect(editor.Point{X: 2, Y: 2}, editor.Point{X: 14, Y: 10}),
		Style:           editor.StrokeStyle{Width: 20},
		MosaicMode:      editor.MosaicModeRectangle,
		MosaicBlockSize: 5,
	}
	full, err := RenderAnnotations(base, []editor.RenderCommand{command})
	if err != nil {
		t.Fatal(err)
	}
	crop := image.Rect(4, 3, 13, 9)
	cropped, err := RenderAnnotationsCropped(base, []editor.RenderCommand{command}, crop)
	if err != nil {
		t.Fatal(err)
	}
	for y := 0; y < cropped.Bounds().Dy(); y++ {
		for x := 0; x < cropped.Bounds().Dx(); x++ {
			got := color.NRGBAModel.Convert(cropped.At(x, y)).(color.NRGBA)
			want := color.NRGBAModel.Convert(full.At(x+crop.Min.X, y+crop.Min.Y)).(color.NRGBA)
			if got != want {
				t.Fatalf("crop pixel (%d,%d) = %#v, want %#v", x, y, got, want)
			}
		}
	}
}

func TestMosaicLongDiagonalVisitsStrokeCorridorNotBoundingBox(t *testing.T) {
	origins := mosaicBlockOrigins(
		mosaicSegment{from: editor.Point{X: 0, Y: 0}, to: editor.Point{X: 3840, Y: 2160}},
		12,
		14,
	)
	if len(origins) == 0 || len(origins) > 12000 {
		t.Fatalf("candidate mosaic blocks = %d, expected a bounded stroke corridor", len(origins))
	}
}

func BenchmarkMosaic4KDiagonal(b *testing.B) {
	base := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Points:          []editor.Point{{X: 0, Y: 0}, {X: 3839, Y: 2159}},
		Style:           editor.StrokeStyle{Width: 24},
		MosaicBlockSize: 12,
	}
	b.ResetTimer()
	for range b.N {
		if _, err := RenderAnnotations(base, []editor.RenderCommand{command}); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkMosaic4KPointPreview(b *testing.B) {
	base := image.NewRGBA(image.Rect(0, 0, 3840, 2160))
	point := editor.Point{X: 1920, Y: 1080}
	command := editor.RenderCommand{
		Tool:            editor.ToolMosaic,
		Points:          []editor.Point{point, point},
		Style:           editor.StrokeStyle{Width: 24},
		MosaicBlockSize: 12,
	}
	b.ResetTimer()
	for range b.N {
		if _, err := RenderAnnotations(base, []editor.RenderCommand{command}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestEncodePNGRejectsNil(t *testing.T) {
	if _, err := EncodePNG(nil); err == nil {
		t.Fatal("expected nil image error")
	}
}
