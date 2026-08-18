/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_ptp.c — see upf_ptp.h. Pure functions, no DPDK state: directly
 * unit-testable from Go via cgo. */

#include <string.h>
#include "upf_ptp.h"

#define PTP_EVENT_PORT   319
#define PTP_GENERAL_PORT 320
#define PTP_HDR_LEN      34

/* 5GS suffix TLV (mirrors edge/tsn/gptp): type(2)=3, len(2)=14,
 * orgId 00-00-5A, orgSubType 00-53-01, TSi(8). */
#define SUFFIX_TLV_LEN 18
static const uint8_t suffix_prefix[10] = {
    0x00, 0x03, 0x00, 0x0E, 0x00, 0x00, 0x5A, 0x00, 0x53, 0x01
};

static uint16_t rd16(const uint8_t *p) { return (uint16_t)(p[0] << 8 | p[1]); }
static void     wr16(uint8_t *p, uint16_t v) { p[0] = (uint8_t)(v >> 8); p[1] = (uint8_t)v; }

static uint64_t rd64(const uint8_t *p)
{
    uint64_t v = 0;
    for (int i = 0; i < 8; i++) v = v << 8 | p[i];
    return v;
}
static void wr64(uint8_t *p, uint64_t v)
{
    for (int i = 7; i >= 0; i--) { p[i] = (uint8_t)v; v >>= 8; }
}

/* RFC 1071 IPv4 header checksum. */
static void ip_checksum(uint8_t *ip, uint8_t ihl)
{
    ip[10] = ip[11] = 0;
    uint32_t sum = 0;
    for (int i = 0; i < ihl; i += 2)
        sum += rd16(ip + i);
    while (sum >> 16)
        sum = (sum & 0xFFFF) + (sum >> 16);
    wr16(ip + 10, (uint16_t)~sum);
}

/* Fixed body length per PTP message type (IEEE 1588-2019 §13) —
 * needed to find where the TLV chain starts. 0xFF = unknown. */
static uint8_t ptp_body_len(uint8_t msg_type)
{
    switch (msg_type & 0x0F) {
    case 0x0: /* Sync */
    case 0x1: /* Delay_Req */
    case 0x8: /* Follow_Up */
    case 0x9: /* Delay_Resp: 10 ts + 10 reqPortId */
        return (msg_type & 0x0F) == 0x9 ? 20 : 10;
    case 0x2: /* Pdelay_Req: ts + 10 reserved */
        return 20;
    case 0x3: /* Pdelay_Resp */
    case 0xA: /* Pdelay_Resp_Follow_Up */
        return 20;
    case 0xB: /* Announce */
        return 30;
    default:
        return 0xFF;
    }
}

uint16_t upf_ptp_process(uint8_t *ip_pkt, uint16_t ip_len, uint32_t cap,
                         uint8_t ingress, uint64_t now_ns,
                         uint64_t *residence_ns_out)
{
    if (residence_ns_out) *residence_ns_out = 0;
    if (ip_len < 20 || ((ip_pkt[0] >> 4) & 0x0F) != 4)
        return ip_len;
    uint8_t ihl = (uint8_t)((ip_pkt[0] & 0x0F) * 4);
    if (ihl < 20 || ip_pkt[9] != 17 /* UDP */ || ip_len < (uint16_t)(ihl + 8))
        return ip_len;

    uint8_t *udp = ip_pkt + ihl;
    uint16_t dport = rd16(udp + 2);
    if (dport != PTP_EVENT_PORT && dport != PTP_GENERAL_PORT)
        return ip_len;
    uint16_t udp_len = rd16(udp + 4);
    if (udp_len < 8 + PTP_HDR_LEN || (uint16_t)(ihl + udp_len) > ip_len)
        return ip_len;

    uint8_t *ptp = udp + 8;
    uint16_t ptp_avail = (uint16_t)(udp_len - 8);
    if ((ptp[1] & 0x0F) != 2) /* versionPTP */
        return ip_len;
    uint16_t msg_len = rd16(ptp + 2);
    if (msg_len < PTP_HDR_LEN || msg_len > ptp_avail)
        return ip_len;
    uint8_t body = ptp_body_len(ptp[0]);
    if (body == 0xFF || (uint16_t)(PTP_HDR_LEN + body) > msg_len)
        return ip_len;

    if (ingress) {
        /* §5.27.1.2.2 ingress TT: stamp TSi into the 5GS suffix. */
        if ((uint32_t)ip_len + SUFFIX_TLV_LEN > cap)
            return ip_len; /* no headroom — pass through untouched */
        uint8_t *end = ptp + msg_len;
        /* Shift anything after the PTP message (normally nothing). */
        uint16_t tail = (uint16_t)(ip_len - (uint16_t)(end - ip_pkt));
        if (tail)
            memmove(end + SUFFIX_TLV_LEN, end, tail);
        memcpy(end, suffix_prefix, sizeof(suffix_prefix));
        wr64(end + sizeof(suffix_prefix), now_ns);
        msg_len = (uint16_t)(msg_len + SUFFIX_TLV_LEN);
        udp_len = (uint16_t)(udp_len + SUFFIX_TLV_LEN);
        ip_len  = (uint16_t)(ip_len + SUFFIX_TLV_LEN);
        wr16(ptp + 2, msg_len);
        wr16(udp + 4, udp_len);
        udp[6] = udp[7] = 0; /* UDP checksum 0 = unset (valid for IPv4) */
        wr16(ip_pkt + 2, ip_len);
        ip_checksum(ip_pkt, ihl);
        return ip_len;
    }

    /* Egress TT: walk the TLV chain after the body, find the 5GS
     * suffix, fold the residence time into correctionField
     * (ns × 2^16, IEEE 1588 §13.3.2.9) and strip the TLV. */
    uint8_t *tlv = ptp + PTP_HDR_LEN + body;
    uint8_t *msg_end = ptp + msg_len;
    while (tlv + 4 <= msg_end) {
        uint16_t t_len = rd16(tlv + 2);
        uint8_t *t_end = tlv + 4 + t_len;
        if (t_end > msg_end)
            break;
        if (t_len == SUFFIX_TLV_LEN - 4 &&
            memcmp(tlv, suffix_prefix, sizeof(suffix_prefix)) == 0) {
            uint64_t tsi = rd64(tlv + sizeof(suffix_prefix));
            uint64_t residence = now_ns > tsi ? now_ns - tsi : 0;
            if (residence_ns_out) *residence_ns_out = residence;
            /* correctionField += residence << 16 (signed 64-bit BE). */
            int64_t corr = (int64_t)rd64(ptp + 8);
            corr += (int64_t)(residence << 16);
            wr64(ptp + 8, (uint64_t)corr);
            /* Strip the TLV. */
            uint16_t rest = (uint16_t)(ip_len - (uint16_t)(t_end - ip_pkt));
            memmove(tlv, t_end, rest);
            msg_len = (uint16_t)(msg_len - SUFFIX_TLV_LEN);
            udp_len = (uint16_t)(udp_len - SUFFIX_TLV_LEN);
            ip_len  = (uint16_t)(ip_len - SUFFIX_TLV_LEN);
            wr16(ptp + 2, msg_len);
            wr16(udp + 4, udp_len);
            udp[6] = udp[7] = 0;
            wr16(ip_pkt + 2, ip_len);
            ip_checksum(ip_pkt, ihl);
            return ip_len;
        }
        tlv = t_end;
    }
    return ip_len;
}
