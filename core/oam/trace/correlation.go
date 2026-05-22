// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// correlation.go — UE-call correlation index across N1/N2, SBI, and N4.
//
// One row in trace_correlation collects every transport-layer ID
// the 5GC has stamped on a single UE call so the operator panel can
// pivot from "I have an NGAP capture" to "show me the SBI fan-out
// and PFCP control-plane events on the same call" without joining
// per-NF logs in their head.
//
// Spec anchors:
//
//   - 3GPP TS 29.500 §6.10.2.5 — `3gpp-Sbi-Correlation-Info` HTTP
//     header carrying SUPI / GPSI / NF-instance-ID across SBI hops.
//   - 3GPP TS 23.502 §4.4.1.2 — N4 Session Establishment / Modification
//     (SEID pairs that bind SMF↔UPF state to a PDU session).
//   - 3GPP TS 38.413 — NGAP IE AMF-UE-NGAP-ID + RAN-UE-NGAP-ID; the
//     N2 side identifiers we record here.
//   - 3GPP TS 32.422 — Subscriber Trace; ngap_trace_ref bridges to a
//     trace_sessions row when one is active.
//
// The package is best-effort: every call site treats the persistence
// failure as a no-op (the call still completes). We never block the
// signalling hot path on this index.
package trace

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// CorrelationInput carries every ID a producer might know about a UE
// call. Pointers are used for the integer keys so a producer can
// distinguish "didn't observe" from "observed zero" — useful when a
// PDU-session ID of 0 is invalid but a TEID of 0 is meaningful.
type CorrelationInput struct {
	CallID        string `json:"call_id,omitempty"`
	IMSI          string `json:"imsi,omitempty"`
	AmfUeNgapID   *int64 `json:"amf_ue_ngap_id,omitempty"`
	RanUeNgapID   *int64 `json:"ran_ue_ngap_id,omitempty"`
	GnbID         string `json:"gnb_id,omitempty"`
	PduSessionID  *int64 `json:"pdu_session_id,omitempty"`
	SeidUp        *int64 `json:"seid_up,omitempty"`
	SeidCp        *int64 `json:"seid_cp,omitempty"`
	TeidDl        *int64 `json:"teid_dl,omitempty"`
	TeidUl        *int64 `json:"teid_ul,omitempty"`
	OtelTraceID   string `json:"otel_trace_id,omitempty"`
	NgapTraceRef  string `json:"ngap_trace_ref,omitempty"`
	SbiCorrID     string `json:"sbi_corr_id,omitempty"`
}

// ErrNoNaturalKey is returned by RegisterCorrelation when neither
// call_id nor any of the natural keys we can look up by are set.
// We refuse a write that nobody could ever query for — those rows
// are noise that bloat the index and confuse the panel.
var ErrNoNaturalKey = errors.New(
	"correlation: at least one of call_id, imsi, amf_ue_ngap_id, " +
		"seid_up, otel_trace_id or sbi_corr_id is required")

// RegisterCorrelation upserts the correlation row. If `in.CallID` is
// empty, we hunt for an existing row keyed by the strongest natural
// key in the input (preference: imsi → amf_ue_ngap_id → seid_up →
// otel_trace_id → sbi_corr_id). If we find one, we update it in
// place; otherwise we synthesise a call_id and insert. Returns the
// final call_id. Best-effort: panics in the underlying engine bubble
// as errors and the caller is expected to log + continue.
func RegisterCorrelation(in CorrelationInput) (string, error) {
	if !hasNaturalKey(in) {
		return "", ErrNoNaturalKey
	}
	db, err := engine.Open()
	if err != nil {
		return "", err
	}

	callID := in.CallID
	if callID == "" {
		callID = findCallID(db, in)
	}
	if callID == "" {
		callID = newCallID()
		if _, err := db.Exec(`INSERT INTO trace_correlation
			(call_id, imsi, amf_ue_ngap_id, ran_ue_ngap_id, gnb_id,
			 pdu_session_id, seid_up, seid_cp, teid_dl, teid_ul,
			 otel_trace_id, ngap_trace_ref, sbi_corr_id)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			callID, nullStr(in.IMSI), nullInt(in.AmfUeNgapID),
			nullInt(in.RanUeNgapID), nullStr(in.GnbID),
			nullInt(in.PduSessionID), nullInt(in.SeidUp),
			nullInt(in.SeidCp), nullInt(in.TeidDl), nullInt(in.TeidUl),
			nullStr(in.OtelTraceID), nullStr(in.NgapTraceRef),
			nullStr(in.SbiCorrID),
		); err != nil {
			return "", err
		}
		return callID, nil
	}

	// UPDATE — COALESCE keeps already-set fields when the producer
	// only knows half the picture (e.g. AMF first writes IMSI +
	// AMF-UE-NGAP-ID; SMF later attaches SEID + TEIDs).
	if _, err := db.Exec(`UPDATE trace_correlation SET
		imsi          = COALESCE(NULLIF(?, ''), imsi),
		amf_ue_ngap_id= COALESCE(?, amf_ue_ngap_id),
		ran_ue_ngap_id= COALESCE(?, ran_ue_ngap_id),
		gnb_id        = COALESCE(NULLIF(?, ''), gnb_id),
		pdu_session_id= COALESCE(?, pdu_session_id),
		seid_up       = COALESCE(?, seid_up),
		seid_cp       = COALESCE(?, seid_cp),
		teid_dl       = COALESCE(?, teid_dl),
		teid_ul       = COALESCE(?, teid_ul),
		otel_trace_id = COALESCE(NULLIF(?, ''), otel_trace_id),
		ngap_trace_ref= COALESCE(NULLIF(?, ''), ngap_trace_ref),
		sbi_corr_id   = COALESCE(NULLIF(?, ''), sbi_corr_id),
		updated_at    = datetime('now')
		WHERE call_id = ?`,
		in.IMSI, nullInt(in.AmfUeNgapID), nullInt(in.RanUeNgapID),
		in.GnbID, nullInt(in.PduSessionID), nullInt(in.SeidUp),
		nullInt(in.SeidCp), nullInt(in.TeidDl), nullInt(in.TeidUl),
		in.OtelTraceID, in.NgapTraceRef, in.SbiCorrID, callID,
	); err != nil {
		return "", err
	}
	return callID, nil
}

// LookupCallID returns the single correlation row for `callID`, or
// the empty map if not found. The map shape matches the row JSON.
func LookupCallID(callID string) (map[string]any, error) {
	rows, err := queryCorrelation("WHERE call_id=?", []any{callID}, 1)
	if err != nil || len(rows) == 0 {
		return nil, err
	}
	return rows[0], nil
}

// LookupByIMSI returns every correlation row tied to `imsi`.
func LookupByIMSI(imsi string) ([]map[string]any, error) {
	return queryCorrelation("WHERE imsi=?", []any{imsi}, 0)
}

// LookupByAmfNgapID returns every correlation row tied to an
// AMF-UE-NGAP-ID (TS 38.413).
func LookupByAmfNgapID(id int64) ([]map[string]any, error) {
	return queryCorrelation("WHERE amf_ue_ngap_id=?", []any{id}, 0)
}

// LookupBySEID returns rows whose UPF-side SEID matches.
func LookupBySEID(seid int64) ([]map[string]any, error) {
	return queryCorrelation("WHERE seid_up=? OR seid_cp=?", []any{seid, seid}, 0)
}

// LookupByOtelTraceID returns rows whose OTEL trace_id matches —
// closes the loop between distributed-tracing collectors and 3GPP
// transport identifiers.
func LookupByOtelTraceID(tid string) ([]map[string]any, error) {
	return queryCorrelation("WHERE otel_trace_id=?", []any{tid}, 0)
}

// LookupBySbiCorrID returns rows whose 3gpp-Sbi-Correlation-Info
// (TS 29.500) matches.
func LookupBySbiCorrID(id string) ([]map[string]any, error) {
	return queryCorrelation("WHERE sbi_corr_id=?", []any{id}, 0)
}

// ListCorrelations returns the most recent `limit` rows; 0 = 200.
func ListCorrelations(limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 200
	}
	return queryCorrelation("", nil, limit)
}

// DeleteCorrelation removes one row. Returns true if a row was
// deleted; false if the call_id is unknown.
func DeleteCorrelation(callID string) (bool, error) {
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec("DELETE FROM trace_correlation WHERE call_id=?", callID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// PurgeCorrelations wipes every row — operator panel "reset" button.
func PurgeCorrelations() error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM trace_correlation")
	return err
}

// ── helpers ──────────────────────────────────────────────────────

func hasNaturalKey(in CorrelationInput) bool {
	if in.CallID != "" || in.IMSI != "" || in.OtelTraceID != "" ||
		in.SbiCorrID != "" || in.NgapTraceRef != "" {
		return true
	}
	if in.AmfUeNgapID != nil || in.SeidUp != nil || in.SeidCp != nil {
		return true
	}
	return false
}

// findCallID walks the natural keys in priority order and returns the
// call_id of the first matching row.
func findCallID(db *sql.DB, in CorrelationInput) string {
	preds := []struct {
		clause string
		args   []any
		ok     bool
	}{
		{"imsi=?", []any{in.IMSI}, in.IMSI != ""},
		{"amf_ue_ngap_id=?", []any{ptrInt(in.AmfUeNgapID)}, in.AmfUeNgapID != nil},
		{"seid_up=?", []any{ptrInt(in.SeidUp)}, in.SeidUp != nil},
		{"otel_trace_id=?", []any{in.OtelTraceID}, in.OtelTraceID != ""},
		{"sbi_corr_id=?", []any{in.SbiCorrID}, in.SbiCorrID != ""},
	}
	for _, p := range preds {
		if !p.ok {
			continue
		}
		var id string
		err := db.QueryRow(
			"SELECT call_id FROM trace_correlation WHERE "+
				p.clause+" ORDER BY started_at DESC LIMIT 1", p.args...,
		).Scan(&id)
		if err == nil && id != "" {
			return id
		}
	}
	return ""
}

func newCallID() string {
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		// non-zero fallback so the call_id is queryable.
		ns := time.Now().UnixNano()
		for i := 0; i < 8; i++ {
			b[i] = byte(ns >> (i * 8))
		}
	}
	return "call-" + hex.EncodeToString(b[:])
}

func queryCorrelation(where string, args []any, limit int) ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT call_id, imsi, amf_ue_ngap_id, ran_ue_ngap_id, gnb_id,
	             pdu_session_id, seid_up, seid_cp, teid_dl, teid_ul,
	             otel_trace_id, ngap_trace_ref, sbi_corr_id,
	             started_at, updated_at
	      FROM trace_correlation`
	if where != "" {
		q += " " + where
	}
	q += " ORDER BY updated_at DESC, id DESC"
	if limit > 0 {
		q += fmt.Sprintf(" LIMIT %d", limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}

func nullInt(p *int64) any {
	if p == nil {
		return sql.NullInt64{}
	}
	return *p
}

func ptrInt(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}
