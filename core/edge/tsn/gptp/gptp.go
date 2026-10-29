// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

// Package gptp implements the (g)PTP message processing the 5GS
// performs when acting as an IEEE 802.1AS time-aware system —
// TS 23.501 §5.27.1 (Rel-19):
//
//	§5.27.1.2.2  Sync/Follow_Up distribution: the ingress TT (NW-TT
//	  for DL, DS-TT for UL) timestamps the message with the 5G
//	  internal system clock (TSi), adds the upstream link delay to
//	  the correctionField, replaces the cumulative rateRatio, and
//	  carries TSi across the 5GS in a Suffix field; the egress TT
//	  timestamps again (TSe), converts the residence time
//	  (TSe − TSi, 5GS time) into TSN GM time using the cumulative
//	  rateRatio, adds it to the correctionField and strips the
//	  Suffix (TS 23.501 Annex H).
//	§5.27.1.7    5GS as (g)PTP grandmaster: NW-TT (or DS-TT)
//	  generates Sync/Follow_Up with the 5G clock as
//	  originTimestamp and cumulative rateRatio = 1.
//
// Wire format: IEEE 802.1AS-2020 / IEEE 1588-2019 common header +
// Sync / Follow_Up bodies, Ethernet transport (Ethertype 0x88F7).
// The cumulative rateRatio travels in the Follow_Up information TLV's
// cumulativeScaledRateOffset = (rateRatio − 1) × 2^41 (802.1AS §11.4.4.3).
package gptp

import (
	"encoding/binary"
	"errors"
	"fmt"
)

// EthertypePTP is the PTP-over-Ethernet Ethertype (IEEE 1588 Annex E).
const EthertypePTP = 0x88F7

// Message types — IEEE 1588-2019 Table 36.
const (
	MsgSync               byte = 0x0
	MsgDelayReq           byte = 0x1
	MsgPdelayReq          byte = 0x2
	MsgPdelayResp         byte = 0x3
	MsgFollowUp           byte = 0x8
	MsgDelayResp          byte = 0x9
	MsgPdelayRespFollowUp byte = 0xA
	MsgAnnounce           byte = 0xB
	MsgSignaling          byte = 0xC
)

// Header flags (octets 6-7), IEEE 1588-2019 Table 37.
const (
	FlagTwoStep = 0x0200 // twoStepFlag, bit 1 of octet 6 (big-endian uint16)
)

const (
	headerLen   = 34
	tsLen       = 10 // PTP Timestamp: 48-bit seconds + 32-bit nanoseconds
	syncLen     = headerLen + tsLen
	followUpTLV = 32 // Follow_Up information TLV incl. type/length
)

// Timestamp is the 10-octet PTP timestamp.
type Timestamp struct {
	Seconds     uint64 // 48-bit
	Nanoseconds uint32
}

// TotalNS collapses the timestamp to nanoseconds since the PTP epoch.
func (t Timestamp) TotalNS() uint64 {
	return t.Seconds*1_000_000_000 + uint64(t.Nanoseconds)
}

// TimestampFromNS builds a Timestamp from nanoseconds since the epoch.
func TimestampFromNS(ns uint64) Timestamp {
	return Timestamp{Seconds: ns / 1_000_000_000, Nanoseconds: uint32(ns % 1_000_000_000)}
}

// PortIdentity — IEEE 1588 §5.3.5.
type PortIdentity struct {
	ClockIdentity [8]byte
	PortNumber    uint16
}

// Message is a decoded (g)PTP PDU (Sync, Follow_Up or Announce header;
// other types keep Body opaque).
type Message struct {
	// ── common header (IEEE 1588-2019 §13.3) ──
	MessageType     byte // low nibble of octet 0
	MajorSdoID      byte // high nibble of octet 0 (1 = gPTP per 802.1AS)
	VersionPTP      byte
	MinorVersionPTP byte
	DomainNumber    byte
	MinorSdoID      byte
	Flags           uint16
	// CorrectionScaled is the raw correctionField: nanoseconds × 2^16
	// (IEEE 1588 §13.3.2.9). Signed.
	CorrectionScaled int64
	SourcePort       PortIdentity
	SequenceID       uint16
	ControlField     byte
	LogMsgInterval   int8

	// ── Sync / Follow_Up body ──
	OriginTimestamp Timestamp // originTimestamp / preciseOriginTimestamp

	// ── Follow_Up information TLV (802.1AS §11.4.4) ──
	HasFollowUpTLV bool
	// CumulativeScaledRateOffset = (cumulative rateRatio − 1) × 2^41.
	CumulativeScaledRateOffset int32
	GmTimeBaseIndicator        uint16
	LastGmPhaseChange          [12]byte
	ScaledLastGmFreqChange     int32

	// ── 5GS Suffix (TS 23.501 §5.27.1.2.2 / Annex H) ──
	// The ingress TT's timestamp TSi (5G internal system clock, ns),
	// carried across the 5GS in an organization-specific suffix TLV
	// and stripped at the egress TT. nil = no suffix present.
	SuffixTSiNS *uint64

	// ── Announce body (IEEE 1588-2019 §13.5) ──
	Announce *AnnounceBody

	// Body keeps the undecoded remainder for message types this
	// package does not model (Signaling, Pdelay, ...).
	Body []byte
}

// ClockQuality — IEEE 1588-2019 §5.3.7.
type ClockQuality struct {
	ClockClass              byte
	ClockAccuracy           byte
	OffsetScaledLogVariance uint16
}

// AnnounceBody — IEEE 1588-2019 §13.5 (the fields the BMCA consumes;
// TS 23.501 §5.27.1.6: "the NW-TT ... processes Announce messages ...
// and executes the BMCA").
type AnnounceBody struct {
	OriginTimestamp       Timestamp
	CurrentUtcOffset      int16
	GrandmasterPriority1  byte
	GrandmasterQuality    ClockQuality
	GrandmasterPriority2  byte
	GrandmasterIdentity   [8]byte
	StepsRemoved          uint16
	TimeSource            byte
}

// suffix TLV: ORGANIZATION_EXTENSION (type 3) with a private
// organizationId — the container format of TS 23.501 Annex H is left
// implementation-specific within the 5GS; both TT ends live in this
// codebase so the shape below is the 5GS-internal convention.
const (
	tlvOrgExtension  = 0x0003
	suffixOrgSubType = 0x5301 // "5GS TSi"
)

var suffixOrgID = [3]byte{0x00, 0x00, 0x5A} // private OUI space

// CumulativeRateRatio returns the multiplicative cumulative rateRatio.
func (m *Message) CumulativeRateRatio() float64 {
	return 1.0 + float64(m.CumulativeScaledRateOffset)/(1<<41)
}

// SetCumulativeRateRatio stores a multiplicative rateRatio.
func (m *Message) SetCumulativeRateRatio(r float64) {
	m.CumulativeScaledRateOffset = int32((r - 1.0) * (1 << 41))
}

// CorrectionNS returns the correctionField in nanoseconds.
func (m *Message) CorrectionNS() float64 {
	return float64(m.CorrectionScaled) / 65536.0
}

// AddCorrectionNS adds nanoseconds to the correctionField.
func (m *Message) AddCorrectionNS(ns float64) {
	m.CorrectionScaled += int64(ns * 65536.0)
}

// TwoStep reports the twoStepFlag.
func (m *Message) TwoStep() bool { return m.Flags&FlagTwoStep != 0 }

// ---------------------------------------------------------------------------
// Decode
// ---------------------------------------------------------------------------

// Decode parses a (g)PTP PDU (the Ethernet payload after Ethertype
// 0x88F7).
func Decode(b []byte) (*Message, error) {
	if len(b) < headerLen {
		return nil, errors.New("gptp: message shorter than common header")
	}
	m := &Message{
		MessageType:     b[0] & 0x0F,
		MajorSdoID:      b[0] >> 4,
		VersionPTP:      b[1] & 0x0F,
		MinorVersionPTP: b[1] >> 4,
		DomainNumber:    b[4],
		MinorSdoID:      b[5],
		Flags:           binary.BigEndian.Uint16(b[6:]),
		CorrectionScaled: int64(binary.BigEndian.Uint64(b[8:])),
		SequenceID:      binary.BigEndian.Uint16(b[30:]),
		ControlField:    b[32],
		LogMsgInterval:  int8(b[33]),
	}
	copy(m.SourcePort.ClockIdentity[:], b[20:28])
	m.SourcePort.PortNumber = binary.BigEndian.Uint16(b[28:])

	msgLen := int(binary.BigEndian.Uint16(b[2:]))
	if msgLen < headerLen || msgLen > len(b) {
		msgLen = len(b)
	}
	rest := b[headerLen:msgLen]

	switch m.MessageType {
	case MsgAnnounce:
		// IEEE 1588-2019 §13.5: originTimestamp(10) currentUtcOffset(2)
		// reserved(1) gmPriority1(1) gmClockQuality(4) gmPriority2(1)
		// gmIdentity(8) stepsRemoved(2) timeSource(1) = 30 octets.
		if len(rest) < 30 {
			return nil, errors.New("gptp: announce body truncated")
		}
		a := &AnnounceBody{
			OriginTimestamp:      decodeTimestamp(rest),
			CurrentUtcOffset:     int16(binary.BigEndian.Uint16(rest[10:])),
			GrandmasterPriority1: rest[13],
			GrandmasterQuality: ClockQuality{
				ClockClass:              rest[14],
				ClockAccuracy:           rest[15],
				OffsetScaledLogVariance: binary.BigEndian.Uint16(rest[16:]),
			},
			GrandmasterPriority2: rest[18],
			StepsRemoved:         binary.BigEndian.Uint16(rest[27:]),
			TimeSource:           rest[29],
		}
		copy(a.GrandmasterIdentity[:], rest[19:27])
		m.Announce = a
	case MsgSync, MsgFollowUp:
		if len(rest) < tsLen {
			return nil, errors.New("gptp: sync/follow_up body truncated")
		}
		m.OriginTimestamp = decodeTimestamp(rest)
		rest = rest[tsLen:]
		// Optional TLVs: Follow_Up information TLV + 5GS suffix.
		for len(rest) >= 4 {
			tlvType := binary.BigEndian.Uint16(rest)
			tlvLen := int(binary.BigEndian.Uint16(rest[2:]))
			if len(rest) < 4+tlvLen {
				break
			}
			val := rest[4 : 4+tlvLen]
			if tlvType == tlvOrgExtension && tlvLen >= 6 {
				sub := uint32(val[3])<<16 | uint32(val[4])<<8 | uint32(val[5])
				// 802.1AS Follow_Up information TLV
				// (orgId 00-80-C2, orgSubType 1, 28 octets).
				if val[0] == 0x00 && val[1] == 0x80 && val[2] == 0xC2 &&
					sub == 1 && tlvLen >= 28 {
					m.HasFollowUpTLV = true
					m.CumulativeScaledRateOffset = int32(binary.BigEndian.Uint32(val[6:]))
					m.GmTimeBaseIndicator = binary.BigEndian.Uint16(val[10:])
					copy(m.LastGmPhaseChange[:], val[12:24])
					m.ScaledLastGmFreqChange = int32(binary.BigEndian.Uint32(val[24:28]))
				}
				// 5GS suffix: orgId 00-00-5A, subtype 0x5301, 8-octet TSi
				// (TS 23.501 Annex H — 5GS-internal container).
				if val[0] == suffixOrgID[0] && val[1] == suffixOrgID[1] && val[2] == suffixOrgID[2] &&
					sub == suffixOrgSubType && tlvLen >= 14 {
					tsi := binary.BigEndian.Uint64(val[6:14])
					m.SuffixTSiNS = &tsi
				}
			}
			rest = rest[4+tlvLen:]
		}
	default:
		m.Body = append([]byte(nil), rest...)
	}
	return m, nil
}

func decodeTimestamp(b []byte) Timestamp {
	return Timestamp{
		Seconds:     uint64(b[0])<<40 | uint64(b[1])<<32 | uint64(binary.BigEndian.Uint32(b[2:])),
		Nanoseconds: binary.BigEndian.Uint32(b[6:]),
	}
}

// ---------------------------------------------------------------------------
// Encode
// ---------------------------------------------------------------------------

// Encode serialises the message (header + Sync/Follow_Up body + TLVs;
// opaque Body for other types).
func (m *Message) Encode() []byte {
	out := make([]byte, headerLen)
	out[0] = m.MajorSdoID<<4 | m.MessageType&0x0F
	out[1] = m.MinorVersionPTP<<4 | m.VersionPTP&0x0F
	out[4] = m.DomainNumber
	out[5] = m.MinorSdoID
	binary.BigEndian.PutUint16(out[6:], m.Flags)
	binary.BigEndian.PutUint64(out[8:], uint64(m.CorrectionScaled))
	copy(out[20:28], m.SourcePort.ClockIdentity[:])
	binary.BigEndian.PutUint16(out[28:], m.SourcePort.PortNumber)
	binary.BigEndian.PutUint16(out[30:], m.SequenceID)
	out[32] = m.ControlField
	out[33] = byte(m.LogMsgInterval)

	switch m.MessageType {
	case MsgAnnounce:
		a := m.Announce
		if a == nil {
			a = &AnnounceBody{}
		}
		body := make([]byte, 30)
		copy(body, encodeTimestamp(a.OriginTimestamp))
		binary.BigEndian.PutUint16(body[10:], uint16(a.CurrentUtcOffset))
		body[13] = a.GrandmasterPriority1
		body[14] = a.GrandmasterQuality.ClockClass
		body[15] = a.GrandmasterQuality.ClockAccuracy
		binary.BigEndian.PutUint16(body[16:], a.GrandmasterQuality.OffsetScaledLogVariance)
		body[18] = a.GrandmasterPriority2
		copy(body[19:27], a.GrandmasterIdentity[:])
		binary.BigEndian.PutUint16(body[27:], a.StepsRemoved)
		body[29] = a.TimeSource
		out = append(out, body...)
	case MsgSync, MsgFollowUp:
		out = append(out, encodeTimestamp(m.OriginTimestamp)...)
		if m.HasFollowUpTLV {
			tlv := make([]byte, 4+28)
			binary.BigEndian.PutUint16(tlv, tlvOrgExtension)
			binary.BigEndian.PutUint16(tlv[2:], 28)
			tlv[4], tlv[5], tlv[6] = 0x00, 0x80, 0xC2 // orgId per 802.1AS
			tlv[9] = 0x01                             // orgSubType = 1
			binary.BigEndian.PutUint32(tlv[10:], uint32(m.CumulativeScaledRateOffset))
			binary.BigEndian.PutUint16(tlv[14:], m.GmTimeBaseIndicator)
			copy(tlv[16:28], m.LastGmPhaseChange[:])
			binary.BigEndian.PutUint32(tlv[28:], uint32(m.ScaledLastGmFreqChange))
			out = append(out, tlv...)
		}
		if m.SuffixTSiNS != nil {
			tlv := make([]byte, 4+14)
			binary.BigEndian.PutUint16(tlv, tlvOrgExtension)
			binary.BigEndian.PutUint16(tlv[2:], 14)
			tlv[4], tlv[5], tlv[6] = suffixOrgID[0], suffixOrgID[1], suffixOrgID[2]
			tlv[7] = byte(suffixOrgSubType >> 16 & 0xFF)
			tlv[8] = byte(suffixOrgSubType >> 8 & 0xFF)
			tlv[9] = byte(suffixOrgSubType & 0xFF)
			binary.BigEndian.PutUint64(tlv[10:], *m.SuffixTSiNS)
			out = append(out, tlv...)
		}
	default:
		out = append(out, m.Body...)
	}
	binary.BigEndian.PutUint16(out[2:], uint16(len(out)))
	return out
}

func encodeTimestamp(t Timestamp) []byte {
	b := make([]byte, tsLen)
	b[0] = byte(t.Seconds >> 40)
	b[1] = byte(t.Seconds >> 32)
	binary.BigEndian.PutUint32(b[2:], uint32(t.Seconds))
	binary.BigEndian.PutUint32(b[6:], t.Nanoseconds)
	return b
}

// ---------------------------------------------------------------------------
// 5GS TT transforms — TS 23.501 §5.27.1.2.2
// ---------------------------------------------------------------------------

// IngressAtTT performs the 5GS ingress translator processing on the
// message that carries the timing information (one-step Sync, or the
// Follow_Up of a two-step pair) — TS 23.501 §5.27.1.2.2.1 steps 2-6:
//
//   - add the link delay from the upstream node, expressed in TSN GM
//     time, to the correctionField;
//   - replace the cumulative rateRatio with the updated value
//     (previous rateRatio × neighborRateRatio, per IEEE 802.1AS);
//   - record the ingress timestamp TSi (5G internal system clock) in
//     the 5GS Suffix so the egress TT can compute the residence time.
func IngressAtTT(m *Message, tsiNS uint64, linkDelayGMNS float64, neighborRateRatio float64) error {
	if m.MessageType != MsgSync && m.MessageType != MsgFollowUp {
		return fmt.Errorf("gptp: ingress transform on message type %#x", m.MessageType)
	}
	m.AddCorrectionNS(linkDelayGMNS)
	m.HasFollowUpTLV = true
	m.SetCumulativeRateRatio(m.CumulativeRateRatio() * neighborRateRatio)
	tsi := tsiNS
	m.SuffixTSiNS = &tsi
	return nil
}

// EgressAtTT performs the egress translator processing — TS 23.501
// §5.27.1.2.2.1 step 8 / §5.27.1.2.2.2:
//
//   - residence time = TSe − TSi in 5GS time (both taken from the 5G
//     internal system clock);
//   - converted into TSN GM time using the cumulative rateRatio;
//   - added to the correctionField;
//   - the 5GS Suffix is removed before the message leaves the 5GS.
//
// Returns the residence time (5GS ns) for §5.27.1.12 monitoring.
func EgressAtTT(m *Message, tseNS uint64) (uint64, error) {
	if m.SuffixTSiNS == nil {
		return 0, errors.New("gptp: egress without 5GS ingress suffix (TSi)")
	}
	tsi := *m.SuffixTSiNS
	if tseNS < tsi {
		return 0, fmt.Errorf("gptp: TSe %d before TSi %d", tseNS, tsi)
	}
	residence5GS := tseNS - tsi
	m.AddCorrectionNS(float64(residence5GS) * m.CumulativeRateRatio())
	m.SuffixTSiNS = nil
	return residence5GS, nil
}

// GenerateGMSync builds the two-step Sync + Follow_Up pair the NW-TT
// (or DS-TT) emits when the 5GS acts as the (g)PTP grandmaster —
// TS 23.501 §5.27.1.7: preciseOriginTimestamp = 5G internal system
// clock, cumulative rateRatio = 1, correctionField = 0.
func GenerateGMSync(gmIdentity [8]byte, portNumber uint16, domain byte,
	seqID uint16, nowNS uint64) (sync, followUp *Message) {

	src := PortIdentity{ClockIdentity: gmIdentity, PortNumber: portNumber}
	sync = &Message{
		MessageType:  MsgSync,
		MajorSdoID:   1, // gPTP (802.1AS §8.1)
		VersionPTP:   2,
		DomainNumber: domain,
		Flags:        FlagTwoStep,
		SourcePort:   src,
		SequenceID:   seqID,
		ControlField: 0x00,
	}
	followUp = &Message{
		MessageType:     MsgFollowUp,
		MajorSdoID:      1,
		VersionPTP:      2,
		DomainNumber:    domain,
		SourcePort:      src,
		SequenceID:      seqID,
		ControlField:    0x02,
		OriginTimestamp: TimestampFromNS(nowNS),
		HasFollowUpTLV:  true,
		// cumulative rateRatio = 1 → scaled offset 0 (§5.27.1.7).
		CumulativeScaledRateOffset: 0,
	}
	return sync, followUp
}
