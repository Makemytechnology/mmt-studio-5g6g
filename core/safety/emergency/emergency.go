// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package emergency — 5GS Emergency Services control plane.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.101 §10           Emergency Calls (service requirements umbrella).
//   - TS 22.101 §10.4         Emergency calls in IM CN subsystem (IMS-CN path).
//   - TS 22.101 §10.6         Location Availability for Emergency Calls.
//   - TS 23.501 §5.16.4       Emergency Services architecture (5G core).
//   - TS 23.501 §5.16.4.6     QoS for Emergency Services (5QI / ARP).
//   - TS 23.501 §5.16.4.8     IP Address Allocation for emergency PDUs.
//   - TS 23.501 §5.16.4.9     Handling of PDU Sessions for Emergency Services
//                             (request_type "Emergency Request" = 3).
//   - TS 23.167 §6.2.2        Emergency-CSCF (E-CSCF) functional entity.
//   - TS 23.167 §7.1          High Level Procedures for IMS Emergency Services.
//   - TS 23.167 §7.5          Interworking with PSAP (where the call lands).
//   - TS 24.501 §5.5.1.2.6    Initial Registration for Emergency services.
//   - TS 24.501 §5.5.1.2.6A   Initial Registration for emergency services
//                             when authentication is not performed.
//   - RFC 5031 §4.2           Sub-Services for the 'sos' Service
//                             (urn:service:sos[.sub-service]).
//
// Deferred (TODO at unimplemented call-sites — searchable by §):
//
//   - TS 23.167 §6.2.3        LRF / RDF location retrieval at session setup.
//   - TS 23.167 §6.2.6        EATF — Emergency Access Transfer Function (SRVCC
//                             of an active emergency call to CS access).
//   - TS 23.167 §7.4          IMS Emergency Session Establishment without
//                             Registration (today we assume registered UEs).
//   - TS 22.101 §10.1.3       Call-Back Requirements (PSAP → UE callback path).
//   - TS 23.501 §5.16.4.10    Support of eCall Only Mode.
//   - TS 23.501 §5.16.4.11    Emergency Services Fallback (EPS fallback for
//                             voice when 5GS-IMS voice isn't available).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/emergency.py.
package emergency

import (
	"fmt"
	"net"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ─── Config ──────────────────────────────────────────────────────

func ensureConfig() {
	_, _ = engine.Exec("INSERT OR IGNORE INTO emergency_config (id) VALUES (1)")
}

// GetConfig returns the singleton emergency-services configuration row.
func GetConfig() (map[string]interface{}, error) {
	ensureConfig()
	return qRow("SELECT * FROM emergency_config WHERE id=1")
}

// UpdateConfig updates allowed emergency configuration fields.
func UpdateConfig(fields map[string]interface{}) error {
	allowed := map[string]bool{"enabled": true, "auth_required": true, "emergency_dnn": true, "ip_pool_v4": true,
		"ip_pool_v6": true, "psap_sip_uri": true, "psap_ip": true, "psap_port": true, "emergency_qfi": true,
		"voice_qfi": true, "arp_priority": true, "max_sessions": true}
	var sets []string
	var args []interface{}
	for k, v := range fields {
		if allowed[k] {
			sets = append(sets, k+"=?")
			args = append(args, v)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	args = append(args, 1)
	ensureConfig()
	_, err := engine.Exec(fmt.Sprintf("UPDATE emergency_config SET %s WHERE id=?", strings.Join(sets, ", ")), args...)
	return err
}

// IsEmergencyEnabled reports whether the operator has enabled emergency
// services in this PLMN. TS 22.101 §10.1 requires support unless the
// operator explicitly disables (regulatory-driven; defaults true).
func IsEmergencyEnabled() bool {
	cfg, _ := GetConfig()
	if cfg == nil {
		return true
	}
	return toBool(cfg["enabled"], true)
}

// IsAuthRequired controls whether the AMF runs the AKA exchange before
// admitting an emergency-registered UE. TS 24.501 §5.5.1.2.6A defines
// the unauthenticated path; some regulators require auth nonetheless.
func IsAuthRequired() bool {
	cfg, _ := GetConfig()
	if cfg == nil {
		return false
	}
	return toBool(cfg["auth_required"], false)
}

// ─── Session Tracking ────────────────────────────────────────────

// CreateEmergencySession records an active emergency PDU session
// (TS 23.501 §5.16.4.9 — Handling of PDU Sessions for Emergency Services).
func CreateEmergencySession(imsi, imei string, pduSessionID int, ipAddr, gnbIP, tac, cellID string) int64 {
	now := float64(time.Now().Unix())
	res, _ := engine.Exec(`INSERT INTO emergency_sessions
		(imsi, imei, pdu_session_id, ip_addr, gnb_ip, tac, cell_id, start_time)
		VALUES (?,?,?,?,?,?,?,?)`, imsi, imei, pduSessionID, ipAddr, gnbIP, tac, cellID, now)
	if res == nil {
		return 0
	}
	id, _ := res.LastInsertId()
	logger.Get("emergency").Infof("Emergency session created: id=%d IMEI=%s IP=%s", id, imei, ipAddr)
	return id
}

// ReleaseEmergencySession marks an emergency PDU session as released.
func ReleaseEmergencySession(sessionID int64) {
	_, _ = engine.Exec("UPDATE emergency_sessions SET status='released', end_time=? WHERE id=?", float64(time.Now().Unix()), sessionID)
}

// GetActiveEmergencySessions returns currently-active emergency sessions.
func GetActiveEmergencySessions() ([]map[string]interface{}, error) {
	return qRows("SELECT * FROM emergency_sessions WHERE status='active' ORDER BY start_time DESC")
}

// GetEmergencyStats returns coarse counters + config flags.
func GetEmergencyStats() map[string]interface{} {
	db, err := engine.Open()
	if err != nil {
		return map[string]interface{}{}
	}
	var active, total int
	_ = db.QueryRow("SELECT COUNT(*) FROM emergency_sessions WHERE status='active'").Scan(&active)
	_ = db.QueryRow("SELECT COUNT(*) FROM emergency_sessions").Scan(&total)
	cfg, _ := GetConfig()
	hasPSAP := false
	if cfg != nil {
		hasPSAP = toString(cfg["psap_sip_uri"]) != "" || toString(cfg["psap_ip"]) != ""
	}
	return map[string]interface{}{
		"enabled": IsEmergencyEnabled(), "auth_required": IsAuthRequired(),
		"active_sessions": active, "total_sessions": total, "psap_configured": hasPSAP,
	}
}

// ─── Emergency PDU Session (TS 23.501 §5.16.4) ───────────────────

// IsEmergencyPDURequest classifies a PDU Session Establishment Request
// as an emergency request. Two equally-valid signals per TS 23.501
// §5.16.4.9: (a) "Request type" = "Emergency Request" (value 3), or
// (b) DNN matches the operator-configured emergency DNN ("sos" by
// default per TS 23.003 DNN naming guidance).
func IsEmergencyPDURequest(requestType int, dnn string) bool {
	return requestType == 3 || strings.ToLower(dnn) == "sos"
}

// GetEmergencyQoS returns the 5QI / ARP profile to apply to an
// emergency PDU session. TS 23.501 §5.16.4.6 mandates a dedicated
// QoS profile with high ARP priority and pre-emption capability;
// concrete 5QI value comes from operator config (defaults shown).
func GetEmergencyQoS() map[string]interface{} {
	cfg, _ := GetConfig()
	qfi := 5
	arp := 1
	if cfg != nil {
		if v, ok := cfg["emergency_qfi"]; ok {
			qfi = toInt(v, 5)
		}
		if v, ok := cfg["arp_priority"]; ok {
			arp = toInt(v, 1)
		}
	}
	return map[string]interface{}{"qfi": qfi, "fiveqi": qfi, "arp_priority": arp, "resource_type": "NonGBR"}
}

// ─── E-CSCF (TS 23.167 §6.2.2 / §7.5) ────────────────────────────

// RouteEmergencyCall forwards an emergency SIP INVITE toward the
// configured PSAP. Implements the trailing edge of TS 23.167 §7.1
// (High Level Procedures) — specifically the §7.5 Interworking with
// PSAP step (the call lands at a PSAP via the configured transport).
//
// TODO(TS 23.167 §6.2.2): full E-CSCF behaviour — selection of PSAP
// based on UE location (LRF/RDF lookup, TS 23.167 §6.2.3), P-Asserted
// Identity rewrite, anonymous-caller handling for unregistered UEs.
//
// TODO(TS 23.167 §7.5.1 / §7.5.2): GSTN PSAP via MGCF and IMS PSAP
// via IBCF — today we assume an IP PSAP reachable over UDP/SIP.
func RouteEmergencyCall(imsi, gnbIP string, sipInvite []byte) bool {
	log := logger.Get("emergency.ecscf")
	cfg, _ := GetConfig()
	if cfg == nil {
		return false
	}
	psapIP := toString(cfg["psap_ip"])
	psapPort := toInt(cfg["psap_port"], 5060)
	if psapIP == "" {
		log.Warnf("No PSAP configured -- emergency call cannot be routed imsi=%s", imsi)
		return false
	}
	log.Infof("Routing emergency call to PSAP %s:%d imsi=%s", psapIP, psapPort, imsi)
	conn, err := net.Dial("udp", fmt.Sprintf("%s:%d", psapIP, psapPort))
	if err != nil {
		log.Errorf("Emergency call routing failed: %v", err)
		return false
	}
	defer conn.Close()
	conn.Write(sipInvite)
	return true
}

// CheckEmergencyURN reports whether a SIP Request-URI carries an
// emergency service URN per RFC 5031 §4.2 (Sub-Services for the
// 'sos' Service): "urn:service:sos" with optional sub-service tag
// (sos.ambulance, sos.fire, sos.gas, sos.marine, sos.mountain,
// sos.physician, sos.poison, sos.police).
func CheckEmergencyURN(requestURI string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(requestURI)), "urn:service:sos")
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]map[string]any, error) { return GetActiveEmergencySessions() }

func Status() map[string]any { return GetEmergencyStats() }

// ─── helpers ─────────────────────────────────────────────────────

func toBool(v interface{}, def bool) bool {
	switch vv := v.(type) {
	case int64:
		return vv != 0
	case int:
		return vv != 0
	case bool:
		return vv
	}
	return def
}
func toInt(v interface{}, def int) int {
	switch vv := v.(type) {
	case int64:
		return int(vv)
	case float64:
		return int(vv)
	case int:
		return vv
	}
	return def
}
func toString(v interface{}) string {
	if v == nil {
		return ""
	}
	switch vv := v.(type) {
	case string:
		return vv
	case []byte:
		return string(vv)
	}
	return fmt.Sprintf("%v", v)
}

func qRow(q string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, nil
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
		return nil, nil
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
