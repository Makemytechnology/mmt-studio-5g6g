// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// S-CSCF REGISTER handling (TS 24.229 §5.4.1 "Registration and
// authentication"). The auth-mechanism dispatch described in
// §5.4.1.1 (IMS-AKA / SIP Digest / NASS-IMS-bundled / GPRS-IMS-
// bundled) is NOT implemented here yet — this file only extracts
// IMS identities from an incoming REGISTER. PDF anchor:
// specs/3gpp/ts_124229v190600p.pdf.
package cscf

import (
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/libs/sip"
)

// RegisterFields are the IMS identities extracted from a SIP REGISTER request.
type RegisterFields struct {
	IMPI    string
	IMPU    string
	Contact string
	Expires int
}

// ParseRegister extracts IMS identities from a SIP REGISTER request
// (TS 24.229 §5.4.1 — S-CSCF Registration and authentication) using
// the shared libs/sip message types.
func ParseRegister(req *sip.SipRequest) RegisterFields {
	f := RegisterFields{
		IMPI:    ExtractIMPI(req.GetHeader("Authorization"), req.GetHeader("From")),
		IMPU:    ExtractIMPU(req.GetHeader("To")),
		Contact: req.GetHeader("Contact"),
	}
	if exp := req.GetHeader("Expires"); exp != "" {
		if n, err := strconv.Atoi(exp); err == nil {
			f.Expires = n
		}
	}
	return f
}

// ExtractIMPI extracts IMPI from SIP REGISTER request headers.
// Per TS 24.229 §5.4.1.1, the S-CSCF identifies the user from the
// Authorization header's username parameter when present, else from
// the public identity carried in the To header (from which the
// private identity is derived per operator policy). The IMPI string
// format itself is defined in 3GPP TS 23.003 §13.3 "Private User
// Identity" — specs/3gpp/ts_123003v190600p.pdf.
func ExtractIMPI(authHeader, fromHeader string) string {
	if authHeader != "" {
		// Parse username from Authorization: Digest username="..."
		if idx := strings.Index(authHeader, `username="`); idx >= 0 {
			rest := authHeader[idx+10:]
			if end := strings.Index(rest, `"`); end >= 0 {
				return rest[:end]
			}
		}
	}
	// Fall back to From header URI
	if fromHeader != "" {
		uri := fromHeader
		if idx := strings.Index(uri, "<"); idx >= 0 {
			uri = uri[idx+1:]
			if end := strings.Index(uri, ">"); end >= 0 {
				uri = uri[:end]
			}
		}
		// sip:user@domain -> user@domain
		if strings.HasPrefix(uri, "sip:") {
			return uri[4:]
		}
		return uri
	}
	return ""
}

// ExtractIMPU extracts IMPU from SIP REGISTER To header.
func ExtractIMPU(toHeader string) string {
	uri := toHeader
	if idx := strings.Index(uri, "<"); idx >= 0 {
		uri = uri[idx+1:]
		if end := strings.Index(uri, ">"); end >= 0 {
			uri = uri[:end]
		}
	}
	// Strip tag parameter
	if idx := strings.Index(uri, ";tag="); idx >= 0 {
		uri = uri[:idx]
	}
	return uri
}

// IMSIFromIMPI extracts the IMSI portion from an IMPI.
// IMPI format per TS 23.003 §13.3 "Private User Identity":
// <IMSI>@ims.mnc<MNC>.mcc<MCC>.3gppnetwork.org
// (specs/3gpp/ts_123003v190600p.pdf).
func IMSIFromIMPI(impi string) string {
	if idx := strings.Index(impi, "@"); idx >= 0 {
		return impi[:idx]
	}
	return impi
}

// TelURIFromIMPI builds a tel: URI from IMPI if the subscriber has an MSISDN.
func TelURIFromIMPI(impi, msisdn string) string {
	if msisdn != "" && strings.HasPrefix(msisdn, "+") {
		return "tel:" + msisdn
	}
	return ""
}
