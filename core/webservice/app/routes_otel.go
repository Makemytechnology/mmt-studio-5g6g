// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_otel.go — REST surface for OpenTelemetry observability.
//
// Wires `oam/otel` to /api/otel/*. The package owns config (read
// from infra_config.otel_*), span emission, the in-memory ring of
// recent spans, per-(NF, operation) counters, and the eventual
// OTLP/Prometheus export path (deferred until the SDK dep is
// vendored — the operator-side surface here is fully functional
// today).
//
// Spec anchors:
//
//   - W3C Trace Context — trace_id (16 bytes hex) / span_id (8 bytes
//     hex) / parent linkage.
//   - TS 28.552 §6  PM measurements via OTEL exporters (deferred).
//   - TS 28.554 §5  E2E KPIs that map to OTEL traces (deferred).
//
// Response shapes are `{ok: true, ...}` envelopes throughout.
package app

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/mmt/mmt-studio-core/oam/otel"
)

func (s *Server) registerOTELRoutes() {
	r := s.Router

	// ── Status / dashboard ────────────────────────────────────────
	r.Get("/api/otel/status", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "status": otel.Status()})
	})

	r.Get("/api/otel/config", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "config": otel.LoadConfig()})
	})

	// ── Config update (sparse PATCH against infra_config.otel_*) ──
	r.Patch("/api/otel/config", func(w http.ResponseWriter, rq *http.Request) {
		var d map[string]any
		if !decodeJSON(w, rq, &d) {
			return
		}
		// Only allow-list the otel_* columns to keep this surface from
		// drifting into a generic infra_config patch path.
		patch := map[string]any{}
		for _, k := range []string{
			"otel_enabled", "otel_metrics_enabled", "otel_traces_enabled",
			"otel_logs_enabled", "otel_exporter", "otel_endpoint",
			"otel_prometheus_port",
		} {
			if v, ok := d[k]; ok {
				patch[k] = v
			}
		}
		if len(patch) == 0 {
			jsonError(w, "no otel_* fields in patch", http.StatusBadRequest)
			return
		}
		cfg, err := otel.UpdateConfig(patch)
		if err != nil {
			if otel.IsBadInput(err) {
				jsonError(w, err.Error(), http.StatusBadRequest)
			} else {
				jsonError(w, err.Error(), http.StatusInternalServerError)
			}
			return
		}
		jsonReply(w, map[string]any{"ok": true, "config": cfg})
	})

	// ── Smoke-test span emission ──────────────────────────────────
	// Operator drills + tester smoke checks. Body fields are all
	// optional; defaults give a clean root span on the AMF NF that
	// the panel can correlate immediately.
	r.Post("/api/otel/test-span", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			NF             string            `json:"nf"`
			Operation      string            `json:"operation"`
			ParentTraceID  string            `json:"parent_trace_id"`
			ParentSpanID   string            `json:"parent_span_id"`
			Attributes     map[string]string `json:"attributes"`
			Status         string            `json:"status"`
			EventName      string            `json:"event_name"`
			DurationUS     int64             `json:"duration_us"` // 0 = end immediately
		}
		if rq.ContentLength > 0 && !decodeJSON(w, rq, &d) {
			return
		}
		if d.NF == "" {
			d.NF = "amf"
		}
		if d.Operation == "" {
			d.Operation = "test.smoke"
		}
		span := otel.StartSpan(d.NF, d.Operation, d.ParentTraceID, d.ParentSpanID)
		for k, v := range d.Attributes {
			span.SetAttribute(k, v)
		}
		if d.EventName != "" {
			span.AddEvent(d.EventName, map[string]string{"source": "operator"})
		}
		// duration_us > 0 lets the tester pin the duration without sleeping
		// in-process — we just rewrite the start_time. Otherwise End()
		// stamps now-now (≈0ms), which is fine for the smoke path.
		if d.DurationUS > 0 {
			span.StartTime -= d.DurationUS
		}
		span.End(d.Status)
		jsonReply(w, map[string]any{
			"ok":       true,
			"trace_id": span.TraceID,
			"span_id":  span.SpanID,
		})
	})

	// ── Recent spans (in-memory ring) ─────────────────────────────
	r.Get("/api/otel/spans", func(w http.ResponseWriter, rq *http.Request) {
		limit := 100
		if v := rq.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil && n > 0 {
				limit = n
			}
		}
		traceID := rq.URL.Query().Get("trace_id")
		nf := rq.URL.Query().Get("nf")
		operation := rq.URL.Query().Get("operation")
		spans := otel.FilterSpans(traceID, nf, operation, limit)
		if spans == nil {
			spans = []otel.Span{}
		}
		jsonReply(w, map[string]any{
			"ok":    true,
			"spans": spans,
			"count": len(spans),
		})
	})

	// ── Single trace tree (sorted by start_time) ──────────────────
	r.Get("/api/otel/spans/{trace_id}", func(w http.ResponseWriter, rq *http.Request) {
		traceID := chi.URLParam(rq, "trace_id")
		if traceID == "" {
			jsonError(w, "trace_id required", http.StatusBadRequest)
			return
		}
		rows := otel.GetTrace(traceID)
		if len(rows) == 0 {
			jsonError(w, "trace_id not in ring", http.StatusNotFound)
			return
		}
		jsonReply(w, map[string]any{
			"ok":       true,
			"trace_id": traceID,
			"spans":    rows,
			"count":    len(rows),
		})
	})

	// ── Per-(NF, operation) counters ──────────────────────────────
	r.Get("/api/otel/counters", func(w http.ResponseWriter, _ *http.Request) {
		jsonReply(w, map[string]any{"ok": true, "counters": otel.SpanCounters()})
	})

	// ── Reset (panel button) ──────────────────────────────────────
	r.Post("/api/otel/reset", func(w http.ResponseWriter, _ *http.Request) {
		otel.ResetSpans()
		jsonReply(w, map[string]any{"ok": true})
	})
}
