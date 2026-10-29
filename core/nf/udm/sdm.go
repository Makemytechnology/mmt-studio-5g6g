// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// udm/sdm.go — Nudm_SubscriberDataManagement (TS 29.503 §5.2).
//
// Go port of nf/udm/udm_sdm.py. The SDM provides subscription data to
// AMF / NSSF / SMF. The umbrella §5.2.2.2 "Get" operation has
// resource-specific sub-sections (§5.2.2.2.2..20) that select which
// slice of the subscription to return. Callers here map to:
//
//	AMF  ──Nudm_SDM_Get──→ UDM (§5.2.2.2.3 "Access and Mobility
//	                             Subscription Data Retrieval")
//	SMF  ──Nudm_SDM_Get──→ UDM (§5.2.2.2.5 "SMF Session Management
//	                             Subscription Data Retrieval" — default DNN,
//	                             DNN configurations)
//	NSSF ──Nudm_SDM_Get──→ UDM (§5.2.2.2.2 "Slice Selection Subscription
//	                             Data Retrieval")
//
// Every in-process method below is a pass-through to UDR; it forwards
// over SBI once the REST surface lands.
package udm

import (
	"fmt"

	"github.com/mmt/mmt-studio-core/nf/udr"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// SubscribedNSSAI is (SST, SD) as returned to AMF callers.
type SubscribedNSSAI struct {
	SST       int
	SD        *int // nil when slice descriptor absent
	IsDefault bool
}

// GetSubscribedNSSAI is Nudm_SDM_Get carrying Access-and-Mobility
// Subscription Data (TS 29.503 §5.2.2.2.3 "Access and Mobility
// Subscription Data Retrieval"). Subscribed S-NSSAIs are an attribute
// of that resource — TS 23.501 §5.15.4: "The UDM provides the
// subscribed S-NSSAIs for a UE." Called by the AMF during registration.
func GetSubscribedNSSAI(imsi string) ([]SubscribedNSSAI, error) {
	log := logger.Get("udm.sdm").WithIMSI(imsi)
	pm.Inc(pm.UDMSdmGetAM, 1)
	entries, err := udr.GetSubscribedNSSAI(imsi)
	if err != nil {
		log.Warnf("GetSubscribedNSSAI: %v", err)
		return nil, err
	}
	out := make([]SubscribedNSSAI, len(entries))
	for i, e := range entries {
		out[i] = SubscribedNSSAI{SST: e.SST, SD: e.SD, IsDefault: e.IsDefault}
	}
	log.Debugf("Subscribed NSSAI: %d slices", len(out))
	return out, nil
}

// SubscriptionData is the full subscription profile for a UE.
type SubscriptionData struct {
	SubscribedNSSAI []SubscribedNSSAI
	AMBRDLKbps      int64
	AMBRULKbps      int64
}

// GetDefaultSNSSAI returns the default S-NSSAI from subscription —
// a field of Access-and-Mobility Subscription Data (TS 29.503
// §5.2.2.2.3). TS 23.502 §4.3.2.2.1 step 2: AMF picks the default
// S-NSSAI when multiple are in Allowed NSSAI and the UE didn't
// include one in the PDU Session Request. Returns (sst, sd_hex, true)
// on success; sd_hex is "" for no-SD.
func GetDefaultSNSSAI(imsi string) (int, string, bool) {
	log := logger.Get("udm.sdm").WithIMSI(imsi)
	pm.Inc(pm.UDMSdmGetAM, 1)
	entries, err := udr.GetSubscribedNSSAI(imsi)
	if err != nil || len(entries) == 0 {
		return 0, "", false
	}
	for _, e := range entries {
		if e.IsDefault {
			sdHex := ""
			if e.SD != nil {
				sdHex = fmt.Sprintf("%06X", *e.SD)
			}
			log.Debugf("Default S-NSSAI: SST=%d SD=%s", e.SST, sdHex)
			return e.SST, sdHex, true
		}
	}
	return 0, "", false
}

// GetDefaultDNN is a slice of Nudm_SDM_Get carrying Session
// Management Subscription Data (TS 29.503 §5.2.2.2.5 "SMF Session
// Management Subscription Data Retrieval"). Returns the default DNN
// for a (IMSI, S-NSSAI) tuple from the subscriber's ue_slice_dnn
// entries per TS 23.501 §5.6.1 "defaultDnnIndicator". sdHex is the
// 6-char hex string ("" for no-SD). Called by the SMF during PDU
// Session Establishment when the UE omits a DNN.
func GetDefaultDNN(imsi string, sst int, sdHex string) (string, bool) {
	log := logger.Get("udm.sdm").WithIMSI(imsi)
	pm.Inc(pm.UDMSdmGetSM, 1)
	dnn, ok := udr.GetDefaultDNN(imsi, sst, sdHex)
	if ok {
		log.Debugf("Default DNN for SST=%d SD=%s: %s", sst, sdHex, dnn)
	}
	return dnn, ok
}

// GetSubscriptionData is Nudm_SDM_Get carrying Access-and-Mobility
// Subscription Data (TS 29.503 §5.2.2.2.3). Returns the subscribed
// NSSAI and subscribed UE-AMBR in one shot — used by the AMF during
// registration when it needs both slices of subscription state.
func GetSubscriptionData(imsi string) (*SubscriptionData, error) {
	log := logger.Get("udm.sdm").WithIMSI(imsi)
	pm.Inc(pm.UDMSdmGetAM, 1)
	data, err := udr.GetSubscriptionData(imsi)
	if err != nil {
		log.Warnf("GetSubscriptionData: %v", err)
		return nil, err
	}
	if data == nil {
		return nil, nil
	}
	nssai := make([]SubscribedNSSAI, len(data.SubscribedNSSAI))
	for i, e := range data.SubscribedNSSAI {
		nssai[i] = SubscribedNSSAI{SST: e.SST, SD: e.SD, IsDefault: e.IsDefault}
	}
	result := &SubscriptionData{
		SubscribedNSSAI: nssai,
		AMBRDLKbps:      data.AMBRDLKbps,
		AMBRULKbps:      data.AMBRULKbps,
	}
	log.Infof("Subscription data ambr_dl=%d ambr_ul=%d slices=%d",
		result.AMBRDLKbps, result.AMBRULKbps, len(nssai))
	return result, nil
}
