#!/bin/bash
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# run_traffic_engine.sh — start the SA Tester Traffic Agent on a DN/APN host.
#
# The traffic agent is tester-owned and runs independently of sa_core.
# Clone the full tester repo onto the DN box, then run this script.
# Everything — API, dashboard — lives on the same port (default 9100).
#
# Usage:
#   ./run_traffic_engine.sh                         # 0.0.0.0:9100
#   ./run_traffic_engine.sh --port 9200
#   ./run_traffic_engine.sh --bind 10.45.0.1        # bind only to DN iface
#
# Env overrides: SA_AGENT_BIND, SA_AGENT_PORT, SA_AGENT_LOG_LEVEL
#
# Dashboard: http://<this-host>:<port>/
# API docs:  http://<this-host>:<port>/docs

set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info() { echo -e "${GREEN}[+]${NC} $1"; }
warn() { echo -e "${YELLOW}[!]${NC} $1"; }
die()  { echo -e "${RED}[x]${NC} $1"; exit 1; }

BIND="${SA_AGENT_BIND:-0.0.0.0}"
PORT="${SA_AGENT_PORT:-9100}"
LOG_LEVEL="${SA_AGENT_LOG_LEVEL:-INFO}"

while [ $# -gt 0 ]; do
    case "$1" in
        --bind)      BIND="$2"; shift ;;
        --port)      PORT="$2"; shift ;;
        --log-level) LOG_LEVEL="$2"; shift ;;
        -h|--help)   sed -n '2,18p' "$0"; exit 0 ;;
        *) die "Unknown option: $1 (try --help)" ;;
    esac
    shift
done

# ── venv bootstrap ────────────────────────────────────────────────
venv_valid() {
    [ -x "$SCRIPT_DIR/.venv/bin/python" ] || return 1
    "$SCRIPT_DIR/.venv/bin/python" -c "import fastapi, uvicorn" >/dev/null 2>&1
}

if ! venv_valid; then
    if [ -x "$SCRIPT_DIR/install.sh" ]; then
        info "No usable .venv — running install.sh to bootstrap"
        "$SCRIPT_DIR/install.sh"
    else
        warn "install.sh missing — trying a minimal pip install for the agent only"
        python3 -m venv "$SCRIPT_DIR/.venv" || die "python3-venv not installed"
        "$SCRIPT_DIR/.venv/bin/pip" install --upgrade pip
        "$SCRIPT_DIR/.venv/bin/pip" install fastapi uvicorn \
            || die "pip install fastapi uvicorn failed"
    fi
fi

# ── runtime deps ──────────────────────────────────────────────────
command -v iperf3 >/dev/null 2>&1 || die \
    "iperf3 not installed. On Debian/Ubuntu: sudo apt-get install -y iperf3"

# ── run ───────────────────────────────────────────────────────────
unset SA_AGENT_TOKEN    # no auth — simpler, fine for lab setups
info "Starting traffic agent on ${BIND}:${PORT}  (log-level=${LOG_LEVEL})"
HOST_IP="$(hostname -I 2>/dev/null | awk '{print $1}')"
info "Dashboard: http://${HOST_IP:-localhost}:${PORT}/"
info "API docs:  http://${HOST_IP:-localhost}:${PORT}/docs"

exec "$SCRIPT_DIR/.venv/bin/python" -m src.traffic.agent_main \
    --bind "$BIND" --port "$PORT" --log-level "$LOG_LEVEL"
