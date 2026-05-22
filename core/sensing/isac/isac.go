// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package isac — Integrated Sensing and Communication session
// management. Local persistence of the sensing-service primitives
// described in TS 22.137 §4 (description) and §5.1 (functional
// service description) for a 5G wireless sensing service.
//
// Spec anchors:
//   - TS 22.137 §4.1 General — "5G wireless sensing is a technology
//     enabler to acquire information about characteristics of the
//     environment and/or objects within the environment". The
//     Session row is one such acquisition.
//   - TS 22.137 §5.2.2 Configuration and authorization — the 5G
//     network shall configure/authorize/revoke sensing
//     transmitter/receiver authorisation. CreateSession +
//     ActivateSession persist the authorised state.
//   - TS 22.137 §5.2.3 Network exposure — secure means for a third
//     party to receive sensing results. ReportData /
//     ListData / LatestData are the local read paths the exposure
//     layer (NEF / a tester) will sit above.
//   - TS 22.137 §5.2.4 Security and privacy — encryption / integrity
//     of sensing data is out of scope here; the row schema only
//     persists already-authorised data.
//
// TS 22.137 is a Stage-1 service-requirements spec; it does NOT
// define a wire-format session API. Session lifecycle (created →
// active → completed/cancelled) is operator-local policy. Sensing
// type names (presence_detection / object_tracking / …) are
// operator-defined enum keys, not spec terms — TS 22.137 §4.1
// gives examples ("intruder detection, … trajectory tracing,
// collision avoidance, traffic management, health and activity
// monitoring") in narrative form only.
//
// TODO TS 23.288 — when 3GPP publishes a Stage-2 ISAC architecture
// (currently a study item, not a normative spec at the Rel-19 floor
// in specs/3gpp/), wire CreateSession to the canonical
// sensing-session establishment procedure and add the spec PDF.
package isac

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// Valid sensing types — operator-defined enum keys. TS 22.137 §4.1
// gives narrative examples but does NOT list these strings as
// normative; treat the set as local policy.
var validSensingTypes = map[string]bool{
	"presence_detection":     true,
	"object_tracking":        true,
	"environment_monitoring": true,
	"gesture_recognition":    true,
	"intrusion_detection":    true,
}

// Session represents a row in isac_sessions.
type Session struct {
	ID              int64   `json:"id"`
	SensingType     string  `json:"sensing_type"`
	TargetArea      *string `json:"target_area,omitempty"`
	Resolution      *string `json:"resolution,omitempty"`
	ReportIntervalS int     `json:"report_interval_s"`
	Status          string  `json:"status"`
	CreatedAt       string  `json:"created_at"`
	CompletedAt     *string `json:"completed_at,omitempty"`
}

// DataPoint represents a row in isac_data.
type DataPoint struct {
	ID              int64   `json:"id"`
	SessionID       int64   `json:"session_id"`
	Timestamp       string  `json:"timestamp"`
	DetectedObjects *string `json:"detected_objects,omitempty"`
	Environmental   *string `json:"environmental,omitempty"`
	RawData         *string `json:"raw_data,omitempty"`
}

// ---- GUI panel API ----

// List returns all sessions (preserves original stub API).
func List() ([]Session, error) { return ListSessions("", "") }

// Status returns a summary for the GUI panel.
func Status() map[string]any {
	list, _ := ListSessions("", "")
	return map[string]any{"count": len(list), "items": list}
}

// ---- Session CRUD ----

// CreateSession creates a new sensing session — local persistence
// of the "configure/authorize sensing transmitters and receivers"
// requirement (TS 22.137 §5.2.2). The session is in 'created'
// state until ActivateSession transitions it to 'active'.
func CreateSession(sensingType, targetArea, resolution string, reportIntervalS int) (*Session, error) {
	if !validSensingTypes[sensingType] {
		return nil, fmt.Errorf("invalid sensing_type: %s", sensingType)
	}
	if reportIntervalS <= 0 {
		reportIntervalS = 1
	}
	res, err := engine.Exec(`INSERT INTO isac_sessions
		(sensing_type, target_area, resolution, report_interval_s, status)
		VALUES (?,?,?,?,'created')`,
		sensingType, nilIfEmpty(targetArea), nilIfEmpty(resolution), reportIntervalS)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetSession(id)
}

// GetSession returns a session by ID.
func GetSession(id int64) (*Session, error) {
	row := engine.QueryRow(`SELECT id, sensing_type, target_area, resolution,
		report_interval_s, status, created_at, completed_at
		FROM isac_sessions WHERE id=?`, id)
	s, err := scanSession(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return s, err
}

// ListSessions lists sessions with optional type/status filters.
func ListSessions(sensingType, status string) ([]Session, error) {
	q := `SELECT id, sensing_type, target_area, resolution,
		report_interval_s, status, created_at, completed_at
		FROM isac_sessions`
	var where []string
	var args []interface{}
	if sensingType != "" {
		where = append(where, "sensing_type=?")
		args = append(args, sensingType)
	}
	if status != "" {
		where = append(where, "status=?")
		args = append(args, status)
	}
	if len(where) > 0 {
		q += " WHERE " + strings.Join(where, " AND ")
	}
	q += " ORDER BY id"
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.SensingType, &s.TargetArea, &s.Resolution,
			&s.ReportIntervalS, &s.Status, &s.CreatedAt, &s.CompletedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ActivateSession transitions created -> active.
func ActivateSession(id int64) (*Session, error) {
	s, err := GetSession(id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %d not found", id)
	}
	if s.Status != "created" {
		return nil, fmt.Errorf("cannot activate session in state '%s'", s.Status)
	}
	_, err = engine.Exec(`UPDATE isac_sessions SET status='active' WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	return GetSession(id)
}

// CancelSession transitions created/active -> cancelled.
func CancelSession(id int64) (*Session, error) {
	s, err := GetSession(id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %d not found", id)
	}
	if s.Status == "completed" || s.Status == "cancelled" {
		return nil, fmt.Errorf("session already %s", s.Status)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = engine.Exec(`UPDATE isac_sessions SET status='cancelled', completed_at=? WHERE id=?`, now, id)
	if err != nil {
		return nil, err
	}
	return GetSession(id)
}

// CompleteSession transitions active -> completed.
func CompleteSession(id int64) (*Session, error) {
	s, err := GetSession(id)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %d not found", id)
	}
	if s.Status != "active" {
		return nil, fmt.Errorf("cannot complete session in state '%s'", s.Status)
	}
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err = engine.Exec(`UPDATE isac_sessions SET status='completed', completed_at=? WHERE id=?`, now, id)
	if err != nil {
		return nil, err
	}
	return GetSession(id)
}

// DeleteSession removes a session and cascaded data.
func DeleteSession(id int64) error {
	_, err := engine.Exec(`DELETE FROM isac_sessions WHERE id=?`, id)
	return err
}

// ---- Sensing Data ----

// ReportData inserts a sensing data point for an active session.
// Persists the "collect 3GPP sensing data from sensing receivers"
// flow described in TS 22.137 §5.2.1; the exposure layer above
// (third-party NEF API per §5.2.3) reads from ListData / LatestData.
func ReportData(sessionID int64, detectedObjects, environmental, rawData *string) (*DataPoint, error) {
	s, err := GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if s.Status != "active" {
		return nil, fmt.Errorf("session %d is not active (status=%s)", sessionID, s.Status)
	}
	res, err := engine.Exec(`INSERT INTO isac_data
		(session_id, detected_objects, environmental, raw_data)
		VALUES (?,?,?,?)`, sessionID, detectedObjects, environmental, rawData)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetDataPoint(id)
}

// GetDataPoint returns a single data point by ID.
func GetDataPoint(id int64) (*DataPoint, error) {
	row := engine.QueryRow(`SELECT id, session_id, timestamp, detected_objects,
		environmental, raw_data FROM isac_data WHERE id=?`, id)
	var d DataPoint
	err := row.Scan(&d.ID, &d.SessionID, &d.Timestamp, &d.DetectedObjects,
		&d.Environmental, &d.RawData)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &d, err
}

// LatestData returns the most recent data point for a session.
func LatestData(sessionID int64) (*DataPoint, error) {
	row := engine.QueryRow(`SELECT id, session_id, timestamp, detected_objects,
		environmental, raw_data FROM isac_data
		WHERE session_id=? ORDER BY id DESC LIMIT 1`, sessionID)
	var d DataPoint
	err := row.Scan(&d.ID, &d.SessionID, &d.Timestamp, &d.DetectedObjects,
		&d.Environmental, &d.RawData)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &d, err
}

// ListData returns data history for a session.
func ListData(sessionID int64, limit int) ([]DataPoint, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := engine.Query(`SELECT id, session_id, timestamp, detected_objects,
		environmental, raw_data FROM isac_data
		WHERE session_id=? ORDER BY id DESC LIMIT ?`, sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DataPoint
	for rows.Next() {
		var d DataPoint
		if err := rows.Scan(&d.ID, &d.SessionID, &d.Timestamp, &d.DetectedObjects,
			&d.Environmental, &d.RawData); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- Consumer CRUD (TS 22.137 §5.2.3 network exposure — third-party
// API consumers receive sensing results) ----

// Consumer represents a row in isac_consumers — a third-party
// (rescue/safety/automotive app) authorised to consume sensing data.
type Consumer struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	CallbackURL string `json:"callback_url"`
	APIKey      string `json:"api_key"`
	CreatedAt   string `json:"created_at"`
}

// Subscription represents a row in isac_subscriptions — a consumer
// subscribed to receive data from a specific sensing session.
type Subscription struct {
	ID         int64  `json:"id"`
	ConsumerID int64  `json:"consumer_id"`
	SessionID  int64  `json:"session_id"`
	Active     int    `json:"active"`
	CreatedAt  string `json:"created_at"`
}

// RegisterConsumer creates a consumer row and mints an api_key.
func RegisterConsumer(name, callbackURL string) (*Consumer, error) {
	if strings.TrimSpace(name) == "" {
		return nil, fmt.Errorf("name is required")
	}
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return nil, err
	}
	apiKey := hex.EncodeToString(buf)
	res, err := engine.Exec(`INSERT INTO isac_consumers (name, callback_url, api_key)
		VALUES (?, ?, ?)`, name, callbackURL, apiKey)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetConsumer(id)
}

// GetConsumer fetches a consumer by id.
func GetConsumer(id int64) (*Consumer, error) {
	row := engine.QueryRow(`SELECT id, name, callback_url, api_key, created_at
		FROM isac_consumers WHERE id=?`, id)
	var c Consumer
	err := row.Scan(&c.ID, &c.Name, &c.CallbackURL, &c.APIKey, &c.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

// ListConsumers returns all registered consumers.
func ListConsumers() ([]Consumer, error) {
	rows, err := engine.Query(`SELECT id, name, callback_url, api_key, created_at
		FROM isac_consumers ORDER BY id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Consumer
	for rows.Next() {
		var c Consumer
		if err := rows.Scan(&c.ID, &c.Name, &c.CallbackURL, &c.APIKey, &c.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// DeleteConsumer removes a consumer (cascades to subscriptions).
func DeleteConsumer(id int64) error {
	_, err := engine.Exec(`DELETE FROM isac_consumers WHERE id=?`, id)
	return err
}

// Subscribe creates a subscription linking a consumer to a session.
func Subscribe(consumerID, sessionID int64) (*Subscription, error) {
	res, err := engine.Exec(`INSERT INTO isac_subscriptions (consumer_id, session_id)
		VALUES (?, ?)`, consumerID, sessionID)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	return GetSubscription(id)
}

// GetSubscription fetches a subscription by id.
func GetSubscription(id int64) (*Subscription, error) {
	row := engine.QueryRow(`SELECT id, consumer_id, session_id, active, created_at
		FROM isac_subscriptions WHERE id=?`, id)
	var s Subscription
	err := row.Scan(&s.ID, &s.ConsumerID, &s.SessionID, &s.Active, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

// ListSubscriptions returns subscriptions, optionally filtered by consumer or session.
func ListSubscriptions(consumerID, sessionID int64) ([]Subscription, error) {
	q := `SELECT id, consumer_id, session_id, active, created_at FROM isac_subscriptions`
	var args []any
	var conds []string
	if consumerID > 0 {
		conds = append(conds, "consumer_id=?")
		args = append(args, consumerID)
	}
	if sessionID > 0 {
		conds = append(conds, "session_id=?")
		args = append(args, sessionID)
	}
	if len(conds) > 0 {
		q += " WHERE " + strings.Join(conds, " AND ")
	}
	q += " ORDER BY id DESC"
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscription
	for rows.Next() {
		var s Subscription
		if err := rows.Scan(&s.ID, &s.ConsumerID, &s.SessionID, &s.Active, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// DeleteSubscription removes a subscription.
func DeleteSubscription(id int64) error {
	_, err := engine.Exec(`DELETE FROM isac_subscriptions WHERE id=?`, id)
	return err
}

// ---- helpers ----

func scanSession(row *sql.Row) (*Session, error) {
	var s Session
	err := row.Scan(&s.ID, &s.SensingType, &s.TargetArea, &s.Resolution,
		&s.ReportIntervalS, &s.Status, &s.CreatedAt, &s.CompletedAt)
	return &s, err
}

func nilIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

