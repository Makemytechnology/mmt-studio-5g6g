// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package gmlc — Gateway Mobile Location Centre.
//
// Spec anchors:
//
//   - TS 23.273 §4.3.3   GMLC functional description — the network
//                        function this package models. The GMLC is
//                        the LCS-Client-facing entry point that
//                        forwards positioning requests to the LMF.
//   - TS 23.273 §4.4.1   Le reference point — external LCS client
//                        interface. The package's RequestLocation /
//                        GetLocation / CancelLocation surface is
//                        the Le-side projection of the GMLC.
//   - TS 29.572 §5.2.2.2 Nlmf_Location_DetermineLocation — the
//                        downstream SBI operation the GMLC invokes
//                        on the LMF (here: an in-process Go call).
//
// TODO TS 29.515 — Ngmlc service operations (the proper SBI
//                  interface for GMLC consumers / NEF). Today the
//                  callers reach RequestLocation as a Go function;
//                  the HTTP/2 + JSON envelope is not yet modelled.
package gmlc

import (
	"github.com/mmt/mmt-studio-core/nf/lmf"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("gmlc")

// LocationResult is the result of a location request.
type LocationResult struct {
	SessionID    string   `json:"session_id"`
	State        string   `json:"state"`
	Method       string   `json:"method"`
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Altitude     *float64 `json:"altitude,omitempty"`
	UncertaintyM *float64 `json:"uncertainty_m,omitempty"`
	Confidence   *int     `json:"confidence,omitempty"`
	IMSI         string   `json:"imsi"`
}

// RequestLocation initiates a UE positioning request via the LMF
// (TS 29.572 §5.2.2.2 Nlmf_Location_DetermineLocation). The clientType
// values match the LCS client classes referenced in TS 23.273
// §4.3.2 (commercial / emergency / lawful_intercept / value_added).
func RequestLocation(imsi, method string, accuracyM, responseTimeS float64, clientType string) LocationResult {
	if clientType == "" {
		clientType = "commercial"
	}
	var qos map[string]float64
	if accuracyM > 0 || responseTimeS > 0 {
		qos = map[string]float64{}
		if accuracyM > 0 {
			qos["accuracy_m"] = accuracyM
		}
		if responseTimeS > 0 {
			qos["response_time_s"] = responseTimeS
		}
	}

	ctx := lmf.GetLMF()
	session := ctx.RequestLocation(imsi, method, qos, clientType)

	return LocationResult{
		SessionID:    session.SessionID,
		State:        session.State,
		Method:       session.Method,
		Latitude:     session.Latitude,
		Longitude:    session.Longitude,
		Altitude:     session.Altitude,
		UncertaintyM: session.UncertaintyM,
		Confidence:   session.Confidence,
		IMSI:         session.IMSI,
	}
}

// GetLocation retrieves a positioning session result.
func GetLocation(sessionID string) *LocationResult {
	ctx := lmf.GetLMF()
	session := ctx.GetSession(sessionID)
	if session == nil {
		return nil
	}
	return &LocationResult{
		SessionID:    session.SessionID,
		State:        session.State,
		Method:       session.Method,
		Latitude:     session.Latitude,
		Longitude:    session.Longitude,
		Altitude:     session.Altitude,
		UncertaintyM: session.UncertaintyM,
		Confidence:   session.Confidence,
		IMSI:         session.IMSI,
	}
}

// CancelLocation cancels a location request (TS 29.572 §5.2.2.4
// Nlmf_Location_CancelLocation).
func CancelLocation(sessionID string) {
	lmf.GetLMF().CancelSession(sessionID)
}

// LocationHistory returns completed location sessions for a UE.
func LocationHistory(imsi string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 10
	}
	return lmf.GetLMF().LocationHistory(imsi, limit)
}

// RegisterGnbPosition registers a gNB geographic position.
func RegisterGnbPosition(gnbID string, lat, lon, alt float64) {
	lmf.GetLMF().RegisterGnbPosition(gnbID, lat, lon, alt)
}

// RegisterGnbAntenna registers gNB antenna configuration.
func RegisterGnbAntenna(gnbID string, azimuthDeg, beamwidthDeg, downtiltDeg float64, numBeams int) {
	lmf.GetLMF().RegisterGnbAntenna(gnbID, azimuthDeg, beamwidthDeg, downtiltDeg, numBeams)
}

// AllocatePRS allocates a PRS resource for a gNB.
func AllocatePRS(gnbID string, frequencyLayer, periodicityMS, numRB, numSymbols, combSize int) *lmf.PRSResource {
	return lmf.GetLMF().AllocatePRSResource(gnbID, frequencyLayer, periodicityMS, numRB, numSymbols, combSize)
}

// GetPRSConfig returns PRS resources for a gNB.
func GetPRSConfig(gnbID string) []*lmf.PRSResource {
	return lmf.GetLMF().GetPRSResourcesForGnb(gnbID)
}

// DeactivatePRS deactivates a PRS resource.
func DeactivatePRS(prsID int) {
	lmf.GetLMF().DeactivatePRSResource(prsID)
}

// Status returns current GMLC status.
func Status() map[string]any {
	return map[string]any{"status": "ready"}
}

func init() {
	_ = log
}
