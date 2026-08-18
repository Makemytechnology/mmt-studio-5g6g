<!-- Copyright (c) 2026 MakeMyTechnology. Licensed under AGPL-3.0-or-later. -->

# Contributing to MMT Studio

Thanks for your interest in contributing. This project is licensed
under the GNU Affero General Public License v3.0 or later
(AGPL-3.0-or-later) — see [LICENSE](LICENSE) — and is also offered
under a separate commercial licence (see "Commercial licensing"
below).

Please read this short policy before opening a pull request.

## Developer Certificate of Origin (DCO)

Every commit must include a `Signed-off-by:` line with your real name
and a working email address. This is how you certify the
[Developer Certificate of Origin v1.1](https://developercertificate.org/)
— in short, that you have the right to submit the contribution under
the project's licence.

Add the sign-off automatically with:

```
git commit -s
```

A pull request whose commits are not signed off cannot be merged.

## Licence of contributions

By submitting a contribution to this repository, you agree that:

1. Your contribution is licensed under the GNU Affero General Public
   License v3.0 or later (AGPL-3.0-or-later) — the licence of this
   project — and you have the right to license it that way.

2. You also grant MakeMyTechnology a perpetual, worldwide,
   non-exclusive, royalty-free, irrevocable copyright and patent
   licence to use, modify, sub-license, and distribute your
   contribution under any other licence terms, including commercial
   licence terms offered by MakeMyTechnology to third parties.

This dual-licensing grant is what allows MMT Studio to be offered
both under AGPL-3.0 and under a separate commercial licence, without
requiring contributors to assign their copyright. It mirrors the
contribution policy used by Open5GS, GitLab CE, MongoDB, and other
single-vendor copyleft projects.

## Commercial licensing

If your use case is not compatible with AGPL-3.0 obligations — for
example, embedding the core in a closed-source appliance, or operating
it as a managed service without publishing modifications — a commercial
licence is available. Contact **info@makemytechnology.com** for terms.

## Pull-request guidelines

Branch from `main`, keep PRs small and focused on one concern. Match
the existing code style in each component. Spec citations must stay
grounded in the local PDFs under `core/specs/3gpp/`, `core/specs/ietf/`,
and `tester/specs/`. Quote `§` clauses verbatim; never paraphrase from
memory. TS/RFC-numbered TODOs at unimplemented call-sites are preferred
over silent stubs — the code is the audit trail.

### Core (`core/`)

- `cd core && go test ./...` — full unit-test pass.
- `cd core && go test ./nf/tools/speccheck/...` — must pass strict
  (no ungrounded § citations).
- `gofmt` and `go vet` clean.
- Native code (DPDK plugins, C shims) follows the conventions in
  `core/nf/upf/dataplane/`.

### Tester (`tester/`)

- `cd tester && .venv/bin/python -m pytest tests/` — Python unit and
  integration tests. Use the project's virtualenv: `pysctp` and
  several other dependencies live there, not in the system Python.
- Robot framework suites under `tester/robot/` (where applicable) —
  typically run via the project's `run.sh` harness.
- Python files follow the conventions in `tester/src/`; the optional
  Rust data plane under `tester/dp-rust/` follows `cargo fmt` /
  `cargo clippy`.
- Tester bugs should be fixed in the tester. Do not paper over a
  tester quirk by changing core behaviour.

### Orchestrate (`orchestrate/`)

- `docker compose config` must validate.
- `./run_studio.sh up` must bring the full stack to healthy on a
  clean host.
- Keep `docker-compose.yml` as the single source of truth for service
  layout; per-repo `run.sh --docker` wrappers delegate here.

## Reporting bugs

Open a GitHub issue with:

- Steps to reproduce.
- Expected vs. observed behaviour.
- Relevant log output (with care for any IMSI / SUPI / key material
  redacted).
- The exact tag or commit SHA you reproduced against.

## Reporting security issues

Please do **not** open public issues for security vulnerabilities.
Email **info@makemytechnology.com** with the details and we will
respond privately.
