// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Rating engine -- applies tariff plans to pending CDRs and produces
// rated_cdrs rows. Port of nf/chf/rating_engine.py.
//
// Rating formula: charge = (usage_units / tariff.unit_size) * tariff.unit_cost
// Supports: per-volume, per-time, per-event, flat rate.
// Per-5QI and per-DNN rate overrides. Tax calculation.
package chf

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// RatedCDR holds the result of rating a single CDR.
type RatedCDR struct {
	CDRID           string  `json:"cdr_id"`
	IMSI            string  `json:"imsi"`
	ChargeAmount    float64 `json:"charge_amount"`
	Currency        string  `json:"currency"`
	TariffPlanID    *int64  `json:"tariff_plan_id"`
	UnitCount       float64 `json:"unit_count"`
	UnitType        string  `json:"unit_type"`
	DiscountApplied float64 `json:"discount_applied"`
	TaxAmount       float64 `json:"tax_amount"`
	TaxRate         float64 `json:"tax_rate"`
	TotalAmount     float64 `json:"total_amount"`
}

// RateCDR rates a single CDR using the assigned tariff plan.
// If tariff is nil, it is looked up from tariff_plan_id or defaults.
// taxRate is a fraction (e.g. 0.18 for 18%).
func RateCDR(cdr CDR, tariff map[string]interface{}, taxRate float64) *RatedCDR {
	log := logger.Get("chf.rating")

	db, err := engine.Open()
	if err != nil {
		log.Warnf("RateCDR open DB: %v", err)
		return nil
	}

	// Load tariff if not provided.
	if tariff == nil {
		tariff = loadTariff(db, cdr.TariffPlanID)
	}
	if tariff == nil {
		tariff = getDefaultTariff(db)
	}

	chargeType := mapStr(tariff, "charge_type", "volume")
	unitCost := mapFloat(tariff, "unit_cost", 0.01)
	unitSize := mapFloat(tariff, "unit_size", 1048576)
	freeBytes := mapFloat(tariff, "free_tier_bytes", 0)
	freeSecs := mapFloat(tariff, "free_tier_seconds", 0)
	currency := mapStr(tariff, "currency", "USD")

	// Per-5QI rate override.
	if per5qi, ok := tariff["per_5qi_rates"].(string); ok && per5qi != "" {
		var rates map[string]float64
		if json.Unmarshal([]byte(per5qi), &rates) == nil {
			key := fmt.Sprintf("%d", cdr.FiveQI.Int64)
			if r, found := rates[key]; found {
				unitCost = r
			}
		}
	}

	// Per-DNN rate override.
	if perDNN, ok := tariff["per_dnn_rates"].(string); ok && perDNN != "" {
		var rates map[string]float64
		if json.Unmarshal([]byte(perDNN), &rates) == nil {
			if r, found := rates[cdr.DNN.String]; found {
				unitCost = r
			}
		}
	}

	var unitCount float64
	var unitType string

	switch chargeType {
	case "volume":
		billable := math.Max(0, float64(cdr.TotalBytes)-freeBytes)
		if unitSize < 1 {
			unitSize = 1
		}
		unitCount = billable / unitSize
		if unitSize >= 1048576 {
			unitType = "MB"
		} else if unitSize >= 1024 {
			unitType = "KB"
		} else {
			unitType = "B"
		}
	case "time":
		billable := math.Max(0, cdr.DurationSec-freeSecs)
		if unitSize < 1 {
			unitSize = 1
		}
		unitCount = billable / unitSize
		if unitSize >= 60 {
			unitType = "min"
		} else {
			unitType = "sec"
		}
	case "event":
		unitCount = 1
		unitType = "event"
	case "flat":
		unitCount = 1
		unitType = "flat"
	default:
		unitCount = 0
		unitType = "MB"
	}

	charge := math.Round(unitCount*unitCost*10000) / 10000
	tax := math.Round(charge*taxRate*10000) / 10000
	total := math.Round((charge+tax)*10000) / 10000

	var tariffID *int64
	if v, ok := tariff["id"]; ok {
		switch tv := v.(type) {
		case int64:
			tariffID = &tv
		case float64:
			i := int64(tv)
			tariffID = &i
		}
	}

	return &RatedCDR{
		CDRID:           cdr.CDRID,
		IMSI:            cdr.IMSI,
		ChargeAmount:    charge,
		Currency:        currency,
		TariffPlanID:    tariffID,
		UnitCount:       math.Round(unitCount*10000) / 10000,
		UnitType:        unitType,
		DiscountApplied: 0,
		TaxAmount:       tax,
		TaxRate:         taxRate,
		TotalAmount:     total,
	}
}

// RatePendingCDRs walks all CDRs with rating_status='pending', rates them,
// inserts rated_cdrs rows, and flips status to 'rated'.
// Returns (ratedCount, totalRevenue).
func RatePendingCDRs() (int, error) {
	return RatePendingCDRsWithTax(0, 500)
}

// RatePendingCDRsWithTax is like RatePendingCDRs but with configurable
// tax rate and limit.
func RatePendingCDRsWithTax(taxRate float64, limit int) (int, error) {
	log := logger.Get("chf.rating")
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	if limit <= 0 {
		limit = 500
	}

	rows, err := db.Query(`SELECT id, cdr_id, imsi, msisdn, cdr_type,
		pdu_session_id, dnn, sst, fiveqi,
		start_time, end_time, duration_sec,
		vol_ul_bytes, vol_dl_bytes, total_bytes, pkt_ul, pkt_dl,
		charging_method, tariff_plan_id, rating_status,
		call_id, caller_uri, callee_uri, media_type, created_at
		FROM cdrs WHERE rating_status='pending' ORDER BY created_at LIMIT ?`, limit)
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	rated := 0
	totalRevenue := 0.0

	for rows.Next() {
		var c CDR
		if err := rows.Scan(
			&c.ID, &c.CDRID, &c.IMSI, &c.MSISDN, &c.CDRType,
			&c.PDUSessionID, &c.DNN, &c.SST, &c.FiveQI,
			&c.StartTime, &c.EndTime, &c.DurationSec,
			&c.VolULBytes, &c.VolDLBytes, &c.TotalBytes, &c.PktUL, &c.PktDL,
			&c.ChargingMethod, &c.TariffPlanID, &c.RatingStatus,
			&c.CallID, &c.CallerURI, &c.CalleeURI, &c.MediaType,
			&c.CreatedAt,
		); err != nil {
			log.Warnf("scan CDR: %v", err)
			continue
		}

		r := RateCDR(c, nil, taxRate)
		if r == nil {
			continue
		}

		now := float64(time.Now().Unix())
		if _, err := db.Exec(`INSERT INTO rated_cdrs
			(cdr_id, imsi, charge_amount, currency, tariff_plan_id,
			 unit_count, unit_type, discount_applied,
			 tax_amount, tax_rate, total_amount, rated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			r.CDRID, r.IMSI, r.ChargeAmount, r.Currency, r.TariffPlanID,
			r.UnitCount, r.UnitType, r.DiscountApplied,
			r.TaxAmount, r.TaxRate, r.TotalAmount, now,
		); err != nil {
			log.Warnf("insert rated_cdrs: %v", err)
			continue
		}
		if _, err := db.Exec(`UPDATE cdrs SET rating_status='rated' WHERE id=?`, c.ID); err != nil {
			log.Warnf("update cdr status: %v", err)
		}
		rated++
		totalRevenue += r.TotalAmount
	}
	if rated > 0 {
		log.Infof("Rating: %d CDRs rated, total revenue=%.2f", rated, totalRevenue)
	}
	return rated, rows.Err()
}

// ── internal helpers ────────────────────────────────────────────────────

// loadTariff loads a tariff plan by ID into a map.
func loadTariff(db *sql.DB, tariffID sql.NullInt64) map[string]interface{} {
	if !tariffID.Valid {
		return nil
	}
	var (
		id                         int64
		chargeType, currency       string
		unitCost                   float64
		unitSize                   int64
		freeBytes, freeSecs        int64
		per5qi, perDNN             sql.NullString
	)
	err := db.QueryRow(`SELECT id, charge_type, unit_cost, unit_size, currency,
		COALESCE(free_tier_bytes,0), COALESCE(free_tier_seconds,0),
		per_5qi_rates, per_dnn_rates
		FROM tariff_plans WHERE id=?`, tariffID.Int64,
	).Scan(&id, &chargeType, &unitCost, &unitSize, &currency,
		&freeBytes, &freeSecs, &per5qi, &perDNN)
	if err != nil {
		return nil
	}
	return map[string]interface{}{
		"id":               id,
		"charge_type":      chargeType,
		"unit_cost":        unitCost,
		"unit_size":        float64(unitSize),
		"currency":         currency,
		"free_tier_bytes":  float64(freeBytes),
		"free_tier_seconds": float64(freeSecs),
		"per_5qi_rates":    per5qi.String,
		"per_dnn_rates":    perDNN.String,
	}
}

// getDefaultTariff returns the 'default' tariff plan, creating it if needed.
func getDefaultTariff(db *sql.DB) map[string]interface{} {
	var (
		id                   int64
		chargeType, currency string
		unitCost             float64
		unitSize             int64
	)
	err := db.QueryRow(`SELECT id, charge_type, unit_cost, unit_size, currency
		FROM tariff_plans WHERE name='default'`,
	).Scan(&id, &chargeType, &unitCost, &unitSize, &currency)

	if errors.Is(err, sql.ErrNoRows) {
		db.Exec(`INSERT OR IGNORE INTO tariff_plans
			(name, description, charge_type, unit_cost, unit_size, currency)
			VALUES ('default', 'Default per-MB rate', 'volume', 0.01, 1048576, 'USD')`)
		err = db.QueryRow(`SELECT id, charge_type, unit_cost, unit_size, currency
			FROM tariff_plans WHERE name='default'`,
		).Scan(&id, &chargeType, &unitCost, &unitSize, &currency)
	}
	if err != nil {
		return map[string]interface{}{
			"charge_type": "volume",
			"unit_cost":   0.01,
			"unit_size":   float64(1048576),
			"currency":    "USD",
		}
	}
	return map[string]interface{}{
		"id":          id,
		"charge_type": chargeType,
		"unit_cost":   unitCost,
		"unit_size":   float64(unitSize),
		"currency":    currency,
	}
}

func mapStr(m map[string]interface{}, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return def
}

func mapFloat(m map[string]interface{}, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		switch tv := v.(type) {
		case float64:
			return tv
		case int64:
			return float64(tv)
		case int:
			return float64(tv)
		}
	}
	return def
}
