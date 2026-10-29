// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API helpers for Multi-USIM.
//
// Multi-USIM (a.k.a. MUSIM, "Multiple USIMs in a single UE") is the
// 3GPP framework for a UE that can hold multiple USIM identities
// (each with its own SUPI) and switch between them across paging /
// service request boundaries.
//
// Spec anchors:
//
//   - TS 23.501 §5.34       — System support for Multi-USIM
//                             devices (architecture).
//   - TS 23.502 §4.2.6      — Procedures for Multi-USIM —
//                             registration, paging suspension,
//                             reject-with-cause, busy indication.
//   - TS 24.501 §9.11.3.91  — MUSIM Allowed Indication (NAS IE
//                             that gates whether the UE is
//                             available right now for this USIM).
//
// Local data model (db/schemas/domains.go::MusimDDL):
//
//   - musim_groups        — one device with N USIMs ("device_id" is
//                            the hardware key; "active_imsi" is
//                            whichever USIM is currently selected).
//   - musim_group_members — (group_id, imsi) tuples + priority.
//   - musim_capabilities  — per-IMSI feature flags (MUSIM supported,
//                            max USIM count, min paging interval).
//   - musim_paging_log    — audit of busy / switched / rejected
//                            paging events (TS 23.502 §4.2.6).
package musim

import (
	"database/sql"
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ── Types ───────────────────────────────────────────────────────

// GroupMember mirrors musim_group_members.
type GroupMember struct {
	ID        int64  `json:"id"`
	GroupID   int64  `json:"group_id"`
	IMSI      string `json:"imsi"`
	Priority  int    `json:"priority"`
	USIMIndex *int   `json:"usim_index,omitempty"`
	JoinedAt  string `json:"joined_at"`
}

// Group mirrors musim_groups + the attached members slice the panel
// table needs.
type Group struct {
	ID          int64         `json:"id"`
	DeviceID    string        `json:"device_id"`
	Description string        `json:"description"`
	ActiveIMSI  string        `json:"active_imsi"`
	CreatedAt   string        `json:"created_at"`
	UpdatedAt   string        `json:"updated_at"`
	Members     []GroupMember `json:"members"`
}

// Capability mirrors musim_capabilities (TS 24.501 §9.11.3.91 IE
// derives MUSIMSupported; rest are operator-tunable defaults from
// TS 23.501 §5.34 architecture description).
type Capability struct {
	ID                  int64  `json:"id"`
	IMSI                string `json:"imsi"`
	MUSIMSupported      int    `json:"musim_supported"`
	MaxUSIMCount        int    `json:"max_usim_count"`
	MinPagingIntervalMS int    `json:"min_paging_interval_ms"`
	NegotiatedAt        string `json:"negotiated_at"`
}

// PagingEvent mirrors musim_paging_log.
type PagingEvent struct {
	ID         int64  `json:"id"`
	DeviceID   string `json:"device_id"`
	SourceIMSI string `json:"source_imsi"`
	TargetIMSI string `json:"target_imsi"`
	Reason     string `json:"reason"`
	Outcome    string `json:"outcome"`
	CreatedAt  string `json:"created_at"`
}

// validOutcomes mirrors the schema CHECK constraint on
// musim_paging_log.outcome. Surfaces panel-side as a clean 400
// rather than a SQLite CHECK 500.
var validOutcomes = map[string]bool{
	"delivered": true,
	"switched":  true,
	"timeout":   true,
	"rejected":  true,
}

// ── Stats / dashboard ───────────────────────────────────────────

// Stats returns the dashboard counter set: total groups, total
// members across all groups, count of MUSIM-capable UEs, total
// paging events (across audit log).
func Stats() map[string]any {
	db, err := engine.Open()
	if err != nil {
		return map[string]any{
			"total_groups": 0, "total_members": 0,
			"musim_capable_ues": 0, "total_paging_events": 0,
		}
	}
	g := scalarInt(db, `SELECT COUNT(*) FROM musim_groups`)
	m := scalarInt(db, `SELECT COUNT(*) FROM musim_group_members`)
	c := scalarInt(db, `SELECT COUNT(*) FROM musim_capabilities WHERE musim_supported=1`)
	p := scalarInt(db, `SELECT COUNT(*) FROM musim_paging_log`)
	out := map[string]any{
		"total_groups":        g,
		"total_members":       m,
		"musim_capable_ues":   c,
		"total_paging_events": p,
		// outcome histogram for the dashboard pie chart
		"by_outcome": map[string]int{
			"delivered": scalarInt(db, `SELECT COUNT(*) FROM musim_paging_log WHERE outcome='delivered'`),
			"switched":  scalarInt(db, `SELECT COUNT(*) FROM musim_paging_log WHERE outcome='switched'`),
			"timeout":   scalarInt(db, `SELECT COUNT(*) FROM musim_paging_log WHERE outcome='timeout'`),
			"rejected":  scalarInt(db, `SELECT COUNT(*) FROM musim_paging_log WHERE outcome='rejected'`),
		},
	}
	return out
}

// ── Groups ──────────────────────────────────────────────────────

// ListGroups returns all groups with members preloaded.
func ListGroups() ([]Group, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, device_id, COALESCE(description,''),
		COALESCE(active_imsi,''), created_at, updated_at
		FROM musim_groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.DeviceID, &g.Description, &g.ActiveIMSI,
			&g.CreatedAt, &g.UpdatedAt); err != nil {
			rows.Close()
			return nil, err
		}
		out = append(out, g)
	}
	rows.Close()
	for i := range out {
		mb, err := membersOf(db, out[i].ID)
		if err != nil {
			return nil, err
		}
		out[i].Members = mb
	}
	if out == nil {
		out = []Group{}
	}
	return out, nil
}

// GetGroup returns a single group with members.
func GetGroup(id int64) (*Group, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, device_id, COALESCE(description,''),
		COALESCE(active_imsi,''), created_at, updated_at
		FROM musim_groups WHERE id=?`, id)
	var g Group
	if err := row.Scan(&g.ID, &g.DeviceID, &g.Description, &g.ActiveIMSI,
		&g.CreatedAt, &g.UpdatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	g.Members, err = membersOf(db, g.ID)
	if err != nil {
		return nil, err
	}
	return &g, nil
}

// CreateGroup creates a group and optionally adds member IMSIs.
// The first IMSI (if any) becomes the active USIM (TS 23.502
// §4.2.6 — the UE picks which USIM is "Allowed" at any time;
// operator-side the active selection is administrative).
func CreateGroup(deviceID, description string, imsis []string) (int64, error) {
	log := logger.Get("musim")
	if deviceID == "" {
		return 0, fmt.Errorf("device_id required")
	}
	if len(imsis) > 0 {
		for _, imsi := range imsis {
			if strings.TrimSpace(imsi) == "" {
				return 0, fmt.Errorf("blank imsi in member list")
			}
		}
	}
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	var active any
	if len(imsis) > 0 {
		active = imsis[0]
	}
	res, err := tx.Exec(`INSERT INTO musim_groups (device_id, description, active_imsi)
		VALUES (?, ?, ?)`, deviceID, description, active)
	if err != nil {
		return 0, err
	}
	gid, _ := res.LastInsertId()
	for i, imsi := range imsis {
		idx := i
		if _, err = tx.Exec(`INSERT INTO musim_group_members
			(group_id, imsi, priority, usim_index) VALUES (?, ?, ?, ?)`,
			gid, imsi, i, idx); err != nil {
			return 0, err
		}
	}
	if err = tx.Commit(); err != nil {
		return 0, err
	}
	log.Infof("MUSIM group created id=%d device_id=%s members=%d", gid, deviceID, len(imsis))
	return gid, nil
}

// UpdateGroup applies a sparse update to a group. Allow-listed
// columns: description, active_imsi.
func UpdateGroup(id int64, patch map[string]any) (bool, error) {
	allowed := map[string]bool{
		"description": true,
		"active_imsi": true,
	}
	cols := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		cols = append(cols, k+"=?")
		args = append(args, v)
	}
	if len(cols) == 0 {
		return false, fmt.Errorf("no allowed fields in patch (description|active_imsi)")
	}
	// If active_imsi is being set, verify membership.
	if v, ok := patch["active_imsi"]; ok {
		imsi, _ := v.(string)
		if imsi != "" {
			db, err := engine.Open()
			if err != nil {
				return false, err
			}
			var n int
			if err := db.QueryRow(`SELECT COUNT(*) FROM musim_group_members
				WHERE group_id=? AND imsi=?`, id, imsi).Scan(&n); err != nil {
				return false, err
			}
			if n == 0 {
				return false, fmt.Errorf("active_imsi %s is not a member of group %d", imsi, id)
			}
		}
	}
	cols = append(cols, "updated_at=datetime('now')")
	args = append(args, id)
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	q := "UPDATE musim_groups SET " + strings.Join(cols, ", ") + " WHERE id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// DeleteGroup removes a group (FK CASCADE drops members).
func DeleteGroup(id int64) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec(`DELETE FROM musim_groups WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// AddMember adds a USIM to a group. Honours the per-IMSI capability
// `max_usim_count` (if known) and rejects duplicate IMSI joins.
func AddMember(groupID int64, imsi string, priority, usimIndex int) (int64, error) {
	if imsi == "" {
		return 0, fmt.Errorf("imsi required")
	}
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	// Verify group exists.
	var ok int
	if err := db.QueryRow(`SELECT COUNT(*) FROM musim_groups WHERE id=?`, groupID).Scan(&ok); err != nil {
		return 0, err
	}
	if ok == 0 {
		return 0, fmt.Errorf("group not found")
	}
	res, err := db.Exec(`INSERT INTO musim_group_members
		(group_id, imsi, priority, usim_index) VALUES (?, ?, ?, ?)`,
		groupID, imsi, priority, usimIndex)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// RemoveMember drops a USIM from its group. If the dropped IMSI was
// active, the group's active_imsi is cleared (the UE may then
// select the next-highest-priority member).
func RemoveMember(memberID int64) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	row := db.QueryRow(`SELECT group_id, imsi FROM musim_group_members WHERE id=?`, memberID)
	var gid int64
	var imsi string
	if err := row.Scan(&gid, &imsi); err != nil {
		if err == sql.ErrNoRows {
			return false, nil
		}
		return false, err
	}
	tx, err := db.Begin()
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()
	if _, err = tx.Exec(`DELETE FROM musim_group_members WHERE id=?`, memberID); err != nil {
		return false, err
	}
	if _, err = tx.Exec(`UPDATE musim_groups SET active_imsi=NULL
		WHERE id=? AND active_imsi=?`, gid, imsi); err != nil {
		return false, err
	}
	if err = tx.Commit(); err != nil {
		return false, err
	}
	return true, nil
}

// ── Capabilities ────────────────────────────────────────────────

// ListCapabilities returns every persisted capability row.
func ListCapabilities() ([]Capability, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, imsi, musim_supported, max_usim_count,
		min_paging_interval_ms, negotiated_at FROM musim_capabilities ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Capability
	for rows.Next() {
		var c Capability
		if err := rows.Scan(&c.ID, &c.IMSI, &c.MUSIMSupported,
			&c.MaxUSIMCount, &c.MinPagingIntervalMS, &c.NegotiatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	if out == nil {
		out = []Capability{}
	}
	return out, rows.Err()
}

// UpsertCapability writes (or replaces) the capability row for an
// IMSI. Mirrors the TS 24.501 §9.11.3.91 NAS-side negotiation —
// operator surface lets the panel pre-seed defaults before the UE
// actually registers.
func UpsertCapability(imsi string, supported bool, maxUSIM, minPagingMS int) error {
	if imsi == "" {
		return fmt.Errorf("imsi required")
	}
	if maxUSIM < 1 {
		maxUSIM = 2 // schema default; UE-typical
	}
	if maxUSIM > 8 {
		return fmt.Errorf("max_usim_count %d > 8 (TS 23.501 §5.34: practical UE limit)", maxUSIM)
	}
	if minPagingMS < 0 {
		return fmt.Errorf("min_paging_interval_ms must be >= 0")
	}
	s := 0
	if supported {
		s = 1
	}
	_, err := engine.Exec(`INSERT INTO musim_capabilities
		(imsi, musim_supported, max_usim_count, min_paging_interval_ms,
		 negotiated_at)
		VALUES (?,?,?,?, datetime('now'))
		ON CONFLICT(imsi) DO UPDATE SET
		  musim_supported=excluded.musim_supported,
		  max_usim_count=excluded.max_usim_count,
		  min_paging_interval_ms=excluded.min_paging_interval_ms,
		  negotiated_at=excluded.negotiated_at`,
		imsi, s, maxUSIM, minPagingMS)
	return err
}

// ── Paging audit log + simulator ────────────────────────────────

// ListPagingLog returns the recent paging audit entries, optionally
// filtered by device_id. Default limit is 200 newest rows.
func ListPagingLog(deviceID string, limit int) ([]PagingEvent, error) {
	if limit <= 0 {
		limit = 200
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT id, device_id, COALESCE(source_imsi,''), target_imsi,
		COALESCE(reason,''), outcome, created_at FROM musim_paging_log`
	args := []any{}
	if deviceID != "" {
		q += ` WHERE device_id=?`
		args = append(args, deviceID)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PagingEvent
	for rows.Next() {
		var p PagingEvent
		if err := rows.Scan(&p.ID, &p.DeviceID, &p.SourceIMSI, &p.TargetIMSI,
			&p.Reason, &p.Outcome, &p.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	if out == nil {
		out = []PagingEvent{}
	}
	return out, rows.Err()
}

// PageResult is the outcome returned by Page().
type PageResult struct {
	Outcome      string `json:"outcome"`
	PriorActive  string `json:"prior_active_imsi,omitempty"`
	NewActive    string `json:"new_active_imsi,omitempty"`
	Reason       string `json:"reason"`
	LogID        int64  `json:"log_id"`
}

// Page simulates the network paging an inactive USIM in a MUSIM
// group (TS 23.502 §4.2.6 — Multi-USIM paging procedures). The
// behaviour mirrors the spec:
//
//   - If the target IMSI IS the currently active member → "delivered".
//   - If the target IMSI is in the group but not active → "switched"
//     (the operator side records the switch; in a real network the
//     UE would issue a MUSIM Busy Indication and the AMF either
//     re-pages or pre-empts the active USIM per §4.2.6).
//   - If the target IMSI is not in the group → "rejected".
//
// `reason` is a free-text string the panel can pass through.
func Page(deviceID, targetIMSI, reason string) (*PageResult, error) {
	log := logger.Get("musim.paging")
	if deviceID == "" || targetIMSI == "" {
		return nil, fmt.Errorf("device_id and target_imsi required")
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, COALESCE(active_imsi,'')
		FROM musim_groups WHERE device_id=?`, deviceID)
	var gid int64
	var active string
	if err := row.Scan(&gid, &active); err != nil {
		if err == sql.ErrNoRows {
			return nil, fmt.Errorf("group not found for device_id %s", deviceID)
		}
		return nil, err
	}
	// Membership check.
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM musim_group_members
		WHERE group_id=? AND imsi=?`, gid, targetIMSI).Scan(&n); err != nil {
		return nil, err
	}
	outcome := "rejected"
	newActive := active
	if n > 0 {
		if active == targetIMSI {
			outcome = "delivered"
		} else {
			outcome = "switched"
			newActive = targetIMSI
			if _, err := db.Exec(`UPDATE musim_groups SET active_imsi=?,
				updated_at=datetime('now') WHERE id=?`, targetIMSI, gid); err != nil {
				return nil, err
			}
		}
	}
	res, err := db.Exec(`INSERT INTO musim_paging_log
		(device_id, source_imsi, target_imsi, reason, outcome)
		VALUES (?, ?, ?, ?, ?)`,
		deviceID, active, targetIMSI, reason, outcome)
	if err != nil {
		return nil, err
	}
	lid, _ := res.LastInsertId()
	log.Infof("MUSIM paging device=%s target=%s outcome=%s", deviceID, targetIMSI, outcome)
	return &PageResult{
		Outcome:     outcome,
		PriorActive: active,
		NewActive:   newActive,
		Reason:      reason,
		LogID:       lid,
	}, nil
}

// ── helpers ─────────────────────────────────────────────────────

func membersOf(db *sql.DB, gid int64) ([]GroupMember, error) {
	rows, err := db.Query(`SELECT id, group_id, imsi, priority, usim_index, joined_at
		FROM musim_group_members WHERE group_id=? ORDER BY priority, id`, gid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GroupMember
	for rows.Next() {
		var m GroupMember
		var idx sql.NullInt64
		if err := rows.Scan(&m.ID, &m.GroupID, &m.IMSI, &m.Priority,
			&idx, &m.JoinedAt); err != nil {
			return nil, err
		}
		if idx.Valid {
			v := int(idx.Int64)
			m.USIMIndex = &v
		}
		out = append(out, m)
	}
	if out == nil {
		out = []GroupMember{}
	}
	return out, rows.Err()
}

func scalarInt(db *sql.DB, q string, args ...any) int {
	var n int
	if err := db.QueryRow(q, args...).Scan(&n); err != nil {
		return 0
	}
	return n
}

// ValidateOutcome surfaces the schema CHECK constraint so the
// route layer can return a clean 400.
func ValidateOutcome(s string) bool { return validOutcomes[s] }
