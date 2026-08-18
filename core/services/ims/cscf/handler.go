// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// REGISTER reception handler — wires libs/sip parsing to the
// per-IMPI Registration FSM and serialises the FSM's RegResult into
// a SIP response.
//
// Spec anchors:
//   * TS 24.229 §5.4.1 "Registration and authentication" —
//     specs/3gpp/ts_124229v190600p.pdf
//   * TS 33.203 §6.1 "Authentication and key agreement (AKA)" —
//     specs/3gpp/ts_133203v190100p.pdf
//   * RFC 3261 §8.2.6 "Generating the Response" —
//     specs/ietf/rfc3261.txt (used to echo Via/From/To/Call-ID/CSeq)
//   * RFC 3310 — HTTP Digest Authentication Using AKA (AKAv1-MD5),
//     specs/ietf/rfc3310.txt (WWW-Authenticate encoding)
//   * RFC 4169 — HTTP Digest Authentication Using AKAv2,
//     specs/ietf/rfc4169.txt (SHA-256 variant of the above)
//
// AKA primitives (§5.4.1.2.2 RES/XRES comparison, AV generation
// per TS 33.203 §6.1) are NOT done in this package: the
// RegisterHandlerConfig supplies them as callbacks, so the handler
// itself is protocol-neutral and can be tested with fakes.
package cscf

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/mmt/mmt-studio-core/libs/sip"
)

// RegisterHandlerConfig wires the AKA primitives that the
// Registration FSM needs but that don't belong in a SIP-layer file.
// Real implementations contact the HSS (§5.4.1.2 + TS 29.228) and
// perform AKAv1/AKAv2 per TS 33.203 §6.1
// (specs/3gpp/ts_133203v190100p.pdf).
type RegisterHandlerConfig struct {
	// GenerateAV produces an authentication vector for the given
	// IMPI. Called on every unprotected / initial REGISTER that
	// lacks an integrity-protected=yes parameter. Must return a
	// non-empty map (rand / autn / xres / ck / ik etc.) or the FSM
	// returns a 500 "no_av_supplied".
	GenerateAV func(impi string) map[string][]byte

	// VerifyAuth checks the Authorization header of a protected
	// REGISTER against the AV cached in the FSM. The full request
	// is supplied (for method + Request-URI used in HA2 per
	// RFC 3310 §3.3) along with the cached AV (for XRES used in
	// HA1). Return true iff the response field in the
	// Authorization header matches the locally-computed digest.
	VerifyAuth func(req *sip.SipRequest, av map[string][]byte) bool

	// NormalizeIMPI canonicalises the SIP-extracted IMPI string so
	// the per-IMPI Registration FSM keys consistently across the
	// unprotected and protected REGISTER round-trips.
	//
	// Why: a UE may identify itself differently in the two
	// REGISTERs — the unprotected one has no Authorization header
	// so the SIP layer falls back to the From URI (an IMPU like
	// "sip:+<MSISDN>@<domain>" per TS 23.003 §13.4), while the
	// protected one carries Authorization with the proper IMPI
	// (TS 23.003 §13.3 "<IMSI>@ims.…"). Without normalization, the
	// two REGISTERs land on different FSM instances and the
	// protected REGISTER hits a fresh StateNotRegistered → 403
	// not_challenged.
	//
	// The supplied function should map both forms to the canonical
	// IMSI-derived IMPI. May consult an HSS / DB for the
	// MSISDN→IMSI translation. nil means "use the SIP-extracted
	// IMPI verbatim" (legacy behaviour, fine if every UE uses the
	// IMSI form on the unprotected REGISTER too).
	NormalizeIMPI func(impi string) string
}

// HandleRegister processes an incoming REGISTER request. It returns
// the SIP response the CSCF should send back (401 with challenge,
// 200 OK, 403 auth failure, or 500 on internal errors).
//
// The handler:
//   1. Extracts IMPI / IMPU / Contact / Expires via ParseRegister.
//   2. Discriminates unprotected vs protected vs deregister
//      following the §5.4.1.1 "Introduction" decision tree
//      (simplified — only IMS-AKA branches are wired).
//   3. Drives the per-IMPI Registration FSM.
//   4. Serialises the FSM's RegResult into a SIP response, echoing
//      Via / From / To / Call-ID / CSeq from the request per
//      RFC 3261 §8.2.6 ("Generating the Response").
func (c *CSCF) HandleRegister(req *sip.SipRequest, cfg RegisterHandlerConfig) *sip.SipResponse {
	f := ParseRegister(req)
	if f.IMPI == "" {
		return buildResponse(req, 400, "Bad Request", nil)
	}
	// Canonicalise so the unprotected REGISTER (where the SIP layer
	// falls back to the IMPU From URI) and the protected REGISTER
	// (where Authorization carries the IMSI-derived IMPI) land on
	// the same per-IMPI Registration FSM.
	if cfg.NormalizeIMPI != nil {
		if canon := cfg.NormalizeIMPI(f.IMPI); canon != "" {
			f.IMPI = canon
		}
	}

	reg := c.GetOrCreateRegistration(f.IMPI)

	var res RegResult
	switch {
	case f.Expires == 0:
		// §5.4.1.4: REGISTER with Expires 0 is user-initiated deregistration.
		res = reg.OnDeregister()

	case isIntegrityProtected(req):
		// §5.4.1.2.2 — protected REGISTER: verify response, on
		// success transition to Registered. VerifyAuth gets the
		// AV cached from the prior unprotected challenge so it
		// can run RFC 3310 §3.3 HA1/HA2 with the right XRES.
		authOK := false
		if cfg.VerifyAuth != nil {
			authOK = cfg.VerifyAuth(req, reg.CachedAV())
		}
		res = reg.OnProtectedRegister(f.IMPU, f.Contact, f.Expires, authOK)

	default:
		// §5.4.1.2.1 — unprotected REGISTER: issue 401 + challenge.
		var av map[string][]byte
		if cfg.GenerateAV != nil {
			av = cfg.GenerateAV(f.IMPI)
		}
		res = reg.OnUnprotectedRegister(f.IMPU, f.Contact, f.Expires, av)
	}

	extraHeaders := map[string]string{}
	if res.Code == 401 && len(res.Challenge) > 0 {
		// §5.4.1.2.1A: WWW-Authenticate carries the AKA challenge.
		// RFC 3310 §3.2 fixes the on-wire encoding:
		//   nonce = base64(RAND ‖ AUTN [‖ server-data])
		//   algorithm = AKAv1-MD5
		// encodeAKAChallenge builds the full Digest header from
		// the AV map produced by the AKA generator.
		extraHeaders[sip.HdrWWWAuthenticate] = encodeAKAChallenge(c.IMSDomain, res.Challenge)
	}
	if res.Code == 200 && f.Expires > 0 {
		extraHeaders[sip.HdrExpires] = itoaShim(f.Expires)
	}

	return buildResponse(req, res.Code, res.Reason, extraHeaders)
}

// isIntegrityProtected returns true when an inbound REGISTER should
// take the §5.4.1.2.2 protected branch (FSM transition Challenged →
// Registered after VerifyAuth).
//
// Two acceptance criteria:
//
//  1. TS 24.229 §5.4.1.1 strict form: Authorization header with
//     `integrity-protected=yes` (real IMS UEs negotiate IPsec with
//     the P-CSCF first and only then set this).
//  2. RFC 3310 §3.3 client authentication form: Authorization header
//     with non-empty `nonce=` and `response=` parameters. This is
//     what UEs without IPsec emit (lab test clients, soft clients,
//     UEs roaming on access nets where IPsec was not provisioned).
//
// Either signals "the UE has produced an AKA response we should
// verify". Real verification happens in the supplied VerifyAuth
// callback — this predicate just selects the FSM branch.
func isIntegrityProtected(req *sip.SipRequest) bool {
	auth := req.GetHeader(sip.HdrAuthorization)
	if auth == "" {
		return false
	}
	// Splice commas to spaces so splitFields gives us auth-params.
	for i := 0; i < len(auth); i++ {
		if auth[i] == ',' {
			auth = auth[:i] + " " + auth[i+1:]
		}
	}
	gotNonce, gotResponse := false, false
	for _, field := range splitFields(auth) {
		eq := indexOf(field, '=')
		if eq < 0 {
			continue
		}
		k := trimSIP(field[:eq])
		v := trimSIP(strip(field[eq+1:], '"'))
		switch k {
		case "integrity-protected":
			if v == "yes" {
				return true
			}
		case "nonce":
			if v != "" {
				gotNonce = true
			}
		case "response":
			if v != "" {
				gotResponse = true
			}
		}
	}
	return gotNonce && gotResponse
}

// buildResponse constructs a minimal SIP response echoing the
// required headers from the request per RFC 3261 §8.2.6.
func buildResponse(req *sip.SipRequest, code int, reason string, extra map[string]string) *sip.SipResponse {
	resp := &sip.SipResponse{
		SipMessage: sip.SipMessage{Headers: map[string][]string{}},
		StatusCode: code,
		Reason:     reason,
	}
	for _, h := range []string{sip.HdrVia, sip.HdrFrom, sip.HdrTo, sip.HdrCallID, sip.HdrCSeq} {
		if v := req.GetHeader(h); v != "" {
			resp.SetHeader(h, v)
		}
	}
	for k, v := range extra {
		resp.SetHeader(k, v)
	}
	resp.SetHeader(sip.HdrContentLength, "0")
	return resp
}

// encodeAKAChallenge builds an RFC 3310 §3.2 "Creating a Challenge"
// WWW-Authenticate Digest AKAv1-MD5 header from an AV map.
//
// Per §3.2, the AKA RAND and AUTN are concatenated and base64-encoded
// into the nonce parameter:
//
//     nonce = base64(RAND || AUTN [|| server-data])
//     algorithm = AKAv1-MD5    (no quotes — token, not quoted-string)
//
// realm comes from the IMS home network domain (TS 23.003 §13.2).
// opaque is a server-side handle (RFC 2617 §3.2.1) the UE echoes back
// in the Authorization header on the protected REGISTER. qop offers
// auth and auth-int per the example in RFC 3310 §4.
//
// av is expected to contain "rand" and "autn" raw byte values from the
// AKA generator (TS 33.102 §6.3.2: RAND = 128 bits, AUTN = 128 bits =
// SQN⊕AK ‖ AMF ‖ MAC). Missing keys yield empty fields — the UE will
// fail to compute RES and the next REGISTER lands in §5.4.1.2.3A.
func encodeAKAChallenge(realm string, av map[string][]byte) string {
	if realm == "" {
		realm = "ims.local"
	}
	// nonce = base64(RAND || AUTN). Server-data is optional per §3.2
	// Figure 1; we don't include any.
	nonceBytes := make([]byte, 0, len(av["rand"])+len(av["autn"]))
	nonceBytes = append(nonceBytes, av["rand"]...)
	nonceBytes = append(nonceBytes, av["autn"]...)
	nonce := base64.StdEncoding.EncodeToString(nonceBytes)

	// opaque: 16 random bytes hex-encoded, RFC 2617 §3.2.1.
	opaqueBytes := make([]byte, 16)
	_, _ = rand.Read(opaqueBytes)
	opaque := hex.EncodeToString(opaqueBytes)

	return fmt.Sprintf(
		`Digest realm="%s", nonce="%s", qop="auth,auth-int", opaque="%s", algorithm=AKAv1-MD5`,
		realm, nonce, opaque,
	)
}

// ── trivial string helpers (avoids a strings import) ──

func splitFields(s string) []string {
	var parts []string
	start := 0
	for i, c := range s {
		if c == ' ' || c == '\t' {
			if i > start {
				parts = append(parts, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		parts = append(parts, s[start:])
	}
	return parts
}

func indexOf(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}

func trimSIP(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t') {
		s = s[:len(s)-1]
	}
	return s
}

func strip(s string, c byte) string {
	if len(s) >= 2 && s[0] == c && s[len(s)-1] == c {
		return s[1 : len(s)-1]
	}
	return s
}

func itoaShim(n int) string {
	if n == 0 {
		return "0"
	}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
