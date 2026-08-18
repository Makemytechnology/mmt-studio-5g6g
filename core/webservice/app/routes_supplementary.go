// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_supplementary.go — REST surface for IMS Supplementary
// Services (TS 24.604/611/615/607/608 + TS 22.030).
//
// Wires `services/supplementary` to /api/supplementary/*. The
// package owns the per-IMSI activation/deactivation/interrogation
// of the call-forwarding, call-barring, call-waiting, and
// originating/terminating identification services.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 24.604 §4.5.1 — Communication Forwarding (CFU/CFB/CFNRy/CFNRc)
//                        activation/deactivation.
//   - TS 24.611 §4.5.1 — Communication Barring (BAOC/BAOIC/BAIC).
//   - TS 24.615 §4.5   — Communication Waiting.
//   - TS 24.607 §4.5   — Originating Identification Presentation/Restriction.
//   - TS 24.608 §4.5   — Terminating Identification Presentation/Restriction.
//   - TS 22.030 §6.5.2 — MMI procedure strings (UE keypad).
//   - TS 22.030 Annex B Table B.1 — Service-code catalogue.
//
// All response shapes are `{ok: true, ...}` envelopes.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/services/supplementary"
)

func (s *Server) registerSupplementaryRoutes() {
	r := s.Router

	// ── Status / dashboard ────────────────────────────────────────
	r.Get("/api/supplementary/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok": true, "status": supplementary.Status(),
		})
	})

	// ── Per-subscriber service list ──────────────────────────────
	// `imsi` mandatory; without it we'd be dumping the whole table.
	r.Get("/api/supplementary/services", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi query param required",
				http.StatusBadRequest)
			return
		}
		list, err := supplementary.ListByIMSI(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []supplementary.ServiceRecord{}
		}
		jsonReply(w, map[string]any{
			"ok":       true,
			"imsi":     imsi,
			"services": list,
			"count":    len(list),
		})
	})

	// ── Activate (TS 24.604/611/615/607/608 §4.5.1) ──────────────
	r.Post("/api/supplementary/activate", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI             string `json:"imsi"`
			ServiceType      string `json:"service_type"`
			ForwardingNumber string `json:"forwarding_number"`
			BarringPassword  string `json:"barring_password"`
			ConfigJSON       string `json:"config_json"`
			NoReplyTimer     int    `json:"no_reply_timer"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		rec, errMsg := supplementary.Activate(d.IMSI, d.ServiceType,
			d.ForwardingNumber, d.BarringPassword, d.ConfigJSON,
			d.NoReplyTimer)
		if errMsg != "" {
			jsonError(w, errMsg, http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"active": true,
			"status": "active",
			"record": rec,
		})
	})

	// ── Deactivate ───────────────────────────────────────────────
	r.Post("/api/supplementary/deactivate", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string `json:"imsi"`
			ServiceType string `json:"service_type"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		rec, errMsg := supplementary.Deactivate(d.IMSI, d.ServiceType)
		if errMsg != "" {
			jsonError(w, errMsg, http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"active": false,
			"status": "inactive",
			"record": rec,
		})
	})

	// ── Interrogate (TS 24.604 §4.5.1b et al.) ───────────────────
	r.Get("/api/supplementary/interrogate", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		st := rq.URL.Query().Get("service_type")
		if imsi == "" || st == "" {
			jsonError(w, "imsi and service_type required",
				http.StatusBadRequest)
			return
		}
		rec, exists := supplementary.Interrogate(imsi, st)
		active := exists && rec != nil && rec.Active == 1
		status := "inactive"
		if active {
			status = "active"
		}
		jsonReply(w, map[string]any{
			"ok":     true,
			"exists": exists,
			"active": active,
			"status": status,
			"record": rec,
		})
	})

	// ── Bulk apply (panel save-all button) ───────────────────────
	r.Post("/api/supplementary/bulk", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI     string                   `json:"imsi"`
			Services []map[string]interface{} `json:"services"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IMSI == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		out := supplementary.BulkSet(d.IMSI, d.Services)
		// BulkSet already returns {ok, error?, results}; surface as-is.
		jsonReply(w, out)
	})

	// ── Delete all services for a subscriber ─────────────────────
	r.Delete("/api/supplementary/services", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi == "" {
			jsonError(w, "imsi required", http.StatusBadRequest)
			return
		}
		n, err := supplementary.DeleteAll(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok": true, "imsi": imsi, "deleted": n,
		})
	})

	// ── MMI string parser (TS 22.030 §6.5.2 + Annex B Table B.1) ─
	// The UE keypad path: "*21*+15551234567#" → {procedure:
	// "Activation", service_code: "21", service_name: "CFU", sia:
	// "+15551234567"}. The panel uses this for live "what does this
	// MMI string do?" feedback before the actual XCAP PUT.
	r.Post("/api/supplementary/mmi", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			MMI string `json:"mmi"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.MMI == "" {
			jsonError(w, "mmi required", http.StatusBadRequest)
			return
		}
		req, err := supplementary.ParseMMI(d.MMI)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":           true,
			"procedure":    req.Procedure.String(),
			"service_code": req.ServiceCode,
			"service_name": req.ServiceName,
			"sia":          req.SIA,
			"sib":          req.SIB,
			"sic":          req.SIC,
			"raw":          req.Raw,
		})
	})
}
