// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// eval.go — packet-level firewall evaluation, IDS threshold/window
// enforcement, auto-block with TTL, and per-rule/per-signature hit
// counters.
//
// Spec anchors:
//
//   - TS 33.501 §5.9.1 — trust-boundary check; the firewall sits at
//     the IP perimeter and gates signalling NF entry points.
//   - TS 33.501 §5.9.4 — signalling-monitoring requirements; an IDS
//     signature that exceeds its (threshold, window_s) budget is the
//     escalated event the spec calls for.
//   - TS 33.501 §9.2/§9.3 — N2/N3 source IP authorisation; auto-block
//     keeps a tripped signature from continuing to load the AMF/UPF
//     while an operator investigates.
//
// Hit counters are kept in-memory only — they're observability for
// the operator panel, not durable state. They reset at process
// restart, which matches what the panel expects.
package core_security

import (
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// ── Firewall packet evaluation ───────────────────────────────────

// EvalResult is the outcome of EvalFirewall. Action is one of
// "allow" | "deny" | "rate_limit"; "" Rule means no rule matched
// and Action is the default "allow".
type EvalResult struct {
	Action string `json:"action"`
	Rule   string `json:"rule"`
}

// EvalFirewall walks rules in priority order (lowest priority value
// first), returning the first match. CIDR fields are matched with
// net.ParseCIDR (unless empty, which is "any"); port_range is parsed
// from "low-high" or a single port.
//
// Default when no rule matches is allow + "" — the evaluator is the
// allow-list when explicit deny rules are present and a default-deny
// when an "any/deny" rule sits at the highest priority.
func EvalFirewall(protocol, srcIP, dstIP string, port int) EvalResult {
	rules, err := ListFirewallRules()
	if err != nil {
		return EvalResult{Action: "allow"}
	}
	for _, r := range rules {
		if !r.Enabled {
			continue
		}
		if r.Protocol != "any" && r.Protocol != protocol {
			continue
		}
		if !cidrContains(r.SrcCIDR, srcIP) {
			continue
		}
		if !cidrContains(r.DstCIDR, dstIP) {
			continue
		}
		if !portRangeMatches(r.PortRange, port) {
			continue
		}
		bumpFirewallHit(r.Name, srcIP)
		return EvalResult{Action: r.Action, Rule: r.Name}
	}
	return EvalResult{Action: "allow"}
}

// cidrContains returns true if ip is in cidr; an empty cidr matches
// everything (the rule's "any" sentinel).
func cidrContains(cidr, ip string) bool {
	if cidr == "" {
		return true
	}
	_, ipNet, err := net.ParseCIDR(cidr)
	if err != nil {
		return false
	}
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return false
	}
	return ipNet.Contains(parsed)
}

// portRangeMatches accepts:
//   - ""        — match any port (rule's wildcard)
//   - "N"       — single port
//   - "lo-hi"   — inclusive range
//
// Anything else is treated as "no match" — the evaluator skips the
// rule rather than panic.
func portRangeMatches(spec string, port int) bool {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return true
	}
	if strings.Contains(spec, "-") {
		parts := strings.SplitN(spec, "-", 2)
		lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil || lo > hi {
			return false
		}
		return port >= lo && port <= hi
	}
	n, err := strconv.Atoi(spec)
	if err != nil {
		return false
	}
	return port == n
}

// ── Input validation (called from UpsertFirewallRule extension) ──

// ValidatePortRange returns nil if the spec is "" / "N" / "lo-hi"
// with hi >= lo and 0 <= port <= 65535. Surfaced as a 400 by the
// route layer so bad rules are rejected at write time instead of
// silently failing at eval time.
func ValidatePortRange(spec string) error {
	if spec == "" {
		return nil
	}
	if strings.Contains(spec, "-") {
		parts := strings.SplitN(spec, "-", 2)
		lo, err1 := strconv.Atoi(strings.TrimSpace(parts[0]))
		hi, err2 := strconv.Atoi(strings.TrimSpace(parts[1]))
		if err1 != nil || err2 != nil {
			return fmt.Errorf("port_range %q: not numeric", spec)
		}
		if lo < 0 || hi > 65535 || lo > hi {
			return fmt.Errorf("port_range %q: lo>hi or out of [0,65535]", spec)
		}
		return nil
	}
	n, err := strconv.Atoi(spec)
	if err != nil {
		return fmt.Errorf("port_range %q: not numeric", spec)
	}
	if n < 0 || n > 65535 {
		return fmt.Errorf("port_range %q: out of [0,65535]", spec)
	}
	return nil
}

// ValidateCIDR accepts "" or a parseable CIDR. Bare IPs are rejected
// — the rule schema is CIDR-only to keep matching unambiguous.
func ValidateCIDR(cidr string) error {
	if cidr == "" {
		return nil
	}
	if _, _, err := net.ParseCIDR(cidr); err != nil {
		return fmt.Errorf("cidr %q: %v", cidr, err)
	}
	return nil
}

// ── Hit counters (in-memory) ─────────────────────────────────────

// HitCounter is the panel-facing per-rule / per-signature aggregate.
type HitCounter struct {
	Count   int64  `json:"count"`
	LastIP  string `json:"last_ip"`
	LastHit string `json:"last_hit"`
}

var (
	hitMu     sync.RWMutex
	fwHits    = make(map[string]*HitCounter)
	idsHits   = make(map[string]*HitCounter)
	idsAlerts = make(map[string]*HitCounter) // promoted (post-threshold)
)

func bumpFirewallHit(rule, src string) {
	hitMu.Lock()
	c, ok := fwHits[rule]
	if !ok {
		c = &HitCounter{}
		fwHits[rule] = c
	}
	c.Count++
	c.LastIP = src
	c.LastHit = time.Now().UTC().Format(time.RFC3339)
	hitMu.Unlock()
}

func bumpIDSHit(sig, src string) {
	hitMu.Lock()
	c, ok := idsHits[sig]
	if !ok {
		c = &HitCounter{}
		idsHits[sig] = c
	}
	c.Count++
	c.LastIP = src
	c.LastHit = time.Now().UTC().Format(time.RFC3339)
	hitMu.Unlock()
}

func bumpIDSAlert(sig, src string) {
	hitMu.Lock()
	c, ok := idsAlerts[sig]
	if !ok {
		c = &HitCounter{}
		idsAlerts[sig] = c
	}
	c.Count++
	c.LastIP = src
	c.LastHit = time.Now().UTC().Format(time.RFC3339)
	hitMu.Unlock()
}

// FirewallHits returns a snapshot of per-rule match counters.
func FirewallHits() map[string]HitCounter {
	hitMu.RLock()
	defer hitMu.RUnlock()
	out := make(map[string]HitCounter, len(fwHits))
	for k, v := range fwHits {
		out[k] = *v
	}
	return out
}

// IDSHits returns per-signature raw hit counts AND post-threshold
// promoted-alert counts, keyed by signature name. Both maps share
// the same key space.
func IDSHits() (raw, alerts map[string]HitCounter) {
	hitMu.RLock()
	defer hitMu.RUnlock()
	raw = make(map[string]HitCounter, len(idsHits))
	for k, v := range idsHits {
		raw[k] = *v
	}
	alerts = make(map[string]HitCounter, len(idsAlerts))
	for k, v := range idsAlerts {
		alerts[k] = *v
	}
	return
}

// ResetHits clears both firewall and IDS counters. Used by the panel
// "reset stats" button and by tests.
func ResetHits() {
	hitMu.Lock()
	fwHits = make(map[string]*HitCounter)
	idsHits = make(map[string]*HitCounter)
	idsAlerts = make(map[string]*HitCounter)
	hitMu.Unlock()
}

// ── IDS threshold/window enforcement ─────────────────────────────

// idsBucket holds the sliding-window event timestamps for one
// (signature, sourceIP) pair. We keep the full slice because the
// signature panel wants to show "5/10 in window" — a bare counter
// can't answer that without the timestamps.
type idsBucket struct {
	hits []time.Time
}

var (
	idsBucketMu sync.Mutex
	idsBuckets  = make(map[string]*idsBucket)
)

// idsBucketKey deterministically keys per (signature, source).
func idsBucketKey(sig, src string) string { return sig + "|" + src }

// recordIDSEvent appends now and prunes events older than window.
// Returns the count of events still inside the window — the caller
// compares against `threshold` to decide whether to escalate.
func recordIDSEvent(sig, src string, window time.Duration) int {
	now := time.Now()
	key := idsBucketKey(sig, src)
	idsBucketMu.Lock()
	defer idsBucketMu.Unlock()
	b, ok := idsBuckets[key]
	if !ok {
		b = &idsBucket{}
		idsBuckets[key] = b
	}
	cutoff := now.Add(-window)
	pruned := b.hits[:0]
	for _, t := range b.hits {
		if t.After(cutoff) {
			pruned = append(pruned, t)
		}
	}
	b.hits = append(pruned, now)
	return len(b.hits)
}

// ResetIDSBuckets clears the sliding-window state — used by tests
// to make threshold tests deterministic.
func ResetIDSBuckets() {
	idsBucketMu.Lock()
	idsBuckets = make(map[string]*idsBucket)
	idsBucketMu.Unlock()
}

// SignatureMatches returns true if the signature's pattern fits the
// (eventType, detail) payload. The pattern is interpreted as:
//
//   - exact match against eventType (legacy behaviour), or
//   - a substring match against detail (legacy), or
//   - if surrounded by /…/, a regular expression against detail.
//
// The /regex/ form is opt-in so existing literal patterns still work.
func SignatureMatches(s IDSSignature, eventType, detail string) bool {
	if s.Name == eventType {
		return true
	}
	if s.Pattern == "" {
		return false
	}
	if len(s.Pattern) >= 2 && s.Pattern[0] == '/' && s.Pattern[len(s.Pattern)-1] == '/' {
		re, err := regexp.Compile(s.Pattern[1 : len(s.Pattern)-1])
		if err != nil {
			return false
		}
		return re.MatchString(detail)
	}
	return strings.Contains(detail, s.Pattern)
}

// ── Auto-block with TTL ──────────────────────────────────────────

// BlockIPWithTTL adds the IP to the deny list with an expiry. ttl=0
// is permanent (same as BlockIP). The IP becomes immediately blocked
// — IsBlocked + CheckSignallingAccess gate on it.
func BlockIPWithTTL(ip, reason string, ttl time.Duration) {
	if ip == "" {
		return
	}
	if ttl <= 0 {
		BlockIP(ip, reason)
		return
	}
	expires := time.Now().UTC().Add(ttl).Format(time.RFC3339)
	sfMu.Lock()
	blockedIPs[ip] = reason
	sfMu.Unlock()
	_, _ = engine.Exec(`INSERT INTO security_blocked_ips
		(ip, reason, added_at, added_by, expires_at)
		VALUES (?,?, datetime('now'), 'ids', ?)
		ON CONFLICT(ip) DO UPDATE SET
			reason=excluded.reason,
			expires_at=excluded.expires_at`, ip, reason, expires)
	LogEvent("IP_BLOCKED_TTL", reason, ip, "", "WARNING",
		map[string]interface{}{"ttl_s": int(ttl.Seconds()), "expires_at": expires})
}

// SweepBlockedIPs removes rows whose expires_at has passed. Lazy
// callers (IsBlocked, ListBlockedIPs) also filter on the fly so the
// in-memory deny set never serves expired entries even between
// sweeps.
func SweepBlockedIPs() {
	rows, err := qRows(`SELECT ip, expires_at FROM security_blocked_ips
		WHERE expires_at IS NOT NULL AND expires_at != ''
		AND datetime(expires_at) <= datetime('now')`)
	if err != nil {
		return
	}
	for _, m := range rows {
		ip := asString(m["ip"])
		if ip == "" {
			continue
		}
		sfMu.Lock()
		delete(blockedIPs, ip)
		sfMu.Unlock()
		_, _ = engine.Exec("DELETE FROM security_blocked_ips WHERE ip=?", ip)
		LogEvent("IP_UNBLOCKED", "ttl expired", ip, "", "INFO", nil)
	}
}

// StartTTLSweeper kicks off a background goroutine that calls
// SweepBlockedIPs every interval. Idempotent — only one sweeper
// runs per process.
func StartTTLSweeper(interval time.Duration) {
	sweeperOnce.Do(func() {
		go func() {
			t := time.NewTicker(interval)
			for range t.C {
				SweepBlockedIPs()
			}
		}()
	})
}

var sweeperOnce sync.Once
