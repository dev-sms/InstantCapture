package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type Config struct {
	Hotkey              string `json:"hotkey"`
	HideOnFocusLoss     bool   `json:"hide_on_focus_loss"`
	HideOnMinimize      bool   `json:"hide_on_minimize"`
	CaptureMode         string `json:"capture_mode"`
	StrokeColor         string `json:"stroke_color"`
	StrokeWidth         int    `json:"stroke_width"`
	HighlighterWidth    int    `json:"highlighter_width"`
	HighlighterAlpha    int    `json:"highlighter_alpha"`
	TraySettingsVersion int    `json:"tray_settings_version"`
}

func Default() Config {
	return Config{Hotkey: "Ctrl+Shift+C", HideOnFocusLoss: false, HideOnMinimize: true, CaptureMode: "Region", StrokeColor: "Red", StrokeWidth: 3, HighlighterWidth: 14, HighlighterAlpha: 180, TraySettingsVersion: 1}
}

func Load() (Config, error) {
	path, err := Path()
	if err != nil {
		return Default(), err
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return Default(), fmt.Errorf("read config: %w", err)
	}

	cfg := Default()
	if err := json.Unmarshal(data, &cfg); err != nil {
		return Default(), fmt.Errorf("parse config: %w", err)
	}
	if cfg.TraySettingsVersion == 0 {
		cfg.HideOnFocusLoss = false
		cfg.HideOnMinimize = true
		cfg.TraySettingsVersion = 1
	}
	if cfg.StrokeColor == "" {
		cfg.StrokeColor = Default().StrokeColor
	}
	if cfg.CaptureMode == "" {
		cfg.CaptureMode = Default().CaptureMode
	}
	if cfg.StrokeWidth <= 0 {
		cfg.StrokeWidth = Default().StrokeWidth
	}
	if cfg.HighlighterWidth <= 0 {
		cfg.HighlighterWidth = Default().HighlighterWidth
	}
	if cfg.HighlighterAlpha <= 0 || cfg.HighlighterAlpha > 255 {
		cfg.HighlighterAlpha = Default().HighlighterAlpha
	}
	return cfg, nil
}

func Save(cfg Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func Path() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return filepath.Join(filepath.Dir(exePath), "config.json"), nil
}
