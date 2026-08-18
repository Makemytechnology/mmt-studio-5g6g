// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/nssai.go — Per-UE subscribed NSSAI, slice-DNN, and full subscription tree (TS 23.501 §5.15)
package crud

import (
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// NSSAICatalogEntry is a row in the global slice catalog.
type NSSAICatalogEntry struct {
	ID   int64  `json:"id"`
	SST  int    `json:"sst"`
	SD   string `json:"sd"`
	Name string `json:"name"`
}

// SubscribedNSSAI is the Python subscribed_nssai_list item.
type SubscribedNSSAI struct {
	SST       int
	SD        string
	IsDefault bool
	NSSAIID   int64
	Name      string
}

// SliceDNN is the Python slice_dnn_list item.
type SliceDNN struct {
	SST               int
	SD                string
	DNN               string
	IsDefault         bool
	SliceDNNID        int64
	SubscribedNSSAIID int64
}

// SubscriptionTree mirrors the dict returned by subscription_tree_get.
type SubscriptionTree struct {
	IMSI   string          `json:"imsi"`
	Slices []TreeSlice     `json:"slices"`
}

type TreeSlice struct {
	NSSAIID   int64        `json:"nssai_id"`
	SST       int          `json:"sst"`
	SD        string       `json:"sd,omitempty"`
	Name      string       `json:"name,omitempty"`
	IsDefault bool         `json:"is_default"`
	DNNs      []TreeDNN    `json:"dnns"`
}

type TreeDNN struct {
	DNN       string       `json:"dnn"`
	IsDefault bool         `json:"is_default"`
	Services  []TreeService `json:"services"`
}

type TreeService struct {
	ServiceName string `json:"service_name"`
	IsDefault   bool   `json:"is_default"`
}

// normalizeSST accepts an int / float / decimal / hex string and
// returns an int. JSON numbers decoded into `any` arrive as float64
// (encoding/json) so that case must be handled explicitly — without
// it any numeric SST sent over the catalog API silently clamps to 1.
func normalizeSST(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int32:
		return int(x)
	case int64:
		return int(x)
	case float64:
		return int(x)
	case float32:
		return int(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 1
		}
		// Try decimal first so "10" doesn't become 0x10. Hex is the
		// fallback for spec-style "0a"/"FF" strings the GUI may send.
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
		if n, err := strconv.ParseInt(s, 16, 64); err == nil {
			return int(n)
		}
	}
	return 1
}

// NormalizeSST is exported for consumers that already do the "01"→1 dance.
func NormalizeSST(v any) int { return normalizeSST(v) }

// NSSAICatalogGetOrCreate is the public wrapper around the package-
// private getOrCreateNSSAICatalog. Used by NSaaS provisioning to
// populate nsaas_slices.nssai_catalog_id and any other consumer that
// needs a catalog FK without hand-rolling the upsert.
func NSSAICatalogGetOrCreate(sst any, sd string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	return getOrCreateNSSAICatalog(db, sst, sd)
}

// getOrCreateNSSAICatalog returns the nssai_catalog.id for (sst, sd), creating
// it if missing. Takes a *sql.Tx or *sql.DB via ExecerQueryer.
func getOrCreateNSSAICatalog(q ExecerQueryer, sst any, sd string) (int64, error) {
	sstInt := normalizeSST(sst)
	sd = strings.TrimSpace(sd)
	var id int64
	err := q.QueryRow(
		`SELECT id FROM nssai_catalog WHERE sst=? AND IFNULL(sd,'')=IFNULL(?, '')`,
		sstInt, nullIfEmpty(sd),
	).Scan(&id)
	if err == nil {
		return id, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}
	res, err := q.Exec(
		`INSERT INTO nssai_catalog (sst, sd) VALUES (?, ?)`,
		sstInt, nullIfEmpty(sd),
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// ExecerQueryer is satisfied by *sql.DB and *sql.Tx — used by helpers that
// can run inside or outside a transaction.
type ExecerQueryer interface {
	Exec(query string, args ...any) (sql.Result, error)
	Query(query string, args ...any) (*sql.Rows, error)
	QueryRow(query string, args ...any) *sql.Row
}

// ── NSSAI catalog ───────────────────────────────────────────────────────

func NSSAICatalogList() ([]NSSAICatalogEntry, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, sst, COALESCE(sd,''), COALESCE(name,'') FROM nssai_catalog ORDER BY sst, sd`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []NSSAICatalogEntry
	for rows.Next() {
		var e NSSAICatalogEntry
		if err := rows.Scan(&e.ID, &e.SST, &e.SD, &e.Name); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func NSSAICatalogAdd(sst any, sd, name string) (NSSAICatalogEntry, error) {
	sstInt := normalizeSST(sst)
	db, err := engine.Open()
	if err != nil {
		return NSSAICatalogEntry{}, err
	}
	res, err := db.Exec(
		`INSERT INTO nssai_catalog (sst, sd, name) VALUES (?, ?, ?)`,
		sstInt, nullIfEmpty(sd), nullIfEmpty(name),
	)
	if err != nil {
		return NSSAICatalogEntry{}, err
	}
	id, _ := res.LastInsertId()
	return NSSAICatalogEntry{ID: id, SST: sstInt, SD: sd, Name: name}, nil
}

// NSSAICatalogUpdate edits an existing slice. Empty SD / Name are
// stored as NULL (matches the Add path's nullIfEmpty convention).
// Returns rowsAffected so the caller can distinguish "no row" from
// "no change" (both yield 0 here — caller can re-read if needed).
func NSSAICatalogUpdate(id int64, sst any, sd, name string) (int64, error) {
	sstInt := normalizeSST(sst)
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(
		`UPDATE nssai_catalog SET sst=?, sd=?, name=? WHERE id=?`,
		sstInt, nullIfEmpty(sd), nullIfEmpty(name), id,
	)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

func NSSAICatalogDelete(id int64) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM nssai_catalog WHERE id=?`, id)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ── Subscribed NSSAI ────────────────────────────────────────────────────

// SubscribedNSSAIList returns the subscribed slices for a UE via the FK chain.
func SubscribedNSSAIList(imsi string) ([]SubscribedNSSAI, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT nc.sst, COALESCE(nc.sd,''), usn.is_default, nc.id, COALESCE(nc.name,'')
         FROM ue_subscribed_nssai usn
         JOIN nssai_catalog nc ON nc.id = usn.nssai_id
         JOIN ue u ON u.id = usn.ue_id
         WHERE u.imsi = ?
         ORDER BY nc.sst, nc.sd`, imsi,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SubscribedNSSAI
	for rows.Next() {
		var s SubscribedNSSAI
		var isDef int
		if err := rows.Scan(&s.SST, &s.SD, &isDef, &s.NSSAIID, &s.Name); err != nil {
			return nil, err
		}
		s.IsDefault = isDef != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// SubscribedNSSAIInput is a single item accepted by SubscribedNSSAISave.
// Either NSSAIID OR (SST + SD) must be provided.
type SubscribedNSSAIInput struct {
	NSSAIID   int64
	SST       any    // int or hex string
	SD        string
	IsDefault bool
}

// SubscribedNSSAISave replaces the subscribed slices for a UE.
func SubscribedNSSAISave(imsi string, items []SubscribedNSSAIInput) (int, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	var ueID int64
	if err := db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("UE %s not found", imsi)
		}
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM ue_subscribed_nssai WHERE ue_id=?`, ueID); err != nil {
		return 0, err
	}
	count := 0
	for _, it := range items {
		id := it.NSSAIID
		if id == 0 {
			if it.SST == nil {
				continue
			}
			id, err = getOrCreateNSSAICatalog(txWrap{tx}, it.SST, it.SD)
			if err != nil {
				return 0, err
			}
		}
		isDef := 1
		if !it.IsDefault {
			isDef = 0
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO ue_subscribed_nssai (ue_id, nssai_id, is_default) VALUES (?,?,?)`,
			ueID, id, isDef,
		); err != nil {
			return 0, err
		}
		count++
	}
	return count, tx.Commit()
}

// txWrap adapts *sql.Tx to the ExecerQueryer interface.
type txWrap struct{ tx *sql.Tx }

func (w txWrap) Exec(q string, a ...any) (sql.Result, error) { return w.tx.Exec(q, a...) }
func (w txWrap) Query(q string, a ...any) (*sql.Rows, error) { return w.tx.Query(q, a...) }
func (w txWrap) QueryRow(q string, a ...any) *sql.Row        { return w.tx.QueryRow(q, a...) }

// ── Slice-DNN ───────────────────────────────────────────────────────────

// SliceDNNList returns per-slice DNN authorizations for a UE.
func SliceDNNList(imsi string) ([]SliceDNN, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT nc.sst, COALESCE(nc.sd,''), usd.dnn, usd.is_default, usd.id, usn.id
         FROM ue_slice_dnn usd
         JOIN ue_subscribed_nssai usn ON usn.id = usd.subscribed_nssai_id
         JOIN nssai_catalog nc ON nc.id = usn.nssai_id
         JOIN ue u ON u.id = usn.ue_id
         WHERE u.imsi = ?
         ORDER BY nc.sst, nc.sd, usd.is_default DESC, usd.dnn`, imsi,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SliceDNN
	for rows.Next() {
		var s SliceDNN
		var isDef int
		if err := rows.Scan(&s.SST, &s.SD, &s.DNN, &isDef, &s.SliceDNNID, &s.SubscribedNSSAIID); err != nil {
			return nil, err
		}
		s.IsDefault = isDef != 0
		out = append(out, s)
	}
	return out, rows.Err()
}

// SliceDNNInput is accepted by SliceDNNSave.
type SliceDNNInput struct {
	SST       any
	SD        string
	DNN       string
	IsDefault bool
}

// SliceDNNSave replaces all slice-DNN authorizations for a UE.
func SliceDNNSave(imsi string, items []SliceDNNInput) (int, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	var ueID int64
	if err := db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("UE %s not found", imsi)
		}
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	// Map (sst, sd) → ue_subscribed_nssai.id for this UE.
	type key struct {
		sst int
		sd  string
	}
	rows, err := tx.Query(
		`SELECT usn.id, nc.sst, COALESCE(nc.sd,'')
         FROM ue_subscribed_nssai usn
         JOIN nssai_catalog nc ON nc.id = usn.nssai_id
         WHERE usn.ue_id=?`, ueID,
	)
	if err != nil {
		return 0, err
	}
	m := make(map[key]int64)
	for rows.Next() {
		var usnID int64
		var sst int
		var sd string
		if err := rows.Scan(&usnID, &sst, &sd); err != nil {
			rows.Close()
			return 0, err
		}
		m[key{sst, strings.ToUpper(sd)}] = usnID
	}
	rows.Close()

	// Wipe existing slice_dnn rows (CASCADE kills service_bindings).
	for _, usnID := range m {
		if _, err := tx.Exec(`DELETE FROM ue_slice_dnn WHERE subscribed_nssai_id=?`, usnID); err != nil {
			return 0, err
		}
	}
	for _, it := range items {
		sst := normalizeSST(it.SST)
		sd := strings.ToUpper(strings.TrimSpace(it.SD))
		usnID, ok := m[key{sst, sd}]
		if !ok {
			continue
		}
		isDef := 0
		if it.IsDefault {
			isDef = 1
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default) VALUES (?,?,?)`,
			usnID, it.DNN, isDef,
		); err != nil {
			return 0, err
		}
	}
	return len(items), tx.Commit()
}

// ── Subscription tree ───────────────────────────────────────────────────

// SubscriptionTreeGet returns the nested slices → dnns → services tree for an IMSI.
func SubscriptionTreeGet(imsi string) (*SubscriptionTree, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var ueID int64
	if err := db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	out := &SubscriptionTree{IMSI: imsi}

	// slices
	sliceRows, err := db.Query(
		`SELECT usn.id, nc.id, nc.sst, COALESCE(nc.sd,''), COALESCE(nc.name,''), usn.is_default
         FROM ue_subscribed_nssai usn
         JOIN nssai_catalog nc ON nc.id = usn.nssai_id
         WHERE usn.ue_id = ?
         ORDER BY nc.sst, nc.sd`, ueID,
	)
	if err != nil {
		return nil, err
	}
	type sliceCtx struct {
		usnID int64
		slice *TreeSlice
	}
	var slices []sliceCtx
	for sliceRows.Next() {
		var usnID int64
		var s TreeSlice
		var isDef int
		if err := sliceRows.Scan(&usnID, &s.NSSAIID, &s.SST, &s.SD, &s.Name, &isDef); err != nil {
			sliceRows.Close()
			return nil, err
		}
		s.IsDefault = isDef != 0
		slices = append(slices, sliceCtx{usnID, &s})
	}
	sliceRows.Close()

	// dnns + services per slice
	for _, sc := range slices {
		dnnRows, err := db.Query(
			`SELECT id, dnn, is_default FROM ue_slice_dnn
             WHERE subscribed_nssai_id = ?
             ORDER BY is_default DESC, dnn`, sc.usnID,
		)
		if err != nil {
			return nil, err
		}
		type dnnCtx struct {
			usdID int64
			dnn   *TreeDNN
		}
		var dnns []dnnCtx
		for dnnRows.Next() {
			var usdID int64
			var d TreeDNN
			var isDef int
			if err := dnnRows.Scan(&usdID, &d.DNN, &isDef); err != nil {
				dnnRows.Close()
				return nil, err
			}
			d.IsDefault = isDef != 0
			dnns = append(dnns, dnnCtx{usdID, &d})
		}
		dnnRows.Close()

		for _, dc := range dnns {
			svcRows, err := db.Query(
				`SELECT service_name, is_default FROM service_bindings
                 WHERE slice_dnn_id = ?
                 ORDER BY is_default DESC, service_name`, dc.usdID,
			)
			if err != nil {
				return nil, err
			}
			for svcRows.Next() {
				var sv TreeService
				var isDef int
				if err := svcRows.Scan(&sv.ServiceName, &isDef); err != nil {
					svcRows.Close()
					return nil, err
				}
				sv.IsDefault = isDef != 0
				dc.dnn.Services = append(dc.dnn.Services, sv)
			}
			svcRows.Close()
			sc.slice.DNNs = append(sc.slice.DNNs, *dc.dnn)
		}
		out.Slices = append(out.Slices, *sc.slice)
	}
	return out, nil
}

// SubscriptionTreeSave atomically replaces the whole subscription tree.
// Returns counts of (slices, dnns, bindings) created.
func SubscriptionTreeSave(imsi string, tree SubscriptionTree) (slices, dnns, bindings int, err error) {
	db, err := engine.Open()
	if err != nil {
		return 0, 0, 0, err
	}
	var ueID int64
	if err = db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, 0, 0, fmt.Errorf("UE %s not found", imsi)
		}
		return 0, 0, 0, err
	}
	tx, err := db.Begin()
	if err != nil {
		return 0, 0, 0, err
	}
	defer func() {
		if err != nil {
			_ = tx.Rollback()
		}
	}()

	if _, err = tx.Exec(`DELETE FROM ue_subscribed_nssai WHERE ue_id=?`, ueID); err != nil {
		return 0, 0, 0, err
	}

	for _, sl := range tree.Slices {
		nssaiID := sl.NSSAIID
		if nssaiID == 0 {
			nssaiID, err = getOrCreateNSSAICatalog(txWrap{tx}, sl.SST, sl.SD)
			if err != nil {
				return 0, 0, 0, err
			}
		}
		isDef := 0
		if sl.IsDefault {
			isDef = 1
		}
		if _, err = tx.Exec(
			`INSERT OR IGNORE INTO ue_subscribed_nssai (ue_id, nssai_id, is_default) VALUES (?,?,?)`,
			ueID, nssaiID, isDef,
		); err != nil {
			return 0, 0, 0, err
		}
		var usnID int64
		if err = tx.QueryRow(
			`SELECT id FROM ue_subscribed_nssai WHERE ue_id=? AND nssai_id=?`, ueID, nssaiID,
		).Scan(&usnID); err != nil {
			return 0, 0, 0, err
		}
		slices++

		for _, dn := range sl.DNNs {
			if dn.DNN == "" {
				continue
			}
			dDef := 0
			if dn.IsDefault {
				dDef = 1
			}
			if _, err = tx.Exec(
				`INSERT OR IGNORE INTO ue_slice_dnn (subscribed_nssai_id, dnn, is_default) VALUES (?,?,?)`,
				usnID, dn.DNN, dDef,
			); err != nil {
				return 0, 0, 0, err
			}
			var usdID int64
			if err = tx.QueryRow(
				`SELECT id FROM ue_slice_dnn WHERE subscribed_nssai_id=? AND dnn=?`, usnID, dn.DNN,
			).Scan(&usdID); err != nil {
				return 0, 0, 0, err
			}
			dnns++

			for _, sv := range dn.Services {
				if sv.ServiceName == "" {
					continue
				}
				sDef := 0
				if sv.IsDefault {
					sDef = 1
				}
				if _, err = tx.Exec(
					`INSERT OR IGNORE INTO service_bindings (slice_dnn_id, service_name, is_default) VALUES (?,?,?)`,
					usdID, sv.ServiceName, sDef,
				); err != nil {
					return 0, 0, 0, err
				}
				bindings++
			}
		}
	}
	err = tx.Commit()
	return
}
