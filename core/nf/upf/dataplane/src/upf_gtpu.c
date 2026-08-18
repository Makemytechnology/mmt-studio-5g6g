/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_gtpu.c — GTP-U v1 encode/decode (3GPP TS 29.281) */

#include <string.h>
#include <arpa/inet.h>

#include "upf_gtpu.h"

int gtpu_decode(const uint8_t *buf, uint16_t len, gtpu_decoded_t *out)
{
    if (!buf || !out || len < 8) return -1;

    const gtpu_hdr_t *hdr = (const gtpu_hdr_t *)buf;

    /* Check version=1, PT=1, msg_type=G-PDU */
    if ((hdr->flags & 0xE0) != GTPU_VER_V1) return -1;
    if (!(hdr->flags & GTPU_PT_GTP)) return -1;
    if (hdr->msg_type != GTPU_MSG_GPDU) return -1;

    out->teid = ntohl(hdr->teid);
    out->qfi = 0;

    uint16_t hdr_len = 8;

    /* If any of E/S/PN flags are set, 4 extra bytes follow the mandatory header:
     * SeqNum(2) + N-PDU(1) + NextExtHdr(1) */
    if (hdr->flags & (GTPU_E_FLAG | GTPU_S_FLAG | GTPU_PN_FLAG)) {
        if (len < 12) return -1;
        hdr_len = 12;

        /* Walk extension headers if E flag set */
        if (hdr->flags & GTPU_E_FLAG) {
            uint8_t next_ext = buf[11];  /* NextExtHdr byte */

            while (next_ext != 0x00 && hdr_len < len) {
                if (hdr_len >= len) return -1;
                uint8_t ext_len_units = buf[hdr_len]; /* length in 4-byte units */
                if (ext_len_units == 0) return -1;
                uint16_t ext_total = ext_len_units * 4;

                if (next_ext == GTPU_EXT_PDU_SESSION_CONTAINER && ext_total >= 4) {
                    /* TS 38.415 §5.5.2.1:
                     *   byte[1] = PDU Type(4) | QMP(1) | SNP(1) | spare(2)
                     *   byte[2] = PPP(1) | RQI(1) | QFI(6) */
                    out->qfi = buf[hdr_len + 2] & 0x3F;
                }

                /* next_ext is at the last byte of this extension */
                next_ext = buf[hdr_len + ext_total - 1];
                hdr_len += ext_total;
            }
        }
    }

    if (hdr_len > len) return -1;

    out->payload = buf + hdr_len;
    out->payload_len = len - hdr_len;

    return 0;
}

int gtpu_encode(uint8_t *out_buf, size_t out_buf_sz,
                uint32_t teid, uint8_t qfi,
                const uint8_t *inner_pkt, uint16_t inner_len)
{
    if (!out_buf || !inner_pkt) return -1;

    if (qfi > 0) {
        /* With PDU Session Container extension header:
         * 8 (mandatory) + 4 (seq/npdu/next_ext) + 4 (ext hdr) = 16 bytes */
        uint16_t total = 16 + inner_len;
        if (total > out_buf_sz) return -1;

        gtpu_hdr_t *hdr = (gtpu_hdr_t *)out_buf;
        hdr->flags = GTPU_VER_V1 | GTPU_PT_GTP | GTPU_E_FLAG;
        hdr->msg_type = GTPU_MSG_GPDU;
        /* length field covers everything after the first 8 bytes */
        hdr->length = htons(inner_len + 8);  /* 4 (seq/npdu/next) + 4 (ext) + payload */
        hdr->teid = htonl(teid);

        /* Sequence number (2) + N-PDU (1) + Next Ext Header Type (1) */
        out_buf[8] = 0;
        out_buf[9] = 0;
        out_buf[10] = 0;
        out_buf[11] = GTPU_EXT_PDU_SESSION_CONTAINER;

        /* PDU Session Container extension (DL PDU SESSION INFORMATION, type=0)
         * TS 38.415 §5.5.2.1:
         *   Octet 1: PDU Type(4) | QMP(1) | SNP(1) | spare(2)
         *   Octet 2: PPP(1) | RQI(1) | QFI(6) */
        out_buf[12] = 1;                          /* ext_len = 1 (4 bytes) */
        out_buf[13] = 0x00;                       /* type=0(DL), QMP=0, SNP=0 */
        out_buf[14] = (qfi & 0x3F);              /* PPP=0, RQI=0, QFI */
        out_buf[15] = 0;                          /* next_ext = 0 (no more) */

        memcpy(out_buf + 16, inner_pkt, inner_len);
        return total;
    } else {
        /* No extension headers — simple 8-byte header */
        uint16_t total = 8 + inner_len;
        if (total > out_buf_sz) return -1;

        gtpu_hdr_t *hdr = (gtpu_hdr_t *)out_buf;
        hdr->flags = GTPU_VER_V1 | GTPU_PT_GTP;
        hdr->msg_type = GTPU_MSG_GPDU;
        hdr->length = htons(inner_len);
        hdr->teid = htonl(teid);

        memcpy(out_buf + 8, inner_pkt, inner_len);
        return total;
    }
}
