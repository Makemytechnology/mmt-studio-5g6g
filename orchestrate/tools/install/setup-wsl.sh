#!/usr/bin/env bash
# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
#
# setup-wsl.sh — bootstrap a WSL2 Ubuntu distro to run MMT Studio.
#
# Run as root inside the distro. The Windows-side installer
# (tools/install/install-windows.ps1) calls this for you via:
#
#   wsl -d <distro> -u root -- bash -lc "<this script piped on stdin>"
#
# Idempotent: re-running re-checks each step and skips ones already done.
#
# What it does:
#   1. /etc/wsl.conf  → enable [boot] systemd=true (preserves other keys)
#   2. apt            → ensure ip, nsenter, git, curl, tcpdump are present
#   3. SCTP module    → modprobe + /etc/modules-load.d/sctp.conf
#   4. Hugepages      → /etc/sysctl.d/99-mmt-hugepages.conf (vm.nr_hugepages=512)
#   5. git config     → core.autocrlf=input globally (LF-clean future clones)
#   6. Repo           → clone umbrella mono into $STUDIO_TARGET_DIR
#                       (or skip if $STUDIO_LOCAL_REPO points at an existing tree)
#   7. Studio         → cd <repo>/orchestrate && ./run_studio.sh up
#                       (unless $STUDIO_SKIP_UP=1)
#
# Env knobs (all optional):
#   STUDIO_REPO_URL     — git URL of umbrella mono
#                         (default: https://github.com/Makemytechnology/mmt-studio-5g6g.git)
#   STUDIO_REPO_BRANCH  — branch to check out (default: main)
#   STUDIO_TARGET_DIR   — clone destination
#                         (default: /root/work/mmt-studio)
#   STUDIO_LOCAL_REPO   — absolute path inside WSL to an existing orchestrate
#                         checkout. If set, no clone happens and this dir is used
#                         as the orchestrate root for `run_studio.sh`.
#   STUDIO_SKIP_UP=1    — do everything except `./run_studio.sh up`

set -euo pipefail

: "${STUDIO_REPO_URL:=https://github.com/Makemytechnology/mmt-studio-5g6g.git}"
: "${STUDIO_REPO_BRANCH:=main}"
: "${STUDIO_TARGET_DIR:=/root/work/mmt-studio}"
: "${STUDIO_LOCAL_REPO:=}"
: "${STUDIO_SKIP_UP:=0}"

# ── helpers ────────────────────────────────────────────────────────
if [ -t 1 ]; then
    CYAN=$'\033[36m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; NORMAL=$'\033[0m'
else
    CYAN=; GREEN=; YELLOW=; RED=; NORMAL=
fi
_step() { printf '%s==> %s%s\n' "$CYAN"   "$*" "$NORMAL"; }
_ok()   { printf '%s    %s%s\n' "$GREEN"  "$*" "$NORMAL"; }
_warn() { printf '%s    %s%s\n' "$YELLOW" "$*" "$NORMAL"; }
_die()  { printf '%s    %s%s\n' "$RED"    "$*" "$NORMAL" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || _die "must run as root (use 'wsl -u root')"

grep -qi microsoft /proc/version 2>/dev/null \
    || _warn "kernel doesn't look like WSL2 — continuing anyway"

# ── 1. /etc/wsl.conf systemd ───────────────────────────────────────
_step "wsl.conf: ensure [boot] systemd=true (preserves other sections)"
python3 - <<'PY'
import configparser, os
p = '/etc/wsl.conf'
c = configparser.ConfigParser()
# ConfigParser lowercases section names — keep originals via optionxform.
c.optionxform = str
if os.path.exists(p):
    c.read(p)
changed = False
if not c.has_section('boot'):
    c.add_section('boot'); changed = True
if c.get('boot', 'systemd', fallback='').lower() != 'true':
    c.set('boot', 'systemd', 'true'); changed = True
if changed:
    with open(p, 'w') as f:
        c.write(f)
    print('updated', p)
else:
    print('already enabled')
PY

# ── 2. apt: ensure base utilities ──────────────────────────────────
_step "apt: ensure required host utilities are present"
NEEDED=(iproute2 util-linux git curl ca-certificates procps tcpdump)
MISSING=()
for pkg in "${NEEDED[@]}"; do
    dpkg -s "$pkg" >/dev/null 2>&1 || MISSING+=("$pkg")
done
if [ ${#MISSING[@]} -gt 0 ]; then
    _warn "installing: ${MISSING[*]}"
    DEBIAN_FRONTEND=noninteractive apt-get update -qq
    DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${MISSING[@]}" >/dev/null
    _ok "installed: ${MISSING[*]}"
else
    _ok "all required utilities already present"
fi

# ── 3. SCTP kernel module (load + persist) ─────────────────────────
_step "SCTP kernel module"
if ! lsmod | grep -q '^sctp\b'; then
    modprobe sctp
    _ok "modprobe sctp ok"
else
    _ok "sctp already loaded"
fi
MODFILE=/etc/modules-load.d/sctp.conf
if [ ! -f "$MODFILE" ] || ! grep -qx 'sctp' "$MODFILE"; then
    echo sctp > "$MODFILE"
    _ok "wrote $MODFILE (auto-load on next wsl --shutdown)"
else
    _ok "$MODFILE already configured"
fi
# Sanity-check the per-netns sysctls runc 1.2+ will touch.
test -e /proc/sys/net/sctp/sndbuf_policy \
    || _die "/proc/sys/net/sctp/ tree missing even after modprobe — kernel lacks SCTP support"

# ── 4. Hugepage sysctl drop-in (persistent) ────────────────────────
_step "hugepages: vm.nr_hugepages=512 drop-in"
HPFILE=/etc/sysctl.d/99-mmt-hugepages.conf
if [ ! -f "$HPFILE" ]; then
    cat > "$HPFILE" <<'EOF'
# mmt-studio: persist hugepage allocation across wsl --shutdown
vm.nr_hugepages = 512
EOF
    _ok "wrote $HPFILE"
else
    _ok "$HPFILE already present"
fi
# Apply right now too (don't rely on a relaunch).
sysctl -p "$HPFILE" >/dev/null || true

# ── 5. git config (LF on disk, no CRLF surprises) ──────────────────
_step "git: core.autocrlf=input (global)"
git config --global core.autocrlf input
git config --global init.defaultBranch main 2>/dev/null || true
_ok "git configured"

# ── 6. Repo: clone umbrella mono (or use $STUDIO_LOCAL_REPO) ───────
_step "repo"
if [ -n "$STUDIO_LOCAL_REPO" ]; then
    [ -d "$STUDIO_LOCAL_REPO" ] \
        || _die "STUDIO_LOCAL_REPO does not exist: $STUDIO_LOCAL_REPO"
    REPO_ROOT="$STUDIO_LOCAL_REPO"
    _ok "using existing checkout: $REPO_ROOT"
else
    if [ -d "$STUDIO_TARGET_DIR/.git" ]; then
        _ok "updating existing clone at $STUDIO_TARGET_DIR"
        git -C "$STUDIO_TARGET_DIR" fetch --depth 1 origin "$STUDIO_REPO_BRANCH"
        git -C "$STUDIO_TARGET_DIR" checkout "$STUDIO_REPO_BRANCH"
        git -C "$STUDIO_TARGET_DIR" reset --hard "origin/$STUDIO_REPO_BRANCH"
    else
        _warn "cloning $STUDIO_REPO_URL (branch $STUDIO_REPO_BRANCH) → $STUDIO_TARGET_DIR"
        mkdir -p "$(dirname "$STUDIO_TARGET_DIR")"
        git clone --depth 1 --branch "$STUDIO_REPO_BRANCH" "$STUDIO_REPO_URL" "$STUDIO_TARGET_DIR"
        _ok "cloned"
    fi
    REPO_ROOT="$STUDIO_TARGET_DIR"
fi

# Umbrella mono lays out sub-repos as `core/ tester/ orchestrate/`.
# A standalone orchestrate checkout has docker-compose.yml at its root.
if [ -d "$REPO_ROOT/orchestrate" ] && [ -f "$REPO_ROOT/orchestrate/docker-compose.yml" ]; then
    ORCH_DIR="$REPO_ROOT/orchestrate"
elif [ -f "$REPO_ROOT/docker-compose.yml" ]; then
    ORCH_DIR="$REPO_ROOT"
else
    _die "neither $REPO_ROOT/orchestrate/docker-compose.yml nor $REPO_ROOT/docker-compose.yml exists"
fi
_ok "orchestrate dir: $ORCH_DIR"

# Remember it for stop_studio.sh wrappers / next runs.
mkdir -p /var/lib/mmt-studio
printf '%s\n' "$ORCH_DIR" > /var/lib/mmt-studio/orchestrate-path

# ── 7. Bring the studio up ─────────────────────────────────────────
if [ "$STUDIO_SKIP_UP" = "1" ]; then
    _step "skipping bring-up (STUDIO_SKIP_UP=1)"
    _ok "to start later: cd $ORCH_DIR && ./run_studio.sh up"
    exit 0
fi

_step "bringing up the studio: $ORCH_DIR/run_studio.sh up"
cd "$ORCH_DIR"
# Pipe through tr -d '\r' to survive a CRLF checkout on a Windows mount.
# Harmless on a clean LF tree.
tr -d '\r' < ./run_studio.sh | bash -s -- up

_step "done"
_ok "core   → http://localhost:5000"
_ok "tester → http://localhost:5001"
_ok "stop:    tr -d '\\r' < $ORCH_DIR/stop_studio.sh | bash"
