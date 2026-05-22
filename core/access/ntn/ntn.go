// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ntn — Non-Terrestrial Network support for NR satellite access.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 22.261 §6.3.2.3   Service requirements for satellite access
//                          as one of the multiple access technologies
//                          (the umbrella service-side requirement).
//   - TS 23.501 §5.4.10    Identification + restriction of using NR
//                          satellite access (RAT-type gating).
//   - TS 23.501 §5.4.11    Integrating NR satellite access into 5GS
//                          — the umbrella architecture clause for
//                          satellite-served UEs.
//   - TS 23.501 §5.4.11.4  Verification of UE location — drives the
//                          GeographicTAI / TAIManager surface here.
//   - TS 23.501 §5.4.11.7  Tracking Area handling for NR satellite
//                          access (geographic TAI mapping).
//   - TS 23.501 §5.4.11.9  N2 interface and connection management
//                          for regenerative satellite payload
//                          — the normative clause for the
//                          RegenerativePayload surface.
//   - TS 23.501 §5.4.13    Discontinuous network coverage for
//                          satellite access (LEO pass gaps); the
//                          CoverageManager / DL buffering here is
//                          the operator-side projection of this.
//   - TS 23.501 §5.4.14    UE-Satellite-UE communication.
//   - TS 23.501 §5.43      5G Satellite Backhaul.
//   - TS 38.300 §16.14     NR support for non-terrestrial networks
//                          (NG-RAN-side overall architecture).
//   - TS 38.821 §4.1       NTN overview (transparent / regenerative
//                          payload reference scenarios).
//   - TS 38.821 §5.1       Transparent satellite-based NG-RAN.
//   - TS 38.821 §5.2       Regenerative satellite-based NG-RAN.
//   - TS 38.821 §6.3       UL timing advance / RACH — drives the
//                          ComputePropagationDelay model here.
//   - TS 38.821 §6.2.5     Impact of feeder link switch — drives the
//                          FeederLinkManager surface.
//
// Default NAS-timer extension policy (4× max-RTT guard band) is
// operator policy informed by TS 38.821 §6.3 latency analysis but
// not §-mandated; it sits in GetAdjustedNASTimers as a heuristic.
//
// TODO TS 38.821 (S&F) — store-and-forward operation is not yet a
//                        normative clause in v16.2 of the TR. The
//                        StoreAndForward surface here is operator
//                        policy pending a Rel-19+ landing.
// TODO TS 38.821 (ISL) — Inter-Satellite Links are referenced in
//                        TR §5.x architecture variants but lack a
//                        single-clause normative anchor in v16.2.
// TODO TS 38.331 §6.3.x — NTN-specific RRC IEs (epoch time,
//                         ephemeris parameter set) are not yet
//                         decoded; the SatelliteConfig holds local
//                         orbital parameters only.
// TODO TS 23.502 §4.x   — Satellite-aware Registration / Service
//                         Request signalling is not yet wired into
//                         the AMF; the TAIManager is consulted in
//                         isolation here.
package ntn

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

const (
	EarthRadiusKM      = 6371.0
	SpeedOfLightKMS    = 299792.458
	GravitationalParam = 398600.4418 // km^3/s^2
)

// ---- Satellite Config (in-memory constellation) ----

// SatelliteConfig holds configuration for a single satellite or HAPS.
type SatelliteConfig struct {
	SatID          string  `json:"sat_id"`
	Name           string  `json:"name"`
	OrbitType      string  `json:"orbit_type"` // LEO|MEO|GEO|HAPS
	AltitudeKM     float64 `json:"altitude_km"`
	InclinationDeg float64 `json:"inclination_deg"`
	LongitudeDeg   float64 `json:"longitude_deg"`
	BeamCount      int     `json:"beam_count"`
	BeamDiameterKM float64 `json:"beam_diameter_km"`
	MinRTTMS       float64 `json:"min_rtt_ms"`
	MaxRTTMS       float64 `json:"max_rtt_ms"`
}

func NewSatelliteConfig(satID, name, orbitType string, altKM, inclDeg, lonDeg float64,
	beamCount int, beamDiamKM float64) *SatelliteConfig {
	s := &SatelliteConfig{
		SatID: satID, Name: name, OrbitType: orbitType, AltitudeKM: altKM,
		InclinationDeg: inclDeg, LongitudeDeg: lonDeg,
		BeamCount: beamCount, BeamDiameterKM: beamDiamKM,
	}
	oneWayMS := (altKM / SpeedOfLightKMS) * 1000
	s.MinRTTMS = math.Round(2*oneWayMS*100) / 100
	elRad := math.Pi * 10 / 180
	R := EarthRadiusKM
	h := altKM
	slant := math.Sqrt(math.Pow(R+h, 2)-math.Pow(R*math.Cos(elRad), 2)) - R*math.Sin(elRad)
	maxOneWay := (slant / SpeedOfLightKMS) * 1000
	s.MaxRTTMS = math.Round((2*maxOneWay+s.MinRTTMS)*100) / 100
	return s
}

// GroundStationConfig holds ground station configuration.
type GroundStationConfig struct {
	GSID           string  `json:"gs_id"`
	Name           string  `json:"name"`
	Latitude       float64 `json:"latitude"`
	Longitude      float64 `json:"longitude"`
	ConnectedGnbIP string  `json:"connected_gnb_ip,omitempty"`
	Active         bool    `json:"active"`
}

// Constellation manages the satellite constellation and ground stations.
type Constellation struct {
	mu             sync.Mutex
	satellites     map[string]*SatelliteConfig
	groundStations map[string]*GroundStationConfig
}

// NewConstellation creates a new empty constellation.
func NewConstellation() *Constellation {
	return &Constellation{
		satellites:     make(map[string]*SatelliteConfig),
		groundStations: make(map[string]*GroundStationConfig),
	}
}

func (c *Constellation) AddSatellite(s *SatelliteConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.satellites[s.SatID] = s
	logger.Get("ntn").Infof("Satellite added: %s (%s, %d km, RTT %.1f-%.1fms)",
		s.Name, s.OrbitType, int(s.AltitudeKM), s.MinRTTMS, s.MaxRTTMS)
}

func (c *Constellation) RemoveSatellite(satID string)            { c.mu.Lock(); delete(c.satellites, satID); c.mu.Unlock() }
func (c *Constellation) GetSatellite(satID string) *SatelliteConfig { c.mu.Lock(); defer c.mu.Unlock(); return c.satellites[satID] }

func (c *Constellation) GetAllSatellites() []*SatelliteConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*SatelliteConfig, 0, len(c.satellites))
	for _, s := range c.satellites { out = append(out, s) }
	return out
}

func (c *Constellation) AddGroundStation(gs *GroundStationConfig) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.groundStations[gs.GSID] = gs
	logger.Get("ntn").Infof("Ground station added: %s (%.4f, %.4f)", gs.Name, gs.Latitude, gs.Longitude)
}

func (c *Constellation) GetGroundStation(gsID string) *GroundStationConfig { c.mu.Lock(); defer c.mu.Unlock(); return c.groundStations[gsID] }

func (c *Constellation) GetAllGroundStations() []*GroundStationConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]*GroundStationConfig, 0, len(c.groundStations))
	for _, gs := range c.groundStations { out = append(out, gs) }
	return out
}

func (c *Constellation) GetGroundStationForGnb(gnbIP string) *GroundStationConfig {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, gs := range c.groundStations {
		if gs.ConnectedGnbIP == gnbIP { return gs }
	}
	return nil
}

func (c *Constellation) LoadDefaults() {
	c.AddSatellite(NewSatelliteConfig("LEO-1", "SAT-LEO-1", "LEO", 550, 53, 0, 8, 50))
	c.AddSatellite(NewSatelliteConfig("LEO-2", "SAT-LEO-2", "LEO", 550, 53, 0, 8, 50))
	c.AddSatellite(NewSatelliteConfig("GEO-1", "SAT-GEO-1", "GEO", 35786, 0, 76.5, 16, 500))
	c.AddGroundStation(&GroundStationConfig{GSID: "GS-1", Name: "Gateway-Primary", Latitude: 12.9716, Longitude: 77.5946, Active: true})
}

// DefaultConstellation is the process-wide NTN constellation.
var DefaultConstellation = NewConstellation()

// ---- Ephemeris ----

// SatellitePosition represents a satellite position at a point in time.
type SatellitePosition struct {
	Latitude   float64 `json:"latitude"`
	Longitude  float64 `json:"longitude"`
	AltitudeKM float64 `json:"altitude_km"`
	Timestamp  float64 `json:"timestamp"`
}

// GetSatellitePosition computes satellite position at a given time.
func GetSatellitePosition(sat *SatelliteConfig, atTime float64) *SatellitePosition {
	if atTime == 0 { atTime = float64(time.Now().Unix()) }
	if sat.OrbitType == "GEO" { return &SatellitePosition{0, sat.LongitudeDeg, sat.AltitudeKM, atTime} }
	if sat.OrbitType == "HAPS" { return &SatellitePosition{sat.InclinationDeg, sat.LongitudeDeg, sat.AltitudeKM, atTime} }
	r := EarthRadiusKM + sat.AltitudeKM
	period := 2 * math.Pi * math.Sqrt(math.Pow(r, 3)/GravitationalParam)
	fraction := math.Mod(atTime, period) / period
	angle := 2 * math.Pi * fraction
	inclRad := math.Pi * sat.InclinationDeg / 180
	lat := 180 / math.Pi * math.Asin(math.Sin(inclRad)*math.Sin(angle))
	earthRotDeg := 360.0 / 86400.0
	lonOffset := fraction * 360.0
	earthOffset := math.Mod(atTime, 86400) * earthRotDeg
	lon := math.Mod(sat.LongitudeDeg+lonOffset-earthOffset, 360)
	if lon > 180 { lon -= 360 }
	return &SatellitePosition{math.Round(lat*10000) / 10000, math.Round(lon*10000) / 10000, sat.AltitudeKM, atTime}
}

// ComputeVisibility checks if a satellite is visible from a UE location.
func ComputeVisibility(pos *SatellitePosition, ueLat, ueLon float64, minElevDeg float64) (bool, float64, float64) {
	lat1 := math.Pi * ueLat / 180
	lon1 := math.Pi * ueLon / 180
	lat2 := math.Pi * pos.Latitude / 180
	lon2 := math.Pi * pos.Longitude / 180
	deltaSigma := math.Acos(math.Sin(lat1)*math.Sin(lat2) + math.Cos(lat1)*math.Cos(lat2)*math.Cos(lon2-lon1))
	R := EarthRadiusKM
	h := pos.AltitudeKM
	rho := R / (R + h)
	elevRad := math.Atan2(math.Cos(deltaSigma)-rho, math.Sin(deltaSigma))
	elevDeg := 180 / math.Pi * elevRad
	slant := math.Inf(1)
	if math.Cos(elevRad) > 0 { slant = R * math.Sin(deltaSigma) / math.Cos(elevRad) }
	return elevDeg >= minElevDeg, math.Round(elevDeg*100) / 100, math.Round(slant*100) / 100
}

// ---- Coverage Manager ----
//
// TS 23.501 §5.4.13 — Discontinuous network coverage for satellite
// access. LEO coverage gaps are intrinsic; the CoverageManager
// buffers DL packets while the serving satellite is out of view
// and flushes them when coverage resumes (the §5.4.13.4 paging /
// §5.4.13.2 coverage availability provisioning analogue at the
// dataplane).

// CoverageManager manages NTN coverage analysis and DL buffering.
type CoverageManager struct {
	mu            sync.Mutex
	dlBuffer      map[string][]dlEntry
	maxBufferPerUE int
	bufferTTL     float64
}

type dlEntry struct {
	Timestamp float64
	Data      interface{}
}

func NewCoverageManager() *CoverageManager {
	return &CoverageManager{dlBuffer: make(map[string][]dlEntry), maxBufferPerUE: 100, bufferTTL: 3600}
}

func (m *CoverageManager) CheckCoverage(constellation *Constellation, ueLat, ueLon, minElev float64) map[string]interface{} {
	sats := constellation.GetAllSatellites()
	var bestSat *SatelliteConfig
	var bestPos *SatellitePosition
	bestElev := -90.0
	var bestSlant float64
	visibleCount := 0
	now := float64(time.Now().Unix())
	for _, sat := range sats {
		pos := GetSatellitePosition(sat, now)
		vis, elev, slant := ComputeVisibility(pos, ueLat, ueLon, minElev)
		if vis {
			visibleCount++
			if elev > bestElev { bestElev = elev; bestSat = sat; bestPos = pos; bestSlant = slant }
		}
	}
	if bestSat != nil {
		return map[string]interface{}{
			"covered": true, "serving_satellite": bestSat.SatID, "satellite_name": bestSat.Name,
			"elevation_deg": bestElev, "slant_range_km": bestSlant,
			"visible_satellites": visibleCount, "satellite_position": bestPos,
		}
	}
	return map[string]interface{}{"covered": false, "serving_satellite": nil, "visible_satellites": 0, "message": "No satellite coverage at this location"}
}

func (m *CoverageManager) BufferDLPacket(imsi string, data interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	buf := m.dlBuffer[imsi]
	if len(buf) >= m.maxBufferPerUE { buf = buf[1:] }
	m.dlBuffer[imsi] = append(buf, dlEntry{float64(time.Now().Unix()), data})
}

func (m *CoverageManager) FlushDLBuffer(imsi string) []dlEntry {
	m.mu.Lock()
	buf := m.dlBuffer[imsi]
	delete(m.dlBuffer, imsi)
	m.mu.Unlock()
	now := float64(time.Now().Unix())
	var valid []dlEntry
	for _, e := range buf {
		if now-e.Timestamp < m.bufferTTL { valid = append(valid, e) }
	}
	return valid
}

func (m *CoverageManager) GetBufferStatus(imsi string) map[string]interface{} {
	m.mu.Lock()
	defer m.mu.Unlock()
	if imsi != "" {
		return map[string]interface{}{"imsi": imsi, "buffered_packets": len(m.dlBuffer[imsi])}
	}
	total := 0
	for _, b := range m.dlBuffer { total += len(b) }
	return map[string]interface{}{"total_ues_buffered": len(m.dlBuffer), "total_packets": total}
}

// DefaultCoverageMgr is the process-wide coverage manager.
var DefaultCoverageMgr = NewCoverageManager()

// ---- Feeder Link Manager ----
//
// TS 38.821 §6.2.5 — Impact of feeder link switch. As a LEO satellite
// transits between gateways, the feeder link must hand over from one
// gateway / gNB IP to another. FeederLinkManager records the active
// (sat → gNB) binding and logs each switch event so the GUI / OAM
// layer can trace re-bindings.

type FeederLinkManager struct {
	mu            sync.Mutex
	activeLinks   map[string]map[string]interface{}
	switchHistory []map[string]interface{}
}

func NewFeederLinkManager() *FeederLinkManager {
	return &FeederLinkManager{activeLinks: make(map[string]map[string]interface{})}
}

func (f *FeederLinkManager) RegisterFeederLink(satID, gsID, gnbIP string) map[string]interface{} {
	f.mu.Lock()
	defer f.mu.Unlock()
	old := f.activeLinks[satID]
	f.activeLinks[satID] = map[string]interface{}{"gs_id": gsID, "gnb_ip": gnbIP, "since": time.Now().Unix()}
	if old != nil && old["gs_id"] != gsID {
		f.switchHistory = append(f.switchHistory, map[string]interface{}{
			"sat_id": satID, "from_gs": old["gs_id"], "to_gs": gsID, "timestamp": time.Now().Unix(),
		})
		return old
	}
	return nil
}

func (f *FeederLinkManager) GetActiveLink(satID string) map[string]interface{} { f.mu.Lock(); defer f.mu.Unlock(); return f.activeLinks[satID] }
func (f *FeederLinkManager) GetGnbForSatellite(satID string) string {
	f.mu.Lock(); defer f.mu.Unlock()
	if l := f.activeLinks[satID]; l != nil { if ip, ok := l["gnb_ip"].(string); ok { return ip } }
	return ""
}

func (f *FeederLinkManager) InitiateSwitch(satID, newGSID, newGnbIP string) map[string]interface{} {
	old := f.RegisterFeederLink(satID, newGSID, newGnbIP)
	if old == nil { return map[string]interface{}{"switched": false, "reason": "No previous link or same ground station"} }
	return map[string]interface{}{"switched": true, "sat_id": satID, "from_gs": old["gs_id"], "to_gs": newGSID}
}

func (f *FeederLinkManager) GetSwitchHistory(limit int) []map[string]interface{} {
	f.mu.Lock(); defer f.mu.Unlock()
	if limit <= 0 { limit = 20 }
	start := len(f.switchHistory) - limit
	if start < 0 { start = 0 }
	return f.switchHistory[start:]
}

func (f *FeederLinkManager) GetAllActiveLinks() map[string]map[string]interface{} {
	f.mu.Lock(); defer f.mu.Unlock()
	out := make(map[string]map[string]interface{}, len(f.activeLinks))
	for k, v := range f.activeLinks { out[k] = v }
	return out
}

// DefaultFeederLinkMgr is the process-wide feeder link manager.
var DefaultFeederLinkMgr = NewFeederLinkManager()

// ---- Geographic TAI Manager ----
//
// TS 23.501 §5.4.11.7 — Tracking Area handling for NR satellite
// access. Because a satellite beam moves over the ground, the TA
// the UE observes depends on the UE's own location, not on the
// gNB. The GeographicTAI rows here map a (lat, lon, radius) area
// to a TAC value so the AMF can derive a Tracking Area Identity
// from the UE's reported location (TS 23.501 §5.4.11.4 location
// verification).

type GeographicTAI struct {
	TAIID     string  `json:"tai_id"`
	MCC       string  `json:"mcc"`
	MNC       string  `json:"mnc"`
	TAC       string  `json:"tac"`
	CenterLat float64 `json:"center_lat"`
	CenterLon float64 `json:"center_lon"`
	RadiusKM  float64 `json:"radius_km"`
}

func (t *GeographicTAI) Contains(lat, lon float64) bool { return haversineKM(t.CenterLat, t.CenterLon, lat, lon) <= t.RadiusKM }

type TAIManager struct {
	mu   sync.Mutex
	tais map[string]*GeographicTAI
}

func NewTAIManager() *TAIManager { return &TAIManager{tais: make(map[string]*GeographicTAI)} }

func (m *TAIManager) AddTAI(t *GeographicTAI) { m.mu.Lock(); m.tais[t.TAIID] = t; m.mu.Unlock() }

func (m *TAIManager) GetTAIForLocation(lat, lon float64) *GeographicTAI {
	m.mu.Lock(); defer m.mu.Unlock()
	for _, t := range m.tais { if t.Contains(lat, lon) { return t } }
	return nil
}

func (m *TAIManager) GetTAIList() []*GeographicTAI {
	m.mu.Lock(); defer m.mu.Unlock()
	out := make([]*GeographicTAI, 0, len(m.tais))
	for _, t := range m.tais { out = append(out, t) }
	return out
}

func (m *TAIManager) HasTAIChanged(oldTAC string, newLat, newLon float64) bool {
	t := m.GetTAIForLocation(newLat, newLon)
	if t == nil { return true }
	return t.TAC != oldTAC
}

func (m *TAIManager) LoadDefaults(mcc, mnc string) {
	if mcc == "" { mcc = "001" }
	if mnc == "" { mnc = "01" }
	defs := []struct{ id, tac string; lat, lon, r float64 }{
		{"NTN-TAI-1", "A001", 13.0, 77.5, 500}, {"NTN-TAI-2", "A002", 20.0, 77.5, 500},
		{"NTN-TAI-3", "A003", 28.5, 77.0, 500}, {"NTN-TAI-4", "A004", 13.0, 85.0, 500},
		{"NTN-TAI-5", "A005", 20.0, 85.0, 500},
	}
	for _, d := range defs { m.AddTAI(&GeographicTAI{d.id, mcc, mnc, d.tac, d.lat, d.lon, d.r}) }
}

// DefaultTAIMgr is the process-wide TAI manager.
var DefaultTAIMgr = NewTAIManager()

// ---- Timing ----
//
// TS 38.821 §6.3 — UL timing advance / RACH for NTN. The satellite
// service link adds 2–540 ms RTT depending on orbit (LEO vs. GEO),
// which dwarfs typical terrestrial NAS-timer windows. Two-leg
// propagation (UE → satellite → ground gateway) is modelled
// separately so the AMF can apply the right TA correction per leg.

// ComputePropagationDelay computes one-way propagation delay for the
// UE → satellite → ground-gateway path (TS 38.821 §6.3 timing model).
// When the UE position is known the service-link slant-range is used;
// otherwise the satellite altitude is taken as a worst-case proxy.
func ComputePropagationDelay(sat *SatelliteConfig, ueLat, ueLon *float64) map[string]interface{} {
	pos := GetSatellitePosition(sat, 0)
	feederMS := (sat.AltitudeKM / SpeedOfLightKMS) * 1000
	if ueLat != nil && ueLon != nil {
		vis, elevDeg, slantKM := ComputeVisibility(pos, *ueLat, *ueLon, 0)
		if !vis || elevDeg < 0 {
			return map[string]interface{}{"service_link_ms": nil, "feeder_link_ms": math.Round(feederMS*1000) / 1000, "total_one_way_ms": nil, "rtt_ms": nil, "visible": false, "elevation_deg": elevDeg}
		}
		serviceMS := (slantKM / SpeedOfLightKMS) * 1000
		total := serviceMS + feederMS
		return map[string]interface{}{"service_link_ms": math.Round(serviceMS*1000) / 1000, "feeder_link_ms": math.Round(feederMS*1000) / 1000, "total_one_way_ms": math.Round(total*1000) / 1000, "rtt_ms": math.Round(2*total*1000) / 1000, "visible": true}
	}
	total := 2 * feederMS
	return map[string]interface{}{"service_link_ms": math.Round(feederMS*1000) / 1000, "feeder_link_ms": math.Round(feederMS*1000) / 1000, "total_one_way_ms": math.Round(total/2*1000) / 1000, "rtt_ms": math.Round(total*1000) / 1000, "visible": true}
}

// GetAdjustedNASTimers returns NAS timers (T35xx) extended by a
// guard band proportional to the satellite RTT. Operator policy
// (4× max-RTT) informed by TS 38.821 §6.3 latency analysis; not
// §-mandated. T3512 (periodic registration) is doubled for GEO
// satellites because of the much-longer service-link path.
func GetAdjustedNASTimers(sat *SatelliteConfig) map[string]interface{} {
	maxRTTS := sat.MaxRTTMS / 1000
	guard := 4 * maxRTTS
	base := map[string]float64{"T3510": 15, "T3511": 10, "T3512": 3240, "T3517": 15, "T3521": 15}
	adjusted := make(map[string]interface{})
	for timer, b := range base {
		if timer == "T3512" {
			if sat.OrbitType == "GEO" { adjusted[timer] = b * 2 } else { adjusted[timer] = b }
		} else {
			adjusted[timer] = math.Round((b+guard)*10) / 10
		}
	}
	return adjusted
}

// ---- Phase-2 DB features ----

// RegenerativePayload configures a satellite for regenerative onboard
// NF processing. Architecture anchor: TS 38.821 §5.2 (Regenerative
// satellite-based NG-RAN architectures); core-network projection:
// TS 23.501 §5.4.11.9 (N2 interface and connection management for
// regenerative satellite payload).
func RegenerativePayload(satID string, config map[string]interface{}) (map[string]interface{}, error) {
	satID = strings.TrimSpace(satID)
	if satID == "" { return nil, fmt.Errorf("sat_id is required") }
	onboardNFs, _ := json.Marshal(config["onboard_nfs"])
	capacity := intOr(config, "processing_capacity", 0)
	memory := intOr(config, "memory_mb", 0)
	status := strOr(config, "status", "standby")

	db, err := engine.Open()
	if err != nil { return nil, err }
	var existing int
	_ = db.QueryRow("SELECT id FROM ntn_regenerative_config WHERE sat_id=?", satID).Scan(&existing)
	if existing > 0 {
		_, err = db.Exec("UPDATE ntn_regenerative_config SET onboard_nfs=?, processing_capacity=?, memory_mb=?, status=? WHERE sat_id=?",
			string(onboardNFs), capacity, memory, status, satID)
	} else {
		_, err = db.Exec("INSERT INTO ntn_regenerative_config (sat_id, onboard_nfs, processing_capacity, memory_mb, status) VALUES (?,?,?,?,?)",
			satID, string(onboardNFs), capacity, memory, status)
	}
	if err != nil { return nil, err }
	return GetRegenerativeConfig(satID)
}

func GetRegenerativeConfig(satID string) (map[string]interface{}, error) {
	return queryRowMap("SELECT * FROM ntn_regenerative_config WHERE sat_id=?", satID)
}

func ListRegenerativeConfigs() ([]map[string]interface{}, error) {
	return queryRowsMap("SELECT * FROM ntn_regenerative_config ORDER BY id")
}

func DeleteRegenerativeConfig(satID string) error {
	_, err := engine.Exec("DELETE FROM ntn_regenerative_config WHERE sat_id=?", satID)
	return err
}

// StoreAndForward queues data for store-and-forward delivery while
// the serving satellite is out of ground-station contact. Operator
// policy: there is no single-clause normative anchor for S&F in
// TR 38.821 v16.2 — see package-level TODO TS 38.821 (S&F) above.
// The buffering behaviour is informed by the "discontinuous network
// coverage" model in TS 23.501 §5.4.13 (LEO pass gaps).
func StoreAndForward(satID, data, target string) (map[string]interface{}, error) {
	satID = strings.TrimSpace(satID)
	target = strings.TrimSpace(target)
	if satID == "" { return nil, fmt.Errorf("sat_id is required") }
	if target == "" { return nil, fmt.Errorf("target is required") }
	dataSize := len(data) / 2
	res, err := engine.Exec("INSERT INTO ntn_store_forward (sat_id, target, data_hex, data_size, priority, status) VALUES (?,?,?,?,0,'queued')", satID, target, data, dataSize)
	if err != nil { return nil, err }
	id, _ := res.LastInsertId()
	return map[string]interface{}{"id": id, "sat_id": satID, "target": target, "data_size": dataSize, "status": "queued"}, nil
}

func GetStoreForwardQueue(satID string) ([]map[string]interface{}, error) {
	return queryRowsMap("SELECT * FROM ntn_store_forward WHERE sat_id=? AND status='queued' ORDER BY priority DESC, id", satID)
}

func GetStoreForwardAll(satID string) ([]map[string]interface{}, error) {
	return queryRowsMap("SELECT * FROM ntn_store_forward WHERE sat_id=? ORDER BY id DESC", satID)
}

// ListStoreForwardQueued returns every queued S&F entry across all
// satellites — used by the operator-side "queue" panel and by the
// /api/ntn/phase2/store-forward GET (no sat_id) shape.
func ListStoreForwardQueued() ([]map[string]interface{}, error) {
	return queryRowsMap("SELECT * FROM ntn_store_forward WHERE status='queued' ORDER BY priority DESC, id")
}

func ForwardQueued(entryID int64) error {
	_, err := engine.Exec("UPDATE ntn_store_forward SET status='forwarded', forwarded_at=datetime('now') WHERE id=?", entryID)
	return err
}

func ExpireQueued(entryID int64) error {
	_, err := engine.Exec("UPDATE ntn_store_forward SET status='expired' WHERE id=?", entryID)
	return err
}

// InterSatLink creates/updates an inter-satellite link. Architecture
// reference: TS 38.821 §5 architecture variants discuss ISL but
// without a single-clause normative anchor — see package-level
// TODO TS 38.821 (ISL) above.
func InterSatLink(sat1ID, sat2ID string, config map[string]interface{}) (map[string]interface{}, error) {
	sat1ID = strings.TrimSpace(sat1ID)
	sat2ID = strings.TrimSpace(sat2ID)
	if sat1ID == "" || sat2ID == "" { return nil, fmt.Errorf("sat1_id and sat2_id are required") }
	if sat1ID == sat2ID { return nil, fmt.Errorf("cannot create ISL between a satellite and itself") }
	if sat1ID > sat2ID { sat1ID, sat2ID = sat2ID, sat1ID }
	bw := intOr(config, "bandwidth_mbps", 0)
	latency := floatOr(config, "latency_ms", 0)
	status := strOr(config, "status", "inactive")
	db, err := engine.Open()
	if err != nil { return nil, err }
	var existing int
	_ = db.QueryRow("SELECT id FROM ntn_isl_links WHERE sat1_id=? AND sat2_id=?", sat1ID, sat2ID).Scan(&existing)
	if existing > 0 {
		_, err = db.Exec("UPDATE ntn_isl_links SET bandwidth_mbps=?, latency_ms=?, status=? WHERE sat1_id=? AND sat2_id=?", bw, latency, status, sat1ID, sat2ID)
	} else {
		_, err = db.Exec("INSERT INTO ntn_isl_links (sat1_id, sat2_id, bandwidth_mbps, latency_ms, status) VALUES (?,?,?,?,?)", sat1ID, sat2ID, bw, latency, status)
	}
	if err != nil { return nil, err }
	return GetISLLink(sat1ID, sat2ID)
}

func GetISLLink(sat1ID, sat2ID string) (map[string]interface{}, error) {
	if sat1ID > sat2ID { sat1ID, sat2ID = sat2ID, sat1ID }
	return queryRowMap("SELECT * FROM ntn_isl_links WHERE sat1_id=? AND sat2_id=?", sat1ID, sat2ID)
}

func ListISLLinks() ([]map[string]interface{}, error) { return queryRowsMap("SELECT * FROM ntn_isl_links ORDER BY id") }

func ListISLLinksForSat(satID string) ([]map[string]interface{}, error) {
	return queryRowsMap("SELECT * FROM ntn_isl_links WHERE sat1_id=? OR sat2_id=? ORDER BY id", satID, satID)
}

func DeleteISLLink(linkID int64) error { _, err := engine.Exec("DELETE FROM ntn_isl_links WHERE id=?", linkID); return err }

// GetPhase2Stats returns summary statistics for NTN Phase 2 features.
func GetPhase2Stats() (map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil { return nil, err }
	var regenTotal, regenActive, sfQueued, sfForwarded, islTotal, islActive int
	_ = db.QueryRow("SELECT COUNT(*) FROM ntn_regenerative_config").Scan(&regenTotal)
	_ = db.QueryRow("SELECT COUNT(*) FROM ntn_regenerative_config WHERE status='active'").Scan(&regenActive)
	_ = db.QueryRow("SELECT COUNT(*) FROM ntn_store_forward WHERE status='queued'").Scan(&sfQueued)
	_ = db.QueryRow("SELECT COUNT(*) FROM ntn_store_forward WHERE status='forwarded'").Scan(&sfForwarded)
	_ = db.QueryRow("SELECT COUNT(*) FROM ntn_isl_links").Scan(&islTotal)
	_ = db.QueryRow("SELECT COUNT(*) FROM ntn_isl_links WHERE status='active'").Scan(&islActive)
	return map[string]interface{}{
		"regenerative_total": regenTotal, "regenerative_active": regenActive,
		"store_forward_queued": sfQueued, "store_forward_forwarded": sfForwarded,
		"isl_total": islTotal, "isl_active": islActive,
	}, nil
}

// GetSatCapabilities returns full capabilities for a satellite.
func GetSatCapabilities(satID string) map[string]interface{} {
	regen, _ := GetRegenerativeConfig(satID)
	isl, _ := ListISLLinksForSat(satID)
	hasRegen := regen != nil && regen["status"] == "active"
	return map[string]interface{}{"sat_id": satID, "regenerative": regen, "isl_links": isl, "has_regenerative": hasRegen, "isl_count": len(isl)}
}

// ---- DB Read from original schema ----

// Satellite is a registered NTN satellite from the DB.
type Satellite struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	OrbitType string  `json:"orbit_type"`
	Altitude  float64 `json:"altitude_km"`
	Status    string  `json:"status"`
}

// ListSatellites reads the ntn_satellites table.
func ListSatellites() ([]Satellite, error) {
	db, err := engine.Open()
	if err != nil { return nil, err }
	rows, err := db.Query("SELECT satellite_id, name, orbit_type, altitude_km, status FROM ntn_satellites ORDER BY name")
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) { return nil, nil }
		return nil, err
	}
	defer rows.Close()
	var out []Satellite
	for rows.Next() {
		var s Satellite
		if err := rows.Scan(&s.ID, &s.Name, &s.OrbitType, &s.Altitude, &s.Status); err != nil { continue }
		out = append(out, s)
	}
	return out, nil
}

// NTNConfig is the ntn_config singleton row.
type NTNConfig struct {
	Enabled          bool   `json:"enabled"`
	DefaultOrbitType string `json:"default_orbit_type"`
	MaxPropDelayMS   int    `json:"max_prop_delay_ms"`
	FeederLinkBW     int    `json:"feeder_link_bw_mhz"`
}

// GetConfig reads the NTN config.
func GetConfig() (*NTNConfig, error) {
	db, err := engine.Open()
	if err != nil { return nil, err }
	var c NTNConfig
	var enabled int
	err = db.QueryRow("SELECT enabled, default_orbit_type, max_propagation_delay_ms, feeder_link_bandwidth_mhz FROM ntn_config WHERE id=1").Scan(&enabled, &c.DefaultOrbitType, &c.MaxPropDelayMS, &c.FeederLinkBW)
	if errors.Is(err, sql.ErrNoRows) { return nil, nil }
	if err != nil { return nil, err }
	c.Enabled = enabled != 0
	return &c, nil
}

// ---- helpers ----

func haversineKM(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return EarthRadiusKM * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func intOr(m map[string]interface{}, key string, def int) int {
	if v, ok := m[key]; ok {
		switch vv := v.(type) {
		case float64: return int(vv)
		case int: return vv
		}
	}
	return def
}

func floatOr(m map[string]interface{}, key string, def float64) float64 {
	if v, ok := m[key]; ok {
		if f, ok := v.(float64); ok { return f }
	}
	return def
}

func strOr(m map[string]interface{}, key, def string) string {
	if v, ok := m[key]; ok {
		if s, ok := v.(string); ok && s != "" { return s }
	}
	return def
}

func queryRowMap(query string, args ...interface{}) (map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil { return nil, err }
	rows, err := db.Query(query, args...)
	if err != nil { return nil, err }
	defer rows.Close()
	cols, _ := rows.Columns()
	if !rows.Next() { return nil, nil }
	vals := make([]interface{}, len(cols))
	ptrs := make([]interface{}, len(cols))
	for i := range vals { ptrs[i] = &vals[i] }
	if err := rows.Scan(ptrs...); err != nil { return nil, err }
	m := make(map[string]interface{}, len(cols))
	for i, c := range cols { m[c] = vals[i] }
	return m, nil
}

func queryRowsMap(query string, args ...interface{}) ([]map[string]interface{}, error) {
	db, err := engine.Open()
	if err != nil { return nil, err }
	rows, err := db.Query(query, args...)
	if err != nil { return nil, nil }
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]interface{}
	for rows.Next() {
		vals := make([]interface{}, len(cols))
		ptrs := make([]interface{}, len(cols))
		for i := range vals { ptrs[i] = &vals[i] }
		if err := rows.Scan(ptrs...); err != nil { continue }
		m := make(map[string]interface{}, len(cols))
		for i, c := range cols { m[c] = vals[i] }
		out = append(out, m)
	}
	return out, nil
}
