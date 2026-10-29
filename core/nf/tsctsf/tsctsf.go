// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package tsctsf — Time Sensitive Communication and Time
// Synchronization Function (Rel-19).
//
// The TSCTSF (TS 23.501 §5.27.1.8) exposes 5GS time synchronization and
// TSC QoS toward AFs that are NOT TSN-aware (the TSN AF handles the
// IEEE 802.1Qcc/CNC world; the TSCTSF handles everything else: (g)PTP
// service activation, access-stratum time distribution, IP/Ethernet TSC
// QoS, DetNet).
//
// Service surface per TS 29.565 v19.7.0 (in-process form; Go function =
// service operation):
//
//	§6.1 Ntsctsf_TimeSynchronization  — CapsSubscribe/CapsNotify +
//	     ConfigCreate/Update/Delete for (g)PTP instance activation;
//	§6.2 Ntsctsf_QoSandTSCAssistance  — Create/Update/Delete of TSC app
//	     sessions (maps to Npcf_PolicyAuthorization → PCC rules);
//	§6.3 Ntsctsf_ASTI                 — 5G access stratum time
//	     distribution configuration per UE.
//
// Downstream the TSCTSF uses the PCF exactly like the TSN AF: dynamic
// TSC PCC rules (pcf.InstallTscPccRule) plus PMIC/UMIC pushes
// (smpolicy.PushTscManagement) with TS 24.539 payloads built from the
// ttmgmt codec.
package tsctsf

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/edge/tsn/ttmgmt"
	"github.com/mmt/mmt-studio-core/nf/pcf"
	"github.com/mmt/mmt-studio-core/nf/pcf/smpolicy"
	smfsm "github.com/mmt/mmt-studio-core/nf/pcf/smpolicy/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ---------------------------------------------------------------------------
// Ntsctsf_TimeSynchronization — TS 29.565 §6.1
// ---------------------------------------------------------------------------

// PtpPortConfig mirrors TS 29.565 §6.1.6.2.11 "ConfigForPort".
type PtpPortConfig struct {
	// SUPI of the UE/DS-TT this port config applies to; empty + N6Ind
	// selects the NW-TT N6 port.
	SUPI  string `json:"supi,omitempty"`
	N6Ind bool   `json:"n6_ind,omitempty"`
	// portDS.portEnable (IEEE 1588-2019).
	PtpEnable bool `json:"ptp_enable"`
	// Mean intervals as log2 values (IEEE 802.1AS-2020).
	LogSyncInterval     *int `json:"log_sync_interval,omitempty"`
	LogAnnounceInterval *int `json:"log_announce_interval,omitempty"`
}

// PtpInstanceReq mirrors TS 29.565 §6.1.6.2.10 "PtpInstance".
type PtpInstanceReq struct {
	// InstanceType — "PTP_RELAY" (802.1AS), "BOUNDARY_CLOCK",
	// "E2E_TRANS_CLOCK", "P2P_TRANS_CLOCK" (TS 23.501 §5.27.1.4).
	InstanceType string `json:"instance_type"`
	// Protocol — "PTP" | "GPTP".
	Protocol string `json:"protocol"`
	// PtpProfile — "smpte" | "8021as" | "default".
	PtpProfile  string          `json:"ptp_profile"`
	PortConfigs []PtpPortConfig `json:"port_configs,omitempty"`
}

// TimeSyncConfig mirrors TS 29.565 §6.1.6.2.9 "TimeSyncExposureConfig".
type TimeSyncConfig struct {
	ConfigID string `json:"config_id"`
	// upNodeId — target user plane node (NW-TT / 5GS bridge ID).
	UpNodeID uint64 `json:"up_node_id"`
	// DNN the target PDU sessions live on.
	DNN      string         `json:"dnn"`
	ReqPtpIns PtpInstanceReq `json:"req_ptp_ins"`
	// gmEnable / gmPrio — 5GS acts as (g)PTP grandmaster
	// (TS 23.501 §5.27.1.7).
	GmEnable bool   `json:"gm_enable"`
	GmPrio   uint8  `json:"gm_prio,omitempty"`
	// timeDom — (g)PTP domain number.
	TimeDom uint8 `json:"time_dom"`
	// timeSyncErrBdgt — Uu error budget in nanoseconds
	// (TS 23.501 §5.27.1.9).
	TimeSyncErrBdgtNS uint32 `json:"time_sync_err_bdgt_ns,omitempty"`

	// bookkeeping
	ptpInstanceID uint16
}

// TimeSyncCapability mirrors TS 29.565 §6.1.6.2.5 — what the TSCTSF
// exposes to AFs about a user plane node.
type TimeSyncCapability struct {
	UpNodeID    uint64   `json:"up_node_id"`
	GptpGmCap   bool     `json:"gptp_gm_capable"`
	PtpGmCap    bool     `json:"ptp_gm_capable"`
	DsttSupis   []string `json:"dstt_supis,omitempty"`
}

// ---------------------------------------------------------------------------
// Ntsctsf_QoSandTSCAssistance — TS 29.565 §6.2
// ---------------------------------------------------------------------------

// TscAppSession mirrors TS 29.565 §6.2.6.2.2
// "TscAppSessionContextData" (attributes in active use).
type TscAppSession struct {
	SessionID string `json:"session_id"`
	AfID      string `json:"af_id"`
	// UE addressing — one of: DS-TT MAC (Ethernet PDU session) or
	// SUPI+DNN (IP PDU session).
	UeMac string `json:"ue_mac,omitempty"`
	SUPI  string `json:"supi,omitempty"`
	DNN   string `json:"dnn,omitempty"`
	// tscQosReq — the TSC QoS requirement (TS 29.122 §5.14 shape).
	TscQosReq pcf.TscQosRequirement `json:"tsc_qos_req"`

	imsi  string
	pduID uint8
}

// RuleName derives the PCC rule id of the app session.
func (a *TscAppSession) RuleName() string { return "tsc_" + a.SessionID }

// ---------------------------------------------------------------------------
// Ntsctsf_ASTI — TS 29.565 §6.3
// ---------------------------------------------------------------------------

// AstiConfig mirrors TS 29.565 §6.3.6.2.2 "AccessTimeDistributionData" +
// §6.3.6.2.3 "AfAsTimeDistributionParam".
type AstiConfig struct {
	ConfigID string   `json:"config_id"`
	SUPIs    []string `json:"supis"`
	// asTimeDisEnabled — Uu time distribution active.
	AsTimeDisEnabled bool `json:"as_time_dis_enabled"`
	// timeSyncErrBdgt — nanoseconds.
	TimeSyncErrBdgtNS uint32 `json:"time_sync_err_bdgt_ns,omitempty"`
}

// ---------------------------------------------------------------------------
// Service
// ---------------------------------------------------------------------------

// Service is the TSCTSF instance.
type Service struct {
	mu sync.RWMutex

	// user plane node registry fed by PCF TSC events (§5.28.1 reports
	// reach the TSCTSF the same way they reach the TSN AF).
	upNodes map[uint64]*upNode

	timeSyncConfigs map[string]*TimeSyncConfig
	appSessions     map[string]*TscAppSession
	astiConfigs     map[string]*AstiConfig

	nextPtpInstance uint16
	started         bool
}

type upNode struct {
	id        uint64
	dsttSupis map[string]uint8 // SUPI (imsi) → PDU session id
	dnn       string
}

// Default is the singleton wired by the webservice bootstrap.
var Default = New()

// New builds a TSCTSF.
func New() *Service {
	return &Service{
		upNodes:         map[uint64]*upNode{},
		timeSyncConfigs: map[string]*TimeSyncConfig{},
		appSessions:     map[string]*TscAppSession{},
		astiConfigs:     map[string]*AstiConfig{},
		nextPtpInstance: 1,
	}
}

// Start subscribes to PCF TSC user plane events. Idempotent.
func (s *Service) Start() {
	s.mu.Lock()
	if s.started {
		s.mu.Unlock()
		return
	}
	s.started = true
	s.mu.Unlock()
	pcf.RegisterTscUplaneListener(s.onTscEvent)
	logger.Get("tsctsf").Infof("TSCTSF started (TS 23.501 §5.27.1.8, TS 29.565 Rel-19)")
}

func (s *Service) onTscEvent(ev pcf.TscUplaneEvent) {
	if ev.BridgeInfo == nil {
		return
	}
	s.mu.Lock()
	n := s.upNodes[ev.BridgeInfo.BridgeID]
	if n == nil {
		n = &upNode{id: ev.BridgeInfo.BridgeID, dsttSupis: map[string]uint8{}}
		s.upNodes[ev.BridgeInfo.BridgeID] = n
	}
	n.dsttSupis[ev.IMSI] = ev.PDUSessionID
	n.dnn = ev.DNN
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// Ntsctsf_TimeSynchronization operations
// ---------------------------------------------------------------------------

// Capabilities implements the read side of
// Ntsctsf_TimeSynchronization_CapsSubscribe (TS 29.565 §5.2.2.2): the
// time sync capabilities of every known user plane node.
func (s *Service) Capabilities() []TimeSyncCapability {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []TimeSyncCapability
	for _, n := range s.upNodes {
		cap := TimeSyncCapability{
			UpNodeID: n.id,
			// 5GS can always act as (g)PTP GM using the 5G internal
			// system clock (TS 23.501 §5.27.1.7).
			GptpGmCap: true,
			PtpGmCap:  true,
		}
		for supi := range n.dsttSupis {
			cap.DsttSupis = append(cap.DsttSupis, supi)
		}
		out = append(out, cap)
	}
	return out
}

// ConfigCreate implements Ntsctsf_TimeSynchronization_ConfigCreate
// (TS 29.565 §5.2.2.5): activates a (g)PTP instance in the DS-TT(s) and
// NW-TT of the target user plane node by provisioning PTP instance list
// PMICs (TS 23.501 §5.27.1.8 step "uses PCF and SMF to configure",
// TS 24.539 §9.15, TS 23.501 Annex K.2.2).
func (s *Service) ConfigCreate(cfg *TimeSyncConfig) error {
	log := logger.Get("tsctsf")
	if cfg.ConfigID == "" {
		return fmt.Errorf("tsctsf: config_id required")
	}
	s.mu.Lock()
	if _, dup := s.timeSyncConfigs[cfg.ConfigID]; dup {
		s.mu.Unlock()
		return fmt.Errorf("tsctsf: config %s exists", cfg.ConfigID)
	}
	node := s.upNodes[cfg.UpNodeID]
	cfg.ptpInstanceID = s.nextPtpInstance
	s.nextPtpInstance++
	s.timeSyncConfigs[cfg.ConfigID] = cfg
	s.mu.Unlock()
	if node == nil {
		return fmt.Errorf("tsctsf: unknown user plane node %d", cfg.UpNodeID)
	}

	instance := s.buildPtpInstance(cfg)
	pmic := ttmgmt.NewSetParam(ttmgmt.PortParamPTPInstanceList,
		ttmgmt.EncodePTPInstanceList([]ttmgmt.PTPInstance{instance}))

	// Provision every DS-TT named by the port configs (or all DS-TTs
	// of the node when none are named), plus the NW-TT.
	targets := s.dsttTargets(node, cfg.ReqPtpIns.PortConfigs)
	raw, err := pmic.Encode()
	if err != nil {
		return err
	}
	sent := 0
	for imsi, pduID := range targets {
		k := smfsm.Key{IMSI: imsi, PDUSessionID: pduID}
		if err := smpolicy.PushTscManagement(k,
			&pcf.PortManCont{Container: raw}, nil, nil); err != nil {
			log.Warnf("config %s: PMIC to DS-TT %s: %v", cfg.ConfigID, imsi, err)
			continue
		}
		sent++
	}
	// NW-TT leg: same PTP instance via the N4 path on any session.
	if imsi, pduID, ok := anyOf(targets); ok {
		k := smfsm.Key{IMSI: imsi, PDUSessionID: pduID}
		if err := smpolicy.PushTscManagement(k, nil,
			[]pcf.PortManCont{{Container: raw}}, nil); err != nil {
			log.Warnf("config %s: PMIC to NW-TT: %v", cfg.ConfigID, err)
		}
	}
	log.Infof("time sync config %s: PTP instance %d (%s/%s dom=%d gm=%v) provisioned to %d DS-TT(s) (TS 29.565 §5.2.2.5)",
		cfg.ConfigID, cfg.ptpInstanceID, cfg.ReqPtpIns.Protocol, cfg.ReqPtpIns.PtpProfile,
		cfg.TimeDom, cfg.GmEnable, sent)
	return nil
}

// ConfigDelete implements Ntsctsf_TimeSynchronization_ConfigDelete
// (TS 29.565 §5.2.2.7): removes the PTP instance from the translators
// (delete parameter-entry op on the PTP instance list).
func (s *Service) ConfigDelete(configID string) error {
	s.mu.Lock()
	cfg := s.timeSyncConfigs[configID]
	delete(s.timeSyncConfigs, configID)
	var node *upNode
	if cfg != nil {
		node = s.upNodes[cfg.UpNodeID]
	}
	s.mu.Unlock()
	if cfg == nil {
		return fmt.Errorf("tsctsf: unknown config %s", configID)
	}
	if node == nil {
		return nil
	}
	del := &ttmgmt.Message{
		Type: ttmgmt.MsgManagePortCommand,
		Ops: []ttmgmt.Operation{{
			Code:  ttmgmt.OpDeleteParameterEntry,
			Param: ttmgmt.PortParamPTPInstanceList,
			Value: ttmgmt.EncodePTPInstanceList([]ttmgmt.PTPInstance{{ID: cfg.ptpInstanceID}}),
		}},
	}
	raw, err := del.Encode()
	if err != nil {
		return err
	}
	for imsi, pduID := range s.dsttTargets(node, cfg.ReqPtpIns.PortConfigs) {
		k := smfsm.Key{IMSI: imsi, PDUSessionID: pduID}
		_ = smpolicy.PushTscManagement(k, &pcf.PortManCont{Container: raw}, nil, nil)
	}
	logger.Get("tsctsf").Infof("time sync config %s deleted (PTP instance %d)", configID, cfg.ptpInstanceID)
	return nil
}

// TimeSyncConfigs lists active configurations.
func (s *Service) TimeSyncConfigs() []*TimeSyncConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*TimeSyncConfig, 0, len(s.timeSyncConfigs))
	for _, c := range s.timeSyncConfigs {
		out = append(out, c)
	}
	return out
}

// buildPtpInstance maps the 29.565 PtpInstance request onto the
// TS 24.539 §9.15 wire parameters.
func (s *Service) buildPtpInstance(cfg *TimeSyncConfig) ttmgmt.PTPInstance {
	profile := ttmgmt.PTPProfileDefault
	switch cfg.ReqPtpIns.PtpProfile {
	case "smpte":
		profile = ttmgmt.PTPProfileSMPTE
	case "8021as":
		profile = ttmgmt.PTPProfile8021AS
	}
	transport := ttmgmt.PTPTransportEthernet
	if cfg.ReqPtpIns.Protocol == "PTP" {
		transport = ttmgmt.PTPTransportIPv4
	}
	// defaultDS.instanceType per IEEE 1588: OC=0, BC=1, P2P TC=2, E2E TC=3;
	// PTP Relay (802.1AS) rides the BC encoding with the 802.1AS profile.
	instType := byte(1)
	switch cfg.ReqPtpIns.InstanceType {
	case "P2P_TRANS_CLOCK":
		instType = 2
	case "E2E_TRANS_CLOCK":
		instType = 3
	}
	in := ttmgmt.PTPInstance{
		ID: cfg.ptpInstanceID,
		Params: []ttmgmt.PTPInstanceParam{
			{Name: ttmgmt.PTPParamProfile, Value: []byte{profile}},
			{Name: ttmgmt.PTPParamTransportType, Value: []byte{transport}},
			{Name: ttmgmt.PTPParamInstanceType, Value: []byte{instType}},
			{Name: ttmgmt.PTPParamDomainNumber, Value: []byte{cfg.TimeDom}},
			{Name: ttmgmt.PTPParamInstanceEnable, Value: ttmgmt.EncodeBool(true)},
			{Name: ttmgmt.PTPParamGrandmasterEnabled, Value: ttmgmt.EncodeBool(cfg.GmEnable)},
		},
	}
	if cfg.GmEnable && cfg.GmPrio > 0 {
		in.Params = append(in.Params, ttmgmt.PTPInstanceParam{
			Name: ttmgmt.PTPParamPriority1, Value: []byte{cfg.GmPrio}})
	}
	for _, pc := range cfg.ReqPtpIns.PortConfigs {
		if pc.LogSyncInterval != nil {
			in.Params = append(in.Params, ttmgmt.PTPInstanceParam{
				Name: ttmgmt.PTPParamLogSyncInterval, Value: []byte{byte(int8(*pc.LogSyncInterval))}})
		}
		if pc.LogAnnounceInterval != nil {
			in.Params = append(in.Params, ttmgmt.PTPInstanceParam{
				Name: ttmgmt.PTPParamLogAnnounceInterval, Value: []byte{byte(int8(*pc.LogAnnounceInterval))}})
		}
	}
	return in
}

// dsttTargets resolves the DS-TTs a config addresses: named SUPIs from
// the port configs, else every DS-TT of the node.
func (s *Service) dsttTargets(node *upNode, pcs []PtpPortConfig) map[string]uint8 {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := map[string]uint8{}
	named := false
	for _, pc := range pcs {
		if pc.SUPI != "" {
			named = true
			if pduID, ok := node.dsttSupis[pc.SUPI]; ok {
				out[pc.SUPI] = pduID
			}
		}
	}
	if !named {
		for supi, pduID := range node.dsttSupis {
			out[supi] = pduID
		}
	}
	return out
}

func anyOf(m map[string]uint8) (string, uint8, bool) {
	for k, v := range m {
		return k, v, true
	}
	return "", 0, false
}

// ---------------------------------------------------------------------------
// Ntsctsf_QoSandTSCAssistance operations
// ---------------------------------------------------------------------------

// AppSessionCreate implements Ntsctsf_QoSandTSCAssistance_Create
// (TS 29.565 §5.3.2.2): authorises TSC QoS for an AF session and maps
// it down to Npcf_PolicyAuthorization → PCC rule (§6.2.6 tscQosReq).
func (s *Service) AppSessionCreate(a *TscAppSession) error {
	if a.SessionID == "" {
		return fmt.Errorf("tsctsf: session_id required")
	}
	// Resolve the PDU session.
	var k smfsm.Key
	var ok bool
	switch {
	case a.UeMac != "":
		k, ok = smpolicy.FindAssociationByDsttMac(a.UeMac)
	case a.SUPI != "" && a.DNN != "":
		k, ok = smpolicy.FindAssociationByIMSIDNN(a.SUPI, a.DNN)
	}
	if !ok {
		return fmt.Errorf("tsctsf: no PDU session for app session %s (mac=%q supi=%q dnn=%q)",
			a.SessionID, a.UeMac, a.SUPI, a.DNN)
	}
	a.imsi, a.pduID = k.IMSI, k.PDUSessionID
	if a.DNN == "" {
		if assoc := smpolicy.GetAssociation(k); assoc != nil {
			// DNN travels inside the association context; leave as-is
			// when unknown (rule install requires it though).
		}
	}
	dnn := a.DNN
	if dnn == "" {
		dnn = "tsn"
	}

	pcf.InstallTscPccRule(a.imsi, dnn, a.RuleName(), a.TscQosReq)
	decision, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{"RES_MO_RE"},
	})
	if err != nil {
		pcf.RemoveTscPccRule(a.imsi, dnn, a.RuleName())
		return fmt.Errorf("tsctsf: N7 update: %w", err)
	}
	if err := smpolicy.PushNotify(k, decision); err != nil {
		logger.Get("tsctsf").Warnf("app session %s: UpdateNotify: %v", a.SessionID, err)
	}
	a.DNN = dnn

	s.mu.Lock()
	s.appSessions[a.SessionID] = a
	s.mu.Unlock()
	logger.Get("tsctsf").WithIMSI(a.imsi).Infof(
		"TSC app session %s created af=%s pdu=%d (TS 29.565 §5.3.2.2)", a.SessionID, a.AfID, a.pduID)
	return nil
}

// AppSessionDelete implements Ntsctsf_QoSandTSCAssistance_Delete
// (TS 29.565 §5.3.2.4).
func (s *Service) AppSessionDelete(sessionID string) error {
	s.mu.Lock()
	a := s.appSessions[sessionID]
	delete(s.appSessions, sessionID)
	s.mu.Unlock()
	if a == nil {
		return fmt.Errorf("tsctsf: unknown app session %s", sessionID)
	}
	removed := pcf.RemoveTscPccRule(a.imsi, a.DNN, a.RuleName())
	k := smfsm.Key{IMSI: a.imsi, PDUSessionID: a.pduID}
	if decision, err := smpolicy.Update(k, smpolicy.SmPolicyContextDataUpdate{
		Triggers: []string{"RES_RELEASE"},
	}); err == nil {
		if removed {
			decision.RemovedPccRules = append(decision.RemovedPccRules,
				pcf.PCCRule{ServiceName: a.RuleName()})
		}
		_ = smpolicy.PushNotify(k, decision)
	}
	logger.Get("tsctsf").WithIMSI(a.imsi).Infof("TSC app session %s deleted", sessionID)
	return nil
}

// AppSessions lists active TSC app sessions.
func (s *Service) AppSessions() []*TscAppSession {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*TscAppSession, 0, len(s.appSessions))
	for _, a := range s.appSessions {
		out = append(out, a)
	}
	return out
}

// ---------------------------------------------------------------------------
// Ntsctsf_ASTI operations
// ---------------------------------------------------------------------------

// AstiCreate implements Ntsctsf_ASTI_Create (TS 29.565 §5.4.2.2):
// stores the access-stratum time distribution parameters for the target
// UEs. Enforcement toward NG-RAN (Uu error budget signalling via AMF,
// TS 23.501 §5.27.1.9) picks the config up from AstiFor.
func (s *Service) AstiCreate(cfg *AstiConfig) error {
	if cfg.ConfigID == "" || len(cfg.SUPIs) == 0 {
		return fmt.Errorf("tsctsf: config_id and supis required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.astiConfigs[cfg.ConfigID]; dup {
		return fmt.Errorf("tsctsf: ASTI config %s exists", cfg.ConfigID)
	}
	s.astiConfigs[cfg.ConfigID] = cfg
	logger.Get("tsctsf").Infof("ASTI config %s: %d UE(s) enabled=%v errBudget=%dns (TS 29.565 §5.4.2.2)",
		cfg.ConfigID, len(cfg.SUPIs), cfg.AsTimeDisEnabled, cfg.TimeSyncErrBdgtNS)
	return nil
}

// AstiUpdate implements Ntsctsf_ASTI_Update (§5.4.2.3).
func (s *Service) AstiUpdate(cfg *AstiConfig) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.astiConfigs[cfg.ConfigID]; !ok {
		return fmt.Errorf("tsctsf: unknown ASTI config %s", cfg.ConfigID)
	}
	s.astiConfigs[cfg.ConfigID] = cfg
	return nil
}

// AstiDelete implements Ntsctsf_ASTI_Delete (§5.4.2.4).
func (s *Service) AstiDelete(configID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.astiConfigs[configID]; !ok {
		return fmt.Errorf("tsctsf: unknown ASTI config %s", configID)
	}
	delete(s.astiConfigs, configID)
	return nil
}

// AstiFor returns the active ASTI parameters for a UE (§5.4.2.5 Get /
// the AMF-side enforcement lookup). ok=false when the UE has no config.
func (s *Service) AstiFor(supi string) (enabled bool, errBudgetNS uint32, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, c := range s.astiConfigs {
		for _, u := range c.SUPIs {
			if u == supi {
				return c.AsTimeDisEnabled, c.TimeSyncErrBdgtNS, true
			}
		}
	}
	return false, 0, false
}

// AstiConfigs lists ASTI configurations.
func (s *Service) AstiConfigs() []*AstiConfig {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*AstiConfig, 0, len(s.astiConfigs))
	for _, c := range s.astiConfigs {
		out = append(out, c)
	}
	return out
}

// Status summarises TSCTSF state for the GUI / REST surface.
func (s *Service) Status() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"up_nodes":          len(s.upNodes),
		"time_sync_configs": len(s.timeSyncConfigs),
		"app_sessions":      len(s.appSessions),
		"asti_configs":      len(s.astiConfigs),
	}
}
