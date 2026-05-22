// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ims — IMS subscriber + HSS + basic registration state.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.003 §13.3   IMS domain naming convention — the
//                         ims.mncMNC.mccMCC.3gppnetwork.org form
//                         that DefaultDomain() emits.
//   - TS 23.228 §4.3.3  Identification of users — IMPI / IMPU
//                         schemes that the ims_subscribers row
//                         carries.
//   - TS 23.228 §4.7    Multimedia Resource Function — the MRFP
//                         sub-package implements the media-plane
//                         half of this entity.
//   - TS 23.228 §5.2    Application level registration procedures
//                         (the registration information flow whose
//                         Stored Information / Service Profile is
//                         what this package persists).
//   - TS 24.229 §5.4    S-CSCF procedures (registration in §5.4.1
//                         is the primary consumer of the subscriber
//                         data this package exposes).
//
// Stage-3 SIP signalling lives in services/ims/cscf/. This file is
// the database-backed identity store consumed by the GUI panel:
//   /api/ims/subscribers   — list all IMS subscribers + linked UE
//   /api/ims/registrations — in-memory registered set (from REGISTER flow)
//
// TODO(spec: TS 23.228 §5.2): the full subscription profile
// (Initial Filter Criteria / Service Profile rules per the
// §5.2 registration flow + TS 29.228 reference) is reduced here
// to a single service_profile_id pointer. The S-CSCF iFC engine
// that fans requests out to ASs based on §5.2 trigger-point rules
// is not built; iFC matching is a no-op today.
package ims

import (
	"database/sql"
	"errors"
	"sync"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Subscriber is the ims_subscribers row shape.
type Subscriber struct {
	ID               int64  `json:"id"`
	UEIMSI           string `json:"imsi"`
	MSISDN           string `json:"msisdn,omitempty"`
	IMPI             string `json:"impi"`
	IMPU             string `json:"impu"`
	ServiceProfileID *int64 `json:"service_profile_id,omitempty"`
}

// ListSubscribers returns every IMS subscriber joined with their UE.
func ListSubscribers() ([]Subscriber, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
        SELECT s.id, u.imsi, COALESCE(u.msisdn,''), s.impi, s.impu, s.service_profile_id
        FROM ims_subscribers s
        JOIN ue u ON u.id = s.ue_id
        ORDER BY u.imsi`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Subscriber
	for rows.Next() {
		var s Subscriber
		var sp sql.NullInt64
		if err := rows.Scan(&s.ID, &s.UEIMSI, &s.MSISDN, &s.IMPI, &s.IMPU, &sp); err != nil {
			return nil, err
		}
		if sp.Valid {
			s.ServiceProfileID = &sp.Int64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetOrCreateIMSSubscriber mirrors the Python hss_subscriber helper used
// by auth_upsert. It idempotently creates an IMS subscriber for an IMSI
// with the default profile + canonical IMPI/IMPU format.
func GetOrCreateIMSSubscriber(imsi, domain string) error {
	if domain == "" {
		domain = DefaultDomain(imsi)
	}
	impi := imsi + "@" + domain
	impu := "sip:" + imsi + "@" + domain

	db, err := engine.Open()
	if err != nil {
		return err
	}
	// Already exists?
	var exists int
	err = db.QueryRow(`SELECT 1 FROM ims_subscribers WHERE impi=?`, impi).Scan(&exists)
	if err == nil {
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var ueID int64
	err = db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil // UE not provisioned yet
	}
	if err != nil {
		return err
	}
	// Ensure default service profile exists.
	if _, err := db.Exec(`INSERT OR IGNORE INTO ims_service_profiles
        (name, filter_criteria_json) VALUES ('default_profile', '[]')`); err != nil {
		return err
	}
	var spID int64
	_ = db.QueryRow(`SELECT id FROM ims_service_profiles WHERE name='default_profile'`).Scan(&spID)
	if _, err := db.Exec(`INSERT OR IGNORE INTO ims_subscribers
        (ue_id, impi, impu, service_profile_id) VALUES (?,?,?,?)`,
		ueID, impi, impu, spID); err != nil {
		return err
	}
	logger.Get("ims.hss").Infof("Created IMS subscriber impi=%s impu=%s", impi, impu)
	return nil
}

// DefaultDomain constructs the TS 23.003 §13.3 IMS domain from the IMSI.
func DefaultDomain(imsi string) string {
	if len(imsi) >= 5 {
		mcc := imsi[:3]
		mnc := imsi[3:5]
		if len(mnc) == 2 {
			mnc = "0" + mnc
		}
		return "ims.mnc" + mnc + ".mcc" + mcc + ".3gppnetwork.org"
	}
	return "ims.mnc001.mcc001.3gppnetwork.org"
}

// ── In-memory IMS Registration state ────────────────────────────────────

// Registration tracks a live IMS registration (from SIP REGISTER).
type Registration struct {
	IMPI       string `json:"impi"`
	IMPU       string `json:"impu"`
	Contact    string `json:"contact"`    // Contact URI from REGISTER
	Expires    int    `json:"expires"`    // seconds
	Registered bool   `json:"registered"`
}

var (
	regMu    sync.RWMutex
	regMap   = make(map[string]*Registration)
)

// SetRegistered marks an IMS subscriber as registered (from the CSCF).
func SetRegistered(impi, contact string, expires int) {
	regMu.Lock()
	defer regMu.Unlock()
	regMap[impi] = &Registration{IMPI: impi, Contact: contact, Expires: expires, Registered: true}
}

// ClearRegistered removes registration state (SIP REGISTER with Expires=0).
func ClearRegistered(impi string) {
	regMu.Lock()
	defer regMu.Unlock()
	delete(regMap, impi)
}

// ListRegistrations returns all active registrations.
func ListRegistrations() []Registration {
	regMu.RLock()
	defer regMu.RUnlock()
	out := make([]Registration, 0, len(regMap))
	for _, r := range regMap {
		out = append(out, *r)
	}
	return out
}

// ── Cx Interface (TS 29.228 / TS 29.229) ──────────────────────────────

const (
	DiameterSuccess                     = 2001
	DiameterFirstRegistration           = 2001
	DiameterSubsequentRegistration      = 2002
	DiameterErrorUserUnknown            = 5001
	DiameterErrorIdentityNotRegistered  = 5003
)

// UserAuthorizationRequest — UAR/UAA: check if user is authorized, return S-CSCF.
func UserAuthorizationRequest(impi, impu string) (int, string) {
	sub := getIMSSubscriber(impi)
	if sub == nil {
		// Auto-provision
		parts := splitAt(impi)
		if len(parts) == 2 {
			GetOrCreateIMSSubscriber(parts[0], parts[1])
			sub = getIMSSubscriber(impi)
		}
	}
	if sub == nil { return DiameterErrorUserUnknown, "" }

	// Check existing registration
	regMu.RLock()
	for _, r := range regMap {
		if r.IMPI == impi && r.Registered {
			regMu.RUnlock()
			return DiameterSubsequentRegistration, ""
		}
	}
	regMu.RUnlock()

	scscf := "sip:scscf.ims.mnc001.mcc001.3gppnetwork.org:5064"
	return DiameterFirstRegistration, scscf
}

// ServerAssignmentRequest — SAR/SAA: record S-CSCF assignment.
func ServerAssignmentRequest(impi, impu, serverName, assignmentType string) int {
	sub := getIMSSubscriber(impi)
	if sub == nil {
		parts := splitAt(impi)
		if len(parts) == 2 {
			GetOrCreateIMSSubscriber(parts[0], parts[1])
			sub = getIMSSubscriber(impi)
		}
	}
	if sub == nil { return DiameterErrorUserUnknown }

	if assignmentType == "REGISTRATION" {
		SetRegistered(impi, "<"+impu+">", 3600)
	} else if assignmentType == "DEREGISTRATION" {
		ClearRegistered(impi)
	}
	return DiameterSuccess
}

// LocationInfoRequest — LIR/LIA: find S-CSCF for a given IMPU.
func LocationInfoRequest(impu string) (int, string) {
	regMu.RLock()
	defer regMu.RUnlock()
	for _, r := range regMap {
		if r.IMPU == impu && r.Registered {
			return DiameterSuccess, ""
		}
	}
	return DiameterErrorIdentityNotRegistered, ""
}

func getIMSSubscriber(impi string) *Subscriber {
	db, err := engine.Open()
	if err != nil { return nil }
	row := db.QueryRow(`SELECT s.id, u.imsi, COALESCE(u.msisdn,''), s.impi, s.impu, s.service_profile_id
		FROM ims_subscribers s JOIN ue u ON u.id = s.ue_id WHERE s.impi=?`, impi)
	var s Subscriber
	var sp sql.NullInt64
	if row.Scan(&s.ID, &s.UEIMSI, &s.MSISDN, &s.IMPI, &s.IMPU, &sp) != nil { return nil }
	if sp.Valid { s.ServiceProfileID = &sp.Int64 }
	return &s
}

func splitAt(s string) []string {
	for i, c := range s {
		if c == '@' { return []string{s[:i], s[i+1:]} }
	}
	return []string{s}
}

// ── TAS / MMTel (TS 24.173 / TS 24.604) ──────────────────────────────

// EvaluateIFCs evaluates Initial Filter Criteria for a subscriber.
// Returns list of AS URIs to route through.
func EvaluateIFCs(impu, sipMethod string) []string {
	if sipMethod == "INVITE" {
		return []string{"sip:mmtel@tas.local"}
	}
	return nil
}
