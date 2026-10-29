package main

import (
	"context"
	"flag"
	"fmt"
	"image/png"
	"log"
	"os"

	gioapp "gioui.org/app"

	"instantcapture/internal/app"
	"instantcapture/internal/capture"
	"instantcapture/internal/workflow"
)

const version = "0.1.0-dev"

func main() {
	showVersion := flag.Bool("version", false, "print version and exit")
	smoke := flag.Bool("smoke", false, "run startup checks without opening the GUI")
	captureSnapshot := flag.String("capture-snapshot", "", "capture virtual screen snapshot PNG and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if err := app.SmokeCheck(); err != nil {
		log.Fatal(err)
	}

	if *smoke {
		fmt.Println("InstantCapture smoke check passed")
		return
	}

	if *captureSnapshot != "" {
		snapshot, err := workflow.CaptureScreen(capture.ScreenshotCapturer{})
		if err != nil {
			log.Fatal(err)
		}
		file, err := os.Create(*captureSnapshot)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		if err := png.Encode(file, snapshot.Image); err != nil {
			log.Fatal(err)
		}
		return
	}

	releaseInstance, alreadyRunning, err := app.AcquireSingleInstance()
	if err != nil {
		log.Fatal(err)
	}
	if alreadyRunning {
		app.ActivateExistingInstance()
		return
	}
	defer releaseInstance()

	go func() {
		defer releaseInstance()
		if err := app.Run(context.Background()); err != nil {
			log.Print(err)
			os.Exit(1)
		}
		os.Exit(0)
	}()

	gioapp.Main()
}
