package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
)

type ScreenRegion struct {
	X      int `json:"x"`
	Y      int `json:"y"`
	Width  int `json:"width"`
	Height int `json:"height"`
}

type SavedRegionPreset struct {
	ID     string       `json:"id"`
	Name   string       `json:"name"`
	Region ScreenRegion `json:"region"`
}

func ScreenRegionFromRect(rect image.Rectangle) ScreenRegion {
	rect = rect.Canon()
	return ScreenRegion{X: rect.Min.X, Y: rect.Min.Y, Width: rect.Dx(), Height: rect.Dy()}
}

func (r ScreenRegion) Rect() image.Rectangle {
	if r.Width <= 0 || r.Height <= 0 {
		return image.Rectangle{}
	}
	return image.Rect(r.X, r.Y, r.X+r.Width, r.Y+r.Height)
}

type Config struct {
	Hotkey              string              `json:"hotkey"`
	HotkeyOpensWindow   bool                `json:"hotkey_opens_window"`
	EditAfterCapture    bool                `json:"edit_after_capture"`
	SavedRegionEnabled  bool                `json:"saved_region_enabled"`
	SavedRegion         ScreenRegion        `json:"saved_region,omitempty"`
	SavedRegions        []SavedRegionPreset `json:"saved_regions,omitempty"`
	ActiveSavedRegionID string              `json:"active_saved_region_id,omitempty"`
	HideOnFocusLoss     bool                `json:"hide_on_focus_loss"`
	HideOnMinimize      bool                `json:"hide_on_minimize"`
	CaptureMode         string              `json:"capture_mode"`
	AutoSave            bool                `json:"auto_save"`
	SaveDir             string              `json:"save_dir"`
	StrokeColor         string              `json:"stroke_color"`
	StrokeWidth         int                 `json:"stroke_width"`
	OutputBorderEnabled bool                `json:"output_border_enabled"`
	OutputBorderColor   string              `json:"output_border_color"`
	OutputBorderWidth   int                 `json:"output_border_width"`
	HighlighterWidth    int                 `json:"highlighter_width"`
	HighlighterAlpha    int                 `json:"highlighter_alpha"`
	MosaicBlockSize     int                 `json:"mosaic_block_size"`
	MosaicWidth         int                 `json:"mosaic_width"`
	TraySettingsVersion int                 `json:"tray_settings_version"`
}

func Default() Config {
	return Config{Hotkey: "Ctrl+Shift+C", EditAfterCapture: true, HideOnFocusLoss: false, HideOnMinimize: true, CaptureMode: "Region", AutoSave: false, SaveDir: "", StrokeColor: "Red", StrokeWidth: 3, OutputBorderEnabled: false, OutputBorderColor: "Black", OutputBorderWidth: 4, HighlighterWidth: 14, HighlighterAlpha: 180, MosaicBlockSize: 12, MosaicWidth: 24, TraySettingsVersion: 2}
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
	if cfg.TraySettingsVersion < 2 {
		cfg.EditAfterCapture = true
		cfg.TraySettingsVersion = 2
	}
	migrateSavedRegions(&cfg)
	if cfg.StrokeColor == "" {
		cfg.StrokeColor = Default().StrokeColor
	}
	if cfg.CaptureMode == "" {
		cfg.CaptureMode = Default().CaptureMode
	}
	cfg.SaveDir = NormalizeSaveDir(cfg.SaveDir)
	if cfg.StrokeWidth <= 0 {
		cfg.StrokeWidth = Default().StrokeWidth
	}
	if cfg.OutputBorderColor == "" {
		cfg.OutputBorderColor = Default().OutputBorderColor
	}
	if cfg.OutputBorderWidth <= 0 {
		cfg.OutputBorderWidth = Default().OutputBorderWidth
	}
	if cfg.HighlighterWidth <= 0 {
		cfg.HighlighterWidth = Default().HighlighterWidth
	}
	if cfg.HighlighterAlpha <= 0 || cfg.HighlighterAlpha > 255 {
		cfg.HighlighterAlpha = Default().HighlighterAlpha
	}
	if cfg.MosaicBlockSize < 4 || cfg.MosaicBlockSize > 40 {
		cfg.MosaicBlockSize = Default().MosaicBlockSize
	}
	if cfg.MosaicWidth < 8 || cfg.MosaicWidth > 80 {
		cfg.MosaicWidth = Default().MosaicWidth
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
	cfg.SaveDir = NormalizeSaveDir(cfg.SaveDir)
	cfg.SavedRegion = ScreenRegion{}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	return os.WriteFile(path, data, 0o644)
}

func normalizeSavedRegions(regions []SavedRegionPreset) []SavedRegionPreset {
	normalized := make([]SavedRegionPreset, 0, len(regions))
	seen := make(map[string]bool, len(regions))
	for _, preset := range regions {
		preset.ID = strings.TrimSpace(preset.ID)
		preset.Name = strings.TrimSpace(preset.Name)
		if preset.ID == "" || seen[preset.ID] || preset.Region.Rect().Empty() {
			continue
		}
		if preset.Name == "" {
			preset.Name = fmt.Sprintf("영역 %d", len(normalized)+1)
		}
		seen[preset.ID] = true
		normalized = append(normalized, preset)
	}
	return normalized
}

func migrateSavedRegions(cfg *Config) {
	legacy := cfg.SavedRegion
	cfg.SavedRegion = ScreenRegion{}
	cfg.SavedRegions = normalizeSavedRegions(cfg.SavedRegions)
	if len(cfg.SavedRegions) == 0 && !legacy.Rect().Empty() {
		cfg.SavedRegions = []SavedRegionPreset{{ID: "legacy-region", Name: "영역 1", Region: legacy}}
		if cfg.SavedRegionEnabled {
			cfg.ActiveSavedRegionID = "legacy-region"
		}
	}
	if !savedRegionIDExists(cfg.SavedRegions, cfg.ActiveSavedRegionID) {
		cfg.ActiveSavedRegionID = ""
		if cfg.SavedRegionEnabled && len(cfg.SavedRegions) > 0 {
			cfg.ActiveSavedRegionID = cfg.SavedRegions[0].ID
		}
	}
	if len(cfg.SavedRegions) == 0 {
		cfg.SavedRegionEnabled = false
	}
}

func savedRegionIDExists(regions []SavedRegionPreset, id string) bool {
	for _, preset := range regions {
		if preset.ID == id {
			return true
		}
	}
	return false
}

func NormalizeSaveDir(dir string) string {
	dir = strings.TrimSpace(dir)
	dir = strings.Trim(dir, "\"")
	if dir == "" {
		return ""
	}
	return filepath.Clean(dir)
}

func Path() (string, error) {
	exePath, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve executable path: %w", err)
	}
	return filepath.Join(filepath.Dir(exePath), "config.json"), nil
}
