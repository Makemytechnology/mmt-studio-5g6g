// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

// Package ttmgmt implements the 3GPP TS 24.539 (Rel-19, v19.3.0) port
// management service (PMS) and user plane node management service (UMS)
// messages exchanged between the TSN AF / TSCTSF and the DS-TT / NW-TT
// translators.
//
// These messages are what travels inside the opaque containers defined
// elsewhere:
//
//   - Port Management Information Container — TS 24.501 §9.11.4.27 (NAS,
//     IEI 0x74), TS 29.244 IE 202 (PFCP), TS 29.512/29.514
//     PortManagementContainer.portManCont (N7/N5);
//   - User plane node Management Information Container — TS 29.512/29.514
//     BridgeManagementContainer.bridgeManCont, carried over N4 in the TSC
//     Management Information grouped IEs (TS 29.244 IEs 199-201).
//
// Message layouts follow TS 24.539 §8 (message functional definitions) and
// §9 (information element coding). PMS and UMS share the same list/status/
// update-result shapes; they differ only in the message-type space (§9.1 vs
// §9.5A) and the parameter-name space (Table 9.2.1 vs Table 9.5B.1).
package ttmgmt

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// ---------------------------------------------------------------------------
// Message types
// ---------------------------------------------------------------------------

// Port management service message types — TS 24.539 Table 9.1.1.
const (
	MsgManagePortCommand            byte = 0x01
	MsgManagePortComplete           byte = 0x02
	MsgPortManagementNotify         byte = 0x03
	MsgPortManagementNotifyAck      byte = 0x04
	MsgPortManagementNotifyComplete byte = 0x05
	MsgPortManagementCapability     byte = 0x06
)

// User plane node management service message types — TS 24.539 Table 9.5A.1.
const (
	MsgManageUPNodeCommand       byte = 0x01
	MsgManageUPNodeComplete      byte = 0x02
	MsgUPNodeManagementNotify    byte = 0x03
	MsgUPNodeManagementNotifyAck byte = 0x04
)

// ---------------------------------------------------------------------------
// Operation codes — TS 24.539 Table 9.2.1 / Table 9.5B.1 (same values)
// ---------------------------------------------------------------------------

const (
	OpGetCapabilities          byte = 0x01
	OpReadParameter            byte = 0x02
	OpSetParameter             byte = 0x03
	OpSubscribeNotify          byte = 0x04
	OpUnsubscribe              byte = 0x05
	OpSelectiveRead            byte = 0x06
	OpSelectiveSubscribeNotify byte = 0x07
	OpSelectiveUnsubscribe     byte = 0x08
	OpDeleteParameterEntry     byte = 0x09
)

// opCarriesValue reports whether the operation format includes a
// 2-octet length + value part (TS 24.539 figure 9.2.5 / 9.5B.5).
func opCarriesValue(code byte) bool {
	switch code {
	case OpSetParameter, OpSelectiveRead, OpSelectiveSubscribeNotify,
		OpSelectiveUnsubscribe, OpDeleteParameterEntry:
		return true
	}
	return false
}

// ---------------------------------------------------------------------------
// Port parameter names — TS 24.539 Table 9.2.1
// ---------------------------------------------------------------------------

const (
	PortParamTxPropagationDelay               uint16 = 0x0001
	PortParamTrafficClassTable                uint16 = 0x0002
	PortParamGateEnabled                      uint16 = 0x0003
	PortParamAdminBaseTime                    uint16 = 0x0004
	PortParamAdminControlListLength           uint16 = 0x0005
	PortParamAdminControlList                 uint16 = 0x0006
	PortParamAdminCycleTime                   uint16 = 0x0007
	PortParamTickGranularity                  uint16 = 0x0008
	PortParamTxPropagationDelayDeltaThreshold uint16 = 0x0009
	PortParamAdminCycleTimeExtension          uint16 = 0x000A
	PortParamSupportedListMax                 uint16 = 0x000B
	PortParamQueueMaxSDUTable                 uint16 = 0x000C
	PortParamAdminGateStates                  uint16 = 0x000D

	PortParamLldpV2PortConfigAdminStatusV2  uint16 = 0x0040
	PortParamLldpV2LocChassisIdSubtype      uint16 = 0x0041
	PortParamLldpV2LocChassisId             uint16 = 0x0042
	PortParamLldpV2MessageTxInterval        uint16 = 0x0043
	PortParamLldpV2MessageTxHoldMultiplier  uint16 = 0x0044
	PortParamLldpV2LocPortIdSubtype         uint16 = 0x0060
	PortParamLldpV2LocPortId                uint16 = 0x0061
	PortParamLldpLocSysCap                  uint16 = 0x0062
	PortParamLldpLocManAddrList             uint16 = 0x0063
	PortParamLldpV2RemChassisIdSubtype      uint16 = 0x00A0
	PortParamLldpV2RemChassisId             uint16 = 0x00A1
	PortParamLldpV2RemPortIdSubtype         uint16 = 0x00A2
	PortParamLldpV2RemPortId                uint16 = 0x00A3
	PortParamLldpTTL                        uint16 = 0x00A4
	PortParamLldpRemSysCap                  uint16 = 0x00A5
	PortParamLldpRemManAddrList             uint16 = 0x00A6

	PortParamPSFPMaxStreamFilterInstances uint16 = 0x00D0
	PortParamPSFPMaxStreamGateInstances   uint16 = 0x00D1
	PortParamPSFPMaxFlowMeterInstances    uint16 = 0x00D2
	PortParamPSFPSupportedListMax         uint16 = 0x00D3
	PortParamTSNTimeDomainNumber          uint16 = 0x00D4

	PortParamStreamFilterInstanceTable   uint16 = 0x00E0
	PortParamStreamGateInstanceTable     uint16 = 0x00E1
	PortParamSupportedPTPInstanceTypes   uint16 = 0x00E2
	PortParamSupportedTransportTypes     uint16 = 0x00E3
	PortParamSupportedDelayMechanisms    uint16 = 0x00E4
	PortParamPTPGrandmasterCapable       uint16 = 0x00E5
	PortParamGPTPGrandmasterCapable      uint16 = 0x00E6
	PortParamSupportedPTPProfiles        uint16 = 0x00E7
	PortParamNumSupportedPTPInstances    uint16 = 0x00E8
	PortParamPTPInstanceList             uint16 = 0x00E9

	// DetNet / IP interface parameters (NOTE 4 — NW-TT to TSCTSF).
	PortParamInterfaceType         uint16 = 0x00F0
	PortParamInterfaceEnableStatus uint16 = 0x00F1
	PortParamPhysAddress           uint16 = 0x00F2
	PortParamIPv4EnableStatus      uint16 = 0x00F3
	PortParamIPv4ForwardingStatus  uint16 = 0x00F4
	PortParamIPv4MTU               uint16 = 0x00F5
	PortParamIPv4AddressInfo       uint16 = 0x00F6
	PortParamIPv4NeighborInfo      uint16 = 0x00F7
	PortParamIPv6EnableStatus      uint16 = 0x00F8
	PortParamIPv6ForwardingStatus  uint16 = 0x00F9
	PortParamIPv6MTU               uint16 = 0x00FA
	PortParamIPv6AddressInfo       uint16 = 0x00FB
	PortParamIPv6NeighborInfo      uint16 = 0x00FC
)

// ---------------------------------------------------------------------------
// User plane node parameter names — TS 24.539 Table 9.5B.1
// ---------------------------------------------------------------------------

const (
	NodeParamAddress                    uint16 = 0x0001
	NodeParamID                         uint16 = 0x0003
	NodeParamNWTTPortNumbers            uint16 = 0x0004
	NodeParamStaticFilteringEntries     uint16 = 0x0012
	NodeParamStaticFilteringPortMap     uint16 = 0x0013
	NodeParamLldpV2PortConfigAdmStatus  uint16 = 0x0020
	NodeParamLldpV2LocChassisIdSubtype  uint16 = 0x0021
	NodeParamLldpV2LocChassisId         uint16 = 0x0022
	NodeParamLldpV2MessageTxInterval    uint16 = 0x0023
	NodeParamLldpV2MessageTxHoldMult    uint16 = 0x0024
	NodeParamDSTTPortNeighborDiscovery  uint16 = 0x0050
	NodeParamDiscoveredNeighborInfo     uint16 = 0x0051
	NodeParamPSFPMaxStreamFilterInst    uint16 = 0x0070
	NodeParamPSFPMaxStreamGateInst      uint16 = 0x0071
	NodeParamPSFPMaxFlowMeterInst       uint16 = 0x0072
	NodeParamPSFPSupportedListMax       uint16 = 0x0073
	NodeParamSupportedPTPInstanceTypes  uint16 = 0x0074
	NodeParamSupportedTransportTypes    uint16 = 0x0075
	NodeParamSupportedDelayMechanisms   uint16 = 0x0076
	NodeParamPTPGrandmasterCapable      uint16 = 0x0077
	NodeParamGPTPGrandmasterCapable     uint16 = 0x0078
	NodeParamSupportedPTPProfiles       uint16 = 0x0079
	NodeParamNumSupportedPTPInstances   uint16 = 0x007A
	NodeParamDSTTPortTimeSyncInfoList   uint16 = 0x007B
	NodeParamPTPInstanceSpecification   uint16 = 0x007C
	NodeParamSynchronizationState       uint16 = 0x0090
	NodeParamClockQuality               uint16 = 0x0091
	NodeParamParentTimeSource           uint16 = 0x0092
)

// ---------------------------------------------------------------------------
// Port management service causes — TS 24.539 Table 9.4.1 / 9.5.1
// ---------------------------------------------------------------------------

const (
	CauseParamNotSupported     byte = 0x01
	CauseInvalidParamValue     byte = 0x02
	CauseParamValueUnavailable byte = 0x03 // read only (Table 9.4.1)
	CauseProtocolError         byte = 0x6F // "protocol error, unspecified"
)

// ---------------------------------------------------------------------------
// Data model
// ---------------------------------------------------------------------------

// Operation is one entry of a port / user plane node management list
// (TS 24.539 §9.2 figures 9.2.3-9.2.5, §9.5B figures 9.5B.3-9.5B.5).
type Operation struct {
	Code  byte
	Param uint16 // absent for OpGetCapabilities
	Value []byte // present only when opCarriesValue(Code)
}

// ParamStatus is one "port parameter status" (figure 9.4.3) or
// "port parameter update" (figure 9.5.3) entry.
type ParamStatus struct {
	Param uint16
	Value []byte
}

// ParamError is one "port parameter error" entry (figure 9.4.5 / 9.5.5).
type ParamError struct {
	Param uint16
	Cause byte
}

// Message is a decoded PMS or UMS message. Which fields are meaningful
// depends on Type (see TS 24.539 §8):
//
//	COMMAND           → Ops
//	COMPLETE          → Capabilities and/or Status/StatusErrors and/or
//	                    Updates/UpdateErrors (IEIs 0x70/0x71/0x72)
//	NOTIFY            → Status/StatusErrors (mandatory LV-E)
//	NOTIFY ACK        → (none)
//	NOTIFY COMPLETE   → (none, PMS only)
//	CAPABILITY        → Capabilities (mandatory LV-E, PMS only)
type Message struct {
	Type         byte
	Ops          []Operation
	Capabilities []uint16
	Status       []ParamStatus
	StatusErrors []ParamError
	Updates      []ParamStatus
	UpdateErrors []ParamError
}

// IEIs inside MANAGE ... COMPLETE messages — TS 24.539 tables 8.2.1.1, 8.8.1.1.
const (
	ieiCapability   byte = 0x70
	ieiStatus       byte = 0x71
	ieiUpdateResult byte = 0x72
)

var (
	errTooShort = errors.New("ttmgmt: message too short")
)

// ---------------------------------------------------------------------------
// Encoding
// ---------------------------------------------------------------------------

// Encode serialises the message. The same wire shapes serve PMS and UMS;
// the caller picks the Msg* constant from the matching message-type space.
func (m *Message) Encode() ([]byte, error) {
	out := []byte{m.Type}
	switch m.Type {
	case MsgManagePortCommand: // == MsgManageUPNodeCommand
		body := encodeOps(m.Ops)
		if len(body) == 0 {
			return nil, errors.New("ttmgmt: COMMAND requires at least one operation")
		}
		out = appendLVE(out, body)
	case MsgManagePortComplete: // == MsgManageUPNodeComplete
		if len(m.Capabilities) > 0 {
			out = append(out, ieiCapability)
			out = appendLVE(out, encodeCaps(m.Capabilities))
		}
		if len(m.Status) > 0 || len(m.StatusErrors) > 0 {
			out = append(out, ieiStatus)
			out = appendLVE(out, encodeStatus(m.Status, m.StatusErrors, 2))
		}
		if len(m.Updates) > 0 || len(m.UpdateErrors) > 0 {
			out = append(out, ieiUpdateResult)
			// Port update result value length field is ONE octet
			// (TS 24.539 table 9.5.1: "Length of port parameter
			// value (octet e+2)"), unlike port status where it is
			// two octets (table 9.4.1, octets e+2 to e+3).
			out = appendLVE(out, encodeStatus(m.Updates, m.UpdateErrors, 1))
		}
	case MsgPortManagementNotify: // == MsgUPNodeManagementNotify
		out = appendLVE(out, encodeStatus(m.Status, m.StatusErrors, 2))
	case MsgPortManagementNotifyAck, MsgPortManagementNotifyComplete:
		// header only (NOTIFY ACK doubles as UMS 0x04)
	case MsgPortManagementCapability:
		out = appendLVE(out, encodeCaps(m.Capabilities))
	default:
		return nil, fmt.Errorf("ttmgmt: unknown message type 0x%02x", m.Type)
	}
	return out, nil
}

func appendLVE(dst, body []byte) []byte {
	dst = binary.BigEndian.AppendUint16(dst, uint16(len(body)))
	return append(dst, body...)
}

func encodeOps(ops []Operation) []byte {
	var b []byte
	for _, op := range ops {
		b = append(b, op.Code)
		if op.Code == OpGetCapabilities {
			continue
		}
		b = binary.BigEndian.AppendUint16(b, op.Param)
		if opCarriesValue(op.Code) {
			b = binary.BigEndian.AppendUint16(b, uint16(len(op.Value)))
			b = append(b, op.Value...)
		}
	}
	return b
}

func encodeCaps(names []uint16) []byte {
	b := make([]byte, 0, 2*len(names))
	for _, n := range names {
		b = binary.BigEndian.AppendUint16(b, n)
	}
	return b
}

// encodeStatus builds port status (figure 9.4.2/9.4.4) or port update result
// (figure 9.5.2/9.5.4) contents. lenOctets selects the per-entry value length
// field width: 2 for status, 1 for update result.
func encodeStatus(ok []ParamStatus, errs []ParamError, lenOctets int) []byte {
	b := []byte{byte(len(ok))}
	for _, s := range ok {
		b = binary.BigEndian.AppendUint16(b, s.Param)
		if lenOctets == 2 {
			b = binary.BigEndian.AppendUint16(b, uint16(len(s.Value)))
		} else {
			b = append(b, byte(len(s.Value)))
		}
		b = append(b, s.Value...)
	}
	b = append(b, byte(len(errs)))
	for _, e := range errs {
		b = binary.BigEndian.AppendUint16(b, e.Param)
		b = append(b, e.Cause)
	}
	return b
}

// ---------------------------------------------------------------------------
// Decoding
// ---------------------------------------------------------------------------

// DecodePMS decodes a port management service message (TS 24.539 §9.1 space).
func DecodePMS(b []byte) (*Message, error) { return decode(b, true) }

// DecodeUMS decodes a user plane node management service message
// (TS 24.539 §9.5A space).
func DecodeUMS(b []byte) (*Message, error) { return decode(b, false) }

func decode(b []byte, pms bool) (*Message, error) {
	if len(b) < 1 {
		return nil, errTooShort
	}
	m := &Message{Type: b[0]}
	rest := b[1:]
	maxType := MsgPortManagementCapability
	if !pms {
		maxType = MsgUPNodeManagementNotifyAck
	}
	if m.Type == 0 || m.Type > maxType {
		return nil, fmt.Errorf("ttmgmt: reserved message type 0x%02x", m.Type)
	}
	var err error
	switch m.Type {
	case MsgManagePortCommand:
		var body []byte
		body, rest, err = readLVE(rest)
		if err != nil {
			return nil, err
		}
		if m.Ops, err = decodeOps(body); err != nil {
			return nil, err
		}
	case MsgManagePortComplete:
		for len(rest) > 0 {
			iei := rest[0]
			var body []byte
			body, rest, err = readLVE(rest[1:])
			if err != nil {
				return nil, err
			}
			switch iei {
			case ieiCapability:
				if m.Capabilities, err = decodeCaps(body); err != nil {
					return nil, err
				}
			case ieiStatus:
				if m.Status, m.StatusErrors, err = decodeStatus(body, 2); err != nil {
					return nil, err
				}
			case ieiUpdateResult:
				if m.Updates, m.UpdateErrors, err = decodeStatus(body, 1); err != nil {
					return nil, err
				}
			default:
				// Unknown optional IE: ignore (TS 24.539 §7.5.1).
			}
		}
	case MsgPortManagementNotify:
		var body []byte
		body, _, err = readLVE(rest)
		if err != nil {
			return nil, err
		}
		if m.Status, m.StatusErrors, err = decodeStatus(body, 2); err != nil {
			return nil, err
		}
	case MsgPortManagementNotifyAck:
		// header only in both spaces
	case MsgPortManagementNotifyComplete:
		if !pms {
			return nil, fmt.Errorf("ttmgmt: reserved UMS message type 0x%02x", m.Type)
		}
	case MsgPortManagementCapability:
		if !pms {
			return nil, fmt.Errorf("ttmgmt: reserved UMS message type 0x%02x", m.Type)
		}
		var body []byte
		body, _, err = readLVE(rest)
		if err != nil {
			return nil, err
		}
		if m.Capabilities, err = decodeCaps(body); err != nil {
			return nil, err
		}
	}
	return m, nil
}

func readLVE(b []byte) (body, rest []byte, err error) {
	if len(b) < 2 {
		return nil, nil, errTooShort
	}
	n := int(binary.BigEndian.Uint16(b))
	if len(b) < 2+n {
		return nil, nil, errTooShort
	}
	return b[2 : 2+n], b[2+n:], nil
}

func decodeOps(b []byte) ([]Operation, error) {
	var ops []Operation
	for len(b) > 0 {
		op := Operation{Code: b[0]}
		b = b[1:]
		if op.Code == 0 || op.Code > OpDeleteParameterEntry {
			return nil, fmt.Errorf("ttmgmt: spare operation code 0x%02x", op.Code)
		}
		if op.Code != OpGetCapabilities {
			if len(b) < 2 {
				return nil, errTooShort
			}
			op.Param = binary.BigEndian.Uint16(b)
			b = b[2:]
			if opCarriesValue(op.Code) {
				if len(b) < 2 {
					return nil, errTooShort
				}
				n := int(binary.BigEndian.Uint16(b))
				b = b[2:]
				if len(b) < n {
					return nil, errTooShort
				}
				op.Value = append([]byte(nil), b[:n]...)
				b = b[n:]
			}
		}
		ops = append(ops, op)
	}
	return ops, nil
}

func decodeCaps(b []byte) ([]uint16, error) {
	if len(b)%2 != 0 {
		return nil, errors.New("ttmgmt: capability list length not a multiple of 2")
	}
	caps := make([]uint16, 0, len(b)/2)
	for i := 0; i < len(b); i += 2 {
		caps = append(caps, binary.BigEndian.Uint16(b[i:]))
	}
	return caps, nil
}

func decodeStatus(b []byte, lenOctets int) ([]ParamStatus, []ParamError, error) {
	if len(b) < 1 {
		return nil, nil, errTooShort
	}
	nOK := int(b[0])
	b = b[1:]
	var ok []ParamStatus
	for i := 0; i < nOK; i++ {
		if len(b) < 2+lenOctets {
			return nil, nil, errTooShort
		}
		s := ParamStatus{Param: binary.BigEndian.Uint16(b)}
		b = b[2:]
		var n int
		if lenOctets == 2 {
			n = int(binary.BigEndian.Uint16(b))
			b = b[2:]
		} else {
			n = int(b[0])
			b = b[1:]
		}
		if len(b) < n {
			return nil, nil, errTooShort
		}
		s.Value = append([]byte(nil), b[:n]...)
		b = b[n:]
		ok = append(ok, s)
	}
	if len(b) < 1 {
		return nil, nil, errTooShort
	}
	nErr := int(b[0])
	b = b[1:]
	var errs []ParamError
	for i := 0; i < nErr; i++ {
		if len(b) < 3 {
			return nil, nil, errTooShort
		}
		errs = append(errs, ParamError{
			Param: binary.BigEndian.Uint16(b),
			Cause: b[2],
		})
		b = b[3:]
	}
	// Extended port update contents (figure 9.5.6) may follow for update
	// results; tolerated but not modelled (TS 24.539 §7.5.1 — ignore).
	return ok, errs, nil
}

// ---------------------------------------------------------------------------
// Parameter value helpers
// ---------------------------------------------------------------------------

// GateControlEntry is one AdminControlList entry, encoded as the
// ieee8021STAdminControlList object of IEEE Std 802.1Q clause 17.7.22
// referenced from TS 24.539 table 9.2.1: operation name (1 octet, always 0 =
// SetGateStates per 24.539), gate states bitmap (1 octet, bit N = traffic
// class N gate open), time interval (4 octets, nanoseconds).
type GateControlEntry struct {
	GateStates     byte
	TimeIntervalNS uint32
}

// EncodeAdminControlList serialises entries per IEEE 802.1Q 17.7.22.
func EncodeAdminControlList(entries []GateControlEntry) []byte {
	b := make([]byte, 0, 6*len(entries))
	for _, e := range entries {
		b = append(b, 0 /* SetGateStates */, e.GateStates)
		b = binary.BigEndian.AppendUint32(b, e.TimeIntervalNS)
	}
	return b
}

// DecodeAdminControlList parses an AdminControlList value.
func DecodeAdminControlList(b []byte) ([]GateControlEntry, error) {
	if len(b)%6 != 0 {
		return nil, errors.New("ttmgmt: AdminControlList length not a multiple of 6")
	}
	out := make([]GateControlEntry, 0, len(b)/6)
	for i := 0; i < len(b); i += 6 {
		// Any operation name other than 0 is interpreted as 0
		// (SetGateStates) per TS 24.539 table 9.2.1.
		out = append(out, GateControlEntry{
			GateStates:     b[i+1],
			TimeIntervalNS: binary.BigEndian.Uint32(b[i+2:]),
		})
	}
	return out, nil
}

// EncodeAdminBaseTime builds the 10-octet administrative base time of
// IEEE Std 802.1Q (PTPtime: 48-bit seconds + 32-bit nanoseconds), as
// required by TS 24.539 table 9.2.1 (length 10).
func EncodeAdminBaseTime(seconds uint64, nanoseconds uint32) []byte {
	b := make([]byte, 10)
	b[0] = byte(seconds >> 40)
	b[1] = byte(seconds >> 32)
	binary.BigEndian.PutUint32(b[2:], uint32(seconds))
	binary.BigEndian.PutUint32(b[6:], nanoseconds)
	return b
}

// DecodeAdminBaseTime parses a 10-octet PTPtime value.
func DecodeAdminBaseTime(b []byte) (seconds uint64, nanoseconds uint32, err error) {
	if len(b) != 10 {
		return 0, 0, errors.New("ttmgmt: AdminBaseTime must be 10 octets")
	}
	seconds = uint64(b[0])<<40 | uint64(b[1])<<32 | uint64(binary.BigEndian.Uint32(b[2:]))
	return seconds, binary.BigEndian.Uint32(b[6:]), nil
}

// EncodePropagationDelay encodes txPropagationDelay /
// txPropagationDelayDeltaThreshold: nanoseconds scaled by 2^16 in an 8-octet
// field (TS 24.539 table 9.2.1; same fixed-point convention as the IEEE
// 1588 correctionField). Saturates per the "too big to be represented" rule.
func EncodePropagationDelay(ns float64) []byte {
	b := make([]byte, 8)
	scaled := ns * 65536.0
	const max = float64(^uint64(0) >> 1) // all ones except MSB
	if scaled >= max {
		binary.BigEndian.PutUint64(b, ^uint64(0)>>1)
		return b
	}
	if scaled < 0 {
		scaled = 0
	}
	binary.BigEndian.PutUint64(b, uint64(scaled))
	return b
}

// DecodePropagationDelay returns the delay in nanoseconds.
func DecodePropagationDelay(b []byte) (float64, error) {
	if len(b) != 8 {
		return 0, errors.New("ttmgmt: propagation delay must be 8 octets")
	}
	return float64(binary.BigEndian.Uint64(b)) / 65536.0, nil
}

// EncodeBool encodes a Boolean parameter value (GateEnabled, PTP
// grandmaster capable, ...) per TS 24.539 table 9.2.1.
func EncodeBool(v bool) []byte {
	if v {
		return []byte{0x01}
	}
	return []byte{0x00}
}

// EncodeUint32 / EncodeUint64 are big-endian binary parameter encodings used
// by AdminControlListLength (4), Tick granularity (4), AdminCycleTime (8),
// User plane node ID (8), ...
func EncodeUint32(v uint32) []byte { return binary.BigEndian.AppendUint32(nil, v) }
func EncodeUint64(v uint64) []byte { return binary.BigEndian.AppendUint64(nil, v) }

// EncodeNWTTPortNumbers encodes the "NW-TT port numbers" user plane node
// parameter (TS 24.539 §9.14): one port number per 2 octets.
func EncodeNWTTPortNumbers(ports []uint16) []byte {
	b := make([]byte, 0, 2*len(ports))
	for _, p := range ports {
		b = binary.BigEndian.AppendUint16(b, p)
	}
	return b
}

// DecodeNWTTPortNumbers parses the NW-TT port numbers parameter value.
func DecodeNWTTPortNumbers(b []byte) ([]uint16, error) {
	if len(b)%2 != 0 {
		return nil, errors.New("ttmgmt: NW-TT port numbers length not a multiple of 2")
	}
	out := make([]uint16, 0, len(b)/2)
	for i := 0; i < len(b); i += 2 {
		out = append(out, binary.BigEndian.Uint16(b[i:]))
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// PTP instance list — TS 24.539 §9.15 (PMIC parameter 0x00E9 / UMS
// parameter 0x007C), carrying the TS 23.501 Annex K.1 PTP instance
// data sets toward DS-TT / NW-TT.
// ---------------------------------------------------------------------------

// PTP instance parameter names — TS 24.539 table 9.15.1 (subset in
// active use; the full table spans defaultDS/portDS/timePropertiesDS).
const (
	PTPParamProfile              uint16 = 0x0001
	PTPParamTransportType        uint16 = 0x0002
	PTPParamGrandmasterEnabled   uint16 = 0x0003
	PTPParamGMOnBehalfOfDSTT     uint16 = 0x0004
	PTPParamGMCandidateEnabled   uint16 = 0x0005
	PTPParamClockIdentity        uint16 = 0x0006 // defaultDS.clockIdentity
	PTPParamClockClass           uint16 = 0x0007
	PTPParamClockAccuracy        uint16 = 0x0008
	PTPParamPriority1            uint16 = 0x000A // defaultDS.priority1
	PTPParamPriority2            uint16 = 0x000B
	PTPParamDomainNumber         uint16 = 0x000C // defaultDS.domainNumber
	PTPParamInstanceEnable       uint16 = 0x000E
	PTPParamInstanceType         uint16 = 0x0010 // defaultDS.instanceType
	PTPParamPortState            uint16 = 0x0012 // portDS.portState
	PTPParamLogAnnounceInterval  uint16 = 0x0014
	PTPParamLogSyncInterval      uint16 = 0x0016
	PTPParamDelayMechanism       uint16 = 0x0017
	PTPParamPortEnable           uint16 = 0x001C // portDS.portEnable
	PTPParamTimeSource           uint16 = 0x0020 // defaultDS.timeSource
)

// PTP profile encodings — TS 24.539 table 9.15.1: SMPTE ST 2059-2 =
// 0x00, IEEE 802.1AS = 0x01, IEEE 1588 default profile = 0x02.
const (
	PTPProfileSMPTE    byte = 0x00
	PTPProfile8021AS   byte = 0x01
	PTPProfileDefault  byte = 0x02
)

// PTP transport encodings (same space as "Supported transport types"):
// IPv4 = 0x00, IPv6 = 0x01, Ethernet = 0x02.
const (
	PTPTransportIPv4     byte = 0x00
	PTPTransportIPv6     byte = 0x01
	PTPTransportEthernet byte = 0x02
)

// PTPInstanceParam is one PTP instance parameter (figure 9.15.4 —
// 2-octet name, 1-octet length, value).
type PTPInstanceParam struct {
	Name  uint16
	Value []byte
}

// PTPInstance is one entry of the PTP instance list (figure 9.15.2).
type PTPInstance struct {
	ID     uint16
	Params []PTPInstanceParam
}

// EncodePTPInstanceList serialises the PTP instance list value part
// (TS 24.539 §9.15, octets 4..o).
func EncodePTPInstanceList(instances []PTPInstance) []byte {
	var out []byte
	for _, in := range instances {
		var body []byte
		body = binary.BigEndian.AppendUint16(body, in.ID)
		for _, p := range in.Params {
			body = binary.BigEndian.AppendUint16(body, p.Name)
			body = append(body, byte(len(p.Value)))
			body = append(body, p.Value...)
		}
		out = binary.BigEndian.AppendUint16(out, uint16(len(body)))
		out = append(out, body...)
	}
	return out
}

// DecodePTPInstanceList parses a PTP instance list value part.
func DecodePTPInstanceList(b []byte) ([]PTPInstance, error) {
	var out []PTPInstance
	for len(b) > 0 {
		if len(b) < 2 {
			return nil, errTooShort
		}
		n := int(binary.BigEndian.Uint16(b))
		b = b[2:]
		if len(b) < n || n < 2 {
			return nil, errTooShort
		}
		body := b[:n]
		b = b[n:]
		in := PTPInstance{ID: binary.BigEndian.Uint16(body)}
		body = body[2:]
		for len(body) > 0 {
			if len(body) < 3 {
				return nil, errTooShort
			}
			p := PTPInstanceParam{Name: binary.BigEndian.Uint16(body)}
			vlen := int(body[2])
			body = body[3:]
			if len(body) < vlen {
				return nil, errTooShort
			}
			p.Value = append([]byte(nil), body[:vlen]...)
			body = body[vlen:]
			in.Params = append(in.Params, p)
		}
		out = append(out, in)
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Convenience constructors
// ---------------------------------------------------------------------------

// NewGetCapabilities builds a MANAGE PORT/USER PLANE NODE COMMAND holding a
// single "get capabilities" operation (TS 24.539 §5.2.1 / §6.2.1 / §6.3.1).
func NewGetCapabilities() *Message {
	return &Message{
		Type: MsgManagePortCommand,
		Ops:  []Operation{{Code: OpGetCapabilities}},
	}
}

// NewReadParams builds a COMMAND that reads the given parameters.
func NewReadParams(params ...uint16) *Message {
	m := &Message{Type: MsgManagePortCommand}
	for _, p := range params {
		m.Ops = append(m.Ops, Operation{Code: OpReadParameter, Param: p})
	}
	return m
}

// NewSetParam builds a COMMAND that sets one parameter value.
func NewSetParam(param uint16, value []byte) *Message {
	return &Message{
		Type: MsgManagePortCommand,
		Ops:  []Operation{{Code: OpSetParameter, Param: param, Value: value}},
	}
}

// NewSubscribe builds a COMMAND that subscribes for change notifications of
// the given parameters (TS 24.539 §5.2.2 / §6.2.2 drive the NOTIFY flow).
func NewSubscribe(params ...uint16) *Message {
	m := &Message{Type: MsgManagePortCommand}
	for _, p := range params {
		m.Ops = append(m.Ops, Operation{Code: OpSubscribeNotify, Param: p})
	}
	return m
}

// NewNotify builds a PORT MANAGEMENT NOTIFY / USER PLANE NODE MANAGEMENT
// NOTIFY carrying changed parameter values.
func NewNotify(status ...ParamStatus) *Message {
	return &Message{Type: MsgPortManagementNotify, Status: status}
}
