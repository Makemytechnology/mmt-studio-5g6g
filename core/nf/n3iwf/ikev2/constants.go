// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ikev2 implements RFC 7296 (IKEv2) wire-format encoding /
// decoding for the AMF-side N3IWF, plus the §2.13/§2.14 keying
// material derivation needed to terminate IKE SAs from untrusted
// non-3GPP UEs (TS 24.502 §7.3 references RFC 7296 throughout).
//
// Authoritative spec: RFC 7296 (PDF/text: specs/ietf/rfc7296.txt).
//
// Scope of this package:
//
//   - Pure encode/decode of the wire structures defined in §3.1-§3.16
//     of RFC 7296. No I/O, no UDP socket, no state machine — those
//     live in the parent nf/n3iwf package.
//   - PRF + prf+ + SKEYSEED helpers (§2.13/§2.14) so the IKE_AUTH
//     handler can derive SK_d / SK_a{i,r} / SK_e{i,r} / SK_p{i,r}.
//   - Diffie-Hellman key exchange for the IKE_SA_INIT exchange
//     (§2.10) — group 14 (RFC 3526 §3) is the operator-mandated
//     minimum here.
//
// EAP-5G TLV format (TS 24.502 §9.3.2) lives in nf/n3iwf/eap5g — it
// is carried inside the EAP payload (§3.16) defined here, but its
// TLV grammar is 3GPP-specific.
package ikev2

// ExchangeType — RFC 7296 §3.1 "Exchange Type" table (verbatim):
//
//	IKE_SA_INIT      34
//	IKE_AUTH         35
//	CREATE_CHILD_SA  36
//	INFORMATIONAL    37
type ExchangeType uint8

const (
	ExchangeIKESAInit     ExchangeType = 34
	ExchangeIKEAuth       ExchangeType = 35
	ExchangeCreateChildSA ExchangeType = 36
	ExchangeInformational ExchangeType = 37
)

// PayloadType — RFC 7296 §3.2 "Next Payload Type" table (verbatim):
//
//	No Next Payload                              0
//	Security Association             SA         33
//	Key Exchange                     KE         34
//	Identification - Initiator       IDi        35
//	Identification - Responder       IDr        36
//	Certificate                      CERT       37
//	Certificate Request              CERTREQ    38
//	Authentication                   AUTH       39
//	Nonce                            Ni, Nr     40
//	Notify                           N          41
//	Delete                           D          42
//	Vendor ID                        V          43
//	Traffic Selector - Initiator     TSi        44
//	Traffic Selector - Responder     TSr        45
//	Encrypted and Authenticated      SK         46
//	Configuration                    CP         47
//	Extensible Authentication        EAP        48
type PayloadType uint8

const (
	PayloadNone        PayloadType = 0
	PayloadSA          PayloadType = 33
	PayloadKE          PayloadType = 34
	PayloadIDi         PayloadType = 35
	PayloadIDr         PayloadType = 36
	PayloadCERT        PayloadType = 37
	PayloadCERTREQ     PayloadType = 38
	PayloadAUTH        PayloadType = 39
	PayloadNonce       PayloadType = 40
	PayloadNotify      PayloadType = 41
	PayloadDelete      PayloadType = 42
	PayloadVendorID    PayloadType = 43
	PayloadTSi         PayloadType = 44
	PayloadTSr         PayloadType = 45
	PayloadSK          PayloadType = 46 // Encrypted and Authenticated
	PayloadCP          PayloadType = 47 // Configuration
	PayloadEAP         PayloadType = 48
)

// Header flags — RFC 7296 §3.1:
//
//	+-+-+-+-+-+-+-+-+
//	|X|X|R|V|I|X|X|X|
//	+-+-+-+-+-+-+-+-+
//
// I=Initiator, V=Version, R=Response. X bits MUST be cleared.
const (
	FlagInitiator uint8 = 1 << 3
	FlagVersion   uint8 = 1 << 4
	FlagResponse  uint8 = 1 << 5
)

// HeaderLen — RFC 7296 §3.1 fixed 28-octet IKE header
// (8 SPIi + 8 SPIr + 1 NextPayload + 1 MjVer/MnVer +
//  1 ExchangeType + 1 Flags + 4 MessageID + 4 Length).
const HeaderLen = 28

// PayloadHeaderLen — RFC 7296 §3.2 fixed 4-octet generic payload
// header (1 NextPayload + 1 Critical/RESERVED + 2 PayloadLength).
const PayloadHeaderLen = 4

// VersionByte — RFC 7296 §3.1: "Implementations based on this
// version of IKE MUST set the major version to 2." (MjVer=2 in the
// upper nibble, MnVer=0 in the lower nibble) ⇒ 0x20.
const VersionByte = 0x20
