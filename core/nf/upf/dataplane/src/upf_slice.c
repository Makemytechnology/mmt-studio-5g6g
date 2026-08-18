/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_slice.c — Per-slice UPF data plane context
 *
 * TS 23.501 §5.15.4: Network slicing at UPF level.
 * Each slice gets its own session table, meters, stats, and TUN.
 *
 * The GTP-U socket is shared — UL packets are demuxed by TEID to find
 * which slice owns the session. DL packets are demuxed by dest IP.
 */

#include <string.h>
#include <rte_hash.h>
#include <rte_jhash.h>
#include <rte_malloc.h>
#include <rte_log.h>

#include "upf_slice.h"
#include "upf_session_table.h"

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

/* Global slice array */
static upf_slice_ctx_t slices[UPF_MAX_SLICES];

/* TEID reverse-map value: (slice_id, imsi, pdu_session_id) */
typedef struct __attribute__((__packed__)) {
    uint8_t  slice_id;
    char     imsi[16];
    uint8_t  pdu_session_id;
    uint8_t  _pad[2];
} teid_map_val_t;

/* UE-IP reverse-map value */
typedef teid_map_val_t ueip_map_val_t;

/* Pre-allocated value pools for TEID/UEIP maps (1024 per slice) */
static teid_map_val_t teid_vals[UPF_MAX_SLICES][UPF_SLICE_MAX_SESSIONS];
static ueip_map_val_t ueip_vals[UPF_MAX_SLICES][UPF_SLICE_MAX_SESSIONS];


int upf_slice_init(uint8_t slice_id, uint8_t sst, const char *name)
{
    if (slice_id >= UPF_MAX_SLICES) return -1;

    upf_slice_ctx_t *s = &slices[slice_id];
    if (s->active) {
        RTE_LOG(WARNING, UPF, "Slice %u already initialized\n", slice_id);
        return 0;
    }

    memset(s, 0, sizeof(*s));
    s->slice_id = slice_id;
    s->sst = sst;
    strncpy(s->name, name ? name : "unknown", sizeof(s->name) - 1);
    s->tun_fd = -1;

    /* ── Session hash table (per-slice, independent) ── */
    char hash_name[64];
    snprintf(hash_name, sizeof(hash_name), "upf_sess_%s", s->name);

    struct rte_hash_parameters params = {
        .name = hash_name,
        .entries = UPF_SLICE_MAX_SESSIONS,
        .key_len = sizeof(upf_session_key_t),
        .hash_func = rte_jhash,
        .hash_func_init_val = slice_id,  /* seed differs per slice */
        .socket_id = 0,
        .extra_flag = RTE_HASH_EXTRA_FLAGS_RW_CONCURRENCY_LF,
    };

    s->session_hash = rte_hash_create(&params);
    if (!s->session_hash) {
        RTE_LOG(ERR, UPF, "Slice %s: failed to create session hash\n", s->name);
        return -1;
    }

    /* Pre-allocated session pool */
    snprintf(hash_name, sizeof(hash_name), "upf_pool_%s", s->name);
    s->session_pool = rte_zmalloc(hash_name,
                                   sizeof(upf_session_t) * UPF_SLICE_MAX_SESSIONS,
                                   RTE_CACHE_LINE_SIZE);
    if (!s->session_pool) {
        RTE_LOG(ERR, UPF, "Slice %s: failed to allocate session pool\n", s->name);
        rte_hash_free(s->session_hash);
        s->session_hash = NULL;
        return -1;
    }

    /* ── TEID reverse-map ── */
    snprintf(hash_name, sizeof(hash_name), "upf_teid_%s", s->name);
    struct rte_hash_parameters teid_params = {
        .name = hash_name,
        .entries = UPF_SLICE_MAX_SESSIONS,
        .key_len = sizeof(uint32_t),
        .hash_func = rte_jhash,
        .hash_func_init_val = 0x5E1D0000 | slice_id,
        .socket_id = 0,
        .extra_flag = RTE_HASH_EXTRA_FLAGS_RW_CONCURRENCY_LF,
    };
    s->teid_hash = rte_hash_create(&teid_params);

    /* ── UE-IP reverse-map ── */
    snprintf(hash_name, sizeof(hash_name), "upf_ueip_%s", s->name);
    struct rte_hash_parameters ueip_params = {
        .name = hash_name,
        .entries = UPF_SLICE_MAX_SESSIONS,
        .key_len = sizeof(uint32_t),
        .hash_func = rte_jhash,
        .hash_func_init_val = 0x1E1F0000 | slice_id,
        .socket_id = 0,
        .extra_flag = RTE_HASH_EXTRA_FLAGS_RW_CONCURRENCY_LF,
    };
    s->ueip_hash = rte_hash_create(&ueip_params);

    if (!s->teid_hash || !s->ueip_hash) {
        RTE_LOG(ERR, UPF, "Slice %s: failed to create reverse-map hashes\n", s->name);
        upf_slice_destroy(slice_id);
        return -1;
    }

    /* ── Per-slice meter arrays (dynamically allocated to save memory) ── */
    snprintf(hash_name, sizeof(hash_name), "upf_qm_%s", s->name);
    s->qer_meters = rte_zmalloc(hash_name,
        sizeof(upf_qer_meter_t) * UPF_SLICE_MAX_SESSIONS * UPF_MAX_QER_PER_SESSION,
        RTE_CACHE_LINE_SIZE);
    snprintf(hash_name, sizeof(hash_name), "upf_sm_%s", s->name);
    s->session_meters = rte_zmalloc(hash_name,
        sizeof(upf_session_meter_t) * UPF_SLICE_MAX_SESSIONS,
        RTE_CACHE_LINE_SIZE);
    if (!s->qer_meters || !s->session_meters) {
        RTE_LOG(WARNING, UPF, "Slice %s: meter allocation failed (non-fatal)\n", s->name);
        /* Non-fatal: slice works without per-slice metering */
    }

    s->active = true;
    RTE_LOG(INFO, UPF, "Slice %s (SST=%u, id=%u) initialized — %u max sessions\n",
            s->name, sst, slice_id, UPF_SLICE_MAX_SESSIONS);
    return 0;
}

void upf_slice_destroy(uint8_t slice_id)
{
    if (slice_id >= UPF_MAX_SLICES) return;
    upf_slice_ctx_t *s = &slices[slice_id];

    if (s->qer_meters) { rte_free(s->qer_meters); s->qer_meters = NULL; }
    if (s->session_meters) { rte_free(s->session_meters); s->session_meters = NULL; }
    if (s->session_pool) { rte_free(s->session_pool); s->session_pool = NULL; }
    if (s->session_hash) { rte_hash_free(s->session_hash); s->session_hash = NULL; }
    if (s->teid_hash) { rte_hash_free(s->teid_hash); s->teid_hash = NULL; }
    if (s->ueip_hash) { rte_hash_free(s->ueip_hash); s->ueip_hash = NULL; }

    s->active = false;
    RTE_LOG(INFO, UPF, "Slice %s destroyed\n", s->name);
}

upf_slice_ctx_t *upf_slice_get(uint8_t slice_id)
{
    if (slice_id >= UPF_MAX_SLICES) return NULL;
    return slices[slice_id].active ? &slices[slice_id] : NULL;
}

upf_slice_ctx_t *upf_slice_find_by_sst(uint8_t sst)
{
    for (int i = 0; i < UPF_MAX_SLICES; i++) {
        if (slices[i].active && slices[i].sst == sst)
            return &slices[i];
    }
    return NULL;
}

int upf_slice_get_all(upf_slice_ctx_t **out, int max)
{
    int n = 0;
    for (int i = 0; i < UPF_MAX_SLICES && n < max; i++) {
        if (slices[i].active)
            out[n++] = &slices[i];
    }
    return n;
}

/* ── Session operations (per-slice) ── */

static void _make_key(upf_session_key_t *key, const char *imsi, uint8_t pdu_session_id)
{
    memset(key, 0, sizeof(*key));
    strncpy(key->imsi, imsi, sizeof(key->imsi) - 1);
    key->pdu_session_id = pdu_session_id;
}

upf_session_t *upf_slice_session_create(upf_slice_ctx_t *s,
                                          const char *imsi, uint8_t pdu_session_id)
{
    if (!s || !s->session_hash || !s->session_pool) return NULL;
    if (s->session_count >= UPF_SLICE_MAX_SESSIONS) return NULL;

    upf_session_key_t key;
    _make_key(&key, imsi, pdu_session_id);

    /* Check if already exists */
    int32_t idx = rte_hash_lookup(s->session_hash, &key);
    if (idx >= 0) return NULL;

    idx = rte_hash_add_key(s->session_hash, &key);
    if (idx < 0) return NULL;

    upf_session_t *sess = &s->session_pool[idx];
    memset(sess, 0, sizeof(*sess));
    strncpy(sess->imsi, imsi, sizeof(sess->imsi) - 1);
    sess->pdu_session_id = pdu_session_id;
    sess->sst = s->sst;
    sess->sd = 0xFFFFFF;
    sess->session_idx = (uint32_t)idx;
    sess->active = true;
    s->session_count++;

    return sess;
}

upf_session_t *upf_slice_session_get(upf_slice_ctx_t *s,
                                      const char *imsi, uint8_t pdu_session_id)
{
    if (!s || !s->session_hash || !s->session_pool) return NULL;

    upf_session_key_t key;
    _make_key(&key, imsi, pdu_session_id);

    int32_t idx = rte_hash_lookup(s->session_hash, &key);
    if (idx < 0) return NULL;

    upf_session_t *sess = &s->session_pool[idx];
    return sess->active ? sess : NULL;
}

int upf_slice_session_delete(upf_slice_ctx_t *s,
                              const char *imsi, uint8_t pdu_session_id)
{
    if (!s || !s->session_hash || !s->session_pool) return -1;

    upf_session_key_t key;
    _make_key(&key, imsi, pdu_session_id);

    int32_t idx = rte_hash_lookup(s->session_hash, &key);
    if (idx < 0) return -1;

    s->session_pool[idx].active = false;
    rte_hash_del_key(s->session_hash, &key);
    if (s->session_count > 0) s->session_count--;

    return 0;
}

/* ── Cross-slice lookup (for GTP-U TEID demux) ── */

upf_slice_ctx_t *upf_slice_find_session(const char *imsi, uint8_t pdu_session_id)
{
    for (int i = 0; i < UPF_MAX_SLICES; i++) {
        if (!slices[i].active) continue;
        upf_session_t *sess = upf_slice_session_get(&slices[i], imsi, pdu_session_id);
        if (sess) return &slices[i];
    }
    return NULL;
}

/* Register TEID in slice's reverse map */
int upf_slice_register_teid(upf_slice_ctx_t *s, uint32_t teid,
                             const char *imsi, uint8_t pdu_session_id)
{
    if (!s || !s->teid_hash) return -1;

    int32_t idx = rte_hash_add_key(s->teid_hash, &teid);
    if (idx < 0) return -1;

    teid_map_val_t *val = &teid_vals[s->slice_id][idx];
    val->slice_id = s->slice_id;
    strncpy(val->imsi, imsi, sizeof(val->imsi) - 1);
    val->pdu_session_id = pdu_session_id;
    return 0;
}

/* Register UE-IP in slice's reverse map */
int upf_slice_register_ueip(upf_slice_ctx_t *s, uint32_t ue_addr,
                              const char *imsi, uint8_t pdu_session_id)
{
    if (!s || !s->ueip_hash) return -1;

    int32_t idx = rte_hash_add_key(s->ueip_hash, &ue_addr);
    if (idx < 0) return -1;

    ueip_map_val_t *val = &ueip_vals[s->slice_id][idx];
    val->slice_id = s->slice_id;
    strncpy(val->imsi, imsi, sizeof(val->imsi) - 1);
    val->pdu_session_id = pdu_session_id;
    return 0;
}

/* Find session by TEID across all slices */
upf_slice_ctx_t *upf_slice_find_by_teid(uint32_t teid, upf_session_t **out_sess)
{
    for (int i = 0; i < UPF_MAX_SLICES; i++) {
        if (!slices[i].active || !slices[i].teid_hash) continue;

        int32_t idx = rte_hash_lookup(slices[i].teid_hash, &teid);
        if (idx >= 0) {
            teid_map_val_t *val = &teid_vals[i][idx];
            if (out_sess) {
                *out_sess = upf_slice_session_get(&slices[i],
                                                   val->imsi, val->pdu_session_id);
            }
            return &slices[i];
        }
    }
    return NULL;
}

/* Find session by UE-IP across all slices */
upf_slice_ctx_t *upf_slice_find_by_ueip(uint32_t ue_addr, upf_session_t **out_sess)
{
    for (int i = 0; i < UPF_MAX_SLICES; i++) {
        if (!slices[i].active || !slices[i].ueip_hash) continue;

        int32_t idx = rte_hash_lookup(slices[i].ueip_hash, &ue_addr);
        if (idx >= 0) {
            ueip_map_val_t *val = &ueip_vals[i][idx];
            if (out_sess) {
                *out_sess = upf_slice_session_get(&slices[i],
                                                   val->imsi, val->pdu_session_id);
            }
            return &slices[i];
        }
    }
    return NULL;
}

/* Get per-slice I/O stats */
void upf_slice_get_stats(uint8_t slice_id, upf_io_stats_t *out)
{
    if (!out) return;
    memset(out, 0, sizeof(*out));
    if (slice_id >= UPF_MAX_SLICES || !slices[slice_id].active) return;

    upf_slice_ctx_t *s = &slices[slice_id];
    out->ul_pkts      = __atomic_load_n(&s->io_stats.ul_pkts, __ATOMIC_RELAXED);
    out->ul_bytes     = __atomic_load_n(&s->io_stats.ul_bytes, __ATOMIC_RELAXED);
    out->dl_pkts      = __atomic_load_n(&s->io_stats.dl_pkts, __ATOMIC_RELAXED);
    out->dl_bytes     = __atomic_load_n(&s->io_stats.dl_bytes, __ATOMIC_RELAXED);
    out->ul_dropped   = __atomic_load_n(&s->io_stats.ul_dropped, __ATOMIC_RELAXED);
    out->dl_dropped   = __atomic_load_n(&s->io_stats.dl_dropped, __ATOMIC_RELAXED);
    out->ul_metered   = __atomic_load_n(&s->io_stats.ul_metered, __ATOMIC_RELAXED);
    out->dl_metered   = __atomic_load_n(&s->io_stats.dl_metered, __ATOMIC_RELAXED);
    out->gtpu_errors  = __atomic_load_n(&s->io_stats.gtpu_errors, __ATOMIC_RELAXED);
}

/* Get per-slice QER meter */
upf_qer_meter_t *upf_slice_qer_meter_get(upf_slice_ctx_t *s,
                                           uint32_t session_idx, uint8_t qer_idx)
{
    if (!s || !s->qer_meters || session_idx >= UPF_SLICE_MAX_SESSIONS ||
        qer_idx >= UPF_MAX_QER_PER_SESSION)
        return NULL;
    return &s->qer_meters[session_idx * UPF_MAX_QER_PER_SESSION + qer_idx];
}

/* Get per-slice session meter */
upf_session_meter_t *upf_slice_session_meter_get(upf_slice_ctx_t *s,
                                                   uint32_t session_idx)
{
    if (!s || !s->session_meters || session_idx >= UPF_SLICE_MAX_SESSIONS)
        return NULL;
    return &s->session_meters[session_idx];
}
