// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package iops — Isolated E-UTRAN Operation for Public Safety.
//
// IOPS lets a public-safety eNodeB / gNB keep providing service when
// its backhaul to the macro EPC/5GC is severed. The Local EPC (or
// Local 5GC) takes over, authenticating UEs against pre-cached
// AKA tuples and serving a curated set of mission-critical local
// services until the backhaul is restored.
//
// In-tree this package is the lightweight controller — per-node
// state machine, cached-credential lookup, event log. It is the
// operator's audit trail; the actual radio fall-back logic lives
// in the gNB and the actual local PDU handling in the embedded UPF.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.401 §K.1          Annex K — General description of the
//                             IOPS concept (backhaul loss → Local
//                             EPC → restore). The Annex itself is
//                             informative but the only ground-truth
//                             anchor available locally.
//   - TS 23.401 §K.2          Operation of isolated public safety
//                             networks using a Local EPC.
//   - TS 23.401 §K.2.1        General Description.
//   - TS 23.401 §K.2.2        UE configuration (IOPS-enabled USIM
//                             application, dedicated PLMN identity).
//   - TS 23.401 §K.2.3        IOPS network configuration.
//   - TS 23.401 §K.2.4        IOPS network establishment / termination.
//   - TS 23.401 §K.2.5        UE mobility within / out of IOPS.
//
// Deferred — TS 22.346 (IOPS service requirements) is not loaded
// locally; everything below stays a TODO until the PDF lands:
//
//   - TODO(spec: TS 22.346)  IOPS service requirements: mission-
//                            critical voice + data; priority handling
//                            of MCPTT > MCVideo > MCData; USIM-based
//                            local AKA against the Local EPC.
//   - TODO Nomadic EPS path  (truck-mounted Local EPC scenario;
//                            touched by TS 23.401 Annex K but no
//                            dedicated subclause to anchor against).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/safety_iops.py.
package iops

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// State of a single gNB in the IOPS lifecycle. Encoded as the
// `event_type` recorded in iops_events when the state changes —
// the event log IS the state history (no separate state column).
type State string

const (
	StateNormal       State = "normal"        // backhaul up, Macro EPC serving
	StateBackhaulLost State = "backhaul_lost" // detected; not yet failed-over
	StateIOPSActive   State = "iops_activated"
	StateRestoring    State = "restoring" // backhaul back, draining local sessions
	StateFailed       State = "failed"    // IOPS bring-up failed; degraded
)

// Event types accepted by the iops_events CHECK constraint
// (db/schemas/domains.go).
const (
	EvBackhaulLost  = "backhaul_lost"
	EvIOPSActivated = "iops_activated"
	EvRestoring     = "restoring"
	EvRestored      = "restored"
	EvFailed        = "failed"
)

// Valid forward transitions. Matches TS 23.401 §K.2.4 lifecycle
// (establishment + termination) — IOPS can only be brought up after
// backhaul loss, and only torn down via the restore path.
var validTransitions = map[State]map[State]bool{
	StateNormal:       {StateBackhaulLost: true},
	StateBackhaulLost: {StateIOPSActive: true, StateNormal: true, StateFailed: true},
	StateIOPSActive:   {StateRestoring: true, StateFailed: true},
	StateRestoring:    {StateNormal: true, StateIOPSActive: true},
	StateFailed:       {StateNormal: true},
}

// stateToEvent maps a target State to the iops_events.event_type
// value the schema CHECK constraint accepts.
var stateToEvent = map[State]string{
	StateBackhaulLost: EvBackhaulLost,
	StateIOPSActive:   EvIOPSActivated,
	StateRestoring:    EvRestoring,
	StateNormal:       EvRestored,
	StateFailed:       EvFailed,
}

var (
	mu        sync.Mutex
	gnbStates = make(map[string]State)
)

// transition attempts a state transition. Returns the resulting state
// (current, unchanged, on failure) and whether the transition was valid.
func transition(gnbID string, target State, reason string) (State, bool) {
	current := gnbStates[gnbID]
	if current == "" {
		current = StateNormal
	}
	if !validTransitions[current][target] {
		logger.Get("iops").Warnf("IOPS invalid transition gnb=%s %s->%s",
			gnbID, current, target)
		return current, false
	}
	gnbStates[gnbID] = target
	_ = logEvent(gnbID, stateToEvent[target], reason)
	logger.Get("iops").Infof("IOPS transition gnb=%s %s->%s reason=%q",
		gnbID, current, target, reason)
	return target, true
}

// DetectBackhaulLoss flips a gNB from normal → backhaul_lost.
// TS 23.401 §K.1 — the eNodeB's monitoring of S1 / NG connectivity
// is a precondition for entering IOPS mode.
func DetectBackhaulLoss(gnbID, reason string) map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	st, ok := transition(gnbID, StateBackhaulLost, reason)
	return map[string]interface{}{"success": ok, "gnb_id": gnbID, "state": string(st)}
}

// ActivateIOPS flips backhaul_lost → iops_activated. The Local EPC
// is now serving UEs locally per TS 23.401 §K.2.4 establishment.
func ActivateIOPS(gnbID string) map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	st, ok := transition(gnbID, StateIOPSActive, "")
	return map[string]interface{}{"success": ok, "gnb_id": gnbID, "state": string(st)}
}

// BeginRestoration flips iops_activated → restoring. Backhaul has
// returned; the Local EPC is draining sessions before handing back
// to the Macro EPC (TS 23.401 §K.2.4 termination).
func BeginRestoration(gnbID string) map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	st, ok := transition(gnbID, StateRestoring, "")
	return map[string]interface{}{"success": ok, "gnb_id": gnbID, "state": string(st)}
}

// CompleteRestoration flips restoring → normal. Logged with
// event_type='restored'.
func CompleteRestoration(gnbID string) map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	st, ok := transition(gnbID, StateNormal, "")
	return map[string]interface{}{"success": ok, "gnb_id": gnbID, "state": string(st)}
}

// MarkFailed flips backhaul_lost / iops_activated → failed. Used
// when IOPS bring-up failed (e.g. Local EPC unreachable) — the gNB
// degrades to limited-service mode.
func MarkFailed(gnbID, reason string) map[string]interface{} {
	mu.Lock()
	defer mu.Unlock()
	st, ok := transition(gnbID, StateFailed, reason)
	return map[string]interface{}{"success": ok, "gnb_id": gnbID, "state": string(st)}
}

// GetState returns the current IOPS state for a gNB.
func GetState(gnbID string) string {
	mu.Lock()
	defer mu.Unlock()
	s := gnbStates[gnbID]
	if s == "" {
		return string(StateNormal)
	}
	return string(s)
}

// GetAllStates snapshots state for all tracked gNBs.
func GetAllStates() map[string]string {
	mu.Lock()
	defer mu.Unlock()
	out := make(map[string]string, len(gnbStates))
	for k, v := range gnbStates {
		out[k] = string(v)
	}
	return out
}

// resetForTest clears in-memory gNB state. Intended for tests only.
func resetForTest() {
	mu.Lock()
	defer mu.Unlock()
	gnbStates = make(map[string]State)
}

// ─── Local AKA cache (TS 23.401 §K.2.3) ──────────────────────────

// CachedCredential represents one pre-computed AKA challenge tuple
// the Local EPC can replay during IOPS. Pre-caching is the only
// way to authenticate when the HSS is unreachable — see §K.2.3
// IOPS network configuration.
type CachedCredential struct {
	GnbID       string `json:"gnb_id"`
	IMSI        string `json:"imsi"`
	RandHex     string `json:"rand_hex"`
	AutnHex     string `json:"autn_hex"`
	XresStarHex string `json:"xres_star_hex"`
	KseafHex    string `json:"kseaf_hex"`
	ExpiresAt   string `json:"expires_at"`
}

// CacheCredential UPSERTs an AKA tuple into the Local EPC's pre-cached
// store, scoped to a specific (gnb_id, imsi). UPSERT lets the macro
// HSS push fresh tuples ahead of an expected backhaul outage.
func CacheCredential(c CachedCredential) error {
	if c.GnbID == "" || c.IMSI == "" {
		return errInvalid("gnb_id and imsi are required")
	}
	_, err := engine.Exec(`INSERT INTO iops_cached_credentials
		(gnb_id, imsi, rand_hex, autn_hex, xres_star_hex, kseaf_hex, expires_at)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(gnb_id, imsi) DO UPDATE SET
		  rand_hex=excluded.rand_hex,
		  autn_hex=excluded.autn_hex,
		  xres_star_hex=excluded.xres_star_hex,
		  kseaf_hex=excluded.kseaf_hex,
		  expires_at=excluded.expires_at`,
		c.GnbID, c.IMSI, c.RandHex, c.AutnHex, c.XresStarHex, c.KseafHex, c.ExpiresAt)
	return err
}

// LocalAuthenticate looks up a cached AKA tuple for (gnb_id, imsi)
// and reports whether one is available + non-expired. Real AKA
// challenge replay happens in the AMF/MME — this is just the
// availability gate.
func LocalAuthenticate(gnbID, imsi string) map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{"allowed": false, "reason": "db error"}
	}
	row := db.QueryRow(`SELECT rand_hex, expires_at FROM iops_cached_credentials
		WHERE gnb_id=? AND imsi=? AND expires_at > datetime('now')`,
		gnbID, imsi)
	var rand, exp string
	if err := row.Scan(&rand, &exp); err != nil {
		return map[string]interface{}{
			"allowed": false, "reason": "no fresh cached credential",
		}
	}
	return map[string]interface{}{
		"allowed":    true,
		"gnb_id":     gnbID,
		"imsi":       imsi,
		"method":     "cached_aka",
		"expires_at": exp,
	}
}

// DeleteCachedCredential removes one cached tuple (e.g. after the
// backhaul returns and the macro path takes over).
func DeleteCachedCredential(gnbID, imsi string) error {
	_, err := engine.Exec(
		"DELETE FROM iops_cached_credentials WHERE gnb_id=? AND imsi=?",
		gnbID, imsi)
	return err
}

// ListCachedCredentials returns the (imsi, expires_at) pairs cached
// for one gNB. Used by the operator panel to show "what would Local
// AKA be able to authenticate if backhaul went down right now?".
func ListCachedCredentials(gnbID string) ([]map[string]interface{}, error) {
	return qRows(
		`SELECT imsi, expires_at FROM iops_cached_credentials
		 WHERE gnb_id=? ORDER BY imsi`, gnbID)
}

// ─── Per-gNB config (TS 23.401 §K.2.3) ───────────────────────────

// UpsertConfig writes (or replaces) the per-gNB IOPS configuration.
// The schema's UNIQUE(gnb_id) drives ON CONFLICT … DO UPDATE.
func UpsertConfig(gnbID string, iopsEnabled, localAuthEnabled bool,
	maxLocalUEs int, localIPPool string) error {
	if gnbID == "" {
		return errInvalid("gnb_id is required")
	}
	if maxLocalUEs <= 0 {
		maxLocalUEs = 100
	}
	if localIPPool == "" {
		localIPPool = "10.99.0.0/24"
	}
	en, la := 0, 0
	if iopsEnabled {
		en = 1
	}
	if localAuthEnabled {
		la = 1
	}
	_, err := engine.Exec(`INSERT INTO iops_config
		(gnb_id, iops_enabled, local_auth_enabled, max_local_ues, local_ip_pool)
		VALUES (?,?,?,?,?)
		ON CONFLICT(gnb_id) DO UPDATE SET
		  iops_enabled=excluded.iops_enabled,
		  local_auth_enabled=excluded.local_auth_enabled,
		  max_local_ues=excluded.max_local_ues,
		  local_ip_pool=excluded.local_ip_pool`,
		gnbID, en, la, maxLocalUEs, localIPPool)
	return err
}

// GetConfig returns the per-gNB IOPS config row, or nil if absent.
func GetConfig(gnbID string) (map[string]interface{}, error) {
	rows, err := qRows("SELECT * FROM iops_config WHERE gnb_id=?", gnbID)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// ListConfigs returns every gNB's IOPS configuration row.
func ListConfigs() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM iops_config ORDER BY gnb_id")
}

// ─── Local sessions (TS 23.401 §K.2.4) ───────────────────────────

// LocalService is the curated mission-critical service set served
// during IOPS. Real prioritisation lives in the radio + UPF; this
// is the operator-visible policy view.
type LocalService struct {
	Name          string `json:"name"`
	Enabled       bool   `json:"enabled"`
	Priority      int    `json:"priority"`
	RateLimitKbps int    `json:"rate_limit_kbps"`
}

// DefaultLocalServices returns the default IOPS service catalogue.
// Order matters — index = scheduling priority during contention.
//
// TODO(spec: TS 22.346) — real priority handling needs to ride on
// the public-safety MCX stack (MCPTT > MCVideo > MCData) and honour
// per-mission overrides; we ship a flat dev default.
func DefaultLocalServices() []LocalService {
	return []LocalService{
		{Name: "emergency", Enabled: true, Priority: 1},
		{Name: "ptt", Enabled: true, Priority: 2},
		{Name: "voice", Enabled: true, Priority: 3, RateLimitKbps: 64},
		{Name: "data", Enabled: true, Priority: 4, RateLimitKbps: 256},
	}
}

// CheckServiceAvailable reports whether `serviceName` is admissible
// on `gnbID` given the current IOPS state. In normal state every
// service is open; under IOPS only the curated set is.
func CheckServiceAvailable(gnbID, serviceName string) bool {
	state := GetState(gnbID)
	if state == string(StateNormal) {
		return true
	}
	for _, svc := range DefaultLocalServices() {
		if svc.Name == serviceName && svc.Enabled {
			return true
		}
	}
	return false
}

// CreateLocalSession records a UE session served by the Local EPC.
func CreateLocalSession(gnbID, imsi, serviceType, ipAddress string) (int64, error) {
	res, err := engine.Exec(
		`INSERT INTO iops_local_sessions (gnb_id, imsi, service_type, ip_address)
		 VALUES (?,?,?,?)`,
		gnbID, imsi, serviceType, ipAddress)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// ReleaseLocalSession marks a Local-EPC session released.
func ReleaseLocalSession(sessionID int64) error {
	_, err := engine.Exec(
		`UPDATE iops_local_sessions SET status='released', released_at=datetime('now')
		 WHERE id=? AND status='active'`,
		sessionID)
	return err
}

// ListLocalSessions returns active Local-EPC sessions (for an audit panel).
func ListLocalSessions(gnbID string) ([]map[string]interface{}, error) {
	if gnbID != "" {
		return qRows(
			"SELECT * FROM iops_local_sessions WHERE gnb_id=? AND status='active' ORDER BY id DESC",
			gnbID)
	}
	return qRows(
		"SELECT * FROM iops_local_sessions WHERE status='active' ORDER BY id DESC")
}

// ─── Event log ───────────────────────────────────────────────────

func logEvent(gnbID, eventType, reason string) error {
	_, err := engine.Exec(
		`INSERT INTO iops_events (gnb_id, event_type, reason) VALUES (?,?,?)`,
		gnbID, eventType, reason)
	return err
}

// GetEvents returns the most recent IOPS events.
func GetEvents(gnbID string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	if gnbID != "" {
		return qRows(
			"SELECT * FROM iops_events WHERE gnb_id=? ORDER BY id DESC LIMIT ?",
			gnbID, limit)
	}
	return qRows("SELECT * FROM iops_events ORDER BY id DESC LIMIT ?", limit)
}

// GetStats returns coarse counters for the operator dashboard.
func GetStats() map[string]interface{} {
	mu.Lock()
	active := 0
	for _, s := range gnbStates {
		if s == StateIOPSActive {
			active++
		}
	}
	tracked := len(gnbStates)
	mu.Unlock()
	db, _ := engine.Open()
	var totalEvents, cachedCreds, activeSess int
	if db != nil {
		_ = db.QueryRow("SELECT COUNT(*) FROM iops_events").Scan(&totalEvents)
		_ = db.QueryRow(
			"SELECT COUNT(*) FROM iops_cached_credentials WHERE expires_at > datetime('now')",
		).Scan(&cachedCreds)
		_ = db.QueryRow(
			"SELECT COUNT(*) FROM iops_local_sessions WHERE status='active'",
		).Scan(&activeSess)
	}
	return map[string]interface{}{
		"tracked_gnbs":        tracked,
		"iops_active_gnbs":    active,
		"total_events":        totalEvents,
		"cached_credentials":  cachedCreds,
		"active_local_sessions": activeSess,
		"timestamp":           time.Now().Unix(),
	}
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) { return GetEvents("", 100) }
func Status() map[string]any          { return GetStats() }

// ─── helpers ─────────────────────────────────────────────────────

type errString string

func (e errString) Error() string { return string(e) }

func errInvalid(s string) error { return errString(s) }

func qRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, nil
}
