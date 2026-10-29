# InstantCapture

InstantCapture is a Windows screen capture and annotation tool written in Go.

- Capture a region, window, freeform shape, full monitor, or scrolling content from a global hotkey.
- Re-capture saved screen regions instantly, or capture straight to the clipboard without opening the editor.
- Annotate with pen, shapes, arrows, text, highlighter, and mosaic; crop, rotate, and flip.
- Copy the result as PNG or save it to disk; browse earlier captures in the side filmstrip.

## Requirements

- Windows 10 or later, x64.
- No installer and no network access: everything stays on your PC.

## Getting started

1. Download `InstantCapture-<version>-windows-x64.zip` from the releases page and extract it to a folder you can write to (for example under `Documents`, not `Program Files`). Settings are stored as `config.json` next to the executable.
2. Keep `instantcapture-window-helper.exe` next to `instantcapture.exe`; window and scroll capture use it.
3. Run `instantcapture.exe`. Windows SmartScreen may warn because the binaries are not code-signed; choose **More info → Run anyway** if you trust the download. The `.sha256` file next to each release zip lets you verify it.

## Privacy

InstantCapture makes no network connections. Captures go to the clipboard and, when auto-save is on, to the configured folder (default `Pictures\Screenshots`). `config.json` holds your save folder and saved screen regions; do not share it if those are private.

## Capture flow

1. Run the app.
2. Press the capture hotkey (default `Ctrl+Shift+C`) or click `New Capture`.
3. InstantCapture captures the virtual screen and opens a borderless fullscreen selection overlay.
4. Drag a rectangle in the overlay to crop the target region, or press `Esc` to cancel.
5. Draw with the straight/free/rectangle-fill pen, rectangle, arrow, highlighter, or straight/free/rectangle mosaic tool in the editor window.
6. Click `Copy PNG` / `Save PNG`, or press `Ctrl+C` / `Ctrl+S`.

In Scroll mode, drag the viewport region to include. InstantCapture then controls one wheel step at a time, captures only after the content settles, stops at the detected end, and restores the original scroll position before stitching.

Saved files are written to the configured save folder as `InstantCapture_YYYYMMDD_HHMMSS.png`. By default this is the user's `Pictures\Screenshots` folder.

On Windows, InstantCapture first tries a native Win32 topmost layered overlay window. If native overlay initialization fails, it falls back to the Gio borderless fullscreen overlay.

## 브러시 빠른 조절

- 캔버스에서 `Alt`+오른쪽 버튼을 누른 채 드래그합니다. 좌우는 굵기, 위아래는 형광펜 불투명도 또는 모자이크 블록 크기를 바꿉니다. 펜·도형은 굵기만 조절합니다.
- 브러시 미리보기는 누른 위치에 고정되고, 마우스를 놓으면 설정에 저장합니다.

## 이미지 회전·반전

- 이미지 테두리 상단 중앙 크롭 핸들 위의 `↻`에 마우스를 올리면 회전 안내와 강조 표시가 나타납니다. 이미지가 캔버스 상단에 붙으면 회전 핸들은 테두리 바로 안쪽에 표시됩니다. 기존 사각 크롭 핸들은 그대로 자르기에 사용합니다.
- `↻`를 왼쪽 버튼으로 드래그하면 이미지 중심을 기준으로 1° 단위로 회전하며 상태 표시줄에 각도가 나옵니다. 드래그 중에는 현재 크롭과 주석의 합성 미리보기를 보여주며, 마우스를 놓으면 확정합니다.
- 하단 회전·반전 UI에서 `시계 방향`/`반시계 방향`을 전환하고 `90°`, `180°`, `270°`를 선택하거나 `좌우 반전`, `상하 반전`을 누릅니다.
- **변환은 현재 크롭 안의 원본과 모든 주석(텍스트·모자이크 포함)을 하나의 이미지로 합칩니다.** 변환 후 이전 주석을 개별 수정하거나 이전 크롭 밖을 다시 펼치려면 `Ctrl+Z`로 변환을 취소하세요. 한 번의 드래그/버튼 적용은 undo 한 단계이고 `Ctrl+Y` 또는 `Ctrl+Shift+Z`로 다시 적용할 수 있습니다.
- 출력 외곽선은 이미지에 굽지 않고 기존 설정대로 변환 결과의 직사각형 바깥에 저장·복사 시 한 번 적용합니다. 드래그 미리보기에는 외곽선을 표시하지 않습니다.
- 회전 중 `Esc`, 오른쪽 버튼 또는 포인터 취소는 확정 없이 회전을 취소합니다. 회전 중 `Esc`는 창을 숨기지 않습니다. 회전 중 다른 키보드 단축키와 확대·이동은 잠시 차단됩니다. 이미지 교체나 트레이 숨김도 미확정 회전을 버립니다.
- 확정 후 캔버스 배율/이동을 초기화하며 undo 시 이전 뷰를 복원합니다. 저장·복사는 동일한 확정 결과를 사용합니다. 새 이미지로 교체하면 이전 변환 이력을 지웁니다.
- 90° 배수와 반전은 픽셀을 정확하게 재배치합니다. 임의 각도는 투명 여백이 포함된 외접 사각형을 만들며 보간하므로 반복 적용 시 화질이 저하될 수 있습니다. 결과는 최대 64,000,000픽셀이고 큰 이미지의 확정 처리는 잠시 UI를 멈출 수 있습니다.
- 자동 저장은 기존처럼 캡처 시점에 실행되므로 이미 저장된 캡처 원본 파일은 변하지 않습니다. 변환 후 편집 기반 이미지는 합성 결과이므로 이후 원본 렌더링 API도 이 기반 이미지를 사용합니다.

## Settings

- Click `Settings` to open the centered settings modal.
- Click the hotkey button to enter shortcut assignment mode, then press the key combination to use for capture.
- Click `Mode` in the title toolbar to switch Region/Window/Freeform/Fullscreen capture.
- Use `화면 좌표 추가` in Settings to add reusable regions with a green selection border. The list has no fixed slot limit; select one active region and enable `기억 좌표 사용` so the instant-capture hotkey captures it without reopening the selector.
- Toggle `캡처 후 편집` off to keep the app hidden after capture. The result is copied to the clipboard, optionally auto-saved, and reported through a Windows tray notification without opening the editor.
- Toggle `Minimize to tray` and `Focus-loss tray` from the settings strip.
- Toggle `Auto save` and set a custom save folder by typing a path or using `Browse...`.
- Use the border palette/RGB field and width slider to customize annotation style.
- Cycle the pen mode through straight, freehand, and rectangle; rectangle mode fills the dragged area with the pen color.
- Cycle the mosaic mode through straight, freehand, and rectangle; rectangle mode pixelates the dragged area.
- Adjust mosaic block size and mosaic pen width from the settings modal.
- Settings are saved as `config.json` next to `InstantCapture.exe`.

## Tray behavior

- InstantCapture uses a CGO-free Win32 tray icon based on `Shell_NotifyIconW`.
- Clicking the tray icon restores and focuses the window.
- Minimizing the window hides it to the tray.
- Clicking outside the window can hide it to the tray when `Focus-loss tray` is enabled. It is off by default.
- Pressing `Esc` in the main window cancels the current canvas gesture and hides the app to the tray, except an active image rotation, which is canceled without hiding. Capture overlays continue to use `Esc` to cancel capture.

## Development

Prerequisites:

- Go 1.26.1 or later.
- Visual Studio 2022 (or 2019) Build Tools with the C++ workload and a Windows 10/11 SDK, for the C++/WinRT window-capture helper. `build.cmd` finds `VsDevCmd.bat` automatically.

```powershell
.\build.cmd
dist\instantcapture.exe --smoke
dist\instantcapture.exe
```

`build.cmd` runs all Go tests, generates the icon and Windows resources, builds and tests the native helper, and writes the only executable artifacts to `dist\`. Intermediate compiler and resource files are removed automatically.

Architecture, design decisions, and contributor rules live in [`docs/README.md`](docs/README.md) and [`AGENTS.md`](AGENTS.md).

## Releasing

1. Update the version in `winres.json` (`file_version`, `product_version`, `FileVersion`, `ProductVersion`).
2. After changing dependencies, regenerate notices: `powershell -ExecutionPolicy Bypass -File tools\third-party-notices.ps1`.
3. Run `.\package.cmd`. It builds and tests everything, then writes `release\InstantCapture-<version>-windows-x64.zip` and its `.sha256`. The zip contains only the two executables, this README, `LICENSE`, and `THIRD_PARTY_NOTICES.md`; the local `dist\config.json` is never included.

## License

InstantCapture is released under the [MIT License](LICENSE). Third-party components and their licenses are listed in [`THIRD_PARTY_NOTICES.md`](THIRD_PARTY_NOTICES.md).
