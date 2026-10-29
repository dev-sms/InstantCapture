//go:build !windows

package windows

import (
	"io"

	"gioui.org/app"
	"gioui.org/io/event"
)

type FileDropKind uint8

const (
	FileDropEnter FileDropKind = iota + 1
	FileDropLeave
	FileDropFiles
)

type FileDropEvent struct {
	Kind  FileDropKind
	Paths []string
}

func NativeWindowHandle(event.Event) uintptr { return 0 }

func RegisterImageDropTarget(*app.Window, uintptr, chan<- FileDropEvent) (io.Closer, error) {
	return nil, nil
}
