/* Copyright (c) 2026 MakeMyTechnology. All rights reserved.
 *
 * upf_report.c — MPMC rte_ring producer/consumer for UPF → SMF reports.
 *
 * Authoritative spec: TS 29.244 v19.5.0 §7.5.8 "PFCP Session Report
 * Request" (local PDF: codecs/tlv-3gpp-pfcp/standards/
 * ts_129244v190500p.pdf). API documented in upf_report.h.
 *
 * Implementation notes:
 *   - rte_ring is MPMC (multi-producer multi-consumer); we use it as
 *     MPSC in practice (many DPDK lcores produce, one Go goroutine
 *     drains). Single-producer/single-consumer hints (RING_F_SP_ENQ /
 *     RING_F_SC_DEQ) are NOT set — keep the door open for additional
 *     producers (URR / ErrInd land later from different code paths).
 *   - Records are heap-allocated (malloc) by the producer, the
 *     pointer is enqueued. Consumer copies out and frees. DLDR rate
 *     is low (one per RRC_INACTIVE transition per session), so the
 *     malloc cost is negligible vs the value of avoiding a dedicated
 *     mempool.
 *   - On enqueue ring-full, we malloc-free the record and increment
 *     the drop counter. The UP hot path MUST NOT block on the CP
 *     ring (TS 29.244 §7.5.8 says the report is best-effort, and
 *     the spec gives no upper bound on how the UP signals).
 *   - Init is idempotent: a second init() returns 0 without creating
 *     a new ring (so callers don't have to gate it).
 */

#include "upf_report.h"

#include <rte_ring.h>
#include <rte_log.h>
#include <stdatomic.h>
#include <stdlib.h>
#include <string.h>

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

/* Default ring size if caller passes 0 — must be a power of 2 per
 * DPDK rte_ring semantics. 1024 slots is enough headroom for the
 * 10-second consumer tick (nf/upf/report.go consumeLoop) to drain
 * even at a high burst rate of DLDR/Usage/ErrInd events. */
#define UPF_REPORT_RING_SIZE_DEFAULT 1024

static struct rte_ring *g_report_ring;
static _Atomic uint64_t g_dropped;

int upf_report_init(unsigned ring_size)
{
    if (g_report_ring != NULL) {
        return 0; /* idempotent */
    }
    if (ring_size == 0) {
        ring_size = UPF_REPORT_RING_SIZE_DEFAULT;
    }
    /* rte_ring_create requires a unique name and the size be a power
     * of two. The flags=0 leaves us MPMC. SOCKET_ID_ANY: this ring
     * isn't tied to a NUMA node — the consumer is the Go scheduler
     * which doesn't have an lcore. */
    g_report_ring = rte_ring_create("upf_report_ring",
                                    ring_size,
                                    -1 /* SOCKET_ID_ANY */,
                                    0  /* MPMC */);
    if (g_report_ring == NULL) {
        RTE_LOG(ERR, UPF,
                "upf_report_init: rte_ring_create failed (size=%u) — "
                "DPDK EAL initialised? TS 29.244 §7.5.8 reports will be dropped\n",
                ring_size);
        return -1;
    }
    atomic_store_explicit(&g_dropped, 0, memory_order_relaxed);
    RTE_LOG(INFO, UPF,
            "upf_report_init: report ring created, size=%u (TS 29.244 §7.5.8)\n",
            ring_size);
    return 0;
}

void upf_report_cleanup(void)
{
    if (g_report_ring == NULL) {
        return;
    }
    /* Drain anything left so we don't leak heap-allocated records. */
    void *p;
    while (rte_ring_dequeue(g_report_ring, &p) == 0) {
        free(p);
    }
    rte_ring_free(g_report_ring);
    g_report_ring = NULL;
}

int upf_report_enqueue(const upf_report_t *r)
{
    if (g_report_ring == NULL || r == NULL) {
        return -1;
    }
    upf_report_t *copy = malloc(sizeof(*copy));
    if (copy == NULL) {
        atomic_fetch_add_explicit(&g_dropped, 1, memory_order_relaxed);
        return -1;
    }
    memcpy(copy, r, sizeof(*copy));
    /* rte_ring_enqueue returns 0 on success, -ENOBUFS on full. We
     * treat both ring-full and malloc-fail the same way (drop +
     * count) since the hot path MUST NOT block on the CP. */
    if (rte_ring_enqueue(g_report_ring, copy) != 0) {
        free(copy);
        atomic_fetch_add_explicit(&g_dropped, 1, memory_order_relaxed);
        return -1;
    }
    return 0;
}

unsigned upf_report_drain(upf_report_t *buf, unsigned max)
{
    if (g_report_ring == NULL || buf == NULL || max == 0) {
        return 0;
    }
    unsigned n = 0;
    void *ptrs[64];
    while (n < max) {
        unsigned want = max - n;
        if (want > 64) {
            want = 64;
        }
        unsigned got = rte_ring_dequeue_burst(g_report_ring, ptrs,
                                              want, NULL);
        if (got == 0) {
            break;
        }
        for (unsigned i = 0; i < got && n < max; i++) {
            memcpy(&buf[n], ptrs[i], sizeof(buf[n]));
            free(ptrs[i]);
            n++;
        }
    }
    return n;
}

uint64_t upf_report_dropped(void)
{
    return atomic_load_explicit(&g_dropped, memory_order_relaxed);
}
