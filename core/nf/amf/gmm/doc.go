// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

// Package gmm hosts the AMF's 5GMM state machine (TS 24.501 §5.5).
//
// ── Architectural policy: events in, SBI out ────────────────────────
//
// Target shape:
//
//	┌─────────┐   NAS over N1   ┌──────────┐
//	│   UE    │ ──────────────▶ │   gNB    │
//	└─────────┘                 └────┬─────┘
//	                                 │ NGAP over N2
//	                                 ▼
//	                           ┌──────────┐
//	                           │   NGAP   │  (nf/amf/ngap/…)
//	                           └────┬─────┘
//	                                │ event (in-process)
//	                                ▼
//	                           ┌──────────┐
//	                           │   GMM    │  (this package)
//	                           └────┬─────┘
//	                                │ event (in-process, DL path)
//	                                ▼
//	                           ┌──────────┐
//	                           │   NGAP   │  ← dlnas.Send, ICS, UE-CtxRelease
//	                           └──────────┘
//
//	                 SBI reference points (HTTP/2, TS 23.501 §4.2.6):
//	                   N8  → UDM    (Nudm)
//	                   N11 → SMF    (Nsmf)
//	                   N12 → AUSF   (Nausf)
//	                   N15 → PCF    (Npcf)
//	                   N22 → NSSF   (Nnssf)
//
// The rule:
//
//   - Intra-AMF communication between subsystems (GMM ↔ NGAP, GMM ↔
//     SCTP FSM, etc.) MUST go through events. Same process, same
//     address space, but the call-site shape is an event dispatch,
//     not a direct function call. Reason: spec procedures are
//     specified as message flows; modelling them as events keeps our
//     code faithful to the spec's shape and makes introducing a new
//     NF or splitting the monolith tractable later.
//
//   - Inter-NF communication across 5G reference points (AMF ↔ SMF,
//     AMF ↔ UDM, AMF ↔ AUSF, AMF ↔ PCF, AMF ↔ NSSF, …) MUST use an
//     SBI call shape. Even when the peer NF lives in the same process
//     today (our monolith), the call should look like a Service-Based
//     Interface invocation (request/response over HTTP/2 in a split
//     build). This guarantees that the interface contract — operation
//     names, IDs, resource URIs, error mapping — follows TS 29.5xx
//     and not our internal function signatures.
//
//     Normative SBI references available locally under specs/3gpp/:
//
//       ts_129502v190600p.pdf  Nsmf   (N11)  — SMF
//       ts_129503v190600p.pdf  Nudm   (N8)   — UDM
//       ts_129509v190500p.pdf  Nausf  (N12)  — AUSF
//       ts_129510v190600p.pdf  Nnrf   (Nnrf) — NRF (NF discovery,
//                              NFRegister / NFDeregister / NFStatusSubscribe)
//       ts_129518v190600p.pdf  Namf   (N14 + consumer-facing) — AMF
//       ts_129531v190600p.pdf  Nnssf  (N22)  — NSSF
//       ts_129571v190600p.pdf  Common Data Types for SBI
//                              (ProblemDetails at TS 29.571 §5.2.4
//                              — a 3GPP extension of IETF RFC 7807;
//                              plus SUPI, GPSI, PEI, PLMN, error
//                              responses)
//
//     Architecture + procedures (Stage 2, cited throughout audit):
//
//       ts_123003v190600p.pdf  Numbering, addressing, identification
//                              (5G-GUTI §2.10.1, PLMN BCD §2.2,
//                              NR CGI §19.6, 5G-TMSI §2.10.1)
//       ts_123501v190700p.pdf  5G system architecture
//                              (reference points §4.2.6, NF §6.2,
//                              NSSAI §5.15)
//       ts_123502v190700p.pdf  5G system procedures (Stage 2)
//                              (Registration §4.2.2.2, PDU Session
//                              §4.3, Handover §4.9, …)
//
//     5G security normatives (shared with gmm + ngap crypto paths):
//
//       ts_133501v190600p.pdf  5G security architecture
//                              (KDFs §A.*, NAS security §6.4,
//                              AS security §6.9, primary auth §6.1)
//       ts_133102v190100p.pdf  3G security architecture
//                              (UMTS AKA §6.3, SQN re-sync §6.3.5)
//
//     NGAP / NG-C transport:
//
//       ts_138412v190000p.pdf  NG signalling transport
//                              (SCTP streams + PPID §7)
//
//     Data-plane / interworking (referenced by UPF / N26 paths):
//
//       ts_129281v190200p.pdf  GTP-U
//       ts_129274v190600p.pdf  GTPv2-C  (EPS / N26)
//       ts_129060v190000p.pdf  GTP-C    (EPC)
//
//     Feature NAS (referenced as TODO guards for disabled features):
//
//       ts_124587v190300p.pdf  V2X
//       ts_124554v190500p.pdf  5G ProSe
//       ts_124577v190300p.pdf  A2X
//       ts_131102v190400p.pdf  USIM
//
// Exceptions that stay as plain helpers (NOT events, NOT SBI):
//   - Pure value helpers (encoders, bit packers, cause tables).
//   - State getters/setters on a UE context object that the caller
//     already owns.
//   - Crypto primitives (NAS MAC, NAS encrypt) — the "service" here is
//     purely computational, no peer NF.
//
// ── Current state ───────────────────────────────────────────────────
//
// Today most inter-subsystem calls in this package are plain function
// calls. Call sites carry `TODO(arch: event)` or `TODO(arch: sbi-<Nxx>)`
// markers identifying the target shape. Grep:
//
//	grep -rn 'TODO(arch:' nf/amf/gmm/
//
// to enumerate them. Key hot spots:
//
//   - dlnas.Send(...)               → TODO(arch: event: DL-NAS to NGAP)
//   - initialctxsetup.Send(...)     → TODO(arch: event: ICS to NGAP)
//   - session.Release / Establish   → TODO(arch: sbi-N11: Nsmf_PDUSession)
//   - ausf.GenerateAV / UpdateSQN   → TODO(arch: sbi-N12: Nausf_UEAuthentication)
//   - udm.GetDefault*               → TODO(arch: sbi-N8:  Nudm_SDM)
//   - nssf.SelectAllowedNSSAI       → TODO(arch: sbi-N22: Nnssf_NSSelection)
//
// The existing GMM/NGAP FSM infrastructure (fsm.Of(ue).Fire, Actions,
// TimerSpec) is the substrate events will ride on. The SBI substrate
// does not yet exist; it will land when we split out SMF/AUSF/UDM/PCF/
// NSSF as real services. Until then the SBI TODOs mark where the
// call shape should flip.
package gmm
