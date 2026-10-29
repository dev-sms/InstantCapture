package workflow

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"os"
	"path/filepath"
	"slices"
	"time"

	"instantcapture/internal/capture"
	"instantcapture/internal/editor"
	"instantcapture/internal/exporter"
)

type Mode string

const (
	ModeIdle      Mode = "idle"
	ModeSelecting Mode = "selecting"
	ModeEditing   Mode = "editing"
)

const MaxEditHistoryEntries = 20

type RenderOptions struct {
	OutputBorder exporter.BorderOptions
}

type CaptureSnapshot struct {
	VirtualScreen image.Rectangle
	Displays      []image.Rectangle
	Image         image.Image
}

type Controller struct {
	Capturer         capture.ScreenCapturer
	Editor           *editor.Editor
	AnnotationStyle  editor.StrokeStyle
	HighlighterStyle editor.StrokeStyle
	MosaicStyle      editor.StrokeStyle
	MosaicBlockSize  int
	RenderOptions    RenderOptions

	Mode              Mode
	VirtualScreen     image.Rectangle
	FullScreenImage   image.Image
	CapturedImage     image.Image
	CapturedImageRect image.Rectangle
	CropRect          image.Rectangle
	SelectedImageRect image.Rectangle
	SelectedScreen    image.Rectangle

	nextAnnotationID int
	undoStack        []editSnapshot
	redoStack        []editSnapshot
	previewImage     *image.RGBA
	previewRevision  uint64
}

type editSnapshot struct {
	CapturedImage     image.Image
	CapturedImageRect image.Rectangle
	CropRect          image.Rectangle
	SelectedImageRect image.Rectangle
	SelectedScreen    image.Rectangle
	Editor            *editor.Editor
	NextAnnotationID  int
}

func NewController(capturer capture.ScreenCapturer) *Controller {
	return &Controller{
		Capturer:         capturer,
		Editor:           editor.NewEditor(),
		AnnotationStyle:  editor.DefaultStrokeStyle(),
		HighlighterStyle: editor.StrokeStyle{ColorRGBA: [4]uint8{250, 204, 21, 180}, Width: 14},
		MosaicStyle:      editor.StrokeStyle{Width: 24},
		MosaicBlockSize:  12,
		Mode:             ModeIdle,
	}
}

func (c *Controller) BeginCapture() error {
	if c.Capturer == nil {
		return fmt.Errorf("screen capturer is nil")
	}
	snapshot, err := CaptureScreen(c.Capturer)
	if err != nil {
		return err
	}
	return c.BeginCaptureFromSnapshot(snapshot)
}

func CaptureScreen(capturer capture.ScreenCapturer) (CaptureSnapshot, error) {
	if capturer == nil {
		return CaptureSnapshot{}, fmt.Errorf("screen capturer is nil")
	}
	displays := capturer.Displays()
	virtualScreen := capture.UnionDisplays(displays)
	if virtualScreen.Empty() {
		return CaptureSnapshot{}, fmt.Errorf("no active displays found")
	}

	stitched, err := captureDisplays(capturer, virtualScreen, displays)
	if err != nil {
		return CaptureSnapshot{}, fmt.Errorf("capture displays: %w", err)
	}
	return CaptureSnapshot{VirtualScreen: virtualScreen, Displays: displays, Image: stitched}, nil
}

func captureDisplays(capturer capture.ScreenCapturer, virtualScreen image.Rectangle, displays []image.Rectangle) (image.Image, error) {
	canvas := image.NewRGBA(image.Rect(0, 0, virtualScreen.Dx(), virtualScreen.Dy()))
	for _, display := range displays {
		if display.Empty() {
			continue
		}
		img, err := capturer.Capture(display)
		if err != nil {
			return nil, err
		}
		dst := display.Sub(virtualScreen.Min)
		draw.Draw(canvas, dst, img, img.Bounds().Min, draw.Src)
	}
	return canvas, nil
}

func (c *Controller) BeginCaptureFromSnapshot(snapshot CaptureSnapshot) error {
	if snapshot.VirtualScreen.Empty() {
		return fmt.Errorf("snapshot virtual screen is empty")
	}
	if snapshot.Image == nil {
		return fmt.Errorf("snapshot image is nil")
	}
	c.VirtualScreen = snapshot.VirtualScreen
	c.FullScreenImage = normalizeImage(snapshot.Image)
	c.CapturedImage = nil
	c.CapturedImageRect = image.Rectangle{}
	c.CropRect = image.Rectangle{}
	c.SelectedImageRect = image.Rectangle{}
	c.SelectedScreen = image.Rectangle{}
	c.Editor = editor.NewEditor()
	c.clearEditHistory()
	c.invalidatePreviewImage()
	c.Mode = ModeSelecting
	return nil
}

func (c *Controller) CommitImageSelection(selection image.Rectangle) error {
	if c.Mode != ModeSelecting {
		return fmt.Errorf("cannot commit selection while mode is %s", c.Mode)
	}
	if c.FullScreenImage == nil {
		return fmt.Errorf("full screen image is nil")
	}

	selection = selection.Canon().Intersect(c.FullScreenImage.Bounds())
	if selection.Empty() {
		return fmt.Errorf("selection is empty")
	}

	if selection.Eq(c.FullScreenImage.Bounds()) {
		c.CapturedImage = normalizeImage(c.FullScreenImage)
	} else {
		c.CapturedImage = cropImage(c.FullScreenImage, selection)
	}
	c.ReleaseFullScreenImage()
	c.invalidatePreviewImage()
	c.CapturedImageRect = image.Rectangle{Min: selection.Min, Max: selection.Min.Add(c.CapturedImage.Bounds().Size())}
	c.CropRect = image.Rectangle{Max: c.CapturedImage.Bounds().Size()}
	c.updateSelectedRectsFromCrop()
	c.Editor = editor.NewEditor()
	c.Editor.BeginSelection(editor.Point{})
	if err := c.Editor.UpdateSelection(editor.Point{X: c.CropRect.Dx(), Y: c.CropRect.Dy()}); err != nil {
		return err
	}
	if _, err := c.Editor.CommitSelection(); err != nil {
		return err
	}
	if err := c.Editor.StartEditing(); err != nil {
		return err
	}
	c.Mode = ModeEditing
	c.clearEditHistory()
	return nil
}

func (c *Controller) CommitFreeformSelection(selection image.Rectangle, polygon []image.Point) error {
	if err := c.CommitImageSelection(selection); err != nil {
		return err
	}
	if len(polygon) < 3 {
		return nil
	}
	rgba, ok := c.CapturedImage.(*image.RGBA)
	if !ok {
		return nil
	}
	local := make([]image.Point, 0, len(polygon))
	for _, p := range polygon {
		local = append(local, p.Sub(selection.Min))
	}
	applyPolygonAlphaMask(rgba, local)
	c.invalidatePreviewImage()
	return nil
}

func (c *Controller) CommitFullScreenSelection() error {
	if c.FullScreenImage == nil {
		return fmt.Errorf("full screen image is nil")
	}
	return c.CommitImageSelection(c.FullScreenImage.Bounds())
}

func (c *Controller) CommitScreenSelection(screenRect image.Rectangle) error {
	imageRect := screenRect.Sub(c.VirtualScreen.Min)
	return c.CommitImageSelection(imageRect)
}

func (c *Controller) CommitScreenCapture(screenRect image.Rectangle) error {
	if c.Capturer == nil {
		return fmt.Errorf("screen capturer is nil")
	}
	img, err := c.Capturer.Capture(screenRect)
	if err != nil {
		return fmt.Errorf("capture selected screen rect: %w", err)
	}
	return c.CommitExternalImage(screenRect, img)
}

func (c *Controller) CommitFreeformScreenCapture(selection image.Rectangle, polygon []image.Point) error {
	screenRect := selection.Add(c.VirtualScreen.Min)
	if err := c.CommitScreenCapture(screenRect); err != nil {
		return err
	}
	rgba, ok := c.CapturedImage.(*image.RGBA)
	if ok && len(polygon) >= 3 {
		local := make([]image.Point, 0, len(polygon))
		for _, p := range polygon {
			local = append(local, p.Sub(selection.Min))
		}
		applyPolygonAlphaMask(rgba, local)
		c.invalidatePreviewImage()
	}
	return nil
}

func applyPolygonAlphaMask(rgba *image.RGBA, local []image.Point) {
	for y := 0; y < rgba.Bounds().Dy(); y++ {
		for x := 0; x < rgba.Bounds().Dx(); x++ {
			if !pointInPolygon(image.Pt(x, y), local) {
				off := rgba.PixOffset(x, y)
				rgba.Pix[off+3] = 0
			}
		}
	}
}

func (c *Controller) CommitExternalImage(screenRect image.Rectangle, img image.Image) error {
	if img == nil {
		return fmt.Errorf("external image is nil")
	}
	c.CapturedImage = normalizeImage(img)
	c.ReleaseFullScreenImage()
	c.invalidatePreviewImage()
	screenMin := screenRect.Min
	imageMin := screenMin.Sub(c.VirtualScreen.Min)
	c.CapturedImageRect = image.Rectangle{Min: imageMin, Max: imageMin.Add(c.CapturedImage.Bounds().Size())}
	c.CropRect = image.Rectangle{Max: c.CapturedImage.Bounds().Size()}
	c.updateSelectedRectsFromCrop()
	c.Editor = editor.NewEditor()
	c.Editor.BeginSelection(editor.Point{X: 0, Y: 0})
	if err := c.Editor.UpdateSelection(editor.Point{X: c.CapturedImage.Bounds().Dx(), Y: c.CapturedImage.Bounds().Dy()}); err != nil {
		return err
	}
	if _, err := c.Editor.CommitSelection(); err != nil {
		return err
	}
	if err := c.Editor.StartEditing(); err != nil {
		return err
	}
	c.Mode = ModeEditing
	c.clearEditHistory()
	return nil
}

func (c *Controller) CommitImportedImage(img image.Image) error {
	if img == nil {
		return fmt.Errorf("imported image is nil")
	}
	size := img.Bounds().Size()
	if size.X <= 0 || size.Y <= 0 {
		return fmt.Errorf("imported image is empty")
	}
	c.VirtualScreen = image.Rectangle{Max: size}
	return c.CommitExternalImage(image.Rectangle{Max: size}, img)
}

func (c *Controller) AddAnnotation(tool editor.Tool, points []editor.Point, rect editor.Rect) error {
	return c.addAnnotation(tool, points, rect, editor.PenModeStraight, editor.MosaicModeStraight)
}

func (c *Controller) AddPenAnnotation(mode editor.PenMode, points []editor.Point, rect editor.Rect) error {
	return c.addAnnotation(editor.ToolPen, points, rect, mode, editor.MosaicModeStraight)
}

func (c *Controller) AddMosaicAnnotation(mode editor.MosaicMode, points []editor.Point, rect editor.Rect) error {
	return c.addAnnotation(editor.ToolMosaic, points, rect, editor.PenModeStraight, mode)
}

func (c *Controller) addAnnotation(tool editor.Tool, points []editor.Point, rect editor.Rect, penMode editor.PenMode, mosaicMode editor.MosaicMode) error {
	if c.Mode != ModeEditing {
		return fmt.Errorf("cannot annotate while mode is %s", c.Mode)
	}

	nextID := c.nextAnnotationID + 1
	if tool == editor.ToolRectangle || tool == editor.ToolTriangle || tool == editor.ToolCircle || (tool == editor.ToolPen && penMode == editor.PenModeRectangle) || (tool == editor.ToolMosaic && mosaicMode == editor.MosaicModeRectangle) {
		rect = rect.Normalize()
	}

	annotation := editor.Annotation{
		ID:     fmt.Sprintf("annotation-%d", nextID),
		Tool:   tool,
		Points: append([]editor.Point(nil), points...),
		Rect:   rect,
		Style:  c.styleForTool(tool),
	}
	if tool == editor.ToolPen {
		annotation.PenMode = penMode
	}
	if tool == editor.ToolMosaic {
		annotation.MosaicMode = mosaicMode
		annotation.MosaicBlockSize = c.MosaicBlockSize
	}
	if annotation.Style.Width == 0 {
		annotation.Style = editor.DefaultStrokeStyle()
	}
	if err := annotation.Validate(); err != nil {
		return err
	}
	c.pushEditHistory()
	c.nextAnnotationID = nextID
	if err := c.Editor.AddAnnotation(annotation); err != nil {
		return err
	}
	c.invalidatePreviewImage()
	return nil
}

func (c *Controller) AddTextAnnotation(text string, rect editor.Rect, style editor.TextStyle) (string, error) {
	return c.AddRichTextAnnotation(text, nil, rect, style)
}

func (c *Controller) AddRichTextAnnotation(text string, runs []editor.TextRun, rect editor.Rect, style editor.TextStyle) (string, error) {
	if c.Mode != ModeEditing {
		return "", fmt.Errorf("cannot annotate while mode is %s", c.Mode)
	}
	runs = editor.CanonicalizeTextRuns(text, style.Bold, runs)
	nextID := c.nextAnnotationID + 1
	annotation := editor.Annotation{
		ID:        fmt.Sprintf("annotation-%d", nextID),
		Tool:      editor.ToolText,
		Rect:      rect.Normalize(),
		Text:      text,
		TextStyle: style,
		TextRuns:  append([]editor.TextRun(nil), runs...),
	}
	if err := annotation.Validate(); err != nil {
		return "", err
	}
	c.pushEditHistory()
	c.nextAnnotationID = nextID
	if err := c.Editor.AddAnnotation(annotation); err != nil {
		return "", err
	}
	c.invalidatePreviewImage()
	return annotation.ID, nil
}

func (c *Controller) UpdateTextAnnotation(id, text string, rect editor.Rect, style editor.TextStyle) (bool, error) {
	if c.Mode != ModeEditing {
		return false, fmt.Errorf("cannot annotate while mode is %s", c.Mode)
	}
	current, ok := c.Editor.AnnotationByID(id)
	if !ok || current.Tool != editor.ToolText {
		return false, fmt.Errorf("text annotation %q was not found", id)
	}
	runs := current.TextRuns
	if runs != nil {
		runs = editor.TransformTextRuns(current.Text, text, current.TextStyle.Bold, runs)
		if runs == nil && len([]rune(text)) > 0 && style.Bold != current.TextStyle.Bold {
			runs = []editor.TextRun{{Start: 0, End: len([]rune(text)), Bold: current.TextStyle.Bold}}
		}
	}
	return c.UpdateRichTextAnnotation(id, text, runs, rect, style)
}

func (c *Controller) UpdateRichTextAnnotation(id, text string, runs []editor.TextRun, rect editor.Rect, style editor.TextStyle) (bool, error) {
	if c.Mode != ModeEditing {
		return false, fmt.Errorf("cannot annotate while mode is %s", c.Mode)
	}
	current, ok := c.Editor.AnnotationByID(id)
	if !ok || current.Tool != editor.ToolText {
		return false, fmt.Errorf("text annotation %q was not found", id)
	}
	next := current
	next.Text = text
	next.Rect = rect.Normalize()
	next.TextStyle = style
	next.TextRuns = editor.CanonicalizeTextRuns(text, style.Bold, runs)
	if err := next.Validate(); err != nil {
		return false, err
	}
	if current.Text == next.Text && current.Rect == next.Rect && current.TextStyle == next.TextStyle && slices.Equal(current.TextRuns, next.TextRuns) {
		return false, nil
	}
	c.pushEditHistory()
	if !c.Editor.ReplaceAnnotation(next) {
		return false, fmt.Errorf("text annotation %q was not found", id)
	}
	c.invalidatePreviewImage()
	return true, nil
}

func (c *Controller) RemoveTextAnnotation(id string) (bool, error) {
	if c.Mode != ModeEditing {
		return false, fmt.Errorf("cannot annotate while mode is %s", c.Mode)
	}
	annotation, ok := c.Editor.AnnotationByID(id)
	if !ok || annotation.Tool != editor.ToolText {
		return false, nil
	}
	c.pushEditHistory()
	removed := c.Editor.RemoveAnnotation(id)
	if removed {
		c.invalidatePreviewImage()
	}
	return removed, nil
}

func (c *Controller) UndoAnnotation() bool {
	return c.UndoEdit()
}

func (c *Controller) RedoAnnotation() bool {
	return c.RedoEdit()

}

func (c *Controller) UndoEdit() bool {
	if len(c.undoStack) == 0 {
		return false
	}
	current := c.snapshotEditState()
	last := c.undoStack[len(c.undoStack)-1]
	c.undoStack[len(c.undoStack)-1] = editSnapshot{}
	c.undoStack = c.undoStack[:len(c.undoStack)-1]
	c.redoStack = appendBoundedEditHistory(c.redoStack, current)
	c.restoreEditState(last)
	c.invalidatePreviewImage()
	return true
}

func (c *Controller) RedoEdit() bool {
	if len(c.redoStack) == 0 {
		return false
	}
	current := c.snapshotEditState()
	next := c.redoStack[len(c.redoStack)-1]
	c.redoStack[len(c.redoStack)-1] = editSnapshot{}
	c.redoStack = c.redoStack[:len(c.redoStack)-1]
	c.undoStack = appendBoundedEditHistory(c.undoStack, current)
	c.restoreEditState(next)
	c.invalidatePreviewImage()
	return true
}

func (c *Controller) styleForTool(tool editor.Tool) editor.StrokeStyle {
	style := c.AnnotationStyle
	if tool == editor.ToolHighlight {
		style = c.HighlighterStyle
	} else if tool == editor.ToolMosaic {
		style = c.MosaicStyle
	} else {
		style.ColorRGBA[3] = 255
	}
	return style
}

func (c *Controller) RenderedImage() (*image.RGBA, error) {
	if c.CapturedImage == nil {
		return nil, fmt.Errorf("captured image is nil")
	}
	return exporter.RenderAnnotationsCroppedWithBorder(c.CapturedImage, c.Editor.RenderCommands(), c.VisibleCropRect(), c.RenderOptions.OutputBorder)
}

func (c *Controller) OriginalImage() (*image.RGBA, error) {
	if c.CapturedImage == nil {
		return nil, fmt.Errorf("captured image is nil")
	}
	return exporter.RenderAnnotationsCropped(c.CapturedImage, nil, c.VisibleCropRect())
}

func (c *Controller) PreviewImage() (*image.RGBA, error) {
	if c.CapturedImage == nil {
		return nil, fmt.Errorf("captured image is nil")
	}
	if c.previewImage != nil {
		return c.previewImage, nil
	}
	if len(c.Editor.Annotations) == 0 {
		if rgba, ok := c.CapturedImage.(*image.RGBA); ok {
			c.previewImage = rgba
			return rgba, nil
		}
	}
	preview, err := exporter.RenderAnnotations(c.CapturedImage, c.Editor.RenderCommands())
	if err != nil {
		return nil, err
	}
	c.previewImage = preview
	return preview, nil
}

func (c *Controller) invalidatePreviewImage() {
	c.previewImage = nil
	c.previewRevision++
}

func (c *Controller) PreviewRevision() uint64 {
	return c.previewRevision
}

func (c *Controller) ReleasePreviewCache() {
	if c.previewImage != nil {
		c.invalidatePreviewImage()
	}
}

func (c *Controller) PreviewImageWithPending(command editor.RenderCommand) (*image.RGBA, error) {
	if c.CapturedImage == nil {
		return nil, fmt.Errorf("captured image is nil")
	}
	commands := c.Editor.RenderCommands()
	commands = append(commands, command)
	return exporter.RenderAnnotations(c.CapturedImage, commands)
}

func (c *Controller) PreviewImageWithoutAnnotation(id string) (*image.RGBA, error) {
	if c.CapturedImage == nil {
		return nil, fmt.Errorf("captured image is nil")
	}
	commands := make([]editor.RenderCommand, 0, len(c.Editor.Annotations))
	for _, annotation := range c.Editor.Annotations {
		if annotation.ID != id {
			commands = append(commands, annotation.RenderCommand())
		}
	}
	return exporter.RenderAnnotations(c.CapturedImage, commands)
}

func (c *Controller) PreviewImageReplacingAnnotation(id string, pending editor.RenderCommand) (*image.RGBA, error) {
	if c.CapturedImage == nil {
		return nil, fmt.Errorf("captured image is nil")
	}
	commands := make([]editor.RenderCommand, 0, len(c.Editor.Annotations)+1)
	pendingAdded := false
	for _, annotation := range c.Editor.Annotations {
		if annotation.ID == id {
			if !pending.Rect.Empty() {
				pending.TextRuns = append([]editor.TextRun(nil), pending.TextRuns...)
				commands = append(commands, pending)
				pendingAdded = true
			}
			continue
		}
		commands = append(commands, annotation.RenderCommand())
	}
	if !pendingAdded && !pending.Rect.Empty() {
		pending.TextRuns = append([]editor.TextRun(nil), pending.TextRuns...)
		commands = append(commands, pending)
	}
	return exporter.RenderAnnotations(c.CapturedImage, commands)
}

func (c *Controller) SetOutputBorder(enabled bool, width int, rgba [4]uint8) {
	c.RenderOptions.OutputBorder = exporter.BorderOptions{
		Enabled: enabled,
		Width:   width,
		Color:   color.NRGBA{R: rgba[0], G: rgba[1], B: rgba[2], A: rgba[3]},
	}
}

func (c *Controller) CropCapturedImage(rect image.Rectangle) error {
	if c.Mode != ModeEditing {
		return fmt.Errorf("cannot crop while mode is %s", c.Mode)
	}
	if c.CapturedImage == nil {
		return fmt.Errorf("captured image is nil")
	}

	bounds := c.CapturedImageBounds()
	rect = rect.Canon().Intersect(bounds)
	if rect.Empty() {
		return fmt.Errorf("crop rectangle is empty")
	}
	if rect.Eq(c.VisibleCropRect()) {
		return nil
	}

	c.pushEditHistory()
	c.CropRect = rect
	c.updateSelectedRectsFromCrop()
	if c.Editor != nil {
		c.Editor.Selection = rectToEditorRect(rect)
	}
	return nil
}

func (c *Controller) pushEditHistory() {
	c.undoStack = appendBoundedEditHistory(c.undoStack, c.snapshotEditState())
	c.redoStack = nil
}

func (c *Controller) clearEditHistory() {
	c.undoStack = nil
	c.redoStack = nil
}

func (c *Controller) snapshotEditState() editSnapshot {
	return editSnapshot{
		CapturedImage:     c.CapturedImage,
		CapturedImageRect: c.CapturedImageRect,
		CropRect:          c.CropRect,
		SelectedImageRect: c.SelectedImageRect,
		SelectedScreen:    c.SelectedScreen,
		Editor:            cloneEditor(c.Editor),
		NextAnnotationID:  c.nextAnnotationID,
	}
}

func appendBoundedEditHistory(stack []editSnapshot, item editSnapshot) []editSnapshot {
	stack = append(stack, item)
	if len(stack) > MaxEditHistoryEntries {
		copy(stack, stack[len(stack)-MaxEditHistoryEntries:])
		stack = stack[:MaxEditHistoryEntries]
	}
	return stack
}

func (c *Controller) ReleaseFullScreenImage() {
	c.FullScreenImage = nil
}

func (c *Controller) DiscardSelectionCapture() {
	c.ReleaseFullScreenImage()
	if c.Mode == ModeSelecting {
		c.Mode = ModeIdle
	}
}

func (c *Controller) ResetEditingSession() {
	c.VirtualScreen = image.Rectangle{}
	c.FullScreenImage = nil
	c.CapturedImage = nil
	c.CapturedImageRect = image.Rectangle{}
	c.CropRect = image.Rectangle{}
	c.SelectedImageRect = image.Rectangle{}
	c.SelectedScreen = image.Rectangle{}
	c.Editor = editor.NewEditor()
	c.nextAnnotationID = 0
	c.clearEditHistory()
	c.invalidatePreviewImage()
	c.Mode = ModeIdle
}

func (c *Controller) restoreEditState(snapshot editSnapshot) {
	c.CapturedImage = snapshot.CapturedImage
	c.CapturedImageRect = snapshot.CapturedImageRect
	c.CropRect = snapshot.CropRect
	c.SelectedImageRect = snapshot.SelectedImageRect
	c.SelectedScreen = snapshot.SelectedScreen
	c.Editor = cloneEditor(snapshot.Editor)
	c.nextAnnotationID = snapshot.NextAnnotationID
}

func cloneEditor(src *editor.Editor) *editor.Editor {
	if src == nil {
		return nil
	}
	dst := *src
	dst.Annotations = cloneAnnotations(src.Annotations)
	dst.RedoStack = cloneAnnotations(src.RedoStack)
	return &dst
}

func cloneAnnotations(src []editor.Annotation) []editor.Annotation {
	if src == nil {
		return nil
	}
	dst := make([]editor.Annotation, len(src))
	for i := range src {
		dst[i] = src[i]
		dst[i].Points = append([]editor.Point(nil), src[i].Points...)
		dst[i].TextRuns = append([]editor.TextRun(nil), src[i].TextRuns...)
	}
	return dst
}

func cloneRGBA(src image.Image) *image.RGBA {
	if src == nil {
		return nil
	}
	return cropImage(src, src.Bounds())
}

func (c *Controller) CapturedImageBounds() image.Rectangle {
	if c.CapturedImage == nil {
		return image.Rectangle{}
	}
	return image.Rectangle{Max: c.CapturedImage.Bounds().Size()}
}

func (c *Controller) VisibleCropRect() image.Rectangle {
	bounds := c.CapturedImageBounds()
	if bounds.Empty() {
		return image.Rectangle{}
	}
	rect := c.CropRect.Canon().Intersect(bounds)
	if rect.Empty() {
		return bounds
	}
	return rect
}

func (c *Controller) OutputSize() image.Point {
	return c.VisibleCropRect().Size()
}

func (c *Controller) updateSelectedRectsFromCrop() {
	crop := c.VisibleCropRect()
	if crop.Empty() {
		c.SelectedImageRect = image.Rectangle{}
		c.SelectedScreen = image.Rectangle{}
		return
	}
	min := c.CapturedImageRect.Min.Add(crop.Min)
	c.SelectedImageRect = image.Rectangle{Min: min, Max: min.Add(crop.Size())}
	c.SelectedScreen = c.SelectedImageRect.Add(c.VirtualScreen.Min)
}

func rectToEditorRect(r image.Rectangle) editor.Rect {
	return editor.Rect{Min: editor.Point{X: r.Min.X, Y: r.Min.Y}, Max: editor.Point{X: r.Max.X, Y: r.Max.Y}}
}

func (c *Controller) CopyPNG() error {
	img, err := c.RenderedImage()
	if err != nil {
		return err
	}
	return exporter.CopyPNG(img)
}

func (c *Controller) SavePNG(dir string) (string, error) {
	img, err := c.RenderedImage()
	if err != nil {
		return "", err
	}
	return savePNG(dir, img)
}

func (c *Controller) SaveImage(path string) error {
	img, err := c.RenderedImage()
	if err != nil {
		return err
	}
	return exporter.SaveImage(path, img)
}

func (c *Controller) SaveOriginalPNG(dir string) (string, error) {
	img, err := c.OriginalImage()
	if err != nil {
		return "", err
	}
	return savePNG(dir, img)
}

func savePNG(dir string, img image.Image) (string, error) {
	if dir == "" {
		dir = "."
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create save dir: %w", err)
	}
	path := nextSavePath(dir, time.Now())
	return path, exporter.SavePNG(path, img)
}

func nextSavePath(dir string, now time.Time) string {
	base := "InstantCapture_" + now.Format("20060102_150405")
	path := filepath.Join(dir, base+".png")
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path
	}

	for i := 1; ; i++ {
		candidate := filepath.Join(dir, fmt.Sprintf("%s_%02d.png", base, i))
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate
		}
	}
}

func normalizeImage(src image.Image) *image.RGBA {
	if src == nil {
		return nil
	}
	if rgba, ok := src.(*image.RGBA); ok && rgba.Bounds().Min == (image.Point{}) && rgba.Stride == rgba.Bounds().Dx()*4 && len(rgba.Pix) == rgba.Stride*rgba.Bounds().Dy() {
		return rgba
	}
	return cropImage(src, src.Bounds())
}

func cropImage(src image.Image, rect image.Rectangle) *image.RGBA {
	dst := image.NewRGBA(image.Rect(0, 0, rect.Dx(), rect.Dy()))
	draw.Draw(dst, dst.Bounds(), src, rect.Min, draw.Src)
	return dst
}

func pointInPolygon(p image.Point, poly []image.Point) bool {
	inside := false
	j := len(poly) - 1
	for i := range poly {
		pi := poly[i]
		pj := poly[j]
		if (pi.Y > p.Y) != (pj.Y > p.Y) {
			x := float64(pj.X-pi.X)*float64(p.Y-pi.Y)/float64(pj.Y-pi.Y) + float64(pi.X)
			if float64(p.X) < x {
				inside = !inside
			}
		}
		j = i
	}
	return inside
}
