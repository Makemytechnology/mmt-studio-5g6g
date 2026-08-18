// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ai — AI-powered network operations: config, router, chat API.
//
// Go port of oam/ai/ai_config.py + ai_router.py + ai_api.py.
//
// Central entry points:
//   - Config CRUD: GetConfig, UpdateConfig
//   - Conversation history: SaveConversation, GetConversation, ListSessions
//   - AI Router: Router.Query (dispatches to active provider)
//
// The /api/ai/chat endpoint and other AI REST handlers live here too.
package ai

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/ai/providers"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("ai")

// ValidProviders is the set of recognized provider names.
var ValidProviders = map[string]bool{
	"local": true, "anthropic": true, "openai": true,
	"gemini": true, "custom": true,
}

// ════════════════════════════════════════════════════════════
// Config — ai_config table (single row, id=1)
// ════════════════════════════════════════════════════════════

// Config represents an ai_config row.
type Config struct {
	ActiveProvider  string  `json:"active_provider"`
	LocalEndpoint   string  `json:"local_endpoint"`
	LocalModel      string  `json:"local_model"`
	AnthropicAPIKey string  `json:"anthropic_api_key,omitempty"`
	AnthropicModel  string  `json:"anthropic_model"`
	OpenAIAPIKey    string  `json:"openai_api_key,omitempty"`
	OpenAIModel     string  `json:"openai_model"`
	GeminiAPIKey    string  `json:"gemini_api_key,omitempty"`
	GeminiModel     string  `json:"gemini_model"`
	CustomEndpoint  string  `json:"custom_endpoint,omitempty"`
	CustomAPIKey    string  `json:"custom_api_key,omitempty"`
	CustomModel     string  `json:"custom_model,omitempty"`
	MaxTokens       int     `json:"max_tokens"`
	Temperature     float64 `json:"temperature"`
	SystemPrompt    string  `json:"system_prompt"`
	RAGEnabled      bool    `json:"rag_enabled"`
	VectorstorePath string  `json:"vectorstore_path"`
}

func ensureConfig() {
	db, err := engine.Open()
	if err != nil {
		return
	}
	db.Exec("INSERT OR IGNORE INTO ai_config (id) VALUES (1)")
}

// GetConfig returns the current AI configuration.
func GetConfig() (*Config, error) {
	ensureConfig()
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT active_provider, local_endpoint, local_model,
		anthropic_api_key, anthropic_model, openai_api_key, openai_model,
		gemini_api_key, gemini_model, custom_endpoint, custom_api_key, custom_model,
		max_tokens, temperature, system_prompt, rag_enabled, vectorstore_path
		FROM ai_config WHERE id=1`)

	c := &Config{}
	var anthKey, oaiKey, gemKey, custEP, custKey, custModel sql.NullString
	var ragEnabled int
	err = row.Scan(&c.ActiveProvider, &c.LocalEndpoint, &c.LocalModel,
		&anthKey, &c.AnthropicModel, &oaiKey, &c.OpenAIModel,
		&gemKey, &c.GeminiModel, &custEP, &custKey, &custModel,
		&c.MaxTokens, &c.Temperature, &c.SystemPrompt, &ragEnabled, &c.VectorstorePath)
	if err == sql.ErrNoRows {
		return &Config{ActiveProvider: "local", LocalEndpoint: "http://localhost:11434",
			LocalModel: "llama3.2", MaxTokens: 4096, Temperature: 0.3}, nil
	}
	if err != nil {
		return nil, err
	}
	c.AnthropicAPIKey = anthKey.String
	c.OpenAIAPIKey = oaiKey.String
	c.GeminiAPIKey = gemKey.String
	c.CustomEndpoint = custEP.String
	c.CustomAPIKey = custKey.String
	c.CustomModel = custModel.String
	c.RAGEnabled = ragEnabled != 0
	return c, nil
}

// UpdateConfig updates AI configuration fields. Only updates provided fields.
func UpdateConfig(fields map[string]any) error {
	ensureConfig()
	if prov, ok := fields["active_provider"].(string); ok {
		if !ValidProviders[prov] {
			return fmt.Errorf("invalid provider %q — must be one of: local, anthropic, openai, gemini, custom", prov)
		}
	}

	validFields := map[string]bool{
		"active_provider": true, "local_endpoint": true, "local_model": true,
		"anthropic_api_key": true, "anthropic_model": true,
		"openai_api_key": true, "openai_model": true,
		"gemini_api_key": true, "gemini_model": true,
		"custom_endpoint": true, "custom_api_key": true, "custom_model": true,
		"max_tokens": true, "temperature": true, "system_prompt": true,
		"rag_enabled": true, "vectorstore_path": true,
	}

	var sets []string
	var vals []any
	for k, v := range fields {
		if !validFields[k] {
			continue
		}
		sets = append(sets, k+"=?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil
	}
	vals = append(vals, 1) // WHERE id=1

	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(fmt.Sprintf("UPDATE ai_config SET %s WHERE id=?",
		strings.Join(sets, ", ")), vals...)
	if err == nil {
		log.Info("AI config updated", "fields", sets)
	}
	return err
}

// ════════════════════════════════════════════════════════════
// Conversation History — ai_conversations table
// ════════════════════════════════════════════════════════════

// ConversationMessage is a single message in a conversation.
type ConversationMessage struct {
	Role      string  `json:"role"`
	Content   string  `json:"content"`
	Provider  string  `json:"provider,omitempty"`
	Model     string  `json:"model,omitempty"`
	TokensUsed *int   `json:"tokens_used,omitempty"`
	LatencyMS  *int   `json:"latency_ms,omitempty"`
	Timestamp float64 `json:"timestamp"`
}

// SaveConversation saves a message to conversation history.
func SaveConversation(sessionID, role, content, provider, model string,
	tokensUsed, latencyMS *int) {

	db, err := engine.Open()
	if err != nil {
		return
	}
	db.Exec(`INSERT INTO ai_conversations
		(session_id, role, content, provider, model, tokens_used, latency_ms, timestamp)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		sessionID, role, content, provider, model, tokensUsed, latencyMS, time.Now().Unix())
}

// GetConversation returns conversation history for a session.
func GetConversation(sessionID string, limit int) ([]ConversationMessage, error) {
	if limit <= 0 {
		limit = 50
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT role, content, provider, model, tokens_used, latency_ms, timestamp
		FROM ai_conversations WHERE session_id=? ORDER BY timestamp LIMIT ?`,
		sessionID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ConversationMessage
	for rows.Next() {
		var m ConversationMessage
		var prov, model sql.NullString
		var tokens, lat sql.NullInt64
		if err := rows.Scan(&m.Role, &m.Content, &prov, &model, &tokens, &lat, &m.Timestamp); err != nil {
			continue
		}
		m.Provider = prov.String
		m.Model = model.String
		if tokens.Valid {
			v := int(tokens.Int64)
			m.TokensUsed = &v
		}
		if lat.Valid {
			v := int(lat.Int64)
			m.LatencyMS = &v
		}
		out = append(out, m)
	}
	return out, nil
}

// SessionSummary represents a conversation session listing.
type SessionSummary struct {
	SessionID string  `json:"session_id"`
	Started   float64 `json:"started"`
	LastMsg   float64 `json:"last_msg"`
	Messages  int     `json:"messages"`
	Provider  string  `json:"provider,omitempty"`
}

// ListSessions returns recent conversation sessions.
func ListSessions(limit int) ([]SessionSummary, error) {
	if limit <= 0 {
		limit = 20
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT session_id, MIN(timestamp) as started, MAX(timestamp) as last_msg,
		COUNT(*) as messages, provider
		FROM ai_conversations GROUP BY session_id
		ORDER BY last_msg DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SessionSummary
	for rows.Next() {
		var s SessionSummary
		var prov sql.NullString
		if err := rows.Scan(&s.SessionID, &s.Started, &s.LastMsg, &s.Messages, &prov); err != nil {
			continue
		}
		s.Provider = prov.String
		out = append(out, s)
	}
	return out, nil
}

// ════════════════════════════════════════════════════════════
// Router — central AI query dispatcher
// ════════════════════════════════════════════════════════════

// Router routes queries to the active provider, manages history and context.
type Router struct{}

// DefaultRouter is the global AI router singleton.
var DefaultRouter = &Router{}

// QueryResult is the result of an AI query.
type QueryResult struct {
	Content   string `json:"content"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	TokensUsed int   `json:"tokens_used"`
	LatencyMS  int   `json:"latency_ms"`
	SessionID string `json:"session_id"`
	Error     string `json:"error,omitempty"`
}

// Query sends a prompt to the active provider with optional context and history.
func (r *Router) Query(prompt string, context map[string]any, sessionID string, rag bool) QueryResult {
	cfg, err := GetConfig()
	if err != nil {
		return QueryResult{Content: fmt.Sprintf("[Config error: %v]", err), Error: err.Error()}
	}
	providerName := cfg.ActiveProvider
	if providerName == "" {
		providerName = "local"
	}

	if sessionID == "" {
		sessionID = fmt.Sprintf("q-%d", time.Now().UnixNano())
	}

	// Inject context
	fullPrompt := prompt
	if context != nil {
		fullPrompt = prompt + "\n\n--- SA Core Context ---\n" + formatContext(context)
	}

	// Build message list with history
	messages := r.buildMessages(sessionID, cfg.SystemPrompt, fullPrompt)

	// Save user message
	SaveConversation(sessionID, "user", prompt, providerName, "", nil, nil)

	// Dispatch to provider
	prov := providers.Get(providerName)
	if prov == nil {
		return QueryResult{
			Content: fmt.Sprintf("[Unknown provider: %s]", providerName),
			Error:   "unknown_provider", SessionID: sessionID,
		}
	}

	result := prov.Query(providers.QueryParams{
		SystemPrompt: cfg.SystemPrompt,
		Messages:     toProviderMessages(messages),
		APIKey:       r.getAPIKey(providerName, cfg),
		Endpoint:     r.getEndpoint(providerName, cfg),
		Model:        r.getModel(providerName, cfg),
		MaxTokens:    cfg.MaxTokens,
		Temperature:  cfg.Temperature,
	})

	// Save assistant response
	tok := result.TokensUsed
	lat := result.LatencyMS
	SaveConversation(sessionID, "assistant", result.Content,
		result.Provider, result.Model, &tok, &lat)

	return QueryResult{
		Content:    result.Content,
		Model:      result.Model,
		Provider:   result.Provider,
		TokensUsed: result.TokensUsed,
		LatencyMS:  result.LatencyMS,
		SessionID:  sessionID,
		Error:      result.Error,
	}
}

// GetStatus returns AI service status.
func (r *Router) GetStatus() map[string]any {
	cfg, err := GetConfig()
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	providerName := cfg.ActiveProvider
	if providerName == "" {
		providerName = "local"
	}

	status := map[string]any{
		"active_provider": providerName,
		"model":           r.getModel(providerName, cfg),
		"max_tokens":      cfg.MaxTokens,
		"temperature":     cfg.Temperature,
		"rag_enabled":     cfg.RAGEnabled,
	}

	if providerName == "local" {
		status["provider_health"] = providers.CheckLocalHealth(cfg.LocalEndpoint)
	} else {
		status["provider_health"] = map[string]any{
			"available": r.getAPIKey(providerName, cfg) != "",
		}
	}

	return status
}

type chatMessage struct {
	Role    string
	Content string
}

func (r *Router) buildMessages(sessionID, systemPrompt, userPrompt string) []chatMessage {
	var msgs []chatMessage
	if systemPrompt != "" {
		msgs = append(msgs, chatMessage{Role: "system", Content: systemPrompt})
	}

	// Include recent history
	history, _ := GetConversation(sessionID, 10)
	for _, h := range history {
		if h.Role == "user" || h.Role == "assistant" {
			msgs = append(msgs, chatMessage{Role: h.Role, Content: h.Content})
		}
	}

	msgs = append(msgs, chatMessage{Role: "user", Content: userPrompt})
	return msgs
}

func (r *Router) getAPIKey(provider string, cfg *Config) string {
	switch provider {
	case "anthropic":
		return cfg.AnthropicAPIKey
	case "openai":
		return cfg.OpenAIAPIKey
	case "gemini":
		return cfg.GeminiAPIKey
	case "custom":
		return cfg.CustomAPIKey
	}
	return ""
}

func (r *Router) getEndpoint(provider string, cfg *Config) string {
	switch provider {
	case "local":
		return cfg.LocalEndpoint
	case "custom":
		return cfg.CustomEndpoint
	}
	return ""
}

func (r *Router) getModel(provider string, cfg *Config) string {
	switch provider {
	case "local":
		return cfg.LocalModel
	case "anthropic":
		return cfg.AnthropicModel
	case "openai":
		return cfg.OpenAIModel
	case "gemini":
		return cfg.GeminiModel
	case "custom":
		return cfg.CustomModel
	}
	return "unknown"
}

func toProviderMessages(msgs []chatMessage) []providers.Message {
	out := make([]providers.Message, len(msgs))
	for i, m := range msgs {
		out[i] = providers.Message{Role: m.Role, Content: m.Content}
	}
	return out
}

func formatContext(ctx map[string]any) string {
	var parts []string
	for k, v := range ctx {
		switch val := v.(type) {
		case map[string]any, []any:
			b, _ := json.MarshalIndent(val, "", "  ")
			parts = append(parts, fmt.Sprintf("%s: %s", k, string(b)))
		default:
			parts = append(parts, fmt.Sprintf("%s: %v", k, v))
		}
	}
	return strings.Join(parts, "\n")
}

// MaskAPIKey masks an API key for display (shows first 8 + last 4 chars).
func MaskAPIKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:8] + "..." + key[len(key)-4:]
}
