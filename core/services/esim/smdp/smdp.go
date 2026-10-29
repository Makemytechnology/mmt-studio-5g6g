// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package smdp — SM-DP+ Profile Manager (GSMA SGP.22 §3 ES9+).
//
// Spec anchors:
//
//   - GSMA SGP.22 §3.1.2   ES9+ Initiate Authentication.
//   - GSMA SGP.22 §3.1.3   ES9+ Authenticate Client.
//   - GSMA SGP.22 §3.3.x   ES9+ Get Bound Profile Package.
//   - GSMA SGP.22 §3.5     ES9+ Handle Notification.
//
// (GSMA SGP.* identifiers are not §-checked by speccheck — the
// regex matches TS / RFC only.)
//
// Spec anchors that ARE §-checked by speccheck:
//
//   - TS 31.102 §6.1       AKA procedure — the K / OPc fields
//                          flow into this on the card side.
//   - TS 33.501 §6.1.3     5G AKA — same key material consumed by
//                          the AMF / AUSF after profile install.
//
// TODO GSMA SGP.22 §5.7  — full ASN.1 BPP wire codec (today: JSON
//                          envelope with hex IV / ciphertext / MAC).
// TODO GSMA SGP.22 §3.4  — Cancel Session / error path is not
//                          modelled yet.
// TODO GSMA SGP.22 §6    — Common Mutual Authentication based on
//                          ECKA(ECDSA) is replaced here by an
//                          opaque challenge/response — the actual
//                          eUICC PKI signature path needs a real
//                          ECKA codec.
package smdp

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/services/esim/profile"
)

var log = logger.Get("esim.smdp")

// SMDPServer is the SM-DP+ profile manager.
type SMDPServer struct {
	SMDPAddress string
	mu          sync.Mutex
	sessions    map[string]*txnSession // transactionID -> session
}

type txnSession struct {
	State          string
	EUICCChallenge string
	ServerChallenge string
	EID            string
	CreatedAt      float64
}

// Global singleton.
var Server = &SMDPServer{SMDPAddress: "smdp.sacore.local", sessions: make(map[string]*txnSession)}

// PrepareProfile creates a downloadable eSIM profile from the
// subscriber DB. Mirrors the operator-side ES2+ "DownloadOrder +
// ConfirmOrder" sequence (GSMA SGP.22 §3.0/§5.6) collapsed into
// one local call. The profile body uses TS 31.102 §4.2 USIM EFs.
func (s *SMDPServer) PrepareProfile(imsi, profileName string) map[string]interface{} {
	if profileName == "" { profileName = "SA Core" }

	// Look up auth data
	row := engine.QueryRow(`SELECT op_type, op, k FROM ue_auth_data WHERE ue_id=(SELECT id FROM ue WHERE imsi=?)`, imsi)
	var opType, opHex, kHex string
	if err := row.Scan(&opType, &opHex, &kHex); err != nil {
		log.Warnf("Cannot prepare profile: no auth for IMSI %s", imsi)
		return nil
	}

	// Allocate ICCID
	iccid := profile.AllocateICCID("")
	matchingID := profile.GenerateMatchingID()
	ac := profile.GenerateActivationCode(s.SMDPAddress, matchingID)

	// Build + encrypt profile
	p := profile.BuildUSIMProfile(imsi, kHex, opHex, iccid, "001", "01", opType)
	pBytes := profile.SerializeProfile(p)
	keys := profile.GenerateSessionKeys()
	encrypted := profile.EncryptProfile(pBytes, keys)

	blob, _ := json.Marshal(map[string]interface{}{
		"encrypted": encrypted,
		"session_keys": map[string]string{
			"enc_key": hex.EncodeToString(keys.EncKey),
			"mac_key": hex.EncodeToString(keys.MacKey),
			"dek":     hex.EncodeToString(keys.DEK),
		},
	})

	now := float64(time.Now().Unix())
	engine.Exec(`INSERT INTO esim_profiles (iccid, imsi, activation_code, matching_id, smdp_address,
		profile_name, profile_type, profile_class, profile_blob, created_at)
		VALUES (?,?,?,?,?,?,'operational','operational',?,?)`,
		iccid, imsi, ac, matchingID, s.SMDPAddress, profileName, string(blob), now)

	log.Infof("Profile prepared: ICCID=%s IMSI=%s", iccid, imsi)
	return map[string]interface{}{
		"iccid": iccid, "imsi": imsi, "activation_code": ac,
		"matching_id": matchingID, "profile_name": profileName,
		"smdp_address": s.SMDPAddress,
		"qr_data": map[string]string{"content": ac, "format": "SGP.22-v2.3.1"},
	}
}

// InitiateAuthentication handles ES9+ Initiate Authentication
// (GSMA SGP.22 §3.1.2). The serverChallenge is a 16-byte random
// nonce that the eUICC signs in the AuthenticateClient step.
func (s *SMDPServer) InitiateAuthentication(txnID, euiccChallenge string) map[string]interface{} {
	ch := make([]byte, 16); rand.Read(ch)
	serverChallenge := hex.EncodeToString(ch)
	s.mu.Lock()
	s.sessions[txnID] = &txnSession{State: "initiated", EUICCChallenge: euiccChallenge,
		ServerChallenge: serverChallenge, CreatedAt: float64(time.Now().Unix())}
	s.mu.Unlock()
	return map[string]interface{}{
		"transactionId": txnID, "serverChallenge": serverChallenge,
		"serverSigned1": map[string]string{"transactionId": txnID, "euiccChallenge": euiccChallenge, "serverAddress": s.SMDPAddress},
	}
}

// AuthenticateClient handles ES9+ Authenticate Client
// (GSMA SGP.22 §3.1.3). The eUICC's signed payload is opaque
// here — see package-level TODO GSMA SGP.22 §6 for the real
// ECKA(ECDSA) signature verification.
func (s *SMDPServer) AuthenticateClient(txnID string, euiccSigned1 map[string]interface{}) map[string]interface{} {
	s.mu.Lock()
	sess := s.sessions[txnID]
	if sess == nil || sess.State != "initiated" { s.mu.Unlock(); return map[string]interface{}{"error": "invalid transaction"} }
	sess.State = "authenticated"
	if euiccSigned1 != nil { if eid, ok := euiccSigned1["eid"].(string); ok { sess.EID = eid } }
	s.mu.Unlock()
	if sess.EID != "" {
		now := float64(time.Now().Unix())
		engine.Exec(`INSERT INTO esim_euicc (eid, last_contact, registered_at) VALUES (?,?,?)
			ON CONFLICT(eid) DO UPDATE SET last_contact=excluded.last_contact`, sess.EID, now, now)
	}
	return map[string]interface{}{"transactionId": txnID}
}

// GetBoundProfilePackage handles ES9+ GetBoundProfilePackage
// (GSMA SGP.22 §3.3.x). Returns the encrypted profile body the
// LPA installs into the eUICC. The body shape is JSON-wrapped
// here; the spec mandates ASN.1 (TODO GSMA SGP.22 §5.7).
func (s *SMDPServer) GetBoundProfilePackage(txnID, matchingID string) map[string]interface{} {
	s.mu.Lock()
	sess := s.sessions[txnID]
	if sess == nil || sess.State != "authenticated" { s.mu.Unlock(); return map[string]interface{}{"error": "not authenticated"} }
	s.mu.Unlock()

	row := engine.QueryRow(`SELECT iccid, profile_name, profile_state, profile_blob FROM esim_profiles WHERE matching_id=?`, matchingID)
	var iccid, name, state string; var blob []byte
	if err := row.Scan(&iccid, &name, &state, &blob); err != nil {
		return map[string]interface{}{"error": "no profile for matching ID"}
	}
	if state != "available" && state != "reserved" {
		return map[string]interface{}{"error": "profile already " + state}
	}

	now := float64(time.Now().Unix())
	eid := sess.EID
	engine.Exec(`UPDATE esim_profiles SET profile_state='downloaded', downloaded_at=?, eid=? WHERE iccid=?`, now, eid, iccid)
	engine.Exec(`INSERT INTO esim_notifications (iccid, eid, event_type, result_code, timestamp) VALUES (?,?,'download',0,?)`, iccid, eid, now)

	s.mu.Lock(); sess.State = "delivered"; s.mu.Unlock()
	return map[string]interface{}{"transactionId": txnID, "boundProfilePackage": map[string]interface{}{
		"iccid": iccid, "profileName": name, "profileData": string(blob)}}
}

// HandleNotification processes ES9+ lifecycle notifications
// (GSMA SGP.22 §3.5 Handle Notification). install / enable /
// disable / delete events drive profile state transitions.
func (s *SMDPServer) HandleNotification(iccid, eid, eventType string, seqNumber int) {
	stateMap := map[string]string{"install": "installed", "enable": "enabled", "disable": "disabled", "delete": "deleted"}
	if ns, ok := stateMap[eventType]; ok {
		engine.Exec(`UPDATE esim_profiles SET profile_state=? WHERE iccid=?`, ns, iccid)
	}
	now := float64(time.Now().Unix())
	engine.Exec(`INSERT INTO esim_notifications (iccid, eid, seq_number, event_type, result_code, timestamp)
		VALUES (?,?,?,?,0,?)`, iccid, eid, seqNumber, eventType, now)
	log.Infof("ES9+ Notification: ICCID=%s event=%s EID=%s", iccid, eventType, eid)
}

// GetProfileStatus returns profile info by ICCID.
func (s *SMDPServer) GetProfileStatus(iccid string) map[string]interface{} {
	row := engine.QueryRow(`SELECT id, iccid, imsi, profile_state, profile_name FROM esim_profiles WHERE iccid=?`, iccid)
	var id int64; var ic, im, st, nm string
	if err := row.Scan(&id, &ic, &im, &st, &nm); err != nil { return nil }
	return map[string]interface{}{"id": id, "iccid": ic, "imsi": im, "profile_state": st, "profile_name": nm}
}
