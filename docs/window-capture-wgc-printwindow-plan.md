# InstantCapture 진짜 창 캡쳐 개발 계획

작성일: 2026-06-05

## 배경

현재 InstantCapture의 Window mode는 화면에 보이는 데스크톱 스냅샷을 기준으로 대상 창 영역을 잘라내는 `visible snapshot crop` 방식이다. 이 방식은 구현이 단순하고 빠르지만, 다른 창에 가려진 영역이나 화면 밖으로 벗어난 영역을 복원하지 못한다. 따라서 사용자가 기대하는 "진짜 창 캡쳐"와는 다르며, 현재 구현은 임시 동작으로 명확히 구분해야 한다.

이번 문서는 구현 작업이 아니라 추후 개발 방향을 남기는 설계 문서다. 실제 구현은 현재 우선순위에서 제외한다.

## 목표

- 가려진 창 영역까지 가능한 한 복원하는 진짜 창 캡쳐 방식을 설계한다.
- Windows Graphics Capture(WGC)를 우선 적용하는 방향을 정리한다.
- WGC가 실패하거나 지원되지 않는 경우 PrintWindow 기반 fallback을 검토한다.
- 최후 fallback으로 현재 방식인 visible snapshot crop을 유지하는 전략을 정의한다.
- Go 앱에서 Windows 네이티브 캡쳐 기능을 안전하게 호출하기 위한 optional helper exe 구조를 제안한다.
- IPC와 PNG 전달 방식을 정리한다.
- 추후 구현 시 검증해야 할 체크리스트와 알려진 한계를 문서화한다.

## 비목표

- 이번 단계에서 진짜 창 캡쳐 기능을 구현하지 않는다.
- 현재 Window mode의 동작을 변경하지 않는다.
- macOS/Linux 창 캡쳐 구현을 다루지 않는다.
- 보안 정책상 캡쳐가 금지된 창을 우회하는 기능을 목표로 하지 않는다.
- DRM/보호 콘텐츠, UAC 보안 데스크톱, 관리자 권한 경계 우회는 지원 대상으로 보지 않는다.
- 모든 앱에서 100% 동일한 결과를 보장하는 것을 목표로 하지 않는다.

## 현재 구현의 한계

현재 Window mode는 대상 창의 위치와 크기를 찾은 뒤, 이미 화면에 렌더링된 데스크톱 이미지를 해당 좌표로 crop하는 방식이다.

한계는 다음과 같다.

- 대상 창이 다른 창에 가려지면 가려진 부분까지 그대로 캡쳐된다.
- 대상 창이 최소화되어 있으면 실제 창 내용을 얻기 어렵다.
- 대상 창이 화면 밖에 일부 위치하면 화면 밖 영역을 복원할 수 없다.
- 창의 rounded corner, shadow, DPI scaling, multi-monitor 좌표계에 따라 crop 경계가 어긋날 수 있다.
- 캡쳐 결과가 대상 창의 실제 렌더링 버퍼가 아니라 현재 모니터에 보이는 합성 결과에 의존한다.

따라서 현재 Window mode는 "창 선택 crop"에 가깝고, "진짜 창 캡쳐"로 표시하거나 설명해서는 안 된다.

## API 선택지

### 1. Windows Graphics Capture(WGC)

WGC는 Windows 10 1903 이후에서 사용할 수 있는 공식 캡쳐 API다. `GraphicsCaptureItem`을 통해 창 또는 모니터 단위 캡쳐를 수행할 수 있으며, 최신 Windows에서 가장 권장되는 접근이다.

장점:

- Windows의 최신 공식 캡쳐 경로다.
- Direct3D 기반 캡쳐 파이프라인과 잘 맞는다.
- 많은 일반 데스크톱 앱에서 가려진 창 영역 캡쳐 가능성이 높다.
- 향후 프레임 스트림, 비디오 녹화, 고성능 캡쳐로 확장하기 쉽다.

주의점:

- OS 버전 요구사항이 있다.
- 일부 창은 보안/정책/렌더링 방식 때문에 캡쳐가 제한될 수 있다.
- 창 핸들(HWND)에서 `GraphicsCaptureItem`을 생성하려면 WinRT interop이 필요하다.
- Direct3D texture를 PNG로 변환하는 네이티브 코드가 필요하다.

권장 용도:

- 추후 진짜 창 캡쳐의 1순위 구현 경로.

### 2. PrintWindow

PrintWindow는 Win32 API로, 특정 HWND에게 자신의 내용을 그리도록 요청하는 방식이다. 오래된 API지만 일부 앱에서는 가려진 창 캡쳐 fallback으로 동작할 수 있다.

장점:

- Win32 API 기반이라 helper exe에서 비교적 직접 호출할 수 있다.
- WGC가 실패하는 구형 환경 또는 특정 창에서 대체 경로가 될 수 있다.
- 정적인 Win32/GDI 기반 앱에서는 결과가 양호할 수 있다.

주의점:

- 많은 최신 앱, Chromium/Electron, GPU 가속 앱에서는 빈 화면이나 깨진 이미지가 나올 수 있다.
- 앱이 WM_PRINT/WM_PRINTCLIENT 처리를 제대로 하지 않으면 실패한다.
- 최소화된 창이나 UWP/DirectComposition 기반 창에서 신뢰성이 낮다.

권장 용도:

- WGC 실패 시 2순위 fallback.

### 3. visible snapshot crop

현재 방식이다. 데스크톱 스크린샷에서 대상 창 좌표를 잘라낸다.

장점:

- 단순하고 빠르다.
- OS API 실패와 관계없이 사용자가 실제로 보고 있는 화면을 캡쳐할 수 있다.
- 기존 구현을 유지할 수 있다.

주의점:

- 가려진 영역을 복원하지 못한다.
- 진짜 창 캡쳐가 아니라 화면 crop이다.

권장 용도:

- WGC와 PrintWindow가 모두 실패했을 때의 최후 fallback.

## fallback 전략

추후 구현 시 권장 순서는 다음과 같다.

1. **WGC 우선 시도**
   - HWND 기반으로 `GraphicsCaptureItem`을 생성한다.
   - Direct3D frame을 획득한다.
   - BGRA/RGBA 버퍼를 PNG로 인코딩한다.
   - 정상 PNG가 생성되면 즉시 성공 처리한다.

2. **PrintWindow fallback**
   - WGC가 OS 미지원, 권한 제한, API 실패, 빈 프레임 등으로 실패하면 시도한다.
   - `PrintWindow(hwnd, hdc, PW_RENDERFULLCONTENT)`를 우선 검토한다.
   - 결과가 완전히 투명하거나 단색/빈 이미지로 판단되면 실패 처리한다.

3. **visible snapshot crop 최후 fallback**
   - WGC와 PrintWindow가 모두 실패하면 현재 Window mode crop 방식을 사용한다.
   - 결과 메타데이터에 fallback 종류를 기록한다.
   - UI에서는 "가려진 영역은 복원되지 않을 수 있음"을 안내한다.

권장 결과 메타데이터:

```json
{
  "method": "wgc | printwindow | visible-crop",
  "hwnd": "0x00000000",
  "windowTitle": "...",
  "width": 1280,
  "height": 720,
  "fallbackReason": "..."
}
```

## helper 구조

Go 앱 본체에서 WGC와 PrintWindow를 직접 안정적으로 다루기 어렵다면 별도 Windows 전용 helper exe를 두는 구조를 권장한다.

권장 구성:

```text
InstantCapture
├─ Go app / main process
│  ├─ 창 목록 조회
│  ├─ 캡쳐 요청 생성
│  ├─ helper exe 실행
│  └─ PNG 결과 수신 및 클립보드/저장 처리
└─ native helper exe
   ├─ HWND 유효성 검사
   ├─ WGC 캡쳐
   ├─ PrintWindow fallback
   ├─ PNG 인코딩
   └─ JSON 메타데이터 출력
```

helper exe 책임:

- 입력으로 HWND, 캡쳐 방식 옵션, 출력 경로 또는 stdout 모드를 받는다.
- WGC 가능 여부와 OS 버전을 확인한다.
- WGC 캡쳐를 수행한다.
- 실패 시 PrintWindow를 시도한다.
- 필요 시 visible crop은 앱 본체에서 수행하거나 helper에서 별도 옵션으로 수행한다.
- 성공/실패 결과를 JSON으로 반환한다.

앱 본체 책임:

- 사용자 UI와 창 선택 흐름을 유지한다.
- helper exe 실행 timeout을 관리한다.
- helper 실패 시 현재 crop 방식으로 fallback한다.
- 캡쳐 결과를 기존 편집/복사/저장 파이프라인에 연결한다.

권장 구현 언어 후보:

- C++/WinRT: WGC/Direct3D/Win32 interop에 가장 직접적이다.
- C#/.NET: WGC 접근성이 좋고 개발 난이도가 낮을 수 있으나 배포 런타임 고려가 필요하다.
- Rust: windows crate로 구현 가능하나 Direct3D/WGC PNG 인코딩 난이도를 검토해야 한다.

## IPC

초기 구현은 복잡한 상시 IPC보다 단발성 child process 호출을 권장한다.

### 요청 방식

앱 본체가 helper exe를 실행하며 인자를 전달한다.

예시:

```text
instantcapture-window-helper.exe capture-window --hwnd 0x00123456 --out C:\Temp\ic-window.png --json
```

권장 인자:

- `capture-window`: 창 캡쳐 명령
- `--hwnd`: 대상 HWND
- `--out`: PNG 출력 경로
- `--method`: `auto`, `wgc`, `printwindow` 중 선택
- `--timeout-ms`: helper 내부 timeout
- `--json`: stdout에 JSON 결과 출력

### 응답 방식

stdout에는 JSON만 출력하고, 디버그 로그는 stderr로 분리한다.

성공 예시:

```json
{
  "ok": true,
  "method": "wgc",
  "outputPath": "C:\\Temp\\ic-window.png",
  "width": 1280,
  "height": 720
}
```

실패 예시:

```json
{
  "ok": false,
  "method": "wgc",
  "errorCode": "WGC_UNAVAILABLE",
  "message": "Windows Graphics Capture is unavailable on this system."
}
```

## PNG 전달

권장 PNG 전달 방식은 파일 경로 기반이다.

이유:

- stdout으로 대용량 바이너리를 전달하는 것보다 안정적이다.
- JSON 메타데이터와 이미지 데이터를 분리할 수 있다.
- 기존 저장/복사/편집 파이프라인에 연결하기 쉽다.

권장 흐름:

1. 앱 본체가 임시 PNG 경로를 생성한다.
2. helper exe에 HWND와 출력 경로를 넘긴다.
3. helper exe가 PNG 파일을 기록한다.
4. helper exe가 stdout JSON으로 결과를 반환한다.
5. 앱 본체가 PNG 파일을 읽어 기존 캡쳐 이미지 처리 흐름에 전달한다.
6. 처리 완료 후 임시 파일을 정리한다.

주의사항:

- temp 파일명은 충돌 방지를 위해 UUID 또는 timestamp+random suffix를 사용한다.
- helper 완료 전 파일을 읽지 않는다.
- 실패 시 부분 생성된 PNG 파일을 삭제한다.
- PNG 파일 크기와 이미지 크기를 검증해 빈 결과를 걸러낸다.

## 검증 체크리스트

추후 구현 후 다음 항목을 확인한다.

- [ ] 일반 Win32 앱 창을 WGC로 캡쳐할 수 있다.
- [ ] Electron/Chromium 기반 앱 창을 WGC로 캡쳐할 수 있다.
- [ ] 대상 창이 다른 창에 부분적으로 가려져도 WGC 결과가 정상인지 확인한다.
- [ ] WGC 실패 시 PrintWindow fallback이 실행된다.
- [ ] PrintWindow 결과가 빈 이미지일 때 실패로 판정하고 visible crop으로 넘어간다.
- [ ] WGC와 PrintWindow가 모두 실패하면 현재 visible snapshot crop으로 캡쳐된다.
- [ ] fallback method가 결과 메타데이터에 기록된다.
- [ ] 다중 모니터 환경에서 좌표와 DPI scaling이 어긋나지 않는다.
- [ ] 100%, 125%, 150%, 200% DPI 배율에서 결과 크기를 확인한다.
- [ ] 최소화된 창, 화면 밖 창, 투명/레이어드 창의 동작을 기록한다.
- [ ] 관리자 권한 앱과 일반 권한 앱 사이의 캡쳐 가능 여부를 확인한다.
- [ ] 보호 콘텐츠/DRM 창에서 실패 처리가 안전하게 동작한다.
- [ ] helper exe timeout, crash, stdout JSON 파싱 실패를 앱 본체가 처리한다.
- [ ] 임시 PNG 파일이 성공/실패 경로 모두에서 정리된다.
- [ ] 캡쳐 결과가 기존 copy/save/annotation 흐름과 충돌하지 않는다.

## 알려진 한계

- WGC도 모든 창을 완벽히 캡쳐하지 못할 수 있다.
- OS 보안 정책, 앱 자체 보호 플래그, DRM 콘텐츠는 캡쳐가 차단될 수 있다.
- PrintWindow는 최신 GPU 가속 앱에서 빈 화면을 반환할 수 있다.
- 최소화된 창은 실제 렌더링 surface가 없어 캡쳐가 실패하거나 오래된 프레임이 나올 수 있다.
- 투명 창, layered window, rounded corner, shadow는 API별 결과가 다를 수 있다.
- 고 DPI와 다중 모니터 환경에서는 좌표 변환 오류가 발생하기 쉽다.
- helper exe 배포, 코드 서명, 백신 오탐 가능성을 별도로 관리해야 한다.
- WGC 구현은 Direct3D/WinRT interop 지식이 필요하므로 단순 JavaScript 수정으로 끝나지 않는다.

## 권장 결론

InstantCapture의 추후 진짜 창 캡쳐는 **WGC 우선, PrintWindow fallback, visible snapshot crop 최후 fallback** 구조로 설계한다.

현재 Window mode는 계속 사용할 수 있지만, 기능 설명에서는 가려진 창 영역을 복원하지 못하는 `visible snapshot crop`임을 명확히 해야 한다. 진짜 창 캡쳐 구현은 Windows 전용 native helper exe를 별도로 만들고, 앱 본체는 helper를 child process로 호출해 PNG 파일과 JSON 메타데이터를 수신하는 구조가 가장 안전하다.

추후 작업 우선순위는 다음과 같다.

1. Windows helper exe 최소 프로토타입 작성
2. HWND 기반 WGC 캡쳐 검증
3. PNG 인코딩 및 JSON 응답 형식 확정
4. PrintWindow fallback 추가
5. 앱 본체의 helper 호출 및 visible crop fallback 연결
6. DPI/multi-monitor/failure case 검증
