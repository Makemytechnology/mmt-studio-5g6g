// Package exposure — NWDAF analytics exposure to AFs (via NEF).
//
// Spec anchors:
//
//   - TS 23.288 §6.1.1.2  Analytics subscribe/unsubscribe by AFs via NEF —
//                         the subscribe/notify pattern realised here.
//   - TS 23.288 §6.1.2.2  Analytics request by AFs via NEF — the one-shot
//                         (LogQuery query_type='one_shot') case.
//   - TS 23.288 §6.1.3    Contents of Analytics Exposure — what each
//                         notification payload should include (drives the
//                         postNotification body).
//   - TS 29.522 §4.4      Northbound APIs at the NEF — exposure routes
//                         here mirror the Nnef_EventExposure shape.
//
// Deferred:
//
//   - TODO(spec: TS 29.522 clause 5, "Nnef_AnalyticsExposure JSON
//     schemas") — wire-level OpenAPI is not enforced; we keep an
//     internal map[string]any payload.
//   - TODO(spec: TS 23.288 §6.2.9, "User consent for analytics") — no
//     consent gate at the exposure boundary yet; CheckAnalyticsPermission
//     only enforces the per-consumer allow-list.
//
// Provides CRUD for exposure consumers, subscriptions, audit log,
// API-key authentication, and a background subscription notifier.
package exposure

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("nwdaf.exposure")

// ExposureTypes maps external exposure-API names (the Stage-3 query
// strings used by AFs) to the internal NWDAF analytics IDs from
// TS 23.288 §6.1.3 (Contents of Analytics Exposure).
var ExposureTypes = map[string]string{
	"ue_mobility":         "UE_MOBILITY",
	"ue_communication":    "UE_COMMUNICATION",
	"nf_load":             "NF_LOAD",
	"network_performance": "QOS_SUSTAINABILITY",
	"abnormal_behaviour":  "ABNORMAL_BEHAVIOUR",
	"qos_sustainability":  "QOS_SUSTAINABILITY",
	"pdu_session":         "PDU_SESSION",
	"slice_load":          "SLICE_LOAD",
}

// ----------------------------------------------------------------
// Consumers CRUD
// ----------------------------------------------------------------

// ListConsumers returns all exposure consumers.
func ListConsumers() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM nwdaf_exposure_consumers ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// GetConsumer returns a single consumer by ID.
func GetConsumer(consumerID int64) (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM nwdaf_exposure_consumers WHERE id=?", consumerID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results, _ := scanRows(rows)
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// GetConsumerByAPIKey looks up a consumer by API key.
func GetConsumerByAPIKey(apiKey string) (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM nwdaf_exposure_consumers WHERE api_key=? AND active=1", apiKey)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results, _ := scanRows(rows)
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// CreateConsumer inserts a new consumer. Returns the new row ID.
func CreateConsumer(name, callbackURL, apiKey string, allowedAnalytics []string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	allowedJSON, _ := json.Marshal(allowedAnalytics)
	result, err := db.Exec(
		"INSERT INTO nwdaf_exposure_consumers (name, callback_url, api_key, allowed_analytics) VALUES (?,?,?,?)",
		name, callbackURL, apiKey, string(allowedJSON),
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// DeleteConsumer deletes a consumer (cascades to subscriptions).
func DeleteConsumer(consumerID int64) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM nwdaf_exposure_consumers WHERE id=?", consumerID)
	return err
}

// ----------------------------------------------------------------
// Subscriptions CRUD
// ----------------------------------------------------------------

// ListSubscriptions returns all active subscriptions with consumer name.
func ListSubscriptions() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT s.*, c.name AS consumer_name
		FROM nwdaf_exposure_subscriptions s
		JOIN nwdaf_exposure_consumers c ON c.id = s.consumer_id
		ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// GetSubscription returns a single subscription by ID.
func GetSubscription(subID int64) (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query("SELECT * FROM nwdaf_exposure_subscriptions WHERE id=?", subID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	results, _ := scanRows(rows)
	if len(results) == 0 {
		return nil, nil
	}
	return results[0], nil
}

// CreateSubscription inserts a new subscription. Returns the new row ID.
func CreateSubscription(consumerID int64, analyticsType, targetType, targetID string,
	intervalS int, callbackURL string) (int64, error) {
	db, err := engine.Open()
	if err != nil {
		return 0, err
	}
	result, err := db.Exec(
		`INSERT INTO nwdaf_exposure_subscriptions
		 (consumer_id, analytics_type, target_type, target_id, interval_s, callback_url)
		 VALUES (?,?,?,?,?,?)`,
		consumerID, analyticsType, targetType, targetID, intervalS, callbackURL,
	)
	if err != nil {
		return 0, err
	}
	return result.LastInsertId()
}

// UpdateSubscriptionNotified updates last_notified_at for a subscription.
func UpdateSubscriptionNotified(subID int64, timestamp string) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("UPDATE nwdaf_exposure_subscriptions SET last_notified_at=? WHERE id=?",
		timestamp, subID)
	return err
}

// DeleteSubscription deletes a subscription.
func DeleteSubscription(subID int64) error {
	db, err := engine.Open()
	if err != nil {
		return err
	}
	_, err = db.Exec("DELETE FROM nwdaf_exposure_subscriptions WHERE id=?", subID)
	return err
}

// GetActiveSubscriptions returns all active subscriptions for notification processing.
func GetActiveSubscriptions() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT s.*, c.name AS consumer_name, c.api_key
		FROM nwdaf_exposure_subscriptions s
		JOIN nwdaf_exposure_consumers c ON c.id = s.consumer_id
		WHERE s.active = 1 AND c.active = 1
		ORDER BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// ----------------------------------------------------------------
// Audit Log
// ----------------------------------------------------------------

// LogQuery inserts an audit log entry.
func LogQuery(consumerID *int64, analyticsType, queryType string, responseCode int) {
	db, err := engine.Open()
	if err != nil {
		return
	}
	_, _ = db.Exec(
		`INSERT INTO nwdaf_exposure_log (consumer_id, analytics_type, query_type, response_code) VALUES (?,?,?,?)`,
		consumerID, analyticsType, queryType, responseCode,
	)
}

// GetLog returns recent audit log entries.
func GetLog(limit int) ([]map[string]any, error) {
	if limit <= 0 {
		limit = 50
	}
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`
		SELECT l.*, c.name AS consumer_name
		FROM nwdaf_exposure_log l
		LEFT JOIN nwdaf_exposure_consumers c ON c.id = l.consumer_id
		ORDER BY l.id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

// GetStats returns exposure statistics.
func GetStats() (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	queryInt := func(q string) int {
		var n int
		_ = db.QueryRow(q).Scan(&n)
		return n
	}
	return map[string]any{
		"active_consumers":          queryInt("SELECT COUNT(*) FROM nwdaf_exposure_consumers WHERE active=1"),
		"active_subscriptions":      queryInt("SELECT COUNT(*) FROM nwdaf_exposure_subscriptions WHERE active=1"),
		"total_queries":             queryInt("SELECT COUNT(*) FROM nwdaf_exposure_log"),
		"one_shot_queries":          queryInt("SELECT COUNT(*) FROM nwdaf_exposure_log WHERE query_type='one_shot'"),
		"subscription_notifications": queryInt("SELECT COUNT(*) FROM nwdaf_exposure_log WHERE query_type='subscription'"),
	}, nil
}

// ----------------------------------------------------------------
// Auth helpers
// ----------------------------------------------------------------

// GenerateAPIKey generates a random 32-char hex API key for a consumer.
func GenerateAPIKey() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// ValidateAPIKey validates an API key and returns the consumer record.
func ValidateAPIKey(apiKey string) (map[string]any, error) {
	if apiKey == "" {
		return nil, nil
	}
	consumer, err := GetConsumerByAPIKey(apiKey)
	if err != nil || consumer == nil {
		return nil, err
	}
	active, _ := consumer["active"].(int64)
	if active == 0 {
		return nil, nil
	}
	return consumer, nil
}

// CheckAnalyticsPermission checks whether a consumer is authorized for a specific analytics type.
func CheckAnalyticsPermission(consumer map[string]any, analyticsType string) bool {
	allowed, _ := consumer["allowed_analytics"].(string)
	if allowed == "" {
		return true // empty = all allowed
	}
	var allowedList []string
	if err := json.Unmarshal([]byte(allowed), &allowedList); err != nil {
		// Try comma-separated
		for _, a := range strings.Split(allowed, ",") {
			if s := strings.TrimSpace(a); s != "" {
				allowedList = append(allowedList, s)
			}
		}
	}
	if len(allowedList) == 0 {
		return true
	}
	for _, a := range allowedList {
		if a == analyticsType {
			return true
		}
	}
	return false
}

// ----------------------------------------------------------------
// Subscription Manager (background notifier)
// ----------------------------------------------------------------

// SubscriptionManager manages periodic analytics notification delivery.
type SubscriptionManager struct {
	checkInterval time.Duration
	mu            sync.Mutex
	running       bool
	stopCh        chan struct{}
}

// NewSubscriptionManager creates a new manager.
func NewSubscriptionManager(checkIntervalSec int) *SubscriptionManager {
	if checkIntervalSec <= 0 {
		checkIntervalSec = 10
	}
	return &SubscriptionManager{
		checkInterval: time.Duration(checkIntervalSec) * time.Second,
	}
}

// Start starts the background notification loop.
func (m *SubscriptionManager) Start() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.running {
		return
	}
	m.running = true
	m.stopCh = make(chan struct{})
	go m.loop()
	log.Infof("Exposure subscription manager started (check_interval=%s)", m.checkInterval)
}

// Stop stops the background notification loop.
func (m *SubscriptionManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	close(m.stopCh)
	log.Infof("Exposure subscription manager stopped")
}

func (m *SubscriptionManager) loop() {
	ticker := time.NewTicker(m.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stopCh:
			return
		case <-ticker.C:
			m.CheckAndNotify()
		}
	}
}

// CheckAndNotify checks all active subscriptions and sends notifications where due.
func (m *SubscriptionManager) CheckAndNotify() {
	subs, err := GetActiveSubscriptions()
	if err != nil {
		log.Debugf("Failed to fetch active subscriptions: %v", err)
		return
	}

	now := time.Now().UTC()

	for _, sub := range subs {
		lastStr, _ := sub["last_notified_at"].(string)
		var last time.Time
		if lastStr != "" {
			last, _ = time.Parse(time.RFC3339, lastStr)
		}
		if last.IsZero() {
			last = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
		}

		intervalS := int64(60)
		if v, ok := sub["interval_s"].(int64); ok && v > 0 {
			intervalS = v
		}

		elapsed := now.Sub(last).Seconds()
		if elapsed < float64(intervalS) {
			continue
		}

		// POST to callback
		callbackURL, _ := sub["callback_url"].(string)
		code := 200
		if callbackURL != "" {
			code = postNotification(sub, nil, callbackURL)
		}

		nowISO := now.Format(time.RFC3339)

		subID, _ := sub["id"].(int64)
		_ = UpdateSubscriptionNotified(subID, nowISO)

		consumerID, _ := sub["consumer_id"].(int64)
		analyticsType, _ := sub["analytics_type"].(string)
		LogQuery(&consumerID, analyticsType, "subscription", code)
		_ = code
	}
}

func postNotification(sub map[string]any, result any, callbackURL string) int {
	payload, _ := json.Marshal(map[string]any{
		"subscription_id": sub["id"],
		"consumer":        sub["consumer_name"],
		"analytics_type":  sub["analytics_type"],
		"result":          result,
		"timestamp":       time.Now().UTC().Format(time.RFC3339),
	})

	req, err := http.NewRequest("POST", callbackURL, strings.NewReader(string(payload)))
	if err != nil {
		return 0
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Debugf("Exposure notification failed: sub=%v -> %s: %v", sub["id"], callbackURL, err)
		return 0
	}
	defer resp.Body.Close()
	return resp.StatusCode
}

// ----------------------------------------------------------------
// helpers
// ----------------------------------------------------------------

func scanRows(rows interface {
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

// ExposureSubMgr is the global subscription manager singleton.
var ExposureSubMgr = NewSubscriptionManager(10)

// SortedExposureTypes returns sorted exposure type names.
func SortedExposureTypes() []string {
	keys := make([]string, 0, len(ExposureTypes))
	for k := range ExposureTypes {
		keys = append(keys, k)
	}
	// Simple sort
	for i := 0; i < len(keys); i++ {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

// TypeInfo describes a single exposure analytics type.
type TypeInfo struct {
	Type        string `json:"type"`
	InternalID  string `json:"internal_id"`
	Description string `json:"description"`
}

// ListExposureTypes lists supported exposure analytics types.
func ListExposureTypes() []TypeInfo {
	var types []TypeInfo
	for _, name := range SortedExposureTypes() {
		desc := strings.ReplaceAll(name, "_", " ")
		desc = strings.Title(desc) //nolint:staticcheck
		types = append(types, TypeInfo{
			Type:        name,
			InternalID:  ExposureTypes[name],
			Description: desc,
		})
	}
	return types
}

func init() {
	// Ensure the fmt package is used (for error formatting).
	_ = fmt.Sprintf
}
