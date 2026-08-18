// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_ran_sharing.go — REST surface for NG-RAN Sharing.
//
// Wires `security/ran_sharing` to /api/ran-sharing/*. Models the two
// operator-side artefacts NG-RAN Sharing requires: a sharing
// agreement (which PLMNs share which gNB, with what capacity split)
// and a per-gNB allocation map. The AMF / SMF call CheckAccess()
// during Initial-Registration to decide whether a UE from a partner
// PLMN is admissible on a shared cell.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 22.261 §6.21       NG-RAN Sharing — service requirements
//                           (MORAN / MOCN concepts, broadcast
//                           obligations, admission contract).
//   - TS 22.261 §6.21.2.2   Indirect network sharing.
//   - TS 23.501 §5.17.4     Network sharing support and EPS/5GS
//                           interworking.
//
// All response envelopes carry `{ok: true, ...}` to match the
// existing ran_sharing.html GUI panel.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/security/ran_sharing"
)

func (s *Server) registerRANSharingRoutes() {
	r := s.Router

	// ── Stats / dashboard ─────────────────────────────────────────
	r.Get("/api/ran-sharing/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": ran_sharing.GetStats()})
	})

	r.Get("/api/ran-sharing/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": ran_sharing.GetStats()})
	})

	// ── Agreements (TS 22.261 §6.21) ──────────────────────────────
	r.Get("/api/ran-sharing/agreements", func(w http.ResponseWriter, rq *http.Request) {
		status := rq.URL.Query().Get("status")
		list, err := ran_sharing.ListAgreements(status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "agreements": list})
	})

	r.Post("/api/ran-sharing/agreements", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Name               string                 `json:"name"`
			SharingType        string                 `json:"sharing_type"`
			ParticipatingPLMNs string                 `json:"participating_plmns"`
			CapacitySplit      map[string]interface{} `json:"capacity_split"`
			PriorityRules      map[string]interface{} `json:"priority_rules"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		row, err := ran_sharing.CreateAgreement(d.Name, d.SharingType,
			d.ParticipatingPLMNs, d.CapacitySplit, d.PriorityRules)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "agreement": row})
	})

	r.Get("/api/ran-sharing/agreements/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		row, err := ran_sharing.GetAgreement(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "agreement not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "agreement": row})
	})

	r.Patch("/api/ran-sharing/agreements/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		var fields map[string]interface{}
		if !decodeJSON(w, rq, &fields) {
			return
		}
		row, err := ran_sharing.UpdateAgreement(id, fields)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "agreement": row})
	})

	r.Delete("/api/ran-sharing/agreements/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		ok := ran_sharing.DeleteAgreement(id)
		jsonReply(w, map[string]any{"ok": ok})
	})

	r.Post("/api/ran-sharing/agreements/{id}/activate", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		row, err := ran_sharing.ActivateAgreement(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "agreement": row})
	})

	r.Post("/api/ran-sharing/agreements/{id}/deactivate", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid id", http.StatusBadRequest)
			return
		}
		row, err := ran_sharing.DeactivateAgreement(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "agreement": row})
	})

	// ── Per-gNB allocation (MORAN — TS 22.261 §6.21) ──────────────
	r.Get("/api/ran-sharing/gnb-map", func(w http.ResponseWriter, rq *http.Request) {
		var agrID int64
		if v := rq.URL.Query().Get("agreement_id"); v != "" {
			agrID, _ = strconv.ParseInt(v, 10, 64)
		}
		list, err := ran_sharing.ListGnBMap(agrID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "gnb_map": list})
	})

	r.Post("/api/ran-sharing/gnb-map", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AgreementID          int64  `json:"agreement_id"`
			GnbID                string `json:"gnb_id"`
			AllocatedCapacityPct int    `json:"allocated_capacity_pct"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.AgreementID == 0 || d.GnbID == "" {
			jsonError(w, "agreement_id and gnb_id required", http.StatusBadRequest)
			return
		}
		row, err := ran_sharing.UpsertGnBMap(d.AgreementID, d.GnbID, d.AllocatedCapacityPct)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated,
			map[string]any{"ok": true, "allocation": row})
	})

	r.Delete("/api/ran-sharing/gnb-map/{agreement_id}/{gnb_id}", func(w http.ResponseWriter, rq *http.Request) {
		agrID, err := strconv.ParseInt(chi.URLParam(rq, "agreement_id"), 10, 64)
		if err != nil {
			jsonError(w, "invalid agreement_id", http.StatusBadRequest)
			return
		}
		gnbID := chi.URLParam(rq, "gnb_id")
		if err := ran_sharing.DeleteGnBMap(agrID, gnbID); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Admission gate (CheckAccess) ──────────────────────────────
	r.Post("/api/ran-sharing/check-access", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			PLMN  string `json:"plmn"`
			GnbID string `json:"gnb_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.PLMN == "" || d.GnbID == "" {
			jsonError(w, "plmn and gnb_id required", http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"access": ran_sharing.CheckAccessMap(d.PLMN, d.GnbID),
		})
	})

	// ── Usage log ─────────────────────────────────────────────────
	r.Get("/api/ran-sharing/usage-log", func(w http.ResponseWriter, rq *http.Request) {
		var agrID int64
		if v := rq.URL.Query().Get("agreement_id"); v != "" {
			agrID, _ = strconv.ParseInt(v, 10, 64)
		}
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		list, err := ran_sharing.ListUsageLog(agrID, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"ok": true, "usage": list})
	})

	r.Post("/api/ran-sharing/usage-log", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			AgreementID    int64   `json:"agreement_id"`
			PLMN           string  `json:"plmn"`
			GnbID          string  `json:"gnb_id"`
			UECount        int     `json:"ue_count"`
			ThroughputMbps float64 `json:"throughput_mbps"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := ran_sharing.InsertUsageLog(d.AgreementID, d.PLMN, d.GnbID,
			d.UECount, d.ThroughputMbps); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReplyStatus(w, http.StatusCreated, map[string]any{"ok": true})
	})
}
