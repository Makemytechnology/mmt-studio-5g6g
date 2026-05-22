// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package common — MCX common utilities: identity, priority, key
// management, config, user profiles, groups.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.280 §8.1.1   MC ID — the MCX user identity (federated
//                          across MCPTT/MCVideo/MCData; rendered as
//                          a SIP URI in the application plane).
//   - TS 23.280 §8.1.2   MC service user ID — the per-service user
//                          identity (this package's MCPTT-ID form).
//   - TS 23.280 §8.1.3   MC service group ID — per-service group
//                          identity used for group-call setup.
//   - TS 23.280 §8.1.4   MC system ID — the operator-assigned
//                          system identifier.
//   - TS 23.280 §10.1.4  MC service user profile (priority and role
//                          attributes that drive call admission).
//   - TS 33.180 §5.2     Group key management (GMK distribution).
//   - TS 33.180 §7       MIKEY-SAKKE based group / private call
//                          media keying — applies to the encrypted
//                          GroupKey we generate here when a group
//                          is created or rotated.
//
// Stage-3 protocol-on-the-wire details (XML body shapes, MIME
// types, exact Group-Document XML) live in TS 24.379 (MCPTT) and
// in the stage-3 specs TS 24.281 (MCVideo) / TS 24.282 (MCData) —
// the latter two are not yet in-tree, so rendering hooks for them
// are flagged with TS-numbered TODOs at the relevant call sites
// rather than §-cited.
package common

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("mcx.common")

// ── Identity (TS 23.280 §8.1) ──

// DefaultMCXDomain is the application-plane domain part of the MC
// service ID (TS 23.280 §8.1.2). Real deployments set MCX_DOMAIN
// from the operator's configuration management server (TS 23.280
// §7.4.2.3.2 "Configuration management server").
const DefaultMCXDomain = "mcx.sacore.local"

func MCXDomain() string {
	if d := os.Getenv("MCX_DOMAIN"); d != "" { return d }
	return DefaultMCXDomain
}

// IMSIToMCPTTID derives an MCPTT MC service ID (TS 23.280 §8.1.2)
// from the bound USIM's IMSI. The "mcptt:" scheme is a SIP-URI
// scheme alternative; production deployments typically use a "sip:"
// MC service ID (TS 23.280 §8.3.1 "Relationship between MC service
// ID and public user identity") provisioned by the IdMS.
//
// TODO(spec: TS 23.280 §8.3.1): replace the "mcptt:"-scheme local
// derivation with the IMPU mapping returned by the configuration
// management server. The current scheme is a stop-gap that lets
// the MCX panel show a stable ID without a full IdMS round-trip.
func IMSIToMCPTTID(imsi string) string { return "mcptt:" + imsi + "@" + MCXDomain() }

func MCPTTIDToIMSI(id string) string {
	if !strings.HasPrefix(id, "mcptt:") { return "" }
	local := id[6:]
	if at := strings.Index(local, "@"); at >= 0 { return local[:at] }
	return local
}

func IMSIToIMPU(imsi, imsDomain string) string { return "sip:" + imsi + "@" + imsDomain }

func MCPTTIDToIMPU(id, imsDomain string) string {
	imsi := MCPTTIDToIMSI(id)
	if imsi == "" { return "" }
	return IMSIToIMPU(imsi, imsDomain)
}

// ValidateMCPTTID enforces the "mcptt:user@domain" structure of an
// MC service ID per TS 23.280 §8.1.2. A non-empty user part and a
// non-empty domain part are both required — an MC service ID with
// no user is meaningless under §8.1.2.
func ValidateMCPTTID(id string) bool {
	if !strings.HasPrefix(id, "mcptt:") {
		return false
	}
	rest := id[6:]
	at := strings.Index(rest, "@")
	if at <= 0 { // missing '@', or empty user
		return false
	}
	if at >= len(rest)-1 { // empty domain
		return false
	}
	return true
}

// ── Priority ──
//
// Local FloorController ordering ranks (lower numeric value wins),
// NOT the SIP Resource-Priority namespace values sent on the wire.
// Per TS 24.379 §6.2.8.1.15 the Resource-Priority namespace + values
// are retrieved from the MCPTT service configuration (TS 24.484);
// the constants here serve only the in-process float ordering and
// the §4.1.1.4 "preempt-override" decision.
const (
	PriorityEmergency     = 1
	PriorityImminentPeril = 2
	PriorityHigh          = 3
	PriorityNormal        = 5
	PriorityLow           = 7
	PriorityBackground    = 9
	PreemptThreshold      = 3
)

var PriorityNames = map[int]string{
	PriorityEmergency: "emergency", PriorityImminentPeril: "imminent_peril",
	PriorityHigh: "high", PriorityNormal: "normal",
	PriorityLow: "low", PriorityBackground: "background",
}

func CanPreempt(requester, holder int) bool {
	if requester == PriorityEmergency { return true }
	return requester < holder
}

func EffectivePriority(userPri, callPri int, emergency bool) int {
	if emergency { return PriorityEmergency }
	if userPri < callPri { return userPri }
	return callPri
}

func PriorityName(level int) string {
	if n, ok := PriorityNames[level]; ok { return n }
	return fmt.Sprintf("level-%d", level)
}

// ── Key Manager (TS 33.180 §5.2 / §7.3) ──

// GenerateGroupKey returns a fresh 256-bit Group Master Key (GMK)
// per TS 33.180 §7.3 "Group communications" media-keying. In a
// fully MIKEY-SAKKE flow this GMK is wrapped to each member's
// SAKKE identity (TS 33.180 §5.2.1 "MIKEY-SAKKE for MC services")
// before being delivered through the Group Management Server
// notification (TS 23.280 §10.2.x).
//
// TODO(spec: TS 33.180 §5.2.1): wire the MIKEY-SAKKE wrapping. We
// generate the key here but do not yet publish it through the GMS
// notification flow with member-specific SAKKE wrapping.
//
// TODO(spec: TS 33.180 §7.4): the per-call key derivation chain
// (GMK → GMK-ID → MKFC → SRTP master key) is not implemented; the
// raw GMK is stored as the group's encryption_key column, which
// is sufficient for non-mission-critical demo flows but not for
// SRTP/SRTCP actually negotiated under §7.5.
func GenerateGroupKey() string {
	b := make([]byte, 32)
	rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

// ── Config (mcx_config.py) ──

func envInt(k string, def int) int { v := os.Getenv(k); if v == "" { return def }; n, _ := strconv.Atoi(v); return n }

var (
	MCXSIPPort    = envInt("MCX_SIP_PORT", 8808)
	MCXWSPort     = envInt("MCX_WS_PORT", 8809)
	MCXRTPPortMin = envInt("MCX_RTP_PORT_MIN", 10000)
	MCXRTPPortMax = envInt("MCX_RTP_PORT_MAX", 10100)
	FloorTimeout  = envInt("MCX_FLOOR_TIMEOUT", 30)
	MaxFloorQueue = envInt("MCX_MAX_FLOOR_QUEUE", 10)
	MaxFileSizeMB = envInt("MCX_MAX_FILE_SIZE_MB", 50)
)

// ── Key Manager DB operations ──

func RotateGroupKey(groupID int64) string {
	k := GenerateGroupKey()
	engine.Exec(`UPDATE mcx_groups SET encryption_key=? WHERE id=?`, k, groupID)
	return k
}

func GetGroupKey(groupID int64) string {
	row := engine.QueryRow(`SELECT encryption_key FROM mcx_groups WHERE id=?`, groupID)
	var key sql.NullString; row.Scan(&key)
	if key.Valid && key.String != "" { return key.String }
	k := GenerateGroupKey()
	engine.Exec(`UPDATE mcx_groups SET encryption_key=? WHERE id=?`, k, groupID)
	return k
}

// ── Config Service (config_service.py) ──

func GenerateUEConfig(mcpttID, serverHost string) map[string]interface{} {
	row := engine.QueryRow(`SELECT display_name, priority, org, role FROM mcx_user_profiles WHERE mcptt_id=?`, mcpttID)
	var name, role string; var priority int; var org sql.NullString
	if err := row.Scan(&name, &priority, &org, &role); err != nil { return nil }
	rows, _ := engine.Query(`SELECT g.id, g.name, g.group_type FROM mcx_groups g
		JOIN mcx_group_members gm ON gm.group_id=g.id WHERE gm.mcptt_id=? ORDER BY g.id`, mcpttID)
	var groups []map[string]interface{}
	if rows != nil { defer rows.Close()
		for rows.Next() { var gid int64; var gn, gt string; rows.Scan(&gid, &gn, &gt)
			groups = append(groups, map[string]interface{}{"id": gid, "name": gn, "type": gt}) } }
	orgVal := ""; if org.Valid { orgVal = org.String }
	return map[string]interface{}{
		"mcptt_id": mcpttID, "display_name": name, "priority": priority, "org": orgVal, "role": role,
		"servers": map[string]interface{}{
			"rest_api": fmt.Sprintf("http://%s:5000/api/mcx", serverHost),
			"websocket": fmt.Sprintf("ws://%s:%d", serverHost, MCXWSPort),
			"sip": map[string]interface{}{"host": serverHost, "port": MCXSIPPort, "transport": "UDP"},
			"rtp": map[string]interface{}{"port_min": MCXRTPPortMin, "port_max": MCXRTPPortMax},
		},
		"domain": MCXDomain(), "groups": groups,
	}
}

// ── Group Manager (TS 23.280 §8.1.3 + §10.2 group management) ──

// CreateMCXGroup persists an MC service group ID per TS 23.280
// §8.1.3.1 (general group ID rules) and §10.2.2.1 / §10.2.2.2
// (Group creation request / response information flows). The DB
// row mirrors the on-the-wire Group-Document XML defined in
// TS 24.481, which we don't render yet — see TODO below.
//
// TODO(spec: TS 24.481): render and publish the Group-Document XML
// to the Group Management Server (XCAP PUT). Today the group lives
// only in the local mcx_groups table; clients cannot fetch it via
// the standard GMS document URI.
func CreateMCXGroup(name, groupType string, maxMembers, priority int) map[string]interface{} {
	if maxMembers <= 0 { maxMembers = 50 }; if priority <= 0 { priority = 5 }; if groupType == "" { groupType = "normal" }
	res, err := engine.Exec(`INSERT INTO mcx_groups (name, group_type, max_members, priority, enabled, created_at)
		VALUES (?,?,?,?,1,strftime('%%s','now'))`, name, groupType, maxMembers, priority)
	if err != nil { return nil }
	id, _ := res.LastInsertId()
	return map[string]interface{}{"id": id, "name": name, "group_type": groupType, "max_members": maxMembers, "priority": priority}
}

func JoinGroup(groupID int64, mcpttID, role string) (bool, string) {
	if role == "" { role = "member" }
	row := engine.QueryRow(`SELECT 1 FROM mcx_user_profiles WHERE mcptt_id=?`, mcpttID)
	var x int; if row.Scan(&x) != nil { return false, "User not found" }
	row = engine.QueryRow(`SELECT max_members FROM mcx_groups WHERE id=?`, groupID)
	var maxMem int; if row.Scan(&maxMem) != nil { return false, "Group not found" }
	row = engine.QueryRow(`SELECT COUNT(*) FROM mcx_group_members WHERE group_id=?`, groupID)
	var cnt int; row.Scan(&cnt)
	if cnt >= maxMem { return false, "Group full" }
	_, err := engine.Exec(`INSERT INTO mcx_group_members (group_id, mcptt_id, role, joined_at) VALUES (?,?,?,strftime('%%s','now'))`, groupID, mcpttID, role)
	if err != nil { return false, "Already a member" }
	return true, "Joined"
}

func LeaveGroup(groupID int64, mcpttID string) bool {
	res, _ := engine.Exec(`DELETE FROM mcx_group_members WHERE group_id=? AND mcptt_id=?`, groupID, mcpttID)
	n, _ := res.RowsAffected(); return n > 0
}

func GetGroupInfo(groupID int64) map[string]interface{} {
	row := engine.QueryRow(`SELECT id, name, group_type, max_members, priority FROM mcx_groups WHERE id=?`, groupID)
	var id int64; var name, gtype string; var maxMem, pri int
	if row.Scan(&id, &name, &gtype, &maxMem, &pri) != nil { return nil }
	rows, _ := engine.Query(`SELECT gm.mcptt_id, gm.role, up.display_name FROM mcx_group_members gm
		JOIN mcx_user_profiles up ON up.mcptt_id=gm.mcptt_id WHERE gm.group_id=?`, groupID)
	var members []map[string]interface{}
	if rows != nil { defer rows.Close()
		for rows.Next() { var mid, mrole, mname string; rows.Scan(&mid, &mrole, &mname)
			members = append(members, map[string]interface{}{"mcptt_id": mid, "role": mrole, "display_name": mname}) } }
	return map[string]interface{}{"id": id, "name": name, "group_type": gtype, "max_members": maxMem, "priority": pri, "members": members}
}

// ── User Profile (user_profile.py) ──

func GetOrCreateMCXUser(imsi, displayName string, priority int) map[string]interface{} {
	mcpttID := IMSIToMCPTTID(imsi)
	row := engine.QueryRow(`SELECT id, ue_id, mcptt_id, display_name, priority FROM mcx_user_profiles WHERE mcptt_id=?`, mcpttID)
	var id, ueID int64; var mid, name string; var pri int
	if row.Scan(&id, &ueID, &mid, &name, &pri) == nil {
		return map[string]interface{}{"id": id, "ue_id": ueID, "mcptt_id": mid, "display_name": name, "priority": pri}
	}
	row = engine.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi)
	if row.Scan(&ueID) != nil { return nil }
	if displayName == "" { if len(imsi) >= 4 { displayName = "User-" + imsi[len(imsi)-4:] } else { displayName = "User-" + imsi } }
	if priority <= 0 { priority = 5 }
	engine.Exec(`INSERT INTO mcx_user_profiles (ue_id, mcptt_id, display_name, priority, enabled, created_at) VALUES (?,?,?,?,1,strftime('%%s','now'))`,
		ueID, mcpttID, displayName, priority)
	log.Infof("MCX user created mcptt_id=%s imsi=%s", mcpttID, imsi)
	return map[string]interface{}{"ue_id": ueID, "mcptt_id": mcpttID, "display_name": displayName, "priority": priority}
}
