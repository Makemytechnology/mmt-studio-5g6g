# AMF / NGAP — Porting status

Reference: `../../../mmt_studio_core/nf/amf/ngap/*.py` (~12.5k LOC total)

## Landed
- `wire/` — NGAP-PDU envelope codec. Hand-rolled APER encoder/decoder
  that fills the `[]byte` gap in `codecs/asn1-go`'s generated
  `InitiatingMessage` / `SuccessfulOutcome` / `UnsuccessfulOutcome`.
  Round-trip tests cover all three CHOICE branches + every criticality.
  Exposes `PeekProcedureCode()` for cheap dispatcher pre-filter.
- `transport.go` / `transport_stub.go` / `transport_linux.go` —
  `Listener` / `IncomingConn` interface + TCP stub backend (shared across
  all platforms today). Linux production path needs `ishidawataru/sctp`
  wired in `platformListen`; the interface contract already matches real
  SCTP semantics (stream-aware `Send`/`Recv`, multi-homing-ready).
- `server.go` — accept loop, per-gNB goroutine, cleanup + FM alarm on
  association loss, `RemoveAllForGnb` on disconnect.
- `dispatch.go` — procedureCode demux using `wire.Decode` for full
  envelope parsing; per-procedure handlers register at init time.
- `ngsetup/` — **full NG Setup Request → NGSetupResponse** using the
  generated NGAP codec:
  - Decodes `NGSetupRequest` IEs: GlobalRANNodeID (IE 27),
    RANNodeName (IE 82), SupportedTAList (IE 102), DefaultPagingDRX (IE 21)
  - Populates `gnbctx` (name, id, supported TAs, broadcast PLMNs, slices)
  - Builds `NGSetupResponse` with AMFName, ServedGUAMIList,
    RelativeAMFCapacity from `nf/amf/ctx`
  - Wraps with `wire.Encode` and sends on stream 0
  - Clears correlated SCTP-loss alarm on accept
  - End-to-end test: builds a real request → handler → decodes response
    and verifies every IE round-trips

## Python procedures still to port (in priority order)

| Python file                                            | TS §     | Notes                                                         |
|--------------------------------------------------------|----------|---------------------------------------------------------------|
| `ngap_initial_ue_message.py`                           | 38.413 §8.6.1 | Allocate AMF-UE-NGAP-ID, tee NAS PDU to new `nf/amf/gmm/`     |
| `ngap_downlink_nas_transport.py`                       | 38.413 §8.6.2 | Network → UE NAS transport                                    |
| `ngap_uplink_nas_transport.py`                         | 38.413 §8.6.3 | UE → network NAS transport                                    |
| `ngap_initial_context_setup.py`                        | 38.413 §8.3.1 | Request/Response + UERadioCapability                          |
| `ngap_ue_context_release.py`                           | 38.413 §8.3.3 | UE context release command + complete                         |
| `ngap_pdu_session_resource_setup.py`                   | 38.413 §8.2.1 | PDU session resource setup (coordinate with SMF)              |
| `ngap_pdu_session_resource_modify.py`                  | 38.413 §8.2.3 | PDU session resource modify                                   |
| `ngap_pdu_session_resource_release.py`                 | 38.413 §8.2.4 | PDU session resource release                                  |
| `ngap_paging.py`                                       | 38.413 §8.5.1 | Paging request (CN triggered)                                 |
| `ngap_handover.py`                                     | 38.413 §8.4   | Xn/N2-based handover set                                      |
| `ngap_amf_config_update.py` / `ngap_ran_config_update.py` | 38.413 §8.7.3/8.7.4 | Config update exchange                             |
| `ngap_ng_reset.py`                                     | 38.413 §8.7.2 | Full vs partial NG Reset                                      |
| `ngap_pws.py`                                          | 38.413 §8.9   | Public Warning System broadcast                               |

## Known gaps / TODOs

- **NGSetupFailure IEs** — `sendFailure()` currently emits an empty
  `UnsuccessfulOutcome` envelope. Real code should carry a `Cause` IE
  (TS 38.413 §9.3.1.2). Extend once PLMN / TAC validation lands from
  `infra/plmn` and `infra/tac`.
- **PLMN/TAC validation on NG Setup** — the Python reference rejects
  gNBs whose PLMNs don't intersect the AMF's supported list and warns
  on unknown TACs. The Go port accepts unconditionally today; wire the
  check in once `infra/plmn` ports.
- **Production SCTP** — `transport_linux.go#platformListen` still
  delegates to the TCP stub. Swap in `ishidawataru/sctp` before any
  real gNB interop.
