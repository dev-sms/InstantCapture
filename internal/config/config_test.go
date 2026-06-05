package config

import "testing"

func TestDefaultConfig(t *testing.T) {
	cfg := Default()
	if cfg.Hotkey != "Ctrl+Shift+C" {
		t.Fatalf("unexpected default hotkey: %q", cfg.Hotkey)
	}
	if cfg.HideOnFocusLoss || !cfg.HideOnMinimize {
		t.Fatal("focus-loss hide should be off and minimize hide should be on by default")
	}
	if cfg.TraySettingsVersion != 1 {
		t.Fatalf("unexpected tray settings version: %d", cfg.TraySettingsVersion)
	}
	if cfg.StrokeColor != "Red" || cfg.StrokeWidth != 3 {
		t.Fatalf("unexpected stroke defaults: %s %d", cfg.StrokeColor, cfg.StrokeWidth)
	}
	if cfg.HighlighterWidth != 14 || cfg.HighlighterAlpha != 180 {
		t.Fatalf("unexpected highlighter defaults: %d %d", cfg.HighlighterWidth, cfg.HighlighterAlpha)
	}
	if cfg.CaptureMode != "Region" {
		t.Fatalf("unexpected capture mode: %q", cfg.CaptureMode)
	}
}
