// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pws — Public Warning System operator panel + alert lifecycle.
//
// PWS in 5GS is anchored at TS 23.501 §4.4.1 (architecture) and
// TS 23.501 §5.16.1 (functional description), both of which defer
// the wire-level realisation to TS 23.041 (the CBE → CBC → AMF →
// NG-RAN chain). The AMF-side N2 (NGAP) fan-out lives in
// nf/amf/pws/dispatch.go (TS 38.413 §8.9 procedures); this package
// is the operator-facing CRUD + state machine that produces what
// gets fanned out and records the per-gNB outcome.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.501 §4.4.1        Public Warning System (5GS architecture
//                             defers wire-level realisation to TS 23.041).
//   - TS 23.501 §5.16.1       Public Warning System (functional
//                             description in 5GS).
//   - TS 38.413 §8.9          NGAP Warning Message Transmission
//                             Procedures (Write-Replace / PWS Cancel /
//                             PWS Restart Indication / PWS Failure).
//
// Deferred — TS 23.041 (Cell Broadcast Service realisation) is not
// loaded locally; everything CBS-wire is a TODO until the PDF lands.
// The unimplemented surfaces are:
//
//   - TODO(spec: TS 23.041)   ETWS / CMAS message structure on the
//                             CBE → CBC leg.
//   - TODO(spec: TS 23.041)   CB-DATA / CB-PAGE encoding (GSM 7-bit
//                             packing, language indicator, page count).
//   - TODO(spec: TS 23.041)   Serial Number / Message Identifier
//                             allocation rules (today we randomise).
//   - TODO(spec: TS 23.041)   SBc-AP between CBC and AMF (we accept
//                             alerts directly from the operator panel).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/safety_pws.py.
package pws

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// AlertType / Severity / Urgency vocabularies — mirror the CHECK
// constraints on pws_alerts (db/schemas/domains.go). Kept as Go-side
// frozen sets so callers can validate without hitting SQLite.
var (
	alertTypes = map[string]bool{"etws": true, "cmas": true, "eu_alert": true, "test": true}
	severities = map[string]bool{"extreme": true, "severe": true, "moderate": true, "minor": true, "unknown": true}
	urgencies  = map[string]bool{"immediate": true, "expected": true, "future": true, "past": true, "unknown": true}
)

// Status values for pws_alerts.status (DB CHECK).
const (
	StatusDraft        = "draft"
	StatusBroadcasting = "broadcasting"
	StatusCompleted    = "completed"
	StatusCancelled    = "cancelled"
)

// ─── Alert CRUD ──────────────────────────────────────────────────

// CreateAlert creates a PWS alert in 'draft' state. The alert is not
// transmitted until BroadcastAlert flips its state — this lets an
// operator stage and review wording before anything goes on the air.
//
// target_areas is stored as a JSON-encoded TAI list to keep the
// row column simple while preserving multi-cell broadcast scope.
// TS 23.041 has stricter rules for how the SBc-AP message carries
// the area list — see TODO at the top of this file.
func CreateAlert(config map[string]interface{}) (map[string]interface{}, error) {
	msgText, _ := config["message_text"].(string)
	if msgText == "" {
		return nil, fmt.Errorf("message_text is required")
	}
	atype := strOr(config, "alert_type", "cmas")
	if !alertTypes[atype] {
		return nil, fmt.Errorf("invalid alert_type %q", atype)
	}
	sev := strOr(config, "severity", "unknown")
	if !severities[sev] {
		return nil, fmt.Errorf("invalid severity %q", sev)
	}
	urg := strOr(config, "urgency", "unknown")
	if !urgencies[urg] {
		return nil, fmt.Errorf("invalid urgency %q", urg)
	}

	// TODO(spec: TS 23.041) — serial_number / message_identifier
	// should follow the operator-configured allocation rules; we
	// randomise for the dev/lab path until the SBc-AP layer lands.
	msgID := rand.Intn(65535)
	serialNum := rand.Intn(65535)

	var targetAreasJSON string
	if v, ok := config["target_areas"]; ok && v != nil {
		b, _ := json.Marshal(v)
		targetAreasJSON = string(b)
	} else if v, ok := config["tai_list"]; ok && v != nil {
		// Back-compat with earlier callers that used `tai_list`.
		b, _ := json.Marshal(v)
		targetAreasJSON = string(b)
	}

	res, err := engine.Exec(`INSERT INTO pws_alerts
		(message_id, serial_number, alert_type, message_text, language, severity,
		 urgency, category, target_areas, number_of_broadcasts, repetition_period_s, status)
		VALUES (?,?,?,?,?,?,?,?,?,?,?, 'draft')`,
		msgID, serialNum, atype, msgText, strOr(config, "language", "en"),
		sev, urg, strOr(config, "category", "safety"), targetAreasJSON,
		intOr(config, "number_of_broadcasts", 10),
		intOr(config, "repetition_period_s", 60))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	logger.Get("pws").Infof("PWS alert created: id=%d type=%s severity=%s", id, atype, sev)
	return getAlert(id)
}

func getAlert(id int64) (map[string]interface{}, error) {
	return qRow("SELECT * FROM pws_alerts WHERE id=?", id)
}

// GetAlert is a public alias for getAlert.
func GetAlert(id int64) (map[string]interface{}, error) { return getAlert(id) }

// ListAlerts returns all alerts, optionally filtered by status.
func ListAlerts(status string) ([]map[string]interface{}, error) {
	if status != "" {
		return qRows("SELECT * FROM pws_alerts WHERE status=? ORDER BY id DESC", status)
	}
	return qRows("SELECT * FROM pws_alerts ORDER BY id DESC")
}

// BroadcastAlert flips draft → broadcasting and stamps broadcast_at.
// The actual NGAP fan-out is the AMF's job (nf/amf/pws/dispatch.go);
// this function only records the operator's intent.
func BroadcastAlert(alertID int64) (map[string]interface{}, error) {
	a, _ := getAlert(alertID)
	if a == nil {
		return nil, fmt.Errorf("alert %d not found", alertID)
	}
	if fmt.Sprintf("%v", a["status"]) != StatusDraft {
		return nil, fmt.Errorf("alert %d not in draft state", alertID)
	}
	_, err := engine.Exec(
		"UPDATE pws_alerts SET status='broadcasting', broadcast_at=datetime('now') WHERE id=?",
		alertID)
	if err != nil {
		return nil, err
	}
	logger.Get("pws").Infof("PWS alert broadcasting: id=%d", alertID)
	return getAlert(alertID)
}

// CancelAlert flips draft|broadcasting → cancelled. Per TS 38.413
// §8.9.2 (PWS Cancel procedure) the AMF must then send a PWS CANCEL
// REQUEST to every gNB carrying the same Message Identifier + Serial
// Number — that fan-out is nf/amf/pws/dispatch.CancelToAll.
func CancelAlert(alertID int64) (map[string]interface{}, error) {
	_, err := engine.Exec(
		"UPDATE pws_alerts SET status='cancelled' WHERE id=? AND status IN ('draft','broadcasting')",
		alertID)
	if err != nil {
		return nil, err
	}
	return getAlert(alertID)
}

// CompleteAlert flips broadcasting → completed and stamps completed_at.
// Used when number_of_broadcasts has been reached or the operator
// declares the warning over (without a Cancel that would also Kill
// any cached message in the UE).
func CompleteAlert(alertID int64) (map[string]interface{}, error) {
	_, err := engine.Exec(
		"UPDATE pws_alerts SET status='completed', completed_at=datetime('now') WHERE id=? AND status='broadcasting'",
		alertID)
	if err != nil {
		return nil, err
	}
	return getAlert(alertID)
}

// DeleteAlert removes an alert and (via FK ON DELETE CASCADE) its
// delivery log rows.
func DeleteAlert(alertID int64) error {
	_, err := engine.Exec("DELETE FROM pws_alerts WHERE id=?", alertID)
	return err
}

// ─── CBS Encoding (placeholder) ──────────────────────────────────

// EncodeCBSMessage returns a metadata sketch of the CBS encoding for
// `text`. The real packed payload is a TS 23.041 concern (GSM 7-bit
// packing into 82-char pages, max 15 pages). The CBC normally owns
// this — we just surface page count + IDs so the operator panel can
// preview "this alert will take N pages" before broadcast.
//
// TODO(spec: TS 23.041) — produce the actual packed bytes (CB-DATA
// pages with header + GSM 7-bit body) once the SBc-AP layer is in.
func EncodeCBSMessage(text string, msgID, serialNum int) map[string]interface{} {
	pages := (len(text) + 82) / 83
	if pages == 0 {
		pages = 1
	}
	if pages > 15 {
		pages = 15
	}
	return map[string]interface{}{
		"message_id":    msgID,
		"serial_number": serialNum,
		"pages":         pages,
		"text_length":   len(text),
		"encoding":      "gsm7", // placeholder; real packing is TODO
	}
}

// ─── Delivery Log ────────────────────────────────────────────────

// RecordDelivery appends one row to pws_delivery_log. Status must be
// one of {pending, delivered, failed, acknowledged} per the schema's
// CHECK constraint; invalid values are rejected by SQLite.
func RecordDelivery(alertID int64, gnbID, status string) error {
	deliveredAt := ""
	ackAt := ""
	switch status {
	case "delivered":
		deliveredAt = time.Now().UTC().Format(time.RFC3339)
	case "acknowledged":
		now := time.Now().UTC().Format(time.RFC3339)
		deliveredAt, ackAt = now, now
	}
	_, err := engine.Exec(
		`INSERT INTO pws_delivery_log (alert_id, gnb_id, status, delivered_at, ack_at)
		 VALUES (?,?,?,?,?)`,
		alertID, gnbID, status,
		nullIfEmpty(deliveredAt), nullIfEmpty(ackAt))
	return err
}

// GetDeliveries returns all delivery rows for an alert.
func GetDeliveries(alertID int64) ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM pws_delivery_log WHERE alert_id=? ORDER BY id", alertID)
}

// ListDeliveryLog returns the most recent delivery rows across every
// alert (newest first), joined with the alert's message_id /
// alert_type so the operator panel can render one row without a
// second round-trip.
func ListDeliveryLog(limit int) ([]map[string]interface{}, error) {
	if limit <= 0 {
		limit = 200
	}
	return qRows(
		`SELECT d.id, d.alert_id, d.gnb_id, d.status, d.delivered_at, d.ack_at,
		        a.message_id, a.alert_type
		 FROM pws_delivery_log d
		 LEFT JOIN pws_alerts a ON a.id = d.alert_id
		 ORDER BY d.id DESC LIMIT ?`, limit)
}

// ─── Stats ───────────────────────────────────────────────────────

// GetStats returns coarse counters for the operator dashboard.
// GetStats returns coarse counters for the operator dashboard. Shape
// matches templates/pws.html: `total_alerts` plus an
// `alerts_by_status` sub-map and `total_deliveries`.
func GetStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var total, draft, broadcasting, completed, cancelled, deliveries int
	_ = db.QueryRow("SELECT COUNT(*) FROM pws_alerts").Scan(&total)
	_ = db.QueryRow("SELECT COUNT(*) FROM pws_alerts WHERE status='draft'").Scan(&draft)
	_ = db.QueryRow("SELECT COUNT(*) FROM pws_alerts WHERE status='broadcasting'").Scan(&broadcasting)
	_ = db.QueryRow("SELECT COUNT(*) FROM pws_alerts WHERE status='completed'").Scan(&completed)
	_ = db.QueryRow("SELECT COUNT(*) FROM pws_alerts WHERE status='cancelled'").Scan(&cancelled)
	_ = db.QueryRow("SELECT COUNT(*) FROM pws_delivery_log").Scan(&deliveries)
	return map[string]interface{}{
		"total_alerts": total,
		"alerts_by_status": map[string]int{
			"draft":        draft,
			"broadcasting": broadcasting,
			"completed":    completed,
			"cancelled":    cancelled,
		},
		"total_deliveries": deliveries,
	}
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) {
	all, err := ListAlerts("")
	out := make([]map[string]any, len(all))
	for i, m := range all {
		out[i] = m
	}
	return out, err
}

func Status() map[string]any { return GetStats() }

// ─── helpers ─────────────────────────────────────────────────────

func strOr(m map[string]interface{}, k, def string) string {
	if v, ok := m[k].(string); ok && v != "" {
		return v
	}
	return def
}

func intOr(m map[string]interface{}, k string, def int) int {
	if v, ok := m[k].(float64); ok {
		return int(v)
	}
	if v, ok := m[k].(int); ok {
		return v
	}
	if v, ok := m[k].(int64); ok {
		return int(v)
	}
	return def
}

func nullIfEmpty(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}

func init() { rand.Seed(time.Now().UnixNano()); _ = strings.TrimSpace }

func qRow(q string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() {
		return nil, nil
	}
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals {
		ptrs[i] = &vals[i]
	}
	rows.Scan(ptrs...)
	m := make(map[string]interface{}, len(cols))
	for i, c := range cols {
		m[c] = vals[i]
	}
	return m, nil
}

func qRows(q string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		rows.Scan(ptrs...)
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols {
			m[c] = vals[i]
		}
		out = append(out, m)
	}
	return out, nil
}
