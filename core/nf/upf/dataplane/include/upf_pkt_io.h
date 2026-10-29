/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_pkt_io.h — Packet I/O: GTP-U socket (N3) + TUN interface (N6)
 *
 * N3 interface (gNB side):
 *   - UDP socket on port 2152 — receives GTP-U encapsulated UL packets
 *   - Sends GTP-U encapsulated DL packets to gNB
 *
 * N6 interface (DNN/internet side):
 *   - TUN device — receives raw IP DL packets from kernel routing
 *   - Writes decapsulated UL IP packets for kernel routing to DNN
 *
 * Processing loop (single-threaded for Phase 2):
 *   - select() on both GTP-U socket and TUN fd
 *   - UL: GTP-U recv → decap → classify(PDR) → QER → URR → FAR → TUN write
 *   - DL: TUN read → classify(PDR) → QER → URR → FAR → GTP-U encap → send
 */

#ifndef UPF_PKT_IO_H
#define UPF_PKT_IO_H

#include <stdint.h>
#include "upf_types.h"

/* Configuration for packet I/O */
typedef struct {
    const char *n3_bind_addr;   /* N3 (GTP-U) bind address, e.g. "0.0.0.0" */
    uint16_t    n3_port;        /* GTP-U port (default 2152) */
    const char *tun_name;       /* TUN device name, e.g. "upfgtp" */
    const char *tun_addr;       /* TUN IP address, e.g. "10.45.0.1" */
    const char *tun_mask;       /* TUN netmask, e.g. "255.255.0.0" */
} upf_pkt_io_cfg_t;

/* Initialize packet I/O: create GTP-U socket and TUN device.
 * Returns 0 on success, -1 on error. */
int upf_pkt_io_init(const upf_pkt_io_cfg_t *cfg);

/* Run the packet processing loop (blocking).
 * Call from a dedicated thread. Returns on error or after upf_pkt_io_stop(). */
int upf_pkt_io_run(void);

/* Signal the processing loop to stop. */
void upf_pkt_io_stop(void);

/* Clean up I/O resources. */
void upf_pkt_io_cleanup(void);

/* Get I/O stats.
 *
 * Drop classification:
 *   ul_dropped / dl_dropped   — total dropped (includes all reasons below)
 *   ul_no_session / dl_no_session
 *     — unknown TEID (UL) or no session for dst UE-IP (DL). Typically
 *       harmless post-teardown stragglers. Distinct from "active-session"
 *       drops which signal a real bottleneck.
 *   ul_metered / dl_metered   — dropped by QER/AMBR rate limiting
 *   Active-session drops can be derived as:
 *     active = dropped - no_session - metered
 */
typedef struct {
    uint64_t ul_pkts;       /* UL packets processed */
    uint64_t ul_bytes;      /* UL bytes (inner IP) */
    uint64_t dl_pkts;       /* DL packets processed */
    uint64_t dl_bytes;      /* DL bytes (inner IP) */
    uint64_t ul_dropped;    /* UL packets dropped (total — see classification above) */
    uint64_t dl_dropped;    /* DL packets dropped (total) */
    uint64_t ul_no_session; /* UL dropped: unknown TEID / session gone (teardown race) */
    uint64_t dl_no_session; /* DL dropped: no session for dst UE-IP */
    uint64_t ul_metered;    /* UL dropped by QER MBR / AMBR metering */
    uint64_t dl_metered;    /* DL dropped by QER MBR / AMBR metering */
    uint64_t gtpu_errors;   /* GTP-U decode errors */
    uint32_t _debug_last_unknown_teid;    /* last TEID that failed lookup */
    uint32_t _debug_last_registered_teid; /* last TEID registered */
} upf_io_stats_t;

/* Get current I/O stats (lock-free read). */
void upf_pkt_io_get_stats(upf_io_stats_t *out);

/* Reverse-map management — install / release at PFCP §7.5.2 / §7.5.6.
 *
 * The add functions are forward-declared here so the dp_api shim can
 * call them without importing the static internals. The del companions
 * are required for §7.5.6 ("delete an existing PFCP session at the UP
 * function") to actually release the F-TEID (§5.5.1) and UE IP
 * (§8.2.62) resources tied to that session — without them the hashes
 * leak and saturate at MAX_TEID_MAP. Returns 0 on success, -1 on
 * uninitialised table or absent key (idempotent). */
int upf_pkt_io_add_teid_map(uint32_t teid, const char *imsi, uint8_t pdu_session_id);
int upf_pkt_io_add_ueip_map(uint32_t ue_addr, const char *imsi, uint8_t pdu_session_id);
int upf_pkt_io_del_teid_map(uint32_t teid);
int upf_pkt_io_del_ueip_map(uint32_t ue_addr);

/* Flush buffered DL packets for a session's FAR.
 * Called from upf_dp_update_far when action transitions BUFF→FORW.
 * TS 29.244 §5.2.1: buffered packets are forwarded once the DL FAR
 * has valid tunnel info. Returns number of packets flushed. */
int upf_pkt_io_flush_dl_buf(upf_session_t *sess, upf_far_t *far);

#endif /* UPF_PKT_IO_H */
