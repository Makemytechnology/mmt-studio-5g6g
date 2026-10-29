// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package s1ap -- S1AP procedures for EPC MME (TS 36.413).
//
// Go port of access/epc/mme/s1ap/*.py. Handles S1 Setup, Initial UE
// Message, UL/DL NAS Transport, E-RAB Setup, Handover, Path Switch,
// UE Context Release, and eNB context management.
package s1ap

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

const (
	S1APSCTPPort = 36412
	S1APPPID     = 18
)

// ---- eNB Context (TS 36.413 section 9.2) ----

// EnbCtx represents a connected eNB.
type EnbCtx struct {
	EnbIP        string   `json:"enb_ip"`
	EnbName      string   `json:"enb_name"`
	EnbID        string   `json:"enb_id"`
	Connected    bool     `json:"connected"`
	TACs         []string `json:"tacs"`
	PLMNIdentity []byte   `json:"-"`
	PagingDRX    string   `json:"paging_drx,omitempty"`
	ConnectedAt  time.Time `json:"connected_at"`
}

var (
	enbMu       sync.Mutex
	enbRegistry = make(map[string]*EnbCtx) // enb_ip -> ctx
)

// RegisterEnb registers a connected eNB after S1 Setup.
func RegisterEnb(enb *EnbCtx) {
	enbMu.Lock()
	defer enbMu.Unlock()
	enb.Connected = true
	enb.ConnectedAt = time.Now()
	enbRegistry[enb.EnbIP] = enb
	logger.Get("epc.mme.s1ap").Infof("eNB registered: ip=%s name=%s id=%s", enb.EnbIP, enb.EnbName, enb.EnbID)
}

// UnregisterEnb removes an eNB.
func UnregisterEnb(enbIP string) {
	enbMu.Lock()
	defer enbMu.Unlock()
	if e, ok := enbRegistry[enbIP]; ok {
		e.Connected = false
		delete(enbRegistry, enbIP)
		logger.Get("epc.mme.s1ap").Infof("eNB unregistered: ip=%s", enbIP)
	}
}

// GetEnb returns an eNB by IP.
func GetEnb(enbIP string) *EnbCtx {
	enbMu.Lock()
	defer enbMu.Unlock()
	return enbRegistry[enbIP]
}

// ListEnbs returns all connected eNBs.
func ListEnbs() []*EnbCtx {
	enbMu.Lock()
	defer enbMu.Unlock()
	out := make([]*EnbCtx, 0, len(enbRegistry))
	for _, e := range enbRegistry {
		out = append(out, e)
	}
	return out
}

// ---- S1 Setup (TS 36.413 section 8.7.1) ----

// HandleS1SetupRequest processes an S1 Setup Request from eNB.
func HandleS1SetupRequest(enbIP, enbName, enbID string, tacs []string, plmnID []byte) map[string]interface{} {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("S1 Setup Request from %s (name=%s, id=%s)", enbIP, enbName, enbID)

	enb := &EnbCtx{
		EnbIP:        enbIP,
		EnbName:      enbName,
		EnbID:        enbID,
		TACs:         tacs,
		PLMNIdentity: plmnID,
	}
	RegisterEnb(enb)

	// Build S1 Setup Response
	return map[string]interface{}{
		"message_type":   "s1_setup_response",
		"mme_name":       "sacore-mme",
		"relative_capacity": 255,
		"enb_ip":         enbIP,
	}
}

// ---- Initial UE Message (TS 36.413 section 8.6.1) ----

// HandleInitialUEMessage processes an Initial UE Message containing NAS.
func HandleInitialUEMessage(enbIP string, enbUeS1apID int, nasPDU []byte) map[string]interface{} {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("Initial UE Message from eNB %s (eNB-UE=%d, NAS=%d bytes)", enbIP, enbUeS1apID, len(nasPDU))
	return map[string]interface{}{
		"message_type": "initial_ue_message",
		"enb_ip":       enbIP,
		"enb_ue_id":    enbUeS1apID,
		"nas_size":     len(nasPDU),
	}
}

// ---- UL/DL NAS Transport (TS 36.413 section 8.6.3/8.6.2) ----

// HandleUplinkNASTransport processes an Uplink NAS Transport.
func HandleUplinkNASTransport(mmeUeID, enbUeID int, nasPDU []byte) {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("UL NAS Transport: MME-UE=%d eNB-UE=%d NAS=%d bytes", mmeUeID, enbUeID, len(nasPDU))
}

// GenerateDownlinkNASTransport builds a DL NAS Transport.
func GenerateDownlinkNASTransport(mmeUeID, enbUeID int, nasPDU []byte) map[string]interface{} {
	return map[string]interface{}{
		"message_type": "dl_nas_transport",
		"mme_ue_id":    mmeUeID,
		"enb_ue_id":    enbUeID,
		"nas_size":     len(nasPDU),
	}
}

// ---- E-RAB Setup (TS 36.413 section 8.2.1) ----

// GenerateERABSetupRequest builds an E-RAB Setup Request for bearer activation.
func GenerateERABSetupRequest(mmeUeID, enbUeID, ebi int, qci int, nasPDU []byte) map[string]interface{} {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("E-RAB Setup Request: MME-UE=%d EBI=%d QCI=%d", mmeUeID, ebi, qci)
	return map[string]interface{}{
		"message_type": "erab_setup_request",
		"mme_ue_id":    mmeUeID,
		"enb_ue_id":    enbUeID,
		"ebi":          ebi,
		"qci":          qci,
	}
}

// HandleERABSetupResponse processes an E-RAB Setup Response.
func HandleERABSetupResponse(mmeUeID int, successEBIs, failedEBIs []int) {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("E-RAB Setup Response: MME-UE=%d success=%v failed=%v", mmeUeID, successEBIs, failedEBIs)
}

// ---- Handover (TS 36.413 section 8.4) ----

// HandleHandoverRequired processes Handover Required from source eNB.
func HandleHandoverRequired(mmeUeID int, targetEnbIP string, cause string, srcToTgtContainer []byte) map[string]interface{} {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("Handover Required: MME-UE=%d target=%s cause=%s", mmeUeID, targetEnbIP, cause)
	return map[string]interface{}{
		"message_type": "handover_request",
		"mme_ue_id":    mmeUeID,
		"target_enb":   targetEnbIP,
	}
}

// GenerateHandoverCommand builds a Handover Command to source eNB.
func GenerateHandoverCommand(mmeUeID int, tgtToSrcContainer []byte) map[string]interface{} {
	return map[string]interface{}{"message_type": "handover_command", "mme_ue_id": mmeUeID}
}

// HandleHandoverNotify processes Handover Notify from target eNB.
func HandleHandoverNotify(mmeUeID int, ecgi string, tai string) {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("Handover Notify: MME-UE=%d ECGI=%s TAI=%s", mmeUeID, ecgi, tai)
}

// ---- Path Switch (TS 36.413 section 8.4.4) ----

// HandlePathSwitchRequest processes a Path Switch Request (X2 handover).
func HandlePathSwitchRequest(mmeUeID, enbUeID int, newEnbIP, ecgi, tai string) map[string]interface{} {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("Path Switch Request: MME-UE=%d newEnb=%s ECGI=%s TAI=%s", mmeUeID, newEnbIP, ecgi, tai)
	return map[string]interface{}{
		"message_type": "path_switch_ack",
		"mme_ue_id":    mmeUeID,
		"new_enb":      newEnbIP,
	}
}

// ---- UE Context Release (TS 36.413 section 8.3.2) ----

// GenerateUEContextReleaseCommand sends UE Context Release Command.
func GenerateUEContextReleaseCommand(mmeUeID int, causeGroup, causeValue string) map[string]interface{} {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("UE Context Release Command: MME-UE=%d cause=%s/%s", mmeUeID, causeGroup, causeValue)
	return map[string]interface{}{
		"message_type": "ue_context_release_command",
		"mme_ue_id":    mmeUeID,
		"cause":        fmt.Sprintf("%s/%s", causeGroup, causeValue),
	}
}

// HandleUEContextReleaseComplete processes UE Context Release Complete.
func HandleUEContextReleaseComplete(mmeUeID int) {
	log := logger.Get("epc.mme.s1ap")
	log.Infof("UE Context Release Complete: MME-UE=%d", mmeUeID)
}

// ---- Status API ----

// Status returns current S1AP state for the GUI panel.
func Status() map[string]any {
	log := logger.Get("s1ap")
	_ = log
	_ = engine.Open
	enbs := ListEnbs()
	return map[string]any{
		"status":     "ready",
		"enb_count":  len(enbs),
		"sctp_port":  S1APSCTPPort,
	}
}
