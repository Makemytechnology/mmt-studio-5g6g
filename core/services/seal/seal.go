// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package seal — Service Enabler Architecture Layer (SEAL).
//
// SEAL is the common services tier that vertical applications
// (MCX, V2X, UAS, FRMCS) share — group management, configuration,
// identity, location, key-management, network-resource enablers.
// We model the four most commonly-used services here.
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.434 §6           SEAL functional model — common SEAL
//                            services bus + per-service functions.
//   - TS 23.434 §9           Location management — VAL user location
//                            reporting + subscription (LMS).
//   - TS 23.434 §10          Group management — group create /
//                            member add / remove / join / leave (GMS).
//   - TS 23.434 §10.3        Procedures and information flows for
//                            group management (group / membership
//                            create / update / delete + notifications).
//   - TS 23.434 §11          Configuration management — per-user /
//                            per-group config distribution to VAL UEs
//                            (CMS).
//   - TS 23.434 §12          Identity management — VAL user identity
//                            allocation / mapping to 3GPP UE identity
//                            (IMSI), OpenID Connect (IdMS).
//   - TS 24.546 §5           SEAL CM protocol (UE ↔ CMS).
//   - TS 24.547 §5           SEAL IdMS protocol (UE ↔ IdMS, OAuth2 /
//                            OIDC token issuance).
//   - TS 24.548 §5           SEAL GM protocol (UE ↔ GMS, group
//                            announcement / membership signalling).
//
// Deferred (TODO at unimplemented call-sites — searchable by §):
//
//   - TS 23.434 §9.3         On-demand location request (LMS → UE
//                            "report-now") — today only periodic
//                            push subscription is modelled.
//   - TS 23.434 §12          Federated VAL identity across operators
//                            and OAuth2 / OIDC token issuance flows.
//   - TS 23.434 §13          Key management (KMS) — MIKEY-SAKKE key
//                            delivery (TS 33.180).
//   - TS 23.434 §14          Network Resource Management — QoS
//                            request to PCF on behalf of VAL.
//   - TS 24.546 §6           CMS notification channel (server-push
//                            of config changes to live VAL UEs).
//   - TS 24.547 §6           OAuth2 token refresh + revocation
//                            (today the IdMS surface only carries
//                            the val_user_id ↔ IMSI binding).
//
// Mirrors the tester-side dataclass module at
// mmt_studio_core_tester/src/protocol/seal.py.
package seal

import (
	"database/sql"
	"fmt"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ─── Types ───────────────────────────────────────────────────────

// Group is one row of seal_groups — TS 23.434 §10 GMS-managed group.
type Group struct {
	ID          int64    `json:"id"`
	Name        string   `json:"name"`
	Description *string  `json:"description,omitempty"`
	AppID       *string  `json:"app_id,omitempty"`
	ConfigJSON  *string  `json:"config_json,omitempty"`
	CreatedAt   string   `json:"created_at"`
	Members     []Member `json:"members,omitempty"`
}

// Member is one VAL participant in a SEAL group (TS 23.434 §10.3).
type Member struct {
	ID       int64  `json:"id"`
	GroupID  int64  `json:"group_id"`
	IMSI     string `json:"imsi"`
	Role     string `json:"role"`
	JoinedAt string `json:"joined_at"`
}

// LocationSub is a periodic location-reporting subscription
// (TS 23.434 §9 — Location Management Server).
type LocationSub struct {
	ID             int64   `json:"id"`
	TargetType     string  `json:"target_type"`
	TargetID       string  `json:"target_id"`
	CallbackURL    string  `json:"callback_url"`
	IntervalS      int     `json:"interval_s"`
	Active         int     `json:"active"`
	LastNotifiedAt *string `json:"last_notified_at,omitempty"`
	CreatedAt      string  `json:"created_at"`
}

// Config is one config item managed by SEAL CMS (TS 23.434 §11).
type Config struct {
	ID          int64   `json:"id"`
	TargetType  string  `json:"target_type"`
	TargetID    string  `json:"target_id"`
	ConfigKey   string  `json:"config_key"`
	ConfigValue *string `json:"config_value,omitempty"`
	UpdatedAt   string  `json:"updated_at"`
}

// ─── GUI panel API ───────────────────────────────────────────────

func List() ([]Group, error) { return ListGroups() }

func Status() map[string]any {
	groups, _ := ListGroups()
	subs, _ := ListLocationSubs(true)
	return map[string]any{"groups": len(groups), "location_subs": len(subs)}
}

// ─── Group CRUD (TS 23.434 §10 GMS) ──────────────────────────────

func ListGroups() ([]Group, error) {
	rows, err := engine.Query(`SELECT id, name, description, app_id, config_json, created_at
		FROM seal_groups ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.Name, &g.Description, &g.AppID, &g.ConfigJSON, &g.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

func GetGroup(id int64) (*Group, error) {
	row := engine.QueryRow(`SELECT id, name, description, app_id, config_json, created_at
		FROM seal_groups WHERE id=?`, id)
	var g Group
	err := row.Scan(&g.ID, &g.Name, &g.Description, &g.AppID, &g.ConfigJSON, &g.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	g.Members, _ = ListMembers(id)
	return &g, nil
}

func CreateGroup(name, description, appID string, configJSON *string) (int64, error) {
	if name == "" {
		return 0, fmt.Errorf("group name is required")
	}
	res, err := engine.Exec(`INSERT INTO seal_groups (name, description, app_id, config_json)
		VALUES (?,?,?,?)`, name, nilStr(description), nilStr(appID), configJSON)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DeleteGroup(id int64) error {
	_, err := engine.Exec(`DELETE FROM seal_groups WHERE id=?`, id)
	return err
}

// ─── Member CRUD (TS 23.434 §10.3) ───────────────────────────────

func ListMembers(groupID int64) ([]Member, error) {
	rows, err := engine.Query(`SELECT id, group_id, imsi, role, joined_at
		FROM seal_group_members WHERE group_id=? ORDER BY id`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Member
	for rows.Next() {
		var m Member
		if err := rows.Scan(&m.ID, &m.GroupID, &m.IMSI, &m.Role, &m.JoinedAt); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// AddMember adds a VAL user to a SEAL group with a role.
//
// TODO(TS 23.434 §10.3): full Group membership update request /
// response / notification flow (§10.3.2.6 / §10.3.2.7 / §10.3.2.8)
// — today the GMS-side AddMember is the only entry path; UE-
// initiated join is not modelled.
func AddMember(groupID int64, imsi, role string) (int64, error) {
	if role == "" {
		role = "member"
	}
	validRoles := map[string]bool{"admin": true, "member": true, "viewer": true}
	if !validRoles[role] {
		return 0, fmt.Errorf("invalid role '%s'", role)
	}
	res, err := engine.Exec(`INSERT INTO seal_group_members (group_id, imsi, role)
		VALUES (?,?,?)`, groupID, imsi, role)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func RemoveMember(groupID int64, imsi string) error {
	_, err := engine.Exec(`DELETE FROM seal_group_members WHERE group_id=? AND imsi=?`, groupID, imsi)
	return err
}

// ─── Location Subscriptions (TS 23.434 §9 LMS) ───────────────────

func ListLocationSubs(activeOnly bool) ([]LocationSub, error) {
	q := `SELECT id, target_type, target_id, callback_url, interval_s,
		active, last_notified_at, created_at FROM seal_location_subs`
	if activeOnly {
		q += " WHERE active=1"
	}
	q += " ORDER BY id"
	rows, err := engine.Query(q)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []LocationSub
	for rows.Next() {
		var s LocationSub
		if err := rows.Scan(&s.ID, &s.TargetType, &s.TargetID, &s.CallbackURL,
			&s.IntervalS, &s.Active, &s.LastNotifiedAt, &s.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// CreateLocationSub establishes a periodic location-push subscription
// (TS 23.434 §9 — VAL server subscribes to UE location updates).
//
// TODO(TS 23.434 §9.3): on-demand "report-now" location request is
// not implemented — only periodic push.
func CreateLocationSub(targetType, targetID, callbackURL string, intervalS int) (int64, error) {
	if intervalS <= 0 {
		intervalS = 60
	}
	res, err := engine.Exec(`INSERT INTO seal_location_subs
		(target_type, target_id, callback_url, interval_s, active)
		VALUES (?,?,?,?,1)`, targetType, targetID, callbackURL, intervalS)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func DeactivateLocationSub(id int64) error {
	_, err := engine.Exec(`UPDATE seal_location_subs SET active=0 WHERE id=?`, id)
	return err
}

// ─── Config CRUD (TS 23.434 §11 CMS) ─────────────────────────────

// GetConfig returns one (target, key) → value config item from the
// SEAL Configuration Management Server store.
func GetConfig(targetType, targetID, configKey string) (*Config, error) {
	row := engine.QueryRow(`SELECT id, target_type, target_id, config_key, config_value, updated_at
		FROM seal_configs WHERE target_type=? AND target_id=? AND config_key=?`,
		targetType, targetID, configKey)
	var c Config
	err := row.Scan(&c.ID, &c.TargetType, &c.TargetID, &c.ConfigKey, &c.ConfigValue, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &c, err
}

// SetConfig stores or overwrites a CMS config item.
//
// TODO(TS 24.546 §6): notification of live VAL UEs that their config
// changed (the spec defines a notification channel on SEAL-CM) —
// today the row is updated but no UE is poked.
func SetConfig(targetType, targetID, configKey, configValue string) error {
	_, err := engine.Exec(`INSERT INTO seal_configs (target_type, target_id, config_key, config_value)
		VALUES (?,?,?,?)
		ON CONFLICT(target_type, target_id, config_key) DO UPDATE SET
		config_value=excluded.config_value, updated_at=datetime('now')`,
		targetType, targetID, configKey, configValue)
	return err
}

// ─── VAL User Identity (TS 23.434 §12 IdMS) ──────────────────────

// VALUser is one mapping from a SEAL VAL user identity to a 3GPP UE
// identity (IMSI). The IdMS is the source of truth for this binding.
type VALUser struct {
	ID        int64   `json:"id"`
	VALUserID string  `json:"val_user_id"`
	IMSI      string  `json:"imsi"`
	AppID     *string `json:"app_id,omitempty"`
	CreatedAt string  `json:"created_at"`
}

// MapVALUser binds a VAL user identity to a UE identity.
//
// TODO(TS 24.547 §5 / §6): full OAuth2 token issuance + refresh +
// revocation for VAL user authentication — today we model only the
// underlying val_user_id ↔ IMSI binding row.
func MapVALUser(valUserID, imsi string, appID *string) (int64, error) {
	if valUserID == "" || imsi == "" {
		return 0, fmt.Errorf("val_user_id and imsi required")
	}
	res, err := engine.Exec(`INSERT INTO seal_val_users (val_user_id, imsi, app_id)
		VALUES (?,?,?)
		ON CONFLICT(val_user_id) DO UPDATE SET imsi=excluded.imsi, app_id=excluded.app_id`,
		valUserID, imsi, appID)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func UnmapVALUser(valUserID string) error {
	_, err := engine.Exec(`DELETE FROM seal_val_users WHERE val_user_id=?`, valUserID)
	return err
}

func ResolveVALUser(valUserID string) (*VALUser, error) {
	row := engine.QueryRow(`SELECT id, val_user_id, imsi, app_id, created_at
		FROM seal_val_users WHERE val_user_id=?`, valUserID)
	var u VALUser
	err := row.Scan(&u.ID, &u.VALUserID, &u.IMSI, &u.AppID, &u.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	return &u, err
}

func ResolveIMSI(imsi string) ([]VALUser, error) {
	rows, err := engine.Query(`SELECT id, val_user_id, imsi, app_id, created_at
		FROM seal_val_users WHERE imsi=?`, imsi)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VALUser
	for rows.Next() {
		var u VALUser
		if err := rows.Scan(&u.ID, &u.VALUserID, &u.IMSI, &u.AppID, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

func ListVALUsers() ([]VALUser, error) {
	rows, err := engine.Query(`SELECT id, val_user_id, imsi, app_id, created_at
		FROM seal_val_users ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VALUser
	for rows.Next() {
		var u VALUser
		if err := rows.Scan(&u.ID, &u.VALUserID, &u.IMSI, &u.AppID, &u.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ─── Location Query (TS 23.434 §9) ───────────────────────────────

// GetLocation returns the latest known position for an IMSI by
// joining onto the LMF positioning_sessions table — the SEAL LMS
// surfaces network-positioning results to VAL servers.
func GetLocation(imsi string) map[string]interface{} {
	row := engine.QueryRow(`SELECT latitude, longitude, uncertainty_m, method, completed_at
		FROM positioning_sessions WHERE imsi=? AND state='COMPLETED'
		ORDER BY completed_at DESC LIMIT 1`, imsi)
	var lat, lon *float64
	var unc *float64
	var method, completed *string
	if row.Scan(&lat, &lon, &unc, &method, &completed) != nil {
		return nil
	}
	return map[string]interface{}{
		"imsi": imsi, "latitude": lat, "longitude": lon,
		"uncertainty_m": unc, "method": method, "timestamp": completed,
	}
}

// ─── Config push/list ────────────────────────────────────────────

func ListConfigs() ([]Config, error) {
	rows, err := engine.Query(`SELECT id, target_type, target_id, config_key, config_value, updated_at
		FROM seal_configs ORDER BY target_type, target_id, config_key`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Config
	for rows.Next() {
		var c Config
		if err := rows.Scan(&c.ID, &c.TargetType, &c.TargetID, &c.ConfigKey, &c.ConfigValue, &c.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func DeleteConfig(id int64) error {
	_, err := engine.Exec(`DELETE FROM seal_configs WHERE id=?`, id)
	return err
}

// GetSEALStats returns aggregate counters for the OAM panel.
func GetSEALStats() map[string]interface{} {
	var groups, members, subs, configs, users int
	row := engine.QueryRow(`SELECT COUNT(*) FROM seal_groups`)
	row.Scan(&groups)
	row = engine.QueryRow(`SELECT COUNT(*) FROM seal_group_members`)
	row.Scan(&members)
	row = engine.QueryRow(`SELECT COUNT(*) FROM seal_location_subs WHERE active=1`)
	row.Scan(&subs)
	row = engine.QueryRow(`SELECT COUNT(*) FROM seal_configs`)
	row.Scan(&configs)
	row = engine.QueryRow(`SELECT COUNT(*) FROM seal_val_users`)
	row.Scan(&users)
	return map[string]interface{}{
		"groups": groups, "members": members, "active_location_subs": subs,
		"configs": configs, "val_users": users,
	}
}

func nilStr(s string) interface{} {
	if s == "" {
		return nil
	}
	return s
}
