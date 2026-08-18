// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// UP-function side of the Rel-19 node-level TSC reporting:
//
//	TS 29.244 §7.4.4.1 — the CP function arms Clock Drift Control
//	  Information at Association Setup (parsed in
//	  handler_association.go into Handler.clockDriftArmed);
//	§7.4.5.1 — the UP function answers with PFCP Node Report Request
//	  carrying Clock Drift Report(s) (Node Report Type CKDR, IE 205:
//	  Time Domain Number 206 + Time Offset Measurement 209 +
//	  Cumulative rateRatio Measurement 210);
//	§7.5.8.3 — per-session Ethernet Traffic Information (MAC
//	  Addresses Detected, §8.2.98) rides a Session Report Request
//	  Usage Report.
package pfcp

import (
	"encoding/binary"
	"fmt"
	"net"

	genpfcp "github.com/mmt/pfcpgen/generated"
)

// ReportClockDrift sends a §7.4.5.1 Node Report Request with one Clock
// Drift Report to the associated CP function. Driven by the studio's
// clock-domain panel / TSCTSF when the external (g)PTP time base moves
// against the 5GS clock (TS 23.501 §5.27.2.4 feeds on these).
func (h *Handler) ReportClockDrift(timeDomain uint8, timeOffsetNS int64, cumRateRatio int32, hasRateRatio bool) error {
	h.mu.Lock()
	armed, peer := h.clockDriftArmed, h.assocPeer
	h.mu.Unlock()
	if !armed || peer == nil {
		return fmt.Errorf("nwtt: clock drift reporting not armed by CP (no §7.4.4.1 Clock Drift Control Information)")
	}

	off := make([]byte, 8)
	binary.BigEndian.PutUint64(off, uint64(timeOffsetNS))
	cdr := genpfcp.ClockDriftReport{
		TSNTimeDomainNumber:   genpfcp.TSNTimeDomainNumber{Value: []byte{timeDomain}},
		TimeOffsetMeasurement: &genpfcp.TimeOffsetMeasurement{Value: off},
	}
	if hasRateRatio {
		rr := make([]byte, 4)
		binary.BigEndian.PutUint32(rr, uint32(cumRateRatio))
		cdr.CumulativeRateRatioMeasurement = &genpfcp.CumulativeRateRatioMeasurement{Value: rr}
	}
	req := &genpfcp.NodeReportRequest{
		NodeID:           genpfcp.NodeID{Type: 0, IPv4: net.ParseIP("127.0.0.1").To4()},
		NodeReportType:   genpfcp.NodeReportType{Value: []byte{0x04}}, // CKDR (§8.2.69 bit 3)
		ClockDriftReport: []genpfcp.ClockDriftReport{cdr},
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("node report encode: %w", err)
	}
	// Node-level message: no SEID (§7.2.2.4.2).
	respBytes, err := h.t.SendRequest(peer, EncodedMessage{
		MsgType: genpfcp.MessageTypeNodeReportRequest,
		IEs:     payload,
	})
	if err != nil {
		return fmt.Errorf("node report send: %w", err)
	}
	var resp genpfcp.NodeReportResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("node report response decode: %w", err)
	}
	if resp.Cause.Value != 1 {
		return fmt.Errorf("node report rejected: cause=%d", resp.Cause.Value)
	}
	h.log.Infof("§7.4.5.1 Clock Drift Report sent: domain=%d offset=%dns rateRatio=%v",
		timeDomain, timeOffsetNS, hasRateRatio)
	return nil
}

// reportDetectedMACs ships §8.2.98 MAC Addresses Detected to the CP in
// a §7.5.8.3 Session Report Request Usage Report (trigger MACAR).
// Runs in its own goroutine — SendRequest blocks on the response and
// must never run on the transport's read loop.
func (h *Handler) reportDetectedMACs(imsi string, pduSessionID uint8, macs [][6]byte) {
	h.mu.Lock()
	sess := h.byIMSI[imsiPduKey{imsi, pduSessionID}]
	h.mu.Unlock()
	if sess == nil || len(macs) == 0 {
		return
	}
	// §8.2.98 value: count + count×6 MAC octets + zero-length C-TAG
	// and S-TAG fields.
	val := []byte{byte(len(macs))}
	for _, m := range macs {
		val = append(val, m[:]...)
	}
	val = append(val, 0x00, 0x00) // C-TAG len, S-TAG len

	req := &genpfcp.SessionReportRequest{
		SEID:       sess.CPSEID,
		ReportType: genpfcp.ReportType{USAR: 1},
		UsageReport: []genpfcp.UsageReportSessionReportRequest{{
			URRID: genpfcp.URRID{Value: 0xFFFFFFFD}, // reserved: Ethernet reporting slot
			URSEQN: genpfcp.URSEQN{Value: func() []byte {
				b := make([]byte, 4)
				binary.BigEndian.PutUint32(b, uint32(h.nextSEID.Add(1)))
				return b
			}()},
			UsageReportTrigger: genpfcp.UsageReportTrigger{Flags: 0}, // MACAR rides octet 7; flags model is 16-bit — event carried by ETI presence
			EthernetTrafficInformation: &genpfcp.EthernetTrafficInformation{
				MACAddressesDetected: []genpfcp.MACAddressesDetected{{Value: val}},
			},
		}},
	}
	payload, err := stripHeader(req)
	if err != nil {
		h.log.Warnf("MAC report encode: %v", err)
		return
	}
	go func(peer *net.UDPAddr, seid uint64) {
		if _, err := h.t.SendRequest(peer, EncodedMessage{
			MsgType: genpfcp.MessageTypeSessionReportRequest,
			SEID:    seid,
			IEs:     payload,
		}); err != nil {
			h.log.Warnf("§7.5.8.3 MAC report send: %v", err)
			return
		}
		h.log.WithIMSI(imsi).Infof("§8.2.98 MAC Addresses Detected reported: %d MAC(s) pduSessID=%d",
			len(macs), pduSessionID)
	}(sess.Peer, sess.CPSEID)
}
