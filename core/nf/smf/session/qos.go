// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Minimal TS 24.501 §9.11.4.13 Authorized-QoS-Rules encoder.
//
// The Python reference builds a detailed rule set (default match-all +
// per-flow rules keyed by 5-tuple). For the Go skeleton we emit a single
// "default rule, match-all" QoS rule that establishes a working default
// QoS flow on the UE — enough for the UE to route packets via the UPF
// after PDU Session Establishment Accept. Per-flow rules land with the
// PCF port.
//
// Wire format (TS 24.501 §9.11.4.13):
//
//	OuterHeader : [ QoSRuleID(8) | LengthOfQoSRule(16) | RuleOpCode(3) +
//	                DQR(1) + NumPktFilters(4) ]
//	PktFilters  : for each filter →
//	              [ Spare(2) + Dir(2) + Id(4) | Len(8) |
//	                Component(Type + Value...) ]
//	Tail        : [ Precedence(8) | Spare(2) + QFI(6) ]
package session

import "encoding/binary"

// BuildDefaultQoSRule returns the bytes of a single "default, match-all"
// QoS rule (TS 24.501 §9.11.4.13 — Rule Operation Code = 1 "Create new
// QoS rule", DQR=1, one match-all packet filter).
//
// The match-all filter is required by many UE implementations (Python
// reference always emits it). Without it, the UE may ignore the rule
// entirely and no default flow gets installed → UL/DL data dropped.
// Raw wire for default rule: 01 00 06 31 31 01 01 ff 01
//
//	01     QoS rule ID = 1
//	00 06  length of rule body = 6
//	31     op=001 (create new) | DQR=1 | NumFilters=1 → 0b0011_0001
//	31     filter: spare(00) | dir=11 (bidirectional, bits 6-5) |
//	       PFI=0001 (bits 4-1) → 0b0011_0001
//	01     filter contents length = 1
//	01     packet filter component type = 1 (match-all)
//	ff     precedence = 255
//	01     spare(00) | QFI (6 bits) = 1
//
// Per §9.11.4.13 Figure 9.11.4.13.4 the filter-header octet is laid
// out `spare(bit 8-7) | direction(bit 6-5) | PFI(bit 4-1)`. The
// "PFI=0 on create" rule in the spec applies only to UE→network
// messages (MODIFY QoS Rule in ULNAS); network→UE assigns real IDs,
// and many UE implementations reject PFI=0 on authorized rules.
// Default rule has exactly one filter so we hard-code PFI=1.
func BuildDefaultQoSRule(ruleID, qfi uint8, precedence uint8) []byte {
	// Op byte: op=001 "Create new" | DQR=1 | NumFilters=1 → 0b001_1_0001 = 0x31
	opByte := byte(0x31)

	// One match-all filter: dir=bidirectional (11) in bits 6-5,
	// PFI=1 in bits 4-1, contents length = 1, component type = 1
	// (match-all, no value). Full byte: 0b00_11_0001 = 0x31.
	filter := []byte{0x31, 0x01, 0x01}

	// Tail: precedence + (Spare(2) | QFI(6))
	tail := []byte{precedence, qfi & 0x3F}

	inner := []byte{opByte}
	inner = append(inner, filter...)
	inner = append(inner, tail...)

	lenBuf := make([]byte, 2)
	binary.BigEndian.PutUint16(lenBuf, uint16(len(inner)))

	out := []byte{ruleID}
	out = append(out, lenBuf...)
	out = append(out, inner...)
	return out
}
