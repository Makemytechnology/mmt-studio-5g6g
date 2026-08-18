#!/usr/bin/env bash
# Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later.
#
# install.sh — single-host installer for MMT Studio on Linux,
# VirtualBox guests (Ubuntu / Debian), and WSL2 Ubuntu.
#
# Usage:
#   curl -fsSL https://github.com/Makemytechnology/mmt-studio-5g6g/releases/latest/download/install.sh \
#     | sudo bash -s -- --role=both
#
# Flags:
#   --role={core|tester|both}   Deployment role. Default: both.
#                               core   → run sacore + satraffic only
#                               tester → run satester only (point at remote core)
#                               both   → single-host all-in-one (the default
#                                        run_studio.sh shape)
#   --core-url=URL              When --role=tester, the remote core's base
#                               URL (e.g. http://10.0.0.42:5000). Also pins
#                               AMF_IP/UPF_IP for tester's gNB sim.
#   --core-host=HOST            Shorthand: derives --core-url, AMF_IP, UPF_IP
#                               from a single host/IP, using the standard ports.
#   --version=vX.Y.Z            Used only to label the .env. Default: latest.
#   --install-dir=PATH          Where to drop compose file + .env.
#                               Default: /opt/mmt-studio.
#   --source-dir=PATH           Path to the mono source tree (must contain
#                               core/, tester/, orchestrate/ subdirs). If
#                               omitted, install.sh expects to be running
#                               from inside such a tree (e.g. orchestrate/
#                               in a fresh git clone of mmt-studio).
#   --skip-up                   Do everything except `docker compose up`.
#   --no-preflight              Skip host-mutation preflights (hugepages,
#                               sctp.ko). Useful when the operator's already
#                               provisioned those out of band.
#   -h, --help                  Show this help.
#
# Host requirements:
#   - Docker Engine 24+ with the Compose v2 plugin (or Docker Desktop on WSL2).
#   - kernel SCTP module (TS 38.412 §7 — NGAP transport).
#   - hugepages >= 512 × 2 MiB (DPDK EAL — TAP PMD on WSL2, real PMDs on metal).
#   - For --role=core: ports 5000/tcp, 38412/sctp, 2152/udp, 8805/udp reachable.
#   - For --role=tester: outbound reach to the core's IP on those same ports.

set -euo pipefail

# ── flag defaults ──────────────────────────────────────────────────
ROLE="both"
CORE_URL=""
CORE_HOST=""
VERSION="${MMT_STUDIO_VERSION:-latest}"
INSTALL_DIR="/opt/mmt-studio"
SOURCE_DIR=""
SKIP_UP=0
SKIP_PREFLIGHT=0
# Default-on per-test Wireshark watcher (mirrors install.ps1's
# behaviour on Windows -- operators expect Wireshark to "just
# work" after a fresh install without having to also run
# ./run_studio.sh manually). Set to 1 to opt out (--no-wireshark).
NO_WIRESHARK=0

# Where the installer script + compose + .env files live in this
# release tarball (or the upstream raw URL when invoked via curl).
ASSET_BASE="${MMT_STUDIO_ASSET_BASE:-https://github.com/Makemytechnology/mmt-studio-5g6g/releases/latest/download}"

# Allow the script's siblings (when extracted from the offline
# bundle) to override ASSET_BASE — that path is favoured when the
# files are colocated, falling back to ASSET_BASE for fetch.
SCRIPT_DIR="$(cd "$(dirname -- "${BASH_SOURCE[0]}")" >/dev/null 2>&1 && pwd)"

# ── arg parse ──────────────────────────────────────────────────────
usage() { sed -n '3,40p' "${BASH_SOURCE[0]}"; }
while [ $# -gt 0 ]; do
    case "$1" in
        --role=*)         ROLE="${1#*=}";;
        --role)           shift; ROLE="$1";;
        --core-url=*)     CORE_URL="${1#*=}";;
        --core-url)       shift; CORE_URL="$1";;
        --core-host=*)    CORE_HOST="${1#*=}";;
        --core-host)      shift; CORE_HOST="$1";;
        --version=*)      VERSION="${1#*=}";;
        --version)        shift; VERSION="$1";;
        --install-dir=*)  INSTALL_DIR="${1#*=}";;
        --install-dir)    shift; INSTALL_DIR="$1";;
        --source-dir=*)   SOURCE_DIR="${1#*=}";;
        --source-dir)     shift; SOURCE_DIR="$1";;
        --skip-up)        SKIP_UP=1;;
        --no-preflight)   SKIP_PREFLIGHT=1;;
        --no-wireshark)   NO_WIRESHARK=1;;
        -h|--help)        usage; exit 0;;
        *) echo "unknown flag: $1" >&2; usage; exit 2;;
    esac
    shift
done

case "$ROLE" in core|tester|both) ;; *)
    echo "--role must be core|tester|both (got: $ROLE)" >&2; exit 2;;
esac

# ── log helpers ────────────────────────────────────────────────────
if [ -t 1 ]; then
    CYAN=$'\033[36m'; GREEN=$'\033[32m'; YELLOW=$'\033[33m'; RED=$'\033[31m'; NORMAL=$'\033[0m'
else
    CYAN=; GREEN=; YELLOW=; RED=; NORMAL=
fi
# Phase counter so the long install steps surface a [N/M] prefix in
# the operator's terminal -- the parent install_on_windows.bat tails
# this output via install_log_tail.ps1, so a clear "step n of total"
# answers the recurring "how long is this taking?" question without
# the operator having to read tcpdump/docker buildkit output.
#
# Total is a static best-effort count of the _step calls in this
# script's main flow (preflights are slightly host-dependent so the
# number may be 1-2 off on hosts that skip e.g. hugepages, but it's
# accurate enough to convey "we're past the halfway mark").
_STEP_NUM=0
# Count for WSL2+role=both (the most common Windows install path):
# host-detect, preflight {docker, sctp, hugepages, apt}, locate, install-dir,
# env, pre-pull, build, start, summary == 12. Linux/native skips some
# preflights so the counter may stop short there -- still informative.
_STEP_TOTAL=12
_step() {
    _STEP_NUM=$((_STEP_NUM + 1))
    printf '%s==> [%d/%d] %s%s\n' "$CYAN" "$_STEP_NUM" "$_STEP_TOTAL" "$*" "$NORMAL"
}
# _substep is for nested progress (e.g. per-image pulls under
# "pre-pulling base images") that shouldn't bump the top-level
# phase counter.
_substep() { printf '%s    -> %s%s\n' "$CYAN" "$*" "$NORMAL"; }
_ok()   { printf '%s    %s%s\n' "$GREEN"  "$*" "$NORMAL"; }
_warn() { printf '%s    %s%s\n' "$YELLOW" "$*" "$NORMAL"; }
_die()  { printf '%s    %s%s\n' "$RED"    "$*" "$NORMAL" >&2; exit 1; }

# ── root check / self-elevate ──────────────────────────────────────
# Symmetry with install_on_windows.bat: the operator types ONE
# command (no leading `sudo`); if we aren't root yet, re-exec via
# sudo so the password prompt comes from us instead of forcing the
# operator to remember to prefix the command. `exec` replaces this
# shell so the install continues in the same terminal -- no double
# fork, no orphan child. `-E` preserves environment variables (e.g.
# MMT_STUDIO_VERSION, MMT_STUDIO_ASSET_BASE) across the elevation.
if [ "$(id -u)" -ne 0 ]; then
    if ! command -v sudo >/dev/null 2>&1; then
        _die "must run as root (sudo not installed; install sudo or re-run via 'su -c')"
    fi
    echo "Re-running with sudo (enter your password if prompted)..."
    exec sudo -E bash "$0" "$@"
fi

# ── host detection ─────────────────────────────────────────────────
HOST_KIND="linux"
if grep -qi microsoft /proc/version 2>/dev/null; then
    HOST_KIND="wsl2"
elif [ -d /sys/devices/virtual/dmi/id ] \
     && grep -qi -E 'virtualbox|innotek' /sys/class/dmi/id/sys_vendor 2>/dev/null; then
    HOST_KIND="vbox-guest"
fi
_step "host detected: $HOST_KIND  role: $ROLE  version: $VERSION"

# ── derive core URL / IPs ──────────────────────────────────────────
if [ -n "$CORE_HOST" ] && [ -z "$CORE_URL" ]; then
    CORE_URL="http://${CORE_HOST}:5000"
fi
CORE_IP=""
if [ -n "$CORE_URL" ]; then
    # http://host:port → host
    CORE_IP="$(echo "$CORE_URL" | sed -E 's#^[a-z]+://##; s#/.*##; s#:[0-9]+$##')"
fi
if [ "$ROLE" = "tester" ] && [ -z "$CORE_URL" ]; then
    _warn "--role=tester without --core-url/--core-host — defaulting to localhost"
    _warn "    set --core-url=http://<core-host>:5000 to point at a remote core"
    CORE_URL="http://127.0.0.1:5000"
    CORE_IP="127.0.0.1"
fi

# ── preflight: docker ──────────────────────────────────────────────
_step "preflight: docker"
command -v docker >/dev/null 2>&1 \
    || _die "docker not found. Install Docker Engine 24+ (https://docs.docker.com/engine/install/) and re-run."
DOCKER_VER="$(docker version --format '{{.Server.Version}}' 2>/dev/null || true)"
[ -n "$DOCKER_VER" ] || _die "docker daemon not reachable (start it: sudo systemctl start docker)"
_ok "docker $DOCKER_VER"
docker compose version >/dev/null 2>&1 \
    || _die "docker compose v2 plugin missing. Install with: apt-get install docker-compose-plugin"
_ok "docker compose: $(docker compose version --short 2>&1)"

# ── preflight: SCTP + hugepages + apt utilities ────────────────────
if [ "$SKIP_PREFLIGHT" = "0" ]; then
    _step "preflight: kernel SCTP module"
    if [ -e /proc/sys/net/sctp/sndbuf_policy ]; then
        _ok "sctp already available"
    else
        modprobe sctp 2>/dev/null || true
        [ -e /proc/sys/net/sctp/sndbuf_policy ] \
            || _warn "SCTP not loaded. NGAP (TS 38.412 §7) will fail until 'modprobe sctp' succeeds."
        if [ -e /proc/sys/net/sctp/sndbuf_policy ]; then
            mkdir -p /etc/modules-load.d
            echo sctp > /etc/modules-load.d/mmt-studio-sctp.conf
            _ok "sctp loaded; persisted via /etc/modules-load.d/mmt-studio-sctp.conf"
        fi
    fi

    if [ "$ROLE" != "tester" ]; then
        _step "preflight: hugepages (DPDK EAL — 2 MiB pool)"
        HP_NEED=1024
        HP_SYSFS=/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages

        # The DPDK EAL reads `/sys/kernel/mm/hugepages/hugepages-2048kB/nr_hugepages`
        # to learn the per-page-size pool. vm.nr_hugepages is the alias
        # for the default size, but on some kernels (and on WSL2) the
        # alias doesn't allocate 2 MiB pages until you also poke the
        # per-size sysfs node. Try sysctl first, then the direct sysfs
        # write, then verify by reading back the per-size node.
        [ -f "$HP_SYSFS" ] \
            || _die "no 2MB-hugepage sysfs node ($HP_SYSFS) — kernel lacks hugepage support; sacore DPDK init cannot proceed"

        HP_NOW="$(cat "$HP_SYSFS" 2>/dev/null || echo 0)"
        if [ "$HP_NOW" -lt "$HP_NEED" ]; then
            _warn "hugepages-2048kB=$HP_NOW (need >= $HP_NEED) — allocating"
            sysctl -w "vm.nr_hugepages=$HP_NEED" >/dev/null 2>&1 || true
            echo "$HP_NEED" > "$HP_SYSFS" 2>/dev/null || true
            HP_NOW="$(cat "$HP_SYSFS" 2>/dev/null || echo 0)"
            if [ "$HP_NOW" -lt "$HP_NEED" ]; then
                # Hard fail with host-kind-specific guidance — the
                # previous warn-and-continue path always left sacore
                # in a broken-DPDK state at first run.
                msg="hugepage allocation failed: vm.nr_hugepages=$HP_NOW, need >= $HP_NEED.
    Free RAM may be too low to back $HP_NEED × 2 MiB = $((HP_NEED * 2)) MiB.
    Manual fix:
      sudo sysctl -w vm.nr_hugepages=$HP_NEED
      echo $HP_NEED | sudo tee $HP_SYSFS
      cat $HP_SYSFS    # must read back as >= $HP_NEED"
                if [ "$HOST_KIND" = "wsl2" ]; then
                    msg="$msg
    WSL2: enable systemd in /etc/wsl.conf [boot] systemd=true, drop
    a /etc/sysctl.d/99-mmt-hugepages.conf with 'vm.nr_hugepages = $HP_NEED',
    then 'wsl --shutdown' on the Windows host and relaunch."
                fi
                _die "$msg"
            fi
            # Persist via sysctl.d only if /etc/sysctl.d is writable
            # (it is on bare metal + VBox + WSL2-with-systemd, isn't
            # on read-only-rootfs containers — but install.sh shouldn't
            # be running inside a container anyway).
            mkdir -p /etc/sysctl.d
            printf '# mmt-studio: persist hugepage allocation\nvm.nr_hugepages = %d\n' \
                "$HP_NEED" > /etc/sysctl.d/99-mmt-hugepages.conf 2>/dev/null || true
            _ok "hugepages-2048kB=$HP_NOW applied (persisted via /etc/sysctl.d/99-mmt-hugepages.conf)"
        else
            _ok "hugepages-2048kB=$HP_NOW"
        fi

        # Ensure hugetlbfs is mounted at /dev/hugepages on the host so
        # the bind-mount into sacore actually carries a usable fs. On
        # most distros systemd-hugetlbfs.service handles this; on
        # minimal setups (some VBox / WSL2 images) we mount on demand.
        if ! mountpoint -q /dev/hugepages 2>/dev/null; then
            mkdir -p /dev/hugepages
            if mount -t hugetlbfs nodev /dev/hugepages 2>/dev/null; then
                _ok "mounted hugetlbfs on /dev/hugepages"
            else
                _warn "could not mount hugetlbfs on /dev/hugepages — sacore may still"
                _warn "    work if the kernel's default hugepage pool is usable, but"
                _warn "    if DPDK EAL fails, mount manually: sudo mount -t hugetlbfs nodev /dev/hugepages"
            fi
        fi
    fi

    if command -v apt-get >/dev/null 2>&1; then
        _step "preflight: base utilities (apt)"
        NEEDED=(iproute2 util-linux ca-certificates procps)
        MISSING=()
        for pkg in "${NEEDED[@]}"; do
            dpkg -s "$pkg" >/dev/null 2>&1 || MISSING+=("$pkg")
        done
        if [ ${#MISSING[@]} -gt 0 ]; then
            DEBIAN_FRONTEND=noninteractive apt-get update -qq
            DEBIAN_FRONTEND=noninteractive apt-get install -y --no-install-recommends "${MISSING[@]}" >/dev/null
            _ok "installed: ${MISSING[*]}"
        else
            _ok "base utilities present"
        fi
    fi
fi

# ── locate source tree ─────────────────────────────────────────────
# The mono ships as one repo with three sibling subdirs (core/,
# tester/, orchestrate/). The docker-compose.yml in orchestrate/
# references sibling paths (../core, ../tester) as build contexts;
# `docker compose build` only works when those paths exist.
#
# Resolution order:
#   1. --source-dir flag (absolute path to mono root)
#   2. SCRIPT_DIR is orchestrate/ and its parent has core/ + tester/
#   3. SCRIPT_DIR has core/ + tester/ as siblings of itself (mono root)
_step "locating source tree"
ORCH_DIR=""
if [ -n "$SOURCE_DIR" ]; then
    [ -d "$SOURCE_DIR/orchestrate" ] && [ -d "$SOURCE_DIR/core" ] && [ -d "$SOURCE_DIR/tester" ] \
        || _die "--source-dir $SOURCE_DIR is not a mono checkout (need core/, tester/, orchestrate/)"
    ORCH_DIR="$SOURCE_DIR/orchestrate"
elif [ -d "$(dirname "$SCRIPT_DIR")/core" ] && [ -d "$(dirname "$SCRIPT_DIR")/tester" ]; then
    ORCH_DIR="$SCRIPT_DIR"
elif [ -d "$SCRIPT_DIR/core" ] && [ -d "$SCRIPT_DIR/tester" ] && [ -d "$SCRIPT_DIR/orchestrate" ]; then
    ORCH_DIR="$SCRIPT_DIR/orchestrate"
else
    _die "no mono source tree found. Clone first:
    git clone https://github.com/Makemytechnology/mmt-studio-5g6g.git
    cd mmt-studio-5g6g/orchestrate && sudo ./install.sh --role=$ROLE
Or pass --source-dir=/path/to/mono."
fi
_ok "source: $ORCH_DIR (siblings core/, tester/)"

# ── install dir ────────────────────────────────────────────────────
_step "install dir: $INSTALL_DIR"
mkdir -p "$INSTALL_DIR"

# ── stage compose + .env ───────────────────────────────────────────
# install.sh runs `docker compose` from $ORCH_DIR (where the source
# tree lives) so the build contexts (../core, ../tester) resolve.
# $INSTALL_DIR holds .env + symlinks for operator convenience.
ENV_FILE="$INSTALL_DIR/.env"

_step "writing $ENV_FILE"
{
    echo "# Generated by install.sh $(date -u +%Y-%m-%dT%H:%M:%SZ)"
    echo "# Re-running install.sh overwrites this file."
    echo
    echo "# Local image tags built from the $VERSION source tree at"
    echo "# $ORCH_DIR/.."
    echo "SACORE_IMAGE=mmt-studio-core:${VERSION}"
    echo "SATESTER_IMAGE=mmt-studio-tester:${VERSION}"
    if [ -n "$CORE_URL" ]; then
        echo
        echo "# --role=tester: wire satester at a remote sacore"
        echo "SACORE_BASE_URL=$CORE_URL"
        if [ -n "$CORE_IP" ]; then
            echo "AMF_IP=$CORE_IP"
            echo "UPF_IP=$CORE_IP"
        fi
    fi
} > "$ENV_FILE"

# Symlink the three source dirs INTO $INSTALL_DIR so the
# install-dir layout is a self-contained compose project. Without
# the per-dir symlinks, the existing docker-compose.yml symlink
# alone isn't enough: the compose file's relative build contexts
# (`context: ../core`, `context: ../tester`) are resolved by
# docker compose against the symlink's parent ($INSTALL_DIR), not
# against the symlink target. Customer on Windows hit this --
# `cd /opt/mmt-studio && docker compose up` died with
#   unable to prepare context: path "/opt/core" not found
# because /opt/core doesn't exist. With the symlinks below,
# `../core` from anywhere in /opt/mmt-studio (including
# /opt/mmt-studio/orchestrate) resolves to /opt/mmt-studio/core,
# which links back to the real source tree.
#
# `-n` (no-dereference) on the per-dir links: without it, a
# subsequent install run pointed at a different source tree would
# resolve the existing symlink and write INSIDE the previous
# target rather than re-pointing the symlink.
ln -sf  "$ORCH_DIR/docker-compose.yml" "$INSTALL_DIR/docker-compose.yml"
ln -sfn "$ORCH_DIR"                    "$INSTALL_DIR/orchestrate"
ln -sfn "$ORCH_DIR/../core"            "$INSTALL_DIR/core"
ln -sfn "$ORCH_DIR/../tester"          "$INSTALL_DIR/tester"
_ok "env + source symlinks staged under $INSTALL_DIR/ (orchestrate, core, tester)"

# Hand the .env into the orchestrate dir so `docker compose` from
# there picks up the image-tag overrides too.
cp "$ENV_FILE" "$ORCH_DIR/.env"

# ── build images ───────────────────────────────────────────────────
if [ "$SKIP_UP" = "1" ]; then
    _step "skipping build + bring-up (--skip-up)"
    _ok "to start later:"
    _ok "    cd $ORCH_DIR && docker compose --profile $ROLE up -d --build"
    exit 0
fi

_step "pre-pulling base images (works around BuildKit DNS-namespace quirks)"
# BuildKit's build sandbox runs in its own network namespace that does
# NOT inherit the docker daemon's `dns` setting (the 8.8.8.8/1.1.1.1
# entries install.ps1 writes to daemon.json). On Docker Desktop / WSL
# hosts where /etc/resolv.conf points at the WSL forwarder 10.255.255.254,
# BuildKit's "load metadata for docker.io/library/<base>" step times
# out with:
#   target satester: failed to solve: failed to fetch oauth token:
#   Post "https://auth.docker.io/token": dial tcp: lookup auth.docker.io
#   on 10.255.255.254:53: i/o timeout
# The daemon's OWN pull path uses the working daemon-level resolver
# (proven by the alpine pull probe install.ps1 runs at step 6), so a
# plain `docker pull` succeeds where BuildKit's metadata fetch fails.
# Once the base images are in the local image store, BuildKit's
# metadata resolver hits the local cache instead of going to docker.io.
#
# Extract `FROM <ref>` lines from every Dockerfile under core/ and
# tester/ so adding a new base image (or bumping a version) Just Works
# without touching this script.
_pull_bases() {
    local roots="$ORCH_DIR/../core $ORCH_DIR/../tester"
    local aliases seen img
    # Collect every `AS <alias>` target so we don't try to `docker pull`
    # multi-stage aliases (e.g. `FROM ubuntu:24.04 AS base` followed by
    # `FROM base AS runner` -- `base` is a stage, not an image, and
    # `docker pull base` predictably 404s on the registry).
    #
    # `tr '\n' ' '` is the load-bearing bit: sort -u returns newline-
    # separated tokens, but the case-glob below (`*" $img "*`) looks
    # for SPACE-delimited matches. Without the tr, `aliases` contains
    # `base\nrunner\n...` and the pattern never matches `base` (no
    # surrounding spaces -- the previous char is a newline, not a
    # space). Linux customer hit this: install.sh dutifully tried
    # `docker pull base`, got the 404, and emitted a confusing
    # "pull base failed" warning.
    aliases=$(
        find $roots -name 'Dockerfile' -type f 2>/dev/null \
            -exec grep -hiE '^[[:space:]]*FROM[[:space:]]+.+[[:space:]]+AS[[:space:]]+' {} + |
        awk '{print $NF}' | sort -u | tr '\n' ' '
    )
    seen=""
    while IFS= read -r img; do
        # Skip empty, variable-only refs ($ARG), and the special `scratch`.
        [ -n "$img" ] || continue
        case "$img" in
            scratch|*\$*) continue ;;
        esac
        # Skip multi-stage aliases (aliases is space-delimited -- see
        # the tr-converted sort above for why).
        case " $aliases " in *" $img "*) continue ;; esac
        # Dedup so we don't pull `ubuntu:24.04` three times.
        case " $seen " in *" $img "*) continue ;; esac
        seen="$seen $img"
        _substep "pull $img"
        docker pull "$img" || _warn "pull $img failed (build may still succeed if cached)"
    done < <(
        find $roots -name 'Dockerfile' -type f 2>/dev/null \
            -exec grep -hiE '^[[:space:]]*FROM[[:space:]]+' {} + |
        awk '{print $2}'
    )
}
_pull_bases

_step "building images (--profile $ROLE) — this can take ~10 min on first run"
# --progress=plain so each buildkit step prints a discrete line
# instead of the default fancy in-place TUI -- which renders as
# ANSI escape soup when stdout isn't a TTY (e.g. when this script
# is invoked from install.ps1 via wsl) and gives operators no
# sense of progress through the 8-stage Dockerfile.
( cd "$ORCH_DIR" && docker compose --profile "$ROLE" build --progress=plain )
_ok "images built"

# ── pre-flight cleanup ─────────────────────────────────────────────
# Re-running install.sh, or installing after the dev/source workflow
# (orchestrate/run_studio.sh, project=orchestrate) used the same host,
# can leave containers named sacore/satester/satraffic and the mmtnet
# bridge owned by a different compose project. `docker compose up`
# then fails with "container name … already in use". Force-remove
# anything not owned by THIS install's project before the up.
_install_force_remove_stale() {
    local proj_self svc proj
    proj_self="$(basename "$ORCH_DIR")"   # default compose project name
    for svc in sacore satester satraffic; do
        docker inspect "$svc" >/dev/null 2>&1 || continue
        proj=$(docker inspect -f \
            '{{index .Config.Labels "com.docker.compose.project"}}' \
            "$svc" 2>/dev/null) || proj=""
        if [ "$proj" != "$proj_self" ]; then
            _substep "removing stale container $svc (project=${proj:-none})"
            docker rm -f "$svc" >/dev/null 2>&1 || true
        fi
    done
    if docker network inspect mmtnet >/dev/null 2>&1; then
        proj=$(docker network inspect mmtnet -f \
            '{{index .Labels "com.docker.compose.project"}}' \
            2>/dev/null) || proj=""
        if [ "$proj" != "$proj_self" ]; then
            docker network rm mmtnet >/dev/null 2>&1 || true
        fi
    fi
}
_install_force_remove_stale

# ── bring up ───────────────────────────────────────────────────────
_step "starting stack (--profile $ROLE)"
if ! ( cd "$ORCH_DIR" && docker compose --profile "$ROLE" up -d ); then
    echo "" >&2
    _warn "docker compose up failed."
    _warn "  → if you saw 'container name … already in use' or 'network … exists', recover with:"
    _warn "       cd $ORCH_DIR && docker compose --profile $ROLE down --remove-orphans --volumes"
    _warn "       docker rm -f sacore satester satraffic 2>/dev/null"
    _warn "       docker network rm mmtnet 2>/dev/null"
    _warn "    then re-run install.sh, or run: $ORCH_DIR/run_studio.sh reset && $ORCH_DIR/run_studio.sh up"
    exit 1
fi

_step "summary"
case "$ROLE" in
    both)
        _ok "core   → http://localhost:5000"
        _ok "tester → http://localhost:5001"
        ;;
    core)
        _ok "core   → http://localhost:5000  (also reachable on this host's LAN IPs)"
        _ok "tester  →  install separately with: install.sh --role=tester --core-host=$(hostname -I | awk '{print $1}')"
        ;;
    tester)
        _ok "tester  → http://localhost:5001  (wired to $CORE_URL)"
        ;;
esac
# Operator-actionable next steps depend on whether install.sh is
# the user's primary interface (native Linux / VirtualBox) or just
# the inner half of install_on_windows.bat (WSL2). On WSL2 the
# parent install.ps1 prints Windows-friendly equivalents
# (run_studio.bat / wsl-wrapped commands) right after we return,
# and the cd-and-`&&` Linux commands below would NOT work if
# pasted into a Windows PowerShell terminal -- operators have
# tried, hit "&& is not a valid statement separator", and gotten
# confused. Skip them on WSL2 to avoid the trap.
if [ "$HOST_KIND" != "wsl2" ]; then
    _ok "logs:  cd $ORCH_DIR && docker compose --profile $ROLE logs -f"
    _ok "stop:  cd $ORCH_DIR && docker compose --profile $ROLE down"
fi

# ── auto-arm the per-test Wireshark watcher ───────────────────────
# Mirrors install.ps1's default-on behaviour on Windows. Without
# this, Linux operators ran the published install one-liner,
# clicked Run on a test in the satester UI, saw no Wireshark, and
# only after discovering `./run_studio.sh` was the missing second
# step did they get the per-test window.
#
# Guarded on a handful of preconditions so we don't waste time /
# spam errors where the watcher can't possibly work:
#   * --no-wireshark           operator explicitly opted out
#   * SKIP_UP                  stack isn't actually running; nothing to capture
#   * HOST_KIND=wsl2           install.ps1 (the Windows parent) arms its own watcher
#   * SUDO_USER unset          we have no real user to drop privs to (X auth would fail)
#   * DISPLAY unset            no X server reachable (headless / CI host)
#   * wireshark / curl missing  obvious
if [ "$NO_WIRESHARK" = "0" ] \
   && [ "$SKIP_UP" = "0" ] \
   && [ "$HOST_KIND" != "wsl2" ] \
   && [ -n "${SUDO_USER:-}" ] \
   && [ -n "${DISPLAY:-}" ] \
   && command -v wireshark >/dev/null 2>&1 \
   && command -v curl >/dev/null 2>&1; then
    _step "arming per-test Wireshark watcher"
    # Drop privs back to $SUDO_USER so wireshark reaches their X
    # display (root's $XAUTHORITY usually points nowhere useful).
    # `-E` preserves DISPLAY + XAUTHORITY across the sudo, `setsid
    # -f` puts the watcher in its own session so it survives this
    # script's exit, and `</dev/null >>log 2>&1` detaches stdio so
    # install.sh doesn't block on it. The watcher itself
    # backgrounds the polling loop and returns quickly, so the
    # whole call returns in <1 s.
    if setsid -f sudo -u "$SUDO_USER" -E "$ORCH_DIR/run_studio.sh" watcher \
            </dev/null >>/tmp/install-watcher-spawn.log 2>&1; then
        _ok "watcher armed -- run any test from http://localhost:5001 to see Wireshark open"
    else
        _warn "watcher spawn failed (see /tmp/install-watcher-spawn.log) -- run ./run_studio.sh watcher manually if you want Wireshark per test"
    fi
fi
