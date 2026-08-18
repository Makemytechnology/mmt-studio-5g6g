// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_ursp.go — REST surface for UE Route Selection Policy (URSP).
//
// Wires `nf/pcf/ursp` to /api/ursp/*. The package owns the rule
// store, the precedence-ordered evaluator, and the URSP IE encoder
// used by the AMF/SMF on the NAS path.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.503 §6.6     — UE Route Selection Policy (umbrella).
//   - TS 23.503 §6.6.2.1 — Traffic Descriptor components
//                          (app_id / ip_3tuple / dnn / fqdn / conn_cap / domain).
//   - TS 23.503 §6.6.2.2 — Route Selection Descriptor components
//                          (S-NSSAI, DNN, PDU session type, access type, …).
//   - TS 24.526 Table 5.2.1 — Encoded TD/RSD type IDs.
//   - TS 24.501 §5.4.4   — UE Configuration Update / URSP delivery
//                          on the NAS path.
//
// Before this surface landed, /api/ursp/* was a 6-line stub block in
// routes_nsaas.go returning empty objects; the package's CRUD,
// evaluator, and encoder were unreachable from the panel and tester.
//
// All response shapes are `{ok: true, ...}` envelopes.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/nf/pcf/ursp"
)

func (s *Server) registerURSPRoutes() {
	r := s.Router

	// ── Status / dashboard ───────────────────────────────────────
	r.Get("/api/ursp/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "status": ursp.Status()})
	})

	// ── Rule CRUD (TS 23.503 §6.6) ───────────────────────────────

	// List rules. Empty imsi → all rules; with imsi → per-UE +
	// global rules merged by precedence (the package's List() does
	// the merge so a UE sees its UE-specific rules above globals).
	r.Get("/api/ursp/rules", func(w http.ResponseWriter, rq *http.Request) {
		imsi := rq.URL.Query().Get("imsi")
		rules, err := ursp.List(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rules == nil {
			rules = []ursp.Rule{}
		}
		jsonReply(w, map[string]any{
			"ok":    true,
			"rules": rules,
			"count": len(rules),
		})
	})

	// Read one rule with its TDs + RSDs attached.
	r.Get("/api/ursp/rules/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		rule, err := ursp.Get(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if rule == nil {
			jsonError(w, "rule not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "rule": rule})
	})

	// Create rule + descriptors atomically. Accepts two body shapes:
	//
	//   plural  (panel) — { traffic_descriptors: [...], route_descriptors: [...] }
	//   singular (TS 23.503 §6.6.2.1 wire-shape) —
	//            { traffic_descriptor: { dnn, app_id, ip_3tuple, … },
	//              route_selection_descriptor: {
	//                precedence, component: { sst, sd, dnn, … } } }
	//
	// Per §6.6.2.1.2 a Traffic Descriptor is the logical AND of its
	// component fields; the singular object expands to one
	// TrafficDescriptor row per non-empty field. Per §6.6.2.2 the
	// Route Selection Descriptor's nested `component` carries the
	// S-NSSAI / DNN / PDU-Session-Type / Access-Type IEs.
	r.Post("/api/ursp/rules", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI               string                   `json:"imsi"`
			Precedence         int                      `json:"precedence"`
			Description        string                   `json:"description"`
			Enabled            int                      `json:"enabled"`
			TrafficDescriptors []ursp.TrafficDescriptor `json:"traffic_descriptors"`
			RouteDescriptors   []ursp.RouteDescriptor   `json:"route_descriptors"`
			// Singular shapes per TS 23.503 §6.6.2.1.
			TrafficDescriptor map[string]any `json:"traffic_descriptor"`
			RouteSelDesc      map[string]any `json:"route_selection_descriptor"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if len(d.TrafficDescriptors) == 0 && d.TrafficDescriptor != nil {
			d.TrafficDescriptors = expandTrafficDescriptor(d.TrafficDescriptor)
		}
		if len(d.RouteDescriptors) == 0 && d.RouteSelDesc != nil {
			d.RouteDescriptors = expandRouteSelectionDescriptor(d.RouteSelDesc)
		}
		if d.Enabled == 0 {
			// Tester / panel rarely include this field; an enabled=0
			// rule is silently invisible to EvaluateURSP, so default
			// to enabled on create.
			d.Enabled = 1
		}
		id, err := ursp.CreateRule(ursp.CreateInput{
			IMSI:               d.IMSI,
			Precedence:         d.Precedence,
			Description:        d.Description,
			Enabled:            d.Enabled,
			TrafficDescriptors: d.TrafficDescriptors,
			RouteDescriptors:   d.RouteDescriptors,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id, "rule_id": id})
	})

	// Sparse update — allow-listed columns only (precedence,
	// description, enabled, imsi). 404 on unknown id.
	r.Patch("/api/ursp/rules/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		var patch map[string]any
		if !decodeJSON(w, rq, &patch) {
			return
		}
		ok, err := ursp.UpdateRule(id, patch)
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		if !ok {
			jsonError(w, "rule not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// Delete rule (FK CASCADE removes TDs + RSDs).
	r.Delete("/api/ursp/rules/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		ok, err := ursp.DeleteRule(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "rule not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "id": id})
	})

	// ── URSP IE delivery (TS 24.501 §5.4.4) ─────────────────────

	// Build the encoded URSP IE for a single rule — used by the
	// panel's "push to UE" button and by the tester's encode TC.
	r.Post("/api/ursp/rules/{id}/push", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		ie, err := ursp.BuildURSPIEForRule(id)
		if err != nil {
			jsonError(w, err.Error(), http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "ursp_ie": ie})
	})

	// Build the encoded URSP IE for a UE (global + UE-specific
	// rules merged). GET because it's idempotent.
	r.Get("/api/ursp/ie/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		imsi := chi.URLParam(rq, "imsi")
		ie, err := ursp.BuildURSPIEForUE(imsi)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "imsi": imsi, "ursp_ie": ie})
	})

	// ── Evaluator (TS 23.503 §6.6 — first-match-by-precedence) ──
	// Body accepts the panel `{imsi, traffic: {...}}` shape and the
	// flat `{imsi, app_id, dnn, ...}` shape — top-level fields fill
	// in when `traffic` omits them, so callers can pick whichever
	// is convenient.
	r.Post("/api/ursp/evaluate", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IMSI    string `json:"imsi"`
			Traffic struct {
				AppID    string `json:"app_id"`
				DNN      string `json:"dnn"`
				FQDN     string `json:"fqdn"`
				DstIP    string `json:"dst_ip"`
				DstPort  string `json:"dst_port"`
				Protocol string `json:"protocol"`
				ConnCap  string `json:"conn_cap"`
				Domain   string `json:"domain"`
			} `json:"traffic"`
			// Flat aliases — populated when the caller omits `traffic`.
			AppID    string `json:"app_id"`
			DNN      string `json:"dnn"`
			FQDN     string `json:"fqdn"`
			DstIP    string `json:"dst_ip"`
			DstPort  string `json:"dst_port"`
			Protocol string `json:"protocol"`
			ConnCap  string `json:"conn_cap"`
			Domain   string `json:"domain"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		coalesce := func(envelope, flat string) string {
			if envelope != "" {
				return envelope
			}
			return flat
		}
		res, err := ursp.EvaluateURSP(d.IMSI, ursp.TrafficInfo{
			AppID:    coalesce(d.Traffic.AppID, d.AppID),
			DNN:      coalesce(d.Traffic.DNN, d.DNN),
			FQDN:     coalesce(d.Traffic.FQDN, d.FQDN),
			DstIP:    coalesce(d.Traffic.DstIP, d.DstIP),
			DstPort:  coalesce(d.Traffic.DstPort, d.DstPort),
			Protocol: coalesce(d.Traffic.Protocol, d.Protocol),
			ConnCap:  coalesce(d.Traffic.ConnCap, d.ConnCap),
			Domain:   coalesce(d.Traffic.Domain, d.Domain),
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out := map[string]any{"ok": true}
		if res == nil {
			out["matched"] = false
			out["matched_rule"] = nil
			out["route_descriptor"] = nil
		} else {
			out["matched"] = true
			out["matched_rule"] = map[string]any{
				"rule_id":     res.RuleID,
				"precedence":  res.RulePrecedence,
				"description": res.RuleDescription,
			}
			out["route_descriptor"] = res.RouteDescriptor
		}
		jsonReply(w, out)
	})
}

// expandTrafficDescriptor turns the TS 23.503 §6.6.2.1 singular
// `traffic_descriptor` object — which is the logical AND of one
// or more component fields — into the per-component TD rows the
// rule store keeps. Recognised component keys map 1:1 to the
// match_type enum in nf/pcf/ursp.TrafficDescriptor.
func expandTrafficDescriptor(td map[string]any) []ursp.TrafficDescriptor {
	var out []ursp.TrafficDescriptor
	add := func(matchType, value string) {
		if value == "" {
			return
		}
		out = append(out, ursp.TrafficDescriptor{
			MatchType:  matchType,
			MatchValue: value,
		})
	}
	if s, _ := td["app_id"].(string); s != "" {
		add("app_id", s)
	}
	if s, _ := td["ip_3tuple"].(string); s != "" {
		add("ip_3tuple", s)
	}
	if s, _ := td["dnn"].(string); s != "" {
		add("dnn", s)
	}
	if s, _ := td["fqdn"].(string); s != "" {
		add("fqdn", s)
	}
	if s, _ := td["conn_cap"].(string); s != "" {
		add("conn_cap", s)
	}
	if s, _ := td["domain"].(string); s != "" {
		add("domain", s)
	}
	// A `match_all: true` rule has no concrete components and so
	// no row to insert into ursp_traffic_descriptors (whose CHECK
	// enumerates the §6.6.2.1.2 component types). CreateInput
	// requires at least one TD; surfacing the empty result lets
	// the route's CreateRule call return a clear "no descriptors"
	// error rather than silently inserting an unmatchable rule.
	return out
}

// expandRouteSelectionDescriptor flattens the TS 23.503 §6.6.2.2
// nested `{precedence, component: {sst, sd, dnn, …}}` shape into
// the flat RouteDescriptor used by the rule store. Also accepts
// a flat object (no `component` envelope) for clients that already
// flatten on their side.
func expandRouteSelectionDescriptor(rsd map[string]any) []ursp.RouteDescriptor {
	prec := 1
	if v, ok := rsd["precedence"].(float64); ok {
		prec = int(v)
	}
	comp, ok := rsd["component"].(map[string]any)
	if !ok {
		comp = rsd
	}
	var rd ursp.RouteDescriptor
	rd.Precedence = prec
	if v, ok := comp["sst"].(float64); ok {
		sst := int(v)
		rd.SST = &sst
	}
	if s, _ := comp["sd"].(string); s != "" {
		rd.SD = &s
	}
	if s, _ := comp["dnn"].(string); s != "" {
		rd.DNN = s
	}
	if s, _ := comp["pdu_session_type"].(string); s != "" {
		rd.PDUSessionType = s
	}
	if s, _ := comp["access_type"].(string); s != "" {
		rd.AccessType = s
	}
	return []ursp.RouteDescriptor{rd}
}
