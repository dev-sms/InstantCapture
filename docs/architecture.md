# InstantCapture architecture

Last code audit: 2026-09-01.

This living document describes the current working tree. The working tree contained substantial pre-existing, uncommitted development beyond `HEAD` during the audit; those changes were preserved and are included only where verified in code and tests.

## System overview

InstantCapture is a Windows-first desktop screen-capture and annotation application. The main executable and most UI/domain code are Go. Gio provides the application window, while direct Win32 integrations provide the tray, global shortcuts, native selection overlay, file dialogs, drag/drop, window discovery, and other Windows behavior. A separately built C++/WinRT helper performs WGC window and scroll capture.

## Package ownership

- `cmd/instantcapture`: CLI flags, smoke checks, single-instance startup, and Gio main-loop entry.
- `internal/app`: application bootstrap, DPI setup, and single-instance policy.
- `internal/ui`: main-window composition, event serialization, capture dispatch, settings, editor interaction, import/drop, filmstrip, tray, and shortcut behavior.
- `internal/capture`: display enumeration and screen-capture abstraction.
- `internal/workflow`: capture/edit state machine, virtual-screen stitching, crop state, bounded history, rendering requests, and save/copy operations.
- `internal/editor`: annotation models, validation, editor state, and render commands.
- `internal/exporter`: rendering, clipboard output, image encoding, and atomic file replacement.
- `internal/nativeoverlay`: primary Win32 selection overlay and scroll-selection interaction.
- `internal/overlay`: Gio fallback selector and shared overlay options/selection state.
- `internal/platform/windows`: Windows API adapters, window targeting, helper invocation, dialogs, known folders, recycle bin, single-instance primitives, and drag/drop.
- `internal/hotkey` and `internal/tray`: global capture shortcut and CGO-free tray icon/message loop, including capture-completion balloon notifications while the main window remains hidden.
- `tools/window-capture-helper`: C++/WinRT WGC and scroll-stitching helper.
- `tools/makeicon`: generated application icon tooling.

Dependencies flow from the UI toward workflow/domain packages and platform adapters. The workflow and editor packages do not own windows or tray state.

## Startup and shutdown

1. The executable parses diagnostic flags and enables DPI awareness.
2. Normal GUI startup acquires a per-user named mutex. A second process activates the existing visible window and exits.
3. The app starts `internal/ui.RunMainWindow` and enters Gio's main loop.
4. The UI loads executable-adjacent JSON configuration, creates the workflow controller, and starts tray, global-hotkey, filmstrip, focus polling, and window-event handling.
5. UI state changes are serialized through the main select loop. Background operations report through buffered channels.
6. Context cancellation, tray Quit, or window destruction exits the loop; deferred cleanup stops the tray, hotkey registrar, and native resources.

## Capture flow

1. Capture startup asynchronously hides the Gio main window, waits until it is no longer visible, flushes pending DWM composition, and captures each display into one virtual-screen image.
2. Fullscreen mode commits the active monitor directly.
3. Region and freeform modes open the native Win32 overlay. Only native-overlay unavailability falls back to the Gio selector.
4. The screen outside an active rectangular region is dimmed while the selected interior is shown at original brightness. Native and Gio overlays must preserve this behavior consistently.
5. Window mode enumerates visible window targets, uses the native overlay for selection, attempts helper-based WGC capture, and falls back to a visible screen-region capture.
6. Scroll mode uses the native overlay only to select a window-local viewport region. The helper then owns each discrete wheel input, waits for measured movement and visual settlement, captures one accepted frame per step, detects the natural end, restores the original scroll position, and reuses the fixed/sticky-aware stitcher.
7. Remembered regions are stored as an unrestricted preset list in absolute Windows screen coordinates. One preset is active at a time, and the instant-capture hotkey can commit it directly from the virtual-screen snapshot without opening an overlay; the region must be fully covered by the current physical display rectangles.
8. Successful selections enter workflow editing state when editing is enabled. With post-capture editing disabled, the app remains hidden, copies and optionally auto-saves the capture, sends a tray notification, and immediately releases the editing session.

Capture-overlay `Esc` and right-click cancel selection. In the main window, unmodified `Esc` closes or cancels the active modal, editor, shortcut assignment, or canvas gesture and then hides the program to the tray. An active image rotation is canceled without hiding.

## Editing and output

The workflow controller owns the captured image, visible crop, selected screen coordinates, annotations, and a bounded 20-entry edit history. The UI owns transient gestures, text editing, pan/zoom, and canvas-view history. Export rendering applies the visible crop, annotations, mosaic/highlighter behavior, and optional output border.

Gio samples images bilinearly without mipmaps, so below 50% zoom the canvas draws a copy box-averaged by an integer factor (`internal/ui/imagescale.go`), cached per displayed bitmap and factor and released with the other transient caches. At whole-number zoom levels of 100% and above it uses nearest filtering. The crop magnifier and rotation preview keep drawing the full-resolution bitmap.

Copy emits PNG data. Save supports the formats implemented by the exporter and uses same-directory temporary files plus atomic replacement. Auto-save writes the cropped unannotated original. Imported, dropped, and clipboard images are decoded asynchronously with file-size and pixel-count limits.

## Image transforms

Image rotation/flip behavior is specified in [ADR-0001](adr/0001-flatten-image-transforms.md).

- `internal/workflow/transform.go` composes the existing preview, restricts it to the visible crop, and inverse-maps it into a new immutable RGBA bitmap. Exact quarter turns/flips preserve pixels; arbitrary integer degrees use premultiplied-alpha bilinear sampling with transparent surroundings and expanded bounds.
- The captured bitmap becomes the transformed composition, annotation lists are cleared, and the crop becomes the entire result. The existing 20-entry controller history restores the old bitmap, annotations, crop, and coordinate metadata. Popped history slots are zeroed to release otherwise-retained bitmap references.
- The output border remains an output setting, applied once after transformation; it is excluded from the flattened bitmap. Capture-time original auto-save remains unchanged. After a transform, `OriginalImage` refers to the new flattened editing base.
- The UI owns the rotation gesture and view-coordinate hit target, independent of the crop handles. Pointer grab tracks release outside the image; angle changes are rounded to degrees. During the gesture, Gio rotates the cached composition clipped to the current crop, without creating a CPU bitmap per frame or controller history. The output border is omitted until commit. Escape, pointer cancellation, secondary press, image replacement, and transient-cache release discard the gesture.
- Commit allocates one result and records one controller/UI history step; the view resets and undo restores its previous zoom/pan. Save/copy use the same committed rendering. The result allocation is limited to 64 million pixels before composing. A gesture has no separate retained image cache. Existing cache release and delayed OS-memory cleanup remain in use; large transforms execute synchronously on the UI event path.

## Persistence and integrations

- Configuration is JSON next to the executable and is merged with defaults on load. It includes the saved-region preset list, active preset, whether the hotkey uses it, and whether captures open the editor. The former single-region field is migrated into the preset list on load.
- The default save location resolves through Windows known folders.
- The filmstrip scans the save folder, loads thumbnails on workers, and sends deletion through the Windows recycle bin. List thumbnails are decoded to their on-screen box in physical pixels; the hover preview decodes only the hovered file, on its own worker, and keeps that single image until transient caches are released. Both use a box pre-pass followed by Catmull-Rom.
- The native helper is a hidden, timeout-bound child process that exchanges JSON metadata and temporary PNG files. `INSTANTCAPTURE_WINDOW_HELPER` can override helper discovery and is an executable trust boundary.

## Concurrency boundaries

- Gio UI mutations occur on the main event path.
- Capture, helper execution, image decoding, folder scans, thumbnail generation, and recycle operations use goroutines and channels.
- The tray runs an OS-thread-locked Win32 message loop.
- OLE drag/drop registration is performed against the native Gio window handle.

## Runtime and security constraints

- The product targets Windows 10 or later, runs as the invoking user, and uses per-monitor-v2 DPI awareness.
- Protected content, secure desktops, elevated-process boundaries, minimized/cloaked windows, and unsupported renderer paths may not be capturable. The application does not bypass those controls.
- Scroll capture and helper execution use bounded steps, frames, timeouts, and output validation.
- Generated executables, resource objects, build trees, temporary images, and runtime configuration are not architecture sources.

## Verification

Final verification and packaging use `build.cmd`. It runs Go tests and the native helper tests, builds both executables into `dist/`, and removes intermediate compiler/resource files. The resource generator is pinned (`go-winres@v0.3.3`). `package.cmd` runs `build.cmd`, then `tools/package-release.ps1` zips an explicit file list (both executables, README, `LICENSE`, `THIRD_PARTY_NOTICES.md`) into git-ignored `release/` with a SHA-256 file, so the executable-adjacent runtime `config.json` is never shipped. `tools/third-party-notices.ps1` regenerates the notices from the modules linked into the Windows executable. Release binaries are not code-signed. GUI capture appearance and tray integration require a Windows manual smoke check in addition to automated tests.
