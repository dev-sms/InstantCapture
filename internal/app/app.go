package app

import (
	"context"

	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/ui"
)

func Run(ctx context.Context) error {
	if err := SmokeCheck(); err != nil {
		return err
	}

	return ui.RunMainWindow(ctx)
}

func SmokeCheck() error {
	return platformwindows.EnableDPIAwareness()
}
