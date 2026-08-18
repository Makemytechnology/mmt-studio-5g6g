#!/usr/bin/env bash
# Stop both core + tester containers and remove the mmtnet bridge.
# Thin alias for `./run_studio.sh down` — kept as a separate entry
# point so `stop_studio.sh` is discoverable next to `run_studio.sh`.
#
# Examples:
#   ./stop_studio.sh             # docker compose down (containers + network)
#   ./stop_studio.sh --volumes   # also remove named volumes
#   ./stop_studio.sh --rmi all   # also remove the built images

set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
exec "$SCRIPT_DIR/run_studio.sh" down "$@"
