// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package resilience — NF instance health, failover, geo-redundancy, state
// replication (TS 23.501 §5.19).
//
// Go port of infra/resilience/. Combines:
//   - resilience_manager.py   → NF instance CRUD, heartbeat, failover
//   - state_replication.py    → state snapshot save/restore
//   - geo_redundancy.py       → site management + site failover
//   - resilience_crud.py      → all DB access
//
// The "Resilience" GUI panel reads these tables to visualize the current
// topology across multi-site deployments.
package resilience

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("resilience")

// ════════════════════════════════════════════════════════════
// NF Instance — resilience_nf_instances
// ════════════════════════════════════════════════════════════

// NFInstance represents a resilience_nf_instances row.
type NFInstance struct {
	ID              int64  `json:"id"`
	NFType          string `json:"nf_type"`
	InstanceID      string `json:"instance_id"`
	Endpoint        string `json:"endpoint"`
	Priority        int    `json:"priority"`
	Role            string `json:"role"`   // active | standby
	Health          string `json:"health"` // healthy | degraded | failed
	LastHeartbeatAt string `json:"last_heartbeat_at,omitempty"`
	CreatedAt       string `json:"created_at"`
}

// RegisterNFInstance registers an NF instance for health monitoring and failover.
func RegisterNFInstance(nfType, instanceID, endpoint string, priority int, role string) (*NFInstance, error) {
	if role != "active" && role != "standby" {
		return nil, fmt.Errorf("invalid role: %s (must be active or standby)", role)
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT INTO resilience_nf_instances
		(nf_type, instance_id, endpoint, priority, role, health)
		VALUES (?, ?, ?, ?, ?, 'healthy')`,
		nfType, instanceID, endpoint, priority, role)
	if err != nil {
		return nil, err
	}
	pk, _ := res.LastInsertId()
	log.Info("NF registered", "type", nfType, "id", instanceID, "endpoint", endpoint, "role", role)
	return nfGetByPK(db, pk)
}

// Heartbeat records a heartbeat from an NF instance. Sets health to healthy.
func Heartbeat(instanceID string) (*NFInstance, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := db.Exec(`UPDATE resilience_nf_instances
		SET last_heartbeat_at=?, health='healthy' WHERE instance_id=?`,
		now, instanceID)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, nil
	}
	log.Debug("heartbeat received", "id", instanceID)
	return NFGet(instanceID)
}

// DetectFailure marks an NF instance as failed.
func DetectFailure(instanceID string) (*NFInstance, error) {
	inst, err := nfUpdate(instanceID, map[string]any{"health": "failed"})
	if inst != nil {
		log.Warn("NF instance marked FAILED", "id", instanceID)
	}
	return inst, err
}

// Failover promotes the highest-priority healthy standby to active for an NF type.
func Failover(nfType, reason string) (map[string]any, error) {
	instances, err := NFList(nfType)
	if err != nil {
		return nil, err
	}
	if len(instances) == 0 {
		return nil, fmt.Errorf("no instances registered for %s", nfType)
	}

	var active []NFInstance
	var standby []NFInstance
	for _, inst := range instances {
		if inst.Role == "active" {
			active = append(active, inst)
		}
		if inst.Role == "standby" && inst.Health != "failed" {
			standby = append(standby, inst)
		}
	}
	if len(standby) == 0 {
		return nil, fmt.Errorf("no healthy standby available for %s", nfType)
	}

	// Pick highest priority standby
	best := standby[0]
	for _, s := range standby[1:] {
		if s.Priority > best.Priority {
			best = s
		}
	}

	fromID := ""
	if len(active) > 0 {
		fromID = active[0].InstanceID
	}

	// Demote current active(s)
	for _, a := range active {
		nfUpdate(a.InstanceID, map[string]any{"role": "standby", "health": "failed"})
	}
	// Promote standby
	nfUpdate(best.InstanceID, map[string]any{"role": "active"})
	// Log failover
	failoverLogInsert(nfType, fromID, best.InstanceID, reason, "")

	log.Warn("failover executed", "nf_type", nfType, "from", fromID, "to", best.InstanceID, "reason", reason)

	return map[string]any{
		"nf_type":       nfType,
		"from_instance": fromID,
		"to_instance":   best.InstanceID,
		"reason":        reason,
	}, nil
}

// GetNFHealth returns all NF instances (health overview).
func GetNFHealth() ([]NFInstance, error) {
	return NFList("")
}

// GetActiveInstance returns the active instance for a given NF type, or nil.
func GetActiveInstance(nfType string) (*NFInstance, error) {
	instances, err := NFList(nfType)
	if err != nil {
		return nil, err
	}
	for _, inst := range instances {
		if inst.Role == "active" {
			return &inst, nil
		}
	}
	return nil, nil
}

// NFList returns NF instances, optionally filtered by type.
func NFList(nfType string) ([]NFInstance, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if nfType != "" {
		rows, err = db.Query(`SELECT id, nf_type, instance_id, endpoint, priority,
			role, health, last_heartbeat_at, created_at
			FROM resilience_nf_instances WHERE nf_type=?
			ORDER BY priority DESC, id`, nfType)
	} else {
		rows, err = db.Query(`SELECT id, nf_type, instance_id, endpoint, priority,
			role, health, last_heartbeat_at, created_at
			FROM resilience_nf_instances
			ORDER BY nf_type, priority DESC, id`)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanNFInstances(rows)
}

// NFGet returns an NF instance by instance_id, or nil.
func NFGet(instanceID string) (*NFInstance, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, nf_type, instance_id, endpoint, priority,
		role, health, last_heartbeat_at, created_at
		FROM resilience_nf_instances WHERE instance_id=?`, instanceID)
	return scanSingleNF(row)
}

// NFDelete removes an NF instance.
func NFDelete(instanceID string) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec("DELETE FROM resilience_nf_instances WHERE instance_id=?", instanceID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func nfGetByPK(db *sql.DB, pk int64) (*NFInstance, error) {
	row := db.QueryRow(`SELECT id, nf_type, instance_id, endpoint, priority,
		role, health, last_heartbeat_at, created_at
		FROM resilience_nf_instances WHERE id=?`, pk)
	return scanSingleNF(row)
}

func nfUpdate(instanceID string, fields map[string]any) (*NFInstance, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	allowed := map[string]bool{
		"nf_type": true, "endpoint": true, "priority": true,
		"role": true, "health": true, "last_heartbeat_at": true,
	}
	var sets []string
	var vals []any
	for col, val := range fields {
		if !allowed[col] {
			continue
		}
		sets = append(sets, col+"=?")
		vals = append(vals, val)
	}
	if len(sets) == 0 {
		return NFGet(instanceID)
	}
	vals = append(vals, instanceID)
	q := fmt.Sprintf("UPDATE resilience_nf_instances SET %s WHERE instance_id=?",
		joinStrings(sets, ", "))
	_, err = db.Exec(q, vals...)
	if err != nil {
		return nil, err
	}
	return NFGet(instanceID)
}

func scanNFInstances(rows *sql.Rows) ([]NFInstance, error) {
	var out []NFInstance
	for rows.Next() {
		var n NFInstance
		var hb sql.NullString
		if err := rows.Scan(&n.ID, &n.NFType, &n.InstanceID, &n.Endpoint,
			&n.Priority, &n.Role, &n.Health, &hb, &n.CreatedAt); err != nil {
			continue
		}
		if hb.Valid {
			n.LastHeartbeatAt = hb.String
		}
		out = append(out, n)
	}
	return out, nil
}

func scanSingleNF(row *sql.Row) (*NFInstance, error) {
	var n NFInstance
	var hb sql.NullString
	err := row.Scan(&n.ID, &n.NFType, &n.InstanceID, &n.Endpoint,
		&n.Priority, &n.Role, &n.Health, &hb, &n.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if hb.Valid {
		n.LastHeartbeatAt = hb.String
	}
	return &n, nil
}

// ════════════════════════════════════════════════════════════
// Sites — resilience_sites (geo-redundancy)
// ════════════════════════════════════════════════════════════

// Site represents a resilience_sites row.
type Site struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Location  string `json:"location"`
	Role      string `json:"role"`   // active | standby | dr_site
	Status    string `json:"status"` // online | offline | failover
	CreatedAt string `json:"created_at"`
}

// RegisterSite registers a geo-redundant site.
func RegisterSite(name, location, role string) (*Site, error) {
	if role != "active" && role != "standby" && role != "dr_site" {
		return nil, fmt.Errorf("invalid site role: %s", role)
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT INTO resilience_sites (name, location, role) VALUES (?, ?, ?)`,
		name, location, role)
	if err != nil {
		return nil, err
	}
	pk, _ := res.LastInsertId()
	log.Info("site registered", "name", name, "location", location, "role", role)
	return siteGetByPK(db, pk)
}

// ListSites returns all registered sites.
func ListSites() ([]Site, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT id, name, location, role, status, created_at FROM resilience_sites ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Site
	for rows.Next() {
		var s Site
		if err := rows.Scan(&s.ID, &s.Name, &s.Location, &s.Role, &s.Status, &s.CreatedAt); err != nil {
			continue
		}
		out = append(out, s)
	}
	return out, nil
}

// SiteDelete removes a site by name.
func SiteDelete(name string) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec("DELETE FROM resilience_sites WHERE name=?", name)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// TriggerSiteFailover demotes current active site and promotes standby.
func TriggerSiteFailover(reason string) (map[string]any, error) {
	sites, err := ListSites()
	if err != nil {
		return nil, err
	}

	var active []Site
	var standby []Site
	for _, s := range sites {
		if s.Role == "active" {
			active = append(active, s)
		}
		if s.Role == "standby" && s.Status == "online" {
			standby = append(standby, s)
		}
	}
	if len(standby) == 0 {
		return nil, fmt.Errorf("no online standby site available for failover")
	}

	fromName := ""
	if len(active) > 0 {
		fromName = active[0].Name
	}
	toName := standby[0].Name

	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	// Demote active
	for _, a := range active {
		db.Exec("UPDATE resilience_sites SET role='standby', status='failover' WHERE name=?", a.Name)
	}
	// Promote standby
	db.Exec("UPDATE resilience_sites SET role='active', status='online' WHERE name=?", toName)
	// Log
	failoverLogInsert("site", fromName, toName, reason, toName)

	log.Warn("site failover", "from", fromName, "to", toName, "reason", reason)

	return map[string]any{
		"from_site": fromName,
		"to_site":   toName,
		"reason":    reason,
	}, nil
}

func siteGetByPK(db *sql.DB, pk int64) (*Site, error) {
	var s Site
	err := db.QueryRow("SELECT id, name, location, role, status, created_at FROM resilience_sites WHERE id=?", pk).
		Scan(&s.ID, &s.Name, &s.Location, &s.Role, &s.Status, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &s, nil
}

// ════════════════════════════════════════════════════════════
// Failover Log — resilience_failover_log
// ════════════════════════════════════════════════════════════

// FailoverLogEntry represents a resilience_failover_log row.
type FailoverLogEntry struct {
	ID           int64  `json:"id"`
	NFType       string `json:"nf_type"`
	FromInstance string `json:"from_instance"`
	ToInstance   string `json:"to_instance"`
	Reason       string `json:"reason"`
	SiteName     string `json:"site"`
	CreatedAt    string `json:"created_at"`
}

func failoverLogInsert(nfType, fromInst, toInst, reason, site string) {
	db, err := engine.Open()
	if err != nil {
		return
	}
	db.Exec(`INSERT INTO resilience_failover_log
		(nf_type, from_instance, to_instance, reason, site)
		VALUES (?, ?, ?, ?, ?)`, nfType, fromInst, toInst, reason, site)
	log.Info("failover logged", "nf", nfType, "from", fromInst, "to", toInst, "reason", reason)
}

// ListFailoverLog returns failover event history.
func ListFailoverLog(nfType string, limit int) ([]FailoverLogEntry, error) {
	if limit <= 0 {
		limit = 100
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var rows *sql.Rows
	if nfType != "" {
		rows, err = db.Query(`SELECT id, nf_type, from_instance, to_instance, reason, site, created_at
			FROM resilience_failover_log WHERE nf_type=? ORDER BY id DESC LIMIT ?`, nfType, limit)
	} else {
		rows, err = db.Query(`SELECT id, nf_type, from_instance, to_instance, reason, site, created_at
			FROM resilience_failover_log ORDER BY id DESC LIMIT ?`, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []FailoverLogEntry
	for rows.Next() {
		var e FailoverLogEntry
		var site, from, to, reason sql.NullString
		if err := rows.Scan(&e.ID, &e.NFType, &from, &to, &reason, &site, &e.CreatedAt); err != nil {
			continue
		}
		e.FromInstance = from.String
		e.ToInstance = to.String
		e.Reason = reason.String
		e.SiteName = site.String
		out = append(out, e)
	}
	return out, nil
}

// ════════════════════════════════════════════════════════════
// State Replication — resilience_state_snapshots
// ════════════════════════════════════════════════════════════

// Snapshot represents a resilience_state_snapshots row.
type Snapshot struct {
	ID           int64  `json:"id"`
	NFType       string `json:"nf_type"`
	StateData    string `json:"state_data"`
	SnapshotAt   string `json:"snapshot_at"`
	ReplicatedTo string `json:"replicated_to"`
}

// ReplicateState saves a state snapshot for an NF type.
func ReplicateState(nfType string, stateData any, replicatedTo string) (*Snapshot, error) {
	var serialized string
	switch v := stateData.(type) {
	case string:
		serialized = v
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return nil, fmt.Errorf("marshal state: %w", err)
		}
		serialized = string(b)
	}

	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	res, err := db.Exec(`INSERT INTO resilience_state_snapshots
		(nf_type, state_data, replicated_to) VALUES (?, ?, ?)`,
		nfType, serialized, replicatedTo)
	if err != nil {
		return nil, err
	}
	pk, _ := res.LastInsertId()
	log.Info("state replicated", "nf_type", nfType, "size", len(serialized), "to", replicatedTo)

	var snap Snapshot
	err = db.QueryRow(`SELECT id, nf_type, state_data, snapshot_at, replicated_to
		FROM resilience_state_snapshots WHERE id=?`, pk).
		Scan(&snap.ID, &snap.NFType, &snap.StateData, &snap.SnapshotAt, &snap.ReplicatedTo)
	if err != nil {
		return nil, err
	}
	return &snap, nil
}

// GetReplicatedState returns the latest snapshot for an NF type, with parsed data.
func GetReplicatedState(nfType string) (*Snapshot, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	var repTo sql.NullString
	err = db.QueryRow(`SELECT id, nf_type, state_data, snapshot_at, replicated_to
		FROM resilience_state_snapshots WHERE nf_type=? ORDER BY id DESC LIMIT 1`, nfType).
		Scan(&snap.ID, &snap.NFType, &snap.StateData, &snap.SnapshotAt, &repTo)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	snap.ReplicatedTo = repTo.String
	return &snap, nil
}

// GetReplicationStatus returns per-NF-type replication summary.
func GetReplicationStatus() (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, nf_type, state_data, snapshot_at, replicated_to
		FROM resilience_state_snapshots ORDER BY id DESC LIMIT 500`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type nfInfo struct {
		NFType      string `json:"nf_type"`
		LatestAt    string `json:"latest_snapshot_at"`
		ReplicatedTo string `json:"replicated_to"`
		Count       int    `json:"snapshot_count"`
	}
	byType := map[string]*nfInfo{}
	total := 0
	for rows.Next() {
		var id int64
		var nfType, stateData, snapAt string
		var repTo sql.NullString
		if err := rows.Scan(&id, &nfType, &stateData, &snapAt, &repTo); err != nil {
			continue
		}
		total++
		if _, ok := byType[nfType]; !ok {
			byType[nfType] = &nfInfo{
				NFType:       nfType,
				LatestAt:     snapAt,
				ReplicatedTo: repTo.String,
			}
		}
		byType[nfType].Count++
	}

	nfTypes := make([]any, 0, len(byType))
	for _, v := range byType {
		nfTypes = append(nfTypes, v)
	}
	return map[string]any{
		"nf_types":        nfTypes,
		"total_snapshots": total,
	}, nil
}

// ════════════════════════════════════════════════════════════
// Stats
// ════════════════════════════════════════════════════════════

// ResilienceStats contains resilience statistics.
type ResilienceStats struct {
	TotalInstances int `json:"total_instances"`
	Healthy        int `json:"healthy"`
	Degraded       int `json:"degraded"`
	Failed         int `json:"failed"`
	Sites          int `json:"sites"`
	TotalFailovers int `json:"total_failovers"`
}

// GetStats returns overall resilience statistics.
func GetStats() (*ResilienceStats, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	s := &ResilienceStats{}
	db.QueryRow("SELECT COUNT(*) FROM resilience_nf_instances").Scan(&s.TotalInstances)
	db.QueryRow("SELECT COUNT(*) FROM resilience_nf_instances WHERE health='healthy'").Scan(&s.Healthy)
	db.QueryRow("SELECT COUNT(*) FROM resilience_nf_instances WHERE health='degraded'").Scan(&s.Degraded)
	db.QueryRow("SELECT COUNT(*) FROM resilience_nf_instances WHERE health='failed'").Scan(&s.Failed)
	db.QueryRow("SELECT COUNT(*) FROM resilience_sites").Scan(&s.Sites)
	db.QueryRow("SELECT COUNT(*) FROM resilience_failover_log").Scan(&s.TotalFailovers)
	return s, nil
}

// ── helpers ──

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += sep + s
	}
	return out
}
