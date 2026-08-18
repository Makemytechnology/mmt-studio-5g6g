// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package v2x — Vehicle-to-Everything service over 5GS.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.186 §5            5G V2X service requirements (high-level
//                             use cases: vehicle platooning, advanced
//                             driving, extended sensors, remote driving).
//   - TS 23.287 §4.2          Reference architecture for V2X over 5GS.
//   - TS 23.287 §4.4          5G V2X policy / parameter provisioning.
//   - TS 23.287 §5.1.2        V2X policy / parameter provisioning
//                             procedure (PCF → UE).
//   - TS 23.287 §5.2          V2X authorization (subscription + UE
//                             authorization for V2X over PC5/Uu).
//   - TS 23.287 §5.4          PC5 QoS framework (PQI, ARP, PFI, PDB,
//                             PER, max data burst, averaging window).
//   - TS 23.287 §5.4.4        Standardized PQI values (Table 5.4.4-1).
//   - TS 23.287 §5.5          V2X subscription data (PC5 AMBR, UE type).
//   - TS 24.587 §5            5G NAS procedures for V2X over PC5
//                             (V2X policy delivery via UE Policy
//                             Container, TS 24.501 §D.6.1).
//   - TS 24.588 §5            PC5 signalling protocol procedures
//                             (link establishment, modification, release
//                             on PC5-S).
//
// Deferred (TODO at unimplemented call-sites — searchable by §):
//
//   - TS 23.287 §5.3          V2X service authorisation in roaming
//                             (HPLMN vs. VPLMN policy).
//   - TS 23.287 §5.6          UE-to-Network relay (V2X over Uu via
//                             relay UE — TS 23.304 hand-off).
//   - TS 23.287 §6.x          V2X message family routing
//                             (V2X-PSID gating, application-layer).
//   - TS 24.588 §6            PC5 unicast link security establishment
//                             (PC5 RRC + V2X security — TS 33.536).
//   - TS 22.186 §5.5          Remote driving QoS budgets (≥10 Mb/s UL).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/v2x.py.
package v2x

import (
	"database/sql"
	"errors"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// errInvalidUEType is returned by AuthorizeUE when ueType is not
// in the TS 23.287 §5.5 enum {"vehicle","pedestrian"}.
var errInvalidUEType = errors.New("invalid v2x_ue_type (must be 'vehicle' or 'pedestrian')")

// ServiceType represents one row of v2x_service_types — i.e. one
// (PQI → 5G QoS attributes) mapping per TS 23.287 §5.4.4 / Table
// 5.4.4-1 (Standardized PQI to QoS characteristics mapping).
type ServiceType struct {
	ID              int64   `json:"id"`
	ServiceName     string  `json:"service_name"`
	PQI             int     `json:"pqi"`
	ResourceType    string  `json:"resource_type"`
	PriorityLevel   int     `json:"priority_level"`
	PacketDelayMS   int     `json:"packet_delay_ms"`
	PacketErrorRate string  `json:"packet_error_rate"`
	MaxDataBurst    *int    `json:"max_data_burst,omitempty"`
	AvgWindowMS     *int    `json:"avg_window_ms,omitempty"`
	FiveQIUu        *int    `json:"fiveqi_uu,omitempty"`
	Description     *string `json:"description,omitempty"`
}

// Config represents a row in v2x_config (operator-tunable knobs:
// NR PC5 frequencies, TX power class, congestion thresholds, etc.).
type Config struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// V2XSubscription holds the V2X-specific subscription fields that
// the UDM/PCF reads off the ue table. Mirrors the data shape of the
// "V2X subscription" container in TS 23.287 §5.5.
type V2XSubscription struct {
	V2XAuthorized  bool   `json:"v2x_authorized"`
	V2XUEType      string `json:"v2x_ue_type"`
	V2XPC5AMBRKbps int    `json:"v2x_pc5_ambr_kbps"`
}

// ─── GUI panel API ───────────────────────────────────────────────

// List returns all configured PQI mappings (preserves stub API).
func List() ([]ServiceType, error) { return ListServiceTypes() }

// Status returns a summary for the GUI panel.
func Status() map[string]any {
	list, _ := ListServiceTypes()
	return map[string]any{"count": len(list), "items": list}
}

// ─── Service Type CRUD (TS 23.287 §5.4.4) ────────────────────────

// ListServiceTypes returns all PQI-to-QoS mappings — the tabulated
// values that the AMF/SMF/PCF consult when assigning a flow's QoS
// over PC5 (TS 23.287 §5.4.4 Table 5.4.4-1).
func ListServiceTypes() ([]ServiceType, error) {
	rows, err := engine.Query(`SELECT id, service_name, pqi, resource_type,
		priority_level, packet_delay_ms, packet_error_rate,
		max_data_burst, avg_window_ms, fiveqi_uu, description
		FROM v2x_service_types ORDER BY pqi`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ServiceType
	for rows.Next() {
		var s ServiceType
		if err := rows.Scan(&s.ID, &s.ServiceName, &s.PQI, &s.ResourceType,
			&s.PriorityLevel, &s.PacketDelayMS, &s.PacketErrorRate,
			&s.MaxDataBurst, &s.AvgWindowMS, &s.FiveQIUu, &s.Description); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetServiceType returns the (PQI → QoS) row for a given PQI value.
// PQI is the PC5 analogue of 5QI on Uu (TS 23.287 §5.4.4).
func GetServiceType(pqi int) (*ServiceType, error) {
	row := engine.QueryRow(`SELECT id, service_name, pqi, resource_type,
		priority_level, packet_delay_ms, packet_error_rate,
		max_data_burst, avg_window_ms, fiveqi_uu, description
		FROM v2x_service_types WHERE pqi=?`, pqi)
	var s ServiceType
	err := row.Scan(&s.ID, &s.ServiceName, &s.PQI, &s.ResourceType,
		&s.PriorityLevel, &s.PacketDelayMS, &s.PacketErrorRate,
		&s.MaxDataBurst, &s.AvgWindowMS, &s.FiveQIUu, &s.Description)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &s, err
}

// ─── V2X Operator Config ─────────────────────────────────────────

// GetConfig returns a V2X config value by key (e.g. "nr_pc5_frequencies").
func GetConfig(key string) (string, error) {
	row := engine.QueryRow(`SELECT value FROM v2x_config WHERE key=?`, key)
	var val string
	err := row.Scan(&val)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return val, err
}

// SetConfig sets a V2X config value.
func SetConfig(key, value string) error {
	_, err := engine.Exec(`INSERT OR REPLACE INTO v2x_config (key, value)
		VALUES (?,?)`, key, value)
	return err
}

// ListConfig returns all V2X config entries.
func ListConfig() ([]Config, error) {
	rows, err := engine.Query(`SELECT key, value FROM v2x_config ORDER BY key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Config
	for rows.Next() {
		var c Config
		if err := rows.Scan(&c.Key, &c.Value); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ─── Subscription Helpers (TS 23.287 §5.5) ───────────────────────

// LoadSubscription retrieves the V2X subscription data for a UE.
// Returns nil when the UE is unknown or not authorized for V2X
// (TS 23.287 §5.5 — only authorized UEs receive policy provisioning).
func LoadSubscription(imsi string) *V2XSubscription {
	row := engine.QueryRow(`SELECT v2x_authorized, v2x_ue_type, v2x_pc5_ambr_kbps
		FROM ue WHERE imsi=?`, imsi)
	var auth int
	var ueType sql.NullString
	var ambr sql.NullInt64
	if err := row.Scan(&auth, &ueType, &ambr); err != nil {
		return nil
	}
	if auth == 0 {
		return nil
	}
	t := "vehicle"
	if ueType.Valid && ueType.String != "" {
		t = ueType.String
	}
	a := 0
	if ambr.Valid {
		a = int(ambr.Int64)
	}
	return &V2XSubscription{V2XAuthorized: true, V2XUEType: t, V2XPC5AMBRKbps: a}
}

// IsAuthorized checks whether a UE is authorized for V2X services
// (TS 23.287 §5.2 — V2X authorization predicate).
func IsAuthorized(imsi string) bool {
	sub := LoadSubscription(imsi)
	return sub != nil && sub.V2XAuthorized
}

// GetPC5QoSParams returns the full PC5 QoS table available to a
// V2X-authorized UE. PC5 QoS framework: TS 23.287 §5.4.
func GetPC5QoSParams(imsi string) []ServiceType {
	sub := LoadSubscription(imsi)
	if sub == nil {
		return nil
	}
	types, _ := ListServiceTypes()
	return types
}

// BuildV2XPolicyParams constructs the V2X policy parameters that the
// PCF delivers to a UE for V2X over PC5.
//
// Wire-format on the NAS path: TS 24.587 §5 (UE Policy Delivery) —
// the result of this function is the "V2X policy" body that the AMF
// wraps in a UE Policy Container (TS 24.501 §D.6.1). The fields
// follow TS 23.287 §5.1.2 (V2X policy / parameter provisioning).
func BuildV2XPolicyParams(imsi string, sub *V2XSubscription) map[string]interface{} {
	if sub == nil || !sub.V2XAuthorized {
		return nil
	}
	authPLMNs := loadAuthorizedPLMNs(imsi)
	qosParams := GetPC5QoSParams(imsi)
	freqs := LoadFrequencies()
	return map[string]interface{}{
		"auth_policy": map[string]interface{}{
			"authorized_plmns": authPLMNs, "ue_type": sub.V2XUEType,
			"pc5_rats": []string{"nr"}, "pc5_ambr_kbps": sub.V2XPC5AMBRKbps,
		},
		"pc5_qos_params":  qosParams,
		"v2x_frequencies": freqs,
	}
}

func loadAuthorizedPLMNs(imsi string) []string {
	rows, err := engine.Query(`SELECT plmn_id FROM v2x_authorized_plmns WHERE imsi=?`, imsi)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		rows.Scan(&p)
		out = append(out, p)
	}
	return out
}

// CreateServiceType inserts a new operator-defined PQI row. The
// PQI / 5QI / resource-type semantics are TS 23.287 §5.4.4 — Table
// 5.4.4-1 values are seeded; operators may add custom rows for their
// own (e.g. CAM/DENM-specific) flow profiles.
func CreateServiceType(s ServiceType) (int64, error) {
	res, err := engine.Exec(`INSERT INTO v2x_service_types
		(service_name, pqi, resource_type, priority_level,
		 packet_delay_ms, packet_error_rate, max_data_burst,
		 avg_window_ms, fiveqi_uu, description)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		s.ServiceName, s.PQI, s.ResourceType, s.PriorityLevel,
		s.PacketDelayMS, s.PacketErrorRate, s.MaxDataBurst,
		s.AvgWindowMS, s.FiveQIUu, s.Description)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateServiceType replaces a row by PQI. Operator-driven; updating
// the standardised Table 5.4.4-1 rows is allowed but not advised
// (the local seeds match the spec values verbatim).
func UpdateServiceType(pqi int, s ServiceType) error {
	_, err := engine.Exec(`UPDATE v2x_service_types SET
		service_name=?, resource_type=?, priority_level=?,
		packet_delay_ms=?, packet_error_rate=?, max_data_burst=?,
		avg_window_ms=?, fiveqi_uu=?, description=?
		WHERE pqi=?`,
		s.ServiceName, s.ResourceType, s.PriorityLevel,
		s.PacketDelayMS, s.PacketErrorRate, s.MaxDataBurst,
		s.AvgWindowMS, s.FiveQIUu, s.Description, pqi)
	return err
}

// DeleteServiceType drops a PQI row.
func DeleteServiceType(pqi int) error {
	_, err := engine.Exec(`DELETE FROM v2x_service_types WHERE pqi=?`, pqi)
	return err
}

// AuthorizeUE sets the V2X subscription flag on the UE row for imsi
// (TS 23.287 §5.2 V2X authorization + §5.5 V2X subscription data).
// ueType ∈ {"vehicle","pedestrian"}; pc5AMBRKbps is the PC5 AMBR
// cap that propagates into the V2X policy container.
//
// If the IMSI has no row, we INSERT one carrying the V2X fields.
// In production the UDM owns subscription provisioning; a standalone
// deployment with a curated panel is allowed to upsert here so the
// operator does not have to pre-provision the bare row first.
func AuthorizeUE(imsi, ueType string, pc5AMBRKbps int) error {
	if ueType == "" {
		ueType = "vehicle"
	}
	if ueType != "vehicle" && ueType != "pedestrian" {
		return errInvalidUEType
	}
	res, err := engine.Exec(`UPDATE ue SET v2x_authorized=1,
		v2x_ue_type=?, v2x_pc5_ambr_kbps=? WHERE imsi=?`,
		ueType, pc5AMBRKbps, imsi)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		_, err = engine.Exec(`INSERT INTO ue (imsi, v2x_authorized,
			v2x_ue_type, v2x_pc5_ambr_kbps) VALUES (?,1,?,?)`,
			imsi, ueType, pc5AMBRKbps)
		return err
	}
	return nil
}

// DeauthorizeUE clears the V2X subscription on a UE row. After this
// call ProvisionPolicy returns nil (TS 23.287 §5.1.2 — only
// authorised UEs receive V2X policy).
func DeauthorizeUE(imsi string) error {
	// Idempotent — clearing a UE that's already unauthorised
	// (or unknown) is a no-op, not an error. The spec gate is
	// "no V2X policy without authorised subscription" (§5.1.2);
	// the de-authorise itself doesn't carry that gate.
	_, err := engine.Exec(`UPDATE ue SET v2x_authorized=0,
		v2x_ue_type=NULL, v2x_pc5_ambr_kbps=NULL WHERE imsi=?`, imsi)
	return err
}

// AddAuthorizedPLMN appends a PLMN id to a UE's authorized-PLMN list
// (TS 23.287 §5.1.2 — "authorized PLMNs" element of the V2X policy
// container). Idempotent on (imsi, plmn_id).
func AddAuthorizedPLMN(imsi, plmnID string) error {
	_, err := engine.Exec(`INSERT OR IGNORE INTO v2x_authorized_plmns
		(imsi, plmn_id) VALUES (?,?)`, imsi, plmnID)
	return err
}

// DeleteAuthorizedPLMN removes a single (imsi, plmn_id) row.
func DeleteAuthorizedPLMN(imsi, plmnID string) error {
	_, err := engine.Exec(`DELETE FROM v2x_authorized_plmns
		WHERE imsi=? AND plmn_id=?`, imsi, plmnID)
	return err
}

// ListAuthorizedPLMNs returns the current list (TS 23.287 §5.1.2).
func ListAuthorizedPLMNs(imsi string) []string {
	return loadAuthorizedPLMNs(imsi)
}

// ProvisionPolicy is the operator-side surface for the TS 23.287
// §5.1.2 V2X policy / parameter provisioning procedure. It builds
// the V2X policy container body (auth_policy + pc5_qos_params +
// v2x_frequencies), writes one row to v2x_policy_log so the
// operator panel can audit "did UE X get policy", and returns the
// container body.
//
// On the wire this body would be wrapped in a UE Policy Container
// (TS 24.587 §5 / TS 24.501 §D.6.1) and pushed via the AMF; the wire
// envelope itself is deferred (see TODO TS 24.587 §5).
//
// Returns nil when the UE is not V2X-authorised — the spec says
// only authorised UEs receive policy.
func ProvisionPolicy(imsi string) map[string]interface{} {
	sub := LoadSubscription(imsi)
	body := BuildV2XPolicyParams(imsi, sub)
	if body == nil {
		return nil
	}
	plmns := loadAuthorizedPLMNs(imsi)
	qos := GetPC5QoSParams(imsi)
	freqs := LoadFrequencies()
	_, _ = engine.Exec(`INSERT INTO v2x_policy_log
		(imsi, ue_type, pc5_ambr_kbps, plmn_count, qos_count, freq_count)
		VALUES (?,?,?,?,?,?)`,
		imsi, sub.V2XUEType, sub.V2XPC5AMBRKbps,
		len(plmns), len(qos), len(freqs))
	return body
}

// PolicyLogEntry is one audit row for a TS 23.287 §5.1.2 V2X policy
// provisioning.
type PolicyLogEntry struct {
	ID           int64  `json:"id"`
	IMSI         string `json:"imsi"`
	UEType       string `json:"ue_type"`
	PC5AMBRKbps  int    `json:"pc5_ambr_kbps"`
	PLMNCount    int    `json:"plmn_count"`
	QoSCount     int    `json:"qos_count"`
	FreqCount    int    `json:"freq_count"`
	CreatedAt    string `json:"created_at"`
}

// ListPolicyLog returns up to `limit` recent policy-provisioning
// audit rows. If imsi != "", filters to that subscriber.
func ListPolicyLog(imsi string, limit int) ([]PolicyLogEntry, error) {
	if limit <= 0 || limit > 1000 {
		limit = 100
	}
	var (
		rows *sql.Rows
		err  error
	)
	if imsi == "" {
		rows, err = engine.Query(`SELECT id, imsi, COALESCE(ue_type,''),
			COALESCE(pc5_ambr_kbps,0), plmn_count, qos_count, freq_count,
			created_at FROM v2x_policy_log
			ORDER BY id DESC LIMIT ?`, limit)
	} else {
		rows, err = engine.Query(`SELECT id, imsi, COALESCE(ue_type,''),
			COALESCE(pc5_ambr_kbps,0), plmn_count, qos_count, freq_count,
			created_at FROM v2x_policy_log
			WHERE imsi=? ORDER BY id DESC LIMIT ?`, imsi, limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []PolicyLogEntry
	for rows.Next() {
		var e PolicyLogEntry
		if err := rows.Scan(&e.ID, &e.IMSI, &e.UEType, &e.PC5AMBRKbps,
			&e.PLMNCount, &e.QoSCount, &e.FreqCount, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// LoadFrequencies returns the NR PC5 frequencies advertised to UEs
// (TS 23.287 §5.1.2 — V2X frequencies are part of the V2X policy
// container so the UE knows which carriers to use for sidelink).
func LoadFrequencies() []int {
	val, err := GetConfig("nr_pc5_frequencies")
	if err != nil || val == "" {
		return nil
	}
	var freqs []int
	for _, f := range strings.Split(val, ",") {
		f = strings.TrimSpace(f)
		if n, err := strconv.Atoi(f); err == nil {
			freqs = append(freqs, n)
		}
	}
	return freqs
}
