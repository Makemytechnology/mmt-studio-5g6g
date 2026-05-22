// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package eas — Edge Application Server registry and discovery for the
// 5GC Edge Computing service (TS 23.548 §6.2 EAS Discovery and
// Re-discovery; TS 23.548 §6.8 mapping between EAS address Information
// and DNAI). The discovery procedure here implements the
// Distributed-Anchor Connectivity Model variant (TS 23.548 §6.2.2.2).
//
// Discovery scoring weights (DNAI +50, DNN +30, S-NSSAI +20, capacity
// +0..20, proximity +0..30) are operator policy — TS 23.548 §6.2 only
// mandates that discovery deliver the EAS "as close as possible to the
// UE" topologically; the specific ranking function is left to the AF /
// EES implementation. Don't read these constants as spec-derived.
//
// EAS lifecycle (Create/Update/Delete) is anchored to the TS 23.558
// EDGEAPP architecture: the EES holds EAS registration state via the
// Eees_EASRegistration_* APIs (TS 23.558 §8.4.3.4). Our local row is
// the persisted view of that state.
package eas

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ---- Row types ----

// EAS represents a row in the eas_registry table.
type EAS struct {
	ID                int64    `json:"id"`
	AppID             string   `json:"app_id"`
	Name              *string  `json:"name,omitempty"`
	EndpointURL       string   `json:"endpoint_url"`
	DNAI              *string  `json:"dnai,omitempty"`
	Latitude          *float64 `json:"latitude,omitempty"`
	Longitude         *float64 `json:"longitude,omitempty"`
	SupportedDNNs     *string  `json:"supported_dnns,omitempty"`
	SupportedSlices   *string  `json:"supported_slices,omitempty"`
	Capacity          int      `json:"capacity"`
	ActiveConnections int      `json:"active_connections"`
	Status            string   `json:"status"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`

	// Computed fields (discovery only)
	DistanceKM *float64 `json:"distance_km,omitempty"`
	Score      *float64 `json:"score,omitempty"`
}

// DNAIMapping represents a row in the eas_dnai_map table.
type DNAIMapping struct {
	ID           int64   `json:"id"`
	DNAI         string  `json:"dnai"`
	Description  *string `json:"description,omitempty"`
	LocationHint *string `json:"location_hint,omitempty"`
	UPFInstance  *string `json:"upf_instance,omitempty"`
}

// DiscoveryLog represents a row in the eas_discovery_log table.
type DiscoveryLog struct {
	ID            int64   `json:"id"`
	IMSI          *string `json:"imsi,omitempty"`
	AppID         *string `json:"app_id,omitempty"`
	CriteriaJSON  *string `json:"criteria_json,omitempty"`
	ResultsCount  int     `json:"results_count"`
	SelectedEASID *int64  `json:"selected_eas_id,omitempty"`
	CreatedAt     string  `json:"created_at"`
}

// DiscoveryCriteria holds the inputs to Discover().
type DiscoveryCriteria struct {
	IMSI        string   `json:"imsi"`
	AppID       string   `json:"app_id"`
	DNN         string   `json:"dnn,omitempty"`
	SST         *int     `json:"sst,omitempty"`
	DNAI        string   `json:"dnai,omitempty"`
	UELatitude  *float64 `json:"ue_latitude,omitempty"`
	UELongitude *float64 `json:"ue_longitude,omitempty"`
}

// ---- EAS Registry CRUD ----

// List returns all registered EAS instances (preserves GUI panel API).
func List() ([]EAS, error) { return ListEAS() }

// Status returns a summary for the GUI panel.
func Status() map[string]any {
	list, _ := ListEAS()
	return map[string]any{"count": len(list), "items": list}
}

// ListEAS returns all registered EAS instances.
func ListEAS() ([]EAS, error) {
	rows, err := engine.Query(`SELECT id, app_id, name, endpoint_url, dnai,
		latitude, longitude, supported_dnns, supported_slices,
		capacity, active_connections, status, created_at, updated_at
		FROM eas_registry ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanEASRows(rows)
}

// GetEAS returns a single EAS by ID.
func GetEAS(id int64) (*EAS, error) {
	row := engine.QueryRow(`SELECT id, app_id, name, endpoint_url, dnai,
		latitude, longitude, supported_dnns, supported_slices,
		capacity, active_connections, status, created_at, updated_at
		FROM eas_registry WHERE id=?`, id)
	e, err := scanEASRow(row)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return e, err
}

// CreateEAS registers a new EAS instance — local persistence of the
// "EAS registration request" defined in TS 23.558 §8.4.3.2.2 +
// §8.4.3.3.2 (information flow). Wire-format Eees_EASRegistration_
// Request operation lives in TS 23.558 §8.4.3.4.2.
// Returns the new row ID.
func CreateEAS(appID, endpointURL string, name, dnai *string,
	lat, lon *float64, supportedDNNs, supportedSlices *string,
	capacity int, status string) (int64, error) {

	if strings.TrimSpace(appID) == "" {
		return 0, fmt.Errorf("app_id is required")
	}
	if strings.TrimSpace(endpointURL) == "" {
		return 0, fmt.Errorf("endpoint_url is required")
	}
	if status == "" {
		status = "active"
	}

	res, err := engine.Exec(`INSERT INTO eas_registry
		(app_id, name, endpoint_url, dnai, latitude, longitude,
		 supported_dnns, supported_slices, capacity, status)
		VALUES (?,?,?,?,?,?,?,?,?,?)`,
		appID, name, endpointURL, dnai, lat, lon,
		supportedDNNs, supportedSlices, capacity, status)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateEAS updates mutable fields on an EAS row — local persistence
// of the "EAS registration update request" defined in TS 23.558
// §8.4.3.2.3 + §8.4.3.3.4. Wire-format Eees_EASRegistration_Update
// operation lives in TS 23.558 §8.4.3.4.3.
func UpdateEAS(id int64, fields map[string]interface{}) error {
	if len(fields) == 0 {
		return nil
	}
	allowed := map[string]bool{
		"name": true, "endpoint_url": true, "dnai": true,
		"latitude": true, "longitude": true,
		"supported_dnns": true, "supported_slices": true,
		"capacity": true, "active_connections": true, "status": true,
	}
	var setClauses []string
	var args []interface{}
	for k, v := range fields {
		if !allowed[k] {
			continue
		}
		setClauses = append(setClauses, k+"=?")
		args = append(args, v)
	}
	if len(setClauses) == 0 {
		return nil
	}
	setClauses = append(setClauses, "updated_at=datetime('now')")
	args = append(args, id)
	q := fmt.Sprintf("UPDATE eas_registry SET %s WHERE id=?",
		strings.Join(setClauses, ", "))
	_, err := engine.Exec(q, args...)
	return err
}

// DeleteEAS removes an EAS from the registry — local persistence of
// the "EAS de-registration request" defined in TS 23.558 §8.4.3.2.4
// + §8.4.3.3.6. Wire-format Eees_EASRegistration_Deregister
// operation lives in TS 23.558 §8.4.3.4.4.
func DeleteEAS(id int64) error {
	_, err := engine.Exec(`DELETE FROM eas_registry WHERE id=?`, id)
	return err
}

// ---- DNAI Map CRUD ----
//
// TS 23.548 §6.8 — Support for mapping between EAS address Information
// and DNAI. Each EAS endpoint is anchored to a DNAI, which the SMF
// uses to select the local PSA-UPF and steer the user-plane to the
// edge site. UPF-instance hint (UPFInstance) is operator-local and
// out-of-spec.

// ListDNAI returns all DNAI mappings.
func ListDNAI() ([]DNAIMapping, error) {
	rows, err := engine.Query(`SELECT id, dnai, description, location_hint, upf_instance
		FROM eas_dnai_map ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DNAIMapping
	for rows.Next() {
		var d DNAIMapping
		if err := rows.Scan(&d.ID, &d.DNAI, &d.Description,
			&d.LocationHint, &d.UPFInstance); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// CreateDNAI inserts a new DNAI mapping.
func CreateDNAI(dnai string, description, locationHint, upfInstance *string) (int64, error) {
	res, err := engine.Exec(`INSERT INTO eas_dnai_map
		(dnai, description, location_hint, upf_instance)
		VALUES (?,?,?,?)`, dnai, description, locationHint, upfInstance)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// DeleteDNAI removes a DNAI mapping by ID.
func DeleteDNAI(id int64) error {
	_, err := engine.Exec(`DELETE FROM eas_dnai_map WHERE id=?`, id)
	return err
}

// ---- Discovery ----

// Discover finds and ranks EAS instances matching the given criteria.
// TS 23.548 §6.2.2.2 — EAS Discovery Procedure (Distributed Anchor
// model). The spec mandates returning an EAS topologically close to
// the UE; the ranking function (scoreEAS) implements that selection.
//
// For the Session-Breakout connectivity model (TS 23.548 §6.2.3.2.2,
// EAS Discovery Procedure with EASDF), use DiscoverViaEASDF below —
// it skips the IP-locality scoring path that's specific to the
// Distributed-Anchor model.
func Discover(c DiscoveryCriteria) ([]EAS, error) {
	if c.AppID == "" {
		return nil, nil
	}

	rows, err := engine.Query(`SELECT id, app_id, name, endpoint_url, dnai,
		latitude, longitude, supported_dnns, supported_slices,
		capacity, active_connections, status, created_at, updated_at
		FROM eas_registry WHERE app_id=? AND status='active'`, c.AppID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	candidates, err := scanEASRows(rows)
	if err != nil {
		return nil, err
	}

	// Proximity + scoring
	for i := range candidates {
		dist := computeDistance(&candidates[i], c.UELatitude, c.UELongitude)
		candidates[i].DistanceKM = dist
		s := scoreEAS(&candidates[i], c)
		candidates[i].Score = &s
	}

	// Sort by score descending
	for i := 1; i < len(candidates); i++ {
		for j := i; j > 0 && deref(candidates[j].Score) > deref(candidates[j-1].Score); j-- {
			candidates[j], candidates[j-1] = candidates[j-1], candidates[j]
		}
	}

	// Log discovery
	var selectedID *int64
	if len(candidates) > 0 {
		selectedID = &candidates[0].ID
	}
	criteriaJSON, _ := json.Marshal(c)
	logDiscovery(pstr(c.IMSI), pstr(c.AppID), pstr(string(criteriaJSON)),
		len(candidates), selectedID)

	return candidates, nil
}

// EASDFAnswer is what the EASDF returns to a DNS Query for an EAS
// FQDN under the Session-Breakout connectivity model. The SMF uses
// the result to insert a ULCL/BP/Local-PSA towards the selected EAS
// (TS 23.548 §6.2.3.2.2 step where DNS Response triggers UPF insertion).
type EASDFAnswer struct {
	FQDN     string `json:"fqdn"`
	EAS      EAS    `json:"eas"`
	DNAI     string `json:"dnai,omitempty"`     // mapped DNAI for ULCL/BP placement (TS 23.548 §6.8)
	UEIPHint string `json:"ue_ip_hint,omitempty"` // EDNS Client Subnet hint (RFC 7871) carried back to caller
}

// DiscoverViaEASDF resolves an EAS FQDN under the Session-Breakout
// Connectivity Model — TS 23.548 §6.2.3.2.2 ("EAS Discovery Procedure
// with EASDF"). The caller (an SMF or EASDF stub) presents:
//
//   - the FQDN extracted from the UE's DNS Query,
//   - optionally an AppID hint (when the AF has provided EAS Deployment
//     Information per TS 23.548 §6.2.3.4 to narrow the candidate set).
//
// The function picks one active EAS by FQDN match (or by AppID match
// if FQDN didn't resolve), favouring the candidate whose DNAI matches
// the criteria and whose capacity term is highest. The returned
// EASDFAnswer carries the DNAI the SMF will use for L-PSA / ULCL
// placement (TS 23.548 §6.8 EAS↔DNAI mapping).
//
// FQDN-to-EAS lookup matches against the endpoint_url; the spec
// presumes the EAS Profile carries an FQDN field (TS 23.558 §8.2.4),
// but our row schema doesn't expose it separately yet — see TODO.
//
// TODO TS 23.558 §8.2.4 — split a dedicated FQDN field out of
// endpoint_url so EASDF lookup is exact rather than substring.
// TODO TS 23.548 §6.2.3.4 — wire to AF-provided EAS Deployment
// Information stored in UDR (Nudr_DataRepository); today the candidate
// set is the local registry only.
func DiscoverViaEASDF(fqdn string, c DiscoveryCriteria) (*EASDFAnswer, error) {
	if strings.TrimSpace(fqdn) == "" {
		return nil, fmt.Errorf("fqdn is required")
	}

	rows, err := engine.Query(`SELECT id, app_id, name, endpoint_url, dnai,
		latitude, longitude, supported_dnns, supported_slices,
		capacity, active_connections, status, created_at, updated_at
		FROM eas_registry WHERE status='active'`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	all, err := scanEASRows(rows)
	if err != nil {
		return nil, err
	}

	fqdnLower := strings.ToLower(fqdn)
	var matched []EAS
	for _, e := range all {
		if strings.Contains(strings.ToLower(e.EndpointURL), fqdnLower) {
			matched = append(matched, e)
		}
	}
	// Fallback: if no FQDN substring match, use AppID criterion when
	// the caller supplied one (matches the AF-narrowed candidate set
	// TS 23.548 §6.2.3.4 implies but doesn't formally specify here).
	if len(matched) == 0 && c.AppID != "" {
		for _, e := range all {
			if e.AppID == c.AppID {
				matched = append(matched, e)
			}
		}
	}
	if len(matched) == 0 {
		return nil, nil // EASDF returns NXDOMAIN-equivalent
	}

	// Score against criteria (DNAI / DNN / S-NSSAI / capacity), but
	// drop the proximity term since Session-Breakout doesn't depend
	// on UE topological location for L-PSA selection — that's the
	// SMF's job using the DNAI map (§6.8).
	for i := range matched {
		c2 := c
		c2.UELatitude, c2.UELongitude = nil, nil
		matched[i].DistanceKM = nil
		s := scoreEAS(&matched[i], c2)
		matched[i].Score = &s
	}
	for i := 1; i < len(matched); i++ {
		for j := i; j > 0 && deref(matched[j].Score) > deref(matched[j-1].Score); j-- {
			matched[j], matched[j-1] = matched[j-1], matched[j]
		}
	}

	pick := matched[0]
	ans := &EASDFAnswer{
		FQDN: fqdn,
		EAS:  pick,
	}
	if pick.DNAI != nil {
		ans.DNAI = *pick.DNAI
	}

	criteriaJSON, _ := json.Marshal(c)
	logDiscovery(pstr(c.IMSI), pstr(c.AppID), pstr(string(criteriaJSON)),
		len(matched), &pick.ID)
	return ans, nil
}

// MapEASToDNAI returns the DNAI assigned to a given EAS instance —
// the explicit lookup behind TS 23.548 §6.8 (mapping between EAS
// address Information and DNAI). Returns "" (no mapping) when the
// EAS doesn't carry a DNAI in its registration row.
//
// TODO TS 23.548 §6.8.2: bidirectional N6/N9 routing translation
// (DNAI → UPF instance + N6 routing-info) is delegated to the
// SMF/SMSF stack — this helper just surfaces the EAS-side half.
func MapEASToDNAI(easID int64) (string, error) {
	row := engine.QueryRow(`SELECT dnai FROM eas_registry WHERE id=?`, easID)
	var dnai sql.NullString
	err := row.Scan(&dnai)
	if err == sql.ErrNoRows {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if !dnai.Valid {
		return "", nil
	}
	return dnai.String, nil
}

// ResolveDNAIForFQDN combines the FQDN→EAS resolution path with the
// §6.8 EAS→DNAI map — i.e. given a UE DNS Query, what L-PSA / ULCL
// DNAI should the SMF target? Returns "" when no EAS matches or
// when the matching EAS has no DNAI assigned.
//
// Convenience wrapper for callers that just want the DNAI string;
// for the full EASDFAnswer (with the chosen EAS row attached), call
// DiscoverViaEASDF directly.
func ResolveDNAIForFQDN(fqdn string, c DiscoveryCriteria) (string, error) {
	ans, err := DiscoverViaEASDF(fqdn, c)
	if err != nil || ans == nil {
		return "", err
	}
	return ans.DNAI, nil
}

// ListDiscoveryLog returns recent discovery log entries.
func ListDiscoveryLog(limit int) ([]DiscoveryLog, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := engine.Query(`SELECT id, imsi, app_id, criteria_json,
		results_count, selected_eas_id, created_at
		FROM eas_discovery_log ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []DiscoveryLog
	for rows.Next() {
		var d DiscoveryLog
		if err := rows.Scan(&d.ID, &d.IMSI, &d.AppID, &d.CriteriaJSON,
			&d.ResultsCount, &d.SelectedEASID, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// ---- Internal helpers ----

func logDiscovery(imsi, appID, criteriaJSON *string, count int, selectedID *int64) {
	_, _ = engine.Exec(`INSERT INTO eas_discovery_log
		(imsi, app_id, criteria_json, results_count, selected_eas_id)
		VALUES (?,?,?,?,?)`, imsi, appID, criteriaJSON, count, selectedID)
}

func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dLon/2)*math.Sin(dLon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func computeDistance(e *EAS, ueLat, ueLon *float64) *float64 {
	if ueLat == nil || ueLon == nil || e.Latitude == nil || e.Longitude == nil {
		return nil
	}
	d := math.Round(haversineKM(*ueLat, *ueLon, *e.Latitude, *e.Longitude)*100) / 100
	return &d
}

func scoreEAS(e *EAS, c DiscoveryCriteria) float64 {
	score := 0.0

	// DNAI match +50
	if c.DNAI != "" && e.DNAI != nil && *e.DNAI == c.DNAI {
		score += 50
	}
	// DNN match +30
	if c.DNN != "" && e.SupportedDNNs != nil {
		var dnns []string
		if json.Unmarshal([]byte(*e.SupportedDNNs), &dnns) == nil {
			for _, d := range dnns {
				if d == c.DNN {
					score += 30
					break
				}
			}
		}
	}
	// SST match +20
	if c.SST != nil && e.SupportedSlices != nil {
		var slices []interface{}
		if json.Unmarshal([]byte(*e.SupportedSlices), &slices) == nil {
			for _, s := range slices {
				switch v := s.(type) {
				case float64:
					if int(v) == *c.SST {
						score += 20
					}
				case map[string]interface{}:
					if sst, ok := v["sst"].(float64); ok && int(sst) == *c.SST {
						score += 20
					}
				}
			}
		}
	}
	// Capacity score +0..20
	if e.Capacity > 0 {
		avail := float64(e.Capacity-e.ActiveConnections) / float64(e.Capacity)
		if avail < 0 {
			avail = 0
		}
		score += avail * 20
	}
	// Proximity score +0..30
	if e.DistanceKM != nil && *e.DistanceKM < 99999 {
		score += 30 * math.Exp(-*e.DistanceKM/30)
	}

	return math.Round(score*100) / 100
}

func scanEASRows(rows *sql.Rows) ([]EAS, error) {
	var out []EAS
	for rows.Next() {
		var e EAS
		err := rows.Scan(&e.ID, &e.AppID, &e.Name, &e.EndpointURL, &e.DNAI,
			&e.Latitude, &e.Longitude, &e.SupportedDNNs, &e.SupportedSlices,
			&e.Capacity, &e.ActiveConnections, &e.Status, &e.CreatedAt, &e.UpdatedAt)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func scanEASRow(row *sql.Row) (*EAS, error) {
	var e EAS
	err := row.Scan(&e.ID, &e.AppID, &e.Name, &e.EndpointURL, &e.DNAI,
		&e.Latitude, &e.Longitude, &e.SupportedDNNs, &e.SupportedSlices,
		&e.Capacity, &e.ActiveConnections, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	return &e, err
}

func pstr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}
