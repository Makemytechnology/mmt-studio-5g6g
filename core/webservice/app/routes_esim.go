// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_esim.go — REST surface for the eSIM panel + SM-DP+
// operator side.
//
// Wires `services/esim` (and `services/esim/smdp`) to /api/esim/*
// and /api/smdp/*. Before this surface landed, /api/esim/* was
// three `[]` stubs in routes_misc.go and two `{ok: true}` stubs in
// routes_nsaas.go; the SM-DP+ ES9+ helpers were unreachable from
// the tester.
//
// Spec anchors (GSMA SGP.* identifiers are informative — speccheck
// only matches TS/RFC):
//
//   - GSMA SGP.22 §3.0/§5.6 — ES2+ DownloadOrder / ConfirmOrder
//                             (collapsed into POST /api/esim/order).
//   - GSMA SGP.22 §3.1.2   — ES9+ InitiateAuthentication.
//   - GSMA SGP.22 §3.1.3   — ES9+ AuthenticateClient.
//   - GSMA SGP.22 §3.3.x   — ES9+ GetBoundProfilePackage.
//   - GSMA SGP.22 §3.5     — ES9+ HandleNotification.
//   - GSMA SGP.22 §4.1     — Activation Code format.
//   - TS 31.102 §4.2       — USIM EFs populated by the BPP.
//
// All response shapes are `{ok: true, ...}` envelopes.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/services/esim"
	"github.com/mmt/mmt-studio-core/services/esim/smdp"
)

func (s *Server) registerESIMRoutes() {
	r := s.Router

	// ── Dashboard ────────────────────────────────────────────────
	r.Get("/api/esim/stats", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "stats": esim.Stats()})
	})

	// ── Profiles ─────────────────────────────────────────────────
	r.Get("/api/esim/profiles", func(w http.ResponseWriter, rq *http.Request) {
		state := rq.URL.Query().Get("state")
		imsi := rq.URL.Query().Get("imsi")
		var (
			profiles []esim.Profile
			err      error
		)
		if imsi != "" {
			profiles, err = esim.GetProfilesForIMSI(imsi)
		} else {
			profiles, err = esim.ListProfiles(state)
		}
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if profiles == nil {
			profiles = []esim.Profile{}
		}
		jsonReply(w, map[string]any{
			"ok":       true,
			"profiles": profiles,
			"count":    len(profiles),
		})
	})

	r.Get("/api/esim/profile/{iccid}", func(w http.ResponseWriter, rq *http.Request) {
		p, err := esim.GetProfileByICCID(chi.URLParam(rq, "iccid"))
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if p == nil {
			jsonError(w, "profile not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "profile": p})
	})

	// Order — operator-side ES2+ DownloadOrder + ConfirmOrder.
	r.Post("/api/esim/order", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI        string `json:"imsi"`
			ProfileName string `json:"profile_name"`
			SMDPAddress string `json:"smdp_address"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		order, err := esim.OrderProfile(d.IMSI, d.ProfileName, d.SMDPAddress)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{
			"ok":      true,
			"profile": order,
			"qr_data": map[string]string{
				"content": order.ActivationCode,
				"format":  "SGP.22-v2.3.1",
			},
		})
	})

	// State transition — enable / disable / delete via the SGP.22
	// lifecycle (validated by SetProfileState).
	r.Patch("/api/esim/profile/{iccid}/state", func(w http.ResponseWriter, rq *http.Request) {
		iccid := chi.URLParam(rq, "iccid")
		var d struct {
			State string `json:"state"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if err := esim.SetProfileState(iccid, d.State); err != nil {
			code := http.StatusBadRequest
			if err.Error() == "profile not found" {
				code = http.StatusNotFound
			}
			jsonError(w, err.Error(), code)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "iccid": iccid, "state": d.State})
	})

	// Release (allow-listed: available / reserved only).
	r.Post("/api/esim/profile/{iccid}/release", func(w http.ResponseWriter, rq *http.Request) {
		iccid := chi.URLParam(rq, "iccid")
		if err := esim.ReleaseProfile(iccid); err != nil {
			code := http.StatusBadRequest
			if err.Error() == "profile not found" {
				code = http.StatusNotFound
			}
			jsonError(w, err.Error(), code)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "iccid": iccid})
	})

	// ── eUICCs ───────────────────────────────────────────────────
	r.Get("/api/esim/euiccs", func(w http.ResponseWriter, _ *http.Request) {
		euiccs, err := esim.ListEUICCs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if euiccs == nil {
			euiccs = []esim.EUICC{}
		}
		jsonReply(w, map[string]any{"ok": true, "euiccs": euiccs, "count": len(euiccs)})
	})

	r.Post("/api/esim/euiccs", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			EID        string `json:"eid"`
			DeviceInfo string `json:"device_info"`
			LPAVersion string `json:"lpa_version"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.EID == "" {
			jsonError(w, "eid required", http.StatusBadRequest)
			return
		}
		id, err := esim.RegisterEUICC(d.EID, d.DeviceInfo, d.LPAVersion)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id, "eid": d.EID})
	})

	r.Delete("/api/esim/euiccs/{eid}", func(w http.ResponseWriter, rq *http.Request) {
		eid := chi.URLParam(rq, "eid")
		if err := esim.DeleteEUICC(eid); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "eid": eid})
	})

	// ── Notifications (SGP.22 §3.5 audit log) ────────────────────
	r.Get("/api/esim/notifications", func(w http.ResponseWriter, rq *http.Request) {
		iccid := rq.URL.Query().Get("iccid")
		limit := 50
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		notes, err := esim.ListNotifications(iccid, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if notes == nil {
			notes = []esim.Notification{}
		}
		jsonReply(w, map[string]any{
			"ok":            true,
			"notifications": notes,
			"count":         len(notes),
		})
	})

	// ── SM-DP+ ES9+ Mutual-Auth path (GSMA SGP.22 §3.1) ──────────
	// These routes drive the same in-process functions the LPA-side
	// path uses today; the tester exercises them without an eUICC
	// signature (TODO §6 ECKA(ECDSA) verification).

	r.Post("/api/smdp/initiate-auth", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TransactionID  string `json:"transaction_id"`
			EUICCChallenge string `json:"euicc_challenge"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.TransactionID == "" {
			jsonError(w, "transaction_id required", http.StatusBadRequest)
			return
		}
		res := smdp.Server.InitiateAuthentication(d.TransactionID, d.EUICCChallenge)
		out := map[string]any{"ok": true}
		for k, v := range res {
			out[k] = v
		}
		jsonReply(w, out)
	})

	r.Post("/api/smdp/authenticate-client", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TransactionID string                 `json:"transaction_id"`
			EUICCSigned1  map[string]interface{} `json:"euicc_signed1"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		res := smdp.Server.AuthenticateClient(d.TransactionID, d.EUICCSigned1)
		if e, ok := res["error"]; ok {
			jsonError(w, asString(e), http.StatusBadRequest)
			return
		}
		out := map[string]any{"ok": true}
		for k, v := range res {
			out[k] = v
		}
		jsonReply(w, out)
	})

	r.Post("/api/smdp/get-bpp", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TransactionID string `json:"transaction_id"`
			MatchingID    string `json:"matching_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		res := smdp.Server.GetBoundProfilePackage(d.TransactionID, d.MatchingID)
		if e, ok := res["error"]; ok {
			jsonError(w, asString(e), http.StatusBadRequest)
			return
		}
		out := map[string]any{"ok": true}
		for k, v := range res {
			out[k] = v
		}
		jsonReply(w, out)
	})

	r.Post("/api/smdp/notify", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			ICCID     string `json:"iccid"`
			EID       string `json:"eid"`
			EventType string `json:"event_type"`
			SeqNumber int    `json:"seq_number"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.ICCID == "" || d.EventType == "" {
			jsonError(w, "iccid and event_type required", http.StatusBadRequest)
			return
		}
		smdp.Server.HandleNotification(d.ICCID, d.EID, d.EventType, d.SeqNumber)
		jsonReply(w, map[string]any{"ok": true})
	})

	r.Get("/api/smdp/profile/{iccid}/status", func(w http.ResponseWriter, rq *http.Request) {
		iccid := chi.URLParam(rq, "iccid")
		res := smdp.Server.GetProfileStatus(iccid)
		if res == nil {
			jsonError(w, "profile not found", http.StatusNotFound)
			return
		}
		out := map[string]any{"ok": true}
		for k, v := range res {
			out[k] = v
		}
		jsonReply(w, out)
	})
}

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
