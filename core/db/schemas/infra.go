// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/schemas/infra.go — Infrastructure configuration (single-row table)
package schemas

var InfraDDL = []string{
	`CREATE TABLE IF NOT EXISTS infra_config (
      id                    INTEGER PRIMARY KEY CHECK (id = 1),

      infra_mode            TEXT NOT NULL DEFAULT 'standalone'
                            CHECK (infra_mode IN ('standalone','vertical','horizontal')),

      db_type               TEXT NOT NULL DEFAULT 'sqlite'
                            CHECK (db_type IN ('sqlite','postgresql')),
      db_dsn                TEXT NOT NULL DEFAULT '',
      db_pool_min           INTEGER NOT NULL DEFAULT 2,
      db_pool_max           INTEGER NOT NULL DEFAULT 20,

      kv_store_type         TEXT NOT NULL DEFAULT 'dict'
                            CHECK (kv_store_type IN ('dict','redis')),
      redis_url             TEXT NOT NULL DEFAULT 'redis://localhost:6379/0',

      web_host              TEXT NOT NULL DEFAULT '0.0.0.0',
      web_port              INTEGER NOT NULL DEFAULT 5000,
      web_workers           INTEGER NOT NULL DEFAULT 1,

      amf_separate_process  INTEGER NOT NULL DEFAULT 0,
      amf_instance_id       INTEGER NOT NULL DEFAULT 1,

      upf_mode              TEXT NOT NULL DEFAULT 'socket'
                            CHECK (upf_mode IN ('socket','pmd')),
      -- SMF↔UPF is always PFCP/N4 (TS 29.244) via the integrated
      -- loopback socket; the per-interface selector + REST path
      -- + bridge-mode toggle were removed. upf_pfcp_port overrides
      -- the §6.1 default 8805 for the loopback listener.
      upf_pfcp_port         INTEGER NOT NULL DEFAULT 8805,

      upf_max_sessions      INTEGER NOT NULL DEFAULT 8192,
      upf_dpdk_mem_mb       INTEGER NOT NULL DEFAULT 256,
      upf_mbuf_pool_size    INTEGER NOT NULL DEFAULT 32768,
      upf_rx_ring_size      INTEGER NOT NULL DEFAULT 2048,
      upf_tx_ring_size      INTEGER NOT NULL DEFAULT 2048,
      hugepage_size         TEXT NOT NULL DEFAULT '2M' CHECK (hugepage_size IN ('2M','1G')),
      hugepage_count        INTEGER NOT NULL DEFAULT 512,
      dpdk_log_level        TEXT NOT NULL DEFAULT 'INFO' CHECK (dpdk_log_level IN ('ERROR','WARNING','INFO','DEBUG')),

      tun_txqueuelen        INTEGER NOT NULL DEFAULT 65536,
      gtpu_rcvbuf_mb        INTEGER NOT NULL DEFAULT 32,
      gtpu_sndbuf_mb        INTEGER NOT NULL DEFAULT 32,
      sysctl_netdev_backlog INTEGER NOT NULL DEFAULT 65536,

      pmd_n3_device         TEXT NOT NULL DEFAULT 'net_tap0',
      pmd_n6_device         TEXT NOT NULL DEFAULT 'net_tap1',

      cpu_pinning_enabled   INTEGER NOT NULL DEFAULT 0,
      cpu_upf_core          INTEGER NOT NULL DEFAULT -1,
      cpu_amf_core          INTEGER NOT NULL DEFAULT -1,
      cpu_web_core          INTEGER NOT NULL DEFAULT -1,
      cpu_worker_cores      TEXT NOT NULL DEFAULT '',
      cpu_irq_core          INTEGER NOT NULL DEFAULT -1,
      cpu_irq_nic           TEXT NOT NULL DEFAULT '',
      cpu_governor_perf     INTEGER NOT NULL DEFAULT 0,

      sched_upf_nice        INTEGER NOT NULL DEFAULT -10,
      sched_amf_nice        INTEGER NOT NULL DEFAULT  -5,
      sched_web_nice        INTEGER NOT NULL DEFAULT  10,
      sched_worker_nice     INTEGER NOT NULL DEFAULT   0,
      sched_upf_rt          INTEGER NOT NULL DEFAULT   0,

      log_sink              TEXT NOT NULL DEFAULT 'disk'
                            CHECK (log_sink IN ('disk','tmpfs','ram_only','journald','syslog')),
      log_async             INTEGER NOT NULL DEFAULT 1,
      log_flush_each        INTEGER NOT NULL DEFAULT 0,
      log_buffer_size       INTEGER NOT NULL DEFAULT 5000,

      nwdaf_collect_interval INTEGER NOT NULL DEFAULT 30,
      nwdaf_retention_hours  INTEGER NOT NULL DEFAULT 24,

      ids_enabled           INTEGER NOT NULL DEFAULT 1,
      rate_limiter_enabled  INTEGER NOT NULL DEFAULT 1,
      audit_log_enabled     INTEGER NOT NULL DEFAULT 1,
      audit_retention_days  INTEGER NOT NULL DEFAULT 90,

      ai_autonomous_enabled INTEGER NOT NULL DEFAULT 0,
      ai_energy_saving      INTEGER NOT NULL DEFAULT 0,
      ai_predictive_qos     INTEGER NOT NULL DEFAULT 0,
      ai_self_healing       INTEGER NOT NULL DEFAULT 0,
      ai_psm_optimizer      INTEGER NOT NULL DEFAULT 0,

      sctp_multi_home       INTEGER NOT NULL DEFAULT 0,
      sctp_secondary_ip     TEXT NOT NULL DEFAULT '',

      -- SCTP tuning (TS 38.412 §7 leaves values to implementation).
      -- Units are what the Linux kernel struct fields expect:
      -- RTO triplet in ms; HB interval in ms; retransmit counters
      -- are absolute counts.
      sctp_rto_initial_ms   INTEGER NOT NULL DEFAULT 3000,
      sctp_rto_max_ms       INTEGER NOT NULL DEFAULT 120000,
      sctp_rto_min_ms       INTEGER NOT NULL DEFAULT 1000,
      sctp_hb_interval_ms   INTEGER NOT NULL DEFAULT 30000,
      sctp_path_max_retrans INTEGER NOT NULL DEFAULT 5,
      sctp_assoc_max_retrans INTEGER NOT NULL DEFAULT 30,
      sctp_num_streams      INTEGER NOT NULL DEFAULT 16,

      lb_enabled            INTEGER NOT NULL DEFAULT 0,
      lb_instances          TEXT NOT NULL DEFAULT '',
      lb_health_interval    INTEGER NOT NULL DEFAULT 10,
      lb_strategy           TEXT NOT NULL DEFAULT 'least_conn' CHECK (lb_strategy IN ('round_robin','least_conn')),

      otel_enabled          INTEGER NOT NULL DEFAULT 0,
      otel_metrics_enabled  INTEGER NOT NULL DEFAULT 1,
      otel_traces_enabled   INTEGER NOT NULL DEFAULT 1,
      otel_logs_enabled     INTEGER NOT NULL DEFAULT 1,
      otel_exporter         TEXT NOT NULL DEFAULT 'prometheus' CHECK (otel_exporter IN ('prometheus','otlp','console')),
      otel_endpoint         TEXT NOT NULL DEFAULT '',
      otel_prometheus_port  INTEGER NOT NULL DEFAULT 9464,

      traffic_engine_url    TEXT NOT NULL DEFAULT 'http://localhost:9100',

      log_level             TEXT NOT NULL DEFAULT 'INFO' CHECK (log_level IN ('DEBUG','INFO','WARNING','ERROR')),
      log_file_enabled      INTEGER NOT NULL DEFAULT 1,
      log_file_path         TEXT NOT NULL DEFAULT '/var/log/sacore/sacore.log',

      config_schema_version INTEGER NOT NULL DEFAULT 2,

      updated_at            TEXT DEFAULT (datetime('now'))
    )`,

	`CREATE TABLE IF NOT EXISTS infra_config_history (
      id          INTEGER PRIMARY KEY AUTOINCREMENT,
      ts          TEXT NOT NULL DEFAULT (datetime('now')),
      note        TEXT NOT NULL DEFAULT '',
      config_json TEXT NOT NULL
    )`,
}
