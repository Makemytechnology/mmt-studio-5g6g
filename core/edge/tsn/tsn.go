// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package tsn — Time-Sensitive Networking integration with 5GS.
// Local persistence of the 5GS-as-TSN-bridge model defined in
// TS 23.501 §5.27 ("Enablers for Time Sensitive Communications,
// Time Synchronization and Deterministic Networking").
//
// Spec anchors:
//   - TS 23.501 §5.27.0 General — 5GS appears to the TSN domain as
//     an IEEE 802.1 bridge bounded by the DS-TT (UE-side) and
//     NW-TT (UPF-side) translators. Bridge.DSTTPort / NWTTPort
//     name those translator ports.
//   - TS 23.501 §5.27.1 Time Synchronization — gPTP master domain
//     state lives in tsn_clock_domains; ClockDomain.GMIdentity is
//     the gPTP grandmaster identifier persisted from §5.27.1.
//   - TS 23.501 §5.27.2 TSC Assistance Information (TSCAI) and
//     TSC Assistance Container (TSCAC) — Stream.IntervalUS and
//     Stream.MaxFrameSize are the per-stream traffic-pattern hints
//     the SMF feeds the gNB so radio scheduling matches the TSN
//     stream's deterministic profile.
//   - TS 23.501 §5.27.3 Support for TSC QoS Flows — Stream.Mapped5QI
//     and Stream.PDBMS persist the QoS flow that carries this TSN
//     stream end-to-end.
//   - TS 23.501 §5.27.5 5G System Bridge delay — out-of-scope here;
//     the AF reports it dynamically (see TODO).
//
// IEEE 802.1Q gate-schedule terminology (cycle time, gate state)
// is drawn from IEEE 802.1Qbv, which TS 23.501 §5.27.2 references
// but does not itself define. GateSchedule rows are tester-side
// fixtures for end-to-end tests; the 5GC does not normatively
// programme gate state — that's a TSN AF / CNC concern.
//
// TODO TS 23.501 §5.27.5 — wire 5G System Bridge delay reporting
// once the AF→TSN-AF interaction surface is anchored.
package tsn

import (
	"database/sql"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ---- Types ----

// Bridge represents a row in tsn_bridges.
type Bridge struct {
	ID        int64  `json:"id"`
	BridgeID  string `json:"bridge_id"`
	Name      string `json:"name"`
	DSTTPort  string `json:"ds_tt_port"`
	NWTTPort  string `json:"nw_tt_port"`
	VLANID    *int   `json:"vlan_id,omitempty"`
	Status    string `json:"status"`
	CreatedAt string `json:"created_at"`
}

// Stream represents a row in tsn_streams.
type Stream struct {
	ID            int64   `json:"id"`
	BridgeID      int64   `json:"bridge_id"`
	StreamID      string  `json:"stream_id"`
	TrafficClass  int     `json:"traffic_class"`
	Priority      int     `json:"priority"`
	MaxFrameSize  int     `json:"max_frame_size"`
	IntervalUS    int     `json:"interval_us"`
	Mapped5QI     *int    `json:"mapped_5qi,omitempty"`
	PDBMS         *float64 `json:"pdb_ms,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// ClockDomain represents a row in tsn_clock_domains.
type ClockDomain struct {
	ID                   int64   `json:"id"`
	DomainID             string  `json:"domain_id"`
	GMIdentity           string  `json:"gm_identity"`
	SyncAccuracyNS       int     `json:"sync_accuracy_ns"`
	HoldoverCapabilityS  int     `json:"holdover_capability_s"`
	Status               string  `json:"status"`
	LastSyncAt           *string `json:"last_sync_at,omitempty"`
	CreatedAt            string  `json:"created_at"`
}

// GateSchedule represents a row in tsn_gate_schedules.
type GateSchedule struct {
	ID          int64  `json:"id"`
	StreamID    int64  `json:"stream_id"`
	GateState   string `json:"gate_state"`
	StartTimeNS int64  `json:"start_time_ns"`
	DurationNS  int64  `json:"duration_ns"`
	CycleTimeNS int64  `json:"cycle_time_ns"`
}

// ---- GUI panel API ----

// List returns all bridges (preserves stub API).
func List() ([]Bridge, error) { return ListBridges() }

// Status returns a summary for the GUI panel.
func Status() map[string]any {
	bridges, _ := ListBridges()
	streams, _ := ListStreams(0)
	clocks, _ := ListClockDomains()
	return map[string]any{
		"bridges": len(bridges), "streams": len(streams), "clocks": len(clocks),
	}
}

// ---- Bridge CRUD ----

// ListBridges returns all TSN bridges.
func ListBridges() ([]Bridge, error) {
	rows, err := engine.Query(`SELECT id, bridge_id, name, ds_tt_port,
		nw_tt_port, vlan_id, status, created_at FROM tsn_bridges ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Bridge
	for rows.Next() {
		var b Bridge
		if err := rows.Scan(&b.ID, &b.BridgeID, &b.Name, &b.DSTTPort,
			&b.NWTTPort, &b.VLANID, &b.Status, &b.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

// GetBridge returns a bridge by row ID.
func GetBridge(id int64) (*Bridge, error) {
	row := engine.QueryRow(`SELECT id, bridge_id, name, ds_tt_port,
		nw_tt_port, vlan_id, status, created_at FROM tsn_bridges WHERE id=?`, id)
	var b Bridge
	err := row.Scan(&b.ID, &b.BridgeID, &b.Name, &b.DSTTPort,
		&b.NWTTPort, &b.VLANID, &b.Status, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

// GetBridgeByBridgeID looks up a bridge by its operator-supplied
// bridge_id string (not the numeric PK).
func GetBridgeByBridgeID(bridgeID string) (*Bridge, error) {
	row := engine.QueryRow(`SELECT id, bridge_id, name, ds_tt_port, nw_tt_port,
		vlan_id, status, created_at FROM tsn_bridges WHERE bridge_id=?`, bridgeID)
	var b Bridge
	err := row.Scan(&b.ID, &b.BridgeID, &b.Name, &b.DSTTPort,
		&b.NWTTPort, &b.VLANID, &b.Status, &b.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &b, err
}

// DeleteBridgeByBridgeID removes by the operator-supplied id string.
func DeleteBridgeByBridgeID(bridgeID string) error {
	_, err := engine.Exec(`DELETE FROM tsn_bridges WHERE bridge_id=?`, bridgeID)
	return err
}

// GetStreamByStreamID looks up a stream by its operator-supplied
// stream_id string. The OAM panel + tester reference streams by
// the human-meaningful id, not the numeric PK.
func GetStreamByStreamID(streamID string) (*Stream, error) {
	row := engine.QueryRow(`SELECT id, bridge_id, stream_id, traffic_class, priority,
		max_frame_size, interval_us, mapped_5qi, pdb_ms, created_at
		FROM tsn_streams WHERE stream_id=?`, streamID)
	var s Stream
	err := row.Scan(&s.ID, &s.BridgeID, &s.StreamID, &s.TrafficClass, &s.Priority,
		&s.MaxFrameSize, &s.IntervalUS, &s.Mapped5QI, &s.PDBMS, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

// Map5QI maps a TSN traffic class to a 5QI value (TS 23.501
// §5.27.2 + Table 5.7.4-1). Mapping is operator policy; the
// table below mirrors the local panel's defaults.
func Map5QI(trafficClass int) int {
	switch trafficClass {
	case 0:
		return 9
	case 1:
		return 82
	case 2:
		return 83
	case 3:
		return 84
	case 4:
		return 85
	case 5:
		return 86
	}
	return 9
}

// CreateBridge registers a new 5GS TSN bridge.
func CreateBridge(bridgeID, name, dsTTPort, nwTTPort string, vlanID *int) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := engine.Exec(`INSERT INTO tsn_bridges
		(bridge_id, name, ds_tt_port, nw_tt_port, vlan_id, status, created_at)
		VALUES (?,?,?,?,?,'active',?)`,
		bridgeID, name, dsTTPort, nwTTPort, vlanID, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateBridgeStatus changes a bridge's operational status.
func UpdateBridgeStatus(id int64, status string) error {
	_, err := engine.Exec(`UPDATE tsn_bridges SET status=? WHERE id=?`, status, id)
	return err
}

// DeleteBridge removes a bridge.
func DeleteBridge(id int64) error {
	_, err := engine.Exec(`DELETE FROM tsn_bridges WHERE id=?`, id)
	return err
}

// ---- Stream CRUD ----

// ListStreams returns streams, optionally filtered by bridge row ID.
func ListStreams(bridgeID int64) ([]Stream, error) {
	q := `SELECT id, bridge_id, stream_id, traffic_class, priority,
		max_frame_size, interval_us, mapped_5qi, pdb_ms, created_at
		FROM tsn_streams`
	var args []interface{}
	if bridgeID > 0 {
		q += " WHERE bridge_id=?"
		args = append(args, bridgeID)
	}
	q += " ORDER BY id"
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Stream
	for rows.Next() {
		var s Stream
		if err := rows.Scan(&s.ID, &s.BridgeID, &s.StreamID, &s.TrafficClass,
			&s.Priority, &s.MaxFrameSize, &s.IntervalUS, &s.Mapped5QI,
			&s.PDBMS, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CreateStream adds a TSN stream on a bridge.
func CreateStream(bridgeID int64, streamID string, trafficClass, priority,
	maxFrameSize, intervalUS int, mapped5QI *int, pdbMS *float64) (int64, error) {
	if maxFrameSize <= 0 {
		maxFrameSize = 1522
	}
	if intervalUS <= 0 {
		intervalUS = 1000
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := engine.Exec(`INSERT INTO tsn_streams
		(bridge_id, stream_id, traffic_class, priority, max_frame_size,
		 interval_us, mapped_5qi, pdb_ms, created_at)
		VALUES (?,?,?,?,?,?,?,?,?)`,
		bridgeID, streamID, trafficClass, priority, maxFrameSize,
		intervalUS, mapped5QI, pdbMS, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteStream removes a stream.
func DeleteStream(id int64) error {
	_, err := engine.Exec(`DELETE FROM tsn_streams WHERE id=?`, id)
	return err
}

// ---- Clock Domain CRUD ----

// ListClockDomains returns all gPTP clock domains.
func ListClockDomains() ([]ClockDomain, error) {
	rows, err := engine.Query(`SELECT id, domain_id, gm_identity,
		sync_accuracy_ns, holdover_capability_s, status, last_sync_at, created_at
		FROM tsn_clock_domains ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ClockDomain
	for rows.Next() {
		var c ClockDomain
		if err := rows.Scan(&c.ID, &c.DomainID, &c.GMIdentity,
			&c.SyncAccuracyNS, &c.HoldoverCapabilityS, &c.Status,
			&c.LastSyncAt, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// CreateClockDomain registers a gPTP clock domain.
func CreateClockDomain(domainID, gmIdentity string, syncAccuracyNS, holdoverCapS int) (int64, error) {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := engine.Exec(`INSERT INTO tsn_clock_domains
		(domain_id, gm_identity, sync_accuracy_ns, holdover_capability_s, status, created_at)
		VALUES (?,?,?,?,'freerun',?)`,
		domainID, gmIdentity, syncAccuracyNS, holdoverCapS, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// GetClockDomainByDomainID looks up a clock domain by its
// operator-supplied domain_id string.
func GetClockDomainByDomainID(domainID string) (*ClockDomain, error) {
	row := engine.QueryRow(`SELECT id, domain_id, gm_identity,
		sync_accuracy_ns, holdover_capability_s, status, last_sync_at, created_at
		FROM tsn_clock_domains WHERE domain_id=?`, domainID)
	var c ClockDomain
	err := row.Scan(&c.ID, &c.DomainID, &c.GMIdentity,
		&c.SyncAccuracyNS, &c.HoldoverCapabilityS, &c.Status,
		&c.LastSyncAt, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

// DeleteClockDomainByDomainID removes by the operator-supplied id.
func DeleteClockDomainByDomainID(domainID string) error {
	_, err := engine.Exec(`DELETE FROM tsn_clock_domains WHERE domain_id=?`, domainID)
	return err
}

// GetClockDomain looks up a clock domain by numeric primary key.
func GetClockDomain(id int64) (*ClockDomain, error) {
	row := engine.QueryRow(`SELECT id, domain_id, gm_identity,
		sync_accuracy_ns, holdover_capability_s, status, last_sync_at, created_at
		FROM tsn_clock_domains WHERE id=?`, id)
	var c ClockDomain
	err := row.Scan(&c.ID, &c.DomainID, &c.GMIdentity,
		&c.SyncAccuracyNS, &c.HoldoverCapabilityS, &c.Status,
		&c.LastSyncAt, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

// UpdateClockStatus updates a clock domain's sync status.
func UpdateClockStatus(id int64, status string) error {
	if status == "synced" {
		now := time.Now().UTC().Format("2006-01-02 15:04:05")
		_, err := engine.Exec(`UPDATE tsn_clock_domains SET status=?, last_sync_at=? WHERE id=?`,
			status, now, id)
		return err
	}
	_, err := engine.Exec(`UPDATE tsn_clock_domains SET status=? WHERE id=?`, status, id)
	return err
}

// DeleteClockDomain removes a clock domain.
func DeleteClockDomain(id int64) error {
	_, err := engine.Exec(`DELETE FROM tsn_clock_domains WHERE id=?`, id)
	return err
}

// ---- Gate Schedule CRUD ----

// ListGateSchedules returns gate schedules for a stream.
func ListGateSchedules(streamID int64) ([]GateSchedule, error) {
	rows, err := engine.Query(`SELECT id, stream_id, gate_state,
		start_time_ns, duration_ns, cycle_time_ns
		FROM tsn_gate_schedules WHERE stream_id=? ORDER BY id`, streamID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GateSchedule
	for rows.Next() {
		var g GateSchedule
		if err := rows.Scan(&g.ID, &g.StreamID, &g.GateState,
			&g.StartTimeNS, &g.DurationNS, &g.CycleTimeNS); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// CreateGateSchedule adds a gate schedule entry for a stream.
func CreateGateSchedule(streamID int64, gateState string,
	startTimeNS, durationNS, cycleTimeNS int64) (int64, error) {
	res, err := engine.Exec(`INSERT INTO tsn_gate_schedules
		(stream_id, gate_state, start_time_ns, duration_ns, cycle_time_ns)
		VALUES (?,?,?,?,?)`,
		streamID, gateState, startTimeNS, durationNS, cycleTimeNS)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteGateSchedule removes a gate schedule entry.
func DeleteGateSchedule(id int64) error {
	_, err := engine.Exec(`DELETE FROM tsn_gate_schedules WHERE id=?`, id)
	return err
}

