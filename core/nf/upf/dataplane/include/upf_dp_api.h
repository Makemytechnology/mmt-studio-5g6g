/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_dp_api.h — Public C API for UPF data plane
 *
 * Called from Python via ctypes. All functions are thread-safe.
 * EAL init uses --no-huge --no-pci for dev/test without hugepages.
 */

#ifndef UPF_DP_API_H
#define UPF_DP_API_H

#include "upf_types.h"

/* ── Lifecycle ── */

/* Initialize DPDK EAL and data plane structures.
 * argc/argv are forwarded to rte_eal_init().
 * Returns 0 on success, -1 on error. */
int upf_dp_init(int argc, char **argv);

/* Override the session cap before upf_dp_init(). After init the value
 * is frozen. Minimum 256. Returns 0 on success, -1 if already init'd
 * or value rejected. */
int upf_dp_set_max_sessions(uint32_t n);

/* Override PMD-mode tuning before upf_dp_init(). All three values
 * must be positive; 0 means "leave current". Returns 0 on success,
 * -1 if already init'd or any value rejected. */
int upf_dp_set_pmd_tuning(uint32_t mbuf_pool_size,
                          uint16_t rx_ring_size,
                          uint16_t tx_ring_size);

/* Clean shutdown. */
void upf_dp_cleanup(void);

/* ── Session management ── */

/* Create a session. Returns pointer (opaque to Python) or NULL. */
upf_session_t *upf_dp_session_create(const char *imsi, uint8_t pdu_session_id,
                                      const char *dnn, uint8_t sst, uint32_t sd,
                                      uint32_t ue_addr);

/* Delete a session. Returns 0 on success. */
int upf_dp_session_delete(const char *imsi, uint8_t pdu_session_id);

/* Get a session. Returns pointer or NULL. */
upf_session_t *upf_dp_session_get(const char *imsi, uint8_t pdu_session_id);

/* ── Rule management ── */

/* Add a PDR to a session. Returns 0 on success, -1 on error.
 * sdf_rules is a newline-separated string of SDF filter rules (may be NULL). */
int upf_dp_add_pdr(const char *imsi, uint8_t pdu_session_id,
                    uint16_t pdr_id, uint32_t precedence,
                    uint8_t pdi_source, uint8_t qfi,
                    uint32_t far_id, uint32_t qer_id, uint32_t urr_id,
                    const char *sdf_rules);

/* Add a FAR to a session. */
int upf_dp_add_far(const char *imsi, uint8_t pdu_session_id,
                    uint32_t far_id, uint8_t action, uint8_t dst_iface,
                    uint32_t ohc_teid, uint32_t ohc_peer_addr,
                    uint16_t ohc_peer_port, uint8_t ohc_type);

/* Update an existing FAR's tunnel info and action.
 * TS 29.244 §5.2.1: When gNB TEID becomes known (PDUSessionResourceSetupResponse),
 * update from action=BUFF to action=FORW and flush buffered DL packets. */
int upf_dp_update_far(const char *imsi, uint8_t pdu_session_id,
                      uint32_t far_id, uint32_t ohc_teid,
                      uint32_t ohc_peer_addr, uint16_t ohc_peer_port);

/* Update an existing PDR — TS 29.244 v19.5.0 §7.5.4.2. The Update PDR
 * IE "shall identify the PDR among all the PDRs configured for that
 * PFCP session" (mandatory PDR ID). Fields present in the Update IE
 * "shall replace the {PDI|FAR ID|QER ID|URR ID|...} previously stored
 * in the UP function for this PDR".
 *
 * Today the wholesale-replace path is taken — caller passes the full
 * desired-state values. This matches the §7.5.4.2 spec when the SMF
 * sends every field it wants preserved, and it's how
 * applyUpdatePDRToHook on the Go side currently composes the call.
 * If the rule isn't already present this returns -1 (Update of an
 * absent rule is a peer error per the §7.5.4 mandatory ID semantic). */
int upf_dp_update_pdr(const char *imsi, uint8_t pdu_session_id,
                      uint16_t pdr_id, uint32_t precedence,
                      uint8_t pdi_source, uint8_t qfi,
                      uint32_t far_id, uint32_t qer_id, uint32_t urr_id,
                      const char *sdf_rules);

/* Update an existing QER — TS 29.244 v19.5.0 §7.5.4.5. The Update QER
 * IE carries Gate Status / MBR / GBR / QFI as conditional fields,
 * each "shall be present if it needs to be modified". This entry
 * point is wholesale-replace semantics; the rte_meter is reset to
 * fresh state by upf_qer_meter_configure on each call. -1 if the
 * QER ID isn't already in the session. */
int upf_dp_update_qer(const char *imsi, uint8_t pdu_session_id,
                      uint32_t qer_id, uint8_t qfi,
                      uint8_t gate_ul, uint8_t gate_dl,
                      uint64_t mbr_ul, uint64_t mbr_dl,
                      uint64_t gbr_ul, uint64_t gbr_dl);

/* Update an existing URR — TS 29.244 v19.5.0 §7.5.4.4. The Update URR
 * IE carries Measurement Method / Reporting Triggers / Volume
 * Threshold / Time Threshold as conditional fields. Wholesale-replace
 * semantics; this RESETS the per-URR vol/pkt accumulators (memset on
 * the slot). The SMF SHOULD harvest existing usage via §7.5.4.10
 * Query URR before issuing Update URR if the prior counters need to
 * be charged. -1 if the URR ID isn't already in the session. */
int upf_dp_update_urr(const char *imsi, uint8_t pdu_session_id,
                      uint32_t urr_id, uint8_t meas_method,
                      uint8_t reporting_trigger,
                      uint64_t vol_thresh_ul, uint64_t vol_thresh_dl,
                      uint32_t time_thresh);

/* Deactivate a DL FAR — flip action FORW→BUFF and clear tunnel info.
 * TS 23.502 §4.2.6 step 6a (N4 Session Modification on AN Release):
 * "AN or N3 UPF Tunnel Info to be removed, Buffering on". Called when
 * the UE transitions CM-CONNECTED → CM-IDLE so DL packets buffer at
 * the UPF until the §4.2.3.2 Service Request reactivation brings up
 * a new gNB tunnel. FAR Apply Action values per TS 29.244 §8.2.26
 * (FORW=1, BUFF=2). Returns 0 on success, -1 if session/FAR missing. */
int upf_dp_deactivate_dl(const char *imsi, uint8_t pdu_session_id,
                         uint32_t far_id);

/* Add a QER to a session. */
int upf_dp_add_qer(const char *imsi, uint8_t pdu_session_id,
                    uint32_t qer_id, uint8_t qfi,
                    uint8_t gate_ul, uint8_t gate_dl,
                    uint64_t mbr_ul, uint64_t mbr_dl,
                    uint64_t gbr_ul, uint64_t gbr_dl);

/* Add a URR to a session. */
int upf_dp_add_urr(const char *imsi, uint8_t pdu_session_id,
                    uint32_t urr_id, uint8_t meas_method,
                    uint8_t reporting_trigger,
                    uint64_t vol_thresh_ul, uint64_t vol_thresh_dl,
                    uint32_t time_thresh);

/* Set BAR for a session. */
int upf_dp_set_bar(const char *imsi, uint8_t pdu_session_id,
                    uint8_t bar_id, uint8_t notify_cp,
                    uint16_t buf_pkt_count);

/* Remove a rule by ID — TS 29.244 v19.5.0 §7.5.4.6 / .7 / .8 / .9.
 *
 * Each Remove * IE in a §7.5.4 Modification Request "shall identify
 * the {PDR|FAR|URR|QER} to be deleted" by its mandatory ID IE. We
 * implement that by flipping the active flag false on the matching
 * slot in sess->{pdr|far|qer|urr}[]; the classifier and find_*
 * helpers (upf_classifier.c) already test active and skip inactive
 * slots, so removal takes effect immediately for every subsequent
 * packet without an array compaction step.
 *
 * Slot reuse on subsequent Create * is not implemented yet — the
 * classifier accepts duplicate IDs (last one wins by precedence), so
 * any Create with an ID already present should arrive after a Remove
 * with the same ID. Today the SMF doesn't issue mid-session Create
 * with re-used IDs, so this isn't load-bearing.
 *
 * Returns 0 on success, -1 if session or rule absent (idempotent
 * against a peer retransmission of the §7.5.4 message). */
int upf_dp_remove_pdr(const char *imsi, uint8_t pdu_session_id, uint16_t pdr_id);
int upf_dp_remove_far(const char *imsi, uint8_t pdu_session_id, uint32_t far_id);
int upf_dp_remove_qer(const char *imsi, uint8_t pdu_session_id, uint32_t qer_id);
int upf_dp_remove_urr(const char *imsi, uint8_t pdu_session_id, uint32_t urr_id);

/* Set Session-AMBR (TS 23.501 v19.7.0 §5.7.1.6 — UPF responsibility:
 * "UPF performs Session-AMBR enforcement"). Rates in kbps, 0 = unlimited.
 *
 * UE-AMBR (TS 23.501 §5.7.1.6 + §5.7.2.6) is enforced by the (R)AN,
 * not the UPF — there is intentionally no upf_dp_set_ue_ambr. The
 * AMF carries UE-AMBR to the gNB in NGAP UEAggregateMaximumBitRate. */
int upf_dp_set_session_ambr(const char *imsi, uint8_t pdu_session_id,
                             uint64_t ambr_ul_kbps, uint64_t ambr_dl_kbps);

/* ── Packet I/O ── */

/* Initialize packet I/O (GTP-U socket + TUN device).
 * n3_addr: bind address for GTP-U (NULL = "0.0.0.0")
 * n3_port: GTP-U port (0 = 2152)
 * tun_name: TUN device name (NULL = "upfgtp")
 * tun_addr: TUN IP address (e.g. "10.45.0.1")
 * Returns 0 on success, -1 on error. */
int upf_dp_pkt_io_init(const char *n3_addr, uint16_t n3_port,
                        const char *tun_name, const char *tun_addr);

/* Run the packet processing loop (blocking). Call from a thread. */
int upf_dp_pkt_io_run(void);

/* Stop the processing loop. */
void upf_dp_pkt_io_stop(void);

/* Register TEID → session mapping for UL GTP-U lookup. */
int upf_dp_register_teid(uint32_t teid, const char *imsi, uint8_t pdu_session_id);

/* Register UE-IP → session mapping for DL packet lookup. */
int upf_dp_register_ueip(uint32_t ue_addr, const char *imsi, uint8_t pdu_session_id);

/* Release a previously-registered TEID / UE-IP reverse-map entry.
 * Required for TS 29.244 v19.5.0 §7.5.6 PFCP Session Deletion to
 * actually free the F-TEID (§5.5.1) and UE IP (§8.2.62) resources;
 * otherwise the LF rte_hash slots leak and saturate at MAX_TEID_MAP.
 * Idempotent — returns -1 if the key was not present, with no state
 * change. */
int upf_dp_unregister_teid(uint32_t teid);
int upf_dp_unregister_ueip(uint32_t ue_addr);

/* Batch release of TEID + UE-IP reverse-map entries in one cgo trip.
 *
 * Same TS 29.244 v19.5.0 §7.5.6 release semantics as the singular
 * upf_dp_unregister_teid / _ueip — but the dataplane walks both
 * arrays in a tight loop on the EAL thread, so a §7.5.6 deletion
 * holding M PDR keys (M = 2 in today's default-bearer SMF) collapses
 * from M cgo round-trips into 1. PERFORMANCE.md Run 4 showed the
 * per-PDR-key sequential path going super-linear past 64 sessions
 * (33 ms → 197 ms going 64→128 UEs); this entry point is the
 * structural fix. Idempotent — missing keys are silently skipped. */
int upf_dp_unregister_batch(const uint32_t *teids, int n_teids,
                             const uint32_t *ueips, int n_ueips);

/* Classify a single packet (for testing). direction: 0=UL, 1=DL.
 * Returns FAR action (0=drop, 1=forward, -1=no match). */
int upf_dp_classify_packet(const char *imsi, uint8_t pdu_session_id,
                            uint8_t direction,
                            const uint8_t *ip_pkt, uint16_t ip_len);

/* ── Stats ── */

/* Get URR counters for a session/URR. Returns 0 on success.
 * Output params may be NULL if not needed. */
int upf_dp_get_urr_stats(const char *imsi, uint8_t pdu_session_id,
                          uint32_t urr_id,
                          uint64_t *vol_ul, uint64_t *vol_dl,
                          uint64_t *pkt_ul, uint64_t *pkt_dl);

/* Get per-QER drop counters (gate-closed + MBR-exceeded accumulators
 * maintained by upf_classifier on every data packet). Returns 0 on
 * success. Output params may be NULL if not needed. */
int upf_dp_get_qer_stats(const char *imsi, uint8_t pdu_session_id,
                          uint32_t qer_id,
                          uint64_t *dropped_pkts_ul, uint64_t *dropped_pkts_dl,
                          uint64_t *dropped_bytes_ul, uint64_t *dropped_bytes_dl);

/* Get I/O stats.
 * See upf_pkt_io.h for drop-category semantics. Note the field order is
 * load-bearing: the Python ctypes mirror in dpdk_wrapper.py must match. */
typedef struct __attribute__((__packed__)) {
    uint64_t ul_pkts;
    uint64_t ul_bytes;
    uint64_t dl_pkts;
    uint64_t dl_bytes;
    uint64_t ul_dropped;
    uint64_t dl_dropped;
    uint64_t ul_no_session;  /* UL dropped for unknown TEID / session gone */
    uint64_t dl_no_session;  /* DL dropped for no session at dst UE-IP */
    uint64_t ul_metered;     /* UL dropped by QER MBR / AMBR metering */
    uint64_t dl_metered;     /* DL dropped by QER MBR / AMBR metering */
    uint64_t gtpu_errors;
    uint32_t _debug_last_unknown_teid;
    uint32_t _debug_last_registered_teid;
} upf_dp_io_stats_t;

void upf_dp_get_io_stats(upf_dp_io_stats_t *out);

/* Get total active session count. */
uint32_t upf_dp_session_count(void);

/* ── Network Slicing (TS 23.501 §5.15.4) ── */

/* Initialize a slice channel. Call after upf_dp_init().
 * slice_id: 0-2, sst: S-NSSAI SST (1=eMBB, 2=URLLC, 3=mIoT)
 * name: human-readable label.
 * Returns 0 on success. */
int upf_dp_slice_init(uint8_t slice_id, uint8_t sst, const char *name);

/* Destroy a slice channel. */
void upf_dp_slice_destroy(uint8_t slice_id);

/* Get per-slice I/O stats. */
void upf_dp_slice_get_stats(uint8_t slice_id, upf_dp_io_stats_t *out);

/* Get per-slice session count. */
uint32_t upf_dp_slice_session_count(uint8_t slice_id);

/* Create session in a specific slice. */
upf_session_t *upf_dp_slice_session_create(uint8_t slice_id,
                                            const char *imsi, uint8_t pdu_session_id,
                                            const char *dnn, uint8_t sst, uint32_t sd,
                                            uint32_t ue_addr);

/* Get session from a specific slice. */
upf_session_t *upf_dp_slice_session_get(uint8_t slice_id,
                                          const char *imsi, uint8_t pdu_session_id);

/* Delete session from a specific slice. */
int upf_dp_slice_session_delete(uint8_t slice_id,
                                 const char *imsi, uint8_t pdu_session_id);

/* Register TEID in a specific slice's reverse map. */
int upf_dp_slice_register_teid(uint8_t slice_id, uint32_t teid,
                                const char *imsi, uint8_t pdu_session_id);

/* Register UE-IP in a specific slice's reverse map. */
int upf_dp_slice_register_ueip(uint8_t slice_id, uint32_t ue_addr,
                                const char *imsi, uint8_t pdu_session_id);

#endif /* UPF_DP_API_H */
