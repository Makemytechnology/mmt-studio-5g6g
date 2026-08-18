// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package n2

import (
	"net"
	"sync"
)

// BridgeAdapter wraps a Manager into the handler.NASBridge interface
// (defined in nf/n3iwf/handler) without forcing the handler package
// to import nf/n3iwf/n2. The adapter holds a small per-UE map that
// translates the n2-side OnDownlinkNAS(*DownlinkNAS) callback into
// the handler-side onDL(nas, amfUEID) signature.
//
// Usage at startup:
//
//	mgr, err := n2.NewManager(ctx, cfg)
//	if err != nil { ... }
//	h := handler.New(ueMgr, "n3iwf.example.com")
//	h.SetBridge(n2.NewBridgeAdapter(mgr))
//
// The adapter doesn't itself implement handler.NASBridge — that
// would require importing nf/n3iwf/handler, creating a cycle. Instead
// it exposes the raw methods the handler.NASBridge interface defines,
// and the handler package's interface is structurally satisfied by
// any value with these methods. (Go interfaces are duck-typed —
// importing the interface declaration is not required for satisfaction.)
type BridgeAdapter struct {
	mgr *Manager

	mu sync.Mutex
	// onDL[ranUEID] / onICS[ranUEID] are the handler-side callbacks
	// registered via RegisterUE. The adapter wraps them in
	// OnDownlinkNAS / OnInitialContextSetup shims so the n2 layer
	// keeps its richer DownlinkNAS / InitialContextSetup structs
	// internally.
	onDL  map[uint32]func(nas []byte, amfUEID uint64)
	onICS map[uint32]func(amfUEID uint64, knh []byte, piggybackedNAS []byte)
}

// NewBridgeAdapter wraps an existing Manager. The Manager must
// already have completed NG Setup (NewManager returned successfully).
func NewBridgeAdapter(m *Manager) *BridgeAdapter {
	return &BridgeAdapter{
		mgr:   m,
		onDL:  map[uint32]func([]byte, uint64){},
		onICS: map[uint32]func(uint64, []byte, []byte){},
	}
}

// AllocateRANUEID delegates to the Manager.
func (a *BridgeAdapter) AllocateRANUEID() uint32 { return a.mgr.AllocateRANUEID() }

// RegisterUE installs the handler-side onDL + onICS callbacks.
// Internally the adapter wires Manager.RegisterUE with shim
// callbacks that extract the fields the handler cares about
// (NAS-PDU + AMF-UE-NGAP-ID for DL NAS; AMF-UE-NGAP-ID + Knh +
// optional piggybacked NAS for ICS) from the richer n2 structs.
func (a *BridgeAdapter) RegisterUE(ranUEID uint32,
	onDL func(nas []byte, amfUEID uint64),
	onICS func(amfUEID uint64, knh []byte, piggybackedNAS []byte)) {
	a.mu.Lock()
	a.onDL[ranUEID] = onDL
	a.onICS[ranUEID] = onICS
	a.mu.Unlock()
	a.mgr.RegisterUE(&UENGAPCtx{
		RANUEID: ranUEID,
		OnDownlinkNAS: func(dl *DownlinkNAS) {
			a.mu.Lock()
			cb := a.onDL[ranUEID]
			a.mu.Unlock()
			if cb != nil && dl != nil {
				cb(dl.NASPDU, dl.AMFUENGAPID)
			}
		},
		OnInitialContextSetup: func(ics *InitialContextSetup) {
			a.mu.Lock()
			cb := a.onICS[ranUEID]
			a.mu.Unlock()
			if cb != nil && ics != nil {
				cb(ics.AMFUENGAPID, ics.Knh, ics.NASPDU)
			}
		},
	})
}

// UnregisterUE removes both the adapter's callback tables and the
// Manager's dispatch slot.
func (a *BridgeAdapter) UnregisterUE(ranUEID uint32) {
	a.mu.Lock()
	delete(a.onDL, ranUEID)
	delete(a.onICS, ranUEID)
	a.mu.Unlock()
	a.mgr.UnregisterUE(ranUEID)
}

// SendInitialUEMessage builds the §9.2.5.3 PDU from the handler's
// flat (ranUEID, nas, ueIP, uePort, plmn) parameter set.
func (a *BridgeAdapter) SendInitialUEMessage(ranUEID uint32, nas []byte, ueIP net.IP, uePort uint16, plmn []byte) error {
	return a.mgr.SendInitialUEMessage(&InitialUEMessageConfig{
		RANUENGAPID: ranUEID,
		NASPDU:      nas,
		UEOuterIP:   ueIP,
		UEOuterPort: uePort,
		PLMNID:      plmn,
	})
}

// SendUplinkNAS delegates to the Manager.
func (a *BridgeAdapter) SendUplinkNAS(amfUEID uint64, ranUEID uint32, nas []byte, ueIP net.IP, uePort uint16) error {
	return a.mgr.SendUplinkNAS(amfUEID, ranUEID, nas, ueIP, uePort)
}

// SendInitialContextSetupResponse delegates to the Manager.
func (a *BridgeAdapter) SendInitialContextSetupResponse(amfUEID uint64, ranUEID uint32) error {
	return a.mgr.SendInitialContextSetupResponse(amfUEID, ranUEID)
}
