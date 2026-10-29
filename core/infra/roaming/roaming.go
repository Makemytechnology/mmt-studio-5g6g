// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package roaming — Roaming agreement CRUD + active session tracking.
//
// Tracks inter-PLMN roaming agreements (HPLMN ↔ VPLMN, Home-Routed
// vs Local Break-Out), active roaming sessions, and provides the
// agreement lookup the AMF Initial-Registration path uses to decide
// whether to admit a UE from a partner PLMN.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.501 §5.6.3        Session Management — Roaming. The
//                             authoritative clause defining HR vs
//                             LBO architecture and the SMF/UPF roles
//                             in each.
//   - TS 23.501 §5.7.1.11     QoS aspects of home-routed roaming
//                             (the v-PCF / h-PCF coordination shape
//                             that drives our Agreement.RoamingMode).
//   - TS 23.501 §5.17.4       Network sharing support and interworking
//                             between EPS and 5GS — referenced when an
//                             agreement carries SST/DNN restrictions
//                             that interact with shared RAN.
//
// Deferred (TODO at unimplemented surfaces):
//
//   - TS 23.501 §5.34         Specific SMF Service Areas — a pre-Rel-17
//                             optimisation for HR roaming that picks
//                             an I-SMF in the visited network. Today
//                             the SMF endpoint in an Agreement is the
//                             home-PLMN SMF; the I-SMF selection path
//                             is left to the SMF service handler.
//   - TODO(spec: TS 29.510)   Nnrf-disc bootstrap of partner-PLMN NF
//                             endpoints — today the operator hard-codes
//                             AUSF / UDM / SMF / SEPP URIs in the
//                             agreement row.
package roaming

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("roaming")

// ═══════════════════════════════════════════════════════════
// Agreement — roaming_agreements row (TS 23.501 §5.6.3)
// ═══════════════════════════════════════════════════════════

// Agreement represents a roaming agreement with a partner PLMN.
type Agreement struct {
	ID            int64  `json:"id"`
	PartnerPLMNID string `json:"partner_plmn_id"`
	PartnerName   string `json:"partner_name"`
	Direction     string `json:"direction"`    // inbound | outbound | both
	RoamingMode   string `json:"roaming_mode"` // hr | lbo | both
	MaxUEs        int    `json:"max_ues"`
	AllowedSST    string `json:"allowed_sst"`
	AllowedDNN    string `json:"allowed_dnn"`
	AUSFEndpoint  string `json:"ausf_endpoint"`
	UDMEndpoint   string `json:"udm_endpoint"`
	SMFEndpoint   string `json:"smf_endpoint"`
	SEPPEndpoint  string `json:"sepp_endpoint"`
	Enabled       int    `json:"enabled"`
}

// CreateAgreement upserts a roaming agreement (INSERT ... ON CONFLICT UPDATE).
func CreateAgreement(partnerPLMNID, partnerName, direction, roamingMode string,
	maxUEs int, allowedSST, allowedDNN, ausfEP, udmEP, smfEP, seppEP string) error {

	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO roaming_agreements
		(partner_plmn_id, partner_name, direction, roaming_mode, max_ues,
		 allowed_sst, allowed_dnn, ausf_endpoint, udm_endpoint,
		 smf_endpoint, sepp_endpoint)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(partner_plmn_id) DO UPDATE SET
			partner_name=excluded.partner_name, direction=excluded.direction,
			roaming_mode=excluded.roaming_mode, max_ues=excluded.max_ues,
			allowed_sst=excluded.allowed_sst, allowed_dnn=excluded.allowed_dnn,
			ausf_endpoint=excluded.ausf_endpoint, udm_endpoint=excluded.udm_endpoint,
			smf_endpoint=excluded.smf_endpoint, sepp_endpoint=excluded.sepp_endpoint`,
		partnerPLMNID, partnerName, direction, roamingMode, maxUEs,
		allowedSST, allowedDNN, ausfEP, udmEP, smfEP, seppEP)
	if err == nil {
		log.Info("roaming agreement upserted", "partner", partnerPLMNID,
			"dir", direction, "mode", roamingMode)
	}
	return err
}

// DeleteAgreement removes a roaming agreement by partner PLMN ID.
func DeleteAgreement(partnerPLMNID string) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM roaming_agreements WHERE partner_plmn_id=?", partnerPLMNID)
	return err
}

// GetAgreement returns an enabled agreement for the given partner PLMN, or nil.
func GetAgreement(partnerPLMNID string) (*Agreement, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT id, partner_plmn_id, partner_name, direction,
		roaming_mode, max_ues, allowed_sst, allowed_dnn,
		ausf_endpoint, udm_endpoint, smf_endpoint, sepp_endpoint, enabled
		FROM roaming_agreements WHERE partner_plmn_id=? AND enabled=1`, partnerPLMNID)
	a := &Agreement{}
	err = row.Scan(&a.ID, &a.PartnerPLMNID, &a.PartnerName, &a.Direction,
		&a.RoamingMode, &a.MaxUEs, &a.AllowedSST, &a.AllowedDNN,
		&a.AUSFEndpoint, &a.UDMEndpoint, &a.SMFEndpoint, &a.SEPPEndpoint, &a.Enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a, nil
}

// ListAgreements returns all roaming agreements, optionally filtered to enabled only.
func ListAgreements(enabledOnly bool) ([]Agreement, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT id, partner_plmn_id, partner_name, direction,
		roaming_mode, max_ues, allowed_sst, allowed_dnn,
		ausf_endpoint, udm_endpoint, smf_endpoint, sepp_endpoint, enabled
		FROM roaming_agreements`
	if enabledOnly {
		q += " WHERE enabled=1"
	}
	q += " ORDER BY partner_plmn_id"
	rows, err := db.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Agreement
	for rows.Next() {
		var a Agreement
		if err := rows.Scan(&a.ID, &a.PartnerPLMNID, &a.PartnerName, &a.Direction,
			&a.RoamingMode, &a.MaxUEs, &a.AllowedSST, &a.AllowedDNN,
			&a.AUSFEndpoint, &a.UDMEndpoint, &a.SMFEndpoint, &a.SEPPEndpoint,
			&a.Enabled); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out, nil
}

// SetAgreementEnabled enables or disables an agreement.
func SetAgreementEnabled(partnerPLMNID string, enabled bool) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	val := 0
	if enabled {
		val = 1
	}
	_, err = db.Exec("UPDATE roaming_agreements SET enabled=? WHERE partner_plmn_id=?",
		val, partnerPLMNID)
	return err
}

// IsRoamingAllowed checks if inbound roaming is allowed for a UE from this HPLMN.
// Returns the agreement if allowed, nil otherwise.
func IsRoamingAllowed(homePLMNID string) (*Agreement, error) {
	a, err := GetAgreement(homePLMNID)
	if err != nil || a == nil {
		return nil, err
	}
	if a.Direction != "inbound" && a.Direction != "both" {
		return nil, nil
	}
	return a, nil
}

// GetRoamingMode returns the roaming mode for a partner PLMN: "hr", "lbo", or "".
func GetRoamingMode(homePLMNID string) (string, error) {
	a, err := IsRoamingAllowed(homePLMNID)
	if err != nil || a == nil {
		return "", err
	}
	return a.RoamingMode, nil
}

// ═══════════════════════════════════════════════════════════
// RoamingDetection — detect roaming from IMSI
// ═══════════════════════════════════════════════════════════

// DetectResult is the result of roaming detection for a UE.
type DetectResult struct {
	IsRoaming   bool       `json:"is_roaming"`
	HomePLMNID  string     `json:"home_plmn_id"`
	Agreement   *Agreement `json:"agreement,omitempty"`
	RoamingMode string     `json:"roaming_mode,omitempty"`
}

// DetectRoaming checks if a UE is roaming based on its IMSI prefix.
// Derives the home PLMN from the IMSI and looks up the roaming agreement.
func DetectRoaming(imsi string) *DetectResult {
	if len(imsi) < 5 {
		return nil
	}
	// Derive home PLMN from IMSI — try 3-digit MNC then 2-digit
	mcc := imsi[:3]
	for _, mncLen := range []int{3, 2} {
		if len(imsi) < 3+mncLen {
			continue
		}
		mnc := imsi[3 : 3+mncLen]
		candidate := fmt.Sprintf("%s-%s", mcc, mnc)
		a, err := GetAgreement(candidate)
		if err == nil && a != nil {
			allowed, _ := IsRoamingAllowed(candidate)
			if allowed == nil {
				log.Warn("roaming UE — agreement exists but direction not allowed",
					"imsi", imsi, "hplmn", candidate)
				return &DetectResult{IsRoaming: true, HomePLMNID: candidate}
			}
			mode := allowed.RoamingMode
			log.Info("roaming UE detected", "imsi", imsi, "hplmn", candidate, "mode", mode)
			return &DetectResult{
				IsRoaming:   true,
				HomePLMNID:  candidate,
				Agreement:   allowed,
				RoamingMode: mode,
			}
		}
	}
	// No agreement found
	homePLMN := fmt.Sprintf("%s-%s", mcc, imsi[3:5])
	log.Warn("roaming UE — no roaming agreement", "imsi", imsi, "hplmn", homePLMN)
	return &DetectResult{IsRoaming: true, HomePLMNID: homePLMN}
}

// ═══════════════════════════════════════════════════════════
// Session — roaming_sessions tracking
// ═══════════════════════════════════════════════════════════

// Session is a roaming_sessions row.
type Session struct {
	ID            int64   `json:"id"`
	IMSI          string  `json:"imsi"`
	HomePLMNID    string  `json:"home_plmn_id"`
	VisitedPLMNID string  `json:"visited_plmn_id"`
	Direction     string  `json:"direction"`    // inbound | outbound
	RoamingMode   string  `json:"roaming_mode"` // hr | lbo
	PDUSessionID  *int    `json:"pdu_session_id,omitempty"`
	StartTime     float64 `json:"start_time"`
	EndTime       *float64 `json:"end_time,omitempty"`
	Status        string  `json:"status"` // active | released | failed
}

// CreateSession inserts a new roaming session record.
func CreateSession(imsi, homePLMNID, visitedPLMNID, direction, roamingMode string,
	pduSessionID *int) error {

	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`INSERT INTO roaming_sessions
		(imsi, home_plmn_id, visited_plmn_id, direction, roaming_mode,
		 pdu_session_id, start_time)
		VALUES (?, ?, ?, ?, ?, ?, ?)`,
		imsi, homePLMNID, visitedPLMNID, direction, roamingMode,
		pduSessionID, float64(time.Now().Unix()))
	return err
}

// ReleaseSession marks active sessions as released.
func ReleaseSession(imsi string, pduSessionID *int) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	now := float64(time.Now().Unix())
	if pduSessionID != nil {
		_, err = db.Exec(`UPDATE roaming_sessions SET status='released', end_time=?
			WHERE imsi=? AND pdu_session_id=? AND status='active'`,
			now, imsi, *pduSessionID)
	} else {
		_, err = db.Exec(`UPDATE roaming_sessions SET status='released', end_time=?
			WHERE imsi=? AND status='active'`, now, imsi)
	}
	return err
}

// GetActiveSessions returns active roaming sessions (limit default 100).
func GetActiveSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 100
	}
	return querySessions(`SELECT id, imsi, home_plmn_id, visited_plmn_id, direction,
		roaming_mode, pdu_session_id, start_time, end_time, status
		FROM roaming_sessions WHERE status='active' ORDER BY start_time DESC LIMIT ?`, limit)
}

// GetSessionsForIMSI returns all sessions for a given IMSI.
func GetSessionsForIMSI(imsi string) ([]Session, error) {
	return querySessions(`SELECT id, imsi, home_plmn_id, visited_plmn_id, direction,
		roaming_mode, pdu_session_id, start_time, end_time, status
		FROM roaming_sessions WHERE imsi=? ORDER BY start_time DESC`, imsi)
}

func querySessions(query string, args ...any) ([]Session, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var pduSess sql.NullInt64
		var endTime sql.NullFloat64
		if err := rows.Scan(&s.ID, &s.IMSI, &s.HomePLMNID, &s.VisitedPLMNID,
			&s.Direction, &s.RoamingMode, &pduSess, &s.StartTime,
			&endTime, &s.Status); err != nil {
			continue
		}
		if pduSess.Valid {
			v := int(pduSess.Int64)
			s.PDUSessionID = &v
		}
		if endTime.Valid {
			s.EndTime = &endTime.Float64
		}
		out = append(out, s)
	}
	return out, nil
}

// ═══════════════════════════════════════════════════════════
// Stats
// ═══════════════════════════════════════════════════════════

// Stats contains roaming statistics.
type Stats struct {
	ActiveSessions  int `json:"active_sessions"`
	InboundActive   int `json:"inbound_active"`
	OutboundActive  int `json:"outbound_active"`
	TotalSessions   int `json:"total_sessions"`
}

// GetStats returns roaming session statistics.
func GetStats() (*Stats, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	s := &Stats{}
	db.QueryRow("SELECT COUNT(*) FROM roaming_sessions WHERE status='active'").Scan(&s.ActiveSessions)
	db.QueryRow("SELECT COUNT(*) FROM roaming_sessions WHERE status='active' AND direction='inbound'").Scan(&s.InboundActive)
	db.QueryRow("SELECT COUNT(*) FROM roaming_sessions WHERE status='active' AND direction='outbound'").Scan(&s.OutboundActive)
	db.QueryRow("SELECT COUNT(*) FROM roaming_sessions").Scan(&s.TotalSessions)
	return s, nil
}

// ═══════════════════════════════════════════════════════════
// CDR — roaming_cdrs (TS 32.298 / TAP 3.12 Rel-15 wholesale)
// ═══════════════════════════════════════════════════════════
//
// Roaming CDRs accumulate per session and are exported in TAP-style
// batches to the partner PLMN's clearing house. The "export" step is
// stubbed today: it flips `exported=1` and counts rows. Wire-format
// TAP encoding is a follow-up — see TODO in TS 32.298.

// CDR is a roaming_cdrs row.
type CDR struct {
	ID            int64    `json:"id"`
	IMSI          string   `json:"imsi"`
	HomePLMNID    string   `json:"home_plmn_id"`
	VisitedPLMNID string   `json:"visited_plmn_id"`
	Direction     string   `json:"direction"`
	RecordType    string   `json:"record_type"`
	DNN           *string  `json:"dnn,omitempty"`
	SST           *int     `json:"sst,omitempty"`
	StartTime     string   `json:"start_time"`
	EndTime       *string  `json:"end_time,omitempty"`
	BytesUL       int64    `json:"bytes_ul"`
	BytesDL       int64    `json:"bytes_dl"`
	DurationSec   float64  `json:"duration_sec"`
	Cause         *string  `json:"cause,omitempty"`
	Exported      int      `json:"exported"`
}

// CreateCDR inserts a new roaming CDR for a closed session.
func CreateCDR(imsi, homePLMNID, visitedPLMNID, direction, recordType string,
	dnn *string, sst *int, bytesUL, bytesDL int64,
	durationSec float64, cause *string) (int64, error) {

	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`INSERT INTO roaming_cdrs
		(imsi, home_plmn_id, visited_plmn_id, direction, record_type,
		 dnn, sst, bytes_ul, bytes_dl, duration_sec, cause)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		imsi, homePLMNID, visitedPLMNID, direction, recordType,
		dnn, sst, bytesUL, bytesDL, durationSec, cause)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

// CDRStats returns aggregate counts for the dashboard panel.
type CDRStats struct {
	TotalCDRs  int   `json:"total_cdrs"`
	Unexported int   `json:"unexported"`
	TotalBytes int64 `json:"total_bytes"`
}

// GetCDRStats returns CDR aggregates for the panel.
func GetCDRStats() (*CDRStats, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	s := &CDRStats{}
	db.QueryRow("SELECT COUNT(*) FROM roaming_cdrs").Scan(&s.TotalCDRs)
	db.QueryRow("SELECT COUNT(*) FROM roaming_cdrs WHERE exported=0").Scan(&s.Unexported)
	db.QueryRow(`SELECT COALESCE(SUM(bytes_ul) + SUM(bytes_dl), 0)
		FROM roaming_cdrs`).Scan(&s.TotalBytes)
	return s, nil
}

// ListCDRs returns the latest CDRs (limit default 100).
func ListCDRs(limit int) ([]CDR, error) {
	if limit <= 0 {
		limit = 100
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, imsi, home_plmn_id, visited_plmn_id,
		direction, record_type, dnn, sst, start_time, end_time,
		bytes_ul, bytes_dl, duration_sec, cause, exported
		FROM roaming_cdrs ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CDR
	for rows.Next() {
		var c CDR
		var dnn, endTime, cause sql.NullString
		var sst sql.NullInt64
		if err := rows.Scan(&c.ID, &c.IMSI, &c.HomePLMNID, &c.VisitedPLMNID,
			&c.Direction, &c.RecordType, &dnn, &sst, &c.StartTime, &endTime,
			&c.BytesUL, &c.BytesDL, &c.DurationSec, &cause, &c.Exported); err != nil {
			continue
		}
		if dnn.Valid {
			v := dnn.String
			c.DNN = &v
		}
		if sst.Valid {
			v := int(sst.Int64)
			c.SST = &v
		}
		if endTime.Valid {
			v := endTime.String
			c.EndTime = &v
		}
		if cause.Valid {
			v := cause.String
			c.Cause = &v
		}
		out = append(out, c)
	}
	return out, nil
}

// ExportPendingCDRs flips `exported=1` on every unexported row and
// returns the count. The TAP/RAP wire-encoding is deferred — see
// TODO in TS 32.298.
func ExportPendingCDRs() (int, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec("UPDATE roaming_cdrs SET exported=1 WHERE exported=0")
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}
