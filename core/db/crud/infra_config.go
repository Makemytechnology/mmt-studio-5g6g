// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/infra_config.go — Infrastructure configuration singleton CRUD.
package crud

import (
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// InfraConfigFields is the allow-list of mutable columns on infra_config.
// Matches _FIELDS in crud/infra_config.py. Any UpdateInfraConfig key not in
// this set is silently dropped (matching Python behaviour).
var InfraConfigFields = map[string]struct{}{
	"infra_mode": {}, "db_type": {}, "db_dsn": {}, "db_pool_min": {}, "db_pool_max": {},
	"kv_store_type": {}, "redis_url": {},
	"web_host": {}, "web_port": {}, "web_workers": {},
	"amf_separate_process": {}, "amf_instance_id": {},
	"upf_mode": {}, "upf_pfcp_port": {},
	"upf_max_sessions": {}, "upf_dpdk_mem_mb": {}, "upf_mbuf_pool_size": {},
	"upf_rx_ring_size": {}, "upf_tx_ring_size": {},
	"hugepage_size": {}, "hugepage_count": {}, "dpdk_log_level": {},
	"tun_txqueuelen": {}, "gtpu_rcvbuf_mb": {}, "gtpu_sndbuf_mb": {},
	"sysctl_netdev_backlog": {},
	"pmd_n3_device": {}, "pmd_n6_device": {},
	"cpu_pinning_enabled": {},
	"cpu_upf_core": {}, "cpu_amf_core": {}, "cpu_web_core": {},
	"cpu_worker_cores": {}, "cpu_irq_core": {}, "cpu_irq_nic": {},
	"cpu_governor_perf": {},
	"sched_upf_nice": {}, "sched_amf_nice": {}, "sched_web_nice": {},
	"sched_worker_nice": {}, "sched_upf_rt": {},
	"log_sink": {}, "log_async": {}, "log_flush_each": {}, "log_buffer_size": {},
	"config_schema_version": {},
	"nwdaf_collect_interval": {}, "nwdaf_retention_hours": {},
	"ids_enabled": {}, "rate_limiter_enabled": {}, "audit_log_enabled": {}, "audit_retention_days": {},
	"ai_autonomous_enabled": {}, "ai_energy_saving": {}, "ai_predictive_qos": {},
	"ai_self_healing": {}, "ai_psm_optimizer": {},
	"sctp_multi_home": {}, "sctp_secondary_ip": {},
	"traffic_engine_url": {},
	"log_file_enabled": {}, "log_file_path": {},
	"lb_enabled": {}, "lb_instances": {}, "lb_health_interval": {}, "lb_strategy": {},
	"otel_enabled": {}, "otel_metrics_enabled": {}, "otel_traces_enabled": {}, "otel_logs_enabled": {},
	"otel_exporter": {}, "otel_endpoint": {}, "otel_prometheus_port": {},
	"log_level": {},
}

// ensureInfraRow guarantees the singleton row exists. Cheap — IGNOREs on conflict.
func ensureInfraRow() error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT OR IGNORE INTO infra_config (id) VALUES (1)`)
	return err
}

// GetInfraConfig returns the whole infra_config singleton as a map of
// column name → value (string / int64 / float64 / nil, whatever the driver
// hands back). Mirrors the Python dict(row).
func GetInfraConfig() (map[string]any, error) {
	if err := ensureInfraRow(); err != nil {
		return nil, err
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT * FROM infra_config WHERE id=1`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	if !rows.Next() {
		return map[string]any{}, rows.Err()
	}
	scan := make([]any, len(cols))
	ptrs := make([]any, len(cols))
	for i := range scan {
		ptrs[i] = &scan[i]
	}
	if err := rows.Scan(ptrs...); err != nil {
		return nil, err
	}
	out := make(map[string]any, len(cols))
	for i, name := range cols {
		out[name] = scan[i]
	}
	return out, nil
}

// UpdateInfraConfig patches the singleton. Unknown keys are dropped.
// Refreshes updated_at on every call. Returns the number of columns updated.
func UpdateInfraConfig(patch map[string]any) (int, error) {
	if err := ensureInfraRow(); err != nil {
		return 0, err
	}
	filtered := make(map[string]any, len(patch))
	for k, v := range patch {
		if _, ok := InfraConfigFields[k]; ok {
			filtered[k] = v
		}
	}
	if len(filtered) == 0 {
		return 0, nil
	}
	filtered["updated_at"] = float64(time.Now().UnixMilli()) / 1000.0

	// Build deterministic ordered SET clause to keep tests reproducible.
	keys := make([]string, 0, len(filtered))
	for k := range filtered {
		keys = append(keys, k)
	}
	// No sort: order is irrelevant for correctness and the Python
	// reference also uses dict iteration order.
	var set strings.Builder
	args := make([]any, 0, len(keys)+1)
	for i, k := range keys {
		if i > 0 {
			set.WriteString(", ")
		}
		fmt.Fprintf(&set, "%s=?", k)
		args = append(args, filtered[k])
	}
	args = append(args, 1)

	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	if _, err := db.Exec("UPDATE infra_config SET "+set.String()+" WHERE id=?", args...); err != nil {
		return 0, err
	}
	return len(keys) - 1, nil // -1 for updated_at
}
