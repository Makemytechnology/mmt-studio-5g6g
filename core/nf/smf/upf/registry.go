// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package upf — UPF instance registry (TS 23.501 §6.3.3).
//
// Go port of nf/smf/upf_registry.py. Static config (upf_id, upf_ip,
// N3/N6 IPs, supported DNN/SST, max sessions) persists to the
// upf_instances table; runtime metrics (active_sessions,
// load_percent, status, last_heartbeat) live in memory and reset on
// restart — matches the Python reference which never persisted
// transient state.
//
// SMF↔UPF interface is always PFCP/N4 (TS 29.244). The legacy
// per-instance interface_type / REST transport was removed in favour
// of a single spec-grounded wire.
package upf

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// Instance is the persistent UPF config shape.
//
// JSON tags use snake_case so the GUI's `/api/admin/upf-instances`
// consumer (webservice/templates/infrastructure.html) reads
// `upf_id` / `upf_ip` / `n3_ip` / `n6_ip` / `pfcp_port` etc.
// directly. Without tags Go's encoder would emit `UPFID` / `N3IP`
// and the GUI fields would render `undefined`.
type Instance struct {
	UPFID         string    `json:"upf_id"`
	UPFIP         string    `json:"upf_ip"`
	N3IP          string    `json:"n3_ip"`
	N6IP          string    `json:"n6_ip"`
	PFCPPort      int       `json:"pfcp_port"`      // TS 29.244 §6.1 default 8805
	SupportedDNNs []string  `json:"supported_dnns"` // split from supported_dnns TEXT
	SupportedSST  []string  `json:"supported_sst"`  // split from supported_sst TEXT
	MaxSessions   int64     `json:"max_sessions"`
	RegisteredAt  time.Time `json:"registered_at"`
}

// Runtime is the transient metrics side — tracked in memory only.
type Runtime struct {
	ActiveSessions int
	LoadPercent    int
	Status         string // "up" | "degraded" | "down" | "unknown"
	LastHeartbeat  time.Time
}

var (
	runtimeMu sync.RWMutex
	runtimes  = make(map[string]*Runtime)
)

// Register inserts or updates a UPF row and initialises its runtime.
func Register(i Instance) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	dnns := strings.Join(i.SupportedDNNs, ",")
	ssts := strings.Join(i.SupportedSST, ",")
	// Read defaults from infra_config when caller leaves fields at
	// zero. Fall through to spec defaults if infra_config is absent
	// (test harnesses that seed upf_instances directly without
	// exercising the full infra stack).
	if i.PFCPPort == 0 || i.MaxSessions == 0 {
		var cfgPFCPPort, cfgMaxSessions sql.NullInt64
		_ = db.QueryRow(`SELECT upf_pfcp_port, upf_max_sessions FROM infra_config WHERE id=1`).
			Scan(&cfgPFCPPort, &cfgMaxSessions)
		if i.PFCPPort == 0 {
			if cfgPFCPPort.Valid {
				i.PFCPPort = int(cfgPFCPPort.Int64)
			} else {
				i.PFCPPort = 8805 // TS 29.244 §6.1 default
			}
		}
		if i.MaxSessions == 0 {
			if cfgMaxSessions.Valid {
				i.MaxSessions = cfgMaxSessions.Int64
			} else {
				i.MaxSessions = 100000 // matches upf_instances DDL default
			}
		}
	}
	_, err = db.Exec(`
        INSERT INTO upf_instances
            (upf_id, upf_ip, n3_ip, n6_ip, pfcp_port,
             supported_dnns, supported_sst, max_sessions, registered_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
        ON CONFLICT(upf_id) DO UPDATE SET
            upf_ip=excluded.upf_ip, n3_ip=excluded.n3_ip, n6_ip=excluded.n6_ip,
            pfcp_port=excluded.pfcp_port,
            supported_dnns=excluded.supported_dnns,
            supported_sst=excluded.supported_sst,
            max_sessions=excluded.max_sessions`,
		i.UPFID, i.UPFIP, i.N3IP, i.N6IP,
		i.PFCPPort,
		dnns, ssts, i.MaxSessions,
		time.Now().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return err
	}
	runtimeMu.Lock()
	runtimes[i.UPFID] = &Runtime{Status: "unknown", LastHeartbeat: time.Now()}
	runtimeMu.Unlock()
	return nil
}

// Deregister removes a UPF from both the DB and the runtime map.
func Deregister(upfID string) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	if _, err := db.Exec(`DELETE FROM upf_instances WHERE upf_id=?`, upfID); err != nil {
		return err
	}
	runtimeMu.Lock()
	delete(runtimes, upfID)
	runtimeMu.Unlock()
	return nil
}

// Get returns the persistent config of a UPF (no runtime state).
func Get(upfID string) (*Instance, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT upf_id, upf_ip, n3_ip, n6_ip,
        pfcp_port, supported_dnns, supported_sst, max_sessions,
        registered_at FROM upf_instances WHERE upf_id=?`, upfID)
	return scanInstance(row)
}

// List returns every UPF in the registry.
func List() ([]Instance, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT upf_id, upf_ip, n3_ip, n6_ip,
        pfcp_port, supported_dnns, supported_sst, max_sessions,
        registered_at FROM upf_instances ORDER BY upf_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Instance
	for rows.Next() {
		i, err := scanInstance(rows)
		if err != nil {
			return nil, err
		}
		if i != nil {
			out = append(out, *i)
		}
	}
	return out, rows.Err()
}

// Heartbeat records an observation of a UPF's liveness + load.
func Heartbeat(upfID string, activeSessions, loadPct int) {
	runtimeMu.Lock()
	defer runtimeMu.Unlock()
	rt, ok := runtimes[upfID]
	if !ok {
		rt = &Runtime{}
		runtimes[upfID] = rt
	}
	rt.ActiveSessions = activeSessions
	rt.LoadPercent = loadPct
	rt.Status = "up"
	rt.LastHeartbeat = time.Now()
}

// RuntimeOf returns a copy of the current runtime state for a UPF.
func RuntimeOf(upfID string) Runtime {
	runtimeMu.RLock()
	defer runtimeMu.RUnlock()
	if rt, ok := runtimes[upfID]; ok {
		return *rt
	}
	return Runtime{Status: "unknown"}
}

// Select picks the best UPF for a (DNN, SST) pair. Policy: lowest
// load_percent among UPFs advertising both DNN and SST and in "up" state.
// Falls back to any UPF matching DNN+SST when no runtime info exists,
// and finally to any UPF when filtering empties the candidate list.
//
// Slice matching is normalised: when the upf_supported_nssai join
// table has rows for the requested SST, only those UPFs are eligible
// (TS 23.501 §6.3.3). Falls back to the legacy
// upf_instances.supported_sst CSV when the join is empty for this SST
// — keeps un-migrated rows working.
func Select(dnn, sst string) (*Instance, error) {
	all, err := List()
	if err != nil {
		return nil, err
	}
	if len(all) == 0 {
		return nil, errors.New("upf: no UPFs registered")
	}
	// Build the eligible UPF-ID set from the normalised join (preferred)
	// or fall back to the CSV column when the join is empty.
	eligible := upfsForSST(sst)
	useJoin := len(eligible) > 0

	var byDNN []Instance
	for _, u := range all {
		if !contains(u.SupportedDNNs, dnn) {
			continue
		}
		switch {
		case sst == "":
			byDNN = append(byDNN, u)
		case useJoin:
			if _, ok := eligible[u.UPFID]; ok {
				byDNN = append(byDNN, u)
			}
		default:
			if contains(u.SupportedSST, sst) {
				byDNN = append(byDNN, u)
			}
		}
	}
	if len(byDNN) == 0 {
		byDNN = all
	}
	var best *Instance
	bestLoad := 101
	for i := range byDNN {
		u := byDNN[i]
		rt := RuntimeOf(u.UPFID)
		if rt.Status == "down" {
			continue
		}
		load := rt.LoadPercent
		if rt.Status == "unknown" {
			load = 50 // treat as mid-load so a known "up" UPF wins
		}
		if load < bestLoad {
			bestLoad = load
			best = &u
		}
	}
	if best == nil {
		// Everything was "down" — hand back the first anyway; caller
		// decides whether to accept a degraded UPF.
		first := byDNN[0]
		return &first, nil
	}
	return best, nil
}

// upfsForSST returns the set of upf_ids whose upf_supported_nssai
// table has at least one nssai_catalog match for the given SST. The
// SST argument is the same upper-case zero-padded hex shape the SMF
// passes to Select (e.g. "01"); we compare against nssai_catalog.sst
// numerically. Returns an empty set when:
//   - SST is empty (caller skipped slice filtering),
//   - the join table has no rows for this SST,
//   - the DB is unreachable (callers fall back to the CSV path).
//
// Best-effort: any DB error returns an empty set rather than failing
// Select — keeps the SMF working when the migration hasn't run.
func upfsForSST(sstHex string) map[string]struct{} {
	out := map[string]struct{}{}
	if sstHex == "" {
		return out
	}
	sstNum := parseSSTHex(sstHex)
	if sstNum < 0 {
		return out
	}
	db, err := engine.Open()
	if err != nil {
		return out
	}
	rows, err := db.Query(
		`SELECT DISTINCT u.upf_id
           FROM upf_supported_nssai u
           JOIN nssai_catalog n ON n.id = u.nssai_id
           WHERE n.sst = ?`, sstNum)
	if err != nil {
		return out
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			out[id] = struct{}{}
		}
	}
	return out
}

// parseSSTHex accepts SST as upper-case zero-padded hex ("01"…"FF",
// the wire shape per TS 24.501 §9.11.2.8) or as plain decimal ("1"
// or "16"). Returns -1 on parse failure.
func parseSSTHex(s string) int {
	if len(s) == 2 {
		if v, err := strconv.ParseInt(s, 16, 0); err == nil {
			return int(v)
		}
	}
	if v, err := strconv.ParseInt(s, 10, 0); err == nil {
		return int(v)
	}
	return -1
}

type scanner interface {
	Scan(...any) error
}

func scanInstance(r scanner) (*Instance, error) {
	var (
		inst  Instance
		dnns  string
		ssts  string
		n6IP  sql.NullString
		regAt string
	)
	err := r.Scan(&inst.UPFID, &inst.UPFIP, &inst.N3IP, &n6IP,
		&inst.PFCPPort, &dnns, &ssts, &inst.MaxSessions, &regAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	inst.N6IP = n6IP.String
	if dnns != "" {
		inst.SupportedDNNs = strings.Split(dnns, ",")
	}
	if ssts != "" {
		inst.SupportedSST = strings.Split(ssts, ",")
	}
	if t, err := time.Parse("2006-01-02 15:04:05", regAt); err == nil {
		inst.RegisteredAt = t
	}
	return &inst, nil
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
