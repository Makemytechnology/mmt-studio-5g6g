// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package udm — in-memory auth cache.
//
// AUSF hits UDM on every UE registration to read {OP, K, AMF} and to
// bump SQN. At scale (10k–100k UEs) those three reads + one write per
// auth saturate the single SQLite connection and serialise every
// registration. The cache below keeps the whole ue_auth_data table in
// memory; edits via the GUI go through ReloadAuth(imsi) so new values
// land immediately, and SQN bumps are batched into a background
// flusher (sqn_flusher.go) instead of a per-auth write.
package udm

import (
	"sync"

	"github.com/mmt/mmt-studio-core/nf/udr"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// cache state.
var (
	cacheMu    sync.RWMutex
	authByIMSI map[string]*udr.UEAuthData
	sqnDirty   map[string]struct{} // IMSIs with unflushed SQN bumps
)

func init() {
	authByIMSI = make(map[string]*udr.UEAuthData)
	sqnDirty = make(map[string]struct{})
}

// LoadCache reads every ue_auth_data row from UDR into memory. Call
// once at startup after InitContextFromDB. Idempotent — repeated calls
// rebuild the map from scratch.
func LoadCache() error {
	log := logger.Get("udm.cache")
	all, err := udr.GetAllUeAuthData()
	if err != nil {
		return err
	}
	m := make(map[string]*udr.UEAuthData, len(all))
	for _, row := range all {
		cp := row.UEAuthData // copy
		m[row.IMSI] = &cp
	}
	cacheMu.Lock()
	authByIMSI = m
	sqnDirty = make(map[string]struct{})
	cacheMu.Unlock()
	log.Infof("Auth cache loaded: %d subscriber(s)", len(m))
	return nil
}

// ReloadAuth re-reads a single subscriber from UDR into the cache.
// Call after POST /api/ue/auth so GUI edits take effect on the next
// registration. Returns true if the subscriber exists in UDR, false
// if the row was deleted (caller typically followed a DELETE path).
func ReloadAuth(imsi string) (bool, error) {
	ad, err := udr.GetUeAuthData(imsi)
	if err != nil {
		return false, err
	}
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if ad == nil {
		delete(authByIMSI, imsi)
		delete(sqnDirty, imsi)
		return false, nil
	}
	cp := *ad
	authByIMSI[imsi] = &cp
	// A fresh reload from DB means the cached SQN matches what's on
	// disk — drop any stale dirty flag so we don't re-write the same
	// row on the next flush tick.
	delete(sqnDirty, imsi)
	return true, nil
}

// DropAuth removes the subscriber from the cache. Call after DELETE
// /api/ue/auth/{imsi} so the deleted IMSI can't authenticate from
// stale in-memory state.
func DropAuth(imsi string) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	delete(authByIMSI, imsi)
	delete(sqnDirty, imsi)
}

// lookup returns the cached entry (caller must not mutate the returned
// pointer's fields outside of bumpSQN).
func lookup(imsi string) *udr.UEAuthData {
	cacheMu.RLock()
	defer cacheMu.RUnlock()
	return authByIMSI[imsi]
}

// bumpSQN updates the cached SQN and marks the subscriber dirty. The
// background flusher will persist dirty rows to UDR on its next tick.
// No DB hit on the hot auth path.
func bumpSQN(imsi string, newSQN int64) {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	if ad, ok := authByIMSI[imsi]; ok {
		ad.SQN = newSQN
		sqnDirty[imsi] = struct{}{}
	}
}

// takeDirtySnapshot returns a copy of {imsi: sqn} for currently-dirty
// rows AND clears the dirty set. Callers (the flusher) persist the
// snapshot asynchronously; new bumps that land during the flush are
// captured in the next cycle. Under graceful shutdown the flusher
// calls this one more time so nothing is lost.
func takeDirtySnapshot() map[string]int64 {
	cacheMu.Lock()
	defer cacheMu.Unlock()
	out := make(map[string]int64, len(sqnDirty))
	for imsi := range sqnDirty {
		if ad, ok := authByIMSI[imsi]; ok {
			out[imsi] = ad.SQN
		}
	}
	sqnDirty = make(map[string]struct{})
	return out
}
