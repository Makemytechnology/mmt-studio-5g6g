// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SCTP association transition graph — RFC 4960 §4, §9. Every row cites
// the clause it's sourced from so reviewers can check against the
// vendored RFC copy (standards/rfc4960.txt if present) or the IETF
// HTML.
//
// Cascade semantics (TS 38.412 §7):
//
//   When an SCTP association enters FAILED or CLOSED, every NGAP per-UE
//   FSM that was riding on it is driven to RELEASED via EvNGReset. The
//   NGAP table already accepts EvNGReset from every state; this file
//   just fires it for the affected UEs.
package ngap

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/infra/timers"
	gmmfsm "github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/gnbctx"
	ngapfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/fsm"
	sctpfsm "github.com/mmt/mmt-studio-core/nf/amf/ngap/sctpfsm"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/smf/session"
	"github.com/mmt/mmt-studio-core/nf/smf/session/pti"
	"github.com/mmt/mmt-studio-core/oam/fm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var sctpTransitions = []sctpfsm.Transition{
	// RFC 4960 §4 — passive-open path (we're the listener). The kernel
	// handles INIT / INIT-ACK / COOKIE-ECHO / COOKIE-ACK internally;
	// we only see the outcome as SCTP_COMM_UP when the association is
	// ready for DATA.
	{
		From:   sctpfsm.StateClosed,
		Event:  sctpfsm.EvCommUp,
		To:     sctpfsm.StateEstablished,
		Action: actSCTPCommUp,
	},

	// RFC 4960 §9 — graceful close initiated by peer. Peer sent
	// SHUTDOWN; kernel queues any in-flight outbound DATA for
	// retransmit then moves to SHUTDOWN-RECEIVED. We stop emitting
	// new NGAP traffic here but the kernel lets us drain.
	{
		From:   sctpfsm.StateEstablished,
		Event:  sctpfsm.EvShutdownChunkRx,
		To:     sctpfsm.StateShutdownReceived,
		Action: actSCTPShutdownStarted,
	},
	{
		From:   sctpfsm.StateShutdownReceived,
		Event:  sctpfsm.EvShutdownRx,
		To:     sctpfsm.StateClosed,
		Action: actSCTPClosedCascade,
	},

	// RFC 4960 §9.2 — graceful close initiated by us.
	{
		From:   sctpfsm.StateEstablished,
		Event:  sctpfsm.EvShutdownTx,
		To:     sctpfsm.StateShutdownSent,
		Action: actSCTPShutdownStarted,
	},
	{
		From:   sctpfsm.StateShutdownSent,
		Event:  sctpfsm.EvShutdownRx,
		To:     sctpfsm.StateClosed,
		Action: actSCTPClosedCascade,
	},

	// RFC 4960 §5 — association couldn't establish (bad cookie, max
	// init retransmits reached). sac_state = SCTP_CANT_STR_ASSOC.
	{
		From:   sctpfsm.StateClosed,
		Event:  sctpfsm.EvAbort,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},
	{
		From:   sctpfsm.StateCookieWait,
		Event:  sctpfsm.EvAbort,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},
	{
		From:   sctpfsm.StateCookieEchoed,
		Event:  sctpfsm.EvAbort,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},

	// RFC 4960 §6.3.3, §8.2 — peer vanished. sac_state = SCTP_COMM_LOST
	// (fires on either peer-sent ABORT or local asocmaxrxt exhaustion).
	{
		From:   sctpfsm.StateEstablished,
		Event:  sctpfsm.EvCommLost,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},
	{
		From:   sctpfsm.StateEstablished,
		Event:  sctpfsm.EvAbort,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},
	{
		From:   sctpfsm.StateShutdownReceived,
		Event:  sctpfsm.EvCommLost,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},
	{
		From:   sctpfsm.StateShutdownSent,
		Event:  sctpfsm.EvCommLost,
		To:     sctpfsm.StateFailed,
		Action: actSCTPFailedCascade,
	},

	// RFC 4960 §5.1 — restart: the peer re-initialised. Kernel
	// delivers SCTP_RESTART after a fresh INIT on the same verification
	// tag family. We treat it as "connection recycled" — keep FSM in
	// ESTABLISHED but cascade NG-Reset to any UE contexts so they
	// rebuild.
	{
		From:   sctpfsm.StateEstablished,
		Event:  sctpfsm.EvRestart,
		To:     sctpfsm.StateEstablished,
		Action: actSCTPRestart,
	},

	// RFC 6458 §6.1.4 — peer sent OP-ERROR chunks. Observability only.
	{From: sctpfsm.StateEstablished, Event: sctpfsm.EvRemoteError, To: sctpfsm.StateEstablished, Action: actSCTPLog("remote-error")},

	// RFC 6458 §6.1.5 — our outbound DATA couldn't be delivered. Doesn't
	// mean the association died, but operators want to know.
	{From: sctpfsm.StateEstablished, Event: sctpfsm.EvSendFailed, To: sctpfsm.StateEstablished, Action: actSCTPLog("send-failed")},
}

// actSCTPCommUp — association established. Clear any prior "SCTP lost"
// alarm and log.
func actSCTPCommUp(c *sctpfsm.Context) error {
	log := logger.Get("amf.ngap.sctp")
	log.Infof("SCTP COMM_UP %s", c.Key)
	_, _ = fm.Clear("gNB/"+c.Key.GnbIP, fm.CauseLossOfSignal,
		"SCTP association lost", "SCTP COMM_UP received — association re-established")
	return nil
}

// actSCTPShutdownStarted — either side initiated graceful shutdown.
func actSCTPShutdownStarted(c *sctpfsm.Context) error {
	logger.Get("amf.ngap.sctp").Infof("SCTP shutdown started %s reason=%q", c.Key, c.Reason)
	return nil
}

// actSCTPClosedCascade — association closed cleanly; drop every UE FSM
// hanging off this gNB via NGReset so their own state machines clean
// up timers + mirror to DEREGISTERED. Also drops the SCTP FSM entry
// itself — without this, sctpfsm.reg leaked one entry per gNB
// association for the lifetime of the process (no existing caller of
// sctpfsm.Drop covered the terminal-state path).
func actSCTPClosedCascade(c *sctpfsm.Context) error {
	cascadeNGResetForGnb(c.Key.GnbIP, "SCTP_CLOSED")
	sctpfsm.DropPathsForAssoc(c.Key.GnbIP, c.Key.AssocID)
	sctpfsm.Drop(c.Key)
	return nil
}

// actSCTPFailedCascade — association died hard. Same cascade but with
// a fault raised so the operator sees an alarm on the GUI. Drops the
// SCTP FSM entry on terminal transition to StateFailed.
func actSCTPFailedCascade(c *sctpfsm.Context) error {
	log := logger.Get("amf.ngap.sctp")
	log.Warnf("SCTP association failed %s cause=%d reason=%q", c.Key, c.Cause, c.Reason)
	_, _ = fm.Raise(fm.RaiseInput{
		ManagedObject:     "gNB/" + c.Key.GnbIP,
		AlarmType:         fm.AlarmTypeCommunications,
		ProbableCause:     fm.CauseLossOfSignal,
		PerceivedSeverity: fm.SeverityMajor,
		SpecificProblem:   "SCTP association lost",
		AdditionalText:    c.Reason,
	})
	cascadeNGResetForGnb(c.Key.GnbIP, "SCTP_FAILED")
	sctpfsm.DropPathsForAssoc(c.Key.GnbIP, c.Key.AssocID)
	sctpfsm.Drop(c.Key)
	return nil
}

// actSCTPRestart — peer recycled the association. Clean up UE FSMs but
// stay ESTABLISHED (new association will reuse this FSM entry).
func actSCTPRestart(c *sctpfsm.Context) error {
	logger.Get("amf.ngap.sctp").Infof("SCTP RESTART %s — peer re-initialised, cascading NGReset", c.Key)
	cascadeNGResetForGnb(c.Key.GnbIP, "SCTP_RESTART")
	return nil
}

func actSCTPLog(tag string) sctpfsm.Action {
	return func(c *sctpfsm.Context) error {
		logger.Get("amf.ngap.sctp").Infof("[%s] %s event=%s cause=%d", tag, c.Key, c.Event, c.Cause)
		return nil
	}
}

// cascadeNGResetForGnb walks every UE FSM hanging off this gNB and
// tears down NGAP, GMM, and the PDU session(s) at the SMF/UPF.
// Without the GMM side, per-UE NAS retransmit timers (T3550, T3560,
// T3570, etc.) would keep firing for NASMaxRetransmit × Duration
// seconds against a dead association. Without the SMF/UPF side, the
// PFCP session lives on at the UPF (consuming F-TEID / UE-IP /
// session_pool slots) until the next §6.4.1.7 item-(c) duplicate
// detection at re-establish — minutes to forever, depending on
// whether the UE returns.
//
// Spec basis for the immediate release:
//   - TS 24.501 v19.6.0 §5.3.7 "loss of N1 signalling connection
//     ⇒ implicit de-registration" (UE moves to DEREGISTERED with
//     no further signalling).
//   - TS 23.502 v19.7.0 §4.2.2.3.3 Network-initiated Deregistration
//     covers Implicit Deregistration; step 4 defers to §4.2.2.3.2
//     step 2 — "All PDU Sessions ... are released by the AMF
//     sending Nsmf_PDUSession_ReleaseSMContext Request ... for
//     each PDU Session."
//   - TS 23.502 v19.7.0 §4.2.6 step 6a alternative — DEACTIVATE
//     user plane (BUFF) and keep the PDU session alive — applies
//     when a fresh Service Request is expected. After cascade
//     NGReset the UE has no NAS connection, so Service Request
//     cannot reach the AMF; full release is the spec-aligned
//     behaviour, not deactivation.
//   - TS 38.413 §8.7.1.1: a NEW NG Setup superseding a tracked
//     association implies the gNB lost its UE state; full release
//     for those UEs is the only path that keeps SMF/UPF in sync.
//
// PFCP wire shape: N × §7.5.6 Session Deletion (one per UE PDU
// session). DELIBERATELY *not* the bulk-release messages that the
// UPF handler now also dispatches:
//
//   - §7.4.4.5 PFCP Association Release Request (§6.2.8.3) is
//     scoped to the entire CP↔UP association ("the UP function shall
//     delete all the PFCP sessions related to that PFCP association")
//     — using it on gNB SCTP loss would tear down sessions for UEs
//     attached to *other* gNBs sharing the same SMF↔UPF association.
//     Wrong scope. The Association Release path is correct only for
//     SMF graceful shutdown (see nf/smf/upfclient/pfcp_bridge.go.Close).
//
//   - §7.4.6 PFCP Session Set Deletion Request matches by §8.2.61
//     FQ-CSID — and §8.2.61 lists only NF-level variants
//     (SGW-C / PGW-C/SMF / UPF / TWAN / ePDG / MME). There is **no
//     (R)AN/gNB FQ-CSID variant**. Inventing one here would violate
//     the §8.2.61 IE definition. §7.4.6 is correct only for SMF /
//     UPF restart-recovery, not gNB cascade.
//
// So the spec-correct mapping for "gNB lost its UE state" is exactly
// what we do: per-UE implicit-dereg per TS 24.501 §5.3.7 → per-UE
// PDU release per TS 23.502 §4.2.2.3.3 step 4 → §4.2.2.3.2 step 2
// → N × §7.5.6 on the wire. Future readers tempted to "optimise" the
// loop into a single PFCP PDU: don't — both alternatives are spec
// violations, not optimisations. (And per docs/PERFORMANCE.md Run 7, the
// N × §7.5.6 cascade is already ~2 000 sess/s on a laptop; it isn't
// the bottleneck.)
//
// Per-UE cascade order (matters; see releaseUEFromGnb):
//  1. session.ReleaseAll   — PFCP §7.5.6 Session Deletion to UPF.
//  2. NGAP FSM: EvNGReset  — StateReleased (cancels Twait-ICS etc.).
//  3. GMM timer cancel     — T3550/T3560/T3570 + retransmit closures.
//  4. 5GSM PTI release     — symmetric with the regular dereg path.
//  5. GMM FSM registry drop.
func cascadeNGResetForGnb(gnbIP, reason string) {
	log := logger.Get("amf.ngap.sctp")
	ues := uectx.Default.SnapshotForGnb(gnbIP)
	totalReleased := 0
	for _, ue := range ues {
		totalReleased += releaseUEFromGnb(log, gnbIP, ue)
	}
	log.Infof("SCTP cascade: reason=%s gNB=%s dropped %d UE associations (NGAP + GMM) + released %d PDU session(s) (TS 23.502 §4.2.2.3.3)",
		reason, gnbIP, len(ues), totalReleased)
	// Also clear the gNB-level registry mark so future accepts see a
	// fresh state rather than a stale "Connected=true".
	if gnb := gnbctx.Default.GetByIP(gnbIP); gnb != nil {
		gnb.MarkDisconnected()
	}
}

// releaseUEFromGnb runs the per-UE cascade body for one UE on a
// failing/cascaded gNB. Returns the count of PDU sessions §7.5.6-
// released so the caller can sum across UEs. See cascadeNGResetForGnb
// header for the spec basis and why each step is in this order.
func releaseUEFromGnb(log *logger.Logger, gnbIP string, ue *uectx.AmfUeCtx) int {
	// 1. PFCP §7.5.6 Session Deletion per session at the SMF/UPF.
	//    session.ReleaseAll iterates the SMF's per-IMSI session map;
	//    the UPF in turn unregisters the F-TEID / UE-IP and the
	//    §8.2.41 final URR counters land in sacore.log. Wire shape:
	//    one §7.5.6 PDU per PDU session — see header for why this
	//    cannot be collapsed into a single bulk-release PDU.
	released := session.ReleaseAll(ue.IMSI)
	if released > 0 {
		log.WithIMSI(ue.IMSI).Infof(
			"SCTP cascade: released %d PDU session(s) (TS 24.501 §5.3.7 implicit dereg)",
			released)
	}

	// 2. NGAP per-UE FSM teardown — drives the FSM to RELEASED so
	//    Twait-ICS / pdusetup / pdurelease retries unwind.
	k := ngapfsm.Key{GnbKey: gnbIP, AMFUENGAPID: ue.AmfUeNGAPID}
	_ = ngapfsm.Of(k).Fire(&ngapfsm.Context{Key: k, Event: ngapfsm.EvNGReset, Reason: nil})
	ngapfsm.Drop(k)

	// 3. NAS retransmit timers — without this, T3550/T3560/T3570
	//    keep firing against a dead SCTP for NASMaxRetransmit × T.
	timers.M.CancelAllForUE(fmt.Sprintf("%d", ue.AmfUeNGAPID))

	// 4. 5GSM PTI slot release — same cleanup the regular dereg does.
	_ = pti.Default.ReleaseAllForUE(ue.IMSI)

	// 5. GMM FSM registry — release the FSM struct so the package-
	//    level map doesn't grow unbounded across gNB reconnects.
	gmmfsm.Drop(ue)

	return released
}

func init() {
	sctpfsm.SetDefaultTable(sctpTransitions)
}
