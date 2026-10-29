// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package userplane bridges the NWu-side IPsec ESP child SA (RFC
// 4303) and the N3-side GTP-U tunnel (TS 29.281) for the N3IWF's
// user plane (TS 23.501 §6.3.1, TS 24.502 §7.4):
//
//	UE  ⇆  ESP/NWu  ⇆  N3IWF  ⇆  GTP-U/N3  ⇆  UPF
//
// One Bridge per UE PDU session. It owns the per-direction child SA
// (encap/decap keys + replay window) and the pair of TEIDs:
//
//	TEIDDown — N3IWF's receive direction (UPF→N3IWF). N3IWF mints
//	           this and gives it to the AMF in the InitialContextSetup
//	           Response / PDU-Session-Resource-Setup-Response, which
//	           forwards it via SMF→UPF as the destination TEID.
//	TEIDUp   — UPF's receive direction (N3IWF→UPF). UPF mints this
//	           and the SMF lands it in the
//	           PDUSessionResourceSetupRequestTransfer GTP-U TNL Info
//	           per TS 38.413 §9.3.4.1 — the N3IWF puts it in the
//	           outbound G-PDU TEID field.
//
// The bridge is pure: no sockets, no goroutines. The transport
// layer (UDP/4500 for NWu NAT-T, UDP/2152 for GTP-U) calls
// HandleNWu / HandleN3 on each datagram.
package userplane

import (
	"errors"
	"fmt"
	"net"

	"github.com/mmt/mmt-studio-core/nf/n3iwf/esp"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/gtpu"
)

// Bridge ties one UE PDU session's ESP child SA to its N3 GTP-U
// tunnel. Inbound (UE→UPF): ESP decap + GTP-U encap. Outbound
// (UPF→UE): GTP-U decode + ESP encap.
type Bridge struct {
	// SAIn decrypts inbound ESP packets (UE → N3IWF direction). The
	// UE's outbound key is N3IWF's inbound key — see the §2.17
	// KEYMAT layout in nf/n3iwf/ikev2.
	SAIn *esp.SA
	// SAOut encrypts outbound ESP packets (N3IWF → UE direction).
	SAOut *esp.SA

	// TEIDUp is the UPF-side TEID we PUT in outbound G-PDUs.
	TEIDUp uint32
	// TEIDDown is N3IWF's inbound TEID — we VALIDATE inbound G-PDUs
	// land on this value (UPF→N3IWF direction).
	TEIDDown uint32

	// UEAddr is the UE's UDP/4500 endpoint. The transport layer
	// reads this to forward a freshly-encapped ESP-in-UDP packet
	// back to the right UE after a UPF→N3IWF G-PDU has been
	// handled. nil-safe: tests that don't drive sockets leave it.
	UEAddr *net.UDPAddr
	// UPFAddr is the UPF's UDP/2152 (GTP-U) endpoint — destination
	// for G-PDUs the bridge produces from inbound ESP traffic.
	UPFAddr *net.UDPAddr
}

// NewBridge builds a bridge from already-derived per-direction
// ChildSA keys (caller has those from §2.17 DeriveChildSAKeys via
// the ikev2 package). encrIn/integIn = peer→us keys; encrOut/integOut
// = us→peer keys. spiIn / spiOut are the 32-bit ESP SPIs from the
// CREATE_CHILD_SA exchange.
func NewBridge(
	spiIn, spiOut uint32,
	encrIn, integIn, encrOut, integOut []byte,
	teidUp, teidDown uint32,
) (*Bridge, error) {
	saIn, err := esp.NewSA(spiIn, encrIn, integIn)
	if err != nil {
		return nil, fmt.Errorf("userplane: SAIn: %w", err)
	}
	saOut, err := esp.NewSA(spiOut, encrOut, integOut)
	if err != nil {
		return nil, fmt.Errorf("userplane: SAOut: %w", err)
	}
	return &Bridge{
		SAIn:     saIn,
		SAOut:    saOut,
		TEIDUp:   teidUp,
		TEIDDown: teidDown,
	}, nil
}

// HandleNWu processes one ESP packet received from the UE side.
// Decaps, validates the inner IP version is what we expected, and
// returns the GTP-U packet ready to send on N3 toward the UPF.
//
// Per TS 24.502 §7.4 the user-plane child SA carries IP packets
// (the "T-PDU" per TS 29.281 §3.1) — Next Header is IPv4 or IPv6.
func (b *Bridge) HandleNWu(espPkt []byte) ([]byte, error) {
	inner, nextHdr, err := b.SAIn.Decap(espPkt)
	if err != nil {
		return nil, fmt.Errorf("userplane NWu→N3: ESP decap: %w", err)
	}
	if nextHdr != esp.NextHdrIPv4 && nextHdr != esp.NextHdrIPv6 {
		return nil, fmt.Errorf("userplane NWu→N3: unexpected next-header %d (want IPv4/IPv6 per TS 24.502 §7.4)",
			nextHdr)
	}
	gpdu, err := gtpu.EncapGPDU(b.TEIDUp, inner)
	if err != nil {
		return nil, fmt.Errorf("userplane NWu→N3: GTP-U encap: %w", err)
	}
	return gpdu, nil
}

// HandleN3 processes one GTP-U packet received from the UPF side.
// Validates it's a G-PDU on our TEIDDown, peels the T-PDU, and
// returns an ESP packet ready for the NWu socket.
//
// nextHdr selects the inner packet's IP version when re-wrapping
// (TS 24.502 §7.4 calls out IP-only — IPv4 most common today; we
// peek at the inner IP version nibble to pick the right NextHdr).
func (b *Bridge) HandleN3(gpduPkt []byte) ([]byte, error) {
	hdr, tpdu, err := gtpu.DecodeGPDU(gpduPkt)
	if err != nil {
		return nil, fmt.Errorf("userplane N3→NWu: GTP-U decode: %w", err)
	}
	if hdr.TEID != b.TEIDDown {
		return nil, fmt.Errorf("userplane N3→NWu: TEID %x != bridge TEIDDown %x",
			hdr.TEID, b.TEIDDown)
	}
	if len(tpdu) == 0 {
		return nil, errors.New("userplane N3→NWu: empty T-PDU")
	}
	// Peek the IP version: high nibble of byte 0 is the IP version
	// field — 4 for IPv4, 6 for IPv6.
	nextHdr := uint8(esp.NextHdrIPv4)
	if tpdu[0]>>4 == 6 {
		nextHdr = esp.NextHdrIPv6
	}
	espPkt, err := b.SAOut.Encap(tpdu, nextHdr)
	if err != nil {
		return nil, fmt.Errorf("userplane N3→NWu: ESP encap: %w", err)
	}
	return espPkt, nil
}
