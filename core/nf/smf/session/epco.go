// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// epco.go — Extended Protocol Configuration Options (EPCO) IE builder
// for PDU SESSION ESTABLISHMENT ACCEPT.
//
// Spec anchors (read from local PDFs under codecs/tlv-3gpp-nas/
// standards/ and specs/3gpp/):
//
//	TS 24.501 v19.6.2 §8.3.2.10 "Extended protocol configuration
//	  options" (verbatim): "This IE is included in the message when
//	  the network needs to transmit (protocol) data (e.g.
//	  configuration parameters, error codes or messages/events) to
//	  the UE." — Presence O, IEI 0x7B, format TLV-E.
//
//	TS 24.501 v19.6.2 §9.11.4.6 "Extended protocol configuration
//	  options" (verbatim): "See clause 10.5.6.3A in 3GPP TS 24.008."
//
//	TS 24.008 v19.5.0 §10.5.6.3 "Protocol configuration options"
//	  (referenced by §10.5.6.3A with identical encoding) —
//	  Figure 10.5.136/3GPP TS 24.008:
//	    Octet 3: bit 8 = ext (always 1) | bits 7-4 = spare (0000) |
//	             bits 3-1 = Configuration protocol
//	             (000 = "PPP for use with IP PDP type or IP PDN type")
//	    Octets 4+: zero or more Container IDs, each
//	             [Container ID (2 octets BE) | Length (1 octet) |
//	              Contents (Length octets)]
//
//	TS 24.008 §10.5.6.3 Network-to-MS container table (verbatim):
//	  "000CH (P-CSCF IPv4 Address); 000DH (DNS Server IPv4 Address);
//	   … 0010H (IPv4 Link MTU); 0011H (Network support of Local
//	   address in TFT indicator)"
//
//	TS 24.008 §10.5.6.3 P-CSCF IPv4 Address (verbatim): "the
//	  container identifier contents field contains one IPv4 address
//	  corresponding to the P-CSCF address to be used. When there is
//	  a need to include more than one P-CSCF IPv4 address, then more
//	  logical units with the container identifier indicating P-CSCF
//	  IPv4 Address are used. If more than 3 instances of the P-CSCF
//	  IPv4 Address logical unit are received by the MS, then the MS
//	  may ignore all but the first 3 instances…"
//
//	TS 24.008 §10.5.6.3 DNS Server IPv4 Address (verbatim): "the
//	  container identifier contents field contains one IPv4 address
//	  corresponding to the DNS server address to be used. When
//	  there is a need to include more than one DNS Server IPv4
//	  address, then more logical units with the container identifier
//	  indicating DNS Server IPv4 Address are used."
//
//	TS 24.008 §10.5.6.3 IPv4 Link MTU (verbatim): "length of
//	  container identifier contents indicates a length equal to two.
//	  The container identifier contents field contains the binary
//	  coded representation of the IPv4 link MTU size in octets. Bit
//	  8 of the first octet of the container identifier contents
//	  field contains the most significant bit and bit 1 of the
//	  second octet of the container identifier contents field
//	  contains the least significant bit."
//
//	TS 24.008 §10.5.6.3 Network support of Local address in TFT
//	  (verbatim): "container identifier contents field is empty and
//	  the length of container identifier contents indicates a length
//	  equal to zero."
//
//	TS 24.008 §10.5.6.3 Configuration Protocol Options list
//	  (verbatim): "At least the following protocol identifiers (as
//	  defined in RFC 3232) shall be supported in this version of the
//	  protocol: C021H (LCP); C023H (PAP); C223H (CHAP); and 8021H
//	  (IPCP)." … "The detailed coding of the protocol identifier
//	  contents field is specified in the RFC that is associated with
//	  the protocol identifier of that unit: … IPCP is specified in
//	  RFC 1332." 3GPP cross-references RFC 1332 §3.2 Configure-
//	  Request / §3.3 Configure-Ack packet layout and RFC 1877 §1 DNS
//	  option types 129 (Primary DNS) / 131 (Secondary DNS). Those
//	  RFCs are in-scope via this §10.5.6.3 cross-reference.
//
// Data source: every field comes from infra config (apn_config table)
// — apn.PCSCFAddress / apn.DNSPrimary / apn.DNSSecondary / apn.MTU.
// No hard-coded defaults leak into the wire: an unset value simply
// omits the corresponding container.
package session

import (
	"net"

	smfctx "github.com/mmt/mmt-studio-core/nf/smf/ctx"
)

// Container IDs per TS 24.008 §10.5.6.3 Network-to-MS table +
// Configuration Protocol Options list Protocol IDs.
const (
	// Protocol Options list entries (§10.5.6.3: "shall be supported"
	// when present in the UE request).
	protocolIDIPCP = 0x8021 // Internet Protocol Control Protocol (RFC 1332)

	// Additional Parameters list container IDs.
	containerIDPCSCFIPv4      = 0x000C
	containerIDDNSServerIPv4  = 0x000D
	containerIDIPv4LinkMTU    = 0x0010
	containerIDLocalAddrInTFT = 0x0011 // Network support indicator
)

// RFC 1877 DNS option types inside an IPCP packet (cross-referenced
// by TS 24.008 §10.5.6.3 "IPCP is specified in RFC 1332"; RFC 1877
// extends IPCP with the DNS + NBNS options).
const (
	ipcpOptPrimaryDNS   = 129 // Primary DNS Server Address
	ipcpOptSecondaryDNS = 131 // Secondary DNS Server Address
)

// RFC 1332 §3 IPCP packet codes.
const (
	ipcpCodeConfigureRequest = 1
	ipcpCodeConfigureAck     = 2
)

// Octet-3 flag layout per TS 24.008 Figure 10.5.136:
//
//	bit 8     : Extension — "1" for EPCO (§10.5.6.3A)
//	bits 7-4  : Spare, "0000"
//	bits 3-1  : Configuration protocol
//	            000 = PPP for use with IP PDP type or IP PDN type
const epcoHeaderByte = 0x80

// requestedContainers is the decoded set of *Request container IDs
// the UE sent in its §8.3.1.9 Extended Protocol Configuration
// Options IE. Per TS 24.008 §10.5.6.3 each *Request has empty
// content — its presence alone is the capability/ask signal.
//
// Verbatim spec text for the signalling semantics (§10.5.6.3 DNS
// case, the explicit one): "When the DNS Server IPv4 Address
// Request is indicated in N1 mode, the DNS Server IPv4 Address
// Request indicates that the MS supports handling of the DNS Server
// IPv4 address(es) received in the PDU session establishment
// procedure…". The same request/response pattern applies to the
// other containers on this path (P-CSCF IPv4, IPv4 Link MTU).
type requestedContainers struct {
	wantsPCSCFIPv4 bool
	wantsDNSIPv4   bool
	wantsMTU       bool
	// msSupportsLocalAddrInTFT — UE sent 0011H "MS support of Local
	// address in TFT" (capability advert per §10.5.6.3). The network
	// side answers with its own 0011H capability (Network support of
	// Local address in TFT) — but that's a network-driven
	// advertisement, not a response to this one. Kept for logging
	// only.
	msSupportsLocalAddrInTFT bool

	// ipcpConfigReq is the raw IPCP packet bytes (RFC 1332 §3.2
	// Configure-Request code=1) the UE sent inside the 0x8021
	// container. Non-nil when the UE used the legacy PPP IPCP path
	// to request DNS options (RFC 1877 options 129/131). Echoed back
	// as a Configure-Ack (RFC 1332 §3.3, code=2) by
	// buildIPCPConfigureAck. TS 24.008 §10.5.6.3: "8021H (IPCP)
	// shall be supported…" and "IPCP is specified in RFC 1332".
	ipcpConfigReq []byte
}

// parseRequestedEPCO decodes the UE's §8.3.1.9 EPCO IE value bytes
// and returns which response containers the SMF is allowed to emit.
// Invalid / empty input returns the zero value — no responses
// allowed, per §8.3.2.10 the IE is then omitted.
func parseRequestedEPCO(ieBytes []byte) requestedContainers {
	var r requestedContainers
	if len(ieBytes) < 1 {
		return r
	}
	// Octet 1: Extension + spare + Configuration protocol. Only
	// accept PPP/IP (bits 3-1 == 000); other Config Protocols are
	// undefined for this codec per §10.5.6.3.
	if ieBytes[0]&0x07 != 0 {
		return r
	}
	off := 1
	for off+3 <= len(ieBytes) {
		id := uint16(ieBytes[off])<<8 | uint16(ieBytes[off+1])
		n := int(ieBytes[off+2])
		off += 3
		if off+n > len(ieBytes) {
			break
		}
		contents := ieBytes[off : off+n]
		off += n
		// UE-side *Request containers have empty content (length 0).
		// A populated same-numeric-ID in this direction is a spec
		// violation; per §10.5.6.3 rule "If the container identifier
		// contents field is not empty, it shall be ignored." we
		// apply the same "ignore" rule here.
		switch id {
		case protocolIDIPCP: // 8021H IPCP — RFC 1332 packet (populated)
			// Protocol Options list entry per §10.5.6.3: "The
			// protocol identifier contents field of each unit
			// corresponds to a 'Packet' as defined in RFC 1661 that
			// is stripped off the 'Protocol' and the 'Padding'
			// octets." So `contents` = Code | Identifier | Length |
			// Options. Ignore anything that isn't a Config-Req.
			if len(contents) >= 4 && contents[0] == ipcpCodeConfigureRequest {
				r.ipcpConfigReq = append([]byte(nil), contents...)
			}
		case containerIDPCSCFIPv4: // 000CH Request (empty)
			if len(contents) == 0 {
				r.wantsPCSCFIPv4 = true
			}
		case containerIDDNSServerIPv4: // 000DH Request (empty)
			if len(contents) == 0 {
				r.wantsDNSIPv4 = true
			}
		case containerIDIPv4LinkMTU: // 0010H Request (empty)
			if len(contents) == 0 {
				r.wantsMTU = true
			}
		case containerIDLocalAddrInTFT: // 0011H MS support (empty)
			if len(contents) == 0 {
				r.msSupportsLocalAddrInTFT = true
			}
		}
	}
	return r
}

// buildIPCPConfigureAck walks an IPCP Configure-Request packet and
// returns a Configure-Ack with each requested option's value filled
// from the APN config.
//
// RFC 1332 §3.2 Configure-Request layout (verbatim coded per
// §10.5.6.3 cross-reference "IPCP is specified in RFC 1332"):
//
//	0        1          2 3       4...
//	Code(1)  Identifier Length(2) Options...
//
// Configure-Ack (RFC 1332 §3.3) echoes the Identifier and the
// options we CAN fulfill, each with Type(1) + Length(1) + Data.
// Options we cannot fill are omitted — some UE stacks accept this
// (matches the operator reference trace), but the strict RFC
// interpretation would be Configure-Nak for unfillable options.
// TODO(RFC 1661 §5.3 Configure-Nak) — send Nak for options where
// we propose a different value (currently we just skip them).
// Configure-Nak semantics are inherited from PPP base spec
// RFC 1661, which IPCP (RFC 1332) layers on top of.
//
// RFC 1877 §1 DNS options recognised today:
//
//	Type 129 Primary-DNS-Address    4-byte value (IPv4)
//	Type 131 Secondary-DNS-Address  4-byte value (IPv4)
//
// Returns nil when there are no options we can fill (the whole IPCP
// container is then omitted from the response).
func buildIPCPConfigureAck(req []byte, apn *smfctx.APNConfig) []byte {
	if len(req) < 4 {
		return nil
	}
	identifier := req[1]
	// req[2..4] is the Length field; trust it only if ≤ len(req).
	declared := int(req[2])<<8 | int(req[3])
	if declared > len(req) {
		declared = len(req)
	}
	options := req[4:declared]

	var ack []byte
	for off := 0; off+2 <= len(options); {
		optType := options[off]
		optLen := int(options[off+1])
		if optLen < 2 || off+optLen > len(options) {
			break
		}
		// Fill based on option Type; skip unknowns per RFC 1332 §3
		// "the receiver will process the options as if they appeared
		// in the received Configure-Request" with our supplied
		// values.
		switch optType {
		case ipcpOptPrimaryDNS:
			if ip := parseIPv4(apn.DNSPrimary); ip != nil {
				ack = append(ack, ipcpOptPrimaryDNS, 0x06, ip[0], ip[1], ip[2], ip[3])
			}
		case ipcpOptSecondaryDNS:
			if ip := parseIPv4(apn.DNSSecondary); ip != nil {
				ack = append(ack, ipcpOptSecondaryDNS, 0x06, ip[0], ip[1], ip[2], ip[3])
			}
		}
		off += optLen
	}
	if len(ack) == 0 {
		return nil
	}
	// RFC 1332 §3.3 header: Code=2, Identifier echoed, Length = 4 +
	// options.
	pkt := make([]byte, 4+len(ack))
	pkt[0] = ipcpCodeConfigureAck
	pkt[1] = identifier
	totalLen := uint16(len(pkt))
	pkt[2] = byte(totalLen >> 8)
	pkt[3] = byte(totalLen)
	copy(pkt[4:], ack)
	return pkt
}

// buildExtendedPCO returns the opaque IE-value bytes for
// ExtendedProtocolConfigurationOptions (TS 24.501 §9.11.4.6, wire
// format per TS 24.008 §10.5.6.3). Returns nil when there's nothing
// to answer — per §8.3.2.10 "included… when the network needs to
// transmit data".
//
// Emission is gated on the UE's *Request containers (per §10.5.6.3
// the request signals the MS is capable of handling the response).
// Unsolicited responses are avoided:
//
//	8021H IPCP                       — emit Configure-Ack iff UE sent
//	                                    Configure-Request and at least
//	                                    one option (primary/secondary
//	                                    DNS) can be filled
//	000CH P-CSCF IPv4 Address        — emit iff UE sent 000CH Request
//	                                    AND apn.PCSCFAddress is set
//	000DH DNS Server IPv4 Address    — emit iff UE sent 000DH Request
//	                                    AND apn.DNSPrimary/Secondary set
//	0010H IPv4 Link MTU              — emit iff UE sent 0010H Request
//	                                    AND apn.MTU > 0
//	0011H Network support of Local   — emit iff UE sent 0011H
//	       address in TFT indicator    (mirrors the MS-side advert)
func buildExtendedPCO(apn *smfctx.APNConfig, requestedIE []byte) []byte {
	if apn == nil {
		return nil
	}
	req := parseRequestedEPCO(requestedIE)

	var containers []byte

	// ── 8021H IPCP Configure-Ack ──────────────────────────────────
	// Spec TS 24.008 §10.5.6.3 (verbatim): "At least the following
	// protocol identifiers (as defined in RFC 3232) shall be
	// supported in this version of the protocol: … 8021H (IPCP)."
	// RFC 1332 §3.3 Configure-Ack echoes the Identifier with values
	// for the requested RFC 1877 options. Emitted only when we can
	// fill at least one option (strict omit-on-empty).
	if req.ipcpConfigReq != nil {
		if ack := buildIPCPConfigureAck(req.ipcpConfigReq, apn); ack != nil {
			containers = append(containers, encodeContainer(protocolIDIPCP, ack)...)
		}
	}

	// ── 000CH P-CSCF IPv4 Address ─────────────────────────────────
	// Spec TS 24.008 §10.5.6.3 (verbatim): "the container identifier
	// contents field contains one IPv4 address corresponding to the
	// P-CSCF address to be used." Only emit if the UE asked AND the
	// APN has the value configured.
	if req.wantsPCSCFIPv4 {
		if ip := parseIPv4(apn.PCSCFAddress); ip != nil {
			containers = append(containers, encodeContainer(containerIDPCSCFIPv4, ip)...)
		}
	}

	// ── 000DH DNS Server IPv4 Address ─────────────────────────────
	// Spec: "When the DNS Server IPv4 Address Request is indicated
	// in N1 mode, the DNS Server IPv4 Address Request indicates
	// that the MS supports handling of the DNS Server IPv4
	// address(es) received in the PDU session establishment
	// procedure…". One container per configured server.
	if req.wantsDNSIPv4 {
		for _, dns := range []string{apn.DNSPrimary, apn.DNSSecondary} {
			ip := parseIPv4(dns)
			if ip == nil {
				continue
			}
			containers = append(containers, encodeContainer(containerIDDNSServerIPv4, ip)...)
		}
	}

	// ── 0010H IPv4 Link MTU ───────────────────────────────────────
	// Spec: "length … equal to two. … binary coded representation of
	// the IPv4 link MTU size in octets. Bit 8 of the first octet …
	// contains the most significant bit" — big-endian uint16.
	if req.wantsMTU && apn.MTU > 0 && apn.MTU <= 0xFFFF {
		mtu := []byte{byte(apn.MTU >> 8), byte(apn.MTU)}
		containers = append(containers, encodeContainer(containerIDIPv4LinkMTU, mtu)...)
	}

	// ── 0011H Network support of Local address in TFT ─────────────
	// Spec: "container identifier contents field is empty and the
	// length of container identifier contents indicates a length
	// equal to zero." This is a capability exchange — the UE's
	// 0011H "MS support of Local address in TFT" paired with the
	// network's 0011H "Network support…". Emit when the UE advertised
	// its own support.
	if req.msSupportsLocalAddrInTFT {
		containers = append(containers, encodeContainer(containerIDLocalAddrInTFT, nil)...)
	}

	// Nothing to say? Omit the whole IE per §8.3.2.10 "included…
	// when the network needs to transmit…".
	if len(containers) == 0 {
		return nil
	}

	// Prepend the fixed octet-3 header (0x80 = ext + PPP/IP).
	out := make([]byte, 0, 1+len(containers))
	out = append(out, epcoHeaderByte)
	out = append(out, containers...)
	return out
}

// encodeContainer wires one [Container ID | Length | Contents]
// triplet per TS 24.008 §10.5.6.3. The simple 1-octet-length form
// covers DNS / MTU / TFT and every other fixed-length container the
// 5G-native containers use; the 2-octet-length form in §10.5.6.3
// NOTE applies only to a handful of IDs (0x0023, 0x0024, 0x0030,
// 0x0031, 0x0032, 0x0041, 0x0051, 0x0056) — add a dedicated helper
// when one of those lands.
func encodeContainer(id uint16, contents []byte) []byte {
	out := make([]byte, 3+len(contents))
	out[0] = byte(id >> 8)
	out[1] = byte(id)
	out[2] = byte(len(contents))
	copy(out[3:], contents)
	return out
}

// parseIPv4 parses a dotted-quad string to 4 bytes network-order.
// Returns nil for IPv6, empty, or malformed input — caller skips
// the container, per §10.5.6.3 the 000DH container is IPv4-only
// (000EH covers MSISDN, 0001H P-CSCF IPv6, etc. — add when needed).
func parseIPv4(s string) []byte {
	if s == "" {
		return nil
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return []byte{v4[0], v4[1], v4[2], v4[3]}
	}
	return nil
}
