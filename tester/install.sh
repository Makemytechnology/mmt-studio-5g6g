#!/bin/bash
# install.sh — SA Tester 5G: comprehensive install for source checkouts.
#
# Sets up a self-contained virtualenv at ./.venv so run.sh works on any
# Linux host that has Python >= 3.10. Installs system deps via the
# distro's package manager and pip-installs the compiled Python packages
# (cryptography, pydantic-core, httptools, cffi, pysctp) into the venv.
#
# Supported OS/arch: Linux x86_64 and Linux aarch64.
#
# Usage:  ./install.sh                    # system deps + venv + pip installs
#         ./install.sh --skip-system      # skip package-manager step
#         ./install.sh --python /usr/bin/python3.12   # force interpreter

set -e

SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
cd "$SCRIPT_DIR"

GREEN='\033[0;32m'; YELLOW='\033[1;33m'; RED='\033[0;31m'; NC='\033[0m'
info()  { echo -e "${GREEN}[+]${NC} $1"; }
warn()  { echo -e "${YELLOW}[!]${NC} $1"; }
error() { echo -e "${RED}[x]${NC} $1"; exit 1; }

DO_SYSTEM=1
PYTHON_BIN=""
while [ $# -gt 0 ]; do
    case "$1" in
        --skip-system|--no-apt)  DO_SYSTEM=0 ;;
        --python)                PYTHON_BIN="$2"; shift ;;
        -h|--help)               sed -n '2,15p' "$0"; exit 0 ;;
        *) error "Unknown option: $1" ;;
    esac
    shift
done

# ── 0. Sanity: OS + architecture ───────────────────────────────────────
UNAME_S="$(uname -s)"
if [ "$UNAME_S" != "Linux" ]; then
    error "Only Linux is supported (detected: $UNAME_S)."
fi

ARCH="$(uname -m)"
case "$ARCH" in
    x86_64|amd64)   ARCH_OK=1; ARCH_CANON="x86_64" ;;
    aarch64|arm64)  ARCH_OK=1; ARCH_CANON="aarch64" ;;
    *)  error "Unsupported architecture: $ARCH (need x86_64 or aarch64)." ;;
esac

# ── 1. System dependencies ─────────────────────────────────────────────
# Detect the distro's package manager and install the same conceptual set
# of packages on each. Pkg names are normalised per-distro where needed.
install_system_deps() {
    SUDO=""
    [ "$(id -u)" -ne 0 ] && SUDO="sudo"

    if command -v apt-get >/dev/null 2>&1; then
        info "Installing system dependencies via apt (Debian/Ubuntu)..."
        $SUDO apt-get update -qq
        $SUDO apt-get install -y -qq \
            python3 python3-venv python3-pip python3-dev \
            build-essential pkg-config \
            libsctp-dev lksctp-tools \
            libffi-dev libssl-dev \
            iperf3 net-tools iproute2
    elif command -v dnf >/dev/null 2>&1; then
        info "Installing system dependencies via dnf (Fedora/RHEL/Rocky)..."
        $SUDO dnf install -y \
            python3 python3-pip python3-devel \
            gcc gcc-c++ make pkgconfig \
            lksctp-tools lksctp-tools-devel \
            libffi-devel openssl-devel \
            iperf3 net-tools iproute
    elif command -v yum >/dev/null 2>&1; then
        info "Installing system dependencies via yum (RHEL/CentOS)..."
        $SUDO yum install -y \
            python3 python3-pip python3-devel \
            gcc gcc-c++ make pkgconfig \
            lksctp-tools lksctp-tools-devel \
            libffi-devel openssl-devel \
            iperf3 net-tools iproute
    elif command -v zypper >/dev/null 2>&1; then
        info "Installing system dependencies via zypper (openSUSE/SLES)..."
        $SUDO zypper --non-interactive install \
            python3 python3-pip python3-devel \
            gcc gcc-c++ make pkg-config \
            lksctp-tools libsctp-devel \
            libffi-devel libopenssl-devel \
            iperf net-tools iproute2 || true
    elif command -v pacman >/dev/null 2>&1; then
        info "Installing system dependencies via pacman (Arch/Manjaro)..."
        $SUDO pacman -Sy --needed --noconfirm \
            python python-pip \
            base-devel pkgconf \
            lksctp-tools \
            libffi openssl \
            iperf3 net-tools iproute2
    elif command -v apk >/dev/null 2>&1; then
        info "Installing system dependencies via apk (Alpine)..."
        $SUDO apk add --no-cache \
            python3 py3-pip python3-dev \
            build-base pkgconfig \
            lksctp-tools lksctp-tools-dev \
            libffi-dev openssl-dev \
            iperf3 net-tools iproute2
    else
        warn "No known package manager (apt/dnf/yum/zypper/pacman/apk) found."
        warn "Install these manually before continuing:"
        warn "  python3 (>=3.10) + venv + pip, python3 headers, a C toolchain,"
        warn "  libsctp (runtime + headers), libffi, openssl, iperf3, iproute2."
        return 0
    fi
}

if [ "$DO_SYSTEM" -eq 1 ]; then
    install_system_deps
fi

# ── 2. Pick a Python interpreter ───────────────────────────────────────
if [ -z "$PYTHON_BIN" ]; then
    for cand in python3.12 python3.11 python3.10 python3; do
        if command -v "$cand" >/dev/null 2>&1; then
            PYTHON_BIN="$(command -v "$cand")"
            break
        fi
    done
fi
[ -z "$PYTHON_BIN" ] && error "No Python 3 interpreter found."

PY_VER=$("$PYTHON_BIN" -c 'import sys; print("%d.%d" % sys.version_info[:2])')
PY_MAJOR=$("$PYTHON_BIN" -c 'import sys; print(sys.version_info[0])')
PY_MINOR=$("$PYTHON_BIN" -c 'import sys; print(sys.version_info[1])')
if [ "$PY_MAJOR" -lt 3 ] || { [ "$PY_MAJOR" -eq 3 ] && [ "$PY_MINOR" -lt 10 ]; }; then
    error "Python >= 3.10 required (found $PY_VER)."
fi
info "Using Python $PY_VER at $PYTHON_BIN"

# ── 3. Create venv ─────────────────────────────────────────────────────
VENV_DIR="$SCRIPT_DIR/.venv"
if [ ! -d "$VENV_DIR" ]; then
    info "Creating virtualenv at $VENV_DIR ..."
    "$PYTHON_BIN" -m venv "$VENV_DIR"
else
    info "Reusing existing virtualenv at $VENV_DIR"
fi

VPY="$VENV_DIR/bin/python"
"$VPY" -m pip install --upgrade pip setuptools wheel >/dev/null

# ── 4. Python packages ─────────────────────────────────────────────────
# The pure-Python deps (fastapi/uvicorn/starlette/anyio/jinja2/requests/…)
# are bundled under libs/ and loaded via PYTHONPATH. The compiled
# packages that can't ride as a cross-version wheel are listed in
# build/requirements.txt — single source of truth, also consumed by
# build/Dockerfile.
REQ_FILE="$SCRIPT_DIR/build/requirements.txt"
if [ ! -f "$REQ_FILE" ]; then
    error "build/requirements.txt not found — repo checkout looks incomplete"
fi
info "Installing compiled Python packages from build/requirements.txt for Python $PY_VER..."
"$VPY" -m pip install -r "$REQ_FILE"

# anyio (pure python, in libs/) needs exceptiongroup on Python < 3.11.
if [ "$PY_MINOR" -lt 11 ]; then
    "$VPY" -m pip install "exceptiongroup>=1.2.0"
fi

# ── 4b. Kernel tuning for 10k-UE / 10 Gbps workloads ──────────────────
# SA Tester drives up to 10k SCTP associations × multi-Gbps GTP-U traffic.
# Stock Linux ships with conservative socket-buffer caps — e.g.
# net.core.rmem_max = 212 KB — which silently clamps any setsockopt(
# SO_RCVBUF, 2 MB) we do at runtime. We've seen this show up as
# intermittent SCTP aborts under burst load because the kernel recv
# buffer fills before the app can drain.
#
# Drop a sysctl file + a limits file so the tuning persists across
# reboots. Skip with SKIP_KERNEL_TUNING=1 if the host is already
# tuned by a config-management tool (ansible/puppet/etc).
if [ "${SKIP_KERNEL_TUNING:-0}" = "0" ]; then
    # Ensure the SCTP module is loaded *now* and on every future boot
    # *before* systemd-sysctl runs. Without the modules-load.d entry,
    # /proc/sys/net/sctp/ doesn't exist when systemd-sysctl walks
    # /etc/sysctl.d/ at early boot, and every net.sctp.* line below
    # silently no-ops until the module is later autoloaded — at which
    # point the keys appear with kernel defaults, not our values.
    if ! lsmod | grep -q "^sctp "; then
        info "Loading SCTP kernel module"
        sudo modprobe sctp 2>/dev/null || warn "modprobe sctp failed — net.sctp.* sysctls won't apply this boot"
    fi
    MODLOAD_FILE=/etc/modules-load.d/mmt-tester-sctp.conf
    if [ ! -f "$MODLOAD_FILE" ] || ! grep -q '^sctp$' "$MODLOAD_FILE" 2>/dev/null; then
        echo sctp | sudo tee "$MODLOAD_FILE" >/dev/null && \
            info "Installed $MODLOAD_FILE (sctp autoload at boot)"
    fi

    info "Installing kernel-tuning sysctls (/etc/sysctl.d/99-mmt-tester.conf)"
    SYSCTL_FILE=/etc/sysctl.d/99-mmt-tester.conf
    sudo tee "$SYSCTL_FILE" >/dev/null <<'SYSCTL'
# Generated by mmt_studio_core_tester/install.sh — do not edit by hand.
# Re-run install.sh to refresh, or delete this file to revert to distro
# defaults (then `sysctl --system` to apply).

# ── Generic socket buffers — the hard cap that clamps SO_{SND,RCV}BUF ──
net.core.rmem_max     = 16777216
net.core.wmem_max     = 16777216
net.core.rmem_default =  4194304
net.core.wmem_default =  4194304
net.core.optmem_max   =  4194304

# ── SCTP (kernel's own tunable, separate from generic sockets) ──
net.sctp.sctp_rmem            = 4096 4194304 16777216
net.sctp.sctp_wmem            = 4096 4194304 16777216
net.sctp.sctp_mem             = 786432 1048576 1572864
net.sctp.association_max_retrans = 10
net.sctp.path_max_retrans     = 5
net.sctp.rto_min              = 1000
net.sctp.rto_max              = 60000
net.sctp.hb_interval          = 30000

# ── TCP (iperf3 streams) ──
net.ipv4.tcp_rmem             = 4096 87380 16777216
net.ipv4.tcp_wmem             = 4096 65536 16777216
net.ipv4.tcp_mem              = 786432 1048576 1572864
net.ipv4.tcp_mtu_probing      = 1

# ── UDP (GTP-U + iperf3 -u) ──
net.ipv4.udp_rmem_min         = 8192
net.ipv4.udp_wmem_min         = 8192
net.ipv4.udp_mem              = 786432 1048576 1572864

# ── Packet backlogs (keep bursts from being dropped) ──
net.core.netdev_max_backlog   = 30000
net.core.netdev_budget        = 600
net.core.somaxconn            = 4096

# ── Connection table (10k UEs × many flows) ──
net.ipv4.ip_local_port_range  = 10000 65535
net.ipv4.tcp_max_tw_buckets   = 1000000
SYSCTL
    # tcp_congestion_control = bbr only if the module is loadable — many
    # stripped-down kernels don't ship it. Probe and append only on match.
    if grep -qw bbr /proc/sys/net/ipv4/tcp_available_congestion_control 2>/dev/null; then
        echo "net.ipv4.tcp_congestion_control = bbr" | sudo tee -a "$SYSCTL_FILE" >/dev/null
    fi

    info "Applying sysctls now (no reboot required)"
    sudo sysctl --system >/dev/null || warn "sysctl --system returned non-zero (some keys may not apply on this kernel)"

    info "Installing fd/proc limits (/etc/security/limits.d/99-mmt-tester.conf)"
    sudo tee /etc/security/limits.d/99-mmt-tester.conf >/dev/null <<'LIMITS'
# Generated by mmt_studio_core_tester/install.sh — do not edit by hand.
# Each UE uses >= 1 TUN fd + 1 GTP-U socket; at 10k UEs we need room.
*  soft  nofile  1048576
*  hard  nofile  1048576
*  soft  nproc   65535
*  hard  nproc   65535
LIMITS
    warn "limits apply on next login / systemd unit start — existing shells keep their old ulimit"
else
    warn "SKIP_KERNEL_TUNING=1 set — skipping sysctl + limits install"
fi


# ── 5. Smoke-test imports ──────────────────────────────────────────────
# The pure-Python deps live in libs/ and are picked up via PYTHONPATH,
# exactly like run.sh does it.
info "Verifying installation..."
PYTHONPATH="$SCRIPT_DIR/libs:$SCRIPT_DIR/libs/pycrate:$SCRIPT_DIR/libs/sa_crypto" \
"$VPY" - <<'PY'
import importlib, sys
mods = ["fastapi", "uvicorn", "starlette", "pydantic", "anyio",
        "cryptography", "jinja2", "sctp", "pyroute2"]
missing = []
for m in mods:
    try: importlib.import_module(m)
    except Exception as e: missing.append(f"{m}: {e}")
if missing:
    print("FAIL:"); [print(" ", x) for x in missing]; sys.exit(1)
print("All core modules import OK.")
PY

# ── 6. Reclaim root-owned paths from prior sudo-first runs ─────────────
# Older run.sh versions sudo'd first and then created these as root, so a
# freshly-cloned checkout may already have root-owned data/ and .venv/.
# Reclaim them here so the rest of install (and future runs) work as the
# invoking user.
for _dir in "$SCRIPT_DIR/data" "$SCRIPT_DIR/.venv"; do
    if [ -e "$_dir" ] && [ ! -w "$_dir" ]; then
        echo "[+] Reclaiming root-owned $_dir (leftover from a previous sudo run)"
        sudo chown -R "$(id -u):$(id -g)" "$_dir" || {
            echo "[!] Failed to chown $_dir — run manually: sudo chown -R \$USER:\$USER $_dir" >&2
            exit 1
        }
    fi
done

# Runtime state:
#   - SQLite DB at data/sa_tester.db  → auto-created by src/db/engine.py
#   - Logs at /var/log/sa_tester/     → auto-created by run.sh (phase 2)
#   - Test results                    → stored in the SQLite DB, not on disk
# Nothing to mkdir here.

echo ""
info "Install complete."
echo ""
echo "  Run:    sudo ./run.sh"
echo "  Venv:   $VENV_DIR"
echo "  Python: $PY_VER"
echo ""
