// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package aidb — AI infrastructure database schema (DDL).
//
// Go port of oam/ai/db/ai_schema.py. Provides the DDL statements
// for the ai_config and ai_conversations tables. Called by the
// schema bootstrap to ensure tables exist.
package aidb

import "github.com/mmt/mmt-studio-core/db/engine"

// DDL contains the CREATE TABLE statements for AI infrastructure.
var DDL = []string{
	// AI provider configuration (single row)
	`CREATE TABLE IF NOT EXISTS ai_config (
		id                INTEGER PRIMARY KEY CHECK (id = 1),
		active_provider   TEXT NOT NULL DEFAULT 'local',
		local_endpoint    TEXT NOT NULL DEFAULT 'http://localhost:11434',
		local_model       TEXT NOT NULL DEFAULT 'llama3.2',
		anthropic_api_key  TEXT,
		anthropic_model    TEXT NOT NULL DEFAULT 'claude-sonnet-4-20250514',
		openai_api_key    TEXT,
		openai_model      TEXT NOT NULL DEFAULT 'gpt-4o',
		gemini_api_key    TEXT,
		gemini_model      TEXT NOT NULL DEFAULT 'gemini-2.5-flash',
		custom_endpoint   TEXT,
		custom_api_key    TEXT,
		custom_model      TEXT,
		max_tokens        INTEGER NOT NULL DEFAULT 4096,
		temperature       REAL NOT NULL DEFAULT 0.3,
		system_prompt     TEXT NOT NULL DEFAULT 'You are a 5G/4G SA Core network expert assistant. You help operators troubleshoot issues, analyze logs, explain anomalies, and optimize network configuration. You have deep knowledge of 3GPP specifications (TS 23.501, TS 24.501, TS 33.501, TS 38.413, etc.) and the SA Core architecture.',
		rag_enabled       INTEGER NOT NULL DEFAULT 0,
		vectorstore_path  TEXT NOT NULL DEFAULT 'vectorstore.db'
	)`,

	// Conversation history
	`CREATE TABLE IF NOT EXISTS ai_conversations (
		id              INTEGER PRIMARY KEY AUTOINCREMENT,
		session_id      TEXT NOT NULL,
		role            TEXT NOT NULL CHECK (role IN ('system','user','assistant')),
		content         TEXT NOT NULL,
		provider        TEXT,
		model           TEXT,
		tokens_used     INTEGER,
		latency_ms      INTEGER,
		timestamp       REAL NOT NULL
	)`,
	"CREATE INDEX IF NOT EXISTS idx_ai_conv_session ON ai_conversations(session_id)",
	"CREATE INDEX IF NOT EXISTS idx_ai_conv_ts ON ai_conversations(timestamp)",
}

// Status returns current state.
func Status() map[string]any {
	_ = engine.Open
	return map[string]any{"status": "ready", "tables": len(DDL)}
}
