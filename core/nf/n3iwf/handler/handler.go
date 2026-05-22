// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package handler — N3IWF IKEv2 protocol state machine.
//
// The handler is fed parsed UDP datagrams (raw IKEv2 messages) from
// the transport layer and returns the response bytes (or nil for
// fire-and-forget messages). It owns the §7.3 procedure flow:
//
//	UE → IKE_SA_INIT → N3IWF      (RFC 7296 §1.2 + §3.3 SA negotiation)
//	UE ← IKE_SA_INIT ← N3IWF      (responder SA + KE + Nonce)
//	UE → IKE_AUTH (no AUTH, IDi=ID_KEY_ID, CERTREQ?)  (TS 24.502 §7.3.2.1)
//	UE ← IKE_AUTH (EAP-Request/5G-Start, IDr, CERT?)  (TS 24.502 §7.3.2.1)
//	UE ↔ EAP-5G NAS exchanges                          (TS 24.502 §7.3.3)
//	UE ← EAP-Success → signalling IPsec SA up         (TS 24.502 §7.3.2.2)
//
// For IKE_AUTH and beyond, all messages are wrapped in §3.14 SK
// payloads using the suite negotiated in IKE_SA_INIT — the
// nf/n3iwf/ikev2 package handles the encrypt/decrypt + ICV.
//
// Authoritative specs: TS 24.502 v19.3.0 §7.3 (PDF:
// specs/3gpp/ts_124502v190300p.pdf), RFC 7296 (specs/ietf/rfc7296.txt).
package handler

import (
	"crypto/rand"
	"errors"
	"fmt"
	"net"
	"time"

	"github.com/mmt/mmt-studio-core/nf/n3iwf/ctx"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/eap5g"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ikev2"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/ipool"
	"github.com/mmt/mmt-studio-core/nf/n3iwf/userplane"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Handler routes inbound IKEv2 datagrams to per-exchange handlers.
type Handler struct {
	mgr      *ctx.Manager
	identity string // N3IWF FQDN or IP, used as IDr (TS 24.502 §7.3.2.1)

	// bridge forwards EAP-5G-wrapped NAS to the AMF and feeds DL
	// NAS bytes back. nil-safe: when no bridge is set the handler
	// completes IKE_SA_INIT + the IKE_AUTH initial response (IDr +
	// EAP-Request/5G-Start) but cannot forward subsequent
	// EAP-Response/5G-NAS exchanges. Tests supply a fake bridge.
	bridge NASBridge

	// nasTimeout is how long the handler blocks waiting for an AMF
	// DL NAS reply when forwarding an EAP-Response/5G-NAS. Default
	// 5 s — long enough for AMF + AUSF/UDM round-trips, short
	// enough that a stuck AMF doesn't park IKE retransmits.
	nasTimeout time.Duration

	// registry is the user-plane Bridge index. nil when the N3IWF
	// runs in IKE-only mode (no UPF wired); set by SetRegistry
	// during boot. CREATE_CHILD_SA stores key material on
	// UEContext.ChildSA but defers Bridge construction to whoever
	// receives the UPF-side TEID via PDUSessionResourceSetupRequest
	// — that caller invokes RegisterUPSA below.
	registry *userplane.Registry

	// innerIPPool allocates the address the N3IWF returns in the
	// IKE_AUTH success response's CP(CFG_REPLY) payload per
	// TS 24.502 §7.3.2.2 + RFC 7296 §3.15 — the UE's source
	// address inside the IPsec tunnel. nil leaves the IKE_AUTH
	// flow to omit CP (legal but the UE then has no inner IP).
	innerIPPool *ipool.Pool
}

// New builds a handler. identity is what we stuff in the IDr
// payload of the IKE_AUTH response — an FQDN or dotted-quad IP per
// TS 24.502 §7.3.2.1 NOTE: "The N3IWF identifier is the IP address
// or the FQDN of the N3IWF."
//
// bridge may be nil — the handler still completes IKE_SA_INIT and
// emits the IKE_AUTH initial response, but it cannot forward NAS to
// the AMF. Production wires nf/n3iwf/n2.Manager via an adapter.
func New(mgr *ctx.Manager, identity string) *Handler {
	return &Handler{mgr: mgr, identity: identity, nasTimeout: 5 * time.Second}
}

// SetBridge wires the AMF-facing bridge after construction. Useful
// when the bridge depends on Handler being live (some adapters need
// the handler's UE Manager).
func (h *Handler) SetBridge(b NASBridge) { h.bridge = b }

// SetRegistry wires the user-plane Bridge index. Without this,
// CREATE_CHILD_SA still derives + stashes ESP keys on UEContext but
// no live ESP↔GTP-U dispatch happens — the transport layer will
// drop ESP-in-UDP traffic per RFC 4303 §3.4.2.
func (h *Handler) SetRegistry(r *userplane.Registry) { h.registry = r }

// SetInnerIPPool wires the inner-IP allocator the IKE_AUTH success
// path uses to fill the CP(CFG_REPLY) INTERNAL_IP4_ADDRESS attribute
// (TS 24.502 §7.3.2.2). nil leaves CP omitted from the response.
func (h *Handler) SetInnerIPPool(p *ipool.Pool) { h.innerIPPool = p }

// RegisterUPSA finalises a user-plane child SA by binding it to the
// UPF-side TEID and registering a Bridge in the user-plane registry.
// Called by external N2 PDU Session Resource Setup logic once the
// AMF has supplied the UPF's GTP-U info (TS 38.413 §9.3.4.1).
//
// childIdx selects which entry of UEContext.ChildSAs to bind —
// the caller (PDU Session Setup handler) tracks the index as it
// matches inbound CREATE_CHILD_SA exchanges to PDU sessions.
//
// ueAddr / upfAddr are the UDP endpoints the transport layer needs
// for forwarding (UE's UDP/4500, UPF's UDP/2152).
func (h *Handler) RegisterUPSA(ueID, childIdx int, teidUp uint32,
	ueAddr, upfAddr *net.UDPAddr) error {
	if h.registry == nil {
		return errors.New("handler: no user-plane registry — N3IWF in IKE-only mode")
	}
	u := h.mgr.LookupByID(ueID)
	if u == nil {
		return fmt.Errorf("handler: UE-%d not found", ueID)
	}
	if childIdx < 0 || childIdx >= len(u.ChildSAs) {
		return fmt.Errorf("handler: child SA index %d out of range (have %d)",
			childIdx, len(u.ChildSAs))
	}
	csa := &u.ChildSAs[childIdx]
	if csa.Signalling {
		return fmt.Errorf("handler: child SA %d is signalling (no GTP-U binding)", childIdx)
	}
	if csa.TEIDDown == 0 {
		return fmt.Errorf("handler: child SA %d has no TEIDDown allocated", childIdx)
	}
	bridge, err := userplane.NewBridge(
		csa.SPIIn, csa.SPIOut,
		csa.EncrKeyIn, csa.IntegKeyIn,
		csa.EncrKeyOut, csa.IntegKeyOut,
		teidUp, csa.TEIDDown,
	)
	if err != nil {
		return fmt.Errorf("handler: NewBridge: %w", err)
	}
	bridge.UEAddr = ueAddr
	bridge.UPFAddr = upfAddr
	if err := h.registry.Add(bridge); err != nil {
		return fmt.Errorf("handler: registry.Add: %w", err)
	}
	return nil
}

// Handle decodes one IKEv2 datagram from src and returns response
// bytes (or nil if no response is appropriate). Does not transmit
// — that's the transport layer's job.
func (h *Handler) Handle(msg []byte, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")
	hdr, err := ikev2.ParseHeader(msg)
	if err != nil {
		log.Warnf("IKEv2: bad header from %s: %v", src, err)
		return nil, err
	}
	if int(hdr.Length) != len(msg) {
		log.Warnf("IKEv2: hdr.Length=%d != msg=%d from %s", hdr.Length, len(msg), src)
		return nil, fmt.Errorf("hdr.Length mismatch")
	}
	switch hdr.ExchangeType {
	case ikev2.ExchangeIKESAInit:
		return h.handleIKESAInit(hdr, msg, src)
	case ikev2.ExchangeIKEAuth:
		return h.handleIKEAuth(hdr, msg, src)
	case ikev2.ExchangeCreateChildSA:
		return h.handleCreateChildSA(hdr, msg, src)
	case ikev2.ExchangeInformational:
		return h.handleInformational(hdr, msg, src)
	default:
		log.Warnf("IKEv2: unsupported exchange type %d from %s",
			hdr.ExchangeType, src)
		return nil, nil
	}
}

// ---- IKE_SA_INIT (RFC 7296 §1.2 + TS 24.502 §7.3.2.1) ----

func (h *Handler) handleIKESAInit(hdr *ikev2.Header, msg []byte, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")

	// Parse the payload chain.
	payloads, err := ikev2.ParsePayloads(msg[ikev2.HeaderLen:], hdr.NextPayload)
	if err != nil {
		return nil, fmt.Errorf("IKE_SA_INIT payloads: %w", err)
	}

	// Mandatory: SA, KE, Nonce. §1.2 + §3.3, §3.4, §3.9.
	saP := ikev2.Find(payloads, ikev2.PayloadSA)
	keP := ikev2.Find(payloads, ikev2.PayloadKE)
	nonceP := ikev2.Find(payloads, ikev2.PayloadNonce)
	if saP == nil || keP == nil || nonceP == nil {
		return nil, errors.New("IKE_SA_INIT missing mandatory SA/KE/Nonce payload")
	}
	saIn, err := ikev2.ParseSA(saP.Data)
	if err != nil {
		return nil, fmt.Errorf("SA decode: %w", err)
	}
	keIn, err := ikev2.ParseKE(keP.Data)
	if err != nil {
		return nil, fmt.Errorf("KE decode: %w", err)
	}
	nonceIn, err := ikev2.ParseNonce(nonceP.Data)
	if err != nil {
		return nil, fmt.Errorf("Nonce decode: %w", err)
	}

	// Pick a proposal we accept. For now, only AES-CBC-256 + HMAC-
	// SHA256-128 + PRF-HMAC-SHA256 + MODP-2048 (the operator-
	// mandated minimum from ikev2.IKEDefaultProposal). We walk the
	// initiator's proposals in order (§3.3 "from most preferred to
	// least preferred") and accept the first that matches.
	chosen, err := pickAcceptableProposal(saIn)
	if err != nil {
		return h.notifyResponse(hdr, ikev2.NotifyNoProposalChosen, nil), nil
	}
	if keIn.DHGroup != getTransformID(chosen, ikev2.TransformDH) {
		// §3.4: "If the selected proposal uses a different DH group
		// (other than NONE), the message MUST be rejected with a
		// Notify payload of type INVALID_KE_PAYLOAD."
		return h.notifyResponse(hdr, ikev2.NotifyInvalidKEPayload, nil), nil
	}

	// Now we know the suite — set up a UE context, run DH, derive
	// SK_*. The §2.14 SKEYSEED needs Ni|Nr (raw nonces), g^ir, and
	// the eight-octet SPIs from the IKE header.
	u := h.mgr.LookupByAddr(src.IP.String(), src.Port)
	if u == nil {
		u = h.mgr.Create(src.IP.String(), src.Port)
	}
	u.IKEInitiator = hdr.SPIi
	u.IKEInitRequest = append([]byte(nil), msg...) // RealMessage1 for §2.15
	spir, err := ctx.FreshSPIr()
	if err != nil {
		return nil, err
	}
	u.IKEResponder = spir
	u.IKENonceI = []byte(nonceIn)
	u.EncrID = getTransformID(chosen, ikev2.TransformENCR)
	u.PRFID = getTransformID(chosen, ikev2.TransformPRF)
	u.IntegID = getTransformID(chosen, ikev2.TransformINTEG)
	u.EncrKeyLen = encrKeyLenForProposal(chosen)

	// DH: §2.10 + RFC 3526.
	dh, err := ikev2.NewDH(keIn.DHGroup)
	if err != nil {
		return h.notifyResponse(hdr, ikev2.NotifyInvalidKEPayload, nil), nil
	}
	priv, pub, err := dh.GenerateLocal()
	if err != nil {
		return nil, err
	}
	gIR, err := dh.SharedSecret(priv, keIn.Public)
	if err != nil {
		return nil, fmt.Errorf("DH shared: %w", err)
	}
	u.DH = dh
	u.DHPriv = priv
	u.DHPub = pub
	u.SharedKey = gIR

	// Generate our nonce. §3.9: between 16 and 256 octets.
	nr := make([]byte, 32)
	if _, err := rand.Read(nr); err != nil {
		return nil, err
	}
	u.IKENonceR = nr

	// §2.14 keying.
	prf, err := ikev2.NewPRF(u.PRFID)
	if err != nil {
		return nil, err
	}
	integLen := integKeyLen(u.IntegID)
	keys, err := prf.DeriveIKESAKeys(u.IKENonceI, u.IKENonceR, u.SharedKey,
		u.IKEInitiator, u.IKEResponder, integLen, u.EncrKeyLen)
	if err != nil {
		return nil, err
	}
	u.Keys = keys

	// Build per-direction SK suites. SuiteI = decrypt UE→N3IWF
	// (uses SK_ai + SK_ei). SuiteR = encrypt N3IWF→UE (SK_ar + SK_er).
	if u.IntegID == ikev2.INTEG_HMAC_SHA256_128 {
		u.SuiteI, err = ikev2.NewAESCBC_HMACSHA256_128(keys.SK_ei, keys.SK_ai)
		if err == nil {
			u.SuiteR, err = ikev2.NewAESCBC_HMACSHA256_128(keys.SK_er, keys.SK_ar)
		}
	} else {
		err = fmt.Errorf("INTEG id %d not yet supported", u.IntegID)
	}
	if err != nil {
		return nil, err
	}

	h.mgr.RegisterSPIi(u)
	u.State = ctx.StateIKEInit
	log.Infof("IKE_SA_INIT from %s: suite=AES-CBC-%d/HMAC-SHA256-128/PRF-HMAC-SHA256/MODP-%d, SPIr=%x",
		src, u.EncrKeyLen*8, dhBitLen(keIn.DHGroup), spir)

	// Build the response. SA (chosen proposal) || KE (our public) ||
	// Nonce (Nr).
	respSA := &ikev2.SA{Proposals: []ikev2.Proposal{chosen}}
	respPayloads := []ikev2.Payload{
		{Type: ikev2.PayloadSA, Data: respSA.Marshal()},
		{Type: ikev2.PayloadKE, Data: (&ikev2.KE{DHGroup: keIn.DHGroup, Public: pub}).Marshal()},
		{Type: ikev2.PayloadNonce, Data: nr},
	}
	body, firstType := ikev2.MarshalPayloads(respPayloads)

	respHdr := &ikev2.Header{
		SPIi:         hdr.SPIi,
		SPIr:         spir,
		NextPayload:  firstType,
		ExchangeType: ikev2.ExchangeIKESAInit,
		Flags:        ikev2.FlagResponse, // Initiator clear, Response set per §3.1
		MessageID:    hdr.MessageID,
		Length:       uint32(ikev2.HeaderLen + len(body)),
	}
	out := append(ikev2.MarshalHeader(respHdr), body...)
	// Cache RealMessage2 for the §2.15 ResponderSignedOctets used
	// when computing the IKE_AUTH success response's AUTH payload.
	u.IKEInitResponse = append([]byte(nil), out...)
	return out, nil
}

// ---- IKE_AUTH (RFC 7296 §1.2 + TS 24.502 §7.3.2.1) ----
//
// On the initial IKE_AUTH request the UE per TS 24.502 §7.3.2.1
// includes:
//
//	- IDi (ID_KEY_ID with random value)
//	- CERTREQ optional
//	- HPA_INFO Notify optional
//	- NO AUTH payload (signals "use EAP")
//
// We respond with:
//
//	- IDr = N3IWF identifier (FQDN/IP)
//	- CERT optional (only if CERTREQ was set; we don't ship a cert
//	  yet, so we ignore CERTREQ and skip CERT)
//	- EAP-Request/5G-Start
//
// All wrapped in SK using SuiteR.

func (h *Handler) handleIKEAuth(hdr *ikev2.Header, msg []byte, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")
	u := h.mgr.LookupBySPIi(hdr.SPIi)
	if u == nil {
		log.Warnf("IKE_AUTH from %s: no UE context for SPIi=%x", src, hdr.SPIi)
		return nil, errors.New("no UE for SPIi")
	}
	if u.SuiteI == nil || u.SuiteR == nil {
		return nil, errors.New("IKE_AUTH before IKE_SA_INIT completed (no derived keys)")
	}

	// Decrypt the SK-wrapped request — RFC 7296 §3.14.
	_, _, inner, err := u.SuiteI.DecryptMessage(msg)
	if err != nil {
		log.Warnf("IKE_AUTH from %s: SK decrypt failed: %v", src, err)
		return nil, err
	}

	// EAP-5G NAS exchange (IKE_AUTH#2+) — TS 24.502 §7.3.3:
	// after the initial IKE_AUTH/IDr+EAP-Request/5G-Start, every
	// subsequent IKE_AUTH carries an EAP-Response/5G-NAS whose
	// inner NAS PDU has to be relayed to the AMF over N2.
	// Branch off the IDi/AUTH path and forward through the bridge.
	if u.State == ctx.StateEAP5G {
		return h.handleEAP5GResponse(u, hdr, inner, src)
	}

	// Post-EAP-Success final IKE_AUTH per RFC 7296 §2.16: the UE has
	// computed AUTH from the EAP-derived shared secret (Knh) and is
	// asking us to do the same + open the signalling IPsec SA inline
	// per TS 24.502 §7.3.2.2.
	if u.State == ctx.StateEAPSuccess {
		return h.handleIKEAuthFinal(u, hdr, inner, src)
	}

	// Per TS 24.502 §7.3.2.1 the initial IKE_AUTH request from the
	// UE shall:
	//   - include IDi with ID_KEY_ID
	//   - omit AUTH  (signals "use EAP")
	//   - optionally include CERTREQ
	//   - optionally include the EPS-handover Notify (out of scope here)
	idiP := ikev2.Find(inner, ikev2.PayloadIDi)
	if idiP == nil {
		log.Warnf("IKE_AUTH from %s: missing IDi (TS 24.502 §7.3.2.1)", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
	}
	if ikev2.Find(inner, ikev2.PayloadAUTH) != nil {
		log.Warnf("IKE_AUTH from %s: AUTH payload present — UE skipped EAP path (TS 24.502 §7.3.2.1)",
			src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
	}
	idi, err := ikev2.ParseID(idiP.Data)
	if err != nil {
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	// Cache the §3.5 ID payload body verbatim — RestOfInitIDPayload
	// = IDType | RESERVED | IDData = idiP.Data, fed to MACedIDForI in
	// §2.15 when verifying the UE's AUTH on the IKE_AUTH-final.
	u.IDiBody = append([]byte(nil), idiP.Data...)
	log.Infof("IKE_AUTH from %s: IDi type=%d (len=%d) — starting EAP-5G session per TS 24.502 §7.3.3",
		src, idi.Type, len(idi.Data))

	// Build the response per TS 24.502 §7.3.2.1:
	//   IDr  = N3IWF identifier (FQDN/IP)
	//   CERT = optional (only if CERTREQ was present and we have a
	//          cert; we don't ship one yet, so skip — peer will
	//          accept the IKE_AUTH without cert because EAP-5G
	//          provides mutual auth via the EAP-AKA' MSK later)
	//   EAP  = EAP-Request/5G-Start (TS 24.502 §9.3.2.2.1)
	idr := &ikev2.ID{Type: ikev2.IDTypeFQDN, Data: []byte(h.identity)}
	u.EAPID++
	eap5gStart := buildEAP5GStart(u.EAPID)

	innerResp := []ikev2.Payload{
		{Type: ikev2.PayloadIDr, Data: idr.Marshal()},
		{Type: ikev2.PayloadEAP, Data: eap5gStart},
	}
	respHdr := ikev2.Header{
		SPIi:         u.IKEInitiator,
		SPIr:         u.IKEResponder,
		ExchangeType: ikev2.ExchangeIKEAuth,
		Flags:        ikev2.FlagResponse,
		MessageID:    hdr.MessageID,
	}
	out, err := u.SuiteR.EncryptedMessage(respHdr, nil, innerResp)
	if err != nil {
		return nil, fmt.Errorf("IKE_AUTH encrypt: %w", err)
	}
	u.State = ctx.StateEAP5G
	log.Infof("IKE_AUTH response to %s: IDr=%s + EAP-Request/5G-Start (id=%d)",
		src, h.identity, u.EAPID)
	return out, nil
}

// buildEAP5GStart constructs an EAP-Request/5G-Start packet per
// TS 24.502 §9.3.2.2.1. Imported separately to avoid an import
// cycle if eap5g ever needs to import ikev2.
func buildEAP5GStart(identifier uint8) []byte {
	// Inline because importing nf/n3iwf/eap5g here would be cyclic
	// once we add eap5g→handler glue. Using the pure 24.502 §9.3.2.2.1
	// layout: Code(1) | Id(1) | Length(2) | Type(254) |
	//          Vendor-Id(00 28 AF) | Vendor-Type(00 00 00 03) |
	//          Message-Id(1) | Spare(0).
	const codeRequest, typeExpanded = 0x01, 0xFE
	body := []byte{
		codeRequest, identifier,
		0, 0, // Length, patched below
		typeExpanded,
		0x00, 0x28, 0xAF, // Vendor-Id 10415 (3GPP)
		0x00, 0x00, 0x00, 0x03, // Vendor-Type 3 (EAP-5G)
		0x01, // Message-Id 1 (5G-Start)
		0x00, // Spare
	}
	body[2] = byte(len(body) >> 8)
	body[3] = byte(len(body))
	return body
}

// handleEAP5GResponse processes IKE_AUTH#2+ — the EAP-Response/5G-NAS
// path of TS 24.502 §7.3.3. Layout of the inbound SK-decrypted
// payload chain: { EAP } where the EAP packet is an
// EAP-Response/5G-NAS (Code=2, Type=254, Vendor-Id=10415,
// Vendor-Type=3, Message-Id=2) per TS 24.502 §9.3.2.2.2.
//
// We:
//  1. Extract the EAP payload, decode it, peel off the inner NAS PDU.
//  2. Forward the NAS PDU to the AMF via the bridge — first time
//     through it's an InitialUEMessage (TS 38.413 §9.2.5.3); for
//     subsequent forwards (UE.AMFUeNgapID set) it's an
//     UplinkNASTransport (§9.2.5.1).
//  3. Block on UE.WaitDLNAS until the AMF lands a DownlinkNASTransport
//     for this UE (h.nasTimeout cap).
//  4. Wrap the AMF's NAS PDU as an EAP-Request/5G-NAS and ship it
//     back inside the same IKE_AUTH SK reply.
//
// On no bridge or AMF timeout we return AUTHENTICATION_FAILED so the
// UE doesn't sit forever waiting for IKE_AUTH#N+1.
func (h *Handler) handleEAP5GResponse(u *ctx.UEContext, hdr *ikev2.Header,
	inner []ikev2.Payload, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")
	if h.bridge == nil {
		log.Warnf("EAP-5G NAS from %s: no AMF bridge wired — failing IKE_AUTH", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
	}
	eapP := ikev2.Find(inner, ikev2.PayloadEAP)
	if eapP == nil {
		log.Warnf("EAP-5G NAS from %s: missing EAP payload (TS 24.502 §7.3.3)", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	parsed, err := eap5g.Parse(eapP.Data)
	if err != nil {
		log.Warnf("EAP-5G NAS from %s: EAP parse: %v", src, err)
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}
	if parsed.MessageID != eap5g.MsgIDNAS {
		log.Warnf("EAP-5G NAS from %s: unexpected Message-Id=%d (want %d)",
			src, parsed.MessageID, eap5g.MsgIDNAS)
		return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
	}
	if len(parsed.NASPDU) == 0 {
		log.Warnf("EAP-5G NAS from %s: empty NAS PDU (TS 24.502 §9.3.2.2.2)", src)
		return h.encryptedNotify(u, hdr, ikev2.NotifyInvalidSyntax)
	}

	// Allocate RAN-UE-NGAP-ID + register dispatch slot on the first
	// EAP-Response/5G-NAS forward. The bridge's OnDL callback writes
	// the AMF's NAS PDU + AMF-UE-NGAP-ID into our per-UE WaitDLNAS
	// channel.
	ueIP := net.ParseIP(u.UEAddrIP)
	uePort := uint16(u.UEAddrPort)
	if u.RANUeNgapID == nil {
		ranID := h.bridge.AllocateRANUEID()
		ranID64 := int64(ranID)
		u.RANUeNgapID = &ranID64
		u.WaitDLNAS = make(chan []byte, 1)
		uCopy := u // captured for callbacks
		onDL := func(nas []byte, amfID uint64) {
			amf64 := int64(amfID)
			uCopy.AMFUeNgapID = &amf64
			select {
			case uCopy.WaitDLNAS <- append([]byte(nil), nas...):
			default:
				logger.Get("n3iwf.ikev2").Warnf(
					"DL NAS for UE-%d dropped: WaitDLNAS already buffered", uCopy.UEID)
			}
		}
		onICS := func(amfID uint64, knh []byte, piggybackedNAS []byte) {
			// Stash Knh + AMF-UE-NGAP-ID on the UE context. The IPsec
			// child SA derivation per TS 24.502 §7.4 / TS 33.501
			// §6.5.2 is task #20 — for now we just record the data
			// and ack the AMF so the procedure completes, then the
			// piggybacked NAS (typically Registration Accept) goes
			// down to the UE on the WaitDLNAS path.
			if amfID != 0 {
				amf64 := int64(amfID)
				uCopy.AMFUeNgapID = &amf64
			}
			uCopy.Knh = append([]byte(nil), knh...)
			if err := h.bridge.SendInitialContextSetupResponse(amfID, ranID); err != nil {
				logger.Get("n3iwf.ikev2").Warnf(
					"ICS Response send failed for UE-%d: %v", uCopy.UEID, err)
			}
			if len(piggybackedNAS) > 0 {
				select {
				case uCopy.WaitDLNAS <- append([]byte(nil), piggybackedNAS...):
				default:
					logger.Get("n3iwf.ikev2").Warnf(
						"piggybacked NAS for UE-%d dropped: WaitDLNAS already buffered", uCopy.UEID)
				}
			}
		}
		h.bridge.RegisterUE(ranID, onDL, onICS)
		if err := h.bridge.SendInitialUEMessage(ranID, parsed.NASPDU, ueIP, uePort, nil); err != nil {
			log.Warnf("InitialUEMessage send: %v", err)
			return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
		}
		log.Infof("InitialUEMessage shipped to AMF for UE-%d (RAN-NGAP-ID=%d, %d-byte NAS)",
			u.UEID, ranID, len(parsed.NASPDU))
	} else {
		ranID := uint32(*u.RANUeNgapID)
		var amfID uint64
		if u.AMFUeNgapID != nil {
			amfID = uint64(*u.AMFUeNgapID)
		}
		// Reset wait slot so a stale prior reply doesn't bleed in.
		select {
		case <-u.WaitDLNAS:
		default:
		}
		if err := h.bridge.SendUplinkNAS(amfID, ranID, parsed.NASPDU, ueIP, uePort); err != nil {
			log.Warnf("UplinkNASTransport send: %v", err)
			return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
		}
		log.Debugf("UplinkNASTransport shipped to AMF for UE-%d (%d-byte NAS)", u.UEID, len(parsed.NASPDU))
	}

	// Block for the AMF's DownlinkNASTransport.
	var amfNAS []byte
	select {
	case amfNAS = <-u.WaitDLNAS:
	case <-time.After(h.nasTimeout):
		log.Warnf("EAP-5G NAS from %s: AMF DL NAS timeout (%s)", src, h.nasTimeout)
		return h.encryptedNotify(u, hdr, ikev2.NotifyAuthenticationFailed)
	}

	respHdr := ikev2.Header{
		SPIi:         u.IKEInitiator,
		SPIr:         u.IKEResponder,
		ExchangeType: ikev2.ExchangeIKEAuth,
		Flags:        ikev2.FlagResponse,
		MessageID:    hdr.MessageID,
	}

	// If Knh just landed via onICS, the EAP-5G method has yielded a
	// key — RFC 3748 §4.2 says the EAP server replies with an
	// EAP-Success packet to terminate the method. The piggybacked
	// NAS (Registration Accept per TS 24.502 §7.3.2.2) is held aside
	// for delivery on the signalling IPsec SA once IKE_AUTH closes.
	if len(u.Knh) > 0 && u.State != ctx.StateEAPSuccess {
		u.PendingDLNAS = amfNAS
		u.EAPID++
		eapSuccess := eap5g.BuildEAPSuccess(u.EAPID)
		u.State = ctx.StateEAPSuccess
		log.Infof("EAP-5G done for UE-%d (Knh stored, %d-byte NAS held for signalling SA) — sending EAP-Success",
			u.UEID, len(amfNAS))
		return u.SuiteR.EncryptedMessage(respHdr, nil,
			[]ikev2.Payload{{Type: ikev2.PayloadEAP, Data: eapSuccess}})
	}

	// Wrap the AMF NAS PDU as an EAP-Request/5G-NAS and return.
	u.EAPID++
	eapReq := eap5g.Build5GNASRequest(u.EAPID, amfNAS)
	return u.SuiteR.EncryptedMessage(respHdr, nil,
		[]ikev2.Payload{{Type: ikev2.PayloadEAP, Data: eapReq}})
}

// encryptedNotify builds an SK-wrapped error response carrying just
// a Notify payload — used for AUTHENTICATION_FAILED / INVALID_SYNTAX
// during IKE_AUTH per RFC 7296 §3.10.1.
func (h *Handler) encryptedNotify(u *ctx.UEContext, reqHdr *ikev2.Header,
	t ikev2.NotifyType) ([]byte, error) {
	n := &ikev2.Notify{Type: t}
	respHdr := ikev2.Header{
		SPIi:         u.IKEInitiator,
		SPIr:         u.IKEResponder,
		ExchangeType: ikev2.ExchangeIKEAuth,
		Flags:        ikev2.FlagResponse,
		MessageID:    reqHdr.MessageID,
	}
	return u.SuiteR.EncryptedMessage(respHdr, nil,
		[]ikev2.Payload{{Type: ikev2.PayloadNotify, Data: n.Marshal()}})
}

// ---- INFORMATIONAL (§1.4) ----
//
// Per RFC 7296 §1.4: "Informational Exchanges MUST ONLY occur after
// the initial exchanges and are cryptographically protected with the
// negotiated keys." We SK-decrypt the request, walk the inner
// payloads for Delete (§3.11) entries — IKE Delete tears down the
// whole UE context (releases child SAs from the userplane registry
// and the inner IP from the pool); ESP Delete removes specific
// child SAs from the registry and from u.ChildSAs.
//
// An empty INFORMATIONAL request is the IKEv2 keepalive (§1)
// "liveness check" pattern — we just ack with an empty SK payload.

func (h *Handler) handleInformational(hdr *ikev2.Header, msg []byte, src *net.UDPAddr) ([]byte, error) {
	log := logger.Get("n3iwf.ikev2")
	u := h.mgr.LookupBySPIi(hdr.SPIi)
	if u == nil || u.SuiteI == nil || u.SuiteR == nil {
		// Pre-IKE_AUTH INFORMATIONAL has no derived keys — drop.
		log.Warnf("INFORMATIONAL from %s SPIi=%x: no keyed UE context", src, hdr.SPIi)
		return nil, nil
	}
	_, _, inner, err := u.SuiteI.DecryptMessage(msg)
	if err != nil {
		log.Warnf("INFORMATIONAL from %s: SK decrypt: %v", src, err)
		return nil, err
	}

	deleteIKE := false
	for _, p := range inner {
		if p.Type != ikev2.PayloadDelete {
			continue
		}
		d, err := ikev2.ParseDelete(p.Data)
		if err != nil {
			log.Warnf("INFORMATIONAL from %s: Delete decode: %v", src, err)
			continue
		}
		switch d.ProtocolID {
		case ikev2.ProtocolIKE:
			deleteIKE = true
		case ikev2.ProtocolESP:
			h.deleteChildSAs(u, d.SPIs)
		}
	}

	respHdr := ikev2.Header{
		SPIi:         u.IKEInitiator,
		SPIr:         u.IKEResponder,
		ExchangeType: ikev2.ExchangeInformational,
		Flags:        ikev2.FlagResponse,
		MessageID:    hdr.MessageID,
	}
	// §1.4: an INFORMATIONAL response always echoes the request's
	// MessageID and is always SK-wrapped. EncryptedMessage requires
	// at least one inner payload — if there's nothing to say, send
	// a single empty Notify of type 0 ("no further notification").
	innerResp := []ikev2.Payload{{Type: ikev2.PayloadNotify, Data: (&ikev2.Notify{}).Marshal()}}
	out, err := u.SuiteR.EncryptedMessage(respHdr, nil, innerResp)
	if err != nil {
		return nil, fmt.Errorf("INFORMATIONAL encrypt: %w", err)
	}

	if deleteIKE {
		log.Infof("INFORMATIONAL Delete IKE for UE-%d — tearing down (TS 24.502 §7.4 / RFC 7296 §1.4)",
			u.UEID)
		h.tearDownUE(u)
	}
	return out, nil
}

// deleteChildSAs removes the named ESP SPIs (those the peer expects
// in its OUTBOUND direction, i.e. our INBOUND SPIIn per §3.11) from
// the user-plane registry and from the UE's ChildSAs slice.
func (h *Handler) deleteChildSAs(u *ctx.UEContext, spis [][]byte) {
	log := logger.Get("n3iwf.ikev2")
	for _, raw := range spis {
		if len(raw) != 4 {
			continue
		}
		spi := uint32(raw[0])<<24 | uint32(raw[1])<<16 | uint32(raw[2])<<8 | uint32(raw[3])
		// Find + remove from u.ChildSAs.
		kept := u.ChildSAs[:0]
		for _, csa := range u.ChildSAs {
			if csa.SPIIn == spi {
				if h.registry != nil {
					if b := h.registry.LookupBySPI(spi); b != nil {
						h.registry.Remove(b)
					}
				}
				log.Infof("Deleted ESP child SA SPIIn=%08x for UE-%d (RFC 7296 §1.4)",
					spi, u.UEID)
				continue
			}
			kept = append(kept, csa)
		}
		u.ChildSAs = kept
	}
}

// tearDownUE releases all per-UE state when the IKE SA goes away —
// removes every ChildSA from the userplane registry and returns the
// inner IP (if any) to the pool, then drops the UE from the
// Manager. Called from INFORMATIONAL Delete handling (peer-driven)
// or from inactivity timeouts.
func (h *Handler) tearDownUE(u *ctx.UEContext) {
	if h.registry != nil {
		for _, csa := range u.ChildSAs {
			if b := h.registry.LookupBySPI(csa.SPIIn); b != nil {
				h.registry.Remove(b)
			}
		}
	}
	if h.innerIPPool != nil && u.InnerIP != "" {
		if ip := net.ParseIP(u.InnerIP); ip != nil {
			h.innerIPPool.Release(ip)
		}
	}
	h.mgr.Remove(u.UEID)
}

// ---- helpers ----

// notifyResponse builds an IKE_SA_INIT response carrying just one
// Notify payload (used for INVALID_KE_PAYLOAD / NO_PROPOSAL_CHOSEN
// per §3.10.1 + §3.4). For these the responder SPI is zero, since
// no SA was set up yet.
func (h *Handler) notifyResponse(hdr *ikev2.Header, t ikev2.NotifyType, data []byte) []byte {
	n := &ikev2.Notify{Type: t, Data: data}
	pls := []ikev2.Payload{{Type: ikev2.PayloadNotify, Data: n.Marshal()}}
	body, first := ikev2.MarshalPayloads(pls)
	respHdr := &ikev2.Header{
		SPIi:         hdr.SPIi,
		// Per §3.1 "MUST be zero in the first message of an IKE
		// initial exchange (including repeats of that message
		// including a cookie)" — for an outright reject we leave it
		// zero; for the cookie case we'd echo back our chosen SPIr.
		NextPayload:  first,
		ExchangeType: ikev2.ExchangeIKESAInit,
		Flags:        ikev2.FlagResponse,
		MessageID:    hdr.MessageID,
		Length:       uint32(ikev2.HeaderLen + len(body)),
	}
	return append(ikev2.MarshalHeader(respHdr), body...)
}

// pickAcceptableProposal picks the first proposal whose required
// transforms match what the operator allows. For now: AES-CBC-256
// + HMAC-SHA256-128 + PRF-HMAC-SHA256 + MODP-2048 — same set as
// ikev2.IKEDefaultProposal builds. The chosen proposal is returned
// with its SPI cleared (§3.3.1 "for an initial IKE SA negotiation,
// this field MUST be zero").
func pickAcceptableProposal(sa *ikev2.SA) (ikev2.Proposal, error) {
	want := map[ikev2.TransformType]uint16{
		ikev2.TransformENCR:  ikev2.ENCR_AES_CBC,
		ikev2.TransformPRF:   ikev2.PRF_HMAC_SHA256,
		ikev2.TransformINTEG: ikev2.INTEG_HMAC_SHA256_128,
		ikev2.TransformDH:    ikev2.DH_MODP_2048,
	}
	for _, p := range sa.Proposals {
		if p.ProtocolID != ikev2.ProtocolIKE {
			continue
		}
		matched := map[ikev2.TransformType]ikev2.Transform{}
		for _, t := range p.Transforms {
			if id, need := want[t.Type]; need {
				if t.ID == id {
					matched[t.Type] = t
				}
			}
		}
		if len(matched) != len(want) {
			continue
		}
		// Verify ENCR has KeyLength=256.
		encr := matched[ikev2.TransformENCR]
		if !hasKeyLength(encr, 256) {
			continue
		}
		// Build the chosen proposal — single transform per type.
		out := ikev2.Proposal{
			Num:        p.Num,
			ProtocolID: ikev2.ProtocolIKE,
			Transforms: []ikev2.Transform{
				matched[ikev2.TransformENCR],
				matched[ikev2.TransformPRF],
				matched[ikev2.TransformINTEG],
				matched[ikev2.TransformDH],
			},
		}
		return out, nil
	}
	return ikev2.Proposal{}, errors.New("no acceptable proposal")
}

func hasKeyLength(t ikev2.Transform, bits uint16) bool {
	for _, a := range t.Attributes {
		if a.Type == ikev2.AttrKeyLength && a.IsTV && a.TVValue == bits {
			return true
		}
	}
	return false
}

func getTransformID(p ikev2.Proposal, tt ikev2.TransformType) uint16 {
	for _, t := range p.Transforms {
		if t.Type == tt {
			return t.ID
		}
	}
	return 0
}

func encrKeyLenForProposal(p ikev2.Proposal) int {
	for _, t := range p.Transforms {
		if t.Type != ikev2.TransformENCR {
			continue
		}
		for _, a := range t.Attributes {
			if a.Type == ikev2.AttrKeyLength && a.IsTV {
				return int(a.TVValue) / 8
			}
		}
	}
	return 0
}

func integKeyLen(id uint16) int {
	switch id {
	case ikev2.INTEG_HMAC_SHA256_128:
		return 32 // RFC 4868 §2.1: HMAC-SHA-256 key = 32 octets
	case ikev2.INTEG_HMAC_SHA1_96:
		return 20 // SHA-1 hash output
	}
	return 0
}

func dhBitLen(id uint16) int {
	switch id {
	case ikev2.DH_MODP_2048:
		return 2048
	case ikev2.DH_MODP_3072:
		return 3072
	}
	return 0
}

