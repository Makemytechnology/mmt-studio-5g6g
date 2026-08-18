/* Copyright (c) 2026 MakeMyTechnology. All rights reserved. */
/* upf_pkt_io_dpdk.c — DPDK PMD-based packet I/O for UPF fast path
 *
 * Replaces socket/TUN-based I/O with zero-copy DPDK poll-mode:
 *   N3 (GTP-U): TAP or AF_PACKET PMD — rte_eth_rx/tx_burst
 *   N6 (Core):  TAP PMD — connects to kernel for routing/NAT
 *   Packets:    rte_mbuf from hugepage mempool — zero-copy
 *   I/O loop:   Tight poll (no select, no syscalls on fast path)
 *
 * PMD options (selected via EAL --vdev):
 *   --vdev=net_tap0    — TAP device (portable, no NIC binding)
 *   --vdev=net_af_packet0,iface=eth0 — AF_PACKET (use existing kernel NIC)
 *
 * Pipeline:
 *   UL: N3 PMD rx → GTP-U decap → PDR match(+QFI) → QER/Meter → URR → FAR → N6 PMD tx
 *   DL: N6 PMD rx → PDR match → QER/Meter → URR → FAR → GTP-U encap → N3 PMD tx
 *
 * 3GPP references:
 *   TS 29.244 — PFCP: PDR/FAR/QER/URR pipeline
 *   TS 38.415 — GTP-U extension header (PDU Session Container + QFI)
 *   TS 23.501 §5.7 — QoS model (MBR, GBR, AMBR)
 */

#include <stdio.h>
#include <string.h>
#include <signal.h>

#include <rte_eal.h>
#include <rte_ethdev.h>
#include <rte_mbuf.h>
#include <rte_mempool.h>
#include <rte_ring.h>
#include <rte_cycles.h>
#include <rte_log.h>
#include <rte_ip.h>
#include <rte_udp.h>
#include <rte_hash.h>
#include <rte_jhash.h>
#include <rte_errno.h>

#include "upf_pkt_io.h"
#include "upf_gtpu.h"
#include "upf_classifier.h"
#include "upf_qos_meter.h"
#include "upf_session_table.h"

#define RTE_LOGTYPE_UPF RTE_LOGTYPE_USER1

/* ── Configuration ──
 * MBUF pool size and RX/TX ring sizes come from the runtime globals
 * g_upf_mbuf_pool_size, g_upf_rx_ring_size, g_upf_tx_ring_size (see
 * upf_types.h). Python calls upf_dp_set_pmd_tuning() before
 * upf_dp_init() to drive these from the DB. */
#define MBUF_CACHE_SIZE   256
#define MBUF_DATA_SIZE    RTE_MBUF_DEFAULT_BUF_SIZE
#define BURST_SIZE        32
#define GTP_U_PORT        2152

/* ── Port IDs ── */
static uint16_t n3_port_id = RTE_MAX_ETHPORTS;  /* N3 (GTP-U to/from gNB) */
static uint16_t n6_port_id = RTE_MAX_ETHPORTS;  /* N6 (to/from core network) */

/* ── Mempool ── */
static struct rte_mempool *mbuf_pool = NULL;

/* ── Stats ── */
static upf_io_stats_t io_stats;

/* ── Hash tables (same as socket-based version) ── */
#define MAX_TEID_MAP 65536
static struct rte_hash *teid_hash = NULL;
static struct rte_hash *ueip_hash = NULL;

typedef struct {
    char     imsi[16];
    uint8_t  pdu_session_id;
} teid_map_entry_t;

static teid_map_entry_t teid_entries[MAX_TEID_MAP];
static teid_map_entry_t ueip_entries[MAX_TEID_MAP];

static volatile int running = 0;
static uint32_t last_unknown_teid = 0;
static uint32_t last_registered_teid = 0;

/* ── Port initialization ── */

static int init_port(uint16_t port_id)
{
    struct rte_eth_conf port_conf = {0};
    struct rte_eth_dev_info dev_info;
    int ret;

    ret = rte_eth_dev_info_get(port_id, &dev_info);
    if (ret != 0) {
        RTE_LOG(ERR, UPF, "Cannot get device info for port %u: %d\n", port_id, ret);
        return ret;
    }

    /* Single RX + TX queue */
    ret = rte_eth_dev_configure(port_id, 1, 1, &port_conf);
    if (ret != 0) {
        RTE_LOG(ERR, UPF, "Cannot configure port %u: %d\n", port_id, ret);
        return ret;
    }

    /* Setup RX queue (ring size from g_upf_rx_ring_size) */
    ret = rte_eth_rx_queue_setup(port_id, 0, g_upf_rx_ring_size,
                                  rte_eth_dev_socket_id(port_id), NULL, mbuf_pool);
    if (ret < 0) {
        RTE_LOG(ERR, UPF, "Cannot setup RX queue for port %u: %d\n", port_id, ret);
        return ret;
    }

    /* Setup TX queue (ring size from g_upf_tx_ring_size) */
    ret = rte_eth_tx_queue_setup(port_id, 0, g_upf_tx_ring_size,
                                  rte_eth_dev_socket_id(port_id), NULL);
    if (ret < 0) {
        RTE_LOG(ERR, UPF, "Cannot setup TX queue for port %u: %d\n", port_id, ret);
        return ret;
    }

    /* Start device */
    ret = rte_eth_dev_start(port_id);
    if (ret < 0) {
        RTE_LOG(ERR, UPF, "Cannot start port %u: %d\n", port_id, ret);
        return ret;
    }

    /* Enable promiscuous mode */
    rte_eth_promiscuous_enable(port_id);

    RTE_LOG(INFO, UPF, "PMD port %u initialized (RX=%u TX=%u)\n",
            port_id, g_upf_rx_ring_size, g_upf_tx_ring_size);
    return 0;
}

/* ── TEID / UE-IP registration (same interface as socket version) ── */

int upf_pkt_io_add_teid_map(uint32_t teid, const char *imsi, uint8_t pdu_session_id)
{
    if (!teid_hash) return -1;
    uint32_t key = teid;
    int32_t idx = rte_hash_add_key(teid_hash, &key);
    if (idx < 0) return -1;
    strncpy(teid_entries[idx].imsi, imsi, 15);
    teid_entries[idx].pdu_session_id = pdu_session_id;
    __atomic_store_n(&last_registered_teid, teid, __ATOMIC_RELAXED);
    RTE_LOG(INFO, UPF, "TEID 0x%08x registered: %s/%u\n", teid, imsi, pdu_session_id);
    return 0;
}

int upf_pkt_io_add_ueip_map(uint32_t ue_addr, const char *imsi, uint8_t pdu_session_id)
{
    if (!ueip_hash) return -1;
    int32_t idx = rte_hash_add_key(ueip_hash, &ue_addr);
    if (idx < 0) return -1;
    strncpy(ueip_entries[idx].imsi, imsi, 15);
    ueip_entries[idx].pdu_session_id = pdu_session_id;
    return 0;
}

/* ── UL processing: N3 rx → GTP-U decap → classify → N6 tx ── */

static inline void process_ul_mbuf(struct rte_mbuf *mbuf)
{
    uint8_t *pkt = rte_pktmbuf_mtod(mbuf, uint8_t *);
    uint16_t pkt_len = rte_pktmbuf_data_len(mbuf);

    /* Skip Ethernet + IP + UDP headers to get GTP-U */
    /* TAP PMD delivers raw Ethernet frames */
    if (pkt_len < 14 + 20 + 8 + 8) return;  /* eth + ip + udp + gtpu min */

    uint8_t *ip_hdr = pkt + 14;  /* skip ethernet */
    uint8_t ip_proto = ip_hdr[9];
    if (ip_proto != 17) return;  /* not UDP */

    uint8_t ip_ihl = (ip_hdr[0] & 0x0F) * 4;
    uint8_t *udp_hdr = ip_hdr + ip_ihl;
    uint16_t dst_port = (udp_hdr[2] << 8) | udp_hdr[3];
    if (dst_port != GTP_U_PORT) return;  /* not GTP-U */

    uint8_t *gtpu_start = udp_hdr + 8;
    int gtpu_len = pkt_len - 14 - ip_ihl - 8;

    gtpu_decoded_t decoded;
    if (gtpu_decode(gtpu_start, gtpu_len, &decoded) < 0) {
        __atomic_add_fetch(&io_stats.gtpu_errors, 1, __ATOMIC_RELAXED);
        return;
    }

    /* TS 38.415 §5.5.2.1: QFI mandatory for 5G NR */
    if (decoded.qfi == 0) {
        __atomic_add_fetch(&io_stats.ul_dropped, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Session lookup by TEID */
    int32_t idx = rte_hash_lookup(teid_hash, &decoded.teid);
    if (idx < 0) {
        __atomic_add_fetch(&io_stats.ul_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.ul_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    teid_map_entry_t *entry = &teid_entries[idx];
    upf_session_t *sess = upf_session_get(entry->imsi, entry->pdu_session_id);
    if (!sess) {
        __atomic_add_fetch(&io_stats.ul_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.ul_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Classify + QER + Meter + URR */
    upf_classify_result_t result;
    int action = upf_process_packet(sess, 0, decoded.qfi,
                                     decoded.payload, decoded.payload_len, &result);

    if (action < 0 || !result.gate_pass) {
        __atomic_add_fetch(&io_stats.ul_dropped, 1, __ATOMIC_RELAXED);
        return;
    }
    if (!result.meter_pass) {
        __atomic_add_fetch(&io_stats.ul_metered, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Forward to N6: rebuild mbuf with inner IP packet only */
    rte_pktmbuf_adj(mbuf, (uint16_t)(decoded.payload - pkt));
    /* rte_pktmbuf_trim if needed */

    /* TX to N6 port */
    uint16_t sent = rte_eth_tx_burst(n6_port_id, 0, &mbuf, 1);
    if (sent == 0) {
        rte_pktmbuf_free(mbuf);
        __atomic_add_fetch(&io_stats.ul_dropped, 1, __ATOMIC_RELAXED);
        return;
    }

    __atomic_add_fetch(&io_stats.ul_pkts, 1, __ATOMIC_RELAXED);
    __atomic_add_fetch(&io_stats.ul_bytes, decoded.payload_len, __ATOMIC_RELAXED);
}

/* ── DL processing: N6 rx → classify → GTP-U encap → N3 tx ── */

static inline void process_dl_mbuf(struct rte_mbuf *mbuf)
{
    uint8_t *pkt = rte_pktmbuf_mtod(mbuf, uint8_t *);
    uint16_t pkt_len = rte_pktmbuf_data_len(mbuf);

    /* TAP PMD delivers Ethernet frames — skip eth header */
    if (pkt_len < 14 + 20) return;

    uint8_t *ip_pkt = pkt + 14;
    uint16_t ip_len = pkt_len - 14;
    uint8_t version = (ip_pkt[0] >> 4) & 0x0F;
    if (version != 4) return;

    /* Extract destination IP */
    uint32_t dst_addr;
    memcpy(&dst_addr, ip_pkt + 16, 4);

    /* Session lookup by UE IP */
    int32_t idx = rte_hash_lookup(ueip_hash, &dst_addr);
    if (idx < 0) {
        __atomic_add_fetch(&io_stats.dl_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.dl_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    teid_map_entry_t *entry = &ueip_entries[idx];
    upf_session_t *sess = upf_session_get(entry->imsi, entry->pdu_session_id);
    if (!sess) {
        __atomic_add_fetch(&io_stats.dl_dropped,    1, __ATOMIC_RELAXED);
        __atomic_add_fetch(&io_stats.dl_no_session, 1, __ATOMIC_RELAXED);
        return;
    }

    /* Classify + QER + Meter + URR */
    upf_classify_result_t result;
    int action = upf_process_packet(sess, 1, 0, ip_pkt, ip_len, &result);

    if (action < 0 || !result.gate_pass) {
        __atomic_add_fetch(&io_stats.dl_dropped, 1, __ATOMIC_RELAXED);
        return;
    }
    if (!result.meter_pass) {
        __atomic_add_fetch(&io_stats.dl_metered, 1, __ATOMIC_RELAXED);
        return;
    }

    if (action == 1 && result.far && result.far->ohc_type == 1) {
        uint8_t qfi = result.matched_pdr ? result.matched_pdr->qfi : 0;

        /* GTP-U encap into a new mbuf */
        uint8_t gtpu_buf[2048];
        int gtpu_len = gtpu_encode(gtpu_buf, sizeof(gtpu_buf),
                                    result.far->ohc_teid, qfi, ip_pkt, ip_len);
        if (gtpu_len <= 0) return;

        /* Build UDP/IP/Ethernet headers + GTP-U payload in a new mbuf */
        struct rte_mbuf *tx_mbuf = rte_pktmbuf_alloc(mbuf_pool);
        if (!tx_mbuf) return;

        /* For TAP PMD, we need to construct the full frame:
         * Eth(14) + IP(20) + UDP(8) + GTP-U(encapped)
         * Simplified: just send the GTP-U as UDP payload via sendto-equivalent
         * TODO: Build full Ethernet frame for real PMD */
        uint8_t *tx_data = rte_pktmbuf_mtod(tx_mbuf, uint8_t *);
        memcpy(tx_data, gtpu_buf, gtpu_len);
        tx_mbuf->data_len = gtpu_len;
        tx_mbuf->pkt_len = gtpu_len;

        uint16_t sent = rte_eth_tx_burst(n3_port_id, 0, &tx_mbuf, 1);
        if (sent == 0) {
            rte_pktmbuf_free(tx_mbuf);
            __atomic_add_fetch(&io_stats.dl_dropped, 1, __ATOMIC_RELAXED);
        } else {
            __atomic_add_fetch(&io_stats.dl_pkts, 1, __ATOMIC_RELAXED);
            __atomic_add_fetch(&io_stats.dl_bytes, ip_len, __ATOMIC_RELAXED);
        }
    }
}

/* ── Main poll loop ── */

static void pmd_poll_loop(void)
{
    struct rte_mbuf *rx_bufs[BURST_SIZE];
    uint16_t nb_rx;

    RTE_LOG(INFO, UPF, "PMD poll loop started (N3=%u, N6=%u, burst=%u)\n",
            n3_port_id, n6_port_id, BURST_SIZE);

    while (running) {
        /* ── UL: Poll N3 port for GTP-U packets ── */
        if (n3_port_id < RTE_MAX_ETHPORTS) {
            nb_rx = rte_eth_rx_burst(n3_port_id, 0, rx_bufs, BURST_SIZE);
            for (uint16_t i = 0; i < nb_rx; i++) {
                process_ul_mbuf(rx_bufs[i]);
                /* process_ul_mbuf may have freed or forwarded the mbuf */
            }
        }

        /* ── DL: Poll N6 port for IP packets (from kernel/internet) ── */
        if (n6_port_id < RTE_MAX_ETHPORTS) {
            nb_rx = rte_eth_rx_burst(n6_port_id, 0, rx_bufs, BURST_SIZE);
            for (uint16_t i = 0; i < nb_rx; i++) {
                process_dl_mbuf(rx_bufs[i]);
                rte_pktmbuf_free(rx_bufs[i]);  /* DL creates new tx mbuf */
            }
        }
    }

    RTE_LOG(INFO, UPF, "PMD poll loop stopped\n");
}

/* ── Public API (same interface as socket version) ── */

int upf_pkt_io_init(const upf_pkt_io_cfg_t *cfg)
{
    if (!cfg) return -1;

    memset(&io_stats, 0, sizeof(io_stats));

    /* Create mbuf mempool from hugepages.
     * Try lookup first (may exist from previous init in same process). */
    mbuf_pool = rte_mempool_lookup("upf_mbuf_pool");
    if (mbuf_pool) {
        RTE_LOG(INFO, UPF, "mbuf pool already exists (reusing)\n");
    } else {
        mbuf_pool = rte_pktmbuf_pool_create("upf_mbuf_pool",
                                             g_upf_mbuf_pool_size, MBUF_CACHE_SIZE,
                                             0, MBUF_DATA_SIZE,
                                             rte_socket_id());
        if (!mbuf_pool) {
            RTE_LOG(WARNING, UPF, "Cannot create %u-mbuf pool (rte_errno=%d) — trying 1024\n",
                    g_upf_mbuf_pool_size, rte_errno);
            mbuf_pool = rte_pktmbuf_pool_create("upf_mbuf_pool_sm",
                                                 1024, 128, 0, MBUF_DATA_SIZE,
                                                 rte_socket_id());
        }
        if (!mbuf_pool) {
            RTE_LOG(ERR, UPF, "Cannot create mbuf pool (rte_errno=%d). "
                    "Check: sudo rm -f /dev/hugepages/rtemap_* && restart\n", rte_errno);
            return -1;
        }
    }
    RTE_LOG(INFO, UPF, "mbuf pool ready\n");

    /* Create TEID and UE-IP hash tables */
    struct rte_hash_parameters teid_params = {
        .name = "upf_teid_map", .entries = MAX_TEID_MAP,
        .key_len = sizeof(uint32_t), .hash_func = rte_jhash, .socket_id = 0,
    };
    teid_hash = rte_hash_create(&teid_params);
    if (!teid_hash) {
        RTE_LOG(ERR, UPF, "Failed to create TEID hash\n");
        return -1;
    }

    struct rte_hash_parameters ueip_params = {
        .name = "upf_ueip_map", .entries = MAX_TEID_MAP,
        .key_len = sizeof(uint32_t), .hash_func = rte_jhash, .socket_id = 0,
    };
    ueip_hash = rte_hash_create(&ueip_params);
    if (!ueip_hash) {
        RTE_LOG(ERR, UPF, "Failed to create UE-IP hash\n");
        return -1;
    }

    /* Initialize QoS metering — non-fatal, forwarding works without it */
    if (upf_qos_meter_init() < 0) {
        RTE_LOG(WARNING, UPF, "QoS meter init failed — metering disabled\n");
    }

    /* Initialize available PMD ports */
    uint16_t nb_ports = rte_eth_dev_count_avail();
    RTE_LOG(INFO, UPF, "DPDK PMD ports available: %u\n", nb_ports);

    if (nb_ports >= 2) {
        /* Two ports: N3 (port 0) + N6 (port 1) */
        n3_port_id = 0;
        n6_port_id = 1;
    } else if (nb_ports == 1) {
        /* Single port: shared N3/N6 (TAP mode) */
        n3_port_id = 0;
        n6_port_id = 0;
        RTE_LOG(WARNING, UPF, "Single PMD port — N3 and N6 share port 0\n");
    } else {
        RTE_LOG(WARNING, UPF, "No PMD ports — falling back to socket I/O\n");
        return 1;  /* positive = fallback to socket mode */
    }

    /* Initialize ports */
    for (uint16_t p = 0; p < nb_ports && p < 2; p++) {
        if (init_port(p) < 0) {
            RTE_LOG(ERR, UPF, "Failed to init port %u\n", p);
            return -1;
        }
    }

    RTE_LOG(INFO, UPF, "DPDK PMD fast path initialized: N3=port%u N6=port%u\n",
            n3_port_id, n6_port_id);
    return 0;
}

int upf_pkt_io_run(void)
{
    running = 1;
    pmd_poll_loop();
    return 0;
}

void upf_pkt_io_stop(void)
{
    running = 0;
}

void upf_pkt_io_cleanup(void)
{
    running = 0;
    if (n3_port_id < RTE_MAX_ETHPORTS) rte_eth_dev_stop(n3_port_id);
    if (n6_port_id < RTE_MAX_ETHPORTS && n6_port_id != n3_port_id)
        rte_eth_dev_stop(n6_port_id);
    if (teid_hash) { rte_hash_free(teid_hash); teid_hash = NULL; }
    if (ueip_hash) { rte_hash_free(ueip_hash); ueip_hash = NULL; }
    /* Note: mbuf_pool freed by EAL cleanup */
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
