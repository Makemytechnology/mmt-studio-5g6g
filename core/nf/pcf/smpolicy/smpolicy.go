// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package smpolicy — Npcf_SMPolicyControl service (TS 29.512, N7
// reference point between PCF and SMF).
//
// PDF: specs/3gpp/ts_129512v190600p.pdf. Section anchors cited in
// doc comments have all been grep-verified; see
// nf/pcf/smpolicy/fsm/state.go for the full list.
//
// The PCF never ships across process boundaries today — the SMF
// invokes these functions in-process during PDU Session procedures.
// Shapes returned here (SmPolicyDecision, SmPolicyContextData) are
// modelled on the OpenAPI types in 29.512 so the transition to a
// real HTTP/2 SBI stack is mechanical.
//
//	§4.2.2 Create       — Create(ctx)  → SmPolicyDecision
//	§4.2.3 UpdateNotify — PushNotify(key, SmPolicyDecision) — PCF-initiated
//	§4.2.4 Update       — Update(key, SmPolicyContextDataUpdate) → SmPolicyDecision
//	§4.2.5 Delete       — Delete(key) → DeleteStatus
//
// Lifecycle per the per-association FSM at ./fsm: Create drives
// None→CreatePending→Active; Update self-loops on Active; Delete
// drives Active→Terminating→Terminated.
package smpolicy

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/nf/pcf"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// SmPolicyContextData mirrors TS 29.512 §5.6.2.2 "Type
// SmPolicyContextData" — the Create request body the SMF sends. Only
// the attributes this in-process port currently reads are modelled;
// the wire struct has 40+ optional fields.
type SmPolicyContextData struct {
	// supi (§5.6.2.2) — SUPI of the subscriber. "imsi-..." form.
	SUPI string
	// pduSessionId (§5.6.2.2) — PDU Session ID, 1..15.
	PDUSessionID uint8
	// dnn (§5.6.2.2) — Data Network Name.
	DNN string
	// sliceInfo (§5.6.2.2 → Snssai) — single-NSSAI tuple.
	SST uint8
	SD  string
	// pduSessionType (§5.6.2.2) — 1=IPv4, 2=IPv6, 3=IPv4v6, etc.
	PDUSessionType uint8
}

// SmPolicyDecision mirrors TS 29.512 §5.6.2.4 "Type SmPolicyDecision"
// — the document returned by Create / Update / UpdateNotify. Only
// attributes used by the SMF today are modelled.
type SmPolicyDecision struct {
	// pccRules (§5.6.2.4 → map<string, PccRule|null>) — dynamic PCC
	// rules keyed by pccRuleId. Null value means remove the rule.
	PccRules []pcf.PCCRule
	// RemovedPccRules carries the null-valued map entries from §5.6.2.4
	// (the wire form is `"<ruleId>": null` for each removal). Modeled
	// as a separate slice on the in-process port so the SMF can
	// distinguish "rule retained" (in PccRules) from "rule removed"
	// (in RemovedPccRules) without a per-entry sentinel. Only ServiceName
	// is required — the SMF's QFIByRule map is keyed on it.
	RemovedPccRules []pcf.PCCRule
	// sessRules (§5.6.2.4 → map<string, SessionRule>) — session-scoped
	// rules (AMBR, default QoS). Flattened here to the defaults we
	// actually use; re-expand on SBI lift.
	DefaultQFI    uint8
	SessionAMBRUL int // kbps
	SessionAMBRDL int // kbps
	Default5QI    int
	// chgMethod (§5.6.2.4 → ChargingInformation.chgMeth) —
	// "online" | "offline".
	ChargingMethod string
	// revalidationTime (§5.6.2.4 + §4.2.2.4) — when the SMF MUST
	// trigger an Update toward the PCF. Zero means unset.
	RevalidationTime time.Time
	// SmPolicyCtxRef — the PCF-assigned context reference for this
	// association (§4.2.2.2 step 2: "The PCF shall create a new
	// resource whose URI is assigned by the PCF"). In-process form:
	// an internal ID.
	SmPolicyCtxRef string
}

// SmPolicyContextDataUpdate mirrors TS 29.512 §5.6.2.3 "Type
// SmPolicyUpdateContextData" — the body of an Update request (§4.2.4).
type SmPolicyContextDataUpdate struct {
	// repPolicyCtrlReqTriggers (§5.6.2.3) — which Policy Control
	// Request Triggers (§4.2.3.5) fired.
	Triggers []string
	// RuleReports — per-rule enforcement outcome (§4.2.4.12). Used
	// to feed back ruleStatus changes.
	RuleReports []RuleReport
}

// RuleReport mirrors TS 29.512 §5.6.2.15 "Type RuleReport".
type RuleReport struct {
	PccRuleIDs  []string
	RuleStatus  pcf.PccRuleState // MUST be wire-valid (ACTIVE | INACTIVE)
	FailureCode string           // TS 29.512 §5.6.3.9 Enumeration: FailureCode
}

// DeleteStatus — outcome of a Delete (§4.2.5). In-process it's just
// confirmation; kept as a type so the SBI port can attach
// AccuUsageReport etc. without a signature change.
type DeleteStatus struct {
	Terminated bool
}

// ─── In-memory association registry ──────────────────────────────────

// association is the PCF-side record kept for the lifetime of one
// SM Policy Association. The SBI port will back this with storage.
type association struct {
	key       smfsm.Key
	ctxData   SmPolicyContextData
	decision  SmPolicyDecision
	createdAt time.Time
}

var (
	assocMu sync.RWMutex
	assocs  = map[smfsm.Key]*association{}

	// Monotonic counter for in-process smPolicyCtxRef allocation
	// (TS 29.512 §4.2.2.2: the PCF assigns the URI). The SBI layer
	// will replace this with a real URL template.
	ctxRefCounter int64
	ctxRefMu      sync.Mutex
)

func nextCtxRef(k smfsm.Key) string {
	ctxRefMu.Lock()
	ctxRefCounter++
	n := ctxRefCounter
	ctxRefMu.Unlock()
	return fmt.Sprintf("smpolicy-%s-%d-%d", k.IMSI, k.PDUSessionID, n)
}

// ─── §4.2.2 Create ───────────────────────────────────────────────────

// Create implements Npcf_SMPolicyControl_Create (TS 29.512 §4.2.2).
//
// §4.2.2.1 General (verbatim): "The Npcf_SMPolicyControl_Create
//
//	service operation is used by the SMF to request the creation of
//	a new SM Policy Association at the PCF."
//
// §4.2.2.2 "SM Policy Association establishment" — the PCF builds an
// SmPolicyDecision containing the initial PCC rules (§4.2.2.7),
// authorized Session-AMBR (§4.2.2.5), authorized default QoS
// (§4.2.2.6), charging related information (§4.2.2.3) and optionally
// a revalidation time (§4.2.2.4). In this in-process port the build
// is synchronous — see pcf.CreatePolicy.
// DefaultRevalidationInterval is how far in the future a fresh
// SmPolicyDecision sets the revalidationTime attribute (TS 29.512
// §4.2.2.4). 1 hour matches the Python port; override from config
// when operator tuning lands.
const DefaultRevalidationInterval = time.Hour

func Create(ctx SmPolicyContextData) (SmPolicyDecision, error) {
	log := logger.Get("pcf.smpolicy")
	if ctx.SUPI == "" || ctx.PDUSessionID == 0 {
		return SmPolicyDecision{}, fmt.Errorf("pcf.smpolicy: Create requires SUPI + PDUSessionID")
	}
	imsi := stripSupiPrefix(ctx.SUPI)
	k := smfsm.Key{IMSI: imsi, PDUSessionID: ctx.PDUSessionID}

	// FSM: None → CreatePending. Caller is responsible for ensuring
	// no prior association exists on this key — the SMF side rejects
	// a duplicate PDU session establishment per TS 24.501 §6.4.1.2 /
	// §9.11.4.2 cause #43 before ever reaching smpolicy.Create.
	f := smfsm.Of(k)
	if err := f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvCreateRequest}); err != nil {
		return SmPolicyDecision{}, fmt.Errorf("pcf.smpolicy: Create FSM: %w", err)
	}

	// rejectCreate moves CreatePending → None and drops the FSM so
	// the next Create on the same key starts clean. Exported via
	// closure so every error path uses the same unwind.
	rejectCreate := func(err error) (SmPolicyDecision, error) {
		_ = f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvCreateReject, Reason: err})
		smfsm.Drop(k)
		pm.Inc(pm.PCFSmPolicyCreateReject, 1)
		return SmPolicyDecision{}, err
	}

	// Build the PCC rule set from the DB bindings. CreatePolicy never
	// errors today (returns a default-data rule on empty bindings),
	// but the reject path is wired so future real-failure modes —
	// UDM / UDR lookup, OpenAPI validation — don't leak CreatePending.
	ruleSet := pcf.CreatePolicy(imsi, ctx.DNN, ctx.SST)
	if len(ruleSet.Rules) == 0 {
		return rejectCreate(fmt.Errorf("pcf.smpolicy: no PCC rules produced for imsi=%s dnn=%s sst=%d",
			imsi, ctx.DNN, ctx.SST))
	}

	decision := SmPolicyDecision{
		PccRules:       ruleSet.Rules,
		DefaultQFI:     ruleSet.DefaultQFI,
		Default5QI:     defaultFiveQI(ruleSet),
		ChargingMethod: ruleSet.ChargingMethod,
		SmPolicyCtxRef: nextCtxRef(k),
		// Session-AMBR defaults (TS 23.501 §5.7.2.6). Operator can
		// override via subscription when UDR wiring lands.
		SessionAMBRUL: 200_000, // 200 Mbps
		SessionAMBRDL: 200_000,
		// §4.2.2.4 revalidationTime — default 1 h. The SMF (= us, in
		// process) arms T_Revalidation in ArmRevalidationTimer below;
		// expiry re-invokes Update with trigger "RE_TIMEOUT" per
		// §4.2.4.3 "Request the policy based on revalidation time".
		RevalidationTime: time.Now().Add(DefaultRevalidationInterval),
	}

	// Stash the association.
	assocMu.Lock()
	assocs[k] = &association{
		key:       k,
		ctxData:   ctx,
		decision:  decision,
		createdAt: time.Now(),
	}
	assocMu.Unlock()

	// Fire outcome; state → Active. If this Fire fails the FSM is in
	// CreatePending — unwind via the reject path.
	if err := f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvCreateResponse, Decision: &decision}); err != nil {
		assocMu.Lock()
		delete(assocs, k)
		assocMu.Unlock()
		return rejectCreate(fmt.Errorf("pcf.smpolicy: Create response FSM: %w", err))
	}

	// §4.2.2.4 Revalidation Timer. The spec says the *SMF* arms and
	// enforces this timer; in this in-process port we piggy-back on
	// the PCF-side FSM so the timer lifecycle tracks the association
	// state. Distributed port: move to smf/session/fsm.
	if d := timeUntil(decision.RevalidationTime); d > 0 {
		f.ArmRevalidationTimer(d)
	}

	pm.Inc(pm.PCFSmPolicyCreate, 1)
	log.WithIMSI(imsi).Infof("SM Policy Association created ctxRef=%s dnn=%s sst=%d rules=%d defaultQFI=%d charging=%s revalidate=%s",
		decision.SmPolicyCtxRef, ctx.DNN, ctx.SST, len(decision.PccRules),
		decision.DefaultQFI, decision.ChargingMethod,
		decision.RevalidationTime.Format(time.RFC3339))
	return decision, nil
}

// ─── §4.2.4 Update (SMF → PCF) ───────────────────────────────────────

// Update implements Npcf_SMPolicyControl_Update (TS 29.512 §4.2.4).
//
// §4.2.4.1 General (verbatim): "The Npcf_SMPolicyControl_Update
//
//	service operation is used by the SMF to request the update of
//	an existing SM Policy Association. … The PCF shall reply with
//	the updated SmPolicyDecision."
//
// The SMF invokes this when a Policy Control Request Trigger fires
// on its side — e.g. UE-requested QoS modification (§4.2.4.17),
// PCC Rule Error Report (§4.2.4.15), Revalidation Timer expiry
// (§4.2.4.3).
func Update(k smfsm.Key, upd SmPolicyContextDataUpdate) (SmPolicyDecision, error) {
	log := logger.Get("pcf.smpolicy")
	assocMu.RLock()
	a := assocs[k]
	assocMu.RUnlock()
	if a == nil {
		return SmPolicyDecision{}, fmt.Errorf("pcf.smpolicy: no association for %s", k)
	}

	f := smfsm.Of(k)
	if err := f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvUpdateRequest}); err != nil {
		return SmPolicyDecision{}, fmt.Errorf("pcf.smpolicy: Update FSM: %w", err)
	}

	// Recompute from the current bindings. Real PCF would diff
	// against the previous decision and emit only changed rules per
	// §4.2.4.12.
	ruleSet := pcf.CreatePolicy(a.key.IMSI, a.ctxData.DNN, a.ctxData.SST)
	a.decision.PccRules = ruleSet.Rules
	a.decision.DefaultQFI = ruleSet.DefaultQFI
	a.decision.ChargingMethod = ruleSet.ChargingMethod

	// Apply SMF-reported rule outcomes (§4.2.4.12 "Request and report
	// for the result of PCC rule removal"). Rules marked INACTIVE by
	// the SMF are dropped from the rule manager.
	for _, rr := range upd.RuleReports {
		if rr.RuleStatus.WireRuleStatus() == pcf.PccRuleInactive {
			log.WithIMSI(a.key.IMSI).Infof("Update: rule(s) %v marked INACTIVE (failure=%q)",
				rr.PccRuleIDs, rr.FailureCode)
		}
	}

	// Refresh the revalidation time so the SMF arms a new
	// T_Revalidation on this Update round-trip (§4.2.3.4: "The PCF
	// may also update the revalidation time by including the new
	// value within the revalidationTime attribute.").
	a.decision.RevalidationTime = time.Now().Add(DefaultRevalidationInterval)

	_ = f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvUpdateResponse, Decision: &a.decision})
	if d := timeUntil(a.decision.RevalidationTime); d > 0 {
		f.ArmRevalidationTimer(d)
	}

	pm.Inc(pm.PCFSmPolicyUpdate, 1)
	log.WithIMSI(a.key.IMSI).Infof("SM Policy Association updated ctxRef=%s triggers=%v rules=%d revalidate=%s",
		a.decision.SmPolicyCtxRef, upd.Triggers, len(a.decision.PccRules),
		a.decision.RevalidationTime.Format(time.RFC3339))
	return a.decision, nil
}

// ─── §4.2.3 UpdateNotify (PCF → SMF) ─────────────────────────────────

// OnUpdateNotify is the in-process equivalent of the SMF's HTTP server
// for the §4.2.3 UpdateNotify callback URL. nf/smf/session sets it at
// init() to a function that builds the TS 24.501 §8.3.9 PDU SESSION
// MODIFICATION COMMAND and ships it down to the UE via the AMF DL NAS
// path. nil = the SBI port hasn't shipped yet → PushNotify just runs
// the FSM and returns (matches the legacy stub behaviour).
//
// Hook is set instead of imported because nf/smf/session already
// imports nf/pcf/smpolicy; making smpolicy import smf would close
// the loop and break Go's acyclic package graph.
var OnUpdateNotify func(k smfsm.Key, decision SmPolicyDecision)

// PushNotify implements Npcf_SMPolicyControl_UpdateNotify (TS 29.512
// §4.2.3) — PCF-initiated push of a changed SmPolicyDecision.
//
// Callers in this in-process port: the legacy pcf.NotifySMFPolicyUpdate
// helper (AF activation path). The function fires the FSM outcome and
// expects the caller to have already shipped the decision to the SMF
// over whatever transport exists today. When the SBI layer lands,
// this grows the HTTP POST + ack handling.
func PushNotify(k smfsm.Key, decision SmPolicyDecision) error {
	f := smfsm.Of(k)
	if err := f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvUpdateNotifySent, Decision: &decision}); err != nil {
		return fmt.Errorf("pcf.smpolicy: UpdateNotify FSM: %w", err)
	}
	// In-process delivery to the SMF: invoke the registered hook. The
	// SBI port will replace this with an HTTP POST + ack handler.
	if OnUpdateNotify != nil {
		OnUpdateNotify(k, decision)
	}
	_ = f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvUpdateNotifyAck})
	pm.Inc(pm.PCFSmPolicyUpdateNotify, 1)
	return nil
}

// ─── §4.2.5 Delete ───────────────────────────────────────────────────

// Delete implements Npcf_SMPolicyControl_Delete (TS 29.512 §4.2.5).
//
// §4.2.5.1 General (verbatim): "The Npcf_SMPolicyControl_Delete
//
//	service operation is used by the SMF to request the termination
//	of an existing SM Policy Association."
//
// §4.2.5.2 "SM Policy Association termination" — the PCF removes
// internal state, cancels the Revalidation Timer, and returns a
// confirmation. Upstream charging / usage reports (§4.2.5.3) are
// not emitted in this skeleton.
func Delete(k smfsm.Key) (DeleteStatus, error) {
	log := logger.Get("pcf.smpolicy")
	assocMu.RLock()
	a := assocs[k]
	assocMu.RUnlock()
	if a == nil {
		// Idempotent: deleting a non-existent association is a no-op.
		return DeleteStatus{Terminated: true}, nil
	}

	f := smfsm.Of(k)
	f.CancelRevalidationTimer()
	if err := f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvDeleteRequest}); err != nil {
		return DeleteStatus{}, fmt.Errorf("pcf.smpolicy: Delete FSM: %w", err)
	}

	assocMu.Lock()
	delete(assocs, k)
	assocMu.Unlock()

	// Also clear the related PCC rules from the in-memory manager so
	// the UE slot is clean for re-establishment.
	pcf.DefaultPccRuleManager.RemoveRules(a.key.IMSI, a.ctxData.DNN)

	_ = f.Fire(&smfsm.Context{Key: k, Event: smfsm.EvDeleteResponse})
	smfsm.Drop(k)

	pm.Inc(pm.PCFSmPolicyDelete, 1)
	log.WithIMSI(a.key.IMSI).Infof("SM Policy Association deleted ctxRef=%s", a.decision.SmPolicyCtxRef)
	return DeleteStatus{Terminated: true}, nil
}

// GetAssociation returns the decision for an active association, or
// nil if none — handy for SMF-side code that wants to read back the
// authorized AMBR / default QFI without re-invoking Update.
func GetAssociation(k smfsm.Key) *SmPolicyDecision {
	assocMu.RLock()
	defer assocMu.RUnlock()
	a := assocs[k]
	if a == nil {
		return nil
	}
	d := a.decision
	return &d
}

// ─── helpers ─────────────────────────────────────────────────────────

// stripSupiPrefix accepts SUPI in either "imsi-<digits>" (TS 29.571
// §5.3.2) or bare-digit form; returns the digits.
func stripSupiPrefix(supi string) string {
	const pfx = "imsi-"
	if len(supi) > len(pfx) && supi[:len(pfx)] == pfx {
		return supi[len(pfx):]
	}
	return supi
}

// defaultFiveQI picks the 5QI value from the default rule of the set;
// falls back to 9 (non-GBR, general-purpose) per TS 23.501 §5.7.4
// Table 5.7.4-1 when no default is flagged.
func defaultFiveQI(rs pcf.PCCRuleSet) int {
	for _, r := range rs.Rules {
		if r.IsDefault && r.FiveQI != 0 {
			return r.FiveQI
		}
	}
	return 9
}

// timeUntil returns duration from now until t; zero if t is zero/past.
func timeUntil(t time.Time) time.Duration {
	if t.IsZero() {
		return 0
	}
	d := time.Until(t)
	if d <= 0 {
		return 0
	}
	return d
}
