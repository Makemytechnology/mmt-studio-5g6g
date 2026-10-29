// Package collectors — NWDAF data collectors for AMF, SMF, UPF.
//
// Spec anchors:
//
//   - TS 23.288 §6.2     Procedures for Data Collection — top-level
//     framing for the collection cycle.
//   - TS 23.288 §6.2.2   Data Collection from NFs — each Collect*Data()
//     function below realises the §6.2.2 cycle for
//     one NF, returning DataPoint slices that the
//     analytics engine can later aggregate.
//
// Layering: the AMF/SMF/UPF state singletons live in heavyweight NF
// packages (the SMF session store and the UPF manager transitively pull
// in the DPDK/cgo dataplane), so this package does NOT import them — that
// would force the dataplane library onto every NWDAF unit test. Instead it
// defines per-NF provider hooks that the composition root (the webservice
// main, which already links those NFs) wires at startup via the
// Set*StatsProvider funcs. The shared PM counter registry (oam/pm) is a
// pure-Go OAM primitive and is read directly. A provider left unset simply
// contributes no DataPoints, and the collection loop keeps running.
//
// Deferred:
//
//   - TODO(spec: TS 23.288 §6.2.3) Data Collection from OAM. Currently
//     all collectors pull NF in-process state; OAM-side performance
//     files are not consumed yet.
//   - TODO(spec: TS 23.288 §6.2.6.1, "Bulked Data Collection") — bulk
//     pull from many NFs in one round-trip.
//   - TODO(spec: TS 23.288 §6.2.4) Correlation between network data and
//     service data. Each DataPoint stays single-NF for now.
package collectors

import (
	"encoding/json"
	"time"

	"github.com/mmt/mmt-studio-core/nf/nwdaf/analytics"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

var log = logger.Get("nwdaf.collectors")

// AMFStats is a snapshot of AMF UE-context state for mobility analytics.
type AMFStats struct {
	TotalUEs   int
	Registered int
	Connected  int
	Idle       int
}

// SMFStats is a snapshot of SMF session state for PDU-session,
// UE-communication and slice-load analytics.
type SMFStats struct {
	ActiveSessions  int
	SessionsByDNN   map[string]int // DNN -> active session count
	SessionsBySlice map[string]int // SST (decimal string) -> active session count
	IPPoolUsage     map[string]int // "dnn_vN" -> allocated address count
}

// UPFStats is a snapshot of UPF traffic for QoS-sustainability analytics.
type UPFStats struct {
	RxPkts         uint64 // uplink packets at the UPF
	TxPkts         uint64 // downlink packets at the UPF
	RxBytes        uint64
	TxBytes        uint64
	Dropped        uint64 // UL + DL dropped
	ActiveSessions int
}

var (
	amfStatsFunc func() AMFStats
	smfStatsFunc func() SMFStats
	upfStatsFunc func() UPFStats
)

// SetAMFStatsProvider registers the live AMF UE-context stats source.
func SetAMFStatsProvider(fn func() AMFStats) { amfStatsFunc = fn }

// SetSMFStatsProvider registers the live SMF session stats source.
func SetSMFStatsProvider(fn func() SMFStats) { smfStatsFunc = fn }

// SetUPFStatsProvider registers the live UPF traffic stats source.
func SetUPFStatsProvider(fn func() UPFStats) { upfStatsFunc = fn }

// CollectAll runs all collectors and returns the combined data points.
func CollectAll() []analytics.DataPoint {
	var all []analytics.DataPoint
	all = append(all, CollectAMFData()...)
	all = append(all, CollectSMFData()...)
	all = append(all, CollectUPFData()...)
	return all
}

// emit marshals data and appends a DataPoint; a marshal failure drops the
// point rather than emitting malformed JSON.
func emit(points *[]analytics.DataPoint, sourceNF, analyticsID string, now float64, data map[string]any) {
	raw, err := json.Marshal(data)
	if err != nil {
		return
	}
	*points = append(*points, analytics.DataPoint{
		SourceNF:    sourceNF,
		AnalyticsID: analyticsID,
		DataJSON:    string(raw),
		CollectedAt: now,
	})
}

// CollectAMFData collects current AMF state for analytics.
//
// Spec anchor: TS 23.288 §6.2.2 (Data Collection from NFs):
//   - UE mobility events (registration, deregistration)
//   - Registration success/failure rates (via PM counters)
func CollectAMFData() []analytics.DataPoint {
	var points []analytics.DataPoint
	now := float64(time.Now().Unix())

	// UE context statistics from the live AMF UE registry.
	if amfStatsFunc != nil {
		func() {
			defer func() { recover() }()
			st := amfStatsFunc()
			emit(&points, "AMF", analytics.AnalyticsUEMobility, now, map[string]any{
				"total_ues":  st.TotalUEs,
				"registered": st.Registered,
				"connected":  st.Connected,
				"idle":       st.Idle,
			})
		}()
	}

	// PM counters snapshot — feeds both NF load (registration/session
	// rate) and abnormal-behaviour (auth/MAC/session failure) analytics,
	// which read the same pm_counters map under different Analytics IDs.
	func() {
		defer func() { recover() }()
		counters := pmSnapshot()
		emit(&points, "AMF", analytics.AnalyticsNFLoad, now, map[string]any{
			"pm_counters": counters,
		})
		emit(&points, "AMF", analytics.AnalyticsAbnormalBehaviour, now, map[string]any{
			"pm_counters": counters,
		})
	}()

	log.Debugf("AMF collector: %d points", len(points))
	return points
}

// CollectSMFData collects current SMF state for analytics.
//
// Spec anchor: TS 23.288 §6.2.2 (Data Collection from NFs):
//   - PDU session count per DNN, per slice
//   - IP pool utilization
func CollectSMFData() []analytics.DataPoint {
	var points []analytics.DataPoint
	now := float64(time.Now().Unix())

	if smfStatsFunc != nil {
		func() {
			defer func() { recover() }()
			st := smfStatsFunc()

			// Session view drives UE-communication / PDU-session analytics
			// (computeUECommunication reads total_sessions, sessions_by_dnn,
			// ip_pool_usage); both Analytics IDs share the same shape.
			sessionData := map[string]any{
				"total_sessions":  st.ActiveSessions,
				"sessions_by_dnn": toAnyMap(st.SessionsByDNN),
				"ip_pool_usage":   toAnyMap(st.IPPoolUsage),
			}
			emit(&points, "SMF", analytics.AnalyticsPDUSession, now, sessionData)
			emit(&points, "SMF", analytics.AnalyticsUECommunication, now, sessionData)

			// Slice view drives slice-load analytics (computeSliceLoad reads
			// sessions_by_slice keyed by SST).
			emit(&points, "SMF", analytics.AnalyticsSliceLoad, now, map[string]any{
				"sessions_by_slice": toAnyMap(st.SessionsBySlice),
			})
		}()
	}

	log.Debugf("SMF collector: %d points", len(points))
	return points
}

// CollectUPFData collects current UPF state for analytics.
//
// Spec anchor: TS 23.288 §6.2.2 (Data Collection from NFs):
//   - Traffic volume (UL/DL bytes)
//   - Packet counts and drop rates
func CollectUPFData() []analytics.DataPoint {
	var points []analytics.DataPoint
	now := float64(time.Now().Unix())

	if upfStatsFunc != nil {
		func() {
			defer func() { recover() }()
			st := upfStatsFunc()
			// computeQoSSustainability treats uplink as rx and downlink as
			// tx at the UPF, reading rx_pkts/tx_pkts/dropped/rx_bytes/tx_bytes.
			emit(&points, "UPF", analytics.AnalyticsQoSSustainability, now, map[string]any{
				"rx_pkts":         st.RxPkts,
				"tx_pkts":         st.TxPkts,
				"rx_bytes":        st.RxBytes,
				"tx_bytes":        st.TxBytes,
				"dropped":         st.Dropped,
				"active_sessions": st.ActiveSessions,
			})
		}()
	}

	log.Debugf("UPF collector: %d points", len(points))
	return points
}

// pmSnapshot returns a copy of the live PM counter registry as a generic
// map suitable for embedding in a DataPoint's pm_counters field.
func pmSnapshot() map[string]any {
	out := map[string]any{}
	for k, v := range pm.Default.All() {
		out[k] = v
	}
	return out
}

// toAnyMap widens a map[string]int into map[string]any for JSON embedding.
func toAnyMap(m map[string]int) map[string]any {
	out := make(map[string]any, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
