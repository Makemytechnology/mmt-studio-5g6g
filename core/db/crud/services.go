// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/services.go — QoS services CRUD (TS 23.501 §5.7.4)
package crud

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// Service is the wire shape returned by services_list / services_get.
type Service struct {
	Name             string            `json:"name"`
	FiveQI           int               `json:"fiveqi"`
	ResourceType     string            `json:"resource_type"` // GBR | NonGBR
	ArpPriority      int               `json:"arp_priority"`
	ArpPcap          int               `json:"arp_pcap"`
	ArpPvuln         int               `json:"arp_pvuln"`
	GBRULKbps        *int              `json:"gbr_ul_kbps,omitempty"`
	GBRDLKbps        *int              `json:"gbr_dl_kbps,omitempty"`
	MBRULKbps        *int              `json:"mbr_ul_kbps,omitempty"`
	MBRDLKbps        *int              `json:"mbr_dl_kbps,omitempty"`
	FlowRules        []json.RawMessage `json:"flow_rules"`
	ChargingProfile  string            `json:"charging_profile,omitempty"`
	Status           string            `json:"status"` // ACTIVE | INACTIVE | …
}

var reSvcName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

// validStatuses matches TS 23.503 §6.1.3.4 PCC-rule status values.
var validStatuses = map[string]struct{}{
	"ACTIVE": {}, "INACTIVE": {}, "INACTIVE (event gated)": {},
	"PENDING": {}, "REMOVED": {},
}

func scanService(row interface{ Scan(...any) error }) (*Service, error) {
	var (
		name, rtype, status                 string
		fiveqi, apr, pcap, pvuln            int
		gUL, gDL, mUL, mDL                  sql.NullInt64
		flowJSON, cp                        sql.NullString
	)
	err := row.Scan(&name, &fiveqi, &rtype, &apr, &pcap, &pvuln,
		&gUL, &gDL, &mUL, &mDL, &flowJSON, &cp, &status)
	if err != nil {
		return nil, err
	}
	s := &Service{
		Name: name, FiveQI: fiveqi, ResourceType: rtype,
		ArpPriority: apr, ArpPcap: pcap, ArpPvuln: pvuln,
		Status: status, ChargingProfile: cp.String,
	}
	if gUL.Valid {
		v := int(gUL.Int64)
		s.GBRULKbps = &v
	}
	if gDL.Valid {
		v := int(gDL.Int64)
		s.GBRDLKbps = &v
	}
	if mUL.Valid {
		v := int(mUL.Int64)
		s.MBRULKbps = &v
	}
	if mDL.Valid {
		v := int(mDL.Int64)
		s.MBRDLKbps = &v
	}
	if flowJSON.Valid && flowJSON.String != "" {
		_ = json.Unmarshal([]byte(flowJSON.String), &s.FlowRules)
	}
	if s.FlowRules == nil {
		s.FlowRules = []json.RawMessage{}
	}
	if s.Status == "" {
		s.Status = "ACTIVE"
	}
	return s, nil
}

// ServicesList returns all services sorted by name.
func ServicesList() ([]Service, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
        SELECT name,fiveqi,resource_type,arp_priority,arp_pcap,arp_pvuln,
               gbr_ul_kbps,gbr_dl_kbps,mbr_ul_kbps,mbr_dl_kbps,flow_json,
               charging_profile,status
        FROM services ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Service
	for rows.Next() {
		s, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *s)
	}
	return out, rows.Err()
}

// ServicesGet returns a single service or nil if not found.
func ServicesGet(name string) (*Service, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`
        SELECT name,fiveqi,resource_type,arp_priority,arp_pcap,arp_pvuln,
               gbr_ul_kbps,gbr_dl_kbps,mbr_ul_kbps,mbr_dl_kbps,flow_json,
               charging_profile,status
        FROM services WHERE name=?`, name)
	s, err := scanService(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return s, err
}

// ServicesUpsert validates + inserts/updates a service.
// Returns the saved name, or an error describing the validation failure.
func ServicesUpsert(s Service) (string, error) {
	name := strings.TrimSpace(s.Name)
	if name == "" || !reSvcName.MatchString(name) {
		return "", errors.New("service name must be non-empty [A-Za-z0-9_]+")
	}
	if s.FiveQI < 1 || s.FiveQI > 255 {
		return "", errors.New("5QI must be 1..255")
	}
	rtype := strings.TrimSpace(s.ResourceType)
	if rtype == "" {
		rtype = "NonGBR"
	}
	if rtype != "GBR" && rtype != "NonGBR" {
		return "", errors.New("resource_type must be GBR or NonGBR")
	}
	status := strings.TrimSpace(s.Status)
	if status == "" {
		status = "ACTIVE"
	}
	if _, ok := validStatuses[status]; !ok {
		return "", fmt.Errorf("status must be one of ACTIVE/INACTIVE/INACTIVE (event gated)/PENDING/REMOVED")
	}

	flowJSON := "[]"
	if len(s.FlowRules) > 0 {
		b, err := json.Marshal(s.FlowRules)
		if err != nil {
			return "", errors.New("flow_rules must be JSON-serializable list")
		}
		flowJSON = string(b)
	}

	// GBR-only: drop GBR kbps when not GBR
	gbrUL, gbrDL := (*int)(nil), (*int)(nil)
	if rtype == "GBR" {
		gbrUL = s.GBRULKbps
		gbrDL = s.GBRDLKbps
	}

	cp := sql.NullString{String: s.ChargingProfile, Valid: s.ChargingProfile != ""}

	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	var exists int
	err = db.QueryRow(`SELECT 1 FROM services WHERE name=?`, name).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = db.Exec(`
            INSERT INTO services
              (name,fiveqi,resource_type,arp_priority,arp_pcap,arp_pvuln,
               gbr_ul_kbps,gbr_dl_kbps,mbr_ul_kbps,mbr_dl_kbps,flow_json,
               charging_profile,status)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			name, s.FiveQI, rtype, s.ArpPriority, s.ArpPcap, s.ArpPvuln,
			ptrOrNil(gbrUL), ptrOrNil(gbrDL),
			ptrOrNil(s.MBRULKbps), ptrOrNil(s.MBRDLKbps),
			flowJSON, cp, status,
		)
		if err != nil {
			return "", err
		}
		return name, nil
	}
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`
        UPDATE services
        SET fiveqi=?, resource_type=?, arp_priority=?, arp_pcap=?, arp_pvuln=?,
            gbr_ul_kbps=?, gbr_dl_kbps=?, mbr_ul_kbps=?, mbr_dl_kbps=?, flow_json=?,
            charging_profile=?, status=?
        WHERE name=?`,
		s.FiveQI, rtype, s.ArpPriority, s.ArpPcap, s.ArpPvuln,
		ptrOrNil(gbrUL), ptrOrNil(gbrDL),
		ptrOrNil(s.MBRULKbps), ptrOrNil(s.MBRDLKbps),
		flowJSON, cp, status, name,
	)
	return name, err
}

// ServicesDelete removes the named service. Returns rowsAffected.
func ServicesDelete(name string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM services WHERE name=?`, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ptrOrNil returns the pointed-to int as driver-safe any, or nil (for NULL).
func ptrOrNil(p *int) any {
	if p == nil {
		return nil
	}
	return *p
}
