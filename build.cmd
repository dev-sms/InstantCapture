@echo off
setlocal EnableExtensions

set "ROOT=%~dp0"
set "DIST=%ROOT%dist"
set "TEMP_BUILD=%TEMP%\InstantCapture-build-%RANDOM%-%RANDOM%"
set "RESOURCE_BASE=%ROOT%cmd\instantcapture\rsrc"

if not exist "%DIST%" mkdir "%DIST%"
if errorlevel 1 exit /b %ERRORLEVEL%
mkdir "%TEMP_BUILD%"
if errorlevel 1 exit /b %ERRORLEVEL%

call :build
set "RESULT=%ERRORLEVEL%"

del /q "%RESOURCE_BASE%_windows_386.syso" 2>nul
del /q "%RESOURCE_BASE%_windows_amd64.syso" 2>nul
rmdir /s /q "%TEMP_BUILD%" 2>nul

if not "%RESULT%"=="0" exit /b %RESULT%
echo Build completed: %DIST%
exit /b 0

:build
pushd "%ROOT%"

echo [1/6] Running Go tests...
go test ./...
if errorlevel 1 goto :failed

echo [2/6] Generating application icon...
go run ./tools/makeicon
if errorlevel 1 goto :failed

echo [3/6] Generating Windows resources...
go run github.com/tc-hib/go-winres@v0.3.3 make --in winres.json --out cmd\instantcapture\rsrc
if errorlevel 1 goto :failed

echo [4/6] Loading MSVC environment...
where cl.exe >nul 2>nul
if errorlevel 1 call :load_msvc
if errorlevel 1 goto :failed

echo [5/6] Building and testing native helper...
cl /nologo /std:c++20 /await /EHsc /permissive- /W4 ^
  /DWIN32_LEAN_AND_MEAN /DNOMINMAX /D_SILENCE_CLANG_COROUTINE_MESSAGE ^
  /Fo:"%TEMP_BUILD%\helper-" ^
  /Fe:"%DIST%\instantcapture-window-helper.exe" ^
  "%ROOT%tools\window-capture-helper\src\main.cpp" ^
  d3d11.lib dxgi.lib user32.lib ole32.lib oleaut32.lib runtimeobject.lib uiautomationcore.lib windowsapp.lib windowscodecs.lib ^
  /link /SUBSYSTEM:WINDOWS /ENTRY:mainCRTStartup
if errorlevel 1 goto :failed

cl /nologo /std:c++20 /await /EHsc /permissive- /W4 ^
  /DWIN32_LEAN_AND_MEAN /DNOMINMAX /D_SILENCE_CLANG_COROUTINE_MESSAGE ^
  /Fo:"%TEMP_BUILD%\test-" ^
  /Fe:"%TEMP_BUILD%\instantcapture-window-helper-tests.exe" ^
  "%ROOT%tools\window-capture-helper\src\stitch_test.cpp" ^
  d3d11.lib dxgi.lib user32.lib ole32.lib oleaut32.lib runtimeobject.lib uiautomationcore.lib windowsapp.lib windowscodecs.lib
if errorlevel 1 goto :failed

"%TEMP_BUILD%\instantcapture-window-helper-tests.exe"
if errorlevel 1 goto :failed

echo [6/6] Building InstantCapture...
go build -ldflags="-H=windowsgui" -o "%DIST%\instantcapture.exe" ./cmd/instantcapture
if errorlevel 1 goto :failed

popd
exit /b 0

:failed
set "BUILD_ERROR=%ERRORLEVEL%"
popd
exit /b %BUILD_ERROR%

:load_msvc
if exist "%ProgramFiles%\Microsoft Visual Studio\2022\BuildTools\Common7\Tools\VsDevCmd.bat" (
  call "%ProgramFiles%\Microsoft Visual Studio\2022\BuildTools\Common7\Tools\VsDevCmd.bat" -arch=x64
  if errorlevel 1 exit /b 1
  exit /b 0
)
if exist "%ProgramFiles%\Microsoft Visual Studio\2022\Community\Common7\Tools\VsDevCmd.bat" (
  call "%ProgramFiles%\Microsoft Visual Studio\2022\Community\Common7\Tools\VsDevCmd.bat" -arch=x64
  if errorlevel 1 exit /b 1
  exit /b 0
)
if exist "%ProgramFiles(x86)%\Microsoft Visual Studio\2019\BuildTools\Common7\Tools\VsDevCmd.bat" (
  call "%ProgramFiles(x86)%\Microsoft Visual Studio\2019\BuildTools\Common7\Tools\VsDevCmd.bat" -arch=x64
  if errorlevel 1 exit /b 1
  exit /b 0
)
echo MSVC developer command prompt not found. 1>&2
exit /b 1
