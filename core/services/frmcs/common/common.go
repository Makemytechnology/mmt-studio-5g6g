// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package common — FRMCS identity and priority primitives.
//
// Railway operation uses role-based addressing — a FunctionalAlias
// identifies a role (e.g. "driver of train 8421", "controller of
// area X") rather than a specific device or subscriber. The alias
// is bound to an MCPTT user at log-on and released at log-off, so
// the same radio hardware can serve different roles across shifts.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 22.289 §4.4.1   The §4.4 "Coexistence of automated train
//                          control with other train applications"
//                          description that lists the priority
//                          stack: MTTC (highest) > CCTV > emergency
//                          calls > passenger data; emergency calls
//                          must be establishable independent of
//                          already-running services. This anchors
//                          the ServiceClass ranking below.
//   - TS 22.289 §4.4.2   Requirements [R4.4.2-1] (high-priority
//                          latency/availability is preserved under
//                          contention) and [R4.4.2-2] (high-
//                          priority startup is not blocked by
//                          lower-priority services) — these drive
//                          the FRMCS-side preempt policy that
//                          rec.go and shunting.go invoke.
//   - TS 23.289 §4.3.3   QoS for MCPTT (used for FRMCS voice
//                          profile selection).
//   - TS 23.289 §4.3.4   QoS for MCVideo.
//   - TS 23.289 §4.3.5   QoS for MCData.
//
// UIC FRS/SRS specifies the canonical FunctionalAlias schema (e.g.
// "driver-{trainNumber}", "controller-{regionCode}"). Those
// documents are not yet in-tree — TODO below.
package common

// FunctionalAlias is a railway role identifier that is bound to an
// MCPTT user for the duration of a logon session.
//
// TODO(spec: UIC FRS / SRS): adopt the canonical alias schema
// once the UIC FRS / SRS PDFs are committed under specs/. Today
// callers pass a free-form string ("driver-01", "ground-A") and
// the package only enforces that it is non-empty.
type FunctionalAlias string

// ServiceClass ranks FRMCS services so the MCPTT floor controller
// can preempt correctly. REC is the only class that must never be
// preempted, per the priority stack described in TS 22.289 §4.4.1
// (emergency calls top-of-stack) and the no-blocking requirement
// of §4.4.2 [R4.4.2-2].
//
// TODO(spec: UIC FRS): the canonical UIC priority hierarchy (REC,
// shunting emergency, assured voice, business, passenger) extends
// further than this 4-class collapse. The full table will be
// modelled once the UIC PDFs are in-tree.
type ServiceClass int

const (
	ServiceREC       ServiceClass = iota // Railway Emergency Call (TS 22.289 §4.4.1 emergency-call requirement)
	ServiceUrgent                        // operational urgent (e.g. shunting emergency)
	ServiceAssured                       // point-to-point / group assured voice
	ServiceBusiness                      // business voice and non-critical data
)
