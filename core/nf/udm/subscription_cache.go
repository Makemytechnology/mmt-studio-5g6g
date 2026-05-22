// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// UE-AMBR (subscription) in-memory cache.
//
// TS 23.501 §5.7.3 UE-AMBR is stored on the ue row and read by SMF on
// every PDU session establish. A per-session SQL round-trip becomes
// the hot-path bottleneck at 100k UEs; cache the table like we do for
// auth / APN / service-bindings. Refreshed on POST /api/ue/subscription
// (and siblings), loaded once at startup.
package udm

import (
	"sync"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// AMBR mirrors crud.SubscriptionAMBR in kbps but kept local so higher
// layers don't import crud directly.
type AMBR struct {
	UplinkKbps   int64
	DownlinkKbps int64
}

var (
	subMu    sync.RWMutex
	subCache map[string]AMBR
)

func init() { subCache = make(map[string]AMBR) }

// LoadSubscriptionCache fills subCache with every ue.ambr_*_kbps row.
// Called once at startup; safe to re-run to resync.
func LoadSubscriptionCache() error {
	log := logger.Get("udm.subscription")
	db, err := engine.Open()
	if err != nil {
		return err
	}
	rows, err := db.Query(`SELECT imsi, ambr_ul_kbps, ambr_dl_kbps FROM ue`)
	if err != nil {
		return err
	}
	defer rows.Close()
	m := make(map[string]AMBR, 1024)
	for rows.Next() {
		var imsi string
		var ul, dl int64
		if err := rows.Scan(&imsi, &ul, &dl); err != nil {
			continue
		}
		m[imsi] = AMBR{UplinkKbps: ul, DownlinkKbps: dl}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	subMu.Lock()
	subCache = m
	subMu.Unlock()
	log.Infof("Subscription (UE-AMBR) cache loaded: %d subscriber(s)", len(m))
	return nil
}

// ReloadSubscription re-reads a single ue row. Call after POST
// /api/ue/subscription so UE-AMBR edits take effect on the next
// PDU session without a restart.
func ReloadSubscription(imsi string) error {
	sub, err := crud.SubscriptionGetByIMSI(imsi)
	if err != nil {
		return err
	}
	subMu.Lock()
	defer subMu.Unlock()
	if sub == nil {
		delete(subCache, imsi)
		return nil
	}
	subCache[imsi] = AMBR{
		UplinkKbps:   sub.AMBR.UplinkKbps,
		DownlinkKbps: sub.AMBR.DownlinkKbps,
	}
	return nil
}

// DropSubscription removes a subscriber from the cache (DELETE path).
func DropSubscription(imsi string) {
	subMu.Lock()
	defer subMu.Unlock()
	delete(subCache, imsi)
}

// GetSubscriptionAMBR returns the cached AMBR for an IMSI, and whether
// a row exists. Zero values mean "unlimited" per TS 23.501 §5.7.3.
func GetSubscriptionAMBR(imsi string) (AMBR, bool) {
	subMu.RLock()
	defer subMu.RUnlock()
	a, ok := subCache[imsi]
	return a, ok
}
