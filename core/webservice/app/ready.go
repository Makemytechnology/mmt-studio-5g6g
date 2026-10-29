// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// ready — readiness flag exposed via /api/admin/sys-info.ready.
//
// The HTTP server comes up immediately after route registration so callers
// (notably the tester's reset_to_baseline poll loop) can observe the new
// boot_id without first waiting for all NFs to initialize — UPF DPDK EAL
// init alone can add 10-15 s of "connection refused" on top of the actual
// process restart cost.
//
// While HTTP is reachable, ready=false signals "process is up but NFs
// aren't yet wired"; the tester treats that the same as "still down"
// for the purposes of test-start gating. main() flips ready=true after
// the last NF init returns, at which point boot_id_changed AND
// ready==true together mean "fresh process, fully wired, safe to drive".
package app

import (
	"sync"
	"sync/atomic"
)

var ready atomic.Bool

// SetReady marks the service as fully initialized. Call once, after every
// NF (AMF, SMF, UDM, UPF, upfloop, IMS, …) has finished startup.
func SetReady() { ready.Store(true) }

// IsReady reports whether SetReady has been called.
func IsReady() bool { return ready.Load() }

// Runtime NGAP bind address — set once at AMF startup, read by sys-info
// so the Network Config GUI can render the actually-bound IP when
// network_config.amf_ip is empty (auto-pick path, TS 38.412 §7).
var (
	ngapMu       sync.RWMutex
	ngapBindAddr string
)

// SetNGAPBindAddr records the resolved "host:port" that the AMF SCTP
// listener is bound to. main() calls this with amfSvc.LocalAddr() right
// after amf.Start returns.
func SetNGAPBindAddr(addr string) {
	ngapMu.Lock()
	ngapBindAddr = addr
	ngapMu.Unlock()
}

// NGAPBindAddr returns the recorded NGAP bind address, or "" if unset.
func NGAPBindAddr() string {
	ngapMu.RLock()
	defer ngapMu.RUnlock()
	return ngapBindAddr
}
