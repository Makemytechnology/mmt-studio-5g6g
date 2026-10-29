#!/usr/bin/env bash
# Compatibility shim — install.sh is now a sub-command of run.sh.
# Existing callers (Makefile `make install`, tools/release/Dockerfile,
# README quick-start) keep working unchanged.
exec "$(dirname "$0")/run.sh" install "$@"
