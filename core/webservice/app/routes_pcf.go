// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_pcf.go — REST surface for the Policy Control Function (PCF).
//
// Wires `nf/pcf` and `nf/pcf/smpolicy` to /api/pcf/*. The packages
// own:
//
//   - PCC rule definition + in-memory manager state (TS 23.503 §6.3,
//     TS 29.512 §5.6.2.6).
//   - SM Policy Association lifecycle Create/Update/Delete (TS 29.512
//     §4.2.2 / §4.2.4 / §4.2.5).
//
// Before this surface landed, the PCF had no operator REST entry
// points at all — every panel call had to read raw DB rows, and the
// SM-Policy machinery was in-process-only. These routes expose the
// same shapes the SBI port will eventually emit so the panel and
// tester can drive the lifecycle end-to-end today.
//
// All response shapes are `{ok: true, ...}` envelopes.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/pcf"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
)

func (s *Server) registerPCFRoutes() {
	r := s.Router

	// ── Stats / dashboard ────────────────────────────────────────
	r.Get("/api/pcf/stats", func(w http.ResponseWriter, _ *http.Request) {
		out := map[string]any{"ok": true}
		for k, v := range pcf.Stats() {
			out[k] = v
		}
		// Merge SM-policy registry counts under an explicit key so
		// the panel doesn't conflate PCC-rule counts with associations.
		out["sm_policy"] = smpolicy.Stats()
		jsonReply(w, out)
	})

	// ── PCC rule readouts (TS 23.503 §6.3 / TS 29.512 §5.6.2.6) ──
	r.Get("/api/pcf/pcc-rules", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		dnn := rq.URL.Query().Get("dnn")
		rules := pcf.ListPccRules(imsi, dnn)
		jsonReply(w, map[string]any{
			"ok":    true,
			"rules": rules,
			"count": len(rules),
		})
	})

	// "What policy would the PCF return for this triple?" — non-
	// invasive preview that doesn't open an SM Policy Association.
	r.Get("/api/pcf/policy-preview", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		dnn := rq.URL.Query().Get("dnn")
		var sst uint8
		if v := rq.URL.Query().Get("sst"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n >= 0 && n <= 255 {
				sst = uint8(n)
			}
		}
		if sst == 0 {
			sst = 1 // default eMBB
		}
		set := pcf.PreviewPolicy(imsi, dnn, sst)
		jsonReply(w, map[string]any{
			"ok":              true,
			"imsi":            imsi,
			"dnn":             dnn,
			"sst":             sst,
			"rules":           set.Rules,
			"default_qfi":     set.DefaultQFI,
			"charging_method": set.ChargingMethod,
		})
	})

	// ── SM Policy Associations (TS 29.512 §4.2) ──────────────────

	// List active associations.
	r.Get("/api/pcf/sm-policy", func(w http.ResponseWriter, _ *http.Request) {
		assocs := smpolicy.ListAssociations()
		jsonReply(w, map[string]any{
			"ok":           true,
			"associations": assocs,
			"count":        len(assocs),
		})
	})

	// Read one association by IMSI + PDU session id.
	r.Get("/api/pcf/sm-policy/{imsi}/{pdu_id}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		pid, err := strconv.Atoi(chi.URLParam(rq, "pdu_id"))
		if err != nil || pid < 1 || pid > 255 {
			jsonError(w, "pdu_id must be 1..255", http.StatusBadRequest)
			return
		}
		view := smpolicy.GetAssociationView(imsi, uint8(pid))
		if view == nil {
			jsonError(w, "association not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "association": view})
	})

	// Create — TS 29.512 §4.2.2. The body mirrors §5.6.2.2
	// SmPolicyContextData (the attributes this in-process port uses).
	r.Post("/api/pcf/sm-policy", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			SUPI           string `json:"supi"`
			IMSI           string `json:"imsi"` // accept either
			PDUSessionID   uint8  `json:"pdu_session_id"`
			DNN            string `json:"dnn"`
			SST            uint8  `json:"sst"`
			SD             string `json:"sd"`
			PDUSessionType uint8  `json:"pdu_session_type"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		supi := d.SUPI
		if supi == "" && d.IMSI != "" {
			supi = "imsi-" + d.IMSI
		}
		if supi == "" {
			jsonError(w, "supi or imsi required", http.StatusBadRequest)
			return
		}
		if d.PDUSessionID < 1 || d.PDUSessionID > 15 {
			jsonError(w, "pdu_session_id must be 1..15 (TS 23.501 §5.7.1.4)",
				http.StatusBadRequest)
			return
		}
		if d.DNN == "" {
			jsonError(w, "dnn required", http.StatusBadRequest)
			return
		}
		if d.SST == 0 {
			d.SST = 1
		}
		decision, err := smpolicy.Create(smpolicy.SmPolicyContextData{
			SUPI:           supi,
			PDUSessionID:   d.PDUSessionID,
			DNN:            d.DNN,
			SST:            d.SST,
			SD:             d.SD,
			PDUSessionType: d.PDUSessionType,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		ruleNames := make([]string, 0, len(decision.PccRules))
		for _, r := range decision.PccRules {
			ruleNames = append(ruleNames, r.ServiceName)
		}
		jsonReply(w, map[string]any{
			"ok":               true,
			"ctx_ref":          decision.SmPolicyCtxRef,
			"default_qfi":      decision.DefaultQFI,
			"default_5qi":      decision.Default5QI,
			"charging_method":  decision.ChargingMethod,
			"session_ambr_ul":  decision.SessionAMBRUL,
			"session_ambr_dl":  decision.SessionAMBRDL,
			"rules":            decision.PccRules,
			"rule_names":       ruleNames,
			"rule_count":       len(decision.PccRules),
			"revalidation_at":  decision.RevalidationTime,
		})
	})

	// Update — TS 29.512 §4.2.4. Triggers per §4.2.3.5.
	r.Patch("/api/pcf/sm-policy/{imsi}/{pdu_id}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		pid, err := strconv.Atoi(chi.URLParam(rq, "pdu_id"))
		if err != nil || pid < 1 || pid > 255 {
			jsonError(w, "pdu_id must be 1..255", http.StatusBadRequest)
			return
		}
		var d struct {
			Triggers    []string                `json:"triggers"`
			RuleReports []smpolicy.RuleReport   `json:"rule_reports"`
		}
		if rq.ContentLength > 0 {
			if !decodeJSON(w, rq, &d) {
				return
			}
		}
		k := smfsm.Key{IMSI: imsi, PDUSessionID: uint8(pid)}
		decision, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
			Triggers:    d.Triggers,
			RuleReports: d.RuleReports,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{
			"ok":              true,
			"ctx_ref":         decision.SmPolicyCtxRef,
			"rule_count":      len(decision.PccRules),
			"revalidation_at": decision.RevalidationTime,
		})
	})

	// Delete — TS 29.512 §4.2.5. Idempotent: the package's Delete
	// returns Terminated=true even when the key is unknown, so we
	// surface it as 200 either way.
	r.Delete("/api/pcf/sm-policy/{imsi}/{pdu_id}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		pid, err := strconv.Atoi(chi.URLParam(rq, "pdu_id"))
		if err != nil || pid < 1 || pid > 255 {
			jsonError(w, "pdu_id must be 1..255", http.StatusBadRequest)
			return
		}
		k := smfsm.Key{IMSI: imsi, PDUSessionID: uint8(pid)}
		st, err := smpolicy.Delete(k)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "terminated": st.Terminated})
	})

	// ── V2X policy (TS 23.287) — operator readout ────────────────
	r.Get("/api/pcf/v2x/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		assoc := pcf.GetPC5QoSForGnb(imsi)
		if assoc == nil {
			jsonError(w, "no v2x policy association", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "association": assoc})
	})
}
