# Copyright (c) 2026 MakeMyTechnology. All rights reserved.
# src/cluster/ — Distributed test execution: controller + workers
#
# Architecture:
#   Controller (1) — orchestrates test runs, assigns gNBs, aggregates results
#   Workers (N)    — each simulates a range of gNBs + their UEs
#
# Modes:
#   standalone — single machine, no cluster (current behavior)
#   cluster    — controller + workers, distributed execution
