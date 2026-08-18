// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/bindings.go — Service-binding CRUD (TS 23.501 §5.7.2)
package crud

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// Binding is the response shape of bindings_list.
type Binding struct {
	IMSI        string `json:"imsi"`
	DNN         string `json:"dnn"`
	SST         string `json:"sst"` // hex-string to match Python wire format
	SD          string `json:"sd"`
	ServiceName string `json:"service_name"`
	IsDefault   bool   `json:"is_default"`
}

// BindingFilter narrows the list. All fields are optional ("" matches all).
type BindingFilter struct {
	IMSI string
	DNN  string
	SST  string
	SD   string
}

// BindingsList returns bindings joined up the FK chain, ordered by is_default DESC, service_name.
func BindingsList(f BindingFilter) ([]Binding, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var sb strings.Builder
	sb.WriteString(`SELECT u.imsi, usd.dnn, nc.sst, COALESCE(nc.sd,''), sb.service_name, sb.is_default
        FROM service_bindings sb
        JOIN ue_slice_dnn usd ON usd.id = sb.slice_dnn_id
        JOIN ue_subscribed_nssai usn ON usn.id = usd.subscribed_nssai_id
        JOIN nssai_catalog nc ON nc.id = usn.nssai_id
        JOIN ue u ON u.id = usn.ue_id WHERE 1=1`)
	var args []any
	if f.IMSI != "" {
		sb.WriteString(` AND u.imsi=?`)
		args = append(args, f.IMSI)
	}
	if f.DNN != "" {
		sb.WriteString(` AND usd.dnn=?`)
		args = append(args, f.DNN)
	}
	if f.SST != "" {
		sb.WriteString(` AND nc.sst=?`)
		args = append(args, normalizeSST(f.SST))
	}
	if f.SD != "" {
		sb.WriteString(` AND IFNULL(nc.sd,'')=?`)
		args = append(args, f.SD)
	}
	sb.WriteString(` ORDER BY sb.is_default DESC, sb.service_name ASC`)

	rows, err := db.Query(sb.String(), args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Binding
	for rows.Next() {
		var b Binding
		var sst int
		var isDef int
		if err := rows.Scan(&b.IMSI, &b.DNN, &sst, &b.SD, &b.ServiceName, &isDef); err != nil {
			return nil, err
		}
		b.SST = fmt.Sprintf("%02X", sst)
		b.IsDefault = isDef != 0
		out = append(out, b)
	}
	return out, rows.Err()
}

// BindingUpsert creates or updates a single binding.
// If isDefault is true, the service must exist and be NonGBR.
func BindingUpsert(imsi, dnn, sst, sd, serviceName string, isDefault bool) (Binding, error) {
	imsi = strings.TrimSpace(imsi)
	dnn = strings.TrimSpace(dnn)
	svc := strings.TrimSpace(serviceName)
	if imsi == "" {
		return Binding{}, errors.New("imsi is required")
	}
	if dnn == "" {
		return Binding{}, errors.New("dnn is required")
	}
	if svc == "" {
		return Binding{}, errors.New("service_name is required")
	}
	sstN := strings.ToUpper(strings.TrimSpace(sst))
	sdN := strings.ToUpper(strings.TrimSpace(sd))

	if isDefault {
		rtype, err := svcResourceType(svc)
		if err != nil {
			return Binding{}, err
		}
		if rtype == "" {
			return Binding{}, fmt.Errorf("unknown service %q", svc)
		}
		if rtype == "GBR" {
			return Binding{}, errors.New("Default QoS flow must be NonGBR")
		}
	}

	ueID, err := UEGetOrCreateByIMSI(imsi, nil)
	if err != nil {
		return Binding{}, err
	}
	db, err := engine.Open()
	if err != nil {
		return Binding{}, err
	}
	tx, err := db.Begin()
	if err != nil {
		return Binding{}, err
	}
	defer tx.Rollback()

	sliceDNNID, err := resolveSliceDNNID(txWrap{tx}, ueID, dnn, sstN, sdN)
	if err != nil {
		return Binding{}, err
	}
	res, err := tx.Exec(
		`UPDATE service_bindings SET is_default=? WHERE slice_dnn_id=? AND service_name=?`,
		b2i(isDefault), sliceDNNID, svc,
	)
	if err != nil {
		return Binding{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		if _, err := tx.Exec(
			`INSERT INTO service_bindings (slice_dnn_id, service_name, is_default) VALUES (?,?,?)`,
			sliceDNNID, svc, b2i(isDefault),
		); err != nil {
			return Binding{}, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Binding{}, err
	}
	return Binding{IMSI: imsi, DNN: dnn, SST: sstN, SD: sdN, ServiceName: svc, IsDefault: isDefault}, nil
}

// BindingsBulkItem is one row in a bulk upsert.
type BindingsBulkItem struct {
	IMSI        string
	DNN         string
	SST         string
	SD          string
	ServiceName string
	IsDefault   bool
}

// BindingsBulk inserts/updates many bindings in a single transaction.
// Returns the number of rows processed (Python returns the same count).
func BindingsBulk(items []BindingsBulkItem) (int, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	count := 0
	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	for _, it := range items {
		imsi := strings.TrimSpace(it.IMSI)
		dnn := strings.TrimSpace(it.DNN)
		svc := strings.TrimSpace(it.ServiceName)
		if imsi == "" || dnn == "" || svc == "" {
			continue
		}
		if it.IsDefault {
			rtype, err := svcResourceType(svc)
			if err != nil || rtype == "" || rtype == "GBR" {
				continue
			}
		}
		ueID, err := UEGetOrCreateByIMSI(imsi, nil)
		if err != nil {
			return count, err
		}
		sst := strings.ToUpper(strings.TrimSpace(it.SST))
		sd := strings.ToUpper(strings.TrimSpace(it.SD))
		sdnID, err := resolveSliceDNNID(txWrap{tx}, ueID, dnn, sst, sd)
		if err != nil {
			return count, err
		}
		res, err := tx.Exec(
			`UPDATE service_bindings SET is_default=? WHERE slice_dnn_id=? AND service_name=?`,
			b2i(it.IsDefault), sdnID, svc,
		)
		if err != nil {
			return count, err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			if _, err := tx.Exec(
				`INSERT OR IGNORE INTO service_bindings (slice_dnn_id, service_name, is_default) VALUES (?,?,?)`,
				sdnID, svc, b2i(it.IsDefault),
			); err != nil {
				return count, err
			}
		}
		count++
	}
	return count, tx.Commit()
}

// BindingsDelete removes bindings for a UE, optionally filtered by dnn/sst/sd/service.
func BindingsDelete(imsi, dnn, sst, sd, service string) (int64, error) {
	if strings.TrimSpace(imsi) == "" {
		return 0, errors.New("imsi is required")
	}
	u, err := UEGetByIMSI(imsi)
	if err != nil || u == nil {
		return 0, err
	}
	var sb strings.Builder
	sb.WriteString(`DELETE FROM service_bindings WHERE slice_dnn_id IN (
        SELECT usd.id FROM ue_slice_dnn usd
        JOIN ue_subscribed_nssai usn ON usn.id = usd.subscribed_nssai_id
        JOIN nssai_catalog nc ON nc.id = usn.nssai_id
        WHERE usn.ue_id = ?`)
	args := []any{u.ID}
	if dnn != "" {
		sb.WriteString(` AND usd.dnn=?`)
		args = append(args, dnn)
	}
	if sst != "" {
		sb.WriteString(` AND nc.sst=?`)
		args = append(args, normalizeSST(sst))
	}
	if sd != "" {
		sb.WriteString(` AND IFNULL(nc.sd,'')=?`)
		args = append(args, strings.ToUpper(sd))
	}
	sb.WriteString(`)`)
	if service != "" {
		sb.WriteString(` AND service_name=?`)
		args = append(args, service)
	}
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(sb.String(), args...)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// svcResourceType looks up services.resource_type. Returns "" if service missing.
func svcResourceType(name string) (string, error) {
	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	var rt string
	err = db.QueryRow(`SELECT resource_type FROM services WHERE name=?`, name).Scan(&rt)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return rt, err
}

// resolveSliceDNNID walks ue→ue_subscribed_nssai→ue_slice_dnn, auto-creating
// the two intermediate rows if missing (matches Python _resolve_slice_dnn_id).
func resolveSliceDNNID(q ExecerQueryer, ueID int64, dnn, sst, sd string) (int64, error) {
	nssaiID, err := getOrCreateNSSAICatalog(q, sst, sd)
	if err != nil {
		return 0, err
	}

	var usnID int64
	err = q.QueryRow(
		`SELECT id FROM ue_subscribed_nssai WHERE ue_id=? AND nssai_id=?`, ueID, nssaiID,
	).Scan(&usnID)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := q.Exec(
			`INSERT INTO ue_subscribed_nssai (ue_id, nssai_id, is_default) VALUES (?,?,1)`, ueID, nssaiID,
		)
		if err != nil {
			return 0, err
		}
		usnID, err = res.LastInsertId()
		if err != nil {
			return 0, err
		}
	} else if err != nil {
		return 0, err
	}

	var usdID int64
	err = q.QueryRow(
		`SELECT id FROM ue_slice_dnn WHERE subscribed_nssai_id=? AND dnn=?`, usnID, dnn,
	).Scan(&usdID)
	if errors.Is(err, sql.ErrNoRows) {
		res, err := q.Exec(
			`INSERT INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default) VALUES (?,?,1)`, usnID, dnn,
		)
		if err != nil {
			return 0, err
		}
		return res.LastInsertId()
	}
	return usdID, err
}
