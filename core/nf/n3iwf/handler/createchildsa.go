// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package handler

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/mmt/mmt-studio-core/nf/n3iwf/ctx"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ikev2"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// handleCreateChildSA implements the CREATE_CHILD_SA exchange of
// RFC 7296 §1.3 — used by N3IWF as the IPsec child-SA establishment
// path defined in TS 24.502 §7.4 (signalling SA on NWu, plus
// per-PDU-session user-plane SAs).
//
// Inbound (after §3.14 SK decrypt):
//
//	SA   — proposal list, ProtocolID=ESP, 4-octet Initiator SPI per
//	       RFC 4303 §2 (§3.3.1 "SPI Size = 4")
//	Ni   — fresh nonce, §2.17 KEYMAT input
//	[KEi]— optional Diffie-Hellman public for PFS (§1.3.1)
//	TSi  — Initiator traffic selectors (§3.13)
//	TSr  — Responder traffic selectors
//
// Outbound:
//
//	SA   — chosen proposal with our 4-octet Responder SPI
//	Nr   — our nonce
//	[KEr]— our DH public if KEi was present
//	TSi/TSr — accepted (we don't narrow today; echo)
//
// Per §2.17 we then derive:
//
//	KEYMAT = prf+(SK_d, [g^ir(new) |] Ni | Nr)
//	{SK_ei | SK_ai | SK_er | SK_ar}
//
// and stash a new ChildSA on the UEContext. Caller is the IKEv2
// dispatch in Handle() — message is wrapped in SK using SuiteI/SuiteR
// of the parent IKE SA (§1.3 "Subsequent IKEv2 messages are
// cryptographically protected ...").
func (h *Handler) handleCreateChildSA(hdr *ikev2.Header, msg []byte, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")
	u := h.mgr.LookupBySPIi(hdr.SPIi)
	if u == nil {
		log.Warnf("CREATE_CHILD_SA from %s: no UE context for SPIi=%x", src, hdr.SPIi)
		return nil, errors.New("no UE for SPIi")
	}
	if u.SuiteI == nil || u.SuiteR == nil {
		return nil, errors.New("CREATE_CHILD_SA before IKE_AUTH (no SK keys)")
	}

	// SK-decrypt — RFC 7296 §3.14.
	_, _, inner, err := u.SuiteI.DecryptMessage(msg)
	if err != nil {
		log.Warnf("CREATE_CHILD_SA from %s: SK decrypt failed: %v", src, err)
		return nil, err
	}

	saP := ikev2.Find(inner, ikev2.PayloadSA)
	nonceP := ikev2.Find(inner, ikev2.PayloadNonce)
	tsiP := ikev2.Find(inner, ikev2.PayloadTSi)
	tsrP := ikev2.Find(inner, ikev2.PayloadTSr)
	if saP == nil || nonceP == nil || tsiP == nil || tsrP == nil {
		log.Warnf("CREATE_CHILD_SA from %s: missing mandatory SA/Nonce/TSi/TSr", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	saIn, err := ikev2.ParseSA(saP.Data)
	if err != nil {
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	nonceI, err := ikev2.ParseNonce(nonceP.Data)
	if err != nil {
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}

	// Pick an ESP proposal we accept (AES-CBC-256 + HMAC-SHA-256-128).
	// Initiator's SPI is in the chosen proposal's SPI field per
	// RFC 7296 §3.3.1 ("the SPI is the SPI by which the SA is named
	// at the sending peer").
	chosenIn, err := pickAcceptableESPProposal(saIn)
	if err != nil {
		log.Warnf("CREATE_CHILD_SA from %s: no acceptable ESP proposal", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyNoProposalChosen)
	}
	if len(chosenIn.SPI) != 4 {
		log.Warnf("CREATE_CHILD_SA from %s: ESP SPI size %d != 4 (RFC 4303 §2)",
			src, len(chosenIn.SPI))
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	peerSPI := binary.BigEndian.Uint32(chosenIn.SPI)

	// PFS DH (§1.3.1) — optional. If KEi is present, run DH and feed
	// the new shared secret into the §2.17 KDF.
	var (
		gIRNew []byte
		ourKE  *ikev2.KE
	)
	if keP := ikev2.Find(inner, ikev2.PayloadKE); keP != nil {
		keIn, err := ikev2.ParseKE(keP.Data)
		if err != nil {
			return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
		}
		// The initiator's DH group must equal what they offered in
		// the SA (§1.3.1). We don't presently advertise PFS DH
		// transforms — reject so the peer drops the KE.
		if getTransformID(chosenIn, ikev2.TransformDH) != keIn.DHGroup {
			return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidKEPayload)
		}
		dh, err := ikev2.NewDH(keIn.DHGroup)
		if err != nil {
			return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidKEPayload)
		}
		priv, pub, err := dh.GenerateLocal()
		if err != nil {
			return nil, err
		}
		shared, err := dh.SharedSecret(priv, keIn.Public)
		if err != nil {
			return nil, fmt.Errorf("CREATE_CHILD_SA DH: %w", err)
		}
		gIRNew = shared
		ourKE = &ikev2.KE{DHGroup: keIn.DHGroup, Public: pub}
	}

	// Mint our SPI + nonce.
	ourSPI, err := freshESPSPI()
	if err != nil {
		return nil, err
	}
	nr := make([]byte, 32)
	if _, err := rand.Read(nr); err != nil {
		return nil, err
	}

	// §2.17 KEYMAT.
	prf, err := ikev2.NewPRF(u.PRFID)
	if err != nil {
		return nil, err
	}
	encrKeyLen := encrKeyLenForProposal(chosenIn)
	if encrKeyLen == 0 {
		encrKeyLen = u.EncrKeyLen // ENCR transform without explicit KeyLength attr — fall back to IKE SA's
	}
	integLen := integKeyLen(getTransformID(chosenIn, ikev2.TransformINTEG))
	if integLen == 0 {
		// AEAD proposals would carry zero, but we don't accept those
		// yet — fail explicitly so the wire bug surfaces.
		return h.encryptedNotify(u, hdr, ikev2.NotifyNoProposalChosen)
	}
	childKeys, err := prf.DeriveChildSAKeys(u.Keys.SK_d, []byte(nonceI), nr, gIRNew,
		encrKeyLen, integLen)
	if err != nil {
		return nil, fmt.Errorf("CREATE_CHILD_SA KEYMAT: %w", err)
	}

	// Stash on the UE.
	signalling := len(u.ChildSAs) == 0
	child := ctx.ChildSA{
		SPIIn:       ourSPI,
		SPIOut:      peerSPI,
		EncrKeyIn:   childKeys.SK_ei, // initiator → responder direction
		IntegKeyIn:  childKeys.SK_ai,
		EncrKeyOut:  childKeys.SK_er, // responder → initiator
		IntegKeyOut: childKeys.SK_ar,
		TSiBytes:    append([]byte(nil), tsiP.Data...),
		TSrBytes:    append([]byte(nil), tsrP.Data...),
		NonceI:      append([]byte(nil), nonceI...),
		NonceR:      append([]byte(nil), nr...),
		Signalling:  signalling,
		CreatedAt:   time.Now(),
	}
	// User-plane SAs need a fresh inbound TEID for the UPF→N3IWF
	// G-PDU direction. Signalling SAs don't terminate on N3 (NAS
	// rides the ESP tunnel inside TCP per TS 24.502 §7.4) so leave
	// TEIDDown zero.
	if !signalling {
		child.TEIDDown = h.mgr.AllocateTEID()
	}
	u.ChildSAs = append(u.ChildSAs, child)
	u.LastActivity = time.Now()
	if u.State == ctx.StateRegistered {
		u.State = ctx.StatePDUActive
	}

	log.Infof("CREATE_CHILD_SA UE-%d: ESP SPIi=%08x SPIr=%08x signalling=%v TEIDDown=%08x (TS 24.502 §7.4)",
		u.UEID, peerSPI, ourSPI, child.Signalling, child.TEIDDown)

	// Build response. Chosen proposal — clone with our 4-octet SPI
	// in place of the initiator's. Drop attributes from the matched
	// transforms that we don't want to echo (only KeyLength matters).
	respProp := chosenIn
	respProp.SPI = make([]byte, 4)
	binary.BigEndian.PutUint32(respProp.SPI, ourSPI)
	respSA := &ikev2.SA{Proposals: []ikev2.Proposal{respProp}}

	respPayloads := []ikev2.Payload{
		{Type: ikev2.PayloadSA, Data: respSA.Marshal()},
		{Type: ikev2.PayloadNonce, Data: nr},
	}
	if ourKE != nil {
		respPayloads = append(respPayloads,
			ikev2.Payload{Type: ikev2.PayloadKE, Data: ourKE.Marshal()})
	}
	// Echo TSi/TSr unchanged — §2.9 lets the responder narrow but we
	// don't today. Caller can plug a narrower in later for strict
	// per-PDU-session selectors.
	respPayloads = append(respPayloads,
		ikev2.Payload{Type: ikev2.PayloadTSi, Data: append([]byte(nil), tsiP.Data...)},
		ikev2.Payload{Type: ikev2.PayloadTSr, Data: append([]byte(nil), tsrP.Data...)},
	)

	respHdr := ikev2.Header{
		SPIi:         u.IKEInitiator,
		SPIr:         u.IKEResponder,
		ExchangeType: ikev2.ExchangeCreateChildSA,
		Flags:        ikev2.FlagResponse,
		MessageID:    hdr.MessageID,
	}
	return u.SuiteR.EncryptedMessage(respHdr, nil, respPayloads)
}

// pickAcceptableESPProposal walks an SA payload and returns the first
// ESP proposal matching our policy (AES-CBC-256 + HMAC-SHA-256-128).
// Same shape as pickAcceptableProposal but for ProtocolESP and without
// a mandatory PRF/DH transform.
//
// Returns the proposal AS-IS (preserves the initiator's 4-octet SPI
// in p.SPI so the caller can extract the peer's outbound SPI per
// RFC 7296 §3.3.1) — caller substitutes its own SPI when building
// the response.
func pickAcceptableESPProposal(sa *ikev2.SA) (ikev2.Proposal, error) {
	want := map[ikev2.TransformType]uint16{
		ikev2.TransformENCR:  ikev2.ENCR_AES_CBC,
		ikev2.TransformINTEG: ikev2.INTEG_HMAC_SHA256_128,
	}
	for _, p := range sa.Proposals {
		if p.ProtocolID != ikev2.ProtocolESP {
			continue
		}
		matched := map[ikev2.TransformType]ikev2.Transform{}
		for _, t := range p.Transforms {
			if id, need := want[t.Type]; need && t.ID == id {
				matched[t.Type] = t
			}
		}
		if len(matched) != len(want) {
			continue
		}
		// AES-CBC requires KeyLength=256.
		if !hasKeyLength(matched[ikev2.TransformENCR], 256) {
			continue
		}
		// Pull ESN as offered (§3.3.2 ESN_NONE / ESN_USE) — default
		// to ESN_NONE if peer omitted it.
		esn := ikev2.Transform{Type: ikev2.TransformESN, ID: ikev2.ESN_NONE}
		for _, t := range p.Transforms {
			if t.Type == ikev2.TransformESN {
				esn = t
				break
			}
		}
		out := ikev2.Proposal{
			Num:        p.Num,
			ProtocolID: ikev2.ProtocolESP,
			SPI:        append([]byte(nil), p.SPI...),
			Transforms: []ikev2.Transform{
				matched[ikev2.TransformENCR],
				matched[ikev2.TransformINTEG],
				esn,
			},
		}
		// Preserve a DH transform if it was offered + matched (PFS
		// handling above looks at chosen.Transforms via getTransformID).
		for _, t := range p.Transforms {
			if t.Type == ikev2.TransformDH {
				out.Transforms = append(out.Transforms, t)
				break
			}
		}
		return out, nil
	}
	return ikev2.Proposal{}, errors.New("no acceptable ESP proposal")
}

// freshESPSPI returns a non-zero 32-bit ESP SPI, avoiding the
// reserved range 0..255 (RFC 7296 §2.6: "values 0 through 255 are
// reserved by IANA"). Equivalent of ctx.FreshSPIr but for ESP's
// 4-octet field per RFC 4303 §2.
func freshESPSPI() (uint32, error) {
	for {
		var b [4]byte
		if _, err := rand.Read(b[:]); err != nil {
			return 0, err
		}
		v := binary.BigEndian.Uint32(b[:])
		if v > 255 {
			return v, nil
		}
	}
}
