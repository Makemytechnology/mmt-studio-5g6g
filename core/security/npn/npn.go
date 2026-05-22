// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package npn -- Non-Public Networks (TS 23.501 section 5.30).
//
// Go port of security/npn/*.py. Manages NPN networks, CAG (Closed Access
// Groups), CAG membership, SNPN authentication, and NPN configuration.
package npn

import (
	"fmt"
	"regexp"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var cagIDRe = regexp.MustCompile(`^[0-9A-Fa-f]{8}$`)

// ValidateCAGID validates CAG-ID format: 8 hex digits (TS 23.501 section 5.30.2).
func ValidateCAGID(cagID string) bool { return cagIDRe.MatchString(cagID) }

// ---- NPN Network CRUD ----

// CreateNPN creates a non-public network.
func CreateNPN(name, npnType, plmn, nid string) (int64, error) {
	if npnType == "" { npnType = "PNI-NPN" }
	res, err := engine.Exec("INSERT INTO npn_networks (name, npn_type, plmn, nid, status) VALUES (?,?,?,?,'active')", name, npnType, plmn, nid)
	if err != nil { return 0, err }
	id, _ := res.LastInsertId()
	logger.Get("npn").Infof("NPN created: id=%d name=%s type=%s", id, name, npnType)
	return id, nil
}

// GetNPN returns a single NPN.
func GetNPN(id int64) (map[string]interface{}, error) { return qRow("SELECT * FROM npn_networks WHERE id=?", id) }

// ListNPNs returns all NPN networks.
func ListNPNs() ([]map[string]interface{}, error) { return qRows("SELECT * FROM npn_networks ORDER BY id") }

// DeleteNPN removes an NPN.
func DeleteNPN(id int64) error { _, err := engine.Exec("DELETE FROM npn_networks WHERE id=?", id); return err }

// ---- CAG CRUD (TS 23.501 section 5.30.2) ----

// CreateCAG creates a Closed Access Group.
func CreateCAG(cagID string, npnID int64, name, description string) (int64, error) {
	if !ValidateCAGID(cagID) { return 0, fmt.Errorf("invalid CAG-ID '%s': must be 8 hex digits", cagID) }
	res, err := engine.Exec("INSERT INTO npn_cag (cag_id, npn_id, name, description) VALUES (?,?,?,?)", cagID, npnID, name, description)
	if err != nil { return 0, err }
	id, _ := res.LastInsertId()
	logger.Get("npn.cag").Infof("CAG created: cag_id=%s npn_id=%d name=%s", cagID, npnID, name)
	return id, nil
}

// GetCAG returns a CAG by row ID.
func GetCAG(id int64) (map[string]interface{}, error) { return qRow("SELECT * FROM npn_cag WHERE id=?", id) }

// ListCAGs returns all CAGs, optionally filtered by NPN.
func ListCAGs(npnID int64) ([]map[string]interface{}, error) {
	if npnID > 0 { return qRows("SELECT * FROM npn_cag WHERE npn_id=? ORDER BY id", npnID) }
	return qRows("SELECT * FROM npn_cag ORDER BY id")
}

// DeleteCAG removes a CAG.
func DeleteCAG(id int64) error { _, err := engine.Exec("DELETE FROM npn_cag WHERE id=?", id); return err }

// ---- CAG Membership ----

// AddMember adds an IMSI to a CAG.
func AddMember(cagRowID int64, imsi string) (int64, error) {
	res, err := engine.Exec("INSERT OR IGNORE INTO npn_cag_members (cag_id, imsi) VALUES (?,?)", cagRowID, imsi)
	if err != nil { return 0, err }
	id, _ := res.LastInsertId()
	return id, nil
}

// RemoveMember removes an IMSI from a CAG.
func RemoveMember(cagRowID int64, imsi string) error {
	_, err := engine.Exec("DELETE FROM npn_cag_members WHERE cag_id=? AND imsi=?", cagRowID, imsi)
	return err
}

// ListMembers returns members of a CAG.
func ListMembers(cagRowID int64) ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM npn_cag_members WHERE cag_id=? ORDER BY imsi", cagRowID)
}

// CheckMembership checks if an IMSI is a member of a CAG.
func CheckMembership(cagID, imsi string) bool {
	db, err := engine.Open()
	if err != nil { return false }
	var count int
	_ = db.QueryRow("SELECT COUNT(*) FROM npn_cag_members m JOIN npn_cag c ON c.id=m.cag_id WHERE c.cag_id=? AND m.imsi=?", cagID, imsi).Scan(&count)
	return count > 0
}

// ---- SNPN Authentication (TS 33.501 section 6.1.4) ----

// AuthenticateSNPN validates SNPN access for a UE.
func AuthenticateSNPN(imsi, cagID, npnNID string) map[string]interface{} {
	log := logger.Get("npn.auth")
	if !ValidateCAGID(cagID) {
		logAccess(imsi, cagID, npnNID, "denied", "invalid CAG-ID format")
		return map[string]interface{}{"allowed": false, "reason": "invalid CAG-ID format"}
	}
	if !CheckMembership(cagID, imsi) {
		log.Infof("SNPN auth denied: IMSI=%s not in CAG=%s", imsi, cagID)
		logAccess(imsi, cagID, npnNID, "denied", "IMSI not in CAG")
		return map[string]interface{}{"allowed": false, "reason": "IMSI not in CAG"}
	}
	log.Infof("SNPN auth allowed: IMSI=%s CAG=%s NID=%s", imsi, cagID, npnNID)
	logAccess(imsi, cagID, npnNID, "admitted", "ok")
	return map[string]interface{}{"allowed": true, "cag_id": cagID, "nid": npnNID}
}

// logAccess persists an NPN admission decision into npn_access_log
// per TS 23.501 §5.30 for OAM/audit. Best-effort: never blocks the
// auth path on a logging failure. Looks up the integer FKs for
// npn_id (by NID) and cag_id (by hex CAG-ID via the DDL table name
// `npn_cag`) so the row is queryable from the GUI; leaves them NULL
// on miss.
func logAccess(imsi, cagID, npnNID, action, reason string) {
	var npnID, cagRowID *int64
	if db, err := engine.Open(); err == nil {
		var n int64
		if err := db.QueryRow(
			`SELECT id FROM npn_networks WHERE nid=? LIMIT 1`,
			npnNID,
		).Scan(&n); err == nil {
			npnID = &n
		}
		var c int64
		if err := db.QueryRow(
			`SELECT id FROM npn_cag WHERE cag_id=? LIMIT 1`,
			cagID,
		).Scan(&c); err == nil {
			cagRowID = &c
		}
	}
	if _, err := engine.Exec(
		`INSERT INTO npn_access_log (imsi, npn_id, cag_id, action, reason)
		 VALUES (?,?,?,?,?)`,
		imsi, npnID, cagRowID, action, reason,
	); err != nil {
		logger.Get("npn.auth").Warnf("npn_access_log INSERT failed: %v", err)
	}
}

// ---- NPN Manager ----

// GetNPNStatus returns NPN status summary.
func GetNPNStatus() map[string]interface{} {
	db, err := engine.Open()
	if err != nil { return map[string]interface{}{} }
	var npnCount, cagCount, memberCount int
	_ = db.QueryRow("SELECT COUNT(*) FROM npn_networks").Scan(&npnCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM npn_cag").Scan(&cagCount)
	_ = db.QueryRow("SELECT COUNT(*) FROM npn_cag_members").Scan(&memberCount)
	return map[string]interface{}{
		"npn_count":    npnCount,
		"cag_count":    cagCount,
		"member_count": memberCount,
	}
}

// ---- GUI panel API ----

// List returns rows from npn_networks.
func List() ([]map[string]any, error) { return ListNPNs() }

// Status returns current state.
func Status() map[string]any {
	list, _ := ListNPNs()
	return map[string]any{"count": len(list)}
}

// ---- helpers ----

func qRow(q string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open(); if err != nil { return nil, err }
	rows, err := db.Query(q, args...); if err != nil { return nil, nil }
	defer rows.Close(); cols, _ := rows.Columns(); if !rows.Next() { return nil, nil }
	vals := make([]interface{}, len(cols)); ptrs := make([]interface{}, len(cols))
	for i := range vals { ptrs[i] = &vals[i] }; rows.Scan(ptrs...)
	m := make(map[string]interface{}, len(cols)); for i, c := range cols { m[c] = vals[i] }; return m, nil
}

func qRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open(); if err != nil { return nil, err }
	rows, err := db.Query(q, args...); if err != nil { return nil, nil }
	defer rows.Close(); cols, _ := rows.Columns(); var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols)); ptrs := make([]interface{}, len(cols))
		for i := range vals { ptrs[i] = &vals[i] }; rows.Scan(ptrs...)
		m := make(map[string]interface{}, len(cols)); for i, c := range cols { m[c] = vals[i] }; out = append(out, m)
	}; return out, nil
}
