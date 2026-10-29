/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_sdf_parser.c — SDF filter string parser
 *
 * Parses IPFilterRule:  action dir proto from src [port[-port]] to dst [port[-port]]
 * Examples:
 *   "permit out ip from 10.0.0.0/8 to any"
 *   "permit out 17 from any to 10.45.0.2 1234-1235"
 *   "permit out ip from any to any"
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <arpa/inet.h>

#include "upf_sdf_parser.h"

/* Parse "a.b.c.d/prefix" or "a.b.c.d" or "any" into addr+mask */
static int parse_addr(const char *s, uint32_t *addr, uint32_t *mask)
{
    if (strcmp(s, "any") == 0) {
        *addr = 0;
        *mask = 0;
        return 0;
    }

    char buf[64];
    strncpy(buf, s, sizeof(buf) - 1);
    buf[sizeof(buf) - 1] = '\0';

    char *slash = strchr(buf, '/');
    int prefix = 32;
    if (slash) {
        *slash = '\0';
        prefix = atoi(slash + 1);
        if (prefix < 0 || prefix > 32) return -1;
    }

    struct in_addr ia;
    if (inet_pton(AF_INET, buf, &ia) != 1) return -1;

    *addr = ia.s_addr;  /* already network byte order */
    if (prefix == 0)
        *mask = 0;
    else
        *mask = htonl(~((1U << (32 - prefix)) - 1));

    return 0;
}

/* Parse "port" or "port-port" or nothing. Returns 0 on success. */
static int parse_port_range(const char *s, uint16_t *lo, uint16_t *hi)
{
    if (!s || s[0] == '\0') {
        *lo = 0;
        *hi = 65535;
        return 0;
    }

    char *dash = strchr(s, '-');
    if (dash) {
        *lo = (uint16_t)atoi(s);
        *hi = (uint16_t)atoi(dash + 1);
    } else {
        *lo = *hi = (uint16_t)atoi(s);
    }
    return 0;
}

int upf_sdf_parse(const char *rule_str, upf_sdf_filter_t *out)
{
    if (!rule_str || !out) return -1;

    memset(out, 0, sizeof(*out));
    out->src_port_hi = 65535;
    out->dst_port_hi = 65535;

    /* Tokenize a local copy */
    char buf[256];
    strncpy(buf, rule_str, sizeof(buf) - 1);
    buf[sizeof(buf) - 1] = '\0';

    char *tokens[12];
    int ntok = 0;
    char *p = strtok(buf, " \t");
    while (p && ntok < 12) {
        tokens[ntok++] = p;
        p = strtok(NULL, " \t");
    }

    if (ntok < 6) return -1;  /* minimum: action dir proto from src to dst */

    int ti = 0;

    /* action */
    if (strcmp(tokens[ti], "permit") == 0)
        out->action = 1;
    else if (strcmp(tokens[ti], "deny") == 0)
        out->action = 2;
    else
        return -1;
    ti++;

    /* direction: in=1, out=2, bidirectional=3
     * TS 29.244 §8.2.5: SDF filter direction — bidirectional matches both. */
    if (strcmp(tokens[ti], "in") == 0)
        out->direction = 1;
    else if (strcmp(tokens[ti], "out") == 0)
        out->direction = 2;
    else if (strcmp(tokens[ti], "bidirectional") == 0)
        out->direction = 3;
    else
        return -1;
    ti++;

    /* protocol */
    if (strcmp(tokens[ti], "ip") == 0)
        out->proto = 255;  /* any */
    else
        out->proto = (uint8_t)atoi(tokens[ti]);
    ti++;

    /* "from" keyword */
    if (strcmp(tokens[ti], "from") != 0) return -1;
    ti++;

    /* source address — parse into locals then store, since upf_sdf_filter_t is
     * packed (ctypes interop) and taking addresses of packed fields is UB on
     * strict-alignment targets (-Waddress-of-packed-member). */
    if (ti >= ntok) return -1;
    uint32_t addr, mask;
    if (parse_addr(tokens[ti], &addr, &mask) < 0) return -1;
    out->src_addr = addr;
    out->src_mask = mask;
    ti++;

    /* optional source port(s) before "to" */
    if (ti < ntok && strcmp(tokens[ti], "to") != 0) {
        uint16_t lo, hi;
        parse_port_range(tokens[ti], &lo, &hi);
        out->src_port_lo = lo;
        out->src_port_hi = hi;
        ti++;
    }

    /* "to" keyword */
    if (ti >= ntok || strcmp(tokens[ti], "to") != 0) return -1;
    ti++;

    /* destination address */
    if (ti >= ntok) return -1;
    if (parse_addr(tokens[ti], &addr, &mask) < 0) return -1;
    out->dst_addr = addr;
    out->dst_mask = mask;
    ti++;

    /* optional destination port(s) */
    if (ti < ntok) {
        uint16_t lo, hi;
        parse_port_range(tokens[ti], &lo, &hi);
        out->dst_port_lo = lo;
        out->dst_port_hi = hi;
    }

    return 0;
}

int upf_sdf_format(const upf_sdf_filter_t *f, char *buf, size_t bufsz)
{
    if (!f || !buf || bufsz == 0) return -1;

    const char *act = (f->action == 1) ? "permit" : "deny";
    const char *dir = (f->direction == 1) ? "in" : "out";

    char proto_str[8];
    if (f->proto == 255)
        snprintf(proto_str, sizeof(proto_str), "ip");
    else
        snprintf(proto_str, sizeof(proto_str), "%u", f->proto);

    char src[32], dst[32];
    if (f->src_addr == 0 && f->src_mask == 0) {
        snprintf(src, sizeof(src), "any");
    } else {
        struct in_addr ia = { .s_addr = f->src_addr };
        /* Count prefix bits from mask */
        uint32_t m = ntohl(f->src_mask);
        int prefix = __builtin_popcount(m);
        snprintf(src, sizeof(src), "%s/%d", inet_ntoa(ia), prefix);
    }

    if (f->dst_addr == 0 && f->dst_mask == 0) {
        snprintf(dst, sizeof(dst), "any");
    } else {
        struct in_addr ia = { .s_addr = f->dst_addr };
        uint32_t m = ntohl(f->dst_mask);
        int prefix = __builtin_popcount(m);
        snprintf(dst, sizeof(dst), "%s/%d", inet_ntoa(ia), prefix);
    }

    int n = snprintf(buf, bufsz, "%s %s %s from %s", act, dir, proto_str, src);

    if (f->src_port_lo != 0 || f->src_port_hi != 65535) {
        if (f->src_port_lo == f->src_port_hi)
            n += snprintf(buf + n, bufsz - n, " %u", f->src_port_lo);
        else
            n += snprintf(buf + n, bufsz - n, " %u-%u", f->src_port_lo, f->src_port_hi);
    }

    n += snprintf(buf + n, bufsz - n, " to %s", dst);

    if (f->dst_port_lo != 0 || f->dst_port_hi != 65535) {
        if (f->dst_port_lo == f->dst_port_hi)
            n += snprintf(buf + n, bufsz - n, " %u", f->dst_port_lo);
        else
            n += snprintf(buf + n, bufsz - n, " %u-%u", f->dst_port_lo, f->dst_port_hi);
    }

    return n;
}
