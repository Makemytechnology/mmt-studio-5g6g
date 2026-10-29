// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// TS 24.501 NAS QoS IE encoders for the §6.4.2 PDU Session
// Modification flow (and reused by §6.4.1 establishment).
//
// Spec anchors (PDF: specs/3gpp/ts_124501v190602p.pdf):
//
//   * §9.11.4.12 "QoS flow descriptions" — the AuthorizedQoSFlow
//     Descriptions IE in PDU SESSION MODIFICATION COMMAND
//     (§8.3.9.8) and ESTABLISHMENT ACCEPT (§8.3.2.9).
//   * §9.11.4.13 "QoS rules" — the AuthorizedQoSRules IE in
//     §8.3.9.6 + §8.3.2.5.
//
// These two IEs are the wire-form of the §5.7 QoS model: rules
// classify uplink user traffic + identify downlink QoS flows
// (§9.11.4.13 NOTE), and the flow descriptions tell the UE what
// 5QI / GFBR / MFBR each QFI carries (§9.11.4.12 + §6.2.5.1.1.4).
//
// Today we emit the minimum viable shape — one rule + one flow
// description per QFI, no packet filters, with 5QI as the only
// flow parameter — which is enough for the UE to enumerate the
// new bearers and report them to upper layers (e.g. RTP socket
// QoS marking). GFBR / MFBR / Averaging Window parameters land
// in a follow-up that wires bandwidth from the PCF decision.
//
// The byte-level layouts here match the figures verbatim against
// the in-tree PDF; comment headers cite octet positions so a
// reader with the spec open can confirm the layout without
// running the encoder.
package session

import (
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// QoSRuleSpec is the SMF-side view of a §9.11.4.13 QoS rule.
type QoSRuleSpec struct {
	// RuleID is the QoS rule identifier (octet 4 of §9.11.4.13.2,
	// 1..255). 0 is reserved for "no QoS rule identifier assigned"
	// per the §9.11.4.13 IE coding for the QFI nibble — caller
	// should pass a non-zero value.
	RuleID uint8

	// QFI binds this rule to the QoS flow (octet m+2, low 6 bits).
	// 1..63 per §9.11.4.12 QFI coding.
	QFI uint8

	// Default = true sets the DQR bit in octet 7 ("the QoS rule is
	// the default QoS rule").
	Default bool

	// Precedence in octet m+1 (0..255, lower = higher priority per
	// §6.2.5.1.1.2).
	Precedence uint8

	// Delete = true emits opcode 0b010 "Delete existing QoS rule"
	// (§9.11.4.13.2 octet 7 binary table). The UE matches by RuleID
	// only (§9.11.4.13 abnormal case 9). Precedence and QFI fields
	// are still emitted (length-of-rule = 3) but ignored by the UE
	// for the Delete operation. Cannot be combined with Default = true:
	// §9.11.4.13.2 abnormal case 4 forbids deleting the default rule.
	Delete bool
}

// QoSFlowSpec is the SMF-side view of a §9.11.4.12 flow description.
type QoSFlowSpec struct {
	// QFI in octet 4 (low 6 bits, 1..63).
	QFI uint8

	// FiveQI is encoded as a parameter (parameter ID 0x01) in the
	// parameters list. TS 23.501 §5.7.4 Table 5.7.4-1 standardised
	// 5QI values.
	FiveQI uint8

	// Delete = true emits opcode 0b010 "Delete existing QoS flow
	// description" (§9.11.4.12.2 octet 5 binary table). The UE matches
	// by QFI only; the parameters list is empty (E = 0, num = 0), so
	// the entry shrinks to a 3-byte (octets 4 / 5 / 6) fixed header.
	Delete bool
}

// Operation codes per §9.11.4.12 (octet 5 bits 8-6) and §9.11.4.13
// (octet 7 bits 8-6). Both IEs share the same 3-bit opcode space —
// 001=Create, 010=Delete (§9.11.4.13.2 + §9.11.4.12.2 binary tables).
const (
	qosFlowOpCreate uint8 = 0b001 // §9.11.4.12 octet 5 — Create new QoS flow description
	qosFlowOpDelete uint8 = 0b010 // §9.11.4.12 octet 5 — Delete existing QoS flow description
	qosRuleOpCreate uint8 = 0b001 // §9.11.4.13 octet 7 — Create new QoS rule
	qosRuleOpDelete uint8 = 0b010 // §9.11.4.13 octet 7 — Delete existing QoS rule
)

// Parameter identifiers per §9.11.4.12 octet 7+:
//
//	01H 5QI
//	02H GFBR uplink
//	03H GFBR downlink
//	04H MFBR uplink
//	05H MFBR downlink
//	06H Averaging window
//	07H EPS bearer identity
const (
	flowParam5QI uint8 = 0x01
)

// BuildAuthorizedQoSRules produces the IE *contents* (the bytes that
// follow the §9.11.4.13.1 IEI + length envelope) for a list of QoS
// rules. The caller drops this into nas.AuthorizedQoSRules.Value
// and the parent encoder writes the LV-E envelope around it.
//
// One rule layout (§9.11.4.13.2, m=7 when no packet filters):
//
//	octet 4:    QoS rule identifier
//	octet 5-6:  Length of QoS rule contents (16-bit BE), counts
//	            from octet 7 inclusive.
//	octet 7:    [opcode 3b][DQR 1b][num_packet_filters 4b]
//	octet 8..m: Packet filter list (empty when num=0)
//	octet m+1:  QoS rule precedence (1 octet)
//	octet m+2:  [Spare 1b][Segregation 1b][QFI 6b]
//
// We always emit num_packet_filters=0 and Segregation=0; with
// Length-of-rule = 3 (octets 7, m+1=8, m+2=9). Total per rule = 6
// octets (1 ID + 2 length + 3 contents).
func BuildAuthorizedQoSRules(rules []QoSRuleSpec) []byte {
	if len(rules) == 0 {
		return nil
	}
	out := make([]byte, 0, 6*len(rules))
	for _, r := range rules {
		// Octet 7: opcode << 5 | DQR << 4 | num_filters.
		// §9.11.4.13.2 abnormal case 4: Delete on the default rule
		// is forbidden — clamp DQR to 0 if both flags were set.
		op := qosRuleOpCreate
		dqr := uint8(0)
		if r.Default {
			dqr = 1
		}
		if r.Delete {
			op = qosRuleOpDelete
			dqr = 0
		}
		op7 := (op << 5) | (dqr << 4) // num_filters = 0
		// Octet m+2: Segregation=0, QFI in low 6 bits. For Delete
		// the UE keys on RuleID only; precedence + QFI are still
		// emitted to keep length-of-rule = 3.
		omplus2 := r.QFI & 0x3F

		out = append(out,
			r.RuleID,           // octet 4: rule identifier
			0x00, 0x03,         // octets 5-6: length-of-rule = 3
			op7,                // octet 7
			r.Precedence,       // octet m+1
			omplus2,            // octet m+2
		)
	}
	return out
}

// BuildAuthorizedQoSFlowDescriptions produces the IE contents for
// §9.11.4.12 with one entry per supplied flow.
//
// One flow description (§9.11.4.12.2, with a 5QI-only parameters
// list per §9.11.4.12.4):
//
//	octet 4:    [Spare 2b][QFI 6b]
//	octet 5:    [opcode 3b][Spare 5b]
//	octet 6:    [Spare 1b][E 1b][num_parameters 6b]
//	octet 7:    parameter identifier (0x01 = 5QI)
//	octet 8:    length of parameter contents (1 octet for 5QI)
//	octet 9:    parameter contents — the 5QI value
//
// E=1 ("parameters list is included") + num=1. Total per flow = 6 octets.
func BuildAuthorizedQoSFlowDescriptions(flows []QoSFlowSpec) []byte {
	if len(flows) == 0 {
		return nil
	}
	out := make([]byte, 0, 6*len(flows))
	for _, f := range flows {
		if f.Delete {
			// §9.11.4.12.2 Delete: opcode=010, E=0, num_parameters=0.
			// UE keys on QFI only — entry shrinks to 3 bytes (octets
			// 4 / 5 / 6) with no parameters list.
			out = append(out,
				f.QFI&0x3F,                // octet 4
				qosFlowOpDelete<<5,        // octet 5
				0x00,                      // octet 6: E=0, num=0
			)
			continue
		}
		op5 := qosFlowOpCreate << 5 // Create new QoS flow description
		// Octet 6: spare=0, E=1, num_parameters=1.
		o6 := byte(0x40 | 0x01) // 0b01000001

		out = append(out,
			f.QFI&0x3F, // octet 4
			op5,        // octet 5
			o6,         // octet 6
			flowParam5QI,
			0x01,       // length of 5QI contents = 1
			f.FiveQI,   // 5QI value
		)
	}
	return out
}

// SpecsFromPolicyDecision derives the (rules, flows, qfiByService)
// triple for the §8.3.9 PDU SESSION MODIFICATION COMMAND from an
// SmPolicyDecision. Each PCC rule's 5QI gets one flow description +
// one QoS rule. QFI numbering starts at 2 (the default flow is QFI 1
// from the initial Establishment); rule IDs start at 2 (rule 1 is
// the default rule). All rules are non-default ("Create new QoS
// rule", DQR=0) because the default rule is already in place from
// the §6.4.1.3 Accept.
//
// qfiByService maps PccRule.ServiceName → allocated QFI so the symmetric
// rule-delete path (BYE / AF Delete) can look up "which QFI did we
// install for this PCF service" without re-deriving from a stale
// decision. Same key the PCF uses to identify the dynamic rule
// (TS 29.512 §5.6.2.4 PccRule pccRuleId — service-name flavoured
// for the in-process port).
//
// `existing` is the session's current ServiceName → QFI mapping
// (sess.QFIByRule). Rules whose ServiceName is already present are
// skipped — TS 23.501 v19.7.0 §5.7.1.5 makes the QFI "unique within
// a PDU Session" and stable for the lifetime of a QoS Flow, so a
// follow-up §4.3.3 Modification (e.g. AF activates conv_video while
// conv_voice is already installed) must NOT renumber existing flows.
// Only services not in `existing` produce new specs, and their QFIs
// are allocated above the highest QFI currently in use to keep the
// numbering monotonic across consecutive Modifications.
func SpecsFromPolicyDecision(rules []pcf.PCCRule, existing map[string]uint8) ([]QoSRuleSpec, []QoSFlowSpec, map[string]uint8) {
	if len(rules) == 0 {
		return nil, nil, nil
	}
	log := logger.Get("smf.policymod.qos")
	var ruleSpecs []QoSRuleSpec
	var flowSpecs []QoSFlowSpec
	qfiByService := map[string]uint8{}
	// Allocator state — start above any QFI already in use so existing
	// flows aren't disturbed (§5.7.1.5 stable QFI lifetime). QFI 1 is
	// the default flow established at PDU Session Establishment, so
	// the floor is at least 2.
	next := uint8(2)
	for _, q := range existing {
		if q >= next {
			next = q + 1
		}
	}
	inUse := make(map[uint8]struct{}, len(existing))
	for _, q := range existing {
		inUse[q] = struct{}{}
	}
	for _, r := range rules {
		if r.IsDefault || r.FiveQI == 0 {
			// Default rule is provisioned at Establishment, skip on
			// modification. 5QI=0 is "reserved" per §9.11.4.12 5QI
			// coding so don't emit a flow description for it.
			continue
		}
		// Skip rules whose service already has a QFI bound — they're
		// installed and stable. Re-emitting them as a Create would
		// renumber the flow on the UE and trip §5.7.1.5.
		if r.ServiceName != "" {
			if _, ok := existing[r.ServiceName]; ok {
				continue
			}
		}
		// Find the next free QFI ≥ next, skipping ones in use.
		for ; next <= 63; next++ {
			if _, taken := inUse[next]; !taken {
				break
			}
		}
		if next > 63 {
			log.Warnf("PDU mod: QFI exhausted at 63, dropping further rules")
			break
		}
		qfi := next
		next++
		inUse[qfi] = struct{}{}
		ruleSpecs = append(ruleSpecs, QoSRuleSpec{
			RuleID:     qfi, // QFI also doubles as a stable rule ID
			QFI:        qfi,
			Default:    false,
			Precedence: 100,
		})
		flowSpecs = append(flowSpecs, QoSFlowSpec{
			QFI:    qfi,
			FiveQI: uint8(r.FiveQI),
		})
		if r.ServiceName != "" {
			qfiByService[r.ServiceName] = qfi
		}
	}
	return ruleSpecs, flowSpecs, qfiByService
}

