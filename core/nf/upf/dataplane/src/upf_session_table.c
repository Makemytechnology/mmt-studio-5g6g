/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_session_table.c — Session table backed by rte_hash
 *
 * Uses DPDK cuckoo hash with lock-free reader-writer concurrency.
 * Sessions are stored in a pre-allocated array; hash maps key → index.
 */

#include <stdlib.h>
#include <string.h>
#include <rte_hash.h>
#include <rte_jhash.h>
#include <rte_malloc.h>
#include <rte_log.h>

#include "upf_session_table.h"

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

static struct rte_hash *session_hash = NULL;
static upf_session_t  *session_pool = NULL;
static uint32_t        session_count = 0;

/* Deferred-free ring for the session_hash, which is created with
 * RTE_HASH_EXTRA_FLAGS_RW_CONCURRENCY_LF. rte_hash_del_key only marks
 * the slot freed; rte_hash_free_key_with_position actually returns it
 * to the free-list. We must not free immediately because pkt_io
 * readers (process_ul_packet / process_dl_packet) may have already
 * resolved a position via rte_hash_lookup and still be in their
 * upf_session_get → use window. Holding N=64 freed positions before
 * each is reclaimed gives readers grace equal to N session deletions
 * — far more than a single packet's lookup window. Same pattern as
 * upf_pkt_io.c's teid_deferred / ueip_deferred rings.
 *
 * Spec ground: TS 29.244 v19.5.0 §7.5.6 PFCP Session Deletion — the
 * UP function "shall delete the PFCP session". With LF mode that
 * requires the explicit free, otherwise the slot is permanently
 * deferred and the table eventually rejects new sessions. */
/* Depth: see upf_pkt_io.c DEFERRED_FREE_DEPTH for rationale. Same
 * Run 4 cascade super-linearity applied here (session_hash holds the
 * primary session-key→pool slot mapping; deletion releases via the
 * deferred-free ring). 256 covers 128-UE concurrent cascade without
 * forcing synchronous frees. */
#define SESSION_DEFERRED_DEPTH 256
typedef struct {
    int32_t  position;
    bool     valid;
} session_deferred_slot_t;

static session_deferred_slot_t session_deferred[SESSION_DEFERRED_DEPTH];
static unsigned                session_deferred_idx = 0;

static void make_key(upf_session_key_t *key, const char *imsi, uint8_t pdu_session_id)
{
    memset(key, 0, sizeof(*key));
    strncpy(key->imsi, imsi, sizeof(key->imsi) - 1);
    key->pdu_session_id = pdu_session_id;
}

int upf_session_table_init(void)
{
    struct rte_hash_parameters params = {
        .name = "upf_session_table",
        .entries = g_upf_max_sessions,
        .key_len = sizeof(upf_session_key_t),
        .hash_func = rte_jhash,
        .hash_func_init_val = 0,
        .socket_id = 0,
        .extra_flag = RTE_HASH_EXTRA_FLAGS_RW_CONCURRENCY_LF,
    };

    session_hash = rte_hash_create(&params);
    if (!session_hash) {
        RTE_LOG(ERR, UPF, "Failed to create session hash table\n");
        return -1;
    }

    /* rte_hash internally allocates num_key_slots = entries + 1, so
     * rte_hash_add_key can return indices in [0, entries]. Allocate
     * one extra session to avoid out-of-bounds access. */
    session_pool = rte_zmalloc("upf_sessions",
                                sizeof(upf_session_t) * (g_upf_max_sessions + 1),
                                RTE_CACHE_LINE_SIZE);
    if (!session_pool) {
        RTE_LOG(ERR, UPF, "Failed to allocate session pool\n");
        rte_hash_free(session_hash);
        session_hash = NULL;
        return -1;
    }

    session_count = 0;
    RTE_LOG(INFO, UPF, "Session table initialized (max %u sessions)\n",
            g_upf_max_sessions);
    return 0;
}

void upf_session_table_destroy(void)
{
    if (session_pool) {
        rte_free(session_pool);
        session_pool = NULL;
    }
    if (session_hash) {
        rte_hash_free(session_hash);
        session_hash = NULL;
    }
    session_count = 0;
}

upf_session_t *upf_session_create(const char *imsi, uint8_t pdu_session_id)
{
    if (!session_hash || !session_pool) return NULL;
    if (session_count >= g_upf_max_sessions) return NULL;

    upf_session_key_t key;
    make_key(&key, imsi, pdu_session_id);

    /* Check if already exists */
    int32_t idx = rte_hash_lookup(session_hash, &key);
    if (idx >= 0) {
        RTE_LOG(WARNING, UPF, "Session already exists: %s/%u\n", imsi, pdu_session_id);
        return NULL;
    }

    /* Add to hash — DPDK assigns the index */
    idx = rte_hash_add_key(session_hash, &key);
    if (idx < 0) {
        RTE_LOG(ERR, UPF, "Failed to add session to hash: %s/%u\n", imsi, pdu_session_id);
        return NULL;
    }

    upf_session_t *sess = &session_pool[idx];
    memset(sess, 0, sizeof(*sess));
    strncpy(sess->imsi, imsi, sizeof(sess->imsi) - 1);
    sess->pdu_session_id = pdu_session_id;
    sess->sd = 0xFFFFFF;  /* default: not set */
    sess->session_idx = (uint32_t)idx;  /* Index into meter arrays */
    sess->active = true;
    session_count++;

    RTE_LOG(INFO, UPF, "Session created: %s/%u (idx=%d, total=%u)\n",
            imsi, pdu_session_id, idx, session_count);
    return sess;
}

upf_session_t *upf_session_get(const char *imsi, uint8_t pdu_session_id)
{
    if (!session_hash || !session_pool) return NULL;

    upf_session_key_t key;
    make_key(&key, imsi, pdu_session_id);

    int32_t idx = rte_hash_lookup(session_hash, &key);
    if (idx < 0) return NULL;

    upf_session_t *sess = &session_pool[idx];
    return sess->active ? sess : NULL;
}

int upf_session_delete(const char *imsi, uint8_t pdu_session_id)
{
    if (!session_hash || !session_pool) return -1;

    upf_session_key_t key;
    make_key(&key, imsi, pdu_session_id);

    int32_t idx = rte_hash_lookup(session_hash, &key);
    if (idx < 0) return -1;

    /* Free DL packet buffer if allocated */
    if (session_pool[idx].dl_buf) {
        free(session_pool[idx].dl_buf);
        session_pool[idx].dl_buf = NULL;
    }

    /* Mark inactive BEFORE removing from the hash so a reader that has
     * already resolved `idx` via rte_hash_lookup sees active=false on
     * its follow-on upf_session_get and bails safely. */
    session_pool[idx].active = false;

    int32_t pos = rte_hash_del_key(session_hash, &key);

    /* LF mode: del marks the slot, free actually releases. Defer N
     * deletions before reclaim so any in-flight reader has exited the
     * lookup→use window. */
    if (pos >= 0) {
        session_deferred_slot_t *slot = &session_deferred[session_deferred_idx];
        if (slot->valid) {
            rte_hash_free_key_with_position(session_hash, slot->position);
        }
        slot->position = pos;
        slot->valid    = true;
        session_deferred_idx = (session_deferred_idx + 1u) % SESSION_DEFERRED_DEPTH;
    }

    if (session_count > 0) session_count--;

    RTE_LOG(INFO, UPF, "Session deleted: %s/%u (total=%u)\n",
            imsi, pdu_session_id, session_count);
    return 0;
}

uint32_t upf_session_count(void)
{
    return session_count;
}
