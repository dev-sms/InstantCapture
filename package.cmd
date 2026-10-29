@echo off
setlocal EnableExtensions

rem Builds, tests, and zips a release into release\.
set "ROOT=%~dp0"

call "%ROOT%build.cmd"
if errorlevel 1 exit /b %ERRORLEVEL%

powershell -NoProfile -ExecutionPolicy Bypass -File "%ROOT%tools\package-release.ps1"
exit /b %ERRORLEVEL%
