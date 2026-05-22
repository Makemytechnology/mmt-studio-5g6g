// Package analytics — NWDAF analytics computation engine.
//
// Each Analytics ID below maps to a §6.x Output-Analytics clause in
// TS 23.288. Where a clause exists in the loaded R19 PDF the cite is
// strict (§-form, speccheck-grounded); where the clause has been
// renumbered or is purely Stage 3, a TODO(spec:) prose form is used.
//
// Supported Analytics IDs:
//
//   - TS 23.288 §6.5     NF load analytics                  (NF_LOAD)
//   - TS 23.288 §6.7.2   UE mobility analytics              (UE_MOBILITY)
//   - TS 23.288 §6.7.3   UE Communication Analytics         (UE_COMMUNICATION)
//   - TS 23.288 §6.9     QoS Sustainability Analytics       (QOS_SUSTAINABILITY)
//   - TS 23.288 §6.7.5   Abnormal behaviour analytics       (ABNORMAL_BEHAVIOUR)
//   - TS 23.288 §6.3     Slice load level analytics         (SLICE_LOAD)
//
// TODO(spec: TS 23.288 §6.4, "Observed Service Experience") — close
// neighbour to the PDU_SESSION ID we expose; we map PDU_SESSION onto
// computeUECommunication() until a proper §6.4 implementation exists.
package analytics

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

// Analytics ID constants per TS 23.288 §6.1 (Procedures for analytics
// exposure) — names match the Stage 2 catalogue.
const (
	AnalyticsNFLoad            = "NF_LOAD"
	AnalyticsUEMobility        = "UE_MOBILITY"
	AnalyticsUECommunication   = "UE_COMMUNICATION"
	AnalyticsQoSSustainability = "QOS_SUSTAINABILITY"
	AnalyticsAbnormalBehaviour = "ABNORMAL_BEHAVIOUR"
	AnalyticsPDUSession        = "PDU_SESSION"
	AnalyticsSliceLoad         = "SLICE_LOAD"
)

// ValidAnalyticsIDs is the set of supported analytics IDs.
var ValidAnalyticsIDs = map[string]bool{
	AnalyticsNFLoad:            true,
	AnalyticsUEMobility:        true,
	AnalyticsUECommunication:   true,
	AnalyticsQoSSustainability: true,
	AnalyticsAbnormalBehaviour: true,
	AnalyticsPDUSession:        true,
	AnalyticsSliceLoad:         true,
}

// DataPoint is a collected data point from an NF.
type DataPoint struct {
	SourceNF    string  `json:"source_nf"`
	AnalyticsID string  `json:"analytics_id"`
	IMSI        string  `json:"imsi,omitempty"`
	DNN         string  `json:"dnn,omitempty"`
	DataJSON    string  `json:"data_json"`
	CollectedAt float64 `json:"collected_at"`
}

// AnalyticsResult holds the output of a compute_analytics call.
type AnalyticsResult struct {
	AnalyticsID    string         `json:"analytics_id"`
	Result         map[string]any `json:"result"`
	Confidence     float64        `json:"confidence"`
	Message        string         `json:"message,omitempty"`
	ComputedAt     float64        `json:"computed_at,omitempty"`
	DataPointsUsed int            `json:"data_points_used,omitempty"`
	TimeWindowSec  int            `json:"time_window_sec,omitempty"`
}

// ComputeAnalytics computes analytics for a given Analytics ID.
//
// Spec anchor: TS 23.288 §6.1.3 (Contents of Analytics Exposure) —
// output includes statistics and/or predictions, with a confidence
// score per prediction.
func ComputeAnalytics(analyticsID string, dataPoints []DataPoint, timeWindow int) AnalyticsResult {
	if timeWindow <= 0 {
		timeWindow = 300
	}
	now := float64(time.Now().Unix())
	cutoff := now - float64(timeWindow)

	var recent []DataPoint
	for _, dp := range dataPoints {
		if dp.CollectedAt > cutoff {
			recent = append(recent, dp)
		}
	}

	if len(recent) == 0 {
		return AnalyticsResult{
			AnalyticsID: analyticsID,
			Result:      map[string]any{},
			Confidence:  0.0,
			Message:     "No data in time window",
		}
	}

	type computeFn func([]DataPoint) AnalyticsResult

	dispatch := map[string]computeFn{
		AnalyticsNFLoad:            computeNFLoad,
		AnalyticsUEMobility:        computeUEMobility,
		AnalyticsUECommunication:   computeUECommunication,
		AnalyticsQoSSustainability: computeQoSSustainability,
		AnalyticsAbnormalBehaviour: computeAbnormalBehaviour,
		AnalyticsPDUSession:        computePDUSession,
		AnalyticsSliceLoad:         computeSliceLoad,
	}

	fn, ok := dispatch[analyticsID]
	if !ok {
		return AnalyticsResult{
			AnalyticsID: analyticsID,
			Result:      map[string]any{},
			Confidence:  0.0,
			Message:     fmt.Sprintf("Unknown analytics ID: %s", analyticsID),
		}
	}

	result := fn(recent)
	result.AnalyticsID = analyticsID
	result.ComputedAt = now
	result.DataPointsUsed = len(recent)
	result.TimeWindowSec = timeWindow
	return result
}

// parseDataJSON parses the data_json field of a DataPoint.
func parseDataJSON(dp DataPoint) map[string]any {
	var data map[string]any
	if err := json.Unmarshal([]byte(dp.DataJSON), &data); err != nil {
		return map[string]any{}
	}
	return data
}

// mean computes the arithmetic mean of a float64 slice.
func mean(vals []float64) float64 {
	if len(vals) == 0 {
		return 0
	}
	sum := 0.0
	for _, v := range vals {
		sum += v
	}
	return sum / float64(len(vals))
}

// computeNFLoad — NF Load analytics. Spec anchor: TS 23.288 §6.5.
func computeNFLoad(dataPoints []DataPoint) AnalyticsResult {
	type counterSnapshot struct {
		ts       float64
		counters map[string]any
	}
	var series []counterSnapshot
	for _, dp := range dataPoints {
		data := parseDataJSON(dp)
		if pm, ok := data["pm_counters"]; ok {
			if pmMap, ok := pm.(map[string]any); ok {
				series = append(series, counterSnapshot{ts: dp.CollectedAt, counters: pmMap})
			}
		}
	}

	if len(series) == 0 {
		return AnalyticsResult{Result: map[string]any{"load_level": "unknown"}, Confidence: 0.3}
	}

	getFloat := func(m map[string]any, key string) float64 {
		if v, ok := m[key]; ok {
			switch n := v.(type) {
			case float64:
				return n
			case int:
				return float64(n)
			case json.Number:
				f, _ := n.Float64()
				return f
			}
		}
		return 0
	}

	var regAttempts, sessAttempts []float64
	for _, cs := range series {
		regAttempts = append(regAttempts, getFloat(cs.counters, "RM.RegAtt"))
		sessAttempts = append(sessAttempts, getFloat(cs.counters, "SM.SessAtt"))
	}

	// Rate = delta between consecutive snapshots
	var regRates, sessRates []float64
	for i := 1; i < len(regAttempts); i++ {
		if regAttempts[i] >= regAttempts[i-1] {
			regRates = append(regRates, regAttempts[i]-regAttempts[i-1])
		}
	}
	for i := 1; i < len(sessAttempts); i++ {
		if sessAttempts[i] >= sessAttempts[i-1] {
			sessRates = append(sessRates, sessAttempts[i]-sessAttempts[i-1])
		}
	}

	avgRegRate := mean(regRates)
	avgSessRate := mean(sessRates)

	totalRate := avgRegRate + avgSessRate
	var loadLevel string
	switch {
	case totalRate > 50:
		loadLevel = "high"
	case totalRate > 10:
		loadLevel = "medium"
	default:
		loadLevel = "low"
	}

	trend := "stable"
	if len(regRates) >= 3 {
		half := len(regRates) / 2
		firstHalf := mean(regRates[:half])
		secondHalf := mean(regRates[half:])
		if secondHalf > firstHalf*1.2 {
			trend = "increasing"
		} else if secondHalf < firstHalf*0.8 {
			trend = "decreasing"
		}
	}

	latestCounters := map[string]any{}
	if len(series) > 0 {
		latestCounters = series[len(series)-1].counters
	}

	confidence := 0.5 + float64(len(series))*0.05
	if confidence > 0.95 {
		confidence = 0.95
	}

	return AnalyticsResult{
		Result: map[string]any{
			"load_level":            loadLevel,
			"trend":                 trend,
			"avg_registration_rate": math.Round(avgRegRate*100) / 100,
			"avg_session_rate":      math.Round(avgSessRate*100) / 100,
			"latest_counters":       latestCounters,
		},
		Confidence: confidence,
	}
}

// computeUEMobility — UE Mobility analytics. Spec anchor: TS 23.288 §6.7.2.
func computeUEMobility(dataPoints []DataPoint) AnalyticsResult {
	type ueSnapshot struct {
		ts         float64
		total      int
		registered int
		connected  int
	}
	var counts []ueSnapshot
	for _, dp := range dataPoints {
		data := parseDataJSON(dp)
		if _, ok := data["total_ues"]; ok {
			total := int(toFloat(data["total_ues"]))
			registered := int(toFloat(data["registered"]))
			connected := int(toFloat(data["connected"]))
			counts = append(counts, ueSnapshot{dp.CollectedAt, total, registered, connected})
		}
	}

	if len(counts) == 0 {
		return AnalyticsResult{Result: map[string]any{}, Confidence: 0.2}
	}

	latest := counts[len(counts)-1]
	var totals []float64
	peak := 0
	for _, u := range counts {
		totals = append(totals, float64(u.total))
		if u.total > peak {
			peak = u.total
		}
	}

	confidence := 0.5 + float64(len(counts))*0.05
	if confidence > 0.9 {
		confidence = 0.9
	}

	return AnalyticsResult{
		Result: map[string]any{
			"current_ues":        latest.total,
			"current_registered": latest.registered,
			"current_connected":  latest.connected,
			"avg_ues":            math.Round(mean(totals)*10) / 10,
			"peak_ues":           peak,
			"samples":            len(counts),
		},
		Confidence: confidence,
	}
}

// computeUECommunication — UE Communication analytics.
// Spec anchor: TS 23.288 §6.7.3 (UE Communication Analytics).
func computeUECommunication(dataPoints []DataPoint) AnalyticsResult {
	var sessionData []map[string]any
	for _, dp := range dataPoints {
		data := parseDataJSON(dp)
		if _, ok := data["total_sessions"]; ok {
			sessionData = append(sessionData, data)
		}
	}

	if len(sessionData) == 0 {
		return AnalyticsResult{Result: map[string]any{}, Confidence: 0.2}
	}

	latest := sessionData[len(sessionData)-1]
	result := map[string]any{
		"total_active_sessions": latest["total_sessions"],
		"sessions_by_dnn":      latest["sessions_by_dnn"],
		"ip_pool_usage":        latest["ip_pool_usage"],
	}
	return AnalyticsResult{Result: result, Confidence: 0.8}
}

// computeQoSSustainability — QoS Sustainability analytics.
// Spec anchor: TS 23.288 §6.9.
func computeQoSSustainability(dataPoints []DataPoint) AnalyticsResult {
	var upfStats []map[string]any
	for _, dp := range dataPoints {
		if dp.SourceNF == "UPF" {
			data := parseDataJSON(dp)
			if _, ok := data["rx_pkts"]; ok {
				upfStats = append(upfStats, data)
			}
		}
	}

	if len(upfStats) == 0 {
		return AnalyticsResult{
			Result:     map[string]any{"qos_status": "no_data"},
			Confidence: 0.1,
		}
	}

	latest := upfStats[len(upfStats)-1]
	rxPkts := toFloat(latest["rx_pkts"])
	txPkts := toFloat(latest["tx_pkts"])
	dropped := toFloat(latest["dropped"])
	totalPkts := rxPkts + txPkts
	denom := totalPkts
	if denom < 1 {
		denom = 1
	}
	dropRate := dropped / denom

	qosStatus := "sustainable"
	if dropRate > 0.05 {
		qosStatus = "degraded"
	} else if dropRate > 0.01 {
		qosStatus = "at_risk"
	}

	rxBytes := toFloat(latest["rx_bytes"])
	txBytes := toFloat(latest["tx_bytes"])

	confidence := 0.6 + float64(len(upfStats))*0.05
	if confidence > 0.9 {
		confidence = 0.9
	}

	return AnalyticsResult{
		Result: map[string]any{
			"qos_status":       qosStatus,
			"drop_rate":        math.Round(dropRate*1e6) / 1e6,
			"total_packets":    int64(totalPkts),
			"dropped_packets":  int64(dropped),
			"throughput_bytes": int64(rxBytes + txBytes),
		},
		Confidence: confidence,
	}
}

// computeAbnormalBehaviour — Abnormal behaviour related network data
// analytics. Spec anchor: TS 23.288 §6.7.5.
func computeAbnormalBehaviour(dataPoints []DataPoint) AnalyticsResult {
	var alerts []map[string]any

	for _, dp := range dataPoints {
		data := parseDataJSON(dp)
		counters, _ := data["pm_counters"].(map[string]any)
		if counters == nil {
			continue
		}

		authFail := toFloat(counters["AUTH.Fail"])
		authAtt := toFloat(counters["AUTH.Att"])
		if authAtt > 0 && authFail/authAtt > 0.3 {
			alerts = append(alerts, map[string]any{
				"type":     "AUTH_FAILURE_SPIKE",
				"severity": "high",
				"detail":   fmt.Sprintf("Auth failure rate %.0f%% (%.0f/%.0f)", authFail/authAtt*100, authFail, authAtt),
			})
		}

		macFail := toFloat(counters["AUTH.FailMAC"])
		if macFail > 5 {
			alerts = append(alerts, map[string]any{
				"type":     "MAC_VERIFICATION_FAILURES",
				"severity": "critical",
				"detail":   fmt.Sprintf("%.0f MAC failures detected -- possible replay/MITM", macFail),
			})
		}

		sessFail := toFloat(counters["SM.SessFail"])
		sessAtt := toFloat(counters["SM.SessAtt"])
		if sessAtt > 0 && sessFail/sessAtt > 0.2 {
			alerts = append(alerts, map[string]any{
				"type":     "SESSION_FAILURE_SPIKE",
				"severity": "medium",
				"detail":   fmt.Sprintf("Session failure rate %.0f%%", sessFail/sessAtt*100),
			})
		}
	}

	confidence := 0.5
	if len(alerts) > 0 {
		confidence = 0.7
	}

	return AnalyticsResult{
		Result: map[string]any{
			"anomaly_detected": len(alerts) > 0,
			"alerts":           alerts,
			"alert_count":      len(alerts),
		},
		Confidence: confidence,
	}
}

// computePDUSession — PDU Session analytics. Currently a thin alias to
// computeUECommunication; see header TODO(spec: TS 23.288 §6.4) for the
// proper Observed-Service-Experience implementation.
func computePDUSession(dataPoints []DataPoint) AnalyticsResult {
	return computeUECommunication(dataPoints)
}

// computeSliceLoad — Network Slice Load analytics.
// Spec anchor: TS 23.288 §6.3.
func computeSliceLoad(dataPoints []DataPoint) AnalyticsResult {
	sliceData := map[string][]float64{}
	for _, dp := range dataPoints {
		data := parseDataJSON(dp)
		if bySlice, ok := data["sessions_by_slice"].(map[string]any); ok {
			for sst, count := range bySlice {
				sliceData[sst] = append(sliceData[sst], toFloat(count))
			}
		}
	}

	result := map[string]any{}
	for sst, counts := range sliceData {
		current := 0.0
		if len(counts) > 0 {
			current = counts[len(counts)-1]
		}
		peak := 0.0
		for _, c := range counts {
			if c > peak {
				peak = c
			}
		}
		result[sst] = map[string]any{
			"current_sessions": int(current),
			"avg_sessions":     math.Round(mean(counts)*10) / 10,
			"peak_sessions":    int(peak),
		}
	}

	confidence := 0.2
	if len(sliceData) > 0 {
		confidence = 0.7
	}

	return AnalyticsResult{
		Result:     map[string]any{"slices": result},
		Confidence: confidence,
	}
}

// toFloat converts a JSON-parsed value to float64.
func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	case json.Number:
		f, _ := n.Float64()
		return f
	}
	return 0
}
