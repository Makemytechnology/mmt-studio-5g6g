// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Per-path state for multi-homed SCTP associations. RFC 4960 §8.2 says
// each peer address ("path") inside an association has its own
// reachability state driven by HEARTBEAT ACKs / timeouts. Linux
// delivers path-state changes via SCTP_PEER_ADDR_CHANGE notifications
// (RFC 6458 §6.1.2).
//
// Kept in a separate file because a typical lab deployment uses
// single-homed SCTP and the path-FSM stays at Active forever — no
// reason to pollute the association FSM with it. Multi-homed
// deployments (dual-backhaul / redundant-interface gNBs) need this
// layer to alarm when one path fails over.
package sctpfsm

import (
	"fmt"
	"sync"

	"github.com/mmt/mmt-studio-core/oam/logger"
)

// PathState — RFC 4960 §8.2.
type PathState int

const (
	PathUnknown PathState = iota
	PathActive            // path is reachable, OK to send DATA on
	PathInactive          // HB misses exceeded path_max_rxt
	PathUnreachable       // one-time "address removed" event
	PathConfirmed         // kernel said SPC_CONFIRMED (reachable after previous inactive)
)

func (s PathState) String() string {
	switch s {
	case PathActive:
		return "ACTIVE"
	case PathInactive:
		return "INACTIVE"
	case PathUnreachable:
		return "UNREACHABLE"
	case PathConfirmed:
		return "CONFIRMED"
	}
	return "UNKNOWN"
}

// PathKey identifies one peer address inside an association.
type PathKey struct {
	GnbIP   string
	AssocID int32
	Peer    string // the specific peer IP the kernel reported (IPv4 or IPv6 text)
}

func (k PathKey) String() string {
	return fmt.Sprintf("%s#%d/peer=%s", k.GnbIP, k.AssocID, k.Peer)
}

// PathInfo is the current state + cause of last change.
type PathInfo struct {
	Key   PathKey
	State PathState
	Error uint16 // kernel SPC error code from sctp_paddr_change.spc_error
}

var (
	pathsMu sync.RWMutex
	paths   = map[PathKey]*PathInfo{}
)

// SetPathState updates one path's state. Usually called from the
// SCTP_PEER_ADDR_CHANGE notification parser. Transitions fire a log at
// Info (ACTIVE → any) or Warn (any → INACTIVE/UNREACHABLE) so an
// operator sees them in the journal.
func SetPathState(k PathKey, s PathState, errCode uint16) {
	pathsMu.Lock()
	prev := paths[k]
	info := &PathInfo{Key: k, State: s, Error: errCode}
	paths[k] = info
	pathsMu.Unlock()

	log := logger.Get("amf.ngap.sctp.path")
	if prev == nil {
		log.Infof("PATH %s = %s (err=%d)", k, s, errCode)
		return
	}
	if prev.State == s {
		return
	}
	if s == PathInactive || s == PathUnreachable {
		log.Warnf("PATH %s: %s → %s (err=%d)", k, prev.State, s, errCode)
	} else {
		log.Infof("PATH %s: %s → %s (err=%d)", k, prev.State, s, errCode)
	}
}

// DropPathsForAssoc clears every path entry for a vanished association.
// Called when the SCTP FSM hits CLOSED/FAILED.
func DropPathsForAssoc(gnbIP string, assocID int32) {
	pathsMu.Lock()
	defer pathsMu.Unlock()
	for k := range paths {
		if k.GnbIP == gnbIP && k.AssocID == assocID {
			delete(paths, k)
		}
	}
}

// PathSnapshot — /api/amf/ngap/sctp/paths JSON row.
type PathSnapshot struct {
	GnbIP   string `json:"gnb_ip"`
	AssocID int32  `json:"assoc_id"`
	Peer    string `json:"peer"`
	State   string `json:"state"`
	Error   uint16 `json:"error,omitempty"`
}

// AllPaths returns every current path entry.
func AllPaths() []PathSnapshot {
	pathsMu.RLock()
	defer pathsMu.RUnlock()
	out := make([]PathSnapshot, 0, len(paths))
	for _, p := range paths {
		out = append(out, PathSnapshot{
			GnbIP: p.Key.GnbIP, AssocID: p.Key.AssocID, Peer: p.Key.Peer,
			State: p.State.String(), Error: p.Error,
		})
	}
	return out
}
