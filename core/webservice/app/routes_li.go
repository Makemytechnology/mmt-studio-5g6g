// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_li.go — REST surface for Lawful Intercept (TS 33.126 / TS
// 33.127 / TS 33.128).
//
// Wires `security/li` to /api/li/*. The package owns the warrant
// life-cycle, the IRI/CC capture path, the X1 ADMF→POI provisioning
// surface, the X2/X3 deliverers, and the immutable LI audit log.
// This surface drives `templates/li.html`.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 33.126 — LI requirements (warrant authority, target identity,
//                 scope CHECK).
//   - TS 33.127 §5.2 LI administrative function security — drives the
//                 X-LI-Auth-Token gate (see routes_li_auth.go).
//   - TS 33.127 §6.2 X1 — ADMF → POI provisioning (Provision /
//                 Modify / Deactivate verbs surfaced under /api/li/x1).
//   - TS 33.127 §6.3 X2 — IRI delivery to MDF.
//   - TS 33.127 §6.4 X3 — CC delivery to MDF.
//   - TS 33.128       — Stage-3 events catalogue (IRI / CC structure).
//
// Every route is gated by requireLIAuth — no operator action against
// the warrant / IRI / audit surface can run without the token (or in
// dev-mode with an empty stored secret).
//
// AI hooks (`/ai/correlate`, `/ai/patterns`) return structured stubs;
// they are explicitly out-of-scope for TS 33.126/.127/.128.
package app

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/security/li"
)

func (s *Server) registerLIRoutes() {
	r := s.Router

	// ── Warrants (TS 33.126 §5.x) ─────────────────────────────────
	r.Get("/api/li/warrants", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		status := rq.URL.Query().Get("status")
		rows, err := li.ListWarrants(status)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []map[string]interface{}{}
		}
		jsonReply(w, rows)
	}))

	r.Post("/api/li/warrant", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		var body struct {
			WarrantID    string `json:"warrant_id"`
			Authority    string `json:"authority"`
			CaseRef      string `json:"case_reference"`
			TargetIMSI   string `json:"target_imsi"`
			TargetMSISDN string `json:"target_msisdn"`
			Scope        string `json:"scope"`
			StartTime    string `json:"start_time"`
			EndTime      string `json:"end_time"`
			MDFEndpoint  string `json:"mdf_endpoint"`
			Operator     string `json:"operator"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&body); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		if body.Operator == "" {
			body.Operator = liOperatorFromRequest(rq)
		}
		if err := li.CreateWarrant(body.WarrantID, body.Authority, body.CaseRef,
			body.TargetIMSI, body.TargetMSISDN, body.Scope,
			body.StartTime, body.EndTime, body.MDFEndpoint, body.Operator); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "warrant_id": body.WarrantID})
	}))

	r.Post("/api/li/warrant/{id}/revoke", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		li.RevokeWarrant(id, liOperatorFromRequest(rq))
		jsonReply(w, map[string]any{"ok": true})
	}))

	r.Post("/api/li/warrant/{id}/delete", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		li.DeleteWarrant(id, liOperatorFromRequest(rq))
		jsonReply(w, map[string]any{"ok": true})
	}))

	// ── IRI / CC (TS 33.127 §6.3 / §6.4 + TS 33.128) ──────────────
	r.Get("/api/li/warrant/{id}/iri", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		records, err := li.GetIRIEvents(id, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if records == nil {
			records = []map[string]interface{}{}
		}
		jsonReply(w, map[string]any{"records": records})
	}))

	r.Post("/api/li/warrant/{id}/mark-delivered", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		var body struct {
			MaxID int64 `json:"max_id"`
		}
		_ = json.NewDecoder(rq.Body).Decode(&body)
		if body.MaxID <= 0 {
			body.MaxID = 1 << 62
		}
		li.MarkDelivered(id, body.MaxID)
		jsonReply(w, map[string]any{"ok": true})
	}))

	r.Get("/api/li/cc-sessions", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		rows, err := li.GetActiveCCSessions(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []map[string]interface{}{}
		}
		jsonReply(w, rows)
	}))

	// ── Stats / dashboard ────────────────────────────────────────
	r.Get("/api/li/stats", requireLIAuth(func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, li.Status())
	}))

	// ── X1 ADMF → POI provisioning (TS 33.127 §6.2) ──────────────
	// Same data primitives as /api/li/warrant{,/{id}/revoke}, but the
	// route prefix lets operator runbooks and tester scripts speak
	// in spec terms rather than HTTP-route ones, and each call emits
	// an x1_* audit row in addition to the underlying warrant_* row
	// so the trail distinguishes ADMF-driven calls from operator-
	// panel calls.
	r.Post("/api/li/x1/provision", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		var in li.X1ProvisionInput
		if err := json.NewDecoder(rq.Body).Decode(&in); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		if in.Operator == "" {
			in.Operator = liOperatorFromRequest(rq)
		}
		if err := li.X1Provision(in); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "warrant_id": in.WarrantID})
	}))

	r.Post("/api/li/x1/modify", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		var in li.X1ModifyInput
		if err := json.NewDecoder(rq.Body).Decode(&in); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		if in.Operator == "" {
			in.Operator = liOperatorFromRequest(rq)
		}
		if err := li.X1Modify(in); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "warrant_id": in.WarrantID})
	}))

	r.Post("/api/li/x1/deactivate/{id}", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		id := chi.URLParam(rq, "id")
		li.X1Deactivate(id, liOperatorFromRequest(rq))
		jsonReply(w, map[string]any{"ok": true})
	}))

	// ── Audit log (TS 33.127 §5.2 attributability) ───────────────
	r.Get("/api/li/audit", requireLIAuth(func(w http.ResponseWriter, rq *http.Request) {
		warrantID := rq.URL.Query().Get("warrant_id")
		limit, _ := strconv.Atoi(rq.URL.Query().Get("limit"))
		rows, err := li.GetAuditLog(warrantID, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rows == nil {
			rows = []map[string]interface{}{}
		}
		jsonReply(w, rows)
	}))

	// ── AI hooks (out of TS 33.128 scope; structured stubs) ──────
	r.Get("/api/li/warrant/{ref}/ai/correlate", requireLIAuth(func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"summary": ""})
	}))

	r.Get("/api/li/warrant/{ref}/ai/patterns", requireLIAuth(func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"patterns": []any{}})
	}))
}
