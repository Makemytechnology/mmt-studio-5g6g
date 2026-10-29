// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package app

import (
	"net/http"

	"github.com/mmt/mmt-studio-core/nf/amf"
)

// RegisterAMFRoutes adds the JSON endpoints that the Contexts / gNBs web
// panels poll. The state lives in the amf package registries — we shape
// the JSON here.
func (s *Server) RegisterAMFRoutes() {
	s.Router.Get("/api/amf/ues", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, amf.UEs(nil))
	})
	s.Router.Get("/api/amf/gnbs", func(w http.ResponseWriter, r *http.Request) {
		jsonReply(w, amf.Gnbs(nil))
	})
}
