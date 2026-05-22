// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mcptt — MCPTT call management + floor control.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.280   Common functional architecture for MCX (Stage 2).
//   - TS 23.379   MCPTT Stage 2 (functional architecture / info flows).
//   - TS 24.379   MCPTT call control (Stage 3 — SIP-level).
//   - TS 24.380   MCPTT media plane control (Stage 3 — floor protocol).
//
// Files in this package:
//
//   floor.go       — TS 24.380 §6.3.5 floor-control server state
//                    machine. All floor-state transitions happen
//                    here and only here.
//   mcptt.go       — call lifecycle (StartGroupCall / InitiateGroupCall
//                    / EndCall), DB persistence helpers, MCX SIP
//                    message builders, FloorManager singleton.
//
// Go port of services/mcx/mcptt/call_manager.py + floor_control.py +
// floor_participant.py.
package mcptt

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("mcx.mcptt")

// ── Priority constants (from mcx/common/priority.py) ──
//
// These are internal FloorController ordering ranks (lower value
// wins), NOT the SIP Resource-Priority values sent on the wire.
// Per TS 24.379 §6.2.8.1.15, the wire Resource-Priority namespace
// and values are retrieved from the MCPTT service configuration
// document (TS 24.484), not hardcoded.

const (
	PriorityEmergency = 1
	PriorityNormal    = 5
	// MaxFloorQueue caps the per-call floor-request queue depth.
	// The spec allows queueing per TS 24.380 §6.3.5.4 ("U: not
	// permitted and Floor Taken") when the floor-request-queue is
	// negotiated by the MCPTT service configuration; we apply the
	// cap unconditionally so denial behaviour is deterministic.
	MaxFloorQueue = 10
)

// canPreempt is a local-policy collapse of TS 24.380 §4.1.1.4
// ("Determine on-network effective priority"), which lists seven
// input parameters (floor priority, user-priority from TS 24.481,
// participant type, call type, effective priority of the current
// talker, etc.) and maps them to four outcomes: preempt-override,
// preempt-revoke, queued, or rejected. Only the priority input and
// the preempt-override outcome are implemented here (see
// floor.go:doPreempt).
//
// TODO(spec: TS 24.380 §4.1.1.4): wire the remaining six §4.1.1.4
// inputs (user-priority lookup, participant type, call type, etc.)
// and the preempt-revoke outcome (§6.3.5.6 "U: pending Floor
// Revoke") so the full §4.1.1.4 decision matrix is honored.
func canPreempt(req, holder int) bool { return req == PriorityEmergency || req < holder }

// ── Floor Manager singleton ──

type FloorManager struct {
	mu sync.Mutex
	cs map[string]*FloorController
}

var GlobalFloorMgr = &FloorManager{cs: make(map[string]*FloorController)}

func (fm *FloorManager) GetOrCreate(id string) *FloorController {
	fm.mu.Lock(); defer fm.mu.Unlock()
	if c, ok := fm.cs[id]; ok { return c }
	c := NewFloorController(id); fm.cs[id] = c; return c
}
func (fm *FloorManager) Get(id string) *FloorController { fm.mu.Lock(); defer fm.mu.Unlock(); return fm.cs[id] }
func (fm *FloorManager) Remove(id string) { fm.mu.Lock(); defer fm.mu.Unlock(); delete(fm.cs, id) }

// ── Call Manager ──

// Call is an active MCPTT call (kept for backward compat).
type Call struct {
	ID          string `json:"id"`
	GroupID     string `json:"group_id"`
	Initiator   string `json:"initiator"`
	FloorHolder string `json:"floor_holder,omitempty"`
	State       string `json:"state"`
	Priority    int    `json:"priority"`
	StartedAt   string `json:"started_at"`
}

var (
	callMu sync.RWMutex
	calls  = map[string]*Call{}
	seq    int
)

func StartGroupCall(groupID, initiator string, priority int) *Call {
	callMu.Lock(); defer callMu.Unlock()
	seq++; c := &Call{ID: fmt.Sprintf("ptt-%d", seq), GroupID: groupID, Initiator: initiator,
		FloorHolder: initiator, State: "active", Priority: priority, StartedAt: time.Now().Format(time.RFC3339)}
	calls[c.ID] = c; log.Infof("MCPTT call started id=%s group=%s", c.ID, groupID); return c
}

func EndCall(callID string) {
	callMu.Lock(); defer callMu.Unlock()
	if c, ok := calls[callID]; ok { c.State = "released" }
	GlobalFloorMgr.Remove(callID)
}

func ListCalls() []Call {
	callMu.RLock(); defer callMu.RUnlock()
	out := make([]Call, 0, len(calls)); for _, c := range calls { out = append(out, *c) }; return out
}

// InitiateGroupCall creates a group call with DB persistence.
func InitiateGroupCall(originator string, groupID int, emergency bool) map[string]interface{} {
	ct := "group"; if emergency { ct = "emergency" }
	members := listGroupMembers(groupID)
	callID := dbCreateCall(ct, originator, groupID, members)
	if callID == "" { return nil }
	dbUpdateCallState(callID, "active")
	fc := GlobalFloorMgr.GetOrCreate(callID)
	for _, pid := range members { fc.AddParticipant(pid, PriorityNormal) }
	return map[string]interface{}{"call_id": callID, "call_type": ct, "state": "active", "participants": members}
}

// InitiatePrivateCall creates a 1:1 call.
func InitiatePrivateCall(originator, target string, emergency bool) map[string]interface{} {
	ct := "private"; if emergency { ct = "emergency" }
	pids := []string{originator, target}
	callID := dbCreateCall(ct, originator, 0, pids)
	if callID == "" { return nil }
	dbUpdateCallState(callID, "active")
	fc := GlobalFloorMgr.GetOrCreate(callID)
	for _, pid := range pids { fc.AddParticipant(pid, PriorityNormal) }
	return map[string]interface{}{"call_id": callID, "call_type": ct, "state": "active", "participants": pids}
}

// ── DB helpers ──

func dbCreateCall(callType, originator string, groupID int, participants []string) string {
	callID := fmt.Sprintf("call-%d", time.Now().UnixNano())
	pJSON, _ := json.Marshal(participants)
	var gid interface{}; if groupID > 0 { gid = groupID }
	engine.Exec(`INSERT INTO mcx_active_calls (call_id, call_type, originator, group_id, participants, state, priority, started_at)
		VALUES (?,?,?,?,?,'pending',5,?)`, callID, callType, originator, gid, string(pJSON), float64(time.Now().Unix()))
	return callID
}

func dbUpdateCallState(callID, state string) {
	engine.Exec(`UPDATE mcx_active_calls SET state=? WHERE call_id=?`, state, callID)
}

func updateCallFloorHolder(callID, holder string) {
	if holder == "" {
		engine.Exec(`UPDATE mcx_active_calls SET floor_holder=NULL WHERE call_id=?`, callID)
	} else {
		engine.Exec(`UPDATE mcx_active_calls SET floor_holder=? WHERE call_id=?`, holder, callID)
	}
}

func listGroupMembers(groupID int) []string {
	rows, err := engine.Query(`SELECT mcptt_id FROM mcx_group_members WHERE group_id=?`, groupID)
	if err != nil { return nil }
	defer rows.Close()
	var out []string; for rows.Next() { var id string; rows.Scan(&id); out = append(out, id) }; return out
}

func recordFloorEvent(callID, mcpttID, event string, priority int) {
	engine.Exec(`INSERT INTO mcx_floor_history (call_id, mcptt_id, event, priority, timestamp)
		VALUES (?,?,?,?,?)`, callID, mcpttID, event, priority, float64(time.Now().Unix()))
}

// ── SIP Handler (TS 24.379 §6.2 MCPTT use of SIP) ──
//
// These builders synthesise the bare-minimum SIP fields the MCX
// panel uses for INVITE / 200 OK / BYE; they DO NOT yet stamp the
// MCPTT-specific headers required by the spec:
//
//   * MIME body application/vnd.3gpp.mcptt-info+xml carrying the
//     <mcpttinfo> root element (TS 24.379 §6.3.2.2.9 "Populate MIME
//     bodies").
//   * Resource-Priority header in the MCPTT namespace (RFC 4412 +
//     TS 24.379 §6.2.8.1.15 / TS 24.484 service-config namespace).
//   * Feature-tag g.3gpp.mcptt + Accept-Contact (TS 24.379 §6.2.4
//     "Use of SIP Feature-Tags").
//
// TODO(spec: TS 24.379 §6.2.4 / §6.2.8.1.15 / §6.3.2.2.9 / §6.5):
// emit the MCPTT MIME body, Resource-Priority namespace, and
// feature-tag before sending these signalling messages on real
// on-network dialogs. The current builders are sufficient for the
// panel-side preview but not for an interoperable MCPTT INVITE.

// BuildMCXInvite builds a SIP INVITE for MCX call setup.
func BuildMCXInvite(callerURI, calleeURI, host string, port int, rtpIP string, rtpPort int, isVideo bool, sessionID string) map[string]string {
	if sessionID == "" { sessionID = fmt.Sprintf("mcx-%d", time.Now().UnixNano()) }
	media := "audio"
	if isVideo { media = "audio+video" }
	return map[string]string{
		"method": "INVITE", "uri": calleeURI,
		"from": callerURI, "to": calleeURI,
		"call_id": sessionID, "media": media,
		"rtp_ip": rtpIP, "rtp_port": fmt.Sprintf("%d", rtpPort),
	}
}

// BuildMCXBye builds a SIP BYE for MCX call teardown.
func BuildMCXBye(callerURI, calleeURI, callID, host string, port int, fromTag, toTag string) map[string]string {
	return map[string]string{
		"method": "BYE", "uri": calleeURI,
		"from": callerURI, "to": calleeURI,
		"call_id": callID, "from_tag": fromTag, "to_tag": toTag,
	}
}

// BuildMCX200OK builds a 200 OK response to an MCX INVITE.
func BuildMCX200OK(callID, fromURI, toURI, host string, port int, rtpIP string, rtpPort int, isVideo bool) map[string]string {
	media := "audio"
	if isVideo { media = "audio+video" }
	return map[string]string{
		"status": "200", "reason": "OK",
		"call_id": callID, "from": fromURI, "to": toURI,
		"media": media, "rtp_ip": rtpIP, "rtp_port": fmt.Sprintf("%d", rtpPort),
	}
}

// JoinCall adds a participant to an existing call.
func JoinCall(callID, mcpttID string) map[string]interface{} {
	callMu.Lock()
	c, ok := calls[callID]
	callMu.Unlock()
	if !ok || c == nil { return nil }
	fc := GlobalFloorMgr.GetOrCreate(callID)
	fc.AddParticipant(mcpttID, PriorityNormal)
	engine.Exec(`UPDATE mcx_active_calls SET participants=json_insert(participants, '$[#]', ?) WHERE call_id=?`, mcpttID, callID)
	return map[string]interface{}{"call_id": callID, "mcptt_id": mcpttID, "status": "joined"}
}

// LeaveCall removes a participant from an existing call.
func LeaveCall(callID, mcpttID string) map[string]interface{} {
	callMu.Lock()
	c, ok := calls[callID]
	callMu.Unlock()
	if !ok || c == nil { return nil }
	fc := GlobalFloorMgr.Get(callID)
	if fc != nil { fc.RemoveParticipant(mcpttID) }
	return map[string]interface{}{"call_id": callID, "mcptt_id": mcpttID, "status": "left"}
}

// GetCall returns a call by ID.
func GetCall(callID string) *Call {
	callMu.RLock(); defer callMu.RUnlock()
	return calls[callID]
}
