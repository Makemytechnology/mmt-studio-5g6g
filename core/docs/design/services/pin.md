# pin — Personal IoT Networks

## 1. Role / scope

`services/pin/` is the local Personal IoT Network registry implementing
the data-plane plumbing of TS 23.542 — short-range networks of sensors,
actuators, wearables and gateways anchored on a 5GS UE. The package
owns four bits of state in the SQLite engine: PIN networks
(`pin_networks`), PIN elements (`pin_elements`), gateway reachability
(in-memory `gateways` map) and a relayed-data audit log
(`pin_data_log`). It does not speak any wire protocol — element-level
transport (BLE, Zigbee, WiFi, Thread, NFC) is handled below; this
package is the 3GPP-side bookkeeping that the OAM panel and a future
PIN AF read.

## 2. File map

| File | LOC | Role |
|------|-----|------|
| `pin.go` | 268 | All types, CRUD, gateway tracker, data-relay |

Total module: ~268 LOC, single Go file under `services/pin/`.

## 3. API surface

No external wire protocol. The public Go surface is the GUI-panel /
OAM API:

| Function | TS 23.542 § | Notes |
|----------|-------------|-------|
| `ListNetworks(ownerIMSI)` | §6.2 | Network CRUD |
| `GetNetwork(id)` | §6.2 | Returns network + linked elements |
| `CreateNetwork(...)` | §6.2 | Inserts row, validates name + owner |
| `SetGateway(pinID, gwIMSI)` | §6.2 | Bind PINGW to a network |
| `DeleteNetwork(id)` | §6.2 | Cascade by FK |
| `ListElements(pinID)` | §6.3 | Element CRUD |
| `AddElement(...)` | §6.3 | Validates element_type ∈ {sensor,actuator,gateway,wearable} and protocol ∈ {BLE,Zigbee,WiFi,Thread,NFC} (`pin.go:137-140`) |
| `RegisterGateway(imsi, caps)` | §5.2 / §6.4-6.5 | In-memory `gateways` map |
| `UpdateGatewayReachability(imsi, ok)` | §5.2 | Flips status |
| `RelayData(pinID, eid, hex, dir)` | §6.4 | Gates on gateway reachability, appends to log |
| `ListDataLog(pinID, limit)` | §6.4 | Audit |

## 4. Headline procedures

**Network create → gateway bind → element add.** A tenant creates a
PIN network with `CreateNetwork` (mandatory `owner_imsi` + `name`,
`pin.go:91-106`). A gateway registers via `RegisterGateway` which
keeps the `caps` map in process; `SetGateway` then binds it onto a
network row by IMSI. Elements (sensors / actuators / etc.) are
attached via `AddElement` and start in `disconnected` status.

**Data relay** (`pin.go:201-235`). The path checks: (a) the network
exists, (b) the element belongs to that network, (c) if a gateway is
bound and reachable, the relay proceeds. If the gateway is
`unreachable` the call returns
`"PINGW <imsi> is unreachable -- cannot relay"`. On success a row is
inserted into `pin_data_log` with direction (`UL` default) and the
hex payload's byte length.

## 5. Key types

```go
type Network  { ID, OwnerIMSI, Name, Description, GatewayIMSI, Status,
                ConfigJSON, CreatedAt, Elements []Element }
type Element  { ID, PinID, ElementID, ElementType, Protocol, Name,
                Status, LastSeenAt, CreatedAt }
type Gateway  { IMSI, Capabilities, Status, RegisteredAt, LastSeen }
```

## 6. Stubs / TODOs

The package has no source TODO comments. Implicit gaps (gleaned from
absent tables / call paths):

- No PIN AF: 3GPP Rel-18 introduces a Personal IoT Network AF that
  exposes the network to external SBI. Today only the local DB is
  written.
- No reachability heartbeat — `UpdateGatewayReachability` is
  caller-driven; nothing pings the gateway itself.
- Element protocol is a free-form string after the validity check; no
  protocol-specific framing.

## 7. References

- TS 23.542 §5.2 / §6.2 / §6.3 / §6.4 / §6.5 — Personal IoT Networks,
  cited inline at the relevant CRUD functions.

---

*Last refreshed against commit `13a181d`.*
