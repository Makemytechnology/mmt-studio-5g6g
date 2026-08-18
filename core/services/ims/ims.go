// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// IMS subsystem entry point — boots the CSCF SIP listener so a
// REGISTER from a UE / tester reaches HandleRegister and gets a
// real 401 challenge response instead of timing out.
//
// Spec anchors:
//   * TS 24.229 §5.4.1 "Registration and authentication" —
//     specs/3gpp/ts_124229v190600p.pdf.
//   * RFC 3261 §10 "Registrations" + §18 "Transport" —
//     specs/ietf/rfc3261.txt.
//
// AKA crypto path is stubbed pending the implementation milestone:
// GenerateAV returns a placeholder challenge so the unprotected
// REGISTER path lands the UE in StateChallenged and we send back
// a 401, and VerifyAuth returns false so any protected REGISTER
// resolves to 403 auth_failed. The wire protocol is real; the
// crypto needs TS 33.203 §6.1 (specs/3gpp/ts_133203v190100p.pdf)
// + RFC 3310 (specs/ietf/rfc3310.txt) wired into the AV generator
// + WWW-Authenticate encoder.
package ims

import (
	"context"
	"crypto/rand"

	"github.com/mmt/mmt-studio-core/libs/sip"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/services/ims/conference"
	"github.com/mmt/mmt-studio-core/services/ims/cscf"
)

var imsLog = logger.Get("ims")

// Config controls the IMS subsystem boot.
type Config struct {
	// Host is the bind address for the SIP listener. Default "0.0.0.0".
	Host string

	// Port is the SIP UDP port. Default 5060 (RFC 3261 §19.1.2).
	Port int

	// IMSDomain is the home network realm used in challenge headers
	// and synthesised IMPIs (TS 23.003 §13.2). Default "ims.local".
	IMSDomain string
}

// Service is the running IMS subsystem handle.
type Service struct {
	cscfCore   *cscf.CSCF
	cscfServer *cscf.Server
}

// Start boots the IMS subsystem. On success it returns a Service
// whose Stop method tears the listener down.
func Start(cfg Config) (*Service, error) {
	if cfg.Host == "" {
		cfg.Host = "0.0.0.0"
	}
	if cfg.Port == 0 {
		cfg.Port = 5060
	}
	if cfg.IMSDomain == "" {
		cfg.IMSDomain = "ims.local"
	}

	c := cscf.New(cfg.Host, cfg.Port, cfg.IMSDomain)
	// Wire the AF→PCF→SMF chain (TS 29.514 §4.2.2 / TS 29.512
	// §4.2.3) so an INVITE's SDP m-lines drive PCC rule activation
	// and the matching N7 push to the SMF.
	c.AuthorizeMedia = AuthorizeMediaFromSDP
	c.ReleaseMedia = ReleaseMedia
	// Wire the §5.3.2 conference focus so INVITEs to the conference
	// factory URI get a §5.3.2.3.1 200 OK with an "isfocus" Contact
	// instead of the registrar's terminating-routing 480.
	c.ConferenceAS = conference.NewConferenceAS(cfg.IMSDomain)
	handlerCfg := cscf.RegisterHandlerConfig{
		// Try real Milenage-driven AV from UDM-supplied creds; fall
		// back to a random stub if the IMPI doesn't resolve to a
		// provisioned IMSI (lab / unprovisioned UE).
		GenerateAV: func(impi string) map[string][]byte {
			if av := realGenerateAV(impi); av != nil {
				return av
			}
			imsLog.Warnf("AKA: falling back to stub AV for IMPI=%s", impi)
			return stubGenerateAV(impi)
		},
		VerifyAuth: realVerifyAuth,
		NormalizeIMPI: func(impi string) string {
			return canonicalIMPI(impi, cfg.IMSDomain)
		},
	}
	srv := cscf.NewServer(c, handlerCfg)
	if err := srv.Start(cfg.Host, cfg.Port); err != nil {
		return nil, err
	}
	imsLog.Infof("IMS: CSCF listening on %s:%d/UDP (domain=%s)", cfg.Host, cfg.Port, cfg.IMSDomain)
	return &Service{cscfCore: c, cscfServer: srv}, nil
}

// Stop releases the SIP listener.
func (s *Service) Stop(_ context.Context) {
	if s == nil || s.cscfServer == nil {
		return
	}
	s.cscfServer.Stop()
}

// CSCF returns the CSCF container — exposed for read-only inspection
// (GUI list-registrations endpoint, tests).
func (s *Service) CSCF() *cscf.CSCF { return s.cscfCore }

// stubGenerateAV is a placeholder authentication-vector generator.
// Real S-CSCF AV generation runs over Cx with the HSS (TS 29.228 —
// not in-tree) and applies the AKAv1-MD5 / AKAv2 wrapper from RFC
// 3310 / RFC 4169 (specs/ietf/rfc3310.txt, rfc4169.txt) around keys
// derived per TS 33.203 §6.1 (specs/3gpp/ts_133203v190100p.pdf).
//
// Until the HSS path is wired we emit:
//   * RAND  : 16 random bytes (TS 33.102 §6.3.2 RAND length).
//   * AUTN  : 16 random bytes (SQN⊕AK ‖ AMF ‖ MAC, 6+2+8 = 16 bytes
//             per TS 33.102 §6.3.2). Random instead of structured
//             since we don't have a real K/OPc to derive AK / MAC.
//   * XRES  : 8 random bytes (TS 33.102 §6.3.2 RES length, 4..16 B).
// Sizes match real AVs so the on-wire RFC 3310 §3.2 nonce (32-byte
// base64) is the right length, and the UE's RFC 3310 §3.3 client
// authentication code parses cleanly even though it'll fail
// RES/XRES comparison until VerifyAuth has real crypto.
func stubGenerateAV(impi string) map[string][]byte {
	randBytes := make([]byte, 16)
	autnBytes := make([]byte, 16)
	xresBytes := make([]byte, 8)
	_, _ = rand.Read(randBytes)
	_, _ = rand.Read(autnBytes)
	_, _ = rand.Read(xresBytes)
	return map[string][]byte{
		"rand": randBytes,
		"autn": autnBytes,
		"xres": xresBytes,
	}
}

// stubVerifyAuth is the offline / unprovisioned-UE fallback — it
// always returns false so the protected-REGISTER branch in
// registration.onProtected (§5.4.1.2.3A) yields 403 auth_failed.
// Production wiring lives in realVerifyAuth (aka.go) which performs
// proper RFC 3310 §3.3 / TS 33.203 §6.1 digest verification against
// the cached XRES.
func stubVerifyAuth(*sip.SipRequest, map[string][]byte) bool {
	return false
}
