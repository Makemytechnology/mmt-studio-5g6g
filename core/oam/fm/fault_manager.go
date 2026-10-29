// Package fm — 5GC fault management.
//
// Spec anchors:
//
//   - TS 28.532 §11.2a — Generic fault supervision management service.
//     This is the only directly-applicable section in the loaded R19
//     PDF; §11.2a itself defers to TS 28.111 (the actual operations
//     and data model) which is not loaded locally — see TODO below.
//   - TS 28.532 §11.5 — Streaming data reporting service. Currently
//     used as the conceptual model for the alarm-stream surface;
//     wire-level streaming is not implemented (alarms are persisted
//     and queried via REST instead).
//
// Deferred surfaces (PDFs not in specs/3gpp/, so cited as
// TODO(spec:) prose only — invisible to speccheck):
//
//   - TODO(spec: TS 28.111, "Generic fault supervision Stage 2/3")
//     — the canonical alarm operations: subscribe / getAlarmList /
//     acknowledgeAlarm / clearAlarm / notifyNewAlarm / notifyAckChanged.
//   - TODO(spec: ITU-T X.733, "Alarm reporting function") — the alarm
//     types, perceived severities, and probable-cause enumerations
//     used below. We track the X.733 vocabulary but do not implement
//     the X.733 management context or the STATE-CHANGE service.
//
// Implementation notes:
//
//   - Alarm lifecycle:
//     · Raise() — create or update an active alarm
//     · Clear() — sets perceivedSeverity=Cleared, records clearTime
//     · Ack()   — marks alarm as acknowledged by an operator
//   - Correlation: duplicate alarms with the same
//     (ManagedObject, ProbableCause, SpecificProblem) tuple are merged;
//     the existing alarm has RaiseCount++ rather than a new row being
//     inserted. This collapses the "fault flap" pattern (e.g. multiple
//     cleanup goroutines firing for the same SCTP association loss)
//     into one operator-visible event.
//   - Persistence: active + cleared alarms are written to the alarms
//     table. On Init() the manager hydrates its in-memory cache from
//     active rows.
package fm

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// ── 3GPP / X.733 enumerations ───────────────────────────────────────────

const (
	AlarmTypeCommunications = "Communications"
	AlarmTypeProcessing     = "Processing"
	AlarmTypeEnvironmental  = "Environmental"
	AlarmTypeQoS            = "QoS"
	AlarmTypeEquipment      = "Equipment"
)

var alarmTypes = map[string]struct{}{
	AlarmTypeCommunications: {}, AlarmTypeProcessing: {},
	AlarmTypeEnvironmental: {}, AlarmTypeQoS: {}, AlarmTypeEquipment: {},
}

const (
	SeverityCritical      = "Critical"
	SeverityMajor         = "Major"
	SeverityMinor         = "Minor"
	SeverityWarning       = "Warning"
	SeverityIndeterminate = "Indeterminate"
	SeverityCleared       = "Cleared"
)

var severityOrder = map[string]int{
	SeverityCritical:      0,
	SeverityMajor:         1,
	SeverityMinor:         2,
	SeverityWarning:       3,
	SeverityIndeterminate: 4,
	SeverityCleared:       5,
}

// Probable causes (X.733 §8.1.3 — subset used in 5G core).
const (
	CauseLossOfSignal                  = "lossOfSignal"
	CauseCommunicationsSubsystemFailure = "communicationsSubsystemFailure"
	CauseDegradedSignal                = "degradedSignal"
	CauseCallSetupFailure              = "callSetUpFailure"
	CauseConnectionEstablishmentError  = "connectionEstablishmentError"
	CauseSoftwareError                 = "softwareError"
	CauseSoftwareProgramError          = "softwareProgramError"
	CauseConfigurationError            = "configurationOrCustomizationError"
	CauseCorruptData                   = "corruptData"
	CauseOutOfMemory                   = "outOfMemory"
	CauseStorageCapacityProblem        = "storageCapacityProblem"
	CauseThresholdCrossed              = "thresholdCrossed"
	CauseQoSResourceNotAvailable       = "resourcesNotAvailable"
	CauseResponseTimeExcessive         = "responseTimeExcessive"
	CauseBandwidthReduced              = "bandwidthReduced"
	CauseEquipmentMalfunction          = "equipmentMalfunction"
	CausePowerProblem                  = "powerProblem"
	CauseApplicationSubsystemFailure   = "applicationSubsystemFailure"
)

// Alarm is the row shape in the alarms table.
type Alarm struct {
	AlarmID           int64   `json:"alarm_id"`
	ManagedObject     string  `json:"managed_object"`
	AlarmType         string  `json:"alarm_type"`
	ProbableCause     string  `json:"probable_cause"`
	PerceivedSeverity string  `json:"perceived_severity"`
	SpecificProblem   string  `json:"specific_problem"`
	AdditionalText    string  `json:"additional_text"`
	AdditionalInfo    string  `json:"additional_info"`
	EventTime         float64 `json:"event_time"`
	LastRaised        float64 `json:"last_raised"`
	ClearTime         *float64 `json:"clear_time,omitempty"`
	AckState          string  `json:"ack_state"`
	AckTime           *float64 `json:"ack_time,omitempty"`
	AckUser           string  `json:"ack_user,omitempty"`
	RaiseCount        int     `json:"raise_count"`
}

// Manager is the stateful singleton. Use Default or NewManager.
type Manager struct {
	mu          sync.Mutex
	seq         int64
	active      map[string]*Alarm // correlation key → alarm
	initialized bool
	log         *logger.Logger
}

// Default is the process-wide singleton used by the convenience helpers.
var Default = NewManager()

// NewManager creates an independent manager — mostly useful for tests.
func NewManager() *Manager {
	return &Manager{
		active: make(map[string]*Alarm),
		log:    logger.Get("fm"),
	}
}

// Init loads active alarms from DB into memory. Safe to call repeatedly.
func (m *Manager) Init() error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	rows, err := db.Query(
		`SELECT alarm_id, managed_object, alarm_type, probable_cause,
                perceived_severity, specific_problem, additional_text,
                additional_info, event_time, last_raised, clear_time,
                ack_state, ack_time, ack_user, raise_count
         FROM alarms WHERE perceived_severity != 'Cleared' ORDER BY alarm_id`)
	if err != nil {
		return err
	}
	defer rows.Close()

	m.mu.Lock()
	defer m.mu.Unlock()
	m.active = make(map[string]*Alarm)
	for rows.Next() {
		var a Alarm
		var clearTime, ackTime sql.NullFloat64
		var ackUser sql.NullString
		if err := rows.Scan(
			&a.AlarmID, &a.ManagedObject, &a.AlarmType, &a.ProbableCause,
			&a.PerceivedSeverity, &a.SpecificProblem, &a.AdditionalText,
			&a.AdditionalInfo, &a.EventTime, &a.LastRaised, &clearTime,
			&a.AckState, &ackTime, &ackUser, &a.RaiseCount,
		); err != nil {
			return err
		}
		if clearTime.Valid {
			v := clearTime.Float64
			a.ClearTime = &v
		}
		if ackTime.Valid {
			v := ackTime.Float64
			a.AckTime = &v
		}
		a.AckUser = ackUser.String
		m.active[correlationKey(a.ManagedObject, a.ProbableCause, a.SpecificProblem)] = &a
		if a.AlarmID > m.seq {
			m.seq = a.AlarmID
		}
	}
	m.initialized = true
	if n := len(m.active); n > 0 {
		m.log.Infof("Fault manager initialized — %d active alarm(s) loaded", n)
	} else {
		m.log.Info("Fault manager initialized — no active alarms")
	}
	return rows.Err()
}

func correlationKey(mo, cause, problem string) string {
	return mo + "::" + cause + "::" + problem
}

// RaiseInput groups arguments to Raise — matches Python kwargs.
type RaiseInput struct {
	ManagedObject     string
	AlarmType         string
	ProbableCause     string
	PerceivedSeverity string // NOT Cleared — use Clear()
	SpecificProblem   string
	AdditionalText    string
	AdditionalInfo    any // JSON-serialized if non-nil
}

// Raise creates a new alarm or updates a correlated one. Returns alarm_id.
func (m *Manager) Raise(in RaiseInput) (int64, error) {
	if _, ok := alarmTypes[in.AlarmType]; !ok {
		m.log.Warnf("Invalid alarm type %q", in.AlarmType)
	}
	if _, ok := severityOrder[in.PerceivedSeverity]; !ok || in.PerceivedSeverity == SeverityCleared {
		m.log.Warnf("Invalid severity %q for Raise (use Clear)", in.PerceivedSeverity)
	}

	info := ""
	if in.AdditionalInfo != nil {
		if b, err := json.Marshal(in.AdditionalInfo); err == nil {
			info = string(b)
		}
	}

	key := correlationKey(in.ManagedObject, in.ProbableCause, in.SpecificProblem)
	now := nowSec()

	m.mu.Lock()
	existing, ok := m.active[key]
	var alarm *Alarm
	// Track whether the operator-visible state actually changed. The
	// alarm is correlated by its (managedObject, probableCause,
	// specificProblem) tuple — repeated raises with the same severity
	// bump a counter on the existing record but do NOT generate a
	// distinct alarm event. Logging each re-raise as "ALARM …" floods
	// operators when the underlying fault flaps or fans out (e.g.
	// multiple cleanup goroutines all firing for the same SCTP
	// association loss). See TS 28.532 §11.2a (Generic fault
	// supervision) cited in the package header for the contract.
	prevSeverity := ""
	if ok {
		prevSeverity = existing.PerceivedSeverity
		existing.PerceivedSeverity = in.PerceivedSeverity
		existing.AdditionalText = in.AdditionalText
		existing.AdditionalInfo = info
		existing.LastRaised = now
		existing.RaiseCount++
		alarm = existing
	} else {
		m.seq++
		alarm = &Alarm{
			AlarmID:           m.seq,
			ManagedObject:     in.ManagedObject,
			AlarmType:         in.AlarmType,
			ProbableCause:     in.ProbableCause,
			PerceivedSeverity: in.PerceivedSeverity,
			SpecificProblem:   in.SpecificProblem,
			AdditionalText:    in.AdditionalText,
			AdditionalInfo:    info,
			EventTime:         now,
			LastRaised:        now,
			AckState:          "Unacknowledged",
			RaiseCount:        1,
		}
		m.active[key] = alarm
	}
	snap := *alarm
	m.mu.Unlock()

	if err := m.persist(&snap); err != nil {
		return 0, err
	}
	// New alarm or severity escalation/de-escalation → operator-visible
	// event, log at WARN. Same-severity re-raise → silent counter bump.
	if !ok || prevSeverity != in.PerceivedSeverity {
		m.log.Warnf("ALARM [%s] %s: %s — %s (%s) id=%d",
			in.PerceivedSeverity, in.ManagedObject, in.SpecificProblem,
			in.ProbableCause, in.AlarmType, snap.AlarmID)
	}
	return snap.AlarmID, nil
}

// Clear resolves a correlated alarm. Returns alarm_id (0 if not found).
func (m *Manager) Clear(managedObject, probableCause, specificProblem, additionalText string) (int64, error) {
	key := correlationKey(managedObject, probableCause, specificProblem)
	now := nowSec()

	m.mu.Lock()
	alarm, ok := m.active[key]
	if !ok {
		m.mu.Unlock()
		return 0, nil
	}
	delete(m.active, key)
	alarm.PerceivedSeverity = SeverityCleared
	alarm.ClearTime = &now
	if additionalText != "" {
		alarm.AdditionalText = additionalText
	}
	snap := *alarm
	m.mu.Unlock()

	if err := m.persist(&snap); err != nil {
		return 0, err
	}
	m.log.Infof("ALARM CLEARED: %s — %s (id=%d)", managedObject, specificProblem, snap.AlarmID)
	return snap.AlarmID, nil
}

// ClearByID clears a specific alarm by id (manual clear from GUI).
func (m *Manager) ClearByID(id int64, additionalText string) (bool, error) {
	now := nowSec()
	m.mu.Lock()
	var found *Alarm
	var foundKey string
	for k, a := range m.active {
		if a.AlarmID == id {
			found = a
			foundKey = k
			break
		}
	}
	if found == nil {
		m.mu.Unlock()
		return false, nil
	}
	delete(m.active, foundKey)
	found.PerceivedSeverity = SeverityCleared
	found.ClearTime = &now
	if additionalText != "" {
		found.AdditionalText = additionalText
	}
	snap := *found
	m.mu.Unlock()

	if err := m.persist(&snap); err != nil {
		return false, err
	}
	m.log.Infof("ALARM CLEARED (manual): %s — %s (id=%d)",
		snap.ManagedObject, snap.SpecificProblem, snap.AlarmID)
	return true, nil
}

// Ack marks an alarm acknowledged. user defaults to "operator" when empty.
func (m *Manager) Ack(id int64, user string) (bool, error) {
	if user == "" {
		user = "operator"
	}
	now := nowSec()

	m.mu.Lock()
	for _, a := range m.active {
		if a.AlarmID == id {
			a.AckState = "Acknowledged"
			a.AckTime = &now
			a.AckUser = user
			snap := *a
			m.mu.Unlock()
			if err := m.persist(&snap); err != nil {
				return false, err
			}
			m.log.Infof("ALARM ACK: id=%d by %s", id, user)
			return true, nil
		}
	}
	m.mu.Unlock()

	// Try to ack a cleared alarm directly in DB.
	db, err := engine.Open()
	if err != nil {
		return false, err
	}
	res, err := db.Exec(
		`UPDATE alarms SET ack_state='Acknowledged', ack_time=?, ack_user=? WHERE alarm_id=?`,
		now, user, id)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	if n > 0 {
		m.log.Infof("ALARM ACK: id=%d by %s", id, user)
	}
	return n > 0, nil
}

// ActiveAlarms returns a snapshot of all non-cleared alarms, sorted by
// severity (ascending) then last_raised (descending).
func (m *Manager) ActiveAlarms() []Alarm {
	m.mu.Lock()
	out := make([]Alarm, 0, len(m.active))
	for _, a := range m.active {
		out = append(out, *a)
	}
	m.mu.Unlock()
	sort.Slice(out, func(i, j int) bool {
		si, sj := severityOrder[out[i].PerceivedSeverity], severityOrder[out[j].PerceivedSeverity]
		if si != sj {
			return si < sj
		}
		return out[i].LastRaised > out[j].LastRaised
	})
	return out
}

// History returns up to limit rows from DB, newest first.
// If includeActive=false only Cleared alarms are returned.
func (m *Manager) History(limit int, includeActive bool) ([]Alarm, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 200
	}
	q := `SELECT alarm_id, managed_object, alarm_type, probable_cause,
                 perceived_severity, specific_problem, additional_text,
                 additional_info, event_time, last_raised, clear_time,
                 ack_state, ack_time, ack_user, raise_count
          FROM alarms`
	if !includeActive {
		q += ` WHERE perceived_severity='Cleared'`
	}
	q += ` ORDER BY alarm_id DESC LIMIT ?`
	rows, err := db.Query(q, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Alarm
	for rows.Next() {
		var a Alarm
		var ct, at sql.NullFloat64
		var au sql.NullString
		if err := rows.Scan(
			&a.AlarmID, &a.ManagedObject, &a.AlarmType, &a.ProbableCause,
			&a.PerceivedSeverity, &a.SpecificProblem, &a.AdditionalText,
			&a.AdditionalInfo, &a.EventTime, &a.LastRaised, &ct,
			&a.AckState, &at, &au, &a.RaiseCount,
		); err != nil {
			return nil, err
		}
		if ct.Valid {
			v := ct.Float64
			a.ClearTime = &v
		}
		if at.Valid {
			v := at.Float64
			a.AckTime = &v
		}
		a.AckUser = au.String
		out = append(out, a)
	}
	return out, rows.Err()
}

// Counts aggregates active alarms by severity. Includes a "total" field.
func (m *Manager) Counts() map[string]int {
	out := map[string]int{
		SeverityCritical: 0, SeverityMajor: 0, SeverityMinor: 0,
		SeverityWarning: 0, SeverityIndeterminate: 0,
	}
	m.mu.Lock()
	for _, a := range m.active {
		if _, ok := out[a.PerceivedSeverity]; ok {
			out[a.PerceivedSeverity]++
		}
	}
	m.mu.Unlock()
	total := 0
	for _, v := range out {
		total += v
	}
	out["total"] = total
	return out
}

// ClearAll clears every active alarm (optionally filtered by managedObject).
// Returns the count cleared.
func (m *Manager) ClearAll(managedObject string) (int, error) {
	now := nowSec()
	m.mu.Lock()
	var toClear []*Alarm
	for k, a := range m.active {
		if managedObject == "" || a.ManagedObject == managedObject {
			a.PerceivedSeverity = SeverityCleared
			a.ClearTime = &now
			toClear = append(toClear, &Alarm{})
			*toClear[len(toClear)-1] = *a
			delete(m.active, k)
		}
	}
	m.mu.Unlock()
	for _, snap := range toClear {
		if err := m.persist(snap); err != nil {
			return 0, err
		}
	}
	if n := len(toClear); n > 0 {
		scope := ""
		if managedObject != "" {
			scope = " for " + managedObject
		}
		m.log.Infof("ALARM CLEAR-ALL: %d alarm(s) cleared%s", n, scope)
	}
	return len(toClear), nil
}

func (m *Manager) persist(a *Alarm) error {
	if !m.initialized {
		return nil
	}
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`
        INSERT INTO alarms (
            alarm_id, managed_object, alarm_type, probable_cause,
            perceived_severity, specific_problem, additional_text,
            additional_info, event_time, last_raised, clear_time,
            ack_state, ack_time, ack_user, raise_count
        ) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
        ON CONFLICT(alarm_id) DO UPDATE SET
            perceived_severity = excluded.perceived_severity,
            additional_text = excluded.additional_text,
            additional_info = excluded.additional_info,
            last_raised = excluded.last_raised,
            clear_time = excluded.clear_time,
            ack_state = excluded.ack_state,
            ack_time = excluded.ack_time,
            ack_user = excluded.ack_user,
            raise_count = excluded.raise_count`,
		a.AlarmID, a.ManagedObject, a.AlarmType, a.ProbableCause,
		a.PerceivedSeverity, a.SpecificProblem, a.AdditionalText,
		a.AdditionalInfo, a.EventTime, a.LastRaised, nullFloat(a.ClearTime),
		a.AckState, nullFloat(a.AckTime), nullStr(a.AckUser), a.RaiseCount,
	)
	if err != nil {
		return fmt.Errorf("persist alarm %d: %w", a.AlarmID, err)
	}
	return nil
}

// ── Convenience singleton helpers ───────────────────────────────────────

// Raise forwards to Default.Raise.
func Raise(in RaiseInput) (int64, error) { return Default.Raise(in) }

// Clear forwards to Default.Clear.
func Clear(managedObject, probableCause, specificProblem, additionalText string) (int64, error) {
	return Default.Clear(managedObject, probableCause, specificProblem, additionalText)
}

// ErrNotInitialized is returned by Raise if the manager has not been Init()ed.
var ErrNotInitialized = errors.New("fault manager not initialized")

func nowSec() float64 {
	return float64(time.Now().UnixNano()) / float64(time.Second)
}

func nullFloat(p *float64) any {
	if p == nil {
		return nil
	}
	return *p
}

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}
