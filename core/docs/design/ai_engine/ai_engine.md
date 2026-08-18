# ai_engine — Design Document

AI assistant engine for the MMT 5G Core. Three thin packages
totalling **73 LOC** that expose a `Status()`/`List()` surface to
the GUI panel and reserve namespace for a future RAG pipeline +
assistant protocol.

## 1. Role / Scope

`ai_engine/` is mostly a placeholder — at the time of this snapshot
it owns only one piece of behaviour: reading the
`ai_conversations` table from `db/engine` and surfacing the row
count + list to the GUI panel. The two subpackages (`pipeline/`,
`protocol/`) are stubs that hold the future home of the RAG
pipeline and the assistant protocol respectively; both currently
just return `{"status": "ready"}`.

| Concern | Where | Status |
|---------|-------|--------|
| List recent assistant conversations | `ai_engine.List()` | Implemented (1000-row cap) |
| Aggregate count for GUI panel | `ai_engine.Status()` | Implemented |
| RAG pipeline | `ai_engine/pipeline/Status()` | **Stub** — returns `{"status": "ready"}` |
| Assistant protocol | `ai_engine/protocol/Status()` | **Stub** — returns `{"status": "ready"}` |

## 2. Architecture

```
┌───────────────────────────────────────────────────────────┐
│ GUI panel                                                 │
│   ai_engine.Status() / ai_engine.List()                   │
│   ai_engine/pipeline.Status()                             │
│   ai_engine/protocol.Status()                             │
└──────────────────────┬────────────────────────────────────┘
                       │
                       ▼
┌───────────────────────────────────────────────────────────┐
│ ai_engine                                                 │
│  List()    SELECT * FROM ai_conversations ORDER BY 1      │
│            LIMIT 1000                                     │
│  Status()  → {"count": len(List())}                       │
│                                                           │
│  ┌──────────────────────┐    ┌──────────────────────────┐ │
│  │ ai_engine/pipeline   │    │ ai_engine/protocol       │ │
│  │  Status() — stub     │    │  Status() — stub         │ │
│  │  (RAG pipeline TBD)  │    │  (assistant proto TBD)   │ │
│  └──────────────────────┘    └──────────────────────────┘ │
└──────────────────────┬────────────────────────────────────┘
                       │ engine.Open / db.Query
                       ▼
┌───────────────────────────────────────────────────────────┐
│ db/engine — SQLite                                        │
│   ai_conversations                                        │
└───────────────────────────────────────────────────────────┘
```

## 3. File / Package Map

| File | LOC | Role |
|------|-----|------|
| `ai_engine/ai_engine.go` | 39 | `List()` + `Status()` for the GUI panel |
| `ai_engine/pipeline/pipeline.go` | 17 | RAG pipeline placeholder — `Status()` |
| `ai_engine/protocol/protocol.go` | 17 | Assistant protocol placeholder — `Status()` |

## 4. Public API

```go
// ai_engine — list rows from ai_conversations (capped at 1000).
func List() ([]map[string]any, error)

// ai_engine — GUI panel surface; returns {"count": <n>}.
func Status() map[string]any

// ai_engine/pipeline — placeholder.
func Status() map[string]any   // {"status": "ready"}

// ai_engine/protocol — placeholder.
func Status() map[string]any   // {"status": "ready"}
```

`List()` opens a fresh `engine.Open()` per call, runs
`SELECT * FROM ai_conversations ORDER BY 1 LIMIT 1000`, and
materialises every row as `map[string]any` keyed by column name
(`ai_engine.go:11-30`). Errors during `db.Query` are silently
swallowed (`return nil, nil` at `ai_engine.go:16`); errors during
per-row `Scan` skip the row (`ai_engine.go:24`).

## 5. Lifecycle

There is no lifecycle proper — both `Status()` calls in
`pipeline/` and `protocol/` are **stubs**:

```go
func Status() map[string]any {
    log := logger.Get("pipeline")  // / "protocol"
    _ = log
    _ = engine.Open
    return map[string]any{"status": "ready"}
}
```

`_ = log` and `_ = engine.Open` are intentional reservations of
the `oam/logger` and `db/engine` imports so the eventual real
implementation slots in without an import diff.

## 6. Key Types

None. The package exports no struct types — `List()` returns
`[]map[string]any` and `Status()` returns `map[string]any`.

## 7. Stubs / TODOs

The entire `pipeline/` and `protocol/` packages are stubs. There
are no `TODO` markers in source — the placeholder shape is signalled
by the bodies returning `{"status": "ready"}` and the `_ = log` /
`_ = engine.Open` import reservations.

Future scope (inferred from package doc comments — `// Package
pipeline -- RAG pipeline.` and `// Package protocol -- AI assistant
protocol.`):

- **pipeline/** — retrieval-augmented-generation pipeline (chunk
  / embed / retrieve / answer).
- **protocol/** — assistant protocol (request / response shape;
  streaming token surface; tool-call envelope).

## 8. References

No spec citations exist in source — `ai_engine/` is a product
namespace, not a 3GPP-derived one. The only external references
are imports of in-repo packages:

- `github.com/mmt/mmt-studio-core/db/engine` — SQLite handle.
- `github.com/mmt/mmt-studio-core/oam/logger` — logger factory.

---
*Last refreshed against commit `13a181d`.*
