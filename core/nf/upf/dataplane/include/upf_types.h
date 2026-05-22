/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_types.h — UPF data plane core structures
 *
 * 3GPP TS 29.244 data model: PDR, FAR, QER, URR, BAR
 * All structs are packed for ctypes interop with Python.
 */

#ifndef UPF_TYPES_H
#define UPF_TYPES_H

#include <stdint.h>
#include <stdbool.h>
#include <netinet/in.h>

/* ── Limits ── */
/* UPF_MAX_SESSIONS is the **default** (compile-time) session cap.
 * At runtime, Python may override via upf_dp_set_max_sessions() before
 * upf_dp_init(); the actual cap then lives in the g_upf_max_sessions
 * global and all session-indexed storage is sized from that value.
 * Keep the #define around as the initial value and as the floor used
 * by sanity checks. */
#ifndef UPF_MAX_SESSIONS
#define UPF_MAX_SESSIONS        4096
#endif
/* Runtime cap — initialised from UPF_MAX_SESSIONS, overridable from
 * Python. Read everywhere that previously used UPF_MAX_SESSIONS for
 * bounds/allocation. */
extern uint32_t g_upf_max_sessions;

/* PMD-mode runtime knobs. Read by upf_pkt_io_dpdk.c at init time.
 * Python overrides via upf_dp_set_pmd_tuning() before upf_dp_init(). */
extern uint32_t g_upf_mbuf_pool_size;
extern uint16_t g_upf_rx_ring_size;
extern uint16_t g_upf_tx_ring_size;
#define UPF_MAX_PDR_PER_SESSION 16
#define UPF_MAX_FAR_PER_SESSION 16
#define UPF_MAX_QER_PER_SESSION 8
#define UPF_MAX_URR_PER_SESSION 8
#define UPF_MAX_SDF_FILTERS     8
#define UPF_MAX_DNN_LEN         64

/* DL packet buffer for FAR action=BUFF (TS 29.244 §5.2.1) */
#define UPF_DL_BUF_SLOTS        32
#define UPF_DL_BUF_PKT_SZ       1600

/* ── SDF Filter (TS 29.244 §8.2.5) — IPv4 + IPv6 ── */
typedef struct __attribute__((__packed__)) {
    uint8_t  action;         /* 1=permit, 2=deny */
    uint8_t  direction;      /* 1=in, 2=out */
    uint8_t  proto;          /* IP protocol (6=TCP, 17=UDP, 255=any) */
    uint8_t  addr_type;      /* 0=v4, 1=v6 */
    uint32_t src_addr;       /* IPv4: network byte order, 0 = any */
    uint32_t src_mask;       /* IPv4: network byte order */
    uint16_t src_port_lo;    /* host byte order, 0 = any */
    uint16_t src_port_hi;
    uint32_t dst_addr;       /* IPv4 */
    uint32_t dst_mask;       /* IPv4 */
    uint16_t dst_port_lo;
    uint16_t dst_port_hi;
    uint8_t  src_addr6[16];  /* IPv6 source address */
    uint8_t  src_mask6[16];  /* IPv6 source mask (prefix) */
    uint8_t  dst_addr6[16];  /* IPv6 destination address */
    uint8_t  dst_mask6[16];  /* IPv6 destination mask */
} upf_sdf_filter_t;

/* ── PDR — Packet Detection Rule (TS 29.244 §7.5.2) — IPv4 + IPv6 ── */
typedef struct __attribute__((__packed__)) {
    uint16_t pdr_id;
    uint32_t precedence;        /* lower = higher priority */
    uint8_t  pdi_source;        /* 0=access(UL), 1=core(DL), 2=CP */
    uint8_t  addr_type;         /* 0=v4, 1=v6, 2=v4v6 (dual-stack) */

    /* PDI match fields — IPv4 */
    uint32_t ue_addr;           /* UE IPv4, network byte order */
    /* PDI match fields — IPv6 */
    uint8_t  ue_addr6[16];      /* UE IPv6 address */

    uint8_t  qfi;               /* QoS Flow Identifier (0-63) */
    uint8_t  n_sdf;             /* number of SDF filters */
    uint8_t  _pad1[2];
    upf_sdf_filter_t sdf[UPF_MAX_SDF_FILTERS];

    /* Associated rule IDs */
    uint32_t far_id;
    uint32_t qer_id;            /* 0 = none */
    uint32_t urr_id;            /* 0 = none */

    bool     active;
    uint8_t  _pad2[3];
} upf_pdr_t;

/* ── FAR — Forwarding Action Rule (TS 29.244 §7.5.2.3) — IPv4 + IPv6 ── */
typedef struct __attribute__((__packed__)) {
    uint32_t far_id;
    uint8_t  action;            /* 0=drop, 1=forward, 2=buffer, 3=notify_cp */
    uint8_t  dst_iface;         /* 0=access, 1=core, 2=CP */
    uint8_t  _pad0[2];

    /* Outer Header Creation (GTP-U encap for forwarding).
     * Convention: all 32-bit fields in HOST byte order (numerical value).
     * Convert with htonl() at the wire boundary (sin_addr, GTP-U encode).
     * This matches upf_gtpu.c gtpu_encode/decode which works on host-order
     * TEIDs, and stays architecture-agnostic (same logical value on LE/BE). */
    uint32_t ohc_teid;          /* GTP-U TEID, HOST byte order */
    uint32_t ohc_peer_addr;     /* GTP-U peer IPv4, HOST byte order */
    uint8_t  ohc_peer_addr6[16];/* GTP-U peer IPv6 */
    uint16_t ohc_peer_port;     /* usually 2152 */
    uint8_t  ohc_type;          /* 0=none, 1=GTP-U/UDP/IPv4, 2=GTP-U/UDP/IPv6 */
    uint8_t  _pad1;

    bool     active;
    uint8_t  _pad2[3];
} upf_far_t;

/* ── QER — QoS Enforcement Rule (TS 29.244 §7.5.2.6) ── */
typedef struct __attribute__((__packed__)) {
    uint32_t qer_id;
    uint8_t  gate_status_ul;    /* 0=open, 1=closed */
    uint8_t  gate_status_dl;
    uint8_t  qfi;
    uint8_t  _pad0;

    /* MBR / GBR in kbps */
    uint64_t mbr_ul;
    uint64_t mbr_dl;
    uint64_t gbr_ul;
    uint64_t gbr_dl;

    /* Per-QER drop counters (gate + meter) — updated by fast path */
    uint64_t dropped_pkts_ul;   /* UL packets dropped (gate closed or MBR exceeded) */
    uint64_t dropped_pkts_dl;   /* DL packets dropped */
    uint64_t dropped_bytes_ul;  /* UL bytes dropped */
    uint64_t dropped_bytes_dl;  /* DL bytes dropped */

    bool     active;
    uint8_t  _pad1[7];
} upf_qer_t;

/* ── URR — Usage Reporting Rule (TS 29.244 §7.5.2.4) ── */
typedef struct __attribute__((__packed__)) {
    uint32_t urr_id;
    uint8_t  measurement_method; /* bit0=duration, bit1=volume, bit2=event */
    uint8_t  reporting_trigger;  /* bit0=periodic, bit1=vol_threshold, bit2=time_threshold */
    uint8_t  _pad0[2];

    uint64_t vol_threshold_ul;   /* bytes */
    uint64_t vol_threshold_dl;
    uint32_t time_threshold;     /* seconds */
    uint32_t _pad1;

    /* Counters (updated by fast path) */
    uint64_t vol_ul;
    uint64_t vol_dl;
    uint64_t pkt_ul;
    uint64_t pkt_dl;
    uint64_t start_time;         /* epoch ns */

    bool     active;
    uint8_t  _pad2[7];
} upf_urr_t;

/* ── BAR — Buffering Action Rule (TS 29.244 §7.5.2.5) ── */
typedef struct __attribute__((__packed__)) {
    uint8_t  bar_id;
    uint8_t  notify_cp;          /* 1 = send notification to CP */
    uint16_t buf_pkt_count;      /* max packets to buffer, 0 = unlimited */
    bool     active;
    uint8_t  _pad[3];
} upf_bar_t;

/* ── DL Packet Buffer (TS 29.244 §5.2.1) ──
 * Ring buffer for DL packets while FAR action=BUFF (before gNB TEID known).
 * Flushed when FAR is updated to action=FORW. */
typedef struct {
    uint8_t  pkt[UPF_DL_BUF_SLOTS][UPF_DL_BUF_PKT_SZ];
    uint16_t len[UPF_DL_BUF_SLOTS];
    uint8_t  qfi[UPF_DL_BUF_SLOTS];
    uint16_t head;
    uint16_t count;
} upf_dl_buf_t;

/* ── UPF Session (per PDU session) ── */
typedef struct __attribute__((__packed__)) {
    /* Session key: IMSI (BCD, 15 digits max + null) */
    char     imsi[16];
    uint8_t  pdu_session_id;
    uint8_t  _pad0[3];

    /* DNN and slice */
    char     dnn[UPF_MAX_DNN_LEN];
    uint8_t  sst;
    uint8_t  _pad1[3];
    uint32_t sd;                 /* 0xFFFFFF = not set */

    /* UE address */
    uint32_t ue_addr;            /* network byte order */

    /* Rules */
    upf_pdr_t pdr[UPF_MAX_PDR_PER_SESSION];
    upf_far_t far[UPF_MAX_FAR_PER_SESSION];
    upf_qer_t qer[UPF_MAX_QER_PER_SESSION];
    upf_urr_t urr[UPF_MAX_URR_PER_SESSION];
    upf_bar_t bar;               /* one BAR per session */

    uint8_t  n_pdr;
    uint8_t  n_far;
    uint8_t  n_qer;
    uint8_t  n_urr;

    /* Session-AMBR (TS 23.501 v19.7.0 §5.7.1.6) — aggregate rate
     * limit for this PDU session, enforced at the UPF: "UPF performs
     * Session-AMBR enforcement as specified in clause 5.7.1.8". */
    uint64_t session_ambr_ul;    /* kbps, 0 = unlimited */
    uint64_t session_ambr_dl;    /* kbps, 0 = unlimited */

    /* UE-AMBR (TS 23.501 v19.7.0 §5.7.1.6 + §5.7.2.6) is enforced by
     * the (R)AN, NOT by the UPF: "The (R)AN shall enforce UE-AMBR
     * (see clause 5.7.2.6) in UL and DL per UE for Non-GBR QoS
     * Flows." TS 29.244 v19.5.0 has no UE-AMBR IE so PFCP can't carry
     * it to the UPF anyway. The previous ue_ambr_ul/dl fields here
     * fed an out-of-spec per-UE rte_meter that has been removed. */

    /* Session index in meter arrays (set at creation) */
    uint32_t session_idx;

    /* DL packet buffer — used while DL FAR action=BUFF (TS 29.244 §5.2.1) */
    upf_dl_buf_t *dl_buf;       /* heap-allocated, NULL if not buffering */

    bool     active;
    uint8_t  _pad2[3];
} upf_session_t;

#endif /* UPF_TYPES_H */
