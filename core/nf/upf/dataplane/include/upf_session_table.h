/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_session_table.h — Session table backed by rte_hash (cuckoo hash)
 *
 * Key: (imsi, pdu_session_id) → upf_session_t*
 * Lock-free concurrent readers, single writer.
 */

#ifndef UPF_SESSION_TABLE_H
#define UPF_SESSION_TABLE_H

#include "upf_types.h"

/* Composite key for session lookup */
typedef struct __attribute__((__packed__)) {
    char    imsi[16];
    uint8_t pdu_session_id;
    uint8_t _pad[3];
} upf_session_key_t;

/* Initialize the session table. Call once after EAL init.
 * Returns 0 on success, -1 on error. */
int upf_session_table_init(void);

/* Destroy the session table and free all sessions. */
void upf_session_table_destroy(void);

/* Create a new session. Returns pointer to the allocated session,
 * or NULL if table is full or session already exists.
 * Caller must fill in the session fields after creation. */
upf_session_t *upf_session_create(const char *imsi, uint8_t pdu_session_id);

/* Look up a session. Returns NULL if not found. */
upf_session_t *upf_session_get(const char *imsi, uint8_t pdu_session_id);

/* Delete a session. Returns 0 on success, -1 if not found. */
int upf_session_delete(const char *imsi, uint8_t pdu_session_id);

/* Get total number of active sessions. */
uint32_t upf_session_count(void);

#endif /* UPF_SESSION_TABLE_H */
