/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_dpi.c — TLS SNI extraction + DNS response parsing + PFD matching
 *
 * TS 23.501 §5.8: Application Detection and Control
 * All detection is metadata-based — works with encrypted traffic.
 */

#include <string.h>
#include <arpa/inet.h>
#include <ctype.h>

#include "upf_dpi.h"

/* ── TLS SNI Extraction ── */

int upf_extract_sni(const uint8_t *data, uint16_t len,
                    upf_sni_result_t *result)
{
    memset(result, 0, sizeof(*result));

    /* TLS record header: type(1) version(2) length(2) */
    if (len < 5) return -1;
    if (data[0] != 0x16) return -1;  /* Not a handshake record */

    uint16_t rec_len = ntohs(*(const uint16_t *)(data + 3));
    if (5 + rec_len > len) return -1;

    const uint8_t *hs = data + 5;
    uint16_t hs_len = rec_len;

    /* Handshake header: type(1) length(3) */
    if (hs_len < 4) return -1;
    if (hs[0] != 0x01) return -1;  /* Not ClientHello */

    uint32_t ch_len = (hs[1] << 16) | (hs[2] << 8) | hs[3];
    const uint8_t *ch = hs + 4;
    if (ch_len + 4 > hs_len) return -1;

    /* ClientHello: version(2) + random(32) = skip 34 bytes */
    if (ch_len < 34) return -1;
    uint16_t pos = 34;

    /* Session ID: length(1) + data */
    if (pos >= ch_len) return -1;
    uint8_t sid_len = ch[pos++];
    pos += sid_len;

    /* Cipher suites: length(2) + data */
    if (pos + 2 > ch_len) return -1;
    uint16_t cs_len = ntohs(*(const uint16_t *)(ch + pos));
    pos += 2 + cs_len;

    /* Compression methods: length(1) + data */
    if (pos >= ch_len) return -1;
    uint8_t cm_len = ch[pos++];
    pos += cm_len;

    /* Extensions: length(2) */
    if (pos + 2 > ch_len) return -1;
    uint16_t ext_len = ntohs(*(const uint16_t *)(ch + pos));
    pos += 2;

    /* Walk extensions to find SNI (type=0x0000) */
    uint16_t ext_end = pos + ext_len;
    if (ext_end > ch_len) ext_end = ch_len;

    while (pos + 4 <= ext_end) {
        uint16_t ext_type = ntohs(*(const uint16_t *)(ch + pos));
        uint16_t ext_data_len = ntohs(*(const uint16_t *)(ch + pos + 2));
        pos += 4;

        if (ext_type == 0x0000 && ext_data_len > 5) {
            /* SNI extension: list_len(2) + type(1)=0 + name_len(2) + name */
            uint16_t name_len = ntohs(*(const uint16_t *)(ch + pos + 3));
            if (name_len > 0 && name_len < UPF_DPI_MAX_SNI_LEN && pos + 5 + name_len <= ext_end) {
                memcpy(result->sni, ch + pos + 5, name_len);
                result->sni[name_len] = '\0';
                result->sni_len = name_len;
                result->detected = true;
                /* Lowercase for matching */
                for (int i = 0; i < name_len; i++)
                    result->sni[i] = tolower(result->sni[i]);
                return 0;
            }
        }
        pos += ext_data_len;
    }

    return -1;  /* No SNI found */
}


/* ── DNS Response Parsing ── */

/* Skip a DNS name (handling compression pointers) */
static int _skip_dns_name(const uint8_t *pkt, uint16_t pkt_len, uint16_t offset)
{
    int jumps = 0;
    while (offset < pkt_len) {
        uint8_t label_len = pkt[offset];
        if (label_len == 0) return offset + 1;
        if ((label_len & 0xC0) == 0xC0) return offset + 2; /* Pointer */
        offset += 1 + label_len;
        if (++jumps > 64) return -1;  /* Too many labels */
    }
    return -1;
}

/* Extract domain name from DNS packet */
static int _extract_dns_name(const uint8_t *pkt, uint16_t pkt_len, uint16_t offset,
                              char *buf, uint16_t buf_len)
{
    int pos = 0, jumps = 0;
    uint16_t cur = offset;

    while (cur < pkt_len && pos < buf_len - 1) {
        uint8_t label_len = pkt[cur];
        if (label_len == 0) break;
        if ((label_len & 0xC0) == 0xC0) {
            /* Compression pointer */
            if (cur + 1 >= pkt_len) return -1;
            cur = ((label_len & 0x3F) << 8) | pkt[cur + 1];
            if (++jumps > 10) return -1;
            continue;
        }
        if (pos > 0) buf[pos++] = '.';
        cur++;
        for (int i = 0; i < label_len && cur + i < pkt_len && pos < buf_len - 1; i++)
            buf[pos++] = tolower(pkt[cur + i]);
        cur += label_len;
    }
    buf[pos] = '\0';
    return pos;
}

int upf_parse_dns_response(const uint8_t *data, uint16_t len,
                           upf_dns_result_t *result)
{
    memset(result, 0, sizeof(*result));
    result->ttl = 0xFFFFFFFF;

    /* DNS header: ID(2) FLAGS(2) QDCOUNT(2) ANCOUNT(2) NSCOUNT(2) ARCOUNT(2) = 12 bytes */
    if (len < 12) return -1;

    uint16_t flags = ntohs(*(const uint16_t *)(data + 2));
    if (!(flags & 0x8000)) return -1;  /* QR=0 means query, not response */

    uint16_t qdcount = ntohs(*(const uint16_t *)(data + 4));
    uint16_t ancount = ntohs(*(const uint16_t *)(data + 6));
    if (ancount == 0) return -1;

    /* Extract query domain name */
    uint16_t offset = 12;
    _extract_dns_name(data, len, offset, result->domain, UPF_DPI_MAX_DOMAIN_LEN);

    /* Skip question section */
    for (int i = 0; i < qdcount && offset < len; i++) {
        int end = _skip_dns_name(data, len, offset);
        if (end < 0) return -1;
        offset = end + 4;  /* QTYPE(2) + QCLASS(2) */
    }

    /* Parse answer section — extract A and AAAA records */
    for (int i = 0; i < ancount && offset + 10 < len; i++) {
        int name_end = _skip_dns_name(data, len, offset);
        if (name_end < 0) break;
        offset = name_end;

        if (offset + 10 > len) break;
        uint16_t rtype = ntohs(*(const uint16_t *)(data + offset));
        uint32_t rttl = ntohl(*(const uint32_t *)(data + offset + 4));
        uint16_t rdlen = ntohs(*(const uint16_t *)(data + offset + 8));
        offset += 10;

        if (rttl < result->ttl) result->ttl = rttl;

        if (rtype == 1 && rdlen == 4 && result->num_ipv4 < UPF_DPI_MAX_DNS_IPS) {
            /* A record */
            memcpy(&result->resolved_ipv4[result->num_ipv4], data + offset, 4);
            result->num_ipv4++;
            result->detected = true;
        } else if (rtype == 28 && rdlen == 16 && result->num_ipv6 < UPF_DPI_MAX_DNS_IPS) {
            /* AAAA record */
            memcpy(result->resolved_ipv6[result->num_ipv6], data + offset, 16);
            result->num_ipv6++;
            result->detected = true;
        }

        offset += rdlen;
    }

    if (result->ttl == 0xFFFFFFFF) result->ttl = 300;
    return result->detected ? 0 : -1;
}


/* ── PFD Rule Matching ── */

/* Simple wildcard match (supports leading *.) */
static int _wildcard_match(const char *pattern, const char *str)
{
    if (pattern[0] == '*' && pattern[1] == '.') {
        /* *.example.com matches example.com and sub.example.com */
        const char *suffix = pattern + 1;  /* .example.com */
        int suf_len = strlen(suffix);
        int str_len = strlen(str);
        if (str_len >= suf_len && strcmp(str + str_len - suf_len, suffix) == 0)
            return 1;
        /* Also match exact (without the *.) */
        if (strcmp(str, pattern + 2) == 0) return 1;
    }
    return strcmp(pattern, str) == 0;
}

const char *upf_pfd_match_sni(const upf_pfd_table_t *table, const char *sni)
{
    for (uint16_t i = 0; i < table->count; i++) {
        if (table->rules[i].detection_type == 0) {  /* sni */
            if (_wildcard_match(table->rules[i].pattern, sni))
                return table->rules[i].app_id;
        }
    }
    return NULL;
}

const char *upf_pfd_match_ip(const upf_pfd_table_t *table, uint32_t ip_addr)
{
    uint32_t ip_host = ntohl(ip_addr);
    for (uint16_t i = 0; i < table->count; i++) {
        if (table->rules[i].detection_type == 2) {  /* ip_range */
            if ((ip_host & table->rules[i].ip_mask) == table->rules[i].ip_net)
                return table->rules[i].app_id;
        }
    }
    return NULL;
}

const char *upf_pfd_match_port(const upf_pfd_table_t *table, uint8_t proto,
                               uint16_t dst_port)
{
    for (uint16_t i = 0; i < table->count; i++) {
        if (table->rules[i].detection_type == 4) {  /* port_range */
            if (dst_port >= table->rules[i].port_lo && dst_port <= table->rules[i].port_hi)
                return table->rules[i].app_id;
        }
    }
    return NULL;
}
