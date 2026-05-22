/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_classifier.c — Packet classification and rule processing
 *
 * Pipeline: PDR match → QER gate/meter → URR counters → FAR action
 *
 * PDR matching:
 *   1. Filter by direction (pdi_source: 0=access/UL, 1=core/DL)
 *   2. Match UE address (for DL: dst_ip == ue_addr, for UL: src_ip == ue_addr)
 *   3. Match SDF filters (5-tuple: proto, src/dst addr+mask, src/dst port)
 *   4. Select highest-priority (lowest precedence value) match
 */

#include <string.h>
#include <arpa/inet.h>
#include <rte_log.h>

#include "upf_classifier.h"
#include "upf_qos_meter.h"

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

/* Minimal IPv4 header for extracting 5-tuple */
typedef struct __attribute__((__packed__)) {
    uint8_t  ver_ihl;
    uint8_t  tos;
    uint16_t tot_len;
    uint16_t id;
    uint16_t frag_off;
    uint8_t  ttl;
    uint8_t  proto;
    uint16_t check;
    uint32_t src_addr;
    uint32_t dst_addr;
} ipv4_hdr_t;

/* Minimal IPv6 header for extracting 5-tuple */
typedef struct __attribute__((__packed__)) {
    uint32_t ver_tc_flow;    /* version(4) + traffic class(8) + flow label(20) */
    uint16_t payload_len;
    uint8_t  next_header;    /* protocol: 6=TCP, 17=UDP, 58=ICMPv6 */
    uint8_t  hop_limit;
    uint8_t  src_addr[16];
    uint8_t  dst_addr[16];
} ipv6_hdr_t;

/* Result structure for dual-stack 5-tuple extraction */
typedef struct {
    uint8_t  ip_version;     /* 4 or 6 */
    uint8_t  proto;
    uint32_t src_addr4;      /* IPv4 src (if v4) */
    uint32_t dst_addr4;      /* IPv4 dst (if v4) */
    uint8_t  src_addr6[16];  /* IPv6 src (if v6) */
    uint8_t  dst_addr6[16];  /* IPv6 dst (if v6) */
    uint16_t src_port;
    uint16_t dst_port;
} five_tuple_t;

/* Extract 5-tuple from IPv4 packet */
static int extract_5tuple_v4(const uint8_t *ip_pkt, uint16_t ip_len,
                              five_tuple_t *ft)
{
    if (ip_len < 20) return -1;

    const ipv4_hdr_t *ip = (const ipv4_hdr_t *)ip_pkt;
    uint8_t ihl = (ip->ver_ihl & 0xF) * 4;
    if (ihl < 20 || ihl > ip_len) return -1;

    ft->ip_version = 4;
    ft->proto = ip->proto;
    ft->src_addr4 = ip->src_addr;
    ft->dst_addr4 = ip->dst_addr;
    ft->src_port = 0;
    ft->dst_port = 0;

    if ((ft->proto == 6 || ft->proto == 17) && ip_len >= ihl + 4) {
        const uint8_t *l4 = ip_pkt + ihl;
        ft->src_port = ntohs(*(const uint16_t *)l4);
        ft->dst_port = ntohs(*(const uint16_t *)(l4 + 2));
    }

    return 0;
}

/* Extract 5-tuple from IPv6 packet.
 * Reserved for IPv6 wiring in extract_5tuple(); defined here so the v6 path
 * lands in one commit once the IPv6 classify path goes live (TS 29.244 §5.2). */
static int extract_5tuple_v6(const uint8_t *ip_pkt, uint16_t ip_len,
                              five_tuple_t *ft) __attribute__((unused));
static int extract_5tuple_v6(const uint8_t *ip_pkt, uint16_t ip_len,
                              five_tuple_t *ft)
{
    if (ip_len < 40) return -1;

    const ipv6_hdr_t *ip6 = (const ipv6_hdr_t *)ip_pkt;

    ft->ip_version = 6;
    ft->proto = ip6->next_header;
    memcpy(ft->src_addr6, ip6->src_addr, 16);
    memcpy(ft->dst_addr6, ip6->dst_addr, 16);
    ft->src_port = 0;
    ft->dst_port = 0;

    /* Skip extension headers to find TCP/UDP — simplified: only handle
     * direct TCP/UDP next_header (no extension header chain) */
    if ((ft->proto == 6 || ft->proto == 17) && ip_len >= 40 + 4) {
        const uint8_t *l4 = ip_pkt + 40;
        ft->src_port = ntohs(*(const uint16_t *)l4);
        ft->dst_port = ntohs(*(const uint16_t *)(l4 + 2));
    }

    return 0;
}

/* Extract 5-tuple — auto-detect IPv4 or IPv6 from version nibble */
static int extract_5tuple(const uint8_t *ip_pkt, uint16_t ip_len,
                           uint8_t *proto, uint32_t *src_addr, uint32_t *dst_addr,
                           uint16_t *src_port, uint16_t *dst_port)
{
    /* Legacy wrapper — extracts IPv4 only (used by existing classify path) */
    if (ip_len < 20) return -1;

    uint8_t ver = (ip_pkt[0] >> 4) & 0xF;
    if (ver != 4) return -1;

    five_tuple_t ft;
    if (extract_5tuple_v4(ip_pkt, ip_len, &ft) < 0) return -1;

    *proto = ft.proto;
    *src_addr = ft.src_addr4;
    *dst_addr = ft.dst_addr4;
    *src_port = ft.src_port;
    *dst_port = ft.dst_port;
    return 0;
}

/* Check if a packet matches a single SDF filter */
static int sdf_match(const upf_sdf_filter_t *sdf,
                      uint8_t proto, uint32_t src_addr, uint32_t dst_addr,
                      uint16_t src_port, uint16_t dst_port)
{
    /* Protocol check (255 = any) */
    if (sdf->proto != 255 && sdf->proto != proto)
        return 0;

    /* Source address (masked) */
    if (sdf->src_mask != 0) {
        if ((src_addr & sdf->src_mask) != (sdf->src_addr & sdf->src_mask))
            return 0;
    }

    /* Destination address (masked) */
    if (sdf->dst_mask != 0) {
        if ((dst_addr & sdf->dst_mask) != (sdf->dst_addr & sdf->dst_mask))
            return 0;
    }

    /* Source port range */
    if (sdf->src_port_lo != 0 || sdf->src_port_hi != 65535) {
        if (src_port < sdf->src_port_lo || src_port > sdf->src_port_hi)
            return 0;
    }

    /* Destination port range */
    if (sdf->dst_port_lo != 0 || sdf->dst_port_hi != 65535) {
        if (dst_port < sdf->dst_port_lo || dst_port > sdf->dst_port_hi)
            return 0;
    }

    return 1;  /* match */
}

/* Check if packet matches a PDR.
 *
 * TS 29.244 §5.2.1: PDI (Packet Detection Information) matching includes:
 *   - Source Interface (pdi_source: 0=access/UL, 1=core/DL)
 *   - QFI (UL only: from GTP-U PDU Session Container, TS 38.415 §5.5.2.1)
 *   - UE IP Address
 *   - SDF Filters (5-tuple: proto, src/dst addr+mask, src/dst port range)
 */
static int pdr_match(const upf_pdr_t *pdr, uint8_t direction, uint8_t qfi,
                      uint8_t proto, uint32_t src_addr, uint32_t dst_addr,
                      uint16_t src_port, uint16_t dst_port)
{
    if (!pdr->active) return 0;
    if (pdr->pdi_source != direction) return 0;

    /* TS 29.244 §5.2.1: QFI matching for UL packets.
     * The QFI from the GTP-U PDU Session Container must match the PDR's QFI.
     * For DL, QFI is not part of incoming PDI (UPF sets it on encap). */
    if (direction == 0 && pdr->qfi > 0 && qfi > 0 && pdr->qfi != qfi)
        return 0;

    /* UE address check. Both sides are network byte order:
     * pdr->ue_addr is set in upf_dp_session_create via htonl(host-order arg
     * from Go), and src_addr/dst_addr here come from a raw IPv4 header
     * memcpy (wire order). */
    if (pdr->ue_addr != 0) {
        if (direction == 0) {
            if (src_addr != pdr->ue_addr) return 0;
        } else {
            if (dst_addr != pdr->ue_addr) return 0;
        }
    }

    /* SDF filter check — at least one must match (OR logic) */
    if (pdr->n_sdf > 0) {
        int any_match = 0;
        for (uint8_t i = 0; i < pdr->n_sdf; i++) {
            if (sdf_match(&pdr->sdf[i], proto, src_addr, dst_addr,
                           src_port, dst_port)) {
                if (pdr->sdf[i].action == 2) return 0;  /* deny */
                any_match = 1;
                break;
            }
        }
        if (!any_match) return 0;
    }

    return 1;
}

/* Find FAR by ID in session */
static upf_far_t *find_far(upf_session_t *sess, uint32_t far_id)
{
    for (uint8_t i = 0; i < sess->n_far; i++) {
        if (sess->far[i].far_id == far_id && sess->far[i].active)
            return &sess->far[i];
    }
    return NULL;
}

/* Find QER by ID in session */
static upf_qer_t *find_qer(upf_session_t *sess, uint32_t qer_id)
{
    for (uint8_t i = 0; i < sess->n_qer; i++) {
        if (sess->qer[i].qer_id == qer_id && sess->qer[i].active)
            return &sess->qer[i];
    }
    return NULL;
}

/* Find URR by ID in session */
static upf_urr_t *find_urr(upf_session_t *sess, uint32_t urr_id)
{
    for (uint8_t i = 0; i < sess->n_urr; i++) {
        if (sess->urr[i].urr_id == urr_id && sess->urr[i].active)
            return &sess->urr[i];
    }
    return NULL;
}

int upf_classify(upf_session_t *sess, uint8_t direction, uint8_t qfi,
                  const uint8_t *ip_pkt, uint16_t ip_len,
                  upf_classify_result_t *result)
{
    if (!sess || !ip_pkt || !result) return -1;

    memset(result, 0, sizeof(*result));

    /* Extract 5-tuple */
    uint8_t proto;
    uint32_t src_addr, dst_addr;
    uint16_t src_port, dst_port;

    if (extract_5tuple(ip_pkt, ip_len, &proto, &src_addr, &dst_addr,
                        &src_port, &dst_port) < 0) {
        return -1;
    }

    /* Find best matching PDR (lowest precedence value = highest priority)
     * TS 29.244 §5.2.1: PDR matching includes QFI for UL direction */
    upf_pdr_t *best_pdr = NULL;
    uint32_t best_prec = UINT32_MAX;

    for (uint8_t i = 0; i < sess->n_pdr; i++) {
        if (pdr_match(&sess->pdr[i], direction, qfi, proto,
                       src_addr, dst_addr, src_port, dst_port)) {
            if (sess->pdr[i].precedence < best_prec) {
                best_pdr = &sess->pdr[i];
                best_prec = sess->pdr[i].precedence;
            }
        }
    }

    if (!best_pdr) return -1;

    result->matched_pdr = best_pdr;
    result->far = find_far(sess, best_pdr->far_id);
    result->qer = (best_pdr->qer_id > 0) ? find_qer(sess, best_pdr->qer_id) : NULL;
    result->urr = (best_pdr->urr_id > 0) ? find_urr(sess, best_pdr->urr_id) : NULL;
    result->action = result->far ? result->far->action : 0;
    result->gate_pass = 1;   /* default open until QER check */
    result->meter_pass = 1;  /* default pass until meter check */

    return 0;
}

int upf_process_packet(upf_session_t *sess, uint8_t direction, uint8_t qfi,
                        const uint8_t *ip_pkt, uint16_t ip_len,
                        upf_classify_result_t *result)
{
    /* Step 1: Classify (PDR match incl. QFI for UL, TS 29.244 §5.2.1) */
    if (upf_classify(sess, direction, qfi, ip_pkt, ip_len, result) < 0) {
        return -1;  /* no matching PDR */
    }

    /* Step 2a: QER — gate check (TS 29.244 §5.4.1) */
    if (result->qer) {
        uint8_t gate = (direction == 0) ? result->qer->gate_status_ul
                                         : result->qer->gate_status_dl;
        if (gate != 0) {  /* 0=open, 1=closed */
            result->gate_pass = 0;
            /* Per-QER drop counters */
            if (direction == 0) {
                __atomic_add_fetch(&result->qer->dropped_pkts_ul, 1, __ATOMIC_RELAXED);
                __atomic_add_fetch(&result->qer->dropped_bytes_ul, ip_len, __ATOMIC_RELAXED);
            } else {
                __atomic_add_fetch(&result->qer->dropped_pkts_dl, 1, __ATOMIC_RELAXED);
                __atomic_add_fetch(&result->qer->dropped_bytes_dl, ip_len, __ATOMIC_RELAXED);
            }
            return 0;  /* drop: gate closed */
        }
    }

    /* Step 2b: QER MBR meter (TS 29.244 §5.4.2 — per-flow rate enforcement)
     * Uses DPDK rte_meter srTCM: CIR=MBR, GREEN=pass, RED=drop */
    if (result->qer && (result->qer->mbr_ul > 0 || result->qer->mbr_dl > 0)) {
        uint8_t qer_idx = 0;
        for (uint8_t i = 0; i < sess->n_qer; i++) {
            if (sess->qer[i].qer_id == result->qer->qer_id) { qer_idx = i; break; }
        }
        upf_qer_meter_t *qm = upf_qer_meter_get(sess->session_idx, qer_idx);
        if (qm && !upf_qer_meter_check(qm, direction, ip_len)) {
            result->meter_pass = 0;
            /* Per-QER drop counters (MBR exceeded) */
            if (direction == 0) {
                __atomic_add_fetch(&result->qer->dropped_pkts_ul, 1, __ATOMIC_RELAXED);
                __atomic_add_fetch(&result->qer->dropped_bytes_ul, ip_len, __ATOMIC_RELAXED);
            } else {
                __atomic_add_fetch(&result->qer->dropped_pkts_dl, 1, __ATOMIC_RELAXED);
                __atomic_add_fetch(&result->qer->dropped_bytes_dl, ip_len, __ATOMIC_RELAXED);
            }
            return 0;  /* drop: MBR exceeded */
        }
    }

    /* Step 2c: Session-AMBR meter (TS 23.501 §5.7.1.6)
     * TS 23.501 §5.7.3: GBR flows are EXEMPT from AMBR policing */
    int is_gbr = result->qer && (result->qer->gbr_ul > 0 || result->qer->gbr_dl > 0);
    if (!is_gbr && (sess->session_ambr_ul > 0 || sess->session_ambr_dl > 0)) {
        upf_session_meter_t *sm = upf_session_meter_get(sess->session_idx);
        if (sm && !upf_session_meter_check(sm, direction, ip_len)) {
            result->meter_pass = 0;
            return 0;  /* drop: Session-AMBR exceeded */
        }
    }

    /* UE-AMBR enforcement is the (R)AN's responsibility per
     * TS 23.501 v19.7.0 §5.7.1.6: "The (R)AN shall enforce UE-AMBR
     * (see clause 5.7.2.6) in UL and DL per UE for Non-GBR QoS
     * Flows." The AMF conveys UE-AMBR to the (R)AN via the
     * UEAggregateMaximumBitRate IE in PDU Session Resource Setup
     * Request — see nf/amf/ngap/pdusetup. The UPF's enforcement
     * scope here is Session-AMBR (§5.7.1.6 paragraph above) plus
     * per-flow MBR (QER §5.7.1.8) only. */

    /* Step 3: URR — update counters (only for packets that passed all meters) */
    if (result->urr) {
        if (direction == 0) {
            __atomic_add_fetch(&result->urr->vol_ul, ip_len, __ATOMIC_RELAXED);
            __atomic_add_fetch(&result->urr->pkt_ul, 1, __ATOMIC_RELAXED);
        } else {
            __atomic_add_fetch(&result->urr->vol_dl, ip_len, __ATOMIC_RELAXED);
            __atomic_add_fetch(&result->urr->pkt_dl, 1, __ATOMIC_RELAXED);
        }
    }

    /* Step 4: Return FAR action */
    return result->action;
}
