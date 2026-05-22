// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_trace.go — REST surface for Subscriber Trace (TS 32.422 / TS 32.423).
//
// Wires `oam/trace` to /api/trace/*. The package owns the trace
// session life-cycle (Start → Capture-on-event → Stop), per-session
// records, and the `trace_sessions` / `trace_records` persistence.
// This surface drives `templates/traces.html`.
//
// Spec anchors (TODOs because the PDFs are not in specs/3gpp/):
//
//   - TODO(spec: TS 32.421) — Subscriber and equipment trace; trace
//                             concepts and requirements (the "Trace
//                             Recording Session" concept).
//   - TODO(spec: TS 32.422) — Subscriber and equipment trace; trace
//                             control and configuration management.
//                             Defines Trace Reference, depth /
//                             interfaces selection, NE Type list.
//   - TODO(spec: TS 32.423) — Subscriber and equipment trace; trace
//                             data definition and management. The
//                             per-record fields below follow §5.1.
//   - TODO(spec: TS 28.531) — Management-Based Trace (MDT) activation
//                             via 5GC-MnS; deferred to OAM provisioning.
//
// All response shapes are `{ok: true, ...}` envelopes — matches
// `templates/traces.html` (`d.ok && d.sessions` / `d.ok && d.records`
// guards in the JS).
package app

import (
	"encoding/json"
	"encoding/xml"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/trace"
)

func (s *Server) registerTraceRoutes() {
	r := s.Router

	// ── Start session (TS 32.422 §5.6 trace job activation) ──────
	r.Post("/api/trace/start", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			TraceRef    string `json:"trace_ref"`
			IMSI        string `json:"imsi"`
			GnbIP       string `json:"gnb_ip"`
			Depth       string `json:"depth"`
			Interfaces  string `json:"interfaces"`
			DurationSec int    `json:"duration_sec"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		// CHECK constraint at the schema level rejects bad depths too,
		// but a clean 400 here is friendlier than a 500-from-SQLite.
		if d.Depth != "" {
			switch d.Depth {
			case trace.DepthMinimum, trace.DepthMedium, trace.DepthMaximum:
			default:
				jsonError(w, "depth must be one of minimum|medium|maximum",
					http.StatusBadRequest)
				return
			}
		}
		if d.DurationSec < 0 || d.DurationSec > 86400 {
			jsonError(w, "duration_sec must be in [0, 86400]",
				http.StatusBadRequest)
			return
		}
		ref, err := trace.StartSession(trace.SessionInput{
			TraceRef: d.TraceRef, IMSI: d.IMSI, GnbIP: d.GnbIP,
			Depth: d.Depth, Interfaces: d.Interfaces,
			DurationSec: d.DurationSec,
		})
		if err != nil {
			jsonError(w, err.Error(), http.StatusBadRequest)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "trace_ref": ref})
	})

	// ── List sessions ─────────────────────────────────────────────
	r.Get("/api/trace/sessions", func(w http.ResponseWriter, _ *http.Request) {
		list, err := trace.ListSessions()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if list == nil {
			list = []map[string]any{}
		}
		jsonReply(w, map[string]any{"ok": true, "sessions": list})
	})

	// ── Stop session ──────────────────────────────────────────────
	r.Post("/api/trace/{ref}/stop", func(w http.ResponseWriter, rq *http.Request) {
		ref := chi.URLParam(rq, "ref")
		if ref == "" {
			jsonError(w, "trace_ref required", http.StatusBadRequest)
			return
		}
		ok, err := trace.StopSession(ref)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "session not found or already stopped",
				http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "trace_ref": ref})
	})

	// ── Delete session (CASCADE deletes records via FK) ───────────
	r.Delete("/api/trace/{ref}", func(w http.ResponseWriter, rq *http.Request) {
		ref := chi.URLParam(rq, "ref")
		db, err := engine.Open()
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		res, err := db.Exec(`DELETE FROM trace_sessions WHERE trace_ref=?`, ref)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()
		if n == 0 {
			jsonError(w, "trace_ref not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "trace_ref": ref})
	})

	// ── Per-session records (TS 32.423 §5.1) ──────────────────────
	r.Get("/api/trace/{ref}/records", func(w http.ResponseWriter, rq *http.Request) {
		ref := chi.URLParam(rq, "ref")
		limit := 500
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		records, err := listSessionRecords(ref, limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok": true, "trace_ref": ref, "records": records,
		})
	})

	// ── Export as JSON / XML (TS 32.423 §6 file exchange) ─────────
	r.Get("/api/trace/{ref}/export/{fmt}", func(w http.ResponseWriter, rq *http.Request) {
		ref := chi.URLParam(rq, "ref")
		format := chi.URLParam(rq, "fmt")
		records, err := listSessionRecords(ref, 0)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		switch format {
		case "json":
			w.Header().Set("Content-Type", "application/json")
			w.Header().Set("Content-Disposition",
				"attachment; filename=\"trace-"+ref+".json\"")
			_ = json.NewEncoder(w).Encode(map[string]any{
				"trace_ref": ref, "records": records,
			})
		case "xml":
			w.Header().Set("Content-Type", "application/xml")
			w.Header().Set("Content-Disposition",
				"attachment; filename=\"trace-"+ref+".xml\"")
			// Per TS 32.423 §6 the canonical export is ASN.1 BER inside
			// a TraceRecord file — we ship a flat XML envelope instead so
			// operators can grep / diff exports without a schema compiler.
			type recordXML struct {
				XMLName   xml.Name `xml:"Record"`
				Timestamp string   `xml:"timestamp,attr"`
				Iface     string   `xml:"interface,attr"`
				Direction string   `xml:"direction,attr"`
				MsgType   string   `xml:"msgType"`
				IMSI      string   `xml:"imsi,omitempty"`
				Summary   string   `xml:"summary,omitempty"`
			}
			type traceXML struct {
				XMLName  xml.Name    `xml:"TraceRecord"`
				TraceRef string      `xml:"traceRef,attr"`
				Records  []recordXML `xml:"Records>Record"`
			}
			out := traceXML{TraceRef: ref}
			for _, rec := range records {
				ts, _ := rec["timestamp"].(string)
				iface, _ := rec["interface"].(string)
				dir, _ := rec["direction"].(string)
				mt, _ := rec["msg_type"].(string)
				imsi, _ := rec["imsi"].(string)
				summ, _ := rec["summary"].(string)
				out.Records = append(out.Records, recordXML{
					Timestamp: ts, Iface: iface, Direction: dir,
					MsgType: mt, IMSI: imsi, Summary: summ,
				})
			}
			_ = xml.NewEncoder(w).Encode(out)
		default:
			jsonError(w, "fmt must be json or xml",
				http.StatusBadRequest)
		}
	})

	// ── AI hooks (oam/ai not wired into this surface yet) ────────
	// Keep these returning a structured "not configured" so the
	// panel renders cleanly without the AI router being live.
	r.Get("/api/trace/{ref}/ai/analyze", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok":       true,
			"analysis": "AI router not configured; configure /api/ai/config to enable trace analysis.",
		})
	})
	r.Get("/api/trace/{ref}/ai/bottleneck", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{
			"ok":      true,
			"message": "AI router not configured; no bottleneck analysis available.",
		})
	})

	s.registerTraceCorrelationRoutes()
}

// ── Correlation index ────────────────────────────────────────────
// Bridges N1/N2 (NGAP, NAS), SBI (TS 29.500 §6.10.2.5
// 3gpp-Sbi-Correlation-Info), N4 (PFCP SEID/TEID pairs), and OTEL
// trace IDs onto a single call_id keyed row so the operator panel
// can pivot from any one identifier to the rest of the call.
func (s *Server) registerTraceCorrelationRoutes() {
	r := s.Router

	r.Post("/api/trace/correlation", func(w http.ResponseWriter, rq *http.Request) {
		var d trace.CorrelationInput
		if !decodeJSON(w, rq, &d) {
			return
		}
		callID, err := trace.RegisterCorrelation(d)
		if err != nil {
			if err == trace.ErrNoNaturalKey {
				jsonError(w, err.Error(), http.StatusBadRequest)
				return
			}
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		row, _ := trace.LookupCallID(callID)
		jsonReply(w, map[string]any{
			"ok":      true,
			"call_id": callID,
			"row":     row,
		})
	})

	r.Get("/api/trace/correlation", func(w http.ResponseWriter, rq *http.Request) {
		limit := 0
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		rows, err := trace.ListCorrelations(limit)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{
			"ok":           true,
			"correlations": rows,
			"count":        len(rows),
		})
	})

	r.Get("/api/trace/correlation/{call_id}", func(w http.ResponseWriter, rq *http.Request) {
		callID := chi.URLParam(rq, "call_id")
		row, err := trace.LookupCallID(callID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row == nil {
			jsonError(w, "call_id not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "row": row})
	})

	r.Delete("/api/trace/correlation/{call_id}", func(w http.ResponseWriter, rq *http.Request) {
		callID := chi.URLParam(rq, "call_id")
		ok, err := trace.DeleteCorrelation(callID)
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !ok {
			jsonError(w, "call_id not found", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{"ok": true, "call_id": callID})
	})

	r.Post("/api/trace/correlation/reset", func(w http.ResponseWriter, _ *http.Request) {
		if err := trace.PurgeCorrelations(); err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]any{"ok": true})
	})

	// Pivot lookups — each surfaces every row tied to the supplied
	// transport-layer identifier. The panel's "show me everything
	// about this UE call" button cascades these in a single fetch.
	r.Get("/api/trace/correlation/by/imsi/{imsi}", func(w http.ResponseWriter, rq *http.Request) {
		rows, err := trace.LookupByIMSI(chi.URLParam(rq, "imsi"))
		respondCorrelationList(w, rows, err)
	})
	r.Get("/api/trace/correlation/by/amf-ue-ngap-id/{id}", func(w http.ResponseWriter, rq *http.Request) {
		id, err := strconv.ParseInt(chi.URLParam(rq, "id"), 10, 64)
		if err != nil {
			jsonError(w, "id must be integer", http.StatusBadRequest)
			return
		}
		rows, err := trace.LookupByAmfNgapID(id)
		respondCorrelationList(w, rows, err)
	})
	r.Get("/api/trace/correlation/by/seid/{seid}", func(w http.ResponseWriter, rq *http.Request) {
		seid, err := strconv.ParseInt(chi.URLParam(rq, "seid"), 10, 64)
		if err != nil {
			jsonError(w, "seid must be integer", http.StatusBadRequest)
			return
		}
		rows, err := trace.LookupBySEID(seid)
		respondCorrelationList(w, rows, err)
	})
	r.Get("/api/trace/correlation/by/otel-trace-id/{tid}", func(w http.ResponseWriter, rq *http.Request) {
		rows, err := trace.LookupByOtelTraceID(chi.URLParam(rq, "tid"))
		respondCorrelationList(w, rows, err)
	})
	r.Get("/api/trace/correlation/by/sbi-corr-id/{id}", func(w http.ResponseWriter, rq *http.Request) {
		rows, err := trace.LookupBySbiCorrID(chi.URLParam(rq, "id"))
		respondCorrelationList(w, rows, err)
	})
}

func respondCorrelationList(w http.ResponseWriter, rows []map[string]any, err error) {
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rows == nil {
		rows = []map[string]any{}
	}
	jsonReply(w, map[string]any{
		"ok":           true,
		"correlations": rows,
		"count":        len(rows),
	})
}

// listSessionRecords loads all trace_records for one session, newest-first.
// Limit ≤ 0 means no LIMIT (used by the export endpoint).
func listSessionRecords(ref string, limit int) ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	q := `SELECT trace_ref, interface, direction, msg_type, msg_code,
	             imsi, summary, hex_dump, latency_us, timestamp
	      FROM trace_records WHERE trace_ref=?
	      ORDER BY timestamp DESC, id DESC`
	args := []any{ref}
	if limit > 0 {
		q += ` LIMIT ?`
		args = append(args, limit)
	}
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if rows.Scan(ptrs...) != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = scan[i]
		}
		out = append(out, row)
	}
	if out == nil {
		out = []map[string]any{}
	}
	return out, nil
}
