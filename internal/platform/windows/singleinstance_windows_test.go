//go:build windows

package windows

import (
	"fmt"
	"os"
	"testing"
	"time"
)

func TestAcquireSingleInstance(t *testing.T) {
	name := fmt.Sprintf(`Local\InstantCapture.Test.%d.%d`, os.Getpid(), time.Now().UnixNano())
	release, alreadyRunning, err := AcquireSingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	if alreadyRunning {
		t.Fatal("first acquisition reported an existing instance")
	}
	defer release()

	releaseDuplicate, alreadyRunning, err := AcquireSingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	releaseDuplicate()
	if !alreadyRunning {
		t.Fatal("duplicate acquisition was not detected")
	}

	release()
	releaseAfterClose, alreadyRunning, err := AcquireSingleInstance(name)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseAfterClose()
	if alreadyRunning {
		t.Fatal("released instance remained locked")
	}
}
