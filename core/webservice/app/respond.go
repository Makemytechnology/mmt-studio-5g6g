// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// HTTP response helpers shared by every route file in this package.
// Centralised so handlers stay focused on business logic instead of
// header / encoding boilerplate.
package app

import (
	"encoding/json"
	"net/http"
)

// jsonReply writes v as JSON with a 200 status. Encoding errors are
// dropped — by the time we're encoding the response headers are
// already on the wire and there's nothing actionable left to do.
func jsonReply(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// jsonReplyStatus is jsonReply with an explicit status code, for the
// rare endpoint that returns JSON with non-200 (e.g. readiness 503).
func jsonReplyStatus(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// jsonError writes {"error": msg} with the given status code.
// Argument order matches stdlib's http.Error so call-sites read the
// same way.
func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// decodeJSON parses the request body into v. On failure it writes a
// 400 JSON error and returns false; the caller should `return`.
// Returns true on success.
func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		jsonError(w, err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}
