// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NTN Phase 2 surface — operator-side state for the Rel-19+
// enhancements layered on top of the Phase-1 NTN architecture:
//
//   1. 5G Satellite Backhaul (TS 23.501 §5.43) — using a satellite
//      link to backhaul a terrestrial gNB (the operator-visible
//      half is which gNB rides which satellite link, with what
//      capacity).
//   2. Store-and-Forward (S&F) — TR 38.821 lists this as a study
//      item without a single normative §-clause in v16.2; we
//      surface a per-IMSI buffer counter the operator can drain.
//   3. Inter-Satellite Links (ISL) — same status; we surface a
//      pair-table of (sat_a → sat_b) with a "next-hop" annotation.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.501 §5.43         Support for 5G Satellite Backhaul —
//                             the only Phase-2 enhancement with a
//                             dedicated normative clause in our
//                             local PDFs.
//   - TS 22.261 §6.3.2.3      Service requirements for satellite
//                             access (umbrella; same anchor as
//                             Phase-1 NTN).
//
// Deferred — these remain operator stubs until the Phase-2 normative
// PDFs land (TS 38.821 v17+, TS 23.501 Rel-19 amendments):
//
//   - TODO Store-and-Forward  Buffer + drain counters here let an
//                             operator see how much the constellation
//                             is queueing during eclipse / pass gaps;
//                             the actual buffer lives on-board.
//   - TODO Inter-Satellite    The pair table here is purely the
//     Links                   operator-visible mapping; real ISL
//                             routing is a constellation function.
//   - TODO(spec: TS 38.821)   Phase-2 architecture variants (multi-
//                             RAT NTN, RAT-handover S&F).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/access_mobility.py.

package ntn

import (
	"errors"
	"sort"
	"sync"
	"time"
)

// ─── 5G Satellite Backhaul (TS 23.501 §5.43) ─────────────────────

// BackhaulLink describes one terrestrial-gNB → satellite uplink
// the operator has provisioned for backhaul. The capacity_mbps is
// the contracted ceiling; current_mbps is reported by the operator
// monitoring path (here, just the last UpdateUsage value).
type BackhaulLink struct {
	GnbID         string  `json:"gnb_id"`
	SatelliteID   string  `json:"satellite_id"`
	CapacityMbps  float64 `json:"capacity_mbps"`
	CurrentMbps   float64 `json:"current_mbps"`
	Active        bool    `json:"active"`
	UpdatedAtUnix int64   `json:"updated_at"`
}

// BackhaulManager is the operator-side §5.43 ledger.
type BackhaulManager struct {
	mu    sync.Mutex
	links map[string]*BackhaulLink // key = gnbID
}

// NewBackhaulManager returns an empty backhaul ledger.
func NewBackhaulManager() *BackhaulManager {
	return &BackhaulManager{links: map[string]*BackhaulLink{}}
}

// DefaultBackhaulMgr is the package-level singleton consumed by the
// /api/ntn/phase2/backhaul/* operator surface.
var DefaultBackhaulMgr = NewBackhaulManager()

// Provision registers (or replaces) the satellite-backhaul mapping
// for one terrestrial gNB. Sets active=true and zero current usage.
//
// TS 23.501 §5.43 mandates the operator's awareness of which gNB
// is satellite-backhauled vs. terrestrial-backhauled — this is
// the surface that captures it.
func (m *BackhaulManager) Provision(gnbID, satID string, capMbps float64) error {
	if gnbID == "" || satID == "" {
		return errors.New("gnb_id and satellite_id are required")
	}
	if capMbps <= 0 {
		return errors.New("capacity_mbps must be > 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[gnbID] = &BackhaulLink{
		GnbID: gnbID, SatelliteID: satID,
		CapacityMbps: capMbps, CurrentMbps: 0,
		Active:        true,
		UpdatedAtUnix: time.Now().Unix(),
	}
	return nil
}

// Deprovision removes a backhaul mapping (e.g. the operator switched
// the gNB back to fibre). Returns whether a row was actually removed.
func (m *BackhaulManager) Deprovision(gnbID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.links[gnbID]; !ok {
		return false
	}
	delete(m.links, gnbID)
	return true
}

// SetActive toggles the active flag without removing the row. Used
// for transient outages.
func (m *BackhaulManager) SetActive(gnbID string, active bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[gnbID]
	if !ok {
		return false
	}
	link.Active = active
	link.UpdatedAtUnix = time.Now().Unix()
	return true
}

// UpdateUsage records the latest observed throughput on this link.
// Returns false if `gnbID` has no provisioned link.
func (m *BackhaulManager) UpdateUsage(gnbID string, mbps float64) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[gnbID]
	if !ok {
		return false
	}
	if mbps < 0 {
		mbps = 0
	}
	link.CurrentMbps = mbps
	link.UpdatedAtUnix = time.Now().Unix()
	return true
}

// Get returns the current backhaul row for `gnbID`, or nil if
// unset. Returns a copy so callers can't mutate the ledger.
func (m *BackhaulManager) Get(gnbID string) *BackhaulLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[gnbID]
	if !ok {
		return nil
	}
	cp := *link
	return &cp
}

// All returns every provisioned link, sorted by gnb_id for stable
// dashboard rendering.
func (m *BackhaulManager) All() []*BackhaulLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*BackhaulLink, 0, len(m.links))
	for _, l := range m.links {
		cp := *l
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].GnbID < out[j].GnbID })
	return out
}

// Stats returns operator-dashboard counters.
func (m *BackhaulManager) Stats() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	var totalCap, totalCur float64
	active := 0
	for _, l := range m.links {
		totalCap += l.CapacityMbps
		totalCur += l.CurrentMbps
		if l.Active {
			active++
		}
	}
	util := 0.0
	if totalCap > 0 {
		util = (totalCur / totalCap) * 100.0
	}
	return map[string]interface{}{
		"total_links":        len(m.links),
		"active_links":       active,
		"total_capacity_mbps": totalCap,
		"total_usage_mbps":   totalCur,
		"utilization_pct":    util,
	}
}

// ─── Store-and-Forward queue (TODO TS 38.821) ────────────────────

// SAFManager tracks per-IMSI uplink buffers for UEs that produced
// data while the satellite was out of feeder-link reach. The actual
// buffer is on-board the satellite; this is just the operator-side
// counter so a dashboard can show "N MB queued for IMSI X".
type SAFManager struct {
	mu      sync.Mutex
	queues  map[string]*SAFQueue // key = imsi
}

// SAFQueue is one UE's S&F counter view.
type SAFQueue struct {
	IMSI         string `json:"imsi"`
	QueuedBytes  int64  `json:"queued_bytes"`
	LastEnqueue  int64  `json:"last_enqueue_unix,omitempty"`
	LastDelivery int64  `json:"last_delivery_unix,omitempty"`
}

// NewSAFManager returns an empty S&F ledger.
func NewSAFManager() *SAFManager { return &SAFManager{queues: map[string]*SAFQueue{}} }

// DefaultSAFMgr is the package-level singleton consumed by the
// /api/ntn/phase2/saf/* operator surface.
var DefaultSAFMgr = NewSAFManager()

// Enqueue records that `bytes` were queued on-board for `imsi`.
// Negative inputs are clamped to 0.
func (s *SAFManager) Enqueue(imsi string, bytes int64) error {
	if imsi == "" {
		return errors.New("imsi is required")
	}
	if bytes < 0 {
		bytes = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[imsi]
	if q == nil {
		q = &SAFQueue{IMSI: imsi}
		s.queues[imsi] = q
	}
	q.QueuedBytes += bytes
	q.LastEnqueue = time.Now().Unix()
	return nil
}

// Drain records that `bytes` were delivered to terrestrial 5GC.
// Caps QueuedBytes at 0 (delivery beyond what we tracked is fine —
// the on-board counter is authoritative).
func (s *SAFManager) Drain(imsi string, bytes int64) error {
	if imsi == "" {
		return errors.New("imsi is required")
	}
	if bytes < 0 {
		bytes = 0
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	q := s.queues[imsi]
	if q == nil {
		return nil
	}
	q.QueuedBytes -= bytes
	if q.QueuedBytes < 0 {
		q.QueuedBytes = 0
	}
	q.LastDelivery = time.Now().Unix()
	return nil
}

// QueueFor returns one IMSI's queue (or nil).
func (s *SAFManager) QueueFor(imsi string) *SAFQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	q, ok := s.queues[imsi]
	if !ok {
		return nil
	}
	cp := *q
	return &cp
}

// AllQueues returns every queue snapshot, sorted by IMSI.
func (s *SAFManager) AllQueues() []*SAFQueue {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*SAFQueue, 0, len(s.queues))
	for _, q := range s.queues {
		cp := *q
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].IMSI < out[j].IMSI })
	return out
}

// Stats returns aggregate S&F counters.
func (s *SAFManager) Stats() map[string]interface{} {
	s.mu.Lock()
	defer s.mu.Unlock()
	var total int64
	nonEmpty := 0
	for _, q := range s.queues {
		total += q.QueuedBytes
		if q.QueuedBytes > 0 {
			nonEmpty++
		}
	}
	return map[string]interface{}{
		"queued_imsis":        len(s.queues),
		"non_empty_queues":    nonEmpty,
		"total_queued_bytes":  total,
	}
}

// ─── Inter-Satellite Links (TODO TS 38.821) ──────────────────────

// ISLLink is a single (satA → satB) hop the operator has registered
// for awareness. The constellation owns the actual routing; this
// is just the dashboard view.
type ISLLink struct {
	From       string  `json:"from"`
	To         string  `json:"to"`
	BandwidthMbps float64 `json:"bandwidth_mbps"`
	Active     bool    `json:"active"`
}

// ISLManager is the operator-side ledger of registered ISL hops.
type ISLManager struct {
	mu    sync.Mutex
	links map[string]*ISLLink // key = "from->to"
}

// NewISLManager returns an empty ISL ledger.
func NewISLManager() *ISLManager { return &ISLManager{links: map[string]*ISLLink{}} }

// DefaultISLMgr is the package-level singleton consumed by the
// /api/ntn/phase2/isl/* operator surface.
var DefaultISLMgr = NewISLManager()

func islKey(from, to string) string { return from + "->" + to }

// AddLink registers a directed ISL hop.
func (m *ISLManager) AddLink(from, to string, bwMbps float64) error {
	if from == "" || to == "" {
		return errors.New("from and to are required")
	}
	if from == to {
		return errors.New("self-loop links are not allowed")
	}
	if bwMbps <= 0 {
		return errors.New("bandwidth_mbps must be > 0")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.links[islKey(from, to)] = &ISLLink{
		From: from, To: to, BandwidthMbps: bwMbps, Active: true,
	}
	return nil
}

// RemoveLink removes a directed ISL hop.
func (m *ISLManager) RemoveLink(from, to string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	k := islKey(from, to)
	if _, ok := m.links[k]; !ok {
		return false
	}
	delete(m.links, k)
	return true
}

// SetActive toggles the active flag on one ISL hop without removing it.
func (m *ISLManager) SetActive(from, to string, active bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	link, ok := m.links[islKey(from, to)]
	if !ok {
		return false
	}
	link.Active = active
	return true
}

// All returns every ISL hop sorted by (from, to).
func (m *ISLManager) All() []*ISLLink {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*ISLLink, 0, len(m.links))
	for _, l := range m.links {
		cp := *l
		out = append(out, &cp)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].From != out[j].From {
			return out[i].From < out[j].From
		}
		return out[i].To < out[j].To
	})
	return out
}

// Neighbours returns every directly-reachable destination from `from`.
// Order matches the All() ordering.
func (m *ISLManager) Neighbours(from string) []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []string{}
	for _, l := range m.links {
		if l.From == from && l.Active {
			out = append(out, l.To)
		}
	}
	sort.Strings(out)
	return out
}

// Stats returns aggregate ISL counters.
func (m *ISLManager) Stats() map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	active := 0
	for _, l := range m.links {
		if l.Active {
			active++
		}
	}
	return map[string]interface{}{
		"total_isl_links": len(m.links),
		"active_isl_links": active,
	}
}
