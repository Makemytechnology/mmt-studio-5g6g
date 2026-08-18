# ambient — Design Document

Ambient IoT (AmIoT) tag and reader registry for the MMT 5G Core.
Stage-1 service-requirements aligned with **TS 22.369**; Stage-2/3
specs are not yet at a Rel-19 floor in `specs/3gpp/`, so the
package today is local persistence + an inventory event log.

## 1. Role / Scope

Ambient IoT (Rel-19) targets battery-less / energy-harvested tags
that communicate via backscatter or active radio. Per
**TS 22.369 §4.2** they are characterised by:

- No battery (energy-harvested) or limited storage capacitor;
- Communication via backscatter or active radio;
- Tag class A / B / C distinguishing energy availability, storage,
  and half- / full-duplex support (§5.2).

This package handles:

- Operator provisioning of tags + readers (CRUD).
- Reader heartbeat / liveness for the operator dashboard.
- Inventory event log (KPIs from §6.2 / §6.3 / §6.5) — a reader
  observation of one or more tags.
- Tag last-seen / last-payload tracking via `SeenTag`.

Out of scope until Stage-2/3 land:

- AmIoT NAS / AS signalling — TODO `TS 23.369` (`ambient.go:31`).
- AmIoT-specific authentication / privacy.
- Trigger and command procedures.

## 2. Architecture

```
┌─────────────────────────────────────────────────────────────────┐
│  Operator dashboard / GUI panel                                 │
│   Status() / ListTags() / ListReaders() / ListInventoryEvents() │
└────────────────────────────┬────────────────────────────────────┘
                             │
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│  iot/ambient                                                    │
│                                                                 │
│  Tag CRUD          RegisterTag / GetTag / ListTags /            │
│                    SeenTag / DeleteTag                          │
│                                                                 │
│  Reader CRUD       RegisterReader / GetReader / ListReaders /   │
│                    HeartbeatReader                              │
│                                                                 │
│  Event log         LogInventory / ListInventoryEvents           │
└────────────────────────────┬────────────────────────────────────┘
                             │ engine.Exec / Query
                             ▼
┌─────────────────────────────────────────────────────────────────┐
│  db/engine — SQLite                                             │
│   iot_tags                                                      │
│   iot_readers                                                   │
│   iot_inventory_events                                          │
└─────────────────────────────────────────────────────────────────┘
                             ▲
                             │ writes via SeenTag, LogInventory
                             │
            ┌────────────────┴───────────────────┐
            │ Reader-side dataplane              │
            │ (BS↔AmIoT / BS↔Intermediate UE↔   │
            │  AmIoT / BS↔Intermediate node↔     │
            │  AmIoT — TS 22.369 §4.4)           │
            └────────────────────────────────────┘
```

## 3. File / Package Map

| File | LOC | Role |
|------|-----|------|
| `iot/ambient/ambient.go` | 286 | All CRUD + event log + `Status()` |
| `iot/ambient/ambient_test.go` | 179 | Tag class gate, inventory KPI capture, reader heartbeat |

## 4. Public API

```go
// Tag CRUD — TS 22.369 §5.2 tag class A/B/C
func RegisterTag(tagID, tagClass, tagType string, groupID, owner *string) error
func GetTag(tagID string) (*Tag, error)
func ListTags(tagType string) ([]Tag, error)
func SeenTag(tagID, readerID string, payload *string, lat, lon *float64) error
func DeleteTag(tagID string) error

// Reader CRUD — TS 22.369 §4.4 communication topologies
func RegisterReader(readerID string, gnbIP, capabilities *string, lat, lon *float64) error
func HeartbeatReader(readerID string) error
func GetReader(readerID string) (*Reader, error)
func ListReaders() ([]Reader, error)

// Event log — TS 22.369 §6.2 / §6.3 / §6.5 KPI capture
func LogInventory(readerID, eventType string, tagsFound int, resultJSON *string) (int64, error)
func ListInventoryEvents(limit int) ([]InventoryEvent, error)

// GUI panel
func Status() map[string]any
```

`tagClass` is gated to `"A" | "B" | "C"` per §5.2; default `"A"`
when caller passes empty string. `eventType` defaults to
`"inventory"` and records into the four KPI buckets §4.3 lists:
inventory, sensor_read, actuator, track.

## 5. Lifecycle

A typical observation flow:

```
1. Operator: RegisterTag("tag-1234", "B", "sensor", &group, &owner)
   → INSERT iot_tags

2. Operator: RegisterReader("rdr-1", &gnbIP, ...) ; HeartbeatReader periodically
   → INSERT iot_readers ; UPDATE last_heartbeat

3. Reader observes tags in field
   → SeenTag("tag-1234", "rdr-1", &payload, &lat, &lon)
       → UPDATE iot_tags last_seen_at, last_reader_id, payload, location
   → LogInventory("rdr-1", "inventory", 17, &resultJSON)
       → INSERT iot_inventory_events row id=N

4. Dashboard: Status() → {"tags": ..., "active_readers": ..., "events": ...}
```

`HeartbeatReader` flips `status='active'` and stamps
`last_heartbeat` — the operator dashboard uses this as the
liveness signal.

## 6. Key Types

```go
type Tag struct {
    TagID, TagClass, TagType   string
    GroupID, Owner, DataPayload *string
    LastSeenAt, LastReaderID    *string
    Latitude, Longitude         *float64
    RegisteredAt                string
}

type Reader struct {
    ReaderID                       string
    GnbIP, Capabilities            *string
    Latitude, Longitude            *float64
    Status                         string  // active | offline | maintenance
    LastHeartbeat                  *string
}

type InventoryEvent struct {
    ID                  int64
    ReaderID, EventType string  // inventory | sensor_read | actuator | track
    TagsFound           int
    ResultJSON          *string
    Timestamp           string
}
```

## 7. Stubs / TODOs

| Site | TS | Comment |
|------|-----|---------|
| `ambient.go:31` | TS 23.369 (Stage-2 AmIoT) | Anchor inventory + trigger procedures when Stage-2 lands |

No on-the-wire stage-3 codec exists yet — when AmIoT NAS/AS PDUs
arrive at the codec/yaml layer (codecs/tlv-3gpp-nas), the dataplane
hook here is `SeenTag` + `LogInventory`.

## 8. References

Spec citations grounded in `iot/ambient/ambient.go`:

- **TS 22.369 §4.2** — Characteristics of Ambient IoT (battery /
  capacitor / backscatter or active radio). Anchors `Tag` and the
  per-tag `data_payload` semantics.
- **TS 22.369 §4.3** — Typical AmIoT use cases. Source of the
  four `event_type` buckets: inventory, sensor_read, actuator,
  track.
- **TS 22.369 §4.4** — Communication modes (Topology 1 BS↔AmIoT,
  Topology 2 BS↔Intermediate UE↔AmIoT, Topology 3 BS↔Intermediate
  node↔AmIoT). Captured by `Reader.GnbIP`.
- **TS 22.369 §5.2** — Functional service requirements (tag class
  A/B/C). Enforced in `RegisterTag`.
- **TS 22.369 §6.2 / §6.3 / §6.5** — Performance requirements
  (Inventory / Sensor / Actuator KPIs). Captured by
  `LogInventory(... tags_found, result_json ...)`.
- **TS 23.369** — Stage-2 AmIoT architecture (TODO at
  `ambient.go:31`).

---
*Last refreshed against commit `13a181d`.*
