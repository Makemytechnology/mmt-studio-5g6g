// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// routes_li_auth.go — access gate for the /api/li/* surface.
//
// TS 33.127 §5.2 "LI administrative function security" requires the
// LI surface (warrant CRUD, IRI extract, audit) to be access-
// controlled — only authenticated operators acting under a valid
// authority should ever be able to read warrants or pull IRI events.
// The full ADMF/LEA mTLS chain in TS 33.127 §6 is out of scope for
// the local product (X1 listener is not wired); we ship a token-gate
// as the minimum-viable enforcement so the operator OAM panel and
// any tester running the API are bound to a configured secret.
//
// The token lives in network_config.li_auth_token (DB-driven, no
// compile-time secret). Empty token = "auth disabled" — used by dev
// runs / unit tests / first-boot before the operator sets a secret.
// In that mode any X-LI-Auth-Token value (including missing) is
// accepted, but the audit log records the caller-supplied operator
// header so the trail still attributes actions.

package app

import (
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// liAuthHeader is the canonical header name the OAM template sends
// (see webservice/templates/li.html — fetch() always carries it).
const liAuthHeader = "X-LI-Auth-Token"

// liOperatorHeader carries the operator identity for the audit row.
// Optional — falls back to the IP if missing.
const liOperatorHeader = "X-LI-Operator"

// loadLIAuthToken reads network_config.li_auth_token. Returns "" on
// any error — that maps to "auth disabled" so the panel keeps
// working on a fresh DB. Operator-set values flip enforcement on.
func loadLIAuthToken() string {
	db, err := engine.Open()
	if err != nil {
		return ""
	}
	var tok string
	_ = db.QueryRow("SELECT li_auth_token FROM network_config WHERE id=1").Scan(&tok)
	return strings.TrimSpace(tok)
}

// requireLIAuth wraps an HTTP handler and enforces the X-LI-Auth-Token
// header against network_config.li_auth_token. Empty stored token =
// open mode (dev / first-boot). Mismatch = 401.
func requireLIAuth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		want := loadLIAuthToken()
		if want != "" {
			got := r.Header.Get(liAuthHeader)
			if subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 {
				jsonError(w, "li: unauthorized", http.StatusUnauthorized)
				return
			}
		}
		h(w, r)
	}
}

// liOperatorFromRequest extracts the operator identity for the audit
// row: explicit X-LI-Operator header > basic-auth user > "remote-IP"
// > "system". Per TS 33.127 §5.2 every ADMF action must be
// attributable.
func liOperatorFromRequest(r *http.Request) string {
	if v := strings.TrimSpace(r.Header.Get(liOperatorHeader)); v != "" {
		return v
	}
	if u, _, ok := r.BasicAuth(); ok && u != "" {
		return u
	}
	if r.RemoteAddr != "" {
		return r.RemoteAddr
	}
	return "system"
}
