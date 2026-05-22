/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_dpi.h — Deep Packet Inspection: TLS SNI + DNS snoop + PFD match
 *
 * TS 23.501 §5.8: Application Detection and Control
 *
 * Detection methods (metadata-based, works with encrypted traffic):
 *   1. TLS SNI extraction from ClientHello (plaintext even in TLS 1.3)
 *   2. DNS response snooping (passive observation of DNS answers)
 *   3. IP range matching (known CDN/service IP blocks)
 */

#ifndef UPF_DPI_H
#define UPF_DPI_H

#include <stdint.h>
#include <stdbool.h>

#define UPF_DPI_MAX_SNI_LEN     256
#define UPF_DPI_MAX_DOMAIN_LEN  256
#define UPF_DPI_MAX_APP_ID_LEN  64
#define UPF_DPI_MAX_DNS_IPS     8
#define UPF_DPI_MAX_PFD_RULES   256

/* ── TLS SNI Extraction ── */

/* Result of SNI extraction from TLS ClientHello */
typedef struct {
    char     sni[UPF_DPI_MAX_SNI_LEN];
    uint16_t sni_len;
    bool     detected;
} upf_sni_result_t;

/* Extract SNI from TLS ClientHello — TS 23.501 §5.8
 *
 * TLS record header: content_type(1)=0x16, version(2), length(2)
 * Handshake header: type(1)=0x01 (ClientHello), length(3)
 * ClientHello: version(2), random(32), session_id(var), cipher_suites(var),
 *              compression(var), extensions(var)
 * Extensions: type(2)=0x0000 (server_name), length(2), SNI list...
 *
 * Returns 0 on success, -1 if not a TLS ClientHello or no SNI found.
 */
int upf_extract_sni(const uint8_t *tcp_payload, uint16_t len,
                    upf_sni_result_t *result);

/* ── DNS Response Snooping ── */

/* Result of DNS response parsing */
typedef struct {
    char     domain[UPF_DPI_MAX_DOMAIN_LEN];
    uint32_t resolved_ipv4[UPF_DPI_MAX_DNS_IPS];   /* A records (network byte order) */
    uint8_t  resolved_ipv6[UPF_DPI_MAX_DNS_IPS][16]; /* AAAA records */
    uint8_t  num_ipv4;
    uint8_t  num_ipv6;
    uint32_t ttl;       /* minimum TTL from answer records */
    bool     detected;
} upf_dns_result_t;

/* Parse DNS response — extract domain + resolved A/AAAA records.
 * Only parses responses (QR=1). Skips queries.
 *
 * Returns 0 on success, -1 if not a valid DNS response.
 */
int upf_parse_dns_response(const uint8_t *udp_payload, uint16_t len,
                           upf_dns_result_t *result);

/* ── PFD Rule Matching ── */

/* PFD rule loaded from Python via ctypes */
typedef struct {
    char    app_id[UPF_DPI_MAX_APP_ID_LEN];
    uint8_t detection_type;     /* 0=sni, 1=dns, 2=ip_range, 3=host, 4=port_range */
    char    pattern[UPF_DPI_MAX_SNI_LEN];   /* wildcard pattern or CIDR */
    /* For ip_range: pre-parsed network address + mask */
    uint32_t ip_net;            /* network address (host byte order) */
    uint32_t ip_mask;           /* network mask (host byte order) */
    uint16_t port_lo;           /* for port_range */
    uint16_t port_hi;
} upf_pfd_rule_t;

/* PFD rule table (loaded from Python) */
typedef struct {
    upf_pfd_rule_t rules[UPF_DPI_MAX_PFD_RULES];
    uint16_t       count;
} upf_pfd_table_t;

/* Match SNI against PFD rules. Returns app_id or NULL. */
const char *upf_pfd_match_sni(const upf_pfd_table_t *table, const char *sni);

/* Match IP against PFD rules. Returns app_id or NULL. */
const char *upf_pfd_match_ip(const upf_pfd_table_t *table, uint32_t ip_addr);

/* Match port against PFD rules. Returns app_id or NULL. */
const char *upf_pfd_match_port(const upf_pfd_table_t *table, uint8_t proto,
                               uint16_t dst_port);

#endif /* UPF_DPI_H */
