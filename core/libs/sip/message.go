// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package sip — SIP message parser + builder (RFC 3261).
//
// Shared SIP protocol kernel consumed by services/ims, services/mcx,
// and services/frmcs: message parsing, header manipulation,
// dialog/transaction tracking, and UDP/TCP transport.
//
// This file defines SipMessage, SipRequest, SipResponse — the core
// types that every CSCF handler consumes.
package sip

import (
	"fmt"
	"strings"
)

// SipMessage is the base for requests and responses.
type SipMessage struct {
	Headers map[string][]string // case-preserved key → multi-value
	Body    string
}

// GetHeader returns the first value for a header (case-insensitive), or "".
func (m *SipMessage) GetHeader(name string) string {
	lower := strings.ToLower(name)
	for k, v := range m.Headers {
		if strings.ToLower(k) == lower && len(v) > 0 {
			return v[0]
		}
	}
	return ""
}

// GetHeaderValues returns all values for a header (case-insensitive).
func (m *SipMessage) GetHeaderValues(name string) []string {
	lower := strings.ToLower(name)
	for k, v := range m.Headers {
		if strings.ToLower(k) == lower {
			return v
		}
	}
	return nil
}

// SetHeader replaces all values for a header.
func (m *SipMessage) SetHeader(name, value string) {
	lower := strings.ToLower(name)
	for k := range m.Headers {
		if strings.ToLower(k) == lower {
			delete(m.Headers, k)
		}
	}
	m.Headers[name] = []string{value}
}

// AddHeader appends a value. Via headers should use prepend=true per RFC 3261 §18.2.2.
func (m *SipMessage) AddHeader(name, value string, prepend bool) {
	lower := strings.ToLower(name)
	for k := range m.Headers {
		if strings.ToLower(k) == lower {
			if prepend {
				m.Headers[k] = append([]string{value}, m.Headers[k]...)
			} else {
				m.Headers[k] = append(m.Headers[k], value)
			}
			return
		}
	}
	m.Headers[name] = []string{value}
}

// SipRequest is a SIP request (INVITE, REGISTER, BYE, …).
type SipRequest struct {
	SipMessage
	Method     string // INVITE, REGISTER, BYE, ACK, CANCEL, OPTIONS, …
	RequestURI string // sip:user@domain
}

// SipResponse is a SIP response (100 Trying, 200 OK, 401, …).
type SipResponse struct {
	SipMessage
	StatusCode int
	Reason     string
}

// String renders the start line for logging.
func (r *SipRequest) String() string {
	return fmt.Sprintf("%s %s SIP/2.0", r.Method, r.RequestURI)
}
func (r *SipResponse) String() string {
	return fmt.Sprintf("SIP/2.0 %d %s", r.StatusCode, r.Reason)
}

// Serialize renders a full SIP message back to wire bytes.
func (r *SipRequest) Serialize() []byte {
	return serializeMsg(r.String(), &r.SipMessage)
}
func (r *SipResponse) Serialize() []byte {
	return serializeMsg(r.String(), &r.SipMessage)
}

func serializeMsg(startLine string, m *SipMessage) []byte {
	var sb strings.Builder
	sb.WriteString(startLine)
	sb.WriteString("\r\n")
	for k, vals := range m.Headers {
		for _, v := range vals {
			sb.WriteString(k)
			sb.WriteString(": ")
			sb.WriteString(v)
			sb.WriteString("\r\n")
		}
	}
	sb.WriteString("\r\n")
	sb.WriteString(m.Body)
	return []byte(sb.String())
}
