// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package uas — Unmanned Aerial Systems control plane (UAV/USS).
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.125 §5            UAS service requirements (UAV identity,
//                             command/control, remote identification,
//                             flight authorization).
//   - TS 23.256 §4.2          Reference architecture for UAS (UAV-C2
//                             between UAV and UAV-C; UAS via 5GS).
//   - TS 23.256 §5.2.1        UAV authentication & authorization (UAA)
//                             — UAS NF and USS interaction.
//   - TS 23.256 §5.2.3        Pairing authorization between UAV and
//                             UAV-C (the "C2 pairing" the EstablishC2
//                             call models).
//   - TS 23.256 §5.2.4        UAV flight authorization with USS/UTM —
//                             UTM checks the flight plan and returns
//                             permit/deny + restrictions (vertical /
//                             corridor / time-window).
//   - TS 23.256 §5.2.5        Remote identification (Net-RID) of UAVs
//                             — Remote ID broadcast / network publish.
//   - TS 23.256 §5.2.6        UAV location reporting / tracking
//                             (USS subscribes to position events).
//   - TS 23.256 §5.5          C2 communication (DCC, VLOS / BVLOS) —
//                             default 5QI=3 in §5.5 / TS 23.501 Table
//                             5.7.4-1 (V2X / DCC characteristic).
//   - ASTM F3411-22a §4       Remote ID broadcast message format
//                             (informative — Net-RID in TS 23.256
//                             §5.2.5 references ASTM F3411 / RFC 9153).
//
// Deferred (TODO at unimplemented call-sites — searchable by §):
//
//   - TS 23.256 §5.2.2        UAV-Map / DAA (Detect-and-Avoid) data
//                             exchange with USS.
//   - TS 23.256 §5.2.7        UAV-USS / UAV-UTM Application Function
//                             discovery via NRF.
//   - TS 23.256 §5.4          Group communication for UAV swarms.
//   - TS 23.256 §5.6          UAV C2 link recovery — currently the
//                             FailoverC2 helper just marks the session
//                             failed; the spec calls for a switch to a
//                             redundant DN / alternate DRB.
//   - TS 22.125 §5.3.x        Command-and-control authentication of
//                             the UAV-C controller identity (today we
//                             accept the controller_id string verbatim).
//   - ASTM F3411 §5           Direct (broadcast) Remote ID over
//                             Bluetooth-LE / Wi-Fi NaN (out-of-scope
//                             for the 5GC; we model only Net-RID).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/uas.py.
package uas

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ─── Types ───────────────────────────────────────────────────────

// UAV mirrors uas_registry — the local UAS NF view of a registered
// UAV (TS 23.256 §5.2.1 — registration is the precondition for UAA).
type UAV struct {
	ID           int64    `json:"id"`
	IMSI         *string  `json:"imsi,omitempty"`
	UAVID        string   `json:"uav_id"`
	SerialNumber *string  `json:"serial_number,omitempty"`
	Manufacturer *string  `json:"manufacturer,omitempty"`
	Model        *string  `json:"model,omitempty"`
	MaxSpeedMPS  *float64 `json:"max_speed_mps,omitempty"`
	MaxAltitudeM *float64 `json:"max_altitude_m,omitempty"`
	Status       string   `json:"status"`
	CreatedAt    string   `json:"created_at"`
}

// FlightAuth is the result of TS 23.256 §5.2.4 (UAV flight
// authorization with USS) persisted as a uas_flight_auth row.
type FlightAuth struct {
	ID             int64   `json:"id"`
	UAVID          string  `json:"uav_id"`
	FlightID       string  `json:"flight_id"`
	FlightPlanJSON *string `json:"flight_plan_json,omitempty"`
	Authorized     int     `json:"authorized"`
	Restrictions   *string `json:"restrictions,omitempty"`
	Status         string  `json:"status"`
	AuthorizedAt   *string `json:"authorized_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

// NoFlyZone is a USS-published forbidden volume. The lat/lon box
// model is a deliberate simplification of the polygonal NoFlyArea
// that USS publishes per ASTM F3548 / TS 23.256 §5.2.4.
type NoFlyZone struct {
	ID        int64    `json:"id"`
	Name      string   `json:"name"`
	LatMin    float64  `json:"lat_min"`
	LatMax    float64  `json:"lat_max"`
	LonMin    float64  `json:"lon_min"`
	LonMax    float64  `json:"lon_max"`
	AltMaxM   *float64 `json:"alt_max_m,omitempty"`
	Reason    *string  `json:"reason,omitempty"`
	Active    int      `json:"active"`
	CreatedAt string   `json:"created_at"`
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]UAV, error) { return ListUAVs() }

func Status() map[string]any {
	uavs, _ := ListUAVs()
	zones, _ := ListNoFlyZones()
	return map[string]any{"uavs": len(uavs), "no_fly_zones": len(zones)}
}

// ─── UAV Registry (TS 23.256 §5.2.1) ─────────────────────────────

func ListUAVs() ([]UAV, error) {
	rows, err := engine.Query(`SELECT id, imsi, uav_id, serial_number,
		manufacturer, model, max_speed_mps, max_altitude_m, status, created_at
		FROM uas_registry WHERE status!='deregistered' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []UAV
	for rows.Next() {
		var u UAV
		if err := rows.Scan(&u.ID, &u.IMSI, &u.UAVID, &u.SerialNumber,
			&u.Manufacturer, &u.Model, &u.MaxSpeedMPS, &u.MaxAltitudeM,
			&u.Status, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func GetUAV(id int64) (*UAV, error) {
	row := engine.QueryRow(`SELECT id, imsi, uav_id, serial_number,
		manufacturer, model, max_speed_mps, max_altitude_m, status, created_at
		FROM uas_registry WHERE id=?`, id)
	var u UAV
	err := row.Scan(&u.ID, &u.IMSI, &u.UAVID, &u.SerialNumber,
		&u.Manufacturer, &u.Model, &u.MaxSpeedMPS, &u.MaxAltitudeM,
		&u.Status, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

func GetUAVByUAVID(uavID string) (*UAV, error) {
	row := engine.QueryRow(`SELECT id, imsi, uav_id, serial_number,
		manufacturer, model, max_speed_mps, max_altitude_m, status, created_at
		FROM uas_registry WHERE uav_id=?`, uavID)
	var u UAV
	err := row.Scan(&u.ID, &u.IMSI, &u.UAVID, &u.SerialNumber,
		&u.Manufacturer, &u.Model, &u.MaxSpeedMPS, &u.MaxAltitudeM,
		&u.Status, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

// RegisterUAV creates a UAV registry entry. The uav_id (CAA-Level
// UAV ID per TS 23.256 §5.2.5) is auto-generated when blank.
func RegisterUAV(imsi, uavID, serialNumber, manufacturer, model string,
	maxSpeedMPS, maxAltitudeM float64) (int64, error) {
	if uavID == "" {
		uavID = fmt.Sprintf("UAV-%08X", time.Now().UnixNano()&0xFFFFFFFF)
	}
	res, err := engine.Exec(`INSERT INTO uas_registry
		(imsi, uav_id, serial_number, manufacturer, model, max_speed_mps, max_altitude_m, status)
		VALUES (?,?,?,?,?,?,?,'registered')`,
		nilStr(imsi), uavID, nilStr(serialNumber), nilStr(manufacturer),
		nilStr(model), maxSpeedMPS, maxAltitudeM)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DeregisterUAV(id int64) error {
	_, err := engine.Exec(`UPDATE uas_registry SET status='deregistered' WHERE id=?`, id)
	return err
}

func DeleteUAV(id int64) error {
	_, err := engine.Exec(`DELETE FROM uas_registry WHERE id=?`, id)
	return err
}

// DeleteUAVByUAVID drops a registry row by the CAA-Level UAV ID
// (TS 23.256 §5.2.5). The operator panel and the tester address
// UAVs by their string ID rather than the local autoinc primary key.
func DeleteUAVByUAVID(uavID string) error {
	_, err := engine.Exec(`DELETE FROM uas_registry WHERE uav_id=?`, uavID)
	return err
}

// ─── Flight Authorization (TS 23.256 §5.2.4) ─────────────────────

// AuthorizeFlight implements the UAV side of the §5.2.4 procedure:
// the UAS NF asks the USS/UTM whether the flight plan is admissible.
// The local USS stand-in checks no-fly zones and per-UAV envelope
// (max speed / max altitude); a real deployment would forward the
// request to an external USS over the UAE-USS / UAE-UTM reference
// point (TS 22.125 §5.4).
func AuthorizeFlight(uavID string, flightPlan map[string]interface{}) (map[string]interface{}, error) {
	uav, err := GetUAVByUAVID(uavID)
	if err != nil {
		return nil, err
	}
	if uav == nil {
		return map[string]interface{}{"authorized": false, "error": "UAV not found"}, nil
	}
	if uav.Status == "deregistered" {
		return map[string]interface{}{"authorized": false, "error": "UAV deregistered"}, nil
	}
	if uav.Status == "grounded" {
		return map[string]interface{}{"authorized": false, "error": "UAV grounded"}, nil
	}

	violations := checkNoFlyZones(flightPlan)
	if len(violations) > 0 {
		return map[string]interface{}{"authorized": false, "error": "no-fly zone violation", "violations": violations}, nil
	}

	flightID := fmt.Sprintf("FLT-%08X", time.Now().UnixNano()&0xFFFFFFFF)
	planJSON, _ := json.Marshal(flightPlan)
	var restrictions []string
	if uav.MaxSpeedMPS != nil {
		restrictions = append(restrictions, fmt.Sprintf("max_speed=%.1fm/s", *uav.MaxSpeedMPS))
	}
	if uav.MaxAltitudeM != nil {
		restrictions = append(restrictions, fmt.Sprintf("max_alt=%.1fm", *uav.MaxAltitudeM))
	}

	now := time.Now().UTC().Format(time.RFC3339)
	_, err = engine.Exec(`INSERT INTO uas_flight_auth
		(uav_id, flight_id, flight_plan_json, authorized, restrictions, status, authorized_at)
		VALUES (?,?,?,1,?,?,?)`,
		uavID, flightID, string(planJSON), strings.Join(restrictions, "; "), "authorized", now)
	if err != nil {
		return nil, err
	}

	_, _ = engine.Exec(`UPDATE uas_registry SET status='active' WHERE uav_id=?`, uavID)

	return map[string]interface{}{
		"authorized": true, "flight_id": flightID, "restrictions": restrictions,
	}, nil
}

func RevokeAuthorization(flightID string) error {
	_, err := engine.Exec(`UPDATE uas_flight_auth SET status='revoked' WHERE flight_id=?`, flightID)
	return err
}

// ─── No-Fly Zones (TS 23.256 §5.2.4) ─────────────────────────────

func ListNoFlyZones() ([]NoFlyZone, error) {
	rows, err := engine.Query(`SELECT id, name, lat_min, lat_max, lon_min, lon_max,
		alt_max_m, reason, active, created_at FROM uas_no_fly_zones WHERE active=1 ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NoFlyZone
	for rows.Next() {
		var z NoFlyZone
		if err := rows.Scan(&z.ID, &z.Name, &z.LatMin, &z.LatMax, &z.LonMin, &z.LonMax,
			&z.AltMaxM, &z.Reason, &z.Active, &z.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, z)
	}
	return out, rows.Err()
}

func CreateNoFlyZone(name string, latMin, latMax, lonMin, lonMax float64, altMaxM *float64, reason string) (int64, error) {
	res, err := engine.Exec(`INSERT INTO uas_no_fly_zones
		(name, lat_min, lat_max, lon_min, lon_max, alt_max_m, reason, active)
		VALUES (?,?,?,?,?,?,?,1)`, name, latMin, latMax, lonMin, lonMax, altMaxM, nilStr(reason))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DeleteNoFlyZone(id int64) error {
	_, err := engine.Exec(`UPDATE uas_no_fly_zones SET active=0 WHERE id=?`, id)
	return err
}

func checkNoFlyZones(flightPlan map[string]interface{}) []map[string]interface{} {
	zones, _ := ListNoFlyZones()
	waypoints, _ := flightPlan["waypoints"].([]interface{})
	var violations []map[string]interface{}
	for _, wp := range waypoints {
		wpm, ok := wp.(map[string]interface{})
		if !ok {
			continue
		}
		lat, _ := wpm["lat"].(float64)
		lon, _ := wpm["lon"].(float64)
		alt, _ := wpm["alt_m"].(float64)
		for _, z := range zones {
			if lat >= z.LatMin && lat <= z.LatMax && lon >= z.LonMin && lon <= z.LonMax {
				if z.AltMaxM == nil || *z.AltMaxM <= 0 || alt <= *z.AltMaxM {
					violations = append(violations, map[string]interface{}{
						"zone_id": z.ID, "zone_name": z.Name, "reason": z.Reason,
					})
				}
			}
		}
	}
	return violations
}

// ─── Position Tracking & Net-RID (TS 23.256 §5.2.5 / §5.2.6) ─────

func UpdatePosition(uavID string, lat, lon, alt, heading, speed float64) error {
	uav, err := GetUAVByUAVID(uavID)
	if err != nil {
		return err
	}
	if uav == nil {
		return fmt.Errorf("UAV %s not found", uavID)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	_, err = engine.Exec(`INSERT INTO uas_positions (uav_id, latitude, longitude, altitude_m, heading_deg, speed_mps, timestamp)
		VALUES (?,?,?,?,?,?,?)`, uavID, lat, lon, alt, heading, speed, now)
	return err
}

func GetPosition(uavID string) map[string]interface{} {
	row := engine.QueryRow(`SELECT uav_id, latitude, longitude, altitude_m, heading_deg, speed_mps, timestamp
		FROM uas_positions WHERE uav_id=? ORDER BY id DESC LIMIT 1`, uavID)
	var uid string
	var lat, lon, alt, hdg, spd float64
	var ts string
	if row.Scan(&uid, &lat, &lon, &alt, &hdg, &spd, &ts) != nil {
		return nil
	}
	return map[string]interface{}{
		"uav_id": uid, "latitude": lat, "longitude": lon, "altitude_m": alt,
		"heading_deg": hdg, "speed_mps": spd, "timestamp": ts,
	}
}

func GetFlightHistory(uavID string, limit int) []map[string]interface{} {
	if limit <= 0 {
		limit = 100
	}
	rows, err := engine.Query(`SELECT uav_id, latitude, longitude, altitude_m, heading_deg, speed_mps, timestamp
		FROM uas_positions WHERE uav_id=? ORDER BY id DESC LIMIT ?`, uavID, limit)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []map[string]interface{}
	for rows.Next() {
		var uid string
		var lat, lon, alt, hdg, spd float64
		var ts string
		rows.Scan(&uid, &lat, &lon, &alt, &hdg, &spd, &ts)
		out = append(out, map[string]interface{}{
			"uav_id": uid, "latitude": lat, "longitude": lon, "altitude_m": alt,
			"heading_deg": hdg, "speed_mps": spd, "timestamp": ts,
		})
	}
	return out
}

// DetectAnomaly checks for flight envelope deviations — used by the
// TS 23.256 §5.2.6 location-reporting path to flag the USS.
func DetectAnomaly(uavID string) map[string]interface{} {
	pos := GetPosition(uavID)
	if pos == nil {
		return map[string]interface{}{"anomaly": false, "details": []string{"No position data"}}
	}

	flight := getActiveFlight(uavID)
	if flight == nil {
		return map[string]interface{}{"anomaly": false, "details": []string{"No active flight plan"}}
	}

	uav, _ := GetUAVByUAVID(uavID)
	var details []string

	// Default cap = 120 m AGL (FAA Part 107 / EASA Open category — the
	// global default the schema uses for max_altitude_m).
	maxAlt := 120.0
	if uav != nil && uav.MaxAltitudeM != nil {
		maxAlt = *uav.MaxAltitudeM
	}
	if alt, _ := pos["altitude_m"].(float64); alt > maxAlt {
		details = append(details, fmt.Sprintf("Altitude %.1fm exceeds max %.1fm", alt, maxAlt))
	}

	maxSpd := 20.0
	if uav != nil && uav.MaxSpeedMPS != nil {
		maxSpd = *uav.MaxSpeedMPS
	}
	if spd, _ := pos["speed_mps"].(float64); spd > maxSpd {
		details = append(details, fmt.Sprintf("Speed %.1fm/s exceeds max %.1fm/s", spd, maxSpd))
	}

	lat, _ := pos["latitude"].(float64)
	lon, _ := pos["longitude"].(float64)
	zones, _ := ListNoFlyZones()
	for _, z := range zones {
		if lat >= z.LatMin && lat <= z.LatMax && lon >= z.LonMin && lon <= z.LonMax {
			details = append(details, fmt.Sprintf("Inside no-fly zone: %s", z.Name))
		}
	}

	return map[string]interface{}{"anomaly": len(details) > 0, "details": details}
}

func getActiveFlight(uavID string) map[string]interface{} {
	row := engine.QueryRow(`SELECT flight_id, flight_plan_json, status FROM uas_flight_auth
		WHERE uav_id=? AND status='authorized' ORDER BY id DESC LIMIT 1`, uavID)
	var fid, plan, status string
	if row.Scan(&fid, &plan, &status) != nil {
		return nil
	}
	return map[string]interface{}{"flight_id": fid, "flight_plan_json": plan, "status": status}
}

// RemoteIDBroadcast assembles a Network Remote ID frame for a UAV
// (TS 23.256 §5.2.5 — Net-RID, with field semantics borrowed from
// ASTM F3411-22a §4 for IDType, UAType, Operator ID, and the
// authenticated Location/Vector).
//
// TODO(ASTM F3411 §5): Direct (broadcast) Remote ID over BLE / Wi-Fi
// NaN is the UAV-local responsibility, not the 5GC's.
func RemoteIDBroadcast(uavID string) (map[string]interface{}, error) {
	uav, err := GetUAVByUAVID(uavID)
	if err != nil {
		return nil, err
	}
	if uav == nil {
		return nil, fmt.Errorf("UAV %s not found", uavID)
	}

	pos := GetPosition(uavID)
	flight := getActiveFlight(uavID)

	uasID := uavID
	if uav.SerialNumber != nil {
		uasID = *uav.SerialNumber
	}

	rid := map[string]interface{}{
		"ua_type": "Rotorcraft", "id_type": "serial_number", "uas_id": uasID,
		"uav_id":      uavID,
		"operator_id": "", "timestamp_utc": time.Now().UTC().Format(time.RFC3339),
	}
	if uav.SerialNumber != nil {
		rid["serial_number"] = *uav.SerialNumber
	} else {
		rid["serial_number"] = ""
	}
	if uav.IMSI != nil {
		rid["operator_id"] = *uav.IMSI
	}
	if pos != nil {
		rid["latitude"] = pos["latitude"]
		rid["longitude"] = pos["longitude"]
		rid["geodetic_altitude_m"] = pos["altitude_m"]
		rid["height_agl_m"] = pos["altitude_m"]
		rid["direction_deg"] = pos["heading_deg"]
		rid["speed_horizontal_mps"] = pos["speed_mps"]
		rid["timestamp"] = pos["timestamp"]
	}
	if flight != nil {
		rid["flight_id"] = flight["flight_id"]
		rid["flight_status"] = flight["status"]
	} else {
		rid["flight_id"] = nil
		rid["flight_status"] = "none"
	}
	return rid, nil
}

// ─── C2 Link Management (TS 23.256 §5.5) ─────────────────────────

// C2Default5QI is the standardized default for UAV command & control
// PDU sessions — 5QI=3 (V2X / DCC characteristic; PDB 50 ms,
// PER 1e-3, GBR) per TS 23.501 §5.7.4 / Table 5.7.4-1, called out
// for UAS use in TS 23.256 §5.5.
const C2Default5QI = 3

type C2Session struct {
	ID           int64  `json:"id"`
	UAVID        string `json:"uav_id"`
	ControllerID string `json:"controller_id"`
	QoS5QI       int    `json:"qos_5qi"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
}

// EstablishC2 sets up a UAV ↔ UAV-C control link (TS 23.256 §5.2.3
// pairing followed by §5.5 C2 communication establishment). One
// active C2 session per UAV is the model the spec describes.
func EstablishC2(uavID, controllerID string, qos5qi int) (map[string]interface{}, error) {
	if qos5qi <= 0 {
		qos5qi = C2Default5QI
	}
	uav, err := GetUAVByUAVID(uavID)
	if err != nil {
		return nil, err
	}
	if uav == nil {
		return nil, fmt.Errorf("UAV %s not found", uavID)
	}

	row := engine.QueryRow(`SELECT id FROM uas_c2_sessions WHERE uav_id=? AND status='active' LIMIT 1`, uavID)
	var existingID int64
	if row.Scan(&existingID) == nil {
		return nil, fmt.Errorf("UAV %s already has active C2 session id=%d", uavID, existingID)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	res, err := engine.Exec(`INSERT INTO uas_c2_sessions (uav_id, controller_id, qos_5qi, status, created_at)
		VALUES (?,?,?,'active',?)`, uavID, controllerID, qos5qi, now)
	if err != nil {
		return nil, err
	}
	sid, _ := res.LastInsertId()

	return map[string]interface{}{
		"c2_session_id": sid, "uav_id": uavID, "controller_id": controllerID,
		"qos_5qi": qos5qi, "status": "active",
	}, nil
}

func GetC2Status(sessionID int64) (*C2Session, error) {
	row := engine.QueryRow(`SELECT id, uav_id, controller_id, qos_5qi, status, created_at
		FROM uas_c2_sessions WHERE id=?`, sessionID)
	var s C2Session
	err := row.Scan(&s.ID, &s.UAVID, &s.ControllerID, &s.QoS5QI, &s.Status, &s.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

func TerminateC2(sessionID int64) error {
	_, err := engine.Exec(`UPDATE uas_c2_sessions SET status='terminated' WHERE id=? AND status='active'`, sessionID)
	return err
}

// FailoverC2 marks a C2 session failed and surfaces a manual-action
// hint. TODO(TS 23.256 §5.6): full C2 link recovery (alternate DRB
// switch / redundant DN) is not implemented — today the operator
// must re-establish via EstablishC2 after addressing root cause.
func FailoverC2(sessionID int64) (map[string]interface{}, error) {
	s, err := GetC2Status(sessionID)
	if err != nil {
		return nil, err
	}
	if s == nil {
		return nil, fmt.Errorf("C2 session %d not found", sessionID)
	}

	engine.Exec(`UPDATE uas_c2_sessions SET status='failed' WHERE id=?`, sessionID)
	return map[string]interface{}{
		"c2_session_id": sessionID, "status": "failed",
		"uav_id": s.UAVID, "controller_id": s.ControllerID,
		"action": "manual_failover_required",
	}, nil
}

// CheckAuthorization returns the current §5.2.4 flight auth status.
func CheckAuthorization(uavID string) map[string]interface{} {
	flight := getActiveFlight(uavID)
	if flight == nil {
		return map[string]interface{}{"uav_id": uavID, "authorized": false, "flight": nil}
	}
	return map[string]interface{}{"uav_id": uavID, "authorized": true, "flight": flight}
}

// GetUASStats returns aggregate counters for the OAM panel.
func GetUASStats() map[string]interface{} {
	var total, active, registered, flights, c2 int
	row := engine.QueryRow(`SELECT COUNT(*) FROM uas_registry`)
	row.Scan(&total)
	row = engine.QueryRow(`SELECT COUNT(*) FROM uas_registry WHERE status='active'`)
	row.Scan(&active)
	row = engine.QueryRow(`SELECT COUNT(*) FROM uas_registry WHERE status='registered'`)
	row.Scan(&registered)
	row = engine.QueryRow(`SELECT COUNT(*) FROM uas_flight_auth WHERE status='authorized'`)
	row.Scan(&flights)
	row = engine.QueryRow(`SELECT COUNT(*) FROM uas_c2_sessions WHERE status='active'`)
	row.Scan(&c2)
	zones, _ := ListNoFlyZones()
	return map[string]interface{}{
		"total_uavs": total, "active_uavs": active, "registered_uavs": registered,
		"active_flights": flights, "active_c2_sessions": c2, "no_fly_zones": len(zones),
	}
}

// ─── helpers ─────────────────────────────────────────────────────

func nilStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
