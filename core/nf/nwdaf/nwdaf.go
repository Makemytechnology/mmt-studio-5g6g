// Package nwdaf — Network Data Analytics Function (NWDAF).
//
// Spec anchors:
//
//   - TS 23.288 §6.1     Procedures for analytics exposure — the umbrella
//                         service definition for NWDAF consumers (request +
//                         subscribe). GetAnalytics() / Subscribe() below
//                         realise §6.1.2 (Analytics Request) and §6.1.1
//                         (Analytics Subscribe/Unsubscribe) respectively.
//   - TS 23.288 §6.1.1   Analytics Subscribe/Unsubscribe — drives the
//                         Subscribe() / Unsubscribe() / processSubscriptions()
//                         call loop.
//   - TS 23.288 §6.1.2   Analytics Request — drives the one-shot
//                         GetAnalytics() entry point.
//   - TS 23.288 §6.2     Procedures for Data Collection — the
//                         collectionLoop() runs the §6.2.2 cycle (data from
//                         NFs into nwdaf_data_points) every collectInterval.
//
// Deferred surfaces (PDFs not local; TODO(spec:) prose only):
//
//   - TODO(spec: TS 29.520, "Nnwdaf services Stage 3") — the canonical
//     OpenAPI surface for Nnwdaf_AnalyticsInfo and Nnwdaf_EventsSubscription.
//     The handlers here keep the same request/response shape but do not
//     implement the §5.x JSON schemas verbatim.
//   - TODO(spec: TS 23.288 §6.2.6.1, "Bulked Data Collection") — bulk
//     collection from multiple NFs in one request.
//   - TODO(spec: TS 23.288 §6.2.7) — Event Muting Mechanism. Not implemented.
package nwdaf

import (
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/nf/nwdaf/analytics"
	"github.com/mmt/mmt-studio-core/nf/nwdaf/collectors"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("nwdaf")

// Service is the NWDAF service singleton.
type Service struct {
	mu              sync.Mutex
	running         bool
	stopCh          chan struct{}
	collectInterval time.Duration
	dataCache       map[string][]analytics.DataPoint // analyticsID -> recent data points
	maxCachePoints  int
}

// NewService creates a new NWDAF service.
func NewService(collectIntervalSec int) *Service {
	if collectIntervalSec <= 0 {
		collectIntervalSec = 30
	}
	return &Service{
		collectInterval: time.Duration(collectIntervalSec) * time.Second,
		dataCache:       make(map[string][]analytics.DataPoint),
		maxCachePoints:  500,
	}
}

// Start starts the background collection + notification loop.
func (s *Service) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true
	s.stopCh = make(chan struct{})
	go s.collectionLoop()
	log.Infof("NWDAF started (collection interval=%s)", s.collectInterval)
}

// Stop stops the NWDAF service.
func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.stopCh)
	log.Infof("NWDAF stopped")
}

// GetAnalytics — Nnwdaf_AnalyticsInfo — request analytics on demand.
//
// Spec anchor: TS 23.288 §6.1.2 (Analytics Request). Consumer NF
// requests analytics for a specific Analytics ID, optionally scoped to a
// UE / DNN / slice.
//
// TODO(spec: TS 29.520 clause 5.1) — JSON-schema-faithful Stage 3 surface.
func (s *Service) GetAnalytics(analyticsID string, targetIMSI, targetDNN string, timeWindow int) analytics.AnalyticsResult {
	if !analytics.ValidAnalyticsIDs[analyticsID] {
		return analytics.AnalyticsResult{
			AnalyticsID: analyticsID,
			Result:      map[string]any{},
			Message:     "Unknown analytics ID: " + analyticsID,
		}
	}

	if timeWindow <= 0 {
		timeWindow = 300
	}

	// Get cached data points
	s.mu.Lock()
	points := make([]analytics.DataPoint, len(s.dataCache[analyticsID]))
	copy(points, s.dataCache[analyticsID])
	s.mu.Unlock()

	// Filter by scope
	if targetIMSI != "" {
		var filtered []analytics.DataPoint
		for _, p := range points {
			if p.IMSI == targetIMSI || p.IMSI == "" {
				filtered = append(filtered, p)
			}
		}
		points = filtered
	}
	if targetDNN != "" {
		var filtered []analytics.DataPoint
		for _, p := range points {
			if p.DNN == targetDNN || p.DNN == "" {
				filtered = append(filtered, p)
			}
		}
		points = filtered
	}

	// Compute
	result := analytics.ComputeAnalytics(analyticsID, points, timeWindow)

	// Store result in DB
	func() {
		defer func() { recover() }()
		db, err := engine.Open()
		if err != nil {
			return
		}
		now := float64(time.Now().Unix())
		scopeJSON, _ := json.Marshal(map[string]any{"imsi": targetIMSI, "dnn": targetDNN})
		resultJSON, _ := json.Marshal(result.Result)
		_, _ = db.Exec(`INSERT INTO nwdaf_analytics
			(analytics_id, target_period, scope_json, result_json, confidence, computed_at, valid_until)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			analyticsID, time.Duration(timeWindow)*time.Second,
			string(scopeJSON), string(resultJSON),
			result.Confidence, now, now+float64(timeWindow))
	}()

	return result
}

// Subscribe creates an analytics subscription.
//
// Spec anchor: TS 23.288 §6.1.1 (Analytics Subscribe/Unsubscribe).
// Consumer NF subscribes for continuous analytics.
//
// TODO(spec: TS 29.520 clause 5.2) — JSON-schema-faithful Stage 3 surface.
func (s *Service) Subscribe(consumerNF, analyticsID string, targetIMSI, targetDNN, targetSST string,
	callbackURL string, intervalSec int) string {
	if !analytics.ValidAnalyticsIDs[analyticsID] {
		return ""
	}

	b := make([]byte, 4)
	for i := range b {
		b[i] = "0123456789abcdef"[time.Now().UnixNano()%16]
		time.Sleep(time.Nanosecond)
	}
	subID := "nwdaf-sub-" + time.Now().Format("20060102150405")

	func() {
		defer func() { recover() }()
		db, err := engine.Open()
		if err != nil {
			return
		}
		_, err = db.Exec(`INSERT INTO nwdaf_subscriptions
			(sub_id, consumer_nf, analytics_id, target_imsi, target_dnn,
			 target_sst, callback_url, interval_sec, status, created_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'active', ?)`,
			subID, consumerNF, analyticsID, nilIfEmpty(targetIMSI), nilIfEmpty(targetDNN),
			nilIfEmpty(targetSST), nilIfEmpty(callbackURL), intervalSec, float64(time.Now().Unix()))
		if err != nil {
			log.Errorf("Subscription create failed: %v", err)
			subID = ""
		}
	}()

	if subID != "" {
		log.Infof("Subscription created: %s (consumer=%s, analytics=%s)", subID, consumerNF, analyticsID)
	}
	return subID
}

// Unsubscribe cancels an analytics subscription.
func (s *Service) Unsubscribe(subID string) bool {
	db, err := engine.Open()
	if err != nil {
		return false
	}
	_, err = db.Exec("UPDATE nwdaf_subscriptions SET status='cancelled' WHERE sub_id=?", subID)
	if err != nil {
		return false
	}
	log.Infof("Subscription cancelled: %s", subID)
	return true
}

// ListSubscriptions lists active subscriptions.
func (s *Service) ListSubscriptions() []map[string]any {
	db, err := engine.Open()
	if err != nil {
		return nil
	}
	rows, err := db.Query("SELECT * FROM nwdaf_subscriptions WHERE status='active' ORDER BY created_at")
	if err != nil {
		return nil
	}
	defer rows.Close()
	results, _ := scanAllRows(rows)
	return results
}

// GetRecentAnalytics returns recent analytics results from DB.
func (s *Service) GetRecentAnalytics(analyticsID string, limit int) []map[string]any {
	if limit <= 0 {
		limit = 20
	}
	db, err := engine.Open()
	if err != nil {
		return nil
	}

	var rows interface {
		Close() error
		Columns() ([]string, error)
		Next() bool
		Scan(dest ...any) error
	}

	if analyticsID != "" {
		rows, err = db.Query(
			"SELECT * FROM nwdaf_analytics WHERE analytics_id=? ORDER BY computed_at DESC LIMIT ?",
			analyticsID, limit)
	} else {
		rows, err = db.Query(
			"SELECT * FROM nwdaf_analytics ORDER BY computed_at DESC LIMIT ?", limit)
	}
	if err != nil {
		return nil
	}
	defer rows.Close()

	results, _ := scanAllRows(rows)

	// Parse JSON fields
	for i, r := range results {
		if rj, ok := r["result_json"].(string); ok && rj != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(rj), &parsed) == nil {
				results[i]["result_json"] = parsed
			}
		}
		if sj, ok := r["scope_json"].(string); ok && sj != "" {
			var parsed map[string]any
			if json.Unmarshal([]byte(sj), &parsed) == nil {
				results[i]["scope_json"] = parsed
			}
		}
	}

	return results
}

// Status returns a summary for diagnostics.
func (s *Service) Status() map[string]any {
	s.mu.Lock()
	total := 0
	for _, pts := range s.dataCache {
		total += len(pts)
	}
	s.mu.Unlock()
	return map[string]any{
		"cached_data_points": total,
		"analytics_ids":      len(analytics.ValidAnalyticsIDs),
	}
}

// ----------------------------------------------------------------
// Background collection
// ----------------------------------------------------------------

func (s *Service) collectionLoop() {
	ticker := time.NewTicker(s.collectInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.stopCh:
			return
		case <-ticker.C:
			func() {
				defer func() { recover() }()
				s.collectAll()
				s.processSubscriptions()
			}()
		}
	}
}

func (s *Service) collectAll() {
	allPoints := collectors.CollectAll()

	// Store in DB
	now := float64(time.Now().Unix())
	func() {
		defer func() { recover() }()
		db, err := engine.Open()
		if err != nil {
			return
		}
		for _, dp := range allPoints {
			collectedAt := dp.CollectedAt
			if collectedAt == 0 {
				collectedAt = now
			}
			_, _ = db.Exec(`INSERT INTO nwdaf_data_points
				(source_nf, analytics_id, imsi, dnn, data_json, collected_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				dp.SourceNF, dp.AnalyticsID, nilIfEmpty(dp.IMSI), nilIfEmpty(dp.DNN),
				dp.DataJSON, collectedAt)
		}
		// Prune old data points (keep last 24h)
		cutoff := now - 86400
		_, _ = db.Exec("DELETE FROM nwdaf_data_points WHERE collected_at < ?", cutoff)
	}()

	// Update cache
	s.mu.Lock()
	for _, dp := range allPoints {
		aid := dp.AnalyticsID
		s.dataCache[aid] = append(s.dataCache[aid], dp)
		if len(s.dataCache[aid]) > s.maxCachePoints {
			s.dataCache[aid] = s.dataCache[aid][len(s.dataCache[aid])-s.maxCachePoints:]
		}
	}
	s.mu.Unlock()
}

func (s *Service) processSubscriptions() {
	db, err := engine.Open()
	if err != nil {
		return
	}
	rows, err := db.Query("SELECT * FROM nwdaf_subscriptions WHERE status='active'")
	if err != nil {
		return
	}
	defer rows.Close()
	subs, _ := scanAllRows(rows)

	now := float64(time.Now().Unix())
	for _, sub := range subs {
		lastNotified := 0.0
		if v, ok := sub["last_notified"]; ok && v != nil {
			switch n := v.(type) {
			case float64:
				lastNotified = n
			case int64:
				lastNotified = float64(n)
			}
		}

		intervalSec := 60.0
		if v, ok := sub["interval_sec"].(int64); ok && v > 0 {
			intervalSec = float64(v)
		}

		if now-lastNotified < intervalSec {
			continue
		}

		analyticsID, _ := sub["analytics_id"].(string)
		targetIMSI, _ := sub["target_imsi"].(string)
		targetDNN, _ := sub["target_dnn"].(string)

		result := s.GetAnalytics(analyticsID, targetIMSI, targetDNN, 300)

		// Deliver notification
		callbackURL, _ := sub["callback_url"].(string)
		if callbackURL != "" {
			s.sendNotification(sub, result)
		}

		// Update last_notified
		subID, _ := sub["sub_id"].(string)
		func() {
			defer func() { recover() }()
			db2, err := engine.Open()
			if err != nil {
				return
			}
			_, _ = db2.Exec("UPDATE nwdaf_subscriptions SET last_notified=? WHERE sub_id=?", now, subID)
		}()
	}
}

func (s *Service) sendNotification(sub map[string]any, result analytics.AnalyticsResult) {
	callbackURL, _ := sub["callback_url"].(string)
	payload, _ := json.Marshal(map[string]any{
		"subscription_id": sub["sub_id"],
		"analytics_id":    sub["analytics_id"],
		"result":          result,
		"timestamp":       time.Now().Unix(),
	})
	req, err := http.NewRequest("POST", callbackURL, strings.NewReader(string(payload)))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Debugf("Notification failed: %s -> %s: %v", sub["sub_id"], callbackURL, err)
		return
	}
	defer resp.Body.Close()
	log.Debugf("Notification sent: %s -> %s (HTTP %d)", sub["sub_id"], callbackURL, resp.StatusCode)
}

// ----------------------------------------------------------------
// Helpers
// ----------------------------------------------------------------

func nilIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func scanAllRows(rows interface {
	Columns() ([]string, error)
	Next() bool
	Scan(dest ...any) error
}) ([]map[string]any, error) {
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

// DefaultService is the global singleton.
var DefaultService = NewService(30)
