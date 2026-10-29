//go:build windows

package windows

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"
)

func TestMoveFileToRecycleBinUsesValidatedAbsolutePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "capture.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRecyclePaths := recyclePaths
	t.Cleanup(func() { recyclePaths = oldRecyclePaths })
	var received []string
	recyclePaths = func(paths []string) error {
		received = append([]string(nil), paths...)
		return nil
	}

	if err := MoveFileToRecycleBin(path); err != nil {
		t.Fatal(err)
	}
	if len(received) != 1 || !filepath.IsAbs(received[0]) {
		t.Fatalf("recycle paths = %#v", received)
	}
}

func TestMoveFileToRecycleBinRejectsDirectories(t *testing.T) {
	if err := MoveFileToRecycleBin(t.TempDir()); err == nil {
		t.Fatal("expected directory to be rejected")
	}
}

func TestMoveFileToRecycleBinSurfacesRecycleFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "capture.png")
	if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
	oldRecyclePaths := recyclePaths
	t.Cleanup(func() { recyclePaths = oldRecyclePaths })
	recyclePaths = func([]string) error { return errors.New("shell unavailable") }

	err := MoveFileToRecycleBin(path)

	if err == nil || !strings.Contains(err.Error(), "shell unavailable") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMoveFilesToRecycleBinBatchesNormalizedPaths(t *testing.T) {
	dir := t.TempDir()
	first := filepath.Join(dir, "one.png")
	second := filepath.Join(dir, "two.png")
	for _, path := range []string{first, second} {
		if err := os.WriteFile(path, []byte("png"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	oldRecyclePaths := recyclePaths
	t.Cleanup(func() { recyclePaths = oldRecyclePaths })
	var received []string
	recyclePaths = func(paths []string) error {
		received = append([]string(nil), paths...)
		return nil
	}

	if err := MoveFilesToRecycleBin([]string{first, second, strings.ToUpper(first), ""}); err != nil {
		t.Fatal(err)
	}
	if len(received) != 2 {
		t.Fatalf("batched paths = %#v, want 2 unique files", received)
	}
}

func TestDoubleNullTerminatedPathsBuildsShellMultiString(t *testing.T) {
	encoded, err := doubleNullTerminatedPaths([]string{`C:\captures\one.png`, `C:\captures\two.png`})
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) < 2 || encoded[len(encoded)-1] != 0 || encoded[len(encoded)-2] != 0 {
		t.Fatalf("path list is not double-NUL terminated: %#v", encoded)
	}
	nulls := 0
	for _, value := range encoded {
		if value == 0 {
			nulls++
		}
	}
	if nulls != 3 {
		t.Fatalf("NUL count = %d, want 3", nulls)
	}
}

func TestRecycleFileNativeUsesUndoableSilentDelete(t *testing.T) {
	oldInvoke := invokeSHFileOperationW
	t.Cleanup(func() { invokeSHFileOperationW = oldInvoke })
	var received shFileOperation
	invokeSHFileOperationW = func(operation *shFileOperation) (int32, error) {
		received = *operation
		return 0, nil
	}

	if err := recycleFileNative(filepath.Join(t.TempDir(), "capture.png")); err != nil {
		t.Fatal(err)
	}
	if received.wFunc != fileOperationDelete {
		t.Fatalf("operation = %#x, want delete", received.wFunc)
	}
	if received.fFlags&fileOperationAllowUndo == 0 {
		t.Fatal("recycle operation is missing FOF_ALLOWUNDO")
	}
	if received.fFlags&(fileOperationSilent|fileOperationNoConfirmation|fileOperationNoErrorUI) != fileOperationSilent|fileOperationNoConfirmation|fileOperationNoErrorUI {
		t.Fatalf("shell UI suppression flags = %#x", received.fFlags)
	}
}

func TestRecycleFileNativeSkipsEmptyPath(t *testing.T) {
	oldInvoke := invokeSHFileOperationW
	t.Cleanup(func() { invokeSHFileOperationW = oldInvoke })
	called := false
	invokeSHFileOperationW = func(*shFileOperation) (int32, error) {
		called = true
		return 0, nil
	}

	if err := recycleFileNative("   "); err != nil {
		t.Fatal(err)
	}
	if called {
		t.Fatal("native API should not be called for an empty path")
	}
}

func TestRecycleFileNativeRejectsAbortedOperation(t *testing.T) {
	oldInvoke := invokeSHFileOperationW
	t.Cleanup(func() { invokeSHFileOperationW = oldInvoke })
	invokeSHFileOperationW = func(operation *shFileOperation) (int32, error) {
		operation.fAnyOperationsAborted = 1
		return 0, nil
	}

	err := recycleFileNative(filepath.Join(t.TempDir(), "capture.png"))

	if err == nil || !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRecycleFileNativeReturnsShellFailureCode(t *testing.T) {
	oldInvoke := invokeSHFileOperationW
	t.Cleanup(func() { invokeSHFileOperationW = oldInvoke })
	invokeSHFileOperationW = func(*shFileOperation) (int32, error) {
		return 0x71, nil
	}

	err := recycleFileNative(filepath.Join(t.TempDir(), "capture.png"))

	if err == nil || !strings.Contains(err.Error(), "0x00000071") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestSHFileOperationStructMatchesWindowsABI(t *testing.T) {
	pointerSize := unsafe.Sizeof(uintptr(0))
	expectedSize := uintptr(32)
	expectedFromOffset := uintptr(8)
	expectedAbortedOffset := uintptr(20)
	if pointerSize == 8 {
		expectedSize = 56
		expectedFromOffset = 16
		expectedAbortedOffset = 36
	}
	operation := shFileOperation{}
	if got := unsafe.Sizeof(operation); got != expectedSize {
		t.Fatalf("SHFILEOPSTRUCTW size = %d, want %d", got, expectedSize)
	}
	if got := unsafe.Offsetof(operation.pFrom); got != expectedFromOffset {
		t.Fatalf("pFrom offset = %d, want %d", got, expectedFromOffset)
	}
	if got := unsafe.Offsetof(operation.fAnyOperationsAborted); got != expectedAbortedOffset {
		t.Fatalf("fAnyOperationsAborted offset = %d, want %d", got, expectedAbortedOffset)
	}
}

func TestMoveFileToRecycleBinNativeSmoke(t *testing.T) {
	if os.Getenv("INSTANTCAPTURE_RECYCLE_SMOKE") != "1" {
		t.Skip("set INSTANTCAPTURE_RECYCLE_SMOKE=1 to exercise the real recycle bin")
	}
	path := filepath.Join(t.TempDir(), "InstantCapture_recycle_smoke.tmp")
	if err := os.WriteFile(path, []byte("recycle smoke"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := MoveFileToRecycleBin(path); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("file still exists after recycle operation: %v", err)
	}
}
