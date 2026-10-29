@echo off
REM Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
REM
REM install_on_windows.bat -- one-line wrapper around install.ps1.
REM
REM Exists ONLY because .ps1 files don't double-click on Windows
REM (they open in Notepad by default). install.ps1 itself handles
REM self-elevation (UAC), running the install hidden, and tailing
REM the log back to whichever terminal launched this script. Args
REM are forwarded:
REM
REM   install_on_windows.bat
REM   install_on_windows.bat -Role tester -CoreHost 10.0.0.42
REM
setlocal EnableExtensions
set "PS1=%~dp0install.ps1"
if not exist "%PS1%" (
    echo ERROR: install.ps1 not found at "%PS1%"
    pause
    exit /b 1
)
powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%PS1%" %*
exit /b %ERRORLEVEL%
