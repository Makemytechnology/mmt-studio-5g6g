// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Quota manager -- online charging quota management (TS 32.291 section 6.1.3.2).
// Port of nf/chf/quota_manager.py.
//
// Manages quota grants for online (prepaid) charging:
//   - Grant quota units for a service
//   - Report usage against granted quota
//   - Check remaining quota
//   - Revoke quota (on session release or policy change)
package chf

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// QuotaGrant holds the result of a grant_quota call.
type QuotaGrant struct {
	GrantedUnits int64   `json:"granted_units"`
	ValidityTime int64   `json:"validity_time"`
	GrantID      string  `json:"grant_id"`
	ExpiresAt    float64 `json:"expires_at"`
	Reason       string  `json:"reason,omitempty"`
}

// QuotaStatus holds the result of report_usage or check_quota.
type QuotaStatus struct {
	GrantedUnits   int64   `json:"granted_units"`
	UsedUnits      int64   `json:"used_units"`
	RemainingQuota int64   `json:"remaining_quota"`
	ValidityTime   int64   `json:"validity_time"`
	ExpiresAt      float64 `json:"expires_at"`
	Status         string  `json:"status"`
}

// GrantQuota grants quota units for an online charging session.
// TS 32.291 section 6.1.3.2: The CHF grants a quota of service units
// to the CTF based on available balance and charging profile limits.
func GrantQuota(imsi, service string, requestedUnits int64) QuotaGrant {
	log := logger.Get("chf.quota")

	db, err := engine.Open()
	if err != nil {
		return QuotaGrant{Reason: "db_error"}
	}

	// Look up subscriber balance.
	var balance, creditLimit float64
	var status string
	err = db.QueryRow(`SELECT balance_amount, credit_limit, status FROM balances
		WHERE imsi=? AND balance_type='main'`, imsi).Scan(&balance, &creditLimit, &status)
	if err != nil {
		log.Warnf("Quota grant rejected: no balance record for imsi=%s", imsi)
		return QuotaGrant{Reason: "no_balance"}
	}
	if status == "exhausted" {
		log.Warnf("Quota grant rejected: balance exhausted for imsi=%s", imsi)
		return QuotaGrant{Reason: "balance_exhausted"}
	}

	// Look up charging profile limits for this service.
	maxFromProfile := requestedUnits
	var validityTime int64 = 3600

	profile := getChargingProfile(db, service)
	if profile != nil {
		if profile.volQuotaUL > 0 && profile.volQuotaUL < maxFromProfile {
			maxFromProfile = profile.volQuotaUL
		}
		if profile.volQuotaDL > 0 && profile.volQuotaDL < maxFromProfile {
			maxFromProfile = profile.volQuotaDL
		}
		if profile.timeQuotaSec > 0 {
			validityTime = profile.timeQuotaSec
		}
	}

	// Determine max grantable units based on balance.
	unitCost := estimateUnitCost(db, service)
	availableFunds := balance + creditLimit
	var maxFromBalance int64
	if unitCost > 0 {
		maxFromBalance = int64(availableFunds / unitCost)
	} else {
		maxFromBalance = requestedUnits
	}
	if maxFromBalance < 0 {
		maxFromBalance = 0
	}

	granted := requestedUnits
	if maxFromProfile < granted {
		granted = maxFromProfile
	}
	if maxFromBalance < granted {
		granted = maxFromBalance
	}

	if granted <= 0 {
		log.Warnf("Quota grant: 0 units for imsi=%s service=%s (balance=%.2f, requested=%d)",
			imsi, service, balance, requestedUnits)
		return QuotaGrant{Reason: "insufficient_funds"}
	}

	// Record the grant.
	grantID := fmt.Sprintf("QG-%s-%d", imsi, time.Now().UnixNano()%1e10)
	now := float64(time.Now().Unix())
	expiresAt := now + float64(validityTime)

	_, err = db.Exec(`INSERT INTO quota_grants
		(imsi, service_name, granted_units, used_units,
		 validity_time, granted_at, expires_at, status)
		VALUES (?, ?, ?, 0, ?, ?, ?, 'active')`,
		imsi, service, granted, validityTime, now, expiresAt)
	if err != nil {
		log.Warnf("Quota grant insert: %v", err)
		return QuotaGrant{Reason: "db_error"}
	}

	log.Infof("Quota granted: imsi=%s service=%s granted=%d/%d validity=%ds grant=%s",
		imsi, service, granted, requestedUnits, validityTime, grantID)

	return QuotaGrant{
		GrantedUnits: granted,
		ValidityTime: validityTime,
		GrantID:      grantID,
		ExpiresAt:    expiresAt,
	}
}

// ReportUsage reports usage against a granted quota.
// TS 32.291 section 6.1.3.2: The CTF reports consumed units to the CHF.
func ReportUsage(imsi, service string, usedUnits int64) QuotaStatus {
	log := logger.Get("chf.quota")

	db, err := engine.Open()
	if err != nil {
		return QuotaStatus{Status: "error"}
	}

	// Find the active grant.
	var grantID int64
	var grantedUnits, currentUsed int64
	var expiresAt sql.NullFloat64
	err = db.QueryRow(`SELECT id, granted_units, used_units, expires_at
		FROM quota_grants
		WHERE imsi=? AND service_name=? AND status='active'
		ORDER BY granted_at DESC LIMIT 1`, imsi, service,
	).Scan(&grantID, &grantedUnits, &currentUsed, &expiresAt)
	if err != nil {
		log.Warnf("Usage report: no active grant for imsi=%s service=%s", imsi, service)
		return QuotaStatus{Status: "no_grant"}
	}

	now := float64(time.Now().Unix())
	if expiresAt.Valid && now > expiresAt.Float64 {
		db.Exec("UPDATE quota_grants SET status='expired' WHERE id=?", grantID)
		log.Infof("Quota expired: imsi=%s service=%s", imsi, service)
		return QuotaStatus{Status: "expired"}
	}

	newUsed := currentUsed + usedUnits
	remaining := grantedUnits - newUsed
	if remaining < 0 {
		remaining = 0
	}

	if remaining <= 0 {
		db.Exec("UPDATE quota_grants SET used_units=?, status='exhausted' WHERE id=?",
			newUsed, grantID)
		log.Infof("Quota exhausted: imsi=%s service=%s used=%d/%d",
			imsi, service, newUsed, grantedUnits)
		return QuotaStatus{
			GrantedUnits:   grantedUnits,
			UsedUnits:      newUsed,
			RemainingQuota: 0,
			Status:         "exhausted",
		}
	}

	db.Exec("UPDATE quota_grants SET used_units=? WHERE id=?", newUsed, grantID)
	log.Debugf("Usage reported: imsi=%s service=%s used=%d remaining=%d",
		imsi, service, newUsed, remaining)

	return QuotaStatus{
		GrantedUnits:   grantedUnits,
		UsedUnits:      newUsed,
		RemainingQuota: remaining,
		Status:         "active",
	}
}

// CheckQuota returns current quota status for a subscriber+service
// without consuming units.
func CheckQuota(imsi, service string) QuotaStatus {
	db, err := engine.Open()
	if err != nil {
		return QuotaStatus{Status: "error"}
	}

	var grantedUnits, usedUnits, validityTime int64
	var expiresAt sql.NullFloat64
	var grantStatus string
	err = db.QueryRow(`SELECT granted_units, used_units, validity_time,
		expires_at, status
		FROM quota_grants
		WHERE imsi=? AND service_name=? AND status='active'
		ORDER BY granted_at DESC LIMIT 1`, imsi, service,
	).Scan(&grantedUnits, &usedUnits, &validityTime, &expiresAt, &grantStatus)
	if err != nil {
		return QuotaStatus{Status: "no_grant"}
	}

	now := float64(time.Now().Unix())
	if expiresAt.Valid && now > expiresAt.Float64 {
		db.Exec(`UPDATE quota_grants SET status='expired'
			WHERE imsi=? AND service_name=? AND status='active'`, imsi, service)
		return QuotaStatus{
			GrantedUnits: grantedUnits,
			UsedUnits:    usedUnits,
			ExpiresAt:    expiresAt.Float64,
			Status:       "expired",
		}
	}

	remaining := grantedUnits - usedUnits
	if remaining < 0 {
		remaining = 0
	}
	return QuotaStatus{
		GrantedUnits:   grantedUnits,
		UsedUnits:      usedUnits,
		RemainingQuota: remaining,
		ValidityTime:   validityTime,
		ExpiresAt:      expiresAt.Float64,
		Status:         grantStatus,
	}
}

// RevokeQuota revokes all active quota grants for a subscriber+service.
// TS 32.291 section 6.1.3.2: Called on session release or policy change.
// Returns number of grants revoked.
func RevokeQuota(imsi, service string) int64 {
	log := logger.Get("chf.quota")

	db, err := engine.Open()
	if err != nil {
		return 0
	}

	result, err := db.Exec(`UPDATE quota_grants SET status='revoked'
		WHERE imsi=? AND service_name=? AND status='active'`, imsi, service)
	if err != nil {
		return 0
	}
	count, _ := result.RowsAffected()
	if count > 0 {
		log.Infof("Quota revoked: imsi=%s service=%s grants=%d", imsi, service, count)
	}
	return count
}

// GetAllGrants lists quota grants with optional filters.
func GetAllGrants(imsi, status string, limit int) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}

	query := `SELECT id, imsi, service_name, granted_units, used_units,
		validity_time, granted_at, expires_at, status
		FROM quota_grants WHERE 1=1`
	var params []interface{}

	if imsi != "" {
		query += " AND imsi=?"
		params = append(params, imsi)
	}
	if status != "" {
		query += " AND status=?"
		params = append(params, status)
	}
	query += " ORDER BY granted_at DESC LIMIT ?"
	params = append(params, limit)

	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var grants []map[string]interface{}
	for rows.Next() {
		var (
			id, grantedUnits, usedUnits, validityTime int64
			grantedAt                                  float64
			expiresAt                                  sql.NullFloat64
			grantIMSI, serviceName, grantStatus        string
		)
		if rows.Scan(&id, &grantIMSI, &serviceName, &grantedUnits, &usedUnits,
			&validityTime, &grantedAt, &expiresAt, &grantStatus) != nil {
			continue
		}
		grants = append(grants, map[string]interface{}{
			"id":             id,
			"imsi":           grantIMSI,
			"service_name":   serviceName,
			"granted_units":  grantedUnits,
			"used_units":     usedUnits,
			"validity_time":  validityTime,
			"granted_at":     grantedAt,
			"expires_at":     expiresAt.Float64,
			"status":         grantStatus,
		})
	}
	return grants, rows.Err()
}

// ── internal helpers ────────────────────────────────────────────────────

type chargingProfile struct {
	volQuotaUL   int64
	volQuotaDL   int64
	timeQuotaSec int64
}

// getChargingProfile looks up charging profile for a service.
func getChargingProfile(db *sql.DB, serviceName string) *chargingProfile {
	var cp chargingProfile
	err := db.QueryRow(`SELECT COALESCE(cp.vol_quota_ul,0), COALESCE(cp.vol_quota_dl,0),
		COALESCE(cp.time_quota_sec,0)
		FROM charging_profiles cp
		JOIN services s ON s.charging_profile = cp.name
		WHERE s.name = ?`, serviceName,
	).Scan(&cp.volQuotaUL, &cp.volQuotaDL, &cp.timeQuotaSec)
	if err != nil {
		return nil
	}
	return &cp
}

// estimateUnitCost estimates cost per unit for quota calculation.
func estimateUnitCost(db *sql.DB, service string) float64 {
	var unitCost float64
	var unitSize int64

	// Try to find tariff via service binding.
	err := db.QueryRow(`SELECT tp.unit_cost, tp.unit_size FROM tariff_plans tp
		JOIN charging_profiles cp ON cp.name = tp.name
		JOIN services s ON s.charging_profile = cp.name
		WHERE s.name = ? LIMIT 1`, service,
	).Scan(&unitCost, &unitSize)
	if err == nil && unitSize > 0 {
		return unitCost / float64(unitSize)
	}

	// Fallback: default tariff.
	err = db.QueryRow(`SELECT unit_cost, unit_size FROM tariff_plans WHERE name='default'`,
	).Scan(&unitCost, &unitSize)
	if err == nil && unitSize > 0 {
		return unitCost / float64(unitSize)
	}

	return 0.01 / 1048576 // $0.01/MB fallback
}
