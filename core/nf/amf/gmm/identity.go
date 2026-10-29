// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5GMM Identification procedure (TS 24.501 §5.4.3) and SUPI extraction.
//
// (Previous file-level comment cited §5.4.4 — that section is
// "Generic UE configuration update" in v19.6.2, not Identification.
// Corrected here.)
//
// Port of nf/amf/gmm/gmm_identity.py. Covers the Identity Response handler
// plus the common "extract SUPI / IMSI from 5GSMobileIdentity" path used
// by every other procedure that needs to pin down a subscriber.
//
// ── Spec-compliance gaps (§5.4.3) ────────────────────────────────────
//
// TODO(spec: TS 24.501 §5.4.3.2) — sendIdentityRequest requests only
//   "SUCI" today. The Identity type IE (§9.11.3.3) can also ask for
//   IMEI / IMEISV / 5G-S-TMSI / EUI-64 / MAC address. The AMF shall
//   request the appropriate type driven by why we need an identity
//   (e.g. post-auth IMEISV check driven by SMC IMEISV request).
//
// TODO(spec: TS 24.501 §5.4.3.4) — "Upon receipt of the IDENTITY
//   RESPONSE the network shall stop the timer T3570." Our FSM
//   StopTimers ["T3570"] on the EvIdentityResponse transition
//   handles this declaratively, but there's no explicit
//   timer-cancelled log line so operators can't see it in traces.
//
// TODO(spec: TS 24.501 §5.4.3.5 a — "Transmission failure … UE shall
//   re-initiate the registration procedure") — this is UE-side, but
//   the AMF-side network reaction is: if we don't receive an Identity
//   Response and T3570 expires N3570 times, the AMF shall abort the
//   ongoing registration. FSM handles retransmits; the "abort on 5th
//   expiry + release N1 NAS" final action is not wired.
//
// TODO(spec: TS 24.501 §5.4.3.3 — "Upon receipt of IDENTITY REQUEST …
//   if Identity type set to 'SUCI' …") — UE-side, but the AMF must
//   accept any supported identity type in IDENTITY RESPONSE. Today
//   handleIdentityResponse handles SUCI + 5G-GUTI; IMEI/IMEISV/EUI-64/
//   MAC-address responses aren't parsed into a useful form.
package gmm

import (
	"encoding/hex"
	"errors"
	"fmt"

	genngap "github.com/mmt/asn1go/protocols/ngap/generated"
	nas "github.com/mmt/nasgen/generated"
	"github.com/mmt/nasgen/pkg/runtime"
	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/infra/timers"
	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/nf/amf/gmm/fsm"
	"github.com/mmt/mmt-studio-core/nf/amf/uectx"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// ErrNoSUPI is returned when a mobile identity can't be resolved to a SUPI
// (IMSI) — for example because it's a SUCI using protection scheme A or B
// and the home-network private key isn't available for decryption.
var ErrNoSUPI = errors.New("gmm: could not resolve SUPI")

// ResolveSUPI extracts the SUPI (IMSI) from a 5GSMobileIdentity IE.
//
// Supported paths:
//   - SUCI with protection scheme 0 (null-scheme): SchemeOutput is the MSIN
//     digits in BCD — combine with the HN PLMN to form the IMSI.
//   - 5G-GUTI: resolve via the registry (previous registration still holds
//     the IMSI on the UE context).
//   - NoIdentity: return ErrNoSUPI — caller should trigger Identity Request.
//
// All three SUCI protection schemes are supported:
//   - Scheme 0: null-scheme (SchemeOutput is BCD-encoded MSIN)
//   - Scheme 1: ECIES Profile A (Curve25519 / X25519)
//   - Scheme 2: ECIES Profile B (NIST secp256r1 / P-256)
//
// For schemes 1/2 the HN private key is looked up from ue_auth_data
// via db/crud.HNKeyLookup (hn_private_key column provisioned by the
// operator in the UE auth panel).
func ResolveSUPI(id runtime.MobileIdentity5GS, current string) (string, error) {
	switch v := id.(type) {
	case *runtime.SUCI:
		plmn := formatPLMN(v.HomeNetworkId)
		switch v.ProtectionSchemeId {
		case 0:
			// Null-scheme: SchemeOutput is BCD-encoded MSIN.
			msin := runtime.DecodeTBCD(v.SchemeOutput)
			if msin == "" {
				return "", ErrNoSUPI
			}
			return plmn + msin, nil
		case 1, 2:
			// ECIES Profile A (1) or B (2): decrypt SchemeOutput.
			return decryptSUCI(v, plmn)
		default:
			return "", fmt.Errorf("gmm: unknown SUCI scheme %d", v.ProtectionSchemeId)
		}
	case *runtime.GUTI5G:
		// GUTI is resolved through the UE registry. First prefer the
		// caller-supplied current IMSI (set when the calling UE ctx
		// already has a populated IMSI — e.g. intra-AMF periodic reg).
		// When that's empty, fall back to looking up the GUTI's 5G-TMSI
		// in the UE registry (TS 24.501 §9.11.3.4 figure 9.11.3.4.3:
		// TMSI is the last 4 octets of the 5G-GUTI; TS 23.501 §5.9.4
		// assigns one 5G-TMSI per AMF per UE). A cached ctx from a
		// prior registration on this AMF carries the IMSI; copying it
		// avoids an Identity procedure round-trip and fills [IMSI:...]
		// on all subsequent logs from this registration flow.
		if current != "" {
			return current, nil
		}
		if cached := uectx.Default.LookupByTMSI(v.TMSI5G); cached != nil && cached.IMSI != "" {
			return cached.IMSI, nil
		}
		return "", ErrNoSUPI
	case *runtime.NoIdentity, nil:
		return "", ErrNoSUPI
	default:
		return "", fmt.Errorf("gmm: unsupported mobile identity %T", id)
	}
}

// decryptSUCI decrypts an ECIES-protected SUCI using the home-network
// private key stored in ue_auth_data.hn_private_key. The SchemeOutput
// layout is: ephemeral_pubkey || ciphertext || mac_tag(8).
//
// Profile A: pubkey=32 bytes (X25519)
// Profile B: pubkey=33 bytes (compressed P-256)
func decryptSUCI(suci *runtime.SUCI, plmn string) (string, error) {
	log := logger.Get("amf.gmm.identity")

	// Look up HN private key from DB.
	mcc := suci.HomeNetworkId.MCC
	mnc := suci.HomeNetworkId.MNC
	profile, keyHex, found, err := crud.HNKeyLookup(mcc, mnc, 0)
	if err != nil {
		return "", fmt.Errorf("gmm: HN key lookup: %w", err)
	}
	if !found || keyHex == "" {
		return "", fmt.Errorf("gmm: no HN private key for PLMN %s%s scheme %d",
			mcc, mnc, suci.ProtectionSchemeId)
	}
	hnPrivKey, err := hex.DecodeString(keyHex)
	if err != nil {
		return "", fmt.Errorf("gmm: bad HN private key hex: %w", err)
	}

	data := suci.SchemeOutput
	var pubKeyLen int
	switch suci.ProtectionSchemeId {
	case 1: // Profile A: X25519 pubkey = 32 bytes
		pubKeyLen = 32
	case 2: // Profile B: compressed P-256 pubkey = 33 bytes
		pubKeyLen = 33
	}
	if len(data) < pubKeyLen+8+1 {
		return "", fmt.Errorf("gmm: SUCI SchemeOutput too short (%d bytes)", len(data))
	}
	uePubKey := data[:pubKeyLen]
	ciphertext := data[pubKeyLen : len(data)-8]
	macTag := data[len(data)-8:]

	var msinBytes []byte
	switch suci.ProtectionSchemeId {
	case 1:
		ecies, err := sacrypto.NewProfileA(hnPrivKey)
		if err != nil {
			return "", err
		}
		msinBytes, err = ecies.Unprotect(uePubKey, ciphertext, macTag)
		if err != nil {
			return "", fmt.Errorf("gmm: ECIES Profile A decrypt: %w", err)
		}
	case 2:
		ecies, err := sacrypto.NewProfileB(hnPrivKey)
		if err != nil {
			return "", err
		}
		msinBytes, err = ecies.Unprotect(uePubKey, ciphertext, macTag)
		if err != nil {
			return "", fmt.Errorf("gmm: ECIES Profile B decrypt: %w", err)
		}
	}

	// msinBytes is the decrypted BCD-encoded MSIN.
	msin := runtime.DecodeTBCD(msinBytes)
	if msin == "" {
		return "", ErrNoSUPI
	}
	log.Infof("SUCI decrypted (profile %s) → MSIN=%s", profile, msin)
	return plmn + msin, nil
}

// BCD digit unpacking lives in the codec runtime as
// runtime.DecodeTBCD (TS 31.102 + TS 24.008 §10.5.1.4 — low nibble
// first, 0xF filler ends the sequence). Same primitive PFCP
// runtime exposes; both NAS and PFCP carry IMSI/MSIN/IMEI as TBCD.

// formatPLMN renders a PlmnId as its MCC+MNC BCD-decoded digits: "00101".
// MNC width is preserved (2 or 3 digits) — the IMSI length carries the
// operator-determined MNC length per TS 23.003 §2.2. Padding a 2-digit MNC
// to 3 would produce a 16-digit IMSI that wouldn't match the DB record.
func formatPLMN(p runtime.PlmnId) string {
	return p.MCC + p.MNC
}

// ── 5GMM Identity procedure ─────────────────────────────────────────────

func init() {
	Register(MsgIdentityResponse, handleIdentityResponse)
}

// handleIdentityResponse processes IDENTITY RESPONSE (TS 24.501 §8.2.19 +
// §5.4.3.4 "Identification completion by the network").
//
// Per §5.4.3.4: "Upon receipt of the IDENTITY RESPONSE the network shall
// stop the timer T3570." The FSM StopTimers ["T3570"] on the
// EvIdentityResponse transition handles the cancel declaratively.
//
// Outcome paths:
//   - Identity resolves to a SUPI → resume the triggering procedure
//     (registration: call startAuthentication with the resolved SUPI).
//   - ResolveSUPI returns ErrNoSUPI (UE answered "No identity" per
//     §5.4.3.5 b) → reject the registration with cause #9
//     "UE identity cannot be derived by the network" and release N1 NAS.
//   - Decode / unexpected type → REGISTRATION REJECT cause #111.
func handleIdentityResponse(ue *uectx.AmfUeCtx, msgType uint8, inner []byte, _ []byte) {
	log := logger.Get("amf.gmm.identity")
	if !allowedIn(ue, "IDENTITY RESPONSE", fsm.StateIdentification) {
		return
	}

	// T3570 is cancelled by the FSM on leaving StateIdentification.

	msg, err := nas.DecodeNASMessage(inner)
	if err != nil {
		log.Errorf("Identity Response decode: %v", err)
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvIdentityResponseFail, Inner: inner})
		abortRegistrationAndReleaseN1(ue, CauseProtocolError,
			genngap.CauseNasUnspecified)
		return
	}
	ir, ok := msg.(*nas.IdentityResponse)
	if !ok {
		log.Errorf("Identity Response: unexpected type %T", msg)
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvIdentityResponseFail, Inner: inner})
		abortRegistrationAndReleaseN1(ue, CauseProtocolError,
			genngap.CauseNasUnspecified)
		return
	}

	supi, err := ResolveSUPI(ir.MobileIdentity5GS, ue.IMSI)
	if err != nil {
		log.WithIMSI(ue.IMSI).Warnf("Identity Response — could not resolve SUPI amfUeID=%d: %v",
			ue.AmfUeNGAPID, err)
		// §5.5.1.2.5 cause #9 "UE identity cannot be derived by the
		// network" — UE will wipe its identity state and retry.
		_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvIdentityResponseFail, Inner: inner})
		abortRegistrationAndReleaseN1(ue, CauseUEIdentityCannotBeDerived,
			genngap.CauseNasUnspecified)
		return
	}
	ue.IMSI = supi
	// Pair the IMSI write with timer-manager registration so future
	// timer retransmit / expiry log lines for this UE carry the
	// [IMSI:…] tag automatically (infra/timers/manager.go logEvent).
	timers.M.RegisterUE(fmt.Sprintf("%d", ue.AmfUeNGAPID), supi)
	// Orphan guard: if another ctx is already indexed under this SUPI
	// (late-arriving Identity Response while a parallel registration
	// has already progressed, or a stale re-registration), drop it so
	// the Insert below doesn't leave the old amfID un-reachable via
	// byAmfID.
	if prior := uectx.Default.LookupByIMSI(supi); prior != nil && prior != ue {
		uectx.Default.Remove(prior)
	}
	uectx.Default.Insert(ue) // re-index under the new IMSI

	log.WithIMSI(ue.IMSI).Infof("Identity resolved amfUeID=%d supi=%s raw=%s",
		ue.AmfUeNGAPID, supi, hex.EncodeToString(inner))

	// If the trigger for this Identity procedure was a Registration (the
	// typical case when the UE sent SUCI with NoIdentity OR a 5G-GUTI
	// whose mapping the AMF had lost across a restart), carry on to
	// Authentication now that we have the SUPI. Run NSSF selection
	// FIRST — the RR that opened this procedure went through
	// handleRegistrationRequest and bailed early at ResolveSUPI without
	// computing Allowed NSSAI. Without this call, InitialContextSetup
	// would later fail the PRESENCE mandatory check on AllowedNSSAI
	// per TS 38.413 §9.2.2.1 "INITIAL CONTEXT SETUP REQUEST" message
	// IE table.
	//
	// IDENTITY RESPONSE carries no Requested NSSAI IE — per TS 24.501
	// §8.2.22 "Identity response" message IE table the only IEs are
	// EPD / SHT / Spare half octet / Message type / 5GS mobile
	// identity. Pass nil requested; NSSF then runs the default path
	// (Subscribed ∩ AMF-serving ∩ gNB-configured) per TS 23.501
	// §5.15.5.2.1, same as an RR without RequestedNSSAI.
	if ue.GMMProc == uectx.GMMProcRegistration {
		// TS 24.501 v19.6.2 §5.5.1.2.5 (Initial registration not
		// accepted) + §5.4.1.2 (primary auth procedure) — UDM
		// existence gate, mirror of the SUCI initial-RR path in
		// registration.go. The Identity Response just resolved a
		// SUPI; if UDM has no provisioning for it (Nudm_UEAuth-
		// entication_Get returns nil per TS 29.503 §5.4.2.2), the
		// network shall reject with cause #3 "Illegal UE" — the
		// same #3 mapping auth.go's startAuthentication produces
		// when ausf.GenerateAV (→ udm.GetAuthData) comes back empty
		// (subscriber unknown / no credentials). Failing fast here
		// avoids the wasted runNSSFWithRequested call below and
		// keeps the rejection cause stable should a #62 fast-path
		// ever be added to this branch (mirror of f73f6bb0's
		// regression that hit registration.go).
		// Per TS 23.502 §4.2.2.2.2 the registration step ordering
		// places Authentication (steps 4-7) ahead of Allowed-NSSAI
		// determination (step 8); the gate is consistent with that
		// ordering even though we still run NSSF eagerly here so
		// ICS has its Mandatory AllowedNSSAI IE (TS 38.413 §9.2.2.1).
		if creds, gerr := udm.GetAuthData(supi); gerr != nil || creds == nil {
			log.WithIMSI(supi).Warnf("Identity Response: subscriber not provisioned in UDM — aborting cause #3 per TS 24.501 §5.5.1.2.5")
			pm.Inc(pm.RegFail, 1)
			abortRegistrationAndReleaseN1(ue, CauseIllegalUE,
				genngap.CauseNasAuthenticationFailure)
			return
		}
		runNSSFWithRequested(ue, nil)
		startAuthentication(ue)
	}
	// Fire EvIdentityResponse → FSM moves IDENTIFICATION → AUTHENTICATION,
	// T3570 cancelled, T3560 armed. startAuthentication has already sent
	// the Auth Request so RetxNASPDU holds the right bytes.
	_ = fsm.Of(ue).Fire(&fsm.Context{UE: ue, Event: fsm.EvIdentityResponse, Inner: inner})
}
