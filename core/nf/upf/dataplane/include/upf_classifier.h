/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_classifier.h — Packet classification against PDRs
 *
 * Packet processing pipeline per 3GPP TS 29.244:
 *   1. PDR match: find highest-priority PDR matching the packet 5-tuple
 *   2. QER apply: gate check + rate metering (token bucket)
 *   3. URR update: volume/packet counters
 *   4. FAR execute: drop / forward / buffer / GTP-U encap
 *
 * Two entry points:
 *   - upf_classify_ul(): UL packet from GTP-U (after decap) → match access PDR
 *   - upf_classify_dl(): DL packet from core/TUN → match core PDR
 */

#ifndef UPF_CLASSIFIER_H
#define UPF_CLASSIFIER_H

#include "upf_types.h"

/* Classification result */
typedef struct {
    upf_pdr_t *matched_pdr;   /* PDR that matched, or NULL */
    upf_far_t *far;           /* associated FAR, or NULL */
    upf_qer_t *qer;           /* associated QER, or NULL */
    upf_urr_t *urr;           /* associated URR, or NULL */
    uint8_t    action;         /* FAR action: 0=drop, 1=forward, 2=buffer */
    uint8_t    gate_pass;      /* 1 if QER gate is open, 0 if closed */
    uint8_t    meter_pass;     /* 1 if all meters pass (QER MBR + AMBR), 0 if rate-limited */
} upf_classify_result_t;

/* Match an IP packet against the PDRs in a session.
 * direction: 0=UL(access), 1=DL(core)
 * qfi: QFI from GTP-U PDU Session Container (UL), or 0 for DL.
 *      TS 29.244 §5.2.1: QFI is part of PDI for UL PDR matching.
 *      TS 38.415 §5.5.2.1: QFI mandatory in 5G NR GTP-U.
 * ip_pkt points to the inner IPv4 header.
 * Returns 0 if a PDR matched, -1 if no match. */
int upf_classify(upf_session_t *sess, uint8_t direction, uint8_t qfi,
                  const uint8_t *ip_pkt, uint16_t ip_len,
                  upf_classify_result_t *result);

/* Process a packet through the full pipeline (classify + QER + URR).
 * Updates URR counters and checks QER gate.
 * qfi: QFI from GTP-U header (UL) or 0 (DL).
 * Returns the FAR action (0=drop, 1=forward, 2=buffer, -1=no match). */
int upf_process_packet(upf_session_t *sess, uint8_t direction, uint8_t qfi,
                        const uint8_t *ip_pkt, uint16_t ip_len,
                        upf_classify_result_t *result);

#endif /* UPF_CLASSIFIER_H */
