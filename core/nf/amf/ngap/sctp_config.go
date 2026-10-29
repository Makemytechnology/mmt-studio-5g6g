// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
//go:build linux

package ngap

import (
	"github.com/mmt/mmt-studio-core/db/crud"
)

// sctpConfig carries operator-tunable SCTP values from infra_config.
// Missing or zero fields mean "use the built-in default"; transport_linux.go
// nonZero-guards each value before wiring it into setsockopt.
type sctpConfig struct {
	RTOInitialMs    uint32
	RTOMaxMs        uint32
	RTOMinMs        uint32
	HBIntervalMs    uint32
	PathMaxRetrans  uint32
	AssocMaxRetrans uint32
	NumStreams      uint32
}

// loadSCTPConfig reads the SCTP tuning fields from infra_config. The
// table is the single singleton row (id=1); errors are swallowed so
// start-up doesn't fail when the DB isn't reachable yet — the code
// just runs on hardcoded defaults.
func loadSCTPConfig() sctpConfig {
	cfg, err := crud.GetInfraConfig()
	if err != nil {
		return sctpConfig{}
	}
	return sctpConfig{
		RTOInitialMs:    u32(cfg["sctp_rto_initial_ms"]),
		RTOMaxMs:        u32(cfg["sctp_rto_max_ms"]),
		RTOMinMs:        u32(cfg["sctp_rto_min_ms"]),
		HBIntervalMs:    u32(cfg["sctp_hb_interval_ms"]),
		PathMaxRetrans:  u32(cfg["sctp_path_max_retrans"]),
		AssocMaxRetrans: u32(cfg["sctp_assoc_max_retrans"]),
		NumStreams:      u32(cfg["sctp_num_streams"]),
	}
}

func u32(v any) uint32 {
	switch x := v.(type) {
	case int64:
		if x < 0 {
			return 0
		}
		return uint32(x)
	case float64:
		if x < 0 {
			return 0
		}
		return uint32(x)
	}
	return 0
}

func nonZero(v, fallback uint32) uint32 {
	if v == 0 {
		return fallback
	}
	return v
}
