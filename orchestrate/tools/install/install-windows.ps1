# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
#
# install-windows.ps1 -- one-shot Windows installer for MMT Studio.
#
# Bootstraps a fresh Windows 11 (or Windows 10 build 19041+) machine
# to run the full 5G studio end-to-end:
#
#   1. WSL2 runtime (installs if missing; warns if a reboot is needed)
#   2. Ubuntu distro (default Ubuntu-24.04 -- verified per mono-meta README)
#   3. Docker Desktop (downloads + silent install if not present;
#      enables WSL2 engine and adds the target distro to the
#      integration list by editing settings-store.json)
#   4. Hand-off to .\setup-wsl.sh inside the distro, which loads SCTP,
#      installs the hugepages sysctl drop-in, clones the umbrella
#      mono repo, and runs `./run_studio.sh up`.
#
# Run from an ELEVATED PowerShell prompt (Run as Administrator).
# Re-running is safe: each step detects existing state and skips if
# already done.
#
# Usage:
#   .\install-windows.ps1
#   .\install-windows.ps1 -Distro Ubuntu-24.04
#   .\install-windows.ps1 -RepoUrl https://github.com/your-org/fork.git
#   .\install-windows.ps1 -LocalRepoPath C:\Work\mmt-studio-orchestrate
#   .\install-windows.ps1 -SkipBringUp
#
# Compatible with Windows PowerShell 5.1 and PowerShell 7+.
# (Note: this script avoids PS7-only `&&`/`||` operators in PS code;
# any `&&`/`||` you see lives inside a bash string handed to wsl.)
#
# One-line bootstrap (once umbrella repo is public):
#   irm https://raw.githubusercontent.com/Makemytechnology/mmt-studio-5g6g/main/orchestrate/tools/install/install-windows.ps1 | iex

[CmdletBinding()]
param(
    [string]$Distro = 'Ubuntu-24.04',
    [string]$RepoUrl = 'https://github.com/Makemytechnology/mmt-studio-5g6g.git',
    [string]$RepoBranch = 'main',
    [string]$LocalRepoPath = '',
    [switch]$SkipDockerDesktop,
    [switch]$SkipBringUp
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

# ---- logging helpers ----------------------------------------------
function Write-Step($msg) { Write-Host "==> $msg" -ForegroundColor Cyan }
function Write-Ok  ($msg) { Write-Host "    $msg" -ForegroundColor Green }
function Write-Note($msg) { Write-Host "    $msg" -ForegroundColor Yellow }
function Write-Skip($msg) { Write-Host "    skip: $msg" -ForegroundColor DarkGray }

# ---- admin check --------------------------------------------------
$principal = [Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()
if (-not $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)) {
    throw "Re-run from an elevated PowerShell prompt (Run as Administrator). WSL and Docker installs need it."
}
Write-Ok "Running elevated."

# ---- Step 1: Windows version --------------------------------------
Write-Step "Windows version check"
$ver = [System.Environment]::OSVersion.Version
if ($ver.Build -lt 19041) {
    throw "Windows build $($ver.Build) is too old. WSL2 needs build 19041+ (Windows 10 2004 or any Windows 11)."
}
Write-Ok "Build $($ver.Build) supports WSL2."

# ---- Step 2: WSL2 runtime -----------------------------------------
Write-Step "WSL2 runtime"
if (-not (Get-Command wsl.exe -ErrorAction SilentlyContinue)) {
    Write-Note "wsl.exe not present -- running 'wsl --install --no-distribution'."
    wsl --install --no-distribution
    Write-Note "Reboot Windows and then re-run this script. Stopping here."
    return
}
wsl --update | Out-Null
Write-Ok "WSL2 ready."

# ---- Step 3: Ubuntu distro ----------------------------------------
Write-Step "Distro: $Distro"
# `wsl --list --quiet` returns UTF-16 with trailing nulls. Normalize.
$installedRaw = (wsl --list --quiet) -join "`n"
$installed = ($installedRaw -replace "`0","") -split "`r?`n" | ForEach-Object { $_.Trim() } | Where-Object { $_ }
if ($installed -contains $Distro) {
    Write-Skip "$Distro already installed."
} else {
    Write-Note "Installing $Distro (may take several minutes)..."
    wsl --install -d $Distro --no-launch
    # Force first-launch initialization so /etc and the root account settle.
    wsl -d $Distro -u root -- bash -c "true" | Out-Null
    Write-Ok "$Distro installed."
}

# ---- Step 4: Docker Desktop ---------------------------------------
if (-not $SkipDockerDesktop) {
    Write-Step "Docker Desktop"
    $ddExe = Join-Path $env:ProgramFiles 'Docker\Docker\Docker Desktop.exe'
    if (-not (Test-Path $ddExe)) {
        Write-Note "Docker Desktop not found -- downloading installer..."
        $url = 'https://desktop.docker.com/win/main/amd64/Docker%20Desktop%20Installer.exe'
        $dl  = Join-Path $env:TEMP 'DockerDesktopInstaller.exe'
        Invoke-WebRequest -UseBasicParsing -Uri $url -OutFile $dl
        Write-Note "Running silent install (install --quiet --accept-license --backend=wsl-2)..."
        $p = Start-Process -FilePath $dl -ArgumentList 'install','--quiet','--accept-license','--backend=wsl-2' -Wait -PassThru
        if ($p.ExitCode -ne 0) {
            throw "Docker Desktop installer exited $($p.ExitCode)."
        }
        Write-Ok "Docker Desktop installed."
    } else {
        Write-Skip "Docker Desktop already installed: $ddExe"
    }

    # Configure: enable the WSL2 engine and integrate the target distro.
    $settingsPath = Join-Path $env:APPDATA 'Docker\settings-store.json'
    if (-not (Test-Path $settingsPath)) {
        $settingsPath = Join-Path $env:APPDATA 'Docker\settings.json'
    }
    if (-not (Test-Path $settingsPath)) {
        Write-Note "Docker Desktop settings file not found; launching it once to seed it..."
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
    $cfgProps = $cfg.PSObject.Properties.Name
    $changed = $false
    $wslOn = $false
    if ($cfgProps -contains 'WslEngineEnabled') { $wslOn = [bool]$cfg.WslEngineEnabled }
    if (-not $wslOn) {
        $cfg | Add-Member -NotePropertyName WslEngineEnabled -NotePropertyValue $true -Force
        $changed = $true
        Write-Note "Setting WslEngineEnabled = true."
    }
    $currentDistros = @()
    if ($cfgProps -contains 'IntegratedWslDistros' -and $cfg.IntegratedWslDistros) {
        $currentDistros = @($cfg.IntegratedWslDistros)
    }
    if ($currentDistros -notcontains $Distro) {
        $cfg | Add-Member -NotePropertyName IntegratedWslDistros -NotePropertyValue ($currentDistros + $Distro) -Force
        $changed = $true
        Write-Note "Adding $Distro to IntegratedWslDistros."
    }
    if ($changed) {
        # Plain UTF-8 (no BOM); matches Docker Desktop's own writes.
        $json = $cfg | ConvertTo-Json -Depth 32
        [System.IO.File]::WriteAllText($settingsPath, $json, (New-Object System.Text.UTF8Encoding $false))
        Write-Ok "Docker Desktop settings updated."
        Get-Process 'Docker Desktop' -ErrorAction SilentlyContinue | Stop-Process -Force
        Start-Sleep -Seconds 3
        Start-Process -FilePath $ddExe | Out-Null
    } else {
        Write-Skip "Docker Desktop already configured for $Distro."
        if (-not (Get-Process 'Docker Desktop' -ErrorAction SilentlyContinue)) {
            Start-Process -FilePath $ddExe | Out-Null
        }
    }

    # Wait for the daemon to be reachable from inside the distro.
    Write-Step "Waiting for Docker engine to be reachable from $Distro (up to 180s)..."
    $deadline = (Get-Date).AddSeconds(180)
    $ready = $false
    $probeCmd = 'docker version --format ''{{.Server.Version}}'' >/dev/null 2>&1; if [ $? -eq 0 ]; then echo OK; else echo NO; fi'
    while ((Get-Date) -lt $deadline) {
        $probe = (wsl -d $Distro -u root -- bash -c $probeCmd).Trim()
        if ($probe -eq 'OK') { $ready = $true; break }
        Start-Sleep -Seconds 4
    }
    if (-not $ready) {
        throw "Docker engine not reachable from $Distro after 180s. Open Docker Desktop, verify Settings -> Resources -> WSL Integration lists '$Distro', then re-run."
    }
    Write-Ok "Docker engine reachable from $Distro."
}

# ---- Step 5: hand off to setup-wsl.sh -----------------------------
Write-Step "Running setup-wsl.sh inside $Distro"

if ($PSScriptRoot) {
    $here = $PSScriptRoot
} else {
    $here = Split-Path -Parent $MyInvocation.MyCommand.Definition
}
$setupScript = Join-Path $here 'setup-wsl.sh'
if (-not (Test-Path $setupScript)) {
    throw "Cannot find setup-wsl.sh next to this installer (looked in $here)."
}

# Pre-compute the WSL-side orchestrate path so we can print a clean
# stop command at the end.
$envParts = @(
    "STUDIO_REPO_URL='$RepoUrl'",
    "STUDIO_REPO_BRANCH='$RepoBranch'"
)
$wslLocalPath = ''
$stopPath = '/root/work/mmt-studio/orchestrate'
if ($LocalRepoPath -and $LocalRepoPath.Length -gt 0) {
    if (-not (Test-Path $LocalRepoPath)) {
        throw "LocalRepoPath not found: $LocalRepoPath"
    }
    $drive = $LocalRepoPath.Substring(0,1).ToLower()
    $tail  = $LocalRepoPath.Substring(2) -replace '\\','/'
    $wslLocalPath = "/mnt/$drive$tail"
    $envParts += "STUDIO_LOCAL_REPO='$wslLocalPath'"
    # If pointing at a standalone orchestrate checkout (has docker-compose.yml at root),
    # the stop path is that directory directly; if pointing at the umbrella mono root,
    # it's $wslLocalPath/orchestrate. setup-wsl.sh sorts this out; for the stop hint
    # we conservatively show the input path. Re-running setup-wsl.sh prints the actual
    # orchestrate path.
    $stopPath = $wslLocalPath
    Write-Ok "Using local checkout: $LocalRepoPath  -> $wslLocalPath"
}
if ($SkipBringUp) { $envParts += "STUDIO_SKIP_UP=1" }

# Pipe the bash setup through wsl, stripping any CRLF that a Windows
# git checkout (autocrlf=true) might have introduced. No repo edits.
$bashSrc = (Get-Content -Raw $setupScript) -replace "`r",""
$wrapper = ($envParts -join ' ') + ' bash'
$bashSrc | wsl -d $Distro -u root -- bash -lc $wrapper
if ($LASTEXITCODE -ne 0) {
    throw "setup-wsl.sh exited with code $LASTEXITCODE."
}

Write-Step "Done."
Write-Ok "core   -> http://localhost:5000"
Write-Ok "tester -> http://localhost:5001"
Write-Host ""
Write-Host "To stop the studio later:" -ForegroundColor Cyan
$stopCmd = "wsl -d $Distro -u root -- bash -lc `"cd '$stopPath' ; tr -d '\r' < stop_studio.sh | bash`""
Write-Host "    $stopCmd" -ForegroundColor Gray
