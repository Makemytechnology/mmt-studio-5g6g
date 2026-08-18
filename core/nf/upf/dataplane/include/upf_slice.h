/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_slice.h — Per-slice UPF data plane context
 *
 * TS 23.501 §5.15.4: Each S-NSSAI can have its own UPF instance.
 * This module provides 3 independent slice channels:
 *   Slice 0: eMBB  (SST=1) — Enhanced Mobile Broadband
 *   Slice 1: URLLC (SST=2) — Ultra-Reliable Low-Latency Communications
 *   Slice 2: mIoT  (SST=3) — Massive IoT
 *
 * Each slice has independent:
 *   - Session table (rte_hash)
 *   - QoS meter arrays
 *   - I/O statistics
 *   - TUN interface (N6 egress)
 *   - TEID/UE-IP reverse lookup maps
 *
 * Shared across slices:
 *   - DPDK EAL (single init)
 *   - GTP-U socket (port 2152, demux by TEID → slice)
 *   - Hugepage memory pool
 */

#ifndef UPF_SLICE_H
#define UPF_SLICE_H

#include <stdint.h>
#include <stdbool.h>
#include <rte_hash.h>

#include "upf_types.h"
#include "upf_qos_meter.h"
#include "upf_pkt_io.h"

#define UPF_MAX_SLICES          3
#define UPF_SLICE_MAX_SESSIONS  1024   /* per slice (reduced for hugepage budget) */

/* Standard slice types (TS 23.501 §5.15.2.2) */
#define UPF_SST_EMBB   1
#define UPF_SST_URLLC  2
#define UPF_SST_MIOT   3

/* Per-slice context — completely independent pipeline */
typedef struct {
    uint8_t  slice_id;                  /* 0, 1, 2 */
    uint8_t  sst;                       /* S-NSSAI SST (1=eMBB, 2=URLLC, 3=mIoT) */
    char     name[32];                  /* "eMBB", "URLLC", "mIoT" */
    bool     active;                    /* slice initialized */

    /* Session table (rte_hash + pre-allocated pool) */
    struct rte_hash *session_hash;
    upf_session_t   *session_pool;
    uint32_t         session_count;

    /* Per-slice I/O stats */
    upf_io_stats_t   io_stats;

    /* Per-slice TUN interface (N6 egress) */
    int              tun_fd;
    char             tun_name[16];      /* "upf_embb", "upf_urllc", "upf_miot" */

    /* Per-slice TEID → session reverse map */
    struct rte_hash *teid_hash;

    /* Per-slice UE-IP → session reverse map */
    struct rte_hash *ueip_hash;

    /* Per-slice QoS meter arrays (allocated separately, not in struct) */
    upf_qer_meter_t     *qer_meters;       /* [UPF_SLICE_MAX_SESSIONS][MAX_QER] */
    upf_session_meter_t *session_meters;    /* [UPF_SLICE_MAX_SESSIONS] */

} upf_slice_ctx_t;

/* ── Lifecycle ── */

/* Initialize a slice context. Call after EAL init.
 * Returns 0 on success, -1 on error. */
int upf_slice_init(uint8_t slice_id, uint8_t sst, const char *name);

/* Destroy a slice context and free resources. */
void upf_slice_destroy(uint8_t slice_id);

/* Get slice context by ID. Returns NULL if not initialized. */
upf_slice_ctx_t *upf_slice_get(uint8_t slice_id);

/* Find slice by SST. Returns NULL if no slice for this SST. */
upf_slice_ctx_t *upf_slice_find_by_sst(uint8_t sst);

/* Get all active slice contexts. Returns count. */
int upf_slice_get_all(upf_slice_ctx_t **out, int max);

/* ── Per-slice session CRUD ── */

/* Create a session in this slice. Returns NULL on duplicate / over-cap. */
upf_session_t *upf_slice_session_create(upf_slice_ctx_t *s,
                                         const char *imsi, uint8_t pdu_session_id);

/* Look up a session in this slice. Returns NULL if not present / inactive. */
upf_session_t *upf_slice_session_get(upf_slice_ctx_t *s,
                                      const char *imsi, uint8_t pdu_session_id);

/* Delete a session from this slice. Returns 0 on success, -1 if absent. */
int upf_slice_session_delete(upf_slice_ctx_t *s,
                              const char *imsi, uint8_t pdu_session_id);

/* Register TEID → (imsi, pdu_session_id) in this slice's UL reverse map. */
int upf_slice_register_teid(upf_slice_ctx_t *s, uint32_t teid,
                             const char *imsi, uint8_t pdu_session_id);

/* Register UE-IP → (imsi, pdu_session_id) in this slice's DL reverse map. */
int upf_slice_register_ueip(upf_slice_ctx_t *s, uint32_t ue_addr,
                              const char *imsi, uint8_t pdu_session_id);

/* ── Session routing ── */

/* Find which slice a session belongs to (by IMSI lookup across all slices).
 * Returns slice context or NULL. */
upf_slice_ctx_t *upf_slice_find_session(const char *imsi, uint8_t pdu_session_id);

/* Find slice by TEID (for UL GTP-U demux).
 * Returns slice context and sets *out_sess. */
upf_slice_ctx_t *upf_slice_find_by_teid(uint32_t teid, upf_session_t **out_sess);

/* Find slice by UE-IP (for DL TUN demux).
 * Returns slice context and sets *out_sess. */
upf_slice_ctx_t *upf_slice_find_by_ueip(uint32_t ue_addr, upf_session_t **out_sess);

/* ── Stats ── */

/* Copy per-slice I/O counters into *out. Zeroed out if slice inactive. */
void upf_slice_get_stats(uint8_t slice_id, upf_io_stats_t *out);

#endif /* UPF_SLICE_H */
