// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package sepp_policy — Operator policy layer for the SEPP (TS 29.573).
//
// The SEPP itself (`infra/roaming/sepp`) is a transparent N32-f
// reverse proxy with TLS termination. This package is the
// **operator-side admission and topology-hiding contract** the proxy
// would consult on every inter-PLMN request:
//
//   - Peer-PLMN allow-list (TS 29.573 §5.2 N32-c capability set, TS
//     33.501 §13.1 SBI mutual-TLS at the PLMN border) — which PLMN
//     IDs and FQDNs are allowed to talk N32 to this 5GC.
//   - Topology hiding (TS 29.573 §5.3.x) — per-peer rules for hiding
//     internal NF FQDNs / callback URLs, strip-header lists, and an
//     optional FQDN rewrite.
//   - N32 audit log — every forwarded / rejected / rewritten request
//     is recorded for forensics + billing.
//
// The proxy at `infra/roaming/sepp/sepp.go` is the data-plane; this
// package owns the persisted state and exposes the admission verb
// `CheckPeerAccess(plmn, path)` that the proxy will eventually
// consult (TODO at the proxy site — see proxyHandler).
package sepp_policy

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("sepp_policy")

// ── Status / vocabulary ──────────────────────────────────────────

const (
	StatusActive   = "active"
	StatusInactive = "inactive"
	StatusBlocked  = "blocked"
)

// ValidStatus reports whether s is in the schema CHECK set.
func ValidStatus(s string) bool {
	switch s {
	case StatusActive, StatusInactive, StatusBlocked:
		return true
	}
	return false
}

// ── Peers (TS 33.501 §13.1 SBI border auth) ───────────────────────

// Peer is a row in sepp_peers — one peer PLMN.
type Peer struct {
	ID            int64  `json:"id"`
	PlmnID        string `json:"plmn_id"`
	FQDN          string `json:"fqdn"`
	PublicSAN     string `json:"public_san"`
	AllowedPaths  string `json:"allowed_paths"` // CSV; empty = all paths
	Status        string `json:"status"`
	Description   string `json:"description"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

// CreatePeer inserts a peer; returns the new row.
func CreatePeer(p Peer) (*Peer, error) {
	if p.PlmnID == "" || p.FQDN == "" {
		return nil, fmt.Errorf("plmn_id and fqdn required")
	}
	if p.Status == "" {
		p.Status = StatusActive
	}
	if !ValidStatus(p.Status) {
		return nil, fmt.Errorf("invalid status %q (active|inactive|blocked)", p.Status)
	}
	res, err := engine.Exec(`INSERT INTO sepp_peers
		(plmn_id, fqdn, public_san, allowed_paths, status, description)
		VALUES (?,?,?,?,?,?)`,
		p.PlmnID, p.FQDN, p.PublicSAN, p.AllowedPaths, p.Status, p.Description)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	log.Infof("SEPP peer added: id=%d plmn=%s fqdn=%s status=%s",
		id, p.PlmnID, p.FQDN, p.Status)
	return GetPeer(id)
}

// GetPeer returns one row or nil if not found.
func GetPeer(id int64) (*Peer, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, plmn_id, fqdn, public_san, allowed_paths,
		status, description, created_at, updated_at
		FROM sepp_peers WHERE id=?`, id)
	var p Peer
	var san, allowedPaths, desc, createdAt, updatedAt sql.NullString
	if err := row.Scan(&p.ID, &p.PlmnID, &p.FQDN, &san, &allowedPaths,
		&p.Status, &desc, &createdAt, &updatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.PublicSAN, p.AllowedPaths = san.String, allowedPaths.String
	p.Description, p.CreatedAt, p.UpdatedAt = desc.String, createdAt.String, updatedAt.String
	return &p, nil
}

// GetPeerByPLMN looks up a peer by PLMN-id (the panel + admission
// path use this).
func GetPeerByPLMN(plmnID string) (*Peer, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var id int64
	err = db.QueryRow(`SELECT id FROM sepp_peers WHERE plmn_id=?`, plmnID).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return GetPeer(id)
}

// ListPeers returns all peers ordered by plmn_id; optional status filter.
func ListPeers(status string) ([]Peer, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT id, plmn_id, fqdn, public_san, allowed_paths, status,
		description, created_at, updated_at FROM sepp_peers`
	var rows *sql.Rows
	if status != "" {
		q += ` WHERE status=? ORDER BY plmn_id`
		rows, err = db.Query(q, status)
	} else {
		q += ` ORDER BY plmn_id`
		rows, err = db.Query(q)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Peer
	for rows.Next() {
		var p Peer
		var san, allowedPaths, desc, createdAt, updatedAt sql.NullString
		if rows.Scan(&p.ID, &p.PlmnID, &p.FQDN, &san, &allowedPaths,
			&p.Status, &desc, &createdAt, &updatedAt) != nil {
			continue
		}
		p.PublicSAN, p.AllowedPaths = san.String, allowedPaths.String
		p.Description, p.CreatedAt, p.UpdatedAt = desc.String, createdAt.String, updatedAt.String
		out = append(out, p)
	}
	return out, nil
}

// UpdatePeer patches allow-listed columns. Returns the new row.
func UpdatePeer(id int64, fields map[string]interface{}) (*Peer, error) {
	allowed := map[string]struct{}{
		"fqdn": {}, "public_san": {}, "allowed_paths": {},
		"status": {}, "description": {},
	}
	sets, args := []string{}, []interface{}{}
	for k, v := range fields {
		if _, ok := allowed[k]; !ok {
			continue
		}
		if k == "status" {
			s, _ := v.(string)
			if !ValidStatus(s) {
				return nil, fmt.Errorf("invalid status %q", s)
			}
		}
		sets = append(sets, k+"=?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return GetPeer(id)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, time.Now().UTC().Format("2006-01-02 15:04:05"))
	args = append(args, id)
	q := "UPDATE sepp_peers SET " + strings.Join(sets, ", ") + " WHERE id=?"
	if _, err := engine.Exec(q, args...); err != nil {
		return nil, err
	}
	return GetPeer(id)
}

// DeletePeer drops a peer and its topology-hiding row (FK CASCADE).
func DeletePeer(id int64) (bool, error) {
	res, err := engine.Exec(`DELETE FROM sepp_peers WHERE id=?`, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// ── Topology hiding (TS 29.573 §5.3.x) ────────────────────────────

// TopologyHiding mirrors a row from sepp_topology_hiding.
type TopologyHiding struct {
	ID                int64  `json:"id"`
	PeerID            int64  `json:"peer_id"`
	HideInternalFQDN  bool   `json:"hide_internal_fqdn"`
	HideCallbacks     bool   `json:"hide_callbacks"`
	ReplaceFQDN       string `json:"replace_fqdn"`
	StripHeaders      string `json:"strip_headers"` // CSV
	UpdatedAt         string `json:"updated_at"`
}

// UpsertTopologyHiding inserts or updates the per-peer rule (one row
// per peer; FK + UNIQUE on peer_id makes it 1:1).
func UpsertTopologyHiding(t TopologyHiding) (*TopologyHiding, error) {
	if t.PeerID == 0 {
		return nil, fmt.Errorf("peer_id required")
	}
	hideFQDN, hideCB := 0, 0
	if t.HideInternalFQDN {
		hideFQDN = 1
	}
	if t.HideCallbacks {
		hideCB = 1
	}
	_, err := engine.Exec(`INSERT INTO sepp_topology_hiding
		(peer_id, hide_internal_fqdn, hide_callbacks, replace_fqdn, strip_headers, updated_at)
		VALUES (?,?,?,?,?, datetime('now'))
		ON CONFLICT(peer_id) DO UPDATE SET
			hide_internal_fqdn=excluded.hide_internal_fqdn,
			hide_callbacks=excluded.hide_callbacks,
			replace_fqdn=excluded.replace_fqdn,
			strip_headers=excluded.strip_headers,
			updated_at=datetime('now')`,
		t.PeerID, hideFQDN, hideCB, t.ReplaceFQDN, t.StripHeaders)
	if err != nil {
		return nil, err
	}
	return GetTopologyHiding(t.PeerID)
}

// GetTopologyHiding returns the rule for a peer; nil if none set
// (default policy is "hide everything" per §5.3.x).
func GetTopologyHiding(peerID int64) (*TopologyHiding, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var t TopologyHiding
	var hideFQDN, hideCB int
	var replaceFQDN, stripHeaders, updatedAt sql.NullString
	err = db.QueryRow(`SELECT id, peer_id, hide_internal_fqdn, hide_callbacks,
		replace_fqdn, strip_headers, updated_at
		FROM sepp_topology_hiding WHERE peer_id=?`, peerID).Scan(
		&t.ID, &t.PeerID, &hideFQDN, &hideCB,
		&replaceFQDN, &stripHeaders, &updatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	t.HideInternalFQDN, t.HideCallbacks = hideFQDN == 1, hideCB == 1
	t.ReplaceFQDN, t.StripHeaders = replaceFQDN.String, stripHeaders.String
	t.UpdatedAt = updatedAt.String
	return &t, nil
}

// ListTopologyHiding returns every per-peer rule.
func ListTopologyHiding() ([]TopologyHiding, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, peer_id, hide_internal_fqdn, hide_callbacks,
		replace_fqdn, strip_headers, updated_at FROM sepp_topology_hiding ORDER BY peer_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TopologyHiding
	for rows.Next() {
		var t TopologyHiding
		var hideFQDN, hideCB int
		var replaceFQDN, stripHeaders, updatedAt sql.NullString
		if rows.Scan(&t.ID, &t.PeerID, &hideFQDN, &hideCB,
			&replaceFQDN, &stripHeaders, &updatedAt) != nil {
			continue
		}
		t.HideInternalFQDN, t.HideCallbacks = hideFQDN == 1, hideCB == 1
		t.ReplaceFQDN, t.StripHeaders = replaceFQDN.String, stripHeaders.String
		t.UpdatedAt = updatedAt.String
		out = append(out, t)
	}
	return out, nil
}

// DeleteTopologyHiding drops the per-peer rule. Falls back to default
// (hide everything) on the next request.
func DeleteTopologyHiding(peerID int64) error {
	_, err := engine.Exec(`DELETE FROM sepp_topology_hiding WHERE peer_id=?`, peerID)
	return err
}

// ── Admission gate ───────────────────────────────────────────────

// AccessResult is what CheckPeerAccess returns. The proxy at
// `infra/roaming/sepp/sepp.go` would consult this on every inbound
// request and reject early on Allowed=false.
type AccessResult struct {
	Allowed bool   `json:"allowed"`
	Reason  string `json:"reason"`
	PeerID  int64  `json:"peer_id,omitempty"`
	Status  string `json:"status,omitempty"`
}

// CheckPeerAccess decides whether a peer-PLMN's request to `path` is
// admissible. Default-deny: an unknown PLMN is rejected even if the
// proxy already terminated TLS — the cert check handles identity,
// this surface handles **authorisation**.
func CheckPeerAccess(plmnID, path string) AccessResult {
	if plmnID == "" {
		LogN32(plmnID, "inbound", path, "", 0, 0, "rejected", "empty plmn")
		return AccessResult{Allowed: false, Reason: "empty plmn"}
	}
	p, err := GetPeerByPLMN(plmnID)
	if err != nil {
		log.Errorf("sepp peer lookup: %v", err)
		LogN32(plmnID, "inbound", path, "", 0, 0, "rejected", "lookup error")
		return AccessResult{Allowed: false, Reason: "lookup error"}
	}
	if p == nil {
		LogN32(plmnID, "inbound", path, "", 0, 0, "rejected", "unknown peer")
		return AccessResult{Allowed: false, Reason: "unknown peer (default-deny)"}
	}
	if p.Status != StatusActive {
		LogN32(plmnID, "inbound", path, "", 0, 0, "rejected",
			"peer status="+p.Status)
		return AccessResult{Allowed: false, Reason: "peer " + p.Status,
			PeerID: p.ID, Status: p.Status}
	}
	// Path filter: allowed_paths CSV; empty = all paths permitted.
	if p.AllowedPaths != "" && path != "" {
		if !pathInList(path, p.AllowedPaths) {
			LogN32(plmnID, "inbound", path, "", 0, 0, "rejected",
				"path not in allowed_paths")
			return AccessResult{Allowed: false,
				Reason: "path not in allowed_paths",
				PeerID: p.ID, Status: p.Status}
		}
	}
	return AccessResult{Allowed: true, Reason: "matched active peer",
		PeerID: p.ID, Status: p.Status}
}

func pathInList(path, csv string) bool {
	for _, raw := range strings.FieldsFunc(csv, func(r rune) bool {
		return r == ',' || r == ';' || r == ' '
	}) {
		prefix := strings.TrimSpace(raw)
		if prefix == "" {
			continue
		}
		// Prefix match — operators allow-list things like
		// "/nudm-uecm/v1" without listing every path under it.
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}

// ── N32 audit log ─────────────────────────────────────────────────

// LogN32 records one N32 transaction. Best-effort; never fails the
// caller on a logging error.
func LogN32(peerPLMN, direction, path, method string,
	statusCode, latencyMs int, action, reason string) {
	if direction != "inbound" && direction != "outbound" {
		direction = "inbound"
	}
	switch action {
	case "forwarded", "rejected", "rewritten":
	default:
		action = "forwarded"
	}
	if _, err := engine.Exec(`INSERT INTO sepp_n32_log
		(peer_plmn, direction, path, method, status_code, latency_ms, action, reason)
		VALUES (?,?,?,?,?,?,?,?)`,
		peerPLMN, direction, path, method, statusCode, latencyMs, action, reason); err != nil {
		log.Warnf("sepp_n32_log insert failed: %v", err)
	}
}

// ListN32Log returns recent transactions. Optional peer / action / direction filters.
func ListN32Log(filterPeer, filterAction, filterDirection string, limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 100
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT id, peer_plmn, direction, path, method, status_code,
		latency_ms, action, reason, created_at FROM sepp_n32_log WHERE 1=1`
	var args []interface{}
	if filterPeer != "" {
		q += ` AND peer_plmn=?`
		args = append(args, filterPeer)
	}
	if filterAction != "" {
		q += ` AND action=?`
		args = append(args, filterAction)
	}
	if filterDirection != "" {
		q += ` AND direction=?`
		args = append(args, filterDirection)
	}
	q += ` ORDER BY id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var id, status, latency int64
		var peer, dir, path, method, action, reason, createdAt string
		if rows.Scan(&id, &peer, &dir, &path, &method, &status, &latency,
			&action, &reason, &createdAt) != nil {
			continue
		}
		out = append(out, map[string]interface{}{
			"id":          id,
			"peer_plmn":   peer,
			"direction":   dir,
			"path":        path,
			"method":      method,
			"status_code": status,
			"latency_ms":  latency,
			"action":      action,
			"reason":      reason,
			"created_at":  createdAt,
		})
	}
	return out, nil
}

// ── Stats / status ───────────────────────────────────────────────

// GetStats returns a counter snapshot for the dashboard.
func GetStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	q := func(s string) int64 {
		var n int64
		_ = db.QueryRow(s).Scan(&n)
		return n
	}
	return map[string]interface{}{
		"total_peers":     q(`SELECT COUNT(*) FROM sepp_peers`),
		"active_peers":    q(`SELECT COUNT(*) FROM sepp_peers WHERE status='active'`),
		"blocked_peers":   q(`SELECT COUNT(*) FROM sepp_peers WHERE status='blocked'`),
		"hiding_rules":    q(`SELECT COUNT(*) FROM sepp_topology_hiding`),
		"requests_total":  q(`SELECT COUNT(*) FROM sepp_n32_log`),
		"forwarded":       q(`SELECT COUNT(*) FROM sepp_n32_log WHERE action='forwarded'`),
		"rejected":        q(`SELECT COUNT(*) FROM sepp_n32_log WHERE action='rejected'`),
		"rewritten":       q(`SELECT COUNT(*) FROM sepp_n32_log WHERE action='rewritten'`),
	}
}
