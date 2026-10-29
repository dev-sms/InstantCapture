//go:build windows

package windows

import (
	"testing"
)

func TestFileDropFormatRequestsShellFileList(t *testing.T) {
	format := fileDropFormat()
	if format.CfFormat != cfHDrop || format.DwAspect != dvAspectContent || format.Lindex != -1 || format.Tymed != tymedHGlobal {
		t.Fatalf("unexpected CF_HDROP format: %#v", format)
	}
}

func TestDropEffectReflectsAcceptedFiles(t *testing.T) {
	effect := uint32(99)
	setDropEffect(&effect, true)
	if effect != dropEffectCopy {
		t.Fatalf("accepted effect = %d, want copy", effect)
	}
	setDropEffect(&effect, false)
	if effect != dropEffectNone {
		t.Fatalf("rejected effect = %d, want none", effect)
	}
}

func TestDropTargetEmitsHoverLifecycle(t *testing.T) {
	events := make(chan FileDropEvent, 2)
	target := &nativeDropTarget{events: events, active: true}
	if got := dropDragLeave(target); got != sOK {
		t.Fatalf("DragLeave HRESULT = %#x", got)
	}
	if target.active {
		t.Fatal("DragLeave should clear active state")
	}
	select {
	case event := <-events:
		if event.Kind != FileDropLeave {
			t.Fatalf("event kind = %v, want leave", event.Kind)
		}
	default:
		t.Fatal("DragLeave did not emit an event")
	}
}
