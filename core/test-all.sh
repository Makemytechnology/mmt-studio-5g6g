#!/usr/bin/env bash
# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
#
# Runs every test across all three codec generators from the workspace root.
# Equivalent to `go test ./...` that go.work alone won't give you.
set -e
cd "$(dirname "$0")"
exec go test \
    github.com/mmt/nasgen/... \
    github.com/mmt/pfcpgen/... \
    github.com/mmt/asn1go/... \
    "$@"
