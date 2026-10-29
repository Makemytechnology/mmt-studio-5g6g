#!/usr/bin/env bash
# MMT Studio Core (Go) — single entry point.
#
# Tier-1 (just works):
#     ./run.sh                 # set up + build + run, skipping what's done
#
# Tier-2 (advanced):
#     ./run.sh install [--fresh] [--skip-dpdk]
#     ./run.sh build   [--fresh] [--race] [--vet] [--skip-upf]
#     ./run.sh run     [--no-elevate] [--port=N] [--host=H] [--addr=H:P]
#     ./run.sh --fresh         # wipe everything + reinstall + rebuild + run
#     ./run.sh --help / --help-advanced

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

# ── Constants ─────────────────────────────────────────────────────────
GO_MIN="1.21"
GO_VER_INSTALL="1.23.8"
BINARY="$SCRIPT_DIR/sacore-web"
DPDK_DIR="$SCRIPT_DIR/libs/dpdk-25.11"
UPF_DIR="$SCRIPT_DIR/nf/upf/dataplane"
UPF_SO="$UPF_DIR/libupf_dp.so"
DPDK_BUILD_MARKER="$DPDK_DIR/build/lib/librte_hash.so"
# Build-config signature: if any of the meson options below change, bump this
# string and ensure_install_dpdk() will re-run meson from scratch instead of
# reusing a stale build tree.
DPDK_BUILD_SIG_FILE="$DPDK_DIR/build/.mmt-build-sig"
DPDK_BUILD_SIG="v1-generic-tap-ring"
PIDFILE="/run/sacore.pid"

# ── Shared CGO/DPDK environment (sourced helper) ─────────────────────
# shellcheck disable=SC1091
. "$SCRIPT_DIR/scripts/lib/cgo-env.sh"

# ── Output helpers ────────────────────────────────────────────────────
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RED=$'\033[1;31m'; C_YEL=$'\033[1;33m'; C_GRN=$'\033[1;32m'
    C_CYN=$'\033[1;36m'; C_DIM=$'\033[2m';    C_RST=$'\033[0m'
else
    C_RED=""; C_YEL=""; C_GRN=""; C_CYN=""; C_DIM=""; C_RST=""
fi
PHASE="run"
_ts()    { date +'%H:%M:%S:%3N'; }
_info()  { printf '%s %s[%s]%s %s\n'    "$(_ts)" "$C_CYN" "$PHASE" "$C_RST" "$*"; }
_ok()    { printf '%s %s[%s]%s %s\n'    "$(_ts)" "$C_GRN" "$PHASE" "$C_RST" "$*"; }
_warn()  { printf '%s %s[%s]%s %s\n'    "$(_ts)" "$C_YEL" "$PHASE" "$C_RST" "$*" >&2; }
_error() { printf '%s %s[%s]%s %s\n'    "$(_ts)" "$C_RED" "$PHASE" "$C_RST" "$*" >&2; }
_dim()   { printf '%s %s[%s]%s %s%s%s\n' "$(_ts)" "$C_CYN" "$PHASE" "$C_RST" "$C_DIM" "$*" "$C_RST"; }

_step_start() { STEP_START=$SECONDS; }
_step_end() {
    local label="$1"
    local e=$((SECONDS - ${STEP_START:-$SECONDS}))
    local mins=$((e/60)) secs=$((e%60))
    if [ "$mins" -gt 0 ]; then
        _info "${label} took ${mins}m ${secs}s"
    else
        _info "${label} took ${secs}s"
    fi
}

# Render ninja's "[N/M] action ..." step counter as a single overwriting
# line; non-progress lines (warnings, final messages) are discarded.
_ninja_progress() {
    local cur tot pct
    while IFS= read -r line; do
        if [[ "$line" =~ ^\[([0-9]+)/([0-9]+)\] ]]; then
            cur="${BASH_REMATCH[1]}"
            tot="${BASH_REMATCH[2]}"
            pct=$(( cur * 100 / tot ))
            printf '\r%s %s[%s]%s     building DPDK ... %3d%% (%d/%d)\033[K' \
                "$(_ts)" "$C_CYN" "$PHASE" "$C_RST" "$pct" "$cur" "$tot"
        fi
    done
    printf '\n'
}

# ── Help text (tiered) ────────────────────────────────────────────────
_help() {
    cat <<'EOF'
Usage: ./run.sh
       Set up everything (Go, DPDK, UPF, binary) and launch sacore-web.
       First run installs and builds. Subsequent runs skip what's done.

       Listening on http://localhost:5000 by default.

       Stop with Ctrl-C, or `pkill sacore-web`.

Advanced:
  ./run.sh install [--fresh] [--skip-dpdk]
  ./run.sh build   [--fresh] [--race] [--vet] [--skip-upf]
  ./run.sh run     [--no-elevate] [--port=N] [--host=H] [--addr=H:P]
  ./run.sh --fresh                       # wipe everything + reinstall + rebuild + run
  ./run.sh --docker [up|down|logs]       # run sacore + satraffic (core-side stack) from ../mmt-studio-orchestrate/docker-compose.yml

  See ./run.sh --help-advanced for full flag descriptions.
EOF
}

_help_advanced() {
    cat <<'EOF'
Advanced flags:

install:
  --fresh         wipe DPDK build dir + UPF .o/.so, then reinstall everything
  --skip-dpdk     skip DPDK + UPF (control-plane only — laptop dev path)

build:
  --fresh         wipe UPF .o/.so + sacore-web, then rebuild
  --race          add -race to go build (~2× slower binary)
  --vet           go vet ./... before build
  --skip-upf      skip UPF rebuild (Go-only inner loop)

run:
  --no-elevate    skip the root phase (hugepages, sysctl); useful in containers
  --port=N        listen port (default 5000)
  --host=H        listen host (default empty = all interfaces)
  --addr=H:P      full host:port override (overrides --host/--port)

docker:
  --docker        bring up the core-side stack (`sacore` + `satraffic`)
                  from the orchestrate compose at
                  ../mmt-studio-orchestrate/docker-compose.yml in the
                  foreground; Ctrl-C stops it. `satraffic` is the
                  Python traffic-agent slave (same image as the tester,
                  shares sacore's netns) so a remote tester can drive
                  UL/DL iperf3 sessions from the DN side. To bring up
                  both the core-side stack AND the tester orchestrator
                  at once, use the orchestrate repo's ./run_studio.sh.
  --docker up     same as --docker, but detached (-d)
  --docker down   stop + remove sacore + satraffic containers
  --docker logs   tail sacore + satraffic logs
  --fresh         (with --docker) pass --no-cache to docker compose build

  Examples:
      ./run.sh --docker            # build + run core-side stack in foreground
      ./run.sh --docker up         # build + run core-side stack detached
      ./run.sh --docker logs       # tail sacore + satraffic logs
      ./run.sh --docker down       # stop + remove sacore + satraffic
      ./run.sh --fresh --docker    # rebuild core-side images from scratch, then run

Default invocation (./run.sh, no sub-command) chains: install + build + run.
With --fresh, default also wipes DPDK build + UPF + sacore-web first.

Environment:
  SACORE_HUGEPAGE_COUNT   number of 2MB hugepages (default 512)
  SACORE_RCVBUF_MB        socket rcvbuf MB (default 32)
  SACORE_SNDBUF_MB        socket sndbuf MB (default 32)
  SACORE_BACKLOG          netdev_max_backlog (default 65536)
  SACORE_CPU_PERF=1       set cpufreq governor to "performance"
  NO_COLOR=1              disable colour output

Argument order: sub-command (if any) must be FIRST. Flags follow.
Wrong:  ./run.sh --race build
Right:  ./run.sh build --race
EOF
}

# ── Argument parsing ──────────────────────────────────────────────────
VERB="default"
FRESH=0
SKIP_DPDK=0
SKIP_UPF=0
RACE=0
VET=0
NO_ELEVATE=0
HOST=""
PORT=""
ADDR_OVERRIDE=""
DOCKER=0
DOCKER_ACTION=""    # "" → up (foreground); "up" → up -d; "down"; "logs"

case "${1:-}" in
    install|build|run) VERB="$1"; shift ;;
    -h|--help)         _help; exit 0 ;;
    --help-advanced)   _help_advanced; exit 0 ;;
    "")                : ;;     # default verb
    -*)                : ;;     # leading flag → default verb
    *)
        printf '[run] unknown sub-command: %s\n' "$1" >&2
        printf '[run]   → run `./run.sh --help` for usage\n' >&2
        exit 2
        ;;
esac

_expect_docker_action=0
for arg in "$@"; do
    if [ "$_expect_docker_action" = "1" ]; then
        _expect_docker_action=0
        case "$arg" in
            up|down|logs) DOCKER_ACTION="$arg"; continue ;;
        esac
    fi
    case "$arg" in
        --docker)        DOCKER=1; _expect_docker_action=1 ;;
        --fresh)         FRESH=1 ;;
        --skip-dpdk)     SKIP_DPDK=1 ;;
        --skip-upf)      SKIP_UPF=1 ;;
        --race)          RACE=1 ;;
        --vet)           VET=1 ;;
        --no-elevate)    NO_ELEVATE=1 ;;
        --port=*)        PORT="${arg#--port=}" ;;
        --host=*)        HOST="${arg#--host=}" ;;
        --addr=*)        ADDR_OVERRIDE="${arg#--addr=}" ;;
        -h|--help)       _help; exit 0 ;;
        --help-advanced) _help_advanced; exit 0 ;;
        install|build|run)
            printf '[run] sub-command "%s" must be the FIRST argument (got it after flags)\n' "$arg" >&2
            printf '[run]   → use `./run.sh %s ...`\n' "$arg" >&2
            exit 2
            ;;
        *)
            printf '[run] unknown flag: %s\n' "$arg" >&2
            printf '[run]   → run `./run.sh --help` for usage\n' >&2
            exit 2
            ;;
    esac
done

# Compose final ADDR. Order-independent: --addr= short-circuits both,
# otherwise --host + --port compose at the end (fixes the prior bug
# where `--host=H --port=N` silently dropped H, and the inverse).
if [ -n "$ADDR_OVERRIDE" ]; then
    ADDR="$ADDR_OVERRIDE"
else
    ADDR="${HOST}:${PORT:-5000}"
fi
NGAP_ADDR=":38412"

# ── Probes (cheap state checks; silent on success) ───────────────────

# Compare the running Go's MAJOR.MINOR against $GO_MIN. Returns 0 (ok)
# when the running Go meets or exceeds the floor; 1 otherwise.
_go_version_ok() {
    local v
    v=$(go version 2>/dev/null | grep -oE 'go[0-9]+\.[0-9]+' | head -1 | sed 's/^go//')
    [ -z "$v" ] && return 1
    local maj="${v%%.*}" min="${v#*.}"
    min="${min%%.*}"
    local mmaj="${GO_MIN%%.*}" mmin="${GO_MIN#*.}"
    if   [ "$maj" -gt "$mmaj" ]; then return 0
    elif [ "$maj" -eq "$mmaj" ] && [ "$min" -ge "$mmin" ]; then return 0
    else return 1
    fi
}

_upf_stale() {
    if find "$UPF_DIR/src" "$UPF_DIR/include" -newer "$UPF_SO" \
            \( -name '*.c' -o -name '*.h' \) -print -quit 2>/dev/null \
            | grep -q .; then
        return 0
    fi
    # Makefile edits (CFLAGS / -march / -mno-avx) must also force a rebuild.
    if [ "$UPF_DIR/Makefile" -nt "$UPF_SO" ]; then
        return 0
    fi
    return 1
}

_check_no_live_binary() {
    [ -f "$PIDFILE" ] || return 0
    local old
    old=$(cat "$PIDFILE" 2>/dev/null || true)
    if [ -n "$old" ] && kill -0 "$old" 2>/dev/null; then
        _error "refusing to wipe sacore-web while pid $old is running"
        _error "  → stop it first: sudo kill $old"
        exit 1
    fi
}

# ── ensure_install_* helpers ──────────────────────────────────────────

ensure_install_go() {
    if command -v go >/dev/null 2>&1 && _go_version_ok; then return 0; fi
    if [ -x /usr/local/go/bin/go ]; then
        export PATH="/usr/local/go/bin:$PATH"
        if _go_version_ok; then return 0; fi
    fi
    _info "Go ≥ $GO_MIN not found → downloading $GO_VER_INSTALL"
    local arch tar url
    arch=$(uname -m); arch="${arch/x86_64/amd64}"; arch="${arch/aarch64/arm64}"
    tar="go${GO_VER_INSTALL}.linux-${arch}.tar.gz"
    url="https://go.dev/dl/$tar"
    if command -v wget >/dev/null 2>&1; then
        wget -q -O "/tmp/$tar" "$url" || { _error "wget failed: $url"; exit 2; }
    elif command -v curl >/dev/null 2>&1; then
        curl -fsSL -o "/tmp/$tar" "$url" || { _error "curl failed: $url"; exit 2; }
    else
        _error "neither wget nor curl found — install Go ≥ $GO_MIN manually: https://go.dev/dl/"
        exit 2
    fi
    if [ -w /usr/local ]; then
        tar -C /usr/local -xzf "/tmp/$tar"
    elif command -v sudo >/dev/null 2>&1; then
        sudo tar -C /usr/local -xzf "/tmp/$tar"
    else
        _error "cannot write /usr/local and no sudo — extract Go manually"
        exit 2
    fi
    rm -f "/tmp/$tar"
    export PATH="/usr/local/go/bin:$PATH"
    if ! grep -q '/usr/local/go/bin' "$HOME/.bashrc" 2>/dev/null; then
        echo 'export PATH=$PATH:/usr/local/go/bin' >> "$HOME/.bashrc"
        _info "added /usr/local/go/bin to ~/.bashrc"
    fi
    if ! _go_version_ok; then
        _error "Go install verification failed (need ≥ $GO_MIN, got $(go version 2>&1))"
        exit 2
    fi
}

# Run apt only when something is missing. Caller passes the package list.
_ensure_apt_pkgs() {
    local missing=()
    for pkg in "$@"; do
        if ! dpkg -s "$pkg" >/dev/null 2>&1; then
            missing+=("$pkg")
        fi
    done
    if [ ${#missing[@]} -eq 0 ]; then return 0; fi
    _info "apt deps missing: ${missing[*]} → installing"
    if [ "$EUID" -eq 0 ]; then
        apt-get update -qq && apt-get install -y -qq "${missing[@]}"
    elif command -v sudo >/dev/null 2>&1 && (sudo -n true 2>/dev/null || sudo true); then
        sudo apt-get update -qq && sudo apt-get install -y -qq "${missing[@]}"
    else
        _error "need root to install: ${missing[*]}"
        _error "  → sudo apt-get install ${missing[*]}"
        exit 2
    fi
}

ensure_install_apt() {
    if [ "$SKIP_DPDK" -eq 1 ]; then
        _ensure_apt_pkgs build-essential
    else
        _ensure_apt_pkgs build-essential meson ninja-build python3-pyelftools libnuma-dev pkg-config
    fi
}

ensure_install_dpdk() {
    if [ -f "$DPDK_BUILD_MARKER" ] && [ -d "$DPDK_DIR/build/lib" ] \
       && [ -f "$DPDK_BUILD_SIG_FILE" ] \
       && [ "$(cat "$DPDK_BUILD_SIG_FILE" 2>/dev/null)" = "$DPDK_BUILD_SIG" ]; then
        return 0
    fi
    if [ -f "$DPDK_BUILD_MARKER" ]; then
        _info "DPDK build present but signature mismatch — rebuilding"
    fi
    _info "DPDK 25.11 not built → ./libs/dpdk-25.11 (this takes ~3 min)"
    _step_start
    (
        cd "$DPDK_DIR"
        rm -rf build
        meson setup build \
            -Dplatform=generic \
            -Dcpu_instruction_set=generic \
            -Dmax_numa_nodes=1 \
            -Ddisable_drivers='*' \
            -Denable_drivers='net/tap,net/ring,mempool/ring' \
            -Dtests=false -Dexamples='' 2>&1 | tail -5
        if [ -t 1 ]; then
            ninja -C build 2>&1 | _ninja_progress
        else
            ninja -C build 2>&1 | tail -3
        fi
    )
    _step_end "DPDK build"
    if [ ! -f "$DPDK_BUILD_MARKER" ]; then
        _error "DPDK build did not produce $DPDK_BUILD_MARKER"
        _error "  → see libs/dpdk-25.11/build/meson-logs/meson-log.txt"
        _error "  → try \`./run.sh install --fresh\` after fixing the issue"
        exit 4
    fi
    # Stamp the build signature so the next ensure_install_dpdk() call can
    # tell whether this tree matches the current meson options.
    echo "$DPDK_BUILD_SIG" > "$DPDK_BUILD_SIG_FILE"
    _ok "DPDK 25.11 built"
    # Re-source CGO env now that DPDK paths exist.
    # shellcheck disable=SC1091
    . "$SCRIPT_DIR/scripts/lib/cgo-env.sh"
}

# True when host-level setup (modprobe sctp, /etc/modules-load.d,
# /etc/sysctl.d) is meaningless or unsafe to attempt. Catches:
#   - Docker/Podman build containers (buildkit doesn't always create
#     /.dockerenv, so we don't rely on that signal)
#   - Runtime containers without kmod/sysctl tooling installed
#   - Any environment where /proc isn't writable
# The detector is intentionally conservative: ABSENCE of `lsmod` (kmod
# package), or non-writable /proc/sys, both mean we can't load kernel
# modules from here regardless of root/sudo. Host configuration is the
# concern of mmt-studio-orchestrate run_studio.sh (Docker path) or the
# operator's native run.sh invocation (bare metal) — both of those run
# OUTSIDE the container, where lsmod and /proc/sys *are* present.
_skip_host_setup() {
    # Belt + suspenders signals — match if any one is true.
    [ -f /.dockerenv ] && return 0
    [ -n "${container:-}" ] && return 0
    grep -qE '/(docker|lxc|kubepods|containerd|buildkit)' /proc/1/cgroup 2>/dev/null && return 0
    # The strongest practical signal: no `lsmod` on PATH means this
    # host has no module-loading tools, so skipping is correct anyway.
    command -v lsmod >/dev/null 2>&1 || return 0
    # /proc/sys read-only (`ro` in mountinfo) → can't sysctl --system.
    grep -qE ' /proc/sys [^ ]* ro,' /proc/self/mountinfo 2>/dev/null && return 0
    return 1
}

ensure_install_sctp() {
    if _skip_host_setup; then
        _info "container build — skipping SCTP host setup (kernel module + /etc/modules-load.d are host-side concerns)"
        return 0
    fi
    if ! lsmod | grep -q '^sctp '; then
        if [ "$EUID" -eq 0 ]; then
            modprobe sctp 2>/dev/null && _info "loaded SCTP kernel module" || \
                _warn "modprobe sctp failed — NGAP will use TCP fallback"
        elif command -v sudo >/dev/null 2>&1 && sudo -n modprobe sctp 2>/dev/null; then
            _info "loaded SCTP kernel module"
        else
            _warn "could not load sctp module (no root) — NGAP will use TCP fallback"
        fi
    fi
    local dst="/etc/modules-load.d/sacore-sctp.conf"
    if [ -f "$dst" ] && grep -q '^sctp$' "$dst" 2>/dev/null; then return 0; fi
    if [ "$EUID" -eq 0 ]; then
        printf 'sctp\n' > "$dst" 2>/dev/null && _info "installed $dst" || true
    elif command -v sudo >/dev/null 2>&1 && \
         printf 'sctp\n' | sudo -n tee "$dst" >/dev/null 2>&1; then
        _info "installed $dst"
    fi
}

ensure_install_sysctl() {
    if _skip_host_setup; then
        _info "container build — skipping sysctl host setup (/etc/sysctl.d is host-side; compose can apply at runtime via sysctls:)"
        return 0
    fi
    local src="$SCRIPT_DIR/scripts/sysctl/99-sacore.conf"
    local dst="/etc/sysctl.d/99-sacore.conf"
    [ -f "$src" ] || return 0
    if [ -f "$dst" ] && cmp -s "$src" "$dst"; then return 0; fi
    if [ "$EUID" -eq 0 ]; then
        cp "$src" "$dst" 2>/dev/null && \
            sysctl --system >/dev/null 2>&1 && \
            _info "installed $dst + sysctl --system applied" || true
    elif command -v sudo >/dev/null 2>&1 && sudo -n cp "$src" "$dst" 2>/dev/null; then
        sudo -n sysctl --system >/dev/null 2>&1 || true
        _info "installed $dst + sysctl --system applied"
    fi
}

# ── ensure_build_* helpers ────────────────────────────────────────────

ensure_build_upf() {
    if [ "$SKIP_DPDK" -eq 1 ]; then
        return 0
    fi
    if [ ! -f "$DPDK_BUILD_MARKER" ]; then
        _error "DPDK not built — run \`./run.sh install\` first"
        exit 4
    fi
    if [ -f "$UPF_SO" ] && ! _upf_stale; then
        return 0
    fi
    _info "UPF dataplane → $UPF_SO"
    _step_start
    local log="/tmp/build-upf.log"
    : >"$log"
    if ! make -C "$UPF_DIR" -j"$(nproc 2>/dev/null || echo 4)" >"$log" 2>&1; then
        _error "UPF build failed"
        _error "  → see $log (tail below):"
        tail -20 "$log" >&2
        exit 5
    fi
    _step_end "UPF build"
    # Re-source CGO env so CGO_LDFLAGS picks up the newly-built .so.
    # shellcheck disable=SC1091
    . "$SCRIPT_DIR/scripts/lib/cgo-env.sh"
}

ensure_build_go() {
    if [ "$VET" -eq 1 ]; then
        _info "go vet ./..."
        (cd webservice && go vet ./...) || { _error "go vet failed"; exit 3; }
    fi
    local flags=()
    [ "$RACE" -eq 1 ] && flags+=(-race)
    _step_start
    local log="/tmp/build-go.log"
    : >"$log"
    if ! (cd webservice && go build -v "${flags[@]}" -o "$BINARY" ./cmd/sacore-web) >"$log" 2>&1; then
        _error "go build failed"
        _error "  → see $log (tail below):"
        tail -20 "$log" >&2
        exit 3
    fi
    chmod +x "$BINARY" 2>/dev/null || true
    local total
    total=$(wc -l <"$log" 2>/dev/null | tr -d ' ' || echo 0)
    [ -z "$total" ] && total=0
    if [ "$total" -gt 0 ]; then
        local rec
        rec=$(grep -c '^github\.com/mmt/mmt-studio-core' "$log" 2>/dev/null || true)
        [ -z "$rec" ] && rec=0
        _info "go build: recompiled $rec mmt-studio-core + $((total - rec)) third-party packages"
        _step_end "Go build"
    fi
}

# ── Sub-command bodies ────────────────────────────────────────────────

# Run the install pipeline. With_artifacts=1 also builds UPF + Go binary
# so the `install` sub-command produces a runnable binary on its own
# (preserves the Dockerfile contract: `RUN ./install.sh` → sacore-web).
do_install() {
    local with_artifacts="${1:-0}"
    PHASE="install"
    if [ "$FRESH" -eq 1 ]; then
        _info "--fresh: wiping DPDK build + UPF artifacts"
        rm -rf "$DPDK_DIR/build"
        rm -f "$UPF_DIR"/*.o "$UPF_SO"
    fi
    ensure_install_go
    ensure_install_apt
    if [ "$SKIP_DPDK" -eq 0 ]; then
        ensure_install_dpdk
    fi
    ensure_install_sctp
    ensure_install_sysctl
    if [ "$with_artifacts" = "1" ]; then
        if [ "$SKIP_DPDK" -eq 0 ]; then
            ensure_build_upf
        fi
        ensure_build_go
    fi
    _ok "ok"
}

do_build() {
    PHASE="build"
    if [ "$FRESH" -eq 1 ]; then
        _check_no_live_binary
        _info "--fresh: wiping UPF artifacts + sacore-web"
        rm -f "$UPF_DIR"/*.o "$UPF_SO" "$BINARY"
    fi
    if [ "$SKIP_UPF" -eq 0 ]; then
        ensure_build_upf
    fi
    ensure_build_go
    _ok "ok"
}

do_run() {
    PHASE="run"
    if [ ! -x "$BINARY" ]; then
        _error "binary not found at $BINARY"
        _error "  → run \`./run.sh\` (chains install + build + run) or \`./run.sh build\`"
        exit 1
    fi

    if [ "$NO_ELEVATE" -eq 1 ] || [ "$EUID" -eq 0 ]; then
        do_run_phase2
        return
    fi

    if ! command -v sudo >/dev/null 2>&1; then
        _warn "sudo not found — running unprivileged (hugepages/sysctl will be skipped)"
        NO_ELEVATE=1
        do_run_phase2
        return
    fi

    _info "elevating to root for runtime tuning..."
    local sudo_args=("$0" "run")
    [ -n "$HOST" ]          && sudo_args+=("--host=$HOST")
    [ -n "$PORT" ]          && sudo_args+=("--port=$PORT")
    [ -n "$ADDR_OVERRIDE" ] && sudo_args+=("--addr=$ADDR_OVERRIDE")
    exec sudo -E "${sudo_args[@]}"
}

do_run_phase2() {
    _setup_runtime
    _info "starting on $ADDR (NGAP $NGAP_ADDR, PID $$)"
    exec "$BINARY" --addr "$ADDR" --ngap-addr "$NGAP_ADDR"
}

_setup_runtime() {
    if [ "$NO_ELEVATE" -eq 1 ] && [ "$EUID" -ne 0 ]; then
        _warn "no-elevate: skipping hugepages + sysctl tuning (running unprivileged)"
        # Still re-source CGO env in case .so paths are needed at LD time.
        # shellcheck disable=SC1091
        . "$SCRIPT_DIR/scripts/lib/cgo-env.sh"
        return
    fi

    # Single-instance guard.
    if [ -f "$PIDFILE" ]; then
        local old
        old=$(cat "$PIDFILE" 2>/dev/null || true)
        if [ -n "$old" ] && kill -0 "$old" 2>/dev/null; then
            _warn "stopping previous sacore (pid $old)..."
            kill "$old" 2>/dev/null || true
            sleep 2
            kill -0 "$old" 2>/dev/null && kill -9 "$old" 2>/dev/null || true
        fi
    fi
    echo $$ > "$PIDFILE" 2>/dev/null || true
    trap 'rm -f "$PIDFILE"' EXIT

    modprobe sctp 2>/dev/null || true

    local hp_count="${SACORE_HUGEPAGE_COUNT:-512}"
    local hp_dir=/sys/kernel/mm/hugepages/hugepages-2048kB
    if [ -d "$hp_dir" ]; then
        local cur
        cur=$(cat "$hp_dir/nr_hugepages" 2>/dev/null || echo 0)
        if [ "$cur" -lt "$hp_count" ]; then
            echo "$hp_count" > "$hp_dir/nr_hugepages" 2>/dev/null || true
        fi
        _info "hugepages: $(cat "$hp_dir/nr_hugepages" 2>/dev/null || echo 0) × 2MB"
    fi

    for thp in /sys/kernel/mm/transparent_hugepage/enabled \
               /sys/kernel/mm/transparent_hugepage/defrag; do
        [ -w "$thp" ] && echo never > "$thp" 2>/dev/null || true
    done

    local rcv_mb="${SACORE_RCVBUF_MB:-32}"
    local snd_mb="${SACORE_SNDBUF_MB:-32}"
    local backlog="${SACORE_BACKLOG:-65536}"
    local rcv_b=$((rcv_mb * 1024 * 1024))
    local snd_b=$((snd_mb * 1024 * 1024))
    sysctl -qw net.core.rmem_default="$rcv_b"  2>/dev/null || true
    sysctl -qw net.core.rmem_max="$rcv_b"      2>/dev/null || true
    sysctl -qw net.core.wmem_default="$snd_b"  2>/dev/null || true
    sysctl -qw net.core.wmem_max="$snd_b"      2>/dev/null || true
    sysctl -qw net.core.netdev_max_backlog="$backlog" 2>/dev/null || true
    _info "socket tuning: rcvbuf=${rcv_mb}M sndbuf=${snd_mb}M backlog=$backlog"

    if [ "${SACORE_CPU_PERF:-0}" = "1" ]; then
        for g in /sys/devices/system/cpu/cpu*/cpufreq/scaling_governor; do
            [ -w "$g" ] && echo performance > "$g" 2>/dev/null || true
        done
        _info "CPU governor: performance"
    fi

    # Re-source CGO env so LD_LIBRARY_PATH covers UPF + DPDK at exec time.
    # shellcheck disable=SC1091
    . "$SCRIPT_DIR/scripts/lib/cgo-env.sh"
}

# ── --docker short-circuit ────────────────────────────────────────────
# Bypass the native install/build/run path. Delegates to the
# orchestrate repo's compose file — the single source of truth for
# containerized dev (bridge net, static IPs). Brings up the core-side
# stack: `sacore` (Go core) + `satraffic` (traffic-agent slave that
# shares sacore's netns, same image as the tester). For the full
# stack incl. the tester orchestrator, use
# ../mmt-studio-orchestrate/run_studio.sh.
do_docker() {
    PHASE="docker"
    local compose="$SCRIPT_DIR/../mmt-studio-orchestrate/docker-compose.yml"
    if [ ! -f "$compose" ]; then
        _error "orchestrate compose not found: $compose"
        _error "  → expected ../mmt-studio-orchestrate/docker-compose.yml"
        _error "  → clone Makemytechnology/mmt-studio-orchestrate alongside this repo"
        _error "  → for native run instead, drop the --docker flag"
        exit 2
    fi
    if ! command -v docker >/dev/null 2>&1; then
        _error "docker CLI not found — install Docker Engine ≥ 24"
        exit 2
    fi
    if ! docker compose version >/dev/null 2>&1; then
        _error "'docker compose' v2 not available (got '$(docker --version 2>&1)')"
        _error "  → install the docker-compose-plugin package"
        exit 2
    fi

    local dc=(docker compose -f "$compose")
    # Core-side stack: the Go core + the traffic-agent slave that lives
    # in core's netns. `satraffic` uses `network_mode: service:sacore`
    # so compose orders it after sacore automatically.
    local svcs=(sacore satraffic)
    # Best-effort: rename the host veth to a legible name after up.
    # Defers to the orchestrate repo's run_studio.sh, which knows how
    # to map containers → veths and uses interactive sudo.
    local rename_helper="$SCRIPT_DIR/../mmt-studio-orchestrate/run_studio.sh"
    _maybe_rename_veth() {
        if [ -x "$rename_helper" ]; then
            _info "renaming host veth (sudo may prompt)..."
            "$rename_helper" rename-veths || _warn "veth rename skipped (non-fatal)"
        fi
    }
    case "$DOCKER_ACTION" in
        down)
            _info "stopping + removing core-side containers (${svcs[*]})..."
            exec "${dc[@]}" rm -sf "${svcs[@]}"
            ;;
        logs)
            _info "tailing core-side logs (${svcs[*]}, Ctrl-C to detach)..."
            exec "${dc[@]}" logs -f "${svcs[@]}"
            ;;
        up)
            if [ "$FRESH" -eq 1 ]; then
                _info "--fresh: rebuilding ${svcs[*]} images with --no-cache"
                "${dc[@]}" build --no-cache "${svcs[@]}" || exit $?
            fi
            _info "starting core-side stack (${svcs[*]}, detached)..."
            "${dc[@]}" up --build -d "${svcs[@]}" || exit $?
            _maybe_rename_veth
            ;;
        ""|*)
            if [ "$FRESH" -eq 1 ]; then
                _info "--fresh: rebuilding ${svcs[*]} images with --no-cache"
                "${dc[@]}" build --no-cache "${svcs[@]}" || exit $?
            fi
            _info "starting core-side stack (${svcs[*]}, foreground; Ctrl-C to stop)..."
            exec "${dc[@]}" up --build "${svcs[@]}"
            ;;
    esac
}

if [ "$DOCKER" -eq 1 ]; then
    do_docker
fi

# ── Dispatch ──────────────────────────────────────────────────────────
case "$VERB" in
    install)
        do_install 1
        ;;
    build)
        do_build
        ;;
    run)
        do_run
        ;;
    default)
        if [ "$FRESH" -eq 1 ]; then
            PHASE="run"
            _check_no_live_binary
            _info "--fresh: wiping DPDK build, UPF artifacts, and sacore-web"
            rm -rf "$DPDK_DIR/build"
            rm -f "$UPF_DIR"/*.o "$UPF_SO" "$BINARY"
            FRESH=0
        fi
        do_install 0
        do_build
        do_run
        ;;
esac
