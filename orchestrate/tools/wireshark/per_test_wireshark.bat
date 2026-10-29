@echo off
REM Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
REM
REM per_test_wireshark.bat -- double-click launcher for the Windows
REM equivalent of `run_studio.sh --wireshark`. Runs the polling
REM watcher in this console window with ExecutionPolicy Bypass so the
REM user doesn't have to unblock the .ps1 manually.
REM
REM Args are forwarded:
REM   per_test_wireshark.bat -TesterUrl http://192.168.7.42:5001
REM   per_test_wireshark.bat -DisplayFilter "ngap || pfcp"

setlocal EnableExtensions
set "HERE=%~dp0"
set "PS1=%HERE%per_test_wireshark.ps1"
if not exist "%PS1%" (
    echo ERROR: per_test_wireshark.ps1 not found at "%PS1%"
    pause
    exit /b 1
)
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*
set "RC=%ERRORLEVEL%"
echo.
if %RC% neq 0 (
    echo Watcher exited with code %RC%.
) else (
    echo Watcher exited cleanly.
)
echo (Press any key to close this window.)
pause >nul
exit /b %RC%
