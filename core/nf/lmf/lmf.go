// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package lmf — Location Management Function.
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.273 §4.3.8     LMF functional description (5G LCS Stage 2).
//   - TS 23.273 §6.2       5GC-MT-LR procedure — the typical
//                          Mobile-Terminating Location Request the LMF
//                          serves on the NL1 reference point.
//   - TS 29.572 §5.2.2.2   Nlmf_Location_DetermineLocation — the SBI
//                          operation RequestLocation models. Stage-3
//                          wire transport (HTTP/2 + JSON) is not yet
//                          modelled; see TODO at RequestLocation.
//   - TS 29.572 §5.2.2.4   Nlmf_Location_CancelLocation — modelled by
//                          CancelSession.
//   - TS 38.305 §8.1       GNSS positioning methods (A-GNSS).
//   - TS 38.305 §8.9       NR Enhanced cell ID (NR E-CID).
//   - TS 38.305 §8.10      Multi-RTT positioning.
//   - TS 38.305 §8.11      DL-AoD positioning.
//   - TS 38.305 §8.12      DL-TDOA positioning.
//   - TS 38.305 §8.13      UL-TDOA positioning.
//   - TS 38.305 §8.14      UL-AoA positioning.
//   - TS 38.211 §7.4.1.7   Positioning Reference Signal (PRS) — the
//                          per-resource configuration AllocatePRSResource
//                          persists. PRS combSize ∈ {2,4,6,12} and
//                          numSymbols × combSize ≤ 12 are §7.4.1.7
//                          constraints.
//
// Hybrid methods (GNSS+E-CID, RTT+AoA) are local fusion policy —
// TS 38.305 §4.3 acknowledges combinations but does not normatively
// specify the weighting; the inverse-variance fusion below is operator
// policy, not a spec mandate.
//
// Method-selection logic in selectMethod() is operator policy as well
// (the QoS → method mapping derives from accuracy/response-time hints,
// not from any spec table).
//
// TODO TS 29.572 §6 — wire the HTTP/2 + JSON Nlmf_Location SBI surface
//                     once the SBI router lands. Today the calls are
//                     intra-process Go invocations.
// TODO TS 38.455 §8.2 — model the NRPPa Location Information Transfer
//                       Procedures (E-CID Measurement Initiation /
//                       Report, Multi-RTT, TDOA, AoA, etc). Today
//                       HandleNRPPaMeasurementResponse only ingests
//                       a pre-decoded measurement map.
// TODO TS 37.355 §6   — model the LPP message envelope for UE-side
//                       assistance / measurements. Today
//                       HandleLPPMeasurementResponse only ingests a
//                       pre-decoded data map.
// TODO TS 23.273 §6.2 — model the full 5GC-MT-LR signalling chain
//                       (LCS Service Request → AMF → LMF → NRPPa /
//                       LPP). Today RequestLocation jumps straight
//                       into the per-method execute*() branch.
// TODO TS 23.273 §6.x — Geofencing / area event triggers (see
//                       GeofenceEngine below) ride on the area-event
//                       reporting referenced throughout §6 procedures
//                       but are not stand-alone clauses; the engine
//                       here is decision-only and the DB-backed
//                       geofence storage isn't wired yet.
package lmf

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("lmf")

// ================================================================
// Location Session
// ================================================================

// Session is a single positioning request.
type Session struct {
	SessionID   string  `json:"session_id"`
	IMSI        string  `json:"imsi"`
	Method      string  `json:"method"`
	State       string  `json:"state"` // PENDING, ACTIVE, COMPLETED, FAILED, CANCELLED
	CreatedAt   float64 `json:"created_at"`
	CompletedAt float64 `json:"completed_at,omitempty"`

	// QoS requirements
	QoS map[string]float64 `json:"qos,omitempty"`

	// Measurement inputs
	CellInfo        map[string]any   `json:"cell_info,omitempty"`
	TimingAdvance   *float64         `json:"timing_advance,omitempty"`
	BeamIndex       *int             `json:"beam_index,omitempty"`
	RTTMeasurements []map[string]any `json:"rtt_measurements,omitempty"`
	TDOAMeasurements []map[string]any `json:"tdoa_measurements,omitempty"`
	AoAMeasurements []map[string]any `json:"aoa_measurements,omitempty"`
	AoDMeasurements []map[string]any `json:"aod_measurements,omitempty"`
	GNSSData        map[string]any   `json:"gnss_data,omitempty"`

	// Result
	Latitude     *float64 `json:"latitude,omitempty"`
	Longitude    *float64 `json:"longitude,omitempty"`
	Altitude     *float64 `json:"altitude,omitempty"`
	UncertaintyM *float64 `json:"uncertainty_m,omitempty"`
	Confidence   *int     `json:"confidence,omitempty"`
}

// PRSResource is a PRS resource configuration (TS 38.211 §7.4.1.7).
type PRSResource struct {
	PRSResourceID       int    `json:"prs_resource_id"`
	GnbID               string `json:"gnb_id"`
	FrequencyLayer      int    `json:"frequency_layer"`
	DLPRSResourceSetID  int    `json:"dl_prs_resource_set_id"`
	PeriodicityMS       int    `json:"periodicity_ms"`
	SlotOffset          int    `json:"slot_offset"`
	NumRB               int    `json:"num_rb"`
	StartPRB            int    `json:"start_prb"`
	NumSymbols          int    `json:"num_symbols"`
	CombSize            int    `json:"comb_size"`
	REOffset            int    `json:"re_offset"`
	SequenceID          int    `json:"sequence_id"`
	Active              bool   `json:"active"`
	CreatedAt           float64 `json:"created_at"`
}

// ================================================================
// LMF Context
// ================================================================

// Context manages location sessions and positioning computations.
type Context struct {
	mu             sync.Mutex
	sessions       map[string]*Session
	sessionCounter int
	gnbPositions   map[string]map[string]float64 // gnb_id -> {lat, lon, alt}
	gnbAntennaInfo map[string]map[string]float64 // gnb_id -> {azimuth_deg, beamwidth_deg, ...}
	prsResources   map[int]*PRSResource
	prsCounter     int
}

var (
	lmfOnce sync.Once
	lmfCtx  *Context
)

// GetLMF returns the global LMF context singleton.
func GetLMF() *Context {
	lmfOnce.Do(func() {
		lmfCtx = &Context{
			sessions:       make(map[string]*Session),
			gnbPositions:   make(map[string]map[string]float64),
			gnbAntennaInfo: make(map[string]map[string]float64),
			prsResources:   make(map[int]*PRSResource),
		}
		log.Infof("LMF initialized")
	})
	return lmfCtx
}

// RegisterGnbPosition registers a gNB's geographic position.
func (c *Context) RegisterGnbPosition(gnbID string, lat, lon, alt float64) {
	c.gnbPositions[gnbID] = map[string]float64{"lat": lat, "lon": lon, "alt": alt}
	log.Infof("LMF: gNB %s position registered: %.6f, %.6f, alt=%.1f", gnbID, lat, lon, alt)
}

// RegisterGnbAntenna registers gNB antenna configuration for AoD/AoA.
func (c *Context) RegisterGnbAntenna(gnbID string, azimuthDeg, beamwidthDeg, downtiltDeg float64, numBeams int) {
	c.gnbAntennaInfo[gnbID] = map[string]float64{
		"azimuth_deg":    azimuthDeg,
		"beamwidth_deg":  beamwidthDeg,
		"downtilt_deg":   downtiltDeg,
		"num_beams":      float64(numBeams),
		"beam_spacing_deg": beamwidthDeg / math.Max(float64(numBeams), 1),
	}
	log.Infof("LMF: gNB %s antenna registered: azimuth=%.1f beamwidth=%.1f beams=%d",
		gnbID, azimuthDeg, beamwidthDeg, numBeams)
}

// ================================================================
// Nlmf_Location service (TS 29.572 §5.2)
// ================================================================

// RequestLocation — Nlmf_Location_DetermineLocation per TS 29.572
// §5.2.2.2. The SBI HTTP/2 envelope is not modelled yet; this is an
// in-process call. See package-level TODO TS 29.572 §6.
func (c *Context) RequestLocation(imsi, method string, qos map[string]float64, lcsClientType string) *Session {
	if method == "" || method == "auto" {
		method = c.selectMethod(qos)
	}

	c.mu.Lock()
	c.sessionCounter++
	sessionID := fmt.Sprintf("loc-%05d", c.sessionCounter)
	c.mu.Unlock()

	session := &Session{
		SessionID: sessionID,
		IMSI:      imsi,
		Method:    method,
		QoS:       qos,
		State:     "PENDING",
		CreatedAt: float64(time.Now().Unix()),
	}
	c.mu.Lock()
	c.sessions[sessionID] = session
	c.mu.Unlock()

	log.Infof("LMF: Location request %s for IMSI=%s method=%s client=%s",
		sessionID, imsi, method, lcsClientType)

	session.State = "ACTIVE"
	func() {
		defer func() {
			if r := recover(); r != nil {
				session.State = "FAILED"
				log.Errorf("LMF: Positioning failed for %s: %v", sessionID, r)
			}
		}()
		switch method {
		case "ecid":
			c.executeECID(session)
		case "multi_rtt":
			c.executeMultiRTT(session)
		case "dl_tdoa":
			c.executeDLTDOA(session)
		case "ul_tdoa":
			c.executeULTDOA(session)
		case "dl_aod":
			c.executeDLAoD(session)
		case "ul_aoa":
			c.executeULAoA(session)
		case "agnss":
			c.executeAGNSS(session)
		case "hybrid_gnss_ecid":
			c.executeHybridGNSSECID(session)
		case "hybrid_rtt_aoa":
			c.executeHybridRTTAoA(session)
		default:
			c.executeECID(session)
		}
	}()

	// Store in DB
	c.storeSession(session)
	return session
}

func (c *Context) storeSession(s *Session) {
	defer func() { recover() }()
	db, err := engine.Open()
	if err != nil {
		return
	}
	now := time.Now().Format(time.RFC3339)
	lat := 0.0
	if s.Latitude != nil {
		lat = *s.Latitude
	}
	lon := 0.0
	if s.Longitude != nil {
		lon = *s.Longitude
	}
	unc := 0.0
	if s.UncertaintyM != nil {
		unc = *s.UncertaintyM
	}
	_, _ = db.Exec(`INSERT INTO positioning_sessions
		(session_id, imsi, method, state, latitude, longitude, uncertainty_m, created_at, completed_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.SessionID, s.IMSI, s.Method, s.State, lat, lon, unc, now,
		sql.NullString{String: now, Valid: s.State == "COMPLETED"})

	if s.State == "COMPLETED" && s.Latitude != nil {
		_, _ = db.Exec(`INSERT INTO location_history
			(imsi, latitude, longitude, uncertainty_m, method, source, timestamp)
			VALUES (?, ?, ?, ?, ?, 'lmf', ?)`,
			s.IMSI, lat, lon, unc, s.Method, now)
	}
}

// GetSession returns a session by ID.
func (c *Context) GetSession(sessionID string) *Session {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessions[sessionID]
}

// CancelSession cancels a location session.
func (c *Context) CancelSession(sessionID string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.sessions[sessionID]; ok {
		s.State = "CANCELLED"
	}
}

// ListSessions returns recent positioning sessions from DB.
func (c *Context) ListSessions(limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 100
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT session_id, imsi, method, state,
		COALESCE(latitude,0), COALESCE(longitude,0), COALESCE(uncertainty_m,0)
		FROM positioning_sessions ORDER BY created_at DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Session
	for rows.Next() {
		var s Session
		var lat, lon, unc float64
		if err := rows.Scan(&s.SessionID, &s.IMSI, &s.Method, &s.State, &lat, &lon, &unc); err != nil {
			continue
		}
		s.Latitude = &lat
		s.Longitude = &lon
		s.UncertaintyM = &unc
		out = append(out, s)
	}
	return out, nil
}

// GetSessionByID returns a session from DB.
func (c *Context) GetSessionByID(sessionID string) (*Session, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var s Session
	var lat, lon, unc float64
	err = db.QueryRow(`SELECT session_id, imsi, method, state,
		COALESCE(latitude,0), COALESCE(longitude,0), COALESCE(uncertainty_m,0)
		FROM positioning_sessions WHERE session_id=?`, sessionID).Scan(
		&s.SessionID, &s.IMSI, &s.Method, &s.State, &lat, &lon, &unc)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	s.Latitude = &lat
	s.Longitude = &lon
	s.UncertaintyM = &unc
	return &s, nil
}

// LocationHistory returns completed sessions for a UE (from in-memory cache).
func (c *Context) LocationHistory(imsi string, limit int) []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var results []map[string]any
	for _, s := range c.sessions {
		if s.IMSI == imsi && s.State == "COMPLETED" {
			results = append(results, map[string]any{
				"session_id":    s.SessionID,
				"method":        s.Method,
				"state":         s.State,
				"latitude":      s.Latitude,
				"longitude":     s.Longitude,
				"uncertainty_m": s.UncertaintyM,
				"created_at":    s.CreatedAt,
			})
		}
	}
	if len(results) > limit {
		results = results[len(results)-limit:]
	}
	return results
}

// ================================================================
// Method selection — operator policy
// ================================================================
// TS 38.305 §4.3 lists the standard positioning methods; the
// QoS-driven choice here (accuracy / response-time → method) is
// not §-mandated, so no §-cite is attached to selectMethod itself.

func (c *Context) selectMethod(qos map[string]float64) string {
	if qos == nil {
		return "ecid"
	}
	accuracy := qos["accuracy_m"]
	if accuracy == 0 {
		accuracy = 100
	}
	switch {
	case accuracy <= 3:
		if qos["response_time_s"] >= 5 {
			return "multi_rtt"
		}
		return "hybrid_rtt_aoa"
	case accuracy <= 5:
		return "hybrid_rtt_aoa"
	case accuracy <= 10:
		if len(c.gnbAntennaInfo) > 0 {
			return "dl_aod"
		}
		return "dl_tdoa"
	case accuracy <= 15:
		return "hybrid_gnss_ecid"
	case accuracy <= 30:
		return "ul_aoa"
	default:
		return "ecid"
	}
}

// ================================================================
// Positioning methods
// ================================================================

func setResult(s *Session, lat, lon float64, alt *float64, uncM float64, conf int) {
	s.Latitude = &lat
	s.Longitude = &lon
	s.Altitude = alt
	s.UncertaintyM = &uncM
	s.Confidence = &conf
	s.State = "COMPLETED"
	s.CompletedAt = float64(time.Now().Unix())
}

// executeECID runs the NR Enhanced Cell-ID method (TS 38.305 §8.9).
// Use the first known gNB position as approximation; in production
// the LMF would query the AMF UE context for the serving cell.
//
// TODO TS 38.305 §8.9.3 / §8.9.4 — Downlink / Uplink NR E-CID
// Positioning Procedures (proper measurement collection over NRPPa
// rather than the heuristic offset-from-first-gNB shortcut here).
func (c *Context) executeECID(s *Session) {
	for gnbID, pos := range c.gnbPositions {
		ta := 0.0
		if s.TimingAdvance != nil {
			ta = *s.TimingAdvance
		}
		distanceM := ta * 8.14e-9 * 3e8 / 2

		var lat, lon float64
		beamBearing := c.getBeamBearing(gnbID, s.BeamIndex)
		if beamBearing != nil && distanceM > 0 {
			lat, lon = offsetPosition(pos["lat"], pos["lon"], *beamBearing, distanceM)
			unc := math.Max(distanceM*0.3, 30)
			setResult(s, lat, lon, floatPtr(pos["alt"]), unc, 68)
		} else {
			lat, lon = pos["lat"], pos["lon"]
			unc := math.Max(distanceM, 50)
			setResult(s, lat, lon, floatPtr(pos["alt"]), unc, 68)
		}

		s.CellInfo = map[string]any{"gnb_id": gnbID}
		log.Infof("LMF: E-CID result for %s: lat=%.6f lon=%.6f +/-%.0fm",
			s.IMSI, lat, lon, *s.UncertaintyM)
		return
	}

	// No gNB positions known: report coarse result
	unc := 500.0
	conf := 50
	s.UncertaintyM = &unc
	s.Confidence = &conf
	s.State = "COMPLETED"
	s.CompletedAt = float64(time.Now().Unix())
}

// executeMultiRTT runs Multi-RTT positioning (TS 38.305 §8.10). Needs
// ≥3 RTT measurements for circle trilateration; falls back to E-CID.
func (c *Context) executeMultiRTT(s *Session) {
	if len(s.RTTMeasurements) >= 3 {
		c.trilaterateRTT(s)
	} else {
		c.executeECID(s)
	}
}

func (c *Context) trilaterateRTT(s *Session) {
	var circles []circle
	for _, m := range s.RTTMeasurements {
		gnbID, _ := m["gnb_id"].(string)
		gnbPos := c.gnbPositions[gnbID]
		if gnbPos == nil {
			if gp, ok := m["gnb_pos"].(map[string]any); ok {
				gnbPos = map[string]float64{
					"lat": toFloat(gp["lat"]), "lon": toFloat(gp["lon"]),
				}
			}
		}
		if gnbPos == nil {
			continue
		}
		rttNS := toFloat(m["rtt_ns"])
		distM := rttNS * 1e-9 * 3e8 / 2
		circles = append(circles, circle{gnbPos["lat"], gnbPos["lon"], distM})
	}

	if len(circles) >= 3 {
		lat, lon := trilaterate(circles)
		setResult(s, lat, lon, nil, 3, 90)
		log.Infof("LMF: Multi-RTT result for %s: lat=%.6f lon=%.6f +/-3m", s.IMSI, lat, lon)
	}
}

// executeDLTDOA runs DL-TDOA positioning (TS 38.305 §8.12).
// Hyperbolic trilateration needs ≥3 RSTD measurements; falls back to E-CID.
func (c *Context) executeDLTDOA(s *Session) {
	if len(s.TDOAMeasurements) >= 3 {
		c.hyperbolicTrilaterate(s)
	} else {
		c.executeECID(s)
	}
}

// executeULTDOA runs UL-TDOA positioning (TS 38.305 §8.13). Same
// hyperbolic solver as DL-TDOA; the difference is the measurement
// origin (gNB-side SRS vs. UE-side PRS), which the upstream stage
// has already resolved into the same RSTD shape by the time we
// reach this layer.
func (c *Context) executeULTDOA(s *Session) {
	if len(s.TDOAMeasurements) >= 3 {
		c.hyperbolicTrilaterate(s)
	} else {
		c.executeECID(s)
	}
}

func (c *Context) hyperbolicTrilaterate(s *Session) {
	if len(s.TDOAMeasurements) < 3 {
		c.executeECID(s)
		return
	}

	// Simplified: use weighted centroid of gNB positions
	ref := s.TDOAMeasurements[0]
	refPos := c.getGnbPos(ref)
	if refPos == nil {
		c.executeECID(s)
		return
	}

	var positions []map[string]float64
	var rangeDiffs []float64
	for _, m := range s.TDOAMeasurements {
		pos := c.getGnbPos(m)
		if pos == nil {
			continue
		}
		positions = append(positions, pos)
		rstdNS := toFloat(m["rstd_ns"])
		rangeDiffs = append(rangeDiffs, rstdNS*1e-9*3e8)
	}

	if len(positions) < 3 {
		c.executeECID(s)
		return
	}

	// Weighted centroid initial estimate
	totalWeight := 0.0
	latSum := 0.0
	lonSum := 0.0
	for i, pos := range positions {
		weight := 1.0 / math.Max(math.Abs(rangeDiffs[i]), 1)
		latSum += pos["lat"] * weight
		lonSum += pos["lon"] * weight
		totalWeight += weight
	}
	estLat := latSum / totalWeight
	estLon := lonSum / totalWeight

	setResult(s, estLat, estLon, nil, 10, 80)
	log.Infof("LMF: TDOA result for %s: lat=%.6f lon=%.6f +/-10m", s.IMSI, estLat, estLon)
}

// executeDLAoD runs DL-AoD positioning (TS 38.305 §8.11). With ≥2
// gNBs intersect bearing rays; with one + a distance hint, project
// along the beam.
func (c *Context) executeDLAoD(s *Session) {
	if len(s.AoDMeasurements) == 0 {
		c.executeECID(s)
		return
	}

	type bearing struct {
		gnbPos     map[string]float64
		azimuthDeg float64
		distanceM  *float64
	}
	var bearings []bearing

	for _, m := range s.AoDMeasurements {
		gnbID, _ := m["gnb_id"].(string)
		gnbPos := c.gnbPositions[gnbID]
		if gnbPos == nil {
			continue
		}
		if c.gnbAntennaInfo[gnbID] == nil {
			continue
		}
		beamIdx := int(toFloat(m["beam_index"]))
		az := c.getBeamBearing(gnbID, &beamIdx)
		if az == nil {
			continue
		}
		var dist *float64
		if d, ok := m["distance_m"]; ok {
			f := toFloat(d)
			dist = &f
		}
		bearings = append(bearings, bearing{gnbPos, *az, dist})
	}

	if len(bearings) == 0 {
		c.executeECID(s)
		return
	}

	if len(bearings) >= 2 {
		lat, lon := intersectBearings(
			bearings[0].gnbPos, bearings[0].azimuthDeg,
			bearings[1].gnbPos, bearings[1].azimuthDeg)
		setResult(s, lat, lon, nil, 8, 80)
	} else if bearings[0].distanceM != nil {
		lat, lon := offsetPosition(bearings[0].gnbPos["lat"], bearings[0].gnbPos["lon"],
			bearings[0].azimuthDeg, *bearings[0].distanceM)
		setResult(s, lat, lon, nil, 15, 70)
	} else {
		lat, lon := offsetPosition(bearings[0].gnbPos["lat"], bearings[0].gnbPos["lon"],
			bearings[0].azimuthDeg, 200)
		setResult(s, lat, lon, nil, 100, 50)
	}
}

// executeULAoA runs UL-AoA positioning (TS 38.305 §8.14). With ≥2
// gNBs intersect azimuth rays; with one + distance hint, project.
func (c *Context) executeULAoA(s *Session) {
	if len(s.AoAMeasurements) == 0 {
		c.executeECID(s)
		return
	}

	type bearing struct {
		gnbPos     map[string]float64
		azimuthDeg float64
		distanceM  *float64
	}
	var bearings []bearing

	for _, m := range s.AoAMeasurements {
		gnbID, _ := m["gnb_id"].(string)
		gnbPos := c.gnbPositions[gnbID]
		if gnbPos == nil {
			continue
		}
		azRaw := toFloat(m["azimuth"])
		az := azRaw
		if azRaw > 360 {
			az = azRaw / 10.0
		}
		var dist *float64
		if d, ok := m["distance_m"]; ok {
			f := toFloat(d)
			dist = &f
		}
		bearings = append(bearings, bearing{gnbPos, az, dist})
	}

	if len(bearings) == 0 {
		c.executeECID(s)
		return
	}

	if len(bearings) >= 2 {
		lat, lon := intersectBearings(
			bearings[0].gnbPos, bearings[0].azimuthDeg,
			bearings[1].gnbPos, bearings[1].azimuthDeg)
		setResult(s, lat, lon, nil, 20, 75)
	} else if bearings[0].distanceM != nil {
		lat, lon := offsetPosition(bearings[0].gnbPos["lat"], bearings[0].gnbPos["lon"],
			bearings[0].azimuthDeg, *bearings[0].distanceM)
		setResult(s, lat, lon, nil, 30, 65)
	} else {
		lat, lon := offsetPosition(bearings[0].gnbPos["lat"], bearings[0].gnbPos["lon"],
			bearings[0].azimuthDeg, 150)
		setResult(s, lat, lon, nil, 100, 50)
	}
}

// executeAGNSS — Assisted GNSS path (TS 38.305 §8.1). The UE-reported
// fix is taken at face value; the LMF does not re-solve. In a full
// stack the assistance data exchange runs over LPP (TS 37.355 §6).
func (c *Context) executeAGNSS(s *Session) {
	if s.GNSSData != nil {
		lat := toFloat(s.GNSSData["lat"])
		lon := toFloat(s.GNSSData["lon"])
		alt := toFloat(s.GNSSData["alt"])
		accM := toFloat(s.GNSSData["accuracy_m"])
		if accM == 0 {
			accM = 10
		}
		setResult(s, lat, lon, &alt, accM, 95)
	} else {
		c.executeECID(s)
	}
}

// executeHybridGNSSECID fuses an A-GNSS fix (TS 38.305 §8.1) with an
// NR E-CID fix (§8.9) by inverse-variance weighting. The fusion is
// operator policy; TS 38.305 §4.3 acknowledges combinations but does
// not normatively specify the weighting.
func (c *Context) executeHybridGNSSECID(s *Session) {
	var gnssLat, gnssLon, gnssUnc float64
	hasGNSS := false
	if s.GNSSData != nil {
		gnssLat = toFloat(s.GNSSData["lat"])
		gnssLon = toFloat(s.GNSSData["lon"])
		gnssUnc = toFloat(s.GNSSData["accuracy_m"])
		if gnssUnc == 0 {
			gnssUnc = 10
		}
		hasGNSS = true
	}

	// Compute E-CID sub-session
	ecidSession := &Session{IMSI: s.IMSI, Method: "ecid", State: "ACTIVE",
		TimingAdvance: s.TimingAdvance, BeamIndex: s.BeamIndex}
	c.executeECID(ecidSession)
	hasECID := ecidSession.State == "COMPLETED" && ecidSession.Latitude != nil

	if hasGNSS && hasECID {
		ecidLat := *ecidSession.Latitude
		ecidLon := *ecidSession.Longitude
		ecidUnc := *ecidSession.UncertaintyM

		wGNSS := 1.0 / (gnssUnc * gnssUnc)
		wECID := 1.0 / (ecidUnc * ecidUnc)
		totalW := wGNSS + wECID

		lat := (gnssLat*wGNSS + ecidLat*wECID) / totalW
		lon := (gnssLon*wGNSS + ecidLon*wECID) / totalW
		unc := 1.0 / math.Sqrt(totalW)
		setResult(s, lat, lon, nil, unc, 92)
	} else if hasGNSS {
		setResult(s, gnssLat, gnssLon, nil, gnssUnc, 95)
	} else if hasECID {
		setResult(s, *ecidSession.Latitude, *ecidSession.Longitude, ecidSession.Altitude,
			*ecidSession.UncertaintyM, 68)
	} else {
		s.State = "FAILED"
	}
}

// executeHybridRTTAoA fuses Multi-RTT (TS 38.305 §8.10) with UL-AoA
// (§8.14) per gNB. Each gNB contributes a position from RTT-derived
// distance + AoA bearing; the average is taken. RTT trilateration
// adds an extra higher-weight position when ≥3 RTT measurements
// are available. Like §8.1+§8.9 fusion above, the combination is
// operator policy.
func (c *Context) executeHybridRTTAoA(s *Session) {
	type pos struct {
		lat, lon float64
		weight   float64
	}
	var positions []pos

	// Match RTT and AoA by gNB
	rttByGnb := map[string]map[string]any{}
	for _, m := range s.RTTMeasurements {
		gnbID, _ := m["gnb_id"].(string)
		rttByGnb[gnbID] = m
	}
	aoaByGnb := map[string]map[string]any{}
	for _, m := range s.AoAMeasurements {
		gnbID, _ := m["gnb_id"].(string)
		aoaByGnb[gnbID] = m
	}

	// Hybrid RTT+AoA per gNB
	for gnbID, rttM := range rttByGnb {
		aoaM, ok := aoaByGnb[gnbID]
		if !ok {
			continue
		}
		gnbPos := c.gnbPositions[gnbID]
		if gnbPos == nil {
			continue
		}
		distM := toFloat(rttM["rtt_ns"]) * 1e-9 * 3e8 / 2
		azRaw := toFloat(aoaM["azimuth"])
		az := azRaw
		if azRaw > 360 {
			az = azRaw / 10.0
		}
		lat, lon := offsetPosition(gnbPos["lat"], gnbPos["lon"], az, distM)
		positions = append(positions, pos{lat, lon, 5.0})
	}

	// RTT trilateration fallback
	if len(s.RTTMeasurements) >= 3 {
		var circles []circle
		for _, m := range s.RTTMeasurements {
			gp := c.getGnbPos(m)
			if gp == nil {
				continue
			}
			circles = append(circles, circle{gp["lat"], gp["lon"],
				toFloat(m["rtt_ns"]) * 1e-9 * 3e8 / 2})
		}
		if len(circles) >= 3 {
			lat, lon := trilaterate(circles)
			positions = append(positions, pos{lat, lon, 3.0})
		}
	}

	if len(positions) > 0 {
		totalW := 0.0
		latSum := 0.0
		lonSum := 0.0
		for _, p := range positions {
			latSum += p.lat * p.weight
			lonSum += p.lon * p.weight
			totalW += p.weight
		}
		setResult(s, latSum/totalW, lonSum/totalW, nil, 5, 88)
	} else {
		c.executeECID(s)
	}
}

// ================================================================
// PRS Configuration (TS 38.211 §7.4.1.7)
// ================================================================

// AllocatePRSResource allocates a PRS resource for a gNB. PRS
// parameters are constrained by TS 38.211 §7.4.1.7:
//
//   - periodicity ∈ {4,5,8,10,16,20,32,40,64,80,160,320,640,1280,
//     2560,5120,10240} slots (§7.4.1.7.4 Table 7.4.1.7.4-1).
//   - combSize ∈ {2,4,6,12} (§7.4.1.7.3 K_PRS comb sizes).
//   - numSymbols ∈ {2,4,6,12} with K_PRS · L_PRS ≤ 12 (§7.4.1.7.3
//     resource-element mapping invariant).
//   - numRB ∈ [24, 272] (§7.4.1.7.3 PRS bandwidth limits).
//
// Out-of-range inputs are clamped to a safe default rather than
// rejected — this matches the operator-friendly contract the GUI
// panel expects.
func (c *Context) AllocatePRSResource(gnbID string, frequencyLayer, periodicityMS, numRB, numSymbols, combSize int) *PRSResource {
	validPeriods := map[int]bool{4: true, 5: true, 8: true, 10: true, 16: true, 20: true,
		32: true, 40: true, 64: true, 80: true, 160: true, 320: true, 640: true,
		1280: true, 2560: true, 5120: true, 10240: true}
	if !validPeriods[periodicityMS] {
		periodicityMS = 20
	}
	validComb := map[int]bool{2: true, 4: true, 6: true, 12: true}
	if !validComb[combSize] {
		combSize = 2
	}
	validSym := map[int]bool{2: true, 4: true, 6: true, 12: true}
	if !validSym[numSymbols] {
		numSymbols = 2
	}
	if numSymbols*combSize > 12 {
		numSymbols = 12 / combSize
	}
	if numRB < 24 {
		numRB = 24
	}
	if numRB > 272 {
		numRB = 272
	}

	c.mu.Lock()
	c.prsCounter++
	prsID := c.prsCounter
	c.mu.Unlock()

	prs := &PRSResource{
		PRSResourceID:  prsID,
		GnbID:          gnbID,
		FrequencyLayer: frequencyLayer,
		PeriodicityMS:  periodicityMS,
		NumRB:          numRB,
		NumSymbols:     numSymbols,
		CombSize:       combSize,
		SequenceID:     (hashString(gnbID)*1000 + prsID) % 4096,
		Active:         true,
		CreatedAt:      float64(time.Now().Unix()),
	}

	c.mu.Lock()
	c.prsResources[prsID] = prs
	c.mu.Unlock()

	log.Infof("LMF: PRS resource %d allocated for gNB %s: period=%dms rb=%d sym=%d comb=%d",
		prsID, gnbID, periodicityMS, numRB, numSymbols, combSize)
	return prs
}

// GetPRSResourcesForGnb returns all active PRS resources for a gNB.
func (c *Context) GetPRSResourcesForGnb(gnbID string) []*PRSResource {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []*PRSResource
	for _, p := range c.prsResources {
		if p.GnbID == gnbID && p.Active {
			result = append(result, p)
		}
	}
	return result
}

// DeactivatePRSResource deactivates a PRS resource.
func (c *Context) DeactivatePRSResource(prsResourceID int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if p, ok := c.prsResources[prsResourceID]; ok {
		p.Active = false
	}
}

// HandleNRPPaMeasurementResponse processes NRPPa measurement
// response from a gNB. The wire-level NRPPa codec (TS 38.455 §8.2
// Location Information Transfer Procedures, e.g. §8.2.3 E-CID
// Measurement Report, §8.2.6 Positioning Information Exchange)
// is not yet modelled — see package-level TODO TS 38.455 §8.2.
// This entry point assumes the NRPPa PDU has already been decoded
// into a list of typed measurement maps.
func (c *Context) HandleNRPPaMeasurementResponse(sessionID string, measurements []map[string]any) {
	s := c.GetSession(sessionID)
	if s == nil {
		return
	}
	for _, m := range measurements {
		mtype, _ := m["type"].(string)
		switch mtype {
		case "rtt":
			s.RTTMeasurements = append(s.RTTMeasurements, m)
		case "tdoa":
			s.TDOAMeasurements = append(s.TDOAMeasurements, m)
		case "aoa":
			s.AoAMeasurements = append(s.AoAMeasurements, m)
		case "aod":
			s.AoDMeasurements = append(s.AoDMeasurements, m)
		}
	}
}

// HandleLPPMeasurementResponse processes an LPP measurement response
// from the UE. The wire-level LPP codec (TS 37.355 §6 messages and
// IEs — ProvideLocationInformation, ProvideAssistanceData, etc.)
// is not yet modelled — see package-level TODO TS 37.355 §6. This
// entry point assumes the LPP PDU has already been decoded into a
// typed map.
func (c *Context) HandleLPPMeasurementResponse(sessionID string, lppData map[string]any) {
	s := c.GetSession(sessionID)
	if s == nil {
		return
	}
	if gnss, ok := lppData["gnss"].(map[string]any); ok {
		s.GNSSData = gnss
	}
	if tdoa, ok := lppData["dl_tdoa"].([]map[string]any); ok {
		s.TDOAMeasurements = append(s.TDOAMeasurements, tdoa...)
	}
	if rtt, ok := lppData["multi_rtt"].([]map[string]any); ok {
		s.RTTMeasurements = append(s.RTTMeasurements, rtt...)
	}
}

// ================================================================
// Geofencing — area-event reporting helper
// ================================================================
// Area-event reporting (enter / leave / within) is referenced
// throughout TS 23.273 §6 procedures (e.g. §6.3 Deferred 5GC-MT-LR
// for area events) but is not a stand-alone clause. This engine is
// a decision-only helper for the GUI layer; the DB-backed
// geofences table exists in db/schemas/positioning.go but the
// query path here is a stub.
//
// TODO TS 23.273 §6.3 — wire CheckPosition() against the geofences
// DB row + area-event reporting state machine.

// GeofenceEngine tracks per-UE per-fence state.
type GeofenceEngine struct {
	mu    sync.Mutex
	state map[string]bool // "imsi:fenceID" -> inside
}

var geofenceEngine = &GeofenceEngine{state: make(map[string]bool)}

// GetGeofenceEngine returns the global geofence engine.
func GetGeofenceEngine() *GeofenceEngine { return geofenceEngine }

// CheckPosition checks UE position against all applicable geofences.
// TODO TS 23.273 §6.3 — read geofences table, compare against lat/lon,
// emit area-event reports per the Deferred 5GC-MT-LR procedure.
func (g *GeofenceEngine) CheckPosition(imsi string, lat, lon float64) []map[string]any {
	return nil
}

// ResetState clears cached geofence state.
func (g *GeofenceEngine) ResetState(imsi string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if imsi == "" {
		g.state = make(map[string]bool)
	} else {
		for k := range g.state {
			if len(k) > len(imsi) && k[:len(imsi)+1] == imsi+":" {
				delete(g.state, k)
			}
		}
	}
}

// ================================================================
// Computation helpers
// ================================================================

type circle struct {
	lat, lon, radiusM float64
}

func trilaterate(circles []circle) (float64, float64) {
	refLat := circles[0].lat
	refLon := circles[0].lon
	mPerDegLat := 111320.0
	mPerDegLon := 111320.0 * math.Cos(math.Pi*refLat/180)

	r0 := circles[0].radiusM
	if len(circles) < 3 {
		return refLat, refLon
	}

	// Build linear system: Ax = b
	// Subtract circle 0 from circle i (in local Cartesian frame
	// with origin at circle 0):
	//   x² + y² = r0²                                 (circle 0)
	//   (x-xi)² + (y-yi)² = ri²                       (circle i)
	// →  2·xi·x + 2·yi·y = r0² − ri² + xi² + yi²
	var A [][2]float64
	var b []float64
	for i := 1; i < len(circles); i++ {
		xi := (circles[i].lon - refLon) * mPerDegLon
		yi := (circles[i].lat - refLat) * mPerDegLat
		ri := circles[i].radiusM
		A = append(A, [2]float64{2 * xi, 2 * yi})
		b = append(b, r0*r0-ri*ri+xi*xi+yi*yi)
	}

	if len(A) >= 2 {
		a11, a12 := A[0][0], A[0][1]
		a21, a22 := A[1][0], A[1][1]
		b1, b2 := b[0], b[1]
		det := a11*a22 - a12*a21
		if math.Abs(det) > 1e-10 {
			x := (b1*a22 - b2*a12) / det
			y := (a11*b2 - a21*b1) / det
			return refLat + y/mPerDegLat, refLon + x/mPerDegLon
		}
	}
	return refLat, refLon
}

func (c *Context) getBeamBearing(gnbID string, beamIndex *int) *float64 {
	if beamIndex == nil {
		return nil
	}
	antenna := c.gnbAntennaInfo[gnbID]
	if antenna == nil {
		return nil
	}
	boresight := antenna["azimuth_deg"]
	beamwidth := antenna["beamwidth_deg"]
	numBeams := int(antenna["num_beams"])
	if numBeams <= 1 {
		return &boresight
	}
	beamSpacing := beamwidth / float64(numBeams)
	offset := (float64(*beamIndex) - float64(numBeams-1)/2.0) * beamSpacing
	bearing := math.Mod(boresight+offset+360, 360)
	return &bearing
}

func offsetPosition(lat, lon, bearingDeg, distanceM float64) (float64, float64) {
	R := 6371000.0
	d := distanceM / R
	brng := bearingDeg * math.Pi / 180
	lat1 := lat * math.Pi / 180
	lon1 := lon * math.Pi / 180

	lat2 := math.Asin(math.Sin(lat1)*math.Cos(d) + math.Cos(lat1)*math.Sin(d)*math.Cos(brng))
	lon2 := lon1 + math.Atan2(math.Sin(brng)*math.Sin(d)*math.Cos(lat1),
		math.Cos(d)-math.Sin(lat1)*math.Sin(lat2))

	return lat2 * 180 / math.Pi, lon2 * 180 / math.Pi
}

func intersectBearings(p1 map[string]float64, az1 float64, p2 map[string]float64, az2 float64) (float64, float64) {
	a1 := az1 * math.Pi / 180
	a2 := az2 * math.Pi / 180
	refLat := (p1["lat"] + p2["lat"]) / 2
	mPerDegLat := 111320.0
	mPerDegLon := 111320.0 * math.Cos(math.Pi*refLat/180)

	x1 := (p1["lon"] - p2["lon"]) * mPerDegLon
	y1 := (p1["lat"] - p2["lat"]) * mPerDegLat

	dx1, dy1 := math.Sin(a1), math.Cos(a1)
	dx2, dy2 := math.Sin(a2), math.Cos(a2)

	denom := dx1*dy2 - dy1*dx2
	if math.Abs(denom) < 1e-10 {
		return (p1["lat"] + p2["lat"]) / 2, (p1["lon"] + p2["lon"]) / 2
	}

	t1 := (-x1*dy2 - (-y1)*dx2) / denom
	ix := x1 + t1*dx1
	iy := y1 + t1*dy1

	return p2["lat"] + iy/mPerDegLat, p2["lon"] + ix/mPerDegLon
}

func haversineDistance(lat1, lon1, lat2, lon2 float64) float64 {
	R := 6371000.0
	dlat := (lat2 - lat1) * math.Pi / 180
	dlon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dlat/2)*math.Sin(dlat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*
			math.Sin(dlon/2)*math.Sin(dlon/2)
	return R * 2 * math.Atan2(math.Sqrt(a), math.Sqrt(1-a))
}

func (c *Context) getGnbPos(m map[string]any) map[string]float64 {
	gnbID, _ := m["gnb_id"].(string)
	if pos, ok := c.gnbPositions[gnbID]; ok {
		return pos
	}
	if gp, ok := m["gnb_pos"].(map[string]any); ok {
		return map[string]float64{"lat": toFloat(gp["lat"]), "lon": toFloat(gp["lon"])}
	}
	return nil
}

func floatPtr(f float64) *float64 { return &f }

func toFloat(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func hashString(s string) int {
	h := 0
	for _, c := range s {
		h = h*31 + int(c)
	}
	if h < 0 {
		h = -h
	}
	return h
}

// Ensure imports are used.
func init() {
	_ = haversineDistance
}
