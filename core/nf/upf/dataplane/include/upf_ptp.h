/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_ptp.h — (g)PTP steering in the GTP-U data path (Rel-19).
 *
 * TS 23.501 §5.27.1.2.2: the ingress TT timestamps (g)PTP event
 * messages with the 5G internal system clock (TSi) and carries the
 * timestamp across the 5GS in a Suffix field; the egress TT folds the
 * residence time (TSe − TSi) into the correctionField and strips the
 * Suffix. This module applies that processing to PTP-over-UDP
 * messages (IEEE 1588-2019 Annex C: UDP ports 319/320) traversing the
 * UPF fast path — the transport type the IP data path can carry
 * (TS 23.501 §5.27.1.4 lists IPv4/IPv6/Ethernet transports).
 *
 * The suffix TLV layout matches the Go side (edge/tsn/gptp):
 * ORGANIZATION_EXTENSION (type 3), orgId 00-00-5A, orgSubType
 * 0x005301, 8-octet TSi (ns) — 18 octets total.
 *
 * The correction applied here is the plain residence time
 * (rateRatio = 1); the rateRatio-scaled variant of §5.27.1.2.2 runs
 * at the NW-TT Go surface where the cumulative rateRatio of the
 * Follow_Up information TLV is tracked. */
#ifndef UPF_PTP_H
#define UPF_PTP_H

#include <stdint.h>

/* Process one IPv4 packet in place.
 *
 *   ingress=1 — packet entering the 5GS (DL at the NW-TT):
 *               append the TSi suffix (packet may GROW by 18 octets;
 *               cap is the buffer capacity).
 *   ingress=0 — packet leaving the 5GS (UL at the NW-TT):
 *               consume the suffix, add residence to correctionField
 *               (packet may SHRINK by 18 octets).
 *
 * Returns the (possibly changed) IP packet length; returns ip_len
 * unchanged when the packet is not a PTP event/general message or
 * nothing had to be done. residence_ns_out (optional) receives the
 * applied residence time on egress. */
uint16_t upf_ptp_process(uint8_t *ip_pkt, uint16_t ip_len, uint32_t cap,
                         uint8_t ingress, uint64_t now_ns,
                         uint64_t *residence_ns_out);

#endif /* UPF_PTP_H */
