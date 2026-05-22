// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_ranging.go — REST surface for Ranging-based services and
// 5G Sidelink Positioning over PC5 (TS 23.586).
//
// This is a **positioning** capability, not an edge-compute one. It
// shares the operator panel's "Positioning" group with the network-
// side LCS surface (routes_positioning.go → nf/lmf, nf/gmlc) but
// the two implement different positioning architectures:
//
//   - nf/lmf + nf/gmlc           TS 23.273 — network locates UE
//   - positioning/ranging        TS 23.586 — UE locates UE on PC5
//
// They share no on-wire procedures; the link is conceptual.
//
// Routes:
//
//   /api/ranging/sessions[/{id}]                  — TS 23.586 §6.2
//                                                    Initiate / read /
//                                                    cancel / delete
//   /api/ranging/anchors[/{id}]                   — §5.2 SL Reference UE
//                                                    placement
//   /api/ranging/position                         — §6.8 operator-side
//                                                    position fix from
//                                                    a measurement set
//   /api/ranging/privacy[/{imsi}]                 — §5.1 authorisation
//                                                    consent gate
//   /api/ranging/status                           — aggregate counters
package app

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/positioning/ranging"
)

func (s *Server) registerRangingRoutes() {
	r := s.Router

	// ── Sessions (TS 23.586 §6.2) ────────────────────────────────
	r.Get("/api/ranging/sessions", func(w http.ResponseWriter, rq *http.Request) {
		list, err := ranging.ListSessions(
			rq.URL.Query().Get("imsi"),
			rq.URL.Query().Get("status"),
		)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []ranging.Session{}
		}
		jsonReply(w, list)
	})
	r.Get("/api/ranging/sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		s, err := ranging.GetSession(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if s == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, s)
	})
	r.Post("/api/ranging/sessions", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			SourceIMSI string `json:"source_imsi"`
			TargetIMSI string `json:"target_imsi"`
			Method     string `json:"method"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		out, err := ranging.InitiateRanging(b.SourceIMSI, b.TargetIMSI, b.Method)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, out)
	})
	r.Post("/api/ranging/sessions/{id}/cancel", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := ranging.CancelSession(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Delete("/api/ranging/sessions/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := ranging.DeleteSession(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Anchors (TS 23.586 §5.2 SL Reference UE) ─────────────────
	r.Get("/api/ranging/anchors", func(w http.ResponseWriter, rq *http.Request) {
		list, err := ranging.ListAnchors()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []ranging.Anchor{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/ranging/anchors", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			IMSI       string  `json:"imsi"`
			Latitude   float64 `json:"latitude"`
			Longitude  float64 `json:"longitude"`
			Altitude   float64 `json:"altitude"`
			AnchorType string  `json:"anchor_type"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		id, err := ranging.CreateAnchor(b.IMSI, b.Latitude, b.Longitude, b.Altitude, b.AnchorType)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"id": id, "ok": true})
	})
	r.Get("/api/ranging/anchors/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		a, err := ranging.GetAnchor(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if a == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, a)
	})
	r.Delete("/api/ranging/anchors/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, _ := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err := ranging.DeleteAnchor(id); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Position fix (TS 23.586 §6.8 operator math) ──────────────
	r.Post("/api/ranging/position", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			TargetIMSI string `json:"target_imsi"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		fix, err := ranging.EstimatePosition(b.TargetIMSI)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, fix)
	})

	// ── Privacy (TS 23.586 §5.1 authorisation gate) ──────────────
	r.Get("/api/ranging/privacy/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		p, err := ranging.GetPrivacy(chi.URLParam(rq, "imsi"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p == nil {
			jsonError(w, "not found", http.StatusNotFound)
			return
		}
		jsonReply(w, p)
	})
	r.Get("/api/ranging/privacy", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		if imsi != "" {
			p, err := ranging.GetPrivacy(imsi)
			if err != nil {
				jsonError(w, err.Error(), http.StatusInternalServerError)
				return
			}
			if p == nil {
				jsonError(w, "not found", http.StatusNotFound)
				return
			}
			jsonReply(w, p)
			return
		}
		list, err := ranging.ListPrivacy()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []ranging.PrivacyEntry{}
		}
		jsonReply(w, list)
	})
	r.Post("/api/ranging/privacy", func(w http.ResponseWriter, rq *http.Request) {
		var b struct {
			IMSI            string `json:"imsi"`
			Policy          string `json:"policy"`
			AllowedContacts string `json:"allowed_contacts"`
		}
		if err := json.NewDecoder(rq.Body).Decode(&b); err != nil {
			jsonError(w, "invalid json", http.StatusBadRequest)
			return
		}
		var contacts *string
		if b.AllowedContacts != "" {
			contacts = &b.AllowedContacts
		}
		if err := ranging.SetPrivacy(b.IMSI, b.Policy, contacts); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})
	r.Delete("/api/ranging/privacy/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		if err := ranging.DeletePrivacy(imsi); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/ranging/status", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, ranging.Status())
	})
}
