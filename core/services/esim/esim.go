// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package esim — eSIM profile + eUICC + notification persistence.
//
// Spec anchors:
//
//   - GSMA SGP.22 Consumer eSIM RSP — Activation-Code, Matching-ID,
//                                     and Bound-Profile-Package
//                                     concepts mirrored here.
//                                     (Not §-cited because GSMA
//                                     SGP.* documents are not in
//                                     the speccheck DOC_MAP.)
//   - GSMA SGP.32 IoT eSIM RSP    — IoT-side counterpart.
//   - TS 31.102 §4.2              — USIM ADF EF contents (where the
//                                   IMSI / OPc / K downloaded via
//                                   the profile actually live on
//                                   the card).
//   - TS 23.003 §2.2              — IMSI structure (validated by
//                                   subscriber-side code; this
//                                   package only persists).
//
// Profile-state vocabulary mirrors SGP.22's lifecycle:
//   available → reserved → downloaded → installed → enabled /
//   disabled / deleted.
//
// TODO GSMA SGP.22 §5.7 — full ASN.1-encoded BPP / BoundProfile-
//                         Package wire codec (today this layer
//                         persists only the JSON envelope).
// TODO GSMA SGP.22 §3   — ES2+ operator-side profile order /
//                         download-trigger workflow (today
//                         CreateProfile is a direct DB insert).
// TODO GSMA SGP.32      — IoT Profile Provisioning (eIM, IPA).
// TODO TS 33.501 §6.12  — SUCI computation off the downloaded K
//                         (this package just stores K alongside
//                         IMSI; the SUCI engine lives elsewhere).
package esim

import (
	"database/sql"
	"fmt"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ---- Types ----

type Profile struct {
	ID             int64    `json:"id"`
	ICCID          string   `json:"iccid"`
	IMSI           string   `json:"imsi"`
	EID            *string  `json:"eid,omitempty"`
	ProfileState   string   `json:"profile_state"`
	ActivationCode *string  `json:"activation_code,omitempty"`
	MatchingID     *string  `json:"matching_id,omitempty"`
	SMDPAddress    *string  `json:"smdp_address,omitempty"`
	ProfileName    string   `json:"profile_name"`
	ProfileType    string   `json:"profile_type"`
	ProfileClass   string   `json:"profile_class"`
	CreatedAt      float64  `json:"created_at"`
	ReservedAt     *float64 `json:"reserved_at,omitempty"`
	DownloadedAt   *float64 `json:"downloaded_at,omitempty"`
	InstalledAt    *float64 `json:"installed_at,omitempty"`
}

type EUICC struct {
	ID           int64    `json:"id"`
	EID          string   `json:"eid"`
	DeviceInfo   *string  `json:"device_info,omitempty"`
	LPAVersion   *string  `json:"lpa_version,omitempty"`
	EUICCInfo    *string  `json:"euicc_info,omitempty"`
	CurrentICCID *string  `json:"current_iccid,omitempty"`
	LastContact  *float64 `json:"last_contact,omitempty"`
	RegisteredAt float64  `json:"registered_at"`
}

type Notification struct {
	ID         int64   `json:"id"`
	ICCID      string  `json:"iccid"`
	EID        *string `json:"eid,omitempty"`
	SeqNumber  int     `json:"seq_number"`
	EventType  string  `json:"event_type"`
	ResultCode int     `json:"result_code"`
	Timestamp  float64 `json:"timestamp"`
}

// ---- GUI panel API ----

func List() ([]Profile, error) { return ListProfiles("") }

func Status() map[string]any {
	profiles, _ := ListProfiles("")
	euiccs, _ := ListEUICCs()
	return map[string]any{"profiles": len(profiles), "euiccs": len(euiccs)}
}

// ---- Profile CRUD ----

func ListProfiles(state string) ([]Profile, error) {
	q := `SELECT id, iccid, imsi, eid, profile_state, activation_code,
		matching_id, smdp_address, profile_name, profile_type, profile_class,
		created_at, reserved_at, downloaded_at, installed_at
		FROM esim_profiles`
	var args []interface{}
	if state != "" {
		q += " WHERE profile_state=?"
		args = append(args, state)
	}
	q += " ORDER BY id"
	rows, err := engine.Query(q, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.ICCID, &p.IMSI, &p.EID, &p.ProfileState,
			&p.ActivationCode, &p.MatchingID, &p.SMDPAddress, &p.ProfileName,
			&p.ProfileType, &p.ProfileClass, &p.CreatedAt, &p.ReservedAt,
			&p.DownloadedAt, &p.InstalledAt); err != nil { return nil, err }
		out = append(out, p)
	}
	return out, rows.Err()
}

func GetProfile(id int64) (*Profile, error) {
	row := engine.QueryRow(`SELECT id, iccid, imsi, eid, profile_state, activation_code,
		matching_id, smdp_address, profile_name, profile_type, profile_class,
		created_at, reserved_at, downloaded_at, installed_at
		FROM esim_profiles WHERE id=?`, id)
	var p Profile
	err := row.Scan(&p.ID, &p.ICCID, &p.IMSI, &p.EID, &p.ProfileState,
		&p.ActivationCode, &p.MatchingID, &p.SMDPAddress, &p.ProfileName,
		&p.ProfileType, &p.ProfileClass, &p.CreatedAt, &p.ReservedAt,
		&p.DownloadedAt, &p.InstalledAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &p, err
}

func GetProfileByICCID(iccid string) (*Profile, error) {
	row := engine.QueryRow(`SELECT id, iccid, imsi, eid, profile_state, activation_code,
		matching_id, smdp_address, profile_name, profile_type, profile_class,
		created_at, reserved_at, downloaded_at, installed_at
		FROM esim_profiles WHERE iccid=?`, iccid)
	var p Profile
	err := row.Scan(&p.ID, &p.ICCID, &p.IMSI, &p.EID, &p.ProfileState,
		&p.ActivationCode, &p.MatchingID, &p.SMDPAddress, &p.ProfileName,
		&p.ProfileType, &p.ProfileClass, &p.CreatedAt, &p.ReservedAt,
		&p.DownloadedAt, &p.InstalledAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &p, err
}

func GetProfileByActivationCode(ac string) (*Profile, error) {
	row := engine.QueryRow(`SELECT id, iccid, imsi, eid, profile_state, activation_code,
		matching_id, smdp_address, profile_name, profile_type, profile_class,
		created_at, reserved_at, downloaded_at, installed_at
		FROM esim_profiles WHERE activation_code=?`, ac)
	var p Profile
	err := row.Scan(&p.ID, &p.ICCID, &p.IMSI, &p.EID, &p.ProfileState,
		&p.ActivationCode, &p.MatchingID, &p.SMDPAddress, &p.ProfileName,
		&p.ProfileType, &p.ProfileClass, &p.CreatedAt, &p.ReservedAt,
		&p.DownloadedAt, &p.InstalledAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &p, err
}

func GetProfileByMatchingID(mid string) (*Profile, error) {
	row := engine.QueryRow(`SELECT id, iccid, imsi, eid, profile_state, activation_code,
		matching_id, smdp_address, profile_name, profile_type, profile_class,
		created_at, reserved_at, downloaded_at, installed_at
		FROM esim_profiles WHERE matching_id=?`, mid)
	var p Profile
	err := row.Scan(&p.ID, &p.ICCID, &p.IMSI, &p.EID, &p.ProfileState,
		&p.ActivationCode, &p.MatchingID, &p.SMDPAddress, &p.ProfileName,
		&p.ProfileType, &p.ProfileClass, &p.CreatedAt, &p.ReservedAt,
		&p.DownloadedAt, &p.InstalledAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &p, err
}

func GetProfilesForIMSI(imsi string) ([]Profile, error) {
	rows, err := engine.Query(`SELECT id, iccid, imsi, eid, profile_state, activation_code,
		matching_id, smdp_address, profile_name, profile_type, profile_class,
		created_at, reserved_at, downloaded_at, installed_at
		FROM esim_profiles WHERE imsi=? ORDER BY id`, imsi)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		var p Profile
		if err := rows.Scan(&p.ID, &p.ICCID, &p.IMSI, &p.EID, &p.ProfileState,
			&p.ActivationCode, &p.MatchingID, &p.SMDPAddress, &p.ProfileName,
			&p.ProfileType, &p.ProfileClass, &p.CreatedAt, &p.ReservedAt,
			&p.DownloadedAt, &p.InstalledAt); err != nil { return nil, err }
		out = append(out, p)
	}
	return out, rows.Err()
}

func CreateProfile(iccid, imsi, profileName, profileType, profileClass string,
	activationCode, matchingID, smdpAddress *string) (int64, error) {
	if profileName == "" { profileName = "SA Core" }
	if profileType == "" { profileType = "operational" }
	if profileClass == "" { profileClass = "operational" }
	now := float64(time.Now().Unix())
	res, err := engine.Exec(`INSERT INTO esim_profiles
		(iccid, imsi, profile_state, activation_code, matching_id, smdp_address,
		 profile_name, profile_type, profile_class, created_at)
		VALUES (?,?,'available',?,?,?,?,?,?,?)`,
		iccid, imsi, activationCode, matchingID, smdpAddress,
		profileName, profileType, profileClass, now)
	if err != nil { return 0, err }
	return res.LastInsertId()
}

// UpdateProfileState transitions a profile to a new state.
// Lifecycle (mirrors the GSMA SGP.22 profile state machine):
//
//   available → reserved → downloaded → installed
//                                          ↓
//                                       enabled ⇆ disabled
//                                          ↓
//                                       deleted
func UpdateProfileState(id int64, newState string) error {
	validStates := map[string]bool{
		"available": true, "reserved": true, "downloaded": true,
		"installed": true, "enabled": true, "disabled": true, "deleted": true,
	}
	if !validStates[newState] { return fmt.Errorf("invalid profile state: %s", newState) }
	now := float64(time.Now().Unix())
	q := fmt.Sprintf("UPDATE esim_profiles SET profile_state=?")
	args := []interface{}{newState}
	switch newState {
	case "reserved":
		q += ", reserved_at=?"
		args = append(args, now)
	case "downloaded":
		q += ", downloaded_at=?"
		args = append(args, now)
	case "installed":
		q += ", installed_at=?"
		args = append(args, now)
	}
	q += " WHERE id=?"
	args = append(args, id)
	_, err := engine.Exec(q, args...)
	return err
}

func DeleteProfile(id int64) error {
	_, err := engine.Exec(`DELETE FROM esim_profiles WHERE id=?`, id)
	return err
}

// ---- eUICC CRUD ----

func ListEUICCs() ([]EUICC, error) {
	rows, err := engine.Query(`SELECT id, eid, device_info, lpa_version,
		euicc_info, current_iccid, last_contact, registered_at
		FROM esim_euicc ORDER BY id`)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []EUICC
	for rows.Next() {
		var e EUICC
		if err := rows.Scan(&e.ID, &e.EID, &e.DeviceInfo, &e.LPAVersion,
			&e.EUICCInfo, &e.CurrentICCID, &e.LastContact, &e.RegisteredAt); err != nil { return nil, err }
		out = append(out, e)
	}
	return out, rows.Err()
}

func GetEUICC(eid string) (*EUICC, error) {
	row := engine.QueryRow(`SELECT id, eid, device_info, lpa_version,
		euicc_info, current_iccid, last_contact, registered_at
		FROM esim_euicc WHERE eid=?`, eid)
	var e EUICC
	err := row.Scan(&e.ID, &e.EID, &e.DeviceInfo, &e.LPAVersion,
		&e.EUICCInfo, &e.CurrentICCID, &e.LastContact, &e.RegisteredAt)
	if err == sql.ErrNoRows { return nil, nil }
	return &e, err
}

func RegisterEUICC(eid, deviceInfo, lpaVersion string) (int64, error) {
	now := float64(time.Now().Unix())
	res, err := engine.Exec(`INSERT INTO esim_euicc
		(eid, device_info, lpa_version, registered_at)
		VALUES (?,?,?,?)`, eid, nilStr(deviceInfo), nilStr(lpaVersion), now)
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func DeleteEUICC(eid string) error {
	_, err := engine.Exec(`DELETE FROM esim_euicc WHERE eid=?`, eid)
	return err
}

// ---- Notification Log ----

func LogNotification(iccid string, eid *string, eventType string, resultCode int) (int64, error) {
	now := float64(time.Now().Unix())
	res, err := engine.Exec(`INSERT INTO esim_notifications
		(iccid, eid, event_type, result_code, timestamp)
		VALUES (?,?,?,?,?)`, iccid, eid, eventType, resultCode, now)
	if err != nil { return 0, err }
	return res.LastInsertId()
}

func ListNotifications(iccid string, limit int) ([]Notification, error) {
	if limit <= 0 { limit = 50 }
	rows, err := engine.Query(`SELECT id, iccid, eid, seq_number, event_type,
		result_code, timestamp FROM esim_notifications
		WHERE iccid=? ORDER BY id DESC LIMIT ?`, iccid, limit)
	if err != nil { return nil, err }
	defer rows.Close()
	var out []Notification
	for rows.Next() {
		var n Notification
		if err := rows.Scan(&n.ID, &n.ICCID, &n.EID, &n.SeqNumber,
			&n.EventType, &n.ResultCode, &n.Timestamp); err != nil { return nil, err }
		out = append(out, n)
	}
	return out, rows.Err()
}

func nilStr(s string) interface{} {
	if s == "" { return nil }
	return s
}
