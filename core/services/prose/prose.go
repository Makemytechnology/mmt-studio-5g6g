// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package prose — 5G Proximity Services (D2D over PC5).
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 22.278 §6            5G ProSe service requirements (direct
//                             discovery, direct communication, UE-to-NW
//                             relay, UE-to-UE relay).
//   - TS 23.304 §4.2          Reference architecture for 5G ProSe.
//   - TS 23.304 §5.1          5G ProSe authorization & policy
//                             provisioning (PCF → UE).
//   - TS 23.304 §5.2          5G ProSe Direct Discovery (Models A & B,
//                             open / restricted, with PC3a / PC5).
//   - TS 23.304 §5.3          5G ProSe Direct Communication —
//                             broadcast (§5.3.2), groupcast (§5.3.3),
//                             unicast (§5.3.4) on PC5.
//   - TS 23.304 §5.4          UE-to-Network relay (5G ProSe-Layer-3
//                             relay; PC5 + N1 to 5GC via the relay UE).
//   - TS 23.304 §5.5          UE-to-UE relay.
//   - TS 24.554 §5            5G ProSe NAS-layer procedures (PC3a /
//                             PC3 — provisioning to/from PCF, USM, UE
//                             control plane signalling).
//   - TS 24.555 §5            5G ProSe PC5 signalling protocol —
//                             link establishment / modification /
//                             release on PC5-S.
//
// Deferred (TODO at unimplemented call-sites — searchable by §):
//
//   - TS 23.304 §5.2.4        Restricted (closed) discovery — today
//                             we model only "open" discovery
//                             (CheckAuthorization gates discovery, but
//                             does not enforce per-app permission lists).
//   - TS 23.304 §5.2.6        Discovery message protection (5G ProSe
//                             code material, integrity protection).
//   - TS 23.304 §6.4.3.1      Layer-2 link establishment over PC5 —
//                             today we model only the DB row, not
//                             the full PC5-S handshake.
//   - TS 23.304 §5.5          UE-to-UE relay path (full Layer-3 model).
//   - TS 24.555 §6            PC5 security activation (PC5 RRC keys).
//   - TS 33.503               5G ProSe security — credential delivery,
//                             link-layer keys (referenced by §5.2.6).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/prose.py.
package prose

import (
	"database/sql"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ─── Types ───────────────────────────────────────────────────────

// App is one ProSe application registration row — the (app-id,
// service-code) tuple that participants use to find each other
// (TS 23.304 §5.2 — discovery uses the ProSe Application Code).
type App struct {
	ID            int64  `json:"id"`
	AppID         string `json:"app_id"`
	Name          string `json:"name"`
	ProseAppCode  string `json:"prose_app_code"`
	ValidityHours int    `json:"validity_hours"`
	CreatedAt     string `json:"created_at"`
}

// UEConfig mirrors prose_ue_config — the authorization & feature
// flags the PCF carries to a 5G ProSe UE (TS 23.304 §5.1).
type UEConfig struct {
	ID                   int64  `json:"id"`
	IMSI                 string `json:"imsi"`
	Authorized           int    `json:"authorized"`
	DiscoveryEnabled     int    `json:"discovery_enabled"`
	CommunicationEnabled int    `json:"communication_enabled"`
	RelayCapable         int    `json:"relay_capable"`
	RelayEnabled         int    `json:"relay_enabled"`
	UpdatedAt            string `json:"updated_at"`
}

// Session mirrors prose_sessions — one PC5 link row per active
// unicast / groupcast / relay session.
type Session struct {
	ID          int64   `json:"id"`
	SessionType string  `json:"session_type"`
	SourceIMSI  string  `json:"source_imsi"`
	TargetIMSI  *string `json:"target_imsi,omitempty"`
	GroupID     *string `json:"group_id,omitempty"`
	RelayIMSI   *string `json:"relay_imsi,omitempty"`
	Service     *string `json:"service,omitempty"`
	Status      string  `json:"status"`
	CreatedAt   string  `json:"created_at"`
	ReleasedAt  *string `json:"released_at,omitempty"`
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]App, error) { return ListApps() }

func Status() map[string]any {
	apps, _ := ListApps()
	sessions, _ := ListSessions("", "")
	return map[string]any{"apps": len(apps), "sessions": len(sessions)}
}

// ─── App CRUD ────────────────────────────────────────────────────

func ListApps() ([]App, error) {
	rows, err := engine.Query(`SELECT id, app_id, name, prose_app_code, validity_hours, created_at
		FROM prose_apps ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []App
	for rows.Next() {
		var a App
		if err := rows.Scan(&a.ID, &a.AppID, &a.Name, &a.ProseAppCode, &a.ValidityHours, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func CreateApp(appID, name, proseAppCode string, validityHours int) (int64, error) {
	if validityHours <= 0 {
		validityHours = 24
	}
	res, err := engine.Exec(`INSERT INTO prose_apps (app_id, name, prose_app_code, validity_hours)
		VALUES (?,?,?,?)`, appID, name, proseAppCode, validityHours)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DeleteApp(appID string) error {
	_, err := engine.Exec(`DELETE FROM prose_apps WHERE app_id=?`, appID)
	return err
}

// ─── UE Config (TS 23.304 §5.1 policy provisioning) ──────────────

func GetUEConfig(imsi string) (*UEConfig, error) {
	row := engine.QueryRow(`SELECT id, imsi, authorized, discovery_enabled,
		communication_enabled, relay_capable, relay_enabled, updated_at
		FROM prose_ue_config WHERE imsi=?`, imsi)
	var c UEConfig
	err := row.Scan(&c.ID, &c.IMSI, &c.Authorized, &c.DiscoveryEnabled,
		&c.CommunicationEnabled, &c.RelayCapable, &c.RelayEnabled, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

func SetUEConfig(imsi string, authorized, discoveryEnabled, commEnabled, relayCapable, relayEnabled int) error {
	_, err := engine.Exec(`INSERT INTO prose_ue_config
		(imsi, authorized, discovery_enabled, communication_enabled, relay_capable, relay_enabled)
		VALUES (?,?,?,?,?,?)
		ON CONFLICT(imsi) DO UPDATE SET
		authorized=excluded.authorized, discovery_enabled=excluded.discovery_enabled,
		communication_enabled=excluded.communication_enabled,
		relay_capable=excluded.relay_capable, relay_enabled=excluded.relay_enabled,
		updated_at=datetime('now')`,
		imsi, authorized, discoveryEnabled, commEnabled, relayCapable, relayEnabled)
	return err
}

// ─── Session Management ──────────────────────────────────────────

func ListSessions(imsi, status string) ([]Session, error) {
	q := `SELECT id, session_type, source_imsi, target_imsi, group_id,
		relay_imsi, service, status, created_at, released_at
		FROM prose_sessions`
	var args []interface{}
	var conds []string
	if imsi != "" {
		conds = append(conds, "(source_imsi=? OR target_imsi=?)")
		args = append(args, imsi, imsi)
	}
	if status != "" {
		conds = append(conds, "status=?")
		args = append(args, status)
	}
	if len(conds) > 0 {
		q += " WHERE " + conds[0]
		for _, c := range conds[1:] {
			q += " AND " + c
		}
	}
	q += " ORDER BY id DESC"
	rows, err := engine.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		if err := rows.Scan(&s.ID, &s.SessionType, &s.SourceIMSI, &s.TargetIMSI,
			&s.GroupID, &s.RelayIMSI, &s.Service, &s.Status, &s.CreatedAt, &s.ReleasedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// SetupUnicast establishes a PC5 unicast link between two UEs
// (TS 23.304 §5.3.4 — Direct Communication, unicast mode).
//
// TODO(TS 23.304 §6.4.3.1): full Layer-2 link establishment
// procedure over PC5 — today the row is inserted directly.
//
// TODO(TS 24.555 §5): PC5 signalling protocol (PC5-S Direct Link
// Establishment Request / Accept message exchange) is not on the
// wire here — only the DB representation of an established session.
func SetupUnicast(sourceIMSI, targetIMSI, service string) (int64, error) {
	res, err := engine.Exec(`INSERT INTO prose_sessions
		(session_type, source_imsi, target_imsi, service, status)
		VALUES ('unicast',?,?,?,'active')`, sourceIMSI, targetIMSI, nilStr(service))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// SetupGroupcast establishes a PC5 groupcast session
// (TS 23.304 §5.3.3 — Direct Communication, groupcast mode;
// procedure detail at TS 23.304 §6.4.2).
func SetupGroupcast(sourceIMSI, groupID, service string) (int64, error) {
	res, err := engine.Exec(`INSERT INTO prose_sessions
		(session_type, source_imsi, group_id, service, status)
		VALUES ('groupcast',?,?,?,'active')`, sourceIMSI, nilStr(groupID), nilStr(service))
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// ReleaseSession releases a PC5 session (TS 23.304 §6.4.3.3 —
// Layer-2 link release over PC5, mirrored via TS 24.555 §5
// PC5-S Release Request).
func ReleaseSession(id int64) error {
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	_, err := engine.Exec(`UPDATE prose_sessions SET status='released', released_at=?
		WHERE id=? AND status='active'`, now, id)
	return err
}

func DeleteSession(id int64) error {
	_, err := engine.Exec(`DELETE FROM prose_sessions WHERE id=?`, id)
	return err
}

func nilStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
