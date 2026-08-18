# nbiot — Design Document

NB-IoT / LTE-M power-saving + capability registry for the MMT 5G
Core. Three concerns share one package because they share the
same per-IMSI keying:

1. **PSM** — Power Saving Mode (active / sleeping / unreachable)
2. **eDRX** — Extended idle-mode DRX cycle + Paging Time Window
3. **Capabilities** — NB-IoT radio capability bits the eNB
   advertises to the MME at attach.

## 1. Role / Scope

Per **TS 23.401 §4.3.22** the UE may request an Active Time
(T3324) and a Periodic TAU/RAU Timer (T3412-extended) at every
Attach / TAU. Per **TS 23.401 §5.13a** it may also request an
extended idle-mode DRX cycle + Paging Time Window. NB-IoT-specific
capability bits (multi-tone / Coverage Enhancement level / CP-CIoT
/ UP-CIoT / data-over-NAS) are the per-UE capability set the eNB
advertises at attach (**TS 23.401 §4.3.17** + **TS 24.301
§5.5.1.2.4** NB-S1 capability container).

This package owns:

- The PSM state machine (`active` → `sleeping` → `unreachable`
  → `active`).
- Persistence of negotiated T3324 / T3412-extended.
- Persistence of negotiated eDRX cycle + PTW.
- Persistence of negotiated NB-IoT capability set.
- The aggregate `Status()` for the operator panel.

It does NOT do:

- The actual NAS-IE encoding of T3324 / eDRX / NB-S1 capability —
  that lives in `codecs/tlv-3gpp-nas/` (TODO `TS 24.301 §5.5.1.2.4`
  at `nbiot.go:37`).
- Buffering of DL data while the UE is unreachable — that lives
  in `iot/nidd` (which calls `nbiot.GetPSM` to query state).

## 2. Architecture

```
┌──────────────────────────────────────────────────────────────────┐
│ MME / AMF                                                        │
│   Attach / TAU procedure includes T3324 + T3412-ext + eDRX IEs   │
└────────────────────────────┬─────────────────────────────────────┘
                             │
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│  iot/nbiot                                                       │
│                                                                  │
│  PSM state machine                                               │
│   SetPSM     — store negotiated T3324 / T3412-ext                │
│   GetPSM     — read row                                          │
│   EnterSleep — Active Time expired, suppress paging              │
│                (TS 23.401 §4.3.22). Computes next_wakeup.        │
│   MarkUnreachable — past next_wakeup — SCEF must buffer DL       │
│                     (TS 23.682 §5.13.3)                          │
│   Wake       — UE performs MO TAU / data — back to 'active'      │
│                                                                  │
│  eDRX  (TS 23.401 §5.13a)                                        │
│   SetEDRX / GetEDRX                                              │
│                                                                  │
│  Capabilities  (TS 23.401 §4.3.17 / Annex F)                     │
│   SetCapabilities / GetCapabilities                              │
│                                                                  │
│  GUI panel:  List() / Status()                                   │
└────────────────────────────┬─────────────────────────────────────┘
                             │ engine.Exec / Query
                             ▼
┌──────────────────────────────────────────────────────────────────┐
│  db/engine — SQLite                                              │
│   iot_psm_state                                                  │
│   iot_edrx_config                                                │
│   iot_nbiot_capabilities                                         │
└──────────────────────────────────────────────────────────────────┘
                             ▲
                             │ GetPSM (read-only)
                             │
                  ┌──────────┴───────────┐
                  │ iot/nidd MT NIDD     │
                  │  buffer when UE      │
                  │  state ∈ {sleeping,  │
                  │  unreachable}        │
                  └──────────────────────┘
```

## 3. File / Package Map

| File | LOC | Role |
|------|-----|------|
| `iot/nbiot/nbiot.go` | 307 | PSM / eDRX / capability CRUD + state transitions |
| `iot/nbiot/nbiot_test.go` | 213 | Round-trip tests; PSM transitions |

## 4. Public API

```go
// PSM (TS 23.401 §4.3.22)
func SetPSM(imsi string, t3324Sec, t3412ExtSec int) error
func GetPSM(imsi string) (*PSMState, error)
func EnterSleep(imsi string) error      // active   → sleeping
func MarkUnreachable(imsi string) error // sleeping → unreachable
func Wake(imsi string) error            // any      → active

// eDRX (TS 23.401 §5.13a)
func SetEDRX(imsi, deviceType string, cycleSec, ptwSec float64) error
func GetEDRX(imsi string) (*EDRXConfig, error)

// Capabilities (TS 23.401 §4.3.17 / Annex F)
func SetCapabilities(c Capabilities) error
func GetCapabilities(imsi string) (*Capabilities, error)

// GUI panel
func List() ([]map[string]any, error)
func Status() map[string]any
```

Validation gates (all return descriptive errors, no silent normalisation):

- `SetPSM`: `t3324Sec > 0` and `t3412ExtSec > 0`.
- `SetEDRX`: `cycleSec > 0` and `ptwSec > 0`; `deviceType` defaults
  to `"nbiot"` if empty.
- `SetCapabilities`: `CELevel ∈ [0..2]` per the NAS NB-S1 capability
  container.

## 5. PSM State Machine

```
       SetPSM(t3324, t3412_ext)
                │
                ▼
            ┌────────┐
            │ active │◄───────────────────────────┐
            └────┬───┘                            │
                 │ Active Time (T3324) expires    │ Wake()
                 │ EnterSleep()                   │ (UE does MO TAU /
                 ▼                                │  MO data — §4.3.22)
            ┌──────────┐                          │
            │ sleeping │ ─────────────────────────┘
            └────┬─────┘                          ▲
                 │ next_wakeup passed             │
                 │ MarkUnreachable()              │
                 ▼                                │
            ┌──────────────┐                      │
            │ unreachable  │ ─────────────────────┘
            └──────────────┘
            (SCEF must buffer DL — TS 23.682 §5.13.3)
```

`EnterSleep` computes `next_wakeup = now + T3412-extended` and
stamps it on the row; `iot/nidd` reads the same row to decide
whether MT data should be marked `delivered` or `buffered`.

## 6. Key Types

```go
type PSMState struct {
    IMSI        string
    Enabled     bool
    T3324Sec    int     // Active Time
    T3412ExtSec int     // Periodic TAU timer
    State       string  // active | sleeping | unreachable
    SleepStart  *string
    NextWakeup  *string
}

type EDRXConfig struct {
    IMSI         string
    DeviceType   string  // nbiot | ltem | redcap
    EDRXCycleSec float64
    PTWSec       float64
    Enabled      bool
}

type Capabilities struct {
    IMSI            string
    MultiTone       bool
    CELevel         int   // 0..2 — Coverage Enhancement
    CPCIoTSupported bool  // Control-Plane CIoT (NAS-borne small data)
    UPCIoTSupported bool  // User-Plane CIoT (DRB resume)
    DataOverNAS     bool  // ESM Data Transport
}
```

## 7. Stubs / TODOs

| Site | TS | Comment |
|------|-----|---------|
| `nbiot.go:37` / `nbiot.go:73` | TS 24.301 §5.5.1.2.4 | Anchor the CE-level / multi-tone bit definitions to the NAS capability IE encoding once TS 24.301 is loaded into `specs/3gpp/`. |

The eDRX defaults the file mentions ("eDRX 40.96 s, PTW 2.56 s") match
the CT-WG NAS-IE nominal values; the schema enforces operator-local
clamping rather than re-validating here.

## 8. References

Spec citations grounded in `iot/nbiot/nbiot.go`:

- **TS 23.401 §4.3.22** — UE Power Saving Mode. Verbatim quote in
  the package doc: *"A UE may adopt a PSM that is described in
  TS 23.682. … it shall request an Active Time value and may
  request a Periodic TAU/RAU Timer value during every Attach and
  Tracking Area Update procedure"*.
- **TS 23.401 §5.13a** — Extended Idle mode DRX (eDRX cycle +
  Paging Time Window).
- **TS 23.401 §4.3.17** — Support for Machine Type Communications;
  NB-IoT capability set negotiation.
- **TS 23.401 §4.3.17.8** — NIDD bridge from the SCEF.
- **TS 23.401 Annex F** — NB-IoT-specific capability bits.
- **TS 23.682 §4.5.21** — Power Saving Mode origin.
- **TS 23.682 §5.13.3** — High-latency DL delivery (drives the
  `unreachable` → SCEF-must-buffer rule).
- **TS 24.301 §5.5.1.2.4** — NB-S1 capability container (TODO).

---
*Last refreshed against commit `13a181d`.*
