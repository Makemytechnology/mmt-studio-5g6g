// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package nsaas -- Network Slice as a Service (TS 28.531).
//
// Go port of services/nsaas/*.py.  Tenant/template/slice CRUD for
// nsaas_tenants, nsaas_templates, nsaas_slices, and nsaas_sla tables.
//
// Slice lifecycle (TS 28.531 Section 8.1):
//
//	preparing -> provisioned -> active -> decommissioning -> decommissioned
//	                            active -> modifying -> active
package nsaas

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
)

// Valid state transitions per TS 28.531 Section 8.1.
var transitions = map[string]map[string]bool{
	"preparing":       {"provisioned": true},
	"provisioned":     {"active": true, "decommissioning": true},
	"active":          {"modifying": true, "decommissioning": true},
	"modifying":       {"active": true, "decommissioning": true},
	"decommissioning": {"decommissioned": true},
	"decommissioned":  {},
}

// ---- Types ----

type Tenant struct {
	ID           int64   `json:"id"`
	Name         string  `json:"name"`
	ContactEmail *string `json:"contact_email,omitempty"`
	APIKey       *string `json:"api_key,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

type Template struct {
	ID          int64   `json:"id"`
	Name        string  `json:"name"`
	Description *string `json:"description,omitempty"`
	SST         int     `json:"sst"`
	SD          *string `json:"sd,omitempty"`
	DefaultDNN  *string `json:"default_dnn,omitempty"`
	QoSProfile  *string `json:"qos_profile,omitempty"`
	SLADefaults *string `json:"sla_defaults,omitempty"`
	CreatedAt   string  `json:"created_at"`
}

type Slice struct {
	ID                int64   `json:"id"`
	TenantID          int64   `json:"tenant_id"`
	TemplateID        int64   `json:"template_id"`
	Name              *string `json:"name,omitempty"`
	SST               int     `json:"sst"`
	SD                *string `json:"sd,omitempty"`
	Status            string  `json:"status"`
	ConfigJSON        *string `json:"config_json,omitempty"`
	NSSAICatalogID    *int64  `json:"nssai_catalog_id,omitempty"`
	CreatedAt         string  `json:"created_at"`
	ActivatedAt       *string `json:"activated_at,omitempty"`
	DecommissionedAt  *string `json:"decommissioned_at,omitempty"`
}

// ---- GUI panel API ----

func List() ([]Tenant, error) { return ListTenants() }

func Status() map[string]any {
	tenants, _ := ListTenants()
	slices, _ := ListSlices()
	return map[string]any{"tenants": len(tenants), "slices": len(slices)}
}

// ---- Tenant CRUD ----

func ListTenants() ([]Tenant, error) {
	rows, err := engine.Query(`SELECT id, name, contact_email, api_key, created_at
		FROM nsaas_tenants ORDER BY id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Tenant
	for rows.Next() {
		var t Tenant
		if err := rows.Scan(&t.ID, &t.Name, &t.ContactEmail, &t.APIKey, &t.CreatedAt); err != nil { return nil, err }
		out = append(out, t)
	}
	return out, rows.Err()
}

func GetTenant(id int64) (*Tenant, error) {
	row := engine.QueryRow(`SELECT id, name, contact_email, api_key, created_at
		FROM nsaas_tenants WHERE id=?`, id)
	var t Tenant
	err := row.Scan(&t.ID, &t.Name, &t.ContactEmail, &t.APIKey, &t.CreatedAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &t, err
}

func CreateTenant(name, contactEmail, apiKey string) (int64, error) {
	res, err := engine.Exec(`INSERT INTO nsaas_tenants (name, contact_email, api_key)
		VALUES (?,?,?)`, name, nilStr(contactEmail), nilStr(apiKey))
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func DeleteTenant(id int64) error {
	_, err := engine.Exec(`DELETE FROM nsaas_tenants WHERE id=?`, id)
	return err
}

// ---- Template CRUD ----

func ListTemplates() ([]Template, error) {
	rows, err := engine.Query(`SELECT id, name, description, sst, sd,
		default_dnn, qos_profile, sla_defaults, created_at
		FROM nsaas_templates ORDER BY id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Template
	for rows.Next() {
		var t Template
		if err := rows.Scan(&t.ID, &t.Name, &t.Description, &t.SST, &t.SD,
			&t.DefaultDNN, &t.QoSProfile, &t.SLADefaults, &t.CreatedAt); err != nil { return nil, err }
		out = append(out, t)
	}
	return out, rows.Err()
}

func GetTemplate(id int64) (*Template, error) {
	row := engine.QueryRow(`SELECT id, name, description, sst, sd,
		default_dnn, qos_profile, sla_defaults, created_at
		FROM nsaas_templates WHERE id=?`, id)
	var t Template
	err := row.Scan(&t.ID, &t.Name, &t.Description, &t.SST, &t.SD,
		&t.DefaultDNN, &t.QoSProfile, &t.SLADefaults, &t.CreatedAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &t, err
}

func CreateTemplate(name string, sst int, sd, description, defaultDNN, qosProfile, slaDefaults string) (int64, error) {
	res, err := engine.Exec(`INSERT INTO nsaas_templates
		(name, sst, sd, description, default_dnn, qos_profile, sla_defaults)
		VALUES (?,?,?,?,?,?,?)`, name, sst, nilStr(sd), nilStr(description),
		nilStr(defaultDNN), nilStr(qosProfile), nilStr(slaDefaults))
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func DeleteTemplate(id int64) error {
	_, err := engine.Exec(`DELETE FROM nsaas_templates WHERE id=?`, id)
	return err
}

// ---- Slice Lifecycle ----

func ListSlices() ([]Slice, error) {
	rows, err := engine.Query(`SELECT id, tenant_id, template_id, name, sst, sd,
		status, config_json, nssai_catalog_id, created_at, activated_at, decommissioned_at
		FROM nsaas_slices ORDER BY id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Slice
	for rows.Next() {
		s, err := scanSliceRow(rows)
		if err != nil { return nil, err }
		out = append(out, *s)
	}
	return out, rows.Err()
}

func GetSlice(id int64) (*Slice, error) {
	row := engine.QueryRow(`SELECT id, tenant_id, template_id, name, sst, sd,
		status, config_json, nssai_catalog_id, created_at, activated_at, decommissioned_at
		FROM nsaas_slices WHERE id=?`, id)
	var s Slice
	err := row.Scan(&s.ID, &s.TenantID, &s.TemplateID, &s.Name, &s.SST, &s.SD,
		&s.Status, &s.ConfigJSON, &s.NSSAICatalogID, &s.CreatedAt,
		&s.ActivatedAt, &s.DecommissionedAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &s, err
}

// ProvisionSlice creates a new slice from a template (TS 28.531 Section 8.2).
func ProvisionSlice(templateID, tenantID int64, config map[string]interface{}) (int64, error) {
	tenant, err := GetTenant(tenantID)
	if err != nil { return 0, err }
	if tenant == nil { return 0, fmt.Errorf("tenant %d not found", tenantID) }

	tpl, err := GetTemplate(templateID)
	if err != nil { return 0, err }
	if tpl == nil { return 0, fmt.Errorf("template %d not found", templateID) }

	sst := tpl.SST
	var sd *string
	sd = tpl.SD
	name := tenant.Name + "-" + tpl.Name
	if config != nil {
		if v, ok := config["sst"].(float64); ok { sst = int(v) }
		if v, ok := config["sd"].(string); ok { sd = &v }
		if v, ok := config["name"].(string); ok { name = v }
	}

	var cfgJSON *string
	if config != nil {
		b, _ := json.Marshal(config)
		s := string(b)
		cfgJSON = &s
	}

	// Resolve the catalogue FK so this slice joins the same identity
	// space as PLMN advertisements / UPF anchors / UE subscriptions.
	// Get-or-Create is idempotent: an existing (sst, sd) row is reused.
	sdStr := ""
	if sd != nil { sdStr = *sd }
	catalogID, err := crud.NSSAICatalogGetOrCreate(sst, sdStr)
	if err != nil { return 0, fmt.Errorf("nssai_catalog upsert: %w", err) }

	res, err := engine.Exec(`INSERT INTO nsaas_slices
		(tenant_id, template_id, name, sst, sd, status, config_json, nssai_catalog_id)
		VALUES (?,?,?,?,?,'preparing',?,?)`,
		tenantID, templateID, name, sst, sd, cfgJSON, catalogID)
	if err != nil { return 0, err }
	sliceID, _ := res.LastInsertId()

	// Transition to provisioned
	if err := changeState(sliceID, "provisioned"); err != nil { return sliceID, err }
	return sliceID, nil
}

// ActivateSlice transitions provisioned -> active (TS 28.531 Section 8.1).
func ActivateSlice(id int64) error {
	now := nowISO()
	return changeStateExtra(id, "active", map[string]interface{}{"activated_at": now})
}

// ModifySlice applies changes to an active slice.
func ModifySlice(id int64, changes map[string]interface{}) error {
	if err := changeState(id, "modifying"); err != nil { return err }
	// Apply field changes
	for _, k := range []string{"name", "sst", "sd", "config_json"} {
		if v, ok := changes[k]; ok {
			_, _ = engine.Exec(fmt.Sprintf("UPDATE nsaas_slices SET %s=? WHERE id=?", k), v, id)
		}
	}
	return changeState(id, "active")
}

// DecommissionSlice takes a slice out of service.
func DecommissionSlice(id int64) error {
	s, err := GetSlice(id)
	if err != nil { return err }
	if s == nil { return fmt.Errorf("slice %d not found", id) }
	if s.Status == "modifying" {
		_, _ = engine.Exec(`UPDATE nsaas_slices SET status='active' WHERE id=?`, id)
	}
	if err := changeState(id, "decommissioning"); err != nil { return err }
	now := nowISO()
	return changeStateExtra(id, "decommissioned", map[string]interface{}{"decommissioned_at": now})
}

func DeleteSlice(id int64) error {
	_, err := engine.Exec(`DELETE FROM nsaas_slices WHERE id=?`, id)
	return err
}

// ---- helpers ----

func changeState(id int64, newState string) error {
	return changeStateExtra(id, newState, nil)
}

func changeStateExtra(id int64, newState string, extra map[string]interface{}) error {
	s, err := GetSlice(id)
	if err != nil { return err }
	if s == nil { return fmt.Errorf("slice %d not found", id) }
	allowed := transitions[s.Status]
	if !allowed[newState] {
		return fmt.Errorf("invalid transition: %s -> %s", s.Status, newState)
	}
	q := "UPDATE nsaas_slices SET status=?"
	args := []interface{}{newState}
	for k, v := range extra {
		q += fmt.Sprintf(", %s=?", k)
		args = append(args, v)
	}
	q += " WHERE id=?"
	args = append(args, id)
	_, err = engine.Exec(q, args...)
	return err
}

func scanSliceRow(rows *sql.Rows) (*Slice, error) {
	var s Slice
	err := rows.Scan(&s.ID, &s.TenantID, &s.TemplateID, &s.Name, &s.SST, &s.SD,
		&s.Status, &s.ConfigJSON, &s.NSSAICatalogID, &s.CreatedAt,
		&s.ActivatedAt, &s.DecommissionedAt)
	return &s, err
}

// ---- SLA Management (TS 28.531 Section 8.3 / TS 28.541 Section 6.3.1) ----

type SLAMetric struct {
	ID           int64    `json:"id"`
	SliceID      int64    `json:"slice_id"`
	Metric       string   `json:"metric"`
	TargetValue  float64  `json:"target_value"`
	CurrentValue *float64 `json:"current_value,omitempty"`
	Compliant    int      `json:"compliant"`
	CheckedAt    *string  `json:"checked_at,omitempty"`
}

func ListSLA(sliceID int64) ([]SLAMetric, error) {
	rows, err := engine.Query(`SELECT id, slice_id, metric, target_value,
		current_value, compliant, checked_at
		FROM nsaas_sla WHERE slice_id=? ORDER BY metric`, sliceID)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []SLAMetric
	for rows.Next() {
		var m SLAMetric
		if err := rows.Scan(&m.ID, &m.SliceID, &m.Metric, &m.TargetValue,
			&m.CurrentValue, &m.Compliant, &m.CheckedAt); err != nil { return nil, err }
		out = append(out, m)
	}
	return out, rows.Err()
}

// DefineSLA defines or updates SLA targets for a slice.
func DefineSLA(sliceID int64, metrics map[string]float64) error {
	s, err := GetSlice(sliceID)
	if err != nil { return err }
	if s == nil { return fmt.Errorf("slice %d not found", sliceID) }
	for metric, target := range metrics {
		_, err := engine.Exec(`INSERT INTO nsaas_sla (slice_id, metric, target_value, compliant)
			VALUES (?,?,?,1)
			ON CONFLICT(slice_id, metric) DO UPDATE SET target_value=excluded.target_value,
			checked_at=datetime('now')`, sliceID, metric, target)
		if err != nil { return err }
	}
	return nil
}

// CheckSLA checks SLA compliance for a slice.
func CheckSLA(sliceID int64) (map[string]interface{}, error) {
	s, err := GetSlice(sliceID)
	if err != nil { return nil, err }
	if s == nil { return nil, fmt.Errorf("slice %d not found", sliceID) }

	slaRows, err := ListSLA(sliceID)
	if err != nil { return nil, err }
	if len(slaRows) == 0 {
		return map[string]interface{}{"slice_id": sliceID, "status": "no_sla", "metrics": []interface{}{}}, nil
	}

	hasViolation := false
	var metrics []map[string]interface{}
	for _, row := range slaRows {
		if row.Compliant == 0 { hasViolation = true }
		metrics = append(metrics, map[string]interface{}{
			"metric": row.Metric, "target_value": row.TargetValue,
			"current_value": row.CurrentValue, "compliant": row.Compliant != 0,
			"checked_at": row.CheckedAt,
		})
	}
	status := "compliant"
	if hasViolation { status = "violation" }
	return map[string]interface{}{"slice_id": sliceID, "status": status, "metrics": metrics}, nil
}

// UpdateSLAMetric updates the current measured value and rechecks compliance.
func UpdateSLAMetric(sliceID int64, metric string, currentValue float64) error {
	slaRows, err := ListSLA(sliceID)
	if err != nil { return err }
	var target float64
	found := false
	for _, row := range slaRows {
		if row.Metric == metric { target = row.TargetValue; found = true; break }
	}
	if !found { return fmt.Errorf("no SLA target for slice=%d metric=%s", sliceID, metric) }

	// Compliance: latency_ms / max_ues => lower is better; others => higher is better
	var compliant int
	if metric == "latency_ms" || metric == "max_ues" {
		if currentValue <= target { compliant = 1 }
	} else {
		if currentValue >= target { compliant = 1 }
	}
	_, err = engine.Exec(`UPDATE nsaas_sla SET current_value=?, compliant=?, checked_at=datetime('now')
		WHERE slice_id=? AND metric=?`, currentValue, compliant, sliceID, metric)
	return err
}

// DefineSLAFromTemplate applies SLA defaults from a template.
func DefineSLAFromTemplate(sliceID, templateID int64) error {
	tpl, err := GetTemplate(templateID)
	if err != nil { return err }
	if tpl == nil { return fmt.Errorf("template %d not found", templateID) }
	if tpl.SLADefaults == nil || *tpl.SLADefaults == "" { return nil }

	var defaults map[string]float64
	if err := json.Unmarshal([]byte(*tpl.SLADefaults), &defaults); err != nil { return err }
	return DefineSLA(sliceID, defaults)
}

// ---- Standard Template Seeding (TS 23.501 Section 5.15.2) ----

type stdTemplate struct {
	name, desc, sd, dnn string
	sst                 int
}

var standardTemplates = []stdTemplate{
	{"eMBB", "Enhanced Mobile Broadband -- high throughput, moderate latency", "000001", "internet", 1},
	{"URLLC", "Ultra-Reliable Low Latency -- critical communications", "000002", "internet", 2},
	{"mMTC", "Massive Machine Type Communications -- IoT / sensor networks", "000003", "iot", 3},
	{"Enterprise", "Enterprise dedicated slice -- eMBB with isolation", "000010", "enterprise", 1},
	{"V2X", "Vehicle-to-Everything -- low latency, high reliability (TS 23.287)", "000004", "v2x", 4},
}

// SeedStandardTemplates creates standard slice templates if absent.
func SeedStandardTemplates() int {
	existing, _ := ListTemplates()
	names := map[string]bool{}
	for _, t := range existing { names[t.Name] = true }
	created := 0
	for _, tpl := range standardTemplates {
		if names[tpl.name] { continue }
		if _, err := CreateTemplate(tpl.name, tpl.sst, tpl.sd, tpl.desc, tpl.dnn, "", ""); err == nil {
			created++
		}
	}
	return created
}

// GetStats returns aggregate NSaaS statistics.
func GetStats() map[string]interface{} {
	var tenants, templates, slices, active, decomm int
	row := engine.QueryRow(`SELECT COUNT(*) FROM nsaas_tenants`); row.Scan(&tenants)
	row = engine.QueryRow(`SELECT COUNT(*) FROM nsaas_templates`); row.Scan(&templates)
	row = engine.QueryRow(`SELECT COUNT(*) FROM nsaas_slices`); row.Scan(&slices)
	row = engine.QueryRow(`SELECT COUNT(*) FROM nsaas_slices WHERE status='active'`); row.Scan(&active)
	row = engine.QueryRow(`SELECT COUNT(*) FROM nsaas_slices WHERE status='decommissioned'`); row.Scan(&decomm)
	return map[string]interface{}{
		"tenants": tenants, "templates": templates, "slices": slices,
		"active_slices": active, "decommissioned_slices": decomm,
	}
}

// ---- helpers ----

func nowISO() string { return time.Now().UTC().Format("2006-01-02T15:04:05Z") }

func nilStr(s string) interface{} {
	if s == "" { return nil }
	return s
}
