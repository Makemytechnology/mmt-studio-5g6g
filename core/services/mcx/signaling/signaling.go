// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package signaling — MCX SIP bridge + WebSocket events.
//
// The WebSocket-side bridge is a non-3GPP UI plumbing layer (the
// MCX panel pushes floor-control state changes to a connected
// browser). It is NOT a 3GPP-specified protocol — only the server-
// to-client event payload names are stable.
//
// Spec anchors that constrain the SIP-side helpers:
//
//   - TS 24.379 §6.2  "MCPTT use of SIP" — controls the SIP
//                      message shape for any MCPTT signalling
//                      ferried over the bridge.
//   - TS 24.380 §6.3.5 (per-event names FloorGranted / FloorTaken
//                      / FloorIdle / FloorReleasing) — the
//                      WebSocket event names below mirror the
//                      §6.3.5 floor-server state machine names so
//                      a UI can replay the floor protocol verbatim.
//
// TODO(spec: TS 24.379 §6.2.4 / §6.2.8.1.15): the SIP ↔ WebSocket
// bridge currently shuttles MCX-related SIP messages without
// stamping the MCPTT feature-tag, MIME body, or Resource-Priority
// header. Once mcptt.BuildMCXInvite emits those, the bridge needs
// to forward the full MCPTT request/response untouched.
package signaling

import (
	"encoding/json"
	"time"
)

// ── WebSocket event types (ws_events.py) ──

const (
	FloorGranted          = "floor_granted"
	FloorDenied           = "floor_denied"
	FloorReleased         = "floor_released"
	FloorPreempted        = "floor_preempted"
	FloorQueued           = "floor_queued"
	CallIncoming          = "call_incoming"
	CallEnded             = "call_ended"
	ParticipantJoined     = "participant_joined"
	ParticipantLeft       = "participant_left"
	VideoCallIncoming     = "video_call_incoming"
	VideoCallEnded        = "video_call_ended"
	TransmissionGranted   = "transmission_granted"
	TransmissionReleased  = "transmission_released"
	MessageReceived       = "message_received"
	FileReceived          = "file_received"
	EmergencyAlert        = "emergency_alert"
	ActionFloorRequest    = "floor_request"
	ActionFloorRelease    = "floor_release"
	ActionTxRequest       = "transmission_request"
	ActionTxRelease       = "transmission_release"
)

// EncodeEvent encodes a server-to-client event as JSON.
func EncodeEvent(eventType string, data map[string]interface{}) string {
	msg := map[string]interface{}{
		"type":      eventType,
		"data":      data,
		"timestamp": float64(time.Now().UnixMilli()) / 1000,
	}
	b, _ := json.Marshal(msg)
	return string(b)
}

// DecodeAction decodes a client-to-server action from JSON.
func DecodeAction(raw string) (string, map[string]interface{}) {
	var msg map[string]interface{}
	json.Unmarshal([]byte(raw), &msg)
	action, _ := msg["action"].(string)
	data, _ := msg["data"].(map[string]interface{})
	if data == nil { data = map[string]interface{}{} }
	return action, data
}

// BroadcastFunc is a callback signature for WebSocket broadcasting.
type BroadcastFunc func(eventType string, data map[string]interface{})
