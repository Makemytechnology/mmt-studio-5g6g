/* Copyright (c) 2026 MakeMyTechnology. All rights reserved.
 *
 * upf_report.h — extensible UPF → SMF report framework.
 *
 * Authoritative spec: TS 29.244 v19.5.0 §7.5.8 "PFCP Session Report
 * Request" (PDF: codecs/tlv-3gpp-pfcp/standards/ts_129244v190500p.pdf).
 *
 * §7.5.8 defines the report-carrying message the UP function (UPF)
 * sends to the CP function (SMF) whenever an event worth reporting
 * occurs on the dataplane. The message is a container — the actual
 * report type is one of the IEs in §7.5.8.2 … §7.5.8.6:
 *
 *   §7.5.8.2 Downlink Data Report       (DLDR) — DL packet arrived,
 *                                                 no AN tunnel info
 *                                                 (TS 23.502 §4.2.3.3
 *                                                 step 2a Data Notify)
 *   §7.5.8.3 Usage Report                (URR) — charging / volume /
 *                                                 time / event
 *                                                 thresholds or quotas
 *                                                 (TS 29.244 §5.2.2.3,
 *                                                 §8.2.41 triggers)
 *   §7.5.8.4 Error Indication Report          — GTP-U ERRIND from peer
 *   §7.5.8.5 TSC Management Information       — TSC / TSN feedback
 *   §7.5.8.6 Session Report                   — generic (slice-reload, …)
 *
 * All of these cross the C → Go boundary with the same shape:
 * identify the session (IMSI + PDU Session ID + SEID), carry a
 * type-tagged payload, land on the Go consumer goroutine. The
 * plumbing is shared; adding a new type is (1) new enum value,
 * (2) new payload struct in the union, (3) new handler on the Go
 * side — no hot-path code changes.
 *
 * Transport: an rte_ring MPMC queue created at upf_dp_init time.
 * Producers (classifier / metered-PDR / error-indication handler /
 * …) run on DPDK lcore threads and enqueue lockless. A single Go
 * consumer goroutine drains in batches via upf_report_drain().
 * Overflow drops are counted (upf_report_dropped) rather than
 * blocking — the hot path MUST NOT stall behind the control plane.
 *
 * Why rte_ring instead of a direct C→Go cgo callback:
 *   - DPDK lcore threads don't have Go runtime state. Calling Go
 *     from them via //export works but is fragile (scheduler can't
 *     preempt DPDK busy-polling; long Go calls stall packets).
 *   - rte_ring is MPMC-lockless, single-word atomics on x86, and
 *     already linked by every build.
 *   - Matches the Go → C pattern (cgo_bridge_linux.go dispatch
 *     channel) which already forces async hand-off onto a
 *     dedicated thread.
 */

#ifndef UPF_REPORT_H
#define UPF_REPORT_H

#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* IMSI max (15 BCD digits + null terminator). */
#define UPF_IMSI_MAX 16

/* Report types. Stable integer values so the Go side can mirror
 * them without a marshalling layer. Extend by appending — never
 * renumber. */
enum upf_report_type {
    UPF_REPORT_NONE     = 0,
    UPF_REPORT_DLDR     = 1, /* §7.5.8.2 Downlink Data Report */
    UPF_REPORT_USAGE    = 2, /* §7.5.8.3 Usage Report (URR) */
    UPF_REPORT_ERRIND   = 3, /* §7.5.8.4 Error Indication Report */
    UPF_REPORT_TSC      = 4, /* §7.5.8.5 TSC Management Information */
    UPF_REPORT_SESSREP  = 5, /* §7.5.8.6 Session Report */
};

/* §7.5.8.2 Downlink Data Report payload.
 * Extra IEs we don't carry today (§7.5.8.2 table):
 *   - Downlink Data Service Information (IE 45, optional)
 *   - DL Buffering Duration (IE 47, optional)
 *   - DL Buffering Suggested Packet Count (IE 48, optional)
 *   - DL Low Priority Traffic Marker
 * Add them to this struct when the C classifier starts populating
 * them; the union memory layout stays backwards-compatible because
 * the struct tag (upf_report.type) distinguishes payload types. */
typedef struct upf_dldr_payload {
    uint8_t qfi;  /* QoS Flow ID from the matching PDR */
    uint8_t dscp; /* DSCP from TOS (v4) / TC (v6) for §5.4.3 Paging
                   * Policy Differentiation (TS 23.501) */
    uint8_t pad[6];
} upf_dldr_payload_t;

/* §7.5.8.3 Usage Report payload — TS 29.244 §8.2.41 Usage Report
 * Trigger bits drive 'trigger'; §8.2.20 Volume Measurement fills
 * vol_*; §8.2.21 Duration Measurement fills duration_s. */
typedef struct upf_usage_payload {
    uint32_t urr_id;     /* §8.2.11 URR ID */
    uint32_t trigger;    /* §8.2.41 Usage Report Trigger bitmap:
                          *   bit 0 PERIO (periodic)
                          *   bit 1 VOLTH (volume threshold)
                          *   bit 2 TIMTH (time threshold)
                          *   bit 3 QUHTI (quota-holding)
                          *   bit 4 START
                          *   bit 5 STOPT
                          *   bit 6 DROTH
                          *   bit 7 LIUSA (linked usage)
                          *   bit 8 VOLQU (volume quota)
                          *   bit 9 TIMQU (time quota)
                          *   … (see §8.2.41 for full table) */
    uint64_t vol_ul;     /* §8.2.20 uplink bytes */
    uint64_t vol_dl;     /* §8.2.20 downlink bytes */
    uint64_t pkt_ul;
    uint64_t pkt_dl;
    uint32_t duration_s; /* §8.2.21 duration measurement */
    uint32_t pad;
} upf_usage_payload_t;

/* §7.5.8.4 Error Indication Report payload — GTP-U ERRIND from peer
 * (TS 29.281 §7.3). Carries the offending F-TEID so the SMF can
 * remap / re-establish. */
typedef struct upf_errind_payload {
    uint32_t remote_teid;
    uint32_t remote_addr; /* host byte order */
    uint16_t remote_port;
    uint8_t  pad[6];
} upf_errind_payload_t;

/* §7.5.8.5 TSC Management Information — minimal scaffold. Expand
 * when TSN / TSC integration lands. */
typedef struct upf_tsc_payload {
    uint32_t event_id;
    uint8_t  pad[12];
} upf_tsc_payload_t;

/* Generic §7.5.8.6 Session Report slot. */
typedef struct upf_sessrep_payload {
    uint32_t report_code;
    uint8_t  pad[12];
} upf_sessrep_payload_t;

/* One report record. Sized to fit a DPDK mempool mbuf pri-data slot
 * for future zero-copy. type tags the union. */
typedef struct upf_report {
    uint8_t  type;                /* enum upf_report_type */
    uint8_t  pdu_session_id;      /* 1..15 */
    uint8_t  pad0[6];
    char     imsi[UPF_IMSI_MAX];  /* null-terminated BCD digits */
    uint64_t seid;                /* PFCP SEID — §7.2.2.4.2 */
    uint64_t ts_ns;               /* rte_rdtsc() at enqueue */
    union {
        upf_dldr_payload_t    dldr;
        upf_usage_payload_t   usage;
        upf_errind_payload_t  errind;
        upf_tsc_payload_t     tsc;
        upf_sessrep_payload_t sessrep;
    } u;
} upf_report_t;

/* Initialise the MPMC report ring. ring_size must be a power of 2
 * per DPDK rte_ring semantics (§rte_ring.h). Returns 0 on success,
 * -1 on failure (typically: DPDK not initialised, OOM, or invalid
 * size).
 *
 * Called once from upf_dp_init() on the EAL init thread. */
int upf_report_init(unsigned ring_size);

/* Tear down the ring. Safe to call even if init failed. */
void upf_report_cleanup(void);

/* Enqueue a report. The record is copied into the ring (rte_ring
 * stores pointer-sized slots; the implementation mallocs-or-pools
 * the record and enqueues its pointer). Returns 0 on success, -1
 * when the ring is full (caller MUST treat as drop — the UP hot
 * path cannot block on the control plane).
 *
 * Callable from ANY DPDK lcore. Lockless MPMC. */
int upf_report_enqueue(const upf_report_t *r);

/* Drain up to max records into the caller's buffer. Returns the
 * count dequeued. Intended for a single Go consumer goroutine —
 * MPMC rte_ring does support multi-consumer but only one reader
 * avoids head-of-line stalls when one consumer is slow.
 *
 * Callable from any thread (typically the Go scheduler chooses). */
unsigned upf_report_drain(upf_report_t *buf, unsigned max);

/* Monotonic counter of enqueue() calls that dropped because the
 * ring was full. Exposed so the Go control plane (and operators)
 * can alert when the consumer falls behind. */
uint64_t upf_report_dropped(void);

#ifdef __cplusplus
}
#endif

#endif /* UPF_REPORT_H */
