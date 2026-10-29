package ui

import (
	"container/heap"
	"context"
	"fmt"
	"image"
	"image/color"
	"io"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"gioui.org/app"
	"gioui.org/io/semantic"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/op/clip"
	"gioui.org/op/paint"
	"gioui.org/unit"
	"gioui.org/widget"
	"gioui.org/widget/material"

	platformwindows "instantcapture/internal/platform/windows"
)

const (
	filmstripCollapsedWidth    = 30
	filmstripExpandedWidth     = 220
	thumbnailFallbackWidth     = 180
	thumbnailDisplayHeight     = 104
	filmstripHoverMaxWidth     = 720
	filmstripHoverMaxHeight    = 480
	filmstripHoverFooterHeight = 44
	thumbnailCacheItems        = 64
	thumbnailCacheBytes        = 24 << 20
	thumbnailMaxFileBytes      = 64 << 20
	thumbnailMaxPixels         = 36_000_000
)

type thumbnailKey struct {
	Path       string
	Size       int64
	ModifiedNS int64
}

type filmstripFile struct {
	key      thumbnailKey
	name     string
	modified time.Time
}

type filmstripItem struct {
	file    filmstripFile
	click   widget.Clickable
	recycle widget.Clickable
}

type filmstripScanResult struct {
	generation uint64
	dir        string
	files      []filmstripFile
	err        error
}

// bounds is the thumbnail box in physical pixels, so thumbnails are drawn 1:1.
type thumbnailJob struct {
	key    thumbnailKey
	bounds image.Point
}

type thumbnailResult struct {
	key    thumbnailKey
	bounds image.Point
	img    *image.RGBA
	err    error
}

type hoverPreviewJob struct {
	key    thumbnailKey
	bounds image.Point
}

type hoverPreviewResult struct {
	job hoverPreviewJob
	img *image.RGBA
	err error
}

// hoverPreview holds the one large preview decoded for the hovered capture;
// list thumbnails stay small so the shared cache keeps many of them.
type hoverPreview struct {
	want   hoverPreviewJob
	have   hoverPreviewJob
	img    *image.RGBA
	op     paint.ImageOp
	failed bool
}

type filmstripRecycleResult struct {
	path string
	err  error
}

type thumbnailCacheEntry struct {
	img      *image.RGBA
	op       paint.ImageOp
	bytes    int64
	lastUsed uint64
}

type thumbnailCache struct {
	entries  map[thumbnailKey]*thumbnailCacheEntry
	bytes    int64
	clock    uint64
	maxItems int
	maxBytes int64
}

func newThumbnailCache(maxItems int, maxBytes int64) thumbnailCache {
	return thumbnailCache{
		entries:  make(map[thumbnailKey]*thumbnailCacheEntry),
		maxItems: maxItems,
		maxBytes: maxBytes,
	}
}

func (c *thumbnailCache) get(key thumbnailKey) (*thumbnailCacheEntry, bool) {
	entry, ok := c.entries[key]
	if !ok {
		return nil, false
	}
	c.clock++
	entry.lastUsed = c.clock
	return entry, true
}

func (c *thumbnailCache) add(key thumbnailKey, img *image.RGBA) {
	if img == nil {
		return
	}
	if previous, ok := c.entries[key]; ok {
		c.bytes -= previous.bytes
	}
	c.clock++
	entry := &thumbnailCacheEntry{
		img:      img,
		op:       paint.NewImageOp(img),
		bytes:    int64(len(img.Pix)),
		lastUsed: c.clock,
	}
	c.entries[key] = entry
	c.bytes += entry.bytes
	c.evict()
}

func (c *thumbnailCache) evict() {
	for len(c.entries) > c.maxItems || c.bytes > c.maxBytes {
		var oldestKey thumbnailKey
		var oldest *thumbnailCacheEntry
		for key, entry := range c.entries {
			if oldest == nil || entry.lastUsed < oldest.lastUsed {
				oldestKey = key
				oldest = entry
			}
		}
		if oldest == nil {
			return
		}
		delete(c.entries, oldestKey)
		c.bytes -= oldest.bytes
	}
}

func (c *thumbnailCache) clear() {
	c.entries = make(map[thumbnailKey]*thumbnailCacheEntry)
	c.bytes = 0
	c.clock = 0
}

func (c *thumbnailCache) remove(key thumbnailKey) {
	if entry, ok := c.entries[key]; ok {
		delete(c.entries, key)
		c.bytes -= entry.bytes
	}
}

type filmstripState struct {
	ctx context.Context

	list           widget.List
	toggle         widget.Clickable
	refresh        widget.Clickable
	openFolder     widget.Clickable
	defaultFolder  widget.Clickable
	expanded       bool
	dirty          bool
	progress       float64
	velocity       float64
	lastFrame      time.Time
	selected       string
	items          []*filmstripItem
	dir            string
	sessionDir     string
	scanActive     bool
	scanAgain      bool
	generation     uint64
	pendingDir     string
	scanError      string
	scanResults    chan filmstripScanResult
	thumbJobs      chan thumbnailJob
	thumbResults   chan thumbnailResult
	recycleResults chan filmstripRecycleResult
	folderResults  chan folderPickResult
	queued         map[thumbnailKey]struct{}
	thumbErrors    map[thumbnailKey]string
	cache          thumbnailCache
	recycleActive  bool
	recyclePath    string
	folderActive   bool
	hoveredKey     thumbnailKey
	hovered        bool
	panelWidth     int
	listBounds     image.Point
	hoverJobs      chan hoverPreviewJob
	hoverResults   chan hoverPreviewResult
	hover          hoverPreview
}

type folderPickResult struct {
	dir string
	ok  bool
	err error
}

func newFilmstripState(ctx context.Context) *filmstripState {
	state := &filmstripState{
		ctx:            ctx,
		list:           widget.List{List: layout.List{Axis: layout.Vertical}},
		scanResults:    make(chan filmstripScanResult, 2),
		thumbJobs:      make(chan thumbnailJob, 32),
		thumbResults:   make(chan thumbnailResult, 16),
		recycleResults: make(chan filmstripRecycleResult, 1),
		folderResults:  make(chan folderPickResult, 1),
		queued:         make(map[thumbnailKey]struct{}),
		thumbErrors:    make(map[thumbnailKey]string),
		cache:          newThumbnailCache(thumbnailCacheItems, thumbnailCacheBytes),
		hoverJobs:      make(chan hoverPreviewJob, 1),
		hoverResults:   make(chan hoverPreviewResult, 1),
	}
	go state.thumbnailWorker()
	go state.hoverPreviewWorker()
	return state
}

func (f *filmstripState) thumbnailWorker() {
	for {
		select {
		case <-f.ctx.Done():
			return
		case job := <-f.thumbJobs:
			bounds := job.bounds
			if bounds.X <= 0 || bounds.Y <= 0 {
				bounds = image.Pt(thumbnailFallbackWidth, thumbnailDisplayHeight)
			}
			img, err := decodeThumbnail(job.key, bounds.X, bounds.Y)
			select {
			case f.thumbResults <- thumbnailResult{key: job.key, bounds: job.bounds, img: img, err: err}:
			case <-f.ctx.Done():
				return
			}
		}
	}
}

// hoverPreviewWorker runs apart from the list worker so hovering never waits
// behind a page of thumbnails.
func (f *filmstripState) hoverPreviewWorker() {
	for {
		select {
		case <-f.ctx.Done():
			return
		case job := <-f.hoverJobs:
			img, err := decodeThumbnail(job.key, job.bounds.X, job.bounds.Y)
			select {
			case f.hoverResults <- hoverPreviewResult{job: job, img: img, err: err}:
			case <-f.ctx.Done():
				return
			}
		}
	}
}

// setListThumbnailBounds drops cached thumbnails made for another box size,
// e.g. after the display scale changes.
func (f *filmstripState) setListThumbnailBounds(bounds image.Point) {
	if bounds == f.listBounds {
		return
	}
	f.listBounds = bounds
	f.cache.clear()
	f.queued = make(map[thumbnailKey]struct{})
}

func (f *filmstripState) requestHoverPreview(job hoverPreviewJob) {
	if job == f.hover.want {
		return
	}
	f.hover.want = job
	f.hover.failed = false
	if job == f.hover.have && f.hover.img != nil {
		return
	}
	// Keep only the newest request; an older hover target is no longer wanted.
	select {
	case f.hoverJobs <- job:
	default:
		select {
		case <-f.hoverJobs:
		default:
		}
		select {
		case f.hoverJobs <- job:
		default:
		}
	}
}

func (f *filmstripState) applyHoverPreviewResult(result hoverPreviewResult) {
	if result.job != f.hover.want {
		return
	}
	if result.err != nil {
		f.hover.failed = true
		return
	}
	f.hover.have = result.job
	f.hover.img = result.img
	f.hover.op = paint.NewImageOp(result.img)
}

func (f *filmstripState) releaseHoverPreview() {
	f.hover = hoverPreview{}
}

func (f *filmstripState) requestRefresh(dir string) {
	dir = filepath.Clean(dir)
	f.dirty = false
	f.generation++
	f.pendingDir = dir
	f.scanError = ""
	if f.scanActive {
		f.scanAgain = true
		return
	}
	f.startScan()
}

func (f *filmstripState) startScan() {
	f.scanActive = true
	f.scanAgain = false
	generation := f.generation
	dir := f.pendingDir
	go func() {
		files, err := scanCaptureFolder(dir, 0)
		select {
		case f.scanResults <- filmstripScanResult{generation: generation, dir: dir, files: files, err: err}:
		case <-f.ctx.Done():
		}
	}()
}

func (f *filmstripState) applyScanResult(result filmstripScanResult) {
	f.scanActive = false
	if result.generation == f.generation {
		if result.err != nil {
			f.scanError = result.err.Error()
		} else {
			f.replaceFiles(result.dir, result.files)
			f.scanError = ""
		}
	}
	if f.scanAgain {
		f.startScan()
	}
}

func (f *filmstripState) replaceFiles(dir string, files []filmstripFile) {
	oldItems := make(map[thumbnailKey]*filmstripItem, len(f.items))
	for _, item := range f.items {
		oldItems[item.file.key] = item
	}
	items := make([]*filmstripItem, 0, len(files))
	validKeys := make(map[thumbnailKey]struct{}, len(files))
	for _, file := range files {
		validKeys[file.key] = struct{}{}
		if item, ok := oldItems[file.key]; ok {
			item.file = file
			items = append(items, item)
		} else {
			items = append(items, &filmstripItem{file: file})
		}
	}
	for key := range f.thumbErrors {
		if _, ok := validKeys[key]; !ok {
			delete(f.thumbErrors, key)
		}
	}
	if f.dir != "" && !samePath(f.dir, dir) {
		f.cache.clear()
		f.thumbErrors = make(map[thumbnailKey]string)
		f.queued = make(map[thumbnailKey]struct{})
		f.list.Position = layout.Position{}
	}
	f.dir = dir
	f.items = items
}

func (f *filmstripState) requestThumbnail(key thumbnailKey) {
	if _, ok := f.cache.get(key); ok {
		return
	}
	if _, ok := f.queued[key]; ok {
		return
	}
	if _, failed := f.thumbErrors[key]; failed {
		return
	}
	select {
	case f.thumbJobs <- thumbnailJob{key: key, bounds: f.listBounds}:
		f.queued[key] = struct{}{}
	default:
	}
}

func (f *filmstripState) applyThumbnailResult(result thumbnailResult) {
	if result.bounds != f.listBounds {
		return
	}
	delete(f.queued, result.key)
	if !f.hasKey(result.key) {
		return
	}
	if result.err != nil {
		f.thumbErrors[result.key] = result.err.Error()
		return
	}
	delete(f.thumbErrors, result.key)
	f.cache.add(result.key, result.img)
}

func (f *filmstripState) hasKey(key thumbnailKey) bool {
	for _, item := range f.items {
		if item.file.key == key {
			return true
		}
	}
	return false
}

func scanCaptureFolder(dir string, limit int) ([]filmstripFile, error) {
	folder, err := os.Open(dir)
	if err != nil {
		return nil, fmt.Errorf("스크린샷 폴더 읽기: %w", err)
	}
	defer folder.Close()
	files := make([]filmstripFile, 0, max(0, limit))
	bounded := &filmstripFileHeap{}
	if limit > 0 {
		heap.Init(bounded)
	}
	for {
		infos, readErr := folder.Readdir(256)
		for _, info := range infos {
			if !info.Mode().IsRegular() || !supportedImageExtension(info.Name()) {
				continue
			}
			path := filepath.Join(dir, info.Name())
			file := filmstripFile{
				key: thumbnailKey{
					Path:       filepath.Clean(path),
					Size:       info.Size(),
					ModifiedNS: info.ModTime().UnixNano(),
				},
				name:     info.Name(),
				modified: info.ModTime(),
			}
			if limit <= 0 {
				files = append(files, file)
			} else if bounded.Len() < limit {
				heap.Push(bounded, file)
			} else if filmstripFileNewer(file, (*bounded)[0]) {
				heap.Pop(bounded)
				heap.Push(bounded, file)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("스크린샷 폴더 읽기: %w", readErr)
		}
	}
	if limit > 0 {
		files = append(files, (*bounded)...)
	}
	sort.Slice(files, func(i, j int) bool {
		return filmstripFileNewer(files[i], files[j])
	})
	return files, nil
}

type filmstripFileHeap []filmstripFile

func (h filmstripFileHeap) Len() int { return len(h) }

func (h filmstripFileHeap) Less(i, j int) bool {
	if h[i].modified.Equal(h[j].modified) {
		return strings.ToLower(h[i].name) > strings.ToLower(h[j].name)
	}
	return h[i].modified.Before(h[j].modified)
}

func (h filmstripFileHeap) Swap(i, j int) { h[i], h[j] = h[j], h[i] }

func (h *filmstripFileHeap) Push(value any) {
	*h = append(*h, value.(filmstripFile))
}

func (h *filmstripFileHeap) Pop() any {
	old := *h
	last := old[len(old)-1]
	*h = old[:len(old)-1]
	return last
}

func filmstripFileNewer(a, b filmstripFile) bool {
	if a.modified.Equal(b.modified) {
		return strings.ToLower(a.name) < strings.ToLower(b.name)
	}
	return a.modified.After(b.modified)
}

func supportedImageExtension(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".bmp", ".webp":
		return true
	default:
		return false
	}
}

func decodeThumbnail(key thumbnailKey, maxWidth, maxHeight int) (*image.RGBA, error) {
	file, err := os.Open(key.Path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > thumbnailMaxFileBytes {
		return nil, fmt.Errorf("파일이 너무 큽니다")
	}
	config, _, err := image.DecodeConfig(file)
	if err != nil {
		return nil, err
	}
	if !validImageDimensions(config.Width, config.Height, thumbnailMaxPixels) {
		return nil, fmt.Errorf("지원하지 않는 이미지 크기입니다")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	src, _, err := image.Decode(file)
	if err != nil {
		return nil, err
	}
	return resizeHighQuality(src, image.Pt(maxWidth, maxHeight)), nil
}

func fittedThumbnailSize(source, bounds image.Point) image.Point {
	if source.X <= 0 || source.Y <= 0 || bounds.X <= 0 || bounds.Y <= 0 {
		return image.Pt(1, 1)
	}
	scale := math.Min(float64(bounds.X)/float64(source.X), float64(bounds.Y)/float64(source.Y))
	if scale > 1 {
		scale = 1
	}
	return image.Pt(max(1, int(math.Round(float64(source.X)*scale))), max(1, int(math.Round(float64(source.Y)*scale))))
}

func samePath(a, b string) bool {
	return strings.EqualFold(filepath.Clean(a), filepath.Clean(b))
}

func (u *windowUI) layoutWorkspace(gtx layout.Context, w *app.Window) layout.Dimensions {
	if u.filmstrip == nil {
		return u.layoutCanvas(gtx, w)
	}
	u.filmstrip.hovered = false
	panelWidth := u.filmstripWidth(gtx)
	u.filmstrip.panelWidth = panelWidth
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
			return u.layoutCanvas(gtx, w)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min.X = panelWidth
			gtx.Constraints.Max.X = panelWidth
			return u.layoutFilmstrip(gtx)
		}),
	)
}

func (u *windowUI) filmstripWidth(gtx layout.Context) int {
	f := u.filmstrip
	now := gtx.Now
	if now.IsZero() {
		now = time.Now()
	}
	target := 0.0
	if f.expanded {
		target = 1
	}
	if f.lastFrame.IsZero() {
		f.lastFrame = now
	}
	dt := now.Sub(f.lastFrame).Seconds()
	f.lastFrame = now
	if dt > 0.05 {
		dt = 0.05
	}
	if dt > 0 {
		const stiffness = 180.0
		const damping = 27.0
		acceleration := stiffness*(target-f.progress) - damping*f.velocity
		f.velocity += acceleration * dt
		f.progress += f.velocity * dt
		f.progress = math.Max(0, math.Min(1, f.progress))
	}
	if math.Abs(target-f.progress) < 0.002 && math.Abs(f.velocity) < 0.01 {
		f.progress = target
		f.velocity = 0
	} else {
		gtx.Execute(op.InvalidateCmd{At: now.Add(time.Second / 60)})
	}
	width := filmstripCollapsedWidth + int(math.Round((filmstripExpandedWidth-filmstripCollapsedWidth)*f.progress))
	return gtx.Dp(unit.Dp(width))
}

func (u *windowUI) layoutFilmstrip(gtx layout.Context) layout.Dimensions {
	size := gtx.Constraints.Max
	paint.FillShape(gtx.Ops, color.NRGBA{R: 23, G: 23, B: 27, A: 252}, clip.Rect{Max: size}.Op())
	paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 18}, clip.Rect{Max: image.Pt(1, size.Y)}.Op())
	contentWidth := size.X - gtx.Dp(unit.Dp(filmstripCollapsedWidth))
	return layout.Flex{Axis: layout.Horizontal}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			contentWidth = max(0, contentWidth)
			if contentWidth == 0 {
				return layout.Dimensions{Size: image.Pt(0, size.Y)}
			}
			fullWidth := gtx.Dp(unit.Dp(filmstripExpandedWidth - filmstripCollapsedWidth))
			defer clip.Rect{Max: image.Pt(contentWidth, size.Y)}.Push(gtx.Ops).Pop()
			defer op.Offset(image.Pt(contentWidth-fullWidth, 0)).Push(gtx.Ops).Pop()
			contentContext := gtx
			contentContext.Constraints.Min = image.Pt(fullWidth, size.Y)
			contentContext.Constraints.Max = image.Pt(fullWidth, size.Y)
			u.layoutFilmstripContent(contentContext)
			return layout.Dimensions{Size: image.Pt(contentWidth, size.Y)}
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			gtx.Constraints.Min = image.Pt(gtx.Dp(unit.Dp(filmstripCollapsedWidth)), size.Y)
			gtx.Constraints.Max = gtx.Constraints.Min
			return u.layoutFilmstripRail(gtx)
		}),
	)
}

func (u *windowUI) layoutFilmstripRail(gtx layout.Context) layout.Dimensions {
	fullSize := gtx.Constraints.Max
	gtx.Constraints = layout.Exact(fullSize)
	return u.filmstrip.toggle.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		semantic.Button.Add(gtx.Ops)
		label := "이전 캡처 열기"
		arrow := "‹"
		if u.filmstrip.expanded {
			label = "이전 캡처 닫기"
			arrow = "›"
		}
		semantic.LabelOp(label).Add(gtx.Ops)
		bg := color.NRGBA{R: 31, G: 31, B: 36, A: 255}
		if u.filmstrip.toggle.Hovered() {
			bg = color.NRGBA{R: 45, G: 45, B: 52, A: 255}
		}
		if u.filmstrip.toggle.Pressed() {
			bg = color.NRGBA{R: 56, G: 56, B: 64, A: 255}
		}
		paint.FillShape(gtx.Ops, bg, clip.Rect{Max: fullSize}.Op())
		layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Vertical, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(3))}.Layout(gtx,
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					return layoutFilmstripIcon(gtx)
				}),
				layout.Rigid(func(gtx layout.Context) layout.Dimensions {
					text := material.H6(u.theme, arrow)
					text.TextSize = unit.Sp(16)
					text.Color = color.NRGBA{R: 210, G: 210, B: 220, A: 255}
					return text.Layout(gtx)
				}),
			)
		})
		return layout.Dimensions{Size: fullSize}
	})
}

func layoutFilmstripIcon(gtx layout.Context) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(16)), gtx.Dp(unit.Dp(22)))
	for i := range 3 {
		y := i * gtx.Dp(unit.Dp(7))
		rect := image.Rect(0, y, size.X, y+gtx.Dp(unit.Dp(5)))
		paint.FillShape(gtx.Ops, color.NRGBA{R: 88, G: 166, B: 255, A: 210}, clip.RRect{Rect: rect, SE: 3, SW: 3, NW: 3, NE: 3}.Op(gtx.Ops))
	}
	return layout.Dimensions{Size: size}
}

func (u *windowUI) layoutFilmstripContent(gtx layout.Context) layout.Dimensions {
	return layout.Inset{Top: unit.Dp(12), Bottom: unit.Dp(10), Left: unit.Dp(10), Right: unit.Dp(8)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(10))}.Layout(gtx,
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutFilmstripHeader(gtx)
			}),
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				if u.filmstrip.scanActive && len(u.filmstrip.items) == 0 {
					return u.layoutFilmstripMessage(gtx, "폴더를 읽는 중…")
				}
				if u.filmstrip.scanError != "" {
					return u.layoutFilmstripMessage(gtx, "폴더를 읽지 못했습니다")
				}
				if len(u.filmstrip.items) == 0 {
					return u.layoutFilmstripMessage(gtx, "저장된 캡처가 없습니다")
				}
				list := material.List(u.theme, &u.filmstrip.list)
				return list.Layout(gtx, len(u.filmstrip.items), func(gtx layout.Context, index int) layout.Dimensions {
					return u.layoutFilmstripItem(gtx, u.filmstrip.items[index])
				})
			}),
		)
	})
}

func (u *windowUI) layoutFilmstripHeader(gtx layout.Context) layout.Dimensions {
	count := fmt.Sprintf("%d개", len(u.filmstrip.items))
	if u.filmstrip.scanActive {
		count += " · 갱신 중"
	}
	path := u.filmstripDir()
	if u.filmstrip.sessionDir != "" {
		count = "임시 폴더 · " + count
	}
	buttons := []layout.FlexChild{
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return filmstripHeaderButton(u.theme, &u.filmstrip.openFolder, "폴더 열기").Layout(gtx)
		}),
	}
	if u.filmstrip.sessionDir != "" {
		buttons = append(buttons, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return filmstripHeaderButton(u.theme, &u.filmstrip.defaultFolder, "기본").Layout(gtx)
		}))
	}
	buttons = append(buttons, layout.Rigid(func(gtx layout.Context) layout.Dimensions {
		return filmstripHeaderButton(u.theme, &u.filmstrip.refresh, "새로고침").Layout(gtx)
	}))
	return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx,
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.H6(u.theme, "이전 캡처")
			label.TextSize = unit.Sp(15)
			label.Color = color.NRGBA{R: 245, G: 245, B: 247, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, count)
			label.TextSize = unit.Sp(11)
			label.Color = color.NRGBA{R: 135, G: 135, B: 146, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			label := material.Body2(u.theme, path)
			label.TextSize = unit.Sp(10)
			label.MaxLines = 1
			label.Color = color.NRGBA{R: 105, G: 155, B: 210, A: 255}
			return label.Layout(gtx)
		}),
		layout.Rigid(func(gtx layout.Context) layout.Dimensions {
			return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx, buttons...)
		}),
	)
}

func filmstripHeaderButton(th *material.Theme, button *widget.Clickable, text string) material.ButtonStyle {
	style := neutralButton(th, button, text)
	style.TextSize = unit.Sp(11)
	style.Inset = layout.Inset{Top: unit.Dp(6), Bottom: unit.Dp(6), Left: unit.Dp(7), Right: unit.Dp(7)}
	return style
}

func (u *windowUI) layoutFilmstripMessage(gtx layout.Context, message string) layout.Dimensions {
	return layout.Center.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		label := material.Body2(u.theme, message)
		label.TextSize = unit.Sp(12)
		label.Color = color.NRGBA{R: 135, G: 135, B: 146, A: 255}
		return label.Layout(gtx)
	})
}

func (u *windowUI) layoutFilmstripItem(gtx layout.Context, item *filmstripItem) layout.Dimensions {
	height := gtx.Dp(unit.Dp(142))
	gtx.Constraints.Min.Y = height
	gtx.Constraints.Max.Y = height
	selected := samePath(u.filmstrip.selected, item.file.key.Path)
	card := image.Rectangle{Max: gtx.Constraints.Max}
	bg := color.NRGBA{R: 35, G: 35, B: 41, A: 255}
	if item.click.Hovered() {
		bg = color.NRGBA{R: 44, G: 44, B: 52, A: 255}
	}
	if item.click.Pressed() {
		bg = color.NRGBA{R: 52, G: 52, B: 61, A: 255}
	}
	paint.FillShape(gtx.Ops, bg, clip.RRect{Rect: card, SE: 12, SW: 12, NW: 12, NE: 12}.Op(gtx.Ops))
	if selected {
		drawIconRect(gtx.Ops, image.Rect(1, 1, card.Max.X-1, card.Max.Y-1), color.NRGBA{R: 10, G: 132, B: 255, A: 255}, 2)
	}
	return layout.Inset{Top: unit.Dp(7), Bottom: unit.Dp(7), Left: unit.Dp(7), Right: unit.Dp(5)}.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
		return layout.Flex{Axis: layout.Horizontal, Gap: gtx.Dp(unit.Dp(4))}.Layout(gtx,
			layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
				dims := item.click.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
					semantic.Button.Add(gtx.Ops)
					semantic.LabelOp("이전 캡처 " + item.file.name).Add(gtx.Ops)
					semantic.SelectedOp(selected).Add(gtx.Ops)
					return layout.Flex{Axis: layout.Vertical, Gap: gtx.Dp(unit.Dp(6))}.Layout(gtx,
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							gtx.Constraints.Min.Y = gtx.Dp(unit.Dp(thumbnailDisplayHeight))
							gtx.Constraints.Max.Y = gtx.Constraints.Min.Y
							return u.layoutThumbnail(gtx, item.file.key)
						}),
						layout.Rigid(func(gtx layout.Context) layout.Dimensions {
							return layout.Flex{Axis: layout.Horizontal, Alignment: layout.Middle, Gap: gtx.Dp(unit.Dp(5))}.Layout(gtx,
								layout.Flexed(1, func(gtx layout.Context) layout.Dimensions {
									label := material.Body2(u.theme, item.file.name)
									label.TextSize = unit.Sp(11)
									label.MaxLines = 1
									label.Color = color.NRGBA{R: 225, G: 225, B: 232, A: 255}
									return label.Layout(gtx)
								}),
								layout.Rigid(func(gtx layout.Context) layout.Dimensions {
									label := material.Body2(u.theme, item.file.modified.Format("01/02"))
									label.TextSize = unit.Sp(9)
									label.Color = color.NRGBA{R: 125, G: 125, B: 136, A: 255}
									return label.Layout(gtx)
								}),
							)
						}),
					)
				})
				if item.click.Hovered() {
					u.filmstrip.hoveredKey = item.file.key
					u.filmstrip.hovered = true
				}
				return dims
			}),
			layout.Rigid(func(gtx layout.Context) layout.Dimensions {
				return u.layoutFilmstripRecycleButton(gtx, item)
			}),
		)
	})
}

func (u *windowUI) layoutFilmstripRecycleButton(gtx layout.Context, item *filmstripItem) layout.Dimensions {
	size := image.Pt(gtx.Dp(unit.Dp(34)), gtx.Dp(unit.Dp(40)))
	gtx.Constraints.Min = size
	gtx.Constraints.Max = size
	enabled := !u.filmstrip.recycleActive
	return enabledWidget(gtx, enabled, func(gtx layout.Context) layout.Dimensions {
		return item.recycle.Layout(gtx, func(gtx layout.Context) layout.Dimensions {
			semantic.Button.Add(gtx.Ops)
			semantic.LabelOp("휴지통으로 이동: " + item.file.name).Add(gtx.Ops)
			semantic.EnabledOp(enabled).Add(gtx.Ops)
			bg := color.NRGBA{R: 45, G: 45, B: 52, A: 255}
			fg := color.NRGBA{R: 180, G: 180, B: 192, A: 255}
			if item.recycle.Hovered() && enabled {
				bg = color.NRGBA{R: 92, G: 42, B: 44, A: 255}
				fg = color.NRGBA{R: 255, G: 105, B: 97, A: 255}
			}
			if item.recycle.Pressed() && enabled {
				bg = color.NRGBA{R: 112, G: 46, B: 48, A: 255}
			}
			paint.FillShape(gtx.Ops, bg, clip.RRect{Rect: image.Rectangle{Max: size}, SE: 9, SW: 9, NW: 9, NE: 9}.Op(gtx.Ops))
			drawRecycleIcon(gtx.Ops, size, fg)
			return layout.Dimensions{Size: size}
		})
	})
}

func drawRecycleIcon(ops *op.Ops, size image.Point, c color.NRGBA) {
	cx := size.X / 2
	drawIconRect(ops, image.Rect(cx-6, 13, cx+6, 28), c, 1.5)
	paint.FillShape(ops, c, clip.Rect(image.Rect(cx-8, 10, cx+8, 12)).Op())
	paint.FillShape(ops, c, clip.Rect(image.Rect(cx-3, 7, cx+3, 9)).Op())
}

func (u *windowUI) layoutThumbnail(gtx layout.Context, key thumbnailKey) layout.Dimensions {
	defer clip.RRect{Rect: image.Rectangle{Max: gtx.Constraints.Max}, SE: 8, SW: 8, NW: 8, NE: 8}.Push(gtx.Ops).Pop()
	paint.FillShape(gtx.Ops, color.NRGBA{R: 19, G: 19, B: 23, A: 255}, clip.Rect{Max: gtx.Constraints.Max}.Op())
	u.filmstrip.setListThumbnailBounds(gtx.Constraints.Max)
	if entry, ok := u.filmstrip.cache.get(key); ok {
		img := widget.Image{Src: entry.op, Fit: widget.Contain, Position: layout.Center, Scale: 1}
		return img.Layout(gtx)
	}
	if _, failed := u.filmstrip.thumbErrors[key]; failed {
		return u.layoutFilmstripMessage(gtx, "미리보기 없음")
	}
	u.filmstrip.requestThumbnail(key)
	return u.layoutFilmstripMessage(gtx, "불러오는 중…")
}

func (u *windowUI) layoutFilmstripHoverPreview(gtx layout.Context) {
	if u.filmstrip == nil || !u.filmstrip.expanded || !u.filmstrip.hovered {
		return
	}
	frame := gtx.Dp(unit.Dp(7))
	footerHeight := gtx.Dp(unit.Dp(filmstripHoverFooterHeight))
	availableWidth := gtx.Constraints.Max.X - u.filmstrip.panelWidth - gtx.Dp(unit.Dp(28)) - frame*2
	availableHeight := gtx.Constraints.Max.Y - gtx.Dp(unit.Dp(40)) - footerHeight - frame*2
	maxSize := image.Pt(
		min(gtx.Dp(unit.Dp(filmstripHoverMaxWidth)), availableWidth),
		min(gtx.Dp(unit.Dp(filmstripHoverMaxHeight)), availableHeight),
	)
	if maxSize.X < gtx.Dp(unit.Dp(160)) || maxSize.Y < gtx.Dp(unit.Dp(120)) {
		return
	}
	f := u.filmstrip
	f.requestHoverPreview(hoverPreviewJob{key: f.hoveredKey, bounds: maxSize})
	var previewImage *image.RGBA
	var previewOp paint.ImageOp
	if f.hover.img != nil && f.hover.have.key == f.hoveredKey {
		previewImage, previewOp = f.hover.img, f.hover.op
	} else if entry, ok := f.cache.get(f.hoveredKey); ok && entry.img != nil {
		// Shown until the full-size preview finishes decoding.
		previewImage, previewOp = entry.img, entry.op
	} else {
		return
	}
	sourceSize := previewImage.Bounds().Size()
	previewSize := scaledPreviewSize(sourceSize, maxSize)
	cardSize := previewSize.Add(image.Pt(frame*2, frame*2+footerHeight))
	x := gtx.Constraints.Max.X - u.filmstrip.panelWidth - cardSize.X - gtx.Dp(unit.Dp(12))
	y := (gtx.Constraints.Max.Y - cardSize.Y) / 2
	if x < gtx.Dp(unit.Dp(8)) {
		x = gtx.Dp(unit.Dp(8))
	}
	if y < gtx.Dp(unit.Dp(8)) {
		y = gtx.Dp(unit.Dp(8))
	}
	defer op.Offset(image.Pt(x, y)).Push(gtx.Ops).Pop()
	shadow := image.Rect(0, gtx.Dp(unit.Dp(5)), cardSize.X, cardSize.Y+gtx.Dp(unit.Dp(5)))
	paint.FillShape(gtx.Ops, color.NRGBA{R: 0, G: 0, B: 0, A: 120}, clip.RRect{Rect: shadow, SE: 16, SW: 16, NW: 16, NE: 16}.Op(gtx.Ops))
	paint.FillShape(gtx.Ops, color.NRGBA{R: 35, G: 35, B: 41, A: 255}, clip.RRect{Rect: image.Rectangle{Max: cardSize}, SE: 14, SW: 14, NW: 14, NE: 14}.Op(gtx.Ops))
	imageOffset := op.Offset(image.Pt(frame, frame)).Push(gtx.Ops)
	imageClip := clip.RRect{Rect: image.Rectangle{Max: previewSize}, SE: 9, SW: 9, NW: 9, NE: 9}.Push(gtx.Ops)
	previewContext := gtx
	previewContext.Constraints.Min = previewSize
	previewContext.Constraints.Max = previewSize
	imageOp := previewOp
	imageOp.Filter = paint.FilterLinear
	widget.Image{Src: imageOp, Fit: widget.Contain, Position: layout.Center, Scale: 1}.Layout(previewContext)
	imageClip.Pop()
	imageOffset.Pop()

	footerTop := frame + previewSize.Y
	paint.FillShape(gtx.Ops, color.NRGBA{R: 255, G: 255, B: 255, A: 22}, clip.Rect(image.Rect(frame, footerTop, cardSize.X-frame, footerTop+1)).Op())
	footerOffset := op.Offset(image.Pt(frame, footerTop)).Push(gtx.Ops)
	footerContext := gtx
	footerContext.Constraints = layout.Exact(image.Pt(previewSize.X, footerHeight))
	filename := filepath.Base(u.filmstrip.hoveredKey.Path)
	if filename == "." || filename == "" {
		filename = "이전 캡처"
	}
	layout.Center.Layout(footerContext, func(gtx layout.Context) layout.Dimensions {
		gtx.Constraints.Max.X = max(0, gtx.Constraints.Max.X-gtx.Dp(unit.Dp(20)))
		label := material.Body2(u.theme, filename)
		label.TextSize = unit.Sp(12)
		label.MaxLines = 1
		label.Color = color.NRGBA{R: 225, G: 225, B: 232, A: 255}
		return label.Layout(gtx)
	})
	footerOffset.Pop()
}

func scaledPreviewSize(source, bounds image.Point) image.Point {
	if source.X <= 0 || source.Y <= 0 || bounds.X <= 0 || bounds.Y <= 0 {
		return image.Pt(1, 1)
	}
	scale := math.Min(float64(bounds.X)/float64(source.X), float64(bounds.Y)/float64(source.Y))
	return image.Pt(
		max(1, int(math.Round(float64(source.X)*scale))),
		max(1, int(math.Round(float64(source.Y)*scale))),
	)
}

func (u *windowUI) handleFilmstripButtons(gtx layout.Context) {
	if u.filmstrip == nil {
		return
	}
	for u.filmstrip.toggle.Clicked(gtx) {
		u.filmstrip.expanded = !u.filmstrip.expanded
		u.filmstrip.lastFrame = time.Time{}
		if u.filmstrip.expanded {
			u.filmstrip.requestRefresh(u.filmstripDir())
		}
	}
	for u.filmstrip.refresh.Clicked(gtx) {
		u.filmstrip.thumbErrors = make(map[thumbnailKey]string)
		u.filmstrip.requestRefresh(u.filmstripDir())
	}
	for u.filmstrip.openFolder.Clicked(gtx) {
		u.chooseFilmstripDir()
	}
	for u.filmstrip.defaultFolder.Clicked(gtx) {
		u.restoreDefaultFilmstripDir()
	}
	for _, item := range u.filmstrip.items {
		for item.recycle.Clicked(gtx) {
			u.startFilmstripRecycle(item.file.key.Path)
		}
		for item.click.Clicked(gtx) {
			u.startImageLoad(item.file.key.Path)
		}
	}
}

func (u *windowUI) filmstripDir() string {
	if u.filmstrip != nil && u.filmstrip.sessionDir != "" {
		return u.filmstrip.sessionDir
	}
	return u.configuredSaveDir()
}

func (u *windowUI) chooseFilmstripDir() {
	if u.filmstrip == nil || u.filmstrip.folderActive {
		return
	}
	picker := u.pickFolderPath
	if picker == nil {
		picker = platformwindows.PickFolder
	}
	u.filmstrip.folderActive = true
	u.status = "작업 폴더를 선택하는 중..."
	initial := u.filmstripDir()
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		dir, ok, err := picker(initial, windowTitle)
		select {
		case u.filmstrip.folderResults <- folderPickResult{dir: dir, ok: ok, err: err}:
		case <-ctx.Done():
		}
	}()
}

func (u *windowUI) handleFilmstripFolderResult(result folderPickResult) {
	if u.filmstrip == nil || !u.filmstrip.folderActive {
		return
	}
	u.filmstrip.folderActive = false
	if result.err != nil {
		u.status = "작업 폴더 선택 실패: " + result.err.Error()
		u.showToastDetail("작업 폴더 선택 실패", result.err.Error())
		return
	}
	if !result.ok {
		u.status = "작업 폴더 선택을 취소했습니다."
		return
	}
	dir := filepath.Clean(strings.TrimSpace(result.dir))
	u.filmstrip.sessionDir = dir
	u.filmstrip.requestRefresh(dir)
	u.status = "임시 작업 폴더를 열었습니다: " + dir
}

func (u *windowUI) clearDeletedImage() {
	u.sourceImagePath = ""
	u.imageLoadGeneration++
	u.imageLoadActive = false
	u.resetCanvasView()
	u.clearEditHistory()
	if u.controller != nil {
		u.controller.ResetEditingSession()
	}
}

func (u *windowUI) selectImageAfterRecycle(path string) {
	index := -1
	for i, item := range u.filmstrip.items {
		if samePath(item.file.key.Path, path) {
			index = i
			break
		}
	}
	u.filmstrip.removePath(path)
	if index < 0 || len(u.filmstrip.items) == 0 {
		u.filmstrip.selected = ""
		u.clearDeletedImage()
		return
	}
	if index >= len(u.filmstrip.items) {
		index = len(u.filmstrip.items) - 1
	}
	next := u.filmstrip.items[index].file.key.Path
	u.clearDeletedImage()
	u.filmstrip.selected = next
	u.startImageLoad(next)
}

func (u *windowUI) restoreDefaultFilmstripDir() {
	if u.filmstrip == nil || u.filmstrip.sessionDir == "" {
		return
	}
	u.filmstrip.sessionDir = ""
	dir := u.configuredSaveDir()
	u.filmstrip.requestRefresh(dir)
	u.status = "이전 캡처 목록을 기본 폴더로 되돌렸습니다: " + dir
}

func (u *windowUI) startFilmstripRecycle(path string) {
	if u.filmstrip == nil || u.filmstrip.recycleActive {
		return
	}
	u.filmstrip.recycleActive = true
	u.filmstrip.recyclePath = path
	u.status = "휴지통으로 이동하는 중: " + filepath.Base(path)
	ctx := u.ctx
	if ctx == nil {
		ctx = context.Background()
	}
	go func() {
		err := platformwindows.MoveFileToRecycleBin(path)
		select {
		case u.filmstrip.recycleResults <- filmstripRecycleResult{path: path, err: err}:
		case <-ctx.Done():
		}
	}()
}

func (u *windowUI) handleFilmstripRecycleResult(result filmstripRecycleResult) {
	if u.filmstrip == nil || !samePath(u.filmstrip.recyclePath, result.path) {
		return
	}
	u.filmstrip.recycleActive = false
	u.filmstrip.recyclePath = ""
	name := filepath.Base(result.path)
	if result.err != nil {
		u.status = "휴지통 이동 실패: " + result.err.Error()
		u.showToastDetail("휴지통 이동 실패", name+" · "+result.err.Error())
		u.requestFilmstripRefresh()
		return
	}
	if samePath(u.filmstrip.selected, result.path) {
		u.selectImageAfterRecycle(result.path)
	} else {
		u.filmstrip.removePath(result.path)
	}
	u.filmstrip.invalidateActiveScan()
	u.status = "휴지통으로 이동했습니다: " + name
	u.showToastDetail("휴지통으로 이동", name)
}

func (f *filmstripState) removePath(path string) {
	items := f.items[:0]
	for _, item := range f.items {
		if samePath(item.file.key.Path, path) {
			f.cache.remove(item.file.key)
			delete(f.thumbErrors, item.file.key)
			delete(f.queued, item.file.key)
			continue
		}
		items = append(items, item)
	}
	f.items = items
}

func (f *filmstripState) invalidateActiveScan() {
	if f.scanActive {
		f.generation++
		f.scanAgain = false
	}
}

func (u *windowUI) requestFilmstripRefresh() {
	if u.filmstrip == nil {
		return
	}
	dir := u.filmstripDir()
	if !u.filmstrip.expanded {
		u.filmstrip.dirty = true
		if u.filmstrip.scanActive && !samePath(u.filmstrip.pendingDir, dir) {
			u.filmstrip.generation++
			u.filmstrip.pendingDir = dir
			u.filmstrip.scanAgain = false
		}
		if u.filmstrip.dir != "" && !samePath(u.filmstrip.dir, dir) {
			u.filmstrip.items = nil
			u.filmstrip.dir = ""
			u.filmstrip.cache.clear()
			u.filmstrip.thumbErrors = make(map[thumbnailKey]string)
		}
		return
	}
	u.filmstrip.requestRefresh(dir)
}
