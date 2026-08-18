// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SIP header constants and builder utilities — Go port of sip_headers.py.
package sip

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/google/uuid"
)

// Standard header name constants.
const (
	HdrVia                = "Via"
	HdrFrom               = "From"
	HdrTo                 = "To"
	HdrCallID             = "Call-ID"
	HdrCSeq               = "CSeq"
	HdrContact            = "Contact"
	HdrMaxForwards        = "Max-Forwards"
	HdrContentType        = "Content-Type"
	HdrContentLength      = "Content-Length"
	HdrRoute              = "Route"
	HdrRecordRoute        = "Record-Route"
	HdrWWWAuthenticate    = "WWW-Authenticate"
	HdrAuthorization      = "Authorization"
	HdrProxyAuthenticate  = "Proxy-Authenticate"
	HdrProxyAuthorization = "Proxy-Authorization"
	HdrExpires            = "Expires"
	HdrPAssertedIdentity  = "P-Asserted-Identity"
	HdrPChargingVector    = "P-Charging-Vector"
)

// GenerateBranch returns a unique Via branch (RFC 3261 magic cookie + random).
func GenerateBranch() string {
	b := make([]byte, 8)
	rand.Read(b)
	return "z9hG4bK" + hex.EncodeToString(b)
}

// GenerateCallID returns a unique Call-ID.
func GenerateCallID() string { return uuid.New().String() }

// GenerateTag returns a random tag for From/To headers.
func GenerateTag() string {
	b := make([]byte, 6)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// BuildVia constructs a Via header value.
func BuildVia(transport, host string, port int, branch string) string {
	if branch == "" {
		branch = GenerateBranch()
	}
	return fmt.Sprintf("SIP/2.0/%s %s:%d;branch=%s", transport, host, port, branch)
}

// BuildContact constructs a Contact header value.
func BuildContact(user, host string, port int) string {
	return fmt.Sprintf("<sip:%s@%s:%d>", user, host, port)
}

// BuildFrom constructs a From header with tag.
func BuildFrom(display, uri, tag string) string {
	if tag == "" {
		tag = GenerateTag()
	}
	if display != "" {
		return fmt.Sprintf(`"%s" <%s>;tag=%s`, display, uri, tag)
	}
	return fmt.Sprintf("<%s>;tag=%s", uri, tag)
}

// BuildTo constructs a To header (tag optional).
func BuildTo(display, uri, tag string) string {
	var base string
	if display != "" {
		base = fmt.Sprintf(`"%s" <%s>`, display, uri)
	} else {
		base = fmt.Sprintf("<%s>", uri)
	}
	if tag != "" {
		base += ";tag=" + tag
	}
	return base
}
