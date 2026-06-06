@echo off
setlocal

set "ROOT=%~dp0..\.."
set "OUT=%ROOT%\build\window-capture-helper"
if not exist "%OUT%" mkdir "%OUT%"

where cl.exe >nul 2>nul
if errorlevel 1 (
  if exist "%ProgramFiles(x86)%\Microsoft Visual Studio\2019\BuildTools\Common7\Tools\VsDevCmd.bat" (
    call "%ProgramFiles(x86)%\Microsoft Visual Studio\2019\BuildTools\Common7\Tools\VsDevCmd.bat" -arch=x64
  ) else if exist "%ProgramFiles%\Microsoft Visual Studio\2022\BuildTools\Common7\Tools\VsDevCmd.bat" (
    call "%ProgramFiles%\Microsoft Visual Studio\2022\BuildTools\Common7\Tools\VsDevCmd.bat" -arch=x64
  ) else if exist "%ProgramFiles%\Microsoft Visual Studio\2022\Community\Common7\Tools\VsDevCmd.bat" (
    call "%ProgramFiles%\Microsoft Visual Studio\2022\Community\Common7\Tools\VsDevCmd.bat" -arch=x64
  ) else (
    echo MSVC developer command prompt not found. 1>&2
    exit /b 1
  )
)

cl /nologo /std:c++20 /await /EHsc /permissive- /W4 ^
  /DWIN32_LEAN_AND_MEAN /DNOMINMAX /D_SILENCE_CLANG_COROUTINE_MESSAGE ^
  /Fo:"%OUT%\\" ^
  /Fe:"%OUT%\instantcapture-window-helper.exe" ^
  "%ROOT%\tools\window-capture-helper\src\main.cpp" ^
  d3d11.lib dxgi.lib user32.lib ole32.lib oleaut32.lib runtimeobject.lib uiautomationcore.lib windowsapp.lib windowscodecs.lib ^
  /link /SUBSYSTEM:WINDOWS /ENTRY:mainCRTStartup

exit /b %ERRORLEVEL%
