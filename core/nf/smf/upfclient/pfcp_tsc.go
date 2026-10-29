// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// PFCP TSC extension of the bridge — Rel-19 TS 29.244:
//
//	§7.5.2.1  Create Bridge Info for TSC (IE 194, BII flag) in the
//	          Session Establishment Request;
//	§7.5.3.6  Created Bridge Info for TSC (grouped IE 195: Port Number
//	          196 + NW-TT Port Number 197 + 5GS User Plane Node ID 198)
//	          in the Establishment Response;
//	§7.5.4.18 TSC Management Information (grouped IE 199) carrying
//	          PMIC (202) / UMIC (266) in the Session Modification
//	          Request, answered via grouped IE 200.
//
// Implements the upf.TSCBridge optional capability.
package upfclient

import (
	"encoding/binary"
	"fmt"
	"net"
	"time"

	upf "github.com/mmt/mmt-studio-core/nf/upf"
	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"
)

// RequestBridgeInfo arms the BII flag for the next CommitSession
// (TS 23.502 §4.3.2.2.1 step 10a: the SMF requests bridge information
// for Ethernet PDU sessions with TSC policy armed).
func (p *PfcpBridge) RequestBridgeInfo(imsi string, pduSessionID uint8) error {
	key := sessionKey{imsi, pduSessionID}
	p.mu.Lock()
	defer p.mu.Unlock()
	r := p.pending[key]
	if r == nil {
		r = &pendingRules{}
		p.pending[key] = r
	}
	r.wantBridgeInfo = true
	return nil
}

// BridgeInfo returns the Created Bridge Info the UPF handed back at
// establishment (step 10b). ok=false before establishment or when the
// UPF didn't include IE 195.
func (p *PfcpBridge) BridgeInfo(imsi string, pduSessionID uint8) (uint16, []uint16, uint64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := p.sessions[sessionKey{imsi, pduSessionID}]
	if s == nil || !s.hasBridgeInfo {
		return 0, nil, 0, false
	}
	return s.bridgeDSTTPort, append([]uint16(nil), s.bridgeNWTTPorts...), s.bridgeNodeID, true
}

// SendTSCManagementInformation ships PMIC/UMIC containers over a
// §7.5.4 Session Modification Request (TSC Management Information,
// IE 199) and returns any reply containers from IE 200.
func (p *PfcpBridge) SendTSCManagementInformation(imsi string, pduSessionID uint8,
	entries []upf.TSCMgmtEntry) ([]upf.TSCMgmtEntry, error) {

	key := sessionKey{imsi, pduSessionID}
	p.mu.Lock()
	s := p.sessions[key]
	p.mu.Unlock()
	if s == nil {
		return nil, fmt.Errorf("SendTSCManagementInformation: no established session for %s/%d", imsi, pduSessionID)
	}

	req := &genpfcp.SessionModificationRequest{SEID: s.upSEID}
	for _, e := range entries {
		var tmi genpfcp.TSCManagementInformationSMReq
		if len(e.PMIC) > 0 {
			tmi.PortManagementInformationContainer = &genpfcp.PortManagementInformationContainer{Value: e.PMIC}
			if e.NWTTPort != 0 {
				tmi.NWTTPortNumber = &genpfcp.NWTTPortNumber{Value: encodePortNumber(e.NWTTPort)}
			}
		}
		if len(e.UMIC) > 0 {
			tmi.UserPlaneNodeManagementInformationContainer = &genpfcp.UserPlaneNodeManagementInformationContainer{Value: e.UMIC}
		}
		req.TSCManagementInformation = append(req.TSCManagementInformation, tmi)
	}

	payload, err := stripHeader(req)
	if err != nil {
		return nil, fmt.Errorf("SendTSCManagementInformation encode: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypeSessionModificationRequest, s.upSEID, payload))
	if err != nil {
		return nil, fmt.Errorf("SendTSCManagementInformation send: %w", err)
	}
	var resp genpfcp.SessionModificationResponse
	if err := resp.Decode(respBytes); err != nil {
		return nil, fmt.Errorf("SendTSCManagementInformation response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return nil, fmt.Errorf("SendTSCManagementInformation rejected: cause=%d", resp.Cause.Value)
	}
	var replies []upf.TSCMgmtEntry
	for i := range resp.TSCManagementInformation {
		r := &resp.TSCManagementInformation[i]
		var e upf.TSCMgmtEntry
		if r.PortManagementInformationContainer != nil {
			e.PMIC = r.PortManagementInformationContainer.Value
		}
		if r.UserPlaneNodeManagementInformationContainer != nil {
			e.UMIC = r.UserPlaneNodeManagementInformationContainer.Value
		}
		if r.NWTTPortNumber != nil {
			e.NWTTPort = decodePortNumber(r.NWTTPortNumber.Value)
		}
		if len(e.PMIC) > 0 || len(e.UMIC) > 0 {
			replies = append(replies, e)
		}
	}
	p.log.WithIMSI(imsi).Infof("PFCP §7.5.4.18 TSC Management Information: sent=%d replied=%d",
		len(entries), len(replies))
	return replies, nil
}

// applyCreatedBridgeInfo folds a §7.5.3.6 Created Bridge Info grouped
// IE into the session state. Caller holds p.mu.
func (s *sessionState) applyCreatedBridgeInfo(cbi *genpfcp.CreatedBridgeInfoForTSC) {
	if cbi == nil {
		return
	}
	s.hasBridgeInfo = true
	if cbi.DSTTPortNumber != nil {
		s.bridgeDSTTPort = decodePortNumber(cbi.DSTTPortNumber.Value)
	}
	for i := range cbi.NWTTPortNumber {
		s.bridgeNWTTPorts = append(s.bridgeNWTTPorts, decodePortNumber(cbi.NWTTPortNumber[i].Value))
	}
	if cbi.FiveGSUserPlaneNode != nil {
		s.bridgeNodeID = decodeUserPlaneNodeID(cbi.FiveGSUserPlaneNode.Value)
	}
}

// encodePortNumber builds the 4-octet Port Number value of TS 29.244
// §8.2.141/§8.2.142 (TSN/TSCTS: two zero octets + Unsigned16 port).
func encodePortNumber(port uint16) []byte {
	b := make([]byte, 4)
	binary.BigEndian.PutUint16(b[2:], port)
	return b
}

func decodePortNumber(v []byte) uint16 {
	if len(v) < 4 {
		return 0
	}
	return binary.BigEndian.Uint16(v[2:])
}

// EncodeUserPlaneNodeID builds the §8.2.143 5GS User Plane Node ID
// value: flags octet (BID=1) + Unsigned64 node ID.
func EncodeUserPlaneNodeID(id uint64) []byte {
	b := make([]byte, 9)
	b[0] = 0x01 // BID
	binary.BigEndian.PutUint64(b[1:], id)
	return b
}

func decodeUserPlaneNodeID(v []byte) uint64 {
	if len(v) < 9 || v[0]&0x01 == 0 {
		return 0
	}
	return binary.BigEndian.Uint64(v[1:9])
}

// handleNodeReport terminates a §7.4.5.1 PFCP Node Report Request
// carrying Clock Drift Reports (Rel-19 TSC): each report becomes an
// upf.ReportClockDrift record on the bridge report ring, consumed by
// nf/smf/session (ApplyClockDriftReport → §5.27.2.4 TSCAI math).
// Always answered with a Node Report Response (§7.4.5.2).
func (p *PfcpBridge) handleNodeReport(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	var req genpfcp.NodeReportRequest
	if err := req.Decode(payload); err != nil {
		p.log.Warnf("§7.4.5.1 Node Report decode from %s: %v", peer, err)
		return
	}
	for i := range req.ClockDriftReport {
		cdr := &req.ClockDriftReport[i]
		pl := &upf.ClockDriftPayload{}
		if v := cdr.TSNTimeDomainNumber.Value; len(v) >= 1 {
			pl.TimeDomain = v[0]
		}
		if cdr.TimeOffsetMeasurement != nil && len(cdr.TimeOffsetMeasurement.Value) >= 8 {
			pl.TimeOffsetNS = int64(binary.BigEndian.Uint64(cdr.TimeOffsetMeasurement.Value))
		}
		if cdr.CumulativeRateRatioMeasurement != nil && len(cdr.CumulativeRateRatioMeasurement.Value) >= 4 {
			pl.CumRateRatio = int32(binary.BigEndian.Uint32(cdr.CumulativeRateRatioMeasurement.Value))
			pl.HasRateRatio = true
		}
		r := upf.Report{
			Type:       upf.ReportClockDrift,
			Timestamp:  time.Now(),
			ClockDrift: pl,
		}
		select {
		case p.reports <- r:
			p.log.Infof("§7.4.5.1 Clock Drift Report: domain=%d offset=%dns rateRatio=%v",
				pl.TimeDomain, pl.TimeOffsetNS, pl.HasRateRatio)
		default:
			p.reportsDropped.Add(1)
		}
	}
	resp := &genpfcp.NodeReportResponse{
		NodeID: genpfcp.NodeID{Type: 0, IPv4: p.localNodeIPv4()},
		Cause:  genpfcp.Cause{Value: 1},
	}
	out, err := stripHeader(resp)
	if err != nil {
		return
	}
	_ = p.t.SendResponse(peer, genpfcp.MessageTypeNodeReportResponse, 0,
		hdr.SequenceNumber, out)
}

// decodeMACsDetected parses the §8.2.98 MAC Addresses Detected value:
// count octet + count×6 MAC octets (+ optional C-TAG/S-TAG lengths).
func decodeMACsDetected(v []byte) [][6]byte {
	if len(v) < 1 {
		return nil
	}
	k := int(v[0])
	if len(v) < 1+6*k {
		return nil
	}
	out := make([][6]byte, 0, k)
	for i := 0; i < k; i++ {
		var m [6]byte
		copy(m[:], v[1+6*i:])
		out = append(out, m)
	}
	return out
}
