// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package af — Application Function.
//
// Authoritative specs (PDFs under specs/3gpp/, all grep-verified):
//
//	TS 23.501 v19.7.0 — 5G System architecture. §6.2.10 "Application
//	  Function" defines the AF role and its interactions with the
//	  5GC (PCF via N5, NEF via N33, IMS via N70, MEC/EASDF).
//
//	TS 29.514 v19.6.0 — Npcf_PolicyAuthorization (N5, AF ↔ PCF):
//	  §4.2.2  Create service operation      — authorize service info
//	  §4.2.3  Update service operation      — modify existing auth
//	  §4.2.4  Delete service operation      — terminate auth
//	  §4.2.5  Notify service operation      — PCF → AF push
//	  §4.2.6  Subscribe service operation   — AF subscribes to events
//	  §4.2.7  Unsubscribe service operation — AF tears down subscription
//
//	TS 29.517 v19.6.0 — Naf_EventExposure (AF as event producer):
//	  §4.1  Service description
//	  §4.2  Service operations — Subscribe / Unsubscribe / Notify
//	         (table 4.2.1-1)
//	  §5.6  Data model
//
//	TS 29.522 v19.6.0 — NEF Northbound APIs (Stage 3, N33):
//	  §4.4.7   Procedures for Traffic Influence  (the AF's MEC path)
//	  §4.4.14  Procedures for Analytics Information Exposure
//	  §4.4.33  Procedures for Media Streaming Event Exposure
//	  §4.4.47  Procedures for IMS Event Exposure Management
//	  §5.4     TrafficInfluence API — the REST surface fronting
//	           AF → NEF → PCF for URSP / DNAI steering
//
//	TS 23.548 v19.5.0 — 5G Edge Computing (Stage 2):
//	  §6.2  EAS Discovery and Re-discovery
//	  §6.3  Edge Relocation
//	  §6.4  Network Exposure to Edge Application Server
//	  §6.6  Support of AF Guidance to PCF Determination of URSP Rules
//
//	TS 23.228 v19.6.0 — IMS Stage 2. The IMS AF (typically co-located
//	  with the P-CSCF) authorizes media per SDP via Npcf_PolicyAuth
//	  (TS 29.514 §4.2.2 Create) — this is the VoNR-media path that
//	  feeds into SM Policy at the PCF and drives PDU-session
//	  Modification over §8.2.3 NGAP at the RAN side.
//
// This package owns three blocks, one file because the surfaces are
// small:
//
//  1. AF Session Management — Npcf_PolicyAuthorization via
//     AFSessionManager (per-session FSM at nf/af/fsm).
//  2. Event Exposure — Naf_EventExposure via EventExposureManager.
//  3. Traffic Influence — TS 29.522 §4.4.7 helpers that create a
//     "mec"-type AF session.
//
// Event-driven PDU Session Modification path, end-to-end:
//
//	IMS P-CSCF (SIP INVITE) → af.CreateSession(type=ims,
//	  media_components=…) → handleIMSAuthorization → pcf.HandleAARequest
//	  (TS 29.514 §4.2.2) + smpolicy.PushNotify (TS 29.512 §4.2.3) →
//	  PCF updates SmPolicyDecision → SMF rebuilds Authorized QoS /
//	  AMBR → SMF emits NGAP PDU SESSION RESOURCE MODIFY REQUEST
//	  (TS 38.413 §8.2.3) → gNB installs new QoS flows.
package af

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	affsm "github.com/mmt/mmt-studio-core/nf/af/fsm"
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smpolicyfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

var log = logger.Get("af")

// ================================================================
// AF Session — Npcf_PolicyAuthorization (TS 29.514 §4.2)
// ================================================================

// AF types — the kind of consumer this session represents. These map
// to the per-AF authorization handlers (handleIMSAuthorization,
// handleMECInfluence, handleThirdParty) and are the only values
// CreateSession will accept. They are not §-defined enums in TS 29.514;
// they are this codebase's grouping of the AF roles described across
// TS 23.501 §6.2.10 (ims via P-CSCF, third_party AF via NEF) and
// TS 23.548 §6.6 (mec — AF guidance for URSP).
const (
	AFTypeIMS        = "ims"
	AFTypeMEC        = "mec"
	AFTypeThirdParty = "third_party"
)

// ValidAFTypes is the closed set CreateSession accepts.
var ValidAFTypes = map[string]bool{
	AFTypeIMS:        true,
	AFTypeMEC:        true,
	AFTypeThirdParty: true,
}

// AFSession represents an AF session per TS 29.514 §4.2. Each session
// tracks one authorization request the AF made toward the PCF
// (§4.2.2 Create). Lifecycle runs on the per-session FSM at
// nf/af/fsm: Initial → AuthPending → Active → {UpdatePending, Terminated}.
type AFSession struct {
	SessionID       string           `json:"session_id"`
	AFID            string           `json:"af_id"`
	AFType          string           `json:"af_type"` // ims, mec, third_party
	IMSI            string           `json:"imsi,omitempty"`
	DNN             string           `json:"dnn,omitempty"`
	PDUSessionID    int              `json:"pdu_session_id,omitempty"`
	MediaComponents []map[string]any `json:"media_components,omitempty"`
	TrafficFilters  []map[string]any `json:"traffic_filters,omitempty"`
	Status          string           `json:"status"` // created, active, failed, terminated
	CreatedAt       float64          `json:"created_at"`
	UpdatedAt       float64          `json:"updated_at"`
}

// AFSessionManager manages AF sessions across all AF types.
type AFSessionManager struct {
	mu       sync.Mutex
	sessions map[string]*AFSession
	nextID   int
}

// NewAFSessionManager creates a new manager.
func NewAFSessionManager() *AFSessionManager {
	return &AFSessionManager{
		sessions: make(map[string]*AFSession),
	}
}

// CreateSession implements Npcf_PolicyAuthorization_Create (TS 29.514
// §4.2.2). The AF supplies service information — Media Components for
// IMS, Traffic Filters for MEC — and the PCF authorizes it by updating
// the associated SmPolicyDecision. On Active state the associated PDU
// session's QoS / AMBR / PCC rules will have been refreshed via the
// PCF → SMF UpdateNotify chain (TS 29.512 §4.2.3).
func (m *AFSessionManager) CreateSession(afID, afType, imsi, dnn string,
	pduSessionID int, mediaComponents, trafficFilters []map[string]any) (string, bool) {

	// Reject unauthenticated / mistyped requests at the boundary —
	// TS 29.514 §4.2.2.2 ProblemDetails (invalid request). An empty
	// af_id has no audit trail; an unknown af_type would silently fall
	// through the per-type switch below to success=false but still
	// create a "failed" session record.
	if afID == "" {
		log.Warnf("AF CreateSession rejected: blank af_id")
		return "", false
	}
	if !ValidAFTypes[afType] {
		log.Warnf("AF CreateSession rejected: unknown af_type=%q (want ims|mec|third_party)", afType)
		return "", false
	}

	m.mu.Lock()
	m.nextID++
	sessionID := fmt.Sprintf("af-sess-%05d", m.nextID)
	m.mu.Unlock()

	now := float64(time.Now().Unix())
	session := &AFSession{
		SessionID:       sessionID,
		AFID:            afID,
		AFType:          afType,
		IMSI:            imsi,
		DNN:             dnn,
		PDUSessionID:    pduSessionID,
		MediaComponents: mediaComponents,
		TrafficFilters:  trafficFilters,
		Status:          "created",
		CreatedAt:       now,
		UpdatedAt:       now,
	}

	m.mu.Lock()
	m.sessions[sessionID] = session
	m.mu.Unlock()

	// §4.2.2 Create — drive FSM Initial → AuthPending.
	fk := affsm.Key{SessionID: sessionID}
	f := affsm.Of(fk)
	_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvCreateRequest})

	log.Infof("AF session created: %s (af=%s, type=%s, imsi=%s, dnn=%s)",
		sessionID, afID, afType, orStar(imsi), orStar(dnn))
	pm.Inc(pm.AFSessionCreate, 1)

	success := false
	switch afType {
	case "ims":
		success = m.handleIMSAuthorization(session)
	case "mec":
		success = m.handleMECInfluence(session)
	case "third_party":
		success = m.handleThirdParty(session)
	}

	// Fire outcome: §4.2.2 returns either authorization granted
	// (→ Active) or ProblemDetails (→ Failed). The AF retains the
	// failed session for observability; Delete must be explicit.
	if success {
		session.Status = "active"
		_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvAuthorized})
	} else {
		session.Status = "failed"
		_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvAuthRejected})
	}
	return sessionID, success
}

// UpdateSession implements Npcf_PolicyAuthorization_Update (TS 29.514
// §4.2.3). Common triggers: IMS mid-call SDP re-INVITE (new/removed
// media component), MEC traffic-filter change.
func (m *AFSessionManager) UpdateSession(sessionID string, mediaComponents, trafficFilters []map[string]any) bool {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	m.mu.Unlock()
	if !ok {
		return false
	}

	if mediaComponents != nil {
		session.MediaComponents = mediaComponents
	}
	if trafficFilters != nil {
		session.TrafficFilters = trafficFilters
	}
	session.UpdatedAt = float64(time.Now().Unix())

	// §4.2.3 Update — Active → UpdatePending.
	fk := affsm.Key{SessionID: sessionID}
	f := affsm.Of(fk)
	_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvUpdateRequest})

	log.Infof("AF session updated: %s (media=%d, filters=%d)",
		sessionID, len(session.MediaComponents), len(session.TrafficFilters))
	pm.Inc(pm.AFSessionUpdate, 1)

	var success bool
	switch session.AFType {
	case "ims":
		success = m.handleIMSAuthorization(session)
	case "mec":
		success = m.handleMECInfluence(session)
	default:
		success = true
	}
	if success {
		_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvAuthorized})
	} else {
		_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvAuthRejected})
	}
	return success
}

// DeleteSession implements Npcf_PolicyAuthorization_Delete (TS 29.514
// §4.2.4). Idempotent — deleting an unknown sessionID returns false
// without error.
func (m *AFSessionManager) DeleteSession(sessionID string) bool {
	m.mu.Lock()
	session, ok := m.sessions[sessionID]
	if ok {
		delete(m.sessions, sessionID)
	}
	m.mu.Unlock()

	if !ok {
		return false
	}
	fk := affsm.Key{SessionID: sessionID}
	f := affsm.Of(fk)
	_ = f.Fire(&affsm.Context{Key: fk, Event: affsm.EvDeleteRequest})
	affsm.Drop(fk)

	session.Status = "terminated"
	pm.Inc(pm.AFSessionDelete, 1)
	log.Infof("AF session terminated: %s", sessionID)

	// IMS tear-down path — if this session drove a PCC rule
	// activation, its removal should deactivate it. pcf.HandleSession
	// Termination() does that for the UE's "ims" DNN.
	if session.AFType == "ims" && session.IMSI != "" {
		pcf.HandleSessionTermination(session.IMSI)
	}
	return true
}

// GetSession returns an AF session by ID.
func (m *AFSessionManager) GetSession(sessionID string) *AFSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.sessions[sessionID]
}

// GetSessions returns all sessions, optionally filtered by type.
func (m *AFSessionManager) GetSessions(afType string) []*AFSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*AFSession
	for _, s := range m.sessions {
		if afType == "" || s.AFType == afType {
			result = append(result, s)
		}
	}
	return result
}

// GetSessionsForUE returns active sessions for a UE.
func (m *AFSessionManager) GetSessionsForUE(imsi string) []*AFSession {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*AFSession
	for _, s := range m.sessions {
		if s.IMSI == imsi && s.Status == "active" {
			result = append(result, s)
		}
	}
	return result
}

// handleIMSAuthorization drives the IMS-AF → PCF → SMF chain that
// gives VoNR its dedicated QoS flow.
//
// Call chain (all grep-verified clauses):
//
//  1. TS 29.514 §4.2.2 "Npcf_PolicyAuthorization_Create"
//     — AF posts MediaComponentDescription (SDP-derived) to PCF.
//     pcf.HandleAARequest is the in-process equivalent.
//  2. PCF translates media to dynamic SDF / PCC rules per TS 29.513.
//  3. TS 29.512 §4.2.3 "Npcf_SMPolicyControl_UpdateNotify"
//     — PCF pushes the revised SmPolicyDecision to the SMF.
//     smpolicy.PushNotify is the in-process equivalent.
//  4. SMF refreshes Authorized QoS Rules / Session-AMBR.
//  5. TS 38.413 §8.2.3 "PDU Session Resource Modify" — SMF asks the
//     AMF to relay a PDU SESSION RESOURCE MODIFY REQUEST to the
//     gNB, installing the new QoS flow (5QI/ARP + flow descriptor).
//
// Failure modes:
//   - IMSI absent → §4.2.2.2.x ProblemDetails (invalid request).
//   - No SM Policy Association for UE/PDU → PushNotify returns
//     "no association"; treated as soft failure (IMS media proceeds
//     on best-effort default flow).
func (m *AFSessionManager) handleIMSAuthorization(session *AFSession) bool {
	if session.IMSI == "" {
		log.Warnf("AF IMS session %s: no IMSI", session.SessionID)
		return false
	}
	pm.Inc(pm.AFIMSAuthorize, 1)

	// Step 1: AF → PCF — TS 29.514 §4.2.2.2 "Initial provisioning of
	// service information". Extract media types for the Rx-equivalent
	// PCC rule activation.
	mediaTypes := make([]string, 0, len(session.MediaComponents))
	for _, mc := range session.MediaComponents {
		if t, ok := mc["type"].(string); ok && t != "" {
			mediaTypes = append(mediaTypes, t)
		}
	}
	if ok := pcf.HandleAARequest(session.IMSI, mediaTypes, session.MediaComponents); !ok {
		log.Warnf("AF IMS session %s: PCF HandleAARequest rejected (imsi=%s)",
			session.SessionID, session.IMSI)
		return false
	}

	// Steps 2–5: drive PCF → SMF UpdateNotify so the SMF re-
	// authorises QoS for each active PDU session belonging to this UE
	// on the IMS DNN. The SMF's NGAP Modify leg (pdumodify.SendRequest,
	// TS 38.413 §8.2.3) fires on the resulting decision.
	//
	// Which PDU session is the IMS bearer? The explicit field on the
	// AF session wins; otherwise fall through without a notify — the
	// AF hasn't told us which flow to retarget, so any guess would
	// modify the wrong bearer.
	if session.PDUSessionID > 0 {
		k := smpolicyfsm.Key{IMSI: session.IMSI, PDUSessionID: uint8(session.PDUSessionID)}
		if decision := smpolicy.GetAssociation(k); decision != nil {
			if err := smpolicy.PushNotify(k, *decision); err != nil {
				log.Warnf("AF IMS session %s: smpolicy.PushNotify err=%v",
					session.SessionID, err)
				// Don't fail the AF session — the IMS call can still
				// run over the default QoS flow. The policy mismatch
				// gets flagged in logs + counter.
			}
		} else {
			log.Warnf("AF IMS session %s: no SM Policy Association for imsi=%s pduSessID=%d (UE not in active PDU session?)",
				session.SessionID, session.IMSI, session.PDUSessionID)
		}
	} else {
		log.Infof("AF IMS session %s: no PDUSessionID supplied — PCF→SMF Notify skipped (info only)",
			session.SessionID)
	}

	log.Infof("AF IMS authorization: session=%s imsi=%s pduSessID=%d media=%v",
		session.SessionID, session.IMSI, session.PDUSessionID, mediaTypes)
	return true
}

func (m *AFSessionManager) handleMECInfluence(session *AFSession) bool {
	// In production: call edge.mec.af_influence
	log.Infof("AF MEC influence: session=%s filters=%d", session.SessionID, len(session.TrafficFilters))
	return true
}

func (m *AFSessionManager) handleThirdParty(session *AFSession) bool {
	log.Infof("AF third-party session %s: af=%s (NEF integration pending)", session.SessionID, session.AFID)
	return true
}

// SessionMgr is the global AF session manager singleton.
var SessionMgr = NewAFSessionManager()

// ================================================================
// Event Exposure — Naf_EventExposure (TS 29.517 §4.2)
// ================================================================
//
// TS 29.517 §4.1 (verbatim): "The Naf_EventExposure service enables
// the AF to expose events to any authorised NF."
//
// §4.2 service operations (from Table 4.2.1-1):
//
//	Subscribe   — consumer → AF, create subscription
//	Unsubscribe — consumer → AF, drop subscription
//	Notify      — AF → consumer, push event occurrences
//
// NB: the previous "TS 29.522 §4.4.3" citation was incorrect — that
// section is "Procedures for Device Triggering". The AF-side event
// exposure service lives in TS 29.517. Domain-specific event exposure
// procedures over the NEF surface are in TS 29.522 §4.4.33 (Media
// Streaming), §4.4.47 (IMS), §4.4.14 (Analytics).

// Event types represent the union of exposure events this AF handles.
// Names chosen to match common NEF event-type enums (TS 29.522 §5.6
// UeReachabilityIndication / LocationReport / LossOfConnectivity /
// CommunicationFailure / PduSessionStatus / QoSMonitoringEvent).
const (
	EventUEReachability   = "UE_REACHABILITY"
	EventLocationReport   = "LOCATION_REPORT"
	EventLossOfConnect    = "LOSS_OF_CONNECTIVITY"
	EventCommFailure      = "COMMUNICATION_FAILURE"
	EventPDUSessionStatus = "PDU_SESSION_STATUS"
	EventQoSMonitoring    = "QOS_MONITORING"
)

// ValidEvents is the set of supported event types.
var ValidEvents = map[string]bool{
	EventUEReachability:   true,
	EventLocationReport:   true,
	EventLossOfConnect:    true,
	EventCommFailure:      true,
	EventPDUSessionStatus: true,
	EventQoSMonitoring:    true,
}

// EventSubscription is an AF event subscription per TS 29.517
// §5.6 (AfEventExposureSubsc data model).
type EventSubscription struct {
	SubID             string  `json:"sub_id"`
	AFID              string  `json:"af_id"`
	EventType         string  `json:"event_type"`
	IMSI              string  `json:"imsi,omitempty"`
	CallbackURL       string  `json:"callback_url,omitempty"`
	Status            string  `json:"status"`
	NotificationCount int     `json:"notification_count"`
	CreatedAt         float64 `json:"created_at"`
}

// EventExposureManager manages AF event subscriptions and notifications.
type EventExposureManager struct {
	mu            sync.Mutex
	subscriptions map[string]*EventSubscription
	nextID        int
}

// NewEventExposureManager creates a new manager.
func NewEventExposureManager() *EventExposureManager {
	return &EventExposureManager{
		subscriptions: make(map[string]*EventSubscription),
	}
}

// Subscribe implements Naf_EventExposure_Subscribe (TS 29.517 §4.2).
// Returns the subscription ID (the §5.3 "Individual AF Event
// Exposure Subscription" resource reference) on success, "" on blank
// af_id or invalid event type.
func (m *EventExposureManager) Subscribe(afID, eventType, imsi, callbackURL string) string {
	if afID == "" {
		log.Warnf("AF Subscribe rejected: blank af_id")
		return ""
	}
	if !ValidEvents[eventType] {
		log.Warnf("Invalid event type: %s", eventType)
		return ""
	}
	m.mu.Lock()
	m.nextID++
	subID := fmt.Sprintf("evt-sub-%04d", m.nextID)
	m.subscriptions[subID] = &EventSubscription{
		SubID:       subID,
		AFID:        afID,
		EventType:   eventType,
		IMSI:        imsi,
		CallbackURL: callbackURL,
		Status:      "active",
		CreatedAt:   float64(time.Now().Unix()),
	}
	m.mu.Unlock()

	pm.Inc(pm.AFEventSubscribe, 1)
	log.Infof("Event subscription: %s (af=%s, event=%s, imsi=%s)", subID, afID, eventType, orStar(imsi))
	return subID
}

// Unsubscribe implements Naf_EventExposure_Unsubscribe (TS 29.517
// §4.2). Idempotent on missing subID.
func (m *EventExposureManager) Unsubscribe(subID string) bool {
	m.mu.Lock()
	sub, ok := m.subscriptions[subID]
	if ok {
		delete(m.subscriptions, subID)
	}
	m.mu.Unlock()
	if ok {
		sub.Status = "terminated"
		pm.Inc(pm.AFEventUnsubscribe, 1)
		log.Infof("Event unsubscribed: %s", subID)
	}
	return ok
}

// Notify implements Naf_EventExposure_Notify (TS 29.517 §4.2). Fans
// out one event to every matching active subscription; per-consumer
// HTTP callback is fire-and-forget so a slow consumer doesn't block
// the producer. Callers include (future wiring) the AMF on UE
// reachability changes and the SMF on PDU session state changes.
func (m *EventExposureManager) Notify(eventType, imsi string, eventData map[string]any) {
	m.mu.Lock()
	var matching []*EventSubscription
	for _, s := range m.subscriptions {
		if s.Status == "active" && s.EventType == eventType &&
			(s.IMSI == "" || s.IMSI == imsi) {
			matching = append(matching, s)
		}
	}
	m.mu.Unlock()

	for _, sub := range matching {
		sub.NotificationCount++
		pm.Inc(pm.AFEventNotify, 1)
		if sub.CallbackURL != "" {
			go sendHTTPNotification(sub, eventType, imsi, eventData)
		}
		log.Infof("Event notified: %s -> %s (af=%s, imsi=%s)", eventType, sub.SubID, sub.AFID, imsi)
	}
}

func sendHTTPNotification(sub *EventSubscription, eventType, imsi string, eventData map[string]any) {
	payload := fmt.Sprintf(`{"subscription_id":"%s","event_type":"%s","imsi":"%s","timestamp":%d}`,
		sub.SubID, eventType, imsi, time.Now().Unix())
	req, err := http.NewRequest("POST", sub.CallbackURL, strings.NewReader(payload))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Warnf("HTTP notification to %s failed: %v", sub.CallbackURL, err)
		return
	}
	defer resp.Body.Close()
}

// GetSubscriptions returns subscriptions, optionally filtered by AF ID.
func (m *EventExposureManager) GetSubscriptions(afID string) []*EventSubscription {
	m.mu.Lock()
	defer m.mu.Unlock()
	var result []*EventSubscription
	for _, s := range m.subscriptions {
		if afID == "" || s.AFID == afID {
			result = append(result, s)
		}
	}
	return result
}

// EventMgr is the global event exposure manager singleton.
var EventMgr = NewEventExposureManager()

// ================================================================
// Traffic Influence — TS 29.522 §4.4.7 + §5.4
// ================================================================
//
// §4.4.7 "Procedures for Traffic Influence" — the AF requests
// application-level traffic routing influence; the NEF forwards the
// request to the PCF which adjusts URSP rules (steering toward a
// DNAI / edge site). Edge-computing architecture context:
// TS 23.548 §6.6 "Support of AF Guidance to PCF Determination of
// Proper URSP Rules" + §6.4 "Network Exposure to Edge Application
// Server". The REST surface is §5.4 "TrafficInfluence API".
//
// NB: the previous "§4.4.13 / TS 23.548 §6.3.1" citations were wrong —
// §4.4.13 is RACS Parameter Provisioning and §6.3.1 is inside the
// Edge Relocation section. The correct anchors are §4.4.7 / §5.4 /
// TS 23.548 §6.6.

// RequestTrafficInfluence creates an Individual Traffic Influence
// Subscription (TS 29.522 §5.4.1.3) via the AF session manager.
// Convenience wrapper that encodes the target-IP / FQDN / port /
// edge-site-ID into a single TrafficFilters entry and fires a
// "mec"-type AF session.
func RequestTrafficInfluence(afID, imsi, dnn, targetIP, targetFQDN string,
	targetPort int, edgeSiteID string) (string, bool) {

	trafficFilters := []map[string]any{{
		"ip":           targetIP,
		"fqdn":         targetFQDN,
		"port":         targetPort,
		"edge_site_id": edgeSiteID,
	}}
	pm.Inc(pm.AFTrafficInfluenceCreate, 1)
	return SessionMgr.CreateSession(afID, "mec", imsi, dnn, 0, nil, trafficFilters)
}

// RevokeTrafficInfluence deletes an Individual Traffic Influence
// Subscription (TS 29.522 §5.4.1.3 DELETE).
func RevokeTrafficInfluence(sessionID string) bool {
	pm.Inc(pm.AFTrafficInfluenceDelete, 1)
	return SessionMgr.DeleteSession(sessionID)
}

// ================================================================
// Legacy API compatibility
// ================================================================

// TrafficInfluence is an active AF traffic-routing request (legacy).
type TrafficInfluence struct {
	ID      string `json:"id"`
	AFID    string `json:"af_id"`
	DNN     string `json:"dnn"`
	SNSSAI  string `json:"snssai"`
	DNAI    string `json:"dnai"`
	AppID   string `json:"app_id"`
	Created string `json:"created_at"`
}

// CreateTrafficInfluence registers an AF traffic-routing request (legacy API).
func CreateTrafficInfluence(afID, dnn, snssai, dnai, appID string) string {
	id, _ := RequestTrafficInfluence(afID, "", dnn, "", "", 0, dnai)
	return id
}

// ListTrafficInfluences returns all active influences (from session manager).
func ListTrafficInfluences() []TrafficInfluence {
	sessions := SessionMgr.GetSessions("mec")
	var out []TrafficInfluence
	for _, s := range sessions {
		out = append(out, TrafficInfluence{
			ID:      s.SessionID,
			AFID:    s.AFID,
			DNN:     s.DNN,
			Created: time.Unix(int64(s.CreatedAt), 0).Format(time.RFC3339),
		})
	}
	return out
}

// EventSub is an AF event-exposure subscription (legacy).
type EventSub struct {
	ID     string `json:"id"`
	AFID   string `json:"af_id"`
	Event  string `json:"event"`
	IMSI   string `json:"imsi,omitempty"`
	DNN    string `json:"dnn,omitempty"`
	Active bool   `json:"active"`
}

// SubscribeEvent creates an event-exposure subscription (legacy API).
func SubscribeEvent(afID, event, imsi, dnn string) string {
	return EventMgr.Subscribe(afID, event, imsi, "")
}

// ListEventSubs returns all event subscriptions (legacy API).
func ListEventSubs() []EventSub {
	subs := EventMgr.GetSubscriptions("")
	var out []EventSub
	for _, s := range subs {
		out = append(out, EventSub{
			ID: s.SubID, AFID: s.AFID, Event: s.EventType,
			IMSI: s.IMSI, Active: s.Status == "active",
		})
	}
	return out
}

func orStar(s string) string {
	if s == "" {
		return "*"
	}
	return s
}
