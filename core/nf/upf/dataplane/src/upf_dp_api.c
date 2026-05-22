/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_dp_api.c — Public C API for UPF data plane
 *
 * Wraps session table + rule management into a clean API for Python ctypes.
 */

#include <string.h>
#include <arpa/inet.h>
#include <rte_eal.h>
#include <rte_log.h>

#include "upf_dp_api.h"
#include "upf_qos_meter.h"
#include "upf_session_table.h"
#include "upf_sdf_parser.h"
#include "upf_classifier.h"
#include "upf_slice.h"
#include "upf_pkt_io.h"
#include "upf_report.h"

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

static bool dp_initialized = false;

/* Runtime session cap. Python overrides this via upf_dp_set_max_sessions()
 * before upf_dp_init(); defaults to the compile-time UPF_MAX_SESSIONS. */
uint32_t g_upf_max_sessions = UPF_MAX_SESSIONS;

/* PMD-mode runtime knobs. Defaults come from compile-time macros (which
 * the Makefile can override), but Python always re-sets them from the
 * DB via upf_dp_set_pmd_tuning() on every boot. */
#ifndef MBUF_POOL_SIZE
#define MBUF_POOL_SIZE 8192
#endif
#ifndef RX_RING_SIZE
#define RX_RING_SIZE 1024
#endif
#ifndef TX_RING_SIZE
#define TX_RING_SIZE 1024
#endif
uint32_t g_upf_mbuf_pool_size = MBUF_POOL_SIZE;
uint16_t g_upf_rx_ring_size   = RX_RING_SIZE;
uint16_t g_upf_tx_ring_size   = TX_RING_SIZE;

int upf_dp_set_max_sessions(uint32_t n)
{
    if (dp_initialized) {
        RTE_LOG(WARNING, UPF, "upf_dp_set_max_sessions(%u) ignored — "
                "already initialized\n", n);
        return -1;
    }
    if (n < 256) {
        RTE_LOG(WARNING, UPF, "upf_dp_set_max_sessions(%u) rejected — "
                "minimum 256\n", n);
        return -1;
    }
    g_upf_max_sessions = n;
    return 0;
}

int upf_dp_set_pmd_tuning(uint32_t mbuf, uint16_t rx, uint16_t tx)
{
    if (dp_initialized) {
        RTE_LOG(WARNING, UPF, "upf_dp_set_pmd_tuning() ignored — "
                "already initialized\n");
        return -1;
    }
    /* Lower bounds match the DPDK defaults; values under these cause
     * init failures anyway. 0 is treated as "leave current". */
    if (mbuf && mbuf < 1024) return -1;
    if (rx   && rx   < 128)  return -1;
    if (tx   && tx   < 128)  return -1;
    if (mbuf) g_upf_mbuf_pool_size = mbuf;
    if (rx)   g_upf_rx_ring_size   = rx;
    if (tx)   g_upf_tx_ring_size   = tx;
    return 0;
}

/* ── Lifecycle ── */

int upf_dp_init(int argc, char **argv)
{
    if (dp_initialized) {
        RTE_LOG(WARNING, UPF, "Data plane already initialized\n");
        return 0;
    }

    int ret = rte_eal_init(argc, argv);
    if (ret < 0) {
        fprintf(stderr, "[UPF] rte_eal_init failed: %d\n", ret);
        return -1;
    }

    RTE_LOG(INFO, UPF, "DPDK EAL initialized\n");

    if (upf_session_table_init() < 0) {
        RTE_LOG(ERR, UPF, "Failed to init session table\n");
        return -1;
    }

    if (upf_qos_meter_init() < 0) {
        RTE_LOG(WARNING, UPF, "QoS meter init failed — metering disabled\n");
    }

    /* TS 29.244 §7.5.8 report ring — used by the BUFF action in
     * upf_pkt_io.c to signal DLDR on first-buffered DL packet, and
     * later by URR / ErrInd producers. Non-fatal on failure: the
     * data plane still forwards, only the §7.5.8 reports are lost
     * and TS 23.502 §4.8.2.2b paging won't fire. */
    if (upf_report_init(0) < 0) {
        RTE_LOG(WARNING, UPF, "Report ring init failed — §7.5.8 reports disabled\n");
    }

    dp_initialized = true;
    RTE_LOG(INFO, UPF, "UPF data plane initialized\n");
    return 0;
}

void upf_dp_cleanup(void)
{
    if (!dp_initialized) return;

    upf_session_table_destroy();
    rte_eal_cleanup();
    dp_initialized = false;

    /* Can't use RTE_LOG after eal cleanup */
    fprintf(stderr, "[UPF] Data plane cleaned up\n");
}

/* ── Session management ── */

upf_session_t *upf_dp_session_create(const char *imsi, uint8_t pdu_session_id,
                                      const char *dnn, uint8_t sst, uint32_t sd,
                                      uint32_t ue_addr)
{
    if (!dp_initialized) return NULL;

    upf_session_t *sess = upf_session_create(imsi, pdu_session_id);
    if (!sess) return NULL;

    if (dnn) strncpy(sess->dnn, dnn, UPF_MAX_DNN_LEN - 1);
    sess->sst = sst;
    sess->sd = sd;
    /* Go passes ue_addr in HOST byte order (binary.BigEndian.Uint32).
     * Classifier compares against raw-packet src/dst (NETWORK byte order)
     * and SDF filter src/dst (also NETWORK from inet_pton), so store in
     * network order here. The ueip_hash path normalizes separately. */
    sess->ue_addr = htonl(ue_addr);

    return sess;
}

int upf_dp_session_delete(const char *imsi, uint8_t pdu_session_id)
{
    if (!dp_initialized) return -1;
    return upf_session_delete(imsi, pdu_session_id);
}

upf_session_t *upf_dp_session_get(const char *imsi, uint8_t pdu_session_id)
{
    if (!dp_initialized) return NULL;
    return upf_session_get(imsi, pdu_session_id);
}

/* ── Rule management ── */

/* Locate a session's PDR slot by ID with three-way semantics:
 *   1. Exact ID match (active or inactive) → return that slot, *reused=true.
 *   2. No ID match, n_pdr < cap → return next append slot, *reused=false.
 *   3. No ID match, array full → scan for first inactive slot to
 *      reclaim → *reused=true. Returns NULL only if all slots are
 *      active and none match the ID.
 *
 * This makes Create-* idempotent across §7.5.2 / §7.5.4 (re-Create
 * with same ID = replace) and lets Create after Remove reclaim the
 * inactive slot rather than burn another. Same pattern is inlined
 * for FAR/QER/URR below — kept inline for the type-specific array
 * access (no void-pointer gymnastics on per-type structs). */
static upf_pdr_t *find_or_alloc_pdr_slot(upf_session_t *sess, uint16_t pdr_id, bool *reused) {
    *reused = true;
    for (uint8_t i = 0; i < sess->n_pdr; i++) {
        if (sess->pdr[i].pdr_id == pdr_id) return &sess->pdr[i];
    }
    if (sess->n_pdr < UPF_MAX_PDR_PER_SESSION) {
        *reused = false;
        return &sess->pdr[sess->n_pdr];   /* caller increments n_pdr */
    }
    for (uint8_t i = 0; i < sess->n_pdr; i++) {
        if (!sess->pdr[i].active) return &sess->pdr[i];
    }
    return NULL;
}

int upf_dp_add_pdr(const char *imsi, uint8_t pdu_session_id,
                    uint16_t pdr_id, uint32_t precedence,
                    uint8_t pdi_source, uint8_t qfi,
                    uint32_t far_id, uint32_t qer_id, uint32_t urr_id,
                    const char *sdf_rules)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    bool reused;
    upf_pdr_t *pdr = find_or_alloc_pdr_slot(sess, pdr_id, &reused);
    if (!pdr) return -1;
    /* Wipe before stamping — covers replace-by-ID (matches §7.5.4.2
     * "PDI shall replace the PDI previously stored") and reclaimed
     * inactive slots (no stale fields). */
    memset(pdr, 0, sizeof(*pdr));

    pdr->pdr_id = pdr_id;
    pdr->precedence = precedence;
    pdr->pdi_source = pdi_source;
    pdr->ue_addr = sess->ue_addr;
    pdr->qfi = qfi;
    pdr->far_id = far_id;
    pdr->qer_id = qer_id;
    pdr->urr_id = urr_id;
    pdr->active = true;

    /* Parse SDF filter rules (newline-separated) */
    if (sdf_rules && sdf_rules[0] != '\0') {
        char buf[1024];
        strncpy(buf, sdf_rules, sizeof(buf) - 1);
        buf[sizeof(buf) - 1] = '\0';

        char *line = strtok(buf, "\n");
        while (line && pdr->n_sdf < UPF_MAX_SDF_FILTERS) {
            /* Skip leading whitespace */
            while (*line == ' ' || *line == '\t') line++;
            if (*line != '\0') {
                if (upf_sdf_parse(line, &pdr->sdf[pdr->n_sdf]) == 0) {
                    pdr->n_sdf++;
                } else {
                    RTE_LOG(WARNING, UPF, "Failed to parse SDF: %s\n", line);
                }
            }
            line = strtok(NULL, "\n");
        }
    }

    if (!reused) sess->n_pdr++;
    RTE_LOG(INFO, UPF, "PDR %u %s in %s/%u (precedence=%u, %u SDF filters)\n",
            pdr_id, reused ? "replaced" : "added",
            imsi, pdu_session_id, precedence, pdr->n_sdf);
    return 0;
}

/* Idempotent FAR slot lookup — see find_or_alloc_pdr_slot for the
 * three-way semantics (exact ID match → replace; new ID + room →
 * append; new ID + full → reclaim inactive; otherwise NULL). */
static upf_far_t *find_or_alloc_far_slot(upf_session_t *sess, uint32_t far_id, bool *reused) {
    *reused = true;
    for (uint8_t i = 0; i < sess->n_far; i++) {
        if (sess->far[i].far_id == far_id) return &sess->far[i];
    }
    if (sess->n_far < UPF_MAX_FAR_PER_SESSION) {
        *reused = false;
        return &sess->far[sess->n_far];
    }
    for (uint8_t i = 0; i < sess->n_far; i++) {
        if (!sess->far[i].active) return &sess->far[i];
    }
    return NULL;
}

int upf_dp_add_far(const char *imsi, uint8_t pdu_session_id,
                    uint32_t far_id, uint8_t action, uint8_t dst_iface,
                    uint32_t ohc_teid, uint32_t ohc_peer_addr,
                    uint16_t ohc_peer_port, uint8_t ohc_type)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    bool reused;
    upf_far_t *far = find_or_alloc_far_slot(sess, far_id, &reused);
    if (!far) return -1;
    memset(far, 0, sizeof(*far));

    far->far_id = far_id;
    far->action = action;
    far->dst_iface = dst_iface;
    far->ohc_teid = ohc_teid;
    far->ohc_peer_addr = ohc_peer_addr;
    far->ohc_peer_port = ohc_peer_port;
    far->ohc_type = ohc_type;
    far->active = true;

    if (!reused) sess->n_far++;
    RTE_LOG(INFO, UPF, "FAR %u %s in %s/%u (action=%u)\n",
            far_id, reused ? "replaced" : "added",
            imsi, pdu_session_id, action);
    return 0;
}

int upf_dp_update_far(const char *imsi, uint8_t pdu_session_id,
                      uint32_t far_id, uint32_t ohc_teid,
                      uint32_t ohc_peer_addr, uint16_t ohc_peer_port)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    for (unsigned i = 0; i < sess->n_far; i++) {
        if (sess->far[i].far_id == far_id) {
            uint8_t prev_action = sess->far[i].action;
            sess->far[i].ohc_teid = ohc_teid;
            sess->far[i].ohc_peer_addr = ohc_peer_addr;
            sess->far[i].ohc_peer_port = ohc_peer_port;

            /* TS 29.244 §5.2.1: Transition BUFF→FORW when tunnel info arrives.
             * Flush any buffered DL packets through the now-valid tunnel. */
            if (prev_action == 2 /* buffer */) {
                sess->far[i].action = 1; /* forward */
                sess->far[i].ohc_type = 1; /* GTP-U/UDP/IPv4 */
                int n = upf_pkt_io_flush_dl_buf(sess, &sess->far[i]);
                RTE_LOG(INFO, UPF, "FAR %u updated for %s/%u (BUFF→FORW, "
                        "TEID=0x%08x, peer=%u.%u.%u.%u:%u, flushed=%d)\n",
                        far_id, imsi, pdu_session_id, ohc_teid,
                        (ohc_peer_addr) & 0xFF, (ohc_peer_addr >> 8) & 0xFF,
                        (ohc_peer_addr >> 16) & 0xFF, (ohc_peer_addr >> 24) & 0xFF,
                        ohc_peer_port, n);
            } else {
                RTE_LOG(INFO, UPF, "FAR %u updated for %s/%u (TEID=0x%08x, "
                        "peer=%u.%u.%u.%u:%u)\n",
                        far_id, imsi, pdu_session_id, ohc_teid,
                        (ohc_peer_addr) & 0xFF, (ohc_peer_addr >> 8) & 0xFF,
                        (ohc_peer_addr >> 16) & 0xFF, (ohc_peer_addr >> 24) & 0xFF,
                        ohc_peer_port);
            }
            return 0;
        }
    }
    RTE_LOG(WARNING, UPF, "FAR %u not found in %s/%u for update\n",
            far_id, imsi, pdu_session_id);
    return -1;
}

/* TS 29.244 v19.5.0 §7.5.4.2 Update PDR. Wholesale-replace by ID:
 * caller passes the full desired-state values; the rule MUST already
 * exist in the session ("the Update PDR IE shall identify the PDR
 * ... configured for that PFCP session" — peer error if not).
 * Delegates to upf_dp_add_pdr's idempotent path so the actual replace
 * uses the same memset+stamp logic — same C semantics, distinct entry
 * point so the spec citation lands on the §7.5.4.2 caller. */
int upf_dp_update_pdr(const char *imsi, uint8_t pdu_session_id,
                      uint16_t pdr_id, uint32_t precedence,
                      uint8_t pdi_source, uint8_t qfi,
                      uint32_t far_id, uint32_t qer_id, uint32_t urr_id,
                      const char *sdf_rules)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    bool exists = false;
    for (uint8_t i = 0; i < sess->n_pdr; i++) {
        if (sess->pdr[i].pdr_id == pdr_id) { exists = true; break; }
    }
    if (!exists) {
        RTE_LOG(WARNING, UPF, "Update PDR %u not found in %s/%u\n",
                pdr_id, imsi, pdu_session_id);
        return -1;
    }
    return upf_dp_add_pdr(imsi, pdu_session_id, pdr_id, precedence,
                          pdi_source, qfi, far_id, qer_id, urr_id, sdf_rules);
}

/* TS 29.244 v19.5.0 §7.5.4.5 Update QER. Same pattern as Update PDR
 * — wholesale replace via the idempotent Add path; rule must exist. */
int upf_dp_update_qer(const char *imsi, uint8_t pdu_session_id,
                      uint32_t qer_id, uint8_t qfi,
                      uint8_t gate_ul, uint8_t gate_dl,
                      uint64_t mbr_ul, uint64_t mbr_dl,
                      uint64_t gbr_ul, uint64_t gbr_dl)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    bool exists = false;
    for (uint8_t i = 0; i < sess->n_qer; i++) {
        if (sess->qer[i].qer_id == qer_id) { exists = true; break; }
    }
    if (!exists) {
        RTE_LOG(WARNING, UPF, "Update QER %u not found in %s/%u\n",
                qer_id, imsi, pdu_session_id);
        return -1;
    }
    return upf_dp_add_qer(imsi, pdu_session_id, qer_id, qfi,
                          gate_ul, gate_dl, mbr_ul, mbr_dl, gbr_ul, gbr_dl);
}

/* TS 29.244 v19.5.0 §7.5.4.4 Update URR. Wholesale replace; rule
 * must exist. NOTE: counters are reset (memset) — SMF should fetch
 * via §7.5.4.10 Query URR first if charging requires final usage. */
int upf_dp_update_urr(const char *imsi, uint8_t pdu_session_id,
                      uint32_t urr_id, uint8_t meas_method,
                      uint8_t reporting_trigger,
                      uint64_t vol_thresh_ul, uint64_t vol_thresh_dl,
                      uint32_t time_thresh)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    bool exists = false;
    for (uint8_t i = 0; i < sess->n_urr; i++) {
        if (sess->urr[i].urr_id == urr_id) { exists = true; break; }
    }
    if (!exists) {
        RTE_LOG(WARNING, UPF, "Update URR %u not found in %s/%u\n",
                urr_id, imsi, pdu_session_id);
        return -1;
    }
    return upf_dp_add_urr(imsi, pdu_session_id, urr_id, meas_method,
                          reporting_trigger, vol_thresh_ul, vol_thresh_dl, time_thresh);
}

/* TS 23.502 §4.2.6 step 6a — AN Release user-plane tear-down.
 * Flips DL FAR action FORW→BUFF and clears tunnel info so the UPF
 * starts buffering DL packets instead of forwarding to a stale gNB
 * TEID. Idempotent: returns 0 if the FAR is already in BUFF mode. */
int upf_dp_deactivate_dl(const char *imsi, uint8_t pdu_session_id,
                         uint32_t far_id)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    for (unsigned i = 0; i < sess->n_far; i++) {
        if (sess->far[i].far_id != far_id) continue;
        if (sess->far[i].action == 2 /* BUFF */) {
            return 0; /* already deactivated — idempotent */
        }
        uint8_t prev_action = sess->far[i].action;
        sess->far[i].action = 2;        /* BUFF — TS 29.244 §8.2.26 */
        sess->far[i].ohc_teid = 0;
        sess->far[i].ohc_peer_addr = 0;
        sess->far[i].ohc_peer_port = 0;
        /* ohc_type stays; next activate (BUFF→FORW) reuses it. */
        RTE_LOG(INFO, UPF,
                "FAR %u deactivated for %s/%u (action %u→BUFF, tunnel cleared)\n",
                far_id, imsi, pdu_session_id, prev_action);
        return 0;
    }
    RTE_LOG(WARNING, UPF, "FAR %u not found in %s/%u for deactivate\n",
            far_id, imsi, pdu_session_id);
    return -1;
}

/* Idempotent QER slot lookup. Returns the slot's index in sess->qer
 * (out_idx) so the caller can key the rte_meter slot at
 * meter_array[session_idx][qer_idx]. */
static upf_qer_t *find_or_alloc_qer_slot(upf_session_t *sess, uint32_t qer_id,
                                          bool *reused, uint8_t *out_idx) {
    *reused = true;
    for (uint8_t i = 0; i < sess->n_qer; i++) {
        if (sess->qer[i].qer_id == qer_id) { *out_idx = i; return &sess->qer[i]; }
    }
    if (sess->n_qer < UPF_MAX_QER_PER_SESSION) {
        *reused = false;
        *out_idx = sess->n_qer;
        return &sess->qer[sess->n_qer];
    }
    for (uint8_t i = 0; i < sess->n_qer; i++) {
        if (!sess->qer[i].active) { *out_idx = i; return &sess->qer[i]; }
    }
    return NULL;
}

int upf_dp_add_qer(const char *imsi, uint8_t pdu_session_id,
                    uint32_t qer_id, uint8_t qfi,
                    uint8_t gate_ul, uint8_t gate_dl,
                    uint64_t mbr_ul, uint64_t mbr_dl,
                    uint64_t gbr_ul, uint64_t gbr_dl)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    bool reused;
    uint8_t qer_idx;
    upf_qer_t *qer = find_or_alloc_qer_slot(sess, qer_id, &reused, &qer_idx);
    if (!qer) return -1;
    memset(qer, 0, sizeof(*qer));

    qer->qer_id = qer_id;
    qer->qfi = qfi;
    qer->gate_status_ul = gate_ul;
    qer->gate_status_dl = gate_dl;
    qer->mbr_ul = mbr_ul;
    qer->mbr_dl = mbr_dl;
    qer->gbr_ul = gbr_ul;
    qer->gbr_dl = gbr_dl;
    qer->active = true;

    if (!reused) sess->n_qer++;

    /* Configure rte_meter srTCM for MBR enforcement (TS 29.244
     * v19.5.0 §5.4.2). upf_qer_meter_configure memsets the meter to
     * fresh state, so a replace-by-ID or reclaimed-slot path resets
     * the token bucket cleanly even if MBR rates changed. */
    if (mbr_ul > 0 || mbr_dl > 0) {
        upf_qer_meter_t *qm = upf_qer_meter_get(sess->session_idx, qer_idx);
        if (qm) {
            upf_qer_meter_configure(qm, mbr_ul, mbr_dl);
        }
    }

    RTE_LOG(INFO, UPF, "QER %u %s in %s/%u (QFI=%u, MBR UL/DL=%lu/%lu kbps)\n",
            qer_id, reused ? "replaced" : "added",
            imsi, pdu_session_id, qfi,
            (unsigned long)mbr_ul, (unsigned long)mbr_dl);
    return 0;
}

/* Idempotent URR slot lookup. Note: replace-by-ID resets vol/pkt
 * counters to zero (memset in caller). For §7.5.4.4 Update URR a
 * replace is appropriate only when the SMF intends to reset
 * accumulators; the spec leaves counter-reset semantics to the
 * implementation, but harvesting via Query URR before Update is
 * the safe pattern (which we now also support). */
static upf_urr_t *find_or_alloc_urr_slot(upf_session_t *sess, uint32_t urr_id, bool *reused) {
    *reused = true;
    for (uint8_t i = 0; i < sess->n_urr; i++) {
        if (sess->urr[i].urr_id == urr_id) return &sess->urr[i];
    }
    if (sess->n_urr < UPF_MAX_URR_PER_SESSION) {
        *reused = false;
        return &sess->urr[sess->n_urr];
    }
    for (uint8_t i = 0; i < sess->n_urr; i++) {
        if (!sess->urr[i].active) return &sess->urr[i];
    }
    return NULL;
}

int upf_dp_add_urr(const char *imsi, uint8_t pdu_session_id,
                    uint32_t urr_id, uint8_t meas_method,
                    uint8_t reporting_trigger,
                    uint64_t vol_thresh_ul, uint64_t vol_thresh_dl,
                    uint32_t time_thresh)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    bool reused;
    upf_urr_t *urr = find_or_alloc_urr_slot(sess, urr_id, &reused);
    if (!urr) return -1;
    memset(urr, 0, sizeof(*urr));

    urr->urr_id = urr_id;
    urr->measurement_method = meas_method;
    urr->reporting_trigger = reporting_trigger;
    urr->vol_threshold_ul = vol_thresh_ul;
    urr->vol_threshold_dl = vol_thresh_dl;
    urr->time_threshold = time_thresh;
    urr->active = true;

    if (!reused) sess->n_urr++;
    RTE_LOG(INFO, UPF, "URR %u %s in %s/%u\n",
            urr_id, reused ? "replaced" : "added", imsi, pdu_session_id);
    return 0;
}

/* TS 29.244 v19.5.0 §7.5.4.6 Remove PDR — "shall identify the PDR to
 * be deleted". Sets active=false; pdr_match() in upf_classifier.c
 * line 184 already short-circuits on !active so the rule is
 * immediately invisible to the data path. The QER/URR meter slots
 * keyed by (session_idx, qer_idx) and the URR counters live in the
 * session arrays — they are reset to zero on the next §7.5.2 that
 * lands on this slot via session_pool[idx] memset in
 * upf_session_create. No selective slot release inside an active
 * session: the static session arrays are sized for the lifetime of
 * the session (UPF_MAX_*_PER_SESSION). */
int upf_dp_remove_pdr(const char *imsi, uint8_t pdu_session_id, uint16_t pdr_id)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    for (uint8_t i = 0; i < sess->n_pdr; i++) {
        if (sess->pdr[i].pdr_id == pdr_id && sess->pdr[i].active) {
            sess->pdr[i].active = false;
            RTE_LOG(INFO, UPF, "PDR %u removed from %s/%u\n",
                    pdr_id, imsi, pdu_session_id);
            return 0;
        }
    }
    return -1;
}

/* TS 29.244 v19.5.0 §7.5.4.7 Remove FAR. Any PDR still referencing
 * the removed FAR-ID (via pdr->far_id) gets find_far()=NULL and
 * result->action=0 → drop. Per spec the SMF should remove referencing
 * PDRs first; if it doesn't, traffic on orphan PDRs is dropped (a
 * fail-closed posture, not a crash). */
int upf_dp_remove_far(const char *imsi, uint8_t pdu_session_id, uint32_t far_id)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    for (uint8_t i = 0; i < sess->n_far; i++) {
        if (sess->far[i].far_id == far_id && sess->far[i].active) {
            sess->far[i].action = 0;   /* drop until re-added */
            sess->far[i].active = false;
            RTE_LOG(INFO, UPF, "FAR %u removed from %s/%u\n",
                    far_id, imsi, pdu_session_id);
            return 0;
        }
    }
    return -1;
}

/* TS 29.244 v19.5.0 §7.5.4.9 Remove QER. PDRs referencing the QER
 * via pdr->qer_id get find_qer()=NULL → no gate / no MBR check.
 * Effective behaviour: rule still forwards but at session-AMBR /
 * UE-AMBR rate only. The per-QER rte_meter slot at meter_array[
 * session_idx][qer_idx] is not reclaimed inside an active session;
 * it gets re-initialised on the next CreateSession that lands on
 * this session_idx via the upf_session_t memset in upf_session_create. */
int upf_dp_remove_qer(const char *imsi, uint8_t pdu_session_id, uint32_t qer_id)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    for (uint8_t i = 0; i < sess->n_qer; i++) {
        if (sess->qer[i].qer_id == qer_id && sess->qer[i].active) {
            sess->qer[i].active = false;
            RTE_LOG(INFO, UPF, "QER %u removed from %s/%u\n",
                    qer_id, imsi, pdu_session_id);
            return 0;
        }
    }
    return -1;
}

/* TS 29.244 v19.5.0 §7.5.4.8 Remove URR. Counters in sess->urr[i]
 * are last-known-good until session deletion; the SMF should
 * Query URR (§7.5.4.10) to harvest final usage BEFORE removing
 * if a charging anchor is required. Today's UP function does not
 * include Usage Report IEs in the §7.5.5 Modification Response
 * (Query URR not yet implemented — see TODO at handler.go). */
int upf_dp_remove_urr(const char *imsi, uint8_t pdu_session_id, uint32_t urr_id)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;
    for (uint8_t i = 0; i < sess->n_urr; i++) {
        if (sess->urr[i].urr_id == urr_id && sess->urr[i].active) {
            sess->urr[i].active = false;
            RTE_LOG(INFO, UPF, "URR %u removed from %s/%u\n",
                    urr_id, imsi, pdu_session_id);
            return 0;
        }
    }
    return -1;
}

int upf_dp_set_bar(const char *imsi, uint8_t pdu_session_id,
                    uint8_t bar_id, uint8_t notify_cp,
                    uint16_t buf_pkt_count)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    sess->bar.bar_id = bar_id;
    sess->bar.notify_cp = notify_cp;
    sess->bar.buf_pkt_count = buf_pkt_count;
    sess->bar.active = true;

    RTE_LOG(INFO, UPF, "BAR %u set for %s/%u\n", bar_id, imsi, pdu_session_id);
    return 0;
}

/* ── Stats ── */

int upf_dp_get_urr_stats(const char *imsi, uint8_t pdu_session_id,
                          uint32_t urr_id,
                          uint64_t *vol_ul, uint64_t *vol_dl,
                          uint64_t *pkt_ul, uint64_t *pkt_dl)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    for (uint8_t i = 0; i < sess->n_urr; i++) {
        if (sess->urr[i].urr_id == urr_id && sess->urr[i].active) {
            if (vol_ul) *vol_ul = sess->urr[i].vol_ul;
            if (vol_dl) *vol_dl = sess->urr[i].vol_dl;
            if (pkt_ul) *pkt_ul = sess->urr[i].pkt_ul;
            if (pkt_dl) *pkt_dl = sess->urr[i].pkt_dl;
            return 0;
        }
    }
    return -1;
}

int upf_dp_get_qer_stats(const char *imsi, uint8_t pdu_session_id,
                          uint32_t qer_id,
                          uint64_t *dropped_pkts_ul, uint64_t *dropped_pkts_dl,
                          uint64_t *dropped_bytes_ul, uint64_t *dropped_bytes_dl)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    for (uint8_t i = 0; i < sess->n_qer; i++) {
        if (sess->qer[i].qer_id == qer_id && sess->qer[i].active) {
            if (dropped_pkts_ul)  *dropped_pkts_ul  = sess->qer[i].dropped_pkts_ul;
            if (dropped_pkts_dl)  *dropped_pkts_dl  = sess->qer[i].dropped_pkts_dl;
            if (dropped_bytes_ul) *dropped_bytes_ul = sess->qer[i].dropped_bytes_ul;
            if (dropped_bytes_dl) *dropped_bytes_dl = sess->qer[i].dropped_bytes_dl;
            return 0;
        }
    }
    return -1;
}

/* ── Packet I/O ── */

int upf_dp_pkt_io_init(const char *n3_addr, uint16_t n3_port,
                        const char *tun_name, const char *tun_addr)
{
    upf_pkt_io_cfg_t cfg = {
        .n3_bind_addr = n3_addr,
        .n3_port = n3_port,
        .tun_name = tun_name,
        .tun_addr = tun_addr,
        .tun_mask = "255.255.0.0",
    };
    return upf_pkt_io_init(&cfg);
}

int upf_dp_pkt_io_run(void)
{
    return upf_pkt_io_run();
}

void upf_dp_pkt_io_stop(void)
{
    upf_pkt_io_stop();
}

int upf_dp_register_teid(uint32_t teid, const char *imsi, uint8_t pdu_session_id)
{
    return upf_pkt_io_add_teid_map(teid, imsi, pdu_session_id);
}

int upf_dp_register_ueip(uint32_t ue_addr, const char *imsi, uint8_t pdu_session_id)
{
    return upf_pkt_io_add_ueip_map(ue_addr, imsi, pdu_session_id);
}

/* TS 29.244 v19.5.0 §7.5.6 — release the F-TEID (§5.5.1) and UE IP
 * (§8.2.62) reverse-map slots a session held during §7.5.2.
 * Idempotent at the dataplane: missing keys return -1 but cause no
 * state change. The caller (upf/pfcp handler at session deletion)
 * sweeps every TEID/UE-IP it registered for the session — see the
 * RegisteredTEIDs / RegisteredUEIPs slices on HandlerSession. */
int upf_dp_unregister_teid(uint32_t teid)
{
    return upf_pkt_io_del_teid_map(teid);
}

int upf_dp_unregister_ueip(uint32_t ue_addr)
{
    return upf_pkt_io_del_ueip_map(ue_addr);
}

/* Batched release — one cgo trip walks both arrays. Each entry
 * runs through the same del_*_map path used by the singular
 * unregister, so the deferred-free ring + memset semantics are
 * identical. Returns the number of slots actually released
 * (matched + freed); missing keys count toward the total
 * iterated but NOT toward the released count. */
int upf_dp_unregister_batch(const uint32_t *teids, int n_teids,
                             const uint32_t *ueips, int n_ueips)
{
    int released = 0;
    if (teids && n_teids > 0) {
        for (int i = 0; i < n_teids; i++) {
            if (upf_pkt_io_del_teid_map(teids[i]) == 0) released++;
        }
    }
    if (ueips && n_ueips > 0) {
        for (int i = 0; i < n_ueips; i++) {
            if (upf_pkt_io_del_ueip_map(ueips[i]) == 0) released++;
        }
    }
    return released;
}

int upf_dp_classify_packet(const char *imsi, uint8_t pdu_session_id,
                            uint8_t direction,
                            const uint8_t *ip_pkt, uint16_t ip_len)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    upf_classify_result_t result;
    return upf_process_packet(sess, direction, 0 /* QFI from API test */,
                               ip_pkt, ip_len, &result);
}

void upf_dp_get_io_stats(upf_dp_io_stats_t *out)
{
    if (!out) return;
    upf_io_stats_t raw;
    upf_pkt_io_get_stats(&raw);
    out->ul_pkts = raw.ul_pkts;
    out->ul_bytes = raw.ul_bytes;
    out->dl_pkts = raw.dl_pkts;
    out->dl_bytes = raw.dl_bytes;
    out->ul_dropped    = raw.ul_dropped;
    out->dl_dropped    = raw.dl_dropped;
    out->ul_no_session = raw.ul_no_session;
    out->dl_no_session = raw.dl_no_session;
    out->ul_metered    = raw.ul_metered;
    out->dl_metered    = raw.dl_metered;
    out->gtpu_errors = raw.gtpu_errors;
    out->_debug_last_unknown_teid = raw._debug_last_unknown_teid;
    out->_debug_last_registered_teid = raw._debug_last_registered_teid;
}

uint32_t upf_dp_session_count(void)
{
    return upf_session_count();
}

/* ── Session-AMBR configuration (TS 23.501 §5.7.1.6) ── */
int upf_dp_set_session_ambr(const char *imsi, uint8_t pdu_session_id,
                             uint64_t ambr_ul_kbps, uint64_t ambr_dl_kbps)
{
    upf_session_t *sess = upf_session_get(imsi, pdu_session_id);
    if (!sess) return -1;

    sess->session_ambr_ul = ambr_ul_kbps;
    sess->session_ambr_dl = ambr_dl_kbps;

    upf_session_meter_t *sm = upf_session_meter_get(sess->session_idx);
    if (sm) {
        upf_session_meter_configure(sm, ambr_ul_kbps, ambr_dl_kbps);
    }

    RTE_LOG(INFO, UPF, "Session-AMBR set for %s/%u: UL=%lu DL=%lu kbps\n",
            imsi, pdu_session_id,
            (unsigned long)ambr_ul_kbps, (unsigned long)ambr_dl_kbps);
    return 0;
}

/* UE-AMBR (TS 23.501 v19.7.0 §5.7.1.6 + §5.7.2.6) is enforced by the
 * (R)AN, not the UPF — no upf_dp_set_ue_ambr exists by design. The
 * AMF puts UEAggregateMaximumBitRate in PDU Session Resource Setup
 * Request to the gNB; see nf/amf/ngap/pdusetup. PFCP itself has no
 * UE-AMBR IE (TS 29.244 v19.5.0 has zero "UE-AMBR" mentions). */

/* ═══════════════════════════════════════════════════════════════
 * Network Slicing API — TS 23.501 §5.15.4
 * 3 independent UPF channels: eMBB (SST=1), URLLC (SST=2), mIoT (SST=3)
 * ═══════════════════════════════════════════════════════════════ */

int upf_dp_slice_init(uint8_t slice_id, uint8_t sst, const char *name)
{
    return upf_slice_init(slice_id, sst, name);
}

void upf_dp_slice_destroy(uint8_t slice_id)
{
    upf_slice_destroy(slice_id);
}

void upf_dp_slice_get_stats(uint8_t slice_id, upf_dp_io_stats_t *out)
{
    if (!out) return;
    memset(out, 0, sizeof(*out));

    upf_io_stats_t raw;
    upf_slice_get_stats(slice_id, &raw);
    out->ul_pkts      = raw.ul_pkts;
    out->ul_bytes     = raw.ul_bytes;
    out->dl_pkts      = raw.dl_pkts;
    out->dl_bytes     = raw.dl_bytes;
    out->ul_dropped   = raw.ul_dropped;
    out->dl_dropped   = raw.dl_dropped;
    out->ul_metered   = raw.ul_metered;
    out->dl_metered   = raw.dl_metered;
    out->gtpu_errors  = raw.gtpu_errors;
}

uint32_t upf_dp_slice_session_count(uint8_t slice_id)
{
    upf_slice_ctx_t *s = upf_slice_get(slice_id);
    return s ? s->session_count : 0;
}

upf_session_t *upf_dp_slice_session_create(uint8_t slice_id,
                                            const char *imsi, uint8_t pdu_session_id,
                                            const char *dnn, uint8_t sst, uint32_t sd,
                                            uint32_t ue_addr)
{
    upf_slice_ctx_t *s = upf_slice_get(slice_id);
    if (!s) return NULL;

    upf_session_t *sess = upf_slice_session_create(s, imsi, pdu_session_id);
    if (!sess) return NULL;

    strncpy(sess->dnn, dnn ? dnn : "", sizeof(sess->dnn) - 1);
    sess->sst = sst;
    sess->sd = sd;
    /* Same convention as non-slice path: store in network byte order so
     * the classifier compares apples-to-apples with raw-packet src/dst. */
    sess->ue_addr = htonl(ue_addr);

    RTE_LOG(INFO, UPF, "Slice %s: session created %s/%u (DNN=%s, IP=0x%08X)\n",
            s->name, imsi, pdu_session_id, dnn ? dnn : "", ue_addr);
    return sess;
}

upf_session_t *upf_dp_slice_session_get(uint8_t slice_id,
                                          const char *imsi, uint8_t pdu_session_id)
{
    upf_slice_ctx_t *s = upf_slice_get(slice_id);
    return s ? upf_slice_session_get(s, imsi, pdu_session_id) : NULL;
}

int upf_dp_slice_session_delete(uint8_t slice_id,
                                 const char *imsi, uint8_t pdu_session_id)
{
    upf_slice_ctx_t *s = upf_slice_get(slice_id);
    return s ? upf_slice_session_delete(s, imsi, pdu_session_id) : -1;
}

int upf_dp_slice_register_teid(uint8_t slice_id, uint32_t teid,
                                const char *imsi, uint8_t pdu_session_id)
{
    upf_slice_ctx_t *s = upf_slice_get(slice_id);
    return s ? upf_slice_register_teid(s, teid, imsi, pdu_session_id) : -1;
}

int upf_dp_slice_register_ueip(uint8_t slice_id, uint32_t ue_addr,
                                const char *imsi, uint8_t pdu_session_id)
{
    upf_slice_ctx_t *s = upf_slice_get(slice_id);
    return s ? upf_slice_register_ueip(s, ue_addr, imsi, pdu_session_id) : -1;
}
