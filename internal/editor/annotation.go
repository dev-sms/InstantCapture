package editor

import (
	"errors"
	"slices"
)

type Tool string

type PenMode uint8

type MosaicMode uint8

const (
	ToolPen       Tool = "pen"
	ToolRectangle Tool = "rectangle"
	ToolTriangle  Tool = "triangle"
	ToolCircle    Tool = "circle"
	ToolArrow     Tool = "arrow"
	ToolLine      Tool = "line"
	ToolHighlight Tool = "highlight"
	ToolMosaic    Tool = "mosaic"
	ToolText      Tool = "text"
)

const (
	PenModeStraight PenMode = iota
	PenModeFreehand
	PenModeRectangle
)

const (
	MosaicModeStraight MosaicMode = iota
	MosaicModeFreehand
	MosaicModeRectangle
)

type TextAlignment string

type TextFontFamily string

const (
	TextAlignLeft   TextAlignment = "left"
	TextAlignCenter TextAlignment = "center"
	TextAlignRight  TextAlignment = "right"
)

const (
	TextFontMalgun  TextFontFamily = "Malgun Gothic"
	TextFontGulim   TextFontFamily = "Gulim"
	TextFontDotum   TextFontFamily = "Dotum"
	TextFontBatang  TextFontFamily = "Batang"
	TextFontGungsuh TextFontFamily = "Gungsuh"
)

type StrokeStyle struct {
	ColorRGBA [4]uint8
	Width     float64
}

type TextStyle struct {
	ColorRGBA         [4]uint8
	FontSize          float64
	FontFamily        TextFontFamily
	Bold              bool
	Alignment         TextAlignment
	LineHeight        float64
	Padding           float64
	BackgroundEnabled bool
	BackgroundRGBA    [4]uint8
	BorderEnabled     bool
	BorderRGBA        [4]uint8
	BorderWidth       float64
	CornerRadius      float64
}

type TextRun struct {
	Start int
	End   int
	Bold  bool
}

func CanonicalizeTextRuns(text string, baseBold bool, runs []TextRun) []TextRun {
	styles := textRunStyles(len([]rune(text)), baseBold, runs)
	return textRunsFromStyles(styles, baseBold)
}

func ToggleBoldTextRuns(text string, baseBold bool, runs []TextRun, start, end int) []TextRun {
	runeCount := len([]rune(text))
	if start > end {
		start, end = end, start
	}
	start = max(0, min(start, runeCount))
	end = max(0, min(end, runeCount))
	styles := textRunStyles(runeCount, baseBold, runs)
	if start == end {
		return textRunsFromStyles(styles, baseBold)
	}

	allBold := true
	for _, bold := range styles[start:end] {
		if !bold {
			allBold = false
			break
		}
	}
	for i := start; i < end; i++ {
		styles[i] = !allBold
	}
	return textRunsFromStyles(styles, baseBold)
}

func TransformTextRuns(oldText, newText string, baseBold bool, runs []TextRun) []TextRun {
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	oldStyles := textRunStyles(len(oldRunes), baseBold, runs)

	prefix := 0
	for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	oldSuffix := len(oldRunes)
	newSuffix := len(newRunes)
	for oldSuffix > prefix && newSuffix > prefix && oldRunes[oldSuffix-1] == newRunes[newSuffix-1] {
		oldSuffix--
		newSuffix--
	}

	newStyles := make([]bool, len(newRunes))
	copy(newStyles[:prefix], oldStyles[:prefix])
	insertedBold := baseBold
	if oldSuffix == prefix && prefix > 0 {
		insertedBold = oldStyles[prefix-1]
	} else if prefix < len(oldStyles) {
		insertedBold = oldStyles[prefix]
	} else if prefix > 0 {
		insertedBold = oldStyles[prefix-1]
	}
	for i := prefix; i < newSuffix; i++ {
		newStyles[i] = insertedBold
	}
	copy(newStyles[newSuffix:], oldStyles[oldSuffix:])
	return textRunsFromStyles(newStyles, baseBold)
}

func TransformTextRunsForEdit(oldText, newText string, baseBold bool, runs []TextRun, selectionStart, selectionEnd, newCaret int) []TextRun {
	oldRunes := []rune(oldText)
	newRunes := []rune(newText)
	selectionStart = max(0, min(selectionStart, len(oldRunes)))
	selectionEnd = max(0, min(selectionEnd, len(oldRunes)))
	if selectionStart > selectionEnd {
		selectionStart, selectionEnd = selectionEnd, selectionStart
	}
	newCaret = max(0, min(newCaret, len(newRunes)))
	if selectionStart == selectionEnd && len(newRunes) < len(oldRunes) {
		deleted := len(oldRunes) - len(newRunes)
		if newCaret < selectionStart {
			selectionStart = newCaret
		} else {
			selectionEnd = min(len(oldRunes), selectionStart+deleted)
		}
	}
	inserted := len(newRunes) - (len(oldRunes) - (selectionEnd - selectionStart))
	if inserted < 0 || selectionStart+inserted > len(newRunes) || !slices.Equal(oldRunes[:selectionStart], newRunes[:selectionStart]) || !slices.Equal(oldRunes[selectionEnd:], newRunes[selectionStart+inserted:]) {
		return TransformTextRuns(oldText, newText, baseBold, runs)
	}
	oldStyles := textRunStyles(len(oldRunes), baseBold, runs)
	newStyles := make([]bool, len(newRunes))
	copy(newStyles, oldStyles[:selectionStart])
	insertedBold := baseBold
	if selectionEnd > selectionStart {
		insertedBold = oldStyles[selectionStart]
	} else if selectionStart > 0 {
		insertedBold = oldStyles[selectionStart-1]
	} else if selectionEnd < len(oldStyles) {
		insertedBold = oldStyles[selectionEnd]
	}
	for i := selectionStart; i < selectionStart+inserted; i++ {
		newStyles[i] = insertedBold
	}
	copy(newStyles[selectionStart+inserted:], oldStyles[selectionEnd:])
	return textRunsFromStyles(newStyles, baseBold)
}

func BoldAtRuneOffset(text string, baseBold bool, runs []TextRun, offset int) bool {
	styles := textRunStyles(len([]rune(text)), baseBold, runs)
	if offset < 0 || offset >= len(styles) {
		return baseBold
	}
	return styles[offset]
}

func textRunStyles(runeCount int, baseBold bool, runs []TextRun) []bool {
	styles := make([]bool, runeCount)
	if baseBold {
		for i := range styles {
			styles[i] = true
		}
	}
	for _, run := range runs {
		start, end := run.Start, run.End
		if start > end {
			start, end = end, start
		}
		start = max(0, min(start, runeCount))
		end = max(0, min(end, runeCount))
		for i := start; i < end; i++ {
			styles[i] = run.Bold
		}
	}
	return styles
}

func textRunsFromStyles(styles []bool, baseBold bool) []TextRun {
	if len(styles) == 0 {
		return nil
	}
	runs := make([]TextRun, 0, 4)
	start := 0
	for i := 1; i <= len(styles); i++ {
		if i == len(styles) || styles[i] != styles[start] {
			runs = append(runs, TextRun{Start: start, End: i, Bold: styles[start]})
			start = i
		}
	}
	if len(runs) == 1 && runs[0].Bold == baseBold {
		return nil
	}
	return runs
}

func DefaultTextStyle() TextStyle {
	return TextStyle{
		ColorRGBA:      [4]uint8{0, 0, 0, 255},
		FontSize:       32,
		FontFamily:     TextFontMalgun,
		Alignment:      TextAlignLeft,
		LineHeight:     1.22,
		Padding:        8,
		BackgroundRGBA: [4]uint8{255, 255, 255, 255},
		BorderRGBA:     [4]uint8{15, 23, 42, 255},
		BorderWidth:    1,
		CornerRadius:   8,
	}
}

func EffectiveTextLineHeight(style TextStyle) float64 {
	if style.LineHeight <= 0 {
		return 1.22
	}
	return style.LineHeight
}

func EffectiveTextPadding(style TextStyle) float64 {
	if style.Padding <= 0 {
		return 8
	}
	return style.Padding
}

func NormalizeTextFontFamily(family TextFontFamily) TextFontFamily {
	switch family {
	case TextFontMalgun, TextFontGulim, TextFontDotum, TextFontBatang, TextFontGungsuh:
		return family
	default:
		return TextFontMalgun
	}
}

func TextRunsUseBold(baseBold bool, runs []TextRun) bool {
	if runs == nil {
		return baseBold
	}
	for _, run := range runs {
		if run.Bold {
			return true
		}
	}
	return false
}

func EffectiveRichTextFontFamily(style TextStyle, runs []TextRun) TextFontFamily {
	family := NormalizeTextFontFamily(style.FontFamily)
	if family != TextFontMalgun && TextRunsUseBold(style.Bold, runs) {
		return TextFontMalgun
	}
	return family
}

func DefaultStrokeStyle() StrokeStyle {
	return StrokeStyle{ColorRGBA: [4]uint8{255, 64, 64, 255}, Width: 3}
}

type Annotation struct {
	ID              string
	Tool            Tool
	Points          []Point
	Rect            Rect
	Style           StrokeStyle
	Text            string
	TextStyle       TextStyle
	TextRuns        []TextRun
	PenMode         PenMode
	MosaicMode      MosaicMode
	MosaicBlockSize int
}

type RenderCommand struct {
	Tool            Tool
	Points          []Point
	Rect            Rect
	Style           StrokeStyle
	Text            string
	TextStyle       TextStyle
	TextRuns        []TextRun
	PenMode         PenMode
	MosaicMode      MosaicMode
	MosaicBlockSize int
}

func (a Annotation) Validate() error {
	if a.ID == "" {
		return errors.New("annotation id is required")
	}

	switch a.Tool {
	case ToolPen:
		switch a.PenMode {
		case PenModeStraight, PenModeFreehand:
			if len(a.Points) < 2 {
				return errors.New("path annotation requires at least two points")
			}
		case PenModeRectangle:
			if a.Rect.Empty() {
				return errors.New("rectangle pen requires a non-empty rect")
			}
		default:
			return errors.New("unsupported pen mode")
		}
	case ToolHighlight:
		if len(a.Points) < 2 {
			return errors.New("path annotation requires at least two points")
		}
	case ToolMosaic:
		switch a.MosaicMode {
		case MosaicModeStraight, MosaicModeFreehand:
			if len(a.Points) < 2 {
				return errors.New("path annotation requires at least two points")
			}
		case MosaicModeRectangle:
			if a.Rect.Empty() {
				return errors.New("rectangle mosaic requires a non-empty rect")
			}
		default:
			return errors.New("unsupported mosaic mode")
		}
	case ToolRectangle, ToolTriangle, ToolCircle:
		if a.Rect.Empty() {
			return errors.New("shape annotation requires a non-empty rect")
		}
	case ToolArrow, ToolLine:
		if a.Rect.Min == a.Rect.Max {
			return errors.New("line annotation requires distinct endpoints")
		}
	case ToolText:
		if a.Rect.Empty() {
			return errors.New("text annotation requires a non-empty rect")
		}
		if a.Text == "" {
			return errors.New("text annotation requires text")
		}
		if a.TextStyle.FontSize <= 0 {
			return errors.New("text font size must be positive")
		}
		if a.TextStyle.LineHeight < 0 || a.TextStyle.Padding < 0 || a.TextStyle.BorderWidth < 0 || a.TextStyle.CornerRadius < 0 {
			return errors.New("text box metrics must not be negative")
		}
		if a.TextStyle.BorderEnabled && a.TextStyle.BorderWidth <= 0 {
			return errors.New("enabled text border width must be positive")
		}
		if a.TextStyle.FontFamily != "" && NormalizeTextFontFamily(a.TextStyle.FontFamily) != a.TextStyle.FontFamily {
			return errors.New("unsupported text font family")
		}
		switch a.TextStyle.Alignment {
		case TextAlignLeft, TextAlignCenter, TextAlignRight:
		default:
			return errors.New("unsupported text alignment")
		}
		if err := validateTextRuns(a.Text, a.TextRuns); err != nil {
			return err
		}
	default:
		return errors.New("unsupported annotation tool")
	}

	if a.Tool != ToolText && a.Style.Width <= 0 {
		return errors.New("stroke width must be positive")
	}
	if a.Tool == ToolMosaic && a.MosaicBlockSize <= 0 {
		return errors.New("mosaic block size must be positive")
	}

	return nil
}

func validateTextRuns(text string, runs []TextRun) error {
	if runs == nil {
		return nil
	}
	runeCount := len([]rune(text))
	if len(runs) == 0 {
		return errors.New("explicit text runs must not be empty")
	}
	next := 0
	for i, run := range runs {
		if run.Start != next || run.End <= run.Start || run.End > runeCount {
			return errors.New("text runs must completely and contiguously cover the text")
		}
		if i > 0 && runs[i-1].Bold == run.Bold {
			return errors.New("adjacent text runs with equal style must be merged")
		}
		next = run.End
	}
	if next != runeCount {
		return errors.New("text runs must completely and contiguously cover the text")
	}
	return nil
}

func (a Annotation) RenderCommand() RenderCommand {
	rect := a.Rect
	if a.Tool == ToolRectangle || a.Tool == ToolTriangle || a.Tool == ToolCircle || a.Tool == ToolText || (a.Tool == ToolPen && a.PenMode == PenModeRectangle) || (a.Tool == ToolMosaic && a.MosaicMode == MosaicModeRectangle) {
		rect = rect.Normalize()
	}
	textRuns := append([]TextRun(nil), a.TextRuns...)
	return RenderCommand{Tool: a.Tool, Points: append([]Point(nil), a.Points...), Rect: rect, Style: a.Style, Text: a.Text, TextStyle: a.TextStyle, TextRuns: textRuns, PenMode: a.PenMode, MosaicMode: a.MosaicMode, MosaicBlockSize: a.MosaicBlockSize}
}
