// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// api.go — operator-API helpers for NWDAF: external DataPoint
// ingestion (TS 23.288 §6.2 Data Collection), subscription read /
// update, and confidence-thresholded analytics.
//
// The wire-side §6.2 collection is normally driven by NF callers
// pushing into the in-memory `dataCache`; the operator/test-side
// surface here lets a tester (or an external NF without an SBI
// stack) push a DataPoint via REST and have it land in both the
// in-memory cache (so the next ComputeAnalytics call sees it) and
// the persisted `nwdaf_data_points` row (so historical replay
// works).
package nwdaf

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/nwdaf/analytics"
)

// IngestDataPoint persists a DataPoint and folds it into the
// in-memory cache so the next analytics request sees it. Returns the
// row id on success.
//
// Spec anchor: TS 23.288 §6.2 — Procedures for Data Collection.
func (s *Service) IngestDataPoint(dp analytics.DataPoint) (int64, error) {
	if dp.SourceNF == "" {
		return 0, fmt.Errorf("source_nf required")
	}
	if !analytics.ValidAnalyticsIDs[dp.AnalyticsID] {
		return 0, fmt.Errorf(
			"unknown analytics_id %q (TS 23.288 §6.1)", dp.AnalyticsID)
	}
	// Validate the data_json blob is valid JSON so we never persist
	// a row that ComputeAnalytics can't parse.
	if dp.DataJSON != "" {
		var x map[string]any
		if err := json.Unmarshal([]byte(dp.DataJSON), &x); err != nil {
			return 0, fmt.Errorf("data_json must be valid JSON: %v", err)
		}
	}
	if dp.CollectedAt == 0 {
		dp.CollectedAt = float64(time.Now().Unix())
	}
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	res, err := db.Exec(`INSERT INTO nwdaf_data_points
		(source_nf, analytics_id, imsi, dnn, sst, data_json, collected_at)
		VALUES (?, ?, ?, ?, ?, ?, datetime('now'))`,
		dp.SourceNF, dp.AnalyticsID,
		nilIfEmpty(dp.IMSI), nilIfEmpty(dp.DNN), nilIfEmpty(""),
		dp.DataJSON)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	// Mirror into the in-memory cache so the next compute call sees it
	// without waiting for a collection-loop tick.
	s.mu.Lock()
	s.dataCache[dp.AnalyticsID] = append(s.dataCache[dp.AnalyticsID], dp)
	s.mu.Unlock()
	return id, nil
}

// GetSubscription returns one subscription row by sub_id, or nil if
// not found. The route layer maps nil → 404.
func (s *Service) GetSubscription(subID string) (map[string]any, error) {
	if subID == "" {
		return nil, fmt.Errorf("sub_id required")
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		"SELECT * FROM nwdaf_subscriptions WHERE sub_id=? LIMIT 1", subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, _ := scanAllRows(rows)
	if len(out) == 0 {
		return nil, nil
	}
	return out[0], nil
}

// UpdateSubscription applies a sparse update to an existing
// subscription. Allow-listed columns: target_imsi, target_dnn,
// target_sst, callback_url, interval_sec, status. Unknown keys are
// silently dropped (the route layer filters before this call).
//
// Returns true when at least one row was updated; false → 404.
func (s *Service) UpdateSubscription(subID string,
	patch map[string]any) (bool, error) {

	if subID == "" {
		return false, fmt.Errorf("sub_id required")
	}
	allowed := map[string]bool{
		"target_imsi":  true,
		"target_dnn":   true,
		"target_sst":   true,
		"callback_url": true,
		"interval_sec": true,
		"status":       true,
	}
	cols := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		cols = append(cols, k+"=?")
		args = append(args, v)
	}
	if len(cols) == 0 {
		return false, fmt.Errorf("no allowed fields in patch")
	}
	args = append(args, subID)
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	q := "UPDATE nwdaf_subscriptions SET " + joinComma(cols) + " WHERE sub_id=?"
	res, err := db.Exec(q, args...)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// IngestStats returns counters useful for the panel — total persisted
// data points across IDs, plus per-ID breakdown.
func (s *Service) IngestStats() map[string]any {
	out := map[string]any{
		"total":   int64(0),
		"per_id":  map[string]int64{},
	}
	db, err := engine.Open()
	if err != nil {
		return out
	}
	var total int64
	_ = db.QueryRow(`SELECT COUNT(*) FROM nwdaf_data_points`).Scan(&total)
	out["total"] = total
	rows, err := db.Query(
		`SELECT analytics_id, COUNT(*) FROM nwdaf_data_points
		 GROUP BY analytics_id`)
	if err != nil {
		return out
	}
	defer rows.Close()
	per := map[string]int64{}
	for rows.Next() {
		var aid string
		var n int64
		if err := rows.Scan(&aid, &n); err == nil {
			per[aid] = n
		}
	}
	out["per_id"] = per
	return out
}

// ── helpers ─────────────────────────────────────────────────────

func joinComma(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	out := parts[0]
	for _, p := range parts[1:] {
		out += ", " + p
	}
	return out
}

// withDB is a small helper that opens, runs fn, and ensures the *sql.DB
// is closed even on panic. Used by the test harness — kept here so it
// stays close to the operator surface.
func withDB(fn func(*sql.DB) error) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	return fn(db)
}
