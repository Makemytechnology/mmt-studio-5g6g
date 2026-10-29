// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Manager-level TSC surface (Rel-19): 5GS bridge information and TSC
// Management Information transport toward the UPF bridge implementation
// (TS 29.244 IEs 194/195 and 199/200).
//
// The methods degrade to no-ops when the active UPFBridge does not
// implement the optional TSCBridge interface (e.g. the raw cgo DPDK
// bridge) — same optional-capability pattern as SetBridge.
package upf

import "fmt"

// TSCMgmtEntry is one container of a TSC Management Information
// exchange (TS 29.244 §7.5.4.18): either a PMIC addressed to an NW-TT
// port or a user-plane-node-level UMIC.
type TSCMgmtEntry struct {
	PMIC     []byte
	NWTTPort uint16
	UMIC     []byte
}

// TSCGateConfig is the IEEE 802.1Qbv schedule pushed into the
// dataplane egress gate (TS 23.501 §5.27.4; TS 24.539 parameters
// GateEnabled / AdminBaseTime / AdminControlList / AdminCycleTime).
type TSCGateConfig struct {
	QFI          uint8  // 0 = whole session
	TrafficClass uint8  // bit checked in each gate-states octet
	BaseTimeNS   uint64 // AdminBaseTime, epoch ns
	CycleTimeNS  uint64 // AdminCycleTime
	GateStates   []byte // per entry: bit N = traffic class N open
	DurationsNS  []uint32
}

// TSCGateBridge is the optional dataplane capability for the Rel-19
// egress gate + Ethernet MAC learning — implemented by the cgo DPDK
// bridge (cgo_bridge_linux.go); absent on the wire-only PFCP bridge.
type TSCGateBridge interface {
	SetTSCGate(imsi string, pduSessionID uint8, cfg TSCGateConfig) error
	ClearTSCGate(imsi string, pduSessionID uint8) error
	LearnMAC(imsi string, pduSessionID uint8, mac [6]byte) error
	// SetPTPSteering toggles the §5.27.1.2.2 fast-path (g)PTP
	// processing (upf_ptp.c) for a session.
	SetPTPSteering(imsi string, pduSessionID uint8, enable bool) error
}

// SetTSCGate programs the dataplane gate; no-op error when the active
// bridge has no dataplane gate support.
func (m *Manager) SetTSCGate(imsi string, pduSessionID uint8, cfg TSCGateConfig) error {
	if tb, ok := bridge.(TSCGateBridge); ok && bridge != nil {
		return tb.SetTSCGate(imsi, pduSessionID, cfg)
	}
	return fmt.Errorf("upf: active bridge has no dataplane TSC gate")
}

// ClearTSCGate deactivates the dataplane gate.
func (m *Manager) ClearTSCGate(imsi string, pduSessionID uint8) error {
	if tb, ok := bridge.(TSCGateBridge); ok && bridge != nil {
		return tb.ClearTSCGate(imsi, pduSessionID)
	}
	return nil
}

// LearnMAC records a UE MAC in the dataplane session (Ethernet PDU
// sessions, TS 29.244 §8.2.98).
func (m *Manager) LearnMAC(imsi string, pduSessionID uint8, mac [6]byte) error {
	if tb, ok := bridge.(TSCGateBridge); ok && bridge != nil {
		return tb.LearnMAC(imsi, pduSessionID, mac)
	}
	return nil
}

// SetPTPSteering toggles fast-path (g)PTP steering for a session.
func (m *Manager) SetPTPSteering(imsi string, pduSessionID uint8, enable bool) error {
	if tb, ok := bridge.(TSCGateBridge); ok && bridge != nil {
		return tb.SetPTPSteering(imsi, pduSessionID, enable)
	}
	return nil
}

// TSCBridge is the optional bridge capability for Rel-19 TSC:
// implemented by the PFCP bridge (smf/upfclient) which carries the IEs
// on the wire; not implemented by the raw cgo dataplane bridge.
type TSCBridge interface {
	// RequestBridgeInfo arms the Create Bridge Info for TSC IE
	// (type 194, BII=1) on the next CommitSession for this session
	// (TS 23.502 §4.3.2.2.1 step 10a).
	RequestBridgeInfo(imsi string, pduSessionID uint8) error
	// BridgeInfo returns the Created Bridge Info the UPF sent back in
	// the Session Establishment Response (IE 195): DS-TT port number,
	// NW-TT port numbers and the 5GS User Plane Node ID (step 10b).
	BridgeInfo(imsi string, pduSessionID uint8) (dsttPort uint16, nwttPorts []uint16, nodeID uint64, ok bool)
	// SendTSCManagementInformation ships PMIC/UMIC containers in a
	// Session Modification Request (IE 199) and returns any containers
	// the UPF answered with (IE 200).
	SendTSCManagementInformation(imsi string, pduSessionID uint8, entries []TSCMgmtEntry) ([]TSCMgmtEntry, error)
}

// RequestBridgeInfo arms the bridge-information request for the next
// session commit. No-op (nil) when the bridge lacks TSC support.
func (m *Manager) RequestBridgeInfo(imsi string, pduSessionID uint8) error {
	if tb, ok := bridge.(TSCBridge); ok && bridge != nil {
		return tb.RequestBridgeInfo(imsi, pduSessionID)
	}
	return nil
}

// BridgeInfo reads the Created Bridge Info of an established session.
func (m *Manager) BridgeInfo(imsi string, pduSessionID uint8) (uint16, []uint16, uint64, bool) {
	if tb, ok := bridge.(TSCBridge); ok && bridge != nil {
		return tb.BridgeInfo(imsi, pduSessionID)
	}
	return 0, nil, 0, false
}

// SendTSCManagementInformation forwards PMIC/UMIC containers to the
// user plane over N4 (TS 29.244 §7.5.4.18).
func (m *Manager) SendTSCManagementInformation(imsi string, pduSessionID uint8, entries []TSCMgmtEntry) ([]TSCMgmtEntry, error) {
	if tb, ok := bridge.(TSCBridge); ok && bridge != nil {
		return tb.SendTSCManagementInformation(imsi, pduSessionID, entries)
	}
	return nil, fmt.Errorf("upf: active bridge has no TSC support")
}
