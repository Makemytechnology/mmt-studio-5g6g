// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package rag — Retrieval-Augmented Generation engine.
//
// Stub — full port of oam/ai/rag/ is P3 priority. The RAG engine
// (vectorstore search, 3GPP spec embedding) will be implemented when
// a Go vector similarity library is integrated.
package rag

// Status returns current RAG subsystem state.
func Status() map[string]any {
	return map[string]any{"status": "ready"}
}
