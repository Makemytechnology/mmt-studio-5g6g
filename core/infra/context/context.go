// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package context — global UE context store (TS 23.502 §5.2).
//
// Go port of infra/context/. Thread-safe singleton that holds per-NF UE
// context bundles indexed by IMSI, AMF-UE-NGAP-ID, and 5G-GUTI. Each UE
// has at most one context per NF (AMF / SMF / PCF / IMS).
//
// In the Go architecture, AMF UE contexts live in nf/amf/uectx and SMF
// sessions live in nf/smf/session. This package is the cross-NF index
// layer that the Python reference calls from the health watchdog,
// lifecycle shutdown, and the Contexts GUI panel.
package context

import (
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Bundle groups per-NF context references for one UE. NF-specific struct
// pointers are typed as `any` because the NF packages live in separate
// modules and we want to avoid import cycles. Callers type-assert to the
// concrete type they need (e.g., *uectx.AmfUeCtx, *session.Session).
type Bundle struct {
	AMF any // *uectx.AmfUeCtx
	SMF any // *session.Session or a SmfUeContext from a future port
	PCF any
	IMS any
}

// Store is the process-wide UE context registry.
type Store struct {
	mu       sync.RWMutex
	byIMSI   map[string]*Bundle
	byAmfID  map[int64]string // amf_ue_ngap_id → IMSI
	byGUTI   map[gutiKey]string
}

type gutiKey struct {
	PLMN       string
	RegionID   uint8
	SetID      uint16
	Pointer    uint8
	TMSI       uint32
}

// Default is the package-level singleton.
var Default = NewStore()

// NewStore returns an empty context store.
func NewStore() *Store {
	return &Store{
		byIMSI:  make(map[string]*Bundle),
		byAmfID: make(map[int64]string),
		byGUTI:  make(map[gutiKey]string),
	}
}

// GetOrCreate returns the bundle for an IMSI, creating it on first access.
func (s *Store) GetOrCreate(imsi string) *Bundle {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, ok := s.byIMSI[imsi]
	if !ok {
		b = &Bundle{}
		s.byIMSI[imsi] = b
		logger.Get("infra.context").Debugf("context bundle created imsi=%s", imsi)
	}
	return b
}

// GetByIMSI returns a bundle or nil.
func (s *Store) GetByIMSI(imsi string) *Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.byIMSI[imsi]
}

// GetByAmfID resolves AMF-UE-NGAP-ID → IMSI → bundle.
func (s *Store) GetByAmfID(amfUeID int64) *Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if imsi, ok := s.byAmfID[amfUeID]; ok {
		return s.byIMSI[imsi]
	}
	return nil
}

// GetByGUTI resolves a 5G-GUTI → IMSI → bundle.
func (s *Store) GetByGUTI(plmn string, region uint8, set uint16, ptr uint8, tmsi uint32) *Bundle {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if imsi, ok := s.byGUTI[gutiKey{plmn, region, set, ptr, tmsi}]; ok {
		return s.byIMSI[imsi]
	}
	return nil
}

// IndexAmfID adds AMF-UE-NGAP-ID → IMSI.
func (s *Store) IndexAmfID(amfUeID int64, imsi string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byAmfID[amfUeID] = imsi
}

// IndexGUTI adds a GUTI → IMSI mapping.
func (s *Store) IndexGUTI(plmn string, region uint8, set uint16, ptr uint8, tmsi uint32, imsi string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byGUTI[gutiKey{plmn, region, set, ptr, tmsi}] = imsi
}

// Remove drops all context + indices for a UE.
func (s *Store) Remove(imsi string) *Bundle {
	s.mu.Lock()
	defer s.mu.Unlock()
	b := s.byIMSI[imsi]
	delete(s.byIMSI, imsi)
	for id, i := range s.byAmfID {
		if i == imsi {
			delete(s.byAmfID, id)
		}
	}
	for k, i := range s.byGUTI {
		if i == imsi {
			delete(s.byGUTI, k)
		}
	}
	return b
}

// AllIMSIs returns every tracked IMSI. Used by diagnostics + shutdown sweep.
func (s *Store) AllIMSIs() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]string, 0, len(s.byIMSI))
	for imsi := range s.byIMSI {
		out = append(out, imsi)
	}
	return out
}

// Count returns the number of tracked UEs.
func (s *Store) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.byIMSI)
}

// Summary returns store metrics for the /api/health detail panel.
func (s *Store) Summary() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return map[string]any{
		"ue_count":          len(s.byIMSI),
		"amf_id_index_size": len(s.byAmfID),
		"guti_index_size":   len(s.byGUTI),
	}
}

// Clear resets the store — used by tests + full-reset operator action.
func (s *Store) Clear() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byIMSI = make(map[string]*Bundle)
	s.byAmfID = make(map[int64]string)
	s.byGUTI = make(map[gutiKey]string)
}
