// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package mcx — Mission Critical Services (3GPP MCX family).
//
// Spec anchors (PDFs under specs/3gpp/):
//
//   - TS 23.280  Common functional architecture for MCX (Stage 2).
//   - TS 23.379  MCPTT functional architecture (Stage 2).
//   - TS 23.281  MCVideo functional architecture (Stage 2).
//   - TS 23.282  MCData functional architecture (Stage 2).
//   - TS 24.379  MCPTT call control protocol (Stage 3).
//   - TS 24.380  MCPTT media plane / floor control protocol (Stage 3).
//   - TS 33.180  Security of the Mission Critical Service (MCX security
//                architecture; user auth § 5.1; key management § 5.2;
//                MIKEY-SAKKE-anchored media security § 4.3.5 / § 7).
//
// MCX user profiles, MCX groups, and the dispatch between MCPTT /
// MCVideo / MCData live under TS 23.280 §7 ("Functional model and
// entities") and §8.1 ("Application plane" identities — MC ID, MC
// service ID, MC service group ID, MC system ID). The MCX system
// uses a Common Services Core (CSC) that the per-service planes
// (MCPTT/MCVideo/MCData) all sit on top of (§7.2 "Description of
// the planes").
//
// Sub-packages:
//
//   common/       — TS 23.280 §6, §7 identity/group/key helpers;
//                    TS 33.180 §5.2 group key generation.
//   mcptt/        — TS 23.379 / TS 24.379 (call control) +
//                    TS 24.380 (floor control).
//   mcvideo/      — TS 23.281 (Stage 2). Stage-3 protocol details
//                    require TS 24.281 which is not yet in-tree;
//                    placeholders flag deferred work.
//   mcdata/       — TS 23.282 (Stage 2). Stage-3 protocol details
//                    require TS 24.282; same caveat.
//   signaling/    — WebSocket bridge + SIP shim. Spec anchor is
//                    TS 24.379 §6.2 (MCPTT use of SIP).
//
// This file ports the top-level user/group registry that the MCX GUI
// panel and the GMM auth-hook (auto-create MCX user on UE provisioning)
// consume. Per-service call state lives in the sub-packages.
package mcx

import (
	"database/sql"
	"errors"
	"sync"

	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// User is the mcx_user_profiles row.
type User struct {
	ID          int64  `json:"id"`
	UEID        int64  `json:"ue_id"`
	MCPTTID     string `json:"mcptt_id"`
	DisplayName string `json:"display_name"`
	Priority    int    `json:"priority"`
}

// Group is the mcx_groups row.
type Group struct {
	ID          int64  `json:"id"`
	GroupID     string `json:"group_id"`
	DisplayName string `json:"display_name"`
	GroupType   string `json:"group_type"` // prearranged | chat
}

// ListUsers returns all MCX user profiles.
func ListUsers() ([]User, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, ue_id, mcptt_id, display_name, priority
        FROM mcx_user_profiles ORDER BY mcptt_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []User
	for rows.Next() {
		var u User
		if err := rows.Scan(&u.ID, &u.UEID, &u.MCPTTID, &u.DisplayName, &u.Priority); err != nil {
			return nil, err
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ListGroups returns all MCX groups.
func ListGroups() ([]Group, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id, group_id, display_name, group_type
        FROM mcx_groups ORDER BY group_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Group
	for rows.Next() {
		var g Group
		if err := rows.Scan(&g.ID, &g.GroupID, &g.DisplayName, &g.GroupType); err != nil {
			return nil, err
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// GetOrCreateUser auto-provisions an MCX user for a UE. Called from the
// auth upsert hook so every provisioned UE has an MCX profile ready.
func GetOrCreateUser(imsi, displayName string, priority int) error {
	log := logger.Get("mcx")
	db, err := engine.Open()
	if err != nil {
		return err
	}
	var ueID int64
	err = db.QueryRow(`SELECT id FROM ue WHERE imsi=?`, imsi).Scan(&ueID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	mcpttID := "mcptt:" + imsi
	var exists int
	err = db.QueryRow(`SELECT 1 FROM mcx_user_profiles WHERE mcptt_id=?`, mcpttID).Scan(&exists)
	if err == nil {
		return nil // already exists
	}
	if _, err := db.Exec(`INSERT INTO mcx_user_profiles
        (ue_id, mcptt_id, display_name, priority) VALUES (?,?,?,?)`,
		ueID, mcpttID, displayName, priority); err != nil {
		return err
	}
	log.Infof("MCX user created mcptt_id=%s imsi=%s", mcpttID, imsi)
	return nil
}

// ── In-memory call state ────────────────────────────────────────────────

// Call tracks an active MCPTT / MCVideo session.
type Call struct {
	ID      string `json:"id"`
	Type    string `json:"type"` // ptt | video | data
	GroupID string `json:"group_id"`
	State   string `json:"state"` // active | held | released
}

var (
	callMu sync.RWMutex
	calls  []Call
)

// ListCalls returns active MCX calls.
func ListCalls() []Call {
	callMu.RLock()
	defer callMu.RUnlock()
	out := make([]Call, len(calls))
	copy(out, calls)
	return out
}
