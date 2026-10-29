// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package chf -- Charging Function (TS 32.290 / TS 32.291).
//
// Go port of nf/chf/. Covers:
//   - CDR generation (data + voice + video)   [cdr.go]
//   - Rating engine (tariff plan -> rated CDR) [rating.go]
//   - Balance management (prepaid)             [balance_manager.go]
//   - Quota management (online quota)          [quota_manager.go]
//
// This file ports cdr_generator.py, cdr_export.py and the trigger hooks
// from chf_trigger.py.
package chf

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ── CDR Generation (cdr_generator.py) ──────────────────────────────────

// GenerateDataCDR inserts a CDR row for a data session into the cdrs table.
// Called from the SMF trigger hook on PDU session release.
func GenerateDataCDR(imsi string, pduSessionID int, dnn string,
	startTime, endTime float64, volUL, volDL, pktUL, pktDL int64,
	sst, fiveqi int, chargingMethod string, tariffPlanID *int64) (string, error) {
	log := logger.Get("chf.cdr")

	now := float64(time.Now().Unix())
	if startTime == 0 {
		startTime = now
	}
	if endTime == 0 {
		endTime = now
	}
	duration := endTime - startTime
	total := volUL + volDL
	cdrID := fmt.Sprintf("CDR-%s-%d-%d", imsi, pduSessionID, int64(endTime))

	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`INSERT INTO cdrs
        (cdr_id, imsi, cdr_type, pdu_session_id, dnn, sst, fiveqi,
         start_time, end_time, duration_sec,
         vol_ul_bytes, vol_dl_bytes, total_bytes, pkt_ul, pkt_dl,
         charging_method, tariff_plan_id, rating_status, created_at)
        VALUES (?, ?, 'data', ?, ?, ?, ?,
                ?, ?, ?,
                ?, ?, ?, ?, ?,
                ?, ?, 'pending', ?)`,
		cdrID, imsi, pduSessionID, dnn, fmt.Sprintf("%d", sst), fiveqi,
		startTime, endTime, duration,
		volUL, volDL, total, pktUL, pktDL,
		chargingMethod, tariffPlanID, now,
	)
	if err != nil {
		return "", err
	}
	log.Infof("CDR generated id=%s type=data imsi=%s dnn=%s vol=%dB duration=%.0fs",
		cdrID, imsi, dnn, total, duration)
	return cdrID, nil
}

// GenerateVoiceCDR inserts a voice/video CDR for an IMS call (TS 32.260).
func GenerateVoiceCDR(imsi, callID, callerURI, calleeURI string,
	startTime, endTime float64, mediaType string,
	msisdn, chargingMethod string, tariffPlanID *int64) (string, error) {
	log := logger.Get("chf.cdr")

	now := float64(time.Now().Unix())
	if startTime == 0 {
		startTime = now
	}
	if endTime == 0 {
		endTime = now
	}
	duration := endTime - startTime
	cdrType := "voice"
	if strings.Contains(mediaType, "video") {
		cdrType = "video"
	}
	cdrID := fmt.Sprintf("CDR-%s-%s-%d", imsi, callID, int64(endTime))

	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	_, err = db.Exec(`INSERT INTO cdrs
        (cdr_id, imsi, msisdn, cdr_type,
         start_time, end_time, duration_sec,
         charging_method, tariff_plan_id, rating_status,
         call_id, caller_uri, callee_uri, media_type, created_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?, ?, ?, ?)`,
		cdrID, imsi, msisdn, cdrType,
		startTime, endTime, duration,
		chargingMethod, tariffPlanID,
		callID, callerURI, calleeURI, mediaType, now,
	)
	if err != nil {
		return "", err
	}
	log.Infof("CDR generated id=%s type=%s imsi=%s duration=%.0fs %s->%s",
		cdrID, cdrType, imsi, duration, callerURI, calleeURI)
	return cdrID, nil
}

// CDR holds a row from the cdrs table.
//
// SessionID is the synthetic <imsi>-<pdu_session_id> token the
// Nchf operator API hands back from CreateChargingSession; CDR
// consumers correlate by it (TS 32.291 §6.1.3 "Charging-Data
// Reference"). It's computed on scan rather than stored as a
// dedicated column to avoid a schema migration.
type CDR struct {
	ID             int64          `json:"id"`
	CDRID          string         `json:"cdr_id"`
	SessionID      string         `json:"session_id,omitempty"`
	IMSI           string         `json:"imsi"`
	MSISDN         sql.NullString `json:"msisdn"`
	CDRType        string         `json:"cdr_type"`
	PDUSessionID   sql.NullInt64  `json:"pdu_session_id"`
	DNN            sql.NullString `json:"dnn"`
	SST            sql.NullString `json:"sst"`
	FiveQI         sql.NullInt64  `json:"fiveqi"`
	StartTime      float64        `json:"start_time"`
	EndTime        float64        `json:"end_time"`
	DurationSec    float64        `json:"duration_sec"`
	VolULBytes     int64          `json:"vol_ul_bytes"`
	VolDLBytes     int64          `json:"vol_dl_bytes"`
	TotalBytes     int64          `json:"total_bytes"`
	PktUL          int64          `json:"pkt_ul"`
	PktDL          int64          `json:"pkt_dl"`
	ChargingMethod sql.NullString `json:"charging_method"`
	TariffPlanID   sql.NullInt64  `json:"tariff_plan_id"`
	RatingStatus   string         `json:"rating_status"`
	CallID         sql.NullString `json:"call_id"`
	CallerURI      sql.NullString `json:"caller_uri"`
	CalleeURI      sql.NullString `json:"callee_uri"`
	MediaType      sql.NullString `json:"media_type"`
	CreatedAt      float64        `json:"created_at"`
}

// GetCDRs queries CDRs with optional filters.
func GetCDRs(imsi, cdrType, status string, dateFrom, dateTo float64, limit int) ([]CDR, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 100
	}

	query := "SELECT id, cdr_id, imsi, msisdn, cdr_type, pdu_session_id, dnn, sst, fiveqi, start_time, end_time, duration_sec, vol_ul_bytes, vol_dl_bytes, total_bytes, pkt_ul, pkt_dl, charging_method, tariff_plan_id, rating_status, call_id, caller_uri, callee_uri, media_type, created_at FROM cdrs WHERE 1=1"
	var params []interface{}

	if imsi != "" {
		query += " AND imsi=?"
		params = append(params, imsi)
	}
	if cdrType != "" {
		query += " AND cdr_type=?"
		params = append(params, cdrType)
	}
	if status != "" {
		query += " AND rating_status=?"
		params = append(params, status)
	}
	if dateFrom > 0 {
		query += " AND created_at>=?"
		params = append(params, dateFrom)
	}
	if dateTo > 0 {
		query += " AND created_at<=?"
		params = append(params, dateTo)
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	params = append(params, limit)

	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var cdrs []CDR
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
			continue
		}
		if c.PDUSessionID.Valid {
			c.SessionID = fmt.Sprintf("%s-%d", c.IMSI, c.PDUSessionID.Int64)
		}
		cdrs = append(cdrs, c)
	}
	return cdrs, rows.Err()
}

// CDRStats holds summary statistics for CDRs.
type CDRStats struct {
	Total       int            `json:"total"`
	Pending     int            `json:"pending"`
	Rated       int            `json:"rated"`
	ByType      map[string]int `json:"by_type"`
	TotalVolume int64          `json:"total_volume"`
}

// GetCDRStats returns CDR summary statistics.
func GetCDRStats() (CDRStats, error) {
	db, err := engine.Open()
	if err != nil {
		return CDRStats{}, err
	}
	var stats CDRStats
	stats.ByType = make(map[string]int)

	db.QueryRow("SELECT COUNT(*) FROM cdrs").Scan(&stats.Total)
	db.QueryRow("SELECT COUNT(*) FROM cdrs WHERE rating_status='pending'").Scan(&stats.Pending)
	db.QueryRow("SELECT COUNT(*) FROM cdrs WHERE rating_status='rated'").Scan(&stats.Rated)
	db.QueryRow("SELECT COALESCE(SUM(total_bytes),0) FROM cdrs").Scan(&stats.TotalVolume)

	rows, err := db.Query("SELECT cdr_type, COUNT(*) FROM cdrs GROUP BY cdr_type")
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var t string
			var c int
			if rows.Scan(&t, &c) == nil {
				stats.ByType[t] = c
			}
		}
	}
	return stats, nil
}

// ExportCDRsCSV exports CDRs as a CSV string.
func ExportCDRsCSV(imsi string, dateFrom, dateTo float64, limit int) (string, error) {
	log := logger.Get("chf.export")

	cdrs, err := GetCDRs(imsi, "", "", dateFrom, dateTo, limit)
	if err != nil {
		return "", err
	}

	var b strings.Builder
	// Header
	b.WriteString("CDR_ID,IMSI,MSISDN,Type,DNN,SST,5QI,Start,End,Duration_s,UL_Bytes,DL_Bytes,Total_Bytes,UL_Pkts,DL_Pkts,Charging,Status,Call_ID,Caller,Callee,Media,Created\n")

	for _, c := range cdrs {
		startStr := time.Unix(int64(c.StartTime), 0).UTC().Format("2006-01-02 15:04:05")
		endStr := time.Unix(int64(c.EndTime), 0).UTC().Format("2006-01-02 15:04:05")
		createdStr := time.Unix(int64(c.CreatedAt), 0).UTC().Format("2006-01-02 15:04:05")
		fmt.Fprintf(&b, "%s,%s,%s,%s,%s,%s,%d,%s,%s,%.1f,%d,%d,%d,%d,%d,%s,%s,%s,%s,%s,%s,%s\n",
			c.CDRID, c.IMSI, c.MSISDN.String, c.CDRType,
			c.DNN.String, c.SST.String, c.FiveQI.Int64,
			startStr, endStr, c.DurationSec,
			c.VolULBytes, c.VolDLBytes, c.TotalBytes, c.PktUL, c.PktDL,
			c.ChargingMethod.String, c.RatingStatus,
			c.CallID.String, c.CallerURI.String, c.CalleeURI.String,
			c.MediaType.String, createdStr,
		)
	}

	log.Infof("CDR export: %d records", len(cdrs))
	return b.String(), nil
}

// ── Trigger hooks (chf_trigger.py) ──────────────────────────────────────

// OnPDUSessionCreated is called by SMF when a PDU session is established.
// Starts a charging session to track cumulative usage.
func OnPDUSessionCreated(imsi string, pduSessionID int, dnn string,
	chargingMethod string) bool {
	log := logger.Get("chf.trigger")

	db, err := engine.Open()
	if err != nil {
		log.Errorf("OnPDUSessionCreated DB: %v", err)
		return false
	}
	sessionID := fmt.Sprintf("%s-%d", imsi, pduSessionID)
	now := time.Now().Format(time.RFC3339)
	if _, err := db.Exec(`INSERT OR REPLACE INTO charging_sessions
        (imsi, session_id, service_name, charging_method, status,
         created_at, updated_at)
        VALUES (?, ?, 'default', ?, 'active', ?, ?)`,
		imsi, sessionID, chargingMethod, now, now,
	); err != nil {
		log.Errorf("charging_session insert: %v", err)
		return false
	}

	// Online charging: check balance (prepaid).
	if chargingMethod == "online" {
		allowed, balance := CheckBalance(imsi, 0)
		if !allowed {
			log.Warnf("Online charging: session rejected -- insufficient balance "+
				"(imsi=%s balance=%.2f)", imsi, balance)
			return false
		}
	}
	return true
}

// OnPDUSessionReleased generates a final CDR and closes the charging session.
func OnPDUSessionReleased(imsi string, pduSessionID int) {
	log := logger.Get("chf.trigger")

	db, err := engine.Open()
	if err != nil {
		return
	}
	sessionID := fmt.Sprintf("%s-%d", imsi, pduSessionID)
	now := time.Now().Format(time.RFC3339)

	// Fetch session info for charging method. TS 32.291 §6.1.3 lets
	// a Termination Charging Data Request follow any number of
	// Update requests, so the row may currently be either 'active'
	// (no Update yet) or 'interim' (one or more Updates received) —
	// both must be released to a CDR.
	var chargingMethod string
	var dnn string
	err = db.QueryRow(`SELECT charging_method, COALESCE(service_name,'internet')
		FROM charging_sessions WHERE session_id=? AND status IN ('active','interim')`,
		sessionID).Scan(&chargingMethod, &dnn)
	if err != nil {
		chargingMethod = "offline"
		dnn = "internet"
	}

	// Close charging session.
	if _, err := db.Exec(`UPDATE charging_sessions SET status='released',
        released_at=?, updated_at=? WHERE session_id=? AND status IN ('active','interim')`,
		now, now, sessionID,
	); err != nil {
		log.Warnf("charging_session close: %v", err)
	}

	// Generate data CDR.
	ts := float64(time.Now().Unix())
	cdrID, err := GenerateDataCDR(imsi, pduSessionID, dnn,
		ts-1, ts, 0, 0, 0, 0, 1, 9, chargingMethod, nil)
	if err != nil {
		log.Warnf("CDR on release: %v", err)
		return
	}

	// Online charging: rate and debit balance.
	if chargingMethod == "online" && cdrID != "" {
		cdrs, err := GetCDRs(imsi, "", "", 0, 0, 1)
		if err == nil && len(cdrs) > 0 {
			rated := RateCDR(cdrs[0], nil, 0)
			if rated != nil && rated.TotalAmount > 0 {
				Debit(imsi, rated.TotalAmount, "main", cdrID)
			}
		}
	}
}

// OnIMSCallEnded generates a voice/video CDR on SIP BYE.
func OnIMSCallEnded(imsi, callID, callerURI, calleeURI string,
	startTime, endTime float64, mediaType string) {
	GenerateVoiceCDR(imsi, callID, callerURI, calleeURI,
		startTime, endTime, mediaType, "", "offline", nil)
}

// ── Invoice generation (invoice_generator.py) ─────────────────────────

// Invoice holds a generated billing invoice summary.
type Invoice struct {
	InvoiceNumber string  `json:"invoice_number"`
	IMSI          string  `json:"imsi"`
	MSISDN        string  `json:"msisdn,omitempty"`
	DataCharges   float64 `json:"data_charges"`
	VoiceCharges  float64 `json:"voice_charges"`
	VideoCharges  float64 `json:"video_charges"`
	Tax           float64 `json:"tax"`
	Total         float64 `json:"total"`
}

// GenerateInvoices generates invoices for all subscribers with rated CDRs
// in [periodStart, periodEnd). Port of chf/invoice_generator.py.
func GenerateInvoices(periodStart, periodEnd float64) ([]Invoice, error) {
	log := logger.Get("chf.invoice")
	db, err := engine.Open()
	if err != nil {
		return nil, fmt.Errorf("invoice DB: %w", err)
	}

	rows, err := db.Query(`SELECT DISTINCT r.imsi FROM rated_cdrs r
		JOIN cdrs c ON c.cdr_id = r.cdr_id
		WHERE c.created_at >= ? AND c.created_at < ? AND c.rating_status = 'rated'`,
		periodStart, periodEnd)
	if err != nil {
		return nil, fmt.Errorf("invoice query: %w", err)
	}
	defer rows.Close()

	var imsis []string
	for rows.Next() {
		var imsi string
		if err := rows.Scan(&imsi); err == nil {
			imsis = append(imsis, imsi)
		}
	}

	var invoices []Invoice
	for _, imsi := range imsis {
		inv, err := generateSingleInvoice(db, imsi, periodStart, periodEnd)
		if err != nil {
			log.Warnf("invoice for %s: %v", imsi, err)
			continue
		}
		if inv != nil {
			invoices = append(invoices, *inv)
		}
	}
	log.Infof("Invoices generated: %d for period %.0f-%.0f", len(invoices), periodStart, periodEnd)
	return invoices, nil
}

func generateSingleInvoice(db *sql.DB, imsi string, periodStart, periodEnd float64) (*Invoice, error) {
	// Aggregate rated CDRs by type.
	chargeRows, err := db.Query(`SELECT c.cdr_type, SUM(r.total_amount) as total, COUNT(*) as cnt
		FROM rated_cdrs r JOIN cdrs c ON c.cdr_id = r.cdr_id
		WHERE r.imsi = ? AND c.created_at >= ? AND c.created_at < ?
		GROUP BY c.cdr_type`, imsi, periodStart, periodEnd)
	if err != nil {
		return nil, err
	}
	defer chargeRows.Close()

	var data, voice, video float64
	found := false
	for chargeRows.Next() {
		var ctype string
		var total sql.NullFloat64
		var cnt int
		if err := chargeRows.Scan(&ctype, &total, &cnt); err != nil {
			continue
		}
		found = true
		amt := total.Float64
		switch ctype {
		case "data":
			data = amt
		case "voice":
			voice = amt
		case "video":
			video = amt
		}
	}
	if !found {
		return nil, nil
	}

	// Total tax.
	var taxTotal sql.NullFloat64
	_ = db.QueryRow(`SELECT SUM(r.tax_amount) FROM rated_cdrs r
		JOIN cdrs c ON c.cdr_id = r.cdr_id
		WHERE r.imsi = ? AND c.created_at >= ? AND c.created_at < ?`,
		imsi, periodStart, periodEnd).Scan(&taxTotal)
	tax := taxTotal.Float64

	// MSISDN.
	var msisdn sql.NullString
	_ = db.QueryRow(`SELECT msisdn FROM ue WHERE imsi=?`, imsi).Scan(&msisdn)

	// Generate invoice number.
	now := time.Now()
	invoiceNumber := fmt.Sprintf("INV-%s-%06X",
		now.Format("200601"), now.UnixNano()&0xFFFFFF)

	total := data + voice + video + tax

	_, err = db.Exec(`INSERT INTO invoices (invoice_number, imsi, msisdn,
		billing_period_start, billing_period_end,
		total_data_charges, total_voice_charges, total_video_charges,
		total_tax, total_amount, discount_amount,
		status, generated_at, due_date)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 0, 'generated', ?, ?)`,
		invoiceNumber, imsi, msisdn.String, periodStart, periodEnd,
		data, voice, video, tax, total,
		float64(now.Unix()), float64(now.Unix())+30*86400)
	if err != nil {
		return nil, fmt.Errorf("invoice insert: %w", err)
	}

	// Mark CDRs as invoiced.
	_, _ = db.Exec(`UPDATE cdrs SET rating_status='invoiced'
		WHERE imsi=? AND created_at >= ? AND created_at < ? AND rating_status='rated'`,
		imsi, periodStart, periodEnd)

	return &Invoice{
		InvoiceNumber: invoiceNumber,
		IMSI:          imsi,
		MSISDN:        msisdn.String,
		DataCharges:   data,
		VoiceCharges:  voice,
		VideoCharges:  video,
		Tax:           tax,
		Total:         total,
	}, nil
}

// GetInvoices queries invoices with optional filters. Port of invoice_generator.get_invoices.
func GetInvoices(imsi, status string, limit int) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	query := "SELECT invoice_number, imsi, msisdn, billing_period_start, billing_period_end, " +
		"total_data_charges, total_voice_charges, total_video_charges, " +
		"total_tax, total_amount, status, generated_at FROM invoices WHERE 1=1"
	var args []interface{}
	if imsi != "" {
		query += " AND imsi=?"
		args = append(args, imsi)
	}
	if status != "" {
		query += " AND status=?"
		args = append(args, status)
	}
	if limit <= 0 {
		limit = 50
	}
	query += " ORDER BY generated_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var result []map[string]interface{}
	for rows.Next() {
		var invNum, invIMSI string
		var invMSISDN sql.NullString
		var ps, pe, dc, vc, vidC, tx, ta float64
		var st string
		var gen float64
		if err := rows.Scan(&invNum, &invIMSI, &invMSISDN, &ps, &pe,
			&dc, &vc, &vidC, &tx, &ta, &st, &gen); err != nil {
			continue
		}
		result = append(result, map[string]interface{}{
			"invoice_number": invNum, "imsi": invIMSI,
			"msisdn": invMSISDN.String,
			"billing_period_start": ps, "billing_period_end": pe,
			"total_data_charges": dc, "total_voice_charges": vc,
			"total_video_charges": vidC, "total_tax": tx,
			"total_amount": ta, "status": st, "generated_at": gen,
		})
	}
	return result, nil
}
