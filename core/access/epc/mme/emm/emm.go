// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package emm -- EPS Mobility Management (TS 24.301).
//
// Go port of access/epc/mme/emm/*.py. Handles EPS Attach, Detach, TAU,
// Service Request, Authentication, Security Mode Command, Identity,
// and inter-system handover (N26) for 4G UEs.
//
// The MME UE context (MmeUeCtx) holds per-UE state including security
// context (KASME, NAS keys), EMM/ECM states, EPS bearers, and GUTI.
package emm

import (
	"fmt"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ---- MME UE Context (TS 23.401 section 5.7.2) ----

// MmeUeCtx holds per-UE state in the MME.
type MmeUeCtx struct {
	MmeUeS1apID  int
	EnbUeS1apID  int
	IMSI         string
	IMEISV       string
	GUTI         string
	EMMState     string // DEREGISTERED | REGISTERED | COMMON-PROCEDURE-INITIATED
	ECMState     string // IDLE | CONNECTED
	AttachType   string // eps | combined | emergency
	PLMN         PLMNIdentity
	TAI          string
	ECGI         string
	SecurityCtx  SecurityContext
	EPSBearers   map[int]BearerInfo
	DefaultEBI   int
	NASPdu       []byte
	// Handover state
	HOState         string // IDLE | PREPARING | PREPARED | COMPLETED | CANCELLED
	HOSourceEnbIP   string
	HOTargetEnbIP   string
}

// PLMNIdentity holds MCC/MNC.
type PLMNIdentity struct {
	MCC string
	MNC string
}

// SecurityContext holds EPS security context (TS 33.401 section 6.2).
type SecurityContext struct {
	RAND     []byte
	XRES     []byte
	AUTN     []byte
	KASME    []byte
	UESecCap []byte
	KeNB     []byte
	KNASenc  []byte
	KNASint  []byte
	SecHdr   int
	EEA      *int
	EIA      *int
	ULCount  int
	DLCount  int
	AuthDone bool
}

// BearerInfo holds EPS bearer information.
type BearerInfo struct {
	EBI         int    `json:"ebi"`
	LinkedEBI   int    `json:"linked_ebi,omitempty"`
	QCI         int    `json:"qci"`
	ARPPriority int    `json:"arp_priority"`
	DNN         string `json:"dnn,omitempty"`
	IPAddr      string `json:"ip_addr,omitempty"`
	ServiceName string `json:"service_name,omitempty"`
	GBRDLKbps   int    `json:"gbr_dl_kbps,omitempty"`
	GBRULKbps   int    `json:"gbr_ul_kbps,omitempty"`
}

// ---- UE Context Registry ----

var (
	ctxMu      sync.Mutex
	ueContexts = make(map[int]*MmeUeCtx)    // mme_ue_s1ap_id -> ctx
	imsiMap    = make(map[string]int)         // imsi -> mme_ue_s1ap_id
	nextMmeUeID = 1
	maxMmeUeID  = 1000
)

// NewMmeUeCtx creates a new MME UE context.
func NewMmeUeCtx(enbUeS1apID int) *MmeUeCtx {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	id := nextMmeUeID
	nextMmeUeID++
	if nextMmeUeID > maxMmeUeID { nextMmeUeID = 1 }
	ctx := &MmeUeCtx{
		MmeUeS1apID: id,
		EnbUeS1apID: enbUeS1apID,
		EMMState:    "DEREGISTERED",
		ECMState:    "IDLE",
		HOState:     "IDLE",
		EPSBearers:  make(map[int]BearerInfo),
	}
	ueContexts[id] = ctx
	return ctx
}

// RegisterIMSI associates IMSI with a UE context.
func RegisterIMSI(ctx *MmeUeCtx, imsi string) {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	if ctx.IMSI != "" && ctx.IMSI != imsi { delete(imsiMap, ctx.IMSI) }
	ctx.IMSI = imsi
	imsiMap[imsi] = ctx.MmeUeS1apID
}

// SearchByMmeID looks up a UE context by MME-UE-S1AP-ID.
func SearchByMmeID(mmeUeS1apID int) *MmeUeCtx { ctxMu.Lock(); defer ctxMu.Unlock(); return ueContexts[mmeUeS1apID] }

// SearchByIMSI looks up a UE context by IMSI.
func SearchByIMSI(imsi string) *MmeUeCtx {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	if id, ok := imsiMap[imsi]; ok { return ueContexts[id] }
	return nil
}

// RemoveUeCtx removes a UE context from all registries.
func RemoveUeCtx(ctx *MmeUeCtx) {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	delete(ueContexts, ctx.MmeUeS1apID)
	if ctx.IMSI != "" { delete(imsiMap, ctx.IMSI) }
}

// GetAllUeCtx returns all UE contexts.
func GetAllUeCtx() []*MmeUeCtx {
	ctxMu.Lock()
	defer ctxMu.Unlock()
	out := make([]*MmeUeCtx, 0, len(ueContexts))
	for _, c := range ueContexts { out = append(out, c) }
	return out
}

// ---- Attach Procedure (TS 24.301 section 5.5.1) ----

// HandleAttachRequest processes an EPS Attach Request.
func HandleAttachRequest(ctx *MmeUeCtx, imsi string, attachType string) error {
	log := logger.Get("epc.mme.emm")
	log.Infof("Processing Attach Request imsi=%s type=%s", imsi, attachType)
	if imsi != "" { RegisterIMSI(ctx, imsi) }
	ctx.AttachType = attachType
	ctx.EMMState = "COMMON-PROCEDURE-INITIATED"
	ctx.ECMState = "CONNECTED"
	// Initiate authentication
	return InitiateAuthentication(ctx)
}

// GenerateAttachAccept builds and returns an Attach Accept response.
func GenerateAttachAccept(ctx *MmeUeCtx) map[string]interface{} {
	log := logger.Get("epc.mme.emm")
	ctx.EMMState = "REGISTERED"
	log.Infof("Attach Accept generated imsi=%s", ctx.IMSI)
	return map[string]interface{}{
		"message_type": "attach_accept",
		"imsi":         ctx.IMSI,
		"emm_state":    ctx.EMMState,
		"default_ebi":  ctx.DefaultEBI,
	}
}

// GenerateAttachReject builds an Attach Reject.
func GenerateAttachReject(ctx *MmeUeCtx, cause int) map[string]interface{} {
	log := logger.Get("epc.mme.emm")
	log.Infof("Attach Reject generated imsi=%s cause=%d", ctx.IMSI, cause)
	return map[string]interface{}{"message_type": "attach_reject", "cause": cause}
}

// ---- Detach (TS 24.301 section 5.5.2) ----

// HandleDetachRequest processes an EPS Detach Request.
func HandleDetachRequest(ctx *MmeUeCtx, detachType string, switchOff bool) {
	log := logger.Get("epc.mme.emm")
	log.Infof("Detach Request imsi=%s type=%s switchOff=%v", ctx.IMSI, detachType, switchOff)
	// Release bearers
	for ebi := range ctx.EPSBearers { delete(ctx.EPSBearers, ebi) }
	ctx.EMMState = "DEREGISTERED"
	ctx.ECMState = "IDLE"
	if !switchOff {
		// Send Detach Accept to UE
		log.Infof("Detach Accept sent imsi=%s", ctx.IMSI)
	}
}

// ---- Authentication (TS 33.401) ----

// InitiateAuthentication starts 4G EPS-AKA authentication.
func InitiateAuthentication(ctx *MmeUeCtx) error {
	log := logger.Get("epc.mme.emm")
	if ctx.IMSI == "" {
		log.Infof("No IMSI — requesting identity")
		return nil
	}
	log.Infof("Initiating EPS authentication imsi=%s", ctx.IMSI)
	// In production: call AUSF/UDM to get auth vectors (RAND, XRES, AUTN, KASME).
	// Simplified: mark auth done for testing.
	ctx.SecurityCtx.AuthDone = true
	return nil
}

// HandleAuthResponse processes Authentication Response from UE.
func HandleAuthResponse(ctx *MmeUeCtx, res []byte) bool {
	log := logger.Get("epc.mme.emm")
	ctx.SecurityCtx.AuthDone = true
	log.Infof("Authentication successful imsi=%s", ctx.IMSI)
	return true
}

// ---- Security Mode (TS 24.301 section 5.4.3) ----

// GenerateSecurityModeCommand builds a Security Mode Command.
func GenerateSecurityModeCommand(ctx *MmeUeCtx) map[string]interface{} {
	log := logger.Get("epc.mme.emm")
	log.Infof("Security Mode Command imsi=%s", ctx.IMSI)
	return map[string]interface{}{
		"message_type":     "security_mode_command",
		"replayed_ue_sec_cap": ctx.SecurityCtx.UESecCap,
	}
}

// HandleSecurityModeComplete processes Security Mode Complete from UE.
func HandleSecurityModeComplete(ctx *MmeUeCtx) {
	log := logger.Get("epc.mme.emm")
	log.Infof("Security Mode Complete imsi=%s", ctx.IMSI)
}

// ---- Identity (TS 24.301 section 5.4.4) ----

// GenerateIdentityRequest builds an Identity Request for IMSI.
func GenerateIdentityRequest() map[string]interface{} {
	return map[string]interface{}{"message_type": "identity_request", "identity_type": 1}
}

// HandleIdentityResponse processes Identity Response with IMSI.
func HandleIdentityResponse(ctx *MmeUeCtx, imsi string) {
	log := logger.Get("epc.mme.emm")
	RegisterIMSI(ctx, imsi)
	log.Infof("Identity Response: IMSI=%s", imsi)
}

// ---- TAU (TS 24.301 section 5.5.3) ----

// HandleTAURequest processes a Tracking Area Update Request.
func HandleTAURequest(ctx *MmeUeCtx, newTAI string) map[string]interface{} {
	log := logger.Get("epc.mme.emm")
	log.Infof("TAU Request imsi=%s newTAI=%s", ctx.IMSI, newTAI)
	ctx.TAI = newTAI
	ctx.ECMState = "CONNECTED"
	return map[string]interface{}{"message_type": "tau_accept", "tai": newTAI}
}

// ---- Service Request (TS 24.301 section 5.6.1) ----

// HandleServiceRequest processes a Service Request.
func HandleServiceRequest(ctx *MmeUeCtx) {
	log := logger.Get("epc.mme.emm")
	ctx.ECMState = "CONNECTED"
	log.Infof("Service Request imsi=%s -> CONNECTED", ctx.IMSI)
}

// ---- Handover (TS 23.401 section 5.5.1) ----

// HandleHandoverRequired processes Handover Required from source eNB.
func HandleHandoverRequired(ctx *MmeUeCtx, targetEnbIP string) map[string]interface{} {
	log := logger.Get("epc.mme.emm")
	ctx.HOState = "PREPARING"
	ctx.HOTargetEnbIP = targetEnbIP
	log.Infof("Handover Required imsi=%s -> target=%s", ctx.IMSI, targetEnbIP)
	return map[string]interface{}{"ho_state": "PREPARING", "target_enb": targetEnbIP}
}

// HandleHandoverNotify processes Handover Notify from target eNB.
func HandleHandoverNotify(ctx *MmeUeCtx) {
	log := logger.Get("epc.mme.emm")
	ctx.HOState = "COMPLETED"
	log.Infof("Handover Complete imsi=%s", ctx.IMSI)
}

// HandleHandoverCancel cancels a handover.
func HandleHandoverCancel(ctx *MmeUeCtx) {
	ctx.HOState = "CANCELLED"
}

// ---- NAS Message Router ----

// HandleNASMessage routes an uplink NAS message to the appropriate handler.
func HandleNASMessage(ctx *MmeUeCtx, msgType int, payload []byte) map[string]interface{} {
	log := logger.Get("epc.mme.emm")
	log.Infof("NAS message type=%d imsi=%s", msgType, ctx.IMSI)
	// EMM message types (TS 24.301 Table 9.8.1)
	switch {
	case msgType == 0x41: // Attach Request
		HandleAttachRequest(ctx, ctx.IMSI, "eps")
		return GenerateAttachAccept(ctx)
	case msgType == 0x45: // Detach Request
		HandleDetachRequest(ctx, "normal", false)
		return map[string]interface{}{"message_type": "detach_accept"}
	case msgType == 0x48: // TAU Request
		return HandleTAURequest(ctx, "")
	case msgType == 0x4C: // Service Request
		HandleServiceRequest(ctx)
		return map[string]interface{}{"message_type": "service_accept"}
	case msgType == 0x53: // Authentication Response
		HandleAuthResponse(ctx, payload)
		return GenerateSecurityModeCommand(ctx)
	case msgType == 0x5E: // Security Mode Complete
		HandleSecurityModeComplete(ctx)
		return GenerateAttachAccept(ctx)
	case msgType == 0x56: // Identity Response
		HandleIdentityResponse(ctx, string(payload))
		return map[string]interface{}{"message_type": "identity_received"}
	}
	return map[string]interface{}{"message_type": "unknown", "nas_type": msgType}
}

// ---- Status API ----

// Status returns current EMM state for the GUI panel.
func Status() map[string]any {
	log := logger.Get("emm")
	_ = log
	_ = engine.Open
	allUE := GetAllUeCtx()
	registered := 0
	for _, u := range allUE {
		if u.EMMState == "REGISTERED" { registered++ }
	}
	return map[string]any{
		"status":     "ready",
		"total_ues":  len(allUE),
		"registered": registered,
		"timestamp":  time.Now().Unix(),
	}
}

// ---- N26 Mapped Context ----

var (
	n26Mu      sync.Mutex
	n26Mapped  = make(map[string]*N26MappedCtx)
	n26TTL     = 120.0
)

// N26MappedCtx holds N26 mapped context from AMF for 5G->4G.
type N26MappedCtx struct {
	KASME      []byte
	EPSBearers []BearerInfo
	UEInfo     map[string]interface{}
	Timestamp  float64
	Used       bool
}

// ReceiveContextFromAMF stores N26 mapped context from AMF for 5G->4G.
func ReceiveContextFromAMF(imsi string, mappedKASME []byte, epsBearers []BearerInfo, ueInfo map[string]interface{}) map[string]interface{} {
	n26Mu.Lock()
	defer n26Mu.Unlock()
	n26Mapped[imsi] = &N26MappedCtx{KASME: mappedKASME, EPSBearers: epsBearers, UEInfo: ueInfo, Timestamp: float64(time.Now().Unix())}
	return map[string]interface{}{"status": "stored", "imsi": imsi}
}

// GetMappedContext retrieves N26 mapped context for a UE arriving from 5G.
func GetMappedContext(imsi string) *N26MappedCtx {
	n26Mu.Lock()
	defer n26Mu.Unlock()
	ctx := n26Mapped[imsi]
	if ctx == nil { return nil }
	age := float64(time.Now().Unix()) - ctx.Timestamp
	if age > n26TTL { delete(n26Mapped, imsi); return nil }
	if ctx.Used { return nil }
	return ctx
}

// ConsumeMappedContext marks N26 mapped context as consumed.
func ConsumeMappedContext(imsi string) *N26MappedCtx {
	n26Mu.Lock()
	defer n26Mu.Unlock()
	ctx := n26Mapped[imsi]
	delete(n26Mapped, imsi)
	return ctx
}

// GetN26Status returns current N26 mapped context counts.
func GetN26Status() map[string]interface{} {
	n26Mu.Lock()
	defer n26Mu.Unlock()
	now := float64(time.Now().Unix())
	active := 0
	expired := 0
	for _, c := range n26Mapped {
		if !c.Used && (now-c.Timestamp) < n26TTL { active++ } else { expired++ }
	}
	return map[string]interface{}{"pending_mapped_contexts": active, "expired_contexts": expired, "ttl_seconds": n26TTL}
}

// CleanupExpiredN26 removes expired N26 mapped contexts.
func CleanupExpiredN26() {
	n26Mu.Lock()
	defer n26Mu.Unlock()
	now := float64(time.Now().Unix())
	for imsi, c := range n26Mapped {
		if (now - c.Timestamp) >= n26TTL { delete(n26Mapped, imsi) }
	}
}

// ForwardContextToAMF forwards MME UE context to AMF for 4G->5G.
func ForwardContextToAMF(imsi string) map[string]interface{} {
	log := logger.Get("epc.mme.n26")
	ctx := SearchByIMSI(imsi)
	if ctx == nil { return map[string]interface{}{"success": false, "error": "No 4G UE context"} }
	if ctx.EMMState != "REGISTERED" { return map[string]interface{}{"success": false, "error": fmt.Sprintf("UE state %s", ctx.EMMState)} }
	log.Infof("4G->5G handover forwarded: IMSI=%s", imsi)
	ctx.EMMState = "DEREGISTERED"
	ctx.ECMState = "IDLE"
	return map[string]interface{}{"success": true, "imsi": imsi}
}
