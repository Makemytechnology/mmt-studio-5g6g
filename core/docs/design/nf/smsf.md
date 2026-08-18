# SMSF — Design Document

3GPP TS 23.502 §4.13 / TS 24.011-aligned SMS Function for the MMT 5G
Core. Carries SMS over NAS — UE-encoded SMS-SUBMIT / SMS-DELIVER TPDUs
ride inside CP/RP framing inside the AMF's UL/DL NAS Transport
"Payload Container = SMS" path.

## 1. Role in 5GC

| Reference point | Peer | Wire | Spec |
|-----------------|------|------|------|
| **Nsmsf** | AMF (consumer) | Nsmsf_SMService SBI; in-process today | TS 29.540 (TODO scaffold in `nsmsf_sbi.go`) |
| AMF (in-proc) | AMF | function call from `gmm/ulnas.go` Payload Container=SMS path | TS 24.501 §9.11.3.39 / §8.2.10 / §8.2.11 |

In our build the SMSF is invoked in-process by the AMF when an UL NAS
Transport carrying Payload Container Type = SMS (`§9.11.3.40` value
`2`) lands. The bytes inside are an SM-CP message (TS 24.011 §8.1)
wrapping an RP-PDU (§8.2) wrapping a TPDU (TS 23.040 §9.2.2).

The SMSF:

- Decodes CP / RP / TPDU stack (`nas_bridge.go:ProcessMOSMSFromNAS`).
- Runs the GSM 7-bit / UCS-2 user-data codec (`smsf.go:GSM7Encode/Decode`,
  `encodeUTF16BE`).
- Persists MO records (DB-backed); routes MT toward the destination UE
  (when the destination MSISDN resolves to a known IMSI) or to a
  best-effort "delivered locally" sink.
- Returns CP-ACK + CP-DATA(RP-ACK / RP-ERROR) bytes to the AMF for DL
  NAS Transport.

## 2. Architecture

```
                      ┌──────────────┐
                      │      UE      │
                      └──────┬───────┘
                             │ NAS UL: ULNASTransport (Payload Container Type=SMS)
                             ▼
                      ┌──────────────┐
                      │     AMF      │
                      │ gmm/ulnas    │  decode container, route to SMSF
                      └──────┬───────┘
                             │ in-process call: smsf.ProcessMOSMSFromNAS(IMSI, payload)
                             ▼
┌─────────────────────  SMSF (single Go pkg) ───────────────────────────────┐
│                                                                            │
│  nas_bridge.go                                                             │
│    ProcessMOSMSFromNAS:                                                   │
│       → DecodeCP (TS 24.011 §8.1)                                         │
│       → DecodeRP (TS 24.011 §8.2)                                         │
│       → DecodeSMSSubmit (TS 23.040 §9.2.2.2)                              │
│       → DecodeUserData (TS 23.038 §6.1.2 / §6.2)                          │
│       → ProcessMOSMS (segmentation, DB store, routing)                    │
│       → assemble CPAck + RPAckCPData (or RPErrorCPData)                   │
│                                                                            │
│  codec_decode.go                  smsf.go (~28 KLOC)                       │
│    decoders for CP / RP /          GSM 7-bit alphabet table + ext          │
│    TPDU + DCS + TP-VP              EncodeAddress / DecodeAddress (TS       │
│                                    23.040 §9.1.2.5)                        │
│                                    EncodeSMSSubmit / EncodeSMSDeliver      │
│                                    EncodeRPDataMT / EncodeRPAck /          │
│                                    EncodeRPError / EncodeCPAck/Data/Error  │
│                                    BuildConcatUDH (concat-SMS UDH)         │
│                                    SegmentText (160/153 GSM7,             │
│                                                  70/67 UCS2)              │
│                                    SmsfContext singleton:                  │
│                                      activeSessions map[IMSI]*smsSession   │
│                                      msgReference (TP-MR alloc)           │
│                                                                            │
│  nsmsf_sbi.go    placeholder Nsmsf SBI surface (TS 29.540) — all TODOs    │
└────────────────────────────────────────────────────────────────────────────┘
                             │ DB writes / lookups (sms_messages, gpsi_msisdn rows)
                             ▼
                       db/engine
```

## 3. Package / file map

`nf/smsf/` is intentionally a single Go package — five files, no
sub-packages.

| File | LOC | Role |
|------|-----|------|
| `smsf.go` | 28k bytes (~880 lines) | Encoders, GSM-7 / UCS-2 codecs, address codec, TPDU build (SMS-SUBMIT / SMS-DELIVER), CP/RP encoders, segmentation, `SmsfContext` singleton, MO/MT processing, DB persistence. |
| `codec_decode.go` | ~470 | Decoders for CP / RP / TPDU + DCS + TP-VP + UD; mirrors `smsf.go` encoders. |
| `nas_bridge.go` | ~190 | `ProcessMOSMSFromNAS` (NAS UL bridge entry point); `BuildPayloadContainerSMS`. Per-procedure tracing maps to TS 23.502 §4.13.3.5 steps 4-6. |
| `codec_test.go` | ~290 | Round-trip tests for the encoder/decoder pair. |
| `nsmsf_sbi.go` | 60 | Stage-3 SBI surface stub — all TODOs against TS 29.540 §5.2.2.x. |

## 3a. Operator REST API (`/api/smsf/*`)

Wired in `webservice/app/routes_smsf.go`. All responses follow the
`{ok: true, ...}` envelope; bad input surfaces as 400 at the route
layer rather than a 500 from the codec or DB CHECK.

| Method | Path                          | Purpose |
|--------|-------------------------------|---------|
| GET    | `/api/smsf/stats`             | `{ok, stats:{pending,delivered,failed,expired,total,active_sessions}}`. |
| GET    | `/api/smsf/sessions`          | Active CP/RP session table (TS 24.011 §7.2 / §7.3). |
| GET    | `/api/smsf/messages?imsi=&limit=` | Newest-first; envelope shape. |
| GET    | `/api/smsf/messages/{id}`     | Single message read; 404 if unknown, 400 if id non-int. |
| DELETE | `/api/smsf/messages/{id}`     | Single delete. |
| POST   | `/api/smsf/send`              | MT-SMS — validates MSISDN E.164, encoding ∈ {gsm7,ucs2}, body length ≤ 10000. |
| POST   | `/api/smsf/segment`           | Pure-compute UDH preview: returns `{encoding, segments, count, gsm7_compatible}`. |
| GET    | `/api/smsf/routing`           | Rules list. |
| POST   | `/api/smsf/routing`           | Create — vocabulary `route_type ∈ {local,smsc,forward}`. |
| DELETE | `/api/smsf/routing/{id}`      | Remove. |
| POST   | `/api/smsf/expire`            | Trigger TS 23.040 §9.2.3.12 TP-VP sweep on demand. |
| POST   | `/api/smsf/deliver/{imsi}`    | Drain pending MT-SMS to a UE (TS 23.502 §4.13.3.5 CM-CONNECTED). |

`msisdn_pattern` validation: stricter than the package's permissive
encoder — E.164 shape `^\+?[0-9]{3,15}$`. The 10 000-char body cap
prevents a single POST from queueing >64 segments at GSM-7.

## 4. Wire / NAS interactions

### 4.1 NAS Payload Container = SMS frame stack

```
NAS DL/UL TRANSPORT (TS 24.501 §8.2.10 / §8.2.11)
   └── Payload Container Type IE (§9.11.3.40 = 2 SMS)
   └── Payload Container IE       (§9.11.3.39)
         └── CP message           (TS 24.011 §8.1)
              ├── CP-DATA (0x01)     ┌── RP-DATA MS→Net (0x00)  — MO
              ├── CP-ACK  (0x04)     │   └── TPDU SMS-SUBMIT (TS 23.040 §9.2.2.2)
              └── CP-ERROR(0x10)     ├── RP-DATA Net→MS (0x01)  — MT
                                     │   └── TPDU SMS-DELIVER (§9.2.2.1)
                                     ├── RP-ACK  MS→Net / Net→MS
                                     └── RP-ERROR (with §8.2.5.4 cause)
```

CP message-type constants (`smsf.go:352-363`):

| Const | Value | Direction |
|-------|-------|-----------|
| `CPData` | 0x01 | both |
| `CPAck`  | 0x04 | both |
| `CPError`| 0x10 | both |
| `RPDataMSToNet` | 0x00 | UE → SMSF |
| `RPDataNetToMS` | 0x01 | SMSF → UE |
| `RPAckMSToNet`  | 0x02 | UE → SMSF |
| `RPAckNetToMS`  | 0x03 | SMSF → UE |
| `RPErrorMSToNet`| 0x04 | UE → SMSF |
| `RPErrorNetToMS`| 0x05 | SMSF → UE |

### 4.2 GSM 7-bit / UCS-2 codec (TS 23.038 §6)

`smsf.go:30-150` — basic + ext alphabet tables (`gsm7Basic`, `gsm7Ext`)
+ `IsGSM7Encodable`, `GSM7Encode` (packs septets into octets per
§6.1.2), `GSM7Decode`. UCS-2 path uses `encodeUTF16BE` — the SMS DCS
(`0x08`) signals UCS-2.

### 4.3 Concatenated SMS UDH (TS 23.040 §9.2.3.24.1)

`smsf.go:BuildConcatUDH(refNum, totalParts, partNum, use16bit)`. 8-bit
ref uses IEI `0x00`; 16-bit ref uses IEI `0x08`. Used by `SegmentText`
when text > 160 septets (GSM7) / 70 chars (UCS-2).

## 5. Lifecycle — MO SMS over NAS (TS 23.502 §4.13.3.5)

The AMF is in CM-CONNECTED. From `nas_bridge.go:14-26`:

```
UE                   AMF                              SMSF
 │                    │                                │
 │── UL NAS Transport(SMS, CP-DATA(RP-DATA(SMS-SUBMIT))) ─▶│
 │                    │                                │
 │                    │ ProcessMOSMSFromNAS(IMSI, payload)
 │                    │   DecodeCP  (TS 24.011 §8.1)   │
 │                    │   DecodeRP  (TS 24.011 §8.2)   │
 │                    │   DecodeSMSSubmit (TS 23.040 §9.2.2.2)
 │                    │   DecodeUserData (TS 23.038 §6.x)
 │                    │   ProcessMOSMS:                │
 │                    │     - SegmentText (if needed)  │
 │                    │     - DB INSERT into sms_messages
 │                    │     - route by DA-MSISDN: local UE? → MT path
 │                    │                                │
 │                    │ NASMOResponse {                │
 │                    │   CPAck         = EncodeCPAck(TI),  // step 5
 │                    │   RPAckCPData   = EncodeCPData(TI, EncodeRPAck(MR, true))
 │                    │ }                              │
 │                    │                                │
 │◄── DL NAS Transport(SMS, CP-ACK) ──┤  step 5
 │◄── DL NAS Transport(SMS, CP-DATA(RP-ACK)) ──┤  step 6
```

Failure paths:

- CP decode fail → `(nil, error)` returned to AMF; AMF logs.
- RP decode fail → `CPAck` only (UE will time out).
- TPDU decode fail → `CPAck + CPData(RP-ERROR cause=95
  "Semantically incorrect message")` per `nas_bridge.go:128-138`.
- `ProcessMOSMS.OK == false` → `RP-ERROR cause=21` ("Short message
  transfer rejected") per `nas_bridge.go:159-163`.

CP-ACK / CP-ERROR inbound:

- `CPAck` from UE → log, no further DL (`nas_bridge.go:86-95`).
- `CPError` from UE → log with cause, no further DL (`:96-98`).

## 6. Lifecycle — MT SMS

MT is constructed and queued by `ProcessMOSMS` when destination MSISDN
maps to a registered UE (peer of the SMSF):

1. SMSF builds SMS-DELIVER TPDU via `EncodeSMSDeliver(oaMSISDN, text,
   encoding, udh)` — sets first octet MTI=00, MMS=1, optional UDHI.
2. Wraps in RP-DATA Net→MS via `EncodeRPDataMT(mr, oaSMSC, tpdu)`
   (TS 24.011 §7.3.1.1 / §8.2 layout per `smsf.go:381-411` comment).
3. Wraps in CP-DATA via `EncodeCPData(ti, rpPDU)`.
4. Stores on `SmsfContext.activeSessions[IMSI].PendingMT` until the AMF
   delivers it (today, `deliverLocal` short-circuits to local DB).

The AMF-side delivery (UE in CM-IDLE → Paging) plumbing is **not yet
wired** — see `nsmsf_sbi.go` TODO §5.2.2.5
`Nsmsf_SMService_MtForwardSm`. Notification to AMF would today need a
`Namf_Communication_N1MessageNotify` (TS 29.518 §5.2.2.3) call which
isn't implemented.

## 7. Key types / public API

```go
// smsf.go
const (
    CPData = 0x01; CPAck = 0x04; CPError = 0x10
    RPDataMSToNet = 0x00; RPDataNetToMS = 0x01
    RPAckMSToNet  = 0x02; RPAckNetToMS  = 0x03
    RPErrorMSToNet= 0x04; RPErrorNetToMS= 0x05
)

// codec
func IsGSM7Encodable(text string) bool
func GSM7Encode(text string) (packed []byte, septets int)
func GSM7Decode(data []byte, numSeptets int) string
func EncodeAddress(msisdn string) []byte
func DecodeAddress(data []byte, offset int) (msisdn string, consumed int)
func EncodeSMSSubmit(mr byte, daMSISDN, text, encoding string, udh []byte) []byte
func EncodeSMSDeliver(oaMSISDN, text, encoding string, udh []byte) []byte
func BuildConcatUDH(refNum, totalParts, partNum int, use16bit bool) []byte
func SegmentText(text, encoding string) []string

// CP/RP encoders (TS 24.011 §7 / §8)
func EncodeCPData(ti byte, rpPDU []byte) []byte
func EncodeCPAck (ti byte) []byte
func EncodeCPError(ti, cause byte) []byte
func EncodeRPDataMT(mr byte, oaSMSC string, tpdu []byte) []byte
func EncodeRPAck(mr byte, netToMS bool) []byte
func EncodeRPError(mr, cause byte, netToMS bool) []byte

// codec_decode.go
type CPMsg struct{ MsgType, TI, Cause byte; UserData []byte }
func DecodeCP(data []byte) (*CPMsg, error)
type RPMsg struct{ MTI byte; Reference byte; UserData []byte; ... }
func DecodeRP(data []byte) (*RPMsg, error)
type SMSSubmitTPDU struct{ Reference byte; DAMSISDN string; Encoding, DCS, UDL byte; UD, UDH []byte; ... }
func DecodeSMSSubmit(tpdu []byte) (*SMSSubmitTPDU, error)
func DecodeUserData(encoding string, dcs, udl byte, udh, ud []byte) (string, error)

// SmsfContext singleton — smsf.go:478-560
type SmsfContext struct{ /* sync.Mutex, activeSessions, msgReference */ }
func GetContext() *SmsfContext
func (c *SmsfContext) NextReference() int

// MO entry point — smsf.go (declaration), full path nas_bridge.go
type MOResult struct { OK bool; ID int64; ... }
func ProcessMOSMS(senderIMSI, daMSISDN, text, encoding string) MOResult

// NAS bridge — nas_bridge.go
type NASMOResponse struct {
    CPAck         []byte           // step 5 — EncodeCPAck(ti)
    RPAckCPData   []byte           // step 6 success
    RPErrorCPData []byte           // step 6 fail
    MO            MOResult
}
func ProcessMOSMSFromNAS(senderIMSI string, payload []byte) (*NASMOResponse, error)
func BuildPayloadContainerSMS(cpPDU []byte) []byte
```

## 8. What's not implemented

Grepped TODOs in `nf/smsf/`:

| Area | Status | Source |
|------|--------|--------|
| Nsmsf SBI (TS 29.540 §5.2.2.x — `_Activate`, `_Deactivate`, `_UplinkSMS`, `_MtForwardSm`) | placeholder TODOs | `nsmsf_sbi.go:19-50` |
| ProblemDetails (RFC 7807 + Nsmsf cause set §6.1.6.3) | not emitted | `nsmsf_sbi.go:48-53` |
| MT delivery via Namf_Communication_N1MessageNotify | not implemented | `nsmsf_sbi.go:39-46` |
| TS 24.011 §5.3.2.2 TC1* timer | not plumbed; "rely on the network never losing CP-DATA" | `nas_bridge.go:91-93` |
| RP-ERROR with proper cause from §8.2.5.4 (decode-cause granularity) | always emits cause 21 / 95 / fall-through | `nas_bridge.go:109-111` |
| MS-side RP-ACK / RP-ERROR (response to MT) wired back to MT FSM | not implemented | `nas_bridge.go:118-123` |
| Multi-Payload-Container fragmentation for large SMS over NAS | passthrough only | `nas_bridge.go:180-184` |
| TS 24.011 §8.1.3 — full CP message-type table beyond {Data, Ack, Error} | partial | `codec_decode.go:88` |
| TS 24.011 §8.2.5.3 RP-ACK with optional Status-Report TPDU | not emitted | `codec_decode.go:182` |
| TS 24.011 §8.2.2 RP-SMMA (MTI=4) + reserved | not handled | `codec_decode.go:195` |
| TS 23.040 §9.2.3.12 TP-VP relative / enhanced / absolute formats | partial | `codec_decode.go:356-366` |
| TS 23.038 §4 reserved DCS group `0b11` | TODO | `codec_decode.go:390` |
| TS 23.040 §9.2.3.24 fill-bit padding edge cases | TODO | `codec_decode.go:444` |
| SMS-DELIVER decoder | TODO header at line 477-480 | `codec_decode.go:477` |
| Codec round-trip for SMS-DELIVER | not in `codec_test.go` yet | — |

## 8a. Test coverage

`mmt_studio_core_tester/src/testcases/vas/tc_vas_oam.py` —
operator-API TCs (no UE/gNB needed):

| TC | Coverage |
|----|----------|
| TC-SMS-010 `smsf_stats_envelope` | `/stats` envelope shape (`pending,delivered,failed,expired,total`) |
| TC-SMS-011 `smsf_segment_utility` | `/segment` GSM-7 → UCS-2 fallback when out-of-alphabet chars; bad encoding → 400 |
| TC-SMS-012 `smsf_send_validation` | bad MSISDN / encoding / empty / oversized body → 400 |
| TC-SMS-013 `smsf_routing_crud` | bad route_type / empty pattern → 400; create/list/delete round-trip |
| TC-SMS-014 `smsf_message_not_found` | unknown id → 404; non-int id → 400 |

`tc_sms.py` carries the legacy UE/gNB integration TCs
(TC-SMS-001..004) which exercise MO/MT delivery end-to-end.

## 9. References

Spec citations grepped from `nf/smsf/`:

- **TS 23.038** §4 (DCS), §6.1.2 7-bit packing, §6.2.1 default
  alphabet, §6.2 user-data encoding
- **TS 23.040** §9.1.2.5 Address fields, §9.2 TPDUs, §9.2.2.1
  SMS-DELIVER, §9.2.2.2 SMS-SUBMIT, §9.2.3.5 Status-Report,
  §9.2.3.12 TP-VP, §9.2.3.24 / §9.2.3.24.1 UDH (concat SMS)
- **TS 23.502** §4.13.3 SMS over NAS, §4.13.3.5 MO SMS in CM-CONNECTED
- **TS 24.008** §10.5.4.11 Table 10.5.137 (cause source for §8.2.5.4)
- **TS 24.011** §5.3.2.2 SMC state machine + TC1* timer, §7.2 CP
  layer, §7.3.1.1 / §7.3.3 / §7.3.4 RP procedures, §8.1 CP messages,
  §8.2 RP messages, §8.2.5.1-5.4 RP IE table + RP-Cause
- **TS 24.501** §8.2.10 ULNASTransport, §8.2.11 DLNASTransport,
  §9.11.3.39 Payload Container, §9.11.3.40 Payload Container Type
- **TS 29.540** §5.2 Nsmsf_SMService Service, §6.1 HTTP/2+JSON
  bindings, §6.1.6 Error handling — all TODO scaffold

---
*Last refreshed against commit `13a181d`.*
