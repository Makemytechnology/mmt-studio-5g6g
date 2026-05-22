// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package providers — LLM provider abstraction + implementations.
//
// Go port of oam/ai/providers/. Defines the Provider interface and concrete
// implementations for Local (Ollama), Anthropic, OpenAI, Gemini, and Custom
// endpoints. Each provider implements the same Query method, returning a
// uniform QueryResult.
package providers

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("ai.providers")

// Message is a chat message (system / user / assistant).
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// QueryResult is the uniform response from any provider.
type QueryResult struct {
	Content   string `json:"content"`
	Model     string `json:"model"`
	Provider  string `json:"provider"`
	TokensUsed int   `json:"tokens_used"`
	LatencyMS  int   `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// QueryParams holds parameters for a provider query.
type QueryParams struct {
	Prompt       string
	SystemPrompt string
	Messages     []Message
	APIKey       string
	Endpoint     string
	Model        string
	MaxTokens    int
	Temperature  float64
}

// Provider is the LLM provider interface.
type Provider interface {
	Name() string
	Query(p QueryParams) QueryResult
}

// ════════════════════════════════════════════════════════════
// Local Provider (Ollama / llama.cpp)
// ════════════════════════════════════════════════════════════

// LocalProvider queries a local Ollama instance.
type LocalProvider struct{}

func (lp *LocalProvider) Name() string { return "local" }

func (lp *LocalProvider) Query(p QueryParams) QueryResult {
	endpoint := p.Endpoint
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	model := p.Model
	if model == "" {
		model = "llama3.2"
	}
	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	messages := p.Messages
	if messages == nil {
		messages = buildMessages(p.SystemPrompt, p.Prompt)
	}

	payload := map[string]any{
		"model":    model,
		"messages": messages,
		"stream":   false,
		"options": map[string]any{
			"num_predict": maxTokens,
			"temperature": p.Temperature,
		},
	}
	body, _ := json.Marshal(payload)
	url := strings.TrimRight(endpoint, "/") + "/api/chat"

	start := time.Now()
	resp, err := httpPost(url, body, nil, 120*time.Second)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		log.Warn("local LLM not reachable", "endpoint", endpoint, "err", err)
		return QueryResult{
			Content:  fmt.Sprintf("[Local LLM unavailable at %s. Install Ollama: curl -fsSL https://ollama.com/install.sh | sh && ollama pull %s]", endpoint, model),
			Model:    model, Provider: "local", LatencyMS: latency, Error: err.Error(),
		}
	}

	var data map[string]any
	json.Unmarshal(resp, &data)

	content := ""
	if msg, ok := data["message"].(map[string]any); ok {
		content, _ = msg["content"].(string)
	}
	evalCount, _ := data["eval_count"].(float64)
	promptCount, _ := data["prompt_eval_count"].(float64)

	return QueryResult{
		Content:    content,
		Model:      model,
		Provider:   "local",
		TokensUsed: int(evalCount + promptCount),
		LatencyMS:  latency,
	}
}

// CheckLocalHealth checks if Ollama is running and responsive.
func CheckLocalHealth(endpoint string) map[string]any {
	if endpoint == "" {
		endpoint = "http://localhost:11434"
	}
	url := strings.TrimRight(endpoint, "/") + "/api/tags"
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return map[string]any{"available": false, "models": []string{}}
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	var data map[string]any
	json.Unmarshal(b, &data)

	var models []string
	if modelList, ok := data["models"].([]any); ok {
		for _, m := range modelList {
			if mm, ok := m.(map[string]any); ok {
				if name, ok := mm["name"].(string); ok {
					models = append(models, name)
				}
			}
		}
	}
	return map[string]any{"available": true, "models": models}
}

// ════════════════════════════════════════════════════════════
// Anthropic Provider
// ════════════════════════════════════════════════════════════

const (
	anthropicAPIURL     = "https://api.anthropic.com/v1/messages"
	anthropicAPIVersion = "2023-06-01"
)

// AnthropicProvider queries the Anthropic Messages API.
type AnthropicProvider struct{}

func (ap *AnthropicProvider) Name() string { return "anthropic" }

func (ap *AnthropicProvider) Query(p QueryParams) QueryResult {
	if p.APIKey == "" {
		return QueryResult{
			Content: "[Anthropic API key not configured. Set in AI Settings.]",
			Provider: "anthropic", Error: "No API key",
		}
	}
	model := p.Model
	if model == "" {
		model = "claude-sonnet-4-20250514"
	}
	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	messages := p.Messages
	if messages == nil {
		messages = []Message{{Role: "user", Content: p.Prompt}}
	}
	// Anthropic takes system separately — filter system messages out
	var apiMsgs []Message
	for _, m := range messages {
		if m.Role != "system" {
			apiMsgs = append(apiMsgs, m)
		}
	}

	payloadMap := map[string]any{
		"model":      model,
		"max_tokens": maxTokens,
		"temperature": p.Temperature,
		"messages":   apiMsgs,
	}
	if p.SystemPrompt != "" {
		payloadMap["system"] = p.SystemPrompt
	}
	body, _ := json.Marshal(payloadMap)

	headers := map[string]string{
		"Content-Type":      "application/json",
		"x-api-key":         p.APIKey,
		"anthropic-version": anthropicAPIVersion,
	}

	start := time.Now()
	resp, err := httpPostWithHeaders(anthropicAPIURL, body, headers, 120*time.Second)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		log.Error("Anthropic API error", "err", err)
		return QueryResult{
			Content: fmt.Sprintf("[Anthropic API error: %v]", err),
			Provider: "anthropic", Error: err.Error(), LatencyMS: latency,
		}
	}

	var data map[string]any
	json.Unmarshal(resp, &data)

	content := ""
	if blocks, ok := data["content"].([]any); ok {
		for _, block := range blocks {
			if bm, ok := block.(map[string]any); ok {
				if bm["type"] == "text" {
					if t, ok := bm["text"].(string); ok {
						content += t
					}
				}
			}
		}
	}

	tokens := 0
	if usage, ok := data["usage"].(map[string]any); ok {
		inTok, _ := usage["input_tokens"].(float64)
		outTok, _ := usage["output_tokens"].(float64)
		tokens = int(inTok + outTok)
	}
	respModel := model
	if rm, ok := data["model"].(string); ok {
		respModel = rm
	}

	return QueryResult{
		Content: content, Model: respModel, Provider: "anthropic",
		TokensUsed: tokens, LatencyMS: latency,
	}
}

// ════════════════════════════════════════════════════════════
// OpenAI Provider
// ════════════════════════════════════════════════════════════

const openaiAPIURL = "https://api.openai.com/v1/chat/completions"

// OpenAIProvider queries the OpenAI Chat Completions API.
type OpenAIProvider struct{}

func (op *OpenAIProvider) Name() string { return "openai" }

func (op *OpenAIProvider) Query(p QueryParams) QueryResult {
	if p.APIKey == "" {
		return QueryResult{
			Content: "[OpenAI API key not configured. Set in AI Settings.]",
			Provider: "openai", Error: "No API key",
		}
	}
	model := p.Model
	if model == "" {
		model = "gpt-4o"
	}
	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	messages := p.Messages
	if messages == nil {
		messages = buildMessages(p.SystemPrompt, p.Prompt)
	}

	payload := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"temperature": p.Temperature,
	}
	body, _ := json.Marshal(payload)

	headers := map[string]string{
		"Content-Type":  "application/json",
		"Authorization": "Bearer " + p.APIKey,
	}

	start := time.Now()
	resp, err := httpPostWithHeaders(openaiAPIURL, body, headers, 120*time.Second)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		log.Error("OpenAI API error", "err", err)
		return QueryResult{
			Content: fmt.Sprintf("[OpenAI API error: %v]", err),
			Provider: "openai", Error: err.Error(), LatencyMS: latency,
		}
	}

	var data map[string]any
	json.Unmarshal(resp, &data)

	content := ""
	if choices, ok := data["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				content, _ = msg["content"].(string)
			}
		}
	}

	tokens := 0
	if usage, ok := data["usage"].(map[string]any); ok {
		total, _ := usage["total_tokens"].(float64)
		tokens = int(total)
	}
	respModel := model
	if rm, ok := data["model"].(string); ok {
		respModel = rm
	}

	return QueryResult{
		Content: content, Model: respModel, Provider: "openai",
		TokensUsed: tokens, LatencyMS: latency,
	}
}

// ════════════════════════════════════════════════════════════
// Gemini Provider
// ════════════════════════════════════════════════════════════

const geminiAPIBase = "https://generativelanguage.googleapis.com/v1beta/models"

// GeminiProvider queries the Google Gemini generateContent API.
type GeminiProvider struct{}

func (gp *GeminiProvider) Name() string { return "gemini" }

func (gp *GeminiProvider) Query(p QueryParams) QueryResult {
	if p.APIKey == "" {
		return QueryResult{
			Content: "[Gemini API key not configured. Set in AI Settings.]",
			Provider: "gemini", Error: "No API key",
		}
	}
	model := p.Model
	if model == "" {
		model = "gemini-2.5-flash"
	}
	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	// Build Gemini content format
	var contents []map[string]any
	if p.Messages != nil {
		for _, msg := range p.Messages {
			if msg.Role == "system" {
				continue // handled via systemInstruction
			}
			role := "user"
			if msg.Role == "assistant" {
				role = "model"
			}
			contents = append(contents, map[string]any{
				"role":  role,
				"parts": []map[string]string{{"text": msg.Content}},
			})
		}
	} else {
		contents = []map[string]any{
			{"role": "user", "parts": []map[string]string{{"text": p.Prompt}}},
		}
	}

	payloadMap := map[string]any{
		"contents": contents,
		"generationConfig": map[string]any{
			"maxOutputTokens": maxTokens,
			"temperature":     p.Temperature,
		},
	}
	if p.SystemPrompt != "" {
		payloadMap["systemInstruction"] = map[string]any{
			"parts": []map[string]string{{"text": p.SystemPrompt}},
		}
	}
	body, _ := json.Marshal(payloadMap)
	url := fmt.Sprintf("%s/%s:generateContent?key=%s", geminiAPIBase, model, p.APIKey)

	start := time.Now()
	resp, err := httpPost(url, body, nil, 120*time.Second)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		log.Error("Gemini API error", "err", err)
		return QueryResult{
			Content: fmt.Sprintf("[Gemini API error: %v]", err),
			Provider: "gemini", Error: err.Error(), LatencyMS: latency,
		}
	}

	var data map[string]any
	json.Unmarshal(resp, &data)

	content := ""
	if candidates, ok := data["candidates"].([]any); ok {
		for _, cand := range candidates {
			if cm, ok := cand.(map[string]any); ok {
				if cont, ok := cm["content"].(map[string]any); ok {
					if parts, ok := cont["parts"].([]any); ok {
						for _, part := range parts {
							if pm, ok := part.(map[string]any); ok {
								if t, ok := pm["text"].(string); ok {
									content += t
								}
							}
						}
					}
				}
			}
		}
	}

	tokens := 0
	if usage, ok := data["usageMetadata"].(map[string]any); ok {
		total, _ := usage["totalTokenCount"].(float64)
		tokens = int(total)
	}

	return QueryResult{
		Content: content, Model: model, Provider: "gemini",
		TokensUsed: tokens, LatencyMS: latency,
	}
}

// ════════════════════════════════════════════════════════════
// Custom Provider (OpenAI-compatible endpoint)
// ════════════════════════════════════════════════════════════

// CustomProvider queries an OpenAI-compatible custom endpoint.
type CustomProvider struct{}

func (cp *CustomProvider) Name() string { return "custom" }

func (cp *CustomProvider) Query(p QueryParams) QueryResult {
	if p.Endpoint == "" {
		return QueryResult{
			Content: "[Custom endpoint not configured. Set in AI Settings.]",
			Provider: "custom", Error: "No endpoint",
		}
	}
	model := p.Model
	maxTokens := p.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 4096
	}

	messages := p.Messages
	if messages == nil {
		messages = buildMessages(p.SystemPrompt, p.Prompt)
	}

	payload := map[string]any{
		"model":      model,
		"messages":   messages,
		"max_tokens": maxTokens,
		"temperature": p.Temperature,
	}
	body, _ := json.Marshal(payload)

	headers := map[string]string{
		"Content-Type": "application/json",
	}
	if p.APIKey != "" {
		headers["Authorization"] = "Bearer " + p.APIKey
	}

	url := strings.TrimRight(p.Endpoint, "/") + "/v1/chat/completions"

	start := time.Now()
	resp, err := httpPostWithHeaders(url, body, headers, 120*time.Second)
	latency := int(time.Since(start).Milliseconds())
	if err != nil {
		log.Error("Custom provider error", "err", err)
		return QueryResult{
			Content: fmt.Sprintf("[Custom provider error: %v]", err),
			Provider: "custom", Error: err.Error(), LatencyMS: latency,
		}
	}

	// Parse OpenAI-compatible response
	var data map[string]any
	json.Unmarshal(resp, &data)

	content := ""
	if choices, ok := data["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if msg, ok := choice["message"].(map[string]any); ok {
				content, _ = msg["content"].(string)
			}
		}
	}
	tokens := 0
	if usage, ok := data["usage"].(map[string]any); ok {
		total, _ := usage["total_tokens"].(float64)
		tokens = int(total)
	}

	return QueryResult{
		Content: content, Model: model, Provider: "custom",
		TokensUsed: tokens, LatencyMS: latency,
	}
}

// ════════════════════════════════════════════════════════════
// Registry — get provider by name
// ════════════════════════════════════════════════════════════

var registry = map[string]Provider{
	"local":     &LocalProvider{},
	"anthropic": &AnthropicProvider{},
	"openai":    &OpenAIProvider{},
	"gemini":    &GeminiProvider{},
	"custom":    &CustomProvider{},
}

// Get returns a Provider by name, or nil if unknown.
func Get(name string) Provider {
	return registry[name]
}

// ════════════════════════════════════════════════════════════
// HTTP helpers
// ════════════════════════════════════════════════════════════

func buildMessages(systemPrompt, userPrompt string) []Message {
	var msgs []Message
	if systemPrompt != "" {
		msgs = append(msgs, Message{Role: "system", Content: systemPrompt})
	}
	msgs = append(msgs, Message{Role: "user", Content: userPrompt})
	return msgs
}

func httpPost(url string, body []byte, _ map[string]string, timeout time.Duration) ([]byte, error) {
	return httpPostWithHeaders(url, body, map[string]string{"Content-Type": "application/json"}, timeout)
}

func httpPostWithHeaders(url string, body []byte, headers map[string]string, timeout time.Duration) ([]byte, error) {
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequest("POST", url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return respBody, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody[:min(len(respBody), 200)]))
	}
	return respBody, nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
