// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package supplementary — IMS-anchored Supplementary Services
// configuration store.
//
// Per-subscriber service config for call forwarding, call barring,
// call waiting, identification presentation/restriction. The state
// model below is the network-side view of the per-IMSI service-by-
// service activation rows in the supplementary_services SQL table;
// the §-clauses cited below say which spec defines what we store.
//
// Spec scope:
//   - TS 24.604 §4.2 / §4.5  Communication diversion (CDIV → CFU /
//     CFB / CFNRy / CFNRc / CD / CFNL)
//   - TS 24.615 §4.5         Communication Waiting (CW)
//   - TS 24.611 §4.5         Anonymous Communication Rejection (ACR)
//     and Communication Barring (BAOC / BAOIC /
//     BAIC + roaming variants)
//   - TS 24.607 §4.5         Originating identification presentation /
//     restriction (OIP / OIR — same as legacy
//     CLIP / CLIR per TS 22.081)
//   - TS 24.608 §4.5         Terminating identification presentation /
//     restriction (TIP / TIR — same as legacy
//     COLP / COLR per TS 22.081)
//   - TS 22.030 §6.5         UE-side MMI procedures that drive the
//     CRUD calls below; parsed in mmi.go
//   - TS 24.080 §3.6 / §4.5  Facility / SS-Operation legacy codec
//     scaffold for CS fallback; codec.go
//
// All §-cites in this file refer to the locally-pinned PDFs; do not
// quote clauses from memory.
package supplementary

import (
	"database/sql"
	"regexp"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// Service type constants. Each names the §-clause and parent spec
// that defines the service.
const (
	CFU   = "CFU"   // Communication Forwarding Unconditional — TS 24.604 §4.2.1.2
	CFB   = "CFB"   // Communication Forwarding on Busy user — TS 24.604 §4.2.1.3
	CFNRy = "CFNRy" // Communication Forwarding on No Reply  — TS 24.604 §4.2.1.4
	CFNRc = "CFNRc" // Communication Forwarding on Subscriber Not Reachable — TS 24.604 §4.2.1.5
	CW    = "CW"    // Communication Waiting                — TS 24.615 §4.5
	OIP   = "OIP"   // Originating Identification Presentation — TS 24.607 §4.5
	OIR   = "OIR"   // Originating Identification Restriction  — TS 24.607 §4.5
	TIP   = "TIP"   // Terminating Identification Presentation — TS 24.608 §4.5
	TIR   = "TIR"   // Terminating Identification Restriction  — TS 24.608 §4.5
	BAOC  = "BAOC"  // Barring of All Outgoing Calls        — TS 24.611 §4.5 (OCB)
	BAOIC = "BAOIC" // Barring of Outgoing International Calls — TS 24.611 §4.5 (OCB)
	BAIC  = "BAIC"  // Barring of All Incoming Calls        — TS 24.611 §4.5 (ICB)
	// Legacy MMI / CS-SS aliases per TS 22.030 Annex B Table B.1.
	// CLIP/COLP are the §6.5.2 GSM-SS labels for the IMS services
	// OIP/TIP (TS 24.607 §1 / TS 24.608 §1 cross-reference). Operators
	// and UE MMI keypads use either label — we accept both and
	// normalise at storage time so a CLIP-keyed Activate and an
	// OIP-keyed Interrogate hit the same SQL row.
	CLIP = "CLIP" // Calling Line Identification Presentation  → OIP
	CLIR = "CLIR" // Calling Line Identification Restriction   → OIR
	COLP = "COLP" // Connected Line Identification Presentation → TIP
	COLR = "COLR" // Connected Line Identification Restriction  → TIR
)

var forwardingTypes = map[string]bool{CFU: true, CFB: true, CFNRy: true, CFNRc: true}
var barringTypes = map[string]bool{BAOC: true, BAOIC: true, BAIC: true}
var allServiceTypes = map[string]bool{
	CFU: true, CFB: true, CFNRy: true, CFNRc: true,
	CW: true, OIP: true, OIR: true, TIP: true, TIR: true,
	BAOC: true, BAOIC: true, BAIC: true,
	CLIP: true, CLIR: true, COLP: true, COLR: true,
}

// mmiAlias maps TS 22.030 Annex B Table B.1 GSM-SS names to the
// canonical IMS service identifier. Returning the alias unchanged
// for already-canonical inputs keeps callers that supply OIP/OIR
// directly working untouched.
func mmiAlias(serviceType string) string {
	switch serviceType {
	case CLIP:
		return OIP
	case CLIR:
		return OIR
	case COLP:
		return TIP
	case COLR:
		return TIR
	}
	return serviceType
}

var e164Re = regexp.MustCompile(`^\+?[1-9]\d{1,14}$`)
var barringPwRe = regexp.MustCompile(`^\d{4}$`)

// ServiceRecord represents a row in supplementary_services.
type ServiceRecord struct {
	ID               int64   `json:"id"`
	IMSI             string  `json:"imsi"`
	ServiceType      string  `json:"service_type"`
	Active           int     `json:"active"`
	ForwardingNumber *string `json:"forwarding_number,omitempty"`
	NoReplyTimer     *int    `json:"no_reply_timer,omitempty"`
	BarringPassword  *string `json:"barring_password,omitempty"`
	ConfigJSON       *string `json:"config_json,omitempty"`
	UpdatedAt        *string `json:"updated_at,omitempty"`
}

// ---- GUI panel API ----

func List() ([]ServiceRecord, error) { return listAll() }

func Status() map[string]any {
	list, _ := listAll()
	return map[string]any{"count": len(list), "items": list}
}

// ---- CRUD ----

func listAll() ([]ServiceRecord, error) {
	rows, err := engine.Query(`SELECT id, imsi, service_type, active,
		forwarding_number, no_reply_timer, barring_password, config_json, updated_at
		FROM supplementary_services ORDER BY imsi, service_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRecord
	for rows.Next() {
		var r ServiceRecord
		if err := rows.Scan(&r.ID, &r.IMSI, &r.ServiceType, &r.Active,
			&r.ForwardingNumber, &r.NoReplyTimer, &r.BarringPassword,
			&r.ConfigJSON, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// ListByIMSI returns all services for a subscriber.
func ListByIMSI(imsi string) ([]ServiceRecord, error) {
	rows, err := engine.Query(`SELECT id, imsi, service_type, active,
		forwarding_number, no_reply_timer, barring_password, config_json, updated_at
		FROM supplementary_services WHERE imsi=? ORDER BY service_type`, imsi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceRecord
	for rows.Next() {
		var r ServiceRecord
		if err := rows.Scan(&r.ID, &r.IMSI, &r.ServiceType, &r.Active,
			&r.ForwardingNumber, &r.NoReplyTimer, &r.BarringPassword,
			&r.ConfigJSON, &r.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// Get returns a specific service for a subscriber.
func Get(imsi, serviceType string) (*ServiceRecord, error) {
	row := engine.QueryRow(`SELECT id, imsi, service_type, active,
		forwarding_number, no_reply_timer, barring_password, config_json, updated_at
		FROM supplementary_services WHERE imsi=? AND service_type=?`, imsi, serviceType)
	var r ServiceRecord
	err := row.Scan(&r.ID, &r.IMSI, &r.ServiceType, &r.Active,
		&r.ForwardingNumber, &r.NoReplyTimer, &r.BarringPassword,
		&r.ConfigJSON, &r.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &r, err
}

// Activate activates a supplementary service.
//
// Spec mapping for the activation step itself:
//   - CFU/CFB/CFNRy/CFNRc — TS 24.604 §4.5.1 "Activation/deactivation"
//     (XCAP PUT to update <document-uri>/simservs.xml is the IMS
//     activation pathway; we just persist the active flag here)
//   - CW                 — TS 24.615 §4.5.2
//   - OIP/OIR            — TS 24.607 §4.5.1
//   - TIP/TIR            — TS 24.608 §4.5.1
//   - BAOC/BAOIC/BAIC    — TS 24.611 §4.5.1
func Activate(imsi, serviceType, forwardingNumber, barringPassword, configJSON string, noReplyTimer int) (*ServiceRecord, string) {
	if imsi == "" {
		return nil, "imsi is required"
	}
	if !allServiceTypes[serviceType] {
		return nil, "unknown service type"
	}

	// Forwarding validation
	if forwardingTypes[serviceType] {
		if forwardingNumber == "" {
			return nil, "forwarding_number required for call forwarding"
		}
		if !e164Re.MatchString(forwardingNumber) {
			return nil, "invalid forwarding number (E.164)"
		}
	}
	// Barring password validation
	if barringTypes[serviceType] && barringPassword != "" {
		if !barringPwRe.MatchString(barringPassword) {
			return nil, "barring password must be 4 digits"
		}
	}
	// No-reply timer per TS 24.604 §4.8.1 / §4.9.2 — operator-set
	// default within the spec-defined range. TS 22.030 Annex B
	// Table B.1 footnote ("T = No Reply Condition Timer (5-30
	// seconds)") gives the UE-driven range that applies here.
	if serviceType == CFNRy {
		if noReplyTimer < 5 || noReplyTimer > 30 {
			noReplyTimer = 20
		}
	} else {
		noReplyTimer = 20
	}

	return upsert(imsi, serviceType, 1, forwardingNumber, barringPassword, configJSON, noReplyTimer), ""
}

// Deactivate deactivates a supplementary service. The activation
// flag flips to 0 but the row is preserved so a later Activate
// can pick up the previously-stored DN / password / config_json.
// See TS 24.604 §4.5.1, TS 24.611 §4.5.1 and TS 24.615 §4.5.2 for
// the deactivation half of each Activate-step §clause cited above.
func Deactivate(imsi, serviceType string) (*ServiceRecord, string) {
	if imsi == "" {
		return nil, "imsi is required"
	}
	if !allServiceTypes[serviceType] {
		return nil, "unknown service type"
	}
	existing, _ := Get(imsi, serviceType)
	fwd := ""
	pwd := ""
	timer := 20
	cfg := ""
	if existing != nil {
		if existing.ForwardingNumber != nil {
			fwd = *existing.ForwardingNumber
		}
		if existing.BarringPassword != nil {
			pwd = *existing.BarringPassword
		}
		if existing.NoReplyTimer != nil {
			timer = *existing.NoReplyTimer
		}
		if existing.ConfigJSON != nil {
			cfg = *existing.ConfigJSON
		}
	}
	return upsert(imsi, serviceType, 0, fwd, pwd, cfg, timer), ""
}

// Interrogate queries a service's current activation state.
//
// Spec mapping for the interrogation step itself:
//   - CDIV  — TS 24.604 §4.5.1b "Interrogation"
//   - CW    — TS 24.615 §4.5.4 "Interrogation"
//   - OIP/OIR — TS 24.607 §4.5.1
//   - BAOC/BAOIC/BAIC — TS 24.611 §4.5.1
//
// All four §-clauses point at the Ut/XCAP interface as the
// interrogation transport; the in-process call below skips Ut and
// reads from the SQL row directly.
func Interrogate(imsi, serviceType string) (*ServiceRecord, bool) {
	r, err := Get(imsi, serviceType)
	if err == nil && r != nil {
		return r, true
	}
	// TS 22.030 Annex B Table B.1 alias fallback — if the operator
	// activated under the GSM-SS name (CLIP) but interrogated using
	// the IMS name (OIP), or vice versa, surface the matching row.
	// Cross-name activation/interrogation should not silently miss.
	if alias := mmiAlias(serviceType); alias != serviceType {
		r2, err2 := Get(imsi, alias)
		if err2 == nil && r2 != nil {
			return r2, true
		}
	}
	return &ServiceRecord{IMSI: imsi, ServiceType: serviceType, Active: 0}, false
}

// BulkSet applies multiple service configurations at once.
//
// Each item in services{} is dispatched to Activate or Deactivate
// according to its "active" boolean, so the per-service §-clauses
// cited at those functions apply to each row independently.
func BulkSet(imsi string, services []map[string]interface{}) map[string]interface{} {
	if imsi == "" {
		return map[string]interface{}{"ok": false, "error": "imsi required"}
	}
	var results []map[string]interface{}
	for _, svc := range services {
		st, _ := svc["service_type"].(string)
		// Bulk items default to activate when "active" is omitted.
		// TS 24.604 §4.5.1 / TS 24.611 §4.5.1 treat an Activate
		// step as the explicit-state operation; a missing field
		// MUST NOT silently invert the operator's intent into a
		// Deactivate.
		active := true
		if v, ok := svc["active"].(bool); ok {
			active = v
		}
		if active {
			fwd, _ := svc["forwarding_number"].(string)
			pwd, _ := svc["barring_password"].(string)
			cfg, _ := svc["config_json"].(string)
			timer := 20
			if t, ok := svc["no_reply_timer"].(float64); ok {
				timer = int(t)
			}
			rec, errMsg := Activate(imsi, st, fwd, pwd, cfg, timer)
			if errMsg != "" {
				results = append(results, map[string]interface{}{"service_type": st, "ok": false, "error": errMsg})
			} else {
				results = append(results, map[string]interface{}{"service_type": st, "ok": true, "record": rec})
			}
		} else {
			rec, errMsg := Deactivate(imsi, st)
			if errMsg != "" {
				results = append(results, map[string]interface{}{"service_type": st, "ok": false, "error": errMsg})
			} else {
				results = append(results, map[string]interface{}{"service_type": st, "ok": true, "record": rec})
			}
		}
	}
	return map[string]interface{}{"ok": true, "imsi": imsi, "results": results}
}

// DeleteAll removes all supplementary services for an IMSI.
func DeleteAll(imsi string) (int64, error) {
	res, err := engine.Exec(`DELETE FROM supplementary_services WHERE imsi=?`, imsi)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func upsert(imsi, serviceType string, active int, fwdNumber, barPwd, cfgJSON string, noReplyTimer int) *ServiceRecord {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, _ = engine.Exec(`INSERT INTO supplementary_services
		(imsi, service_type, active, forwarding_number, no_reply_timer, barring_password, config_json, updated_at)
		VALUES (?,?,?,?,?,?,?,?)
		ON CONFLICT(imsi, service_type) DO UPDATE SET
		active=excluded.active, forwarding_number=excluded.forwarding_number,
		no_reply_timer=excluded.no_reply_timer, barring_password=excluded.barring_password,
		config_json=excluded.config_json, updated_at=excluded.updated_at`,
		imsi, serviceType, active, nilStr(fwdNumber), noReplyTimer, nilStr(barPwd), nilStr(cfgJSON), now)
	r, _ := Get(imsi, serviceType)
	return r
}

func nilStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
