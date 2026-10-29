# oam/ai — AI-Assisted Operations

## 1. Role / scope

`oam/ai` is the closed-loop and operator-chat plane of the core. It
glues four things together:

1. **A pluggable LLM router.** Local Ollama, Anthropic, OpenAI,
   Gemini, or a custom OpenAI-compatible endpoint. Active provider
   is one row in `ai_config`.
2. **A self-healing controller.** Watches the health watchdog feed,
   counts consecutive degraded checks per NF, and after a threshold
   raises an alarm or calls a recovery hook.
3. **An anomaly responder.** Consumes NWDAF `ABNORMAL_BEHAVIOUR`
   analytics; rule-based fast-path for known patterns
   (`AUTH_FAILURE_SPIKE`, `MAC_VERIFICATION_FAILURES`,
   `SESSION_FAILURE_SPIKE`); for unknown patterns asks the LLM to
   classify + recommend an action and returns it for human-in-the-loop
   approval.
4. **Conversation history.** Every operator chat (`/api/ai/chat`) and
   internal AI call gets a session id and rows in `ai_conversations`.

Stubs at `oam/ai/hooks/` (NF event-bus hooks for troubleshoot_ue,
analyze_logs, analyze_signalling_trace) and `oam/ai/rag/`
(vectorstore / 3GPP spec embedding) are P3 placeholders; the
package-header comments describe what they will do once a Go vector
library and the NF event bus arrive.

## 2. Architecture

```
   GUI / REST                       NF event bus (pm + health watchdog)
       |                                       |
       v                                       v
  +----+--------------------+        +---------+----------------+
  | oam/ai (Router)         |        | autonomous.EvaluateAndHeal |
  |  Query(prompt, ctx, ses)|        |   (per-NF degraded counter) |
  +----+--+-----------------+        +---------+----------------+
       |  |                                    |
       |  | history                            v
       |  v                          +---------+----------------+
       | +--------------------+      | autonomous.EvaluateAndRespond
       | | ai_conversations   |      |  fastPathResponse (rule)   |
       | +--------------------+      |  aiClassifyAndRecommend    |
       |                             +---------+----------------+
       v                                       |
  +----+----------------+      LLM call        |
  | providers (Get(name))| <-------------------+
  +----+----------------+
       |
       +--> LocalProvider     (Ollama @ http://localhost:11434)
       +--> AnthropicProvider (api.anthropic.com)
       +--> OpenAIProvider    (api.openai.com)
       +--> GeminiProvider    (generativelanguage.googleapis.com)
       +--> CustomProvider    (OpenAI-compat URL from infra_config)
```

Hot loop avoidance: the autonomous package never imports `oam/ai`
directly. Instead `ai.SetAIQueryFunc` injects a function pointer at
boot (`autonomous.go:283`) so the AI router can be called without
producing an import cycle.

## 3. File map

| File | LOC | Role |
|---|---:|---|
| `oam/ai/ai.go` | 462 | `Config`, conversation history, `Router.Query`, `Router.GetStatus` |
| `oam/ai/providers/providers.go` | 596 | `Provider` interface + Local / Anthropic / OpenAI / Gemini / Custom impls |
| `oam/ai/autonomous/autonomous.go` | 328 | self-healing closed loop + anomaly response (fast-path + AI fallback) |
| `oam/ai/db/aidb.go` | 56 | DDL for `ai_config` (1-row) + `ai_conversations` |
| `oam/ai/hooks/hooks.go` | 19 | stub — NF event-bus hooks (P3) |
| `oam/ai/rag/rag.go` | 13 | stub — RAG vectorstore (P3) |

## 4. Public API / contracts — closed-loop & analytics integration points

### `ai.Router` — central LLM dispatcher (`oam/ai/ai.go`)

```go
type QueryResult struct {
    Content    string
    Model      string
    Provider   string
    TokensUsed int
    LatencyMS  int
    SessionID  string
    Error      string
}

func (r *Router) Query(prompt string, context map[string]any,
                       sessionID string, rag bool) QueryResult
func (r *Router) GetStatus() map[string]any
```

`Query` flow:

1. Loads `ai_config` (creates singleton row with `id=1` if absent).
2. Synthesises `sessionID` (`q-<unix-nano>`) when blank.
3. Optionally appends a "--- SA Core Context ---" block from the
   `context` map (used by NF callers to attach state snapshots).
4. Builds a chat-message list from the system prompt, the last 10
   `ai_conversations` rows for this session, and the new user
   message.
5. Saves the user message, dispatches to `providers.Get(name)`,
   saves the assistant response (with tokens + latency).
6. Returns a flat `QueryResult` regardless of provider — caller
   never sees provider-specific JSON.

`GetStatus` reports active provider + model + RAG enabled flag, and
for `local` calls `providers.CheckLocalHealth(endpoint)` to confirm
Ollama is up and list installed models.

### `Config` — `ai_config` row (`oam/ai/ai.go:40-58`)

| Field | Use |
|---|---|
| `ActiveProvider` | one of `local`, `anthropic`, `openai`, `gemini`, `custom` |
| `LocalEndpoint`, `LocalModel` | Ollama defaults `http://localhost:11434`, `llama3.2` |
| `AnthropicAPIKey`, `AnthropicModel` | Anthropic Messages API |
| `OpenAIAPIKey`, `OpenAIModel` | OpenAI Chat Completions |
| `GeminiAPIKey`, `GeminiModel` | Google Gemini generateContent |
| `CustomEndpoint`, `CustomAPIKey`, `CustomModel` | OpenAI-compat URL |
| `MaxTokens`, `Temperature` | shared decoding params |
| `SystemPrompt` | system message prepended to every chat |
| `RAGEnabled`, `VectorstorePath` | RAG plumbing — stubbed today |

`UpdateConfig(map[string]any)` validates `active_provider` against
`ValidProviders` and only writes whitelisted columns.

### `providers` package contract

```go
type Provider interface {
    Name() string
    Query(p QueryParams) QueryResult
}
```

`registry` (`providers.go:537-543`) maps names to singletons; `Get`
returns nil for unknown names (Router converts that to a typed
error). All providers normalise to the same `QueryResult` shape
(`Content`, `Model`, `Provider`, `TokensUsed`, `LatencyMS`,
`Error`).

### Closed-loop integration points (`oam/ai/autonomous`)

**Self-healing** (`autonomous.go:31-126`):

```go
const degradedThreshold = 3 // consecutive checks before action

func EvaluateAndHeal(healthResult map[string]any) *HealingResult
func GetSelfHealingStatus() map[string]int
```

Caller passes a `{"nfs": {<nfName>: {"status": "healthy"|"degraded"|...}}}`
shape. For each NF that's not `healthy`, increments
`degradedCounts[nfName]`; on the third consecutive non-healthy check,
calls `healNF(nfName)` — currently a generic alarm-raise action; the
header at `autonomous.go:117-120` notes per-NF handlers (AMF context
flush, SMF pool check, UPF thread check, DB integrity) are wired in
once the corresponding NF packages are exposed.

**Anomaly response** (`autonomous.go:128-273`):

```go
func EvaluateAndRespond(anomalyResult map[string]any) *AnomalyResult
```

Caller passes `{"result": {"alerts": [{type, severity, detail}, ...]}}`
(the NWDAF analytics shape). Per alert:

1. Tries the rule-based fast path (`fastPathResponse`):

   | Alert type | Action | Auto |
   |---|---|---|
   | `AUTH_FAILURE_SPIKE` | `increase_ids_threshold` (3 / 30s) | yes |
   | `MAC_VERIFICATION_FAILURES` | `raise_critical_alarm` security/NAS | yes |
   | `SESSION_FAILURE_SPIKE` | `check_upf_health` | yes |

2. On miss, calls `aiClassifyAndRecommend` -> `getAIQueryFunc()` ->
   AI router with a structured prompt asking for
   `{"classification","action","reason"}` JSON. Action is one of
   `block_imsi | rate_limit_gnb | adjust_qos | raise_alarm | ignore`.
   Returns with `AutoExecuted=false` (human-in-the-loop) — operator
   approves before action lands.

**Decoupling.** `autonomous` does NOT import `ai`. The hook is set
at boot:

```go
// oam/ai/autonomous/autonomous.go:283
var aiQueryFunc func(prompt, sessionID string) string

func SetAIQueryFunc(fn func(prompt, sessionID string) string)
```

The `oam/ai` init wires this so autonomous can call back without a
cycle.

### Conversation history

```go
func SaveConversation(sessionID, role, content, provider, model string,
                      tokensUsed, latencyMS *int)
func GetConversation(sessionID string, limit int) ([]ConversationMessage, error)
func ListSessions(limit int) ([]SessionSummary, error)
```

`ai_conversations` row shape (DDL at `oam/ai/db/aidb.go:37-47`):
session_id, role (system|user|assistant), content, provider, model,
tokens_used, latency_ms, timestamp (unix seconds). Indexes on
`session_id` and `timestamp`.

## 5. Headline flows / lifecycle

### Operator chat (`/api/ai/chat`)

1. Webservice route calls `Router.Query(prompt, context, sessionID,
   rag)` with the request body.
2. `GetConfig` -> active provider name.
3. History (last 10) + system prompt assembled into the message
   list.
4. User message persisted.
5. `providers.Get(name).Query(...)` runs:
   - **Local:** POST to `<endpoint>/api/chat`, 120 s timeout. On
     unreachable Ollama returns a friendly install hint string.
   - **Anthropic:** POST to `api.anthropic.com/v1/messages` with
     `x-api-key` and `anthropic-version: 2023-06-01`.
   - **OpenAI:** POST to `api.openai.com/v1/chat/completions` with
     `Authorization: Bearer ...`.
   - **Gemini:** POST to
     `generativelanguage.googleapis.com/.../models/<m>:generateContent?key=...`,
     mapping `assistant` role to Gemini's `model`.
   - **Custom:** OpenAI-compat — POST to
     `<endpoint>/v1/chat/completions`.
6. Response normalised to `QueryResult`; assistant message persisted
   with tokens + latency.

### Self-healing tick

Health watchdog calls `autonomous.EvaluateAndHeal(healthResult)`
each cycle. The function locks `degradedMu`, walks the per-NF map:

- `status=="healthy"` -> reset counter, continue.
- otherwise increment. Below threshold -> log DEBUG, continue.
- At threshold -> `healNF(nfName)` produces a `HealingAction`
  (currently `alarm_raised` with a sustained-degradation reason),
  resets the counter, appends to the result.

Operator visibility: `GetSelfHealingStatus()` returns the
non-zero counters; `Status()` includes them under
`{"self_healing": {"degraded_nfs": ...}}`.

### Anomaly classification (unknown alert)

1. Build classification prompt (`autonomous.go:235-246`) with the
   alert type / severity / detail.
2. Invoke `getAIQueryFunc()(prompt, "anomaly-classify")`. If unset
   (no AI configured), return nil — caller silently drops.
3. Extract the first `{...}` JSON block via regex.
4. On parse success: `AnomalyAction{Action,Reason,Classification,
   AutoExecuted=false}`. On parse failure: `raise_alarm` with the
   raw response truncated to 200 chars.

## 6. Stubs / TODOs

- `oam/ai/hooks/hooks.go:5-7` — NF-event-bus hooks (`troubleshoot_ue`,
  `analyze_logs`, `analyze_signalling_trace`) are not yet wired;
  `Status()` returns "ready" so the GUI panel renders.
- `oam/ai/rag/rag.go:5-7` — vectorstore / 3GPP spec embedding deferred
  until a Go vector library is integrated (`Config.VectorstorePath`
  defaults to `vectorstore.db`; `Config.RAGEnabled` is plumbed but
  has no consumer yet).
- `oam/ai/autonomous/autonomous.go:117-120` — `healNF` is generic; the
  per-NF handlers (AMF context flush, SMF pool check, UPF thread
  check, DB integrity check) land when those NF packages expose
  programmatic recovery hooks.

## 7. References

This package cites no 3GPP TS clauses in source — it sits above the
3GPP-defined surfaces. The systems it integrates with carry their
own §-cites:

- NWDAF analytics shape consumed by `EvaluateAndRespond` originates
  in `nf/nwdaf/`; the alert types are defined there.
- Self-healing input is the health-watchdog shape from
  `infra/health`.
- Conversation history table DDL defaults the system prompt to a
  string referencing TS 23.501 / 24.501 / 33.501 / 38.413 — those
  are in the prompt, not in code logic.

---
*Last refreshed against commit `13a181d`.*
