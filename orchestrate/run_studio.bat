@echo off
REM Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
REM
REM run_studio.bat -- Windows runtime helper for MMT Studio. Thin
REM wrapper over run_studio.ps1 with ExecutionPolicy Bypass so the
REM operator doesn't have to unblock the .ps1.
REM
REM Usage:
REM   run_studio.bat                  (default: up + Wireshark)
REM   run_studio.bat up
REM   run_studio.bat down
REM   run_studio.bat restart
REM   run_studio.bat status
REM   run_studio.bat logs
REM   run_studio.bat wireshark
REM   run_studio.bat help
REM
REM Flags are forwarded:
REM   run_studio.bat up -NoWireshark
REM   run_studio.bat -Role tester restart
REM
REM No UAC: wsl + docker.exe work as a normal user once Docker
REM Desktop is installed.

setlocal EnableExtensions
set "HERE=%~dp0"
set "PS1=%HERE%run_studio.ps1"
if not exist "%PS1%" (
    echo ERROR: run_studio.ps1 not found at "%PS1%"
    pause
    exit /b 1
)
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*
set "RC=%ERRORLEVEL%"

REM Pause only on error so steady-state usage (e.g. CI scripts,
REM scheduled tasks) doesn't hang on the prompt; an error window
REM staying open lets the operator read the failure.
if %RC% neq 0 (
    echo.
    echo run_studio exited with code %RC%.
    echo Press any key to close...
    pause >nul
)
exit /b %RC%
