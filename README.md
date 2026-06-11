# InstantCapture

InstantCapture is a Windows-first screen capture and annotation tool written in Go.

MVP goals:

- Start a region capture flow from a global hotkey.
- Select a screen region.
- Annotate the captured image with pen, rectangle, and arrow tools.
- Copy the annotated result as PNG or save it to disk.

## Planned stack

- GUI/overlay: Gio
- Screen capture: `github.com/kbinani/screenshot`
- Global hotkey: `github.com/MakeNowJust/hotkey`
- Drawing/rendering: `github.com/fogleman/gg`
- Image clipboard: `golang.design/x/clipboard`
- Windows APIs: `golang.org/x/sys/windows`

## Current MVP flow

1. Run the app.
2. Press `Ctrl+Shift+C` or click `New Capture`.
3. InstantCapture captures the virtual screen and opens a borderless fullscreen selection overlay.
4. Drag a rectangle in the overlay to crop the target region, or press `Esc` to cancel.
5. Draw with `Pen`, `Rect`, or `Arrow` in the editor window.
6. Click `Copy PNG` / `Save PNG`, or press `Ctrl+C` / `Ctrl+S`.

Saved files are written to the configured save folder as `InstantCapture_YYYYMMDD_HHMMSS.png`. By default this is the user's `Pictures\Screenshots` folder.

On Windows, InstantCapture first tries a native Win32 topmost layered overlay window. If native overlay initialization fails, it falls back to the Gio borderless fullscreen overlay.

## Settings

- Click `Settings` to open the settings strip.
- Click the hotkey button to enter shortcut assignment mode, then press the key combination to use for capture.
- Click `Mode` in the title toolbar to switch Region/Window/Freeform/Fullscreen capture.
- Toggle `Minimize to tray` and `Focus-loss tray` from the settings strip.
- Toggle `Auto save` and set a custom save folder by typing a path or using `Browse...`.
- Use the border palette/RGB field and width slider to customize annotation style.
- Settings are saved as `config.json` next to `InstantCapture.exe`.

## Tray behavior

- InstantCapture uses a CGO-free Win32 tray icon based on `Shell_NotifyIconW`.
- Clicking the tray icon restores and focuses the window.
- Minimizing the window hides it to the tray.
- Clicking outside the window can hide it to the tray when `Focus-loss tray` is enabled. It is off by default.

## Development

```powershell
go test ./...
go run ./cmd/instantcapture --smoke
go run ./cmd/instantcapture
go run ./tools/makeicon
go run github.com/tc-hib/go-winres@latest make --in winres.json --out rsrc
go build -ldflags="-H=windowsgui" -o dist\InstantCapture.exe ./cmd/instantcapture
```

The first GUI milestone opens a placeholder Gio window while the core capture/editor/export pipeline is implemented behind testable interfaces.
