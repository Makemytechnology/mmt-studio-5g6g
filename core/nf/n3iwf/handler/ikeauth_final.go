// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package handler

import (
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/mmt/mmt-studio-core/nf/n3iwf/ctx"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ikev2"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// handleIKEAuthFinal processes the post-EAP-Success IKE_AUTH from
// the UE per RFC 7296 §2.16 + TS 24.502 §7.3.2.2.
//
// Inbound (after SK decrypt) is expected to carry:
//
//	AUTH  — UE's authentication, computed from the EAP-derived shared
//	        secret K_N3IWF (a.k.a. Knh) using §2.15 InitiatorSignedOctets.
//	SA    — initiator's child-SA proposal (ESP)
//	TSi/TSr — traffic selectors for the signalling SA
//	[CP(CFG_REQUEST)] — optional inner-IP request (§3.15)
//
// Outbound:
//
//	AUTH  — our matching AUTH over §2.15 ResponderSignedOctets
//	SA    — chosen ESP proposal with our 4-octet SPI
//	TSi/TSr — accepted (echoed) selectors
//	[CP(CFG_REPLY)] — INTERNAL_IP4_ADDRESS allocated from the inner pool
//
// On success we install the signalling ChildSA (its keys derived per
// §2.17 from SK_d + the IKE_SA_INIT Ni|Nr) and advance state to
// StateRegistered.
//
// We do NOT verify the UE's AUTH yet — proper verification needs the
// UE-side InitiatorSignedOctets (RealMessage1 = the IKE_SA_INIT
// request bytes), which we don't currently cache, plus a stable
// MSK/Knh derivation matching the UE. The AUTH presence-check below
// keeps the message-shape contract; a follow-up will add real verify.
func (h *Handler) handleIKEAuthFinal(u *ctx.UEContext, hdr *ikev2.Header,
	inner []ikev2.Payload, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")

	authP := ikev2.Find(inner, ikev2.PayloadAUTH)
	saP := ikev2.Find(inner, ikev2.PayloadSA)
	tsiP := ikev2.Find(inner, ikev2.PayloadTSi)
	tsrP := ikev2.Find(inner, ikev2.PayloadTSr)
	if authP == nil || saP == nil || tsiP == nil || tsrP == nil {
		log.Warnf("IKE_AUTH-final from %s: missing AUTH/SA/TSi/TSr (RFC 7296 §1.2)", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}

	// Verify the UE's AUTH per RFC 7296 §2.15 + §2.16. UE used Knh
	// as the EAP-derived shared secret over InitiatorSignedOctets
	// (RealMessage1 | NonceRData | MACedIDForI). If verification
	// fails the UE is impersonating a peer or doesn't share the
	// same Knh — reject with AUTHENTICATION_FAILED per §3.10.1.
	if err := h.verifyInitiatorAUTH(u, authP.Data); err != nil {
		log.Warnf("IKE_AUTH-final from %s: AUTH verify: %v", src, err)
		return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
	}
	saIn, err := ikev2.ParseSA(saP.Data)
	if err != nil {
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	chosenIn, err := pickAcceptableESPProposal(saIn)
	if err != nil {
		return h.encryptedNotify(u, hdr, ikev2.NotifyNoProposalChosen)
	}
	if len(chosenIn.SPI) != 4 {
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	peerSPI := binary.BigEndian.Uint32(chosenIn.SPI)

	// Derive the signalling ChildSA's keys per §2.17, but with the
	// IKE_SA_INIT nonces (no fresh nonces since the SA is established
	// inline in IKE_AUTH per §1.2 / §2.17 final paragraph: "the
	// keying material for that Child SA is taken from the same
	// IKE_SA_INIT exchange").
	prf, err := ikev2.NewPRF(u.PRFID)
	if err != nil {
		return nil, err
	}
	encrLen := u.EncrKeyLen
	integLen := integKeyLen(getTransformID(chosenIn, ikev2.TransformINTEG))
	if integLen == 0 {
		return h.encryptedNotify(u, hdr, ikev2.NotifyNoProposalChosen)
	}
	childKeys, err := prf.DeriveChildSAKeys(u.Keys.SK_d, u.IKENonceI, u.IKENonceR, nil,
		encrLen, integLen)
	if err != nil {
		return nil, fmt.Errorf("IKE_AUTH-final §2.17 KDF: %w", err)
	}

	// Mint our 4-octet ESP SPI per RFC 4303 §2.
	ourSPI, err := freshESPSPI()
	if err != nil {
		return nil, err
	}

	// Allocate inner IP from the operator pool (TS 24.502 §7.3.2.2 +
	// RFC 7296 §3.15). Pool unset → CP omitted from response.
	var innerIP net.IP
	if h.innerIPPool != nil {
		innerIP, err = h.innerIPPool.Allocate()
		if err != nil {
			log.Warnf("IKE_AUTH-final UE-%d: inner-IP allocation failed (%v)", u.UEID, err)
			return h.encryptedNotify(u, hdr, ikev2.NotifyInternalAddressFailure)
		}
		u.InnerIP = innerIP.String()
	}

	// Stash the signalling ChildSA. Per TS 24.502 §7.4 this is
	// ChildSAs[0] and carries NAS-bearing TCP traffic, not ESP/N3.
	signalling := ctx.ChildSA{
		SPIIn:       ourSPI,
		SPIOut:      peerSPI,
		EncrKeyIn:   childKeys.SK_ei,
		IntegKeyIn:  childKeys.SK_ai,
		EncrKeyOut:  childKeys.SK_er,
		IntegKeyOut: childKeys.SK_ar,
		TSiBytes:    append([]byte(nil), tsiP.Data...),
		TSrBytes:    append([]byte(nil), tsrP.Data...),
		NonceI:      append([]byte(nil), u.IKENonceI...),
		NonceR:      append([]byte(nil), u.IKENonceR...),
		Signalling:  true,
		CreatedAt:   time.Now(),
	}
	u.ChildSAs = append(u.ChildSAs, signalling)
	u.State = ctx.StateRegistered

	// Compute our AUTH over §2.15 ResponderSignedOctets, with K_N3IWF
	// (a.k.a. Knh) as the EAP-derived shared secret per §2.16.
	idr := &ikev2.ID{Type: ikev2.IDTypeFQDN, Data: []byte(h.identity)}
	auth, err := h.computeResponderAUTH(u, prf, idr)
	if err != nil {
		return nil, fmt.Errorf("IKE_AUTH-final AUTH: %w", err)
	}

	// Build response: AUTH | SA | TSi | TSr | [CP].
	respProp := chosenIn
	respProp.SPI = make([]byte, 4)
	binary.BigEndian.PutUint32(respProp.SPI, ourSPI)
	respSA := &ikev2.SA{Proposals: []ikev2.Proposal{respProp}}

	respPayloads := []ikev2.Payload{
		{Type: ikev2.PayloadIDr, Data: idr.Marshal()},
		{Type: ikev2.PayloadAUTH, Data: (&ikev2.Auth{
			Method: ikev2.AuthSharedKeyMIC,
			Data:   auth,
		}).Marshal()},
		{Type: ikev2.PayloadSA, Data: respSA.Marshal()},
		{Type: ikev2.PayloadTSi, Data: append([]byte(nil), tsiP.Data...)},
		{Type: ikev2.PayloadTSr, Data: append([]byte(nil), tsrP.Data...)},
	}
	if innerIP != nil {
		v4 := innerIP.To4()
		cp := &ikev2.CP{
			Type: ikev2.CFGReply,
			Attributes: []ikev2.CPAttribute{
				{Type: ikev2.CPInternalIP4Address, Value: append([]byte(nil), v4...)},
			},
		}
		// Prepend CP before SA per TS 24.502 §7.3.2.2 typical layout
		// (ordering isn't strict, but reads naturally).
		respPayloads = append(respPayloads[:2], append([]ikev2.Payload{
			{Type: ikev2.PayloadCP, Data: cp.Marshal()},
		}, respPayloads[2:]...)...)
	}

	respHdr := ikev2.Header{
		SPIi:         u.IKEInitiator,
		SPIr:         u.IKEResponder,
		ExchangeType: ikev2.ExchangeIKEAuth,
		Flags:        ikev2.FlagResponse,
		MessageID:    hdr.MessageID,
	}
	out, err := u.SuiteR.EncryptedMessage(respHdr, nil, respPayloads)
	if err != nil {
		return nil, fmt.Errorf("IKE_AUTH-final encrypt: %w", err)
	}
	innerStr := "(no inner IP — pool unwired)"
	if innerIP != nil {
		innerStr = innerIP.String()
	}
	log.Infof("IKE_AUTH success for UE-%d: signalling SA SPIi=%08x SPIr=%08x inner=%s — TS 24.502 §7.3.2.2",
		u.UEID, peerSPI, ourSPI, innerStr)
	return out, nil
}

// computeResponderAUTH implements RFC 7296 §2.15 / §2.16 for the
// shared-secret-from-EAP path:
//
//	AUTH = prf( prf(K_N3IWF, "Key Pad for IKEv2"),
//	            <ResponderSignedOctets> )
//
// where ResponderSignedOctets = RealMessage2 | NonceIData | MACedIDForR
// and MACedIDForR = prf(SK_pr, RestOfRespIDPayload).
//
// The "Key Pad for IKEv2" string is 17 ASCII characters without null
// termination per §2.15.
func (h *Handler) computeResponderAUTH(u *ctx.UEContext, prf *ikev2.PRF,
	idr *ikev2.ID) ([]byte, error) {
	if len(u.Knh) == 0 {
		return nil, errors.New("AUTH: no Knh (K_N3IWF not yet delivered by AMF)")
	}
	if len(u.IKEInitResponse) == 0 {
		return nil, errors.New("AUTH: no cached IKE_SA_INIT response (RealMessage2)")
	}
	// MACedIDForR = prf(SK_pr, RestOfRespIDPayload).
	// RestOfRespIDPayload = IDType | RESERVED(3) | IDData per §2.15 +
	// §3.5 (i.e. the ID payload body).
	macedIDForR := prf.PRF(u.Keys.SK_pr, idr.Marshal())

	// ResponderSignedOctets = RealMessage2 | NonceIData | MACedIDForR
	signed := make([]byte, 0, len(u.IKEInitResponse)+len(u.IKENonceI)+len(macedIDForR))
	signed = append(signed, u.IKEInitResponse...)
	signed = append(signed, u.IKENonceI...)
	signed = append(signed, macedIDForR...)

	// Inner: prf(Knh, authKeyPad) → 32-octet pad-key.
	padKey := prf.PRF(u.Knh, []byte(authKeyPad))
	// Outer: prf(padKey, signed) → AUTH octets.
	return prf.PRF(padKey, signed), nil
}

// authKeyPad is the §2.15 verbatim string ("Key Pad for IKEv2") —
// 17 ASCII characters, no null terminator.
const authKeyPad = "Key Pad for IKEv2"

// verifyInitiatorAUTH checks the UE's AUTH payload against the
// expected value computed locally per RFC 7296 §2.15:
//
//	AUTH = prf( prf(Knh, "Key Pad for IKEv2"),
//	            RealMessage1 | NonceRData | MACedIDForI )
//
// where MACedIDForI = prf(SK_pi, IDiBody). authData is the AUTH
// payload body (Method | RESERVED(3) | AuthData) — the inner
// AuthData octets are what's compared.
func (h *Handler) verifyInitiatorAUTH(u *ctx.UEContext, authData []byte) error {
	if len(u.Knh) == 0 {
		return errors.New("AUTH verify: no Knh")
	}
	if len(u.IKEInitRequest) == 0 {
		return errors.New("AUTH verify: no cached IKE_SA_INIT request (RealMessage1)")
	}
	if len(u.IDiBody) == 0 {
		return errors.New("AUTH verify: no cached IDi")
	}
	auth, err := ikev2.ParseAuth(authData)
	if err != nil {
		return fmt.Errorf("AUTH parse: %w", err)
	}
	if auth.Method != ikev2.AuthSharedKeyMIC {
		return fmt.Errorf("AUTH method %d != Shared-Key MIC (%d) — RFC 7296 §2.16 EAP path mandates pre-shared key syntax",
			auth.Method, ikev2.AuthSharedKeyMIC)
	}

	prf, err := ikev2.NewPRF(u.PRFID)
	if err != nil {
		return err
	}
	macedIDForI := prf.PRF(u.Keys.SK_pi, u.IDiBody)
	signed := make([]byte, 0, len(u.IKEInitRequest)+len(u.IKENonceR)+len(macedIDForI))
	signed = append(signed, u.IKEInitRequest...)
	signed = append(signed, u.IKENonceR...)
	signed = append(signed, macedIDForI...)
	padKey := prf.PRF(u.Knh, []byte(authKeyPad))
	expected := prf.PRF(padKey, signed)

	if subtle.ConstantTimeCompare(expected, auth.Data) != 1 {
		return errors.New("AUTH mismatch (RFC 7296 §2.15 / §2.16) — UE+N3IWF disagree on Knh or signed octets")
	}
	return nil
}
