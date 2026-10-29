package editor

import (
	"reflect"
	"testing"
)

func TestEditorSelectionFlow(t *testing.T) {
	ed := NewEditor()
	ed.BeginSelection(Point{X: 30, Y: 40})

	if err := ed.UpdateSelection(Point{X: 10, Y: 15}); err != nil {
		t.Fatal(err)
	}

	selection, err := ed.CommitSelection()
	if err != nil {
		t.Fatal(err)
	}

	if selection != (Rect{Min: Point{X: 10, Y: 15}, Max: Point{X: 30, Y: 40}}) {
		t.Fatalf("unexpected selection: %#v", selection)
	}

	if ed.State != StateCaptured {
		t.Fatalf("unexpected state: %s", ed.State)
	}
}

func TestEditorAnnotationFlow(t *testing.T) {
	ed := NewEditor()
	ed.BeginSelection(Point{X: 0, Y: 0})
	if err := ed.UpdateSelection(Point{X: 100, Y: 80}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.CommitSelection(); err != nil {
		t.Fatal(err)
	}
	if err := ed.StartEditing(); err != nil {
		t.Fatal(err)
	}

	annotation := Annotation{
		ID:    "rect-1",
		Tool:  ToolRectangle,
		Rect:  NewRect(Point{X: 5, Y: 5}, Point{X: 50, Y: 40}),
		Style: DefaultStrokeStyle(),
	}

	if err := ed.AddAnnotation(annotation); err != nil {
		t.Fatal(err)
	}

	commands := ed.RenderCommands()
	if len(commands) != 1 || commands[0].Tool != ToolRectangle {
		t.Fatalf("unexpected commands: %#v", commands)
	}
}

func TestArrowRenderCommandPreservesDirection(t *testing.T) {
	annotation := Annotation{
		ID:    "arrow-1",
		Tool:  ToolArrow,
		Rect:  Rect{Min: Point{X: 80, Y: 60}, Max: Point{X: 10, Y: 20}},
		Style: DefaultStrokeStyle(),
	}

	if err := annotation.Validate(); err != nil {
		t.Fatal(err)
	}

	command := annotation.RenderCommand()
	if command.Rect.Min != annotation.Rect.Min || command.Rect.Max != annotation.Rect.Max {
		t.Fatalf("arrow direction was not preserved: %#v", command.Rect)
	}
}

func TestShapeAnnotationsNormalizeRenderRect(t *testing.T) {
	for _, tool := range []Tool{ToolRectangle, ToolTriangle, ToolCircle} {
		annotation := Annotation{
			ID:    string(tool),
			Tool:  tool,
			Rect:  Rect{Min: Point{X: 40, Y: 30}, Max: Point{X: 10, Y: 5}},
			Style: DefaultStrokeStyle(),
		}
		if err := annotation.Validate(); err != nil {
			t.Fatalf("%s annotation was rejected: %v", tool, err)
		}
		if got, want := annotation.RenderCommand().Rect, annotation.Rect.Normalize(); got != want {
			t.Fatalf("%s render rect = %#v, want %#v", tool, got, want)
		}
	}
}

func TestEditorAcceptsShapeTools(t *testing.T) {
	ed := NewEditor()
	for _, tool := range []Tool{ToolRectangle, ToolTriangle, ToolCircle} {
		if err := ed.SetActiveTool(tool); err != nil {
			t.Fatalf("SetActiveTool(%s): %v", tool, err)
		}
		if ed.ActiveTool != tool {
			t.Fatalf("active tool = %s, want %s", ed.ActiveTool, tool)
		}
	}
}

func TestEditorUndoRedo(t *testing.T) {
	ed := NewEditor()
	ed.BeginSelection(Point{X: 0, Y: 0})
	if err := ed.UpdateSelection(Point{X: 100, Y: 100}); err != nil {
		t.Fatal(err)
	}
	if _, err := ed.CommitSelection(); err != nil {
		t.Fatal(err)
	}
	if err := ed.StartEditing(); err != nil {
		t.Fatal(err)
	}

	ann := Annotation{ID: "line-1", Tool: ToolLine, Rect: Rect{Min: Point{X: 1, Y: 1}, Max: Point{X: 10, Y: 10}}, Style: DefaultStrokeStyle()}
	if err := ed.AddAnnotation(ann); err != nil {
		t.Fatal(err)
	}
	if _, ok := ed.Undo(); !ok {
		t.Fatal("expected undo")
	}
	if len(ed.Annotations) != 0 {
		t.Fatalf("expected no annotations, got %d", len(ed.Annotations))
	}
	if _, ok := ed.Redo(); !ok {
		t.Fatal("expected redo")
	}
	if len(ed.Annotations) != 1 {
		t.Fatalf("expected one annotation, got %d", len(ed.Annotations))
	}
}

func TestLineAllowsHorizontalAndVertical(t *testing.T) {
	style := DefaultStrokeStyle()
	for _, ann := range []Annotation{
		{ID: "h", Tool: ToolLine, Rect: Rect{Min: Point{X: 1, Y: 5}, Max: Point{X: 20, Y: 5}}, Style: style},
		{ID: "v", Tool: ToolLine, Rect: Rect{Min: Point{X: 5, Y: 1}, Max: Point{X: 5, Y: 20}}, Style: style},
	} {
		if err := ann.Validate(); err != nil {
			t.Fatalf("expected valid line %#v: %v", ann.Rect, err)
		}
	}
}

func TestEditorTranslateAnnotations(t *testing.T) {
	ed := NewEditor()
	ed.Annotations = []Annotation{
		{ID: "path", Tool: ToolPen, Points: []Point{{X: 10, Y: 20}, {X: 30, Y: 40}}, Style: DefaultStrokeStyle()},
		{ID: "rect", Tool: ToolRectangle, Rect: NewRect(Point{X: 5, Y: 8}, Point{X: 20, Y: 24}), Style: DefaultStrokeStyle()},
	}
	ed.RedoStack = []Annotation{{ID: "line", Tool: ToolLine, Rect: Rect{Min: Point{X: 50, Y: 60}, Max: Point{X: 70, Y: 80}}, Style: DefaultStrokeStyle()}}

	ed.TranslateAnnotations(Point{X: -3, Y: -7})

	if got := ed.Annotations[0].Points[0]; got != (Point{X: 7, Y: 13}) {
		t.Fatalf("unexpected translated point: %#v", got)
	}
	if got := ed.Annotations[1].Rect; got != (Rect{Min: Point{X: 2, Y: 1}, Max: Point{X: 17, Y: 17}}) {
		t.Fatalf("unexpected translated rect: %#v", got)
	}
	if got := ed.RedoStack[0].Rect; got != (Rect{Min: Point{X: 47, Y: 53}, Max: Point{X: 67, Y: 73}}) {
		t.Fatalf("unexpected translated redo rect: %#v", got)
	}
}

func TestMosaicAnnotationRequiresBlockSize(t *testing.T) {
	annotation := Annotation{
		ID:     "mosaic-1",
		Tool:   ToolMosaic,
		Points: []Point{{X: 1, Y: 2}, {X: 10, Y: 12}},
		Style:  StrokeStyle{Width: 20},
	}
	if err := annotation.Validate(); err == nil {
		t.Fatal("mosaic without a block size should be rejected")
	}
	annotation.MosaicBlockSize = 8
	if err := annotation.Validate(); err != nil {
		t.Fatal(err)
	}
	if got := annotation.RenderCommand().MosaicBlockSize; got != 8 {
		t.Fatalf("render command block size = %d, want 8", got)
	}
}

func TestRectangleMosaicRequiresAndNormalizesRect(t *testing.T) {
	annotation := Annotation{
		ID:              "mosaic-rect",
		Tool:            ToolMosaic,
		Style:           StrokeStyle{Width: 20},
		MosaicMode:      MosaicModeRectangle,
		MosaicBlockSize: 8,
	}
	if err := annotation.Validate(); err == nil {
		t.Fatal("rectangle mosaic without a rect should be rejected")
	}
	annotation.Rect = Rect{Min: Point{X: 20, Y: 18}, Max: Point{X: 4, Y: 3}}
	if err := annotation.Validate(); err != nil {
		t.Fatal(err)
	}
	command := annotation.RenderCommand()
	if command.MosaicMode != MosaicModeRectangle || command.Rect != annotation.Rect.Normalize() {
		t.Fatalf("rectangle mosaic render command = %#v", command)
	}
}

func TestRectanglePenRequiresAndNormalizesRect(t *testing.T) {
	annotation := Annotation{
		ID:      "pen-rect",
		Tool:    ToolPen,
		Style:   StrokeStyle{Width: 20},
		PenMode: PenModeRectangle,
	}
	if err := annotation.Validate(); err == nil {
		t.Fatal("rectangle pen without a rect should be rejected")
	}
	annotation.Rect = Rect{Min: Point{X: 20, Y: 18}, Max: Point{X: 4, Y: 3}}
	if err := annotation.Validate(); err != nil {
		t.Fatal(err)
	}
	command := annotation.RenderCommand()
	if command.PenMode != PenModeRectangle || command.Rect != annotation.Rect.Normalize() {
		t.Fatalf("rectangle pen render command = %#v", command)
	}
}

func TestTextAnnotationValidationAndRenderCommand(t *testing.T) {
	style := DefaultTextStyle()
	if style.FontFamily != TextFontMalgun {
		t.Fatalf("default text font = %q, want %q", style.FontFamily, TextFontMalgun)
	}
	annotation := Annotation{
		ID:        "text-1",
		Tool:      ToolText,
		Rect:      Rect{Min: Point{X: 80, Y: 60}, Max: Point{X: 10, Y: 20}},
		Text:      "한글 English 123",
		TextStyle: style,
	}
	if err := annotation.Validate(); err != nil {
		t.Fatal(err)
	}
	command := annotation.RenderCommand()
	if command.Text != annotation.Text || command.TextStyle != style {
		t.Fatalf("unexpected text render command: %#v", command)
	}
	if command.Rect != annotation.Rect.Normalize() {
		t.Fatalf("text rect = %#v, want %#v", command.Rect, annotation.Rect.Normalize())
	}
	annotation.Text = ""
	if err := annotation.Validate(); err == nil {
		t.Fatal("empty text should be rejected")
	}
	annotation.Text = "text"
	annotation.TextStyle.FontFamily = "unsupported"
	if err := annotation.Validate(); err == nil {
		t.Fatal("unsupported text font should be rejected")
	}
}

func TestCanonicalizeTextRunsUsesRuneOffsetsAndBaseStyle(t *testing.T) {
	text := "A한🙂B"
	runs := []TextRun{
		{Start: 0, End: 1, Bold: false},
		{Start: 1, End: 2, Bold: true},
		{Start: 2, End: 3, Bold: true},
		{Start: 3, End: 4, Bold: false},
	}
	want := []TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 3, Bold: true}, {Start: 3, End: 4, Bold: false}}
	if got := CanonicalizeTextRuns(text, false, runs); !reflect.DeepEqual(got, want) {
		t.Fatalf("canonical runs = %#v, want %#v", got, want)
	}
	if got := CanonicalizeTextRuns(text, true, []TextRun{{Start: 0, End: 4, Bold: true}}); got != nil {
		t.Fatalf("uniform base-style runs = %#v, want nil", got)
	}
	if !BoldAtRuneOffset(text, false, runs, 2) || BoldAtRuneOffset(text, false, runs, 3) {
		t.Fatal("bold query did not use rune offsets")
	}
}

func TestEffectiveRichTextFontFamilyUsesConsistentBoldFamily(t *testing.T) {
	style := DefaultTextStyle()
	style.FontFamily = TextFontGulim
	if got := EffectiveRichTextFontFamily(style, nil); got != TextFontGulim {
		t.Fatalf("regular family = %q, want %q", got, TextFontGulim)
	}
	if got := EffectiveRichTextFontFamily(style, []TextRun{{Start: 0, End: 1, Bold: true}}); got != TextFontMalgun {
		t.Fatalf("mixed bold family = %q, want %q", got, TextFontMalgun)
	}
	style.Bold = true
	if got := EffectiveRichTextFontFamily(style, []TextRun{{Start: 0, End: 1, Bold: false}}); got != TextFontGulim {
		t.Fatalf("explicit regular family = %q, want %q", got, TextFontGulim)
	}
}

func TestToggleBoldTextRunsNormalizesMixedSelection(t *testing.T) {
	text := "가나다라"
	runs := []TextRun{{Start: 0, End: 2, Bold: true}, {Start: 2, End: 4, Bold: false}}
	want := []TextRun{{Start: 0, End: 3, Bold: true}, {Start: 3, End: 4, Bold: false}}
	if got := ToggleBoldTextRuns(text, false, runs, 3, 1); !reflect.DeepEqual(got, want) {
		t.Fatalf("mixed toggle = %#v, want %#v", got, want)
	}

	want = []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 3, Bold: false}, {Start: 3, End: 4, Bold: true}}
	if got := ToggleBoldTextRuns(text, true, nil, 1, 3); !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy bold toggle = %#v, want %#v", got, want)
	}
}

func TestTransformTextRunsForUnicodeReplacement(t *testing.T) {
	runs := []TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 3, Bold: true}}
	want := []TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 5, Bold: true}}
	if got := TransformTextRuns("가🙂Z", "가🙂한글Z", false, runs); !reflect.DeepEqual(got, want) {
		t.Fatalf("insert transform = %#v, want %#v", got, want)
	}

	runs = []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 4, Bold: false}}
	want = []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 2, Bold: false}}
	if got := TransformTextRuns("A한🙂B", "A界", false, runs); !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement transform = %#v, want %#v", got, want)
	}

	runs = []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 2, Bold: false}}
	want = []TextRun{{Start: 0, End: 2, Bold: true}, {Start: 2, End: 3, Bold: false}}
	if got := TransformTextRuns("ab", "a界b", false, runs); !reflect.DeepEqual(got, want) {
		t.Fatalf("caret insertion transform = %#v, want %#v", got, want)
	}
}

func TestTransformTextRunsForEditDisambiguatesRepeatedRuneDeletion(t *testing.T) {
	runs := []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 2, Bold: false}}
	got := TransformTextRunsForEdit("aa", "a", false, runs, 0, 0, 0)
	if got != nil {
		t.Fatalf("deleting the first bold rune transferred its style to the surviving regular rune: %#v", got)
	}
	got = TransformTextRunsForEdit("aa", "a", false, runs, 2, 2, 1)
	if len(got) != 1 || !got[0].Bold || got[0].Start != 0 || got[0].End != 1 {
		t.Fatalf("backspace should preserve the surviving bold rune: %#v", got)
	}
}

func TestTransformTextRunsForEditPreservesReplacedSelectionStyle(t *testing.T) {
	runs := []TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 2, Bold: true}}
	got := TransformTextRunsForEdit("aB", "a한", false, runs, 1, 2, 2)
	want := []TextRun{{Start: 0, End: 1, Bold: false}, {Start: 1, End: 2, Bold: true}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replacement runs = %#v, want %#v", got, want)
	}
}

func TestTransformTextRunsForEditPreservesStyleAcrossIMECompositionReplacement(t *testing.T) {
	runs := []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 2, Bold: false}}
	firstComposed := TransformTextRunsForEdit("가나", "각나", false, runs, 2, 2, 1)
	if !BoldAtRuneOffset("각나", false, firstComposed, 0) || BoldAtRuneOffset("각나", false, firstComposed, 1) {
		t.Fatalf("first composition replacement changed styles: %#v", firstComposed)
	}
	secondComposed := TransformTextRunsForEdit("가나", "가날", false, runs, 0, 0, 2)
	if !BoldAtRuneOffset("가날", false, secondComposed, 0) || BoldAtRuneOffset("가날", false, secondComposed, 1) {
		t.Fatalf("second composition replacement changed styles: %#v", secondComposed)
	}
}

func TestTextRunValidationRejectsMalformedExplicitRuns(t *testing.T) {
	base := Annotation{
		ID:        "text-runs",
		Tool:      ToolText,
		Rect:      NewRect(Point{}, Point{X: 100, Y: 40}),
		Text:      "한글A",
		TextStyle: DefaultTextStyle(),
	}
	if err := base.Validate(); err != nil {
		t.Fatalf("legacy nil runs rejected: %v", err)
	}
	malformed := [][]TextRun{
		{},
		{{Start: 1, End: 3}},
		{{Start: 0, End: 2}, {Start: 1, End: 3, Bold: true}},
		{{Start: 0, End: 4}},
		{{Start: 0, End: 1}, {Start: 1, End: 3}},
	}
	for _, runs := range malformed {
		annotation := base
		annotation.TextRuns = runs
		if err := annotation.Validate(); err == nil {
			t.Fatalf("malformed runs accepted: %#v", runs)
		}
	}
}

func TestRenderCommandDeepCopiesTextRuns(t *testing.T) {
	annotation := Annotation{
		ID:        "text-copy",
		Tool:      ToolText,
		Rect:      NewRect(Point{}, Point{X: 100, Y: 40}),
		Text:      "ab",
		TextStyle: DefaultTextStyle(),
		TextRuns:  []TextRun{{Start: 0, End: 1, Bold: true}, {Start: 1, End: 2, Bold: false}},
	}
	command := annotation.RenderCommand()
	command.TextRuns[0].Bold = false
	if !annotation.TextRuns[0].Bold {
		t.Fatal("render command shares text runs with annotation")
	}
}
