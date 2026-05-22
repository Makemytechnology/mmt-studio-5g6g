#!/usr/bin/env bash
# MMT Studio — bring up core + tester together on this host.
#
# Wraps /home/bxb/work/mmt-studio-orchestrate/docker-compose.yml (bridge net, static IPs:
# core 172.30.0.10, tester 172.30.0.20). Same UX as each repo's
# ./run.sh --docker.
#
# Examples:
#   ./run_studio.sh                  # tear down any running stack +
#                                    # build + run detached, rename veths,
#                                    # tail logs (Ctrl-C detaches; stack
#                                    # keeps running — use stop_studio.sh)
#   ./run_studio.sh --docker         # same (--docker is the only mode here)
#   ./run_studio.sh up               # tear down + build + run detached
#                                    # (no log tail). Always starts from
#                                    # a clean state — no stale netns.
#   ./run_studio.sh --docker up      # same
#   ./run_studio.sh --role=core      # bring up sacore + satraffic only
#                                    # (skips satester; skips no host-side
#                                    # preflights since core needs DPDK)
#   ./run_studio.sh --role=tester    # bring up satester only (skips sacore
#                                    # + satraffic + hugepages preflight;
#                                    # point at a remote core via the
#                                    # tester web UI gNB profile)
#   ./run_studio.sh --role=both up   # same as default but explicit
#   ./run_studio.sh down             # stop + remove containers + bridge
#   ./run_studio.sh reset            # forceful cleanup when `down` is not
#                                    # enough — removes containers by name
#                                    # (including ones created by a previous
#                                    # install.sh that used a different
#                                    # compose project name), removes the
#                                    # mmtnet bridge, removes named volumes.
#                                    # Safe to run anytime; next `up` rebuilds.
#   ./run_studio.sh logs             # tail both containers' logs
#   ./run_studio.sh logs sacore      # tail one container's logs
#   ./run_studio.sh ps               # docker compose ps
#   ./run_studio.sh --fresh          # same as default but with a
#                                    # --no-cache image rebuild on top
#                                    # (use after Dockerfile / install.sh
#                                    # changes that need a clean image).
#   ./run_studio.sh --fresh up       # same, detached
#   ./run_studio.sh rename-veths     # re-rename host-side veths (sudo) to
#                                    # `sacore-veth` / `satester-veth` so
#                                    # ifconfig/tcpdump/ip-link show clearly
#                                    # which veth belongs to which container.
#                                    # `up` and the default action also start
#                                    # a background docker-events watcher
#                                    # that re-applies the rename on every
#                                    # container restart (each test in
#                                    # baseline/full pretest mode restarts
#                                    # sacore via reset_to_baseline). The
#                                    # watcher is killed by `down` / `stop`.
#   ./run_studio.sh --debug          # also launch host Wireshark inside
#                                    # sacore's netns (via nsenter, sudo).
#                                    # Sees PFCP (lo) + NGAP/GTP-U (eth0)
#                                    # + upfgtp TUN — all in one capture.
#                                    # Pick interface "any" in Wireshark.
#                                    # Closes when you exit Wireshark.
#   ./run_studio.sh --debug up       # same, but no log tail
#   ./run_studio.sh wireshark        # bring up both capture windows:
#                                    #   1. mmtnet0 bridge (NGAP/SIP/PFCP-on-
#                                    #      bridge) — display filter
#                                    #      ngap || sip || pfcp
#                                    #   2. sacore loopback (PFCP SMF↔UPF on
#                                    #      127.0.0.1:8805/8806) — auto-
#                                    #      relaunches on every sacore
#                                    #      restart (each test in baseline/
#                                    #      full pretest mode triggers one)
#                                    # Both killed by `down`/`stop`.
#   per-test Wireshark watcher       # DEFAULT-ON. Spawns a background
#                                    # poller against http://localhost:5001
#                                    # /api/tests; the moment a new
#                                    # test_name flips to status=RUNNING
#                                    # (test triggered from the tester GUI
#                                    # / API), the prior Wireshark window
#                                    # is killed and a fresh one opens on
#                                    # mmtnet0. Window stays open after
#                                    # the test completes — only the NEXT
#                                    # test-start rotates it. Killed by
#                                    # `down`/`stop`. Symmetric with the
#                                    # Windows run_studio.bat default.
#   ./run_studio.sh --no-wireshark   # opt OUT of the watcher (headless
#                                    # / CI / no X-display hosts).
#   ./run_studio.sh --wireshark      # explicit opt-in (no-op while the
#                                    # default is on; kept for old docs).
#
# After bring-up, web UIs:
#   core:   http://localhost:5000
#   tester: http://localhost:5001
#
# In the tester UI, set the gNB profile to:
#   amf_ip=172.30.0.10  amf_port=38412
#   upf_ip=172.30.0.10  gtpu_port=2152

set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
COMPOSE="$SCRIPT_DIR/docker-compose.yml"

# ── Output helpers ───────────────────────────────────────────────────
if [ -t 1 ] && [ -z "${NO_COLOR:-}" ]; then
    C_RED=$'\033[1;31m'; C_YEL=$'\033[1;33m'; C_GRN=$'\033[1;32m'
    C_CYN=$'\033[1;36m'; C_RST=$'\033[0m'
else
    C_RED=""; C_YEL=""; C_GRN=""; C_CYN=""; C_RST=""
fi
_ts()    { date +'%H:%M:%S'; }
_info()  { printf '%s %s[studio]%s %s\n'  "$(_ts)" "$C_CYN" "$C_RST" "$*"; }
_ok()    { printf '%s %s[studio]%s %s\n'  "$(_ts)" "$C_GRN" "$C_RST" "$*"; }
_warn()  { printf '%s %s[studio]%s %s\n'  "$(_ts)" "$C_YEL" "$C_RST" "$*" >&2; }
_error() { printf '%s %s[studio]%s %s\n'  "$(_ts)" "$C_RED" "$C_RST" "$*" >&2; }

_help() {
    sed -n '2,/^set -euo/p' "$0" | sed -n '/^#/p' | sed 's/^# \?//'
    exit 0
}

# ── Preflight ────────────────────────────────────────────────────────
if [ ! -f "$COMPOSE" ]; then
    _error "compose file not found: $COMPOSE"
    exit 2
fi
if ! command -v docker >/dev/null 2>&1; then
    _error "docker CLI not found — install Docker Engine >= 24"
    exit 2
fi
if ! docker compose version >/dev/null 2>&1; then
    _error "'docker compose' v2 not available — install docker-compose-plugin"
    exit 2
fi

# ── Host arch detection + per-arch defaults ──────────────────────────
# Docker/BuildKit already resolves multi-arch base images to the host's
# native arch when compose sets no `platform:`, so the same compose
# file builds on x86_64 and aarch64 (Raspberry Pi 4/5, Apple Silicon)
# unchanged. What still needs platform awareness is the runtime
# preflight: Pi has 4–8 GB total RAM, so a default of 512 × 2 MB
# hugepages (1 GB) is a big chunk; halve it. Pi onboard NICs have no
# DPDK PMD, so the GUI's `mode=pmd` only works against `net_tap` and
# isn't faster than `mode=socket` — flag once at bring-up so operators
# don't chase it.
HOST_ARCH=$(uname -m)
case "$HOST_ARCH" in
    aarch64|arm64)
        HOST_ARCH_LABEL="arm64 (e.g. Raspberry Pi 4/5, Apple Silicon)"
        # Halve the default for memory-constrained Pi hosts; operators
        # can still override via SACORE_HUGEPAGE_COUNT env.
        : "${SACORE_HUGEPAGE_COUNT:=128}"
        IS_PI_LIKE=1
        ;;
    x86_64|amd64)
        HOST_ARCH_LABEL="x86_64"
        # 1024 × 2 MiB = 2 GiB locked. Matches install.sh's target
        # so the dev (run_studio.sh) and customer (install.sh) paths
        # don't diverge on memory provisioning. UPF + TAP rings need
        # north of 512 in practice when both core + tester gNB sims
        # share the host.
        : "${SACORE_HUGEPAGE_COUNT:=1024}"
        IS_PI_LIKE=0
        ;;
    *)
        HOST_ARCH_LABEL="$HOST_ARCH (untested)"
        : "${SACORE_HUGEPAGE_COUNT:=1024}"
        IS_PI_LIKE=0
        ;;
esac
# Always export so the value is visible to docker compose (env is the
# only way to push values into the container env block) and to any
# child process that re-reads it. Required because `set -u` later
# expansions (banner, _ensure_hugepages) trip on truly unset vars.
export SACORE_HUGEPAGE_COUNT

# ── Argument parsing ─────────────────────────────────────────────────
ACTION=""           # "" → up (foreground); up|down|logs|ps|status|restart
FRESH=0
DEBUG=0             # --debug → enable `debug` compose profile (sawireshark)
# Per-test Wireshark watcher: ON by default to match the Windows
# run_studio.bat behaviour. Operators on a headless / CI host pass
# `--no-wireshark` to opt out. `--wireshark` is still accepted (as a
# no-op when default is already on) so older docs / muscle memory
# don't break.
WIRESHARK_FLAG=1
ROLE="both"         # core|tester|both — selects the compose profile and
                    # gates DPDK-only preflights (hugepages, PFCP feed)
EXTRA=()            # passed through to `docker compose <action>`

# Accept --docker as a no-op alias (kept for symmetry with the per-repo
# run.sh scripts; this script is docker-only at the workspace level).
for arg in "$@"; do
    case "$arg" in
        --docker)         ;;            # alias / no-op
        --fresh)          FRESH=1 ;;
        --debug)          DEBUG=1 ;;
        --wireshark)      WIRESHARK_FLAG=1 ;;
        --no-wireshark)   WIRESHARK_FLAG=0 ;;
        --role=*)         ROLE="${arg#--role=}" ;;
        -h|--help|help)   _help ;;
        up|down|logs|ps|status|restart|build|pull|stop|start|rename-veths|wireshark|watcher|reset)
            if [ -z "$ACTION" ]; then
                ACTION="$arg"
            else
                EXTRA+=("$arg")
            fi
            ;;
        *)                EXTRA+=("$arg") ;;
    esac
done

case "$ROLE" in
    core|tester|both) ;;
    *) _error "invalid --role=$ROLE (use core|tester|both)"; exit 2 ;;
esac

# `status` is a friendlier alias for `ps`.
[ "$ACTION" = "status" ] && ACTION="ps"

# docker-compose.yml puts every service behind a profile so a
# customer can install just core or just tester on a given host.
# --role= (default both) selects the profile; every up/down/logs/ps
# below inherits it. Without this, `compose up` errors with "no
# service selected".
DC=(docker compose -f "$COMPOSE" --profile "$ROLE")

# Compose project name this script uses. The script always runs from
# orchestrate/, so docker compose defaults to project=orchestrate. We
# stash it explicitly for the cross-project cleanup below.
DC_PROJECT="orchestrate"

# ── Cross-project cleanup ────────────────────────────────────────────
# Two paths bring this stack up:
#   1. dev / source — orchestrate/run_studio.sh, project=orchestrate
#   2. customer install — install.sh stages /opt/mmt-studio/ and runs
#      `docker compose up` from there, project=mmt-studio (or whatever
#      dir basename is).
# `docker compose down` only removes containers/networks owned by the
# CURRENT project. Anything left by the other path survives and breaks
# the next `compose up` with:
#   Conflict. The container name "/sacore" is already in use
# Same story for the mmtnet bridge ("network with name mmtnet exists
# but was not created for project X").
#
# This helper removes any well-known container / network that exists
# but is NOT labelled with our project. Safe / no-op when nothing
# matches; never touches our own resources.
_force_remove_stale() {
    local svc proj
    for svc in sacore satester satraffic; do
        docker inspect "$svc" >/dev/null 2>&1 || continue
        proj=$(docker inspect -f \
            '{{index .Config.Labels "com.docker.compose.project"}}' \
            "$svc" 2>/dev/null) || proj=""
        if [ "$proj" != "$DC_PROJECT" ]; then
            _info "removing stale container $svc (project=${proj:-none})"
            docker rm -f "$svc" >/dev/null 2>&1 || true
        fi
    done
    if docker network inspect mmtnet >/dev/null 2>&1; then
        proj=$(docker network inspect mmtnet -f \
            '{{index .Labels "com.docker.compose.project"}}' \
            2>/dev/null) || proj=""
        if [ "$proj" != "$DC_PROJECT" ]; then
            if docker network rm mmtnet >/dev/null 2>&1; then
                _info "removed stale network mmtnet (project=${proj:-none})"
            else
                _warn "mmtnet bridge still in use by another container — run \`./run_studio.sh reset\`"
            fi
        fi
    fi
}

# One-line description of what `up` is about to bring up, given $ROLE.
# Used by the dispatch action messages so "starting core + tester" doesn't
# lie when --role=core or --role=tester.
_role_phrase() {
    case "$ROLE" in
        both)   echo "core + tester" ;;
        core)   echo "core (sacore + satraffic)" ;;
        tester) echo "tester (satester)" ;;
    esac
}

# Role-aware URL summary. Only prints lines for services the role
# actually brings up. Suffix is appended to the URL line (host veth
# notes differ between the up-action and default-action summaries).
_print_summary_urls() {
    local suffix="$1"
    if [ "$ROLE" = "both" ] || [ "$ROLE" = "core" ]; then
        _ok "core      → http://localhost:5000   (172.30.0.10$suffix, host veth: sacore-veth)"
        _ok "satraffic → traffic-agent slave on 172.30.0.10:9100 (shares sacore netns; same image as tester)"
    fi
    if [ "$ROLE" = "both" ] || [ "$ROLE" = "tester" ]; then
        _ok "tester    → http://localhost:5001   (172.30.0.20$suffix, host veth: satester-veth)"
    fi
}

# Wrap `docker compose up` so a name-conflict / network failure produces
# an actionable hint instead of a raw docker error.
_compose_up_or_hint() {
    if "${DC[@]}" up --build -d "$@"; then
        return 0
    fi
    _error "docker compose up failed — see output above"
    _error "  → if you see 'container name … already in use' or 'network … exists but was"
    _error "    not created for project …', the stack was previously started under a"
    _error "    different compose project (e.g. by install.sh). Recover with:"
    _error "       ./run_studio.sh reset"
    _error "    then re-run ./run_studio.sh up"
    exit 1
}

# ── Host preflight: hugepages for DPDK ───────────────────────────────
# sacore's UPF uses DPDK; rte_eal_init needs 2 MB hugepages allocated
# on the host. The container only mmaps /dev/hugepages — it can't
# adjust nr_hugepages itself (sysfs is read-only inside the netns'd
# container, and that knob is host-kernel state regardless). So if
# the host has 0 hugepages, DPDK fails (`rte_eal_init failed: -1`)
# and the UPF falls back to "pure-Go mode" — control plane works,
# data plane has nothing installed, every TC-TRF-* run reports
# 0 Mbps because PFCP-installed PDR/FAR/QER never reach a real DP.
#
# Mirrors mmt-studio-core-go/run.sh:560+ (native path) so the
# orchestrate path is self-contained: same env var (SACORE_HUGEPAGE_COUNT,
# default 512), same idempotent "only write if current < target".
_ensure_hugepages() {
    local hp_count="${SACORE_HUGEPAGE_COUNT:-1024}"
    local hp_dir=/sys/kernel/mm/hugepages/hugepages-2048kB
    if [ ! -d "$hp_dir" ]; then
        _error "hugepages dir $hp_dir not found — kernel lacks 2MB hugepage support"
        _error "    DPDK won't init; aborting. Reboot into a kernel with hugepage support."
        exit 1
    fi
    local cur
    cur=$(cat "$hp_dir/nr_hugepages" 2>/dev/null || echo 0)
    if [ "$cur" -ge "$hp_count" ]; then
        _info "hugepages OK: $cur × 2MB (target $hp_count)"
        # Ensure hugetlbfs is actually mounted on /dev/hugepages — the
        # compose file bind-mounts that path into sacore, and without
        # a hugetlbfs there the bind carries nothing usable.
        if ! mountpoint -q /dev/hugepages 2>/dev/null && _ensure_sudo; then
            sudo mkdir -p /dev/hugepages
            sudo mount -t hugetlbfs nodev /dev/hugepages 2>/dev/null || true
        fi
        return 0
    fi
    if ! _ensure_sudo; then
        _error "hugepages: have $cur, need $hp_count, but no sudo to allocate"
        _error "    fix: sudo sysctl -w vm.nr_hugepages=$hp_count"
        _error "         echo $hp_count | sudo tee $hp_dir/nr_hugepages"
        exit 1
    fi
    # Try both the sysctl alias and the direct per-size sysfs write.
    # Some kernels (WSL2 in particular) honour only one of the two.
    sudo sysctl -w "vm.nr_hugepages=$hp_count" >/dev/null 2>&1 || true
    echo "$hp_count" | sudo tee "$hp_dir/nr_hugepages" >/dev/null 2>&1 || true
    cur=$(cat "$hp_dir/nr_hugepages" 2>/dev/null || echo 0)
    if [ "$cur" -ge "$hp_count" ]; then
        _ok "hugepages: allocated $cur × 2MB"
        # Persist via sysctl.d so the value survives reboot.
        echo "vm.nr_hugepages = $hp_count" \
            | sudo tee /etc/sysctl.d/99-mmt-hugepages.conf >/dev/null 2>&1 || true
        if ! mountpoint -q /dev/hugepages 2>/dev/null; then
            sudo mkdir -p /dev/hugepages
            sudo mount -t hugetlbfs nodev /dev/hugepages 2>/dev/null || true
        fi
        return 0
    fi
    _error "hugepages: kernel allocated only $cur of $hp_count (host fragmented or low free RAM)"
    _error "    free up RAM or reboot, then re-run. Need ${hp_count} × 2 MiB = $((hp_count * 2)) MiB locked."
    exit 1
}

# ── IPv4-first resolver precheck ─────────────────────────────────────
# Docker Hub publishes AAAA records for registry-1.docker.io. On a host
# with no working IPv6 default route, glibc still returns the v6 addrs
# first (RFC 6724 default policy) and `docker pull` dials an unreachable
# v6 endpoint, surfacing as:
#   dial tcp [2600:1f18:...]:443: connect: network is unreachable
# The fix is one line in /etc/gai.conf:
#   precedence ::ffff:0:0/96  100
# which promotes IPv4-mapped addresses above native v6.
#
# Only applied when this host genuinely lacks v6 connectivity, so it's
# a no-op on dual-stack machines.
_ensure_ipv4_first() {
    local gai=/etc/gai.conf
    local rule='precedence ::ffff:0:0/96  100'

    # Already set? Anywhere in the file, even commented-uncommented by the operator.
    if [ -f "$gai" ] && grep -Eq '^[[:space:]]*precedence[[:space:]]+::ffff:0:0/96' "$gai"; then
        return 0
    fi

    # Host has a working v6 default route → leave defaults alone.
    if ip -6 route show default 2>/dev/null | grep -q .; then
        return 0
    fi

    _warn "no IPv6 default route — glibc will prefer AAAA records that can't be reached"
    if ! _ensure_sudo; then
        _warn "cannot edit $gai without sudo — if docker pull fails with 'network unreachable' to a [2600:...] addr, append: $rule"
        return 0
    fi

    if echo "# mmt-studio: prefer IPv4 on v4-only hosts (docker.io AAAA workaround)
$rule" | sudo tee -a "$gai" >/dev/null; then
        _ok "gai.conf: appended IPv4-first precedence (reversible: edit $gai)"
    else
        _warn "gai.conf: append failed — manual fix: echo '$rule' | sudo tee -a $gai"
    fi
}

# ── WSL2 detection + preflight ───────────────────────────────────────
# WSL2 (Windows Subsystem for Linux v2) runs a Microsoft kernel inside a
# Hyper-V utility VM. From the guest's perspective:
#   • Hugepages: available via sysctl, but NOT persisted across `wsl --shutdown`
#     unless an explicit /etc/sysctl.d/ drop-in is in place AND systemd is
#     enabled in /etc/wsl.conf [boot].
#   • PCI/VFIO/IOMMU/SR-IOV: not exposed. Real-NIC DPDK won't work — but the
#     UPF defaults to TAP PMD (net_tap0/net_tap1, kernel virtual), which does.
#   • SCTP: stays inside the docker bridge (NGAP between AMF and the in-cluster
#     gNB simulator never crosses the host).
#   • IPv6: usually no default route → docker pull would dial AAAA records and
#     fail. _ensure_ipv4_first() handles this.
# So WSL2 needs: Docker available, hugepages allocated each session (or
# persisted), and the gai.conf IPv4-first rule. This function makes the
# detection explicit and friendly.
_is_wsl2() {
    grep -qi microsoft /proc/version 2>/dev/null
}

_wsl2_preflight() {
    _is_wsl2 || return 0
    _info "WSL2 detected — DPDK runs in TAP PMD mode (no VFIO/IOMMU needed)"

    # Docker is the one hard requirement. Either Docker Desktop's WSL2
    # integration is on, or Docker Engine is installed natively in the distro.
    if ! command -v docker >/dev/null 2>&1; then
        _error "docker CLI not found in WSL2"
        _error "  → Docker Desktop: Settings → Resources → WSL Integration → enable for this distro"
        _error "  → or install Docker Engine: https://docs.docker.com/engine/install/ubuntu/"
        exit 2
    fi

    # Hugepages persistence across `wsl --shutdown`. Optional — _ensure_hugepages
    # will re-allocate every session — but the drop-in saves the operator from
    # always running sudo on first launch after a Windows reboot.
    local sysctl_dropin=/etc/sysctl.d/99-mmt-hugepages.conf
    if [ ! -f "$sysctl_dropin" ] && _ensure_sudo 2>/dev/null; then
        _info "WSL2: installing hugepage sysctl drop-in for cross-session persistence"
        if echo "# mmt-studio: persist hugepage allocation across wsl --shutdown
vm.nr_hugepages = ${SACORE_HUGEPAGE_COUNT:-512}" | sudo tee "$sysctl_dropin" >/dev/null; then
            _ok "wrote $sysctl_dropin"
            if [ ! -f /etc/wsl.conf ] || ! grep -qE '^\s*systemd\s*=\s*true' /etc/wsl.conf 2>/dev/null; then
                _warn "for the drop-in to auto-apply at WSL boot, enable systemd in /etc/wsl.conf:"
                _warn "  echo -e '[boot]\\nsystemd=true' | sudo tee -a /etc/wsl.conf"
                _warn "  then from PowerShell: wsl --shutdown"
            fi
        fi
    fi
}

# ── Wireshark on host bridge ─────────────────────────────────────────
# Launch the host's locally-installed Wireshark capturing the
# `mmtnet0` Docker bridge. The bridge sits in the host netns and
# survives container restarts (every reset_to_baseline triggers one),
# which the previous `nsenter` approach didn't: Wireshark stayed bound
# to the original sacore netns and went blind on the first restart.
#
# What the bridge sees:
#   - NGAP / SCTP-38412 (satester ↔ sacore)
#   - GTP-U / UDP-2152  (satester ↔ sacore, both directions)
#   - HTTP / TCP-5000   (web UI), TCP-9100 (traffic-agent)
# What it doesn't see:
#   - PFCP / UDP-8805 (internal SMF ↔ UPF on sacore's loopback)
#   - inner-UE traffic on upfgtp TUN (post-decap, inside sacore netns)
# The internal pieces are still debuggable via `tcpdump -i lo` inside
# the container when needed — that's the narrower workflow.
_launch_wireshark_in_netns() {
    if ! command -v wireshark >/dev/null 2>&1; then
        _error "wireshark not found on host — install it (e.g. apt-get install wireshark)"
        return 1
    fi
    if ! ip link show mmtnet0 >/dev/null 2>&1; then
        _error "mmtnet0 bridge not present — bring the stack up first (./run_studio.sh up)"
        return 1
    fi

    # X11 plumbing. nsenter only swaps netns; the mount/IPC namespaces
    # are unchanged, so /tmp/.X11-unix and the user's $XAUTHORITY are
    # still reachable. The only blocker is X-server access control:
    # root needs to be authorised to connect to the user's display.
    local display="${DISPLAY:-:0}"
    local xauth="${XAUTHORITY:-$HOME/.Xauthority}"
    if [ ! -e "$xauth" ]; then
        _warn "XAUTHORITY=$xauth missing — Wireshark may fail to open the display"
    fi
    if command -v xhost >/dev/null 2>&1; then
        # Idempotent local-user grant; cheaper and safer than `xhost +`
        # (which opens the X server to anyone). Errors (e.g. headless
        # session, no DISPLAY) are non-fatal — sudo -E may still work
        # if the user already has the cookie set up.
        xhost +SI:localuser:root >/dev/null 2>&1 || true
    fi

    # Cache sudo creds in the foreground BEFORE we setsid/background.
    # `setsid` detaches from the tty, so any later sudo prompt would
    # fail with "a terminal is required to read the password".
    if ! _ensure_sudo; then
        _error "sudo unavailable — cannot enter sacore netns"
        return 1
    fi

    # Sync the bundled 5GC profile into the user's Wireshark config so
    # `wireshark -C 5GC` finds it. The profile only adds two custom
    # columns — AMF-UE-ID (ngap.AMF_UE_NGAP_ID) and SEID (pfcp.seid) —
    # to the default layout; everything else (TEID, IMSI, QFI, ...) is
    # one click away in the packet detail pane. Keeping the profile
    # checked in to the repo means a fresh dev box gets the columns on
    # first open without manual setup. The cp is idempotent and small
    # (one prefs file), so we re-sync every launch in case the bundled
    # version moves forward.
    #
    # Wireshark below runs as root (sudo nsenter), so it would normally
    # read /root/.config/wireshark — bypass that by exporting
    # WIRESHARK_CONFIG_DIR pointing at the invoking user's config.
    # The profile preferences file is world-readable; root just needs
    # to see the path. Avoids the alternative of writing into /root.
    local user_ws_cfg="${XDG_CONFIG_HOME:-$HOME/.config}/wireshark"
    local src_profile="$SCRIPT_DIR/tools/wireshark/profiles/5GC"
    local dst_profile="$user_ws_cfg/profiles/5GC"
    local profile_args=()
    local wsenv=()
    if [ -d "$src_profile" ]; then
        mkdir -p "$dst_profile"
        cp -f "$src_profile"/* "$dst_profile"/ 2>/dev/null || true
        profile_args=(-C 5GC)
        wsenv+=(WIRESHARK_CONFIG_DIR="$user_ws_cfg")
    fi

    # Spin up the PFCP loopback feed BEFORE wireshark so the FIFO it
    # reads from has a writer attached when wireshark opens it. The
    # feed is best-effort: if sacore isn't up yet, or sudo/nsenter
    # aren't available, we still launch the bridge capture and just
    # log the skipped PFCP path. The feed survives sacore restarts
    # internally (docker-events watcher) so a single Wireshark window
    # keeps showing loopback PFCP across every reset_to_baseline.
    local pfcp_iface_args=()
    if _start_pfcp_watcher; then
        pfcp_iface_args=(-i "$PFCP_FIFO")
    fi

    local logf="/tmp/sawireshark.$$.log"
    _info "launching Wireshark on host bridge mmtnet0 — profile=5GC, survives sacore restarts"
    # `nohup` (not `setsid`) so the child stays in this shell's
    # session — sudo's tty_tickets keep the cached creds tied to
    # /dev/pts/X, and a setsid child gets a fresh session with no
    # controlling tty, which invalidates the cache. nohup just
    # ignores SIGHUP, so closing the terminal won't kill Wireshark
    # while still preserving the tty link sudo needs.
    #
    # No BPF capture filter, no nsenter:
    #   * Capturing on `mmtnet0` (a regular ethernet-DLT bridge)
    #     sidesteps the libpcap-on-linux_cooked SCTP-keyword bug
    #     that was silently dropping NGAP packets on `-i any`.
    #   * Skipping nsenter means the capture handle is in the host
    #     netns, not sacore's — every reset_to_baseline restarts
    #     sacore (new PID, new netns); the bridge stays put, so
    #     Wireshark keeps seeing traffic across the restart.
    # The -Y display filter still narrows the GUI to the three
    # control-plane dissectors we care about (NGAP on SCTP, SIP on
    # TCP, PFCP on UDP); clear it (X in the filter bar) to reveal
    # SCTP heartbeats, GTP-U, web/agent TCP, etc.
    nohup sudo --preserve-env=DISPLAY,XAUTHORITY,WIRESHARK_CONFIG_DIR \
        DISPLAY="$display" XAUTHORITY="$xauth" "${wsenv[@]}" \
        wireshark "${profile_args[@]}" -k -i mmtnet0 "${pfcp_iface_args[@]}" \
        -Y "ngap || sip || pfcp" \
        >"$logf" 2>&1 &
    local wpid=$!
    disown || true

    # Give Wireshark ~1.5 s to either start drawing or fail. If it
    # dies in that window, surface the captured stderr — the most
    # common cause is X auth ("could not connect to display") which
    # is invisible otherwise.
    sleep 1.5
    if ! kill -0 "$wpid" 2>/dev/null; then
        _error "Wireshark exited immediately. Last output:"
        if [ -s "$logf" ]; then
            sed 's/^/  | /' "$logf" >&2
        else
            echo "  | (no output captured)" >&2
        fi
        _error "log: $logf"
        return 1
    fi
    _ok "Wireshark launched (close its window to exit). log: $logf"
}

# ── Veth renaming ────────────────────────────────────────────────────
# Docker auto-names host-side veth endpoints (`vethXXXXXX`) per kernel
# defaults — there is no compose flag to change this. After bring-up
# we rename them to legible names so `ifconfig`/`tcpdump`/`ip link`
# show which container each veth belongs to.
#
#   sacore   → host veth `sacore-veth`
#   satester → host veth `satester-veth`
#
# Idempotent. All renames are batched into a single `sudo bash -c`
# invocation so the user is prompted once per bring-up (or zero times
# if sudo creds are already cached, e.g. via `_ensure_sudo` earlier in
# this run). For fully unattended runs, add an /etc/sudoers.d entry:
#   <user> ALL=(root) NOPASSWD: /usr/sbin/ip link set *
# Names are kernel-namespace; they don't collide with container names.
_ensure_sudo() {
    # Cache sudo creds upfront so background rename doesn't have to
    # prompt while compose logs are streaming. Returns 0 on success,
    # 1 if no sudo is available (no tty / wrong password / no sudo).
    if sudo -n true 2>/dev/null; then
        return 0
    fi
    if [ ! -t 0 ]; then
        _warn "no tty — host veth rename will be skipped"
        return 1
    fi
    _info "host veth rename needs sudo (one-time prompt this session)"
    sudo -v || { _warn "sudo failed — veth rename will be skipped"; return 1; }
}

_rename_veths() {
    local svc script=""
    for svc in sacore satester; do
        script+="$(_rename_veth_script_for "$svc")"
    done
    if [ -z "$script" ]; then
        _info "veth rename: nothing to do (already named, or containers not running)"
        return 0
    fi
    # One sudo invocation for all renames. stderr passes through so a
    # real failure (e.g. veth busy, ip-link not found) is visible.
    if sudo bash -c "$script"; then
        _ok "host veths renamed"
    else
        _warn "veth rename failed — re-run \`./run_studio.sh rename-veths\` manually"
    fi
}

# Emit the `ip link set ...` snippet that would rename ONE container's
# host veth to `<svc>-veth`. Empty output means nothing to do (container
# not running, or already correctly named). Shared by _rename_veths
# (one-shot, both services) and _veth_watcher_loop (event-driven, per
# container restart). Keeps the lookup logic in one place so changes
# don't drift between the boot rename and the live rename.
_rename_veth_script_for() {
    local svc="$1" iflink host_if newname
    docker inspect "$svc" >/dev/null 2>&1 || return 0
    iflink=$(docker exec "$svc" cat /sys/class/net/eth0/iflink 2>/dev/null) || return 0
    [ -n "$iflink" ] || return 0
    host_if=$(ip -o link \
        | awk -v idx="$iflink" '$1 ~ "^"idx":" {gsub(":",""); gsub(/@.*/,""); print $2}' \
        | head -1)
    [ -n "$host_if" ] || return 0
    newname="${svc}-veth"
    [ "$host_if" = "$newname" ] && return 0
    printf 'ip link set %s down && ip link set %s name %s && ip link set %s up && echo "renamed: %s -> %s"; ' \
        "$host_if" "$host_if" "$newname" "$newname" "$host_if" "$newname"
}

# ── Veth-rename watcher ──────────────────────────────────────────────
# Every reset_to_baseline (any test in baseline/full pretest mode) exits
# sacore, docker restarts it, and the fresh netns gets a new auto-named
# host veth. The one-shot rename at `up` only catches the first start;
# without this watcher the operator sees `sacore-veth` for ~one test and
# `vethXXXXXX` forever after.
#
# The watcher tails `docker events --filter event=start` for sacore +
# satester and re-applies the rename per restart. It runs detached from
# the script's foreground (Ctrl-C on log-tail doesn't kill it) so the
# rename keeps working across an entire test session. PID stored at
# ${SCRIPT_DIR}/.mmt-veth-watcher.pid; `down`/`stop`/`restart` cleans it.
#
# Privilege model: one outer `sudo` hoists the entire watcher to uid 0
# at start (creds primed via _ensure_sudo while we still hold the tty).
# Inside the loop `ip link set …` runs without its own sudo. The earlier
# shape used `setsid bash -c '… sudo -n …'` which broke under tty_tickets
# the moment setsid orphaned the session — same failure mode the PFCP
# watcher had until commit 3431209.
VETH_WATCHER_PID_FILE="${SCRIPT_DIR}/.mmt-veth-watcher.pid"
VETH_WATCHER_LOG="${SCRIPT_DIR}/.mmt-veth-watcher.log"

_veth_watcher_loop() {
    # docker events streams forever; one line per matching event.
    # --format gives us just the container name so the loop reads
    # cleanly. A short settle delay lets the kernel finish wiring the
    # new netns/veth pair before we look up its host-side name.
    #
    # Runs un-sudo'd: _start_veth_watcher hoists the whole watcher
    # to uid 0 via one outer `sudo bash -c`, so the snippet emitted
    # by _rename_veth_script_for is already running as root here.
    docker events \
        --filter 'event=start' \
        --filter 'container=sacore' \
        --filter 'container=satester' \
        --format '{{.Actor.Attributes.name}}' \
    | while IFS= read -r name; do
        [ -n "$name" ] || continue
        sleep 0.3
        local snippet
        snippet=$(_rename_veth_script_for "$name") || continue
        [ -n "$snippet" ] || continue
        echo "[$(date +%H:%M:%S)] $name started — renaming veth"
        bash -c "$snippet" 2>&1 || \
            echo "[$(date +%H:%M:%S)] WARN: rename for $name failed"
    done
}

_start_veth_watcher() {
    # Kill any stale watcher from a previous run so we don't accumulate
    # ghosts across `up` invocations.
    _stop_veth_watcher
    # Cache sudo creds while the controlling tty is still attached, then
    # spawn the watcher under a single `sudo` umbrella so per-event
    # rename calls don't have to re-authenticate. `nohup` (not setsid)
    # keeps the parent session so sudo's tty_ticket cache stays valid.
    _ensure_sudo || { _warn "sudo unavailable — skipping veth watcher"; return 1; }
    : > "$VETH_WATCHER_LOG"
    nohup sudo --preserve-env=PATH bash -c \
        "$(declare -f _rename_veth_script_for _veth_watcher_loop); _veth_watcher_loop" \
        >>"$VETH_WATCHER_LOG" 2>&1 < /dev/null &
    echo $! > "$VETH_WATCHER_PID_FILE"
    disown 2>/dev/null || true
    _ok "veth watcher started (pid $(cat "$VETH_WATCHER_PID_FILE"), log: ${VETH_WATCHER_LOG##*/})"
}

_stop_veth_watcher() {
    [ -f "$VETH_WATCHER_PID_FILE" ] || return 0
    local pid
    pid=$(cat "$VETH_WATCHER_PID_FILE" 2>/dev/null) || pid=""
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        # Watcher root-shell + docker-events child are uid 0 — plain
        # kill from the user fails with EPERM. Use sudo to TERM the
        # whole process group.
        echo root | sudo -S kill -TERM -- "-$pid" 2>/dev/null \
          || echo root | sudo -S kill -TERM "$pid" 2>/dev/null || true
        sleep 0.2
        echo root | sudo -S kill -KILL -- "-$pid" 2>/dev/null \
          || echo root | sudo -S kill -KILL "$pid" 2>/dev/null || true
        _info "veth watcher stopped (pid $pid)"
    fi
    rm -f "$VETH_WATCHER_PID_FILE"
}

# ── PFCP feed (named pipe into the bridge Wireshark) ─────────────────
# PFCP between SMF and UPF runs on 127.0.0.1:8805 / :8806 — same-process
# loopback traffic, never on any bridge or veth. The bridge Wireshark
# (capturing `mmtnet0` in the host netns) can't see it.
#
# To merge loopback PFCP into the SAME Wireshark window:
#
#   1. A named-pipe FIFO lives on the host filesystem ($PFCP_FIFO).
#   2. A coordinator process holds the FIFO open for write (FD 9) so
#      Wireshark — reading from the other end as a second `-i` capture
#      interface — never sees EOF between sacore incarnations.
#   3. On every sacore start the coordinator runs
#         sudo nsenter -t <pid> -n tcpdump -i lo -w -
#      and feeds its pcap stream into FD 9. The FIRST tcpdump writes
#      the 24-byte pcap global header; SUBSEQUENT ones have that header
#      stripped (see `dd bs=24 count=1`) so wireshark sees a single
#      continuous pcap stream rather than restart-mid-stream garbage.
#   4. `_launch_wireshark_in_netns` adds `-i $PFCP_FIFO` so the single
#      bridge Wireshark gets the loopback PFCP as a second virtual
#      interface alongside `mmtnet0`. One window, two capture sources.
PFCP_WATCHER_PID_FILE="${SCRIPT_DIR}/.mmt-pfcp-watcher.pid"
PFCP_WATCHER_LOG="${SCRIPT_DIR}/.mmt-pfcp-watcher.log"
# FIFO lives in /run — NOT /tmp, NOT SCRIPT_DIR. Why:
#
#   * /tmp (mode 1777, sticky): kernel's fs.protected_fifos=1 blocks
#     root from opening a FIFO owned by another user. The watcher
#     runs as root, FIFO would be bxb-owned → root open EACCES.
#
#   * SCRIPT_DIR under /home/bxb: home directory is mode 0750
#     bxb:bxb. When wireshark is launched via sudo, it sees SUDO_USER
#     and drops privileges before spawning dumpcap. dumpcap then runs
#     as bxb (or worse, the `wireshark` group user) and can't
#     traverse another user's home → stat() on the FIFO path returns
#     EACCES → "error getting information on pipe or socket: Permission
#     denied". The exact failure the operator reported.
#
#   * /run is mode 0755 root:root — universally traversable for stat,
#     only root can write into it. Root creates the FIFO; chmod 0666
#     so dumpcap (whichever uid it lands as) can open it for read.
PFCP_FIFO="/run/mmt-pfcp.pcap"

_pfcp_spawn_tcpdump_into_fifo() {
    # First spawn passes tcpdump's output verbatim (including the
    # 24-byte pcap global header). Subsequent spawns must strip those
    # 24 bytes — wireshark would otherwise treat them as a corrupt
    # packet record and break the capture mid-stream.
    #
    # Runs un-sudo'd: _start_pfcp_watcher hoists the whole watcher to
    # uid=0 via one outer `sudo bash -c`, so we're already root here.
    # Earlier the watcher tried `sudo -n` inside a setsid subshell —
    # setsid drops the controlling tty so the tty_ticket'd cached
    # creds were invalid and tcpdump never started.
    #
    # $1 = sacore PID, $2 = "1" on first spawn else "0".
    local pid="$1" first="$2"
    local logf="/tmp/sapfcp-${pid}.log"
    if [ "$first" = "1" ]; then
        ( nsenter -t "$pid" -n tcpdump -i lo -U -s 0 -w - \
            'udp port 8805 or udp port 8806' 2>>"$logf" \
        ) >&9 &
    else
        # `dd bs=24 count=1 iflag=fullblock` is the streaming-safe way
        # to discard exactly 24 bytes from a pipe — without iflag,
        # the read could return short and leak bytes of packet data.
        ( nsenter -t "$pid" -n tcpdump -i lo -U -s 0 -w - \
            'udp port 8805 or udp port 8806' 2>>"$logf" \
          | { dd bs=24 count=1 iflag=fullblock of=/dev/null status=none; cat; } \
        ) >&9 &
    fi
    disown 2>/dev/null || true
    echo "[$(date +%H:%M:%S)] PFCP tcpdump spawned for sacore pid=$pid (first=$first)"
}

_pfcp_watcher_loop() {
    # Open the FIFO for write on FD 9 and KEEP it open for the lifetime
    # of the watcher. This is what stops wireshark from seeing EOF on
    # the second capture interface between sacore restarts.
    exec 9>"$PFCP_FIFO"
    local first=1
    local pid
    pid=$(docker inspect -f '{{.State.Pid}}' sacore 2>/dev/null) || pid=""
    if [ -n "$pid" ] && [ "$pid" != "0" ]; then
        _pfcp_spawn_tcpdump_into_fifo "$pid" "$first"
        first=0
    fi
    docker events \
        --filter 'event=start' \
        --filter 'container=sacore' \
        --format '{{.Actor.Attributes.name}}' \
    | while IFS= read -r _; do
        # Brief settle delay so the new sacore's netns/loopback is up
        # before we attach. tcpdump on `lo` succeeds instantly once
        # the netns exists.
        sleep 1
        pid=$(docker inspect -f '{{.State.Pid}}' sacore 2>/dev/null) || pid=""
        [ -z "$pid" ] || [ "$pid" = "0" ] && continue
        _pfcp_spawn_tcpdump_into_fifo "$pid" "$first"
        first=0
    done
}

_start_pfcp_watcher() {
    _stop_pfcp_watcher
    if ! command -v nsenter >/dev/null 2>&1 || ! command -v tcpdump >/dev/null 2>&1; then
        _warn "host tcpdump/nsenter missing — PFCP loopback will not be merged into Wireshark"
        return 1
    fi
    if ! docker inspect sacore >/dev/null 2>&1; then
        _warn "sacore container not running — skipping PFCP feed"
        return 1
    fi
    # Cache sudo creds while the controlling tty is still attached.
    # The watcher itself runs as root from the outer `sudo bash -c`
    # below — that single sudo elevation lasts the lifetime of the
    # watcher, so the per-tcpdump-spawn loop never needs its own
    # sudo (the earlier setsid + sudo -n approach failed because
    # setsid drops the tty and tty_tickets invalidated the cache).
    _ensure_sudo || { _warn "sudo unavailable — skipping PFCP feed"; return 1; }
    # (Re)create the FIFO so wireshark's `-i $PFCP_FIFO` has something
    # to open. /run is root-only writable so mkfifo / chmod need sudo;
    # we already primed the cache with _ensure_sudo above.
    sudo -n rm -f "$PFCP_FIFO" 2>/dev/null || true
    if ! sudo -n mkfifo "$PFCP_FIFO" 2>/dev/null; then
        _warn "mkfifo $PFCP_FIFO failed (sudo creds expired?)"
        return 1
    fi
    sudo -n chmod 0666 "$PFCP_FIFO" 2>/dev/null || true
    : > "$PFCP_WATCHER_LOG"
    # `nohup` (not `setsid`) — same reason the bridge wireshark
    # launcher uses nohup: setsid loses the controlling tty which
    # invalidates sudo's tty_ticket cache. nohup ignores SIGHUP but
    # keeps the parent shell's session, so the sudo elevation that
    # _ensure_sudo just primed is still valid here.
    nohup sudo --preserve-env=PFCP_FIFO,PATH bash -c "
        export PFCP_FIFO='$PFCP_FIFO'
        $(declare -f _pfcp_spawn_tcpdump_into_fifo _pfcp_watcher_loop)
        _pfcp_watcher_loop
    " >>"$PFCP_WATCHER_LOG" 2>&1 < /dev/null &
    echo $! > "$PFCP_WATCHER_PID_FILE"
    disown 2>/dev/null || true
    _ok "PFCP feed started (pid $(cat "$PFCP_WATCHER_PID_FILE"); FIFO ${PFCP_FIFO})"
}

_stop_pfcp_watcher() {
    # FIFO lives in /run — sudo to remove. -n: if creds expired the
    # rm silently fails, the stale FIFO is harmless until next start.
    [ -f "$PFCP_WATCHER_PID_FILE" ] || { sudo -n rm -f "$PFCP_FIFO" 2>/dev/null || true; return 0; }
    local pid
    pid=$(cat "$PFCP_WATCHER_PID_FILE" 2>/dev/null) || pid=""
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        # Watcher root-shell + its tcpdump children are uid 0 — plain
        # `kill` from the user fails with EPERM. Use sudo to TERM the
        # whole process group; fall back to KILL after the grace tick.
        echo root | sudo -S kill -TERM -- "-$pid" 2>/dev/null \
          || echo root | sudo -S kill -TERM "$pid" 2>/dev/null || true
        sleep 0.2
        echo root | sudo -S kill -KILL -- "-$pid" 2>/dev/null \
          || echo root | sudo -S kill -KILL "$pid" 2>/dev/null || true
        _info "PFCP feed stopped (pid $pid)"
    fi
    # Best-effort: kill any straggler nsenter+tcpdump spawned by the
    # watcher. The wireshark window itself is the bridge launcher's
    # responsibility — _cleanup_wireshark handles it.
    echo root | sudo -S pkill -TERM -f 'nsenter -t [0-9]+ -n tcpdump -i lo' 2>/dev/null || true
    rm -f "$PFCP_WATCHER_PID_FILE" 2>/dev/null || true
    sudo -n rm -f "$PFCP_FIFO" 2>/dev/null || true
}

# Wait synchronously until the role's containers report Running=true
# (or timeout). For ROLE=both both must be up; for ROLE=core only
# sacore; for ROLE=tester only satester. Used so we can rename veths
# *before* compose starts streaming logs — avoids the prior race where
# a backgrounded rename had to compete with compose for stdin/tty.
_wait_for_running() {
    local i s t need_sacore=0 need_satester=0
    case "$ROLE" in
        both)    need_sacore=1; need_satester=1 ;;
        core)    need_sacore=1 ;;
        tester)  need_satester=1 ;;
    esac
    for i in $(seq 1 30); do
        s=$(docker inspect -f '{{.State.Running}}' sacore   2>/dev/null) || s=""
        t=$(docker inspect -f '{{.State.Running}}' satester 2>/dev/null) || t=""
        if { [ "$need_sacore"   -eq 0 ] || [ "$s" = "true" ]; } \
        && { [ "$need_satester" -eq 0 ] || [ "$t" = "true" ]; }; then
            return 0
        fi
        sleep 1
    done
    _warn "containers not running after 30s (role=$ROLE) — sacore=$s satester=$t"
}

# Kill any leftover Wireshark sessions from `--debug` and remove
# their stderr-capture logs. _launch_wireshark_in_netns nohup's the
# wireshark process so it survives this script's exit (so the GUI
# stays open after `up`), but that means a subsequent `down` leaves
# the process running with a stale capture handle on a deleted bridge.
# Orphans grow every cycle — this is the cleanup point.
_cleanup_wireshark() {
    # `pkill -f` matches the full command line. The pattern is the
    # launcher's signature (-k -i mmtnet0 -Y ngap||sip||pfcp) — plain
    # Wireshark sessions started by hand are NOT matched.
    local pat='wireshark .*-i mmtnet0 .*-Y .*ngap'
    if pgrep -af "$pat" >/dev/null 2>&1; then
        _info "killing orphaned Wireshark sessions"
        echo root | sudo -S pkill -TERM -f "$pat" 2>/dev/null || true
        sleep 1
        echo root | sudo -S pkill -KILL -f "$pat" 2>/dev/null || true
    fi
    # Also catch any pre-fix `nsenter ... wireshark` orphans left from
    # the previous launcher revision.
    if pgrep -af 'nsenter -t .* -n[ ]+wireshark' >/dev/null 2>&1; then
        echo root | sudo -S pkill -TERM -f 'nsenter -t .* -n[ ]+wireshark' 2>/dev/null || true
        sleep 1
        echo root | sudo -S pkill -KILL -f 'nsenter -t .* -n[ ]+wireshark' 2>/dev/null || true
    fi
    # Stale capture logs from the launcher; safe to remove anytime.
    rm -f /tmp/sawireshark.*.log 2>/dev/null || true
}

# ── Per-test rotating Wireshark watcher (--wireshark mode) ───────────
# Polls satester's /api/tests endpoint. The moment a new test_name
# transitions to status=RUNNING, the watcher kills the previous
# Wireshark window (started by us) and opens a fresh one on mmtnet0.
# When the test completes the window stays open so the operator can
# scroll back through the capture — only the NEXT test-start rotates
# it. Lifecycle:
#
#   _start_test_wireshark_watcher  — fork the polling loop, store pid
#   _stop_test_wireshark_watcher   — kill the loop on script exit / down
#
# The poll runs as the invoking user (so it can find the X auth), but
# each Wireshark launch goes through _launch_wireshark_in_netns which
# sudo's into the sacore netns for the actual nsenter dance.
TEST_WS_WATCHER_PID_FILE="/tmp/satest-wireshark-watcher.$USER.pid"

_start_test_wireshark_watcher() {
    if ! command -v wireshark >/dev/null 2>&1; then
        _error "wireshark not found on host — install it (apt-get install wireshark)"
        return 1
    fi
    if ! command -v curl >/dev/null 2>&1; then
        _error "curl not found on host — needed for the satester pcap stream"
        return 1
    fi
    if ! command -v python3 >/dev/null 2>&1; then
        _error "python3 required for --wireshark watcher (parses /api/tests JSON)"
        return 1
    fi
    # Tester reachable?
    if ! curl -fs -o /dev/null --max-time 3 \
            "http://localhost:5001/api/tests" 2>/dev/null; then
        _warn "satester not reachable at http://localhost:5001 yet — watcher will keep retrying"
    fi
    _stop_test_wireshark_watcher    # idempotent — kill any prior watcher first

    _info "--wireshark: starting per-test Wireshark rotation watcher"
    local logf="/tmp/satest-wireshark-watcher.$USER.log"

    # Polling design that mirrors per_test_wireshark.ps1 (Windows):
    #  1. Trigger on ANY change in /api/tests (results.Count grew OR
    #     last entry's test_name changed). The old "trigger only on
    #     status=RUNNING" model raced sub-second tests -- TC-NGS-001
    #     in pretest_mode=delta lives in RUNNING for ~500 ms while
    #     the watcher polls at 1 Hz, so the window was missed often
    #     enough to look broken to operators.
    #  2. On change, kill any prior watcher-spawned Wireshark and
    #     pipe satester's live pcap stream into a fresh one:
    #         curl -N -s http://localhost:5001/api/tests/active/pcap.stream
    #             | wireshark -k -i - -Y 'ngap || sip || pfcp'
    #     The endpoint is served by satester's in-process tcpdump
    #     (tester/src/testcases/_pcap_capture.py) which captures
    #     with millisecond precision around the test's lifetime --
    #     no nsenter, no sudo, no host bridge dependency.
    #  3. Status flips (RUNNING -> PASS) deliberately do NOT rotate.
    #     The existing curl|wireshark pipe is still draining packets;
    #     killing it mid-test would empty the window.
    (
        prev_count=0
        prev_name=""
        # Tracks the previous test's curl→wireshark pipeline pgid
        # so we can take it down (and any straggler wireshark window)
        # when the next test fires.
        pipeline_pgid=""
        while sleep 0.25; do
            resp=$(curl -fs --max-time 3 "http://localhost:5001/api/tests" 2>/dev/null)
            [ -z "$resp" ] && continue
            count=$(printf '%s' "$resp" | python3 -c 'import json,sys
try:
    d=json.load(sys.stdin); print(len(d.get("results") or []))
except: print(0)' 2>/dev/null)
            name=$(printf '%s' "$resp" | python3 -c 'import json,sys
try:
    d=json.load(sys.stdin); r=d.get("results") or []
    print(r[-1].get("test_name") if r else "")
except: print("")' 2>/dev/null)
            new_test=0
            if [ "$count" != "$prev_count" ] || [ "$name" != "$prev_name" ]; then
                if [ -n "$name" ] && [ "$count" != "0" ]; then
                    new_test=1
                fi
            fi
            if [ "$new_test" = "1" ]; then
                echo "[$(date +%H:%M:%S)] new test detected: $name -- capturing then opening Wireshark" >>"$logf"
                # Kill any prior pipeline (whole process group so
                # both curl and wireshark go down cleanly).
                if [ -n "$pipeline_pgid" ] && kill -0 "-$pipeline_pgid" 2>/dev/null; then
                    kill -- -"$pipeline_pgid" 2>/dev/null || true
                fi

                # File-then-open instead of live curl|wireshark pipe.
                # The live pipe raced wireshark's GUI startup: if
                # wireshark wasn't reading from stdin by the time
                # satester's stream endpoint closed (1 s after the
                # test's tcpdump container stopped), curl exited
                # with packets still in the OS-pipe buffer that
                # wireshark sometimes never picked up. Operators
                # reported "wireshark opens at the end of the test
                # only, sometimes logs are missed".
                #
                # The fix sequences the two: curl writes the
                # complete pcap to a temp file, THEN wireshark
                # opens the file (snapshot -r mode). The file is
                # guaranteed complete -- no race, no buffer-drop.
                # User-visible delay is the same as before
                # (~test_duration + 1 s for satester to close the
                # stream + ~2 s for wireshark GUI to render); only
                # the data-loss bug goes away.
                tmpdir=$(mktemp -d "/tmp/mmt-test-pcap.XXXXXX")
                (
                    # New process group so the outer kill can take
                    # the whole pipeline down on the next rotation.
                    setsid bash -c '
                        pcap="$1/test.pcap"
                        log="$2"
                        # Stream the whole pcap to a file. Returns
                        # when satester closes the connection
                        # (~1 s after the in-container tcpdump
                        # exits at test-end).
                        curl -N -s -o "$pcap" "http://localhost:5001/api/tests/active/pcap.stream" 2>/dev/null
                        if [ -s "$pcap" ]; then
                            echo "[$(date +%H:%M:%S)]   pcap captured ($(stat -c%s "$pcap" 2>/dev/null || wc -c <"$pcap") bytes), opening Wireshark" >>"$log"
                            wireshark -r "$pcap" -Y "ngap || sip || pfcp" >>"$log" 2>&1
                        else
                            echo "[$(date +%H:%M:%S)]   pcap empty after stream close -- skipping Wireshark" >>"$log"
                        fi
                        # Cleanup once wireshark exits (operator
                        # closed the window). Snapshot file no
                        # longer needed -- the canonical per-run
                        # pcap lives in data/test_results/.
                        rm -rf "$1"
                    ' _ "$tmpdir" "$logf"
                ) &
                pipeline_pgid=$!
                echo "[$(date +%H:%M:%S)]   pipeline pgid=$pipeline_pgid  tmpdir=$tmpdir" >>"$logf"
            fi
            prev_count="$count"
            prev_name="$name"
        done
    ) >>"$logf" 2>&1 &
    echo "$!" > "$TEST_WS_WATCHER_PID_FILE"
    _info "watcher PID $(cat "$TEST_WS_WATCHER_PID_FILE") — log: $logf"
}

_stop_test_wireshark_watcher() {
    [ -f "$TEST_WS_WATCHER_PID_FILE" ] || return 0
    local pid
    pid=$(cat "$TEST_WS_WATCHER_PID_FILE" 2>/dev/null || true)
    if [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; then
        kill "$pid" 2>/dev/null || true
        sleep 0.3
        kill -9 "$pid" 2>/dev/null || true
    fi
    rm -f "$TEST_WS_WATCHER_PID_FILE"
}

# ── Dispatch ─────────────────────────────────────────────────────────
case "$ACTION" in
    down)
        _info "tearing down core + tester (and bridge network)..."
        # Clean up the side processes that compose-down doesn't know
        # about — currently the nohup'd Wireshark session from
        # `--debug` mode and the per-test rotation watcher from
        # `--wireshark` mode. Run before `compose down` so log lines
        # from the cleanup come out before docker's "Removing X" stream.
        _stop_test_wireshark_watcher
        _cleanup_wireshark
        _stop_veth_watcher
        _stop_pfcp_watcher
        exec "${DC[@]}" down "${EXTRA[@]}"
        ;;
    logs)
        _info "tailing logs (Ctrl-C to detach)..."
        exec "${DC[@]}" logs -f "${EXTRA[@]}"
        ;;
    ps)
        exec "${DC[@]}" ps "${EXTRA[@]}"
        ;;
    wireshark)
        # Stand-alone: assume the stack is already up. Brings up ONE
        # Wireshark window capturing two virtual interfaces in parallel:
        #   1. mmtnet0 bridge — NGAP / SIP / on-bridge PFCP (host netns,
        #      survives sacore restarts).
        #   2. a named-pipe FIFO fed by nsenter+tcpdump on sacore's lo —
        #      SMF↔UPF PFCP on 127.0.0.1:8805/8806. A docker-events
        #      watcher re-attaches the feed on every sacore restart so
        #      the same window keeps showing loopback PFCP across every
        #      reset_to_baseline.
        _launch_wireshark_in_netns
        exit $?
        ;;
    watcher)
        # Just start (or restart) the per-test rotation watcher --
        # doesn't touch the running stack at all. install.sh calls
        # this at the end of a successful install so the operator
        # gets the same default-on Wireshark behaviour install.ps1
        # already provides on Windows. Idempotent: any prior watcher
        # PID gets killed first by _start_test_wireshark_watcher.
        _start_test_wireshark_watcher || _die "watcher failed to start"
        exit 0
        ;;
    restart|stop|start|build|pull)
        # `restart` / `stop` tear down container netns → veth + PFCP
        # watchers' targets disappear. Kill them here so the next `up`
        # starts fresh watchers rather than two fighting over events.
        case "$ACTION" in restart|stop) _stop_veth_watcher; _stop_pfcp_watcher ;; esac
        exec "${DC[@]}" "$ACTION" "${EXTRA[@]}"
        ;;
    up)
        # Always start from a clean slate: tear down any running
        # containers + bridge first so the next `up` doesn't inherit
        # stale netns / partially-recreated services. Cheap (~2s) and
        # the operator-mental-model match — `up` after a code change
        # should mean "start fresh", not "incrementally diff".
        _info "cleaning up any running stack first..."
        _cleanup_wireshark
        "${DC[@]}" down 2>/dev/null || true
        _force_remove_stale
        if [ "$FRESH" -eq 1 ]; then
            # --fresh adds a no-cache image rebuild on top of the
            # always-clean teardown above.
            _info "--fresh: rebuilding images with --no-cache"
            "${DC[@]}" build --no-cache
        fi
        _ensure_sudo || true
        _wsl2_preflight
        # Hugepages are only needed for sacore's DPDK UPF — skip when
        # this host is bringing up tester-only (no DPDK locally).
        if [ "$ROLE" != "tester" ]; then
            _ensure_hugepages
        fi
        _ensure_ipv4_first
        _info "host arch: $HOST_ARCH_LABEL  role=$ROLE  (hugepages target: ${SACORE_HUGEPAGE_COUNT})"
        if [ "$IS_PI_LIKE" -eq 1 ]; then
            _info "Pi/ARM note: DPDK PMD only works against net_tap (no real-NIC PMD on Pi onboard NICs); kernel forwarding is the real path."
        fi
        _info "starting $(_role_phrase) (detached)..."
        _compose_up_or_hint "${EXTRA[@]}"
        _wait_for_running && _rename_veths
        # Keep the rename live across restart-policy restarts (every
        # reset_to_baseline triggers one). Watcher survives the script's
        # exit; `down`/`stop` cleans it up.
        _start_veth_watcher
        _print_summary_urls " inside mmtnet"
        if [ "$DEBUG" -eq 1 ]; then
            _launch_wireshark_in_netns || true
        fi
        if [ "$WIRESHARK_FLAG" -eq 1 ]; then
            # Spin up the per-test rotating Wireshark watcher. It polls
            # http://localhost:5001/api/tests every ~1 s and, the moment
            # a new test_name flips to status=RUNNING, kills the prior
            # Wireshark window and opens a fresh one on mmtnet0. The
            # window stays open after the test completes — only the next
            # test-start rotates it.
            _start_test_wireshark_watcher || _warn "--wireshark watcher failed to start"
        fi
        _info "logs:   ./run_studio.sh logs"
        _info "stop:   ./run_studio.sh down"
        ;;
    rename-veths)
        # Manual re-run of the veth rename pass (useful if a container
        # restarted and got a new auto-named veth, or if the initial
        # rename was skipped — e.g. sudo wasn't authorized in time).
        _ensure_sudo || true
        _rename_veths
        ;;
    reset)
        # Full forceful cleanup. Reaches for things `down` misses:
        #   - containers named sacore/satester/satraffic regardless of
        #     which compose project owns them (install.sh and
        #     run_studio.sh use different project names by default);
        #   - the mmtnet bridge if anything else created it;
        #   - named volumes (satester_data, satester_config, sacore_data).
        # Watcher side processes (veth / PFCP / wireshark) get torn down
        # the same way `down` does. After this, ./run_studio.sh up will
        # rebuild from a guaranteed-clean state.
        _info "resetting MMT Studio (containers + network + volumes)..."
        _stop_test_wireshark_watcher
        _cleanup_wireshark
        _stop_veth_watcher
        _stop_pfcp_watcher
        "${DC[@]}" down --remove-orphans --volumes 2>/dev/null || true
        _force_remove_stale
        # Belt-and-braces: catch any same-named container that survived
        # because its compose-project label was empty/missing.
        for c in sacore satester satraffic; do
            if docker inspect "$c" >/dev/null 2>&1; then
                _info "removing residual container $c"
                docker rm -f "$c" >/dev/null 2>&1 || true
            fi
        done
        if docker network inspect mmtnet >/dev/null 2>&1; then
            if docker network rm mmtnet >/dev/null 2>&1; then
                _info "removed residual network mmtnet"
            else
                _warn "mmtnet network has active endpoints — disconnect manually:"
                _warn "  docker network inspect mmtnet | grep Name"
            fi
        fi
        _ok "reset complete. Next: ./run_studio.sh up"
        ;;
    "")
        # Default "foreground" = bring up detached, rename veths
        # synchronously, then tail logs in the foreground. Ctrl-C
        # detaches (containers keep running); use ./stop_studio.sh
        # to actually stop. Doing rename BEFORE logs stream avoids
        # the prior race where a backgrounded rename fought compose
        # for stdin/tty when sudo prompted.
        # Always start from a clean slate (see `up` action above for
        # rationale). Same teardown, then fresh up + log tail.
        _info "cleaning up any running stack first..."
        _cleanup_wireshark
        "${DC[@]}" down 2>/dev/null || true
        _force_remove_stale
        if [ "$FRESH" -eq 1 ]; then
            _info "--fresh: rebuilding images with --no-cache"
            "${DC[@]}" build --no-cache
        fi
        _ensure_sudo || true
        _wsl2_preflight
        # Hugepages are only needed for sacore's DPDK UPF — skip when
        # this host is bringing up tester-only (no DPDK locally).
        if [ "$ROLE" != "tester" ]; then
            _ensure_hugepages
        fi
        _ensure_ipv4_first
        _info "host arch: $HOST_ARCH_LABEL  role=$ROLE  (hugepages target: ${SACORE_HUGEPAGE_COUNT})"
        if [ "$IS_PI_LIKE" -eq 1 ]; then
            _info "Pi/ARM note: DPDK PMD only works against net_tap (no real-NIC PMD on Pi onboard NICs); kernel forwarding is the real path."
        fi
        _info "starting $(_role_phrase) (detached, then tailing logs)..."
        _compose_up_or_hint "${EXTRA[@]}"
        _wait_for_running && _rename_veths
        # Keep the rename live across restart-policy restarts (every
        # reset_to_baseline triggers one). Watcher survives Ctrl-C
        # detach; `down`/`stop` cleans it up.
        _start_veth_watcher
        _print_summary_urls ""
        if [ "$DEBUG" -eq 1 ]; then
            _launch_wireshark_in_netns || true
        fi
        if [ "$WIRESHARK_FLAG" -eq 1 ]; then
            _start_test_wireshark_watcher || _warn "--wireshark watcher failed to start"
        fi
        _info "tailing logs — Ctrl-C detaches (containers keep running). Stop: ./stop_studio.sh"
        # Run `compose logs -f` as a child (NOT `exec`) so we can trap
        # SIGINT here and kill the logs process ourselves on first ^C.
        # `docker compose logs -f` alone often eats the first SIGINT
        # and only exits on the second — by killing it from the trap
        # we get a single-keypress detach.
        _logs_pid=""
        _on_int() {
            trap - INT TERM
            echo
            if [ -n "$_logs_pid" ]; then
                kill -TERM "$_logs_pid" 2>/dev/null || true
                # Give compose ~1s to exit cleanly, then SIGKILL.
                ( sleep 1; kill -KILL "$_logs_pid" 2>/dev/null || true ) &
                wait "$_logs_pid" 2>/dev/null || true
            fi
            _info "detached (containers keep running). Stop: ./stop_studio.sh"
            exit 0
        }
        trap _on_int INT TERM
        "${DC[@]}" logs -f &
        _logs_pid=$!
        wait "$_logs_pid"
        ;;
    *)
        _error "unknown action: $ACTION"
        _error "  → run \`./run_studio.sh --help\` for usage"
        exit 2
        ;;
esac
