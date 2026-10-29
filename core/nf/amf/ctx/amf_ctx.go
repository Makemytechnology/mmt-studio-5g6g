// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ctx — AMF global context per TS 23.501 §6.2.1.
//
// Go port of nf/amf/amf_ctx.py. Holds AMF identity (name, GUAMI list,
// capacity, PLMN support list) loaded from network_config + supported_plmns.
// The GUAMI list is used in NGAP PDU construction (NG Setup Response, Initial
// Context Setup Request) and in NAS 5G-GUTI derivation.
package ctx

import (
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// GUAMI — Globally Unique AMF Identifier (TS 23.003 §2.10.1).
//
//	PLMNID (3 bytes, BCD-packed) + AMFRegionID (1 byte) + AMFSetID (10 bits) + AMFPointer (6 bits)
type GUAMI struct {
	PLMNID      []byte // 3 bytes
	AMFRegionID uint8
	AMFSetID    uint16 // 10-bit value
	AMFPointer  uint8  // 6-bit value
}

// SNSSAI — TS 38.413 §9.3.1.24.
type SNSSAI struct {
	SST uint8
	SD  []byte // 3 bytes, optional
}

// PLMNSupport — one entry per served PLMN with its slice list.
type PLMNSupport struct {
	PLMNID []byte // 3 bytes
	Slices []SNSSAI
}

// AlgoPriority is one entry from the security_algorithms table.
type AlgoPriority struct {
	Algorithm string // "NEA0".."NEA3" or "NIA0".."NIA3"
	AlgoID    uint8  // 0..3
	Priority  int
}

// NetworkFeatureSupport carries every flag from the network_config row
// that maps 1:1 to a bit in TS 24.501 §9.11.3.5 "5GS network feature
// support". Values are 0/1 for single-bit fields and 0..3 for the
// two-bit fields (EMF, EMC, RestrictEC). The DB is the source of truth;
// Encode() packs the flags into the two-octet wire value.
type NetworkFeatureSupport struct {
	// Octet 3 (byte 1)
	MPSI          uint8 // bit 7 — Access identity 1 valid in RPLMN
	IWKN26        uint8 // bit 6 — Interworking without N26
	EMF           uint8 // bits 5-4 — Emergency services fallback (0..3)
	EMC           uint8 // bits 3-2 — Emergency services support (0..3)
	IMSVoPSN3GPP  uint8 // bit 1 — IMS voice over PS (non-3GPP access)
	IMSVoPS3GPP   uint8 // bit 0 — IMS voice over PS (3GPP access)

	// Octet 4 (byte 2, optional)
	UPCIoT     uint8 // bit 7 — User-plane CIoT 5GS optimisation
	IPHCCPCIoT uint8 // bit 6 — IP header compression for CP CIoT
	N3Data     uint8 // bit 5 — N3 data transfer
	CPCIoT     uint8 // bit 4 — Control-plane CIoT 5GS optimisation
	RestrictEC uint8 // bits 3-2 — Restriction on enhanced coverage (0..3)
	MCSI       uint8 // bit 1 — Access identity 2 valid
	EMCN3      uint8 // bit 0 — Emergency services over non-3GPP access
}

// Encode packs the NFS flags into the two-octet wire representation.
// Byte 3 of the IE (further R17+ flags) is not modelled and omitted.
func (n NetworkFeatureSupport) Encode() []byte {
	b1 := byte(
		(n.MPSI&1)<<7 |
			(n.IWKN26&1)<<6 |
			(n.EMF&3)<<4 |
			(n.EMC&3)<<2 |
			(n.IMSVoPSN3GPP&1)<<1 |
			(n.IMSVoPS3GPP & 1))
	b2 := byte(
		(n.UPCIoT&1)<<7 |
			(n.IPHCCPCIoT&1)<<6 |
			(n.N3Data&1)<<5 |
			(n.CPCIoT&1)<<4 |
			(n.RestrictEC&3)<<2 |
			(n.MCSI&1)<<1 |
			(n.EMCN3 & 1))
	return []byte{b1, b2}
}

// AMF is the process-wide AMF context. Mutated at startup via Initialize,
// then read-only for the lifetime of the process.
type AMF struct {
	mu                   sync.RWMutex
	initialized          bool
	name                 string
	ip                   string // network_config.amf_ip — the address NGAP SCTP binds to
	sctpPort             int    // network_config.sctp_port — NGAP transport port (TS 38.412 §7)
	guamiList            []GUAMI
	relativeAMFCapacity  uint8
	plmnSupportList      []PLMNSupport
	cipheringAlgos       []AlgoPriority // ordered by priority (ascending)
	integrityAlgos       []AlgoPriority // ordered by priority (ascending)
	nfs                  NetworkFeatureSupport
}

// Default is the process-wide singleton.
var Default = &AMF{}

// Initialize rebuilds the context from a configuration snapshot.
// Called once at startup. Subsequent callers must use Name/GUAMIList/etc.
func (a *AMF) Initialize(name string, capacity uint8, guami []GUAMI, plmns []PLMNSupport, ciph, integ []AlgoPriority, nfs NetworkFeatureSupport) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.name = name
	if a.name == "" {
		a.name = "MMT-CORE"
	}
	a.relativeAMFCapacity = capacity
	a.guamiList = append(a.guamiList[:0], guami...)
	a.plmnSupportList = append(a.plmnSupportList[:0], plmns...)
	a.cipheringAlgos = append(a.cipheringAlgos[:0], ciph...)
	a.integrityAlgos = append(a.integrityAlgos[:0], integ...)
	a.nfs = nfs
	a.initialized = true

	log := logger.Get("amf.ctx")
	if len(a.guamiList) == 0 {
		log.Warn("No supported PLMNs — AMF has no GUAMI")
	}
	log.Infof("AMF context: %d GUAMI(s), %d PLMN(s)",
		len(a.guamiList), len(a.plmnSupportList))
}

// Name returns the configured AMF name.
func (a *AMF) Name() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.name
}

// SetIP stores the NGAP bind IP from network_config.amf_ip. Called
// during InitContextFromDB; empty value means "no preference — fall
// back to auto-pick".
func (a *AMF) SetIP(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ip = ip
}

// IP returns the NGAP bind IP from network_config.amf_ip. Empty when
// the DB hasn't been read yet or the column is blank.
func (a *AMF) IP() string {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.ip
}

// SetSCTPPort stores the NGAP transport port from network_config.sctp_port.
// 0 means "no preference — use the spec default". TS 38.412 §7.
func (a *AMF) SetSCTPPort(p int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.sctpPort = p
}

// SCTPPort returns the operator-configured NGAP port from
// network_config.sctp_port. Returns 0 if not yet loaded — callers fall
// back to the spec default 38412 in that case.
func (a *AMF) SCTPPort() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.sctpPort
}

// Capacity returns the configured relativeAMFCapacity (TS 38.413 §9.3.1.10).
func (a *AMF) Capacity() uint8 {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.relativeAMFCapacity
}

// GUAMIList returns a copy of the served GUAMI list.
func (a *AMF) GUAMIList() []GUAMI {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]GUAMI, len(a.guamiList))
	copy(out, a.guamiList)
	return out
}

// NFS returns the configured 5GS network feature support flags, loaded
// from network_config at startup.
func (a *AMF) NFS() NetworkFeatureSupport {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.nfs
}

// PLMNSupportList returns a copy of the configured PLMN support list.
func (a *AMF) PLMNSupportList() []PLMNSupport {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]PLMNSupport, len(a.plmnSupportList))
	copy(out, a.plmnSupportList)
	return out
}

// CipheringAlgos returns the configured ciphering algorithm priority list.
func (a *AMF) CipheringAlgos() []AlgoPriority {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AlgoPriority, len(a.cipheringAlgos))
	copy(out, a.cipheringAlgos)
	return out
}

// IntegrityAlgos returns the configured integrity algorithm priority list.
func (a *AMF) IntegrityAlgos() []AlgoPriority {
	a.mu.RLock()
	defer a.mu.RUnlock()
	out := make([]AlgoPriority, len(a.integrityAlgos))
	copy(out, a.integrityAlgos)
	return out
}

// GUAMIForPLMN returns the GUAMI for a specific PLMN identity, or the first
// registered GUAMI if no exact match is found. Returns nil only when no
// GUAMI is configured at all.
func (a *AMF) GUAMIForPLMN(plmnID []byte) *GUAMI {
	a.mu.RLock()
	defer a.mu.RUnlock()
	for i := range a.guamiList {
		if bytesEqual(a.guamiList[i].PLMNID, plmnID) {
			cp := a.guamiList[i]
			return &cp
		}
	}
	if len(a.guamiList) > 0 {
		cp := a.guamiList[0]
		return &cp
	}
	return nil
}

// Initialized reports whether Initialize has been called.
func (a *AMF) Initialized() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.initialized
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
