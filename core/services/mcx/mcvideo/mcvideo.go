// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mcvideo — MCVideo transmission control + call management.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.281 §6     MCVideo functional model (on-network and
//                       off-network operation).
//   - TS 23.281 §7.1   Group call procedures.
//   - TS 23.281 §7.2   Private call procedures.
//   - TS 23.281 §7.7   Transmission control procedures — the
//                       analogue of MCPTT floor control for the
//                       video plane. The "transmission" in the
//                       structs below corresponds to the §7.7
//                       transmission-of-the-floor concept.
//   - TS 23.281 §7.9   Affiliation / de-affiliation to MCVideo
//                       group(s) (drives Add/RemoveParticipant).
//
// Stage-3 protocol details (the actual SIP message bodies, the
// transmission-control protocol packets, MIME types, etc.) are
// defined in TS 24.281 which is not yet in-tree. Every
// stage-3-shaped TODO below cites that TS.
//
// Note on naming: in MCVideo the floor-of-talk concept is called
// "transmission" (i.e. the right to transmit a video stream). The
// transmission-controller below is the analogue of the MCPTT
// FloorController (TS 24.380 §6.3.5).
package mcvideo

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("mcx.mcvideo")

// ── Transmission Controller (TS 23.281 §7.7) ──
//
// Single-transmitter model: one video stream at a time per call,
// in line with §7.7.1 "General" which describes transmission as
// the right to transmit a video stream. Multi-stream / floor-
// override behaviour is deferred to a future iteration.
//
// TODO(spec: TS 23.281 §7.7.1 / §7.7.2): preemption, queueing, and
// transmission-revoke ("preempt-revoke" in MCPTT terms) are not
// implemented — the controller currently denies a contending
// request when a transmitter is active. §7.7.1 covers the on-
// network model and §7.7.2 the off-network model; both have
// preempt branches we don't yet model.
//
// TODO(spec: TS 24.281): stage-3 transmission-control protocol
// packets (the MCVideo equivalent of the MCPTT floor protocol in
// TS 24.380) are not encoded; this is a pure in-process state
// machine for now.

// TransmissionController manages video floor for a single call.
type TransmissionController struct {
	CallID       string
	Transmitter  string
	Participants map[string]bool
	EventCb      func(string, string, map[string]interface{})
	mu           sync.Mutex
}

func NewTransmissionController(callID string) *TransmissionController {
	return &TransmissionController{CallID: callID, Participants: make(map[string]bool)}
}

func (tc *TransmissionController) AddParticipant(id string) {
	tc.mu.Lock(); defer tc.mu.Unlock(); tc.Participants[id] = true
}

func (tc *TransmissionController) RemoveParticipant(id string) {
	tc.mu.Lock(); defer tc.mu.Unlock()
	delete(tc.Participants, id)
	if tc.Transmitter == id { tc.Transmitter = ""; tc.emit("transmission_released", map[string]interface{}{"mcptt_id": id}) }
}

func (tc *TransmissionController) RequestTransmission(id string) map[string]interface{} {
	tc.mu.Lock(); defer tc.mu.Unlock()
	if !tc.Participants[id] { return map[string]interface{}{"result": "error", "reason": "not_participant"} }
	if tc.Transmitter == "" { tc.Transmitter = id; tc.emit("transmission_granted", map[string]interface{}{"mcptt_id": id}); return map[string]interface{}{"result": "granted"} }
	if tc.Transmitter == id { return map[string]interface{}{"result": "already_transmitting"} }
	return map[string]interface{}{"result": "denied", "reason": "busy", "current_transmitter": tc.Transmitter}
}

func (tc *TransmissionController) ReleaseTransmission(id string) map[string]interface{} {
	tc.mu.Lock(); defer tc.mu.Unlock()
	if tc.Transmitter != id { return map[string]interface{}{"result": "error", "reason": "not_transmitter"} }
	tc.Transmitter = ""; tc.emit("transmission_released", map[string]interface{}{"mcptt_id": id})
	return map[string]interface{}{"result": "released"}
}

func (tc *TransmissionController) GetStatus() map[string]interface{} {
	tc.mu.Lock(); defer tc.mu.Unlock()
	pids := make([]string, 0)
	for p := range tc.Participants { pids = append(pids, p) }
	return map[string]interface{}{"call_id": tc.CallID, "transmitter": tc.Transmitter, "participants": pids}
}

func (tc *TransmissionController) emit(evt string, data map[string]interface{}) {
	if tc.EventCb != nil { go tc.EventCb(tc.CallID, evt, data) }
}

// ── Transmission Manager ──

type TransmissionManager struct {
	mu sync.Mutex
	cs map[string]*TransmissionController
}

var GlobalTxMgr = &TransmissionManager{cs: make(map[string]*TransmissionController)}

func (tm *TransmissionManager) GetOrCreate(id string) *TransmissionController {
	tm.mu.Lock(); defer tm.mu.Unlock()
	if c, ok := tm.cs[id]; ok { return c }
	c := NewTransmissionController(id); tm.cs[id] = c; return c
}
func (tm *TransmissionManager) Get(id string) *TransmissionController { tm.mu.Lock(); defer tm.mu.Unlock(); return tm.cs[id] }
func (tm *TransmissionManager) Remove(id string) { tm.mu.Lock(); defer tm.mu.Unlock(); delete(tm.cs, id) }

// ── Video Call Manager (video_call_manager.py) ──

// VideoCall tracks an active MCVideo call.
type VideoCall struct {
	ID          string   `json:"id"`
	GroupID     string   `json:"group_id,omitempty"`
	Initiator   string   `json:"initiator"`
	Target      string   `json:"target,omitempty"`
	CallType    string   `json:"call_type"` // group | private
	State       string   `json:"state"`
	Participants []string `json:"participants"`
}

var (
	videoCalls = map[string]*VideoCall{}
	vcMu       sync.Mutex
	vcSeq      int
)

// InitiateVideoGroupCall starts a group video call.
func InitiateVideoGroupCall(originator string, groupID int) map[string]interface{} {
	vcMu.Lock(); defer vcMu.Unlock()
	vcSeq++
	callID := fmt.Sprintf("vcall-%d", vcSeq)
	vc := &VideoCall{ID: callID, GroupID: fmt.Sprintf("%d", groupID), Initiator: originator,
		CallType: "group", State: "active", Participants: []string{originator}}
	videoCalls[callID] = vc
	tc := GlobalTxMgr.GetOrCreate(callID)
	tc.AddParticipant(originator)
	log.Infof("MCVideo group call started id=%s group=%d", callID, groupID)
	return map[string]interface{}{"call_id": callID, "call_type": "group", "state": "active",
		"initiator": originator, "group_id": groupID}
}

// InitiateVideoPrivateCall starts a private video call.
func InitiateVideoPrivateCall(originator, target string) map[string]interface{} {
	vcMu.Lock(); defer vcMu.Unlock()
	vcSeq++
	callID := fmt.Sprintf("vcall-%d", vcSeq)
	vc := &VideoCall{ID: callID, Initiator: originator, Target: target,
		CallType: "private", State: "active", Participants: []string{originator, target}}
	videoCalls[callID] = vc
	tc := GlobalTxMgr.GetOrCreate(callID)
	tc.AddParticipant(originator)
	tc.AddParticipant(target)
	log.Infof("MCVideo private call started id=%s %s -> %s", callID, originator, target)
	return map[string]interface{}{"call_id": callID, "call_type": "private", "state": "active",
		"initiator": originator, "target": target}
}

// EndVideoCall terminates a video call.
func EndVideoCall(callID string) map[string]interface{} {
	vcMu.Lock()
	vc, ok := videoCalls[callID]
	if ok { vc.State = "released" }
	vcMu.Unlock()
	GlobalTxMgr.Remove(callID)
	if !ok { return nil }
	log.Infof("MCVideo call ended id=%s", callID)
	return map[string]interface{}{"call_id": callID, "state": "released"}
}

// ListVideoCalls returns all video calls.
func ListVideoCalls() []VideoCall {
	vcMu.Lock(); defer vcMu.Unlock()
	out := make([]VideoCall, 0, len(videoCalls))
	for _, vc := range videoCalls { out = append(out, *vc) }
	return out
}
