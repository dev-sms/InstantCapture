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
	if cfg.AutoSave {
		t.Fatal("auto save should be off by default")
	}
	if cfg.SaveDir != "" {
		t.Fatalf("unexpected default save dir: %q", cfg.SaveDir)
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
