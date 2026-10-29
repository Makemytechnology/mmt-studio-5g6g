// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pwsfailure — PWS Failure Indication procedure handler.
//
// Authoritative spec: TS 38.413 v19.2.0 §8.9.4 "PWS Failure
// Indication" (PDF: specs/3gpp/ts_138413v190200p.pdf). Message
// layout at §9.2.8.6.
//
// §8.9.4.1 General (verbatim): "The purpose of the PWS Failure
//
//	Indication procedure is to inform the AMF that ongoing PWS
//	operation for one or more cells of the NG-RAN node has failed.
//	The procedure uses non UE-associated signalling."
//
// §8.9.4.2 Successful Operation (verbatim): "The NG-RAN node
//
//	initiates the procedure by sending a PWS FAILURE INDICATION
//	message to the AMF. On receipt of a PWS FAILURE INDICATION
//	message, the AMF shall act as defined in TS 23.041."
//
// §8.9.4.3 Abnormal Conditions: "Void."
//
// Procedure code = 33 (TS 38.413 §9.4 line 41257ff:
// "id-PWSFailureIndication ProcedureCode ::= 33"). Per §9.2.8.6 the
// message is an InitiatingMessage with criticality ignore at the
// message level (per the §9.4 elementary-procedure descriptor §8.9.4
// "id-PWSFailureIndication … CRITICALITY ignore").
//
// Mandatory IE table (§9.2.8.6):
//
//	id-PWSFailedCellIDList  M  reject — CHOICE { E-UTRA, NR }
//	id-GlobalRANNodeID      M  reject — gNB / ng-eNB / N3IWF identity
//
// TS 23.041 (PWS architecture) is not in-tree; the AMF action in this
// skeleton is: decode, log, counter, drop. Production deployments
// forward to the Cell Broadcast Centre (CBC) over Nbf/SBc-AP so the
// failed cells can be excluded from subsequent broadcasts. Add that
// hook when the CBC integration lands.
package pwsfailure

import (
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// Handle decodes a PWS FAILURE INDICATION and logs the failed cells
// + originating RAN node. Non-UE-associated, no UE-NGAP-ID look-ups.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.pwsfailure")
	pm.Inc(pm.NGAPPWSFailureInd, 1)

	var msg genngap.PWSFailureIndication
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("PWSFailureIndication decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var (
		failedCells string
		ranNode     string
	)
	for _, ie := range msg.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdPWSFailedCellIDList):
			if ie.Value.PWSFailedCellIDList != nil {
				failedCells = formatFailedCells(ie.Value.PWSFailedCellIDList)
			}
		case int64(genngap.IdGlobalRANNodeID):
			if ie.Value.GlobalRANNodeID != nil {
				ranNode = formatGlobalRANNodeID(ie.Value.GlobalRANNodeID)
			}
		}
	}

	log.Infof("PWS Failure Indication from %s: ranNode=%s failedCells=%s — TS 23.041 CBC forwarding not wired",
		gnb.GnbIP, ranNode, failedCells)
	// TODO(spec: TS 23.041) — forward the failed-cell list to the
	// Cell Broadcast Centre so it can re-route warning messages
	// around the affected cells. Not in-tree yet.
}

// formatFailedCells renders the §9.3.1 PWSFailedCellIDList CHOICE.
func formatFailedCells(c *genngap.PWSFailedCellIDList) string {
	switch c.Present {
	case genngap.PWSFailedCellIDListPresentNRCGIPWSFailedList:
		if c.NRCGIPWSFailedList == nil {
			return "nr:0"
		}
		return fmt.Sprintf("nr:%d", len(*c.NRCGIPWSFailedList))
	case genngap.PWSFailedCellIDListPresentEUTRACGIPWSFailedList:
		if c.EUTRACGIPWSFailedList == nil {
			return "eutra:0"
		}
		return fmt.Sprintf("eutra:%d", len(*c.EUTRACGIPWSFailedList))
	}
	return "unknown"
}

// formatGlobalRANNodeID renders the CHOICE §9.3.1.5. Mirrors the
// helper in pwsrestart/pwsrestart.go — duplicated rather than
// imported to keep package boundaries clean.
func formatGlobalRANNodeID(g *genngap.GlobalRANNodeID) string {
	switch g.Present {
	case genngap.GlobalRANNodeIDPresentGlobalGNBID:
		if g.GlobalGNBID == nil {
			return "gnb:?"
		}
		return fmt.Sprintf("gnb[plmn=%02X%02X%02X]",
			g.GlobalGNBID.PLMNIdentity[0], g.GlobalGNBID.PLMNIdentity[1], g.GlobalGNBID.PLMNIdentity[2])
	case genngap.GlobalRANNodeIDPresentGlobalNgENBID:
		return "ng-enb"
	case genngap.GlobalRANNodeIDPresentGlobalN3IWFID:
		return "n3iwf"
	}
	return "unknown"
}

// Register installs the handler on the AMF dispatcher. Called from
// AMF bootstrap (nf/amf/amf.go).
func Register() {
	ngap.Register(ngap.ProcCodePWSFailureIndication, Handle)
}
