# tcs — Design Document

Tactical Communication System (TCS) sync agent — UE location
tracking, sync-peer management, and a CRDT change log for replicating
state across tactical NIB nodes.

> **Note on the name.** "TCS" in this package stands for **Tactical
> Communication System** (per the package header — "TCS sync agent").
> It is **not** the 3GPP Trusted/Critical Services or Transcoding
> meaning. The implementation is a **Last-Writer-Wins CRDT** sync
> agent with Lamport timestamps over three tables: `ue_location`,
> `sync_peers`, `db_changes`.

## 1. Role / scope

`safety/tcs/` is the data-plane state for tactical NIB-to-NIB
replication:

- **UE location** (`ue_location`) — keyed by SUPI, holds serving
  node id/IP, optional IMS contact URI and MCX endpoint, status,
  Lamport clock, last update timestamp. UPSERTed on attach/detach.
- **Sync peers** (`sync_peers`) — keyed by `node_id`, holds peer
  IP, sidelink L2 ID, IMS / MCX endpoints, link status, node mode,
  optional sidelink RSRP, last-seen timestamp. UPSERTed on
  heartbeat receive.
- **CRDT change log** (`db_changes`) — append-only stream of column-
  level changes with `(lamport_clock, node_id, version)`. The agent
  replicates by streaming `version > sinceVersion` from a peer and
  applying each row through `ApplyChange` (LWW merge: higher Lamport
  clock wins; ties broken by lexicographic `node_id`).

The package is the persistence + merge layer; the actual transport
(IMS/MCX/sidelink heartbeats) lives elsewhere.

## 2. Architecture

```
   gNB / NIB attach event             Peer NIB heartbeat
            │                                  │
            │ SetUELocation                    │ SetSyncPeer
            │  (UPSERT)                        │  (UPSERT)
            ▼                                  ▼
   ┌──────────────────────────────────────────────────────────┐
   │                       safety/tcs                          │
   │                                                           │
   │   ue_location  (supi PK, lamport_clock, updated_at)       │
   │   sync_peers   (node_id PK, link_status, last_seen)       │
   │   db_changes   (id, table, row_key, column, new_value,    │
   │                 lamport_clock, node_id, version)          │
   │                                                           │
   │   ApplyChange  ─── LWW merge:                             │
   │     remote_clock < local_clock          → drop            │
   │     remote_clock = local AND nodeID ≤   → drop            │
   │     else → INSERT db_changes;                             │
   │            if (table='ue_location', col='*') →            │
   │              applyUELocationChange (rebuild row from JSON)│
   └──────────────────────────────────────────────────────────┘
```

### 2.1 LWW merge rule (`ApplyChange`, `tcs.go:224`)

```go
SELECT lamport_clock, node_id FROM db_changes
WHERE table_name=? AND row_key=? AND column_name=?
ORDER BY lamport_clock DESC, node_id DESC LIMIT 1
↓
if remote.lamportClock < local.lamportClock                     → applied=false
if remote.lamportClock == local.lamportClock AND
   remote.nodeID <= local.nodeID                                → applied=false
else                                                             → applied=true
       RecordChange(...)
       if tableName == "ue_location" AND columnName == "*":
           applyUELocationChange(rowKey, newValueJSON, lamportClock)
```

Convention: a `column_name == "*"` change carries a JSON-encoded
**whole row** in `new_value`; the dispatcher (`applyUELocationChange`,
`tcs.go:251`) decodes it and re-runs `SetUELocation` with the same
clock.

## 3. File map

| File | Role |
|------|------|
| `safety/tcs/tcs.go` | UE location + sync peer + CRDT change log + LWW merge (271 LOC) |

No dedicated test file in tree.

## 4. Wire / API surface

No spec wire format implemented. The on-the-wire heartbeat /
replication protocol that drives `SetSyncPeer` and `ApplyChange`
lives outside this package; the §-cites in the header are local
("LWW-CRDT") not 3GPP.

## 5. Headline procedures

### 5.1 UE location attach

```
gNB attach event              tcs pkg
   │                              │
   │── SetUELocation(             │
   │     supi, servingNode*,      │
   │     imsContactURI, mcx*,     │
   │     status, lamportClock) ──▶│
   │                              │ INSERT INTO ue_location ... ON CONFLICT(supi)
   │                              │ DO UPDATE SET ... lamport_clock = excluded.lamport_clock
```

### 5.2 Peer heartbeat

```
peer heartbeat               tcs pkg
   │                            │
   │── SetSyncPeer(             │
   │     nodeID, nodeIP,        │
   │     linkStatus,            │
   │     nodeMode) ────────────▶│
   │                            │ UPSERT sync_peers (last_seen = now())
```

`UpdatePeerLinkStatus` is the lightweight version that just touches
`link_status`.

### 5.3 CRDT replication

```
peer NIB streams changes:
   for change in peer.GetChangesSinceVersion(myLatest, limit):
       result := ApplyChange(change.table, change.rowKey, change.column,
                             change.newValue, change.lamportClock,
                             change.nodeID, change.version)
       if result.applied:
           myLatest = change.version
```

`GetLatestVersion()` returns `MAX(version)` from `db_changes` (0 if
empty) — the cursor for the next pull.

## 6. Key types / public API

```go
type UELocation struct {
    SUPI, ServingNodeID, ServingNodeIP, Status, UpdatedAt string
    IMSContactURI, MCXEndpoint                            *string
    LamportClock                                          int
}

type SyncPeer struct {
    NodeID, NodeIP, LinkStatus, NodeMode, LastSeen string
    SidelinkL2ID, IMSSipURI, MCXEndpoint            *string
    SidelinkRSRP                                    *float64
}

type DBChange struct {
    ID                                int64
    TableName, RowKey, ColumnName     string
    NewValue                          *string
    LamportClock                      int
    NodeID                            string
    Version                           int
    AppliedAt                         string
}

// UE location
func ListUELocations(status string) ([]UELocation, error)
func GetUELocation(supi string) (*UELocation, error)
func SetUELocation(supi, servingNodeID, servingNodeIP string,
    imsContactURI, mcxEndpoint *string, status string, lamportClock int) error
func DeleteUELocation(supi string) error

// Sync peers
func ListSyncPeers() ([]SyncPeer, error)
func GetSyncPeer(nodeID string) (*SyncPeer, error)
func SetSyncPeer(nodeID, nodeIP, linkStatus, nodeMode string) error
func UpdatePeerLinkStatus(nodeID, linkStatus string) error
func DeleteSyncPeer(nodeID string) error

// CRDT change log
func RecordChange(tableName, rowKey, columnName, newValue string,
    lamportClock int, nodeID string, version int) error
func GetChangesSinceVersion(sinceVersion, limit int) ([]DBChange, error)
func GetLatestVersion() int
func ApplyChange(tableName, rowKey, columnName, newValue string,
    lamportClock int, nodeID string, version int) map[string]interface{}  // tcs.go:224

// GUI panel
func List() ([]UELocation, error)
func Status() map[string]any
```

## 7. Stubs / TODOs from grep

No `TODO` comments in `safety/tcs/tcs.go`. The header is concise
and the surfaces are complete relative to its declared scope (LWW
merge over the three tables).

## 8. References

No 3GPP §-cites in source. Local design references only:

- LWW-CRDT (Last-Writer-Wins) with Lamport timestamps for cross-NIB
  replication.

---
*Last refreshed against commit `13a181d`.*
