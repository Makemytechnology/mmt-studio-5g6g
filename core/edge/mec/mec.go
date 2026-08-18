// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mec — Multi-access Edge Computing orchestrator. In-memory
// registry of edge sites + edge applications + traffic-steering rules.
//
// 5GC anchors:
//   - Edge sites map to LADN service areas: TS 23.501 §5.6.5 — "A LADN
//     service area is a set of Tracking Areas. LADN is a service
//     provided by the serving PLMN…". Site.TAIs is exactly that set.
//   - Edge apps reachable by FQDN go through the EASDF: TS 23.548 §5.1
//     EASDF function, §6.2.3.2.2 EAS Discovery Procedure with EASDF.
//     FindAppByFQDN models the EASDF lookup output.
//   - Traffic-steering rules implement AF traffic-routing influence:
//     TS 23.502 §4.3.6 — Application Function influence on traffic
//     routing, service-function chaining and handling of payload.
//
// EAS lifecycle (deploy/undeploy/monitor) is anchored to the TS 23.558
// EDGEAPP architecture: dynamic EAS instantiation triggering
// (TS 23.558 §8.12) and EAS registration (§8.4.3) feed our local
// AppInstance map. The orchestrator is the request originator; the
// EES is the spec's authoritative endpoint.
package mec

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// ---- Domain types ----

// Site represents an edge computing site — i.e. a LADN service area
// (TS 23.501 §5.6.5: "A LADN service area is a set of Tracking Areas")
// paired with its local data-network attach point (LocalDNIP /
// LocalDNCIDR) for the PSA-UPF terminating PDU sessions to that DN.
type Site struct {
	SiteID      string   `json:"site_id"`
	Name        string   `json:"name"`
	TAIs        []string `json:"tais"`
	LocalDNIP   string   `json:"local_dn_ip"`
	LocalDNCIDR string   `json:"local_dn_cidr"`
	Capacity    int      `json:"capacity"`
	Status      string   `json:"status"` // active, maintenance, offline
	CreatedAt   float64  `json:"created_at"`
}

// App represents an Edge Application Server definition.
type App struct {
	AppID    string                 `json:"app_id"`
	Name     string                 `json:"name"`
	FQDN     string                 `json:"fqdn"`
	DNN      string                 `json:"dnn"`
	IPFilter string                 `json:"ip_filter,omitempty"`
	Port     int                    `json:"port,omitempty"`
	Protocol string                 `json:"protocol"`
	Priority int                    `json:"priority"`
	Instances map[string]*AppInstance `json:"instances,omitempty"`
	CreatedAt float64               `json:"created_at"`
}

// AppInstance is a deployed EAS instance at a specific site.
type AppInstance struct {
	SiteID         string  `json:"site_id"`
	AppIP          string  `json:"app_ip"`
	AppPort        int     `json:"app_port"`
	Status         string  `json:"status"` // running, stopped, error
	ActiveSessions int     `json:"active_sessions"`
	DeployedAt     float64 `json:"deployed_at"`
}

// TrafficRule — AF traffic-routing influence rule (TS 23.502 §4.3.6).
// One rule binds an (AppID, SiteID, DNN) tuple to a steering target so
// the SMF programs the local PSA-UPF (or ULCL/BP) accordingly. The
// rule structure here covers the AF-request fields the SMF consumes;
// the authoritative AF-influence procedure (NEF Nnef_TrafficInfluence,
// PCF Npcf_PolicyAuthorization) lives outside this in-memory store.
type TrafficRule struct {
	RuleID     string `json:"rule_id"`
	AppID      string `json:"app_id"`
	SiteID     string `json:"site_id"`
	DNN        string `json:"dnn"`
	TargetIP   string `json:"target_ip,omitempty"`
	TargetFQDN string `json:"target_fqdn,omitempty"`
	TargetPort int    `json:"target_port,omitempty"`
	Priority   int    `json:"priority"`
	CreatedAt  float64 `json:"created_at"`
}

// ---- Registry (in-memory, thread-safe) ----

var (
	mu           sync.Mutex
	sites        = map[string]*Site{}
	apps         = map[string]*App{}
	trafficRules = map[string]*TrafficRule{}
	nextSiteSeq  int
	nextAppSeq   int
	nextRuleSeq  int
)

// ---- GUI panel API ----

// List returns all apps (preserves original stub API).
func List() ([]map[string]any, error) {
	appList := ListApps()
	var out []map[string]any
	for _, a := range appList {
		out = append(out, map[string]any{
			"app_id": a.AppID, "name": a.Name, "dnn": a.DNN, "status": "active",
		})
	}
	return out, nil
}

// Status returns a summary for the GUI panel.
func Status() map[string]any {
	stats := GetStats()
	return stats
}

// ---- Site CRUD ----

// AddSite creates a new edge site.
func AddSite(name string, tais []string, localDNIP, localDNCIDR string, capacity int) *Site {
	if capacity <= 0 {
		capacity = 100
	}
	mu.Lock()
	defer mu.Unlock()
	nextSiteSeq++
	siteID := fmt.Sprintf("edge-%03d", nextSiteSeq)
	s := &Site{
		SiteID:      siteID,
		Name:        name,
		TAIs:        tais,
		LocalDNIP:   localDNIP,
		LocalDNCIDR: localDNCIDR,
		Capacity:    capacity,
		Status:      "active",
		CreatedAt:   float64(time.Now().Unix()),
	}
	sites[siteID] = s
	return s
}

// GetSite returns a site by ID.
func GetSite(siteID string) *Site {
	mu.Lock()
	defer mu.Unlock()
	return sites[siteID]
}

// ListSites returns all edge sites.
func ListSites() []*Site {
	mu.Lock()
	defer mu.Unlock()
	out := make([]*Site, 0, len(sites))
	for _, s := range sites {
		out = append(out, s)
	}
	return out
}

// RemoveSite deletes an edge site.
func RemoveSite(siteID string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := sites[siteID]; ok {
		delete(sites, siteID)
		return true
	}
	return false
}

// FindSiteByTAI returns the edge site whose LADN service area
// (TS 23.501 §5.6.5) covers the given TAI. Used by the SMF to pick a
// local PSA-UPF for a UE attached in that TAI.
func FindSiteByTAI(tai string) *Site {
	mu.Lock()
	defer mu.Unlock()
	for _, s := range sites {
		if s.Status != "active" {
			continue
		}
		for _, t := range s.TAIs {
			if t == tai {
				return s
			}
		}
	}
	return nil
}

// ---- App CRUD ----

// AddApp creates a new edge application definition.
func AddApp(name, fqdn, dnn, ipFilter string, port int, protocol string) *App {
	if protocol == "" {
		protocol = "tcp"
	}
	mu.Lock()
	defer mu.Unlock()
	nextAppSeq++
	appID := fmt.Sprintf("eas-%03d", nextAppSeq)
	a := &App{
		AppID:     appID,
		Name:      name,
		FQDN:      fqdn,
		DNN:       dnn,
		IPFilter:  ipFilter,
		Port:      port,
		Protocol:  protocol,
		Priority:  10,
		Instances: map[string]*AppInstance{},
		CreatedAt: float64(time.Now().Unix()),
	}
	apps[appID] = a
	return a
}

// GetApp returns an app by ID.
func GetApp(appID string) *App {
	mu.Lock()
	defer mu.Unlock()
	return apps[appID]
}

// ListApps returns all edge applications.
func ListApps() []*App {
	mu.Lock()
	defer mu.Unlock()
	out := make([]*App, 0, len(apps))
	for _, a := range apps {
		out = append(out, a)
	}
	return out
}

// RemoveApp deletes an edge application.
func RemoveApp(appID string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := apps[appID]; ok {
		delete(apps, appID)
		return true
	}
	return false
}

// ---- Deployment ----

// DeployInstance deploys an EAS instance at a specific site —
// orchestrator-side persistence of the "Dynamic EAS instantiation
// triggering" outcome described in TS 23.558 §8.12. Once the
// instance is running, it must also be registered with the EES via
// the EAS registration procedure (TS 23.558 §8.4.3.2.2) before EAS
// Discovery (§8.5) can return it.
func DeployInstance(appID, siteID, appIP string, appPort int) (*AppInstance, error) {
	mu.Lock()
	defer mu.Unlock()
	app := apps[appID]
	site := sites[siteID]
	if app == nil {
		return nil, fmt.Errorf("app %s not found", appID)
	}
	if site == nil {
		return nil, fmt.Errorf("site %s not found", siteID)
	}
	inst := &AppInstance{
		SiteID:     siteID,
		AppIP:      appIP,
		AppPort:    appPort,
		Status:     "running",
		DeployedAt: float64(time.Now().Unix()),
	}
	app.Instances[siteID] = inst
	return inst, nil
}

// UndeployInstance removes an EAS instance from a site.
func UndeployInstance(appID, siteID string) bool {
	mu.Lock()
	defer mu.Unlock()
	app := apps[appID]
	if app == nil {
		return false
	}
	if _, ok := app.Instances[siteID]; ok {
		delete(app.Instances, siteID)
		return true
	}
	return false
}

// FindNearestInstance returns the nearest running instance for a TAI.
func FindNearestInstance(appID, tai string) *AppInstance {
	mu.Lock()
	defer mu.Unlock()
	app := apps[appID]
	if app == nil {
		return nil
	}
	for siteID, inst := range app.Instances {
		site := sites[siteID]
		if site == nil || site.Status != "active" || inst.Status != "running" {
			continue
		}
		for _, t := range site.TAIs {
			if t == tai {
				return inst
			}
		}
	}
	return nil
}

// FindAppByFQDN looks up an edge app by FQDN. Models the lookup the
// EASDF performs while building the DNS answer per
// TS 23.548 §6.2.3.2.2 (EAS Discovery Procedure with EASDF).
func FindAppByFQDN(fqdn string) *App {
	mu.Lock()
	defer mu.Unlock()
	lower := strings.ToLower(fqdn)
	for _, a := range apps {
		if strings.ToLower(a.FQDN) == lower {
			return a
		}
	}
	return nil
}

// ---- Traffic Rules ----

// AddTrafficRule creates an AF traffic-routing influence rule
// (TS 23.502 §4.3.6). The rule is consumed by the SMF when applying
// AF-influence: it programs the local PSA-UPF / ULCL to steer matching
// traffic to the indicated edge target.
func AddTrafficRule(appID, siteID, dnn, targetIP, targetFQDN string, targetPort, priority int) *TrafficRule {
	mu.Lock()
	defer mu.Unlock()
	nextRuleSeq++
	ruleID := fmt.Sprintf("rule-%03d", nextRuleSeq)
	r := &TrafficRule{
		RuleID:     ruleID,
		AppID:      appID,
		SiteID:     siteID,
		DNN:        dnn,
		TargetIP:   targetIP,
		TargetFQDN: targetFQDN,
		TargetPort: targetPort,
		Priority:   priority,
		CreatedAt:  float64(time.Now().Unix()),
	}
	trafficRules[ruleID] = r
	return r
}

// ListTrafficRules returns all traffic rules.
func ListTrafficRules() []*TrafficRule {
	mu.Lock()
	defer mu.Unlock()
	out := make([]*TrafficRule, 0, len(trafficRules))
	for _, r := range trafficRules {
		out = append(out, r)
	}
	return out
}

// DeleteTrafficRule removes a traffic rule.
func DeleteTrafficRule(ruleID string) bool {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := trafficRules[ruleID]; ok {
		delete(trafficRules, ruleID)
		return true
	}
	return false
}

// RulesForDNN returns every active traffic rule whose DNN matches.
// Used by the SMF (TS 23.502 §4.3.6) to decide whether AF-influence
// should re-target a PDU session to an edge site, and by the OAM
// panel to render which currently-active sessions are subject to
// edge steering.
func RulesForDNN(dnn string) []*TrafficRule {
	mu.Lock()
	defer mu.Unlock()
	out := []*TrafficRule{}
	for _, r := range trafficRules {
		if r.DNN == "" || r.DNN == dnn {
			out = append(out, r)
		}
	}
	return out
}

// RulesForApp returns every traffic rule for a specific app id,
// optionally narrowed by DNN. Used by the SMF when binding a PDU
// session opening on a known edge app FQDN.
func RulesForApp(appID, dnn string) []*TrafficRule {
	mu.Lock()
	defer mu.Unlock()
	out := []*TrafficRule{}
	for _, r := range trafficRules {
		if r.AppID != appID {
			continue
		}
		if dnn != "" && r.DNN != "" && r.DNN != dnn {
			continue
		}
		out = append(out, r)
	}
	return out
}

// ULCLState records the install state of a TS 23.501 §5.6.4
// Uplink Classifier (or Branching Point) at the UPF for a single
// (PDU session, traffic-rule) pair. The SMF establish path
// (nf/smf/session/establish.go) updates this when it fires the
// PFCP Session Modification with the new PDR/FAR. Operator OAM
// reads it to confirm the AF-influence reached the dataplane.
type ULCLState struct {
	IMSI         string `json:"imsi"`
	PDUSessionID int    `json:"pdu_session_id"`
	RuleID       string `json:"rule_id"`
	PDRID        uint16 `json:"pdr_id"`
	FARID        uint32 `json:"far_id"`
	Installed    bool   `json:"installed"`
	Error        string `json:"error,omitempty"`
	InstalledAt  string `json:"installed_at,omitempty"`
}

type ulclKey struct {
	imsi       string
	pduSession int
	ruleID     string
}

var (
	ulclMu    sync.Mutex
	ulclState = map[ulclKey]*ULCLState{}
)

// RecordULCLInstall persists the result of an ULCL/BP install
// attempt. err==nil → Installed=true; non-nil err is captured in
// Error. Idempotent on re-install (the row is overwritten).
func RecordULCLInstall(imsi string, pduSessionID int, ruleID string,
	pdrID uint16, farID uint32, err error, installedAt string) {
	ulclMu.Lock()
	defer ulclMu.Unlock()
	st := &ULCLState{
		IMSI:         imsi,
		PDUSessionID: pduSessionID,
		RuleID:       ruleID,
		PDRID:        pdrID,
		FARID:        farID,
		Installed:    err == nil,
		InstalledAt:  installedAt,
	}
	if err != nil {
		st.Error = err.Error()
	}
	ulclState[ulclKey{imsi, pduSessionID, ruleID}] = st
}

// ULCLForSession returns every install record for a single PDU
// session (one per matched traffic rule). Used by /api/mec/active-
// sessions to render whether each AF-influence rule actually
// reached the UPF.
func ULCLForSession(imsi string, pduSessionID int) []*ULCLState {
	ulclMu.Lock()
	defer ulclMu.Unlock()
	out := []*ULCLState{}
	for k, v := range ulclState {
		if k.imsi == imsi && k.pduSession == pduSessionID {
			cp := *v
			out = append(out, &cp)
		}
	}
	return out
}

// ClearULCLForSession drops every install record for a session —
// called by the SMF release path so a re-establishment doesn't
// inherit stale state.
func ClearULCLForSession(imsi string, pduSessionID int) {
	ulclMu.Lock()
	defer ulclMu.Unlock()
	for k := range ulclState {
		if k.imsi == imsi && k.pduSession == pduSessionID {
			delete(ulclState, k)
		}
	}
}

// ---- Stats ----

// GetStats returns overall MEC status.
func GetStats() map[string]any {
	mu.Lock()
	defer mu.Unlock()
	totalInstances := 0
	runningInstances := 0
	for _, a := range apps {
		totalInstances += len(a.Instances)
		for _, i := range a.Instances {
			if i.Status == "running" {
				runningInstances++
			}
		}
	}
	activeSites := 0
	for _, s := range sites {
		if s.Status == "active" {
			activeSites++
		}
	}
	return map[string]any{
		"total_sites":       len(sites),
		"active_sites":      activeSites,
		"total_apps":        len(apps),
		"total_instances":   totalInstances,
		"running_instances": runningInstances,
		"traffic_rules":     len(trafficRules),
	}
}
