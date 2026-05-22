// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// IMS-AKA authentication-vector generator — wires the libs/sacrypto
// Milenage primitives to UDM-supplied subscriber credentials so the
// CSCF emits a real RFC 3310 §3.2 challenge that the UE can verify
// against its USIM.
//
// Spec anchors:
//   * TS 33.203 §6.1 "Authentication and key agreement (AKA)" —
//     specs/3gpp/ts_133203v190100p.pdf
//   * TS 33.102 §6.3 (UMTS-AKA primitives — IMS-AKA is the same
//     algorithm set, just framed in HTTP Digest)
//   * TS 35.205 / 35.206 — Milenage f1–f5 (libs/sacrypto/milenage.go)
//   * TS 23.003 §13.3 — Private User Identity (IMPI) format,
//     specs/3gpp/ts_123003v190600p.pdf
//   * TS 23.003 §13.4 — Public User Identity (IMPU) format,
//     specs/3gpp/ts_123003v190600p.pdf
//   * RFC 3310 §3.2 — encoding the AV into Digest WWW-Authenticate,
//     specs/ietf/rfc3310.txt
//
// The AV map this returns is consumed by services/ims/cscf/handler.go
// and services/ims/cscf/registration.go: rand → encoded into nonce,
// autn → encoded into nonce, xres → cached for the protected REGISTER
// VerifyAuth comparison.
package ims

import (
	"crypto/md5"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/libs/sacrypto"
	"github.com/mmt/mmt-studio-core/libs/sip"
	"github.com/mmt/mmt-studio-core/nf/udm"
	"github.com/mmt/mmt-studio-core/nf/udr"
)

// realGenerateAV produces a Milenage-derived AKA challenge for an
// IMPI. The flow:
//
//  1. Resolve IMPI → IMSI (TS 23.003 §13.3 IMSI form, fall back to
//     §13.4 MSISDN form via the ue.msisdn column).
//  2. udm.GetAuthData(imsi) → {K, OP/OPc, AMF, SQN}.
//  3. Generate 16-byte RAND.
//  4. f1(K, RAND, SQN, AMF) = MAC.
//  5. f2345(K, RAND) = (RES, CK, IK, AK).
//  6. AUTN = (SQN ⊕ AK) ‖ AMF ‖ MAC  (TS 33.102 §6.3.2).
//  7. Bump SQN through udm.UpdateAuthSQN so the next round picks up
//     a fresh sequence.
//
// On any failure (subscriber not found, DB error) returns nil — the
// CSCF Registration FSM treats nil as "no_av_supplied" and emits a
// 500. Caller can fall back to stubGenerateAV for offline / lab use.
func realGenerateAV(impi string) map[string][]byte {
	imsi, err := imsiFromIMPI(impi)
	if err != nil {
		imsLog.Warnf("AKA: cannot resolve IMSI from IMPI %q: %v", impi, err)
		return nil
	}

	creds, err := udm.GetAuthData(imsi)
	if err != nil || creds == nil {
		imsLog.Warnf("AKA: udm.GetAuthData(%s): %v", imsi, err)
		return nil
	}

	randBuf := make([]byte, 16)
	if _, err := rand.Read(randBuf); err != nil {
		imsLog.Warnf("AKA: rand.Read: %v", err)
		return nil
	}

	sqn := sqnBytes(creds.SQN)
	m := sacrypto.NewMilenage(creds.OP)
	if creds.OpType == "OPC" {
		m.SetOPc(creds.OP)
	}

	mac, err := m.F1(creds.K, randBuf, sqn, creds.AMF)
	if err != nil {
		imsLog.Warnf("AKA: f1: %v", err)
		return nil
	}
	res, ck, ik, ak, err := m.F2345(creds.K, randBuf)
	if err != nil {
		imsLog.Warnf("AKA: f2345: %v", err)
		return nil
	}

	// AUTN = (SQN ⊕ AK) ‖ AMF ‖ MAC  (TS 33.102 §6.3.2 Figure 8).
	autn := make([]byte, 0, 16)
	for i := 0; i < 6; i++ {
		autn = append(autn, sqn[i]^ak[i])
	}
	autn = append(autn, creds.AMF...)
	autn = append(autn, mac...)

	// Bump SQN write-behind so the next REGISTER gets a fresh
	// sequence (TS 33.102 §6.3.7 "Replay protection by SQN").
	if err := udm.UpdateAuthSQN(imsi, udr.IncrementSQN(creds.SQN)); err != nil {
		imsLog.Warnf("AKA: update_auth_sqn(%s): %v", imsi, err)
	}

	imsLog.Infof("AKA: AV generated for IMSI=%s (SQN=%d)", imsi, creds.SQN)
	return map[string][]byte{
		"rand": randBuf,
		"autn": autn,
		"xres": res,
		"ck":   ck,
		"ik":   ik,
	}
}

// imsiFromIMPI maps an IMPI like
//   001011234560001@ims.mnc001.mcc001.3gppnetwork.org   (TS 23.003 §13.3)
//   +1234560001@ims.mnc001.mcc001.3gppnetwork.org       (§13.4 IMPU form)
// to the bare IMSI digit string the UDM auth path expects.
//
// Per §13.3, an IMSI-derived IMPI has the IMSI as the user part
// directly. Per §13.4, an MSISDN-derived IMPU has "+E.164" as the
// user part — those need a DB lookup to resolve back to IMSI. We
// look the MSISDN up in ue.msisdn (db/schemas/core.go).
func imsiFromIMPI(impi string) (string, error) {
	at := strings.Index(impi, "@")
	user := impi
	if at >= 0 {
		user = impi[:at]
	}
	if user == "" {
		return "", errors.New("empty user part")
	}

	// IMSI form: all digits, 14 or 15 chars (TS 23.003 §2.2).
	if isAllDigits(user) && (len(user) == 14 || len(user) == 15) {
		return user, nil
	}

	// MSISDN / tel-URI form: optional "+" then digits.
	candidate := strings.TrimPrefix(user, "+")
	if !isAllDigits(candidate) || candidate == "" {
		return "", fmt.Errorf("user part %q not IMSI nor MSISDN", user)
	}

	imsi, err := imsiByMSISDN(candidate)
	if err != nil {
		// Try the literal form too — some HSSes provision the MSISDN
		// with the leading "+" preserved.
		if imsi2, err2 := imsiByMSISDN("+" + candidate); err2 == nil {
			return imsi2, nil
		}
		return "", fmt.Errorf("IMSI lookup by MSISDN %s: %w", candidate, err)
	}
	return imsi, nil
}

// canonicalIMPI returns the IMSI-derived IMPI form (TS 23.003 §13.3):
// "<IMSI>@<domain>". Resolves the IMPI's user part to an IMSI via
// imsiFromIMPI (handling both §13.3 IMSI and §13.4 +MSISDN forms),
// then rebuilds the full IMPI string with the supplied home network
// domain. Returns the input unchanged if the lookup fails — the
// caller may still emit 401, just with the per-IMPI FSM keyed under
// the original (non-canonical) string.
func canonicalIMPI(impi, domain string) string {
	imsi, err := imsiFromIMPI(impi)
	if err != nil || imsi == "" {
		return impi
	}
	if at := strings.Index(impi, "@"); at >= 0 && impi[at+1:] != "" {
		// Preserve whatever realm the UE asserted — TS 24.229
		// §5.4.1.1 lets the home domain vary per deployment.
		return imsi + impi[at:]
	}
	if domain == "" {
		domain = "ims.local"
	}
	return imsi + "@" + domain
}

// imsiByMSISDN queries the ue table for an IMSI matching the MSISDN.
func imsiByMSISDN(msisdn string) (string, error) {
	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	var imsi string
	err = db.QueryRow(`SELECT imsi FROM ue WHERE msisdn = ? LIMIT 1`, msisdn).Scan(&imsi)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("no subscriber with MSISDN %s", msisdn)
		}
		return "", err
	}
	return imsi, nil
}

// sqnBytes packs a 48-bit big-endian integer SQN into 6 bytes.
// (Same packing as nf/ausf — kept local to avoid pulling that whole
// package in for one helper.)
func sqnBytes(v int64) []byte {
	b := make([]byte, 6)
	u := uint64(v)
	for i := 5; i >= 0; i-- {
		b[i] = byte(u)
		u >>= 8
	}
	return b
}

// realVerifyAuth verifies a protected REGISTER's Authorization header
// per RFC 3310 §3.3 (Client Authentication for Digest AKAv1-MD5):
//
//   HA1      = MD5(username ":" realm ":" RES)        — RES = av["xres"]
//   HA2      = MD5(method ":" digest-uri)             — auth flavour
//                or MD5(method ":" digest-uri ":" H(body)) for auth-int
//   response = MD5(HA1 ":" nonce ":" nc ":" cnonce ":" qop ":" HA2)
//                if qop is present, else
//   response = MD5(HA1 ":" nonce ":" HA2)
//
// All ":" separators are literal colons (RFC 2617 §3.2.2.2 grammar).
//
// Returns true iff the Authorization header's `response` field matches
// the locally-computed digest. False on any parse error, missing AV,
// or RES mismatch.
func realVerifyAuth(req *sip.SipRequest, av map[string][]byte) bool {
	if req == nil || av == nil {
		return false
	}
	xres, ok := av["xres"]
	if !ok || len(xres) == 0 {
		imsLog.Warnf("AKA verify: no XRES cached")
		return false
	}

	auth := req.GetHeader(sip.HdrAuthorization)
	if auth == "" {
		return false
	}
	params := parseDigestParams(auth)
	want := params["response"]
	if want == "" {
		return false
	}

	username := params["username"]
	realm := params["realm"]
	uri := params["uri"]
	nonce := params["nonce"]
	nc := params["nc"]
	cnonce := params["cnonce"]
	qop := params["qop"]
	method := req.Method

	// Per RFC 3310 §3.3 the "passwd" position in HA1 is the RES
	// (binary), expressed as the lowercase hex of the RES bytes — see
	// RFC 3310 §3.4 worked example which uses the hex-encoded RES.
	// Some clients use the raw bytes directly; both forms are accepted
	// here (compute response under each and pick the one that matches
	// the client).
	resHex := hex.EncodeToString(xres)
	resRaw := string(xres)

	for _, password := range []string{resHex, resRaw} {
		ha1 := md5sum(username + ":" + realm + ":" + password)
		var ha2 string
		// auth-int requires hashing the body; for REGISTER the body
		// is empty so MD5("") is a known constant. We support both.
		body := req.Body
		bodyHash := md5sum(body)
		switch qop {
		case "auth-int":
			ha2 = md5sum(method + ":" + uri + ":" + bodyHash)
		default: // "auth", or absent
			ha2 = md5sum(method + ":" + uri)
		}

		var calc string
		if qop != "" {
			calc = md5sum(ha1 + ":" + nonce + ":" + nc + ":" + cnonce + ":" + qop + ":" + ha2)
		} else {
			calc = md5sum(ha1 + ":" + nonce + ":" + ha2)
		}
		if strings.EqualFold(calc, want) {
			return true
		}
	}
	imsLog.Warnf("AKA verify: digest mismatch for username=%q (got=%s)", username, want)
	return false
}

// md5sum returns the lowercase hex MD5 of s.
func md5sum(s string) string {
	h := md5.Sum([]byte(s))
	return hex.EncodeToString(h[:])
}

// parseDigestParams parses a Digest auth-params string ("Digest k=v,
// k="v", ...") into a map. Quoted-strings are unwrapped, tokens are
// kept verbatim.
func parseDigestParams(header string) map[string]string {
	out := make(map[string]string)
	// Strip the "Digest" scheme prefix if present.
	h := strings.TrimSpace(header)
	if strings.HasPrefix(strings.ToLower(h), "digest ") {
		h = h[len("Digest "):]
	}
	// Walk comma-separated params, respecting double-quoted values.
	var fields []string
	depth := 0
	start := 0
	for i := 0; i < len(h); i++ {
		c := h[i]
		if c == '"' {
			depth ^= 1
		} else if c == ',' && depth == 0 {
			fields = append(fields, h[start:i])
			start = i + 1
		}
	}
	fields = append(fields, h[start:])

	for _, f := range fields {
		f = strings.TrimSpace(f)
		eq := strings.Index(f, "=")
		if eq < 0 {
			continue
		}
		k := strings.ToLower(strings.TrimSpace(f[:eq]))
		v := strings.TrimSpace(f[eq+1:])
		if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
			v = v[1 : len(v)-1]
		}
		out[k] = v
	}
	return out
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}
