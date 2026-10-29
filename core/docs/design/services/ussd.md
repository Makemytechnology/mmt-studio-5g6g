# ussd — USSD over IMS / CS

## 1. Role / scope

`services/ussd/` implements the network-side state for USSD (Unstructured
Supplementary Service Data) sessions per TS 24.390 — the IP-Multimedia
binding of TS 22.090 USSD. The package owns two SQL tables
(`ussd_menus`, `ussd_sessions`) plus an in-memory
`activeSessions` map keyed by session ID. It does NOT yet emit on-
the-wire USSD framing (the legacy MAP `processUnstructuredSS-Request`
op-code 59 lives under `services/supplementary/codec.go` as a stub —
see that file's TODOs for TS 24.090 / TS 24.080 §2.5).

The headline customer-visible feature is a default menu tree (Main
Menu → Balance / Data / Top-up / My Number / Customer Care) seeded by
`SeedDefaultMenus` and reachable via `*100#`, plus quick codes
`*123#` (balance) and `*124#` (data) (`ussd.go:206-230`).

## 2. File map

| File | LOC | Role |
|------|-----|------|
| `ussd.go` | 446 | Menu CRUD + Session FSM + topup execution |

## 3. Wire / API surface

There is no SIP/MAP wire codec in this package. The interactive surface
is in-process Go; the transport bridge (REGISTER routing, MESSAGE-
based USSD-over-SIP per TS 24.390) is the caller's responsibility.

### Operator REST API (`/api/ussd/*`)

Wired in `webservice/app/routes_ussd.go`. All responses follow the
`{ok: true, ...}` envelope.

| Method | Path                            | Purpose |
|--------|---------------------------------|---------|
| GET    | `/api/ussd/status`              | `{ok, status: {count, items}}` for the panel header. |
| GET    | `/api/ussd/menus`               | Full menu tree, ordered by `display_order, id`. |
| POST   | `/api/ussd/menus`               | Create node. Top-level requires `code`; children are positioned by `display_order`. |
| PATCH  | `/api/ussd/menus/{id}`          | Allow-listed update (`title, code, action_type, action_data, display_order, parent_id`). |
| DELETE | `/api/ussd/menus/{id}`          | Remove node (children referencing it remain — schema doesn't CASCADE). |
| POST   | `/api/ussd/menus/seed`          | Seed the default tree (idempotent — only fires when empty). |
| POST   | `/api/ussd/session`             | `{imsi, ussd_string}` → InitiateSession; bad code → 400. |
| POST   | `/api/ussd/session/{id}/continue` | `{input}` → ContinueSession; user input drives the menu walker. |
| POST   | `/api/ussd/session/{id}/end`    | EndSession; marks state=`completed`. |
| GET    | `/api/ussd/sessions`            | List, optional `?imsi=&state=` filters. |

Before this surface landed, the routes were 4-line stubs in
`routes_nsaas.go` (`emptyArrayRoute` + `{ok:true}` no-ops); the
package's CRUD + session machinery was unreachable from the panel
or from the tester. The current surface replaces those stubs.

### Menu CRUD

| Function | Notes |
|----------|-------|
| `ListMenus()` | Tree by `display_order`, `id` |
| `GetMenuByCode(code)` | Lookup short code (`*100#`) |
| `GetChildren(parentID)` | Render submenu |
| `CreateMenu / UpdateMenu / DeleteMenu` | CRUD |

### Session interaction (TS 24.390 §4.2.1 / §4.2.2)

| Function | Spec § | Notes |
|----------|--------|-------|
| `InitiateSession(imsi, code)` | §4.2.1 | Resolves code → menu, leaf actions execute immediately and end |
| `ContinueSession(id, input)` | §4.2.2 | Numeric or text choice; navigates submenus or runs leaf action |
| `EndSession(id)` | — | Marks state=`completed`, drops in-memory entry |
| `ListSessions(imsi, state)` | — | Filtered listing |

A leaf menu's `action_type` is one of `menu` (subtree),
`balance_check`, `data_usage`, `topup`, `show_msisdn`, `custom_text`
(`ussd.go:251-272`).

## 4. Headline procedures

**Session init.** Caller invokes `InitiateSession(imsi, "*100#")`. A
row goes into `ussd_sessions` (state `active`). If the resolved menu
is a non-`menu` action, `executeAction` runs and the session is closed
immediately (returns `type:"response", ended:true`). Otherwise the
menu is rendered (`buildMenuText`) with numbered children plus
`0. Exit` and the session is added to `activeSessions` for follow-up
input.

**Session continuation.** `ContinueSession(id, input)` first checks
session timeout: 180 s per TS 22.090 §3.3 (`ussd.go:286`,
`sessionTimeout = 180 * time.Second`). On timeout the session ends as
`timeout`. Numeric input ∈ [1..len(children)] navigates into a
submenu or executes a leaf. If the current menu is `topup`, free-form
text is accepted as the amount (1..10000) and `executeTopup` updates
the `balances` table + `payment_transactions` log
(`ussd.go:417-439`). Inputs `0` or `exit` close the session
gracefully.

**Top-up execution** (`executeTopup`, `ussd.go:417-439`): looks up the
existing `main` balance row, increments it, inserts a `recharge` row
into `payment_transactions`, and returns a formatted confirmation. If
no balance row exists yet, one is created with currency `USD`.

## 5. Key types

```go
type Menu     { ID, Code, ParentID, Title, ActionType, ActionData,
                DisplayOrder }
type Session  { ID, IMSI, Code, State, CurrentMenuID, SessionData,
                CreatedAt, EndedAt }
type activeSession { imsi, code, currentMenuID, children []Menu,
                     startedAt time.Time }   // in-memory only
```

## 6. Stubs / TODOs

No `TODO(spec: ...)` markers in source. Implicit gaps:

- No on-the-wire USSD-over-IMS framing (TS 24.390 §5 SIP MESSAGE
  carrier). The package is purely the menu / session state machine.
- No legacy MAP USSD op-code emission — the `OpProcessUnstructuredSS*`
  constants live in `services/supplementary/codec.go` as bare values.
- Charging is collapsed to a direct `payment_transactions` insert;
  there is no Ro / Rf or CHF handover.

## 6a. Test coverage

`mmt_studio_core_tester/src/testcases/vas/tc_vas_oam.py` —
operator-API TCs (no UE/gNB needed):

| TC | Coverage |
|----|----------|
| TC-USSD-010 `ussd_menu_seed_and_list` | `/menus/seed` populates default tree, `/menus` lists it |
| TC-USSD-011 `ussd_session_initiate_balance` | `*123#` initiate → text response → end |
| TC-USSD-012 `ussd_session_unknown_code` | `*99999#` → 400; missing IMSI → 400 |
| TC-USSD-013 `ussd_menu_crud` | create / patch / delete; empty/bad patch → 400 |

`tc_ussd.py` carries the legacy UE/gNB integration TCs
(TC-USSD-001..004) which exercise the same surface end-to-end.

## 7. References

- **TS 24.390** §4.2.1 (`InitiateSession`), §4.2.2 (`ContinueSession`)
- **TS 22.090** §3.3 (180 s session timeout, cited inline)

---

*Last refreshed against commit `13a181d`.*
