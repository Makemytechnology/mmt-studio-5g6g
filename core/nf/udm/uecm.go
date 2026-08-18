// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// udm/uecm.go — Nudm_UEContextManagement (TS 29.503 §5.3).
//
// The UECM service tracks which AMF serves each UE so the network can
// answer queries like "where should a Namf_Communication message go?"
// and drive the §5.3.2.3 DeregistrationNotification when a second AMF
// claims the same UE.
//
// Service operations (TS 29.503 §5.3.2):
//
//	§5.3.2.2  Registration                 — AMF → UDM. RegisterAMF.
//	§5.3.2.3  DeregistrationNotification   — UDM → AMF push (sent by
//	                                         UDM to the OLD AMF when a
//	                                         new AMF registers for the
//	                                         same UE). NotifyOldAMF.
//	§5.3.2.4  Deregistration               — AMF → UDM. DeregisterAMF.
//	§5.3.2.5  Get                          — consumer → UDM. GetServingAMF.
//
// Per-UE state lives in the uecm/fsm package: Deregistered ↔
// Registered (no explicit timer in §5.3 — driven purely by the
// AMF-initiated events). The UDR persistence layer is future work
// for multi-AMF deployments; the in-memory registry here is enough
// for the single-AMF reference deployment plus unit tests.
package udm

import (
	"sync"
	"time"

	uecmfsm "github.com/mmt/mmt-studio-core/nf/udm/uecm/fsm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// AMFRegistration is the per-UE record the UDM stores on a successful
// §5.3.2.2 Registration. Mirrors the AmfRegistration type at TS 29.503
// §6.2.6 / §6.2.6.2 (AmfRegistration schema) — only the fields used
// by the in-process callers are modelled; the wire schema carries
// additional attributes (DRFlag, urrpIndicator, backupAmfInfo, …).
type AMFRegistration struct {
	IMSI         string
	AmfUeNgapID  int64
	AmfName      string
	RegisteredAt time.Time
}

var (
	regMu sync.RWMutex
	regs  = map[string]*AMFRegistration{}
)

// RegisterAMF is Nudm_UECM_Registration (TS 29.503 §5.3.2.2). Called
// by the AMF after successful primary authentication per TS 23.502
// §4.2.2.2.2 step 14: "The new AMF registers with the UDM using
// Nudm_UEContextManagement_Registration." If a previous AMF is
// already serving this UE, the UDM sends DeregistrationNotification
// (§5.3.2.3) to the old AMF so it releases its context — that push
// is modelled here as NotifyOldAMF + fires on the old FSM.
//
// Returns the previous registration, if any, so the caller can log
// the override. Safe for concurrent use; per-IMSI FSM serialises
// races on the state transitions.
func RegisterAMF(imsi string, amfUeNgapID int64, amfName string) (*AMFRegistration, error) {
	log := logger.Get("udm.uecm")
	if amfName == "" {
		amfName = "self"
	}
	k := uecmfsm.Key{IMSI: imsi}

	// Capture any prior registration before we overwrite — §5.3.2.3
	// DeregistrationNotification goes to THAT AMF.
	regMu.Lock()
	prev := regs[imsi]
	regs[imsi] = &AMFRegistration{
		IMSI:         imsi,
		AmfUeNgapID:  amfUeNgapID,
		AmfName:      amfName,
		RegisteredAt: time.Now(),
	}
	regMu.Unlock()

	f := uecmfsm.Of(k)
	// If a prior AMF was registered, we notionally send §5.3.2.3
	// DeregistrationNotification — in this in-process port both
	// AMFs are the same binary, so the "notify" is a no-op log line.
	if prev != nil {
		log.WithIMSI(imsi).Infof("§5.3.2.3 DeregistrationNotification: old AMF %s (amf_ue_id=%d) superseded by %s (amf_ue_id=%d)",
			prev.AmfName, prev.AmfUeNgapID, amfName, amfUeNgapID)
		pm.Inc(pm.UDMUecmDeregNotify, 1)
		// Active → Active (same state, fresh registration). We fire
		// a Deregister/Register pair so the FSM log reflects the churn.
		_ = f.Fire(&uecmfsm.Context{Key: k, Event: uecmfsm.EvDeregisterRequest})
	}

	if err := f.Fire(&uecmfsm.Context{Key: k, Event: uecmfsm.EvRegisterRequest}); err != nil {
		return prev, err
	}
	pm.Inc(pm.UDMUecmRegister, 1)
	log.WithIMSI(imsi).Infof("AMF registered amf_ue_id=%d amf=%s", amfUeNgapID, amfName)
	return prev, nil
}

// DeregisterAMF is Nudm_UECM_Deregistration (TS 29.503 §5.3.2.4).
// Called by the AMF when the UE deregisters (UE-initiated or
// AMF-initiated per TS 24.501 §5.5.2) or the UE context is otherwise
// torn down. Idempotent — deregistering an unknown IMSI is a no-op.
func DeregisterAMF(imsi string) {
	log := logger.Get("udm.uecm")
	k := uecmfsm.Key{IMSI: imsi}

	regMu.Lock()
	_, had := regs[imsi]
	delete(regs, imsi)
	regMu.Unlock()

	if !had {
		// No registration on record; skip FSM to avoid a noisy
		// "no transition for DeregisterRequest in state DEREGISTERED".
		return
	}
	f := uecmfsm.Of(k)
	_ = f.Fire(&uecmfsm.Context{Key: k, Event: uecmfsm.EvDeregisterRequest})
	uecmfsm.Drop(k)
	pm.Inc(pm.UDMUecmDeregister, 1)
	log.WithIMSI(imsi).Info("AMF deregistered")
}

// GetServingAMF is Nudm_UECM_Get (TS 29.503 §5.3.2.5). Returns the
// AMFRegistration for a given IMSI, or nil when no AMF currently
// serves this UE. Used by Namf_Communication routing and network-
// initiated deregistration paths.
func GetServingAMF(imsi string) *AMFRegistration {
	regMu.RLock()
	defer regMu.RUnlock()
	r := regs[imsi]
	if r == nil {
		return nil
	}
	// Return a defensive copy.
	cp := *r
	return &cp
}
