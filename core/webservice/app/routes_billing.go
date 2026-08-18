// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Auto-extracted from domain_routes.go (refactor: split god function by
// domain banner). Do not re-merge — keep new domain APIs in their own
// routes_<domain>.go file.
package app

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/chf"
)

func (s *Server) registerBillingRoutes() {
	r := s.Router

	// ── CHF / Billing (billing.py) ───────────────────────────────────
	r.Get("/api/billing/dashboard", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var totalCDRs, pendingCDRs int64
		_ = db.QueryRow(`SELECT COUNT(*) FROM cdrs`).Scan(&totalCDRs)
		_ = db.QueryRow(`SELECT COUNT(*) FROM cdrs WHERE rating_status='pending'`).Scan(&pendingCDRs)
		var totalRevenue float64
		_ = db.QueryRow(`SELECT COALESCE(SUM(total_amount),0) FROM rated_cdrs`).Scan(&totalRevenue)
		var invCount int64
		_ = db.QueryRow(`SELECT COUNT(*) FROM invoices`).Scan(&invCount)
		var balCount int64
		_ = db.QueryRow(`SELECT COUNT(*) FROM balances WHERE status='active'`).Scan(&balCount)
		jsonReply(w, map[string]any{
			"total_cdrs":      totalCDRs,
			"pending_cdrs":    pendingCDRs,
			"total_revenue":   totalRevenue,
			"invoice_count":   invCount,
			"active_balances": balCount,
		})
	})

	r.Get("/api/billing/cdrs", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		cdrType := rq.URL.Query().Get("type")
		status := rq.URL.Query().Get("status")
		limit := 100
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"cdrs": []any{}})
			return
		}
		query := `SELECT cdr_id, imsi, cdr_type, dnn, start_time, end_time,
			vol_ul_bytes, vol_dl_bytes, total_bytes, rating_status
			FROM cdrs WHERE 1=1`
		var args []any
		if imsi != "" {
			query += " AND imsi=?"
			args = append(args, imsi)
		}
		if cdrType != "" {
			query += " AND cdr_type=?"
			args = append(args, cdrType)
		}
		if status != "" {
			query += " AND rating_status=?"
			args = append(args, status)
		}
		query += " ORDER BY created_at DESC LIMIT ?"
		args = append(args, limit)
		rows, err := db.Query(query, args...)
		if err != nil {
			jsonReply(w, map[string]any{"cdrs": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"cdrs": items})
	})

	r.Get("/api/billing/tariff-plans", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"tariffs": []any{}})
			return
		}
		rows, err := db.Query(`SELECT * FROM tariff_plans ORDER BY name`)
		if err != nil {
			jsonReply(w, map[string]any{"tariffs": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"tariffs": items})
	})
	r.Post("/api/billing/tariff-plans", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name            string  `json:"name"`
			Description     string  `json:"description"`
			ChargeType      string  `json:"charge_type"`
			UnitCost        float64 `json:"unit_cost"`
			UnitSize        int64   `json:"unit_size"`
			Currency        string  `json:"currency"`
			FreeTierBytes   int64   `json:"free_tier_bytes"`
			FreeTierSeconds int64   `json:"free_tier_seconds"`
			PeakMultiplier  float64 `json:"peak_multiplier"`
			Per5QIRates     any     `json:"per_5qi_rates"`
			PerDNNRates     any     `json:"per_dnn_rates"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.ChargeType == "" {
			d.ChargeType = "volume"
		}
		if d.UnitCost == 0 {
			d.UnitCost = 0.01
		}
		if d.UnitSize == 0 {
			d.UnitSize = 1048576
		}
		if d.Currency == "" {
			d.Currency = "USD"
		}
		if d.PeakMultiplier == 0 {
			d.PeakMultiplier = 1.0
		}
		qiJSON, _ := json.Marshal(d.Per5QIRates)
		dnnJSON, _ := json.Marshal(d.PerDNNRates)
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		_, err = db.Exec(`INSERT INTO tariff_plans
			(name, description, charge_type, unit_cost, unit_size, currency,
			 free_tier_bytes, free_tier_seconds, peak_multiplier, per_5qi_rates, per_dnn_rates)
			VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
			d.Name, d.Description, d.ChargeType, d.UnitCost, d.UnitSize, d.Currency,
			d.FreeTierBytes, d.FreeTierSeconds, d.PeakMultiplier,
			string(qiJSON), string(dnnJSON))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Delete("/api/billing/tariff-plans/{name}", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		db, _ := engine.Open()
		if db != nil {
			db.Exec(`DELETE FROM tariff_plans WHERE name=?`, name)
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/billing/balances/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"balances": []any{}})
			return
		}
		rows, err := db.Query(`SELECT * FROM balances WHERE imsi=?`, imsi)
		if err != nil {
			jsonReply(w, map[string]any{"balances": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"balances": items})
	})

	r.Post("/api/billing/recharge", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string  `json:"imsi"`
			Amount      float64 `json:"amount"`
			BalanceType string  `json:"balance_type"`
			Reference   string  `json:"reference"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.BalanceType == "" {
			d.BalanceType = "main"
		}
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Upsert balance: add amount to existing or create new
		_, err = db.Exec(`INSERT INTO balances (imsi, balance_type, amount, status)
			VALUES (?, ?, ?, 'active')
			ON CONFLICT(imsi, balance_type) DO UPDATE SET amount = amount + ?`,
			d.IMSI, d.BalanceType, d.Amount, d.Amount)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var newBal float64
		_ = db.QueryRow(`SELECT amount FROM balances WHERE imsi=? AND balance_type=?`,
			d.IMSI, d.BalanceType).Scan(&newBal)
		jsonReply(w, map[string]any{"ok": true, "new_balance": newBal})
	})

	r.Post("/api/billing/rate-pending", func(w http.ResponseWriter, rq *http.Request) {
		n, err := chf.RatePendingCDRs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Compute revenue from rated CDRs
		var revenue float64
		db, dbErr := engine.Open()
		if dbErr == nil {
			_ = db.QueryRow(`SELECT COALESCE(SUM(total_amount),0) FROM rated_cdrs`).Scan(&revenue)
		}
		jsonReply(w, map[string]any{"ok": true, "rated": n, "revenue": revenue})
	})

	// Keep legacy alias
	r.Post("/api/billing/rate-now", func(w http.ResponseWriter, rq *http.Request) {
		n, err := chf.RatePendingCDRs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]int{"rated": n})
	})

	r.Get("/api/billing/rated", dbListRoute(
		`SELECT cdr_id, imsi, charge_amount, currency, total_amount
         FROM rated_cdrs ORDER BY rated_at DESC LIMIT 200`))

	r.Get("/api/billing/invoices", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		status := rq.URL.Query().Get("status")
		limit := 50
		if l, err := strconv.Atoi(rq.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}
		db, err := engine.Open()
		if err != nil {
			jsonReply(w, map[string]any{"invoices": []any{}})
			return
		}
		query := `SELECT * FROM invoices WHERE 1=1`
		var args []any
		if imsi != "" {
			query += " AND imsi=?"
			args = append(args, imsi)
		}
		if status != "" {
			query += " AND status=?"
			args = append(args, status)
		}
		query += " ORDER BY created_at DESC LIMIT ?"
		args = append(args, limit)
		rows, err := db.Query(query, args...)
		if err != nil {
			jsonReply(w, map[string]any{"invoices": []any{}})
			return
		}
		defer rows.Close()
		cols, _ := rows.Columns()
		var items []map[string]any
		for rows.Next() {
			scan := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range scan {
				ptrs[i] = &scan[i]
			}
			if rows.Scan(ptrs...) == nil {
				row := make(map[string]any, len(cols))
				for i, name := range cols {
					row[name] = scan[i]
				}
				items = append(items, row)
			}
		}
		if items == nil {
			items = []map[string]any{}
		}
		jsonReply(w, map[string]any{"invoices": items})
	})

	r.Post("/api/billing/generate-invoices", func(w http.ResponseWriter, rq *http.Request) {
		// Invoice generator not yet ported; stub acknowledges
		var d struct {
			PeriodStart string `json:"period_start"`
			PeriodEnd   string `json:"period_end"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		jsonReply(w, map[string]any{"ok": true, "count": 0, "invoices": []any{}})
	})

	r.Get("/api/billing/export-cdrs", func(w http.ResponseWriter, rq *http.Request) {
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		imsi := rq.URL.Query().Get("imsi")
		query := `SELECT cdr_id, imsi, cdr_type, dnn, start_time, end_time,
			vol_ul_bytes, vol_dl_bytes, total_bytes, rating_status
			FROM cdrs WHERE 1=1`
		var args []any
		if imsi != "" {
			query += " AND imsi=?"
			args = append(args, imsi)
		}
		query += " ORDER BY created_at DESC"
		rows, err := db.Query(query, args...)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer rows.Close()
		w.Header().Set("Content-Type", "text/csv")
		w.Header().Set("Content-Disposition", "attachment; filename=cdrs.csv")
		_, _ = w.Write([]byte("cdr_id,imsi,cdr_type,dnn,start_time,end_time,vol_ul_bytes,vol_dl_bytes,total_bytes,rating_status\n"))
		for rows.Next() {
			var cdrID, cdrImsi, cdrType, dnn, startTime, endTime, status string
			var volUL, volDL, totalBytes int64
			if rows.Scan(&cdrID, &cdrImsi, &cdrType, &dnn, &startTime, &endTime,
				&volUL, &volDL, &totalBytes, &status) == nil {
				line := fmt.Sprintf("%s,%s,%s,%s,%s,%s,%d,%d,%d,%s\n",
					cdrID, cdrImsi, cdrType, dnn, startTime, endTime,
					volUL, volDL, totalBytes, status)
				_, _ = w.Write([]byte(line))
			}
		}
	})
}
