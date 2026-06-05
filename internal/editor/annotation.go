package editor

import "errors"

type Tool string

const (
	ToolPen       Tool = "pen"
	ToolRectangle Tool = "rectangle"
	ToolArrow     Tool = "arrow"
	ToolLine      Tool = "line"
	ToolHighlight Tool = "highlight"
)

type StrokeStyle struct {
	ColorRGBA [4]uint8
	Width     float64
}

func DefaultStrokeStyle() StrokeStyle {
	return StrokeStyle{ColorRGBA: [4]uint8{255, 64, 64, 255}, Width: 3}
}

type Annotation struct {
	ID     string
	Tool   Tool
	Points []Point
	Rect   Rect
	Style  StrokeStyle
}

type RenderCommand struct {
	Tool   Tool
	Points []Point
	Rect   Rect
	Style  StrokeStyle
}

func (a Annotation) Validate() error {
	if a.ID == "" {
		return errors.New("annotation id is required")
	}

	switch a.Tool {
	case ToolPen, ToolHighlight:
		if len(a.Points) < 2 {
			return errors.New("path annotation requires at least two points")
		}
	case ToolRectangle:
		if a.Rect.Empty() {
			return errors.New("shape annotation requires a non-empty rect")
		}
	case ToolArrow, ToolLine:
		if a.Rect.Min == a.Rect.Max {
			return errors.New("line annotation requires distinct endpoints")
		}
	default:
		return errors.New("unsupported annotation tool")
	}

	if a.Style.Width <= 0 {
		return errors.New("stroke width must be positive")
	}

	return nil
}

func (a Annotation) RenderCommand() RenderCommand {
	rect := a.Rect
	if a.Tool == ToolRectangle {
		rect = rect.Normalize()
	}
	return RenderCommand{Tool: a.Tool, Points: append([]Point(nil), a.Points...), Rect: rect, Style: a.Style}
}
