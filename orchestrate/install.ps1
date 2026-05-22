# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
#
# install.ps1 -- one-shot Windows installer for MMT Studio.
#
# Bootstraps a fresh Windows 11 (or Windows 10 build 19041+) box to run
# the full 5G studio end-to-end:
#
#   1. WSL2 runtime (installs if missing; warns if a reboot is needed)
#   2. Ubuntu distro (default Ubuntu-24.04)
#   3. Docker Desktop (silent install + WSL2 backend integration)
#   4. Fetch install.sh from the release tarball into the distro and
#      run it with the chosen role/version flags. install.sh handles
#      hugepages, sctp.ko, compose staging, and `docker compose up`.
#
# Recommended entrypoint is the two-line install_on_windows.bat wrapper:
#
#   git clone https://github.com/Makemytechnology/mmt-studio-5g6g.git
#   mmt-studio-5g6g\orchestrate\install_on_windows.bat
#
# The .bat self-elevates via UAC and forwards to this script. Run
# directly from an ELEVATED PowerShell prompt if you prefer:
#
#   .\install.ps1 -Role both
#   .\install.ps1 -Role tester -CoreHost 10.0.0.42
#   .\install.ps1 -Role core -Version v2026.05.7
#
# Re-running is safe -- each step skips if already done.
#
# Source tree resolution, in priority order:
#   1. -SourceDir <path>             explicit Windows-side checkout
#   2. <script-dir>/../              if invoked from inside a clone
#      (the install_on_windows.bat path — install.ps1 sits in orchestrate/
#       beside core/ and tester/)
#   3. auto-clone into %LOCALAPPDATA%\mmt-studio-5g6g
#      (the legacy `irm | iex` path — installs Git via winget if needed)
#
# Compatible with Windows PowerShell 5.1 and PowerShell 7+.

[CmdletBinding()]
param(
    [ValidateSet('core','tester','both')]
    [string]$Role = 'both',

    [string]$Distro = 'Ubuntu-24.04',

    [string]$Version = 'latest',

    [string]$CoreUrl  = '',
    [string]$CoreHost = '',

    # Where to fetch install.sh + docker-compose.yml from. Override
    # for staging / fork installs. Must serve install.sh at the same
    # base URL.
    [string]$AssetBase = 'https://github.com/Makemytechnology/mmt-studio-5g6g/releases/latest/download',

    # Path on Windows to a local mono checkout (root dir containing
    # core/, tester/, orchestrate/). Will be made visible to WSL via
    # /mnt/<drive>/... and handed to install.sh as --source-dir.
    [string]$SourceDir = '',

    [switch]$SkipDockerDesktop,
    [switch]$SkipBringUp,

    # By default, after install.sh finishes bringing the stack up,
    # we also launch the per-test Wireshark watcher in its own
    # console window -- matching `run_studio.bat`'s default. That
    # way the operator who just installed can run a test from
    # http://localhost:5001 and see Wireshark open automatically,
    # without having to know about a second command (run_studio.bat
    # wireshark). Pass -NoWireshark to opt out (headless / CI hosts,
    # or boxes without Wireshark installed where the watcher would
    # only spew "Wireshark.exe not found" into its log).
    [switch]$NoWireshark,

    # Internal marker: set when the script self-elevates and re-invokes
    # itself hidden as an Administrator. The user never passes this --
    # it's how the elevated child distinguishes itself from the
    # original non-elevated parent (which is responsible for tailing
    # the log).
    [switch]$ElevatedChild
)

# ===================================================================
# Bootstrap: self-elevate + tail
# ===================================================================
# install.ps1 is the only PS file needed for the Windows install --
# the .bat wrapper is a one-liner that just hands off to here. This
# block handles the three dispatch paths:
#
#   1. Already running as Administrator (user ran from admin shell)
#        -> fall through to the install logic below; runs in the
#           same console with normal Write-Host output.
#   2. ElevatedChild: this is the hidden process that path-3 spawned
#        -> fall through; writes everything to the transcript log
#           that the original non-elevated parent is tailing.
#   3. Not Admin and not ElevatedChild (the typical first launch)
#        -> self-elevate a hidden copy of ourselves (UAC prompt),
#           then stay here in the original (non-elevated, visible)
#           console and tail the transcript log so the operator
#           sees install progress in their existing terminal.
#           No second console window.
#
# Why all of this lives in install.ps1: the previous design split it
# into install_on_windows.bat + install_log_tail.ps1 + install.ps1.
# Three files for one install was confusing for operators; this
# collapses to install_on_windows.bat (one-line wrapper) + install.ps1.
function Test-IsAdmin {
    ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

if (-not $ElevatedChild -and -not (Test-IsAdmin)) {
    $logFile  = Join-Path $env:TEMP 'mmt-studio-install.log'
    $doneFile = Join-Path $env:TEMP 'mmt-studio-install.done'
    Remove-Item $logFile  -ErrorAction SilentlyContinue
    Remove-Item $doneFile -ErrorAction SilentlyContinue

    Write-Host "Requesting Administrator elevation -- UAC prompt will appear." -ForegroundColor Cyan
    Write-Host "The install runs elevated in the background; progress streams below." -ForegroundColor Cyan
    Write-Host ""

    # Re-invoke ourselves hidden + elevated, forwarding all the
    # original user args plus the -ElevatedChild marker. The marker
    # tells the new copy to skip this block and run the install.
    $selfPath = $MyInvocation.MyCommand.Path
    $argList  = @('-NoProfile','-ExecutionPolicy','Bypass','-File',"`"$selfPath`"",'-ElevatedChild')
    foreach ($k in $PSBoundParameters.Keys) {
        $v = $PSBoundParameters[$k]
        if ($v -is [switch]) {
            if ($v.IsPresent) { $argList += "-$k" }
        } else {
            $argList += "-$k"
            $argList += "`"$v`""
        }
    }
    Start-Process -FilePath 'powershell.exe' -Verb RunAs -WindowStyle Hidden -ArgumentList $argList | Out-Null

    # ---- tail loop -------------------------------------------------
    # Stream the elevated child's transcript log to this console as
    # bytes arrive. Use shared-read file open so the writer
    # (Start-Transcript inside the elevated child) can keep
    # appending while we read.
    Write-Host "Waiting for elevated install to start..." -ForegroundColor Cyan
    $startup = (Get-Date).AddSeconds(30)
    while (-not (Test-Path $logFile) -and -not (Test-Path $doneFile) -and (Get-Date) -lt $startup) {
        Start-Sleep -Milliseconds 200
    }
    if (-not (Test-Path $logFile) -and -not (Test-Path $doneFile)) {
        Write-Host ""
        Write-Host "No install log appeared within 30s -- the UAC prompt was likely declined." -ForegroundColor Red
        exit 1
    }

    $lastPos = 0
    $idleAfterDone = 0
    $lastGrowTime = Get-Date
    while ($true) {
        if (Test-Path $logFile) {
            try {
                $sz = (Get-Item -LiteralPath $logFile -ErrorAction Stop).Length
                if ($sz -gt $lastPos) {
                    $fs = [System.IO.File]::Open($logFile,
                        [System.IO.FileMode]::Open,
                        [System.IO.FileAccess]::Read,
                        [System.IO.FileShare]::ReadWrite)
                    try {
                        $fs.Position = $lastPos
                        $reader = New-Object System.IO.StreamReader($fs)
                        $chunk = $reader.ReadToEnd()
                        $reader.Close()
                    } finally { $fs.Close() }
                    if ($chunk) { Write-Host -NoNewline $chunk }
                    $lastPos = $sz
                    $idleAfterDone = 0
                    $lastGrowTime = Get-Date
                    continue   # drain bursts fast
                }
            } catch {
                # Transient lock from the writer's flush; skip this tick.
            }
        }
        if (Test-Path $doneFile) {
            $idleAfterDone++
            # ~1 s of no new bytes after the elevated child wrote
            # the exit-code sentinel = it's safe to exit.
            if ($idleAfterDone -ge 4) { break }
        } else {
            # No done sentinel yet. If the log hasn't grown for a
            # very long time, the elevated child likely crashed
            # before it could write the sentinel -- bail out so the
            # user isn't stuck staring at a frozen prompt forever.
            #
            # 1200 s (20 min) covers the slowest legitimate silent
            # stretch we've seen: docker-compose build pulling
            # multi-GB base image layers from a flaky network. The
            # rsync stage and the install.sh phases all now print
            # heartbeats / [N/12] markers within seconds of starting,
            # so any genuine 20-minute silence really IS a crash.
            if (((Get-Date) - $lastGrowTime).TotalSeconds -gt 1200) {
                Write-Host ""
                Write-Host "Log has been idle for 20m with no done-sentinel -- elevated install likely crashed." -ForegroundColor Yellow
                Write-Host "Inspect $logFile for details." -ForegroundColor Yellow
                exit 2
            }
        }
        Start-Sleep -Milliseconds 250
    }

    $rc = 0
    try { $rc = [int]((Get-Content -LiteralPath $doneFile -Raw -ErrorAction Stop).Trim()) } catch { }
    Write-Host ""
    if ($rc -eq 0) {
        Write-Host "----- install finished -----" -ForegroundColor Cyan
    } else {
        Write-Host "----- install exited with code $rc -----" -ForegroundColor Yellow
    }
    Write-Host "Log saved at: $logFile" -ForegroundColor Gray
    exit $rc
}

# Past here: we're running as Administrator (either the user launched
# from an admin shell, or we're the elevated hidden child the parent
# above just spawned). Run the install proper.

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ---- transcript log -----------------------------------------------
# Capture EVERYTHING (console host output, native command stdout/stderr,
# warnings, errors) to a fixed path. The elevated cmd window closes as
# soon as the script exits — without a transcript, every operator
# screenshot of "it failed" is a black box. Start-Transcript flushes
# line-by-line, so even a hard throw before the bottom of the script
# leaves a usable log behind.
#
# Path is %TEMP%\mmt-studio-install.log under the elevated user
# (NOT SYSTEM), so C:\Users\<you>\AppData\Local\Temp\… — same place
# install_on_windows.bat advertises.
$script:_TranscriptPath = Join-Path $env:TEMP 'mmt-studio-install.log'
try { Stop-Transcript | Out-Null } catch { }
try {
    Start-Transcript -Path $script:_TranscriptPath -Force -IncludeInvocationHeader | Out-Null
} catch {
    Write-Host "WARN: Start-Transcript failed: $($_.Exception.Message)" -ForegroundColor Yellow
}

# ---- TLS / SSL preflight ------------------------------------------
# Windows PowerShell 5.1 (shipped with Windows 10/11) defaults to
# TLS 1.0/1.1 only. GitHub, Docker Desktop CDN, and winget endpoints
# have all dropped pre-TLS 1.2 — without this, every Invoke-WebRequest
# / Invoke-RestMethod call fails with
#   "The request was aborted: Could not create SSL/TLS secure channel."
# or "The connection was closed unexpectedly."
# PowerShell 7+ already negotiates TLS 1.2/1.3 so this is a no-op there.
try {
    [Net.ServicePointManager]::SecurityProtocol = `
        [Net.SecurityProtocolType]::Tls12 -bor `
        [Net.SecurityProtocolType]::Tls13
} catch {
    # Tls13 enum value missing on very old .NET — fall back to Tls12.
    [Net.ServicePointManager]::SecurityProtocol = [Net.SecurityProtocolType]::Tls12
}

# ---- logging helpers ----------------------------------------------
# Step counter — each top-level phase increments. Helps the operator
# see "where are we" at a glance even when the WSL invocations are
# spewing build output for minutes at a time.
$script:_StepNum = 0
# 8 phases: Elevation, System check, Windows version, WSL2, Distro,
# Docker Desktop, Docker DNS, install.sh.
$script:_StepTotal = 8
function Write-Step($msg) {
    $script:_StepNum++
    Write-Host ("[{0}/{1}] {2}" -f $script:_StepNum, $script:_StepTotal, $msg) `
        -ForegroundColor Cyan
}
function Write-Ok   ($msg) { Write-Host "      ok  $msg" -ForegroundColor Green }
function Write-Note ($msg) { Write-Host "      ... $msg" -ForegroundColor Yellow }
function Write-Skip ($msg) { Write-Host "      --  $msg" -ForegroundColor DarkGray }
function Write-Fail ($msg) { Write-Host "      !!! $msg" -ForegroundColor Red }

# ---- long-wait helpers --------------------------------------------
# Two recurring patterns that needed proper progress logging --
# without them, operators on slow / fresh machines watched a frozen
# terminal for minutes and assumed the script had hung:
#   1. Docker Desktop installer's own runtime (5-15 min on a
#      first-ever install: HCS image fetch + WSL2 distro creation +
#      service registration). The old code did Start-Process -Wait
#      with no output.
#   2. Engine-reachable polling after Docker Desktop launches. The
#      old timeout was 180 s, which is fine for an already-installed
#      box but trips on a fresh install where the first daemon boot
#      genuinely takes 5+ min. New default 15 min with a heartbeat
#      every 30 s.

function Wait-ProcessWithHeartbeat {
    # Block on a Process until it exits, printing a heartbeat every
    # $HeartbeatSec seconds so the operator knows it's still alive.
    param(
        [Parameter(Mandatory)] [System.Diagnostics.Process]$Process,
        [Parameter(Mandatory)] [string]$Label,
        [int]$HeartbeatSec = 30
    )
    $start = Get-Date
    $lastBeat = 0
    while (-not $Process.HasExited) {
        Start-Sleep -Seconds 5
        $elapsed = [int]((Get-Date) - $start).TotalSeconds
        if (($elapsed - $lastBeat) -ge $HeartbeatSec) {
            Write-Note ("  {0} still running ({1}m {2}s elapsed)..." -f $Label, [Math]::Floor($elapsed/60), ($elapsed % 60))
            $lastBeat = $elapsed
        }
    }
    return $Process.ExitCode
}

function Wait-DockerEngineReady {
    # Poll a WSL distro until `docker version` succeeds (= engine
    # reachable via the WSL integration socket). Returns elapsed
    # seconds on success, -1 on timeout. Heartbeats every 30 s.
    param(
        [Parameter(Mandatory)] [string]$Distro,
        [int]$TimeoutSec = 900,
        [int]$HeartbeatSec = 30
    )
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $start    = Get-Date
    $lastBeat = 0
    $probeCmd = 'docker version --format ''{{.Server.Version}}'' >/dev/null 2>&1; if [ $? -eq 0 ]; then echo OK; else echo NO; fi'
    Write-Note ("waiting for Docker engine reachable from {0} (max {1} min)" -f $Distro, [Math]::Floor($TimeoutSec/60))
    while ((Get-Date) -lt $deadline) {
        $probe = (wsl -d $Distro -u root -- bash -c $probeCmd 2>$null).Trim()
        if ($probe -eq 'OK') {
            return [int]((Get-Date) - $start).TotalSeconds
        }
        $elapsed = [int]((Get-Date) - $start).TotalSeconds
        if (($elapsed - $lastBeat) -ge $HeartbeatSec) {
            $remaining = [int]($deadline - (Get-Date)).TotalSeconds
            Write-Note ("  {0}m {1}s elapsed, still waiting (max {2}m {3}s remaining)..." -f `
                [Math]::Floor($elapsed/60), ($elapsed % 60),
                [Math]::Floor($remaining/60), ($remaining % 60))
            $lastBeat = $elapsed
        }
        Start-Sleep -Seconds 4
    }
    return -1
}

# ---- admin check --------------------------------------------------
Write-Step "Elevation check"
$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Re-run from an elevated PowerShell prompt (Run as Administrator). WSL and Docker installs need it."
}
Write-Ok "running as Administrator"

# ---- System check (CPU / RAM / disk) ------------------------------
# Display the host's specs and flag anything below recommended
# minima for a `--role=both` install. Soft warnings only -- the
# install will continue so operators can still proceed on edge
# hardware (e.g. a lab laptop) at their own risk.
#
# Minima are conservative:
#   * 4 logical CPUs    -- docker compose build runs core (Go) and
#                          tester (Python) builds in parallel; below
#                          4 it gets painfully slow but still works.
#   * 8 GB RAM          -- DPDK + Go compile + Python interp + WSL2
#                          distro overhead. Below 8 the build OOMs.
#   * 25 GB free on C:  -- Docker Desktop install (~1 GB), WSL2 distro
#                          (~3 GB), base images (~2 GB), built sacore
#                          + satester images (~8 GB), Wireshark
#                          watcher netshoot image (~600 MB), plus
#                          headroom. 25 GB has 5 GB to spare on a
#                          successful install.
Write-Step "System check"
$cpuInfo = Get-CimInstance Win32_Processor | Select-Object -First 1
$cpuName = ($cpuInfo.Name).Trim()
$cpuCores = [int]$cpuInfo.NumberOfCores
$cpuLogical = [int]$cpuInfo.NumberOfLogicalProcessors

$cs = Get-CimInstance Win32_ComputerSystem
$ramTotalGB = [Math]::Round($cs.TotalPhysicalMemory / 1GB, 1)
$osi = Get-CimInstance Win32_OperatingSystem
$ramFreeGB  = [Math]::Round($osi.FreePhysicalMemory / 1MB, 1)

$systemDriveLetter = ($env:SystemDrive).Substring(0,1)
$disk = Get-CimInstance Win32_LogicalDisk -Filter "DeviceID='$($env:SystemDrive)'"
$diskFreeGB  = [Math]::Round($disk.FreeSpace / 1GB, 1)
$diskTotalGB = [Math]::Round($disk.Size / 1GB, 1)

Write-Ok ("CPU:     {0}" -f $cpuName)
Write-Ok ("         {0} cores ({1} logical)" -f $cpuCores, $cpuLogical)
Write-Ok ("RAM:     {0} GB total, {1} GB free" -f $ramTotalGB, $ramFreeGB)
Write-Ok ("Disk {0}: {1} GB free of {2} GB" -f $systemDriveLetter, $diskFreeGB, $diskTotalGB)
$winVer = [System.Environment]::OSVersion.Version
Write-Ok ("Windows: build {0}" -f $winVer.Build)

$minCpu  = 4
$minRam  = 8
$minDisk = 25

$warnings = @()
if ($cpuLogical -lt $minCpu) {
    $warnings += "CPU: $cpuLogical logical processors (recommended >= $minCpu) -- builds will be slow"
}
if ($ramTotalGB -lt $minRam) {
    $warnings += "RAM: $ramTotalGB GB total (recommended >= $minRam GB) -- docker compose build may OOM"
}
if ($diskFreeGB -lt $minDisk) {
    $warnings += "Disk $systemDriveLetter`: $diskFreeGB GB free (required >= $minDisk GB) -- install will likely run out of space"
}
if ($warnings.Count -eq 0) {
    Write-Ok ("meets minimum requirements (>= {0} cores, >= {1} GB RAM, >= {2} GB free on {3}:)" -f $minCpu, $minRam, $minDisk, $systemDriveLetter)
} else {
    foreach ($w in $warnings) { Write-Fail $w }
    Write-Note "install will continue; expect slowness / failures matching the warnings above"
}

# ---- Step 3: Windows version --------------------------------------
Write-Step "Windows version"
$ver = [System.Environment]::OSVersion.Version
if ($ver.Build -lt 19041) {
    throw "Windows build $($ver.Build) is too old. WSL2 needs build 19041+ (Windows 10 2004 or any Windows 11)."
}
Write-Ok "build $($ver.Build) supports WSL2"

# ---- Step 3: WSL2 runtime -----------------------------------------
Write-Step "WSL2 runtime"
if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Write-Note "wsl.exe not present — running 'wsl --install --no-distribution'"
    wsl --install --no-distribution | Out-Null
    Write-Note "reboot Windows and then re-run this script — stopping here"
    return
}
wsl --update 2>&1 | Out-Null
Write-Ok "ready"

# ---- Step 4: Ubuntu distro ----------------------------------------
Write-Step "Linux distro: $Distro"
$installedRaw = (wsl --list --quiet) -join "`n"
$installed = ($installedRaw -replace "`0","") -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ }
if ($installed -contains $Distro) {
    Write-Skip "already installed"
} else {
    Write-Note "installing $Distro (may take several minutes)"
    wsl --install -d $Distro --no-launch | Out-Null
    wsl -d $Distro -u root -- bash -c "true" | Out-Null
    Write-Ok "installed"
}

# ---- Step 5: Docker Desktop ---------------------------------------
if (-not $SkipDockerDesktop) {
    Write-Step "Docker Desktop"
    $ddExe = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
    if (-not (Test-Path $ddExe)) {
        Write-Note "downloading installer"
        $url = 'https://desktop.docker.com/win/main/amd64/Docker%20Desktop%20Installer.exe'
        $dl  = Join-Path $env:TEMP 'DockerDesktopInstaller.exe'
        Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $dl
        Write-Note "running silent install (5-15 min on a first-ever install: HCS image + WSL2 distro + service registration)"
        # Heartbeat-friendly install: drop -Wait, use -PassThru and
        # poll. Operators on fresh boxes were watching a frozen
        # terminal for 10+ min and bailing out, thinking we'd hung.
        $p = Start-Process -FilePath $dl `
            -ArgumentList 'install','--quiet','--accept-license','--backend=wsl-2' `
            -PassThru
        $rc = Wait-ProcessWithHeartbeat -Process $p -Label 'Docker Desktop installer'
        if ($rc -ne 0) {
            throw "Docker Desktop installer exited $rc."
        }
        Write-Ok "installed"
    } else {
        Write-Skip "already installed"
    }

    # Configure: enable WSL2 engine and add the target distro to the
    # integration list. Docker Desktop writes settings to one of two
    # filenames depending on version.
    $settingsPath = Join-Path $env:APPDATA 'Docker\settings-store.json'
    if (-not (Test-Path $settingsPath)) {
        $settingsPath = Join-Path $env:APPDATA 'Docker\settings.json'
    }
    if (-not (Test-Path $settingsPath)) {
        Write-Note "seeding settings (launching Docker Desktop once)"
        Start-Process -FilePath $ddExe | Out-Null
        $deadline = (Get-Date).AddSeconds(120)
        while ((Get-Date) -lt $deadline -and -not (Test-Path $settingsPath)) {
            Start-Sleep -Seconds 2
        }
    }
    if (-not (Test-Path $settingsPath)) {
        throw "Docker Desktop settings file never appeared at $settingsPath. Launch Docker Desktop manually and re-run."
    }

    $cfg = Get-Content $settingsPath -Raw | ConvertFrom-Json
    if (-not $cfg) { $cfg = [pscustomobject]@{} }
    # StrictMode-safe property lookup: ask the property-info collection
    # directly. `.PSObject.Properties.Name -contains 'X'` auto-projects
    # `.Name` over the collection, which throws PropertyNotFoundStrict
    # under Set-StrictMode -Version Latest when the collection is empty.
    $changed = $false
    $wslOn = $false
    $wslProp = $cfg.PSObject.Properties['WslEngineEnabled']
    if ($wslProp -and $wslProp.Value) { $wslOn = [bool]$wslProp.Value }
    if (-not $wslOn) {
        $cfg | Add-Member -NotePropertyName WslEngineEnabled -NotePropertyValue $true -Force
        $changed = $true
    }
    $currentDistros = @()
    $distrosProp = $cfg.PSObject.Properties['IntegratedWslDistros']
    if ($distrosProp -and $distrosProp.Value) {
        $currentDistros = @($distrosProp.Value)
    }
    if ($currentDistros -notcontains $Distro) {
        $cfg | Add-Member -NotePropertyName IntegratedWslDistros -NotePropertyValue ($currentDistros + $Distro) -Force
        $changed = $true
    }
    if ($changed) {
        $json = $cfg | ConvertTo-Json -Depth 32
        [System.IO.File]::WriteAllText($settingsPath, $json, (New-Object System.Text.UTF8Encoding $false))
        Write-Note "settings updated; restarting Docker Desktop"
        Get-Process 'Docker Desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
        Start-Sleep -Seconds 3
        Start-Process -FilePath $ddExe | Out-Null
    } else {
        if (-not (Get-Process 'Docker Desktop' -ErrorAction SilentlyContinue)) {
            Start-Process -FilePath $ddExe | Out-Null
        }
    }

    # Wait up to 15 min for the engine to come up. Fresh-install on
    # a cold machine routinely takes 5-10 min for HCS image fetch +
    # WSL2 distro provisioning + engine init; the old 180 s budget
    # tripped on every such box. Heartbeats keep operators informed.
    $probeCmd = 'docker version --format ''{{.Server.Version}}'' >/dev/null 2>&1; if [ $? -eq 0 ]; then echo OK; else echo NO; fi'
    $waitSec = Wait-DockerEngineReady -Distro $Distro -TimeoutSec 900
    if ($waitSec -lt 0) {
        throw "Docker engine not reachable from $Distro after 15 minutes. Open Docker Desktop, verify Settings -> Resources -> WSL Integration lists '$Distro', then re-run."
    }
    Write-Ok "engine reachable from $Distro (after ${waitSec}s)"

    # ---- Step 6: Docker daemon DNS --------------------------------
    # WSL2's auto-generated /etc/resolv.conf points at the Windows
    # virtualised DNS (10.255.255.254) which sporadically times out
    # behind corporate proxies, VPNs, or after Windows network-stack
    # state changes — producing the classic
    #   dial tcp: lookup auth.docker.io on 10.255.255.254:53: i/o timeout
    # during `docker build` / `docker pull`. The fix is to set the
    # docker daemon's `dns` array explicitly. Docker Desktop reads
    # daemon config from %APPDATA%\Docker\daemon.json (= the JSON the
    # Settings -> Docker Engine GUI editor writes). Merging keeps any
    # other settings (insecure-registries, builder…) intact.
    Write-Step "Docker daemon DNS"
    $daemonPath = Join-Path $env:APPDATA 'Docker\daemon.json'
    $daemonDir  = Split-Path $daemonPath -Parent
    if (-not (Test-Path $daemonDir)) {
        New-Item -ItemType Directory -Force -Path $daemonDir | Out-Null
    }
    $daemonCfg = [pscustomobject]@{}
    if (Test-Path $daemonPath) {
        try {
            $raw = Get-Content $daemonPath -Raw
            if ($raw -and $raw.Trim()) {
                $daemonCfg = $raw | ConvertFrom-Json
            }
        } catch {
            Write-Note "daemon.json existed but was not valid JSON — rewriting"
            $daemonCfg = [pscustomobject]@{}
        }
    }
    if (-not $daemonCfg) { $daemonCfg = [pscustomobject]@{} }

    $wantDns = @('8.8.8.8','1.1.1.1')
    $currentDns = @()
    # StrictMode-safe property lookup. .PSObject.Properties.Name auto-
    # enumerates on a collection; on an empty pscustomobject the
    # collection has no elements and `.Name` throws under
    # Set-StrictMode -Version Latest. Asking the collection for the
    # property by name returns $null when absent, which is safe.
    $dnsProp = $daemonCfg.PSObject.Properties['dns']
    if ($dnsProp -and $dnsProp.Value) {
        $currentDns = @($dnsProp.Value)
    }
    $needDnsUpdate = $false
    foreach ($d in $wantDns) {
        if ($currentDns -notcontains $d) { $needDnsUpdate = $true; break }
    }
    if ($needDnsUpdate) {
        $merged = @($wantDns + ($currentDns | Where-Object { $wantDns -notcontains $_ }))
        $daemonCfg | Add-Member -NotePropertyName dns -NotePropertyValue $merged -Force
        $json = $daemonCfg | ConvertTo-Json -Depth 32
        [System.IO.File]::WriteAllText($daemonPath, $json, (New-Object System.Text.UTF8Encoding $false))
        Write-Note "wrote dns=[$($merged -join ', ')]; restarting engine"
        Get-Process 'Docker Desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
        Start-Sleep -Seconds 4
        Start-Process -FilePath $ddExe | Out-Null

        # Same generous timeout as the initial engine-ready wait
        # above (15 min) -- a daemon.json change forces a full
        # restart and on a slow box that can take similarly long.
        $waitSec = Wait-DockerEngineReady -Distro $Distro -TimeoutSec 900
        if ($waitSec -lt 0) {
            throw "Docker engine didn't come back within 15 minutes after the DNS-config restart. Open Docker Desktop manually, confirm it's running, then re-run."
        }
        Write-Ok "engine ready with new DNS (after ${waitSec}s)"
    } else {
        Write-Skip "daemon.json already configured (8.8.8.8 + 1.1.1.1)"
    }

    # ---- DNS preflight from inside Docker -------------------------
    # The failing layer last time was auth.docker.io resolution
    # *inside the build sandbox*. Probe it directly so we fail fast
    # with a clear message before docker build prints a wall of dust.
    #
    # All stderr is redirected to /dev/null INSIDE the bash command so
    # PowerShell never sees it (otherwise wsl's `Unable to find image
    # alpine:3.20 locally` line surfaces as a NativeCommandError and
    # spooks the operator). Stdout from the probe is a single token:
    # OK / FAIL / PULLFAIL. We treat everything except OK as a soft
    # warning — the build proceeds either way.
    Write-Note "probing registry DNS from inside docker"
    $dnsProbeCmd =
        '{ docker pull --quiet alpine:3.20 >/dev/null 2>&1 || echo PULLFAIL; } && ' +
        'docker run --rm --network=bridge alpine:3.20 sh -c ' +
        "'getent hosts registry-1.docker.io >/dev/null 2>&1 && " +
        "getent hosts auth.docker.io >/dev/null 2>&1 && echo OK || echo FAIL' 2>/dev/null"
    $dnsProbe = ''
    try {
        # 2>&1 collapses native stderr into stdout (where PowerShell can
        # see it); the probe itself outputs the single OK/FAIL token on
        # the last line — everything before that is silenced inside bash.
        $raw = wsl -d $Distro -u root -- bash -lc $dnsProbeCmd 2>&1
        $dnsProbe = (($raw | Out-String) -split "`r?`n" | Where-Object { $_ -ne '' } | Select-Object -Last 1)
        if ($dnsProbe) { $dnsProbe = $dnsProbe.Trim() }
    } catch {
        # Native errors from wsl are non-fatal here; the build will
        # produce a much clearer error if DNS really is broken.
        $dnsProbe = "ERR: $($_.Exception.Message)"
    }
    switch ($dnsProbe) {
        'OK' {
            Write-Ok "registry-1.docker.io + auth.docker.io resolve"
        }
        'PULLFAIL' {
            Write-Note "couldn't pull alpine:3.20 for probe — if build also fails with lookup timeouts, check Settings -> Docker Engine has dns=[8.8.8.8,1.1.1.1]"
        }
        default {
            Write-Note "probe returned '$dnsProbe' (not OK) — continuing; build will surface any real DNS issue"
        }
    }
}

# ---- Step 7: stage + run install.sh inside the distro -------------
Write-Step "install.sh inside $Distro (role=$Role version=$Version)"

# Build the install.sh argv. install.sh is the canonical single
# entrypoint; this PowerShell wrapper exists only to bootstrap WSL
# and Docker Desktop on Windows.
$installArgs = @("--role=$Role", "--version=$Version")
if ($CoreUrl)  { $installArgs += "--core-url=$CoreUrl" }
if ($CoreHost) { $installArgs += "--core-host=$CoreHost" }
if ($SkipBringUp) { $installArgs += "--skip-up" }

# Locate install.sh. Three sources, in priority order:
#   1. -SourceDir (Windows path) — operator clone or manual extract
#   2. Adjacent to this .ps1 (operator extracted online bundle on Windows)
#   3. git-clone fallback further below (the irm | iex bootstrap path)
#
# $PSScriptRoot is empty when the script is run via `iex`. The fallback
# `$MyInvocation.MyCommand.Definition` returns the SCRIPT BODY (multi-
# line text) in that case, not a path — feeding it to Split-Path would
# crash with "Cannot find a provider with the name '[Net.…]'" because
# Split-Path parses the literal first line as a provider-qualified path.
# Treat it as a path only when it actually looks like one.
$scriptHere = $null
if ($PSScriptRoot) {
    $scriptHere = $PSScriptRoot
} else {
    $def = $null
    try { $def = $MyInvocation.MyCommand.Definition } catch { $def = $null }
    if ($def -and ($def -notmatch "`n") -and (Test-Path -LiteralPath $def -ErrorAction SilentlyContinue)) {
        $scriptHere = Split-Path -Parent $def
    }
}

$winSourceDir = ''
if ($SourceDir) {
    if (-not (Test-Path $SourceDir)) { throw "SourceDir not found: $SourceDir" }
    $winSourceDir = (Resolve-Path $SourceDir).Path
    foreach ($d in @('core','tester','orchestrate')) {
        if (-not (Test-Path (Join-Path $winSourceDir $d))) {
            throw "SourceDir is not a mono checkout (missing $d subdir): $winSourceDir"
        }
    }
} elseif ($scriptHere) {
    # Standard mono-checkout layout: install.ps1 lives in orchestrate/,
    # with core/ + tester/ as siblings one directory up. This is what
    # install_on_windows.bat hands us after `git clone`.
    $maybeRoot = Split-Path -Parent $scriptHere
    if ($maybeRoot -and
        (Test-Path (Join-Path $maybeRoot 'core')) -and
        (Test-Path (Join-Path $maybeRoot 'tester')) -and
        (Test-Path (Join-Path $maybeRoot 'orchestrate'))) {
        $winSourceDir = $maybeRoot
    } elseif ((Test-Path (Join-Path $scriptHere 'install.sh')) -and
              (Test-Path (Join-Path $scriptHere 'core')) -and
              (Test-Path (Join-Path $scriptHere 'tester'))) {
        # Legacy flat bundle: install.sh + core + tester all in one dir.
        $winSourceDir = $scriptHere
    }
}

if (-not $winSourceDir) {
    # No source tree found — typical for the `irm | iex` one-liner
    # path. Auto-clone the public mono repo so the install is truly
    # seamless. Cached under %LOCALAPPDATA% so repeat runs are fast.
    $cloneRoot = Join-Path $env:LOCALAPPDATA 'mmt-studio-5g6g'
    Write-Note "bootstrapping mono source tree at $cloneRoot"

    # Ensure git for Windows is available. Docker Desktop ships its
    # own git inside its bundled MSYS, but it's not on $PATH; winget
    # is the cleanest user-mode install path on Win 10 1809+ / Win 11.
    if (-not (Get-Command git.exe -ErrorAction SilentlyContinue)) {
        Write-Note "installing Git for Windows via winget"
        if (-not (Get-Command winget.exe -ErrorAction SilentlyContinue)) {
            throw "Neither git nor winget is available. Install Git for Windows manually from https://git-scm.com/download/win and re-run."
        }
        & winget install --id Git.Git --silent --accept-source-agreements --accept-package-agreements | Out-Null
        if ($LASTEXITCODE -ne 0) {
            throw "winget install Git.Git failed (exit $LASTEXITCODE). Install Git manually from https://git-scm.com/download/win and re-run."
        }
        # Refresh PATH for this session so the just-installed git.exe is reachable.
        $env:PATH = [System.Environment]::GetEnvironmentVariable('Path','Machine') + ';' +
                    [System.Environment]::GetEnvironmentVariable('Path','User')
        if (-not (Get-Command git.exe -ErrorAction SilentlyContinue)) {
            throw "Git installed but git.exe still not on PATH. Open a fresh PowerShell window and re-run."
        }
    }

    $repoUrl = 'https://github.com/Makemytechnology/mmt-studio-5g6g.git'

    # Git for Windows defaults to core.autocrlf=true, which rewrites
    # every shell script's LF line endings to CRLF on checkout. Inside
    # WSL bash then chokes with:
    #   /…/install.sh: line 42: $'\r': command not found
    #   /…/install.sh: line 43: set: pipefail: invalid option name
    # Force LF-only line endings for the local clone (the override is
    # passed via -c so it persists to the new repo's .git/config).
    $gitLfFlags = @('-c','core.autocrlf=false','-c','core.eol=lf')

    if (Test-Path (Join-Path $cloneRoot '.git')) {
        Write-Note "cache hit — fetching latest origin/main"
        Push-Location $cloneRoot
        try {
            # Pin the cached repo to LF (idempotent; covers caches
            # created before this fix landed).
            & git config core.autocrlf false
            & git config core.eol lf
            & git @gitLfFlags fetch --quiet origin main
            if ($LASTEXITCODE -ne 0) { throw "git fetch failed (exit $LASTEXITCODE)" }
            # Drop the working tree before reset so the next checkout
            # re-materialises files under the new LF policy regardless
            # of what the cache held before. Without this, files that
            # were already on disk with CRLF stay CRLF.
            & git @gitLfFlags rm --quiet --cached -r '.' | Out-Null
            & git @gitLfFlags reset --hard --quiet origin/main
            if ($LASTEXITCODE -ne 0) { throw "git reset failed (exit $LASTEXITCODE)" }
        } finally { Pop-Location }
    } else {
        Write-Note "cloning $repoUrl (shallow, LF forced)"
        # --depth 1: customer install only needs HEAD; the published
        # mirror is a single-commit-per-publish repo anyway so the full
        # history is just the one release commit. Saves ~80% over the
        # full clone on slow links.
        New-Item -ItemType Directory -Force -Path (Split-Path $cloneRoot -Parent) | Out-Null
        & git @gitLfFlags clone --quiet --depth 1 --branch main $repoUrl $cloneRoot
        if ($LASTEXITCODE -ne 0) {
            throw "git clone $repoUrl failed (exit $LASTEXITCODE). Check internet + DNS, then re-run."
        }
    }

    foreach ($d in @('core','tester','orchestrate')) {
        if (-not (Test-Path (Join-Path $cloneRoot $d))) {
            throw "Clone landed but $d/ missing -- repo layout drift? Path: $cloneRoot"
        }
    }
    $winSourceDir = $cloneRoot
    Write-Ok "source tree ready"
}

# Hand the source tree to the distro via /mnt/<drive>/...
$drive = $winSourceDir.Substring(0,1).ToLower()
$tail  = $winSourceDir.Substring(2) -replace '\\','/'
$wslSource = "/mnt/$drive$tail"
Write-Note "source: $winSourceDir  (wsl: $wslSource)"

# rsync to a Linux-side path so install.sh runs against a native
# filesystem (NTFS mounts confuse docker buildx's COPY perms and
# slow the Go build dramatically).
$wslLinuxRoot = '/root/mmt-studio'
$installArgs += "--source-dir=$wslLinuxRoot"

# Defensive CR-strip on every *.sh inside the staged tree, in case
# the source on the Windows side was checked out via Git for Windows
# with core.autocrlf=true (which produces CRLF line endings that
# break bash with `$'\r': command not found`). The git clone above
# is configured with autocrlf=false, but operators using -SourceDir
# pointing at a pre-existing Windows clone may hit the issue.
Write-Note "copying source tree from Windows into WSL (rsync over /mnt/ is slow -- 5-10 min on first run)"
# Combined wsl invocation: stage (rsync) then install.sh. Single
# call simplifies error handling (one $LASTEXITCODE to check) and
# avoids the Start-Process / Process.ExitCode null-on-Windows
# PowerShell 5.1 weirdness operators hit when we split it.
#
# Two flow-keeping tricks so the parent's tail loop never sees an
# idle stretch long enough to bail out, without spamming the log
# with all ~12 000 source filenames (`-v` did that; operators on
# Windows complained the terminal got drowned in ETSI MAP .asn
# path noise during install):
#
#  1. `rsync --info=name0,progress2` -- emits a single cumulative
#     progress line (`123M 50% 15.6MB/s 0:00:08`) every fraction
#     of a second, overwriting in place via \r. The file size
#     grows continuously (each \r-terminated update adds bytes to
#     the captured log), keeping the parent's idle-detector
#     happy, but the operator sees ONE moving progress line in
#     the tailed terminal instead of a flood. `name0` explicitly
#     suppresses per-file output so the more-verbose default
#     `name1` that newer rsync versions ship doesn't leak through.
#
#  2. `stdbuf -oL -eL` wrapping install.sh -- libc full-buffers
#     printf when stdout isn't a TTY (which it isn't when wsl.exe
#     runs under non-interactive PowerShell). Each [N/12] phase
#     line is short and never fills the 4 KiB block, so without
#     stdbuf the operator sees nothing for 10-15 min. stdbuf flips
#     install.sh's stdout back to line-buffered.
$stageCmd = "set -e; mkdir -p '$wslLinuxRoot'; rsync -a --info=name0,progress2 --delete --exclude='.git/' --exclude='release-tmp/' --exclude='dist/' '$wslSource/' '$wslLinuxRoot/'; find '$wslLinuxRoot' -type f -name '*.sh' -exec sed -i 's/\r`$//' {} +; chmod +x '$wslLinuxRoot/orchestrate/install.sh'"
$argString = ($installArgs | ForEach-Object { "'$_'" }) -join ' '
$runCmd = "stdbuf -oL -eL bash '$wslLinuxRoot/orchestrate/install.sh' $argString"
wsl -d $Distro -u root -- bash -lc "$stageCmd && $runCmd"

if ($LASTEXITCODE -ne 0) {
    throw "install.sh exited with code $LASTEXITCODE inside $Distro."
}

Write-Host ""
Write-Host "MMT Studio is up." -ForegroundColor Cyan
switch ($Role) {
    'both'   { Write-Ok "core   -> http://localhost:5000"; Write-Ok "tester -> http://localhost:5001" }
    'core'   { Write-Ok "core   -> http://localhost:5000" }
    'tester' { Write-Ok "tester -> http://localhost:5001  (wired to $CoreUrl$CoreHost)" }
}
Write-Host ""
Write-Host "To stop later:" -ForegroundColor Cyan
# Use /opt/mmt-studio/orchestrate (the source-symlink install.sh
# now creates) rather than /opt/mmt-studio directly -- compose's
# relative build contexts (../core, ../tester) resolve from cwd,
# not from the symlinked compose-file's target, and pointing the
# operator at the orchestrate symlink keeps `up`/`down`/`build`
# all working from the same path.
$stopCmd = "wsl -d $Distro -u root -- bash -lc `"cd /opt/mmt-studio/orchestrate && docker compose --profile $Role down`""
Write-Host "    $stopCmd" -ForegroundColor Gray

# ---- per-test Wireshark watcher (default-on) ----------------------
# Windows mirror of `run_studio.sh --wireshark`, launched the same
# way run_studio.bat does its watcher (hidden console, log file
# redirected) so the operator's terminal isn't cluttered with a
# second window. Default-on because the alternative -- operator
# finishes install, runs a test, no Wireshark, has to discover
# `run_studio.bat wireshark` to actually get the per-test view --
# was a frequent footgun.
$wsScript = ''
if ($scriptHere) {
    $wsScript = Join-Path $scriptHere 'tools\wireshark\per_test_wireshark.ps1'
}
if (-not $wsScript -or -not (Test-Path $wsScript)) {
    # Fall back to the path under $winSourceDir (e.g. when running via
    # the auto-clone bootstrap, $scriptHere may point at a different
    # checkout than what install.sh staged).
    if ($winSourceDir) {
        $wsScript = Join-Path $winSourceDir 'orchestrate\tools\wireshark\per_test_wireshark.ps1'
    }
}

# Point at run_studio.bat as the steady-state entry point so the
# operator knows where to go next (start/stop/restart/Wireshark).
$runStudio = ''
if ($scriptHere) { $runStudio = Join-Path $scriptHere 'run_studio.bat' }
if (-not $runStudio -or -not (Test-Path $runStudio)) {
    if ($winSourceDir) { $runStudio = Join-Path $winSourceDir 'orchestrate\run_studio.bat' }
}

if (-not $NoWireshark) {
    Write-Host ""
    if (-not (Test-Path $wsScript)) {
        Write-Note "Wireshark watcher script not found at $wsScript -- skipping (run run_studio.bat later if you want it)"
    } else {
        Write-Step "Arming Wireshark watcher (background, no terminal)"
        $watcherLog = Join-Path $env:TEMP 'mmt-wireshark-watcher.log'
        if (Test-Path $watcherLog)          { Remove-Item $watcherLog          -Force -ErrorAction SilentlyContinue }
        if (Test-Path "$watcherLog.err")    { Remove-Item "$watcherLog.err"    -Force -ErrorAction SilentlyContinue }
        # Hidden powershell host + redirected stdout/stderr so the
        # watcher logs are diagnosable without occupying a second
        # console window. Matches run_studio.ps1's Start-WiresharkWatcher
        # exactly so re-runs (run_studio.bat wireshark) replace
        # cleanly.
        Start-Process -FilePath 'powershell.exe' `
            -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File',$wsScript `
            -WindowStyle Hidden `
            -RedirectStandardOutput $watcherLog `
            -RedirectStandardError  "$watcherLog.err" | Out-Null
        Write-Ok "armed -- Wireshark will open when a test transitions to RUNNING"
        Write-Ok "  watcher log: $watcherLog"
    }
}

if ($runStudio -and (Test-Path $runStudio)) {
    Write-Host ""
    Write-Host "Day-to-day control of the studio (start/stop/restart/Wireshark):" -ForegroundColor Cyan
    Write-Host "    $runStudio          # default: start stack + Wireshark watcher" -ForegroundColor Gray
    Write-Host "    $runStudio down     # stop stack + watcher" -ForegroundColor Gray
    Write-Host "    $runStudio restart  # restart everything" -ForegroundColor Gray
}

try { Stop-Transcript | Out-Null } catch { }

# Tell the original non-elevated parent (which is tailing this log
# in its terminal) that we're done. The parent watches for this
# sentinel; once it appears it drains the last log bytes and exits.
# Only meaningful when running as the hidden elevated child --
# if the user invoked install.ps1 directly from an admin shell,
# there's no parent watching and this is just a harmless file write.
if ($ElevatedChild) {
    try {
        Set-Content -LiteralPath (Join-Path $env:TEMP 'mmt-studio-install.done') `
            -Value "0" -Encoding ASCII
    } catch { }
}
