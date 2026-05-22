#!/bin/bash
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# SA Tester 5G — Run from source
# Usage:
#   ./run.sh [--host 0.0.0.0] [--port 5000]
#   ./run.sh --docker [up|down|logs]   # run satester service from the
#                                      # orchestrate repo's compose at
#                                      # ../mmt-studio-orchestrate/
#                                      # docker-compose.yml (single
#                                      # source of truth, bridge net)
#   ./run.sh --fresh --docker          # rebuild image with --no-cache, then run
#
# Phase 1 (as the invoking user): bootstrap. Runs install.sh if .venv/
# is missing or broken. These operations MUST run as the original user —
# anything touched under .venv/ otherwise becomes root-owned and breaks
# subsequent pip operations.
#
# Phase 2 (as root): runtime. TUN interfaces, GTP-U port 2152, SCTP,
# sysctl all need CAP_NET_ADMIN/RAW. The script re-execs under sudo at
# the end of phase 1.
#
# Docker mode: bypasses both phases — the venv is built inside the
# image and the container gets NET_ADMIN/NET_RAW from the compose
# file, so no host-side venv or sudo is needed.

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ════════════════════════════════════════════════════════════════════
#  --docker short-circuit (runs before venv bootstrap + sudo elevation)
# ════════════════════════════════════════════════════════════════════
#
# Examples:
#   ./run.sh --docker            # build + run in foreground
#   ./run.sh --docker up         # build + run detached
#   ./run.sh --docker logs       # tail container logs
#   ./run.sh --docker down       # stop + remove container
#   ./run.sh --fresh --docker    # rebuild image from scratch, then run
DOCKER=0
DOCKER_ACTION=""
FRESH=0
_passthrough=()
_expect_docker_action=0
for arg in "$@"; do
    if [ "$_expect_docker_action" = "1" ]; then
        _expect_docker_action=0
        case "$arg" in
            up|down|logs) DOCKER_ACTION="$arg"; continue ;;
        esac
    fi
    case "$arg" in
        --docker) DOCKER=1; _expect_docker_action=1 ;;
        --fresh)  FRESH=1 ;;
        *)        _passthrough+=("$arg") ;;
    esac
done

if [ "$DOCKER" -eq 1 ]; then
    # Single source of truth: the orchestrate repo's compose at
    # ../mmt-studio-orchestrate/docker-compose.yml (bridge net,
    # static IPs). This script brings up only the `satester`
    # service; for both core + tester at once use the orchestrate
    # repo's run_studio.sh.
    COMPOSE="$SCRIPT_DIR/../mmt-studio-orchestrate/docker-compose.yml"
    if [ ! -f "$COMPOSE" ]; then
        echo "[ERROR] orchestrate compose not found: $COMPOSE" >&2
        echo "[ERROR]   → expected ../mmt-studio-orchestrate/docker-compose.yml" >&2
        echo "[ERROR]   → clone Makemytechnology/mmt-studio-orchestrate alongside this repo" >&2
        echo "[ERROR]   → for native run instead, drop the --docker flag" >&2
        exit 2
    fi
    if ! command -v docker >/dev/null 2>&1; then
        echo "[ERROR] docker CLI not found — install Docker Engine >= 24" >&2
        exit 2
    fi
    if ! docker compose version >/dev/null 2>&1; then
        echo "[ERROR] 'docker compose' v2 not available — install docker-compose-plugin" >&2
        exit 2
    fi

    SVC="satester"
    # Best-effort: rename host veth via the orchestrate helper (uses
    # interactive sudo). Skipped if orchestrate repo is not present.
    RENAME_HELPER="$SCRIPT_DIR/../mmt-studio-orchestrate/run_studio.sh"
    _maybe_rename_veth() {
        if [ -x "$RENAME_HELPER" ]; then
            echo "[INFO] renaming host veth (sudo may prompt)..."
            "$RENAME_HELPER" rename-veths || echo "[WARN] veth rename skipped (non-fatal)"
        fi
    }
    case "$DOCKER_ACTION" in
        down)
            echo "[INFO] stopping + removing $SVC container..."
            exec docker compose -f "$COMPOSE" rm -sf "$SVC"
            ;;
        logs)
            echo "[INFO] tailing $SVC logs (Ctrl-C to detach)..."
            exec docker compose -f "$COMPOSE" logs -f "$SVC"
            ;;
        up)
            if [ "$FRESH" -eq 1 ]; then
                echo "[INFO] --fresh: rebuilding $SVC image with --no-cache"
                docker compose -f "$COMPOSE" build --no-cache "$SVC" || exit $?
            fi
            echo "[INFO] starting $SVC container (detached)..."
            docker compose -f "$COMPOSE" up --build -d "$SVC" || exit $?
            _maybe_rename_veth
            ;;
        ""|*)
            if [ "$FRESH" -eq 1 ]; then
                echo "[INFO] --fresh: rebuilding $SVC image with --no-cache"
                docker compose -f "$COMPOSE" build --no-cache "$SVC" || exit $?
            fi
            echo "[INFO] starting $SVC container (foreground; Ctrl-C to stop)..."
            exec docker compose -f "$COMPOSE" up --build "$SVC"
            ;;
    esac
fi

# Non-docker path: replace $@ with the filtered passthrough args so the
# venv-bootstrap and python invocation below don't see --docker / --fresh.
set -- "${_passthrough[@]}"

# ════════════════════════════════════════════════════════════════════
#  PHASE 1 — bootstrap as the invoking user
# ════════════════════════════════════════════════════════════════════

if [ "$EUID" -ne 0 ]; then
    # Validate the venv by importing the sentinels that most often
    # go missing (pydantic + pysctp). A bare .venv with just python
    # and no pip-installed deps fails the same way as a missing .venv.
    venv_is_valid() {
        [ -x "$SCRIPT_DIR/.venv/bin/python" ] || return 1
        "$SCRIPT_DIR/.venv/bin/python" -c "import pydantic, sctp, cryptography" \
            >/dev/null 2>&1
    }

    if ! venv_is_valid; then
        if [ -d "$SCRIPT_DIR/.venv" ]; then
            echo "[WARN] Stale or broken .venv/ detected — removing and rebuilding" >&2
            rm -rf "$SCRIPT_DIR/.venv"
        else
            echo "[INFO] No .venv detected — running install.sh to bootstrap the environment"
        fi
        if [ ! -x "$SCRIPT_DIR/install.sh" ]; then
            echo "[ERROR] install.sh not found at $SCRIPT_DIR/install.sh" >&2
            exit 1
        fi
        "$SCRIPT_DIR/install.sh" || {
            echo "[ERROR] install.sh failed — see the output above for the root cause." >&2
            echo "[ERROR] Fix the issue and rerun ./run.sh, or run ./install.sh manually." >&2
            exit 1
        }
        venv_is_valid || {
            echo "[ERROR] install.sh completed but the venv still cannot import pydantic/pysctp/cryptography." >&2
            echo "[ERROR] Inspect: $SCRIPT_DIR/.venv/bin/python -c 'import pydantic'" >&2
            exit 1
        }
    fi

    # Elevate to root for phase 2. sudo -E preserves env vars.
    echo "[INFO] Bootstrap complete. Elevating to root for runtime."
    exec sudo -E "$0" "$@"
fi

# ════════════════════════════════════════════════════════════════════
#  PHASE 2 — runtime as root
# ════════════════════════════════════════════════════════════════════

# .venv exists from phase 1. Use its python directly — never fall back
# to system python3, which lacks the compiled deps (pydantic-core,
# pysctp, cryptography) that phase 1 installed into the venv.
PYTHON="$SCRIPT_DIR/.venv/bin/python"
if [ ! -x "$PYTHON" ]; then
    echo "[ERROR] $PYTHON missing inside the privileged context." >&2
    echo "[ERROR] Phase 1 should have created it. Check filesystem permissions." >&2
    exit 1
fi

# iperf3 sanity check (traffic tests depend on it)
if ! iperf3 --version >/dev/null 2>&1; then
    echo "WARNING: iperf3 not available — traffic tests will fail. Run ./install.sh." >&2
fi

# PYTHONPATH: project root + the pure-Python libs kept in-tree
# (pycrate for NGAP/NAS codecs, sa_crypto for milenage/5G crypto).
export PYTHONPATH="$SCRIPT_DIR:$SCRIPT_DIR/libs:$SCRIPT_DIR/libs/pycrate:$SCRIPT_DIR/libs/sa_crypto:${PYTHONPATH:-}"

# Load SCTP kernel module (ignore failure — may already be loaded or built-in)
modprobe sctp 2>/dev/null || true

# Create log directory if not present
[ ! -d /var/log/sa_tester ] && mkdir -p /var/log/sa_tester

echo "SA Tester 5G starting... ($($PYTHON --version))"
# Run via `-m src.app` (not `python src/app.py`) so the module is registered
# in sys.modules as `src.app`. Otherwise it only lives under `__main__`, and
# any later `import src.app` (e.g. from base.py when fetching gtpu_manager)
# re-executes module-level code — re-running the banner, DB init, test
# registry scan, and robot-suite parser on every test trigger.
exec $PYTHON -m src.app "$@"
