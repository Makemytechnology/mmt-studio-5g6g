// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Nsmsf service consumer / producer surface — TS 29.540 §5.2
// (Nsmsf_SMService Service) and §6.1 (HTTP/2 + JSON / OpenAPI bindings).
//
// This file is intentionally a stub: the rest of the SMSF currently
// runs as an in-process callable invoked directly by the AMF
// (nas_bridge.go), bypassing the Nsmsf SBI entirely. Migrating to
// HTTP/2 SBI is mostly a serialisation exercise — every field we
// already exchange in-process maps onto an Nsmsf operation defined
// by §5.2.2.x.
//
// The TODOs below are TS-numbered so a future audit can grep
// `TS 29.540` to find every gap. Each TODO names the operation, the
// HTTP method/URI, and the §clause that defines the body.

package smsf

// TODO(spec: TS 29.540 §5.2.2.2 Nsmsf_SMService_Activate): produce
// a HTTP POST handler at  POST /{apiRoot}/nsmsf-sms/<apiVersion>/
// ue-contexts/{supi}  per §6.1.3.2.3.1 that replaces the implicit
// activation we do today (UE rows in the DB are read on demand).
// Body: ActivateRequestData per §6.1.5.2.2 — carries SUPI, AMF
// callback URI, GPSI, supportedFeatures.
//
// TODO(spec: TS 29.540 §5.2.2.3 Nsmsf_SMService_Deactivate): map to
// DELETE /{apiRoot}/nsmsf-sms/<apiVersion>/ue-contexts/{supi}.
// Required for SMSF state cleanup on UE deregistration. Today the
// AMF does not signal deregistration to the SMSF.
//
// TODO(spec: TS 29.540 §5.2.2.4 Nsmsf_SMService_UplinkSMS): map to
// POST /{apiRoot}/nsmsf-sms/<apiVersion>/ue-contexts/{supi}/sendsms
// (§6.1.3.3.3.1). Body: SMSRecordData per §6.1.5.2.4 — wraps the
// RP-DATA bytes plus access-type / GPSI / userLocation.
// This is the SBI form of ProcessMOSMSFromNAS; once the AMF moves
// to it, ProcessMOSMSFromNAS becomes the producer-side handler
// instead of the AMF-internal callable.
//
// TODO(spec: TS 29.540 §5.2.2.5 Nsmsf_SMService_MtForwardSm): cover
// the MT path — today the SMSF synthesises MT TPDUs locally
// (deliverLocal in smsf.go) without any SBI hand-off. Real-world
// SMS-router integration would land here as a producer-side handler
// for POST /{apiRoot}/nsmsf-sms/<apiVersion>/ue-contexts/{supi}/
// retrieve-pending-messages (§6.1.3.4.3.1), plus a Notify/Subscribe
// pair on the Namf_Communication service (TS 29.518 §5.2.2.3
// N1MessageNotify) for DL delivery to the UE.
//
// TODO(spec: TS 29.540 §6.1.6 Error handling): adopt the
// ProblemDetails (RFC 7807) carrier with Nsmsf-specific cause values
// from §6.1.6.3 Table 6.1.6.3-1 once the producer endpoints exist.
// We currently surface plain Go errors from ProcessMOSMSFromNAS and
// do not map them to the spec-mandated cause set ("INVALID_GPSI",
// "TRIGGER_NOT_AUTHORIZED", "SC_ADDRESS_NOT_INCLUDED", ...).

// nsmsfSurface is a placeholder type that ties the §-cited TODOs
// above to a concrete Go symbol. Exported only because the speccheck
// scanner needs at least one declaration in the file to walk the
// godoc — kept empty so the linter doesn't flag unused state.
type nsmsfSurface struct{}
