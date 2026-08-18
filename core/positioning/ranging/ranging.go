// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ranging — Ranging and Sidelink (SL) Positioning service
// management. Local persistence of the architectural roles defined
// in TS 23.586 §4 and the procedures in §6.
//
// Spec anchors:
//   - TS 23.586 §4.1 General concept — Ranging/SL Positioning
//     enables UE-to-UE distance/angle determination over PC5.
//   - TS 23.586 §5.1 Authorization and Provisioning — UE / NG-RAN
//     authorisation; CreatePrivacyEntry persists per-pair consent.
//   - TS 23.586 §5.2 UE Discovery & Selection — Located /
//     SL Positioning Server / SL Reference UE roles.
//   - TS 23.586 §5.3 Ranging/SL Positioning control — session
//     lifecycle Discover → Initiate → Measure → Result is what
//     InitiateRanging persists locally.
//   - TS 23.586 §6.4 Procedures for UE Discovery — concrete
//     5G ProSe / V2X discovery flow that produces (sourceIMSI,
//     targetIMSI) pairs feeding InitiateRanging.
//   - TS 23.586 §6.8 Procedures of Ranging/SL Positioning control
//     — controls SL positioning at session granularity (start /
//     stop / report).
//
// Method enum (RTT, AoA, multi-RTT) is operator-named here; the
// underlying RAT-side measurement methods belong to NR positioning
// (TS 38.305) which is not in specs/3gpp/. Values are kept as
// strings rather than as enums to stay close to the on-the-wire
// "ranging method" parameter exchanged over RSPP.
//
// TODO TS 38.305 — once the RAN positioning protocol PDF is loaded,
// anchor the method strings to the RSPP "Ranging Method"
// parameter set and validate accuracy ranges per measurement type.
package ranging

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

func errInvalid(msg string) error { return errors.New(msg) }

// ---- Types ----

// Session represents a row in ranging_sessions.
type Session struct {
	ID           int64    `json:"id"`
	SourceIMSI   string   `json:"source_imsi"`
	TargetIMSI   string   `json:"target_imsi"`
	Method       string   `json:"method"`
	Status       string   `json:"status"`
	DistanceM    *float64 `json:"distance_m,omitempty"`
	AzimuthDeg   *float64 `json:"azimuth_deg,omitempty"`
	ElevationDeg *float64 `json:"elevation_deg,omitempty"`
	AccuracyM    *float64 `json:"accuracy_m,omitempty"`
	CreatedAt    string   `json:"created_at"`
	CompletedAt  *string  `json:"completed_at,omitempty"`
}

// Anchor represents a row in ranging_anchors.
type Anchor struct {
	ID         int64   `json:"id"`
	IMSI       string  `json:"imsi"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	Altitude   float64 `json:"altitude"`
	AnchorType string  `json:"anchor_type"`
	Active     int     `json:"active"`
	CreatedAt  string  `json:"created_at"`
}

// ResultLog represents a row in ranging_results_log.
type ResultLog struct {
	ID              int64   `json:"id"`
	SessionID       int64   `json:"session_id"`
	MeasurementType string  `json:"measurement_type"`
	Value           float64 `json:"value"`
	Unit            string  `json:"unit"`
	Timestamp       string  `json:"timestamp"`
}

// PrivacyEntry mirrors a ranging_privacy row — the per-target UE
// authorisation policy that gates whether a source UE may initiate
// ranging against this target (TS 23.586 §5.1 Authorization and
// Provisioning).
type PrivacyEntry struct {
	ID              int64   `json:"id"`
	IMSI            string  `json:"imsi"`
	Policy          string  `json:"policy"`           // 'allow_all' | 'deny_all' | 'contacts_only'
	AllowedContacts *string `json:"allowed_contacts,omitempty"` // CSV of source IMSIs (only when policy='contacts_only')
	UpdatedAt       string  `json:"updated_at"`
}

// ---- GUI panel API ----

// List returns all sessions (preserves stub API).
func List() ([]Session, error) { return ListSessions("", "") }

// Status returns a summary for the GUI panel.
func Status() map[string]any {
	list, _ := ListSessions("", "")
	return map[string]any{"count": len(list), "items": list}
}

// ---- Session CRUD ----

// InitiateRanging starts a ranging session between two UEs —
// local realisation of the Ranging/SL Positioning control flow
// (TS 23.586 §5.3, procedures TS 23.586 §6.8). Authorisation is
// gated by the per-pair privacy entry that maps to the
// "Authorization and Provisioning" requirement of §5.1 / §6.2.
//
// Measurement values returned here are simulated (random within
// realistic ranges); the spec's measurement transport (RSPP over
// PC5 per §5.3.2) is NOT modelled — only the session-level state
// and result persistence.
func InitiateRanging(sourceIMSI, targetIMSI, method string) (map[string]any, error) {
	if method == "" {
		method = "RTT"
	}
	if method != "RTT" && method != "AoA" && method != "multi_RTT" {
		return map[string]any{"ok": false, "error": "invalid method"}, nil
	}
	// Check privacy
	ok, reason := checkPrivacy(sourceIMSI, targetIMSI)
	if !ok {
		return map[string]any{"ok": false, "error": "authorization_denied", "reason": reason}, nil
	}

	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := engine.Exec(`INSERT INTO ranging_sessions
		(source_imsi, target_imsi, method, status, created_at)
		VALUES (?,?,?,'active',?)`, sourceIMSI, targetIMSI, method, now)
	if err != nil {
		return nil, err
	}
	sessionID, _ := res.LastInsertId()

	// Simulate measurement
	meas := simulateMeasurement(method)
	_, _ = engine.Exec(`UPDATE ranging_sessions SET
		distance_m=?, azimuth_deg=?, elevation_deg=?, accuracy_m=?,
		status='completed', completed_at=? WHERE id=?`,
		meas["distance_m"], meas["azimuth_deg"], meas["elevation_deg"],
		meas["accuracy_m"], now, sessionID)

	// Log individual measurements. Measurement-type → meas-map key
	// is explicit to avoid the per-type-suffix mismatch we used to
	// have (azimuth/elevation are deg-valued but were keyed as "_m").
	for _, m := range []struct{ typ, unit, key string }{
		{"distance", "m", "distance_m"},
		{"azimuth", "deg", "azimuth_deg"},
		{"elevation", "deg", "elevation_deg"},
		{"accuracy", "m", "accuracy_m"},
	} {
		_, _ = engine.Exec(`INSERT INTO ranging_results_log
			(session_id, measurement_type, value, unit, timestamp)
			VALUES (?,?,?,?,?)`, sessionID, m.typ, meas[m.key], m.unit, now)
	}

	return map[string]any{
		"ok": true, "session_id": sessionID, "status": "completed",
		"distance_m": meas["distance_m"], "azimuth_deg": meas["azimuth_deg"],
		"elevation_deg": meas["elevation_deg"], "accuracy_m": meas["accuracy_m"],
	}, nil
}

// GetSession returns a session by ID.
func GetSession(id int64) (*Session, error) {
	row := engine.QueryRow(`SELECT id, source_imsi, target_imsi, method, status,
		distance_m, azimuth_deg, elevation_deg, accuracy_m, created_at, completed_at
		FROM ranging_sessions WHERE id=?`, id)
	s, err := scanSessionRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// ListSessions lists sessions with optional IMSI and status filters.
func ListSessions(imsi, status string) ([]Session, error) {
	q := `SELECT id, source_imsi, target_imsi, method, status,
		distance_m, azimuth_deg, elevation_deg, accuracy_m, created_at, completed_at
		FROM ranging_sessions`
	var where []string
	var args []interface{}
	if imsi != "" {
		where = append(where, "(source_imsi=? OR target_imsi=?)")
		args = append(args, imsi, imsi)
	}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id DESC"
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.SourceIMSI, &s.TargetIMSI, &s.Method,
			&s.Status, &s.DistanceM, &s.AzimuthDeg, &s.ElevationDeg,
			&s.AccuracyM, &s.CreatedAt, &s.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CancelSession cancels an active session.
func CancelSession(id int64) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := engine.Exec(`UPDATE ranging_sessions SET status='cancelled', completed_at=?
		WHERE id=? AND status='active'`, now, id)
	return err
}

// DeleteSession removes a session.
func DeleteSession(id int64) error {
	_, err := engine.Exec(`DELETE FROM ranging_sessions WHERE id=?`, id)
	return err
}

// ---- Anchor CRUD ----

// ListAnchors returns all ranging anchors.
func ListAnchors() ([]Anchor, error) {
	rows, err := engine.Query(`SELECT id, imsi, latitude, longitude, altitude,
		anchor_type, active, created_at FROM ranging_anchors ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Anchor
	for rows.Next() {
		var a Anchor
		if err := rows.Scan(&a.ID, &a.IMSI, &a.Latitude, &a.Longitude,
			&a.Altitude, &a.AnchorType, &a.Active, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// GetAnchor returns a single anchor by ID.
func GetAnchor(id int64) (*Anchor, error) {
	row := engine.QueryRow(`SELECT id, imsi, latitude, longitude, altitude,
		anchor_type, active, created_at FROM ranging_anchors WHERE id=?`, id)
	var a Anchor
	if err := row.Scan(&a.ID, &a.IMSI, &a.Latitude, &a.Longitude,
		&a.Altitude, &a.AnchorType, &a.Active, &a.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	return &a, nil
}

// CreateAnchor inserts a new ranging anchor.
func CreateAnchor(imsi string, lat, lon, alt float64, anchorType string) (int64, error) {
	if anchorType == "" {
		anchorType = "ue"
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	res, err := engine.Exec(`INSERT INTO ranging_anchors
		(imsi, latitude, longitude, altitude, anchor_type, active, created_at)
		VALUES (?,?,?,?,?,1,?)`, imsi, lat, lon, alt, anchorType, now)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteAnchor removes an anchor by ID.
func DeleteAnchor(id int64) error {
	_, err := engine.Exec(`DELETE FROM ranging_anchors WHERE id=?`, id)
	return err
}

// ---- Privacy CRUD (TS 23.586 §5.1 Authorization & Provisioning) ----

var validPolicies = map[string]bool{"allow_all": true, "deny_all": true, "contacts_only": true}

// PositionFix is the result of EstimatePosition — a centroid +
// circumradius accuracy from a set of currently-active anchors.
//
// Per TS 23.586 §6.8 the spec leaves the actual positioning math
// to operator implementation (the spec defines the *measurement*
// transport over PC5, not the geometry). The local stack uses a
// simple centroid for OAM-readable output; production would
// replace this with a multilateration / TDOA solver.
type PositionFix struct {
	TargetIMSI string  `json:"target_imsi"`
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	Altitude   float64 `json:"altitude"`
	AccuracyM  float64 `json:"accuracy_m"`
	AnchorCount int    `json:"anchor_count"`
}

// EstimatePosition picks the centroid of all currently-active
// anchors and returns it as the target's position. AccuracyM is
// the maximum great-circle distance from the centroid to any
// anchor (a conservative bound: real position is somewhere in
// that disc). Returns an error if no active anchors exist.
func EstimatePosition(targetIMSI string) (*PositionFix, error) {
	anchors, err := ListAnchors()
	if err != nil {
		return nil, err
	}
	active := make([]Anchor, 0, len(anchors))
	for _, a := range anchors {
		if a.Active != 0 {
			active = append(active, a)
		}
	}
	if len(active) == 0 {
		return nil, fmt.Errorf("no active anchors registered")
	}
	var sumLat, sumLon, sumAlt float64
	for _, a := range active {
		sumLat += a.Latitude
		sumLon += a.Longitude
		sumAlt += a.Altitude
	}
	n := float64(len(active))
	cLat := sumLat / n
	cLon := sumLon / n
	cAlt := sumAlt / n
	maxKM := 0.0
	for _, a := range active {
		d := haversineKM(cLat, cLon, a.Latitude, a.Longitude)
		if d > maxKM {
			maxKM = d
		}
	}
	return &PositionFix{
		TargetIMSI:  targetIMSI,
		Latitude:    round3(cLat),
		Longitude:   round3(cLon),
		Altitude:    round3(cAlt),
		AccuracyM:   round3(maxKM * 1000.0),
		AnchorCount: len(active),
	}, nil
}

// haversineKM — same shape as the EAS pkg's helper. Inlined here
// rather than importing edge/eas to avoid a pkg-level dep cycle.
func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	const radPerDeg = 0.017453292519943295 // π/180
	dLat := (lat2 - lat1) * radPerDeg
	dLon := (lon2 - lon1) * radPerDeg
	la1 := lat1 * radPerDeg
	la2 := lat2 * radPerDeg
	sdLat := math.Sin(dLat / 2)
	sdLon := math.Sin(dLon / 2)
	a := sdLat*sdLat + math.Cos(la1)*math.Cos(la2)*sdLon*sdLon
	return 2 * R * math.Asin(math.Sqrt(a))
}

// SetPrivacy sets (UPSERTs) the per-target privacy policy for an IMSI.
// Implements the operator-facing half of TS 23.586 §5.1 — the spec
// requires UE / NG-RAN / 5GC authorisation for ranging participation;
// this row is the persisted UE-side consent the InitiateRanging
// authorisation gate consults via checkPrivacy.
//
// allowedContacts is only meaningful when policy='contacts_only';
// pass nil otherwise (will be stored NULL).
func SetPrivacy(imsi, policy string, allowedContacts *string) error {
	if imsi == "" {
		return errInvalid("imsi is required")
	}
	if !validPolicies[policy] {
		return errInvalid("invalid policy: " + policy)
	}
	if policy != "contacts_only" {
		allowedContacts = nil
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := engine.Exec(`INSERT INTO ranging_privacy (imsi, policy, allowed_contacts, updated_at)
		VALUES (?,?,?,?)
		ON CONFLICT(imsi) DO UPDATE SET
		policy=excluded.policy, allowed_contacts=excluded.allowed_contacts,
		updated_at=excluded.updated_at`, imsi, policy, allowedContacts, now)
	return err
}

// GetPrivacy returns the privacy entry for an IMSI, or nil when the
// UE has no explicit policy (which the gate treats as allow-all).
func GetPrivacy(imsi string) (*PrivacyEntry, error) {
	row := engine.QueryRow(`SELECT id, imsi, policy, allowed_contacts, updated_at
		FROM ranging_privacy WHERE imsi=?`, imsi)
	var p PrivacyEntry
	err := row.Scan(&p.ID, &p.IMSI, &p.Policy, &p.AllowedContacts, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// ListPrivacy returns all privacy entries.
func ListPrivacy() ([]PrivacyEntry, error) {
	rows, err := engine.Query(`SELECT id, imsi, policy, allowed_contacts, updated_at
		FROM ranging_privacy ORDER BY imsi`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PrivacyEntry
	for rows.Next() {
		var p PrivacyEntry
		if err := rows.Scan(&p.ID, &p.IMSI, &p.Policy, &p.AllowedContacts, &p.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// DeletePrivacy removes the privacy entry for an IMSI (back to
// implicit allow-all).
func DeletePrivacy(imsi string) error {
	_, err := engine.Exec(`DELETE FROM ranging_privacy WHERE imsi=?`, imsi)
	return err
}

// ---- Internal helpers ----

func checkPrivacy(sourceIMSI, targetIMSI string) (bool, string) {
	row := engine.QueryRow(`SELECT policy, allowed_contacts FROM ranging_privacy
		WHERE imsi=?`, targetIMSI)
	var policy string
	var contacts *string
	if err := row.Scan(&policy, &contacts); err != nil {
		return true, "" // no policy = allow
	}
	switch policy {
	case "deny_all":
		return false, "target denies all ranging"
	case "contacts_only":
		if contacts == nil || !strings.Contains(*contacts, sourceIMSI) {
			return false, "source not in target's allowed contacts"
		}
	}
	return true, ""
}

func simulateMeasurement(method string) map[string]float64 {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	m := map[string]float64{}
	switch method {
	case "RTT":
		m["distance_m"] = round3(0.5 + r.Float64()*299.5)
		m["accuracy_m"] = round3(0.1 + r.Float64()*0.3)
	case "AoA":
		m["distance_m"] = round3(1.0 + r.Float64()*199.0)
		m["accuracy_m"] = round3(0.5 + r.Float64()*1.0)
	case "multi_RTT":
		m["distance_m"] = round3(0.3 + r.Float64()*499.7)
		m["accuracy_m"] = round3(0.05 + r.Float64()*0.15)
	}
	m["azimuth_deg"] = round2(r.Float64() * 360)
	m["elevation_deg"] = round2(-15.0 + r.Float64()*60)
	return m
}

func scanSessionRow(row *sql.Row) (*Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.SourceIMSI, &s.TargetIMSI, &s.Method,
		&s.Status, &s.DistanceM, &s.AzimuthDeg, &s.ElevationDeg,
		&s.AccuracyM, &s.CreatedAt, &s.CompletedAt)
	return &s, err
}

func round2(f float64) float64 { return float64(int(f*100+0.5)) / 100 }
func round3(f float64) float64 { return float64(int(f*1000+0.5)) / 1000 }

