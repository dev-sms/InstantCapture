package editor

import "testing"

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
