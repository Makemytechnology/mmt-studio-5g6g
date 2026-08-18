// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM STATUS sender — TS 24.501 v19.6.2 §5.4.6 + §8.2.29.
//
// The 5GMM STATUS message is the AMF's protocol-error feedback channel
// for things that don't justify a full RegistrationReject / DeregReject
// but still must be reported per spec. Verbatim from §7.4 (TS 24.501
// v19.6.2 line 54221-54226):
//
//	"if the network receives a message with message type not defined
//	 for the EPD or not implemented by the receiver, it shall ignore
//	 the message except that it should return a status message
//	 (5GMM STATUS or 5GSM STATUS depending on the EPD) with cause
//	 #97 'message type non-existent or not implemented'."
//
// And from §7.5.1 line 54262-54269 for non-semantical mandatory IE
// errors: "ignore the message except that it should return a status
// message ... with cause #96 'invalid mandatory information'."
//
// Wire format per §8.2.29.1.1:
//	Octet 1: Extended Protocol Discriminator = 0x7E
//	Octet 2: Security header type (4 bits) | Spare half octet (4 bits)
//	         — set SHT=0 here; dlnas.WrapDL bumps to SHT=2
//	         (integrity-and-cipher) when a NAS context is active.
//	Octet 3: Message type = 0x64 (5GMM STATUS, see §9.7)
//	Octet 4: 5GMM cause IE (1 byte, §9.11.3.2)
//
// On §5.4.6.3: "On receipt of a 5GMM STATUS message in the AMF, no
// state transition and no specific action shall be taken" — that's
// the receive-side rule (already in handle5GMMStatus). The send
// path is governed by the per-clause "shall/should return" language
// scattered through §7.4 / §7.5.1 / §5.4.5 etc.
package gmm

import (
	nas "github.com/mmt/nasgen/generated"

	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/dlnas"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// 5GMM cause values defined for the 5GMM STATUS message body, taken
// from TS 24.501 Table 9.11.3.2.1. Only the protocol-error subset is
// listed here; reject-cause constants live in registration.go.
const (
	Cause5GMMSemanticallyIncorrectMsg uint8 = 95
	// 96, 99, 100, 111 already defined in registration.go (also valid
	// in 5GMM STATUS bodies).
	Cause5GMMUnknownOrNotImpl       uint8 = 97
	Cause5GMMNotCompatibleWithState uint8 = 98
)

// Send5GMMStatus emits a 5GMM STATUS message with the given cause. The
// gNB lookup mirrors what sendRegistrationReject does — if the gNB
// dropped between dispatch and now, log + bail (the UE will time out
// on its side, which is fine; the spec uses "should" for the network).
//
// dlnas.Send applies the security wrapper when a NAS context exists
// (SHT=2); when no context yet, the bytes go on the wire plain (SHT=0)
// — also legal because 5GMM STATUS itself carries no sensitive content.
func Send5GMMStatus(ue *uectx.AmfUeCtx, cause uint8) {
	log := logger.Get("amf.gmm.status")

	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		log.WithIMSI(ue.IMSI).Warnf("Send5GMMStatus amfUeID=%d cause=%d — gNB %q gone, dropping",
			ue.AmfUeNGAPID, cause, ue.GnbKey)
		return
	}

	msg := &nas.FivegmmStatus{Cause5GMM: nas.FiveGMMCause{Value: cause}}
	pdu, err := msg.Encode()
	if err != nil {
		log.WithIMSI(ue.IMSI).Errorf("Send5GMMStatus amfUeID=%d encode: %v",
			ue.AmfUeNGAPID, err)
		return
	}
	if err := dlnas.Send(gnb, ue, pdu); err != nil {
		log.WithIMSI(ue.IMSI).Errorf("Send5GMMStatus amfUeID=%d send: %v",
			ue.AmfUeNGAPID, err)
		return
	}
	log.WithIMSI(ue.IMSI).Infof("5GMM STATUS sent amfUeID=%d cause=%d (TS 24.501 §8.2.29 / Table 9.11.3.2.1)",
		ue.AmfUeNGAPID, cause)
}
