// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_security.go — REST surface for 5G Core Security
// (TS 33.501 §5.9 / §9.x).
//
// Wires `security/core_security` to /api/security/*. The package owns
// the signalling firewall, IDS signatures, blocked-IP list, known-gNB
// allow-list, and the immutable audit log. This surface drives
// `templates/security.html` and is the operator API.
//
// Spec anchors (verified against local TS PDFs by speccheck):
//
//   - TS 33.501 §5.9    Core network security — top-level requirements.
//   - TS 33.501 §5.9.1  Trust boundaries — known-gNB / blocked-IP split.
//   - TS 33.501 §5.9.4  Signalling-traffic monitoring — drives the IDS
//                       signatures + audit log surface here.
//   - TS 33.501 §9.2    N2 security — known-gNB allow-list (NGAP source).
//   - TS 33.501 §9.3    N3 security — GTP-U IP perimeter guard.
//   - TS 33.501 §9.9    Non-SBA inter-PLMN interfaces.
//
// Deferred (PDFs not local; TODO(spec:) prose only):
//
//   - TS 33.117 §4.2.3 audit-log schema (cryptographic chaining/signing).
//   - TS 33.117 §4.3 hardening checklist (deployment-side).
//
// Response shapes match `templates/security.html`: `{ok: true, ...}`
// envelopes with domain-noun keys (`d.ids.alerts`, `d.rate_limiter
// .violations`, `d.gtpu.allowed`, `d.audit_summary[].count`, etc.).
package app

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/security/core_security"
)

func (s *Server) registerSecurityRoutes() {
	r := s.Router

	// ── Dashboard aggregator ──────────────────────────────────────
	// One round-trip builds every panel: IDS alerts, blocked sources,
	// IDS rules, rate-limiter violations, GTP-U guard counts, 24h
	// audit summary. Sub-sections degrade independently — a DB read
	// failure in audit_summary doesn't blank the rest.
	r.Get("/api/security/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, buildSecurityStatus())
	})

	// ── Audit log (immutable append-only) ─────────────────────────
	r.Get("/api/security/audit", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		events, err := core_security.GetAuditLog(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if events == nil {
			events = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "events": events})
	})

	// Synthetic audit-event raise (drills, operator-initiated).
	r.Post("/api/security/audit", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			EventType string                 `json:"event_type"`
			Severity  string                 `json:"severity"`
			SourceIP  string                  `json:"source_ip"`
			IMSI      string                 `json:"imsi"`
			Detail    string                 `json:"detail"`
			Extra     map[string]interface{} `json:"extra"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.EventType == "" {
			jsonError(w, "event_type required", http.StatusBadRequest)
			return
		}
		// Schema CHECK gates these too, but a clean 400 beats 500-from-SQL.
		if d.Severity != "" {
			switch d.Severity {
			case "DEBUG", "INFO", "WARNING", "ERROR", "CRITICAL":
			default:
				jsonError(w,
					"severity must be one of DEBUG|INFO|WARNING|ERROR|CRITICAL",
					http.StatusBadRequest)
				return
			}
		}
		core_security.LogEvent(d.EventType, d.Detail, d.SourceIP, d.IMSI,
			d.Severity, d.Extra)
		jsonReply(w, map[string]any{"ok": true, "event_type": d.EventType})
	})

	// ── Firewall rules (TS 33.501 §9.x interface partition) ───────
	r.Get("/api/security/firewall/rules", func(w http.ResponseWriter, _ *http.Request) {
		list, err := core_security.ListFirewallRules()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []core_security.FirewallRule{}
		}
		jsonReply(w, map[string]any{"ok": true, "rules": list})
	})

	r.Post("/api/security/firewall/rules", func(w http.ResponseWriter, rq *http.Request) {
		var d core_security.FirewallRule
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" {
			jsonError(w, "name required", http.StatusBadRequest)
			return
		}
		// Validate vocab here so we can return 400 with a useful message;
		// the package does the same check, but its error string is less
		// helpful for an operator.
		switch d.Protocol {
		case "ngap", "nas", "gtpu", "sbi", "any":
		default:
			jsonError(w, "protocol must be one of ngap|nas|gtpu|sbi|any",
				http.StatusBadRequest)
			return
		}
		switch d.Action {
		case "allow", "deny", "rate_limit":
		default:
			jsonError(w, "action must be one of allow|deny|rate_limit",
				http.StatusBadRequest)
			return
		}
		if err := core_security.UpsertFirewallRule(d); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "name": d.Name})
	})

	r.Delete("/api/security/firewall/rules/{name}", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		if !core_security.DeleteFirewallRule(name) {
			jsonError(w, "rule not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "name": name})
	})

	// ── IDS signatures (TS 33.501 §5.9.4 monitoring) ──────────────
	r.Get("/api/security/ids/signatures", func(w http.ResponseWriter, _ *http.Request) {
		list, err := core_security.ListIDSSignatures()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []core_security.IDSSignature{}
		}
		jsonReply(w, map[string]any{"ok": true, "signatures": list})
	})

	r.Post("/api/security/ids/signatures", func(w http.ResponseWriter, rq *http.Request) {
		var d core_security.IDSSignature
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Name == "" || d.Pattern == "" {
			jsonError(w, "name and pattern required",
				http.StatusBadRequest)
			return
		}
		if d.Severity != "" {
			switch d.Severity {
			case "INFO", "WARNING", "ERROR", "CRITICAL":
			default:
				jsonError(w,
					"severity must be one of INFO|WARNING|ERROR|CRITICAL",
					http.StatusBadRequest)
				return
			}
		}
		if err := core_security.UpsertIDSSignature(d); err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "name": d.Name})
	})

	r.Delete("/api/security/ids/signatures/{name}", func(w http.ResponseWriter, rq *http.Request) {
		name := chi.URLParam(rq, "name")
		if !core_security.DeleteIDSSignature(name) {
			jsonError(w, "signature not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "name": name})
	})

	// ── Blocked IPs (TS 33.501 §5.9.1 trust boundary) ─────────────
	r.Get("/api/security/blocked-ips", func(w http.ResponseWriter, _ *http.Request) {
		list, err := core_security.ListBlockedIPs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "blocked": list})
	})

	r.Post("/api/security/blocked-ips", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IP     string `json:"ip"`
			Reason string `json:"reason"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IP == "" {
			jsonError(w, "ip required", http.StatusBadRequest)
			return
		}
		core_security.BlockIP(d.IP, d.Reason)
		jsonReply(w, map[string]any{"ok": true, "ip": d.IP})
	})

	r.Delete("/api/security/blocked-ips/{ip}", func(w http.ResponseWriter, rq *http.Request) {
		ip := chi.URLParam(rq, "ip")
		core_security.UnblockIP(ip)
		jsonReply(w, map[string]any{"ok": true, "ip": ip})
	})

	// ── Known gNB allow-list (TS 33.501 §9.2) ─────────────────────
	r.Get("/api/security/known-gnbs", func(w http.ResponseWriter, _ *http.Request) {
		list, err := core_security.ListKnownGnBs()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "gnbs": list})
	})

	r.Post("/api/security/known-gnbs", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IP    string `json:"ip"`
			GnbID string `json:"gnb_id"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IP == "" {
			jsonError(w, "ip required", http.StatusBadRequest)
			return
		}
		core_security.RegisterKnownGnB(d.IP, d.GnbID)
		jsonReply(w, map[string]any{"ok": true, "ip": d.IP})
	})

	r.Delete("/api/security/known-gnbs/{ip}", func(w http.ResponseWriter, rq *http.Request) {
		ip := chi.URLParam(rq, "ip")
		core_security.UnregisterGnB(ip)
		jsonReply(w, map[string]any{"ok": true, "ip": ip})
	})

	// ── Default policy catalogue (read-only) ──────────────────────
	r.Get("/api/security/policies", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok":       true,
			"policies": core_security.DefaultPolicies(),
		})
	})

	// ── Rate-limit reset (operator force-clear) ───────────────────
	r.Post("/api/security/rate-limit/reset", func(w http.ResponseWriter, _ *http.Request) {
		core_security.ResetRateLimits()
		jsonReply(w, map[string]any{"ok": true})
	})

	s.registerSecurityHardeningRoutes()
}

// registerSecurityHardeningRoutes wires the firewall packet-evaluator,
// IDS test harness, hit-counter readbacks, TTL block surface, and the
// hit-counter reset button. Split out so the route map for the
// operator surface stays browsable.
func (s *Server) registerSecurityHardeningRoutes() {
	r := s.Router

	// ── Firewall packet evaluator (TS 33.501 §5.9.1) ──────────────
	// "Given this 4-tuple, what would the configured rules do?"
	// Used by the panel's "what-if" tester and by NF call sites that
	// need a programmatic decision.
	r.Post("/api/security/firewall/eval", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			Protocol string `json:"protocol"`
			SrcIP    string `json:"src_ip"`
			DstIP    string `json:"dst_ip"`
			Port     int    `json:"port"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.Protocol == "" {
			d.Protocol = "any"
		}
		res := core_security.EvalFirewall(d.Protocol, d.SrcIP, d.DstIP, d.Port)
		jsonReply(w, map[string]any{
			"ok":     true,
			"action": res.Action,
			"rule":   res.Rule,
		})
	})

	// ── Firewall hit counters (in-memory) ─────────────────────────
	r.Get("/api/security/firewall/hits", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok":   true,
			"hits": core_security.FirewallHits(),
		})
	})

	// ── IDS hit counters (raw + post-threshold alerts) ────────────
	r.Get("/api/security/ids/hits", func(w http.ResponseWriter, _ *http.Request) {
		raw, alerts := core_security.IDSHits()
		jsonReply(w, map[string]any{
			"ok":     true,
			"hits":   raw,
			"alerts": alerts,
		})
	})

	// ── IDS test harness (panel button + tester smoke) ────────────
	// Drives DetectIntrusion from the operator surface so the panel
	// can show "this signature would trip on this event" without
	// fabricating real signalling.
	r.Post("/api/security/ids/test", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			EventType string `json:"event_type"`
			SourceIP  string `json:"source_ip"`
			Detail    string `json:"detail"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.EventType == "" {
			jsonError(w, "event_type required", http.StatusBadRequest)
			return
		}
		detected := core_security.DetectIntrusion(d.EventType, d.SourceIP, d.Detail)
		jsonReply(w, map[string]any{
			"ok":       true,
			"detected": detected,
		})
	})

	// ── Reset hit counters + IDS sliding-window state ─────────────
	r.Post("/api/security/hits/reset", func(w http.ResponseWriter, _ *http.Request) {
		core_security.ResetHits()
		core_security.ResetIDSBuckets()
		jsonReply(w, map[string]any{"ok": true})
	})

	// ── Blocked-IP write with TTL ─────────────────────────────────
	// Mirrors POST /blocked-ips but takes ttl_s. ttl_s=0 is permanent.
	r.Post("/api/security/blocked-ips/ttl", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			IP     string `json:"ip"`
			Reason string `json:"reason"`
			TTLS   int    `json:"ttl_s"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		if d.IP == "" {
			jsonError(w, "ip required", http.StatusBadRequest)
			return
		}
		if d.TTLS < 0 {
			jsonError(w, "ttl_s must be >= 0", http.StatusBadRequest)
			return
		}
		core_security.BlockIPWithTTL(d.IP, d.Reason,
			time.Duration(d.TTLS)*time.Second)
		jsonReply(w, map[string]any{"ok": true, "ip": d.IP})
	})
}

// buildSecurityStatus is the panel aggregator. Each sub-section
// reads its own data source and degrades to a zero/empty payload on
// error so a partial outage doesn't blank the whole panel.
func buildSecurityStatus() map[string]any {
	out := map[string]any{
		"ok":      true,
		"summary": core_security.Status(),
	}

	// IDS alerts: most recent INTRUSION_DETECTED rows.
	alerts := []map[string]any{}
	if rows, err := queryAuditByEvent("INTRUSION_DETECTED", 50); err == nil {
		alerts = rows
	}
	// IDS rules: persisted signature catalogue.
	rules, _ := core_security.ListIDSSignatures()
	if rules == nil {
		rules = []core_security.IDSSignature{}
	}
	// Blocked sources: package's persisted blocked-IP list, mapped
	// to the panel's `{ip: {remaining_sec}}` shape (we don't track
	// TTL today, so report 0 — operators see the IP but no countdown).
	blocked := map[string]any{}
	if list, err := core_security.ListBlockedIPs(); err == nil {
		for _, row := range list {
			ip, _ := row["ip"].(string)
			blocked[ip] = map[string]any{
				"reason":         row["reason"],
				"remaining_sec":  0,
			}
		}
	}
	out["ids"] = map[string]any{
		"alerts":          alerts,
		"rules":           rules,
		"blocked_sources": blocked,
	}

	// Rate-limiter violations: most recent RATE_LIMITED rows.
	violations := []map[string]any{}
	if rows, err := queryAuditByEvent("RATE_LIMITED", 20); err == nil {
		violations = rows
	}
	out["rate_limiter"] = map[string]any{"violations": violations}

	// GTP-U firewall stats: count audit rows in the last 24h, broken
	// down by event_type. The §9.3 perimeter guard tags GTPU_BAD_TEID
	// / GTPU_OVERSIZED on rejection — count those, plus an "allowed"
	// estimator computed as zero today (no allow-counter yet).
	out["gtpu"] = buildGTPUStats()

	// 24h audit summary by severity (for the `secAudit` count card).
	out["audit_summary"] = audit24hBySeverity()

	return out
}

// queryAuditByEvent fetches recent rows of one event_type. Each row
// maps to the panel's alert shape; the package's audit_log schema
// already has source_ip, severity, detail, extra_json + timestamp.
func queryAuditByEvent(eventType string, limit int) ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT event_type, severity, source_ip, imsi, detail,
		extra_json, created_at
		FROM security_audit_log WHERE event_type=?
		ORDER BY created_at DESC, id DESC LIMIT ?`, eventType, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var et, sev, ip, imsi, det, ex, ts string
		if rows.Scan(&et, &sev, &ip, &imsi, &det, &ex, &ts) != nil {
			continue
		}
		out = append(out, map[string]any{
			"event_type": et, "severity": sev, "source": ip,
			"imsi": imsi, "detail": det, "extra_json": ex,
			"timestamp": ts,
			// Panel reads `a.rule` as the title — alias event_type.
			"rule": et,
			// Panel reads `a.count` — single-event row, count=1.
			"count": 1,
		})
	}
	return out, nil
}

// buildGTPUStats counts the §9.3 perimeter-guard rejections in the
// last 24h. `allowed` is zero today — to wire it, add a CheckGTPUPacket
// success counter or an `oam/pm` increment on the allow path.
func buildGTPUStats() map[string]any {
	out := map[string]any{
		"allowed":               0,
		"blocked_unknown_teid":  countAuditEvent("GTPU_BAD_TEID"),
		"blocked_unknown_gnb":   countAuditEvent("UNKNOWN_GNB"),
		"blocked_ip_spoof":      countAuditEvent("GTPU_OVERSIZED"),
		"blocked_blacklist":     countAuditEvent("BLOCKED_IP"),
	}
	return out
}

func countAuditEvent(eventType string) int64 {
	db, err := engine.Open()
	if err != nil {
		return 0
	}
	var n int64
	_ = db.QueryRow(`SELECT COUNT(*) FROM security_audit_log
		WHERE event_type=? AND created_at > datetime('now','-1 day')`, eventType).Scan(&n)
	return n
}

func audit24hBySeverity() []map[string]any {
	db, err := engine.Open()
	if err != nil {
		return []map[string]any{}
	}
	rows, err := db.Query(`SELECT severity, COUNT(*) FROM security_audit_log
		WHERE created_at > datetime('now','-1 day')
		GROUP BY severity ORDER BY severity`)
	if err != nil {
		return []map[string]any{}
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var sev string
		var n int64
		if rows.Scan(&sev, &n) == nil {
			out = append(out, map[string]any{
				"severity": sev,
				"count":    n,
			})
		}
	}
	return out
}
