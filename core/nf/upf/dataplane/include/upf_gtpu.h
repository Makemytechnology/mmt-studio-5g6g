/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_gtpu.h — GTP-U v1 header encode/decode (3GPP TS 29.281)
 *
 * GTP-U header (8 bytes minimum):
 *   Flags(1) | MsgType(1) | Length(2) | TEID(4)
 *
 * Flags: Version=1(3bit), PT=1, E/S/PN bits
 * MsgType: 0xFF = G-PDU (user data)
 *
 * For 5G, Extension Header with PDU Session Container carries QFI.
 */

#ifndef UPF_GTPU_H
#define UPF_GTPU_H

#include <stdint.h>
#include <stddef.h>

/* GTP-U port */
#define GTPU_PORT       2152

/* GTP-U flags */
#define GTPU_VER_V1     0x20    /* Version 1 */
#define GTPU_PT_GTP     0x10    /* Protocol Type = GTP */
#define GTPU_E_FLAG     0x04    /* Extension header flag */
#define GTPU_S_FLAG     0x02    /* Sequence number flag */
#define GTPU_PN_FLAG    0x01    /* N-PDU number flag */

/* Message types */
#define GTPU_MSG_GPDU   0xFF    /* G-PDU (user data) */
#define GTPU_MSG_ECHO_REQ  0x01
#define GTPU_MSG_ECHO_RSP  0x02

/* Extension header types */
#define GTPU_EXT_PDU_SESSION_CONTAINER 0x85

/* GTP-U header (8 bytes, no extensions) */
typedef struct __attribute__((__packed__)) {
    uint8_t  flags;      /* Version(3) | PT(1) | *(1) | E(1) | S(1) | PN(1) */
    uint8_t  msg_type;
    uint16_t length;     /* payload length (excl. first 8 bytes), network order */
    uint32_t teid;       /* Tunnel Endpoint ID, network order */
} gtpu_hdr_t;

/* PDU Session Container extension (DL, 5G) — TS 38.415 §5.5.2.1
 *   Octet 1: PDU Type(4) | QMP(1) | SNP(1) | spare(2)
 *   Octet 2: PPP(1) | RQI(1) | QFI(6) */
typedef struct __attribute__((__packed__)) {
    uint8_t  ext_len;    /* length in 4-byte units (=1 for 4 bytes) */
    uint8_t  type_flags; /* PDU Type(4) | QMP(1) | SNP(1) | spare(2) */
    uint8_t  qfi_flags;  /* PPP(1) | RQI(1) | QFI(6) */
    uint8_t  next_ext;   /* 0x00 = no more extensions */
} gtpu_pdu_sess_container_t;

/* Result of decoding a GTP-U packet */
typedef struct {
    uint32_t teid;          /* TEID from header */
    uint8_t  qfi;           /* QFI from PDU session container, or 0 */
    const uint8_t *payload; /* pointer to inner IP packet */
    uint16_t payload_len;   /* inner IP packet length */
} gtpu_decoded_t;

/* Decode a GTP-U packet. buf/len is the UDP payload (starting at GTP-U header).
 * Returns 0 on success, -1 on error (not GTP-U, too short, etc). */
int gtpu_decode(const uint8_t *buf, uint16_t len, gtpu_decoded_t *out);

/* Encode a GTP-U header (G-PDU) wrapping an inner IP packet.
 * out_buf must have at least inner_len + 16 bytes.
 * If qfi > 0, includes PDU Session Container extension header.
 * Returns total encoded length, or -1 on error. */
int gtpu_encode(uint8_t *out_buf, size_t out_buf_sz,
                uint32_t teid, uint8_t qfi,
                const uint8_t *inner_pkt, uint16_t inner_len);

#endif /* UPF_GTPU_H */
