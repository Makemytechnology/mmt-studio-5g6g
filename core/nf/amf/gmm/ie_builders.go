// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Extracted from registration.go by refactor: split god-file by
// sub-concern. Imports are re-derived by goimports.
package gmm

import (
	"encoding/binary"
	"time"

	crand "crypto/rand"

	"github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/nssf"
	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/nasgen/pkg/runtime"
)

// buildAssigned5GGUTI constructs a 5G-GUTI (TS 24.501 §9.11.3.4 GUTI
// subtype) from the AMF's first configured GUAMI + a freshly-allocated
// 5G-TMSI. Returns nil when the AMF hasn't been initialised with any
// GUAMI — encoder then omits the IE.
//
// 5G-TMSI is a 32-bit identity assigned by the AMF (TS 23.003 §2.10.1);
// it must be unpredictable to the UE's pre-assignment identity so stalkers
// can't correlate registrations. We allocate once and cache on the UE
// context so retransmissions use the same value.
func buildAssigned5GGUTI(ue *uectx.AmfUeCtx) *runtime.GUTI5G {
	list := ctx.Default.GUAMIList()
	if len(list) == 0 {
		return nil
	}
	g := list[0]
	plmn, err := runtime.DecodePlmnBCD(g.PLMNID)
	if err != nil {
		return nil
	}
	if ue.TMSI5G == 0 {
		ue.TMSI5G = allocate5GTMSI()
	}
	return &runtime.GUTI5G{
		Plmn:        plmn,
		AmfRegionId: g.AMFRegionID,
		AmfSetId:    g.AMFSetID,
		AmfPointer:  g.AMFPointer,
		TMSI5G:      ue.TMSI5G,
	}
}

// allocate5GTMSI returns a fresh unpredictable 32-bit 5G-TMSI. Falls back
// to a cheap PRNG if the OS CSPRNG is unavailable (should never happen on
// Linux). Value 0 is reserved — retry on the rare zero draw.
func allocate5GTMSI() uint32 {
	for {
		var b [4]byte
		if _, err := crand.Read(b[:]); err != nil {
			// OS CSPRNG broken — fall back to time-seeded PRNG rather
			// than hand out a duplicate TMSI.
			return uint32(time.Now().UnixNano()&0xFFFFFFFF) | 1
		}
		v := binary.BigEndian.Uint32(b[:])
		if v != 0 {
			return v
		}
	}
}

// buildTAIList encodes a single-entry 5GS TAI list (TS 24.501 §9.11.3.9)
// for the serving gNB's first supported TA. The list-header byte is
// type=0b00 (non-consecutive TACs in one PLMN) with N-1 elements in the
// low 5 bits — value 0 means 1 element.
func buildTAIList(gnb *gnbctx.GnbCtx) *nas.ServiceAreaList {
	if gnb == nil {
		return nil
	}
	plmn, tac := gnb.PrimaryTA()
	if len(plmn) != 3 || len(tac) != 3 {
		return nil
	}
	v := make([]byte, 0, 1+3+3)
	v = append(v, 0x00) // type=00, N-1=0 (1 element)
	v = append(v, plmn...)
	v = append(v, tac...)
	return &nas.ServiceAreaList{Value: v}
}

// buildNetworkFeatureSupport returns the 5GS Network Feature Support IE
// (TS 24.501 §9.11.3.5) with bits taken from the AMF context (which was
// loaded from the network_config table at startup). Operators change the
// flags via the GUI / POST /api/network-config — no code change needed.
func buildNetworkFeatureSupport() *nas.FiveGSNetworkFeatureSupport {
	return &nas.FiveGSNetworkFeatureSupport{Value: ctx.Default.NFS().Encode()}
}

// 5GS Registration Result bit constants — TS 24.501 §9.11.3.6
// Table 9.11.3.6.1. The NAS codec exposes the IE value as a raw
// byte (FiveGMMCause{Value uint8}), so the bit masks live here
// rather than in the codec's enum space.
const (
	// Access type (bits 1..3):
	regResultAccess3GPP    uint8 = 0x01 // 001
	regResultAccessNon3GPP uint8 = 0x02 // 010
	regResultAccessBoth    uint8 = 0x03 // 011

	regResultSMSAllowed     uint8 = 0x08 // bit 4
	regResultNSSAAPerformed uint8 = 0x10 // bit 5
	regResultEmergency      uint8 = 0x20 // bit 6
	regResultDisasterRoam   uint8 = 0x40 // bit 7
	// bit 8 is spare per Table 9.11.3.6.1.
)

// buildRegistrationResult constructs the 5GS Registration Result IE per
// TS 24.501 §9.11.3.6 Table 9.11.3.6.1.
//
// TODO(spec: TS 24.501 §5.5.1.2.4 "SMS allowed bit") — true driver for
//
//	bit 4 is UDM SDM ("SMS subscribed") AND successful SMSF selection;
//	we currently assert "allowed" unconditionally.
//
// TODO(spec: TS 24.501 §5.5.1.2.4) — bit 5 (NSSAA) should be set when
//
//	at least one S-NSSAI requires slice-specific auth/authz.
//
// TODO(spec: TS 24.501 §5.5.1.2.4) — bit 6 (Emergency) set when
//
//	registration type is "emergency"; bit 7 (Disaster roaming) when
//	the UE roamed into a PLMN under disaster-roaming policy.
func buildRegistrationResult(ue *uectx.AmfUeCtx) nas.FiveGMMCause {
	var b uint8
	// 5GS access = 3GPP. Non-3GPP / dual-access not yet plumbed
	// through ue.AccessType.
	b = regResultAccess3GPP

	// SMS over NAS allowed. Today: always-allowed for non-emergency
	// until UDM SMS subscription read lands.
	if ue.RegistrationType != "emergency" {
		b |= regResultSMSAllowed
	}

	if ue.RegistrationType == "emergency" {
		b |= regResultEmergency
	}

	return nas.FiveGMMCause{Value: b}
}

// buildNASAllowedNSSAI encodes ue.AllowedNSSAI ([]nssf.SNSSAI) as a
// NAS NSSAI IE (TS 24.501 §9.11.3.37) carrying S-NSSAI values per
// §9.11.2.8. Returns nil when there are no allowed slices — the IE is
// optional in that case and the encoder will omit it.
//
// S-NSSAI value encoding (§9.11.2.8 Table 9.11.2.8.1):
//
//	Octet 1 : Length of S-NSSAI contents
//	            0x01 = SST only
//	            0x04 = SST + SD
//	Octet 2 : SST (1 byte)
//	Octets  : SD (3 bytes, big-endian) when length >= 4
//
// SD = 0 or 0xFFFFFF is treated as "no SD" (wildcard) and omitted,
// matching the convention used throughout the AMF.
func buildNASAllowedNSSAI(ue *uectx.AmfUeCtx) *nas.NSSAI {
	allowed, _ := ue.AllowedNSSAI.([]nssf.SNSSAI)
	if len(allowed) == 0 {
		return nil
	}
	// Per §9.11.3.37 NOTE 2, at most 8 S-NSSAIs.
	if len(allowed) > 8 {
		allowed = allowed[:8]
	}
	var buf []byte
	for _, s := range allowed {
		if s.SD != 0 && s.SD != 0xFFFFFF {
			buf = append(buf, 0x04, s.SST,
				byte(s.SD>>16), byte(s.SD>>8), byte(s.SD))
		} else {
			buf = append(buf, 0x01, s.SST)
		}
	}
	return &nas.NSSAI{Value: buf}
}

// buildNASRejectedNSSAI encodes ue.RejectedNSSAI ([]nssf.RejectedSNSSAI)
// as a NAS Rejected NSSAI IE (TS 24.501 §9.11.3.46).
//
// Each rejected S-NSSAI record (§9.11.3.46 figure 9.11.3.46.2):
//
//	Octet 1 bits 8..5 : length of rejected S-NSSAI (2 = SST only, 5 = SST+SD)
//	Octet 1 bits 4..1 : cause value
//	                      0x0 = S-NSSAI not available in current PLMN/SNPN
//	                      0x1 = S-NSSAI not available in current registration area
//	                      0x2 = S-NSSAI not available due to failed/revoked NSSAA
//	Octet 2           : SST (1 byte)
//	Octets 3..5       : SD (3 bytes, big-endian) when length field >= 5
//
// Returns nil when NSSF returned no rejections.
func buildNASRejectedNSSAI(ue *uectx.AmfUeCtx) *nas.RejectedNSSAI {
	rejected, _ := ue.RejectedNSSAI.([]nssf.RejectedSNSSAI)
	if len(rejected) == 0 {
		return nil
	}
	// §9.11.3.46 NOTE 0: the number of rejected S-NSSAI(s) shall not
	// exceed eight.
	if len(rejected) > 8 {
		rejected = rejected[:8]
	}
	var buf []byte
	for _, r := range rejected {
		// r.Cause is already the 4-bit §9.11.3.46 cause value (one of
		// nssf.RejectedCause*). NSSF writes the enum directly; no
		// string translation needed.
		if r.SD != 0 && r.SD != 0xFFFFFF {
			// length=5: cause-length byte + SST + 3B SD
			buf = append(buf, (5<<4)|(r.Cause&0x0F), r.SST,
				byte(r.SD>>16), byte(r.SD>>8), byte(r.SD))
		} else {
			// length=2: cause-length byte + SST
			buf = append(buf, (2<<4)|(r.Cause&0x0F), r.SST)
		}
	}
	return &nas.RejectedNSSAI{Value: buf}
}

// buildMICOResponse echoes a MICO indication IE (TS 24.501 §9.11.3.31)
// when the UE requested MICO and AMF policy accepts. Returns nil when
// the UE didn't request MICO (IE omitted per §5.5.1.2.4).
//
//	RAAI  (bit 1) : 0 = registration area allocation "standard"
//	                1 = "all PLMN registration area allocated"
//	SPRTI (bit 2) : strictly periodic registration timer indication
//
// TODO(spec: TS 24.501 §5.5.1.2.4 "all PLMN registration area allocated") —
//
//	setting RAAI=1 lets MICO UEs skip periodic registration updates on
//	TAC changes. Today we return RAAI=0 (standard allocation).
func buildMICOResponse(ue *uectx.AmfUeCtx) *nas.MICOIndication {
	if !ue.MICORequested {
		return nil
	}
	return &nas.MICOIndication{RAAI: 0, SPRTI: 0}
}
