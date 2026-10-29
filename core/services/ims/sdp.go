// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Tiny RFC 4566 SDP parser — just enough to pull the m-line media
// types ("audio", "video", ...), per-media bandwidth ("b=AS:N"), and
// direction attribute (a=sendonly / sendrecv / recvonly / inactive)
// out of an INVITE body so the CSCF can drive the §5.7.4 5QI selection
// on the AF→PCF authorization request and detect hold/resume on a
// re-INVITE per RFC 3264 §5.1.
//
// Spec anchors:
//   * RFC 4566 §5.14 "Media Descriptions" —
//     m=<media> <port> <proto> <fmt> ...
//     specs/ietf/rfc4566.txt.
//   * RFC 4566 §5.8 "Bandwidth" — b=<bwtype>:<bandwidth-kbps>
//   * RFC 4566 §6 "sendonly / recvonly / sendrecv / inactive"
//     direction attributes (session-level default is sendrecv).
//   * RFC 3264 §5.1 "Hold" — a=sendonly on offer = caller put on
//     hold; a=inactive = both directions held.
//   * TS 23.501 §5.7.4 Table 5.7.4-1 — Standardized 5QI to QoS
//     characteristics mapping (Conversational Voice = 5QI 1,
//     Conversational Video = 5QI 2).
//
// Parser is deliberately minimal: no SDP attribute parsing, no offer/
// answer reconciliation (RFC 3264). Returns a list of MediaDescription
// in declaration order so the caller can preserve the offerer's
// media-line ordering when authorizing flows.
package ims

import (
	"strconv"
	"strings"
)

// MediaDescription is one m= line distilled to the fields the
// AF→PCF authorization request needs.
type MediaDescription struct {
	// Type is the media token from m=<media>: "audio", "video",
	// "application", "text", "message" (RFC 4566 §5.14, plus IANA
	// media-type registry).
	Type string

	// Port is the transport port on the offerer side (m= second
	// field). 0 means "media disabled" per §5.14.
	Port int

	// Proto is the m= third field (e.g. "RTP/AVP", "RTP/SAVP",
	// "UDP/TLS/RTP/SAVPF" for IMS WebRTC).
	Proto string

	// Formats is the m= remaining tokens — payload-type numbers
	// (RTP) or fmt names. Kept verbatim.
	Formats []string

	// BandwidthKbps is the b= line value if present, expressed in
	// kbps (RFC 4566 §5.8 "Bandwidth"). 0 if no b= line — the
	// caller can fall back to the codec default for the chosen 5QI.
	BandwidthKbps int

	// Direction is the per-media a= direction attribute per
	// RFC 4566 §6: "sendrecv" (default), "sendonly", "recvonly",
	// "inactive". Empty means the media block didn't override the
	// session-level default (we don't track session-level direction
	// in this stub; sendrecv is the canonical default).
	//
	// On a re-INVITE per RFC 3264 §5.1: sendonly = caller on hold,
	// inactive = both ends held, sendrecv = resume. The AF→PCF
	// path uses this to flip §8.2.7 Gate Status on the QER (TODO:
	// the wire-up of "direction → GateUL/GateDL on PCC rule"
	// belongs in services/ims/af.go + nf/pcf — for now we just
	// surface the parsed value to caller code).
	Direction string
}

// Direction values per RFC 4566 §6 "Media Attributes". Exposed as
// constants so callers can compare without importing the string
// literal everywhere.
const (
	DirSendRecv = "sendrecv"
	DirSendOnly = "sendonly"
	DirRecvOnly = "recvonly"
	DirInactive = "inactive"
)

// ParseSDP returns the m-line MediaDescriptions from an SDP body.
// Lines outside the m= / b= scope of each media block are ignored.
// Returns an empty slice on malformed input — the caller treats that
// as "no media" and the AF call is skipped.
func ParseSDP(body string) []MediaDescription {
	var out []MediaDescription
	var cur *MediaDescription

	// SDP lines are CRLF-terminated per RFC 4566 §5; tolerate bare LF.
	for _, raw := range splitLines(body) {
		line := strings.TrimRight(raw, "\r")
		if len(line) < 2 || line[1] != '=' {
			continue
		}
		typ, val := line[0], line[2:]
		switch typ {
		case 'm':
			// New media section: flush the previous and start fresh.
			if cur != nil {
				out = append(out, *cur)
			}
			cur = parseMLine(val)
		case 'b':
			// b=<bwtype>:<bandwidth>. We accept any bwtype (AS / TIAS
			// / CT / RR / RS) and treat the value as kbps; a real
			// implementation would convert TIAS bps→kbps, but the
			// PCF only uses this as an initial GBR hint.
			if cur != nil {
				if eq := strings.Index(val, ":"); eq >= 0 {
					if n, err := strconv.Atoi(strings.TrimSpace(val[eq+1:])); err == nil {
						cur.BandwidthKbps = n
					}
				}
			}
		case 'a':
			// a=<attribute>[:<value>]. We only care about RFC 4566
			// §6.1 direction attributes; everything else (rtpmap,
			// fmtp, ptime, …) is ignored by this minimal parser.
			if cur == nil {
				continue
			}
			switch strings.TrimSpace(val) {
			case DirSendRecv, DirSendOnly, DirRecvOnly, DirInactive:
				cur.Direction = strings.TrimSpace(val)
			}
		}
	}
	if cur != nil {
		out = append(out, *cur)
	}
	return out
}

// MediaTypes returns the unique m= media-type strings from a parsed
// SDP, dropping any media line whose port is 0 (RFC 4566 §5.14: "If
// the port number is set to zero, this is presumed to be due to a
// rejected offer"). Order preserves the SDP declaration order so the
// AF call ships them in the same order the UE offered them.
func MediaTypes(media []MediaDescription) []string {
	seen := map[string]bool{}
	var out []string
	for _, m := range media {
		if m.Port == 0 {
			continue
		}
		t := strings.ToLower(m.Type)
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// parseMLine splits an m= value into a MediaDescription per
// RFC 4566 §5.14.
func parseMLine(val string) *MediaDescription {
	fields := strings.Fields(val)
	if len(fields) < 3 {
		return nil
	}
	port := 0
	// §5.14 lets the port be "<port>/<number-of-ports>"; we only
	// need the leading port here.
	portStr := fields[1]
	if slash := strings.Index(portStr, "/"); slash > 0 {
		portStr = portStr[:slash]
	}
	if n, err := strconv.Atoi(portStr); err == nil {
		port = n
	}
	md := &MediaDescription{
		Type:  fields[0],
		Port:  port,
		Proto: fields[2],
	}
	if len(fields) > 3 {
		md.Formats = append([]string(nil), fields[3:]...)
	}
	return md
}

// splitLines breaks an SDP body on \n while preserving the rest of
// each line for the trim-CR step in the caller.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	out := make([]string, 0, 16)
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		out = append(out, s[start:])
	}
	return out
}
