// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package udm — Unified Data Management + ARPF.
//
// Authoritative specs (PDFs under specs/3gpp/):
//
//	TS 29.503 v19.6.0 — UDM SBI services.
//	  §5.2  Nudm_SubscriberDataManagement (Nudm_SDM)
//	    §5.2.2.2   Get — umbrella op
//	    §5.2.2.2.3 Access and Mobility Subscription Data Retrieval (AMF consumer)
//	    §5.2.2.2.5 Session Management Subscription Data Retrieval  (SMF consumer)
//	  §5.3  Nudm_UEContextManagement (Nudm_UECM)
//	    §5.3.2.2  Registration                   (AMF → UDM)
//	    §5.3.2.3  DeregistrationNotification     (UDM → AMF push)
//	    §5.3.2.4  Deregistration                 (AMF → UDM)
//	    §5.3.2.5  Get
//	  §5.4  Nudm_UEAuthentication (Nudm_UEAU)
//	    §5.4.2.2  Get — auth credential lookup
//	  §5.5  Nudm_EventExposure                   (not yet implemented)
//
//	TS 33.501 v19.6.0 — 5G security architecture.
//	  §6.1.2  Initiation of authentication & method selection (ARPF role).
//
//	TS 33.102 v19.0.1 — UMTS AKA carryover (§C.3 SQN management).
//
// In this in-process port the UDM is the 3GPP-shaped façade over UDR;
// AUSF / AMF / SMF never poke UDR rows directly, they call Nudm_*_Get
// here and the UDM forwards. The SBI REST surface lands when the
// service layer matures.
//
// This file ports nf/udm/udm_auth.py (AUSF-side credential lookup +
// SQN update). Sibling files:
//
//	sdm.go                — Nudm_SDM  (§5.2)
//	uecm.go               — Nudm_UECM (§5.3)
//	uecm/fsm/             — per-UE UECM registration state machine
//	cache.go              — auth cache (hot-path 0-DB)
//	sqn_flusher.go        — SQN write-behind
//	subscription_cache.go — UE-AMBR cache
package udm

import (
	"github.com/mmt/mmt-studio-core/nf/udr"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// GetAuthData is the Nudm_UEAuthentication_Get operation (TS 29.503
// §5.4.2.2 "Get"). Returns a cache-backed credential bundle for a
// SUPI, or nil when the subscriber is not provisioned. The cache is
// populated at startup by LoadCache and refreshed on GUI edits via
// ReloadAuth / DropAuth — so the hot auth path performs zero DB reads.
func GetAuthData(imsi string) (*udr.UEAuthData, error) {
	log := logger.Get("udm.auth").WithIMSI(imsi)
	pm.Inc(pm.UDMUeAuthGet, 1)
	ad := lookup(imsi)
	if ad == nil {
		log.Warn("Subscriber not found")
		return nil, nil
	}
	// Return a defensive copy so the AUSF caller can't mutate the
	// cached K/OP/AMF slices.
	cp := *ad
	cp.K = append([]byte(nil), ad.K...)
	cp.OP = append([]byte(nil), ad.OP...)
	cp.AMF = append([]byte(nil), ad.AMF...)
	log.Debug("Auth data retrieved (cache)")
	return &cp, nil
}

// UpdateAuthSQN bumps the SQN after a successful AV generation
// (TS 33.102 §C.3.2). Mutates the in-memory cache and marks the row
// dirty; the background SQN flusher persists to UDR on its next tick
// (default 2 s) and once more on shutdown. Keeps the hot path 0-DB.
func UpdateAuthSQN(imsi string, newSQN int64) error {
	bumpSQN(imsi, newSQN)
	return nil
}

// GetAllSubscribers powers the web UI's subscriber list. Passes through
// to UDR — the UDM doesn't impose a new shape.
func GetAllSubscribers() ([]struct {
	IMSI string
	udr.UEAuthData
}, error) {
	return udr.GetAllUeAuthData()
}
