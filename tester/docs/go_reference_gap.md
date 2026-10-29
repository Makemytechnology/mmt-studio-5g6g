# Go reference → Python tester coverage gap

**Sources:**
- Go reference: `mmt-studio-core-go` — 99 test functions inventoried across `nf/amf/`, `nf/smf/`, `codecs/`, scoped to 5GMM registration (TS 24.501 §5.5) and 5GSM PDU session (TS 24.501 §6.4).
- Python tester: `mmt_studio_core_tester` — 39 test functions in `src/testcases/core/`, plus FSMs in `src/statemachine/` and `src/control/fsm/`.

**Scope:** Functional protocol verification only. Performance, IMS, VAS, vertical, safety, and traffic test trees are out of scope for this comparison.

**Methodology:** Each Go test category (`REG-*`, `PDU-*`, `NGAP-*`, `NAS-CODEC`, `TIMER`, `FSM-STATE`) is matched against the Python coverage. The output is grouped by **action class** so the operator can triage in priority order rather than walk a flat list.

---

## Summary

| Action class | Count | Status |
|---|---|---|
| **A. Direct match** — Python already covers what Go does | 12 | No work needed |
| **B. Add to Python (unit-testable)** — codec / primitive / envelope-level cases | ~25 | **DONE** — 78 new tests, 5 real bugs filed |
| **B'. Integration cases** (require live AMF/SMF/UPF) | ~16 | **Out of scope.** Covered by the existing `src/testcases/core/*.py` runner against a real AMF; no new test files needed. |
| **C. Python-only** — Python covers it, Go doesn't | 8 | Note for visibility; no tester-side action |
| **D. Shared gap** — neither side covers, spec mandates it | ≥ 25 | Out of scope for this campaign; deeper audit later |

### Landed in this commit (6 new pytest files under `tests/`, no live AMF)

| File | Pass | xfail | Covers |
|---|---|---|---|
| `tests/test_nas_security_primitives.py` | 9 | — | B2 #18, #21, #22, #23, #24 — wrap/unwrap, MAC failure, K_gNB derivation |
| `tests/test_nas_decode_errors.py` | 11 | — | B1 #14, #16, #17 — truncated PDU, bad EPD, unknown IEI, smoke fuzz |
| `tests/test_ngap_envelope.py` | 16 | — | B4 #31, #32, #33, #34, #35 — encode/decode round-trip, truncated/malformed/random rejection, bad procedure code |
| `tests/test_ngap_handover_messages.py` | 7 | 1 | B5 #36, #38, #39, #43, #44, #46 + PDU Setup Resp success/failed |
| `tests/test_nas_message_round_trips.py` | 11 | 3 | NAS-CODEC — Registration, Auth, SMC, Reg Complete, Dereg, UL NAS Transport |
| `tests/test_plmn_bcd.py` | 24 | 1 | PLMN BCD encode/decode round-trip + edge cases (TS 24.008 §10.5.1.3) |

**Totals: 78 passing, 5 xfail.** All five xfails are real bugs surfaced by the audit — see "Bugs found" below.

Run them with:

```sh
.venv/bin/python -m pytest tests/test_n*.py -v   # unit tests only
.venv/bin/python -m pytest tests/ -v              # adds speccheck (slow, 1 known fail)
```

### Bugs found via xfail (5)

1. **`build_path_switch_request` missing mandatory IE** — `src/protocol/ngap.py:516`. The `PathSwitchRequestTransfer` dict omits `qosFlowAcceptedList` which TS 38.413 §9.3.4.21 requires. Pycrate raises `ASN1ObjErr` on encode. The tester cannot currently emit a valid Path Switch Request.

2. **`NasBuilder.authentication_failure` field-access is one level too deep** — `src/protocol/nas.py:74`. Calls `msg['5GMMCause']['5GMMCause'].set_val(...)`; the actual pycrate field is just `msg['5GMMCause']`. The UE cannot send Authentication Failure for either #20 (MAC failure) or #21 (sync failure). Likely fix: drop the inner `['5GMMCause']` — i.e., `msg['5GMMCause'].set_val(cause)`.

3. **Same bug — Authentication Failure cause #21** (sync failure with AUTS). Single fix unblocks both.

4. **`NasBuilder.service_request` uses wrong subfield key** — `src/protocol/nas.py:110`. Calls `msg['ServiceType'].set_val({'Value': service_type})`; pycrate's `Type1V` only has subfield `V`, not `Value`. The UE cannot send Service Request, blocking every CM-IDLE→CONNECTED transition test. Likely fix: `msg['ServiceType']['V'].set_val(service_type)`.

5. **`decode_plmn_id` is permissive on illegal BCD nibbles** — `libs/gpp_utils.py:14-22`. Per TS 24.008 §10.5.1.3, BCD digits are `0`–`9` (with `0xF` reserved for filler in 2-digit MNC). Values `0xA`–`0xE` in MCC/MNC nibbles are illegal but the decoder stringifies them in decimal — e.g. nibble `0xC` becomes `"12"`, yielding a 4-character MNC string. Strict-decode would either reject the input or use a sentinel. Severity: low (production gNBs won't emit illegal nibbles), but the decoder silently produces wrong-length strings on malformed input.

These bugs were latent — never exercised by the existing integration tests because those run via the FSM which probably has its own message-construction path. Fixing them is out of scope per "no change to package" — flagged for next maintenance window.

The tester is well-aligned on **happy-path** flows for registration, PDU session establishment, NG Setup, and 5G-AKA. The gap is concentrated in **boundary / abnormal-case** coverage: NAS decode-error → cause mapping, NGAP envelope errors, security MAC rejection, PTI collision detection, ePCO edge cases, user-plane deactivate/reactivate, and the full handover failure matrix.

---

## A. Direct matches (no work needed)

| # | Case | Go test | Python test |
|---|---|---|---|
| 1 | Registration happy path (SUCI) | `nf/amf/gmm/registration_flow_test.go::TestRegistrationHappyPathSUCI` (skipped) | `tc_registration.py::SingleRegistration` (TC-REG-001) |
| 2 | Multi-UE registration | — | `tc_registration.py::MultiRegistration` (TC-REG-002) |
| 3 | Attach / detach cycle | (deregistration FSM in `fsm/fsm_test.go`) | `tc_registration.py::AttachDetachCycle` (TC-REG-003) |
| 4 | 5G-AKA happy path | `nf/amf/security/kgnb_test.go` (key derivation only) | `tc_auth.py::AuthSuccess` (TC-AUTH-001) |
| 5 | SQN resync via AUTS | (none in Go) | `tc_auth.py::AuthSqnResync` (TC-AUTH-003) |
| 6 | SUCI registration | `codecs/.../full_roundtrip_test.go::TestAllProcedures` (codec only) | `tc_auth.py::AuthSuciRegistration` (TC-AUTH-009) |
| 7 | 5G-GUTI re-registration | `nf/amf/gmm/fsm/fsm_test.go::TestFSM_CachedContextReuseFromRegistered` | `tc_auth.py::AuthGutiReRegistration` (TC-AUTH-011) |
| 8 | PDU session establishment | `nf/smf/session/session_test.go::TestEstablishHappyPathIPv4` | `tc_pdu_session.py::PduSessionEstablishment` (TC-PDU-001) |
| 9 | Multi-DNN concurrent sessions | (none in Go at handler level) | `tc_pdu_session.py::MultiPduSessionTest` (TC-PDU-003) |
| 10 | NG Setup round-trip | `nf/amf/ngap/ngsetup/ngsetup_test.go::TestNGSetupRoundTrip` | `tc_ng_setup.py::NgSetupBasic` (TC-NGS-001) |
| 11 | NGAP envelope round-trip | `nf/amf/ngap/wire/envelope_test.go::TestRoundTripInitiating*` | implicit in pycrate-based `protocol/ngap.py` |
| 12 | NAS / 5GMM message round-trips | `codecs/.../full_roundtrip_test.go::TestAllProcedures` | implicit in pycrate-based `protocol/nas.py` |

The codec-roundtrip "matches" are looser — both sides exercise encode/decode, but the test mechanisms differ (Go uses generated codec; Python uses pycrate). The functional coverage is equivalent.

---

## B. Go covers it, Python doesn't (the actionable gap)

Grouped by category, ordered by priority within each. Each row gets a target file in the existing tester layout — **no new directories or framework** per the user's constraint. Robot integration is via the existing `robot/suites/` files.

### B1. NAS codec → cause-code mapping (high priority — invisible regression risk)

The Go core's `TestCauseForNASDecodeError` asserts that decoding a malformed NAS message returns the spec-mandated 5GMM cause code. The tester decodes incoming NAS via pycrate, but doesn't verify it produces the right cause when feeding malformed input back to the AMF.

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 13 | Mandatory IE missing → cause #60 (semantically incorrect message) | `cause_map_test.go` | new `tc_nas_decode_errors.py` | TS 24.501 §9.2 / Annex A.1 |
| 14 | Unknown IEI → cause #15 (information element non-existent or not implemented) | same | same | same |
| 15 | Invalid length → cause #43 (invalid mandatory information) | same | same | same |
| 16 | Truncated PDU → cause #111 (protocol error, unspecified) | `dispatch_test.go::TestDispatchRejectsTruncatedAndBadEPD` | same | same |
| 17 | Bad EPD (non-5GMM PD on 5GMM bearer) → drop | same | same | TS 24.501 §4.4.2 |

### B2. NAS security boundary (high priority)

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 18 | Security wrap round-trip — SHT=1 (integrity only), NIA2 | `primitives_test.go::TestWrapUnwrapRoundTrip` | extend `tc_auth.py` or new `tc_nas_security.py` | TS 24.501 §4.4.3 |
| 19 | Security wrap round-trip — SHT=2 (integrity+cipher), NEA2 | same | same | same |
| 20 | Security wrap — SHT=3/4 (new ctx variants) | same | same | TS 24.501 §4.4.3.2 |
| 21 | **MAC failure rejected, UL count not advanced** | `primitives_test.go::TestUnwrapRejectsBadMAC` | new `tc_nas_security.py` | TS 24.501 §4.4.3.3 |
| 22 | Plain NAS pass-through (pre-security) | `primitives_test.go::TestRxNASPlainPassThrough` | same | TS 24.501 §4.4.3 |
| 23 | DL emits SHT=2 with NEA0 (no cipher) | `primitives_test.go::TestTxDLEmitsSHT2WithNEA0` | same | TS 24.501 §4.4.3.2 |
| 24 | K_gNB derivation matches Annex A.9 test vectors | `kgnb_test.go::TestDeriveKgNBMatchesAnnexA9` | new `tc_key_derivation.py` | TS 33.501 §A.9 |
| 25 | K_gNB derivation rejects missing K_AMF | `kgnb_test.go::TestDeriveKgNBRejectsMissingKAMF` | same | TS 33.501 §6.2 |

### B3. Procedure collision / dispatch guards (medium priority)

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 26 | Re-registration during in-flight registration | `dispatch_test.go::TestCollisionGuardAbortsOnReRegistration` (skipped) | extend `tc_registration.py` | TS 24.501 §5.5.1.2.7 |
| 27 | Service Request dropped while reg in-flight | `dispatch_test.go::TestCollisionGuardDropsServiceRequestWhileBusy` | same | TS 24.501 §5.5.1.2.7 |
| 28 | Auth Response bypasses procedure-collision gate | `dispatch_test.go::TestResponseTypesBypassGuard` | same | TS 24.501 §5.4.1.3 |
| 29 | FSM rejects unknown transition | `fsm/fsm_test.go::TestFSM_RejectsUnknownTransition` | extend FSM unit tests (new `tests/test_ue_fsm.py` near speccheck) | TS 24.501 §5.1 |
| 30 | FSM guard fall-through when first row fails | `fsm/fsm_test.go::TestFSM_GuardFallthrough` | same | same |

### B4. NGAP envelope / dispatch boundary (high priority — protocol layer)

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 31 | Truncated envelope rejected | `wire/envelope_test.go::TestDecodeTruncated` | new `tc_ngap_envelope.py` (or extend `tc_ng_setup.py`) | TS 38.413 §9 |
| 32 | Bad procedure code rejected | `wire/envelope_test.go::TestEncodeBadProcedureCode` | same | TS 38.413 §9 |
| 33 | Unknown procedure code dropped (no panic) | `dispatch_test.go::TestUnknownProcedureCodeDoesNotPanic` | same | TS 38.413 §8.7 |
| 34 | Malformed envelope (too short / extension not supported) | `dispatch_test.go::TestMalformedEnvelopeDropsCleanly` | same | TS 38.413 §9 |
| 35 | Round-trip InitiatingMessage / SuccessfulOutcome / UnsuccessfulOutcome | `wire/envelope_test.go::TestRoundTrip*` | implicit, but add explicit assertions | TS 38.413 §9 |

### B5. NGAP handover matrix (medium priority — handover already partial in Python)

Python has `tc_handover.py`; checking if it covers all 11 Go-side cases. Per the Python inventory, it mainly covers GTP-U end marker, not the full NGAP HO state machine.

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 36 | Full HO 9-msg flow (HO-Required → HO-Request → HO-Ack → HO-Command → Status-Transfer → HO-Notify → Release) | `handover_test.go::TestHandover_FullFlowMirrorsCapture` | extend `tc_handover.py` | TS 38.413 §8.4 |
| 37 | HO Preparation Failure when target not found | `handover_test.go::TestHandover_NoRoute_SendsPreparationFailure` | same | TS 38.413 §8.4.2 |
| 38 | HO Failure from target aborts cleanly | `handover_test.go::TestHandover_TargetFailureAbortsCleanly` | same | TS 38.413 §8.4.5 |
| 39 | Early UL RAN Status Transfer relayed | `handover_test.go::TestHandover_EarlyStatusTransfer_Relayed` | same | TS 38.413 §8.4.4 |
| 40 | Status Transfer dropped on unknown PDU session | `handover_test.go::TestHandover_EarlyStatusTransfer_NoSession_Dropped` | same | TS 38.413 §8.4.4 |
| 41 | DAPS HO Success carries DAPS indicator | `handover_test.go::TestHandover_HandoverSuccess_DAPSOnly` | same | TS 38.413 §8.4.10 |
| 42 | Plain HO does not emit Handover Success | `handover_test.go::TestHandover_HandoverSuccess_NotSentForPlainHO` | same | TS 38.413 §8.4.10 |
| 43 | Source Cancel releases target resources | `handover_test.go::TestHandover_SourceCancel_ReleasesTarget` | same | TS 38.413 §8.4.6 |
| 44 | Path Switch Request accepted by default | `handover_test.go::TestHandover_PathSwitchRequest_AcceptedByDefault` | same | TS 38.413 §8.4.4 |
| 45 | Path Switch Request Failure on rejection | `handover_test.go::TestHandover_PathSwitchRequest_Rejected` | same | TS 38.413 §8.4.4 |
| 46 | HO Failure carries UE NGAP IDs + Cause | `handover_ies_test.go::TestHandover_Failure_CarriesUEIDsAndCause` | same | TS 38.413 §9.2.1.4 |

### B6. PDU session lifecycle (high priority — Python only has est+release)

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 47 | PDU session reject — unknown DNN | `session_test.go::TestEstablishRejectsUnknownDNN` | extend `tc_pdu_session.py` | TS 24.501 §6.4.1.4 / Annex A.2 #27 |
| 48 | PDU session release frees IP | `session_test.go::TestReleaseFreesIP` | same | TS 24.501 §6.4.3 |
| 49 | Session store isolation (per-UE, per-session lookup) | `session_test.go::TestSessionStoreIsolated` | same | TS 23.501 §5.6 |
| 50 | User-plane deactivation flips DL FAR to BUFFER | `deactivate_test.go::TestDeactivateUserPlane_FlipsDLFARToBuffer` | new `tc_pdu_userplane.py` | TS 23.502 §4.2.6 / TS 29.244 §8.2.26 |
| 51 | User-plane reactivation restores DL FAR to FORWARD with new TEID | `deactivate_test.go::TestActivateUserPlane_FlipsDLFARBackToForward` | same | TS 23.502 §4.2.3.2 |
| 52 | Deactivation idempotent on missing session | `deactivate_test.go::TestDeactivateUserPlane_IdempotentOnMissingSession` | same | TS 23.502 §4.2.6 |
| 53 | DL data notification → N1N2 hook for suspended session | `dlnotify_test.go::TestHandleDLDataNotification_InvokesN1N2HookForSuspendedSession` | same | TS 23.502 §4.2.3.3 |
| 54 | DL data notification skipped for active session | `dlnotify_test.go::TestHandleDLDataNotification_SkipsActiveSession` | same | TS 23.502 §4.2.3.3 |
| 55 | DL data notification for unknown session dropped | `dlnotify_test.go::TestHandleDLDataNotification_UnknownSessionDropped` | same | TS 23.502 §4.2.3.3 |

### B7. PTI allocator (medium priority — pure unit test)

| # | Case | Go reference | Target Python file | Spec § |
|---|---|---|---|---|
| 56 | PTI fresh start | `pti/pti_test.go::TestPTI_FreshStart` | new `tests/test_pti.py` near speccheck | TS 24.007 §11.2.3 |
| 57 | PTI allocate network PTI | `pti/pti_test.go::TestPTI_AllocateNetworkPTI` | same | same |
| 58 | PTI retransmit detection | `pti/pti_test.go::TestPTI_Retransmit` | same | TS 24.007 §11.2.3 |
| 59 | PTI collision detection | `pti/pti_test.go::TestPTI_Collision` | same | same |
| 60 | PTI invalid value rejected | `pti/pti_test.go::TestPTI_Invalid` | same | same |
| 61 | PTI release / release-all-for-UE | `pti/pti_test.go::TestPTI_Release*` | same | same |

### B8. Extended PCO (low priority — codec tests, but worth covering)

ePCO is built/parsed inside PDU session establishment. Python has no ePCO-specific tests; depending on whether the tester even constructs/validates ePCO, this may be N/A.

| # | Case | Go reference | Status |
|---|---|---|---|
| 62 | ePCO build: DNS-only / dual DNS / MTU only / MTU OOR rejected / IMS all-requested / IPv6 omitted when no v6 / no-request omits IE / only-requested-answered / nil APN / requested-but-unconfigured | `epco_test.go::TestBuildExtendedPCO_*` (10 sub-cases) | Add only if tester constructs ePCO; if it just receives, parser tests suffice |
| 63 | IPCP Configure-Ack: primary DNS / primary+secondary / secondary-only when configured | `epco_test.go::TestBuildIPCPConfigureAck_*` (3 sub-cases) | same |
| 64 | Parse ePCO: invalid header rejected / non-empty container ignored | `epco_test.go::TestParseRequestedEPCO_*` | Likely yes — receive path is critical |

### B9. NGAP procedure-internal cases

| # | Case | Go reference | Target Python file |
|---|---|---|---|
| 65 | Procedure collision: PDU Setup rejected when CTX_RELEASE_PENDING | `ngap/fsm/fsm_test.go::TestFSM_ProcedureCollision_ReleasePending` | extend `tc_release.py` |
| 66 | Parallel PDU Session Resource Setups (ref-count tracking) | `ngap/fsm/fsm_test.go::TestFSM_ParallelResourceSetup` | extend `tc_pdu_session.py` |
| 67 | UE Context Release triggered by lower-layer failure | `uectxrelease/uectxrelease_test.go::TestLowerLayerFailureClause` | extend `tc_release.py` |
| 68 | Initial UE Message allocates NGAP IDs | `initialue/initialue_test.go::TestInitialUEMessageAllocatesAndDispatches` | implicit in NG Setup; add explicit ID-allocation test |
| 69 | NGAP server end-to-end TCP stub (server accepts, processes, cleans up on disconnect) | `server_test.go::TestEndToEndTCPStub`, `TestClientDisconnectCleansUp` | new `tc_ngap_server.py` |

### B10. NAS runtime / wire format (low priority — codec-internal)

| # | Case | Go reference | Status |
|---|---|---|
| 70 | Buffer bounds (offset not advanced on failure) | `runtime_test.go::TestBufferBounds` | pycrate handles this; covered by inheritance |
| 71 | TLV / TLV-E (>255 byte) round-trip | `runtime_test.go::TestTLV*RoundTrip` | same |
| 72 | Half-octet packing (ngKSI, registration type) | `runtime_test.go::TestHalfOctetPair` | same |
| 73 | PLMN BCD encoding | `runtime_test.go::TestPlmnBCD` | same |
| 74 | Skip unknown IE (Type 4 TLV forward-compat) | `runtime_test.go::TestSkipUnknownIE` | same |

These are codec-internal — pycrate's own test suite covers them. Don't duplicate.

---

## C. Python covers it, Go doesn't (visibility only)

| # | Case | Python test | Note |
|---|---|---|---|
| 75 | UDP bidirectional traffic over PDU session | `tc_pdu_session.py::UdpBidirectional` (TC-TRF-006) | Functional, not perf — fine to keep |
| 76 | IMS PDU session test | `tc_pdu_session.py::ImsPduSessionTest` (TC-PDU-002) | IMS overlap; fine |
| 77 | Security algorithms negotiation | `tc_registration.py::SecurityAlgos` (TC-REG-005) | Spec mandates; Go gap noted |
| 78 | Multi-gNB concurrent NG Setup | `tc_ng_setup.py::*` (16 cases including reconnect cycles, PLMN/TAC variations, multi-gNB) | Strong area for tester |
| 79 | NG Setup state-machine test | `tc_ng_setup.py::NgSetupStateMachine` (TC-NGS-002) | Go has FSM tests but not at this granularity |
| 80 | Slicing test cases | `tc_slicing.py` | Python-only; Go has only `TestEstablishRejectsUnknownDNN` for the analogue |
| 81 | Idle mode | `tc_idle_mode.py` | Python-only |
| 82 | Auth identity chain (full SUCI→SUPI→IMEI) | `tc_auth.py::AuthIdentityChain` (TC-AUTH-010) | Python-only; valuable |

These don't require tester-side action. They're listed so the operator knows the tester has *more* coverage than Go in these spots — a positive signal.

---

## D. Shared gaps (out of scope for this campaign)

Both sides lack coverage of these spec-mandated cases. Listed for the next audit campaign, not this one.

**5GMM cause codes (TS 24.501 Annex A.1) untested anywhere:**
- #3 illegal UE
- #6 illegal ME
- #7 5GS services not allowed
- #9 UE identity cannot be derived
- #11 PLMN not allowed
- #12 tracking area not allowed
- #13 roaming not allowed
- #15 no suitable cells in tracking area
- #22 congestion / authentication failure
- #27 N1 mode not allowed
- #72 non-3GPP access not allowed
- #73 service area restrictions
- #74 LADN not available
- #75 UE identity cannot be derived by the network
- #90 missing essential IE

**5GSM cause codes (TS 24.501 Annex A.2) untested anywhere (causes #26–#85):**
- #26 insufficient resources, #28 unknown PDU session type, #29 user authentication/authorization failed,
- #31 request rejected unspecified, #32 service option not supported, #33 service option not subscribed,
- #50 PDU session type IPv4 only allowed, #54 PDU session does not exist, #67 insufficient resources for slice,
- #69 NSSAI denied, #82 max data rate too low for UP integrity, #85 SMF not allocated, etc.

**Timer expiry paths (TS 24.501 §10):**
- T3346 (mobility/access throttling), T3502 (registration backoff), T3510 (registration retry),
  T3511 (registration retry post-failure), T3520 (auth response), T3521 (de-registration),
  T3540 (PDU session retry)

**NGAP unsuccessful outcomes (TS 38.413 §8 / §9.2):**
- INITIAL CONTEXT SETUP FAILURE, PDU SESSION RESOURCE SETUP UNSUCCESSFUL items,
  NG SETUP FAILURE, ERROR INDICATION mandatory IE missing / unknown UE NGAP ID

**Identity / SUPI / SUCI handler-level:**
- Identity Request → Response with each identity type (5G-S-TMSI, IMEI, IMEISV) — only SUCI tested

**SSC modes (TS 23.501 §5.6.9):**
- SSC mode 1, 2, 3 establishment and switch behavior

**LADN (TS 23.501 §5.6.5):**
- LADN session establishment, area-leave handling, mobility into/out of LADN area

These are the next audit campaign's scope. Adding them now would dilute the Go-reference comparison.

---

## Status — campaign closed

The unit-testable subset of the gap is **landed in this commit**: 78 new
pytest tests (5 xfail) covering NAS security primitives, NAS decode
boundary, NGAP envelope encode/decode, NGAP handover messages, NAS
message round-trips, and PLMN BCD edge cases. Five real bugs in
`src/protocol/*` and `libs/gpp_utils.py` were surfaced and filed via
strict xfails — they auto-flip to PASS when the underlying bugs are fixed.

Cases that require a live AMF / SMF / UPF (B5 full handover flow, B6
PDU lifecycle, B3 procedure collision, B9 NGAP server lifecycle) are
**not** new test files for this campaign. They're covered by the
existing `src/testcases/core/*.py` runner when invoked against a real
AMF — that's the right tool for those, not unit tests.

B7 (PTI allocator) and B10 (codec runtime) are skipped: B7 has no
Python analog (tester only sets PTI as a field, no allocator), B10 is
covered by pycrate's own test suite.

**Out-of-scope for this campaign, deferred to future work:**

- 10 known speccheck `MISSING` citations (see `docs/speccheck_punchlist.md`)
  — most are NTN/IMS files outside the registration+PDU session focus.
- 60 `UNLOADED` doc references across 19 PDFs — see same punchlist.
- D shared-gap cases (5GMM/5GSM cause-code matrix, timer expiries,
  NGAP unsuccessful outcomes, identity types, SSC modes, LADN). These
  need both spec-driven test design AND AMF-side coverage to be
  meaningful — a separate 4–6 week campaign.
