// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Extracted from registration.go by refactor: split god-file by
// sub-concern. Imports are re-derived by goimports.
package gmm

import (
	"github.com/mmt/mmt-studio-core/nf/amf/ctx"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/nssf"
	nas "github.com/mmt/nasgen/generated"
)

// runNSSFSelection is the RR-driven entry point used from
// handleRegistrationRequest. It parses the NAS Requested NSSAI IE (TS
// 24.501 §9.11.3.37) off the REGISTRATION REQUEST and hands the
// resolved list to the bare helper.
//
// Spec for the intersection semantics — TS 23.501 §5.15.5.2.1
// "Registration to a set of Network Slices" (PDF:
// specs/3gpp/ts_123501v190700p.pdf): the serving-network Allowed
// NSSAI is derived from the UE's Requested NSSAI under the AMF's
// Subscribed-S-NSSAIs plus the serving-network slice configuration.
//
// Spec for the procedure step — TS 23.502 §4.2.2.2.2 General
// Registration, step 4a "[Conditional] Initial AMF to NSSF:
// Nnssf_NSSelection_Get" (PDF: specs/3gpp/ts_123502v190700p.pdf):
// the AMF invokes NSSF only when it cannot serve all the S-NSSAI(s)
// from Requested NSSAI permitted by the subscription. We always
// invoke because the in-process NSSF is cheap; the SBI TODO below
// tracks promoting this to the conditional gate.
func runNSSFSelection(ue *uectx.AmfUeCtx, rr *nas.RegistrationRequest) {
	runNSSFWithRequested(ue, parseRequestedNSSAI(rr.RequestedNSSAI))
}

// runNSSFWithRequested is the bare version: caller supplies the
// parsed Requested NSSAI list (may be nil/empty when the trigger
// doesn't carry one, e.g. the Identity-procedure path). Per TS 24.501
// §8.2.22 "Identity response" message IE table the message carries
// only (EPD | SHT | Spare | MsgType | Mobile identity) — no Requested
// NSSAI IE. When the 5G-GUTI-without-cached-mapping path forces the
// AMF to run the Identification procedure before it ever saw a
// Requested NSSAI from this connection, Allowed NSSAI must still be
// computed before ICS. Without this helper, handleRegistrationRequest
// returned from its SUCI-path switch-case before NSSF ran,
// handleIdentityResponse went straight to auth/SMC, and
// InitialContextSetup.Send tripped its mandatory-IE guard —
// AllowedNSSAI is PRESENCE mandatory per TS 38.413 §9.2.2.1 "INITIAL
// CONTEXT SETUP REQUEST" message IE table.
func runNSSFWithRequested(ue *uectx.AmfUeCtx, requested []nssf.SNSSAI) {
	amfSlices := amfNSSAI()
	gnbSlices := gnbNSSAI(ue)
	// TODO(arch: sbi-N22: Nnssf_NSSelection_Get) —
	//   specs/3gpp/ts_129531v190600p.pdf §5.2 "NetworkSliceInformationDocument":
	//   GET /network-slice-information with query params (nf-type=AMF,
	//   nf-id, slice-info-request-for-registration, target-amf-set, ...)
	//   returns AuthorizedNssaiAvailabilityData. Today we call nssf
	//   in-process — the TS 23.502 §4.2.2.2.2 step 4a "Conditional"
	//   gate is also elided; we invoke unconditionally. When SBI
	//   lands, reinstate the "cannot serve all S-NSSAIs" conditional.
	result := nssf.SelectAllowedNSSAI(ue.IMSI, requested, amfSlices, gnbSlices, "")
	ue.AllowedNSSAI = result.Allowed
	ue.RejectedNSSAI = result.Rejected
	ue.SubscribedNSSAI = result.Subscribed
}

// allowedNSSAIEmpty reports whether NSSF selection (incl. the
// post-3bd0c43 default-subscribed fallback) produced an empty
// Allowed NSSAI. Used to gate the §5.5.1.2.5 "no network slices
// available" RegReject path.
func allowedNSSAIEmpty(ue *uectx.AmfUeCtx) bool {
	allowed, _ := ue.AllowedNSSAI.([]nssf.SNSSAI)
	return len(allowed) == 0
}

// parseRequestedNSSAI decodes NAS NSSAI IE (TS 24.501 §9.11.3.37) into S-NSSAI
// pairs. Each S-NSSAI: length byte + SST + optional SD(3). length=1|2|4|5|8.
func parseRequestedNSSAI(n *nas.NSSAI) []nssf.SNSSAI {
	if n == nil || len(n.Value) == 0 {
		return nil
	}
	out := []nssf.SNSSAI{}
	b := n.Value
	for len(b) > 0 {
		l := int(b[0])
		if l == 0 || l+1 > len(b) {
			break
		}
		item := b[1 : 1+l]
		s := nssf.SNSSAI{SST: item[0]}
		if len(item) >= 4 { // SST + SD(3)
			s.SD = uint32(item[1])<<16 | uint32(item[2])<<8 | uint32(item[3])
		}
		out = append(out, s)
		b = b[1+l:]
	}
	return out
}

// amfNSSAI returns the AMF's PLMN-support slice set (TS 23.501 §5.15.4).
func amfNSSAI() []nssf.SNSSAI {
	var out []nssf.SNSSAI
	for _, p := range ctx.Default.PLMNSupportList() {
		for _, s := range p.Slices {
			var sd uint32
			if len(s.SD) == 3 {
				sd = uint32(s.SD[0])<<16 | uint32(s.SD[1])<<8 | uint32(s.SD[2])
			}
			out = append(out, nssf.SNSSAI{SST: s.SST, SD: sd})
		}
	}
	return out
}

// gnbNSSAI returns slices supported by the serving gNB (TS 38.413 §9.2.6.1).
func gnbNSSAI(ue *uectx.AmfUeCtx) []nssf.SNSSAI {
	gnb := gnbctx.Default.GetByIP(ue.GnbKey)
	if gnb == nil {
		return nil
	}
	var out []nssf.SNSSAI
	for _, ta := range gnb.SupportedTAs {
		for _, bp := range ta.BroadcastPLMNs {
			for _, sl := range bp.Slices {
				if len(sl.SNSSAIRaw) == 0 {
					continue
				}
				s := nssf.SNSSAI{SST: sl.SNSSAIRaw[0]}
				if len(sl.SNSSAIRaw) >= 4 {
					s.SD = uint32(sl.SNSSAIRaw[1])<<16 | uint32(sl.SNSSAIRaw[2])<<8 | uint32(sl.SNSSAIRaw[3])
				}
				out = append(out, s)
			}
		}
	}
	return out
}
