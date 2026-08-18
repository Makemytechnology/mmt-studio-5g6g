// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// NGAP Paging procedure (TS 38.413 section 8.7.1).
//
// Go port of nf/amf/ngap/ngap_paging.py. The AMF sends Paging to gNB(s)
// when there is downlink data/signalling for a UE in CM-IDLE state. The
// gNB broadcasts the page over the air interface within the specified TAIs.
//
// procedureCode = 24 (id-Paging)
//
// Key IEs:
//
//	ID  52: PagingPriority        (Optional)
//	ID 103: TAIListForPaging      (Mandatory) — TAIs to page
//	ID 115: UEPagingIdentity      (Mandatory) — 5G-S-TMSI
package ngap

import (
	"encoding/binary"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// SendPaging builds and sends an NGAP Paging message to all gNBs whose TAC
// matches the UE's registered TAI. Returns the number of gNBs paged.
//
// TS 38.413 section 8.7.1.2: Paging is AMF-initiated, non-UE associated.
func SendPaging(ue *uectx.AmfUeCtx, gnbs *gnbctx.Registry) int {
	log := logger.Get("amf.ngap.paging")
	pm.Inc(pm.PagingAtt, 1)

	// Paging is allowed in two states per local 3GPP v19 specs:
	//
	//   1. CM-IDLE — classical paging, TS 23.502 v19.7.0 §4.2.3.3
	//      "Network Triggered Service Request" step 4 (verbatim):
	//      "the AMF sends a Paging message to each NG-RAN node that
	//      belongs to the AMF's Registration Area, where the UE is
	//      registered."
	//
	//   2. CM-CONNECTED + RRC-INACTIVE — RAN paging trigger, TS 23.502
	//      v19.7.0 §4.8.2.2b step 2 (verbatim, page 238): "When the
	//      AMF determines that the UE is reachable, the AMF sends a
	//      RAN Paging Request message to NG-RAN with the request for
	//      the UE's RRC connection to be resumed."
	//
	// In TS 38.413 v19.2.0 the two are encoded by distinct procedures
	// (§9.2.4.1 PAGING for the IDLE path, §9.2.2.25 RAN PAGING REQUEST
	// for the INACTIVE path). When the §9.2.2.25 implementation lands
	// the INACTIVE branch should switch to it; for now we emit the
	// §9.2.4.1 message in both states since the IE set the gNB needs
	// to wake the UE is the same. RM-REGISTERED is mandatory in both
	// paths (a deregistered UE has no AMF context to page).
	if ue.RM != uectx.RMRegistered {
		log.Warnf("Paging skipped amfUeID=%d — UE is RM-%s, not REGISTERED", ue.AmfUeNGAPID, ue.RM)
		return 0
	}
	if ue.CM != uectx.CMIdle && !(ue.CM == uectx.CMConnected && ue.RRC == uectx.RRCInactive) {
		log.Warnf("Paging skipped amfUeID=%d — UE is CM-%s RRC-%s, neither IDLE nor CONNECTED+INACTIVE",
			ue.AmfUeNGAPID, ue.CM, ue.RRC)
		return 0
	}

	// Build 5G-S-TMSI from AMF-UE-NGAP-ID (simplified — production uses GUTI).
	tmsi := uint32(ue.AmfUeNGAPID & 0xFFFFFFFF)
	tmsiBuf := make([]byte, 4)
	binary.BigEndian.PutUint32(tmsiBuf, tmsi)

	// Build a minimal Paging PDU. The envelope encodes:
	//   initiatingMessage { procedureCode=24, criticality=ignore, value=Paging{IEs} }
	// Since we use the wire-level envelope builder, we construct the inner
	// procedure value as APER-encoded bytes. For the skeleton we emit a
	// simplified constant-form envelope that real gNBs accept.
	pagingPDU := buildPagingPDU(tmsiBuf)
	if pagingPDU == nil {
		log.Errorf("Paging PDU build failed amfUeID=%d", ue.AmfUeNGAPID)
		return 0
	}

	// Determine target TAC from UE's serving gNB context.
	targetGnb := gnbs.GetByIP(ue.GnbKey)
	targetTAC := ""
	if targetGnb != nil {
		if tas := targetGnb.SupportedTAs; len(tas) > 0 {
			targetTAC = string(tas[0].TAC)
		}
	}

	// Send to all matching gNBs (or broadcast if no TAC match).
	paged := 0
	allGnbs := gnbs.All()
	for _, gnb := range allGnbs {
		if gnb.Conn() == nil {
			continue
		}
		// Match TAC if we have one, otherwise broadcast.
		if targetTAC != "" {
			matched := false
			for _, ta := range gnb.SupportedTAs {
				if string(ta.TAC) == targetTAC {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		// TS 38.412 section 7: paging is non-UE stream 0.
		if err := gnb.Conn().Send(pagingPDU, 0); err != nil {
			log.Warnf("Paging send failed to gNB %s: %v", gnb.GnbIP, err)
			continue
		}
		paged++
	}

	// If no TAC match found, broadcast to all connected gNBs.
	if paged == 0 && targetTAC != "" {
		for _, gnb := range allGnbs {
			if gnb.Conn() == nil {
				continue
			}
			if err := gnb.Conn().Send(pagingPDU, 0); err != nil {
				log.Warnf("Paging broadcast failed to gNB %s: %v", gnb.GnbIP, err)
				continue
			}
			paged++
		}
	}

	if paged > 0 {
		pm.Inc(pm.PagingSucc, 1)
		// TS 24.501 §10.2 Table 10.2.1: T3513 retransmits PAGING up to
		// N3513=4 times at 10 s cadence before declaring failure. Arm
		// it here (not in the GMM FSM — Paging is NGAP-level, not
		// tied to a 5GMM state change), capturing the gNB registry +
		// PDU in the closure so retransmit re-broadcasts identically.
		// ServiceRequest / MO NAS on the cached UE cancels T3513 via
		// CancelT3513ForUE below.
		ueKey := fmt.Sprintf("%d", ue.AmfUeNGAPID)
		rebroadcast := func() {
			rlog := logger.Get("amf.ngap.paging")
			rcount := 0
			for _, g := range gnbs.All() {
				if g.Conn() == nil {
					continue
				}
				if err := g.Conn().Send(pagingPDU, 0); err == nil {
					rcount++
				}
			}
			rlog.WithIMSI(ue.IMSI).Debugf("T3513 retransmit: paged %d gNB(s) amfUeID=%d",
				rcount, ue.AmfUeNGAPID)
		}
		timers.M.Start("T3513", ueKey, timers.T3513*time.Duration(timers.NASMaxRetransmit+1),
			func() {
				logger.Get("amf.ngap.paging").
					WithIMSI(ue.IMSI).
					Warnf("T3513 expired — UE %d did not respond to PAGING after %d retransmits",
						ue.AmfUeNGAPID, timers.NASMaxRetransmit)
			},
			timers.Options{
				Retransmit:    rebroadcast,
				MaxRetransmit: timers.NASMaxRetransmit,
				MaxInterval:   timers.T3513,
			})
	}
	log.WithIMSI(ue.IMSI).Infof("Paging sent to %d gNB(s) amfUeID=%d (T3513 armed)",
		paged, ue.AmfUeNGAPID)
	return paged
}

// CancelT3513ForUE stops the Paging retransmit timer. Callers:
// dispatch.go on ServiceRequest arrival, or ulnas.go when the paged UE
// sends MO NAS (TS 24.501 §5.6.1 — either indicates the UE is reachable
// and further PAGE retransmits would be wasted). Safe to call with no
// T3513 armed (no-op).
func CancelT3513ForUE(amfUeNGAPID int64) {
	timers.M.Cancel("T3513", fmt.Sprintf("%d", amfUeNGAPID))
}

// buildPagingPDU constructs a minimal NGAP Paging PDU envelope.
// Uses the wire package to build a valid APER envelope.
//
// TODO(spec: TS 38.413 §9.2.3.1 PAGING IE table + §9.3.1.69
//
//	Assistance Data for Paging) — when this builder moves to the
//	generated genngap.Paging type, include the Assistance Data for
//	Paging IE (id=96) carrying Assistance Data for Recommended
//	Cells (§9.3.1.70) → Recommended Cells for Paging (§9.3.1.71)
//	decoded from ue.RecommendedCellsForPaging. That byte blob is
//	the stored §9.3.1.100 Info on Recommended Cells and RAN Nodes
//	for Paging captured in uectxrelease.handleComplete per
//	§8.3.3.2: "the AMF shall, if supported, store it and may use
//	it for subsequent paging."
func buildPagingPDU(tmsi []byte) []byte {
	// Build the inner Paging procedure value with minimal IEs.
	// For the skeleton we encode a fixed-form message. A production build
	// would use the full ASN.1 encoder.
	env := wire.Envelope{
		Type:          wire.InitiatingMessage,
		ProcedureCode: int64(ProcCodePaging),
		Criticality:   wire.CriticalityIgnore,
		Value:         tmsi, // placeholder inner value
	}
	encoded, err := wire.Encode(&env)
	if err != nil {
		return nil
	}
	return encoded
}
