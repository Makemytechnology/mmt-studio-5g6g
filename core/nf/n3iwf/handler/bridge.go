// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package handler

import "net"

// NASBridge is the AMF-facing surface the IKE handler needs to
// forward EAP-5G-wrapped NAS PDUs onto N2 and to receive
// AMF-originated DL NAS bytes back. nf/n3iwf/n2 provides a
// concrete adapter wrapping its Manager; tests provide a fake.
//
// Decoupling rationale: the handler builds IKE_AUTH responses on
// its own request/response loop and would otherwise pull the entire
// NGAP codec stack via direct *n2.Manager use. Behind this
// interface the n2 types (DownlinkNAS, ICS) stay encapsulated.
//
// Lifecycle: AllocateRANUEID + RegisterUE happen once per UE on the
// first EAP-Response/5G-NAS forward; UnregisterUE on tunnel teardown.
// The OnDL / OnICS callbacks the handler registers write to the UE's
// WaitDLNAS channel — Manager's recv loop runs them from
// its own goroutine, the handler's IKE_AUTH receiver picks up.
//
// onICS gets (amfUEID, knh32, piggybackedNAS). The handler stores
// Knh on the UE context for the IPsec child-SA derivation in task
// #20 and pushes the piggybacked NAS (if any) into WaitDLNAS so the
// in-flight IKE_AUTH response wraps it as EAP-Request/5G-NAS.
type NASBridge interface {
	// AllocateRANUEID hands out a fresh RAN-UE-NGAP-ID per
	// TS 38.413 §9.3.3.2 (uint32 1..2^32-1, never 0).
	AllocateRANUEID() uint32

	// RegisterUE installs the OnDL + OnICS callbacks for this UE.
	// onDL receives the unwrapped NAS-PDU bytes pulled from a
	// DownlinkNASTransport (TS 38.413 §9.2.5.2). onICS fires when
	// an InitialContextSetupRequest lands (§9.2.2.1) — the handler
	// responds via SendInitialContextSetupResponse and stashes Knh
	// for later IPsec key derivation.
	RegisterUE(ranUEID uint32,
		onDL func(nas []byte, amfUEID uint64),
		onICS func(amfUEID uint64, knh []byte, piggybackedNAS []byte))

	// UnregisterUE removes the callbacks. Idempotent.
	UnregisterUE(ranUEID uint32)

	// SendInitialUEMessage ships the §9.2.5.3 PDU on a UE-associated
	// stream. plmn is optional (nil/short = omit the SelectedPLMN
	// IE).
	SendInitialUEMessage(ranUEID uint32, nas []byte, ueIP net.IP, uePort uint16, plmn []byte) error

	// SendUplinkNAS ships the §9.2.5.1 PDU for every subsequent
	// UE→AMF NAS forward.
	SendUplinkNAS(amfUEID uint64, ranUEID uint32, nas []byte, ueIP net.IP, uePort uint16) error

	// SendInitialContextSetupResponse acks the AMF's ICS request
	// per §9.2.2.2.
	SendInitialContextSetupResponse(amfUEID uint64, ranUEID uint32) error
}
