/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_qos_meter.h — DPDK rte_meter based QoS enforcement
 *
 * 3GPP QoS enforcement per TS 29.244 §5.4, TS 23.501 §5.7:
 *
 *   Per-QER meter:     MBR enforcement per QoS flow (srTCM, CIR=MBR)
 *   Per-session meter: Session-AMBR enforcement (TS 23.501 §5.7.1.6)
 *
 * UE-AMBR enforcement is NOT a UPF responsibility — see TS 23.501
 * v19.7.0 §5.7.1.6: "The (R)AN shall enforce UE-AMBR (see clause
 * 5.7.2.6) in UL and DL per UE for Non-GBR QoS Flows." The AMF
 * conveys UE-AMBR to the (R)AN at PDU Session Resource Setup; the
 * UPF never sees it (TS 29.244 v19.5.0 has no UE-AMBR IE). Earlier
 * UE-AMBR meter code in this file overreached and was removed.
 *
 * GBR flows are EXEMPT from Session-AMBR policing (TS 23.501
 * §5.7.1.6 / §5.7.2.6: AMBR limits "across all Non-GBR QoS Flows").
 *
 * Uses rte_meter srTCM (RFC 2697):
 *   CIR = MBR (bytes/sec)
 *   CBS = burst allowance (10ms of CIR or 12 MTUs, whichever larger)
 *   EBS = 0 (hard policing — RED packets dropped, no yellow band)
 */

#ifndef UPF_QOS_METER_H
#define UPF_QOS_METER_H

#include <stdint.h>
#include <stdbool.h>
#include <rte_meter.h>

#include "upf_types.h"

/* ── Per-QER meter (MBR enforcement per QoS flow) ── */
typedef struct {
    struct rte_meter_srtcm         meter_ul;
    struct rte_meter_srtcm         meter_dl;
    struct rte_meter_srtcm_profile profile_ul;
    struct rte_meter_srtcm_profile profile_dl;
    bool configured;
} upf_qer_meter_t;

/* ── Per-session meter (Session-AMBR) ── */
typedef struct {
    struct rte_meter_srtcm         meter_ul;
    struct rte_meter_srtcm         meter_dl;
    struct rte_meter_srtcm_profile profile_ul;
    struct rte_meter_srtcm_profile profile_dl;
    bool configured;
} upf_session_meter_t;

/* ── Configuration ── */

/* Configure per-QER meter (CIR = MBR). Pass 0 for unlimited. */
int upf_qer_meter_configure(upf_qer_meter_t *m,
                             uint64_t mbr_ul_kbps, uint64_t mbr_dl_kbps);

/* Configure per-session meter (CIR = Session-AMBR). */
int upf_session_meter_configure(upf_session_meter_t *m,
                                 uint64_t ambr_ul_kbps, uint64_t ambr_dl_kbps);

/* ── Metering (fast-path) ── */

/* Check packet against meter. Returns 1=pass(GREEN), 0=drop(RED).
 * direction: 0=UL, 1=DL. pkt_len: IP packet length in bytes. */
int upf_qer_meter_check(upf_qer_meter_t *m, uint8_t direction, uint32_t pkt_len);
int upf_session_meter_check(upf_session_meter_t *m, uint8_t direction, uint32_t pkt_len);

/* ── Global init/cleanup ── */

/* Initialize meter arrays. Call once at startup. */
int upf_qos_meter_init(void);

/* Get QER meter for a session+qer index. */
upf_qer_meter_t *upf_qer_meter_get(uint32_t session_idx, uint8_t qer_idx);

/* Get session meter for a session index. */
upf_session_meter_t *upf_session_meter_get(uint32_t session_idx);

#endif /* UPF_QOS_METER_H */
