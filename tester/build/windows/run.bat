@echo off
REM ============================================================
REM  SA Tester Launcher — uses local embedded Python 3.12
REM  No system-wide Python installation required.
REM  PYTHONUNBUFFERED=1 ensures all thread output is visible.
REM ============================================================

setlocal
set "ROOT=%~dp0"
set "PYTHON=%ROOT%python\python.exe"
set "PYTHONUNBUFFERED=1"

if not exist "%PYTHON%" (
    echo ERROR: Embedded Python not found at %PYTHON%
    echo Please ensure the python\ directory is intact.
    pause
    exit /b 1
)

echo.
echo   SA Tester -- using local Python 3.12
echo   %PYTHON%
echo.

"%PYTHON%" -m sa_tester %*

endlocal
