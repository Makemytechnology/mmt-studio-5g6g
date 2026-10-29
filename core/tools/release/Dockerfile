# Copyright (c) 2026 MakeMyTechnology. Licensed under Apache-2.0.
#
# tools/release/Dockerfile — multi-stage build for mmt-studio-core.
#
# Stage 1 (builder): Ubuntu 24.04 with Go + DPDK build deps. Runs the
# canonical install.sh which produces sacore-web + libupf_dp.so. Tests
# are NOT stripped at this stage — install.sh runs `go test ./...` per
# module to confirm the build is clean.
#
# Stage 2 (runtime): Ubuntu 24.04 minimal. Copies only the runtime
# files (binary, .so, templates, static, run.sh + sysctl preset) from
# the builder. Tests / testdata / spec PDFs are left behind. Adds
# libnuma1 (DPDK runtime dep) and modprobe (for sctp module).
#
# Build:
#   docker build -t mmt-studio-core:<version> -f tools/release/Dockerfile .
#
# Run (privileged because of hugepages + SCTP module + raw sockets):
#   docker run --privileged -p 5000:5000 -p 38412:38412 -p 8805:8805/udp \
#       mmt-studio-core:<version>

# ────────────────────────────────────────────────────────────────────
# Stage 1a: dpdk-builder
#
# Builds DPDK 25.11 into libs/dpdk-25.11/build. This stage's cache key
# only depends on libs/dpdk-25.11/** + run.sh's DPDK build commands,
# so a normal Go-side code edit (nf/, webservice/, etc.) does NOT
# invalidate this 2–3 minute step. Without this split, every source
# change in the parent /src tree busted the unified COPY layer and
# install.sh fell into the "no DPDK_BUILD_MARKER → rebuild from scratch"
# branch (run.sh:335).
# ────────────────────────────────────────────────────────────────────
FROM ubuntu:24.04 AS dpdk-builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        gcc \
        make \
        meson \
        ninja-build \
        pkg-config \
        python3-pyelftools \
        libnuma-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /src
# Copy ONLY the DPDK tree (and run.sh, which carries the meson flags
# in ensure_install_dpdk so we can reuse it instead of duplicating).
COPY libs/dpdk-25.11 ./libs/dpdk-25.11
COPY run.sh ./
COPY install.sh ./

# Build DPDK with the SAME meson flags ensure_install_dpdk() uses in
# run.sh — otherwise the signature stamp at the bottom is a lie, and
# the next stage's install.sh would detect "wrong sig" and rebuild
# DPDK from scratch on every image build (the bug we hit). Keep these
# flags in lockstep with run.sh:ensure_install_dpdk (~line 358).
#
# Drivers enabled: net/tap (TAP PMD for upf_n3/upf_n6), net/ring
# (in-process ring PMD used by some unit tests), mempool/ring (the
# generic ring mempool, required regardless of NIC driver). disable
# everything else so we don't pay the build cost for drivers we never
# load. Tests/examples disabled — they pull in ~150 extra targets.
RUN bash -c 'set -e; SCRIPT_DIR=/src; \
    DPDK_DIR=/src/libs/dpdk-25.11; \
    DPDK_BUILD_SIG="v1-generic-tap-ring"; \
    cd "$DPDK_DIR" && \
    rm -rf build && \
    meson setup build \
        -Dplatform=generic \
        -Dcpu_instruction_set=generic \
        -Dmax_numa_nodes=1 \
        -Ddisable_drivers="*" \
        -Denable_drivers="net/tap,net/ring,mempool/ring" \
        -Dtests=false -Dexamples="" && \
    ninja -C build && \
    echo "$DPDK_BUILD_SIG" > build/.mmt-build-sig'

# ────────────────────────────────────────────────────────────────────
# Stage 1b: builder (Go binary + libupf_dp.so + tests)
# ────────────────────────────────────────────────────────────────────
FROM ubuntu:24.04 AS builder

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        ca-certificates \
        curl \
        git \
        golang-go \
        gcc \
        make \
        meson \
        ninja-build \
        pkg-config \
        python3-pyelftools \
        libnuma-dev \
    && rm -rf /var/lib/apt/lists/*

# Pin Go's compile + module caches to fixed paths so the BuildKit cache
# mounts below (--mount=type=cache,target=...) line up with what the
# toolchain actually reads/writes. Without this, golang-go on Ubuntu
# defaults to $HOME/.cache/go-build and $HOME/go/pkg/mod which would
# bypass our mounts.
ENV GOCACHE=/root/.cache/go-build
ENV GOMODCACHE=/go/pkg/mod

WORKDIR /src
COPY . .
# Overlay the prebuilt DPDK on top of the freshly-copied source. install.sh
# then sees DPDK_BUILD_MARKER (libs/dpdk-25.11/build/lib/librte_hash.so),
# skips the DPDK build, and proceeds straight to libupf_dp.so + sacore-web.
COPY --from=dpdk-builder /src/libs/dpdk-25.11/build ./libs/dpdk-25.11/build

# install.sh builds libupf_dp.so → sacore-web and runs the test sweep.
# Failure here aborts the image build, which is what we want.
#
# BuildKit cache mounts preserve the Go compile cache and module
# download cache across builds. The COPY . . above is still
# invalidated by any source edit (so this layer always re-runs), but
# inside the layer the Go toolchain reuses prior .a files for
# unchanged packages — warm Go rebuilds collapse from ~10 min to
# ~30-90 s. Mount paths must match the GOCACHE / GOMODCACHE env above.
RUN --mount=type=cache,target=/root/.cache/go-build \
    --mount=type=cache,target=/go/pkg/mod \
    ./install.sh

# ────────────────────────────────────────────────────────────────────
# Stage 2: runtime
# ────────────────────────────────────────────────────────────────────
FROM ubuntu:24.04

ENV DEBIAN_FRONTEND=noninteractive

RUN apt-get update && apt-get install -y --no-install-recommends \
        libnuma1 \
        kmod \
        iptables \
        iproute2 \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /opt/mmt-studio-core

COPY --from=builder /src/sacore-web                   ./
COPY --from=builder /src/run.sh                       ./
COPY --from=builder /src/webservice/templates         ./webservice/templates/
COPY --from=builder /src/webservice/static            ./webservice/static/
COPY --from=builder /src/nf/upf/dataplane/libupf_dp.so /usr/lib/

# DPDK shared libs needed by libupf_dp.so at runtime.
COPY --from=builder /src/libs/dpdk-25.11/build/lib/*.so* /usr/lib/

# DPDK driver plugins (mempool_ring, net_tap, …). The binary's EAL
# plugin loader has the build-path baked in, so we mirror it here.
RUN mkdir -p /opt/mmt-studio-core/libs/dpdk-25.11/build/drivers
COPY --from=builder /src/libs/dpdk-25.11/build/drivers/*.so* \
        /opt/mmt-studio-core/libs/dpdk-25.11/build/drivers/

# sysctl preset — operator must opt-in via `sysctl --system` on the host.
COPY --from=builder /src/scripts/sysctl/99-sacore.conf /usr/share/mmt-studio-core/

EXPOSE 5000 38412 8805/udp

# CAP_* equivalents need --privileged or explicit --cap-add at run time;
# this CMD assumes that. See README "Docker" section.
CMD ["./sacore-web", "--addr", ":5000", "--ngap-addr", ":38412"]
