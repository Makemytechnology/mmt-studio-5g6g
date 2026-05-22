// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package frmcs — Future Railway Mobile Communication System.
//
// FRMCS is the UIC-led successor to GSM-R, standardised jointly by
// UIC and 3GPP. Voice / video / data services ride on top of 3GPP
// Mission Critical Services (MCPTT / MCVideo / MCData) with
// railway-specific additions: role-based functional aliases, the
// Railway Emergency Call (REC), on-train-to-track interworking, and
// PC5-based off-network for shunting / train-to-train.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 22.289  Mobile communication system for railways (Stage 1).
//                Used to anchor the high-level priority requirement
//                from §4.4.1 (the "emergency calls established on
//                demand with priority guaranteeing call success
//                independent of running services" requirement).
//   - TS 23.289  Mission Critical services over 5G System (Stage 2).
//                Anchors the §4.3.3 / §4.3.4 / §4.3.5 QoS-for-
//                MCPTT / MCVideo / MCData blocks that FRMCS
//                operational profiles consume.
//
// MCX-family specs (MCPTT 24.379 / 23.379, MCData 23.282, MCVideo
// 23.281, MCX common 23.280, MCX security 33.180) live under
// specs/3gpp/ alongside the FRMCS PDFs; FRMCS inherits them by
// reference.
//
// UIC documents — not yet in-tree:
//   FRMCS FRS, SRS, On-Board architecture, FIS / FFFIS interface
//   specs. Exact document codes to be filled in when PDFs are
//   committed; UIC-specific clauses are TS-name-only references in
//   the code (no §-cite) and tagged with TODO comments.
//
// Note: TS 24.289 and TS 24.280 are not assigned 3GPP documents —
// FRMCS / MCX stage-3 behaviour is carried in the per-service
// stage-3 specs (24.379 / 24.282 / 24.281). TS 24.281 / TS 24.282
// are themselves not yet in-tree (3gpp.org download is gated); the
// MCX implementation flags this with TS-numbered TODOs.
//
// This package is the top-level entry point — subpackages implement
// individual service areas:
//
//   common/    — functional aliases, priority, identity
//   voice/     — REC and assured voice over services/mcx/mcptt
//                (anchored to TS 24.379 §6.2.8.1 emergency group
//                call conditions + TS 24.380 §4.1.1.4 preempt-
//                override outcome)
//   shunting/  — train-to-train / ground-to-driver off-network
//                calls over services/mcx/mcptt off-network (PC5)
//                — anchored to TS 24.379 §10.2.2 off-network FSM.
package frmcs

import (
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("frmcs")

// Service is the FRMCS on-network application entry point.
// It holds references to the underlying MCX services and exposes
// railway-specific operations (REC initiation, functional alias
// registration, etc.).
type Service struct {
	Domain string
}

// New returns a Service configured for the given FRMCS home domain.
func New(domain string) *Service {
	log.Infof("FRMCS: service starting for domain %s", domain)
	return &Service{Domain: domain}
}
