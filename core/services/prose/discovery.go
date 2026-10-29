// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// 5G ProSe Direct Discovery + Communication + Relay (TS 23.304 §5.2 / §5.3 / §5.4).
//
// Spec anchors (§-cites verified against local PDFs by speccheck):
//
//   - TS 23.304 §5.1     ProSe authorization / policy provisioning
//                        (the predicate behind authorizeUE).
//   - TS 23.304 §5.2     Direct Discovery — Models A ("I am here")
//                        and B ("Who is there?").
//   - TS 23.304 §5.2.3   Open Discovery — discovery without per-app
//                        permission filtering (modelled here).
//   - TS 23.304 §5.3     Direct Communication on PC5
//                        (broadcast §5.3.2, groupcast §5.3.3,
//                        unicast §5.3.4).
//   - TS 23.304 §5.4     UE-to-Network relay — Layer-3 relay UE
//                        forwards remote-UE traffic to the 5GC.
//
// TODO(TS 23.304 §5.2.4): Restricted (closed) discovery with per-app
// permission lists, ProSe Restricted Discovery Code material.
//
// TODO(TS 23.304 §5.4.4): UE-to-Network relay path keying & N3IWF
// interaction — today the relay session is just a DB row.

package prose

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("prose")

// ─── Discovery (TS 23.304 §5.2) ──────────────────────────────────

type announcement struct {
	IMSI        string                 `json:"imsi"`
	AppCode     string                 `json:"app_code"`
	Metadata    map[string]interface{} `json:"metadata"`
	AnnouncedAt float64                `json:"announced_at"`
	ExpiresAt   float64                `json:"expires_at"`
}

var (
	announcements   = map[[2]string]*announcement{}
	announcementsMu sync.Mutex
)

// Announce registers ProSe presence (TS 23.304 §5.2 Model A —
// "I am here" — the announcing UE periodically broadcasts its
// ProSe Application Code so monitoring UEs can discover it).
func Announce(imsi, appCode string, validitySec int, metadata map[string]interface{}) map[string]interface{} {
	if !authorizeUE(imsi, "discovery") {
		return map[string]interface{}{"ok": false, "error": "not authorized for discovery"}
	}
	if validitySec <= 0 {
		validitySec = 3600
	}
	now := float64(time.Now().Unix())
	entry := &announcement{IMSI: imsi, AppCode: appCode, Metadata: metadata, AnnouncedAt: now, ExpiresAt: now + float64(validitySec)}
	announcementsMu.Lock()
	announcements[[2]string{imsi, appCode}] = entry
	announcementsMu.Unlock()
	log.Infof("IMSI %s announced app_code=%s validity=%ds", imsi, appCode, validitySec)
	return map[string]interface{}{"ok": true, "announcement": entry}
}

// Withdraw removes an active announcement.
func Withdraw(imsi, appCode string) bool {
	announcementsMu.Lock()
	defer announcementsMu.Unlock()
	_, ok := announcements[[2]string{imsi, appCode}]
	delete(announcements, [2]string{imsi, appCode})
	return ok
}

// Monitor scans for nearby ProSe announcements (TS 23.304 §5.2 —
// Model B "Who is there?" — the monitoring UE collects announcements
// matching a filter).
func Monitor(imsi string, filters map[string]interface{}) map[string]interface{} {
	if !authorizeUE(imsi, "discovery") {
		return map[string]interface{}{"ok": false, "error": "not authorized", "discovered": []interface{}{}}
	}
	excludeSelf := true
	var appCodeFilter string
	if filters != nil {
		if es, ok := filters["exclude_self"].(bool); ok {
			excludeSelf = es
		}
		if ac, ok := filters["app_code"].(string); ok {
			appCodeFilter = ac
		}
	}
	now := float64(time.Now().Unix())
	announcementsMu.Lock()
	var expired [][2]string
	var discovered []map[string]interface{}
	for k, e := range announcements {
		if e.ExpiresAt < now {
			expired = append(expired, k)
			continue
		}
		if excludeSelf && e.IMSI == imsi {
			continue
		}
		if appCodeFilter != "" && e.AppCode != appCodeFilter {
			continue
		}
		discovered = append(discovered, map[string]interface{}{
			"imsi": e.IMSI, "app_code": e.AppCode, "metadata": e.Metadata, "expires_at": e.ExpiresAt,
		})
	}
	for _, k := range expired {
		delete(announcements, k)
	}
	announcementsMu.Unlock()
	return map[string]interface{}{"ok": true, "discovered": discovered}
}

// GetActiveAnnouncements returns all non-expired announcements.
func GetActiveAnnouncements() []map[string]interface{} {
	now := float64(time.Now().Unix())
	announcementsMu.Lock()
	defer announcementsMu.Unlock()
	var out []map[string]interface{}
	for _, e := range announcements {
		if e.ExpiresAt >= now {
			out = append(out, map[string]interface{}{"imsi": e.IMSI, "app_code": e.AppCode, "metadata": e.Metadata})
		}
	}
	return out
}

// ─── Communication (TS 23.304 §5.3) ──────────────────────────────

// SetupUnicastWithAuth wraps SetupUnicast with §5.1 authorization
// gating (TS 23.304 §5.3.4 — Direct Communication, unicast).
func SetupUnicastWithAuth(sourceIMSI, targetIMSI, service string) map[string]interface{} {
	if !authorizeUE(sourceIMSI, "communication") {
		return map[string]interface{}{"ok": false, "error": "source not authorized"}
	}
	if !authorizeUE(targetIMSI, "communication") {
		return map[string]interface{}{"ok": false, "error": "target not authorized"}
	}
	id, err := SetupUnicast(sourceIMSI, targetIMSI, service)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "session_id": id, "session_type": "unicast", "source_imsi": sourceIMSI, "target_imsi": targetIMSI, "status": "active"}
}

// SetupGroupcastWithAuth wraps SetupGroupcast with §5.1 authorization
// (TS 23.304 §5.3.3 — Direct Communication, groupcast).
func SetupGroupcastWithAuth(sourceIMSI, groupID, service string) map[string]interface{} {
	if !authorizeUE(sourceIMSI, "communication") {
		return map[string]interface{}{"ok": false, "error": "not authorized"}
	}
	id, err := SetupGroupcast(sourceIMSI, groupID, service)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "session_id": id, "session_type": "groupcast", "source_imsi": sourceIMSI, "group_id": groupID, "status": "active"}
}

// Release releases a PC5 session (TS 23.304 §6.4.3.3 — Layer-2
// link release over PC5).
func Release(sessionID int64) map[string]interface{} {
	if err := ReleaseSession(sessionID); err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	return map[string]interface{}{"ok": true, "session_id": sessionID, "status": "released"}
}

// ─── Relay (TS 23.304 §5.4) ──────────────────────────────────────

type relayEntry struct {
	IMSI         string  `json:"imsi"`
	ServiceCode  string  `json:"service_code"`
	Connectivity string  `json:"connectivity"`
	RegisteredAt float64 `json:"registered_at"`
	ExpiresAt    float64 `json:"expires_at"`
}

var (
	relayRegistry = map[string]*relayEntry{}
	relayMu       sync.Mutex
	relayValidity = 1800.0
)

// RegisterRelay registers a UE as a UE-to-Network relay
// (TS 23.304 §5.4 — Layer-3 5G ProSe relay UE).
func RegisterRelay(imsi, serviceCode, connectivity string) map[string]interface{} {
	if !authorizeUE(imsi, "relay") {
		return map[string]interface{}{"ok": false, "error": "not authorized for relay"}
	}
	if connectivity == "" {
		connectivity = "5gc"
	}
	now := float64(time.Now().Unix())
	entry := &relayEntry{IMSI: imsi, ServiceCode: serviceCode, Connectivity: connectivity, RegisteredAt: now, ExpiresAt: now + relayValidity}
	relayMu.Lock()
	relayRegistry[imsi] = entry
	relayMu.Unlock()
	return map[string]interface{}{"ok": true, "relay": entry}
}

// DiscoverRelays finds available relay UEs nearby (TS 23.304 §5.4 —
// the remote UE discovers relays via Direct Discovery first).
func DiscoverRelays(imsi, serviceCode string) map[string]interface{} {
	if !authorizeUE(imsi, "discovery") {
		return map[string]interface{}{"ok": false, "error": "not authorized", "relays": []interface{}{}}
	}
	now := float64(time.Now().Unix())
	relayMu.Lock()
	defer relayMu.Unlock()
	var relays []map[string]interface{}
	for ri, e := range relayRegistry {
		if e.ExpiresAt < now {
			delete(relayRegistry, ri)
			continue
		}
		if ri == imsi {
			continue
		}
		if serviceCode != "" && e.ServiceCode != serviceCode {
			continue
		}
		relays = append(relays, map[string]interface{}{
			"imsi": e.IMSI, "service_code": e.ServiceCode, "connectivity": e.Connectivity,
		})
	}
	return map[string]interface{}{"ok": true, "relays": relays}
}

// ConnectViaRelay connects a remote UE through a relay UE
// (TS 23.304 §5.4 — UE-to-Network relay session establishment).
func ConnectViaRelay(remoteIMSI, relayIMSI string) map[string]interface{} {
	if !authorizeUE(remoteIMSI, "communication") {
		return map[string]interface{}{"ok": false, "error": "remote not authorized"}
	}
	relayMu.Lock()
	relay := relayRegistry[relayIMSI]
	relayMu.Unlock()
	if relay == nil {
		return map[string]interface{}{"ok": false, "error": "relay UE not available"}
	}
	if relay.ExpiresAt < float64(time.Now().Unix()) {
		return map[string]interface{}{"ok": false, "error": "relay expired"}
	}
	res, err := engine.Exec(`INSERT INTO prose_sessions (session_type, source_imsi, target_imsi, relay_imsi, service, status) VALUES ('relay',?,?,?,?,'active')`,
		remoteIMSI, relayIMSI, relayIMSI, relay.ServiceCode)
	if err != nil {
		return map[string]interface{}{"ok": false, "error": err.Error()}
	}
	id, _ := res.LastInsertId()
	return map[string]interface{}{"ok": true, "session_id": id, "session_type": "relay", "remote_imsi": remoteIMSI, "relay_imsi": relayIMSI, "status": "active"}
}

// GetActiveRelays returns all currently-registered relay UEs.
func GetActiveRelays() []map[string]interface{} {
	now := float64(time.Now().Unix())
	relayMu.Lock()
	defer relayMu.Unlock()
	var out []map[string]interface{}
	for _, e := range relayRegistry {
		if e.ExpiresAt >= now {
			out = append(out, map[string]interface{}{
				"imsi": e.IMSI, "service_code": e.ServiceCode, "connectivity": e.Connectivity,
			})
		}
	}
	return out
}

// ─── Authorization (TS 23.304 §5.1) ──────────────────────────────

// authorizeUE checks whether a UE is authorized for a specific
// 5G ProSe service. The PCF policy (modelled by prose_ue_config)
// gates discovery / communication / relay independently, per the
// per-feature flags described in TS 23.304 §5.1.
func authorizeUE(imsi, service string) bool {
	cfg, err := GetUEConfig(imsi)
	if err != nil || cfg == nil {
		return false
	}
	if cfg.Authorized == 0 {
		return false
	}
	switch service {
	case "discovery":
		return cfg.DiscoveryEnabled != 0
	case "communication":
		return cfg.CommunicationEnabled != 0
	case "relay":
		return cfg.RelayEnabled != 0 && cfg.RelayCapable != 0
	}
	return false
}

// CheckAuthorization returns the full per-feature authorization
// state for a UE (TS 23.304 §5.1 surface).
func CheckAuthorization(imsi string) map[string]interface{} {
	cfg, err := GetUEConfig(imsi)
	if err != nil || cfg == nil {
		return nil
	}
	return map[string]interface{}{
		"imsi": imsi, "authorized": cfg.Authorized != 0,
		"discovery":     cfg.Authorized != 0 && cfg.DiscoveryEnabled != 0,
		"communication": cfg.Authorized != 0 && cfg.CommunicationEnabled != 0,
		"relay":         cfg.Authorized != 0 && cfg.RelayEnabled != 0 && cfg.RelayCapable != 0,
		"relay_capable": cfg.RelayCapable != 0,
	}
}

// GetStats returns aggregate ProSe statistics for the OAM panel.
func GetStats() map[string]interface{} {
	var apps, configs, authorized, activeSess, totalSess, unicast, groupcast, relay, filters int
	row := engine.QueryRow(`SELECT COUNT(*) FROM prose_apps`)
	row.Scan(&apps)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_ue_config`)
	row.Scan(&configs)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_ue_config WHERE authorized=1`)
	row.Scan(&authorized)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_sessions WHERE status='active'`)
	row.Scan(&activeSess)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_sessions`)
	row.Scan(&totalSess)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_sessions WHERE session_type='unicast' AND status='active'`)
	row.Scan(&unicast)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_sessions WHERE session_type='groupcast' AND status='active'`)
	row.Scan(&groupcast)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_sessions WHERE session_type='relay' AND status='active'`)
	row.Scan(&relay)
	row = engine.QueryRow(`SELECT COUNT(*) FROM prose_discovery_filters`)
	row.Scan(&filters)
	return map[string]interface{}{
		"apps": apps, "ue_configs": configs, "authorized_ues": authorized,
		"active_sessions": activeSess, "total_sessions": totalSess,
		"unicast_sessions": unicast, "groupcast_sessions": groupcast,
		"relay_sessions": relay, "discovery_filters": filters,
	}
}
