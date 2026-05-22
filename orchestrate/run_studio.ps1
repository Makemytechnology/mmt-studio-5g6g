# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
#
# run_studio.ps1 -- Windows runtime helper for MMT Studio.
#
# Mirrors the day-to-day surface of `orchestrate/run_studio.sh` for
# Windows hosts where the stack lives inside a WSL2 distro and Docker
# Desktop's daemon. install_on_windows.bat is one-time bootstrap; this
# script is what operators use after that for start / stop / restart
# and per-test Wireshark.
#
# Default action is `up` with the per-test Wireshark watcher enabled
# (matches `run_studio.sh --wireshark up` semantics, just made the
# default because every Windows operator so far has asked for it).
#
# Usage:
#
#   run_studio.bat                  # up + Wireshark watcher
#   run_studio.bat up
#   run_studio.bat down             # stop stack + kill watcher
#   run_studio.bat restart          # down + up
#   run_studio.bat status           # docker compose ps
#   run_studio.bat logs             # tail compose logs (Ctrl-C to detach)
#   run_studio.bat wireshark        # only (re)start the watcher
#
# Common flags:
#
#   -NoWireshark        # up/restart without launching the watcher
#   -Role <core|tester|both>     # match the compose profile install.sh wrote
#   -Distro Ubuntu-24.04         # override WSL distro name
#   -InstallDir /opt/mmt-studio  # override Linux-side compose root
#
# This script does NOT need Administrator -- WSL + docker.exe work
# from a normal user session.

[CmdletBinding()]
param(
    [Parameter(Position = 0)]
    [ValidateSet('up','down','restart','status','ps','logs','wireshark','help','')]
    [string]$Action = '',

    [ValidateSet('core','tester','both')]
    [string]$Role = 'both',

    [string]$Distro      = 'Ubuntu-24.04',

    # Linux-side install dir written by install.sh. Holds the .env
    # and a symlink to docker-compose.yml; we run compose actions
    # from here so we see the same configuration install.sh did.
    [string]$InstallDir  = '/opt/mmt-studio',

    # Compose project name. install.sh runs `docker compose ... up`
    # from /root/mmt-studio/orchestrate, so docker stores the project
    # as `orchestrate` (basename of that dir). We pin it here via
    # `-p` so we control THOSE containers regardless of which
    # directory we're in -- otherwise `docker compose ps` from
    # /opt/mmt-studio silently shows zero services (project mismatch).
    [string]$Project     = 'orchestrate',

    # Opt OUT of the per-test Wireshark watcher. By default `up` /
    # `restart` (re)start it; `down` always stops it.
    [switch]$NoWireshark
)

Set-StrictMode -Version Latest
# Same reasoning as per_test_wireshark.ps1: keep on 'Continue' because
# we shell out to docker.exe / wsl.exe heavily, and `Stop` would
# promote benign native stderr (e.g. `docker rm` on a missing container)
# into a terminating NativeCommandError.
$ErrorActionPreference = 'Continue'

# ---- log helpers --------------------------------------------------
function Write-Step ($m) { Write-Host ("==> {0}" -f $m) -ForegroundColor Cyan }
function Write-Ok   ($m) { Write-Host ("    ok  {0}" -f $m) -ForegroundColor Green }
function Write-Note ($m) { Write-Host ("    ... {0}" -f $m) -ForegroundColor Yellow }
function Write-Skip ($m) { Write-Host ("    --  {0}" -f $m) -ForegroundColor DarkGray }
function Write-Err  ($m) { Write-Host ("    !!! {0}" -f $m) -ForegroundColor Red }

# ---- paths --------------------------------------------------------
$script:Here = Split-Path -Parent $MyInvocation.MyCommand.Path
$script:WatcherScript = Join-Path $script:Here 'tools\wireshark\per_test_wireshark.ps1'

function Show-Help {
    Get-Content -LiteralPath $MyInvocation.MyCommand.Path |
        Select-Object -First 35 |
        ForEach-Object { Write-Host $_ }
}

# ---- prerequisite probes -----------------------------------------
function Test-Wsl {
    if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
        Write-Err "wsl.exe not found. Run install_on_windows.bat first."
        return $false
    }
    return $true
}

function Test-Docker {
    if (-not (Get-Command docker.exe -ErrorAction SilentlyContinue)) {
        Write-Err "docker.exe not found. Run install_on_windows.bat first to install Docker Desktop."
        return $false
    }
    # Fast path: daemon AND WSL integration both already up.
    # The Windows-side `docker.exe version` ping comes back as soon as
    # the engine accepts connections, but Docker Desktop installs the
    # /var/run/docker.sock symlink in each integrated WSL distro a
    # few seconds LATER. Every compose call here runs through
    # `wsl -d $Distro -u root -- bash -lc "docker compose ..."`, so
    # the WSL-side socket is what actually matters. Probe THAT.
    $null = & wsl.exe -d $Distro -u root -- docker version --format '{{.Server.Version}}' 2>$null
    if ($LASTEXITCODE -eq 0) { return $true }
    # Slow path: try to start Docker Desktop ourselves and wait.
    # Operators asked us to do this so `run_studio.bat` is a one-stop
    # entry point after a fresh boot, when Docker Desktop hasn't
    # auto-started yet.
    return (Start-DockerDesktopAndWait)
}

function Start-DockerDesktopAndWait {
    # 600 s (10 min) is generous; covers a cold first-boot where
    # Docker Desktop has to start the Hyper-V VM + WSL2 distro from
    # scratch before the engine is reachable. Steady-state restarts
    # finish in well under a minute -- the heartbeat below will print
    # progress so the operator isn't watching a frozen prompt.
    param([int]$TimeoutSec = 600)
    # Locate Docker Desktop.exe in the usual install paths.
    $candidates = @()
    if ($env:ProgramFiles)        { $candidates += (Join-Path $env:ProgramFiles        'Docker\Docker\Docker Desktop.exe') }
    if (${env:ProgramFiles(x86)}) { $candidates += (Join-Path ${env:ProgramFiles(x86)} 'Docker\Docker\Docker Desktop.exe') }
    $ddExe = $null
    foreach ($c in $candidates) { if (Test-Path $c) { $ddExe = $c; break } }
    if (-not $ddExe) {
        Write-Err "Docker daemon not reachable and Docker Desktop.exe not found in Program Files."
        Write-Err "Install Docker Desktop (or re-run install_on_windows.bat) and retry."
        return $false
    }

    # If Docker Desktop.exe is already running but the engine isn't
    # ready, don't spawn a second instance -- just wait. The system
    # tray icon already represents the running process; a duplicate
    # launch is at best a no-op, at worst a UAC popup.
    $already = Get-Process 'Docker Desktop' -ErrorAction SilentlyContinue
    if ($already) {
        Write-Note "Docker Desktop process is up but daemon isn't ready yet -- waiting"
    } else {
        Write-Step "Starting Docker Desktop (daemon was not reachable)"
        Start-Process -FilePath $ddExe | Out-Null
    }

    # Poll until the engine is reachable FROM INSIDE THE WSL DISTRO.
    # The Windows-side `docker.exe version` ping comes back as soon
    # as the engine accepts connections, but Docker Desktop's WSL
    # integration plumbs /var/run/docker.sock into each integrated
    # distro a few seconds LATER. If we returned at Windows-readiness
    # the next `wsl ... docker compose up` would explode with
    #   dial unix /var/run/docker.sock: connect: no such file or directory
    # First-boot can take 5-10 min on cold machines or freshly
    # installed Docker Desktop (HCS image fetch + WSL2 distro
    # provisioning + engine init + WSL integration). 600 s gives
    # plenty of headroom; the heartbeat below keeps the operator
    # informed.
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $tick = 0
    while ((Get-Date) -lt $deadline) {
        Start-Sleep -Seconds 3
        $tick++
        $null = & wsl.exe -d $Distro -u root -- docker version --format '{{.Server.Version}}' 2>$null
        if ($LASTEXITCODE -eq 0) {
            Write-Ok "Docker daemon reachable from $Distro after ~$($tick * 3)s"
            return $true
        }
        # Print a heartbeat every 30s so the operator knows we're still trying.
        if (($tick % 10) -eq 0) {
            Write-Note "  still waiting for WSL integration ($($tick * 3)s)..."
        }
    }
    Write-Err "Docker Desktop didn't expose its socket to $Distro within ${TimeoutSec}s."
    Write-Err "Open Docker Desktop -> Settings -> Resources -> WSL Integration, confirm $Distro is enabled, then retry."
    return $false
}

function Get-OrchDir {
    # $InstallDir (/opt/mmt-studio) only holds a SYMLINK to the
    # real docker-compose.yml plus the .env install.sh wrote. If we
    # cd there and run `docker compose`, the build contexts in the
    # compose file (`context: ../core`, `context: ../tester`)
    # resolve against the symlink's location (/opt/mmt-studio), so
    # docker tries to build from `/opt/core` -- which doesn't
    # exist, producing the visible
    #   unable to prepare context: path "/opt/core" not found
    # bug a customer hit. The actual source tree lives wherever
    # install.sh staged it (install.ps1 puts it at /root/mmt-studio
    # via rsync; native installs leave it where the user cloned it).
    # Follow the symlink with `readlink -f`, take its dirname --
    # that's the real orchestrate/ directory.
    #
    # Cached in $script:_OrchDir because every Invoke-Wsl call
    # would otherwise spawn a wsl.exe just to resolve a constant.
    if ($script:_OrchDir) { return $script:_OrchDir }
    $bashCmd = "dirname `$(readlink -f '$InstallDir/docker-compose.yml' 2>/dev/null)"
    $resolved = (& wsl.exe -d $Distro -u root -- bash -c $bashCmd 2>$null)
    if ($resolved) { $resolved = ([string]$resolved).Trim() }
    if (-not $resolved -or $resolved -eq '.') {
        # Fallback: assume the legacy layout install.sh has always
        # produced under -SourceDir invocations.
        $resolved = '/root/mmt-studio/orchestrate'
    }
    $script:_OrchDir = $resolved
    return $resolved
}

function Invoke-Wsl {
    # Helper: run a bash command inside the install distro as root.
    # We bake `cd <orch-dir>` in front because every compose action
    # has to run from a directory where its relative build-context
    # paths resolve. Returns $LASTEXITCODE from the child.
    param([string]$BashCmd)
    $orch = Get-OrchDir
    & wsl.exe -d $Distro -u root -- bash -lc "cd $orch && $BashCmd"
}

function Invoke-Compose {
    # Wrapper that pins the project name. Without -p we'd default to
    # the basename of $InstallDir (`mmt-studio`), which doesn't match
    # what install.sh created (`orchestrate`). Mismatched project ==
    # silently zero services.
    param([string]$ComposeArgs)
    Invoke-Wsl "docker compose -p $Project --profile $Role $ComposeArgs"
}

# ---- Wireshark watcher control -----------------------------------
# We don't track the PID -- we identify watcher processes by their
# command-line containing `per_test_wireshark.ps1`. That handles the
# case where the user closed the watcher's window manually (orphan
# Wireshark + capture container with no PowerShell parent) or where
# multiple `run_studio.bat` invocations interleaved.

function Stop-WiresharkWatcher {
    $killed = $false
    # 1. The watcher PowerShell process.
    Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match 'per_test_wireshark\.ps1' } |
        ForEach-Object {
            try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue; $killed = $true } catch { }
        }
    # 2. Any cmd.exe wrappers our watcher spawned (cmd /c pipe).
    Get-CimInstance Win32_Process -Filter "Name='cmd.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match 'mmt-test-capture\.cmd|mmt-test-capture\b' } |
        ForEach-Object {
            try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch { }
        }
    # 3. Wireshark windows the watcher spawned. Two signatures to
    #    match, neither of which a user's manually-opened Wireshark
    #    would carry:
    #      - SmartTest:   `-r <...mmt-capture-snapshot.pcapng>`
    #      - PerTest/Live:`-k -i -` (stdin live capture)
    Get-CimInstance Win32_Process -Filter "Name='Wireshark.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and ($_.CommandLine -match '-k\s+-i\s+-' -or $_.CommandLine -match 'mmt-capture-snapshot\.pcapng') } |
        ForEach-Object {
            try { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue } catch { }
        }
    # 4. Sidecar tcpdump container.
    & docker.exe rm -f mmt-test-capture 2>$null | Out-Null
    if ($killed) { Write-Ok "stopped prior Wireshark watcher" }
}

function Start-WiresharkWatcher {
    if (-not (Test-Path $script:WatcherScript)) {
        Write-Note "watcher script not found at $script:WatcherScript -- skipping Wireshark"
        return
    }
    # Always stop first so re-running `up` doesn't pile up watchers.
    Stop-WiresharkWatcher

    # No capture-image pre-pull needed in the streaming-endpoint
    # architecture -- the tester captures itself via in-process
    # tcpdump and exposes the pcap over HTTP. Watcher just curl|pipes
    # into Wireshark, no docker management on the Windows side.

    Write-Step "Arming Wireshark watcher (background, no terminal)"
    # The watcher polls /api/tests and, on each new test, opens
    # Wireshark wired to the tester's live pcap stream
    # (GET /api/tests/active/pcap.stream). No Wireshark window until
    # a test fires; same window stays open through the whole test
    # (RUNNING -> PASS doesn't rotate it).
    #
    # Hidden powershell host so the operator's terminal isn't
    # cluttered with a second console. Watcher stdout/stderr goes
    # to a log file under %TEMP% so we can diagnose issues without
    # a visible window. `status` surfaces the path.
    $logFile = Join-Path $env:TEMP 'mmt-wireshark-watcher.log'
    if (Test-Path $logFile) { Remove-Item $logFile -Force -ErrorAction SilentlyContinue }
    # Start-Process can redirect a hidden child's streams to files
    # in one step -- no temp .cmd shim needed.
    # No -PerTest / -Persistent flag -> watcher's default SmartTest
    # mode: a hidden continuous capture container plus a poll loop
    # that opens Wireshark whenever /api/tests reports a new test
    # event (new entry, name change, or status change). Wireshark
    # reads a snapshot of the live pcap so every test's packets are
    # already there when the window opens, regardless of how short
    # the test was.
    Start-Process -FilePath 'powershell.exe' `
        -ArgumentList '-NoProfile','-ExecutionPolicy','Bypass','-File',$script:WatcherScript `
        -WindowStyle Hidden `
        -RedirectStandardOutput $logFile `
        -RedirectStandardError  ($logFile + '.err') | Out-Null
    $script:WatcherLogFile = $logFile
    Write-Ok "armed -- Wireshark will open the first time a test transitions to RUNNING"
    Write-Ok "  watcher log: $logFile"
}

# ---- compose actions ---------------------------------------------
function Studio-Up {
    if (-not (Test-Wsl) -or -not (Test-Docker)) { $script:RC = 1; return }
    Write-Step "Starting MMT Studio (profile=$Role)"
    Invoke-Compose "up -d"
    $rc = $LASTEXITCODE
    if ($rc -ne 0) {
        Write-Err "docker compose up failed (exit $rc)"
        $script:RC = $rc
        return
    }
    Write-Ok "stack up"
    Write-Host ""
    switch ($Role) {
        'both'   { Write-Ok "core   -> http://localhost:5000"; Write-Ok "tester -> http://localhost:5001" }
        'core'   { Write-Ok "core   -> http://localhost:5000" }
        'tester' { Write-Ok "tester -> http://localhost:5001" }
    }
    Write-Host ""
    if (-not $NoWireshark) { Start-WiresharkWatcher }
}

function Studio-Down {
    if (-not (Test-Wsl)) { $script:RC = 1; return }
    # Stop the watcher BEFORE compose down so the running capture
    # container doesn't keep the sacore netns alive (the watcher
    # joined sacore's netns; tearing sacore down while tcpdump is
    # still inside throws an exit storm in the watcher's cmd pipe).
    Stop-WiresharkWatcher
    Write-Step "Stopping MMT Studio (profile=$Role)"
    Invoke-Compose "down"
    $rc = $LASTEXITCODE
    if ($rc -ne 0) {
        Write-Err "docker compose down failed (exit $rc)"
        $script:RC = $rc
        return
    }
    Write-Ok "stack down"
}

function Studio-Restart {
    Studio-Down
    if ($script:RC -ne 0) { return }
    Studio-Up
}

function Studio-Status {
    if (-not (Test-Wsl) -or -not (Test-Docker)) { $script:RC = 1; return }
    Write-Step "Compose status (profile=$Role)"
    Invoke-Compose "ps"
    Write-Host ""
    Write-Step "Wireshark watcher"
    $procs = Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -and $_.CommandLine -match 'per_test_wireshark\.ps1' }
    if ($procs) {
        foreach ($p in $procs) { Write-Ok "running, pid $($p.ProcessId)" }
        $logFile = Join-Path $env:TEMP 'mmt-wireshark-watcher.log'
        if (Test-Path $logFile) { Write-Ok "  log: $logFile" }
    } else {
        Write-Skip "not running"
    }
    $cap = & docker.exe ps --filter 'name=mmt-test-capture' --format '{{.Names}} {{.Status}}' 2>$null
    if ($cap) { Write-Ok "capture container: $cap (Wireshark window is live)" }
}

function Studio-Logs {
    if (-not (Test-Wsl)) { $script:RC = 1; return }
    Write-Step "Tailing compose logs (Ctrl-C to detach; containers keep running)"
    Invoke-Compose "logs -f --tail=200"
    if ($LASTEXITCODE -ne 0) { $script:RC = $LASTEXITCODE }
}

# ---- dispatch -----------------------------------------------------
# IMPORTANT: do NOT call functions as `exit (Studio-X)` -- the
# parenthesised invocation captures the function's ENTIRE success
# stream (including native-command stdout like `docker compose ps`
# rows) into a collection, then passes only the trailing integer to
# `exit`. The compose output never reaches the console. Functions
# store their final code in $script:RC instead; output flows
# normally to the host.
if (-not $Action) { $Action = 'up' }
$script:RC = 0

switch ($Action) {
    'help'      { Show-Help; exit 0 }
    'up'        { Studio-Up }
    'down'      { Studio-Down }
    'restart'   { Studio-Restart }
    'status'    { Studio-Status }
    'ps'        { Studio-Status }
    'logs'      { Studio-Logs }
    'wireshark' { Start-WiresharkWatcher }
    default     {
        Write-Err "unknown action: $Action"
        Show-Help
        $script:RC = 2
    }
}

exit $script:RC
