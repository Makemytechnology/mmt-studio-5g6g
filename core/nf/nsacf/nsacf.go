// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package nsacf -- Network Slice Admission Control Function
// (TS 23.501 section 5.15.11, TS 23.501 section 5.15.12).
//
// Go port of nf/nsacf/. Manages per-slice UE admission quotas with:
//   - Priority-based admission
//   - Preemption (evict lower-priority UE when slice is full)
//   - Reservation (reserve N slots for high-priority UEs)
//   - UE Slice Maximum Bit Rate enforcement
//   - Audit logging
package nsacf

import (
	"database/sql"
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("nsacf")

// ================================================================
// NSACF Context (singleton)
// ================================================================

// sliceKey uniquely identifies a network slice.
type sliceKey struct {
	SST int
	SD  string
}

// sliceLimit holds per-slice admission configuration.
type sliceLimit struct {
	MaxUEs            int
	ReservedUEs       int
	PriorityThreshold int
	PreemptionEnabled bool
}

// Context is the singleton NSACF context -- per-slice admission tracking.
type Context struct {
	mu         sync.Mutex
	admissions map[sliceKey]map[string]bool // (sst,sd) -> set of IMSIs
	limits     map[sliceKey]*sliceLimit
	loaded     bool
}

var (
	nsacfOnce sync.Once
	nsacfCtx  *Context
)

// GetNSACF returns the singleton NSACF context.
func GetNSACF() *Context {
	nsacfOnce.Do(func() {
		nsacfCtx = &Context{
			admissions: make(map[sliceKey]map[string]bool),
			limits:     make(map[sliceKey]*sliceLimit),
		}
		nsacfCtx.loadFromDB()
	})
	return nsacfCtx
}

func (c *Context) loadFromDB() {
	defer func() { recover() }()

	// Load limits
	limits, err := GetAllSliceLimits()
	if err == nil {
		for _, lim := range limits {
			sst, _ := toInt(lim["sst"])
			sd, _ := lim["sd"].(string)
			if sd == "" {
				sd = "000000"
			}
			key := sliceKey{sst, sd}
			c.limits[key] = &sliceLimit{
				MaxUEs:            intVal(lim["max_ues"]),
				ReservedUEs:       intVal(lim["reserved_ues"]),
				PriorityThreshold: intVal(lim["priority_threshold"]),
				PreemptionEnabled: intVal(lim["preemption_enabled"]) != 0,
			}
		}
	}

	// Load admissions
	adms, err := ListAdmissions(nil, nil)
	if err == nil {
		for _, adm := range adms {
			sst, _ := toInt(adm["sst"])
			sd, _ := adm["sd"].(string)
			if sd == "" {
				sd = "000000"
			}
			imsi, _ := adm["imsi"].(string)
			key := sliceKey{sst, sd}
			if c.admissions[key] == nil {
				c.admissions[key] = make(map[string]bool)
			}
			c.admissions[key][imsi] = true
		}
	}

	totalAdm := 0
	for _, s := range c.admissions {
		totalAdm += len(s)
	}
	log.Infof("loaded %d slice limits, %d admission entries", len(c.limits), totalAdm)
	c.loaded = true
}

// ================================================================
// Admission Control (TS 23.501 section 5.15.11)
// ================================================================

// RequestAdmission requests admission for a UE to a network slice.
func (c *Context) RequestAdmission(imsi string, sst int, sd string) map[string]any {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	defer c.mu.Unlock()

	// Already admitted
	if c.admissions[key] != nil && c.admissions[key][imsi] {
		return map[string]any{"allowed": true, "reason": "already_admitted"}
	}

	lim := c.limits[key]
	if lim == nil {
		if c.admissions[key] == nil {
			c.admissions[key] = make(map[string]bool)
		}
		c.admissions[key][imsi] = true
		go persistAdmit(imsi, sst, sd, 0)
		return map[string]any{"allowed": true, "reason": "no_limit_configured"}
	}

	current := len(c.admissions[key])
	if current < lim.MaxUEs {
		if c.admissions[key] == nil {
			c.admissions[key] = make(map[string]bool)
		}
		c.admissions[key][imsi] = true
		go persistAdmit(imsi, sst, sd, 0)
		return map[string]any{"allowed": true, "reason": "capacity_available"}
	}

	return map[string]any{"allowed": false, "reason": "slice_full"}
}

// ReleaseAdmission releases a UE from a network slice.
func (c *Context) ReleaseAdmission(imsi string, sst int, sd string) bool {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.admissions[key] != nil && c.admissions[key][imsi] {
		delete(c.admissions[key], imsi)
		go persistRelease(imsi, sst, sd)
		log.Infof("released imsi=%s from sst=%d sd=%s", imsi, sst, sd)
		return true
	}
	return false
}

// GetSliceStatus returns admission status for a specific slice.
func (c *Context) GetSliceStatus(sst int, sd string) map[string]any {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	defer c.mu.Unlock()

	lim := c.limits[key]
	maxUEs := 0
	reserved := 0
	priThreshold := 0
	preemption := false
	if lim != nil {
		maxUEs = lim.MaxUEs
		reserved = lim.ReservedUEs
		priThreshold = lim.PriorityThreshold
		preemption = lim.PreemptionEnabled
	}
	current := len(c.admissions[key])
	avail := maxUEs - current
	if avail < 0 {
		avail = 0
	}

	return map[string]any{
		"sst":                 sst,
		"sd":                  sd,
		"current_ues":         current,
		"max_ues":             maxUEs,
		"available":           avail,
		"reserved_ues":        reserved,
		"priority_threshold":  priThreshold,
		"preemption_enabled":  preemption,
	}
}

// GetAllStatus returns admission status for all configured slices.
func (c *Context) GetAllStatus() []map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	var result []map[string]any
	for key, lim := range c.limits {
		current := len(c.admissions[key])
		avail := lim.MaxUEs - current
		if avail < 0 {
			avail = 0
		}
		result = append(result, map[string]any{
			"sst":                 key.SST,
			"sd":                  key.SD,
			"current_ues":         current,
			"max_ues":             lim.MaxUEs,
			"available":           avail,
			"reserved_ues":        lim.ReservedUEs,
			"priority_threshold":  lim.PriorityThreshold,
			"preemption_enabled":  lim.PreemptionEnabled,
		})
	}
	return result
}

// SetSliceLimit configures admission limit for a slice.
func (c *Context) SetSliceLimit(sst int, sd string, maxUEs, reservedUEs, priorityThreshold int, preemptionEnabled bool) {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	c.limits[key] = &sliceLimit{
		MaxUEs:            maxUEs,
		ReservedUEs:       reservedUEs,
		PriorityThreshold: priorityThreshold,
		PreemptionEnabled: preemptionEnabled,
	}
	c.mu.Unlock()

	preemptInt := 0
	if preemptionEnabled {
		preemptInt = 1
	}
	_ = UpsertSliceLimit(sst, sd, maxUEs, reservedUEs, priorityThreshold, preemptInt)
	log.Infof("set slice limit sst=%d sd=%s max=%d reserved=%d", sst, sd, maxUEs, reservedUEs)
}

// IsSliceFull checks if a slice has reached its UE admission limit.
func (c *Context) IsSliceFull(sst int, sd string) bool {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	defer c.mu.Unlock()
	lim := c.limits[key]
	if lim == nil {
		return false
	}
	return len(c.admissions[key]) >= lim.MaxUEs
}

// AdmitWithPriority admits a UE with a specific priority.
func (c *Context) AdmitWithPriority(imsi string, sst int, sd string, priority int) {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	if c.admissions[key] == nil {
		c.admissions[key] = make(map[string]bool)
	}
	c.admissions[key][imsi] = true
	c.mu.Unlock()
	go func() {
		_ = InsertAdmission(imsi, sst, sd, priority)
		LogAdmissionEvent(imsi, sst, sd, "admitted", fmt.Sprintf("priority=%d", priority))
	}()
}

// PreemptUE preempts (evicts) a UE from a slice.
func (c *Context) PreemptUE(imsi string, sst int, sd, reason string) {
	if sd == "" {
		sd = "000000"
	}
	key := sliceKey{sst, sd}
	c.mu.Lock()
	if c.admissions[key] != nil {
		delete(c.admissions[key], imsi)
	}
	c.mu.Unlock()
	go func() {
		_ = DeleteAdmission(imsi, sst, sd)
		LogAdmissionEvent(imsi, sst, sd, "preempted", reason)
	}()
	log.Infof("preempted imsi=%s from sst=%d sd=%s reason=%s", imsi, sst, sd, reason)
}

// ================================================================
// UE Slice MBR (TS 23.501 section 5.15.12)
// ================================================================

// SetUESliceMBR configures UE Slice Maximum Bit Rate.
func (c *Context) SetUESliceMBR(imsi string, sst int, sd string, mbrDLKbps, mbrULKbps int) {
	if sd == "" {
		sd = "000000"
	}
	_ = UpsertUESliceMBR(imsi, sst, sd, mbrDLKbps, mbrULKbps)
	log.Infof("set UE Slice MBR imsi=%s sst=%d sd=%s dl=%d ul=%d kbps", imsi, sst, sd, mbrDLKbps, mbrULKbps)
}

// GetUESliceMBR returns the UE Slice MBR for a specific UE/slice.
func (c *Context) GetUESliceMBR(imsi string, sst int, sd string) map[string]any {
	if sd == "" {
		sd = "000000"
	}
	return GetUESliceMBRRecord(imsi, sst, sd)
}

// EnforceSliceMBR evaluates whether the UE exceeds its Slice MBR.
func (c *Context) EnforceSliceMBR(imsi string, sst int, sd string, currentDLKbps, currentULKbps int) map[string]any {
	if sd == "" {
		sd = "000000"
	}
	mbr := GetUESliceMBRRecord(imsi, sst, sd)
	if mbr == nil {
		return map[string]any{"throttle": false, "direction": nil}
	}

	_ = UpdateUESliceMBRUsage(imsi, sst, sd, currentDLKbps, currentULKbps)

	mbrDL := intVal(mbr["mbr_dl_kbps"])
	mbrUL := intVal(mbr["mbr_ul_kbps"])

	exceedDL := mbrDL > 0 && currentDLKbps > mbrDL
	exceedUL := mbrUL > 0 && currentULKbps > mbrUL

	var direction any
	if exceedDL && exceedUL {
		direction = "both"
	} else if exceedDL {
		direction = "dl"
	} else if exceedUL {
		direction = "ul"
	}

	return map[string]any{
		"throttle":        exceedDL || exceedUL,
		"direction":       direction,
		"mbr_dl_kbps":     mbrDL,
		"mbr_ul_kbps":     mbrUL,
		"current_dl_kbps": currentDLKbps,
		"current_ul_kbps": currentULKbps,
	}
}

// ================================================================
// Policy evaluation
// ================================================================

// EvaluateAdmission evaluates whether a UE should be admitted to a slice.
func EvaluateAdmission(imsi string, sst int, sd string) map[string]any {
	if sd == "" {
		sd = "000000"
	}
	ctx := GetNSACF()
	priority := getUEPriority(imsi)
	status := ctx.GetSliceStatus(sst, sd)

	maxUEs := intVal(status["max_ues"])
	if maxUEs == 0 {
		ctx.AdmitWithPriority(imsi, sst, sd, priority)
		return map[string]any{
			"decision": "admit", "reason": "no_limit_configured",
			"preempted_imsi": nil, "priority": priority,
		}
	}

	// Already admitted
	key := sliceKey{sst, sd}
	ctx.mu.Lock()
	alreadyIn := ctx.admissions[key] != nil && ctx.admissions[key][imsi]
	ctx.mu.Unlock()
	if alreadyIn {
		return map[string]any{
			"decision": "admit", "reason": "already_admitted",
			"preempted_imsi": nil, "priority": priority,
		}
	}

	current := intVal(status["current_ues"])
	reserved := intVal(status["reserved_ues"])
	threshold := intVal(status["priority_threshold"])
	preemption, _ := status["preemption_enabled"].(bool)

	unreservedAvail := maxUEs - reserved - current
	if unreservedAvail < 0 {
		unreservedAvail = 0
	}
	reservedAvail := maxUEs - current
	if reservedAvail < 0 {
		reservedAvail = 0
	}

	if current < maxUEs {
		if unreservedAvail > 0 {
			ctx.AdmitWithPriority(imsi, sst, sd, priority)
			return map[string]any{
				"decision": "admit", "reason": "capacity_available",
				"preempted_imsi": nil, "priority": priority,
			}
		} else if reservedAvail > 0 && priority >= threshold {
			ctx.AdmitWithPriority(imsi, sst, sd, priority)
			return map[string]any{
				"decision": "admit", "reason": "admitted_from_reserved_pool",
				"preempted_imsi": nil, "priority": priority,
			}
		} else if reservedAvail > 0 {
			LogAdmissionEvent(imsi, sst, sd, "denied",
				fmt.Sprintf("priority=%d below threshold=%d", priority, threshold))
			return map[string]any{
				"decision": "deny",
				"reason":   fmt.Sprintf("only_reserved_slots_available (priority=%d < threshold=%d)", priority, threshold),
				"preempted_imsi": nil, "priority": priority,
			}
		}
	}

	// Slice full -- try preemption
	if preemption && priority >= threshold {
		victim := GetLowestPriorityAdmission(sst, sd)
		if victim != nil && intVal(victim["priority"]) < priority {
			victimIMSI, _ := victim["imsi"].(string)
			ctx.PreemptUE(victimIMSI, sst, sd,
				fmt.Sprintf("preempted_by=%s (prio %d > %d)", imsi, priority, intVal(victim["priority"])))
			ctx.AdmitWithPriority(imsi, sst, sd, priority)
			return map[string]any{
				"decision": "admit", "reason": "preempted_lower_priority_ue",
				"preempted_imsi": victimIMSI, "priority": priority,
			}
		}
	}

	LogAdmissionEvent(imsi, sst, sd, "denied", "slice_full")
	return map[string]any{
		"decision": "deny", "reason": "slice_full",
		"preempted_imsi": nil, "priority": priority,
	}
}

func getUEPriority(imsi string) int {
	defer func() { recover() }()
	db, err := engine.Open()
	if err != nil {
		return 0
	}
	var arpPriority int
	err = db.QueryRow(`
		SELECT s.arp_priority
		FROM service_bindings sb
		JOIN services s ON sb.service_id = s.id
		JOIN ue u ON sb.ue_id = u.id
		WHERE u.imsi = ?
		ORDER BY s.arp_priority ASC
		LIMIT 1`, imsi).Scan(&arpPriority)
	if err != nil {
		return 0
	}
	// ARP priority 1 = highest, 15 = lowest. Invert.
	p := 16 - arpPriority
	if p < 0 {
		p = 0
	}
	return p
}

// ================================================================
// DB CRUD operations
// ================================================================

func persistAdmit(imsi string, sst int, sd string, priority int) {
	_ = InsertAdmission(imsi, sst, sd, priority)
	LogAdmissionEvent(imsi, sst, sd, "admitted", "capacity_available")
}

func persistRelease(imsi string, sst int, sd string) {
	_ = DeleteAdmission(imsi, sst, sd)
	LogAdmissionEvent(imsi, sst, sd, "released", "ue_released")
}

// GetAllSliceLimits returns all slice limit configurations.
func GetAllSliceLimits() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM nsacf_slice_limits ORDER BY sst, sd")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAllRows(rows)
}

// UpsertSliceLimit creates or updates a slice admission limit.
func UpsertSliceLimit(sst int, sd string, maxUEs, reservedUEs, priorityThreshold, preemptionEnabled int) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT INTO nsacf_slice_limits (sst, sd, max_ues, reserved_ues, priority_threshold, preemption_enabled)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(sst, sd) DO UPDATE SET
			max_ues=excluded.max_ues,
			reserved_ues=excluded.reserved_ues,
			priority_threshold=excluded.priority_threshold,
			preemption_enabled=excluded.preemption_enabled`,
		sst, sd, maxUEs, reservedUEs, priorityThreshold, preemptionEnabled)
	return err
}

// UpdateSliceLimitByID updates a slice limit by its row id.
func UpdateSliceLimitByID(limitID int64, fields map[string]any) error {
	allowed := map[string]bool{
		"max_ues": true, "reserved_ues": true,
		"priority_threshold": true, "preemption_enabled": true,
	}
	var sets []string
	var vals []any
	for k, v := range fields {
		if allowed[k] {
			sets = append(sets, k+"=?")
			vals = append(vals, v)
		}
	}
	if len(sets) == 0 {
		return nil
	}
	vals = append(vals, limitID)
	db, err := engine.Open()
	if err != nil {
		return err
	}
	query := "UPDATE nsacf_slice_limits SET "
	for i, s := range sets {
		if i > 0 {
			query += ", "
		}
		query += s
	}
	query += " WHERE id=?"
	_, err = db.Exec(query, vals...)
	return err
}

// ListAdmissions lists admitted UEs, optionally filtered by slice.
func ListAdmissions(sst, sd interface{}) ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	query := "SELECT * FROM nsacf_admissions"
	var params []any
	var clauses []string
	if sst != nil {
		clauses = append(clauses, "sst=?")
		params = append(params, sst)
	}
	if sd != nil {
		clauses = append(clauses, "sd=?")
		params = append(params, sd)
	}
	if len(clauses) > 0 {
		query += " WHERE "
		for i, c := range clauses {
			if i > 0 {
				query += " AND "
			}
			query += c
		}
	}
	query += " ORDER BY admitted_at DESC"
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanAllRows(rows)
}

// InsertAdmission inserts an admission record.
func InsertAdmission(imsi string, sst int, sd string, priority int) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("INSERT OR IGNORE INTO nsacf_admissions (imsi, sst, sd, priority) VALUES (?, ?, ?, ?)",
		imsi, sst, sd, priority)
	return err
}

// DeleteAdmission removes an admission record.
func DeleteAdmission(imsi string, sst int, sd string) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM nsacf_admissions WHERE imsi=? AND sst=? AND sd=?", imsi, sst, sd)
	return err
}

// GetLowestPriorityAdmission returns the admitted UE with lowest priority.
func GetLowestPriorityAdmission(sst int, sd string) map[string]any {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	rows, err := db.Query(
		"SELECT * FROM nsacf_admissions WHERE sst=? AND sd=? ORDER BY priority ASC LIMIT 1", sst, sd)
	if err != nil {
		return nil
	}
	defer rows.Close()
	results, _ := scanAllRows(rows)
	if len(results) == 0 {
		return nil
	}
	return results[0]
}

// LogAdmissionEvent records an admission event in the audit log.
func LogAdmissionEvent(imsi string, sst int, sd, action, reason string) {
	defer func() { recover() }()
	db, err := engine.Open()
	if err != nil {
		return
	}
	_, _ = db.Exec("INSERT INTO nsacf_admission_log (imsi, sst, sd, action, reason) VALUES (?, ?, ?, ?, ?)",
		imsi, sst, sd, action, reason)
}

// GetAdmissionLog queries the admission audit log.
func GetAdmissionLog(sst, sd, imsi interface{}, limit int) []map[string]any {
	if limit <= 0 {
		limit = 200
	}
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	query := "SELECT * FROM nsacf_admission_log"
	var params []any
	var clauses []string
	if sst != nil {
		clauses = append(clauses, "sst=?")
		params = append(params, sst)
	}
	if sd != nil {
		clauses = append(clauses, "sd=?")
		params = append(params, sd)
	}
	if imsi != nil {
		clauses = append(clauses, "imsi=?")
		params = append(params, imsi)
	}
	if len(clauses) > 0 {
		query += " WHERE "
		for i, c := range clauses {
			if i > 0 {
				query += " AND "
			}
			query += c
		}
	}
	query += " ORDER BY created_at DESC LIMIT ?"
	params = append(params, limit)
	rows, err := db.Query(query, params...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	results, _ := scanAllRows(rows)
	return results
}

// ================================================================
// UE Slice MBR DB operations
// ================================================================

// UpsertUESliceMBR creates or updates UE Slice MBR.
func UpsertUESliceMBR(imsi string, sst int, sd string, mbrDLKbps, mbrULKbps int) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(`
		INSERT INTO nsacf_ue_slice_mbr (imsi, sst, sd, mbr_dl_kbps, mbr_ul_kbps)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(imsi, sst, sd) DO UPDATE SET
			mbr_dl_kbps=excluded.mbr_dl_kbps,
			mbr_ul_kbps=excluded.mbr_ul_kbps`,
		imsi, sst, sd, mbrDLKbps, mbrULKbps)
	return err
}

// GetUESliceMBRRecord returns UE Slice MBR for a specific UE/slice.
func GetUESliceMBRRecord(imsi string, sst int, sd string) map[string]any {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	rows, err := db.Query(
		"SELECT * FROM nsacf_ue_slice_mbr WHERE imsi=? AND sst=? AND sd=?", imsi, sst, sd)
	if err != nil {
		return nil
	}
	defer rows.Close()
	results, _ := scanAllRows(rows)
	if len(results) == 0 {
		return nil
	}
	return results[0]
}

// ListUESliceMBR lists UE Slice MBR records.
func ListUESliceMBR(imsi string) []map[string]any {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	var rows *sql.Rows
	if imsi != "" {
		rows, err = db.Query("SELECT * FROM nsacf_ue_slice_mbr WHERE imsi=? ORDER BY sst, sd", imsi)
	} else {
		rows, err = db.Query("SELECT * FROM nsacf_ue_slice_mbr ORDER BY imsi, sst, sd")
	}
	if err != nil {
		return nil
	}
	defer rows.Close()
	results, _ := scanAllRows(rows)
	return results
}

// UpdateUESliceMBRUsage updates current usage rates.
func UpdateUESliceMBRUsage(imsi string, sst int, sd string, currentDLKbps, currentULKbps int) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec(
		"UPDATE nsacf_ue_slice_mbr SET current_dl_kbps=?, current_ul_kbps=? WHERE imsi=? AND sst=? AND sd=?",
		currentDLKbps, currentULKbps, imsi, sst, sd)
	return err
}

// ================================================================
// Legacy API compatibility
// ================================================================

// Admit checks whether the UE can be admitted (legacy API).
func Admit(imsi string, sst int, sd string) error {
	result := EvaluateAdmission(imsi, sst, sd)
	if result["decision"] == "admit" {
		return nil
	}
	return fmt.Errorf("nsacf: %s", result["reason"])
}

// Release removes a UE's admission (legacy API).
func Release(imsi string, sst int, sd string) {
	GetNSACF().ReleaseAdmission(imsi, sst, sd)
}

// AdmissionCount returns the number of UEs currently admitted to a slice.
func AdmissionCount(sst int, sd string) (int64, error) {
	if sd == "" {
		sd = "000000"
	}
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	var n int64
	err = db.QueryRow("SELECT COUNT(*) FROM nsacf_admissions WHERE sst=? AND sd=?", sst, sd).Scan(&n)
	return n, err
}

// ================================================================
// helpers
// ================================================================

func scanAllRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan {
			ptrs[i] = &scan[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			continue
		}
		row := make(map[string]any, len(cols))
		for i, name := range cols {
			row[name] = scan[i]
		}
		out = append(out, row)
	}
	return out, nil
}

func intVal(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	}
	return 0
}

func toInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	}
	return 0, false
}
