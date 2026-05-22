// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_kpis.go — REST surface for the KPI Dashboard.
//
// /api/kpis serves the nested aggregator that templates/kpis.html
// consumes — one fetch per refresh tick. The shape is opinionated to
// match the panel exactly (amf / smf / upf / ip_pools / ims / mcx / fm
// / charging / services / timestamp). For raw TS 28.552 counter names
// + per-second rates the tester / SRE tooling uses /api/kpis/raw.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 28.552 §5.1 — AMF measurements (RM.* AUTH.* SEC.* MM.* NGAP.*).
//   - TS 28.552 §5.3 — SMF measurements (SM.*).
//   - TS 28.552 §5.4 — UPF measurements (N3.*).
//   - TS 28.554 §6   — End-to-end KPI formulae (Mean-of-Ratios success
//                      rate; surfaced as `*_success_rate` percentages).
//   - TS 23.501 §5.7.4 Table 5.7.4-1 — standardised 5QI → resource-type
//                      (GBR / Delay-Critical-GBR / Non-GBR) mapping
//                      consumed by isGBR() to split SMF.gbr_bearers /
//                      SMF.nongbr_bearers.
//   - TS 28.532       — Fault counts by perceived severity (oam/fm).
package app

import (
	"net/http"
	"strconv"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/amf"
	"github.com/mmt/mmt-studio-core/nf/smf/ipalloc"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	upfMgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/fm"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

func (s *Server) registerKPIsRoutes() {
	r := s.Router

	// ── Aggregated KPI dashboard (nested-by-NF shape) ─────────────
	r.Get("/api/kpis", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, buildKPIDashboard())
	})

	// ── Raw TS 28.552 counters + rates (tester / SRE) ─────────────
	r.Get("/api/kpis/raw", func(w http.ResponseWriter, rq *http.Request) {
		window := 5 * time.Second
		if v := rq.URL.Query().Get("window_sec"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				window = time.Duration(n) * time.Second
			}
		}
		all := pm.Default.All()
		rates := make(map[string]float64, len(all))
		peaks := make(map[string]float64, len(all))
		for k := range all {
			rates[k] = pm.Default.Rate(k, window)
			peaks[k] = pm.Default.PeakRate(k)
		}
		jsonReply(w, map[string]any{
			"counters":          all,
			"rates_per_sec":     rates,
			"peak_rates":        peaks,
			"reg_success_rate":  pm.Default.SuccessRate(pm.RegSucc, pm.RegFail),
			"auth_success_rate": pm.Default.SuccessRate(pm.AuthSucc, pm.AuthFail),
			"sm_success_rate":   pm.Default.SuccessRate(pm.SMSessSucc, pm.SMSessFail),
			"window_sec":        window.Seconds(),
		})
	})

	// ── Reset peak rates (UI button) ──────────────────────────────
	r.Post("/api/kpis/reset-peaks", func(w http.ResponseWriter, _ *http.Request) {
		pm.Default.ResetPeaks()
		jsonReply(w, map[string]bool{"ok": true})
	})
}

// buildKPIDashboard returns the nested shape templates/kpis.html expects.
// All sub-sections are independent so a partial outage in one source
// (e.g. UPF dataplane down) leaves the rest populated.
func buildKPIDashboard() map[string]any {
	return map[string]any{
		"amf":       buildAMFSection(),
		"smf":       buildSMFSection(),
		"upf":       buildUPFSection(),
		"ip_pools":  buildIPPoolsSection(),
		"ims":       buildIMSSection(),
		"mcx":       buildMCXSection(),
		"fm":        fm.Default.Counts(),
		"charging":  buildChargingSection(),
		"services":  buildServicesSection(),
		"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
	}
}

// ── AMF: TS 28.552 §5.1 ─────────────────────────────────────────────────

func buildAMFSection() map[string]any {
	ues := amf.UEs(nil)
	gnbs := amf.Gnbs(nil)

	registered, connected, idle := 0, 0, 0
	gnbUE := map[string]int{}
	for _, ue := range ues {
		// RM-state count (TS 23.501 §5.3.2): only REGISTERED rows are
		// surfaced as "registered_ue"; the rest are deregistered/in-flight.
		if ue.RM == "REGISTERED" {
			registered++
		}
		switch ue.CM {
		case "CONNECTED":
			connected++
		case "IDLE":
			idle++
		}
		if ue.GnbKey != "" {
			gnbUE[ue.GnbKey]++
		}
	}

	gnbDist := make([]map[string]any, 0, len(gnbs))
	connectedGnbs := 0
	for _, g := range gnbs {
		if g.Connected {
			connectedGnbs++
		}
		name := g.Name
		if name == "" {
			name = g.IP
		}
		gnbDist = append(gnbDist, map[string]any{
			"name":      name,
			"ue_count":  gnbUE[g.IP],
			"connected": g.Connected,
		})
	}

	return map[string]any{
		"registered_ue":         registered,
		"connected_ue":          connected,
		"idle_ue":               idle,
		"total_ue_contexts":     len(ues),
		"gnb_count":             connectedGnbs,
		"gnb_total":             len(gnbs),
		"gnb_distribution":      gnbDist,
		"auth_completed":        pm.Default.Get(pm.AuthSucc),
		"security_established":  pm.Default.Get(pm.SecSucc),
		"reg_attempts":          pm.Default.Get(pm.RegAtt),
		"reg_successes":         pm.Default.Get(pm.RegSucc),
		"reg_failures":          pm.Default.Get(pm.RegFail),
		"reg_success_rate":      roundPct(pm.Default.SuccessRate(pm.RegSucc, pm.RegFail)),
		"auth_attempts":         pm.Default.Get(pm.AuthAtt),
		"auth_successes":        pm.Default.Get(pm.AuthSucc),
		"auth_failures":         pm.Default.Get(pm.AuthFail),
		"auth_success_rate":     roundPct(pm.Default.SuccessRate(pm.AuthSucc, pm.AuthFail)),
		"ngap_setup_attempts":   pm.Default.Get(pm.NGAPSetupAtt),
		"ngap_setup_successes":  pm.Default.Get(pm.NGAPSetupSucc),
		"ngap_setup_failures":   pm.Default.Get(pm.NGAPSetupFail),
	}
}

// ── SMF: TS 28.552 §5.3 ─────────────────────────────────────────────────

func buildSMFSection() map[string]any {
	all := session.Default.All()

	active := 0
	gbr, nongbr := 0, 0
	perDNN := map[string]int{}
	perSlice := map[string]int{}

	for _, sess := range all {
		if sess.State == session.StateActive {
			active++
		}
		// One default QoS flow per session today (5QI = sess.FiveQI). When
		// multi-flow lands, switch to per-flow walk; the bearer total then
		// equals sum of flow counts.
		if isGBR(sess.FiveQI) {
			gbr++
		} else {
			nongbr++
		}
		if sess.DNN != "" {
			perDNN[sess.DNN]++
		}
		// S-NSSAI key is "SST" or "SST-SD" (uppercase hex SD per TS 23.003 §28.4.2).
		key := strconv.Itoa(int(sess.SST))
		if sess.SD != "" {
			key += "-" + sess.SD
		}
		perSlice[key]++
	}

	return map[string]any{
		"total_pdu_sessions":   len(all),
		"active_pdu_sessions":  active,
		"total_bearers":        len(all), // 1 default flow per session
		"gbr_bearers":          gbr,
		"nongbr_bearers":       nongbr,
		"sess_attempts":        pm.Default.Get(pm.SMSessAtt),
		"sess_successes":       pm.Default.Get(pm.SMSessSucc),
		"sess_failures":        pm.Default.Get(pm.SMSessFail),
		"sess_success_rate":    roundPct(pm.Default.SuccessRate(pm.SMSessSucc, pm.SMSessFail)),
		"flow_attempts":        pm.Default.Get(pm.SMFlowAtt),
		"flow_successes":       pm.Default.Get(pm.SMFlowSucc),
		"flow_failures":        pm.Default.Get(pm.SMFlowFail),
		"pdu_per_dnn":          perDNN,
		"pdu_per_slice":        perSlice,
	}
}

// isGBR reports whether a standardised 5QI value (TS 23.501 §5.7.4
// Table 5.7.4-1) is a GBR or Delay-Critical-GBR resource type. Operator
// 5QIs (96-127, 248-255) default to non-GBR — re-classify by the
// `services.resource_type` column if the operator profile is loaded.
func isGBR(fiveQI uint8) bool {
	switch fiveQI {
	// Standard GBR — Table 5.7.4-1 rows
	case 1, 2, 3, 4, 65, 66, 67, 75:
		return true
	// Delay-Critical GBR — Table 5.7.4-1 rows
	case 82, 83, 84, 85, 86, 87, 88, 89, 90:
		return true
	}
	return false
}

// ── UPF: TS 28.552 §5.4 ─────────────────────────────────────────────────

func buildUPFSection() map[string]any {
	io := upfMgr.Default.GetIOStats()
	totalPkts := io.ULPkts + io.DLPkts
	totalDropped := io.ULDropped + io.DLDropped
	loss := 0.0
	if totalPkts+totalDropped > 0 {
		loss = float64(totalDropped) / float64(totalPkts+totalDropped) * 100.0
	}
	return map[string]any{
		"sessions":          upfMgr.Default.SessionCount(),
		"running":           upfMgr.Default.Running(),
		"ul_pkts":           io.ULPkts,
		"dl_pkts":           io.DLPkts,
		"ul_bytes":          io.ULBytes,
		"dl_bytes":          io.DLBytes,
		"ul_dropped":        io.ULDropped,
		"dl_dropped":        io.DLDropped,
		"ul_metered":        io.ULMetered,
		"dl_metered":        io.DLMetered,
		"gtpu_errors":       io.GTPUErrors,
		"packet_loss_rate":  roundPct(loss),
	}
}

// ── IP pools (per-DNN allocation vs capacity) ────────────────────────────

func buildIPPoolsSection() []map[string]any {
	usage := ipalloc.Default.UsageDetail() // {"<dnn>_v<4|6>": PoolDetail{count, addresses}}
	totals := ipPoolCapacities()           // {"<dnn>_v<4|6>": total_hosts}

	// Walk the union of keys so a fresh pool with 0 allocations still
	// shows up (otherwise operator can't see capacity).
	keys := map[string]struct{}{}
	for k := range usage {
		keys[k] = struct{}{}
	}
	for k := range totals {
		keys[k] = struct{}{}
	}

	out := make([]map[string]any, 0, len(keys))
	for k := range keys {
		alloc := usage[k].Count
		total := totals[k]
		pct := 0.0
		if total > 0 {
			pct = float64(alloc) / float64(total) * 100.0
		}
		out = append(out, map[string]any{
			"dnn":             k, // "<dnn>_v<4|6>"
			"allocated":       alloc,
			"total":           total,
			"utilization_pct": roundPct(pct),
		})
	}
	return out
}

// ipPoolCapacities computes assignable-host counts per (dnn, version)
// from apn_config + apn_ip_pools, mirroring ipalloc.buildHosts (network
// + broadcast + gateway are not assignable).
func ipPoolCapacities() map[string]int {
	out := map[string]int{}
	db, err := engine.Open()
	if err != nil {
		return out
	}
	rows, err := db.Query(`SELECT a.name, p.cidr, COALESCE(p.ip_version, 4)
		FROM apn_config a JOIN apn_ip_pools p ON p.apn_id = a.id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var dnn, cidr string
		var ver int
		if rows.Scan(&dnn, &cidr, &ver) != nil {
			continue
		}
		hosts := assignableHostCount(cidr, ver)
		key := dnn + "_v" + strconv.Itoa(ver)
		out[key] += hosts
	}
	return out
}

// assignableHostCount is a cheap counter — full host-list expansion
// happens in ipalloc; here we just need a number so we can compute
// utilisation %. v4: 2^(32-prefixlen) − 3 (network, broadcast, gw).
// v6: 2^(128-prefixlen) − 2 (network, gw); capped at 2^20 because the
// allocator caps expansion at 1M entries too.
func assignableHostCount(cidr string, ver int) int {
	for i, c := range cidr {
		if c == '/' {
			pfx, err := strconv.Atoi(cidr[i+1:])
			if err != nil {
				return 0
			}
			max := 32
			if ver == 6 {
				max = 128
			}
			bits := max - pfx
			if bits < 0 {
				return 0
			}
			if bits > 20 {
				bits = 20 // matches expandAll cap of 1<<20 in ipalloc
			}
			total := 1 << bits
			if ver == 4 {
				total -= 3 // network + broadcast + gateway
			} else {
				total -= 2 // network + gateway
			}
			if total < 0 {
				return 0
			}
			return total
		}
	}
	return 0
}

// ── IMS (count via DB; cscf/registration store is in-process & ephemeral) ──

func buildIMSSection() map[string]any {
	db, err := engine.Open()
	if err != nil {
		return map[string]any{
			"total_subscribers":    0,
			"active_registrations": 0,
			"active_calls":         0,
			"calls_by_state":       map[string]int{},
		}
	}

	var subs int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ims_subscribers`).Scan(&subs)

	// Registrations: ims_dialogs rows whose call_id starts with "reg-"
	// don't exist as a thing; CSCF stores registrations in-process
	// (services/ims/cscf/registration.go) so we report zero rather than
	// invent a wrong number. When a persistent registration table lands
	// we'll JOIN it here.
	regs := 0

	// Active calls + per-state breakdown from ims_dialogs.
	var calls int
	_ = db.QueryRow(`SELECT COUNT(*) FROM ims_dialogs WHERE state != 'TERMINATED'`).Scan(&calls)

	byState := map[string]int{}
	rows, err := db.Query(`SELECT state, COUNT(*) FROM ims_dialogs
		WHERE state != 'TERMINATED' GROUP BY state`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var st string
			var n int
			if rows.Scan(&st, &n) == nil {
				byState[st] = n
			}
		}
	}

	return map[string]any{
		"total_subscribers":    subs,
		"active_registrations": regs,
		"active_calls":         calls,
		"calls_by_state":       byState,
	}
}

// ── MCX (mcptt + mcvideo + mcdata) ─────────────────────────────────────

func buildMCXSection() map[string]any {
	zero := func() map[string]any {
		return map[string]any{
			"total_users":    0,
			"total_groups":   0,
			"active_calls":   0,
			"total_messages": 0,
			"floor_grants":   0,
			"calls_by_type":  map[string]int{},
		}
	}
	db, err := engine.Open()
	if err != nil {
		return zero()
	}

	var users, groups, calls, msgs, grants int
	_ = db.QueryRow(`SELECT COUNT(*) FROM mcx_user_profiles WHERE enabled=1`).Scan(&users)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mcx_groups WHERE enabled=1`).Scan(&groups)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mcx_active_calls WHERE state='active'`).Scan(&calls)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mcx_messages`).Scan(&msgs)
	_ = db.QueryRow(`SELECT COUNT(*) FROM mcx_floor_history WHERE event='granted'`).Scan(&grants)

	byType := map[string]int{}
	rows, err := db.Query(`SELECT call_type, COUNT(*) FROM mcx_active_calls
		WHERE state='active' GROUP BY call_type`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t string
			var n int
			if rows.Scan(&t, &n) == nil {
				byType[t] = n
			}
		}
	}

	return map[string]any{
		"total_users":    users,
		"total_groups":   groups,
		"active_calls":   calls,
		"total_messages": msgs,
		"floor_grants":   grants,
		"calls_by_type":  byType,
	}
}

// ── Charging (TS 32.290 / 32.291) ──────────────────────────────────────

func buildChargingSection() map[string]any {
	out := map[string]any{
		"total_profiles":  0,
		"online":          0,
		"offline":         0,
		"linked_services": 0,
	}
	db, err := engine.Open()
	if err != nil {
		return out
	}
	var total, online, offline, linked int
	_ = db.QueryRow(`SELECT COUNT(*) FROM charging_profiles`).Scan(&total)
	_ = db.QueryRow(`SELECT COUNT(*) FROM charging_profiles WHERE charging_method='online'`).Scan(&online)
	_ = db.QueryRow(`SELECT COUNT(*) FROM charging_profiles WHERE charging_method='offline'`).Scan(&offline)
	_ = db.QueryRow(`SELECT COUNT(*) FROM services WHERE charging_profile IS NOT NULL`).Scan(&linked)
	out["total_profiles"] = total
	out["online"] = online
	out["offline"] = offline
	out["linked_services"] = linked
	return out
}

// ── Services catalogue (TS 23.501 §5.7.4) ──────────────────────────────

func buildServicesSection() map[string]any {
	out := map[string]any{
		"total":  0,
		"by_5qi": map[string]int{},
	}
	db, err := engine.Open()
	if err != nil {
		return out
	}
	var total int
	_ = db.QueryRow(`SELECT COUNT(*) FROM services`).Scan(&total)
	by5QI := map[string]int{}
	rows, err := db.Query(`SELECT fiveqi, COUNT(*) FROM services GROUP BY fiveqi`)
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var qi, n int
			if rows.Scan(&qi, &n) == nil {
				by5QI[strconv.Itoa(qi)] = n
			}
		}
	}
	out["total"] = total
	out["by_5qi"] = by5QI
	return out
}

// roundPct truncates a percentage to one decimal place (the panel's
// display precision) so the JSON stays small and stable across polls.
// Returns -1 unchanged (the "no attempts yet" sentinel from
// SuccessRate / packet-loss not-applicable).
func roundPct(v float64) float64 {
	if v < 0 {
		return v
	}
	return float64(int(v*10+0.5)) / 10.0
}
