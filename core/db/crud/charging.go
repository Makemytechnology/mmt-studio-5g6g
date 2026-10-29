// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/charging.go — Charging profile CRUD (TS 32.255, TS 32.291)
package crud

import (
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ChargingProfile mirrors the wire shape returned by charging_profiles_list/get.
type ChargingProfile struct {
	Name             string  `json:"name"`
	ChargingMethod   string  `json:"charging_method"` // "online" | "offline"
	RatingGroup      uint32  `json:"rating_group"`
	MeasVolume       bool    `json:"meas_volume"`
	MeasDuration     bool    `json:"meas_duration"`
	MeasEvent        bool    `json:"meas_event"`
	TrgVolumeLimit   bool    `json:"trg_volume_limit"`
	TrgTimeLimit     bool    `json:"trg_time_limit"`
	TrgSessionTerm   bool    `json:"trg_session_term"`
	TrgQuotaExhaust  bool    `json:"trg_quota_exhaust"`
	TrgThreshold     bool    `json:"trg_threshold"`
	TrgPeriodic      bool    `json:"trg_periodic"`
	VolQuotaUL       *int64  `json:"vol_quota_ul,omitempty"`
	VolQuotaDL       *int64  `json:"vol_quota_dl,omitempty"`
	TimeQuotaSec     *int64  `json:"time_quota_sec,omitempty"`
	FinalUnitAction  string  `json:"final_unit_action,omitempty"` // "" | "terminate" | "redirect" | "restrict"
	RedirectURL      string  `json:"redirect_url,omitempty"`
	VolThresholdUL   *int64  `json:"vol_threshold_ul,omitempty"`
	VolThresholdDL   *int64  `json:"vol_threshold_dl,omitempty"`
	TimeThresholdSec *int64  `json:"time_threshold_sec,omitempty"`
	MeasPeriodSec    *int64  `json:"meas_period_sec,omitempty"`
}

var reCPName = regexp.MustCompile(`^[A-Za-z0-9_]+$`)

const cpCols = `name, charging_method, rating_group,
    meas_volume, meas_duration, meas_event,
    trg_volume_limit, trg_time_limit, trg_session_term,
    trg_quota_exhaust, trg_threshold, trg_periodic,
    vol_quota_ul, vol_quota_dl, time_quota_sec,
    final_unit_action, redirect_url,
    vol_threshold_ul, vol_threshold_dl,
    time_threshold_sec, meas_period_sec`

func scanChargingProfile(row interface{ Scan(...any) error }) (*ChargingProfile, error) {
	var (
		p                                                                ChargingProfile
		mv, md, me, vl, tl, st, qe, th, pr                               int
		volUL, volDL, timeQ, volTUL, volTDL, timeT, measP                sql.NullInt64
		fua, redir                                                       sql.NullString
	)
	err := row.Scan(&p.Name, &p.ChargingMethod, &p.RatingGroup,
		&mv, &md, &me, &vl, &tl, &st, &qe, &th, &pr,
		&volUL, &volDL, &timeQ, &fua, &redir,
		&volTUL, &volTDL, &timeT, &measP)
	if err != nil {
		return nil, err
	}
	p.MeasVolume, p.MeasDuration, p.MeasEvent = mv != 0, md != 0, me != 0
	p.TrgVolumeLimit, p.TrgTimeLimit, p.TrgSessionTerm = vl != 0, tl != 0, st != 0
	p.TrgQuotaExhaust, p.TrgThreshold, p.TrgPeriodic = qe != 0, th != 0, pr != 0
	if volUL.Valid {
		v := volUL.Int64
		p.VolQuotaUL = &v
	}
	if volDL.Valid {
		v := volDL.Int64
		p.VolQuotaDL = &v
	}
	if timeQ.Valid {
		v := timeQ.Int64
		p.TimeQuotaSec = &v
	}
	p.FinalUnitAction = fua.String
	p.RedirectURL = redir.String
	if volTUL.Valid {
		v := volTUL.Int64
		p.VolThresholdUL = &v
	}
	if volTDL.Valid {
		v := volTDL.Int64
		p.VolThresholdDL = &v
	}
	if timeT.Valid {
		v := timeT.Int64
		p.TimeThresholdSec = &v
	}
	if measP.Valid {
		v := measP.Int64
		p.MeasPeriodSec = &v
	}
	return &p, nil
}

// ChargingProfilesList returns all profiles, ordered by name.
func ChargingProfilesList() ([]ChargingProfile, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT ` + cpCols + ` FROM charging_profiles ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ChargingProfile
	for rows.Next() {
		p, err := scanChargingProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

// ChargingProfilesGet returns one profile, or nil if not found.
func ChargingProfilesGet(name string) (*ChargingProfile, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT `+cpCols+` FROM charging_profiles WHERE name=?`, name)
	p, err := scanChargingProfile(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

// ChargingProfilesUpsert validates + inserts/updates. Returns the saved name
// or an error with a user-visible validation message.
func ChargingProfilesUpsert(p ChargingProfile) (string, error) {
	name := strings.TrimSpace(p.Name)
	if name == "" || len(name) > 64 || !reCPName.MatchString(name) {
		return "", errors.New("Name: max 64 chars, alphanumeric + underscores")
	}
	method := strings.TrimSpace(p.ChargingMethod)
	if method == "" {
		method = "offline"
	}
	if method != "online" && method != "offline" {
		return "", errors.New("charging_method must be 'online' or 'offline'")
	}
	if p.RatingGroup < 1 {
		return "", errors.New("rating_group must be 1..4294967295")
	}
	if !(p.MeasVolume || p.MeasDuration || p.MeasEvent) {
		return "", errors.New("At least one measurement method required")
	}

	// Prepaid validation
	if method == "online" {
		if p.MeasVolume && p.VolQuotaUL == nil && p.VolQuotaDL == nil {
			return "", errors.New("Prepaid + volume measurement requires volume quota")
		}
		if p.MeasDuration && p.TimeQuotaSec == nil {
			return "", errors.New("Prepaid + duration measurement requires time quota")
		}
		if p.FinalUnitAction == "" {
			return "", errors.New("Prepaid requires final_unit_action")
		}
		switch p.FinalUnitAction {
		case "terminate", "redirect", "restrict":
		default:
			return "", errors.New("final_unit_action: terminate/redirect/restrict")
		}
		if p.FinalUnitAction == "redirect" && strings.TrimSpace(p.RedirectURL) == "" {
			return "", errors.New("redirect action requires redirect_url")
		}
	}

	fua := sql.NullString{String: p.FinalUnitAction, Valid: p.FinalUnitAction != ""}
	redir := sql.NullString{String: p.RedirectURL, Valid: strings.TrimSpace(p.RedirectURL) != ""}

	db, err := engine.Open()
	if err != nil {
		return "", err
	}
	var exists int
	err = db.QueryRow(`SELECT 1 FROM charging_profiles WHERE name=?`, name).Scan(&exists)
	args := []any{
		method, p.RatingGroup,
		b2i(p.MeasVolume), b2i(p.MeasDuration), b2i(p.MeasEvent),
		b2i(p.TrgVolumeLimit), b2i(p.TrgTimeLimit), b2i(p.TrgSessionTerm),
		b2i(p.TrgQuotaExhaust), b2i(p.TrgThreshold), b2i(p.TrgPeriodic),
		nullableInt(p.VolQuotaUL), nullableInt(p.VolQuotaDL), nullableInt(p.TimeQuotaSec),
		fua, redir,
		nullableInt(p.VolThresholdUL), nullableInt(p.VolThresholdDL),
		nullableInt(p.TimeThresholdSec), nullableInt(p.MeasPeriodSec),
	}
	if errors.Is(err, sql.ErrNoRows) {
		ins := append([]any{name}, args...)
		_, err = db.Exec(
			`INSERT INTO charging_profiles (`+cpCols+`) VALUES
             (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, ins...)
		return name, err
	}
	if err != nil {
		return "", err
	}
	setCols := []string{
		"charging_method=?", "rating_group=?",
		"meas_volume=?", "meas_duration=?", "meas_event=?",
		"trg_volume_limit=?", "trg_time_limit=?", "trg_session_term=?",
		"trg_quota_exhaust=?", "trg_threshold=?", "trg_periodic=?",
		"vol_quota_ul=?", "vol_quota_dl=?", "time_quota_sec=?",
		"final_unit_action=?", "redirect_url=?",
		"vol_threshold_ul=?", "vol_threshold_dl=?",
		"time_threshold_sec=?", "meas_period_sec=?",
	}
	q := fmt.Sprintf(`UPDATE charging_profiles SET %s WHERE name=?`, strings.Join(setCols, ", "))
	args = append(args, name)
	_, err = db.Exec(q, args...)
	return name, err
}

// ChargingProfilesDelete unbinds services that reference it, then deletes.
func ChargingProfilesDelete(name string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	if _, err := db.Exec(`UPDATE services SET charging_profile=NULL WHERE charging_profile=?`, name); err != nil {
		return 0, err
	}
	res, err := db.Exec(`DELETE FROM charging_profiles WHERE name=?`, name)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ChargingProfilesNames returns profile names for GUI dropdowns.
func ChargingProfilesNames() ([]string, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT name FROM charging_profiles ORDER BY name ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullableInt(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
