// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SMF TSC support (Rel-19):
//
//	TS 23.501 §5.27.2.4 — TSCAI derivation from the TSC Assistance
//	  Container inputs the PCF hands over in the PCC rule
//	  (tscaiInputUl/Dl, TS 29.512 §5.6.2.6):
//	    DL BAT = TSCAC BAT (clock-drift corrected) + CN PDB
//	    UL BAT = TSCAC BAT (clock-drift corrected) + UE-DS-TT residence
//	    Periodicity corrected by the cumulative rateRatio the UPF
//	    reported for the flow's (g)PTP time domain (§5.27.1.12/N4).
//	TS 23.501 §5.7.4 Table 5.7.4-1 NOTE 4/5/6 — static CN PDB per
//	  delay-critical GBR 5QI (82/83→1 ms, 85/86→2 ms, 84→5 ms).
//	TS 23.502 §4.3.2.2.1 step 10a/b — 5GS bridge information request
//	  toward the UPF and the TSN_BRIDGE_INFO report toward the PCF.
package session

import (
	"sync"

	nas "github.com/mmt/nasgen/generated"

	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	upfmgr "github.com/mmt/mmt-studio-core/nf/upf"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// TSCAI is the TSC Assistance Information the SMF sends to NG-RAN for
// one QoS flow direction (TS 23.501 §5.27.2 Table 5.27.2-1; wire form
// TS 38.413 §9.3.1.131 TSCAssistanceInformation).
type TSCAI struct {
	// FlowDirection: "UL" | "DL".
	FlowDirection string `json:"flow_direction"`
	// Periodicity in microseconds (NGAP Periodicity is 0..640000 µs).
	PeriodicityUS uint64 `json:"periodicity_us"`
	// BurstArrivalTimeNS — absolute arrival time at the 5G-AN
	// reference point (DL: RAN ingress, UL: UE egress), nanoseconds
	// in the 5GS clock.
	BurstArrivalTimeNS int64 `json:"burst_arrival_time_ns,omitempty"`
	// SurvivalTimeUS — derived per §5.27.2.4 (count × periodicity or
	// direct time), microseconds.
	SurvivalTimeUS uint64 `json:"survival_time_us,omitempty"`
}

// staticCNPDBns returns the static CN PDB for a delay-critical GBR 5QI
// (TS 23.501 Table 5.7.4-1 NOTE 4/5/6), nanoseconds. Non-TSC 5QIs get
// the NOTE 4 floor of 1 ms.
func staticCNPDBns(fiveQI int) int64 {
	switch fiveQI {
	case 82, 83:
		return 1_000_000 // NOTE 4: 1 ms
	case 85, 86:
		return 2_000_000 // NOTE 5: 2 ms
	case 84:
		return 5_000_000 // NOTE 6: 5 ms
	}
	return 1_000_000
}

// ─── UPF clock drift state (TS 29.244 §7.4.5.1 Clock Drift Report) ───

// clockDrift is the latest UPF-reported offset between the 5GS clock
// and an external (g)PTP time domain (TS 29.244 IEs 209/210).
type clockDrift struct {
	TimeOffsetNS   int64
	CumRateRatio   int32 // (rateRatio-1) × 2^41 per IEEE 802.1AS-2020
	HasRateRatio   bool
}

var (
	clockDriftMu sync.RWMutex
	clockDrifts  = map[uint8]clockDrift{} // by TSN time domain number
)

func init() {
	// Wire the PFCP report ring into the TSC state machines. Both
	// report types are synthesised by the SMF-side PFCP bridge from
	// wire messages (nf/smf/upfclient/pfcp_tsc.go):
	//
	//   ReportClockDrift ← §7.4.5.1 Node Report (Clock Drift Report)
	//   ReportEthMAC     ← §7.5.8.3 Usage Report (Ethernet Traffic
	//                       Information / MAC Addresses Detected)
	upfmgr.RegisterReportHandler(upfmgr.ReportClockDrift, func(r *upfmgr.Report) {
		if r.ClockDrift == nil {
			return
		}
		ApplyClockDriftReport(r.ClockDrift.TimeDomain, r.ClockDrift.TimeOffsetNS,
			r.ClockDrift.CumRateRatio, r.ClockDrift.HasRateRatio)
	})
	upfmgr.RegisterReportHandler(upfmgr.ReportEthMAC, func(r *upfmgr.Report) {
		if r.EthMAC == nil {
			return
		}
		handleDetectedMACs(r.IMSI, r.PDUSessionID, r.EthMAC.MACs)
	})
}

// handleDetectedMACs processes §8.2.98 MAC Addresses Detected: store
// on the session and fire the UE_MAC_CH policy control request
// trigger toward the PCF (TS 29.512 §5.6.3.6) so the TSN AF learns
// about stations behind the DS-TT.
func handleDetectedMACs(imsi string, pduSessionID uint8, macs [][6]byte) {
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return
	}
	log := logger.Get("smf.tsc").WithIMSI(imsi)
	k := smfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID}
	for _, m := range macs {
		mac := formatMAC(m)
		dup := false
		for _, have := range sess.DetectedMACs {
			if have == mac {
				dup = true
				break
			}
		}
		if dup {
			continue
		}
		sess.DetectedMACs = append(sess.DetectedMACs, mac)
		if _, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
			Triggers: []string{"UE_MAC_CH"},
			UeMac:    mac,
		}); err != nil {
			log.Warnf("UE_MAC_CH(%s): %v", mac, err)
			continue
		}
		log.Infof("UE_MAC_CH reported: %s pduSessID=%d (TS 29.512 §5.6.3.6)", mac, pduSessionID)
	}
}

func formatMAC(m [6]byte) string {
	const hexd = "0123456789abcdef"
	out := make([]byte, 0, 17)
	for i, b := range m {
		if i > 0 {
			out = append(out, '-')
		}
		out = append(out, hexd[b>>4], hexd[b&0x0F])
	}
	return string(out)
}

// ApplyClockDriftReport ingests a Clock Drift Report from the UPF
// (N4 Node Report, TS 29.244 §7.4.5.1) so subsequent TSCAI derivations
// use the corrected time base (TS 23.501 §5.27.2.4).
func ApplyClockDriftReport(timeDomain uint8, timeOffsetNS int64, cumRateRatio int32, hasRateRatio bool) {
	clockDriftMu.Lock()
	clockDrifts[timeDomain] = clockDrift{
		TimeOffsetNS: timeOffsetNS,
		CumRateRatio: cumRateRatio,
		HasRateRatio: hasRateRatio,
	}
	clockDriftMu.Unlock()
	logger.Get("smf.tsc").Infof("clock drift: domain=%d offset=%dns rateRatio=%v (TS 29.244 §7.4.5.1)",
		timeDomain, timeOffsetNS, hasRateRatio)
}

func driftFor(timeDomain uint64) (clockDrift, bool) {
	clockDriftMu.RLock()
	defer clockDriftMu.RUnlock()
	d, ok := clockDrifts[uint8(timeDomain)]
	return d, ok
}

// rateRatioFactor converts the IEEE 802.1AS cumulative rateRatio
// measurement back to a multiplicative factor.
func (d clockDrift) rateRatioFactor() float64 {
	return 1.0 + float64(d.CumRateRatio)/(1<<41)
}

// DeriveTSCAI computes the NG-RAN TSCAI for a TSC PCC rule per
// TS 23.501 §5.27.2.4. residenceNS is the session's UE-DS-TT residence
// time (0 = UE didn't provide one — the UL BAT then omits the shift,
// which §5.27.2.4 leaves implementation-specific).
func DeriveTSCAI(rule *pcf.PCCRule, residenceNS uint64) (ul, dl *TSCAI) {
	derive := func(in *pcf.TscaiInput, dir string) *TSCAI {
		if in == nil {
			return nil
		}
		out := &TSCAI{FlowDirection: dir, PeriodicityUS: in.PeriodicityUS}
		drift, haveDrift := driftFor(rule.TscaiTimeDom)

		// Periodicity: correct with the cumulative rateRatio when the
		// UPF has reported one for the rule's time domain.
		if haveDrift && drift.HasRateRatio && out.PeriodicityUS > 0 {
			out.PeriodicityUS = uint64(float64(out.PeriodicityUS) * drift.rateRatioFactor())
		}

		// Burst Arrival Time: shift from the external time base into
		// the 5GS clock, then advance to the 5G-AN reference point.
		if in.BurstArrivalTimeNS != 0 {
			bat := in.BurstArrivalTimeNS
			if haveDrift {
				bat += drift.TimeOffsetNS
			}
			if dir == "DL" {
				// TSCAC references the NW-TT ingress; the RAN sees the
				// burst one CN PDB later (§5.27.2.4: "BAT provided to
				// NG-RAN = burst arrival time at UPF + CN PDB").
				bat += staticCNPDBns(rule.FiveQI)
			} else {
				// TSCAC references the DS-TT ingress; the UE egress
				// adds the UE-DS-TT residence time.
				bat += int64(residenceNS)
			}
			out.BurstArrivalTimeNS = bat
		}

		// Survival time (§5.27.2.4): message count → count ×
		// periodicity; direct time → rateRatio-corrected.
		switch {
		case in.SurTimeInNumMsg > 0 && out.PeriodicityUS > 0:
			out.SurvivalTimeUS = uint64(in.SurTimeInNumMsg) * out.PeriodicityUS
		case in.SurTimeInTimeUS > 0:
			st := in.SurTimeInTimeUS
			if haveDrift && drift.HasRateRatio {
				st = uint64(float64(st) * drift.rateRatioFactor())
			}
			out.SurvivalTimeUS = st
		}
		return out
	}
	return derive(rule.TscaiInputUl, "UL"), derive(rule.TscaiInputDl, "DL")
}

// ReportBATOffset reports the NG-RAN's Burst Arrival Time offset for a
// TSC QoS flow to the PCF as a BAT_OFFSET_INFO event (TS 23.501
// §5.27.2.5 EnTSCAC; TS 29.512 BAT offset feedback). The PCF fans it
// out to the TSN AF, which records it on the CNC stream. offsetNS in
// nanoseconds; adjPeriodicityUS 0 = not adjusted. Called by the AMF's
// PDU Session Resource Notify handler.
func ReportBATOffset(imsi string, pduSessionID uint8, qfi uint8, offsetNS int64, adjPeriodicityUS uint64) {
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return
	}
	// Reverse-map QFI → PCC rule id so the TSN AF can match its stream.
	ruleID := ""
	for name, q := range sess.QFIByRule {
		if q == qfi {
			ruleID = name
			break
		}
	}
	if ruleID == "" {
		return // not a policy-managed flow
	}
	k := smfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID}
	if _, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
		BatOffsets: []pcf.BatOffsetInfo{{
			RuleID:                ruleID,
			BatOffsetNS:           offsetNS,
			AdjustedPeriodicityUS: adjPeriodicityUS,
		}},
	}); err != nil {
		logger.Get("smf.tsc").WithIMSI(imsi).Warnf("BAT_OFFSET_INFO report: %v", err)
		return
	}
	logger.Get("smf.tsc").WithIMSI(imsi).Infof(
		"BAT_OFFSET_INFO reported rule=%s qfi=%d offset=%dns adjPeriodicity=%dus (TS 23.501 §5.27.2.5)",
		ruleID, qfi, offsetNS, adjPeriodicityUS)
}

// NGAPModifyTSCByIMSI is the SMF→AMF hook that ships the N2 leg of a
// network-initiated modification: a PDU Session Resource Modify Request
// carrying TSCTrafficCharacteristics for the session's TSC QoS flows
// (TS 23.502 §4.3.3 step 3). Set by nf/amf/hooks.go. nil = N2 not wired
// (the N1 NAS leg still ships via DLNASByIMSI). Kept as a hook so
// nf/smf/session doesn't import the AMF NGAP packages (acyclic graph).
var NGAPModifyTSCByIMSI func(imsi string, pduSessionID uint8, tscai map[uint8][2]*TSCAI) error

// NotifyTSCSessionReleased tells the TSC user-plane listeners (TSN AF)
// that the Ethernet PDU session — and with it the DS-TT port of the
// 5GS bridge — is gone (TS 23.501 §5.28.1: the bridge exists per PDU
// session). No-op for non-Ethernet sessions.
func NotifyTSCSessionReleased(sess *Session) {
	if sess == nil || sess.PDUType != nas.PDUSessionTypeEthernet {
		return
	}
	pcf.NotifyTscUplaneEvent(pcf.TscUplaneEvent{
		IMSI:            sess.IMSI,
		PDUSessionID:    sess.PDUSessionID,
		DNN:             sess.DNN,
		SessionReleased: true,
	})
}

// RefreshSessionTSCAI recomputes the per-QFI TSCAI table for a session
// from the current policy decision. Called on establishment and on
// every UpdateNotify so the AMF/NGAP path always reads fresh values.
func RefreshSessionTSCAI(sess *Session, rules []pcf.PCCRule) {
	table := map[uint8][2]*TSCAI{}
	for i := range rules {
		r := &rules[i]
		if r.TscaiInputUl == nil && r.TscaiInputDl == nil {
			continue
		}
		qfi, ok := sess.QFIByRule[r.ServiceName]
		if !ok {
			continue
		}
		ul, dl := DeriveTSCAI(r, sess.UEDSTTResidenceNS)
		table[qfi] = [2]*TSCAI{ul, dl}
	}
	sess.TSCAIByQFI = table
	if len(table) > 0 {
		logger.Get("smf.tsc").WithIMSI(sess.IMSI).Infof(
			"TSCAI derived for %d QoS flow(s) pduSessID=%d (TS 23.501 §5.27.2.4)",
			len(table), sess.PDUSessionID)
	}
}

// ─── Bridge information reporting (TS 23.502 §4.3.2.2.1) ───

// reportBridgeInfoToPCF ships the 5GS user plane node report to the
// PCF on the TSN_BRIDGE_INFO trigger (TS 29.512 §4.2.4.15): bridge ID +
// DS-TT port from the UPF's Created Bridge Info (N4), DS-TT MAC +
// residence time + PMIC from the UE (N1).
func reportBridgeInfoToPCF(sess *Session) {
	if sess.TSCBridgeID == 0 && sess.DSTTMAC == "" && len(sess.PendingPMIC) == 0 {
		return
	}
	log := logger.Get("smf.tsc").WithIMSI(sess.IMSI)
	upd := smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{smpolicy.TriggerTsnBridgeInfo},
		TsnBridgeInfo: &pcf.TsnBridgeInfo{
			BridgeID:        sess.TSCBridgeID,
			DsttAddr:        sess.DSTTMAC,
			DsttPortNum:     sess.DSTTPortNum,
			DsttResidTimeNS: sess.UEDSTTResidenceNS,
			NwttPortNums:    sess.NWTTPortNums,
		},
	}
	if len(sess.PendingPMIC) > 0 {
		upd.TsnPortManContDstt = &pcf.PortManCont{
			Container: sess.PendingPMIC,
			PortNum:   sess.DSTTPortNum,
		}
		sess.PendingPMIC = nil
	}
	k := smfsm.Key{IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID}
	if _, err := smpolicy.Update(k, upd); err != nil {
		log.Warnf("TSN_BRIDGE_INFO report: %v", err)
		return
	}
	log.Infof("TSN_BRIDGE_INFO reported: bridge=%s dsttPort=%d mac=%s residence=%dns (TS 29.512 §4.2.4.15)",
		pcf.FormatBridgeID(sess.TSCBridgeID), sess.DSTTPortNum, sess.DSTTMAC, sess.UEDSTTResidenceNS)
}

// requestAndReportBridgeInfo drives TS 23.502 §4.3.2.2.1 steps 10a/10b
// after the PFCP establishment: read the Created Bridge Info the UPF
// returned, store it on the session and report to the PCF.
func requestAndReportBridgeInfo(sess *Session) {
	dstt, nwtts, nodeID, ok := upfmgr.Default.BridgeInfo(sess.IMSI, sess.PDUSessionID)
	if !ok {
		return
	}
	sess.TSCBridgeID = nodeID
	sess.DSTTPortNum = dstt
	sess.NWTTPortNums = nwtts
	reportBridgeInfoToPCF(sess)
}

// HandleModificationCompletePMIC forwards a PMIC the UE returned in a
// PDU SESSION MODIFICATION COMPLETE (TS 24.501 §8.3.10, IE 0x74) to
// the PCF → TSN AF/TSCTSF (TS 23.501 §5.28.3.2 DS-TT→AF path).
func HandleModificationCompletePMIC(imsi string, pduSessionID uint8, pmic []byte) {
	if len(pmic) == 0 {
		return
	}
	sess := Default.Get(imsi, pduSessionID)
	if sess == nil {
		return
	}
	k := smfsm.Key{IMSI: imsi, PDUSessionID: pduSessionID}
	upd := smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{smpolicy.TriggerTsnBridgeInfo},
		TsnPortManContDstt: &pcf.PortManCont{
			Container: pmic,
			PortNum:   sess.DSTTPortNum,
		},
	}
	if _, err := smpolicy.Update(k, upd); err != nil {
		logger.Get("smf.tsc").WithIMSI(imsi).Warnf("PMIC uplink report: %v", err)
		return
	}
	logger.Get("smf.tsc").WithIMSI(imsi).Infof(
		"DS-TT PMIC (%d B) forwarded to PCF (TS 23.501 §5.28.3.2)", len(pmic))
}

// forwardTscManagementToUserPlane delivers AF-originated NW-TT PMICs /
// UMIC over N4 TSC Management Information (TS 29.244 IE 199) and
// relays any NW-TT replies back to the PCF (IE 200 → TSN_BRIDGE_INFO).
func forwardTscManagementToUserPlane(sess *Session, nwtts []pcf.PortManCont, umic []byte) {
	log := logger.Get("smf.tsc").WithIMSI(sess.IMSI)
	var entries []upfmgr.TSCMgmtEntry
	for _, pm := range nwtts {
		entries = append(entries, upfmgr.TSCMgmtEntry{PMIC: pm.Container, NWTTPort: pm.PortNum})
	}
	if len(umic) > 0 {
		entries = append(entries, upfmgr.TSCMgmtEntry{UMIC: umic})
	}
	if len(entries) == 0 {
		return
	}
	replies, err := upfmgr.Default.SendTSCManagementInformation(sess.IMSI, sess.PDUSessionID, entries)
	if err != nil {
		log.Warnf("N4 TSC Management Information: %v", err)
		return
	}
	log.Infof("N4 TSC Management Information sent: %d container(s), %d replied (TS 29.244 §7.5.4.18)",
		len(entries), len(replies))
	if len(replies) == 0 {
		return
	}
	// NW-TT replies (MANAGE ... COMPLETE / NOTIFY) go back to the AF
	// via the PCF, same trigger as the discovery path.
	upd := smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{smpolicy.TriggerTsnBridgeInfo},
	}
	for _, r := range replies {
		if len(r.UMIC) > 0 {
			upd.TsnBridgeManCont = r.UMIC
		}
		if len(r.PMIC) > 0 {
			upd.TsnPortManContNwtts = append(upd.TsnPortManContNwtts,
				pcf.PortManCont{Container: r.PMIC, PortNum: r.NWTTPort})
		}
	}
	k := smfsm.Key{IMSI: sess.IMSI, PDUSessionID: sess.PDUSessionID}
	if _, err := smpolicy.Update(k, upd); err != nil {
		log.Warnf("NW-TT reply report: %v", err)
	}
}
