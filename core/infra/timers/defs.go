// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package timers — 3GPP timer definitions + central timer manager.
//
// All durations are time.Duration (the Python reference used seconds
// as floats — here they are typed so callers cannot accidentally pass
// nanoseconds or milliseconds to a seconds-expecting API).
//
// 3GPP references on each constant match the Python port (timer_defs.py).
package timers

import "time"

// ── 5G NAS timers (TS 24.501 §10.2) ─────────────────────────────────────
//
// All values below are PDF-verified against
// specs/3gpp/ts_124501v190602p.pdf (TS 24.501
// v19.6.2). The spec splits these across two tables:
//
//   Table 10.2.1 (p.1131) — UE side
//   Table 10.2.2 (p.1133) — AMF side
//
// We define both tables here because the AMF (a) broadcasts some
// UE-side values (T3502, T3512) inside Registration Accept /
// Configuration Update, and (b) writes the complete set into the
// NTN timer-audit snapshot. Timers the AMF actually arms are in
// the "AMF side" block; UE-side constants are reference only and
// MUST NOT be re-used as AMF guard timers.
//
// Retry counts (N-values): the PDF documents each via normative
// "This retransmission is repeated four times" text in the
// abnormal-cases subsections. Retry count = 4 across the board for
// AMF-side timers; see NASMaxRetransmit below.

const (
	// ─── Table 10.2.2: AMF side ────────────────────────────────────────
	// (PDF line refs relative to `pdftotext -layout`.)
	//
	// T3513 — NOTE 4: "The value of this timer is network dependent"
	//         (PDF p.1136). No spec-mandated default; 10 s is our
	//         operational choice. Retransmit triggered by PAGING
	//         procedure per §5.6.2.2 / §5.6.2.3.
	T3513 = 10 * time.Second

	// T3522 — 6 s (Table 10.2.2 row @ PDF L74951). Retransmit: 4
	//         (PDF L36158 "retransmission is repeated four times").
	//         Cause of start: transmission of MT DEREGISTRATION
	//         REQUEST (§5.5.2.3.2).
	T3522 = 6 * time.Second

	// T3550 — 6 s (Table 10.2.2 row @ PDF L74961). Retransmit: 4
	//         (PDF L28357 / L34437). Cause of start: transmission of
	//         REGISTRATION ACCEPT (§5.5.1.2.4 / §5.5.1.3.4).
	T3550 = 6 * time.Second

	// T3555 — 6 s (Table 10.2.2 row @ PDF L74971). Retransmit: 4
	//         (PDF L21042). Cause of start: transmission of
	//         CONFIGURATION UPDATE COMMAND with ACK bit set
	//         (§5.4.4.2).
	T3555 = 6 * time.Second

	// T3560 — 6 s (Table 10.2.2 row @ PDF L74981). Retransmit: 4
	//         (PDF L17829). Cause of start: transmission of
	//         AUTHENTICATION REQUEST (§5.4.1.3.2) OR SECURITY MODE
	//         COMMAND (§5.4.2.2) — the spec reuses the same timer
	//         for both legs; distinct OnExpiry events in our FSM
	//         keep the action-side distinguishable.
	T3560 = 6 * time.Second

	// T3570 — 6 s (Table 10.2.2 row @ PDF L91-of-table-dump).
	//         Retransmit: 4 (PDF L19413). Cause of start:
	//         transmission of IDENTITY REQUEST (§5.4.3.2).
	T3570 = 6 * time.Second

	// ─── Table 10.2.1: UE side — reference only, do NOT use as AMF guard ─
	//
	// When the codebase re-used these as AMF guards it was wrong
	// (e.g. T3516 "AMF waits for Auth Response" — spec T3516 is UE
	// side; AMF's real auth guard is T3560 above). These entries
	// are kept so we can (a) broadcast them to the UE where the
	// spec mandates the network to echo the value, and (b) decode
	// UE-side behaviour in traces for debugging.
	T3502 = 720 * time.Second   // §5.5.1.2.5 — UE retry after Registration Reject (12 min)
	T3510 = 15 * time.Second    // §5.5.1.2.2 — UE retransmits REGISTRATION REQUEST
	T3511 = 10 * time.Second    // §5.5.1.2.7 — UE retry after Registration failure
	T3512 = 21600 * time.Second // §5.3.1    — UE periodic registration update (6 h, NAS 0x26)
	T3516 = 30 * time.Second    // §5.4.1.3.7 c) — UE authentication-replay protection (MAC-failure timer)
	T3517 = 15 * time.Second    // §5.6.1.5 — UE service request retransmit
	T3519 = 60 * time.Second    // §5.4.1.2 — UE RAND / RES* anti-replay storage
	T3520 = 15 * time.Second    // §5.4.2.3 — UE waits for Security Mode Command result
	T3521 = 15 * time.Second    // §5.5.2.2.2 — UE MO De-registration — waits for DEREG ACCEPT
)

// ── 5G SM — SMF (TS 24.501 Table 10.3.2) ────────────────────────────────

const (
	T3590 = 15 * time.Second
	T3591 = 16 * time.Second
	T3592 = 16 * time.Second
	T3593 = 60 * time.Second
)

// 5G SM — UE side (TS 24.501 Table 10.3.1) — reference only.
const (
	T3580 = 16 * time.Second
	T3581 = 16 * time.Second
	T3582 = 16 * time.Second
	T3583 = 60 * time.Second
)

// ── EPC NAS — MME (TS 24.301) ───────────────────────────────────────────

const (
	T3412 = 3240 * time.Second
	T3410 = 15 * time.Second
	T3413 = 10 * time.Second
	T3450 = 6 * time.Second
	T3460 = 6 * time.Second
	T3470 = 6 * time.Second
	T3485 = 8 * time.Second
	T3486 = 8 * time.Second
	T3489 = 4 * time.Second
	T3495 = 8 * time.Second
)

// ── NGAP (TS 38.413) ────────────────────────────────────────────────────
//
// PDF reference: specs/3gpp/
// ts_138413v190200p.pdf (TS 38.413 v19.2.0). Unlike TS 24.501 which
// has a numbered timer table, TS 38.413 does not define named timers
// for AMF-side procedure guards — §8.3.x describes the procedures
// themselves but leaves guard-timer values entirely to the
// implementation. The values below are ours; no normative citation
// exists for them. Python reference used matching values.
const (
	TWaitUECtxRelease = 10 * time.Second // Guard on UEContextReleaseComplete (§8.3.3)
	THandoverPrep     = 10 * time.Second // Guard on Handover Required → Request/Response (§8.4.2)
	TRelocOverall     = 10 * time.Second // Overall handover relocation guard (§8.4.2.2)
	TNGSetup          = 10 * time.Second // Guard on NG Setup Response (§8.7.1)
	// TWaitICSResponse — guard on InitialContextSetupResponse
	// (§8.3.1). 30 s gives a loaded gNB headroom to process parallel
	// ICS Requests during burst registration without the AMF
	// prematurely giving up. Matches Python reference.
	TWaitICSResponse = 30 * time.Second
)

// ── S1AP (TS 36.413) ────────────────────────────────────────────────────

const (
	TS1WaitUECtxRelease = 10 * time.Second
	TS1HandoverPrep     = 10 * time.Second
	TS1RelocOverall     = 10 * time.Second
)

// ── PFCP (TS 29.244) ────────────────────────────────────────────────────

const (
	TPFCPHeartbeat   = 10 * time.Second
	TPFCPRetransmit  = 3 * time.Second
	NPFCPRetries     = 3
)

// ── IMS / SIP (TS 24.229 / RFC 3261) ────────────────────────────────────

const (
	TSIPT1        = 500 * time.Millisecond
	TSIPT2        = 4 * time.Second
	TSIPTimerB    = 32 * time.Second
	TSIPTimerF    = 32 * time.Second
	TIMSRegExpire = 3600 * time.Second
	TIMSSubExpire = 600 * time.Second
)

// ── Retransmission defaults ─────────────────────────────────────────────

const (
	NASMaxRetransmit = 4
	EPSMaxRetransmit = 4
)
