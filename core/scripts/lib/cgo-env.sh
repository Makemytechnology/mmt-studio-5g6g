# shellcheck shell=bash
# Single source of truth for CGO/DPDK build + load-time environment.
# Sourced by run.sh; safe to source even when artifacts haven't been
# built yet — the optional exports just won't be set.
#
# Caller must set SCRIPT_DIR (absolute path to repo root) before sourcing.

DPDK_DIR="${DPDK_DIR:-$SCRIPT_DIR/libs/dpdk-25.11}"
UPF_DIR="${UPF_DIR:-$SCRIPT_DIR/nf/upf/dataplane}"
UPF_SO="${UPF_SO:-$UPF_DIR/libupf_dp.so}"

export CGO_ENABLED=1

if [ -f "$UPF_SO" ]; then
    export CGO_CFLAGS="-I$UPF_DIR/include"
    export CGO_LDFLAGS="-L$UPF_DIR -L$DPDK_DIR/build/lib -lupf_dp"
fi

if [ -d "$DPDK_DIR/build/lib" ]; then
    export LD_LIBRARY_PATH="$DPDK_DIR/build/lib:${LD_LIBRARY_PATH:-}"
fi
if [ -f "$UPF_SO" ]; then
    export LD_LIBRARY_PATH="$UPF_DIR:${LD_LIBRARY_PATH:-}"
fi
if [ -d "$DPDK_DIR/build/meson-private" ]; then
    export PKG_CONFIG_PATH="$DPDK_DIR/build/meson-private:${PKG_CONFIG_PATH:-}"
fi
