/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_qos_meter.c — DPDK rte_meter srTCM-based QoS enforcement
 *
 * TS 29.244 §5.4:     QER — Gate + MBR metering
 * TS 23.501 §5.7.1.6: Session-AMBR
 * TS 23.501 §5.7.3:   UE-AMBR (non-GBR flows only)
 *
 * Uses srTCM (RFC 2697) per rte_meter:
 *   CIR = rate limit (MBR or AMBR) in bytes/sec
 *   CBS = burst allowance (10ms of CIR or 12 MTUs)
 *   EBS = 0 (hard policing)
 *   GREEN = pass, RED = drop
 */

#include <string.h>
#include <rte_meter.h>
#include <rte_cycles.h>
#include <rte_log.h>
#include <rte_malloc.h>

#include "upf_qos_meter.h"
#include "upf_types.h"

/* Guard: meter functions are no-ops until upf_qos_meter_init() completes */
static bool meter_subsystem_ready = false;

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

#define MAX_QER_METERS_PER_SESSION UPF_MAX_QER_PER_SESSION
#define MTU_BYTES 1500

/* ── Global meter storage ──
 *
 * Sized at init time from g_upf_max_sessions (runtime-configurable via
 * upf_dp_set_max_sessions()). Flattened to 1D arrays — access with the
 * QER_AT() / SESSION_METER_AT() helpers below.  */

static upf_qer_meter_t      *qer_meters = NULL;     /* [g_upf_max_sessions * MAX_QER_METERS_PER_SESSION] */
static upf_session_meter_t  *session_meters = NULL; /* [g_upf_max_sessions] */

#define QER_AT(sess_idx, qer_idx) \
    (&qer_meters[(sess_idx) * MAX_QER_METERS_PER_SESSION + (qer_idx)])
#define SESSION_METER_AT(sess_idx) \
    (&session_meters[(sess_idx)])

/* ── Internal: configure a single srTCM direction ── */

static int _configure_srtcm(struct rte_meter_srtcm_profile *profile,
                             struct rte_meter_srtcm *meter,
                             uint64_t rate_kbps)
{
    if (rate_kbps == 0)
        return 0;  /* unlimited — don't configure */

    struct rte_meter_srtcm_params params;
    uint64_t cir = rate_kbps * 1000 / 8;  /* kbps → bytes/sec */
    uint64_t cbs = cir / 100;             /* 10ms burst */
    if (cbs < 12 * MTU_BYTES)
        cbs = 12 * MTU_BYTES;             /* minimum 12 MTUs */

    params.cir = cir;
    params.cbs = cbs;
    params.ebs = 0;  /* hard policing — RED = drop */

    int ret = rte_meter_srtcm_profile_config(profile, &params);
    if (ret != 0) {
        RTE_LOG(ERR, UPF, "QoS: srTCM profile config failed (rate=%lu kbps): %d\n",
                (unsigned long)rate_kbps, ret);
        return -1;
    }

    ret = rte_meter_srtcm_config(meter, profile);
    if (ret != 0) {
        RTE_LOG(ERR, UPF, "QoS: srTCM meter config failed: %d\n", ret);
        return -1;
    }

    return 1;  /* configured */
}

/* ── Internal: check a single srTCM ── */

static inline int _check_srtcm(struct rte_meter_srtcm *meter,
                                struct rte_meter_srtcm_profile *profile,
                                uint32_t pkt_len)
{
    uint64_t time = rte_rdtsc();
    enum rte_color color = rte_meter_srtcm_color_blind_check(
        meter, profile, time, pkt_len);
    /* GREEN = pass, YELLOW/RED = drop (EBS=0 means no yellow) */
    return (color == RTE_COLOR_GREEN) ? 1 : 0;
}

/* ── Per-QER meter ── */

int upf_qer_meter_configure(upf_qer_meter_t *m,
                             uint64_t mbr_ul_kbps, uint64_t mbr_dl_kbps)
{
    if (!m || !meter_subsystem_ready) return -1;
    memset(m, 0, sizeof(*m));

    int ul_ok = _configure_srtcm(&m->profile_ul, &m->meter_ul, mbr_ul_kbps);
    int dl_ok = _configure_srtcm(&m->profile_dl, &m->meter_dl, mbr_dl_kbps);

    m->configured = (ul_ok > 0 || dl_ok > 0);

    if (m->configured) {
        RTE_LOG(INFO, UPF, "QoS: QER meter configured MBR UL=%lu DL=%lu kbps\n",
                (unsigned long)mbr_ul_kbps, (unsigned long)mbr_dl_kbps);
    }
    return 0;
}

int upf_qer_meter_check(upf_qer_meter_t *m, uint8_t direction, uint32_t pkt_len)
{
    if (!m || !m->configured) return 1;  /* no meter = pass */

    if (direction == 0) {
        /* UL */
        return _check_srtcm(&m->meter_ul, &m->profile_ul, pkt_len);
    } else {
        /* DL */
        return _check_srtcm(&m->meter_dl, &m->profile_dl, pkt_len);
    }
}

/* ── Per-session meter (Session-AMBR) ── */

int upf_session_meter_configure(upf_session_meter_t *m,
                                 uint64_t ambr_ul_kbps, uint64_t ambr_dl_kbps)
{
    if (!m || !meter_subsystem_ready) return -1;
    memset(m, 0, sizeof(*m));

    int ul_ok = _configure_srtcm(&m->profile_ul, &m->meter_ul, ambr_ul_kbps);
    int dl_ok = _configure_srtcm(&m->profile_dl, &m->meter_dl, ambr_dl_kbps);

    m->configured = (ul_ok > 0 || dl_ok > 0);

    if (m->configured) {
        RTE_LOG(INFO, UPF, "QoS: Session-AMBR meter configured UL=%lu DL=%lu kbps\n",
                (unsigned long)ambr_ul_kbps, (unsigned long)ambr_dl_kbps);
    }
    return 0;
}

int upf_session_meter_check(upf_session_meter_t *m, uint8_t direction, uint32_t pkt_len)
{
    if (!m || !m->configured) return 1;
    if (direction == 0)
        return _check_srtcm(&m->meter_ul, &m->profile_ul, pkt_len);
    else
        return _check_srtcm(&m->meter_dl, &m->profile_dl, pkt_len);
}

/* UE-AMBR enforcement removed — per TS 23.501 v19.7.0 §5.7.1.6:
 * "The (R)AN shall enforce UE-AMBR (see clause 5.7.2.6) in UL and DL
 * per UE for Non-GBR QoS Flows." UPF responsibilities (§5.7.1.6) are
 * Session-AMBR + per-flow MBR (QER) only; TS 29.244 v19.5.0 has no
 * UE-AMBR IE for the SMF→UPF wire. The previous per-IMSI rte_meter +
 * ue_meter_hash here was an out-of-spec overreach. */

/* ── Global init ── */

int upf_qos_meter_init(void)
{
    /* Idempotent — safe to call multiple times */
    if (meter_subsystem_ready)
        return 0;

    /* Allocate per-session meter storage from the runtime cap.
     * +1 because rte_hash returns indices in [0, entries] (entries+1 slots). */
    size_t qer_bytes = (size_t)(g_upf_max_sessions + 1)
                       * MAX_QER_METERS_PER_SESSION * sizeof(upf_qer_meter_t);
    size_t sm_bytes  = (size_t)(g_upf_max_sessions + 1) * sizeof(upf_session_meter_t);

    if (!qer_meters)
        qer_meters = rte_zmalloc("upf_qer_meters", qer_bytes, RTE_CACHE_LINE_SIZE);
    if (!session_meters)
        session_meters = rte_zmalloc("upf_session_meters", sm_bytes, RTE_CACHE_LINE_SIZE);

    if (!qer_meters || !session_meters) {
        RTE_LOG(ERR, UPF, "QoS: failed to allocate meter storage "
                "(qer=%zu bytes, sess=%zu bytes)\n", qer_bytes, sm_bytes);
        if (qer_meters)     { rte_free(qer_meters);     qer_meters = NULL; }
        if (session_meters) { rte_free(session_meters); session_meters = NULL; }
        return -1;
    }

    meter_subsystem_ready = true;

    RTE_LOG(INFO, UPF, "QoS: meter subsystem initialized "
            "(max %u sessions, %d QERs/session)\n",
            g_upf_max_sessions, MAX_QER_METERS_PER_SESSION);
    return 0;
}

/* ── Accessor functions ── */

upf_qer_meter_t *upf_qer_meter_get(uint32_t session_idx, uint8_t qer_idx)
{
    if (!qer_meters || session_idx >= g_upf_max_sessions
            || qer_idx >= MAX_QER_METERS_PER_SESSION)
        return NULL;
    return QER_AT(session_idx, qer_idx);
}

upf_session_meter_t *upf_session_meter_get(uint32_t session_idx)
{
    if (!session_meters || session_idx >= g_upf_max_sessions) return NULL;
    return &session_meters[session_idx];
}
