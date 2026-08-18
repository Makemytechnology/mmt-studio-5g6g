// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package epc — EPC interworking: MME + S1AP + 4G NAS.
//
// Go port of access/epc/mme/. The MME handles S1AP (TS 36.413) from eNBs
// and EPS NAS (TS 24.301) from LTE UEs. In the monolithic SA Core build
// the MME shares the same database and UE context store as the AMF, which
// lets 5G↔4G handover (N26 / SRVCC) reference the same subscriber state.
//
// This file covers the MME's global context + attach/detach lifecycle stubs.
// S1AP decode/encode follows a similar pattern to the NGAP codec integration
// already done for the AMF; the S1AP ASN.1 module is already compiled in
// codecs/asn1-go/protocols/s1ap but the resolver gap (same as NGAP was) needs
// closing before real message round-trips work.
//
// For now the EPC panel in the GUI calls /api/epc/status which returns
// the MME state; actual S1AP interop is a Phase-N item.
package epc

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// MME is the global MME context.
type MME struct {
	mu        sync.RWMutex
	Name      string
	MMEGI     uint16 // MME Group ID
	MMEC      uint8  // MME Code
	S1APAddr  string // listen address for S1AP
	Running   bool
	StartedAt time.Time

	// Connected eNBs.
	ENBs map[string]*ENBCtx
}

// ENBCtx is a connected eNB.
type ENBCtx struct {
	ENBIP     string
	ENBName   string
	ENBID     string
	Connected bool
	TACs      []string
}

// Default is the process-wide MME instance.
var Default = &MME{
	Name: "sacore-mme",
	ENBs: make(map[string]*ENBCtx),
}

// Status returns the MME state for the EPC GUI panel.
func (m *MME) Status() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()
	enbs := make([]map[string]any, 0, len(m.ENBs))
	for _, e := range m.ENBs {
		enbs = append(enbs, map[string]any{
			"ip":        e.ENBIP,
			"name":      e.ENBName,
			"id":        e.ENBID,
			"connected": e.Connected,
			"tacs":      e.TACs,
		})
	}
	return map[string]any{
		"name":        m.Name,
		"mmegi":       m.MMEGI,
		"mmec":        m.MMEC,
		"s1ap_addr":   m.S1APAddr,
		"running":     m.Running,
		"enb_count":   len(m.ENBs),
		"enbs":        enbs,
	}
}

// Start initialises the MME (future: S1AP listener).
func (m *MME) Start(addr string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.S1APAddr = addr
	m.Running = true
	m.StartedAt = time.Now()
	logger.Get("epc.mme").Infof("MME started name=%s addr=%s", m.Name, addr)
}
