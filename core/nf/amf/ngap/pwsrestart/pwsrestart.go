// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package pwsrestart — PWS Restart Indication procedure.
//
// Authoritative spec: TS 38.413 v19.2.0 §8.9.3 "PWS Restart
// Indication" (PDF: specs/3gpp/
// ts_138413v190200p.pdf). Message layout at §9.2.8.5.
//
// §8.9.3.1 General (verbatim): "The purpose of the PWS Restart
//
//	Indication procedure is to inform the AMF that PWS information
//	for some or all cells of the NG-RAN node may be reloaded from
//	the CBC if needed. The procedure uses non UE-associated
//	signalling."
//
// §8.9.3.2 Successful Operation (verbatim): "The NG-RAN node
//
//	initiates the procedure by sending a PWS RESTART INDICATION
//	message to the AMF. On receipt of a PWS RESTART INDICATION
//	message, the AMF shall act as defined in TS 23.527."
//
// §8.9.3.3 Abnormal Conditions: "Void."
//
// Procedure code = 34 (TS 38.413 §9.4). Message type per §9.2.8.5:
//
//	InitiatingMessage = PWSRestartIndication  (criticality ignore)
//
// Mandatory IE table (§9.2.8.5):
//
//	id-CellIDListForRestart          M  reject — E-UTRA or NR cells
//	id-GlobalRANNodeID               M  reject — gNB identity
//	id-TAIListForRestart             M  reject — TAIs whose cells restarted
//	id-EmergencyAreaIDListForRestart O  reject — emergency-broadcast areas
//
// TS 23.527 (PWS service architecture) is not in-tree, so the AMF
// action in this skeleton is: decode, log, counter, drop. A full
// deployment forwards the indication to the CBC / CBCF (via Nbf /
// SBc-AP) so warning messages can be re-broadcast on the restarted
// cells. Add that hook when the CBC integration lands.
package pwsrestart

import (
	"fmt"
	"strings"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap"
	"github.com/mmt/mmt-studio-core/nf/amf/ngap/wire"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// Handle decodes a PWS RESTART INDICATION and logs the carried lists.
// Non-UE-associated — no AMFUENGAPID / RANUENGAPID look-ups.
func Handle(gnb *gnbctx.GnbCtx, env *wire.Envelope, _ int) {
	log := logger.Get("amf.ngap.pwsrestart")
	pm.Inc(pm.NGAPPWSRestart, 1)

	var msg genngap.PWSRestartIndication
	if err := msg.UnmarshalAPER(env.Value); err != nil {
		log.Errorf("PWSRestartIndication decode from %s: %v", gnb.GnbIP, err)
		return
	}

	var (
		cellList       string
		ranNode        string
		taiList        string
		emergencyAreas string
	)
	for _, ie := range msg.ProtocolIEs {
		switch int64(ie.Id) {
		case int64(genngap.IdCellIDListForRestart):
			if ie.Value.CellIDListForRestart != nil {
				cellList = formatCellList(ie.Value.CellIDListForRestart)
			}
		case int64(genngap.IdGlobalRANNodeID):
			if ie.Value.GlobalRANNodeID != nil {
				ranNode = formatGlobalRANNodeID(ie.Value.GlobalRANNodeID)
			}
		case int64(genngap.IdTAIListForRestart):
			if ie.Value.TAIListForRestart != nil {
				taiList = formatTAIList(*ie.Value.TAIListForRestart)
			}
		case int64(genngap.IdEmergencyAreaIDListForRestart):
			if ie.Value.EmergencyAreaIDListForRestart != nil {
				emergencyAreas = fmt.Sprintf("%d area(s)",
					len(*ie.Value.EmergencyAreaIDListForRestart))
			}
		}
	}

	log.Infof("PWS Restart Indication from %s: ranNode=%s cells=%s tais=%s emergencyAreas=%s — TS 23.527 CBC forwarding not wired",
		gnb.GnbIP, ranNode, cellList, taiList, emergencyAreas)
	// TODO(spec: TS 23.527) — forward the restart indication to the
	// Cell Broadcast Centre so pending warning messages (PWS, earthquake,
	// tsunami, commercial mobile alert) are re-broadcast on the
	// now-available cells. Not in-tree yet.
}

// formatCellList renders the CHOICE CellIDListForRestart (§9.3.1.55).
func formatCellList(c *genngap.CellIDListForRestart) string {
	switch c.Present {
	case genngap.CellIDListForRestartPresentNRCGIListforRestart:
		if c.NRCGIListforRestart == nil {
			return "nr:0"
		}
		return fmt.Sprintf("nr:%d", len(*c.NRCGIListforRestart))
	case genngap.CellIDListForRestartPresentEUTRACGIListforRestart:
		if c.EUTRACGIListforRestart == nil {
			return "eutra:0"
		}
		return fmt.Sprintf("eutra:%d", len(*c.EUTRACGIListforRestart))
	}
	return "unknown"
}

// formatGlobalRANNodeID renders the CHOICE §9.3.1.5.
func formatGlobalRANNodeID(g *genngap.GlobalRANNodeID) string {
	switch g.Present {
	case genngap.GlobalRANNodeIDPresentGlobalGNBID:
		if g.GlobalGNBID == nil {
			return "gnb:?"
		}
		return fmt.Sprintf("gnb[plmn=%s,id=%s]",
			formatPLMN(g.GlobalGNBID.PLMNIdentity),
			formatGNBID(&g.GlobalGNBID.GNBID))
	case genngap.GlobalRANNodeIDPresentGlobalNgENBID:
		return "ng-enb"
	case genngap.GlobalRANNodeIDPresentGlobalN3IWFID:
		return "n3iwf"
	}
	return "unknown"
}

// formatGNBID renders a gNB-ID (§9.3.1.6) which is a 22..32-bit
// BitString. Returns "<hex>/<bitlen>" e.g. "0000005c/32".
func formatGNBID(g *genngap.GNBID) string {
	if g == nil || g.GNBID == nil {
		return "?"
	}
	b := g.GNBID.Bytes
	out := make([]byte, 0, len(b)*2)
	const hexDigits = "0123456789abcdef"
	for _, v := range b {
		out = append(out, hexDigits[v>>4], hexDigits[v&0x0F])
	}
	return fmt.Sprintf("%s/%d", string(out), g.GNBID.BitLength)
}

// formatTAIList renders a TAIListForRestart (§9.3.1.16).
func formatTAIList(list genngap.TAIListForRestart) string {
	if len(list) == 0 {
		return "0"
	}
	parts := make([]string, 0, len(list))
	for _, t := range list {
		tac := uint32(0)
		if len(t.TAC) >= 3 {
			tac = uint32(t.TAC[0])<<16 | uint32(t.TAC[1])<<8 | uint32(t.TAC[2])
		}
		parts = append(parts, fmt.Sprintf("%s/tac=%d", formatPLMN(t.PLMNIdentity), tac))
	}
	return strings.Join(parts, ",")
}

// formatPLMN renders a 3-byte PLMN Identity (TS 23.003 §2.2).
func formatPLMN(p genngap.PLMNIdentity) string {
	if len(p) < 3 {
		return "?"
	}
	return fmt.Sprintf("%02X%02X%02X", p[0], p[1], p[2])
}

// Register installs the handler on the AMF dispatcher. Called from
// AMF bootstrap (nf/amf/amf.go).
func Register() {
	ngap.Register(ngap.ProcCodePWSRestartIndication, Handle)
}
