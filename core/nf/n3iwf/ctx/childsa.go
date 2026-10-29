// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package ctx

import "time"

// ChildSA is one IPsec ESP child SA negotiated via RFC 7296 §1.3
// CREATE_CHILD_SA. Each direction carries its own (encr, integ) key
// pair and a 32-bit ESP SPI per RFC 4303 §2:
//
//	SPIIn   ⟵ the SPI we (responder) chose; peer puts this in the
//	          ESP header of packets it sends to us, we use it as
//	          the lookup key on inbound ESP.
//	SPIOut  ⟵ the SPI the peer (initiator) advertised; we put it
//	          in the ESP header of packets we send to the peer.
//
// EncrKeyIn/IntegKeyIn  decrypt+verify inbound packets (peer → us).
// EncrKeyOut/IntegKeyOut encrypt+sign outbound packets (us → peer).
//
// Per §2.17 the keying material is laid out in the order:
// {SK_ei | SK_ai | SK_er | SK_ar}, with "i" used for traffic the
// initiator sends. As the responder we therefore use the "r"
// suffixed keys for our outbound direction.
type ChildSA struct {
	SPIIn      uint32
	SPIOut     uint32
	EncrKeyIn  []byte // peer's outbound key = our inbound (SK_ei)
	IntegKeyIn []byte // SK_ai
	EncrKeyOut []byte // our outbound key (SK_er)
	IntegKeyOut []byte // SK_ar

	// TSi / TSr payload bodies as received in the CREATE_CHILD_SA
	// request — echoed unchanged in the response (we don't narrow).
	// Stored as raw RFC 7296 §3.13 payload data so future code can
	// parse selectors when filtering ESP traffic.
	TSiBytes []byte
	TSrBytes []byte

	// Nonces from the CREATE_CHILD_SA exchange (§2.17 input).
	NonceI []byte
	NonceR []byte

	// True for the first/signalling child SA (TS 24.502 §7.4 NWu),
	// false for subsequent per-PDU-session user-plane SAs.
	Signalling bool

	// TEIDDown is the inbound GTP-U TEID the N3IWF allocated for
	// this user-plane child SA — populated only when !Signalling.
	// The N3IWF sends this value to the AMF in
	// PDUSessionResourceSetupResponseTransfer; the UPF uses it as
	// the destination TEID on UPF→N3IWF G-PDUs (TS 29.281 §5.1).
	// Zero for signalling SAs (which don't terminate on N3).
	TEIDDown uint32

	CreatedAt time.Time
}
