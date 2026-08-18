// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Codec scaffold for layer-3 supplementary-service signaling —
// TS 24.080 §3.6 (Facility information element) and §4.5
// (Operations and errors implementation).
//
// In the IMS-anchored 5GS this file is intentionally minimal:
// activation / deactivation / interrogation flow over IMS XCAP
// (TS 24.623) plus signaling over SIP REFER / INVITE per
// TS 24.604 §4.5, TS 24.611 §4.5.x, TS 24.610 §4.5, etc., not via
// MAP/SS-Operation Facility encodings. The legacy Facility-IE form
// is still useful for:
//
//   - CS fall-back UEs that drop into TS 24.008 §10.5.4.15 Facility
//     IE during a CS call;
//   - USSD on legacy networks per TS 24.090 / TS 24.080 §2.5;
//   - cross-codec test fixtures shared with the Python tester.
//
// Every implemented function carries the §-clause it implements;
// every TODO is anchored to the §-clause that defines what's missing.

package supplementary

// ─────────────────────────────────────────────────────────────────
// SS-Operation codes — TS 24.080 §4.5 (imports from MAP-SS / SS-Ops).
//
// The actual operation-code numerical values come from the imported
// MAP modules (MAP-SupplementaryServiceOperations and SS-Operations)
// which assign each operation an ASN.1 OPERATION value via MAP
// TS 29.002 §17.6.4. The codes below are the well-known assignments
// callers rely on; whenever an op-code is added it must be checked
// against the local PDF for both TS 24.080 §4.5 (whether it's still
// imported there) and TS 29.002 §17.6.4 (the numeric assignment).
// ─────────────────────────────────────────────────────────────────
const (
	OpRegisterSS                Op = 10
	OpEraseSS                   Op = 11
	OpActivateSS                Op = 12
	OpDeactivateSS              Op = 13
	OpInterrogateSS             Op = 14
	OpNotifySS                  Op = 16 // TS 24.080 §4.5 — net→MS
	OpRegisterPassword          Op = 17
	OpGetPassword               Op = 18
	OpProcessUnstructuredSSData Op = 19 // legacy USSD (deprecated, TS 22.090)
	OpForwardCheckSSIndication  Op = 38
	OpProcessUnstructuredSSReq  Op = 59 // network-initiated USSD per TS 22.090
	OpUnstructuredSSRequest     Op = 60 // user-initiated USSD per TS 22.090
	OpUnstructuredSSNotify      Op = 61
	OpBuildMPTY                 Op = 30 // TS 22.084 / §4.5
	OpHoldMPTY                  Op = 31
	OpRetrieveMPTY              Op = 32
	OpSplitMPTY                 Op = 33
	OpExplicitCT                Op = 53 // TS 22.091 / TS 24.629
	OpCallDeflection            Op = 117
)

// Op is a TS 24.080 §3.6.4 Operation Code value. Carried on the wire
// inside a Facility IE as: tag(0x02) | length | INTEGER(value).
type Op int

// ─────────────────────────────────────────────────────────────────
// Facility IE component tags — TS 24.080 §3.6.2 Table 3.7.
// The Facility IE is a sequence of one or more "components"; each
// component starts with one of the tags below, followed by length
// and TLV body.
// ─────────────────────────────────────────────────────────────────
const (
	ComponentInvoke       byte = 0xA1 // Invoke component — §3.6.2
	ComponentReturnResult byte = 0xA2 // ReturnResult component — §3.6.2
	ComponentReturnError  byte = 0xA3 // ReturnError component — §3.6.2
	ComponentReject       byte = 0xA4 // Reject component — §3.6.2
)

// IEIFacility is the Facility information element identifier when
// the IE is carried inside a TS 24.008 layer-3 message (e.g.
// FACILITY message, REGISTER message). See TS 24.008 §10.5.4.15.
const IEIFacility byte = 0x1C

// EncodeInvokeFrame builds a minimal Invoke component for an
// SS-Operation per TS 24.080 §3.6.2. Layout:
//
//	0xA1                     -- Invoke tag (§3.6.2)
//	len_invoke               -- short or long form per X.690
//	0x02 0x01 invokeID       -- Invoke ID (§3.6.3, INTEGER)
//	0x02 0x01 opCode         -- Operation Code (§3.6.4, INTEGER)
//	[parameter SEQUENCE]     -- §3.6.5, omitted here
//
// The caller provides the body parameter encoding; this helper just
// concatenates the fixed-shape header. Multi-byte invoke IDs / op
// codes (negative INTEGERs in particular) are NOT yet handled — the
// Op values we actually use today all fit in a single byte.
func EncodeInvokeFrame(invokeID byte, op Op, params []byte) []byte {
	body := []byte{
		0x02, 0x01, invokeID, // §3.6.3
		0x02, 0x01, byte(op), // §3.6.4
	}
	body = append(body, params...)
	if len(body) > 0x7F {
		// X.690 long-form length only; multi-octet body lengths use
		// 0x81 / 0x82 prefix. We don't currently exercise this path.
		return nil
	}
	return append([]byte{ComponentInvoke, byte(len(body))}, body...)
}

// TODO(spec: TS 24.080 §3.6.1): full Component decoder. We encode
// Invoke frames above but don't yet parse incoming
// ReturnResult / ReturnError / Reject components — those carry the
// outcome of a network-initiated SS operation (e.g. forwardCheckSS
// indication) that the UE side or MAP gateway peers consume.
//
// TODO(spec: TS 24.080 §3.6.5): ASN.1 Sequence/Set parameter
// encoding. Each operation in §4.5 brings its own ARGUMENT type
// (TS 29.002 §17.6.4) — RegisterSS-Arg carries SS-Code + DN +
// basicService etc. We don't synthesise those bodies yet.
//
// TODO(spec: TS 24.080 §4.3): error responses (e.g.
// systemFailure(34), illegalSS-Operation(16),
// SS-Incompatibility(20), unknownAlphabet(71)) need a typed Error
// struct + EncodeError frame builder for the negative path.
//
// TODO(spec: TS 24.008 §10.5.4.15): Facility IE container that
// wraps the Invoke/ReturnResult components inside a CS layer-3
// FACILITY / REGISTER message (IEI 0x1C, length-prefixed). Not
// needed for IMS-anchored flows but needed for CS fall-back tests.
//
// TODO(spec: TS 24.090 / TS 24.080 §2.5): unstructured SS data
// (USSD) request/response framing. Today USSD lives in
// services/ussd/ as plain string state; the on-air encoding via
// processUnstructuredSS-Request (op 59) is not generated.
//
// TODO(spec: TS 24.623): XCAP service-config encoding for CFU /
// CFB / CFNRy / CFNRc / CW / CB. The IMS-anchored activation path
// uses HTTP PUT to <document-uri>/simservs.xml; we currently store
// the active flag in the supplementary_services SQL table only,
// without rendering the XCAP XML body.
//
// TODO(spec: TS 24.604 §4.5.1 / §4.5.2): map service.Activate() /
// service.Deactivate() to the §4.5 SIP signaling sequences (REGISTER
// for IMS provisioning, INVITE 3xx for CDIV invocation). The
// in-process supplementary CRUD currently has no link from
// activation to actual signaling.
