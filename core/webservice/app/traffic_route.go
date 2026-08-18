// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Traffic engine proxy routes — Go port of webservice/routes/traffic_api.py.
//
// Proxies /api/traffic/* to the traffic engine REST service
// (tools/traffic/traffic_rest.py). The engine URL is read from
// infra_config; defaults to http://localhost:9100.
package app

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/mmt/mmt-studio-core/db/crud"
)

const (
	defaultEngineURL = "http://localhost:9100"
	engineTimeout    = 5 * time.Second
)

func engineURL() string {
	cfg, err := crud.GetInfraConfig()
	if err == nil {
		if u, ok := cfg["traffic_engine_url"].(string); ok && u != "" {
			return u
		}
	}
	return defaultEngineURL
}

// proxyToEngine forwards a request to the traffic engine and copies the
// JSON response back to the client.
func proxyToEngine(w http.ResponseWriter, method, path string, body io.Reader) {
	url := strings.TrimRight(engineURL(), "/") + path
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		jsonError(w, fmt.Sprintf("bad request: %v", err), http.StatusBadRequest)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: engineTimeout}
	resp, err := client.Do(req)
	if err != nil {
		jsonError(w, fmt.Sprintf("Traffic engine unreachable: %v", err), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

// RegisterTrafficRoutes wires /api/traffic/* endpoints.
func (s *Server) RegisterTrafficRoutes() {
	r := s.Router

	// ── Config ──────────────────────────────────────────────────────
	r.Get("/api/traffic/config", func(w http.ResponseWriter, rq *http.Request) {
		jsonReply(w, map[string]string{"traffic_engine_url": engineURL()})
	})

	r.Post("/api/traffic/config", func(w http.ResponseWriter, rq *http.Request) {
		var d struct {
			URL string `json:"traffic_engine_url"`
		}
		if !decodeJSON(w, rq, &d) {
			return
		}
		url := strings.TrimSpace(d.URL)
		if url == "" {
			jsonError(w, "traffic_engine_url required", http.StatusBadRequest)
			return
		}
		_, err := crud.UpdateInfraConfig(map[string]any{"traffic_engine_url": url})
		if err != nil {
			jsonError(w, err.Error(), http.StatusInternalServerError)
			return
		}
		jsonReply(w, map[string]string{"status": "saved", "traffic_engine_url": url})
	})

	// ── Health ──────────────────────────────────────────────────────
	r.Get("/api/traffic/health", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "GET", "/api/health", nil)
	})

	// ── Sessions ────────────────────────────────────────────────────
	r.Post("/api/traffic/start", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "POST", "/api/traffic/start", rq.Body)
	})

	r.Get("/api/traffic/sessions", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "GET", "/api/traffic/sessions", nil)
	})

	r.Get("/api/traffic/sessions/{session_id}", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "session_id")
		proxyToEngine(w, "GET", "/api/traffic/sessions/"+sid, nil)
	})

	r.Post("/api/traffic/sessions/{session_id}/stop", func(w http.ResponseWriter, rq *http.Request) {
		sid := chi.URLParam(rq, "session_id")
		proxyToEngine(w, "POST", "/api/traffic/sessions/"+sid+"/stop", nil)
	})

	r.Post("/api/traffic/stop-all", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "POST", "/api/traffic/stop-all", nil)
	})

	r.Get("/api/traffic/active", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "GET", "/api/traffic/active", nil)
	})

	// ── Voice/Video Calls ───────────────────────────────────────────
	r.Post("/api/traffic/voice-call", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "POST", "/api/traffic/voice-call", rq.Body)
	})

	r.Post("/api/traffic/video-call", func(w http.ResponseWriter, rq *http.Request) {
		proxyToEngine(w, "POST", "/api/traffic/video-call", rq.Body)
	})
}
