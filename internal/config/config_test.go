package config

import (
	"image"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.Hotkey != "Ctrl+Shift+C" {
		t.Fatalf("unexpected default hotkey: %q", cfg.Hotkey)
	}
	if cfg.HotkeyOpensWindow {
		t.Fatal("hotkey should start capture by default")
	}
	if cfg.HideOnFocusLoss || !cfg.HideOnMinimize {
		t.Fatal("focus-loss hide should be off and minimize hide should be on by default")
	}
	if cfg.TraySettingsVersion != 2 {
		t.Fatalf("unexpected tray settings version: %d", cfg.TraySettingsVersion)
	}
	if !cfg.EditAfterCapture {
		t.Fatal("editing should be enabled by default")
	}
	if cfg.SavedRegionEnabled || !cfg.SavedRegion.Rect().Empty() || len(cfg.SavedRegions) != 0 || cfg.ActiveSavedRegionID != "" {
		t.Fatal("saved region should be disabled by default")
	}
	if cfg.StrokeColor != "Red" || cfg.StrokeWidth != 3 {
		t.Fatalf("unexpected stroke defaults: %s %d", cfg.StrokeColor, cfg.StrokeWidth)
	}
	if cfg.OutputBorderEnabled || cfg.OutputBorderColor != "Black" || cfg.OutputBorderWidth != 4 {
		t.Fatalf("unexpected output border defaults: enabled=%v color=%s width=%d", cfg.OutputBorderEnabled, cfg.OutputBorderColor, cfg.OutputBorderWidth)
	}
	if cfg.HighlighterWidth != 14 || cfg.HighlighterAlpha != 180 {
		t.Fatalf("unexpected highlighter defaults: %d %d", cfg.HighlighterWidth, cfg.HighlighterAlpha)
	}
	if cfg.MosaicBlockSize != 12 || cfg.MosaicWidth != 24 {
		t.Fatalf("unexpected mosaic defaults: block=%d width=%d", cfg.MosaicBlockSize, cfg.MosaicWidth)
	}
	if cfg.CaptureMode != "Region" {
		t.Fatalf("unexpected capture mode: %q", cfg.CaptureMode)
	}
	if cfg.AutoSave {
		t.Fatal("auto save should be off by default")
	}
	if cfg.SaveDir != "" {
		t.Fatalf("unexpected default save dir: %q", cfg.SaveDir)
	}
}

func TestNormalizeSavedRegionsDropsInvalidAndDuplicateEntries(t *testing.T) {
	region := ScreenRegionFromRect(image.Rect(10, 20, 110, 120))
	got := normalizeSavedRegions([]SavedRegionPreset{
		{ID: "a", Region: region},
		{ID: "a", Name: "duplicate", Region: region},
		{ID: "empty", Name: "empty"},
	})
	if len(got) != 1 || got[0].ID != "a" || got[0].Name != "영역 1" {
		t.Fatalf("normalized presets = %#v", got)
	}
}

func TestMigrateLegacySavedRegionIntoPresetList(t *testing.T) {
	want := image.Rect(-500, 20, 100, 420)
	cfg := Config{SavedRegionEnabled: true, SavedRegion: ScreenRegionFromRect(want)}
	migrateSavedRegions(&cfg)
	if len(cfg.SavedRegions) != 1 || cfg.SavedRegions[0].Region.Rect() != want {
		t.Fatalf("migrated regions = %#v", cfg.SavedRegions)
	}
	if cfg.ActiveSavedRegionID != cfg.SavedRegions[0].ID || !cfg.SavedRegion.Rect().Empty() {
		t.Fatalf("active=%q legacy=%v", cfg.ActiveSavedRegionID, cfg.SavedRegion.Rect())
	}
}

func TestMigrateUsesLegacyRegionWhenPresetListIsInvalid(t *testing.T) {
	want := image.Rect(30, 40, 130, 140)
	cfg := Config{
		SavedRegionEnabled: true,
		SavedRegion:        ScreenRegionFromRect(want),
		SavedRegions:       []SavedRegionPreset{{ID: "invalid"}},
	}
	migrateSavedRegions(&cfg)
	if len(cfg.SavedRegions) != 1 || cfg.SavedRegions[0].Region.Rect() != want || cfg.ActiveSavedRegionID != "legacy-region" {
		t.Fatalf("migrated mixed config = %#v active=%q", cfg.SavedRegions, cfg.ActiveSavedRegionID)
	}
}

func TestScreenRegionRoundTrip(t *testing.T) {
	want := image.Rect(-1200, 40, -400, 640)
	if got := ScreenRegionFromRect(want).Rect(); got != want {
		t.Fatalf("region round trip = %v, want %v", got, want)
	}
}

func TestNormalizeSaveDir(t *testing.T) {
	if got := NormalizeSaveDir(` "C:\Users\me\Pictures\Screenshots" `); got != `C:\Users\me\Pictures\Screenshots` {
		t.Fatalf("unexpected normalized dir: %q", got)
	}
	if got := NormalizeSaveDir("  "); got != "" {
		t.Fatalf("blank dir should normalize to empty, got %q", got)
	}
}
