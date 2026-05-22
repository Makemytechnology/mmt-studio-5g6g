# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
#
# per_test_wireshark.ps1 -- Windows-side launcher that opens
# Wireshark on the satester's live per-test pcap stream.
#
# Architecture
# ------------
# Capture is owned by the tester now (tester/src/testcases/
# _pcap_capture.py wraps each test with an in-process tcpdump). The
# tester exposes the live pcap via
#     GET http://<host>:5001/api/tests/active/pcap.stream
# as a chunked HTTP response that stays open for the duration of
# the test. This script just polls /api/tests for new-test events
# and, on each, spawns:
#
#     cmd /c "curl.exe -N -s <stream-url> | "Wireshark.exe" -k -i -"
#
# Wireshark opens already wired to the stream and shows packets as
# tcpdump (inside satester) flushes them. No docker management, no
# bind-mount latency, no container-restart race -- the tester
# starts and stops tcpdump with millisecond precision so the
# capture is already running when the test's SCTP burst fires.
#
# The watcher only rotates Wireshark on a NEW test (results.Count
# grew OR last entry's test_name changed). Status flips (RUNNING ->
# PASS) don't rotate -- the same Wireshark window keeps draining
# the stream until the tester closes it (which happens shortly
# after the test ends).
#
# Manual invocation:
#   .\orchestrate\tools\wireshark\per_test_wireshark.bat
#   .\orchestrate\tools\wireshark\per_test_wireshark.bat -TesterUrl http://10.0.0.42:5001

[CmdletBinding()]
param(
    [string]$TesterUrl     = 'http://localhost:5001',

    # Wireshark display filter. Mirrors run_studio.sh's choice.
    # Capture itself is wide (everything on satester's eth0); this
    # only narrows what the operator sees by default.
    [string]$DisplayFilter = 'ngap || sip || pfcp',

    # Path to Wireshark.exe; auto-detected from Program Files when empty.
    [string]$WiresharkExe  = '',

    # Path to curl.exe. Windows 10+ ships curl natively at
    # C:\Windows\System32\curl.exe but Git for Windows / Cygwin / WSL
    # installs put others on PATH that may behave differently with
    # binary streams. Force the Windows-native one by default.
    [string]$CurlExe       = '',

    # Poll interval for /api/tests in milliseconds.
    [int]$PollMs           = 250
)

Set-StrictMode -Version Latest
# IMPORTANT: do NOT use $ErrorActionPreference='Stop'. Native command
# stderr (Get-CimInstance lookups, curl, taskkill) gets promoted to a
# terminating error under Stop and aborts the watcher silently.
$ErrorActionPreference = 'Continue'

function Write-Info ($m) { Write-Host ("[{0}] {1}" -f (Get-Date -Format HH:mm:ss), $m) -ForegroundColor Cyan }
function Write-Note ($m) { Write-Host ("[{0}] {1}" -f (Get-Date -Format HH:mm:ss), $m) -ForegroundColor Yellow }
function Write-Err  ($m) { Write-Host ("[{0}] {1}" -f (Get-Date -Format HH:mm:ss), $m) -ForegroundColor Red }
function Write-Ok   ($m) { Write-Host ("[{0}] {1}" -f (Get-Date -Format HH:mm:ss), $m) -ForegroundColor Green }

# ---- locate Wireshark.exe ----------------------------------------
if (-not $WiresharkExe) {
    $candidates = @()
    if ($env:ProgramFiles)        { $candidates += (Join-Path $env:ProgramFiles 'Wireshark\Wireshark.exe') }
    if (${env:ProgramFiles(x86)}) { $candidates += (Join-Path ${env:ProgramFiles(x86)} 'Wireshark\Wireshark.exe') }
    foreach ($c in $candidates) { if (Test-Path $c) { $WiresharkExe = $c; break } }
}
if (-not $WiresharkExe -or -not (Test-Path $WiresharkExe)) {
    Write-Err "Wireshark.exe not found. Install Wireshark from https://www.wireshark.org/download.html or pass -WiresharkExe."
    exit 1
}
Write-Ok "Wireshark: $WiresharkExe"

# ---- locate curl.exe ---------------------------------------------
if (-not $CurlExe) {
    $sys32Curl = Join-Path $env:SystemRoot 'System32\curl.exe'
    if (Test-Path $sys32Curl) {
        $CurlExe = $sys32Curl
    } else {
        # Fall back to whatever's first on PATH; most likely Git for
        # Windows' Win64 build. Still understands -N for unbuffered.
        $g = Get-Command curl.exe -ErrorAction SilentlyContinue
        if ($g) { $CurlExe = $g.Source }
    }
}
if (-not $CurlExe -or -not (Test-Path $CurlExe)) {
    Write-Err "curl.exe not found. Windows 10+ ships curl at C:\Windows\System32\curl.exe -- if you don't have it, install Git for Windows or pass -CurlExe."
    exit 1
}
Write-Ok "curl: $CurlExe"

# Initial reachability probe -- non-fatal; the poll loop retries.
try {
    $null = Invoke-RestMethod -Uri "$TesterUrl/api/tests" -Method GET -TimeoutSec 3
    Write-Ok "tester reachable: $TesterUrl"
} catch {
    Write-Note "tester not reachable yet at $TesterUrl -- watcher will keep retrying"
}

# ---- state -------------------------------------------------------
$script:CmdScriptPath = Join-Path $env:TEMP 'mmt-test-wireshark.cmd'
$script:CmdProcess    = $null

function Stop-LiveWireshark {
    # Kill the cmd wrapper (which cascades to curl + wireshark via
    # pipe close), then sweep any orphan Wireshark windows whose
    # command line still carries the streaming-mode signature so we
    # never leave a stale window from a prior trigger.
    if ($script:CmdProcess) {
        try {
            if (-not $script:CmdProcess.HasExited) {
                Stop-Process -Id $script:CmdProcess.Id -Force -ErrorAction SilentlyContinue
            }
        } catch { }
        $script:CmdProcess = $null
    }
    Get-CimInstance Win32_Process -Filter "Name='Wireshark.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match '-k\s+-i\s+-' } |
        ForEach-Object {
            try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch { }
        }
}

function Start-LiveWireshark {
    param([string]$Label)
    Stop-LiveWireshark
    Write-Info "opening Wireshark for: $Label"
    # The cmd pipeline -- binary pcap MUST go through cmd.exe's pipe
    # (PowerShell would coerce stdout/stdin to text streams and
    # corrupt the bytes). Tiny .cmd file dodges nested-quoting hell.
    #
    # `curl -N -s`:
    #   -N : disable libcurl's output buffering so each chunk
    #        arrives at wireshark stdin as the tester flushes it.
    #        Without this we'd see 16 KB-batched updates instead
    #        of packet-by-packet.
    #   -s : silent mode -- suppress progress bar; we still see
    #        body bytes since they go to stdout (which is piped).
    $streamUrl = "$TesterUrl/api/tests/active/pcap.stream"
    $curlCmd   = "`"$CurlExe`" -N -s `"$streamUrl`""
    $wsCmd     = "`"$WiresharkExe`" -k -i - -Y `"$DisplayFilter`""
    $cmdBody   = "@echo off`r`n$curlCmd | $wsCmd`r`n"
    Set-Content -LiteralPath $script:CmdScriptPath -Value $cmdBody -Encoding ASCII -NoNewline
    $script:CmdProcess = Start-Process -FilePath $script:CmdScriptPath -WindowStyle Hidden -PassThru
    Write-Ok "  curl|wireshark pipe started (cmd pid $($script:CmdProcess.Id))"
}

# ---- shutdown handler --------------------------------------------
$cancelHandler = {
    Write-Host ""
    Write-Note "stopping watcher; tearing down live-stream Wireshark"
    Stop-LiveWireshark
    [Environment]::Exit(0)
}
$null = Register-EngineEvent -SourceIdentifier PowerShell.Exiting -Action $cancelHandler

# ---- poll loop ---------------------------------------------------
Write-Info "watcher running. Ctrl-C to stop. Poll every ${PollMs} ms"
Write-Info "  Wireshark will open fresh on every NEW test, wired to the live stream."

# Baseline: count=0, no last name. The runner resets in-memory
# `self.results = []` on init, so a fresh `run_studio up` sees
# count=0 from /api/tests. Initializing to 0 here means the first
# real test (count 0 -> 1) triggers Wireshark.
$prevCount = 0
$prevName  = ''

while ($true) {
    $resp = $null
    try {
        $resp = Invoke-RestMethod -Uri "$TesterUrl/api/tests" -Method GET -TimeoutSec 3
    } catch { Start-Sleep -Milliseconds $PollMs; continue }

    # Tolerate every plausible response shape:
    #   - no `results` property at all
    #   - results = $null
    #   - results = empty array (FALSY in PowerShell)
    #   - results = [single item] (PS may unwrap to scalar)
    $count = 0
    $name  = ''
    if ($resp -and $resp.PSObject.Properties['results']) {
        $rs = @($resp.results)
        $count = $rs.Count
        if ($count -gt 0) {
            $last = $rs[$count - 1]
            if ($last -and $last.PSObject.Properties['test_name']) { $name = "$($last.test_name)" }
        }
    }

    # Trigger ONLY on a new test (count grew OR test_name changed).
    # Same-test status transitions (RUNNING -> PASS) deliberately
    # don't rotate -- the existing curl|wireshark pipe is still
    # streaming packets; killing it mid-test would empty the window.
    $newTest = ($count -ne $prevCount) -or ($name -ne $prevName)
    if ($newTest -and ($count -gt 0)) {
        Start-LiveWireshark -Label $name
    }
    $prevCount = $count
    $prevName  = $name

    Start-Sleep -Milliseconds $PollMs
}
