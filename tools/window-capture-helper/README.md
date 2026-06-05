# InstantCapture Window Capture Helper

Minimal Windows-only prototype for capturing one HWND through Windows Graphics Capture.

## Build

Requirements:

- Windows 10 1903 or newer SDK
- Visual Studio C++ toolchain with C++/WinRT headers
- CMake 3.20+

```powershell
cmake -S tools/window-capture-helper -B build/window-capture-helper -G "Visual Studio 17 2022" -A x64
cmake --build build/window-capture-helper --config Release
```

If CMake is unavailable but Visual Studio Build Tools are installed, use the included MSVC script from the repository root:

```powershell
tools/window-capture-helper/build-msvc.cmd
```

## Run

```powershell
build/window-capture-helper/Release/instantcapture-window-helper.exe capture-window --hwnd 0x00123456 --out C:\Temp\ic-window.png --json
```

PowerShell HWND example:

```powershell
notepad
$hwnd = "0x{0:X}" -f (Get-Process notepad | Select-Object -First 1).MainWindowHandle.ToInt64()
build/window-capture-helper/Release/instantcapture-window-helper.exe capture-window --hwnd $hwnd --out "$env:TEMP\ic-window.png" --method wgc --json
```

Success prints JSON to stdout and writes the PNG to `--out`:

```json
{"ok":true,"method":"wgc","outputPath":"C:\\Temp\\ic-window.png","width":1280,"height":720}
```

Failures also print JSON when `--json` is present. Diagnostic text is reserved for stderr so the Go app can parse stdout safely.

## Prototype limits

- Only `capture-window` and WGC are implemented.
- PrintWindow and visible-crop fallback are intentionally deferred.
- Minimized, cloaked, protected, DRM, secure desktop, and elevated-permission windows may fail or return unusable frames.
- The Go UI looks for the helper in the app directory and under `build/window-capture-helper`.
