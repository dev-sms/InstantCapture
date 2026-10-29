package ui

import (
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gioui.org/app"
	"gioui.org/layout"
	"gioui.org/op"
	"gioui.org/unit"
	"gioui.org/widget"

	"instantcapture/internal/config"
	"instantcapture/internal/workflow"
)

func TestSupportedImageExtension(t *testing.T) {
	for _, name := range []string{"capture.png", "capture.JPG", "capture.jpeg", "capture.gif", "capture.bmp", "capture.webp"} {
		if !supportedImageExtension(name) {
			t.Fatalf("expected %q to be supported", name)
		}
	}
	for _, name := range []string{"capture.txt", "capture.svg", "capture.png.tmp", "capture"} {
		if supportedImageExtension(name) {
			t.Fatalf("expected %q to be rejected", name)
		}
	}
}

func TestScanCaptureFolderFiltersSortsAndLimits(t *testing.T) {
	dir := t.TempDir()
	base := time.Date(2026, 7, 21, 12, 0, 0, 0, time.Local)
	writeTimedFile(t, filepath.Join(dir, "old.png"), base)
	writeTimedFile(t, filepath.Join(dir, "new.JPG"), base.Add(2*time.Minute))
	writeTimedFile(t, filepath.Join(dir, "middle.webp"), base.Add(time.Minute))
	writeTimedFile(t, filepath.Join(dir, "ignored.txt"), base.Add(3*time.Minute))

	files, err := scanCaptureFolder(dir, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 {
		t.Fatalf("files = %d, want 2", len(files))
	}
	if files[0].name != "new.JPG" || files[1].name != "middle.webp" {
		t.Fatalf("unexpected order: %q, %q", files[0].name, files[1].name)
	}
}

func TestScanCaptureFolderReturnsMoreThanPreviousLimit(t *testing.T) {
	dir := t.TempDir()
	for i := range 1205 {
		path := filepath.Join(dir, fmt.Sprintf("capture-%04d.png", i))
		if err := os.WriteFile(path, nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	files, err := scanCaptureFolder(dir, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(files); got != 1205 {
		t.Fatalf("files = %d, want 1205", got)
	}
}

func TestScanCaptureFolderMissingDirectory(t *testing.T) {
	if _, err := scanCaptureFolder(filepath.Join(t.TempDir(), "missing"), 10); err == nil {
		t.Fatal("expected a missing-directory error")
	}
}

func TestRefreshCoalescesWhileScanIsActive(t *testing.T) {
	state := &filmstripState{
		scanActive: true,
		generation: 1,
		pendingDir: "old",
	}

	state.requestRefresh("new")

	if state.generation != 2 || !state.scanAgain {
		t.Fatalf("generation = %d, scanAgain = %v", state.generation, state.scanAgain)
	}
	if state.pendingDir != filepath.Clean("new") {
		t.Fatalf("pending dir = %q", state.pendingDir)
	}
}

func TestStaleScanResultDoesNotReplaceItems(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	current := &filmstripItem{file: filmstripFile{name: "current.png"}}
	state := &filmstripState{
		ctx:         ctx,
		items:       []*filmstripItem{current},
		generation:  2,
		scanActive:  true,
		scanAgain:   true,
		pendingDir:  t.TempDir(),
		scanResults: make(chan filmstripScanResult, 1),
	}

	state.applyScanResult(filmstripScanResult{
		generation: 1,
		files:      []filmstripFile{{name: "stale.png"}},
	})

	if len(state.items) != 1 || state.items[0] != current {
		t.Fatal("stale scan result replaced current items")
	}
}

func TestDecodeThumbnailPreservesAspectRatio(t *testing.T) {
	path := filepath.Join(t.TempDir(), "wide.png")
	writePNG(t, path, image.Pt(400, 200))
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	key := thumbnailKey{Path: path, Size: info.Size(), ModifiedNS: info.ModTime().UnixNano()}

	thumb, err := decodeThumbnail(key, 180, 104)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := thumb.Bounds().Size(), image.Pt(180, 90); got != want {
		t.Fatalf("thumbnail size = %v, want %v", got, want)
	}
}

func TestHoverPreviewScalesLargerThanFilmstripThumbnail(t *testing.T) {
	got := scaledPreviewSize(image.Pt(720, 360), image.Pt(filmstripHoverMaxWidth, filmstripHoverMaxHeight))
	if want := image.Pt(720, 360); got != want {
		t.Fatalf("preview size = %v, want %v", got, want)
	}
	if got.Y <= thumbnailDisplayHeight {
		t.Fatalf("preview height = %d, should exceed thumbnail height", got.Y)
	}
	if filmstripHoverFooterHeight < 40 {
		t.Fatalf("hover footer height = %d, want room for a filename", filmstripHoverFooterHeight)
	}
}

func TestFilmstripHoverPreviewLayoutsWithoutInputSurface(t *testing.T) {
	key := thumbnailKey{Path: "capture.png"}
	cache := newThumbnailCache(4, 8<<20)
	cache.add(key, image.NewRGBA(image.Rect(0, 0, 720, 360)))
	u := &windowUI{
		theme: newTheme(),
		filmstrip: &filmstripState{
			expanded:   true,
			hovered:    true,
			hoveredKey: key,
			panelWidth: filmstripExpandedWidth,
			cache:      cache,
		},
	}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(1100, 760)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}

	u.layoutFilmstripHoverPreview(gtx)
}

func TestThumbnailCacheEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newThumbnailCache(2, 1<<20)
	first := thumbnailKey{Path: "first"}
	second := thumbnailKey{Path: "second"}
	third := thumbnailKey{Path: "third"}
	cache.add(first, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	cache.add(second, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	if _, ok := cache.get(first); !ok {
		t.Fatal("expected first item in cache")
	}
	cache.add(third, image.NewRGBA(image.Rect(0, 0, 10, 10)))

	if _, ok := cache.entries[second]; ok {
		t.Fatal("least recently used item was not evicted")
	}
	if _, ok := cache.entries[first]; !ok {
		t.Fatal("recently used item was evicted")
	}
}

func TestThumbnailCacheHonorsByteBudget(t *testing.T) {
	cache := newThumbnailCache(10, 500)
	first := thumbnailKey{Path: "first"}
	second := thumbnailKey{Path: "second"}
	cache.add(first, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	cache.add(second, image.NewRGBA(image.Rect(0, 0, 10, 10)))

	if _, ok := cache.entries[first]; ok {
		t.Fatal("old image should be evicted to satisfy byte budget")
	}
	if _, ok := cache.entries[second]; !ok {
		t.Fatal("new image should remain cached")
	}
}

func TestThumbnailRequestsAreDeduplicated(t *testing.T) {
	state := &filmstripState{
		thumbJobs:   make(chan thumbnailJob, 2),
		queued:      make(map[thumbnailKey]struct{}),
		thumbErrors: make(map[thumbnailKey]string),
		cache:       newThumbnailCache(4, 1<<20),
	}
	key := thumbnailKey{Path: "capture.png"}

	state.requestThumbnail(key)
	state.requestThumbnail(key)

	if got := len(state.thumbJobs); got != 1 {
		t.Fatalf("queued jobs = %d, want 1", got)
	}
}

func TestStaleThumbnailResultIsIgnored(t *testing.T) {
	current := thumbnailKey{Path: "capture.png", ModifiedNS: 2}
	stale := thumbnailKey{Path: "capture.png", ModifiedNS: 1}
	state := &filmstripState{
		items:       []*filmstripItem{{file: filmstripFile{key: current}}},
		queued:      map[thumbnailKey]struct{}{stale: {}},
		thumbErrors: make(map[thumbnailKey]string),
		cache:       newThumbnailCache(4, 1<<20),
	}

	state.applyThumbnailResult(thumbnailResult{key: stale, img: image.NewRGBA(image.Rect(0, 0, 10, 10))})

	if len(state.cache.entries) != 0 {
		t.Fatal("stale thumbnail was added to cache")
	}
}

func TestThumbnailResultForOldBoundsIsIgnored(t *testing.T) {
	key := thumbnailKey{Path: "capture.png"}
	state := &filmstripState{
		items:       []*filmstripItem{{file: filmstripFile{key: key}}},
		queued:      make(map[thumbnailKey]struct{}),
		thumbErrors: make(map[thumbnailKey]string),
		cache:       newThumbnailCache(4, 1<<20),
	}
	state.cache.add(key, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	state.setListThumbnailBounds(image.Pt(180, 156))
	if len(state.cache.entries) != 0 {
		t.Fatal("thumbnails for the previous box size should be dropped")
	}
	state.applyThumbnailResult(thumbnailResult{key: key, bounds: image.Pt(120, 104), img: image.NewRGBA(image.Rect(0, 0, 10, 10))})
	if len(state.cache.entries) != 0 {
		t.Fatal("result decoded for the old box size was cached")
	}
	state.applyThumbnailResult(thumbnailResult{key: key, bounds: image.Pt(180, 156), img: image.NewRGBA(image.Rect(0, 0, 10, 10))})
	if _, ok := state.cache.get(key); !ok {
		t.Fatal("current-size result should be cached")
	}
}

func TestHoverPreviewKeepsOnlyNewestRequest(t *testing.T) {
	state := &filmstripState{hoverJobs: make(chan hoverPreviewJob, 1)}
	first := hoverPreviewJob{key: thumbnailKey{Path: "a.png"}, bounds: image.Pt(720, 480)}
	second := hoverPreviewJob{key: thumbnailKey{Path: "b.png"}, bounds: image.Pt(720, 480)}
	state.requestHoverPreview(first)
	state.requestHoverPreview(first)
	state.requestHoverPreview(second)
	if len(state.hoverJobs) != 1 || <-state.hoverJobs != second {
		t.Fatal("only the latest hover target should be queued")
	}

	state.applyHoverPreviewResult(hoverPreviewResult{job: first, img: image.NewRGBA(image.Rect(0, 0, 4, 4))})
	if state.hover.img != nil {
		t.Fatal("a preview for an abandoned hover target must be dropped")
	}
	img := image.NewRGBA(image.Rect(0, 0, 4, 4))
	state.applyHoverPreviewResult(hoverPreviewResult{job: second, img: img})
	if state.hover.img != img || state.hover.have != second {
		t.Fatal("current preview should be kept")
	}
	state.requestHoverPreview(first)
	state.requestHoverPreview(second)
	if len(state.hoverJobs) != 1 || <-state.hoverJobs != first {
		t.Fatal("returning to an already decoded preview should not decode it again")
	}

	state.releaseHoverPreview()
	if state.hover.img != nil {
		t.Fatal("release should drop the large preview")
	}
}

func TestDecodeThumbnailKeepsThinLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lines.png")
	src := image.NewRGBA(image.Rect(0, 0, 1600, 900))
	for y := range 900 {
		for x := range 1600 {
			src.Pix[src.PixOffset(x, y)+3] = 255
			if x%4 == 0 {
				copy(src.Pix[src.PixOffset(x, y):], []byte{255, 255, 255, 255})
			}
		}
	}
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(file, src); err != nil {
		t.Fatal(err)
	}
	file.Close()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	thumb, err := decodeThumbnail(thumbnailKey{Path: path, Size: info.Size(), ModifiedNS: info.ModTime().UnixNano()}, 200, 200)
	if err != nil {
		t.Fatal(err)
	}
	// 1px white lines every 4px average to ~1/4 brightness everywhere; a
	// point-sampling scaler would show solid black or white columns instead.
	for x := range thumb.Bounds().Dx() {
		if v := thumb.RGBAAt(x, 50).R; v < 40 || v > 90 {
			t.Fatalf("column %d brightness %d, want an even ~64", x, v)
		}
	}
}

func TestReplaceFilesPrunesObsoleteThumbnailErrors(t *testing.T) {
	current := thumbnailKey{Path: "current.png", ModifiedNS: 2}
	obsolete := thumbnailKey{Path: "old.png", ModifiedNS: 1}
	state := &filmstripState{
		thumbErrors: map[thumbnailKey]string{current: "bad", obsolete: "bad"},
		queued:      make(map[thumbnailKey]struct{}),
		cache:       newThumbnailCache(4, 1<<20),
	}

	state.replaceFiles("screenshots", []filmstripFile{{key: current, name: "current.png"}})

	if _, ok := state.thumbErrors[obsolete]; ok {
		t.Fatal("obsolete thumbnail error was not pruned")
	}
	if _, ok := state.thumbErrors[current]; !ok {
		t.Fatal("current thumbnail error should be retained")
	}
}

func TestFilmstripUsesCollapsedAndExpandedWidths(t *testing.T) {
	u := &windowUI{filmstrip: &filmstripState{progress: 0}}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(900, 400)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}
	if got := u.filmstripWidth(gtx); got != 30 {
		t.Fatalf("collapsed width = %d, want 30", got)
	}
	u.filmstrip.expanded = true
	u.filmstrip.progress = 1
	u.filmstrip.lastFrame = gtx.Now
	if got := u.filmstripWidth(gtx); got != 220 {
		t.Fatalf("expanded width = %d, want 220", got)
	}
}

func TestFilmstripExpansionWidthMovesMonotonicallyFromRest(t *testing.T) {
	start := time.Now()
	state := &filmstripState{expanded: true, progress: 0, lastFrame: start}
	u := &windowUI{filmstrip: state}
	previous := filmstripCollapsedWidth
	for frame := 1; frame <= 30; frame++ {
		var ops op.Ops
		gtx := layout.Context{
			Constraints: layout.Exact(image.Pt(900, 400)),
			Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
			Now:         start.Add(time.Duration(frame) * time.Second / 60),
			Ops:         &ops,
		}
		width := u.filmstripWidth(gtx)
		if width < previous || width > filmstripExpandedWidth {
			t.Fatalf("frame %d width moved out of range: %d -> %d", frame, previous, width)
		}
		previous = width
	}
	if previous <= filmstripCollapsedWidth {
		t.Fatal("filmstrip did not begin expanding")
	}
}

func TestFilmstripRailClickableLayoutUsesFullHeight(t *testing.T) {
	u := &windowUI{theme: newTheme(), filmstrip: &filmstripState{}}
	var ops op.Ops
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(30, 400)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         time.Now(),
		Ops:         &ops,
	}
	if got := u.layoutFilmstripRail(gtx).Size; got != image.Pt(30, 400) {
		t.Fatalf("rail clickable size = %v, want 30x400", got)
	}
}

func TestExpandedFilmstripWorkspaceFitsMinimumWidth(t *testing.T) {
	state := &filmstripState{
		expanded:    true,
		progress:    1,
		list:        widget.List{List: layout.List{Axis: layout.Vertical}},
		queued:      make(map[thumbnailKey]struct{}),
		thumbErrors: make(map[thumbnailKey]string),
		cache:       newThumbnailCache(4, 1<<20),
	}
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		theme:      newTheme(),
		filmstrip:  state,
		appConfig:  config.Default(),
		canvasZoom: 1,
	}
	var ops op.Ops
	now := time.Now()
	state.lastFrame = now
	gtx := layout.Context{
		Constraints: layout.Exact(image.Pt(900, 492)),
		Metric:      unit.Metric{PxPerDp: 1, PxPerSp: 1},
		Now:         now,
		Ops:         &ops,
	}

	dims := u.layoutWorkspace(gtx, nil)

	if got, want := dims.Size, image.Pt(900, 492); got != want {
		t.Fatalf("workspace size = %v, want %v", got, want)
	}
}

func TestStartImageLoadOpensFilmstripSelectionInEditor(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	writePNG(t, path, image.Pt(120, 80))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	u := &windowUI{
		ctx:              ctx,
		controller:       workflow.NewController(&uiFakeCapturer{}),
		filmstrip:        &filmstripState{},
		imageLoadResults: make(chan imageLoadResult, 1),
	}

	u.startImageLoad(path)
	result := <-u.imageLoadResults
	u.handleImageLoadResult(result, new(app.Window))

	if u.controller.Mode != workflow.ModeEditing {
		t.Fatalf("mode = %v, want editing", u.controller.Mode)
	}
	if !samePath(u.filmstrip.selected, path) {
		t.Fatalf("selected = %q, want %q", u.filmstrip.selected, path)
	}
}

func TestStaleImageLoadCannotReplaceNewerSession(t *testing.T) {
	u := &windowUI{
		controller:          workflow.NewController(&uiFakeCapturer{}),
		imageLoadActive:     true,
		imageLoadGeneration: 2,
	}
	stale := imageLoadResult{
		generation: 1,
		path:       "old.png",
		img:        image.NewRGBA(image.Rect(0, 0, 120, 80)),
	}

	u.handleImageLoadResult(stale, nil)

	if u.controller.Mode == workflow.ModeEditing {
		t.Fatal("stale image load replaced the current session")
	}
	if !u.imageLoadActive {
		t.Fatal("stale result changed the newer load state")
	}
}

func TestFilmstripImageLoadIsBlockedDuringCapture(t *testing.T) {
	u := &windowUI{captureActive: true}

	u.startImageLoad("capture.png")

	if u.imageLoadActive || u.imageLoadGeneration != 0 {
		t.Fatal("image load started while capture was active")
	}
}

func TestNewCaptureClearsFilmstripSelection(t *testing.T) {
	u := &windowUI{
		controller: workflow.NewController(&uiFakeCapturer{}),
		filmstrip:  &filmstripState{selected: "old.png"},
	}

	u.finishCaptureReadyWithInitialCopy("캡처 완료")

	if u.filmstrip.selected != "" {
		t.Fatalf("selection = %q, want empty", u.filmstrip.selected)
	}
}

func TestCollapsedFilmstripRefreshOnlyMarksDirty(t *testing.T) {
	dir := t.TempDir()
	state := &filmstripState{
		cache:       newThumbnailCache(4, 1<<20),
		thumbErrors: make(map[thumbnailKey]string),
	}
	u := &windowUI{filmstrip: state, appConfig: config.Config{SaveDir: dir}}

	u.requestFilmstripRefresh()

	if !state.dirty {
		t.Fatal("collapsed filmstrip should be marked dirty")
	}
	if state.scanActive {
		t.Fatal("collapsed filmstrip should not start a scan")
	}
}

func TestFilmstripDirPrefersSessionFolder(t *testing.T) {
	defaultDir := t.TempDir()
	sessionDir := t.TempDir()
	u := &windowUI{
		appConfig: config.Config{SaveDir: defaultDir},
		filmstrip: &filmstripState{sessionDir: sessionDir},
	}
	if got := u.filmstripDir(); got != sessionDir {
		t.Fatalf("filmstrip dir = %q, want session dir %q", got, sessionDir)
	}
	u.filmstrip.sessionDir = ""
	if got := u.filmstripDir(); got != defaultDir {
		t.Fatalf("filmstrip dir = %q, want default dir %q", got, defaultDir)
	}
}

func TestChooseFilmstripDirIsSessionOnly(t *testing.T) {
	defaultDir := t.TempDir()
	sessionDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := newFilmstripState(ctx)
	state.expanded = true
	saved := 0
	u := &windowUI{
		appConfig: config.Config{SaveDir: defaultDir},
		filmstrip: state,
		pickFolderPath: func(initial, ownerTitle string) (string, bool, error) {
			return sessionDir, true, nil
		},
		saveConfig: func(config.Config) error {
			saved++
			return nil
		},
	}
	u.chooseFilmstripDir()
	if !state.folderActive || state.sessionDir != "" {
		t.Fatal("folder picker should run without blocking or applying its result early")
	}
	select {
	case result := <-state.folderResults:
		u.handleFilmstripFolderResult(result)
	case <-time.After(time.Second):
		t.Fatal("folder picker did not return")
	}
	if state.sessionDir != sessionDir || state.pendingDir != sessionDir {
		t.Fatalf("session=%q pending=%q", state.sessionDir, state.pendingDir)
	}
	if u.appConfig.SaveDir != defaultDir || saved != 0 {
		t.Fatalf("default save dir changed: %q, saves=%d", u.appConfig.SaveDir, saved)
	}
}

func TestRecycleSelectedImageLoadsFollowingItem(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "first.png")
	second := filepath.Join(dir, "second.png")
	writePNG(t, second, image.Pt(20, 10))
	state := &filmstripState{
		items: []*filmstripItem{
			{file: filmstripFile{key: thumbnailKey{Path: first}}},
			{file: filmstripFile{key: thumbnailKey{Path: second}}},
		},
		selected: first, recycleActive: true, recyclePath: first,
		cache: newThumbnailCache(4, 1<<20), queued: make(map[thumbnailKey]struct{}), thumbErrors: make(map[thumbnailKey]string),
	}
	u := &windowUI{
		ctx: context.Background(), controller: workflow.NewController(nil), filmstrip: state,
		imageLoadResults: make(chan imageLoadResult, 1), appConfig: config.Config{SaveDir: dir},
		sourceImagePath: first,
	}
	if err := u.controller.CommitImportedImage(image.NewRGBA(image.Rect(0, 0, 5, 5))); err != nil {
		t.Fatal(err)
	}
	u.handleFilmstripRecycleResult(filmstripRecycleResult{path: first})
	if state.selected != second || !u.imageLoadActive {
		t.Fatalf("selected=%q loading=%v", state.selected, u.imageLoadActive)
	}
	if u.sourceImagePath != "" || u.controller.Mode != workflow.ModeIdle {
		t.Fatalf("deleted image remained editable: source=%q mode=%s", u.sourceImagePath, u.controller.Mode)
	}
	select {
	case result := <-u.imageLoadResults:
		u.handleImageLoadResult(result, nil)
	case <-time.After(time.Second):
		t.Fatal("following image did not load")
	}
	if u.sourceImagePath != second || u.controller.Mode != workflow.ModeEditing {
		t.Fatalf("source=%q mode=%s", u.sourceImagePath, u.controller.Mode)
	}
}

func TestRestoreDefaultFilmstripDirUsesLatestSaveDir(t *testing.T) {
	defaultDir := t.TempDir()
	sessionDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := newFilmstripState(ctx)
	state.expanded = true
	state.sessionDir = sessionDir
	u := &windowUI{appConfig: config.Config{SaveDir: defaultDir}, filmstrip: state}
	u.restoreDefaultFilmstripDir()
	if state.sessionDir != "" || state.pendingDir != defaultDir {
		t.Fatalf("session=%q pending=%q", state.sessionDir, state.pendingDir)
	}
}

func TestFilmstripRefreshKeepsSessionFolder(t *testing.T) {
	defaultDir := t.TempDir()
	sessionDir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	state := newFilmstripState(ctx)
	state.expanded = true
	state.sessionDir = sessionDir
	u := &windowUI{appConfig: config.Config{SaveDir: defaultDir}, filmstrip: state}
	u.requestFilmstripRefresh()
	if state.pendingDir != sessionDir {
		t.Fatalf("refresh dir = %q, want session dir %q", state.pendingDir, sessionDir)
	}
}

func TestRecycleSuccessRemovesItemAndClearsSelection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.png")
	key := thumbnailKey{Path: path}
	state := &filmstripState{
		items:         []*filmstripItem{{file: filmstripFile{key: key, name: "capture.png"}}},
		expanded:      true,
		selected:      path,
		recycleActive: true,
		recyclePath:   path,
		cache:         newThumbnailCache(4, 1<<20),
		queued:        map[thumbnailKey]struct{}{key: {}},
		thumbErrors:   map[thumbnailKey]string{key: "old error"},
	}
	state.cache.add(key, image.NewRGBA(image.Rect(0, 0, 10, 10)))
	u := &windowUI{filmstrip: state, appConfig: config.Config{SaveDir: dir}}

	u.handleFilmstripRecycleResult(filmstripRecycleResult{path: path})

	if len(state.items) != 0 {
		t.Fatalf("items = %d, want 0", len(state.items))
	}
	if state.selected != "" || state.recycleActive {
		t.Fatalf("selected = %q, recycleActive = %v", state.selected, state.recycleActive)
	}
	if _, ok := state.cache.entries[key]; ok {
		t.Fatal("recycled thumbnail remained cached")
	}
	if state.scanActive {
		t.Fatal("successful recycle should not trigger a full folder refresh")
	}
	if !strings.Contains(u.status, "휴지통으로 이동했습니다") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestRecycleFailureKeepsItem(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.png")
	state := &filmstripState{
		items:         []*filmstripItem{{file: filmstripFile{key: thumbnailKey{Path: path}}}},
		recycleActive: true,
		recyclePath:   path,
		cache:         newThumbnailCache(4, 1<<20),
		queued:        make(map[thumbnailKey]struct{}),
		thumbErrors:   make(map[thumbnailKey]string),
	}
	u := &windowUI{filmstrip: state, appConfig: config.Config{SaveDir: dir}}

	u.handleFilmstripRecycleResult(filmstripRecycleResult{path: path, err: errors.New("busy")})

	if len(state.items) != 1 {
		t.Fatalf("items = %d, want 1", len(state.items))
	}
	if !strings.Contains(u.status, "휴지통 이동 실패") {
		t.Fatalf("unexpected status: %q", u.status)
	}
}

func TestRecycleSuccessInvalidatesStaleScanResult(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.png")
	key := thumbnailKey{Path: path}
	state := &filmstripState{
		items:         []*filmstripItem{{file: filmstripFile{key: key, name: "capture.png"}}},
		expanded:      true,
		scanActive:    true,
		generation:    4,
		recycleActive: true,
		recyclePath:   path,
		cache:         newThumbnailCache(4, 1<<20),
		queued:        make(map[thumbnailKey]struct{}),
		thumbErrors:   make(map[thumbnailKey]string),
	}
	u := &windowUI{filmstrip: state, appConfig: config.Config{SaveDir: dir}}

	u.handleFilmstripRecycleResult(filmstripRecycleResult{path: path})
	state.applyScanResult(filmstripScanResult{generation: 4, dir: dir, files: []filmstripFile{{key: key, name: "capture.png"}}})

	if len(state.items) != 0 {
		t.Fatal("stale scan result restored a recycled item")
	}
	if state.scanActive {
		t.Fatal("stale scan should finish without starting another refresh")
	}
}

func TestValidImageDimensionsRejectsInvalidAndOversizedImages(t *testing.T) {
	if !validImageDimensions(3840, 2160, maxEditorImagePixels) {
		t.Fatal("expected 4K image to be accepted")
	}
	if validImageDimensions(0, 2160, maxEditorImagePixels) {
		t.Fatal("zero-width image should be rejected")
	}
	if validImageDimensions(100_000, 100_000, maxEditorImagePixels) {
		t.Fatal("oversized image should be rejected")
	}
}

func writeTimedFile(t *testing.T, path string, modified time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, modified, modified); err != nil {
		t.Fatal(err)
	}
}

func writePNG(t *testing.T, path string, size image.Point) {
	t.Helper()
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	img := image.NewRGBA(image.Rectangle{Max: size})
	for y := 0; y < size.Y; y++ {
		for x := 0; x < size.X; x++ {
			img.SetRGBA(x, y, color.RGBA{R: uint8(x), G: uint8(y), B: 180, A: 255})
		}
	}
	if err := png.Encode(file, img); err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
