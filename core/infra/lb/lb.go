// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package lb — Load Balancer for multi-instance deployments.
//
// Go port of infra/lb/. When lb_enabled=1 in infra_config, the LB proxies
// incoming SCTP + HTTP to a pool of AMF/webservice instances selected by
// the configured strategy (round_robin | least_conn).
//
// In monolithic mode (default) the LB is a no-op — all traffic goes to
// the local AMF/webservice directly.
package lb

import (
	"sync"
	"time"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Instance is a backend AMF/webservice endpoint.
type Instance struct {
	Addr        string    `json:"addr"`
	Healthy     bool      `json:"healthy"`
	ActiveConns int       `json:"active_conns"`
	LastCheck   time.Time `json:"last_check"`
}

// LB is the load balancer context.
type LB struct {
	mu        sync.RWMutex
	enabled   bool
	strategy  string // round_robin | least_conn
	instances []Instance
	rrIndex   int
}

// Default is the process-wide LB instance.
var Default = &LB{}

// Init reads the LB config from infra_config and populates the instance list.
func (lb *LB) Init() {
	log := logger.Get("infra.lb")
	cfg, err := crud.GetInfraConfig()
	if err != nil {
		return
	}
	enabled, _ := cfg["lb_enabled"].(int64)
	if enabled == 0 {
		return
	}
	lb.mu.Lock()
	defer lb.mu.Unlock()
	lb.enabled = true
	lb.strategy, _ = cfg["lb_strategy"].(string)
	if lb.strategy == "" {
		lb.strategy = "least_conn"
	}
	addrs, _ := cfg["lb_instances"].(string)
	if addrs != "" {
		for _, a := range splitComma(addrs) {
			lb.instances = append(lb.instances, Instance{Addr: a, Healthy: true})
		}
	}
	log.Infof("LB initialized strategy=%s instances=%d", lb.strategy, len(lb.instances))
}

// Pick returns the next backend instance per the configured strategy.
// Returns "" when the LB is disabled or no healthy instances exist.
func (lb *LB) Pick() string {
	lb.mu.Lock()
	defer lb.mu.Unlock()
	if !lb.enabled || len(lb.instances) == 0 {
		return ""
	}
	switch lb.strategy {
	case "round_robin":
		for range lb.instances {
			lb.rrIndex = (lb.rrIndex + 1) % len(lb.instances)
			if lb.instances[lb.rrIndex].Healthy {
				return lb.instances[lb.rrIndex].Addr
			}
		}
	case "least_conn":
		best := -1
		for i, inst := range lb.instances {
			if inst.Healthy && (best < 0 || inst.ActiveConns < lb.instances[best].ActiveConns) {
				best = i
			}
		}
		if best >= 0 {
			return lb.instances[best].Addr
		}
	}
	return ""
}

// Enabled reports whether the LB is active.
func (lb *LB) Enabled() bool {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return lb.enabled
}

// Status returns the instance list for the GUI panel.
func (lb *LB) Status() map[string]any {
	lb.mu.RLock()
	defer lb.mu.RUnlock()
	return map[string]any{
		"enabled":   lb.enabled,
		"strategy":  lb.strategy,
		"instances": lb.instances,
	}
}

func splitComma(s string) []string {
	var out []string
	for _, p := range []byte(s) {
		if p == ',' {
			continue
		}
	}
	// simple split
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == ',' {
			t := trimSpace(s[start:i])
			if t != "" {
				out = append(out, t)
			}
			start = i + 1
		}
	}
	if t := trimSpace(s[start:]); t != "" {
		out = append(out, t)
	}
	return out
}

func trimSpace(s string) string {
	i, j := 0, len(s)
	for i < j && (s[i] == ' ' || s[i] == '\t') {
		i++
	}
	for j > i && (s[j-1] == ' ' || s[j-1] == '\t') {
		j--
	}
	return s[i:j]
}
