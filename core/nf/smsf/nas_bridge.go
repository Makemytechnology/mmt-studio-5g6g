// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SMSF ↔ AMF NAS bridge.
//
// The AMF receives UL NAS Transport (TS 24.501 §8.2.10) carrying a
// Payload Container of type SMS (§9.11.3.39 value=2). The bytes inside
// are an SM-CP message (TS 24.011 §8.1) wrapping an RP-PDU (§8.2)
// wrapping a TPDU (TS 23.040 §9.2.2). The MO entry point here unwraps
// that stack, routes the message via the existing SMSF processing
// pipeline, and returns the bytes the AMF should ship back inside a
// DL NAS Transport (TS 24.501 §8.2.11) Payload Container.
//
// Procedure tracing — TS 23.502 §4.13.3.5 (MO SMS over NAS in
// CM-CONNECTED) steps 4-6:
//
//	step 4: UE → AMF: UL NAS Transport(SMS, CP-DATA(RP-DATA(SMS-SUBMIT)))
//	step 5: AMF immediately ACKs the CP-Layer to the UE: DL NAS
//	        Transport(SMS, CP-ACK)
//	step 6: SMSF processes the RP-PDU and the AMF later sends:
//	        DL NAS Transport(SMS, CP-DATA(RP-ACK)) once the SC has
//	        accepted the message (we synthesise the RP-ACK locally
//	        because there's no real SC in this build).
//
// The function below returns BOTH responses so the AMF can choose to
// send them back-to-back (our behaviour today) or interleave a real
// SC round-trip in between.

package smsf

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// NASMOResponse is what ProcessMOSMSFromNAS returns to the AMF.
// Both fields are *Payload Container* bytes (the inner SMS PDUs);
// the AMF wraps each in its own DL NAS Transport per TS 24.501
// §8.2.11.
type NASMOResponse struct {
	// CPAck is the DL NAS Transport payload for step 5 of TS 23.502
	// §4.13.3.5 — a CP-ACK that confirms reception of the UE's
	// CP-DATA at the CP layer (TS 24.011 §7.2.2).
	CPAck []byte
	// RPAckCPData is the payload for step 6 — a CP-DATA wrapping an
	// RP-ACK (TS 24.011 §7.3.3) that confirms the SC accepted the
	// SMS-SUBMIT. Empty (nil) when the inbound message could not be
	// processed; in that case RPErrorCPData is populated instead.
	RPAckCPData []byte
	// RPErrorCPData is the §6 payload when the SC rejected the
	// submission, carrying RP-ERROR (TS 24.011 §7.3.4) with the
	// appropriate RP-Cause from §8.2.5.4. Mutually exclusive with
	// RPAckCPData.
	RPErrorCPData []byte
	// MO is the parsed result from the existing SMSF processing
	// pipeline (segmentation / DB store / routing). Useful for
	// logging at the AMF call-site.
	MO MOResult
}

// ProcessMOSMSFromNAS decodes the CP/RP/TPDU stack the UE sent over
// NAS, runs the existing SMSF MO processing pipeline (segmentation,
// DB store, routing — see ProcessMOSMS in smsf.go), and assembles
// the CP-ACK and CP-DATA(RP-ACK) responses the AMF needs to wrap in
// DL NAS Transport per TS 23.502 §4.13.3.5.
//
// Spec walk:
//
//   - CP-DATA decoded per TS 24.011 §8.1 (DecodeCP).
//   - RP-DATA (MS→Net) decoded per TS 24.011 §8.2 (DecodeRP).
//   - SMS-SUBMIT TPDU decoded per TS 23.040 §9.2.2.2 (DecodeSMSSubmit).
//   - TP-User-Data decoded per TS 23.038 §6.1.2 / §6.2 (DecodeUserData).
//   - The same TP-MR is mirrored back in the RP-ACK per TS 24.011
//     §7.3.3 ("the same RP-Message Reference value as ... the
//     RP-DATA message").
func ProcessMOSMSFromNAS(senderIMSI string, payload []byte) (*NASMOResponse, error) {
	log := logger.Get("smsf.nas").WithIMSI(senderIMSI)

	cp, err := DecodeCP(payload)
	if err != nil {
		return nil, fmt.Errorf("CP decode: %w", err)
	}
	switch cp.MsgType {
	case CPData:
		// fall through to RP processing
	case CPAck:
		// TS 24.011 §5.3.2.2: in the GPRS/EPS/5GS RPDU transfer
		// procedure, "On receipt of the CP-ACK message in the Wait
		// for CP-ACK state, the SMC ... resets the timer TC1*".
		// We stop here — no further DL traffic owed for this CP-ACK.
		// TODO(spec: TS 24.011 §5.3.2.2): plumb the TC1* retransmission
		// timer through SmsfContext so we actually reset it here
		// instead of relying on the network never losing CP-DATA.
		log.Debugf("CP-ACK received TI=%d", cp.TI)
		return &NASMOResponse{}, nil
	case CPError:
		log.Warnf("CP-ERROR received TI=%d cause=0x%02X", cp.TI, cp.Cause)
		return &NASMOResponse{}, nil
	default:
		return nil, fmt.Errorf("CP: unhandled message type 0x%02X", cp.MsgType)
	}

	rp, err := DecodeRP(cp.UserData)
	if err != nil {
		// CP layer was fine — ack it, then fail the RP layer.
		// TS 24.011 §7.3.4: RP-ERROR mirrors the RP-Message-Reference
		// of the offending RP-DATA. We don't have one, so emit just
		// the CP-ACK and let the UE time out.
		// TODO(spec: TS 24.011 §8.2.5.4): once we surface decode-cause
		// granularity, return RP-ERROR with cause=111 ("Protocol
		// error, unspecified") instead of swallowing the error.
		log.Warnf("RP decode: %v", err)
		return &NASMOResponse{CPAck: EncodeCPAck(cp.TI)}, nil
	}
	if rp.MTI != RPDataMSToNet {
		// MO direction must be RP-DATA(0). RP-ACK / RP-ERROR are the
		// MS responding to a previous MT — handled by a different
		// path (TODO below).
		// TODO(spec: TS 24.011 §7.3.3 / §7.3.4): wire the MS-side
		// RP-ACK / RP-ERROR back to the matching MT-SMS state machine
		// once SmsfContext tracks per-message RP-Message-Reference.
		log.Debugf("RP MTI=0x%02X (not MS→Net DATA) — TODO MT-side ack handling",
			rp.MTI)
		return &NASMOResponse{CPAck: EncodeCPAck(cp.TI)}, nil
	}

	tpdu, err := DecodeSMSSubmit(rp.UserData)
	if err != nil {
		log.Warnf("SMS-SUBMIT decode: %v", err)
		// Per TS 24.011 §7.3.4 the SMSC would emit RP-ERROR. We mirror
		// that here with cause=95 ("Semantically incorrect message")
		// from TS 24.011 §8.2.5.4 / TS 24.008 §10.5.4.11 Table 10.5.137.
		rpErr := EncodeRPError(rp.Reference, 95, true /*netToMS*/)
		return &NASMOResponse{
			CPAck:         EncodeCPAck(cp.TI),
			RPErrorCPData: EncodeCPData(cp.TI, rpErr),
		}, nil
	}

	text, err := DecodeUserData(tpdu.Encoding, tpdu.DCS, tpdu.UDL, tpdu.UDH, tpdu.UD)
	if err != nil {
		log.Warnf("TP-UD decode: %v", err)
		text = "" // still let the MO record persist with empty body
	}
	log.Infof("MO-SMS TPDU: ref=%d → %s, %d %s octets",
		tpdu.Reference, tpdu.DAMSISDN, tpdu.UDL, tpdu.Encoding)

	mo := ProcessMOSMS(senderIMSI, tpdu.DAMSISDN, text, tpdu.Encoding)

	resp := &NASMOResponse{
		CPAck: EncodeCPAck(cp.TI),
		MO:    mo,
	}
	if mo.OK {
		// RP-ACK Net→MS — TS 24.011 §7.3.3.
		rpAck := EncodeRPAck(rp.Reference, true /*netToMS*/)
		resp.RPAckCPData = EncodeCPData(cp.TI, rpAck)
	} else {
		// RP-ERROR Net→MS — TS 24.011 §7.3.4. Cause=21 ("Short message
		// transfer rejected") per TS 24.011 §8.2.5.4 covers the
		// generic local-routing failure case.
		rpErr := EncodeRPError(rp.Reference, 21, true /*netToMS*/)
		resp.RPErrorCPData = EncodeCPData(cp.TI, rpErr)
	}
	return resp, nil
}

// BuildPayloadContainerSMS frames the bytes returned from
// ProcessMOSMSFromNAS as the *contents* of a UL/DL NAS Transport
// Payload Container per TS 24.501 §9.11.3.39. The Payload Container
// for type=SMS (=2 per §9.11.3.40) carries a single SM-CP message
// per TS 24.501 §9.11.3.39 / TS 24.011 §7.2.
//
// Today this is just an identity helper — the AMF call-site already
// builds the outer DL NAS Transport message via its own NAS encoder.
// Wrapping the helper in its own function keeps the §-cite local to
// the SMSF surface so future refactors (e.g. adding header-compression
// for IoT short-data) have an obvious place to land.
func BuildPayloadContainerSMS(cpPDU []byte) []byte {
	// TODO(spec: TS 24.501 §9.11.3.39): when we start emitting concat
	// SMS over NAS, we may need to fragment across multiple Payload
	// Containers — §9.11.3.39 Table 9.11.3.39-1 caps the container
	// length at 65 535 octets but practical NAS MTUs are far below
	// that. Currently no fragmenting; pass through untouched.
	return cpPDU
}
