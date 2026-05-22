// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package gmm — 5GMM (5G Mobility Management) NAS layer (TS 24.501 §8.2).
//
// This file holds the 5GMM message-type constants used by Dispatch. Every
// value is the raw single-byte "Message type" field of a 5GMM NAS message
// header (TS 24.501 §9.7) — i.e. the byte that follows the Extended
// Protocol Discriminator + Security Header Type octets.
//
// Numbers match TS 24.501 Table 8.1 "5GS Mobility Management messages".
package gmm

// 5GMM message types (TS 24.501 §9.7).
const (
	MsgRegistrationRequest        = 0x41 // §8.2.6
	MsgRegistrationAccept         = 0x42 // §8.2.7
	MsgRegistrationComplete       = 0x43 // §8.2.8
	MsgRegistrationReject         = 0x44 // §8.2.9
	MsgDeregistrationRequestMO    = 0x45 // §8.2.11 (UE-originating)
	MsgDeregistrationAcceptMO     = 0x46 // §8.2.12
	MsgDeregistrationRequestMT    = 0x47 // §8.2.13 (network-originating)
	MsgDeregistrationAcceptMT     = 0x48 // §8.2.14

	MsgServiceRequest  = 0x4C // §8.2.15
	MsgServiceReject   = 0x4D // §8.2.16
	MsgServiceAccept   = 0x4E // §8.2.17
	MsgControlPlaneSvc = 0x4F // §8.2.30 (CP Service Request)

	MsgConfigUpdateCommand  = 0x54 // §8.2.19
	MsgConfigUpdateComplete = 0x55 // §8.2.20

	MsgAuthenticationRequest  = 0x56 // §8.2.1
	MsgAuthenticationResponse = 0x57 // §8.2.2
	MsgAuthenticationReject   = 0x58 // §8.2.3
	MsgAuthenticationFailure  = 0x59 // §8.2.4
	MsgAuthenticationResult   = 0x5A // §8.2.5

	MsgIdentityRequest  = 0x5B // §8.2.21
	MsgIdentityResponse = 0x5C // §8.2.22

	MsgSecurityModeCommand  = 0x5D // §8.2.25
	MsgSecurityModeComplete = 0x5E // §8.2.26
	MsgSecurityModeReject   = 0x5F // §8.2.27

	Msg5GMMStatus    = 0x64 // §8.2.24
	MsgNotification  = 0x65 // §8.2.28
	MsgNotificationR = 0x66 // §8.2.29
	MsgULNASTransport = 0x67 // §8.2.10 — UL NAS Transport (5GSM piggyback)
	MsgDLNASTransport = 0x68 // §8.2.11
)

// ResponseTypes is the set of NAS message types that are always "responses
// to a procedure AMF initiated" — they bypass the pending-procedure guard
// in Dispatch. See TS 24.501 §5.1.3.2 (procedure collision rules).
var ResponseTypes = map[uint8]struct{}{
	MsgRegistrationComplete:   {},
	MsgDeregistrationAcceptMT: {},
	MsgConfigUpdateComplete:   {},
	MsgAuthenticationResponse: {},
	MsgAuthenticationFailure:  {},
	MsgIdentityResponse:       {},
	MsgSecurityModeComplete:   {},
	MsgSecurityModeReject:     {},
	MsgULNASTransport:         {},
}

// MsgName returns a short name for a 5GMM message type, or "" if unknown.
// Used for log lines + KPI labels.
func MsgName(t uint8) string {
	switch t {
	case MsgRegistrationRequest:
		return "RegistrationRequest"
	case MsgRegistrationAccept:
		return "RegistrationAccept"
	case MsgRegistrationComplete:
		return "RegistrationComplete"
	case MsgRegistrationReject:
		return "RegistrationReject"
	case MsgDeregistrationRequestMO:
		return "DeregistrationRequest(MO)"
	case MsgDeregistrationAcceptMO:
		return "DeregistrationAccept(MO)"
	case MsgDeregistrationRequestMT:
		return "DeregistrationRequest(MT)"
	case MsgDeregistrationAcceptMT:
		return "DeregistrationAccept(MT)"
	case MsgServiceRequest:
		return "ServiceRequest"
	case MsgServiceReject:
		return "ServiceReject"
	case MsgServiceAccept:
		return "ServiceAccept"
	case MsgControlPlaneSvc:
		return "ControlPlaneServiceRequest"
	case MsgConfigUpdateCommand:
		return "ConfigUpdateCommand"
	case MsgConfigUpdateComplete:
		return "ConfigUpdateComplete"
	case MsgAuthenticationRequest:
		return "AuthenticationRequest"
	case MsgAuthenticationResponse:
		return "AuthenticationResponse"
	case MsgAuthenticationReject:
		return "AuthenticationReject"
	case MsgAuthenticationFailure:
		return "AuthenticationFailure"
	case MsgAuthenticationResult:
		return "AuthenticationResult"
	case MsgIdentityRequest:
		return "IdentityRequest"
	case MsgIdentityResponse:
		return "IdentityResponse"
	case MsgSecurityModeCommand:
		return "SecurityModeCommand"
	case MsgSecurityModeComplete:
		return "SecurityModeComplete"
	case MsgSecurityModeReject:
		return "SecurityModeReject"
	case Msg5GMMStatus:
		return "5GMMStatus"
	case MsgNotification:
		return "Notification"
	case MsgNotificationR:
		return "NotificationResponse"
	case MsgULNASTransport:
		return "ULNASTransport"
	case MsgDLNASTransport:
		return "DLNASTransport"
	}
	return ""
}
