// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// udr/subscription.go — Nudr_DataRepository: subscription data (TS 29.504).
//
// Provides raw DB access to subscription and NSSAI tables.
// UDM sits on top and exposes the 3GPP service interfaces.
package udr

import (
	"github.com/mmt/mmt-studio-core/db/crud"
)

// SubscribedNSSAIEntry represents one (SST, SD) tuple from the subscription.
type SubscribedNSSAIEntry struct {
	SST       int
	SD        *int // nil when not present
	IsDefault bool
}

// GetSubscribedNSSAI returns the subscribed S-NSSAIs for a subscriber.
// Primary source: ue_subscribed_nssai table.
// Fallback: derived from service_bindings (same logic as Python udm_sdm.py).
func GetSubscribedNSSAI(imsi string) ([]SubscribedNSSAIEntry, error) {
	nssaiList, err := crud.SubscribedNSSAIList(imsi)
	if err != nil {
		return nil, err
	}
	if len(nssaiList) > 0 {
		out := make([]SubscribedNSSAIEntry, 0, len(nssaiList))
		for _, n := range nssaiList {
			e := SubscribedNSSAIEntry{SST: n.SST, IsDefault: n.IsDefault}
			if n.SD != "" {
				sd := crud.NormalizeSST(n.SD) // SD is hex string, reuse normalizer
				e.SD = &sd
			}
			out = append(out, e)
		}
		return out, nil
	}
	// Fallback: derive from service_bindings via slice-dnn
	sliceDNNs, err := crud.SliceDNNList(imsi)
	if err != nil {
		return nil, err
	}
	type key struct{ sst int; sd string }
	seen := map[key]bool{}
	var out []SubscribedNSSAIEntry
	for _, sd := range sliceDNNs {
		k := key{sd.SST, sd.SD}
		if seen[k] {
			continue
		}
		seen[k] = true
		e := SubscribedNSSAIEntry{SST: sd.SST}
		if sd.SD != "" {
			sdVal := crud.NormalizeSST(sd.SD)
			e.SD = &sdVal
		}
		out = append(out, e)
	}
	return out, nil
}

// GetDefaultDNN returns the is_default=1 DNN for (imsi, sst, sd) from
// ue_slice_dnn. TS 23.501 §5.6.1 (defaultDnnIndicator). sdHex is the 6-char
// hex SD string; "" means no-SD slice.
func GetDefaultDNN(imsi string, sst int, sdHex string) (string, bool) {
	list, err := crud.SliceDNNList(imsi)
	if err != nil {
		return "", false
	}
	var fallback string
	for _, sd := range list {
		if sd.SST != sst {
			continue
		}
		// SD match: both empty, or exact hex match
		sdNorm := sd.SD
		if !(sdNorm == sdHex || (sdNorm == "" && sdHex == "")) {
			continue
		}
		if sd.IsDefault {
			return sd.DNN, true
		}
		if fallback == "" {
			fallback = sd.DNN
		}
	}
	if fallback != "" {
		return fallback, true
	}
	return "", false
}

// SubscriptionData is the full subscription profile for a UE.
type SubscriptionData struct {
	SubscribedNSSAI []SubscribedNSSAIEntry
	AMBRDLKbps      int64
	AMBRULKbps      int64
}

// GetSubscriptionData retrieves full subscription data: subscribed NSSAI + AMBR.
// Mirrors Python udm_sdm.get_subscription_data.
func GetSubscriptionData(imsi string) (*SubscriptionData, error) {
	sub, err := crud.SubscriptionGetByIMSI(imsi)
	if err != nil {
		return nil, err
	}
	ambrDL := int64(1000000)
	ambrUL := int64(1000000)
	if sub != nil {
		if sub.AMBR.DownlinkKbps > 0 {
			ambrDL = sub.AMBR.DownlinkKbps
		}
		if sub.AMBR.UplinkKbps > 0 {
			ambrUL = sub.AMBR.UplinkKbps
		}
	}
	nssai, err := GetSubscribedNSSAI(imsi)
	if err != nil {
		return nil, err
	}
	return &SubscriptionData{
		SubscribedNSSAI: nssai,
		AMBRDLKbps:      ambrDL,
		AMBRULKbps:      ambrUL,
	}, nil
}
