// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package wire — NGAP-PDU envelope codec (TS 38.413 §9.1 / §9.3.4.1).
//
// The generated NGAP codec in codecs/asn1-go emits InitiatingMessage /
// SuccessfulOutcome / UnsuccessfulOutcome as three opaque []byte fields
// because its resolver doesn't cross-link NGAP-CommonDataTypes. This
// package fills the gap with a hand-rolled APER envelope built on top of
// the same low-level runtime.PerBitData writer the generated code uses.
//
// The envelope is fixed-shape across every NGAP release:
//
//	NGAP-PDU ::= CHOICE { initiatingMessage, successfulOutcome, unsuccessfulOutcome, ... }
//	InitiatingMessage ::= SEQUENCE {
//	    procedureCode  NGAP-ELEMENTARY-PROCEDURE.&procedureCode ({NGAP-ELEMENTARY-PROCEDURES}),
//	    criticality    NGAP-ELEMENTARY-PROCEDURE.&criticality   ({...}{@procedureCode}),
//	    value          NGAP-ELEMENTARY-PROCEDURE.&InitiatingMessage ({...}{@procedureCode})
//	}
//	(SuccessfulOutcome / UnsuccessfulOutcome have the same shape.)
//
// Wire mapping (APER, aligned) — per TS 38.413 (NGAP-PDU-Descriptions.asn
// and NGAP-CommonDataTypes.asn):
//
//	CHOICE extension bit  : 1 bit (NGAP-PDU HAS "..." → extensible, bit emitted; 0 = root branch)
//	CHOICE index          : 2 bits (0=InitiatingMessage, 1=Successful, 2=Unsuccessful)
//	procedureCode         : INTEGER(0..255)      — 8 bits, aligned → one octet
//	criticality           : ENUMERATED(reject/ignore/notify) — NON-extensible,
//	                        just a 2-bit constrained-whole, NO extension bit
//	value                 : OPEN TYPE            — length determinant + octets
//
// Important non-bugs to remember:
//   - `InitiatingMessage` / `SuccessfulOutcome` / `UnsuccessfulOutcome` are
//     all plain SEQUENCE (no "..."). Non-extensible → NO sequence extension bit.
//   - `Criticality` has no "..." → NO enumerated extension bit.
//
// A previous version emitted spurious extension bits for both the SEQUENCE
// and the Criticality ENUMERATED, which shifted the criticality value one
// bit to the right on the wire. Strict decoders (Wireshark's NGAP dissector,
// real gNBs) read crit from bits 7-6 of byte 2, so our "ignore" (intended
// 0x40) landed as 0x20 and was decoded as "reject" — hence the
// long-running "why does Wireshark say reject?" mystery.
//
// The OPEN TYPE "value" is APER-opaque: its contents are the APER-encoding
// of the concrete procedure PDU (NGSetupRequest, InitialUEMessage, …).
package wire

import (
	"errors"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	runtime "github.com/mmt/asn1go/pkg/runtime"
)

// MessageType names the NGAP-PDU CHOICE alternative. Values match the
// generated `genngap.NGAPPDUPresent*` discriminators (1-based, with
// Nothing=0 reserved as the "no value" sentinel) so both layers speak
// the same enum. A distinct Go type — not a type alias — so we can hang
// a String() method off it for clean log formatting.
//
// APER on the wire uses a 0-based 2-bit CHOICE index; the +1 offset is
// hidden inside Encode/Decode.
type MessageType int

const (
	MessageTypeNothing  MessageType = genngap.NGAPPDUPresentNothing             // 0 — invalid to encode
	InitiatingMessage   MessageType = genngap.NGAPPDUPresentInitiatingMessage   // §9.3.4.1
	SuccessfulOutcome   MessageType = genngap.NGAPPDUPresentSuccessfulOutcome   // §9.3.4.2
	UnsuccessfulOutcome MessageType = genngap.NGAPPDUPresentUnsuccessfulOutcome // §9.3.4.3
)

// String renders the CHOICE name — handy for log lines.
func (m MessageType) String() string {
	switch m {
	case InitiatingMessage:
		return "InitiatingMessage"
	case SuccessfulOutcome:
		return "SuccessfulOutcome"
	case UnsuccessfulOutcome:
		return "UnsuccessfulOutcome"
	}
	return fmt.Sprintf("MessageType(%d)", int(m))
}

// Criticality is a type alias for the NGAP codec's generated enum so
// wire-layer callers and IE-level callers share a single source of truth
// (TS 38.413 §9.3.1.2 `Criticality ::= ENUMERATED {reject, ignore, notify}`).
// Re-exporting the constants lets wire-side code stay import-clean without
// pulling in the full NGAP codec package at every call site.
type Criticality = genngap.Criticality

const (
	CriticalityReject = genngap.CriticalityReject
	CriticalityIgnore = genngap.CriticalityIgnore
	CriticalityNotify = genngap.CriticalityNotify
)

// Envelope is the parsed NGAP-PDU header + inner open-type bytes.
// The inner bytes are the APER-encoding of the concrete procedure PDU
// (NGSetupRequest, InitialUEMessage, DownlinkNASTransport, …).
type Envelope struct {
	Type          MessageType
	ProcedureCode int64
	Criticality   Criticality
	Value         []byte
}

// Decode parses an NGAP-PDU from wire bytes. Returns a descriptive error
// when the first two bits select an unsupported CHOICE extension. Equivalent
// to DecodeNext for the common single-PDU case.
func Decode(b []byte) (*Envelope, error) {
	env, _, err := DecodeNext(b)
	return env, err
}

// DecodeNext parses one NGAP-PDU from the front of b and reports how many
// bytes it consumed. SCTP is free to bundle multiple DATA chunks into a
// single packet (RFC 4960 §6.10), so a single recvmsg/read may deliver
// two or more concatenated NGAP-PDUs; callers loop on DecodeNext until
// the buffer is empty.
func DecodeNext(b []byte) (*Envelope, int, error) {
	if len(b) < 2 {
		return nil, 0, errors.New("ngap envelope: truncated")
	}
	r := runtime.NewReader(b, true)
	extBit, err := r.GetBits(1)
	if err != nil {
		return nil, 0, fmt.Errorf("ngap envelope: ext bit: %w", err)
	}
	if extBit == 1 {
		return nil, 0, errors.New("ngap envelope: extension branches not supported")
	}
	choice, err := r.GetBits(2)
	if err != nil {
		return nil, 0, fmt.Errorf("ngap envelope: choice tag: %w", err)
	}
	if choice > 2 {
		return nil, 0, fmt.Errorf("ngap envelope: unknown choice %d", choice)
	}

	// InitiatingMessage / SuccessfulOutcome / UnsuccessfulOutcome are all
	// NON-extensible SEQUENCE — no sequence extension bit on the wire.
	procCode, err := r.GetConstrainedWhole(0, 255)
	if err != nil {
		return nil, 0, fmt.Errorf("ngap envelope: procedureCode: %w", err)
	}

	// Criticality is NON-extensible ENUMERATED — 2-bit value, no ext bit.
	critVal, err := r.GetConstrainedWhole(0, 2)
	if err != nil {
		return nil, 0, fmt.Errorf("ngap envelope: criticality: %w", err)
	}

	// OPEN TYPE "value" — APER §10.2: unconstrained length determinant, then octets.
	if err := r.AlignReadToByte(); err != nil {
		return nil, 0, fmt.Errorf("ngap envelope: align before open type: %w", err)
	}
	length, err := r.GetLengthDeterminant(0, 0, false)
	if err != nil {
		return nil, 0, fmt.Errorf("ngap envelope: open type length: %w", err)
	}
	val := make([]byte, length)
	for i := range val {
		by, err := r.GetBits(8)
		if err != nil {
			return nil, 0, fmt.Errorf("ngap envelope: open type octet %d: %w", i, err)
		}
		val[i] = byte(by)
	}

	// Consumed byte count = bit position rounded up to the next byte. Open
	// type octets land on byte boundaries after AlignReadToByte, so this is
	// exact, not a best-effort.
	consumed := int((r.Pos + 7) / 8)

	// Bridge the +1 offset: wire CHOICE index 0 → InitiatingMessage (=1
	// in our API, matching genngap.NGAPPDUPresentInitiatingMessage), etc.
	return &Envelope{
		Type:          MessageType(choice) + InitiatingMessage,
		ProcedureCode: procCode,
		Criticality:   Criticality(critVal),
		Value:         val,
	}, consumed, nil
}

// Encode serializes an envelope back to wire bytes. Criticality must be a
// root-branch value (reject/ignore/notify); extensions are not supported.
func Encode(env *Envelope) ([]byte, error) {
	if env == nil {
		return nil, errors.New("ngap envelope: nil")
	}
	if env.Type < InitiatingMessage || env.Type > UnsuccessfulOutcome {
		return nil, fmt.Errorf("ngap envelope: unknown choice %d", env.Type)
	}
	if env.ProcedureCode < 0 || env.ProcedureCode > 255 {
		return nil, fmt.Errorf("ngap envelope: procedureCode %d out of range", env.ProcedureCode)
	}
	if env.Criticality < 0 || env.Criticality > 2 {
		return nil, fmt.Errorf("ngap envelope: criticality %d out of root range", env.Criticality)
	}

	w := runtime.NewWriter(true)

	// NGAP-PDU CHOICE (extensible — "..." present in the ASN.1):
	//   1-bit extension marker (0 = root branch) + 2-bit root index.
	// Convert API-level Type (matches genngap.NGAPPDUPresent*, 1-based,
	// with Nothing=0 reserved) to the wire CHOICE index (0-based).
	if err := w.PutBits(0, 1); err != nil {
		return nil, err
	}
	if err := w.PutBits(uint64(env.Type-InitiatingMessage), 2); err != nil {
		return nil, err
	}

	// InitiatingMessage / SuccessfulOutcome / UnsuccessfulOutcome are
	// NON-extensible SEQUENCE — no extension marker bit on the wire.

	// procedureCode (INTEGER 0..255) — aligned, one octet.
	if err := w.PutConstrainedWhole(env.ProcedureCode, 0, 255); err != nil {
		return nil, err
	}

	// Criticality (ENUMERATED { reject, ignore, notify }) — NON-extensible;
	// just a 2-bit constrained-whole with no extension marker.
	if err := w.PutConstrainedWhole(int64(env.Criticality), 0, 2); err != nil {
		return nil, err
	}

	// OPEN TYPE "value": align, length determinant, octets.
	if err := w.AlignToByte(); err != nil {
		return nil, err
	}
	if err := w.PutLengthDeterminant(uint64(len(env.Value)), 0, 0, false); err != nil {
		return nil, err
	}
	for _, b := range env.Value {
		if err := w.PutBits(uint64(b), 8); err != nil {
			return nil, err
		}
	}
	return w.Bytes(), nil
}

// PeekProcedureCode returns the procedureCode byte without running the full
// envelope decoder. Callers must still run Decode() if they need the payload.
//
// Layout (aligned PER): the CHOICE extension bit, 2-bit choice index, and
// SEQUENCE extension bit occupy only the first 4 bits of byte 0. Because
// procedureCode is `INTEGER(0..255)` its constrained whole encoding aligns
// to the next octet boundary — i.e. the procedureCode always lands in
// byte 1 regardless of which CHOICE alternative / criticality follow.
func PeekProcedureCode(b []byte) (int, bool) {
	if len(b) < 2 {
		return 0, false
	}
	return int(b[1]), true
}
