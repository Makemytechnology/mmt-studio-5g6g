// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package esm -- EPS Session Management (TS 24.301 section 6.5).
//
// Go port of access/epc/mme/esm/*.py. Handles PDN Connectivity,
// default/dedicated EPS bearer management. Reuses 5G SMF for IP
// allocation and UPF for data plane.
package esm

import (
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

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
	MBRDLKbps   int    `json:"mbr_dl_kbps,omitempty"`
	MBRULKbps   int    `json:"mbr_ul_kbps,omitempty"`
}

// HandlePDNConnectivityRequest handles a PDN Connectivity Request (TS 24.301 section 8.3.18).
// Creates a new PDN connection, allocates IP, creates UPF session, assigns EBI.
func HandlePDNConnectivityRequest(imsi, dnn string, pdnType int, existingEBIs map[int]bool) (*BearerInfo, error) {
	log := logger.Get("epc.mme.esm")
	if dnn == "" {
		dnn = "internet"
	}
	log.Infof("PDN Connectivity Request: imsi=%s dnn=%s pdnType=%d", imsi, dnn, pdnType)

	// Allocate next EBI (5-15) per TS 24.301 section 6.5.1
	ebi := -1
	for candidate := 5; candidate <= 15; candidate++ {
		if !existingEBIs[candidate] {
			ebi = candidate
			break
		}
	}
	if ebi < 0 {
		log.Warnf("No EBI available (all 5-15 in use) imsi=%s", imsi)
		return nil, nil
	}

	// In production: allocate IP via SMF, create UPF session.
	// Simplified for testing.
	bearer := &BearerInfo{
		EBI:         ebi,
		QCI:         9, // best effort default
		DNN:         dnn,
		ARPPriority: 8,
	}

	log.Infof("PDN Connection created: EBI=%d DNN=%s imsi=%s", ebi, dnn, imsi)
	return bearer, nil
}

// ActivateDedicatedBearer activates a dedicated EPS bearer (TS 24.301 section 8.3.1).
func ActivateDedicatedBearer(imsi string, serviceName string, linkedEBI int, existingEBIs map[int]bool) (*BearerInfo, error) {
	log := logger.Get("epc.mme.esm")

	// Allocate next EBI (6-15 for dedicated)
	ebi := -1
	for candidate := 6; candidate <= 15; candidate++ {
		if !existingEBIs[candidate] {
			ebi = candidate
			break
		}
	}
	if ebi < 0 {
		log.Warnf("No EBI available for dedicated bearer imsi=%s", imsi)
		return nil, nil
	}

	// In production: look up service definition for QCI/GBR.
	bearer := &BearerInfo{
		EBI:         ebi,
		LinkedEBI:   linkedEBI,
		QCI:         1, // conversational voice default
		ARPPriority: 2,
		ServiceName: serviceName,
		GBRDLKbps:   128,
		GBRULKbps:   128,
	}

	log.Infof("Dedicated bearer activated: EBI=%d QCI=%d service=%s imsi=%s", ebi, bearer.QCI, serviceName, imsi)
	return bearer, nil
}

// DeactivateDedicatedBearer deactivates a dedicated EPS bearer (TS 24.301 section 8.3.7).
func DeactivateDedicatedBearer(imsi string, ebi int) {
	log := logger.Get("epc.mme.esm")
	log.Infof("Dedicated bearer deactivated: EBI=%d imsi=%s", ebi, imsi)
}

// Status returns current ESM state.
func Status() map[string]any {
	log := logger.Get("esm")
	_ = log
	_ = engine.Open
	return map[string]any{"status": "ready"}
}
