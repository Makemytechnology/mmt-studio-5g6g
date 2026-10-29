// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/services.go — Standardized 5QI QoS profiles (TS 23.501 Table 5.7.4-1).
package seed

import "database/sql"

// qosProfile mirrors one row of the catalog.
type qosProfile struct {
	Name           string
	FiveQI         int
	ResourceType   string // GBR | NonGBR
	ArpPri         int
	ArpPcap        int
	ArpPvuln       int
	GBRULKbps      *int
	GBRDLKbps      *int
	MBRULKbps      *int
	MBRDLKbps      *int
	Status         string
}

// StandardQoSProfiles is the canonical list from TS 23.501 §5.7.4.
// Event-gated services start INACTIVE so the operator can enable them per UE.
var StandardQoSProfiles = []qosProfile{
	// GBR (Conversational / Streaming)
	{"conv_voice", 1, "GBR", 1, 1, 0, ip(64), ip(64), ip(128), ip(128), "INACTIVE (event gated)"},
	{"conv_video", 2, "GBR", 2, 1, 0, ip(1000), ip(1000), ip(4000), ip(4000), "INACTIVE (event gated)"},
	{"realtime_gaming", 3, "GBR", 3, 0, 1, ip(500), ip(500), ip(2000), ip(2000), "INACTIVE (event gated)"},
	{"non_conv_video", 4, "GBR", 5, 0, 1, ip(500), ip(500), ip(4000), ip(4000), "INACTIVE (event gated)"},

	// NonGBR (Interactive / Background)
	{"ims_signalling", 5, "NonGBR", 1, 1, 0, nil, nil, nil, nil, "ACTIVE"},
	{"tcp_interactive", 6, "NonGBR", 6, 0, 1, nil, nil, nil, nil, "ACTIVE"},
	{"voice_video_gaming", 7, "NonGBR", 7, 0, 1, nil, nil, nil, nil, "ACTIVE"},
	{"video_streaming", 8, "NonGBR", 8, 0, 1, nil, nil, nil, nil, "ACTIVE"},
	{"default_data", 9, "NonGBR", 9, 0, 1, nil, nil, nil, nil, "ACTIVE"},

	// MCX (Mission Critical — TS 23.280)
	{"mcptt_voice", 65, "GBR", 1, 1, 0, ip(24), ip(24), ip(64), ip(64), "INACTIVE (event gated)"},
	{"mcvideo", 67, "GBR", 2, 1, 0, ip(500), ip(500), ip(2000), ip(2000), "INACTIVE (event gated)"},
	{"mcdata", 69, "NonGBR", 3, 1, 0, nil, nil, nil, nil, "INACTIVE (event gated)"},
	{"mcx_signalling", 70, "NonGBR", 1, 1, 0, nil, nil, nil, nil, "INACTIVE (event gated)"},

	// URLLC (Ultra-Reliable Low Latency — TS 23.501 §5.7.4)
	{"urllc_discrete_auto", 80, "GBR", 1, 1, 0, ip(100), ip(100), ip(1000), ip(1000), "INACTIVE (event gated)"},
	{"urllc_discrete_auto_lo", 82, "GBR", 1, 1, 0, ip(100), ip(100), ip(1000), ip(1000), "INACTIVE (event gated)"},
	{"urllc_electricity", 83, "GBR", 2, 1, 0, ip(50), ip(50), ip(500), ip(500), "INACTIVE (event gated)"},
	{"urllc_process_auto", 84, "GBR", 3, 1, 0, ip(100), ip(100), ip(1000), ip(1000), "INACTIVE (event gated)"},
	{"urllc_process_mon", 85, "GBR", 4, 0, 1, ip(50), ip(50), ip(500), ip(500), "INACTIVE (event gated)"},
}

// SeedServices inserts the standardized QoS profiles. Idempotent.
func SeedServices(db *sql.DB) error {
	for _, p := range StandardQoSProfiles {
		if _, err := db.Exec(`
            INSERT OR IGNORE INTO services
              (name, fiveqi, resource_type, arp_priority, arp_pcap, arp_pvuln,
               gbr_ul_kbps, gbr_dl_kbps, mbr_ul_kbps, mbr_dl_kbps, flow_json,
               charging_profile, status)
            VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, '[]', NULL, ?)`,
			p.Name, p.FiveQI, p.ResourceType, p.ArpPri, p.ArpPcap, p.ArpPvuln,
			ipArg(p.GBRULKbps), ipArg(p.GBRDLKbps),
			ipArg(p.MBRULKbps), ipArg(p.MBRDLKbps),
			p.Status); err != nil {
			return err
		}
	}
	// Force event-gated services back to INACTIVE on fresh DB.
	_, _ = db.Exec(`
        UPDATE services SET status = 'INACTIVE (event gated)'
        WHERE name IN ('conv_voice', 'conv_video', 'realtime_gaming', 'non_conv_video',
                       'mcptt_voice', 'mcvideo', 'mcdata', 'mcx_signalling',
                       'urllc_discrete_auto', 'urllc_discrete_auto_lo', 'urllc_electricity',
                       'urllc_process_auto', 'urllc_process_mon')`)
	return nil
}

func ip(v int) *int { return &v }

func ipArg(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
