// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// IMS-AKA SIP authentication — Go port of sip_auth.py.
// Generates challenges, builds WWW-Authenticate, parses Authorization,
// verifies Digest-AKAv1-MD5 responses (RFC 3310 / TS 33.203).
package sip

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"strings"
)

// ImsAkaVector holds an IMS-AKA authentication vector.
type ImsAkaVector struct {
	RAND  []byte
	AUTN  []byte
	XRES  []byte
	CK    []byte
	IK    []byte
	Nonce string // base64(RAND||AUTN)
}

// BuildWWWAuthenticate builds a WWW-Authenticate header for IMS-AKA.
func BuildWWWAuthenticate(realm, nonce, algorithm string) string {
	if algorithm == "" {
		algorithm = "AKAv1-MD5"
	}
	return fmt.Sprintf(`Digest realm="%s", nonce="%s", algorithm=%s, qop="auth"`,
		realm, nonce, algorithm)
}

// ParseAuthorization parses a SIP Authorization header into key-value pairs.
func ParseAuthorization(header string) map[string]string {
	params := make(map[string]string)
	if header == "" {
		return params
	}
	lower := strings.ToLower(header)
	if strings.HasPrefix(lower, "digest ") {
		header = header[7:]
	}
	for _, part := range strings.Split(header, ",") {
		part = strings.TrimSpace(part)
		idx := strings.Index(part, "=")
		if idx < 0 {
			continue
		}
		key := strings.TrimSpace(part[:idx])
		val := strings.TrimSpace(part[idx+1:])
		val = strings.Trim(val, `"`)
		params[key] = val
	}
	return params
}

// VerifyImsAkaResponse verifies the client's AKA Digest response.
// Falls back to raw XRES comparison if authParams is nil.
func VerifyImsAkaResponse(expectedXRES []byte, receivedResponseHex string,
	authParams map[string]string, method string) bool {
	if method == "" {
		method = "REGISTER"
	}

	md5Hex := func(data []byte) string {
		h := md5.Sum(data)
		return hex.EncodeToString(h[:])
	}

	// Digest-AKAv1-MD5 verification
	if authParams != nil {
		username := authParams["username"]
		realm := authParams["realm"]
		nonce := authParams["nonce"]
		uri := authParams["uri"]
		if username != "" && realm != "" && nonce != "" && uri != "" {
			nc := authParams["nc"]
			if nc == "" {
				nc = "00000001"
			}
			cnonce := authParams["cnonce"]
			qop := authParams["qop"]
			if qop == "" {
				qop = "auth"
			}

			// HA1 with raw XRES bytes as password
			ha1Input := []byte(username + ":" + realm + ":")
			ha1Input = append(ha1Input, expectedXRES...)
			ha1 := md5Hex(ha1Input)

			ha2 := md5Hex([]byte(method + ":" + uri))

			var expected string
			if qop != "" {
				expected = md5Hex([]byte(fmt.Sprintf("%s:%s:%s:%s:%s:%s",
					ha1, nonce, nc, cnonce, qop, ha2)))
			} else {
				expected = md5Hex([]byte(fmt.Sprintf("%s:%s:%s", ha1, nonce, ha2)))
			}
			if receivedResponseHex == expected {
				return true
			}

			// Some UEs use hex(XRES) as password string
			ha1Hex := md5Hex([]byte(fmt.Sprintf("%s:%s:%s",
				username, realm, hex.EncodeToString(expectedXRES))))
			if qop != "" {
				expectedHex := md5Hex([]byte(fmt.Sprintf("%s:%s:%s:%s:%s:%s",
					ha1Hex, nonce, nc, cnonce, qop, ha2)))
				if receivedResponseHex == expectedHex {
					return true
				}
			} else {
				expectedHex := md5Hex([]byte(fmt.Sprintf("%s:%s:%s", ha1Hex, nonce, ha2)))
				if receivedResponseHex == expectedHex {
					return true
				}
			}
			return false
		}
	}

	// Fallback: raw XRES comparison
	received, err := hex.DecodeString(receivedResponseHex)
	if err != nil {
		return false
	}
	if len(received) != len(expectedXRES) {
		return false
	}
	for i := range received {
		if received[i] != expectedXRES[i] {
			return false
		}
	}
	return true
}

// EncodeNonce produces base64(RAND || AUTN) for IMS-AKA.
func EncodeNonce(randBytes, autn []byte) string {
	combined := make([]byte, len(randBytes)+len(autn))
	copy(combined, randBytes)
	copy(combined[len(randBytes):], autn)
	return base64.StdEncoding.EncodeToString(combined)
}
