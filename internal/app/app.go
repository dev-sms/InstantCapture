package app

import (
	"context"

	platformwindows "instantcapture/internal/platform/windows"
	"instantcapture/internal/ui"
)

const (
	instanceMutexName = `Local\InstantCapture.SingleInstance.8E38D14C-936E-4BAE-AE9E-51980B118DB8`
	mainWindowTitle   = "InstantCapture"
)

func Run(ctx context.Context) error {
	return ui.RunMainWindow(ctx)
}

func SmokeCheck() error {
	return platformwindows.EnableDPIAwareness()
}

func AcquireSingleInstance() (func(), bool, error) {
	return platformwindows.AcquireSingleInstance(instanceMutexName)
}

func ActivateExistingInstance() {
	if platformwindows.WindowVisibleByTitle(mainWindowTitle) {
		_ = platformwindows.RestoreWindowByTitle(mainWindowTitle)
	}
}
