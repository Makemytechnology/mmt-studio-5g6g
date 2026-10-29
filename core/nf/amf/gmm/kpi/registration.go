// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package kpi — per-procedure performance counters for the AMF GMM.
//
// Smallest-useful-slice scope:
//   * Registration procedure only (TS 24.501 §5.5.1.2 → 3GPP TS 28.554 §6
//     RM-RegSR and RM-RegMeanTime).
//   * Counters: attempts / successes / failures.
//   * Latency histogram in fixed log-style buckets (no HDR dependency).
//   * In-process state; tester's BenchmarkContext snapshots + diffs.
//
// Future procedures (PDU session establish, handover, paging, auth) plug
// in via a second collector in this same package — same envelope, same
// Snapshot()/Reset() contract. Keep one collector per file.
//
// Why not Prometheus directly: the rest of the admin API speaks JSON
// over /api; operators that want Prom can scrape /api/kpis/snapshot
// from a sidecar exporter — out of scope for the core itself.
package kpi

import (
	"sync"
	"sync/atomic"
	"time"
)

// Bucket boundaries in milliseconds. Chosen for the registration
// latency range we actually see (sub-millisecond DB lookups up to
// multi-second AKA round-trips). Upper bound is "+Inf" implicitly —
// anything ≥ the last boundary lands in the overflow slot.
var regLatencyBoundsMs = []uint64{
	1, 2, 5, 10, 20, 50, 100, 200, 500,
	1000, 2000, 5000, 10000, 30000,
}

// RegistrationKPIs is the wire shape returned by Snapshot. Matches the
// JSON envelope every /api/kpis/* endpoint uses (see docs above).
type RegistrationKPIs struct {
	// Wall-clock window — start/end of the measurement period. Window
	// start is set by Reset(); window end is "now" at Snapshot time.
	WindowStartUnixNs int64 `json:"window_start_unix_ns"`
	WindowEndUnixNs   int64 `json:"window_end_unix_ns"`

	Counters struct {
		Attempts  uint64 `json:"attempts"`
		Successes uint64 `json:"successes"`
		Failures  uint64 `json:"failures"`
		// In-flight attempts — recorded RecordAttempt but neither
		// RecordSuccess nor RecordFailure yet. Useful sanity check
		// for a hung test: in_flight > 0 after the test ended means
		// the AMF lost track of UEs (no transition to REGISTERED or
		// to Registration Reject — typically a timeout that wasn't
		// emitted to the KPI hook).
		InFlight uint64 `json:"in_flight"`
	} `json:"counters"`

	LatencyMs struct {
		MinMs     float64       `json:"min_ms"`
		P50Ms     float64       `json:"p50_ms"`
		P95Ms     float64       `json:"p95_ms"`
		P99Ms     float64       `json:"p99_ms"`
		MaxMs     float64       `json:"max_ms"`
		MeanMs    float64       `json:"mean_ms"`
		Count     uint64        `json:"count"`
		// Bucketed histogram. Each entry: [lower_inclusive_ms,
		// upper_exclusive_ms, count]. The last entry has upper=-1
		// to mean "+Inf" (overflow).
		Histogram [][3]float64  `json:"histogram"`
	} `json:"latency_ms"`
}

// registrationState is the package-private aggregate. One process-wide
// instance — the AMF's KPI counters are not per-context.
type registrationState struct {
	mu sync.Mutex

	// Window start — RFC 3339 epoch ns at the time Reset() last
	// fired (or process start).
	windowStartUnixNs int64

	// Counters are atomic so the hot path (RecordAttempt / Success
	// / Failure) doesn't need the mutex.
	attempts  atomic.Uint64
	successes atomic.Uint64
	failures  atomic.Uint64

	// In-flight map: amfUeID → attempt timestamp (monotonic ns).
	// Mutex-guarded because the map grows/shrinks per UE and atomic
	// ops aren't enough for map operations.
	inFlight map[int64]int64

	// Histogram buckets + raw sample collector for percentile math.
	// We keep raw samples (capped) so percentiles are exact within
	// the sample window. Older samples get pushed out (ring buffer).
	bucketsMs []uint64
	samplesMs []float64 // raw, capped at samplesCap

	sumMs float64
	minMs float64
	maxMs float64
}

const samplesCap = 10000 // enough for any single test; ring-buffer-style truncate

var regState = &registrationState{
	windowStartUnixNs: time.Now().UnixNano(),
	inFlight:          make(map[int64]int64),
	bucketsMs:         make([]uint64, len(regLatencyBoundsMs)+1),
}

// RecordAttempt marks the start of a Registration procedure for amfUeID.
// Called from gmm.registration.go right after the RegistrationRequest
// is logged. Idempotent on duplicate calls for the same amfUeID — we
// keep the FIRST timestamp so retries don't reset the clock.
func RecordAttempt(amfUeID int64) {
	regState.attempts.Add(1)
	regState.mu.Lock()
	if _, exists := regState.inFlight[amfUeID]; !exists {
		regState.inFlight[amfUeID] = time.Now().UnixNano()
	}
	regState.mu.Unlock()
}

// RecordSuccess marks a Registration as completed for amfUeID. Computes
// latency from the matching RecordAttempt; if no attempt was recorded
// (e.g. attempt hook ran before this package was loaded) the success
// is still counted but contributes no latency sample.
func RecordSuccess(amfUeID int64) {
	regState.successes.Add(1)
	regState.mu.Lock()
	startNs, ok := regState.inFlight[amfUeID]
	if ok {
		delete(regState.inFlight, amfUeID)
		lat := time.Now().UnixNano() - startNs
		latMs := float64(lat) / 1e6
		regState.recordLatency(latMs)
	}
	regState.mu.Unlock()
}

// RecordFailure marks a Registration as rejected/aborted for amfUeID.
// Latency is recorded too — operators want to know whether failures
// trip fast (auth reject, bad SUPI) or slow (T3550 timeout).
func RecordFailure(amfUeID int64) {
	regState.failures.Add(1)
	regState.mu.Lock()
	startNs, ok := regState.inFlight[amfUeID]
	if ok {
		delete(regState.inFlight, amfUeID)
		lat := time.Now().UnixNano() - startNs
		latMs := float64(lat) / 1e6
		regState.recordLatency(latMs)
	}
	regState.mu.Unlock()
}

// recordLatency holds regState.mu. Bucket the value + push to the raw
// sample ring.
func (s *registrationState) recordLatency(latMs float64) {
	if latMs < 0 {
		latMs = 0 // clock skew guard
	}
	// Bucket.
	idx := len(regLatencyBoundsMs) // overflow bucket
	for i, b := range regLatencyBoundsMs {
		if latMs < float64(b) {
			idx = i
			break
		}
	}
	s.bucketsMs[idx]++

	// Raw sample (ring buffer).
	if len(s.samplesMs) < samplesCap {
		s.samplesMs = append(s.samplesMs, latMs)
	} else {
		// Wrap. Use sumMs as a simple index proxy — `int(count)%cap`
		// would be more honest but requires an extra counter; the
		// distribution sticks within a single test window so the
		// approximation is fine.
		s.samplesMs[int(s.sumMs)%samplesCap] = latMs
	}

	s.sumMs += latMs
	if s.minMs == 0 || latMs < s.minMs {
		s.minMs = latMs
	}
	if latMs > s.maxMs {
		s.maxMs = latMs
	}
}

// Snapshot returns the current state — safe to call at any time.
// Percentiles are computed from the raw sample buffer (capped at
// samplesCap) so they're exact within that window.
func Snapshot() RegistrationKPIs {
	out := RegistrationKPIs{}
	out.WindowEndUnixNs = time.Now().UnixNano()
	out.Counters.Attempts = regState.attempts.Load()
	out.Counters.Successes = regState.successes.Load()
	out.Counters.Failures = regState.failures.Load()

	regState.mu.Lock()
	out.WindowStartUnixNs = regState.windowStartUnixNs
	out.Counters.InFlight = uint64(len(regState.inFlight))
	out.LatencyMs.Count = uint64(len(regState.samplesMs))
	out.LatencyMs.MinMs = regState.minMs
	out.LatencyMs.MaxMs = regState.maxMs
	if out.LatencyMs.Count > 0 {
		out.LatencyMs.MeanMs = regState.sumMs / float64(out.LatencyMs.Count)
		// Percentiles from a sorted copy. Cheap at samplesCap≤10k.
		ss := make([]float64, len(regState.samplesMs))
		copy(ss, regState.samplesMs)
		// Tiny sort — keep this self-contained (no `sort` import
		// at top to keep the inline cost obvious): insertion sort
		// would be O(n²); use the stdlib's slices.Sort via the
		// `sort` package.
		sortFloats(ss)
		out.LatencyMs.P50Ms = pct(ss, 0.50)
		out.LatencyMs.P95Ms = pct(ss, 0.95)
		out.LatencyMs.P99Ms = pct(ss, 0.99)
	}
	// Build histogram with [lower, upper, count]. Last entry uses
	// upper=-1 to denote "+Inf" (caller deserializes accordingly).
	out.LatencyMs.Histogram = make([][3]float64, 0, len(regLatencyBoundsMs)+1)
	prev := 0.0
	for i, b := range regLatencyBoundsMs {
		out.LatencyMs.Histogram = append(out.LatencyMs.Histogram,
			[3]float64{prev, float64(b), float64(regState.bucketsMs[i])})
		prev = float64(b)
	}
	out.LatencyMs.Histogram = append(out.LatencyMs.Histogram,
		[3]float64{prev, -1, float64(regState.bucketsMs[len(regLatencyBoundsMs)])})
	regState.mu.Unlock()
	return out
}

// Reset zeros every counter and re-stamps the window start to now.
// The BenchmarkContext on the tester side calls this immediately
// before driving the test workload, so percentile math is scoped
// to the test window and not contaminated by prior runs.
func Reset() {
	regState.attempts.Store(0)
	regState.successes.Store(0)
	regState.failures.Store(0)
	regState.mu.Lock()
	regState.windowStartUnixNs = time.Now().UnixNano()
	regState.inFlight = make(map[int64]int64)
	regState.bucketsMs = make([]uint64, len(regLatencyBoundsMs)+1)
	regState.samplesMs = regState.samplesMs[:0]
	regState.sumMs = 0
	regState.minMs = 0
	regState.maxMs = 0
	regState.mu.Unlock()
}
