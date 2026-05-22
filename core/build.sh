#!/usr/bin/env bash
# Compatibility shim — build.sh is now a sub-command of run.sh.
# Inner-loop dev callers keep working unchanged.
exec "$(dirname "$0")/run.sh" build "$@"
