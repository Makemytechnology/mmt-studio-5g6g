// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_fm.go — REST surface for Fault Management.
//
// Wires `oam/fm` to /api/fm/*. The package owns the alarm life-cycle
// (Raise → Ack → Clear), correlation by (managed_object, probable_cause,
// specific_problem), severity bookkeeping, and the persisted history.
// This surface is the GUI's `templates/faults.html` panel API.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 28.532 §11.2a — Generic fault supervision management service.
//                        Defines the alarm life-cycle (raise, change,
//                        ack, clear) and correlation contract.
//   - TS 32.111-1      — TODO(spec): cited prose-only because the PDF
//                        is not in specs/3gpp/. The original 3GPP
//                        alarm-management spec; TS 28.532 §11.2a
//                        defers to it for the operations vocabulary.
//   - ITU-T X.733      — TODO(spec): perceived-severity and
//                        probable-cause enumerations consumed below.
//
// All response shapes match `templates/faults.html`: top-level
// `{alarms, timestamp}` for list endpoints; `{Critical, Major, Minor,
// Warning, Indeterminate, total}` for the severity-counts endpoint.
// Mutating endpoints return `{ok, ...}`.
package app

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/mmt/mmt-studio-core/oam/fm"
)

// alarmTypeOK / severityOK gate operator-supplied values against the
// X.733 vocabularies before we hand them to fm.Default.Raise — the
// underlying package logs a warning on bad values but still raises;
// surfacing a clean 400 here is the spec-compliant operator response.
var alarmTypeOK = map[string]struct{}{
	fm.AlarmTypeCommunications: {}, fm.AlarmTypeProcessing: {},
	fm.AlarmTypeEnvironmental: {}, fm.AlarmTypeQoS: {},
	fm.AlarmTypeEquipment: {},
}

// raiseSeverityOK excludes "Cleared" — TS 28.532 §11.2a contract is to
// transition to Cleared via the clear operation, never via raise.
var raiseSeverityOK = map[string]struct{}{
	fm.SeverityCritical: {}, fm.SeverityMajor: {}, fm.SeverityMinor: {},
	fm.SeverityWarning: {}, fm.SeverityIndeterminate: {},
}

func (s *Server) registerFMRoutes() {
	r := s.Router

	// ── List / dashboard ─────────────────────────────────────────
	r.Get("/api/fm/active-alarms", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"alarms":    fm.Default.ActiveAlarms(),
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	})

	r.Get("/api/fm/alarm-history", func(w http.ResponseWriter, rq *http.Request) {
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		if limit <= 0 {
			limit = 200
		}
		// `include_active=false` filters to Cleared rows only — operators
		// occasionally want a "what has been resolved" view.
		includeActive := true
		if v := rq.URL.Query().Get("include_active"); v != "" {
			if b, err := strconv.ParseBool(v); err == nil {
				includeActive = b
			}
		}
		hist, err := fm.Default.History(limit, includeActive)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if hist == nil {
			hist = []fm.Alarm{}
		}
		jsonReply(w, map[string]any{
			"alarms":    hist,
			"timestamp": float64(time.Now().UnixMilli()) / 1000.0,
		})
	})

	r.Get("/api/fm/alarm-counts", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, fm.Default.Counts())
	})

	// ── Raise (synthetic / drill / operator-initiated) ────────────
	r.Post("/api/fm/raise", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ManagedObject     string `json:"managed_object"`
			AlarmType         string `json:"alarm_type"`
			ProbableCause     string `json:"probable_cause"`
			PerceivedSeverity string `json:"perceived_severity"`
			SpecificProblem   string `json:"specific_problem"`
			AdditionalText    string `json:"additional_text"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.ManagedObject == "" {
			jsonError(w, "managed_object required", http.StatusBadRequest)
			return
		}
		if d.SpecificProblem == "" {
			jsonError(w, "specific_problem required", http.StatusBadRequest)
			return
		}
		if _, ok := alarmTypeOK[d.AlarmType]; !ok {
			jsonError(w,
				"alarm_type must be one of Communications|Processing|Environmental|QoS|Equipment",
				http.StatusBadRequest)
			return
		}
		if _, ok := raiseSeverityOK[d.PerceivedSeverity]; !ok {
			jsonError(w,
				"perceived_severity must be one of Critical|Major|Minor|Warning|Indeterminate",
				http.StatusBadRequest)
			return
		}
		id, err := fm.Default.Raise(fm.RaiseInput{
			ManagedObject:     d.ManagedObject,
			AlarmType:         d.AlarmType,
			ProbableCause:     d.ProbableCause,
			PerceivedSeverity: d.PerceivedSeverity,
			SpecificProblem:   d.SpecificProblem,
			AdditionalText:    d.AdditionalText,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alarm_id": id})
	})

	// ── Acknowledge ───────────────────────────────────────────────
	r.Post("/api/fm/acknowledge", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AlarmID int64  `json:"alarm_id"`
			User    string `json:"user"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.AlarmID == 0 {
			jsonError(w, "alarm_id required", http.StatusBadRequest)
			return
		}
		if d.User == "" {
			d.User = "operator"
		}
		ok, err := fm.Default.Ack(d.AlarmID, d.User)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "alarm not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alarm_id": d.AlarmID, "user": d.User})
	})

	// ── Clear (single) ────────────────────────────────────────────
	r.Post("/api/fm/clear", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AlarmID int64  `json:"alarm_id"`
			Text    string `json:"text"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.AlarmID == 0 {
			jsonError(w, "alarm_id required", http.StatusBadRequest)
			return
		}
		if d.Text == "" {
			d.Text = "manual clear from GUI"
		}
		ok, err := fm.Default.ClearByID(d.AlarmID, d.Text)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "alarm not found or already cleared", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "alarm_id": d.AlarmID})
	})

	// ── Clear-all (optionally scoped to a managed object) ─────────
	r.Post("/api/fm/clear-all", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ManagedObject string `json:"managed_object"`
		}
		// Empty body is fine — that means "clear every active alarm".
		if rq.ContentLength > 0 {
			_ = json.NewDecoder(rq.Body).Decode(&d)
		}
		count, err := fm.Default.ClearAll(d.ManagedObject)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "cleared": count,
			"managed_object": d.ManagedObject})
	})
}
