// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// boot_id — a fresh-process identifier exposed via /api/admin/sys-info.
//
// Why: docker's `restart: unless-stopped` reuses the same container on
// process exit, so the container's hostname stays the same across a
// /api/admin/remove-db-file or /api/admin/restart cycle. Callers (tester
// runner) need a
// signal that genuinely changes per process start so they can detect
// when sa_core has come back from a reset. The boot_id is a random hex
// generated once at package init.
package app

import (
	"crypto/rand"
	"encoding/hex"
	"time"
)

var (
	bootID     = newBootID()
	bootUnixNs = time.Now().UnixNano()
)

func newBootID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// rand.Read on /dev/urandom shouldn't fail; if it does, fall back
		// to nanoseconds so the value is still unique per boot.
		return hex.EncodeToString([]byte(time.Now().Format("20060102T150405.000000000")))
	}
	return hex.EncodeToString(b)
}

// bootIDInit is a no-op called from route registration to guarantee the
// package-level vars are initialized before the first /api/admin/sys-info
// request is served. Go would handle this anyway, but the explicit call
// documents the ordering intent.
func bootIDInit() string {
	return bootID
}
