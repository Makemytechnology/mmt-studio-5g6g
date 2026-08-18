// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/infra/health"
)

// RegisterAPIRoutes wires the JSON endpoints that the HTML panels poll.
// Call after RegisterRoutes in main() so both trees are live.
func (s *Server) RegisterAPIRoutes() {
	s.Router.Get("/api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, health.Watch())
	})

	// Liveness — 200 always while the process is up.
	s.Router.Get("/api/live", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"status":"alive"}`))
	})

	// Readiness — 503 if any probe reports non-healthy.
	s.Router.Get("/api/ready", func(w http.ResponseWriter, r *http.Request) {
		rep := health.Watch()
		code := http.StatusOK
		if rep.Status != "healthy" {
			code = http.StatusServiceUnavailable
		}
		jsonReplyStatus(w, code, map[string]string{"status": rep.Status})
	})

	// /api/fm/* lives in routes_fm.go (registered via RegisterDomainRoutes).
	// /api/kpis lives in routes_kpis.go (same).
}
