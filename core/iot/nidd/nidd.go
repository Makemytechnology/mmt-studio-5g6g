// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package nidd — Non-IP Data Delivery (NIDD) for cellular IoT.
//
// Spec anchors:
//   - TS 23.682 §5.13 Non-IP Data Delivery procedures — overall
//     procedure set: T6a/T6b connection establishment (§5.13.1),
//     NIDD configuration (§5.13.2), MT NIDD (§5.13.3), MO NIDD
//     (§5.13.4), connection release (§5.13.5).
//   - TS 23.682 §5.13.2 NIDD Configuration — "the SCS/AS may use
//     the NIDD Configuration procedure to set up the parameters
//     under which the SCEF will provide NIDD services to the
//     SCS/AS for a specific UE". CreateSession is the local
//     persistence of the per-UE configuration row.
//   - TS 23.682 §5.13.3 Mobile Terminated NIDD procedure — the
//     SCEF buffers DL traffic when the UE is unreachable; we
//     mirror that with status='buffered' on nidd_data_log rows
//     for UEs in PSM 'unreachable'.
//   - TS 23.682 §5.13.4 Mobile Originated NIDD procedure — UE
//     sends Non-IP data via NAS PDU; SCEF forwards to the SCS/AS
//     via T8 callback (the AppServer.callback_url hook).
//   - TS 23.401 §4.3.17.8 Support for Non-IP Data Delivery (NIDD)
//     — EPC bearer-level support; SGW/PGW carry the Non-IP PDU
//     over the SCEF tunnel.
//
// CP CIoT data path (iot_cp_data) implements the small-data
// delivery over NAS that NB-IoT / LTE-M uses when the UE has
// CP-CIoT EPS optimisation negotiated (TS 23.401 §4.3.17 +
// see nbiot.Capabilities.CPCIoTSupported).
//
// TODO TS 29.122 — wire the SCEF T8 northbound API (NIDD
// Configuration / NIDD subscription / NIDD message delivery)
// when the spec PDF is added to specs/3gpp/.
// TODO TS 24.250 §6 — wrap the on-the-wire RDS PDUs (SAPI /
// sequence number / ACK) onto the iot_cp_data payload when
// reliable delivery is requested by the AS.
package nidd

import (
	"database/sql"
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ── Types ────────────────────────────────────────────────────────

// Session — TS 23.682 §5.13.2 NIDD Configuration row. Holds the
// per-(UE, APN, AS) binding the SCEF needs to route NAS-borne
// Non-IP data either way.
type Session struct {
	ID           int64  `json:"id"`
	IMSI         string `json:"imsi"`
	SessionID    string `json:"session_id"`
	APN          string `json:"apn"`
	AppServerURL string `json:"app_server_url"`
	Status       string `json:"status"` // active | suspended | terminated
	CreatedAt    string `json:"created_at"`
}

// CPData — Control-Plane CIoT small-data PDU (TS 23.401 §4.3.17).
// Carried over NAS in the EMM/ESM "ESM Data Transport" message
// (TS 24.301). UL = UE→network, DL = network→UE.
type CPData struct {
	ID          int64   `json:"id"`
	IMSI        string  `json:"imsi"`
	Direction   string  `json:"direction"`         // UL | DL
	DataPayload []byte  `json:"data_payload"`
	APN         *string `json:"apn,omitempty"`
	Delivered   bool    `json:"delivered"`
	CreatedAt   string  `json:"created_at"`
	DeliveredAt *string `json:"delivered_at,omitempty"`
}

// DataLog — per-message persistence for NIDD sessions
// (status mirrors §5.13.3 buffering / delivery outcomes).
type DataLog struct {
	ID          int64   `json:"id"`
	SessionID   int64   `json:"session_id"`
	Direction   string  `json:"direction"`     // UL | DL
	DataHex     string  `json:"data_hex"`
	DataLength  int     `json:"data_length"`
	Status      string  `json:"status"`        // pending | delivered | buffered | failed | expired
	CreatedAt   string  `json:"created_at"`
	DeliveredAt *string `json:"delivered_at,omitempty"`
}

// AppServer — SCS/AS endpoint registry (T8 northbound callback).
type AppServer struct {
	ID          int64  `json:"id"`
	AppServerID string `json:"app_server_id"`
	Name        string `json:"name"`
	CallbackURL string `json:"callback_url"`
	AuthToken   string `json:"auth_token"`
	CreatedAt   string `json:"created_at"`
}

// ── NIDD Session CRUD (TS 23.682 §5.13.2) ────────────────────────

// CreateSession persists a NIDD configuration — the SCS/AS has
// requested NIDD service for a (UE, APN) pair (TS 23.682 §5.13.2).
// Session ID is opaque to the SCEF; we generate one if empty.
func CreateSession(imsi, sessionID, apn, appServerURL string) (*Session, error) {
	if strings.TrimSpace(imsi) == "" {
		return nil, fmt.Errorf("imsi is required")
	}
	if strings.TrimSpace(apn) == "" {
		return nil, fmt.Errorf("apn is required")
	}
	if strings.TrimSpace(appServerURL) == "" {
		return nil, fmt.Errorf("app_server_url is required")
	}
	if sessionID == "" {
		sessionID = fmt.Sprintf("nidd-%s-%d", imsi, time.Now().UnixNano())
	}
	res, err := engine.Exec(`INSERT INTO iot_nidd_sessions
		(imsi, session_id, apn, app_server_url, status)
		VALUES (?, ?, ?, ?, 'active')`,
		imsi, sessionID, apn, appServerURL)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetSession(id)
}

// GetSession reads a session by row ID.
func GetSession(id int64) (*Session, error) {
	row := engine.QueryRow(`SELECT id, imsi, session_id, apn, app_server_url,
		status, created_at FROM iot_nidd_sessions WHERE id=?`, id)
	var s Session
	err := row.Scan(&s.ID, &s.IMSI, &s.SessionID, &s.APN, &s.AppServerURL,
		&s.Status, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

// FindSession returns a session by (imsi, apn) — there can be at
// most one active per pair from the SCEF's POV per §5.13.2.
func FindSession(imsi, apn string) (*Session, error) {
	row := engine.QueryRow(`SELECT id, imsi, session_id, apn, app_server_url,
		status, created_at FROM iot_nidd_sessions
		WHERE imsi=? AND apn=? AND status='active' LIMIT 1`, imsi, apn)
	var s Session
	err := row.Scan(&s.ID, &s.IMSI, &s.SessionID, &s.APN, &s.AppServerURL,
		&s.Status, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

// ListSessions returns NIDD sessions, optionally filtered by IMSI.
func ListSessions(imsi string) ([]Session, error) {
	q := `SELECT id, imsi, session_id, apn, app_server_url, status, created_at
		FROM iot_nidd_sessions`
	var args []interface{}
	if imsi != "" {
		q += " WHERE imsi=?"
		args = append(args, imsi)
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
		if err := rows.Scan(&s.ID, &s.IMSI, &s.SessionID, &s.APN,
			&s.AppServerURL, &s.Status, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SuspendSession marks a session 'suspended' (NIDD Authorisation
// Update flow, TS 23.682 §5.13.8 — ASF may temporarily suspend
// service to a UE).
func SuspendSession(id int64) error {
	_, err := engine.Exec(`UPDATE iot_nidd_sessions SET status='suspended'
		WHERE id=? AND status='active'`, id)
	return err
}

// TerminateSession marks a session 'terminated' (T6a/T6b release,
// TS 23.682 §5.13.5).
func TerminateSession(id int64) error {
	_, err := engine.Exec(`UPDATE iot_nidd_sessions SET status='terminated'
		WHERE id=?`, id)
	return err
}

// GetSessionBySessionID resolves a session by the operator-facing
// opaque session_id token (TS 23.682 §5.13.2 — the SCEF returns
// this in the NIDD Configuration response; SCS/AS uses it for all
// subsequent operations).
func GetSessionBySessionID(sessionID string) (*Session, error) {
	row := engine.QueryRow(`SELECT id, imsi, session_id, apn, app_server_url,
		status, created_at FROM iot_nidd_sessions WHERE session_id=?`,
		sessionID)
	var s Session
	err := row.Scan(&s.ID, &s.IMSI, &s.SessionID, &s.APN, &s.AppServerURL,
		&s.Status, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

// DeleteSessionBySessionID removes a session row by its opaque
// session_id (T6a/T6b release path, TS 23.682 §5.13.5). The data
// log rows linked by FK are NOT cascaded — kept for audit.
func DeleteSessionBySessionID(sessionID string) error {
	_, err := engine.Exec(
		`DELETE FROM iot_nidd_sessions WHERE session_id=?`, sessionID)
	return err
}

// ── MO NIDD (TS 23.682 §5.13.4) ──────────────────────────────────

// SendMO records a UE-originated Non-IP PDU on a session — the SCEF
// would forward this to the SCS/AS via the AppServer.callback_url
// per §5.13.4. Caller passes the raw PDU bytes; we hex-encode for
// audit and persist.
func SendMO(sessionID int64, payload []byte) (*DataLog, error) {
	s, err := GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if s.Status != "active" {
		return nil, fmt.Errorf("session %d is %s, not active", sessionID, s.Status)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("payload cannot be empty")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	dataHex := hex.EncodeToString(payload)
	res, err := engine.Exec(`INSERT INTO nidd_data_log
		(session_id, direction, data_hex, data_length, status,
		 created_at, delivered_at)
		VALUES (?, 'UL', ?, ?, 'delivered', ?, ?)`,
		sessionID, dataHex, len(payload), now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetLog(id)
}

// ── MT NIDD (TS 23.682 §5.13.3) ──────────────────────────────────

// SendMT records a network-originated Non-IP PDU. If the UE is
// reachable the SCEF forwards it to the MME; otherwise the SCEF
// buffers (status='buffered') per §5.13.3 high-latency
// communication path. The PSM-state argument is the upstream
// caller's view (typically nbiot.GetPSM). Empty or 'active' UE →
// 'delivered' immediately.
func SendMT(sessionID int64, payload []byte, ueState string) (*DataLog, error) {
	s, err := GetSession(sessionID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("session %d not found", sessionID)
	}
	if s.Status != "active" {
		return nil, fmt.Errorf("session %d is %s, not active", sessionID, s.Status)
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("payload cannot be empty")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	dataHex := hex.EncodeToString(payload)
	status := "delivered"
	var deliveredAt interface{} = now
	switch ueState {
	case "sleeping", "unreachable":
		status = "buffered"
		deliveredAt = nil
	}
	res, err := engine.Exec(`INSERT INTO nidd_data_log
		(session_id, direction, data_hex, data_length, status,
		 created_at, delivered_at)
		VALUES (?, 'DL', ?, ?, ?, ?, ?)`,
		sessionID, dataHex, len(payload), status, now, deliveredAt)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return GetLog(id)
}

// FlushBuffered marks all 'buffered' DL log rows for a session as
// 'delivered' — invoked when the UE wakes from PSM. TS 23.682
// §5.13.3 calls this "high latency communication" delivery.
func FlushBuffered(sessionID int64) (int, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := engine.Exec(`UPDATE nidd_data_log
		SET status='delivered', delivered_at=?
		WHERE session_id=? AND direction='DL' AND status='buffered'`,
		now, sessionID)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
}

// GetLog reads a single data-log row.
func GetLog(id int64) (*DataLog, error) {
	row := engine.QueryRow(`SELECT id, session_id, direction, data_hex,
		data_length, status, created_at, delivered_at
		FROM nidd_data_log WHERE id=?`, id)
	var d DataLog
	err := row.Scan(&d.ID, &d.SessionID, &d.Direction, &d.DataHex,
		&d.DataLength, &d.Status, &d.CreatedAt, &d.DeliveredAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &d, err
}

// ListLogs returns log entries for a session ordered newest-first.
func ListLogs(sessionID int64, limit int) ([]DataLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := engine.Query(`SELECT id, session_id, direction, data_hex,
		data_length, status, created_at, delivered_at
		FROM nidd_data_log WHERE session_id=? ORDER BY id DESC LIMIT ?`,
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DataLog
	for rows.Next() {
		var d DataLog
		if err := rows.Scan(&d.ID, &d.SessionID, &d.Direction, &d.DataHex,
			&d.DataLength, &d.Status, &d.CreatedAt, &d.DeliveredAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── App-server registry (T8 callback endpoints) ──────────────────

// RegisterAppServer adds an SCS/AS endpoint the SCEF can deliver
// MO NIDD traffic to via the T8 northbound API.
// TODO TS 29.122 — bind app servers to the formal Nnef_NIDD APIs
// when the T8 spec PDF is loaded.
func RegisterAppServer(appServerID, name, callbackURL, authToken string) (*AppServer, error) {
	if strings.TrimSpace(appServerID) == "" {
		return nil, fmt.Errorf("app_server_id is required")
	}
	now := time.Now().UTC().Format(time.RFC3339)
	res, err := engine.Exec(`INSERT INTO nidd_app_servers
		(app_server_id, name, callback_url, auth_token, created_at)
		VALUES (?, ?, ?, ?, ?)`,
		appServerID, name, callbackURL, authToken, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	row := engine.QueryRow(`SELECT id, app_server_id, name, callback_url,
		auth_token, created_at FROM nidd_app_servers WHERE id=?`, id)
	var a AppServer
	err = row.Scan(&a.ID, &a.AppServerID, &a.Name, &a.CallbackURL,
		&a.AuthToken, &a.CreatedAt)
	return &a, err
}

// DeleteAppServer removes an app-server registration by its
// operator-facing app_server_id (TS 29.122 §5 — the NEF
// management surface lets the SCS/AS deregister callbacks).
func DeleteAppServer(appServerID string) error {
	_, err := engine.Exec(
		`DELETE FROM nidd_app_servers WHERE app_server_id=?`, appServerID)
	return err
}

// ListAppServers returns all registered app-server endpoints.
func ListAppServers() ([]AppServer, error) {
	rows, err := engine.Query(`SELECT id, app_server_id, name, callback_url,
		auth_token, created_at FROM nidd_app_servers ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AppServer
	for rows.Next() {
		var a AppServer
		if err := rows.Scan(&a.ID, &a.AppServerID, &a.Name, &a.CallbackURL,
			&a.AuthToken, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// ── CP CIoT data path (TS 23.401 §4.3.17 — small data over NAS) ──

// AppendCP records a NAS-borne small-data PDU (CP CIoT optimisation
// path — TS 23.401 §4.3.17). Direction = UL|DL. The 'delivered' bit
// is initialised false; the higher layer (MME / SCEF) flips it
// after acknowledgement.
func AppendCP(imsi, direction string, payload []byte, apn *string) (*CPData, error) {
	if direction != "UL" && direction != "DL" {
		return nil, fmt.Errorf("direction must be UL or DL")
	}
	if len(payload) == 0 {
		return nil, fmt.Errorf("payload cannot be empty")
	}
	res, err := engine.Exec(`INSERT INTO iot_cp_data
		(imsi, direction, data_payload, apn, delivered)
		VALUES (?, ?, ?, ?, 0)`,
		imsi, direction, payload, apn)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	row := engine.QueryRow(`SELECT id, imsi, direction, data_payload, apn,
		delivered, created_at, delivered_at FROM iot_cp_data WHERE id=?`, id)
	var d CPData
	var delivered int
	err = row.Scan(&d.ID, &d.IMSI, &d.Direction, &d.DataPayload, &d.APN,
		&delivered, &d.CreatedAt, &d.DeliveredAt)
	d.Delivered = delivered != 0
	return &d, err
}

// MarkCPDelivered flips delivered=1 + sets delivered_at on a row.
func MarkCPDelivered(id int64) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := engine.Exec(`UPDATE iot_cp_data SET delivered=1, delivered_at=?
		WHERE id=?`, now, id)
	return err
}

// PendingCP returns undelivered DL CP data for a UE — the MME polls
// this when the UE comes out of PSM.
func PendingCP(imsi string) ([]CPData, error) {
	rows, err := engine.Query(`SELECT id, imsi, direction, data_payload, apn,
		delivered, created_at, delivered_at FROM iot_cp_data
		WHERE imsi=? AND direction='DL' AND delivered=0 ORDER BY id`, imsi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []CPData
	for rows.Next() {
		var d CPData
		var delivered int
		if err := rows.Scan(&d.ID, &d.IMSI, &d.Direction, &d.DataPayload,
			&d.APN, &delivered, &d.CreatedAt, &d.DeliveredAt); err != nil {
			return nil, err
		}
		d.Delivered = delivered != 0
		out = append(out, d)
	}
	return out, rows.Err()
}

// ── GUI panel surface ────────────────────────────────────────────

// List returns CP data rows (preserves the original GUI panel API).
func List() ([]map[string]any, error) {
	rows, err := engine.Query(`SELECT id, imsi, direction, length(data_payload),
		apn, delivered, created_at FROM iot_cp_data ORDER BY id DESC LIMIT 1000`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var imsi, direction, createdAt string
		var dataLen, delivered int
		var apn *string
		if err := rows.Scan(&id, &imsi, &direction, &dataLen, &apn, &delivered, &createdAt); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"id": id, "imsi": imsi, "direction": direction,
			"data_length": dataLen, "apn": apn,
			"delivered": delivered != 0, "created_at": createdAt,
		})
	}
	return out, rows.Err()
}

// Status returns counts for the GUI panel.
func Status() map[string]any {
	row := engine.QueryRow(`SELECT
		(SELECT COUNT(*) FROM iot_nidd_sessions WHERE status='active'),
		(SELECT COUNT(*) FROM iot_cp_data),
		(SELECT COUNT(*) FROM nidd_data_log WHERE status='buffered')`)
	var sessions, cp, buffered int
	_ = row.Scan(&sessions, &cp, &buffered)
	return map[string]any{
		"active_sessions": sessions,
		"cp_data_count":   cp,
		"buffered_dl":     buffered,
	}
}
