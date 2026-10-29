// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package tcs -- Tactical Communication System (TCS) sync agent.
//
// Go port of safety/tcs/*.py.  UE location tracking, sync peer management,
// and CRDT change log.  Tables: ue_location, sync_peers, db_changes.
//
// The TCS sync agent replicates state across tactical NIB nodes using
// LWW-CRDT (Last-Writer-Wins) with Lamport timestamps.
package tcs

import (
	"database/sql"
	"encoding/json"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ---- Types ----

type UELocation struct {
	SUPI           string  `json:"supi"`
	ServingNodeID  string  `json:"serving_node_id"`
	ServingNodeIP  string  `json:"serving_node_ip"`
	IMSContactURI  *string `json:"ims_contact_uri,omitempty"`
	MCXEndpoint    *string `json:"mcx_endpoint,omitempty"`
	Status         string  `json:"status"`
	LamportClock   int     `json:"lamport_clock"`
	UpdatedAt      string  `json:"updated_at"`
}

type SyncPeer struct {
	NodeID        string  `json:"node_id"`
	NodeIP        string  `json:"node_ip"`
	SidelinkL2ID  *string `json:"sidelink_l2_id,omitempty"`
	IMSSipURI     *string `json:"ims_sip_uri,omitempty"`
	MCXEndpoint   *string `json:"mcx_endpoint,omitempty"`
	LinkStatus    string  `json:"link_status"`
	NodeMode      string  `json:"node_mode"`
	SidelinkRSRP  *float64 `json:"sidelink_rsrp,omitempty"`
	LastSeen      string  `json:"last_seen"`
}

type DBChange struct {
	ID           int64   `json:"id"`
	TableName    string  `json:"table_name"`
	RowKey       string  `json:"row_key"`
	ColumnName   string  `json:"column_name"`
	NewValue     *string `json:"new_value,omitempty"`
	LamportClock int     `json:"lamport_clock"`
	NodeID       string  `json:"node_id"`
	Version      int     `json:"version"`
	AppliedAt    string  `json:"applied_at"`
}

// ---- GUI panel API ----

func List() ([]UELocation, error) { return ListUELocations("") }

func Status() map[string]any {
	ues, _ := ListUELocations("")
	peers, _ := ListSyncPeers()
	return map[string]any{"ue_count": len(ues), "peer_count": len(peers)}
}

// ---- UE Location CRUD ----

func ListUELocations(status string) ([]UELocation, error) {
	q := `SELECT supi, serving_node_id, serving_node_ip, ims_contact_uri,
		mcx_endpoint, status, lamport_clock, updated_at FROM ue_location`
	var args []interface{}
	if status != "" {
		q += " WHERE status=?"
		args = append(args, status)
	}
	q += " ORDER BY supi"
	rows, err := engine.Query(q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []UELocation
	for rows.Next() {
		var u UELocation
		if err := rows.Scan(&u.SUPI, &u.ServingNodeID, &u.ServingNodeIP,
			&u.IMSContactURI, &u.MCXEndpoint, &u.Status, &u.LamportClock,
			&u.UpdatedAt); err != nil { return nil, err }
		out = append(out, u)
	}
	return out, rows.Err()
}

func GetUELocation(supi string) (*UELocation, error) {
	row := engine.QueryRow(`SELECT supi, serving_node_id, serving_node_ip,
		ims_contact_uri, mcx_endpoint, status, lamport_clock, updated_at
		FROM ue_location WHERE supi=?`, supi)
	var u UELocation
	err := row.Scan(&u.SUPI, &u.ServingNodeID, &u.ServingNodeIP,
		&u.IMSContactURI, &u.MCXEndpoint, &u.Status, &u.LamportClock,
		&u.UpdatedAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &u, err
}

// SetUELocation upserts a UE location (called on attach/detach).
func SetUELocation(supi, servingNodeID, servingNodeIP string,
	imsContactURI, mcxEndpoint *string, status string, lamportClock int) error {
	_, err := engine.Exec(`INSERT INTO ue_location
		(supi, serving_node_id, serving_node_ip, ims_contact_uri, mcx_endpoint,
		 status, lamport_clock, updated_at)
		VALUES (?,?,?,?,?,?,?,datetime('now'))
		ON CONFLICT(supi) DO UPDATE SET
		serving_node_id=excluded.serving_node_id,
		serving_node_ip=excluded.serving_node_ip,
		ims_contact_uri=excluded.ims_contact_uri,
		mcx_endpoint=excluded.mcx_endpoint,
		status=excluded.status,
		lamport_clock=excluded.lamport_clock,
		updated_at=excluded.updated_at`,
		supi, servingNodeID, servingNodeIP, imsContactURI, mcxEndpoint,
		status, lamportClock)
	return err
}

func DeleteUELocation(supi string) error {
	_, err := engine.Exec(`DELETE FROM ue_location WHERE supi=?`, supi)
	return err
}

// ---- Sync Peer CRUD ----

func ListSyncPeers() ([]SyncPeer, error) {
	rows, err := engine.Query(`SELECT node_id, node_ip, sidelink_l2_id,
		ims_sip_uri, mcx_endpoint, link_status, node_mode, sidelink_rsrp, last_seen
		FROM sync_peers ORDER BY node_id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []SyncPeer
	for rows.Next() {
		var p SyncPeer
		if err := rows.Scan(&p.NodeID, &p.NodeIP, &p.SidelinkL2ID,
			&p.IMSSipURI, &p.MCXEndpoint, &p.LinkStatus, &p.NodeMode,
			&p.SidelinkRSRP, &p.LastSeen); err != nil { return nil, err }
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetSyncPeer(nodeID string) (*SyncPeer, error) {
	row := engine.QueryRow(`SELECT node_id, node_ip, sidelink_l2_id,
		ims_sip_uri, mcx_endpoint, link_status, node_mode, sidelink_rsrp, last_seen
		FROM sync_peers WHERE node_id=?`, nodeID)
	var p SyncPeer
	err := row.Scan(&p.NodeID, &p.NodeIP, &p.SidelinkL2ID,
		&p.IMSSipURI, &p.MCXEndpoint, &p.LinkStatus, &p.NodeMode,
		&p.SidelinkRSRP, &p.LastSeen)
	if err == sql.ErrNoRows { return nil, nil }
	return &p, err
}

// SetSyncPeer upserts a sync peer (called on heartbeat receive).
func SetSyncPeer(nodeID, nodeIP, linkStatus, nodeMode string) error {
	_, err := engine.Exec(`INSERT INTO sync_peers
		(node_id, node_ip, link_status, node_mode, last_seen)
		VALUES (?,?,?,?,datetime('now'))
		ON CONFLICT(node_id) DO UPDATE SET
		node_ip=excluded.node_ip, link_status=excluded.link_status,
		node_mode=excluded.node_mode, last_seen=excluded.last_seen`,
		nodeID, nodeIP, linkStatus, nodeMode)
	return err
}

// UpdatePeerLinkStatus updates just the link_status for a peer.
func UpdatePeerLinkStatus(nodeID, linkStatus string) error {
	_, err := engine.Exec(`UPDATE sync_peers SET link_status=? WHERE node_id=?`,
		linkStatus, nodeID)
	return err
}

func DeleteSyncPeer(nodeID string) error {
	_, err := engine.Exec(`DELETE FROM sync_peers WHERE node_id=?`, nodeID)
	return err
}

// ---- CRDT Change Log ----

// RecordChange inserts a CRDT change entry for replication.
func RecordChange(tableName, rowKey, columnName, newValue string,
	lamportClock int, nodeID string, version int) error {
	_, err := engine.Exec(`INSERT INTO db_changes
		(table_name, row_key, column_name, new_value, lamport_clock, node_id, version)
		VALUES (?,?,?,?,?,?,?)`,
		tableName, rowKey, columnName, newValue, lamportClock, nodeID, version)
	return err
}

// GetChangesSinceVersion returns changes with version > sinceVersion.
func GetChangesSinceVersion(sinceVersion, limit int) ([]DBChange, error) {
	if limit <= 0 { limit = 100 }
	rows, err := engine.Query(`SELECT id, table_name, row_key, column_name,
		new_value, lamport_clock, node_id, version, applied_at
		FROM db_changes WHERE version > ? ORDER BY version ASC LIMIT ?`,
		sinceVersion, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []DBChange
	for rows.Next() {
		var c DBChange
		if err := rows.Scan(&c.ID, &c.TableName, &c.RowKey, &c.ColumnName,
			&c.NewValue, &c.LamportClock, &c.NodeID, &c.Version, &c.AppliedAt); err != nil { return nil, err }
		out = append(out, c)
	}
	return out, rows.Err()
}

// GetLatestVersion returns the highest version number in db_changes.
func GetLatestVersion() int {
	row := engine.QueryRow(`SELECT COALESCE(MAX(version), 0) FROM db_changes`)
	var v int
	_ = row.Scan(&v)
	return v
}

// ApplyChange applies a remote CRDT change using LWW merge.
// Returns {"applied": true} if the change was applied, false if local wins.
func ApplyChange(tableName, rowKey, columnName, newValue string,
	lamportClock int, nodeID string, version int) map[string]interface{} {
	// Check if we already have a newer change for this row+column
	row := engine.QueryRow(`SELECT lamport_clock, node_id FROM db_changes
		WHERE table_name=? AND row_key=? AND column_name=?
		ORDER BY lamport_clock DESC, node_id DESC LIMIT 1`,
		tableName, rowKey, columnName)
	var localClock int
	var localNode string
	if err := row.Scan(&localClock, &localNode); err == nil {
		// LWW: higher clock wins; tie-break on node_id (lexicographic)
		if lamportClock < localClock || (lamportClock == localClock && nodeID <= localNode) {
			return map[string]interface{}{"applied": false}
		}
	}

	// Apply the change
	_ = RecordChange(tableName, rowKey, columnName, newValue, lamportClock, nodeID, version)

	// If it's a ue_location change, apply to the ue_location table
	if tableName == "ue_location" && columnName == "*" {
		applyUELocationChange(rowKey, newValue, lamportClock)
	}

	return map[string]interface{}{"applied": true}
}

func applyUELocationChange(supi, valueJSON string, lamportClock int) {
	var info map[string]interface{}
	if json.Unmarshal([]byte(valueJSON), &info) != nil {
		return
	}
	nodeID, _ := info["serving_node_id"].(string)
	nodeIP, _ := info["serving_node_ip"].(string)
	imsContact, _ := info["ims_contact_uri"].(string)
	mcxEndpoint, _ := info["mcx_endpoint"].(string)
	status, _ := info["status"].(string)
	if status == "" { status = "ATTACHED" }
	_ = SetUELocation(supi, nodeID, nodeIP,
		nilStrPtr(imsContact), nilStrPtr(mcxEndpoint),
		status, lamportClock)
}

func nilStrPtr(s string) *string {
	if s == "" { return nil }
	return &s
}

