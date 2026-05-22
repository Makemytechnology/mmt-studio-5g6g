/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_pkt_io.c — Packet I/O: GTP-U socket (N3) + TUN interface (N6)
 *
 * UL path: GTP-U recv → decap → session lookup → classify → QER → URR → FAR → TUN write
 * DL path: TUN read  → session lookup → classify → QER → URR → FAR → GTP-U encap → send
 *
 * Session lookup for UL: by TEID (from GTP-U header)
 * Session lookup for DL: by destination IP (from inner IPv4 header)
 *
 * Uses select() for I/O multiplexing. Single-threaded for correctness.
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>
#include <fcntl.h>
#include <errno.h>
#include <sys/select.h>
#include <sys/socket.h>
#include <sys/ioctl.h>
#include <netinet/in.h>
#include <arpa/inet.h>
#include <linux/if.h>
#include <linux/if_tun.h>

#include <rte_log.h>
#include <rte_hash.h>
#include <rte_jhash.h>
#include <rte_malloc.h>

#include "upf_pkt_io.h"
#include "upf_gtpu.h"
#include "upf_classifier.h"
#include "upf_qos_meter.h"
#include "upf_session_table.h"
#include "upf_report.h"

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1
#define PKT_BUF_SZ  65536

/* ── State ── */
static int gtpu_fd = -1;          /* GTP-U UDP socket */
static int tun_fd = -1;           /* TUN device fd */
static volatile int running = 0;

/* TEID → (imsi, pdu_session_id) reverse map for UL lookup. */
typedef struct __attribute__((__packed__)) {
    uint32_t teid;
    char     imsi[16];
    uint8_t  pdu_session_id;
    uint8_t  _pad[3];
} teid_map_entry_t;

/* UE IP → (imsi, pdu_session_id) reverse map for DL lookup. */
typedef struct __attribute__((__packed__)) {
    uint32_t ue_addr;  /* network byte order */
    char     imsi[16];
    uint8_t  pdu_session_id;
    uint8_t  _pad[3];
} ueip_map_entry_t;

/* Compile-time floor; runtime size scales with g_upf_max_sessions
 * (see upf_pkt_io_init). With one default bearer per UE installing
 * one TEID + one UE-IP, baseline ratio is 1:1 against session count.
 * 4× allows up to ~3 dedicated bearers per session before hash
 * pressure kicks in. Floor of 8192 keeps small deployments unchanged. */
#define MAX_TEID_MAP_FLOOR  8192
#define MAX_TEID_MAP_RATIO  4

static struct rte_hash *teid_hash = NULL;
static struct rte_hash *ueip_hash = NULL;
/* Dynamically allocated via rte_zmalloc at init; sized to match the
 * hash table's entry count so positions returned by rte_hash_add_key
 * always fall within the array bound. Replaces the static arrays
 * that used to be sized by MAX_TEID_MAP=8192. */
static teid_map_entry_t *teid_entries = NULL;
static ueip_map_entry_t *ueip_entries = NULL;
static uint32_t          teid_map_capacity = 0;

/* Deferred-free ring for RTE_HASH_EXTRA_FLAGS_RW_CONCURRENCY_LF.
 * rte_hash_del_key only marks the slot; rte_hash_free_key_with_position
 * actually releases it. We must not free immediately because pkt_io
 * readers may still hold a position obtained by rte_hash_lookup right
 * before the delete. Holding N freed positions before reusing each
 * gives readers grace equal to N control-plane events — overwhelmingly
 * more than a single packet's lookup→use window. Per DPDK docs, this
 * matches the "RCU-defer" pattern in lieu of a full RCU integration.
 *
 * Spec ground: TS 29.244 v19.5.0 §7.5.6 — "delete an existing PFCP
 * session at the UP function" — the F-TEID resources allocated by
 * the UP function (§5.5.1) and the UE IP allocation (§8.2.62) tied
 * to that session must be released so they don't leak across the
 * §7.5.6 → §7.5.2 (re-)establishment cycle. */
/* Depth = max in-flight cascade size at which we still avoid forcing
 * a synchronous rte_hash_free_key_with_position. PERFORMANCE.md Run 4
 * showed cascade going super-linear at 128 UEs (33 ms → 197 ms when
 * doubling from 64); the ring wrapped mid-cascade and every position
 * 65+ triggered a synchronous free. 256 covers the 128-UE workload
 * comfortably, and at 64-byte slots costs 16 KB extra static state
 * per ring × 2 rings (teid + ueip) = 32 KB total — negligible. */
#define DEFERRED_FREE_DEPTH 256
typedef struct {
    int32_t  position;
    bool     valid;
} deferred_free_slot_t;

static deferred_free_slot_t teid_deferred[DEFERRED_FREE_DEPTH];
static unsigned             teid_deferred_idx = 0;
static deferred_free_slot_t ueip_deferred[DEFERRED_FREE_DEPTH];
static unsigned             ueip_deferred_idx = 0;

/* Queue position into the deferred-free ring. If the slot we're
 * about to overwrite is still holding an old position, fully free it
 * first — at this point that entry is N=DEFERRED_FREE_DEPTH deletions
 * old, so any reader that obtained it has long since exited the
 * lookup→use window. */
static void defer_free_position(struct rte_hash *h,
                                deferred_free_slot_t *ring,
                                unsigned *ring_idx,
                                int32_t position)
{
    deferred_free_slot_t *slot = &ring[*ring_idx];
    if (slot->valid) {
        rte_hash_free_key_with_position(h, slot->position);
    }
    slot->position = position;
    slot->valid    = true;
    *ring_idx      = (*ring_idx + 1u) % DEFERRED_FREE_DEPTH;
}

/* I/O stats */
static upf_io_stats_t io_stats;

/* Forward declarations */
static void fixup_checksums(uint8_t *ip_pkt, int ip_len);

/* Debug: last unknown TEID seen on UL (for troubleshooting) */
static volatile uint32_t last_unknown_teid = 0;
static volatile uint32_t last_registered_teid = 0;

/* ── TUN device ── */

static int tun_alloc(const char *name)
{
    int fd = open("/dev/net/tun", O_RDWR);
    if (fd < 0) {
        RTE_LOG(ERR, UPF, "Cannot open /dev/net/tun: %s\n", strerror(errno));
        return -1;
    }

    struct ifreq ifr;
    memset(&ifr, 0, sizeof(ifr));
    ifr.ifr_flags = IFF_TUN | IFF_NO_PI;  /* TUN device, no packet info header */
    strncpy(ifr.ifr_name, name, IFNAMSIZ - 1);

    if (ioctl(fd, TUNSETIFF, &ifr) < 0) {
        RTE_LOG(ERR, UPF, "TUNSETIFF failed: %s\n", strerror(errno));
        close(fd);
        return -1;
    }

    RTE_LOG(INFO, UPF, "TUN device '%s' created (fd=%d)\n", ifr.ifr_name, fd);
    return fd;
}

/* Convert dotted-quad netmask ("255.255.0.0") to prefix length (16).
 * Returns -1 if the mask is not a contiguous run of 1-bits. */
static int mask_to_prefix(const char *mask)
{
    struct in_addr ia;
    if (!mask || inet_pton(AF_INET, mask, &ia) != 1) return -1;
    uint32_t m = ntohl(ia.s_addr);
    if (m == 0) return 0;
    /* Must be contiguous: ~m + 1 is the lowest set bit; the mask is valid iff
     * (m | (m - 1)) == 0xFFFFFFFF once the trailing zero-run is filled in. */
    uint32_t inv = ~m;
    if (inv & (inv + 1)) return -1;
    return __builtin_popcount(m);
}

static int tun_set_addr(const char *name, const char *addr, const char *mask)
{
    int prefix = mask_to_prefix(mask);
    if (prefix < 0) {
        RTE_LOG(WARNING, UPF, "TUN mask '%s' invalid, defaulting to /16\n",
                mask ? mask : "(null)");
        prefix = 16;
    }

    /* Use ip command to configure TUN — simpler than ioctl for address + route */
    char cmd[256];
    snprintf(cmd, sizeof(cmd), "ip addr add %s/%d dev %s 2>/dev/null",
             addr, prefix, name);
    int ret = system(cmd);

    snprintf(cmd, sizeof(cmd), "ip link set %s up 2>/dev/null", name);
    ret |= system(cmd);

    if (ret != 0) {
        RTE_LOG(WARNING, UPF, "TUN addr/up may have failed (need root?): %s %s\n",
                name, addr);
    }
    return 0;
}

/* ── Reverse maps ── */

int upf_pkt_io_add_teid_map(uint32_t teid, const char *imsi, uint8_t pdu_session_id)
{
    if (!teid_hash) return -1;

    int32_t idx = rte_hash_add_key(teid_hash, &teid);
    if (idx < 0) return -1;

    teid_entries[idx].teid = teid;
    strncpy(teid_entries[idx].imsi, imsi, 15);
    teid_entries[idx].pdu_session_id = pdu_session_id;
    __atomic_store_n(&last_registered_teid, teid, __ATOMIC_RELAXED);
    RTE_LOG(INFO, UPF, "Registered TEID 0x%08x for %s/%u\n", teid, imsi, pdu_session_id);
    return 0;
}

int upf_pkt_io_add_ueip_map(uint32_t ue_addr, const char *imsi, uint8_t pdu_session_id)
{
    if (!ueip_hash) return -1;

    int32_t idx = rte_hash_add_key(ueip_hash, &ue_addr);
    if (idx < 0) return -1;

    ueip_entries[idx].ue_addr = ue_addr;
    strncpy(ueip_entries[idx].imsi, imsi, 15);
    ueip_entries[idx].pdu_session_id = pdu_session_id;
    RTE_LOG(INFO, UPF, "Registered UE-IP %u.%u.%u.%u for %s/%u\n",
            ((uint8_t *)&ue_addr)[0], ((uint8_t *)&ue_addr)[1],
            ((uint8_t *)&ue_addr)[2], ((uint8_t *)&ue_addr)[3],
            imsi, pdu_session_id);
    return 0;
}

/* Release a TEID → (imsi, pdu_session_id) reverse-map entry.
 *
 * Paired with upf_pkt_io_add_teid_map; called from upf_dp_unregister_teid
 * during PFCP §7.5.6 Session Deletion so the F-TEID resource the UP
 * function allocated under §5.5.1 is actually returned. Without this
 * the teid_hash leaks and eventually saturates at MAX_TEID_MAP=8192,
 * silently breaking new sessions' UL classification.
 *
 * Returns 0 on success, -1 if the table is uninitialised or the key
 * isn't present (idempotent — duplicate unregister is not an error). */
int upf_pkt_io_del_teid_map(uint32_t teid)
{
    if (!teid_hash) return -1;

    int32_t pos = rte_hash_del_key(teid_hash, &teid);
    if (pos < 0) return -1;

    /* Wipe the entry so a stale reader that already obtained `pos`
     * before our del sees zeroed (imsi, pdu_session_id) and the
     * follow-on upf_session_get returns NULL safely. */
    memset(&teid_entries[pos], 0, sizeof(teid_entries[pos]));

    defer_free_position(teid_hash, teid_deferred, &teid_deferred_idx, pos);

    RTE_LOG(INFO, UPF, "Unregistered TEID 0x%08x\n", teid);
    return 0;
}

/* Release a UE-IP → (imsi, pdu_session_id) reverse-map entry.
 *
 * Paired with upf_pkt_io_add_ueip_map; called from upf_dp_unregister_ueip
 * during PFCP §7.5.6 Session Deletion. Without this the ueip_hash
 * leaks and eventually saturates, silently breaking new sessions' DL
 * classification.
 *
 * `ue_addr` byte-order convention matches add: host order (LE on
 * x86-64 from Go's binary.BigEndian.Uint32 of the wire bytes — see
 * the comment at process_dl_packet's ntohl).
 *
 * Returns 0 on success, -1 if uninitialised or key absent. */
int upf_pkt_io_del_ueip_map(uint32_t ue_addr)
{
    if (!ueip_hash) return -1;

    int32_t pos = rte_hash_del_key(ueip_hash, &ue_addr);
    if (pos < 0) return -1;

    memset(&ueip_entries[pos], 0, sizeof(ueip_entries[pos]));

    defer_free_position(ueip_hash, ueip_deferred, &ueip_deferred_idx, pos);

    RTE_LOG(INFO, UPF, "Unregistered UE-IP %u.%u.%u.%u\n",
            ((uint8_t *)&ue_addr)[0], ((uint8_t *)&ue_addr)[1],
            ((uint8_t *)&ue_addr)[2], ((uint8_t *)&ue_addr)[3]);
    return 0;
}

/* ── Init ── */

int upf_pkt_io_init(const upf_pkt_io_cfg_t *cfg)
{
    if (!cfg) return -1;

    memset(&io_stats, 0, sizeof(io_stats));

    /* Create TEID reverse-lookup hash */
    /* Size hash + entry arrays from the runtime session cap. With a
     * floor of MAX_TEID_MAP_FLOOR (=8192) the previous fixed-size
     * behaviour is preserved for small deployments; for 100k+
     * sessions on a Xeon host the rte_zmalloc'd arrays grow with
     * g_upf_max_sessions × MAX_TEID_MAP_RATIO. Single source of
     * truth: teid_map_capacity holds the size both rte_hash and the
     * entries array agree on. */
    {
        uint32_t want = (uint32_t)g_upf_max_sessions * MAX_TEID_MAP_RATIO;
        teid_map_capacity = want > MAX_TEID_MAP_FLOOR ? want : MAX_TEID_MAP_FLOOR;
    }

    struct rte_hash_parameters teid_params = {
        .name = "upf_teid_map",
        .entries = teid_map_capacity,
        .key_len = sizeof(uint32_t),
        .hash_func = rte_jhash,
        .socket_id = 0,
    };
    teid_hash = rte_hash_create(&teid_params);
    if (!teid_hash) {
        RTE_LOG(ERR, UPF, "Failed to create TEID hash (capacity=%u)\n", teid_map_capacity);
        return -1;
    }

    /* Create UE-IP reverse-lookup hash with the same capacity. */
    struct rte_hash_parameters ueip_params = {
        .name = "upf_ueip_map",
        .entries = teid_map_capacity,
        .key_len = sizeof(uint32_t),
        .hash_func = rte_jhash,
        .socket_id = 0,
    };
    ueip_hash = rte_hash_create(&ueip_params);
    if (!ueip_hash) {
        RTE_LOG(ERR, UPF, "Failed to create UE-IP hash (capacity=%u)\n", teid_map_capacity);
        return -1;
    }

    /* Allocate the per-position entry arrays. rte_hash_add_key may
     * return positions in [0, entries], so reserve entries+1 slots
     * to match the session_table.c convention. */
    size_t teid_bytes = (size_t)(teid_map_capacity + 1) * sizeof(teid_map_entry_t);
    size_t ueip_bytes = (size_t)(teid_map_capacity + 1) * sizeof(ueip_map_entry_t);
    teid_entries = rte_zmalloc("upf_teid_entries", teid_bytes, RTE_CACHE_LINE_SIZE);
    ueip_entries = rte_zmalloc("upf_ueip_entries", ueip_bytes, RTE_CACHE_LINE_SIZE);
    if (!teid_entries || !ueip_entries) {
        RTE_LOG(ERR, UPF, "Failed to allocate reverse-map entries (teid=%zu B, ueip=%zu B)\n",
                teid_bytes, ueip_bytes);
        return -1;
    }
    RTE_LOG(INFO, UPF, "Reverse-map sized for %u entries (TEID + UE-IP, %zu KB total)\n",
            teid_map_capacity, (teid_bytes + ueip_bytes) / 1024);

    /* Initialize QoS metering subsystem (rte_meter srTCM).
     * Non-fatal: packet forwarding (PDR+FAR) works without metering. */
    if (upf_qos_meter_init() < 0) {
        RTE_LOG(WARNING, UPF, "QoS meter init failed — metering disabled, "
                "packet forwarding still works\n");
    }

    /* GTP-U UDP socket (N3) */
    gtpu_fd = socket(AF_INET, SOCK_DGRAM, 0);
    if (gtpu_fd < 0) {
        RTE_LOG(ERR, UPF, "Cannot create GTP-U socket: %s\n", strerror(errno));
        return -1;
    }

    int reuse = 1;
    setsockopt(gtpu_fd, SOL_SOCKET, SO_REUSEADDR, &reuse, sizeof(reuse));

    struct sockaddr_in bind_addr;
    memset(&bind_addr, 0, sizeof(bind_addr));
    bind_addr.sin_family = AF_INET;
    bind_addr.sin_port = htons(cfg->n3_port ? cfg->n3_port : GTPU_PORT);
    bind_addr.sin_addr.s_addr = cfg->n3_bind_addr ?
        inet_addr(cfg->n3_bind_addr) : INADDR_ANY;

    if (bind(gtpu_fd, (struct sockaddr *)&bind_addr, sizeof(bind_addr)) < 0) {
        RTE_LOG(ERR, UPF, "Cannot bind GTP-U socket to port %u: %s\n",
                ntohs(bind_addr.sin_port), strerror(errno));
        close(gtpu_fd);
        gtpu_fd = -1;
        return -1;
    }

    RTE_LOG(INFO, UPF, "GTP-U socket bound to %s:%u\n",
            cfg->n3_bind_addr ? cfg->n3_bind_addr : "0.0.0.0",
            ntohs(bind_addr.sin_port));

    /* TUN device (N6) */
    const char *tun_name = cfg->tun_name ? cfg->tun_name : "upfgtp";
    tun_fd = tun_alloc(tun_name);
    if (tun_fd < 0) {
        RTE_LOG(WARNING, UPF, "TUN device creation failed (need root). "
                "DL forwarding to kernel disabled.\n");
        /* Continue without TUN — GTP-U still works */
    } else if (cfg->tun_addr) {
        tun_set_addr(tun_name, cfg->tun_addr,
                     cfg->tun_mask ? cfg->tun_mask : "255.255.0.0");
    }

    running = 0;
    return 0;
}

/* ── UL processing: GTP-U → decap → classify → TUN ── */

static void process_ul_packet(uint8_t *buf, int len, struct sockaddr_in *from)
{
    (void)from;
    gtpu_decoded_t decoded;
    if (gtpu_decode(buf, len, &decoded) < 0) {
        __atomic_add_fetch(&io_stats.gtpu_errors, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Lookup session by TEID */
    int32_t idx = rte_hash_lookup(teid_hash, &decoded.teid);
    if (idx < 0) {
        RTE_LOG(WARNING, UPF, "UL: unknown TEID 0x%08x (registered=0x%08x)\n",
                decoded.teid, last_registered_teid);
        __atomic_store_n(&last_unknown_teid, decoded.teid, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.ul_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.ul_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Log raw GTP-U header for UL comparison */
    {
        int dump_len = len < 24 ? len : 24;
        char hex[73];
        for (int i = 0; i < dump_len; i++) sprintf(hex + i*3, "%02X ", buf[i]);
        hex[dump_len*3] = '\0';
        RTE_LOG(DEBUG, UPF, "UL GTP-U raw (%d bytes): %s\n", len, hex);
    }

    teid_map_entry_t *entry = &teid_entries[idx];
    upf_session_t *sess = upf_session_get(entry->imsi, entry->pdu_session_id);
    if (!sess) {
        __atomic_add_fetch(&io_stats.ul_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.ul_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    /* TS 38.415 §5.5.2.1: PDU Session Container with QFI is mandatory for 5G NR.
     * UL GTP-U packets without QFI cannot be mapped to a QoS flow — drop. */
    if (decoded.qfi == 0) {
        RTE_LOG(WARNING, UPF, "UL: dropped — no QFI (missing PDU Session Container, "
                "TS 38.415 §5.5.2.1) TEID=0x%08x\n", decoded.teid);
        __atomic_add_fetch(&io_stats.ul_dropped, 1, __ATOMIC_RELAXED);
        return;
    }

    /* TS 38.415 §5.5.3 / TS 29.244 §5.2.1: QFI is part of the PDI (Packet
     * Detection Information) in each PDR. The classifier matches QFI as part
     * of PDR matching — if no PDR matches the QFI, upf_process_packet returns
     * -1 and the packet is dropped. QFI validation is integrated into the
     * classification engine, not a separate check. */

    /* Classify (PDR match incl. QFI) + QER gate + URR counters */
    upf_classify_result_t result;
    int action = upf_process_packet(sess, 0 /* UL */, decoded.qfi,
                                     decoded.payload, decoded.payload_len, &result);

    if (action < 0 || !result.gate_pass) {
        __atomic_add_fetch(&io_stats.ul_dropped, 1, __ATOMIC_RELAXED);
        return;
    }
    if (!result.meter_pass) {
        __atomic_add_fetch(&io_stats.ul_metered, 1, __ATOMIC_RELAXED);
        return;
    }

    if (action == 1 /* forward */) {
        /* Cast away decode-side const: process_ul_packet's `buf` is a
         * caller-owned writable buffer, and we deliberately mutate it
         * in-place via fixup_checksums() on the hairpin path. */
        uint8_t *ip = (uint8_t *)decoded.payload;
        uint8_t proto = ip[9];
        uint16_t sport = 0, dport = 0;
        if (decoded.payload_len >= 24 && (proto == 6 || proto == 17)) {
            uint8_t ihl = (ip[0] & 0x0F) * 4;
            if (decoded.payload_len >= (uint16_t)(ihl + 4)) {
                sport = (ip[ihl] << 8) | ip[ihl + 1];
                dport = (ip[ihl + 2] << 8) | ip[ihl + 3];
            }
        }
        RTE_LOG(DEBUG, UPF, "UL: %u.%u.%u.%u:%u → %u.%u.%u.%u:%u proto=%u len=%d\n",
                ip[12], ip[13], ip[14], ip[15], sport,
                ip[16], ip[17], ip[18], ip[19], dport,
                proto, decoded.payload_len);

        /* ── UE-to-UE hairpin: if dst IP belongs to another UE, forward
         * directly via GTP-U without going through TUN/kernel.
         * This handles VoNR/ViNR media between two UEs on the same UPF.
         * Without this, TUN delivers locally (same subnet) and never
         * bounces back for DL processing. ── */
        uint32_t dst_addr;
        memcpy(&dst_addr, ip + 16, 4);
        /* memcpy gives wire order (network); the registered key is host
         * order (MSB-first uint32 from Go ipToUint32). Normalize. */
        dst_addr = ntohl(dst_addr);
        int32_t dst_idx = rte_hash_lookup(ueip_hash, &dst_addr);

        if (dst_idx >= 0) {
            /* Destination is another UE — hairpin forward */
            ueip_map_entry_t *dst_entry = &ueip_entries[dst_idx];
            upf_session_t *dst_sess = upf_session_get(dst_entry->imsi,
                                                       dst_entry->pdu_session_id);
            if (dst_sess) {
                /* Classify DL in destination session to get correct FAR + QFI */
                upf_classify_result_t dl_result;
                int dl_action = upf_process_packet(dst_sess, 1 /* DL */, 0,
                                                    decoded.payload, decoded.payload_len,
                                                    &dl_result);
                if (dl_action == 1 && dl_result.far && dl_result.far->ohc_type == 1
                    && dl_result.gate_pass && dl_result.meter_pass) {
                    /* GTP-U encap with correct QFI from destination DL PDR */
                    uint8_t dl_qfi = dl_result.matched_pdr ? dl_result.matched_pdr->qfi : 0;
                    uint8_t gtpu_buf[2048];
                    fixup_checksums(ip, decoded.payload_len);
                    int gtpu_len = gtpu_encode(gtpu_buf, sizeof(gtpu_buf),
                                                dl_result.far->ohc_teid, dl_qfi,
                                                decoded.payload, decoded.payload_len);
                    if (gtpu_len > 0) {
                        struct sockaddr_in peer;
                        memset(&peer, 0, sizeof(peer));
                        peer.sin_family = AF_INET;
                        peer.sin_port = htons(dl_result.far->ohc_peer_port);
                        peer.sin_addr.s_addr = htonl(dl_result.far->ohc_peer_addr); /* host → wire */
                        sendto(gtpu_fd, gtpu_buf, gtpu_len, 0,
                               (struct sockaddr *)&peer, sizeof(peer));
                        RTE_LOG(DEBUG, UPF, "UL→DL hairpin: QFI=%u TEID=0x%08x → %s/%u\n",
                                dl_qfi, dl_result.far->ohc_teid,
                                dst_entry->imsi, dst_entry->pdu_session_id);
                        __atomic_add_fetch(&io_stats.dl_pkts, 1, __ATOMIC_RELAXED);
                        __atomic_add_fetch(&io_stats.dl_bytes, decoded.payload_len, __ATOMIC_RELAXED);
                    }
                }
            }
            /* Also write to TUN for non-GTP-U destinations (P-CSCF, internet) */
            /* Fall through to TUN write — kernel handles SIP, DNS, etc. */
        }

        /* Forward to core (write to TUN → kernel routing) */
        if (tun_fd >= 0) {
            int nw = write(tun_fd, decoded.payload, decoded.payload_len);
            if (nw < 0) {
                RTE_LOG(DEBUG, UPF, "UL: TUN write failed: %s\n", strerror(errno));
            }
        }
        __atomic_add_fetch(&io_stats.ul_pkts, 1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.ul_bytes, decoded.payload_len, __ATOMIC_RELAXED);
    }
    /* action 0=drop (already counted), 2=buffer (TODO) */
}

/* ── Checksum fixup for DL packets read from TUN ──
 *
 * The kernel may leave partial (hardware-offloaded) TCP/UDP checksums
 * for locally-originated packets routed to TUN. Recalculate both IP header
 * and TCP/UDP checksums before GTP-U encapsulation.
 *
 * All accumulation uses native uint16_t reads (byte-order independent
 * for internet checksum — RFC 1071 §2).
 */
static uint32_t csum_add(const void *buf, int len)
{
    const uint16_t *p = (const uint16_t *)buf;
    uint32_t sum = 0;
    while (len > 1) { sum += *p++; len -= 2; }
    if (len == 1) sum += *(const uint8_t *)p;
    return sum;
}

static uint16_t csum_finish(uint32_t sum)
{
    while (sum >> 16) sum = (sum & 0xFFFF) + (sum >> 16);
    return (uint16_t)(~sum);
}

static void fixup_checksums(uint8_t *ip_pkt, int ip_len)
{
    if (ip_len < 20) return;
    uint8_t ihl = (ip_pkt[0] & 0x0F) * 4;
    if (ihl < 20 || ip_len < ihl) return;

    /* ── Fix IP header checksum ── */
    ip_pkt[10] = 0;
    ip_pkt[11] = 0;
    uint16_t ip_csum = csum_finish(csum_add(ip_pkt, ihl));
    memcpy(ip_pkt + 10, &ip_csum, 2);  /* store in native order (matches csum_add) */

    /* ── Fix TCP/UDP checksum ── */
    uint8_t proto = ip_pkt[9];
    uint8_t *l4 = ip_pkt + ihl;
    int l4_len = ip_len - ihl;

    if ((proto == 6 || proto == 17) && l4_len >= 8) {
        int csum_off = (proto == 6) ? 16 : 6;
        if (l4_len < csum_off + 2) return;

        /* Zero existing checksum */
        l4[csum_off] = 0;
        l4[csum_off + 1] = 0;

        /* Pseudo-header: all reads as native uint16_t for consistency */
        uint32_t sum = 0;
        uint16_t *src = (uint16_t *)(ip_pkt + 12);
        uint16_t *dst = (uint16_t *)(ip_pkt + 16);
        sum += src[0] + src[1];     /* source IP */
        sum += dst[0] + dst[1];     /* dest IP */
        sum += htons(proto);        /* protocol (in network order to match uint16_t reads) */
        sum += htons((uint16_t)l4_len);  /* L4 length */

        /* L4 payload */
        sum += csum_add(l4, l4_len);

        uint16_t csum = csum_finish(sum);
        if (proto == 17 && csum == 0) csum = 0xFFFF;

        /* Store in native order (matches how csum_add reads) */
        memcpy(l4 + csum_off, &csum, 2);
    }
}

/* ── DL processing: TUN → classify → GTP-U encap → send ── */

static void process_dl_packet(uint8_t *ip_pkt, int ip_len)
{
    if (ip_len < 20) return;

    /* Check IPv4 version (upper nibble of first byte) — skip IPv6/other */
    uint8_t version = (ip_pkt[0] >> 4) & 0x0F;
    if (version != 4) {
        /* IPv6 ND/RS packets on TUN are expected — don't count as errors */
        return;
    }

    /* Extract destination IP from IPv4 header */
    uint32_t dst_addr;
    memcpy(&dst_addr, ip_pkt + 16, 4);  /* IPv4 dst at offset 16 (network order) */
    /* Registered key is host order (MSB-first uint32 from Go ipToUint32). */
    dst_addr = ntohl(dst_addr);

    /* Lookup session by UE IP */
    int32_t idx = rte_hash_lookup(ueip_hash, &dst_addr);
    if (idx < 0) {
        RTE_LOG(DEBUG, UPF, "DL: no session for dst %u.%u.%u.%u\n",
                ip_pkt[16], ip_pkt[17], ip_pkt[18], ip_pkt[19]);
        __atomic_add_fetch(&io_stats.dl_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.dl_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    ueip_map_entry_t *entry = &ueip_entries[idx];
    upf_session_t *sess = upf_session_get(entry->imsi, entry->pdu_session_id);
    if (!sess) {
        __atomic_add_fetch(&io_stats.dl_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.dl_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Classify + QER + URR */
    upf_classify_result_t result;
    int action = upf_process_packet(sess, 1 /* DL */, 0 /* no QFI for DL */,
                                     ip_pkt, ip_len, &result);

    if (action < 0 || !result.gate_pass) {
        __atomic_add_fetch(&io_stats.dl_dropped, 1, __ATOMIC_RELAXED);
        return;
    }
    if (!result.meter_pass) {
        __atomic_add_fetch(&io_stats.dl_metered, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Debug: log which PDR matched for DL classification */
    if (result.matched_pdr) {
        RTE_LOG(DEBUG, UPF, "DL: matched PDR %u (prec=%u QFI=%u n_sdf=%u) for %u.%u.%u.%u\n",
                result.matched_pdr->pdr_id, result.matched_pdr->precedence,
                result.matched_pdr->qfi, result.matched_pdr->n_sdf,
                ip_pkt[16], ip_pkt[17], ip_pkt[18], ip_pkt[19]);
    }

    /* TS 29.244 §5.2.1: Buffer DL packets while FAR action=BUFF
     * (gNB TEID not yet known — waiting for PDUSessionResourceSetupResponse) */
    if (action == 2 /* buffer */ && sess) {
        bool was_empty = !sess->dl_buf || sess->dl_buf->count == 0;
        if (!sess->dl_buf) {
            sess->dl_buf = calloc(1, sizeof(upf_dl_buf_t));
            if (sess->dl_buf)
                RTE_LOG(INFO, UPF, "DL buffer allocated for %s/%u\n",
                        sess->imsi, sess->pdu_session_id);
        }
        if (sess->dl_buf && sess->dl_buf->count < UPF_DL_BUF_SLOTS) {
            uint16_t idx = (sess->dl_buf->head + sess->dl_buf->count) % UPF_DL_BUF_SLOTS;
            uint16_t copy_len = ip_len < UPF_DL_BUF_PKT_SZ ? ip_len : UPF_DL_BUF_PKT_SZ;
            memcpy(sess->dl_buf->pkt[idx], ip_pkt, copy_len);
            sess->dl_buf->len[idx] = copy_len;
            sess->dl_buf->qfi[idx] = result.matched_pdr ? result.matched_pdr->qfi : 0;
            sess->dl_buf->count++;
            RTE_LOG(DEBUG, UPF, "DL: buffered pkt %u/%u (%u bytes) for %s/%u\n",
                    sess->dl_buf->count, UPF_DL_BUF_SLOTS, copy_len,
                    sess->imsi, sess->pdu_session_id);

            /* TS 29.244 v19.5.0 §7.5.8.2: on the first DL packet that
             * arrives while the FAR is in BUFF state, the UP function
             * shall send a PFCP Session Report Request to the CP
             * function with a Downlink Data Report IE (DLDR). We
             * gate on the buffer transition 0 → 1 so the report is
             * one-shot per inactive period — the Go consumer routes
             * it via dlnotify.go → §4.2.3.3 step 3a Namf_Communication
             * _N1N2MessageTransfer → AMF Paging (TS 23.502 §4.8.2.2b
             * step 2 when the UE is CM-CONNECTED + RRC_INACTIVE). */
            if (was_empty) {
                upf_report_t rpt;
                memset(&rpt, 0, sizeof(rpt));
                rpt.type = UPF_REPORT_DLDR;
                rpt.pdu_session_id = sess->pdu_session_id;
                strncpy(rpt.imsi, sess->imsi, sizeof(rpt.imsi) - 1);
                /* SEID isn't on upf_session_t (C side) — the Go
                 * forwarder (nf/upf/upfloop/upfloop.go forwardOneReport)
                 * resolves it via IMSI+pduSessID lookup against the
                 * Go-side session table that holds CPSEID/UPSEID. */
                rpt.seid = 0;
                rpt.u.dldr.qfi  = result.matched_pdr ? result.matched_pdr->qfi : 0;
                rpt.u.dldr.dscp = ip_pkt[1]; /* IPv4 TOS byte */
                if (upf_report_enqueue(&rpt) == 0) {
                    RTE_LOG(INFO, UPF,
                            "§7.5.8.2 DLDR enqueued for %s/%u (QFI=%u) — buffer 0→1\n",
                            sess->imsi, sess->pdu_session_id, rpt.u.dldr.qfi);
                } else {
                    RTE_LOG(WARNING, UPF,
                            "§7.5.8.2 DLDR enqueue dropped for %s/%u — paging trigger lost\n",
                            sess->imsi, sess->pdu_session_id);
                }
            }
        } else {
            __atomic_add_fetch(&io_stats.dl_dropped, 1, __ATOMIC_RELAXED);
        }
        return;
    }

    if (action == 1 /* forward */ && result.far && result.far->ohc_type == 1) {
        /* Fix IP + TCP/UDP checksums — kernel may leave partial checksums for TUN */
        fixup_checksums(ip_pkt, ip_len);

        /* GTP-U encapsulate and send to gNB */
        uint8_t gtpu_buf[PKT_BUF_SZ];
        uint8_t qfi = result.matched_pdr ? result.matched_pdr->qfi : 0;

        /* ohc_teid is stored as host-byte-order integer;
         * gtpu_encode calls htonl() internally, so pass as-is */
        int gtpu_len = gtpu_encode(gtpu_buf, sizeof(gtpu_buf),
                                    result.far->ohc_teid, qfi,
                                    ip_pkt, ip_len);
        if (gtpu_len > 0) {
            int dump_len = gtpu_len < 24 ? gtpu_len : 24;
            char hex[73];
            for (int i = 0; i < dump_len; i++) sprintf(hex + i*3, "%02X ", gtpu_buf[i]);
            hex[dump_len*3] = '\0';
            RTE_LOG(DEBUG, UPF, "DL GTP-U raw (%d bytes): %s\n", gtpu_len, hex);
        }
        if (gtpu_len < 0) {
            __atomic_add_fetch(&io_stats.dl_dropped, 1, __ATOMIC_RELAXED);
            return;
        }

        struct sockaddr_in gnb_addr;
        memset(&gnb_addr, 0, sizeof(gnb_addr));
        gnb_addr.sin_family = AF_INET;
        gnb_addr.sin_port = htons(result.far->ohc_peer_port);
        gnb_addr.sin_addr.s_addr = htonl(result.far->ohc_peer_addr); /* host → wire */

        ssize_t sent = sendto(gtpu_fd, gtpu_buf, gtpu_len, 0,
               (struct sockaddr *)&gnb_addr, sizeof(gnb_addr));
        if (sent < 0) {
            RTE_LOG(WARNING, UPF, "DL: sendto %u.%u.%u.%u:%u failed: %s\n",
                    ((uint8_t *)&gnb_addr.sin_addr)[0],
                    ((uint8_t *)&gnb_addr.sin_addr)[1],
                    ((uint8_t *)&gnb_addr.sin_addr)[2],
                    ((uint8_t *)&gnb_addr.sin_addr)[3],
                    ntohs(gnb_addr.sin_port), strerror(errno));
            __atomic_add_fetch(&io_stats.dl_dropped, 1, __ATOMIC_RELAXED);
            return;
        }

        __atomic_add_fetch(&io_stats.dl_pkts, 1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.dl_bytes, ip_len, __ATOMIC_RELAXED);

        /* Log DL packet header */
        {
            uint8_t proto = ip_pkt[9];
            uint16_t sport = 0, dport = 0;
            if (ip_len >= 24 && (proto == 6 || proto == 17)) {
                uint8_t ihl = (ip_pkt[0] & 0x0F) * 4;
                if (ip_len >= ihl + 4) {
                    sport = (ip_pkt[ihl] << 8) | ip_pkt[ihl + 1];
                    dport = (ip_pkt[ihl + 2] << 8) | ip_pkt[ihl + 3];
                }
            }
            RTE_LOG(DEBUG, UPF, "DL: %u.%u.%u.%u:%u → %u.%u.%u.%u:%u proto=%u len=%d\n",
                    ip_pkt[12], ip_pkt[13], ip_pkt[14], ip_pkt[15], sport,
                    ip_pkt[16], ip_pkt[17], ip_pkt[18], ip_pkt[19], dport,
                    proto, ip_len);
        }
    }
}

/* ── Flush DL buffer (TS 29.244 §5.2.1) ──
 * Called when FAR transitions from BUFF→FORW (gNB TEID now known).
 * Re-encapsulates and sends each buffered packet. */
int upf_pkt_io_flush_dl_buf(upf_session_t *sess, upf_far_t *far)
{
    if (!sess || !far || !sess->dl_buf || sess->dl_buf->count == 0)
        return 0;

    upf_dl_buf_t *buf = sess->dl_buf;
    int flushed = 0;

    while (buf->count > 0) {
        uint16_t idx = buf->head;
        uint8_t *ip_pkt = buf->pkt[idx];
        uint16_t ip_len = buf->len[idx];
        uint8_t  qfi    = buf->qfi[idx];

        /* GTP-U encapsulate */
        uint8_t gtpu_buf[PKT_BUF_SZ];
        int gtpu_len = gtpu_encode(gtpu_buf, sizeof(gtpu_buf),
                                    far->ohc_teid, qfi, ip_pkt, ip_len);
        if (gtpu_len > 0 && gtpu_fd >= 0) {
            struct sockaddr_in gnb_addr;
            memset(&gnb_addr, 0, sizeof(gnb_addr));
            gnb_addr.sin_family = AF_INET;
            gnb_addr.sin_port = htons(far->ohc_peer_port);
            gnb_addr.sin_addr.s_addr = htonl(far->ohc_peer_addr); /* host → wire */

            sendto(gtpu_fd, gtpu_buf, gtpu_len, 0,
                   (struct sockaddr *)&gnb_addr, sizeof(gnb_addr));

            __atomic_add_fetch(&io_stats.dl_pkts, 1, __ATOMIC_RELAXED);
            __atomic_add_fetch(&io_stats.dl_bytes, ip_len, __ATOMIC_RELAXED);
            flushed++;
        }

        buf->head = (buf->head + 1) % UPF_DL_BUF_SLOTS;
        buf->count--;
    }

    RTE_LOG(INFO, UPF, "DL buffer flushed: %d packets for %s/%u\n",
            flushed, sess->imsi, sess->pdu_session_id);

    /* Free buffer — no longer needed once FAR is FORW */
    free(sess->dl_buf);
    sess->dl_buf = NULL;

    return flushed;
}

/* ── Main processing loop ── */

int upf_pkt_io_run(void)
{
    if (gtpu_fd < 0) {
        RTE_LOG(ERR, UPF, "GTP-U socket not initialized\n");
        return -1;
    }

    running = 1;
    RTE_LOG(INFO, UPF, "Packet I/O loop started\n");

    uint8_t buf[PKT_BUF_SZ];

    while (running) {
        fd_set rfds;
        FD_ZERO(&rfds);
        FD_SET(gtpu_fd, &rfds);

        int maxfd = gtpu_fd;
        if (tun_fd >= 0) {
            FD_SET(tun_fd, &rfds);
            if (tun_fd > maxfd) maxfd = tun_fd;
        }

        struct timeval tv = { .tv_sec = 1, .tv_usec = 0 };
        int ret = select(maxfd + 1, &rfds, NULL, NULL, &tv);
        if (ret < 0) {
            if (errno == EINTR) continue;
            RTE_LOG(ERR, UPF, "select() error: %s\n", strerror(errno));
            break;
        }
        if (ret == 0) continue;  /* timeout, check running flag */

        /* UL: GTP-U packet from gNB */
        if (FD_ISSET(gtpu_fd, &rfds)) {
            struct sockaddr_in from;
            socklen_t fromlen = sizeof(from);
            int n = recvfrom(gtpu_fd, buf, sizeof(buf), 0,
                             (struct sockaddr *)&from, &fromlen);
            if (n > 0) {
                process_ul_packet(buf, n, &from);
            }
        }

        /* DL: IP packet from TUN (kernel) */
        if (tun_fd >= 0 && FD_ISSET(tun_fd, &rfds)) {
            int n = read(tun_fd, buf, sizeof(buf));
            if (n > 0) {
                process_dl_packet(buf, n);
            }
        }
    }

    RTE_LOG(INFO, UPF, "Packet I/O loop stopped\n");
    return 0;
}

void upf_pkt_io_stop(void)
{
    running = 0;
}

void upf_pkt_io_cleanup(void)
{
    upf_pkt_io_stop();

    if (gtpu_fd >= 0) { close(gtpu_fd); gtpu_fd = -1; }
    if (tun_fd >= 0) { close(tun_fd); tun_fd = -1; }

    if (teid_hash) { rte_hash_free(teid_hash); teid_hash = NULL; }
    if (ueip_hash) { rte_hash_free(ueip_hash); ueip_hash = NULL; }
    if (teid_entries) { rte_free(teid_entries); teid_entries = NULL; }
    if (ueip_entries) { rte_free(ueip_entries); ueip_entries = NULL; }
    teid_map_capacity = 0;
}

void upf_pkt_io_get_stats(upf_io_stats_t *out)
{
    if (!out) return;
    out->ul_pkts    = __atomic_load_n(&io_stats.ul_pkts, __ATOMIC_RELAXED);
    out->ul_bytes   = __atomic_load_n(&io_stats.ul_bytes, __ATOMIC_RELAXED);
    out->dl_pkts    = __atomic_load_n(&io_stats.dl_pkts, __ATOMIC_RELAXED);
    out->dl_bytes   = __atomic_load_n(&io_stats.dl_bytes, __ATOMIC_RELAXED);
    out->ul_dropped    = __atomic_load_n(&io_stats.ul_dropped, __ATOMIC_RELAXED);
    out->dl_dropped    = __atomic_load_n(&io_stats.dl_dropped, __ATOMIC_RELAXED);
    out->ul_no_session = __atomic_load_n(&io_stats.ul_no_session, __ATOMIC_RELAXED);
    out->dl_no_session = __atomic_load_n(&io_stats.dl_no_session, __ATOMIC_RELAXED);
    out->ul_metered    = __atomic_load_n(&io_stats.ul_metered, __ATOMIC_RELAXED);
    out->dl_metered    = __atomic_load_n(&io_stats.dl_metered, __ATOMIC_RELAXED);
    out->gtpu_errors = __atomic_load_n(&io_stats.gtpu_errors, __ATOMIC_RELAXED);
    out->_debug_last_unknown_teid = __atomic_load_n(&last_unknown_teid, __ATOMIC_RELAXED);
    out->_debug_last_registered_teid = __atomic_load_n(&last_registered_teid, __ATOMIC_RELAXED);
}
