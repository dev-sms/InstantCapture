# InstantCapture Window Capture Helper

Minimal Windows-only prototype for capturing one HWND through Windows Graphics Capture.

## Build

Requirements:

- Windows 10 1903 or newer SDK
- Visual Studio C++ toolchain with C++/WinRT headers
- CMake 3.20+

Use the repository build script. The compatibility helper script delegates to the same build:

```powershell
.\build.cmd
# or tools\window-capture-helper\build-msvc.cmd
```

## Run

```powershell
dist\instantcapture-window-helper.exe capture-window --hwnd 0x00123456 --out C:\Temp\ic-window.png --json
```

Scrolling capture MVP:

```powershell
dist\instantcapture-window-helper.exe capture-scroll --hwnd 0x00123456 --out C:\Temp\ic-scroll.png --json --max-frames 8 --delay-ms 250 --overlap-px 96
```

`capture-scroll` captures the initial WGC frame, sends one controlled wheel step, waits for measured motion and consecutive visually stable frames, then accepts one frame. It repeats until UI Automation reports the bottom, two consecutive wheel attempts produce no body movement, or a safety limit is reached. The original position is restored and verified before stitching.

The stitcher measures visual overlap and detects viewport-fixed UI. Repeated sticky headers, footers, sidebars, and floating panels are excluded from intermediate body observations; edge chrome is preserved once where recoverable.

PowerShell HWND example:

```powershell
notepad
$hwnd = "0x{0:X}" -f (Get-Process notepad | Select-Object -First 1).MainWindowHandle.ToInt64()
dist\instantcapture-window-helper.exe capture-window --hwnd $hwnd --out "$env:TEMP\ic-window.png" --method wgc --json
```

Success prints JSON to stdout and writes the PNG to `--out`:

```json
{"ok":true,"method":"wgc","outputPath":"C:\\Temp\\ic-window.png","width":1280,"height":720}
```

Failures also print JSON when `--json` is present. Diagnostic text is reserved for stderr so the Go app can parse stdout safely.

## Prototype limits

- Only `capture-window` and WGC are implemented.
- `capture-scroll` requires a vertically scrollable UI Automation `ScrollPattern` in the target window.
- Virtualized lists, infinite feeds, permanently obscured content, smooth scrolling, and animated content can still make alignment or settlement ambiguous.
- PrintWindow and visible-crop fallback are intentionally deferred.
- Minimized, cloaked, protected, DRM, secure desktop, and elevated-permission windows may fail or return unusable frames.
- The deployed app loads the helper beside `dist\instantcapture.exe`.
