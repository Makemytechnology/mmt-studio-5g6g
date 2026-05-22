// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_n26.go — REST surface for the N26 (EPC ↔ 5GC) inter-system
// handover panel.
//
// Wires the AMF-side audit log (`nf/amf/n26`) and the MME-side
// mapped-context cache (`access/epc/mme/n26`) to /api/n26/* per:
//
//   - TS 23.501 §5.17.2.2     Interworking with N26 — only mode that
//                             carries a mapped context.
//   - TS 23.501 §5.17.2.2.2   Mobility for single-registration UEs.
//   - TS 23.501 §5.17.5.2.1   Interworking with N26 for monitoring
//                             events — motivates the audit log.
//   - TS 23.502 §4.11         System interworking procedures with EPC.
//
// What this surface owns:
//
//   - `GET  /api/n26/status`               combined dashboard (audit
//                                          stats + mapped-context
//                                          cache occupancy + enabled).
//   - `POST /api/n26/handover/5g-to-4g`    derive mapped context →
//                                          push to MME → log audit.
//   - `POST /api/n26/handover/4g-to-5g`    forward intent to AMF →
//                                          log audit.
//   - `GET  /api/n26/handovers?limit=N`    audit log (most recent first).
//   - `GET  /api/n26/handovers/{imsi}`     per-IMSI audit history.
//   - `GET  /api/n26/contexts`             current mapped-context cache.
//   - `POST /api/n26/contexts/{imsi}/expire` evict one cached entry.
//   - `POST /api/n26/contexts/cleanup`     evict every TTL-expired row.
//
// The actual NAS / S1 / NGAP signalling lives elsewhere (nf/amf/gmm
// + the EPC handler). This file is the operator-facing panel and the
// test hook that lets the GUI / tester drive the audit log.
package app

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	mmen26 "github.com/mmt/mmt-studio-core/access/epc/mme/n26"
	"github.com/mmt/mmt-studio-core/db/crud"
	amfn26 "github.com/mmt/mmt-studio-core/nf/amf/n26"
)

func (s *Server) registerN26Routes() {
	r := s.Router

	// Combined status — what n26.html's `n26LoadStatus()` consumes.
	r.Get("/api/n26/status", func(w http.ResponseWriter, _ *http.Request) {
		audit := amfn26.Stats()
		mapped := mmen26.Status()
		jsonReply(w, map[string]any{
			"n26_enabled":             true,
			"amf_ue_contexts":         audit["completed"],
			"mme_ue_contexts":         mapped["pending_mapped_contexts"],
			"pending_mapped_contexts": mapped["pending_mapped_contexts"],
			"expired_contexts":        mapped["expired_contexts"],
			"context_ttl_seconds":     mapped["ttl_seconds"],
			"audit":                   audit,
		})
	})

	// 5G → 4G handover. Audit-row vocabulary is fixed by the schema
	// CHECK (db/schemas/domains.go): source_rat ∈ {4G,5G}, status
	// ∈ {initiated, completed, failed}.
	r.Post("/api/n26/handover/5g-to-4g", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI string `json:"imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		ue, err := crud.UEGetByIMSI(d.IMSI)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ue == nil {
			jsonError(w, "UE not found: "+d.IMSI, http.StatusNotFound)
			return
		}
		id, err := amfn26.LogHandover(d.IMSI, amfn26.RAT5G, amfn26.RAT4G,
			amfn26.StatusInitiated)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Push a minimal mapped context to the MME-side cache.
		// KASME / bearers / ueInfo are placeholders here; the live
		// derivation happens in nf/amf/gmm when the SMC completes.
		mmen26.ReceiveContextFromAMF(d.IMSI, []byte{},
			[]map[string]interface{}{},
			map[string]interface{}{"imsi": d.IMSI})
		if _, err := amfn26.MarkCompleted(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// Allocate a synthetic MME-UE-S1AP-ID — operator panel only;
		// real allocation lives at the EPC handler.
		mmeUEID := id
		jsonReply(w, map[string]any{
			"success":         true,
			"audit_id":        id,
			"imsi":            d.IMSI,
			"source_rat":      amfn26.RAT5G,
			"target_rat":      amfn26.RAT4G,
			"mme_ue_s1ap_id":  mmeUEID,
		})
	})

	// 4G → 5G handover.
	r.Post("/api/n26/handover/4g-to-5g", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI string `json:"imsi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		ue, err := crud.UEGetByIMSI(d.IMSI)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if ue == nil {
			jsonError(w, "UE not found: "+d.IMSI, http.StatusNotFound)
			return
		}
		id, err := amfn26.LogHandover(d.IMSI, amfn26.RAT4G, amfn26.RAT5G,
			amfn26.StatusInitiated)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		// MME shim forwards the intent; AMF will derive 5G context
		// server-side via the still-attached S1.
		mmen26.ForwardContextToAMF(d.IMSI)
		if _, err := amfn26.MarkCompleted(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		amfUEID := id
		jsonReply(w, map[string]any{
			"success":         true,
			"audit_id":        id,
			"imsi":            d.IMSI,
			"source_rat":      amfn26.RAT4G,
			"target_rat":      amfn26.RAT5G,
			"amf_ue_ngap_id":  amfUEID,
		})
	})

	// Audit log — most recent first. Returns plain array so the
	// existing GUI handler (`rows.forEach`) consumes it directly.
	r.Get("/api/n26/handovers", func(w http.ResponseWriter, _ *http.Request) {
		list, err := amfn26.List()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, list)
	})

	r.Get("/api/n26/handovers/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		list, err := amfn26.ListForIMSI(chi.URLParam(rq, "imsi"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, list)
	})

	// Mapped-context cache management.
	r.Get("/api/n26/contexts", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, mmen26.Status())
	})

	r.Post("/api/n26/contexts/cleanup", func(w http.ResponseWriter, _ *http.Request) {
		evicted := mmen26.CleanupExpired()
		jsonReply(w, map[string]any{"evicted": evicted})
	})

	r.Post("/api/n26/contexts/{imsi}/expire", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		ctx := mmen26.ConsumeMappedContext(imsi)
		if ctx == nil {
			jsonError(w, "no cached context for imsi", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi})
	})

	// Stats-only endpoint for graphs/dashboards.
	r.Get("/api/n26/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, amfn26.Stats())
	})
}
