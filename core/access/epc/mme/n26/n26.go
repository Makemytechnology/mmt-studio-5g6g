// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package n26 — MME-side N26 mapped-context cache.
//
// On a 5G→4G handover the AMF derives an EPS-shaped mapped context
// (KASME + EPS bearers + UE info) from the 5G UE state and pushes
// it to the MME ahead of the actual S1 handover. The MME parks
// that context here keyed on IMSI; the EPS handler consumes it
// when the matching ATTACH or TAU lands within the TTL window.
//
// The reverse direction (4G→5G) is purely a forwarding shim today:
// the MME reports the UE's intent to move to the AMF — the AMF
// already has the full EPS context via S1 (from the still-attached
// MME) and will derive the 5G mapped context server-side.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.501 §5.17.2.2     Interworking Procedures with N26
//                             interface — only mode that uses
//                             mapped contexts.
//   - TS 23.501 §5.17.2.2.1   General — single-registration UEs
//                             are the in-scope target for this
//                             cache.
//   - TS 23.501 §5.17.2.2.2   Mobility for UEs in single-registration
//                             mode (the lifecycle this cache
//                             supports: store → consume on attach).
//   - TS 23.502 §4.11         System interworking procedures with
//                             EPC — full step-by-step flow.
//
// Deferred (TODO at unimplemented surfaces):
//
//   - TS 23.501 §5.17.2.3     Interworking without N26 — the
//                             non-mapped-context path; out of scope
//                             since this cache only exists for the
//                             N26 path.
//   - The actual handover NAS / S1 / NGAP signalling is in
//                             nf/amf/gmm + the EPC handler;
//                             this package is just the cache + audit.
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/access_mobility.py.
package n26

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// n26CTXttl is the eviction window for an unconsumed mapped context.
// 120 s comes from the Rel-15+ recommendation that EPS attach
// after a 5G→4G handover should land well within two minutes;
// anything older means the UE picked a different RAT and won't
// be back to consume the cached context.
const n26CTXttl = 120 // seconds

// MappedContext holds the EPS-shaped context the AMF derived for a
// 5G→4G handover. KASME is the EPS K-key (32 bytes); EPSBearers is
// the converted bearer list; UEInfo carries identifiers + capability
// bits the MME needs to complete the EPS attach.
type MappedContext struct {
	KASME      []byte                   `json:"kasme,omitempty"`
	EPSBearers []map[string]interface{} `json:"eps_bearers"`
	UEInfo     map[string]interface{}   `json:"ue_info"`
	Timestamp  float64                  `json:"timestamp"`
	Used       bool                     `json:"used"`
}

var (
	mu             sync.Mutex
	mappedContexts = make(map[string]*MappedContext) // imsi -> ctx
)

// resetForTest clears the in-memory cache. Intended for tests only.
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	mappedContexts = make(map[string]*MappedContext)
}

// ReceiveContextFromAMF stores an N26 mapped context pushed from
// the AMF for a 5G→4G handover (TS 23.502 §4.11). Replaces any
// existing context for the same IMSI — the most recent one wins.
func ReceiveContextFromAMF(imsi string, kasme []byte,
	bearers []map[string]interface{},
	ueInfo map[string]interface{}) map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	mappedContexts[imsi] = &MappedContext{
		KASME:      kasme,
		EPSBearers: bearers,
		UEInfo:     ueInfo,
		Timestamp:  float64(time.Now().Unix()),
	}
	logger.Get("epc.mme.n26").Infof(
		"N26 mapped context stored: IMSI=%s, %d bearers (TTL=%ds)",
		imsi, len(bearers), n26CTXttl)
	return map[string]interface{}{
		"status": "stored", "imsi": imsi,
	}
}

// GetMappedContext returns the cached mapped context for `imsi` if
// one is present, fresh, and not yet consumed. Stale entries are
// evicted lazily — calling GetMappedContext on an expired row
// removes it.
func GetMappedContext(imsi string) *MappedContext {
	mu.Lock()
	defer mu.Unlock()
	ctx := mappedContexts[imsi]
	if ctx == nil {
		return nil
	}
	if isExpired(ctx) {
		delete(mappedContexts, imsi)
		return nil
	}
	if ctx.Used {
		return nil
	}
	return ctx
}

// ConsumeMappedContext marks the cached context as consumed and
// removes it. Call this from the EPS attach handler after the
// mapped context has actually been used to authenticate the UE.
func ConsumeMappedContext(imsi string) *MappedContext {
	mu.Lock()
	defer mu.Unlock()
	ctx := mappedContexts[imsi]
	delete(mappedContexts, imsi)
	if ctx != nil {
		logger.Get("epc.mme.n26").Infof("N26 mapped context consumed: IMSI=%s", imsi)
	}
	return ctx
}

// ForwardContextToAMF is the 4G→5G forwarding shim: the MME
// announces the UE's intent to move; the AMF will pull the live EPS
// context via S1. Real signalling lives in the EPC handler.
//
// TODO TS 23.502 §4.11 — wire the actual mapped-context derivation
// (EPS bearer → 5G PDU session) here once the EPC handler can
// hand us the live UE state.
func ForwardContextToAMF(imsi string) map[string]interface{} {
	logger.Get("epc.mme.n26").Infof("4G→5G handover forwarded: IMSI=%s", imsi)
	return map[string]interface{}{"success": true, "imsi": imsi}
}

// GetN26Status reports cache occupancy + TTL settings.
func GetN26Status() map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	now := float64(time.Now().Unix())
	active, expired := 0, 0
	for _, c := range mappedContexts {
		if !c.Used && (now-c.Timestamp) < n26CTXttl {
			active++
		} else {
			expired++
		}
	}
	return map[string]interface{}{
		"pending_mapped_contexts": active,
		"expired_contexts":        expired,
		"ttl_seconds":             n26CTXttl,
	}
}

// CleanupExpired removes every cached row past its TTL. Cheap to
// run periodically from a janitor goroutine.
func CleanupExpired() int {
	mu.Lock()
	defer mu.Unlock()
	n := 0
	for imsi, c := range mappedContexts {
		if isExpired(c) {
			delete(mappedContexts, imsi)
			n++
		}
	}
	return n
}

// Status is the GUI-panel adapter — same shape as GetN26Status.
func Status() map[string]any {
	_ = engine.Open
	return GetN26Status()
}

// isExpired tells whether a cached context has aged past the TTL.
// Caller holds mu.
func isExpired(c *MappedContext) bool {
	return (float64(time.Now().Unix()) - c.Timestamp) >= n26CTXttl
}
