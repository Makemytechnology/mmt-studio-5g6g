// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/seed/billing.go — Default charging profiles (postpaid + prepaid sample)
package seed

import "database/sql"

// SeedChargingProfiles inserts the three reference profiles shipped with the
// Python build (postpaid_default, postpaid_voice, prepaid_data). Idempotent.
func SeedChargingProfiles(db *sql.DB) error {
	type cp struct {
		Name               string
		Method             string
		RatingGroup        int
		MeasVolume         int
		MeasDuration       int
		MeasEvent          int
		TrgVolumeLimit     int
		TrgTimeLimit       int
		TrgSessionTerm     int
		TrgQuotaExhaust    int
		TrgThreshold       int
		TrgPeriodic        int
		VolQuotaUL         *int64
		VolQuotaDL         *int64
		TimeQuotaSec       *int64
		FinalUnitAction    *string
		RedirectURL        *string
		VolThresholdUL     *int64
		VolThresholdDL     *int64
		TimeThresholdSec   *int64
		MeasPeriodSec      *int64
	}
	mb := int64(1024 * 1024)
	gb := 1024 * mb
	term := "terminate"
	profiles := []cp{
		{
			Name: "postpaid_default", Method: "offline", RatingGroup: 1,
			MeasVolume: 1, TrgVolumeLimit: 1, TrgSessionTerm: 1, TrgThreshold: 1,
			VolThresholdUL: i64p(100 * mb), VolThresholdDL: i64p(100 * mb),
		},
		{
			Name: "postpaid_voice", Method: "offline", RatingGroup: 100,
			MeasVolume: 1, MeasDuration: 1,
			TrgVolumeLimit: 1, TrgTimeLimit: 1, TrgSessionTerm: 1, TrgThreshold: 1, TrgPeriodic: 1,
			VolThresholdUL: i64p(10 * mb), VolThresholdDL: i64p(10 * mb),
			TimeThresholdSec: i64p(3600), MeasPeriodSec: i64p(60),
		},
		{
			Name: "prepaid_data", Method: "online", RatingGroup: 200,
			MeasVolume: 1,
			TrgVolumeLimit: 1, TrgSessionTerm: 1, TrgQuotaExhaust: 1,
			VolQuotaUL: i64p(500 * mb), VolQuotaDL: i64p(gb),
			FinalUnitAction: &term,
		},
	}
	for _, p := range profiles {
		var exists int
		err := db.QueryRow(`SELECT 1 FROM charging_profiles WHERE name=?`, p.Name).Scan(&exists)
		if err == nil {
			continue
		}
		if _, err := db.Exec(`
            INSERT INTO charging_profiles
              (name, charging_method, rating_group,
               meas_volume, meas_duration, meas_event,
               trg_volume_limit, trg_time_limit, trg_session_term,
               trg_quota_exhaust, trg_threshold, trg_periodic,
               vol_quota_ul, vol_quota_dl, time_quota_sec,
               final_unit_action, redirect_url,
               vol_threshold_ul, vol_threshold_dl,
               time_threshold_sec, meas_period_sec)
            VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`,
			p.Name, p.Method, p.RatingGroup,
			p.MeasVolume, p.MeasDuration, p.MeasEvent,
			p.TrgVolumeLimit, p.TrgTimeLimit, p.TrgSessionTerm,
			p.TrgQuotaExhaust, p.TrgThreshold, p.TrgPeriodic,
			i64a(p.VolQuotaUL), i64a(p.VolQuotaDL), i64a(p.TimeQuotaSec),
			strA(p.FinalUnitAction), strA(p.RedirectURL),
			i64a(p.VolThresholdUL), i64a(p.VolThresholdDL),
			i64a(p.TimeThresholdSec), i64a(p.MeasPeriodSec),
		); err != nil {
			return err
		}
	}
	return nil
}

func i64p(v int64) *int64 { return &v }
func i64a(p *int64) any {
	if p == nil {
		return nil
	}
	return *p
}
func strA(p *string) any {
	if p == nil {
		return nil
	}
	return *p
}
