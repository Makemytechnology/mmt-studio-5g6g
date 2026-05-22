# UPF Data Plane — Makefile
#
# Builds libupf_dp.so linked against DPDK.
# DPDK must be built first (meson setup build && ninja -C build).
#
# Build modes:
#   make              — Socket/TUN I/O (legacy, works without hugepages)
#   make FAST_PATH=1  — DPDK PMD I/O (zero-copy, requires hugepages + PMD)

DPDK_DIR  := ../../../libs/dpdk-25.11
PKG_CONFIG_PATH := $(DPDK_DIR)/build/meson-uninstalled

CC        := gcc
# Session cap, mbuf pool, and RX/TX ring sizes are all runtime-
# configurable now (set from Python via upf_dp_set_max_sessions() and
# upf_dp_set_pmd_tuning() before upf_dp_init()). The Makefile vars
# below are only the compile-time *defaults* baked in for the case
# where the DB is unreachable. Changes to these values in the GUI do
# NOT require a rebuild; run.sh no longer has a rebuild trigger.
UPF_MAX_SESSIONS   ?= 4096
UPF_MBUF_POOL_SIZE ?= 8192
UPF_RX_RING_SIZE   ?= 1024
UPF_TX_RING_SIZE   ?= 1024
CFLAGS    := -O2 -Wall -Wextra -fPIC -Iinclude
CFLAGS    += -DUPF_MAX_SESSIONS=$(UPF_MAX_SESSIONS)
CFLAGS    += -DMBUF_POOL_SIZE=$(UPF_MBUF_POOL_SIZE)
CFLAGS    += -DRX_RING_SIZE=$(UPF_RX_RING_SIZE)
CFLAGS    += -DTX_RING_SIZE=$(UPF_TX_RING_SIZE)
# Sanitize DPDK pkg-config Cflags: even with -Dplatform=generic, DPDK's
# libdpdk.pc still ships `-march=native -mrtm`, which would make gcc emit
# vpxor / AVX prologues that SIGILL on CPUs without AVX (e.g. VirtualBox /
# Hyper-V guests with masked CPUID). Strip those, then pin our own portable
# baseline. Defence in depth: don't rely on later -march winning.
DPDK_CFLAGS := $(shell PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --cflags libdpdk | sed -E 's/(^| )-march=[^ ]+/\1/g; s/(^| )-mrtm/\1/g')
CFLAGS    += $(DPDK_CFLAGS)
CFLAGS    += -march=x86-64 -mno-avx -mno-avx2 -mno-avx512f
LDFLAGS   := -shared
LDFLAGS   += $(shell PKG_CONFIG_PATH=$(PKG_CONFIG_PATH) pkg-config --libs libdpdk)
# rpath is relative to the .so's location in nf/upf/dataplane/
# (3 levels up from nf/upf/dataplane/ to the repo root).
LDFLAGS   += '-Wl,-rpath,$$ORIGIN/../../../libs/dpdk-25.11/build/lib'

# Common sources (shared between both modes)
COMMON_SRCS := src/upf_sdf_parser.c src/upf_session_table.c src/upf_gtpu.c \
               src/upf_qos_meter.c src/upf_classifier.c src/upf_slice.c src/upf_dp_api.c \
               src/upf_report.c

# Select I/O mode
ifdef FAST_PATH
  CFLAGS += -DUPF_FAST_PATH
  IO_SRC := src/upf_pkt_io_dpdk.c
  $(info Building with DPDK PMD fast path (rte_eth_rx/tx_burst, rte_mbuf, zero-copy))
else
  IO_SRC := src/upf_pkt_io.c
  $(info Building with socket/TUN I/O (legacy mode))
endif

SRCS      := $(COMMON_SRCS) $(IO_SRC)
OBJS      := $(SRCS:.c=.o)

# .so lives next to the C sources so cgo_bridge_linux.go links to a
# stable, single path and rebuilds clobber the previous artifact in place.
TARGET := libupf_dp.so

.PHONY: all clean

all: $(TARGET)

$(TARGET): $(OBJS)
	$(CC) $(OBJS) -o $@ $(LDFLAGS)

src/%.o: src/%.c
	$(CC) $(CFLAGS) -c $< -o $@

# Force rebuild when CFLAGS in this Makefile change, even if the .c source
# itself hasn't been touched. Without this, an edit to -march/-mno-avx flags
# would not invalidate stale .o files, and the resulting .so would carry
# pre-change codegen.
$(OBJS): Makefile

clean:
	rm -f src/*.o $(TARGET)
