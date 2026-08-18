// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package nbiot — NB-IoT / LTE-M power-saving + capability registry.
//
// Spec anchors:
//   - TS 23.401 §4.3.22 UE Power Saving Mode — "A UE may adopt a PSM
//     that is described in TS 23.682. … it shall request an Active
//     Time value and may request a Periodic TAU/RAU Timer value
//     during every Attach and Tracking Area Update procedure".
//     T3324 = Active Time; T3412-extended = Periodic TAU. We persist
//     both per UE in iot_psm_state.
//   - TS 23.401 §5.13a Extended Idle mode DRX — eDRX cycle + Paging
//     Time Window per UE in iot_edrx_config. Defaults match the
//     CT-WG NAS encoding's nominal values (eDRX 40.96 s, PTW 2.56 s).
//   - TS 23.401 §4.3.17 Support for Machine Type Communications —
//     §4.3.17.8 carries the NIDD bridge from the SCEF, and the
//     NB-IoT capability set (multi-tone / CE level / CP-CIoT /
//     UP-CIoT / data-over-NAS) is what the eNB / MME negotiates per
//     §4.3.17.8.1 and Annex F.
//   - TS 23.682 §4.5.21 Power Saving Mode (referenced by TS 23.401
//     §4.3.22) — origin of the PSM concept. Out-of-coverage paging
//     suppression behaviour described there is what the
//     'unreachable' state tracks.
//
// PSM state machine (operator-local labels):
//   active     — connected or idle within the Active Time window
//   sleeping   — Active Time expired, AS deactivated; MME suppresses
//                paging (TS 23.401 §4.3.22)
//   unreachable— sleeping past Periodic TAU due-time; SCEF / NEF
//                must buffer DL data (TS 23.682 §5.13.3)
//
// CE level / multi-tone / CP-CIoT / UP-CIoT / data-over-NAS bits in
// iot_nbiot_capabilities reflect the per-UE radio capability set the
// eNB advertises to the MME at NB-IoT attach (TS 23.401 §4.3.17 +
// TS 24.301 §5.5.1.2.4 NB-S1 capability container).
//
// TODO TS 24.301 §5.5.1.2.4 — anchor the CE-level / multi-tone bit
// definitions to the NAS capability IE encoding once TS 24.301 is
// loaded into specs/3gpp/.
package nbiot

import (
	"database/sql"
	"fmt"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// PSMState — TS 23.401 §4.3.22 PSM state row.
type PSMState struct {
	IMSI         string  `json:"imsi"`
	Enabled      bool    `json:"psm_enabled"`
	T3324Sec     int     `json:"t3324_sec"`     // Active Time
	T3412ExtSec  int     `json:"t3412_ext_sec"` // Periodic TAU timer
	State        string  `json:"psm_state"`     // active | sleeping | unreachable
	SleepStart   *string `json:"sleep_start,omitempty"`
	NextWakeup   *string `json:"next_wakeup,omitempty"`
}

// EDRXConfig — TS 23.401 §5.13a eDRX configuration row.
type EDRXConfig struct {
	IMSI          string  `json:"imsi"`
	DeviceType    string  `json:"device_type"` // nbiot | ltem | redcap
	EDRXCycleSec  float64 `json:"edrx_cycle_sec"`
	PTWSec        float64 `json:"ptw_sec"`
	Enabled       bool    `json:"enabled"`
}

// Capabilities — NB-IoT UE capability set advertised at attach
// (TS 23.401 §4.3.17 / Annex F; per-bit mapping to the NAS
// capability IE is TODO TS 24.301 §5.5.1.2.4).
type Capabilities struct {
	IMSI            string `json:"imsi"`
	MultiTone       bool   `json:"multi_tone"`
	CELevel         int    `json:"ce_level"`           // 0..2 — Coverage Enhancement level
	CPCIoTSupported bool   `json:"cp_ciot_supported"`  // Control-Plane CIoT (NAS-borne small data)
	UPCIoTSupported bool   `json:"up_ciot_supported"`  // User-Plane CIoT (DRB resume)
	DataOverNAS     bool   `json:"data_over_nas"`      // RFC for ESM Data Transport (TS 24.301)
}

// ── PSM CRUD ─────────────────────────────────────────────────────

// SetPSM persists the negotiated PSM parameters for a UE — caller
// is the MME or AMF after the Attach / TAU procedure that includes
// T3324 / T3412-extended (TS 23.401 §4.3.22).
func SetPSM(imsi string, t3324Sec, t3412ExtSec int) error {
	if strings.TrimSpace(imsi) == "" {
		return fmt.Errorf("imsi is required")
	}
	if t3324Sec <= 0 || t3412ExtSec <= 0 {
		return fmt.Errorf("t3324 and t3412_ext must be positive")
	}
	_, err := engine.Exec(`INSERT INTO iot_psm_state
		(imsi, psm_enabled, t3324_sec, t3412_ext_sec, psm_state)
		VALUES (?, 1, ?, ?, 'active')
		ON CONFLICT(imsi) DO UPDATE SET
		  psm_enabled=1, t3324_sec=excluded.t3324_sec,
		  t3412_ext_sec=excluded.t3412_ext_sec`,
		imsi, t3324Sec, t3412ExtSec)
	return err
}

// GetPSM reads the PSM row for a UE.
func GetPSM(imsi string) (*PSMState, error) {
	row := engine.QueryRow(`SELECT imsi, psm_enabled, t3324_sec, t3412_ext_sec,
		psm_state, sleep_start, next_wakeup FROM iot_psm_state WHERE imsi=?`, imsi)
	var p PSMState
	var enabled int
	err := row.Scan(&p.IMSI, &enabled, &p.T3324Sec, &p.T3412ExtSec,
		&p.State, &p.SleepStart, &p.NextWakeup)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	p.Enabled = enabled != 0
	return &p, nil
}

// EnterSleep transitions a UE into PSM 'sleeping' — Active Time
// (T3324) has expired, AS is deactivated. MME must suppress paging
// per TS 23.401 §4.3.22. NextWakeup is now + T3412-extended.
func EnterSleep(imsi string) error {
	p, err := GetPSM(imsi)
	if err != nil || p == nil {
		return fmt.Errorf("PSM not configured for %s", imsi)
	}
	if !p.Enabled {
		return fmt.Errorf("PSM not enabled for %s", imsi)
	}
	now := time.Now().UTC()
	wake := now.Add(time.Duration(p.T3412ExtSec) * time.Second)
	_, err = engine.Exec(`UPDATE iot_psm_state SET psm_state='sleeping',
		sleep_start=?, next_wakeup=? WHERE imsi=?`,
		now.Format(time.RFC3339), wake.Format(time.RFC3339), imsi)
	return err
}

// MarkUnreachable transitions a sleeping UE to 'unreachable' once
// next_wakeup has passed — SCEF must buffer DL traffic per
// TS 23.682 §5.13.3 (Mobile Terminated NIDD with high-latency
// communication path).
func MarkUnreachable(imsi string) error {
	_, err := engine.Exec(`UPDATE iot_psm_state SET psm_state='unreachable'
		WHERE imsi=? AND psm_state='sleeping'`, imsi)
	return err
}

// Wake transitions a UE back to 'active' (UE has performed mobile
// originated TAU or initiated MO data — TS 23.401 §4.3.22).
func Wake(imsi string) error {
	_, err := engine.Exec(`UPDATE iot_psm_state SET psm_state='active',
		sleep_start=NULL, next_wakeup=NULL WHERE imsi=?`, imsi)
	return err
}

// ── eDRX CRUD ────────────────────────────────────────────────────

// SetEDRX persists the negotiated eDRX cycle + PTW for a UE
// (TS 23.401 §5.13a). Defaults already in schema match the CT-WG
// NAS-IE nominal values (eDRX 40.96 s, PTW 2.56 s) and aren't
// re-validated here — operator-local clamping is the schema's job.
func SetEDRX(imsi, deviceType string, cycleSec, ptwSec float64) error {
	if strings.TrimSpace(imsi) == "" {
		return fmt.Errorf("imsi is required")
	}
	if deviceType == "" {
		deviceType = "nbiot"
	}
	if cycleSec <= 0 || ptwSec <= 0 {
		return fmt.Errorf("cycle and ptw must be positive")
	}
	_, err := engine.Exec(`INSERT INTO iot_edrx_config
		(imsi, device_type, edrx_cycle_sec, ptw_sec, enabled)
		VALUES (?, ?, ?, ?, 1)
		ON CONFLICT(imsi) DO UPDATE SET
		  device_type=excluded.device_type,
		  edrx_cycle_sec=excluded.edrx_cycle_sec,
		  ptw_sec=excluded.ptw_sec, enabled=1`,
		imsi, deviceType, cycleSec, ptwSec)
	return err
}

// GetEDRX returns the eDRX row for a UE.
func GetEDRX(imsi string) (*EDRXConfig, error) {
	row := engine.QueryRow(`SELECT imsi, device_type, edrx_cycle_sec, ptw_sec, enabled
		FROM iot_edrx_config WHERE imsi=?`, imsi)
	var e EDRXConfig
	var enabled int
	err := row.Scan(&e.IMSI, &e.DeviceType, &e.EDRXCycleSec, &e.PTWSec, &enabled)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	e.Enabled = enabled != 0
	return &e, nil
}

// ── Capabilities CRUD ────────────────────────────────────────────

// SetCapabilities persists the per-UE NB-IoT capability set
// (TS 23.401 §4.3.17 / Annex F). CE level must be 0..2 per
// the NAS NB-S1 capability container.
func SetCapabilities(c Capabilities) error {
	if strings.TrimSpace(c.IMSI) == "" {
		return fmt.Errorf("imsi is required")
	}
	if c.CELevel < 0 || c.CELevel > 2 {
		return fmt.Errorf("ce_level must be 0..2 (got %d)", c.CELevel)
	}
	_, err := engine.Exec(`INSERT INTO iot_nbiot_capabilities
		(imsi, multi_tone, ce_level, cp_ciot_supported,
		 up_ciot_supported, data_over_nas)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(imsi) DO UPDATE SET
		  multi_tone=excluded.multi_tone, ce_level=excluded.ce_level,
		  cp_ciot_supported=excluded.cp_ciot_supported,
		  up_ciot_supported=excluded.up_ciot_supported,
		  data_over_nas=excluded.data_over_nas`,
		c.IMSI, b2i(c.MultiTone), c.CELevel,
		b2i(c.CPCIoTSupported), b2i(c.UPCIoTSupported), b2i(c.DataOverNAS))
	return err
}

// GetCapabilities reads the NB-IoT capability row for a UE.
func GetCapabilities(imsi string) (*Capabilities, error) {
	row := engine.QueryRow(`SELECT imsi, multi_tone, ce_level,
		cp_ciot_supported, up_ciot_supported, data_over_nas
		FROM iot_nbiot_capabilities WHERE imsi=?`, imsi)
	var c Capabilities
	var mt, cp, up, don int
	err := row.Scan(&c.IMSI, &mt, &c.CELevel, &cp, &up, &don)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.MultiTone = mt != 0
	c.CPCIoTSupported = cp != 0
	c.UPCIoTSupported = up != 0
	c.DataOverNAS = don != 0
	return &c, nil
}

// ── GUI panel surface ────────────────────────────────────────────

// List returns all PSM rows (preserves the original GUI panel API).
func List() ([]map[string]any, error) {
	rows, err := engine.Query(`SELECT imsi, psm_enabled, psm_state, t3324_sec,
		t3412_ext_sec, sleep_start, next_wakeup FROM iot_psm_state ORDER BY imsi`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var imsi, state string
		var enabled, t3324, t3412 int
		var sleep, wake *string
		if err := rows.Scan(&imsi, &enabled, &state, &t3324, &t3412, &sleep, &wake); err != nil {
			continue
		}
		out = append(out, map[string]any{
			"imsi": imsi, "psm_enabled": enabled != 0, "psm_state": state,
			"t3324_sec": t3324, "t3412_ext_sec": t3412,
			"sleep_start": sleep, "next_wakeup": wake,
		})
	}
	return out, rows.Err()
}

// Status returns aggregate counts for the GUI panel.
func Status() map[string]any {
	rows, _ := engine.Query(`SELECT psm_state, COUNT(*) FROM iot_psm_state GROUP BY psm_state`)
	counts := map[string]int{"active": 0, "sleeping": 0, "unreachable": 0}
	if rows != nil {
		defer rows.Close()
		for rows.Next() {
			var state string
			var n int
			if err := rows.Scan(&state, &n); err == nil {
				counts[state] = n
			}
		}
	}
	total := counts["active"] + counts["sleeping"] + counts["unreachable"]
	return map[string]any{
		"count":       total,
		"active":      counts["active"],
		"sleeping":    counts["sleeping"],
		"unreachable": counts["unreachable"],
	}
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
