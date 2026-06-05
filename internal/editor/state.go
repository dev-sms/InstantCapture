package editor

import "fmt"

type CaptureState string

const (
	StateIdle      CaptureState = "idle"
	StateSelecting CaptureState = "selecting"
	StateCaptured  CaptureState = "captured"
	StateEditing   CaptureState = "editing"
)

type Editor struct {
	State       CaptureState
	Selection   Rect
	ActiveTool  Tool
	Annotations []Annotation
	RedoStack   []Annotation
}

func NewEditor() *Editor {
	return &Editor{State: StateIdle, ActiveTool: ToolRectangle}
}

func (e *Editor) BeginSelection(start Point) {
	e.State = StateSelecting
	e.Selection = Rect{Min: start, Max: start}
	e.Annotations = nil
}

func (e *Editor) UpdateSelection(current Point) error {
	if e.State != StateSelecting {
		return fmt.Errorf("cannot update selection while state is %s", e.State)
	}
	e.Selection.Max = current
	return nil
}

func (e *Editor) CommitSelection() (Rect, error) {
	if e.State != StateSelecting {
		return Rect{}, fmt.Errorf("cannot commit selection while state is %s", e.State)
	}

	selection := e.Selection.Normalize()
	if selection.Empty() {
		e.State = StateIdle
		return Rect{}, fmt.Errorf("selection is empty")
	}

	e.Selection = selection
	e.State = StateCaptured
	return selection, nil
}

func (e *Editor) StartEditing() error {
	if e.State != StateCaptured {
		return fmt.Errorf("cannot start editing while state is %s", e.State)
	}
	e.State = StateEditing
	return nil
}

func (e *Editor) SetActiveTool(tool Tool) error {
	switch tool {
	case ToolPen, ToolRectangle, ToolArrow, ToolLine, ToolHighlight:
		e.ActiveTool = tool
		return nil
	default:
		return fmt.Errorf("unsupported tool %q", tool)
	}
}

func (e *Editor) AddAnnotation(annotation Annotation) error {
	if e.State != StateEditing {
		return fmt.Errorf("cannot add annotation while state is %s", e.State)
	}

	if annotation.Style.Width == 0 {
		annotation.Style = DefaultStrokeStyle()
	}

	if err := annotation.Validate(); err != nil {
		return err
	}

	e.Annotations = append(e.Annotations, annotation)
	e.RedoStack = nil
	return nil
}

func (e *Editor) Undo() (Annotation, bool) {
	if e.State != StateEditing || len(e.Annotations) == 0 {
		return Annotation{}, false
	}
	last := e.Annotations[len(e.Annotations)-1]
	e.Annotations = e.Annotations[:len(e.Annotations)-1]
	e.RedoStack = append(e.RedoStack, last)
	return last, true
}

func (e *Editor) Redo() (Annotation, bool) {
	if e.State != StateEditing || len(e.RedoStack) == 0 {
		return Annotation{}, false
	}
	last := e.RedoStack[len(e.RedoStack)-1]
	e.RedoStack = e.RedoStack[:len(e.RedoStack)-1]
	e.Annotations = append(e.Annotations, last)
	return last, true
}

func (e *Editor) RenderCommands() []RenderCommand {
	commands := make([]RenderCommand, 0, len(e.Annotations))
	for _, annotation := range e.Annotations {
		commands = append(commands, annotation.RenderCommand())
	}
	return commands
}

func (e *Editor) TranslateAnnotations(delta Point) {
	translate := func(annotation *Annotation) {
		for i := range annotation.Points {
			annotation.Points[i].X += delta.X
			annotation.Points[i].Y += delta.Y
		}
		annotation.Rect.Min.X += delta.X
		annotation.Rect.Min.Y += delta.Y
		annotation.Rect.Max.X += delta.X
		annotation.Rect.Max.Y += delta.Y
	}
	for i := range e.Annotations {
		translate(&e.Annotations[i])
	}
	for i := range e.RedoStack {
		translate(&e.RedoStack[i])
	}
}
