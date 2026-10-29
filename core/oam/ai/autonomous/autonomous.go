// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package autonomous — AI-driven autonomous network operations.
//
// Go port of oam/ai/autonomous/self_healing.py + anomaly_response.py.
//
// Self-healing closed loop:
//  1. Health watchdog reports NF status (healthy/degraded/unhealthy)
//  2. Track consecutive degraded checks per NF
//  3. After threshold → preemptive action (flush stale state, raise alarm)
//  4. Verify health improved
//
// Anomaly response closed loop:
//  1. NWDAF ABNORMAL_BEHAVIOUR analytics detect anomaly
//  2. Known patterns → fast-path auto-action
//  3. Unknown patterns → AI classify + recommend (human-in-the-loop)
package autonomous

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

var log = logger.Get("ai.autonomous")

// ════════════════════════════════════════════════════════════
// Self-Healing
// ════════════════════════════════════════════════════════════

const degradedThreshold = 3 // consecutive checks before action

var (
	degradedMu     sync.Mutex
	degradedCounts = map[string]int{}
)

// HealingResult is the result of a self-healing evaluation.
type HealingResult struct {
	Actions     []HealingAction    `json:"actions"`
	DegradedNFs map[string]int     `json:"degraded_nfs"`
	Timestamp   float64            `json:"timestamp"`
}

// HealingAction describes a healing action taken.
type HealingAction struct {
	NF     string `json:"nf"`
	Action string `json:"action"`
	Reason string `json:"reason,omitempty"`
	Count  int    `json:"count,omitempty"`
}

// EvaluateAndHeal runs the self-healing closed loop.
// healthResult should have an "nfs" key mapping NF names to status dicts.
func EvaluateAndHeal(healthResult map[string]any) *HealingResult {
	degradedMu.Lock()
	defer degradedMu.Unlock()

	nfs, _ := healthResult["nfs"].(map[string]any)
	var actions []HealingAction

	for nfName, nfStatus := range nfs {
		statusMap, _ := nfStatus.(map[string]any)
		status, _ := statusMap["status"].(string)

		if status == "healthy" {
			delete(degradedCounts, nfName)
			continue
		}

		degradedCounts[nfName]++
		count := degradedCounts[nfName]

		if count < degradedThreshold {
			log.Debug("NF degraded", "nf", nfName, "count", count, "threshold", degradedThreshold)
			continue
		}

		log.Warn("NF degraded — initiating self-healing", "nf", nfName, "count", count)
		action := healNF(nfName)
		if action != nil {
			actions = append(actions, *action)
			degradedCounts[nfName] = 0 // reset after action
		}
	}

	// Build degraded snapshot
	degraded := make(map[string]int)
	for k, v := range degradedCounts {
		if v > 0 {
			degraded[k] = v
		}
	}

	return &HealingResult{
		Actions:     actions,
		DegradedNFs: degraded,
		Timestamp:   float64(time.Now().Unix()),
	}
}

// GetSelfHealingStatus returns the current degraded NF counts.
func GetSelfHealingStatus() map[string]int {
	degradedMu.Lock()
	defer degradedMu.Unlock()
	out := make(map[string]int)
	for k, v := range degradedCounts {
		out[k] = v
	}
	return out
}

func healNF(nfName string) *HealingAction {
	// Generic healing: raise alarm for any NF that hits the threshold.
	// Specific NF handlers (AMF context flush, SMF pool check, UPF thread check,
	// DB integrity) can be wired in when those NF packages are available.
	return &HealingAction{
		NF:     nfName,
		Action: "alarm_raised",
		Reason: fmt.Sprintf("%s sustained degradation — self-healing attempted", nfName),
	}
}

// ════════════════════════════════════════════════════════════
// Anomaly Response
// ════════════════════════════════════════════════════════════

// AnomalyAction describes an anomaly response action.
type AnomalyAction struct {
	Action         string         `json:"action"`
	Target         string         `json:"target,omitempty"`
	Params         map[string]any `json:"params,omitempty"`
	Reason         string         `json:"reason"`
	AutoExecuted   bool           `json:"auto_executed"`
	Classification string         `json:"classification,omitempty"`
}

// AnomalyResult is the result of anomaly response evaluation.
type AnomalyResult struct {
	AlertsProcessed int             `json:"alerts_processed"`
	ActionsTaken    []AnomalyAction `json:"actions_taken"`
	AIReasoning     string          `json:"ai_reasoning"`
	Timestamp       float64         `json:"timestamp"`
}

// EvaluateAndRespond runs the anomaly response closed loop.
// anomalyResult should have result.alerts as the input from NWDAF.
func EvaluateAndRespond(anomalyResult map[string]any) *AnomalyResult {
	result, _ := anomalyResult["result"].(map[string]any)
	alertsRaw, _ := result["alerts"].([]any)
	if len(alertsRaw) == 0 {
		return &AnomalyResult{AIReasoning: "No anomalies detected"}
	}

	var actions []AnomalyAction
	var reasoning []string

	for _, alertRaw := range alertsRaw {
		alert, _ := alertRaw.(map[string]any)
		alertType, _ := alert["type"].(string)
		severity, _ := alert["severity"].(string)
		detail, _ := alert["detail"].(string)

		// Rule-based fast path
		action := fastPathResponse(alertType, severity, detail)
		if action != nil {
			actions = append(actions, *action)
			reasoning = append(reasoning, fmt.Sprintf("%s: %s — %s", alertType, action.Action, action.Reason))
			continue
		}

		// Unknown pattern — attempt AI classification
		aiAction := aiClassifyAndRecommend(alert)
		if aiAction != nil {
			aiAction.AutoExecuted = false // human-in-the-loop for novel threats
			actions = append(actions, *aiAction)
			reasoning = append(reasoning, fmt.Sprintf("%s: AI recommends %s — %s",
				alertType, aiAction.Action, aiAction.Reason))
		}
	}

	if len(actions) > 0 {
		log.Info("anomaly response", "alerts", len(alertsRaw), "actions", len(actions))
	}

	return &AnomalyResult{
		AlertsProcessed: len(alertsRaw),
		ActionsTaken:    actions,
		AIReasoning:     joinStrings(reasoning, "\n"),
		Timestamp:       float64(time.Now().Unix()),
	}
}

func fastPathResponse(alertType, severity, detail string) *AnomalyAction {
	switch alertType {
	case "AUTH_FAILURE_SPIKE":
		return &AnomalyAction{
			Action:       "increase_ids_threshold",
			Target:       "AUTH_FAILURE_BURST",
			Params:       map[string]any{"threshold": 3, "window_sec": 30},
			Reason:       "Auth failure spike — tighten IDS detection window",
			AutoExecuted: true,
		}
	case "MAC_VERIFICATION_FAILURES":
		return &AnomalyAction{
			Action:       "raise_critical_alarm",
			Target:       "security/NAS",
			Params:       map[string]any{"severity": "Critical", "problem": "Possible replay/MITM attack"},
			Reason:       "MAC failures indicate possible active attack — critical alarm",
			AutoExecuted: true,
		}
	case "SESSION_FAILURE_SPIKE":
		return &AnomalyAction{
			Action:       "check_upf_health",
			Target:       "UPF",
			Params:       map[string]any{},
			Reason:       "Session failures may indicate UPF resource exhaustion",
			AutoExecuted: true,
		}
	}
	return nil
}

func aiClassifyAndRecommend(alert map[string]any) *AnomalyAction {
	// Import the AI router to classify unknown anomalies.
	// Uses lazy import pattern to avoid circular dependency.
	alertType, _ := alert["type"].(string)
	alertSev, _ := alert["severity"].(string)
	alertDetail, _ := alert["detail"].(string)

	prompt := fmt.Sprintf(`A network anomaly has been detected:
Type: %s
Severity: %s
Detail: %s

Based on this anomaly in a 5G SA Core network:
1. Classify the threat (attack, misconfiguration, capacity issue, or benign)
2. Recommend ONE specific action from: block_imsi, rate_limit_gnb, adjust_qos, raise_alarm, ignore
3. Explain your reasoning in one sentence.

Respond in JSON: {"classification": "...", "action": "...", "reason": "..."}`,
		alertType, alertSev, alertDetail)

	// Try to use the AI router if available
	if queryFn := getAIQueryFunc(); queryFn != nil {
		content := queryFn(prompt, "anomaly-classify")
		if content != "" {
			// Parse JSON from AI response
			re := regexp.MustCompile(`\{[^}]+\}`)
			match := re.FindString(content)
			if match != "" {
				var parsed map[string]string
				if json.Unmarshal([]byte(match), &parsed) == nil {
					return &AnomalyAction{
						Action:         orDefault(parsed["action"], "raise_alarm"),
						Reason:         orDefault(parsed["reason"], content),
						Classification: parsed["classification"],
					}
				}
			}
			return &AnomalyAction{
				Action:         "raise_alarm",
				Reason:         truncate(content, 200),
				Classification: "unknown",
			}
		}
	}
	return nil
}

// AIQueryFunc is a hook for the AI router query function.
// Set by the ai package to avoid circular imports.
var (
	aiQueryMu   sync.Mutex
	aiQueryFunc func(prompt, sessionID string) string
)

// SetAIQueryFunc registers the AI query function (called by ai package init).
func SetAIQueryFunc(fn func(prompt, sessionID string) string) {
	aiQueryMu.Lock()
	defer aiQueryMu.Unlock()
	aiQueryFunc = fn
}

func getAIQueryFunc() func(prompt, sessionID string) string {
	aiQueryMu.Lock()
	defer aiQueryMu.Unlock()
	return aiQueryFunc
}

// Status returns current autonomous status.
func Status() map[string]any {
	return map[string]any{
		"self_healing": map[string]any{"degraded_nfs": GetSelfHealingStatus()},
		"status":       "ready",
	}
}

// ── helpers ──

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func joinStrings(ss []string, sep string) string {
	if len(ss) == 0 {
		return ""
	}
	out := ss[0]
	for _, s := range ss[1:] {
		out += sep + s
	}
	return out
}
