// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
// db/crud/infra_history.go — last-known-good infra_config snapshots.
//
// Ring buffer capped at MaxHistoryRows. run.sh / the lifecycle stamp a
// snapshot after a clean phase-2 boot so the GUI can revert a tuning
// change that caused a crash loop.
package crud

import (
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/mmt/mmt-studio-core/db/engine"
)

const MaxHistoryRows = 10

// InfraSnapshot is a saved point-in-time infra_config.
type InfraSnapshot struct {
	ID     int64
	TS     float64
	Note   string
	Config map[string]any
}

// StampInfra appends a snapshot, then trims the ring to MaxHistoryRows.
func StampInfra(cfg map[string]any, note string) error {
	b, err := json.Marshal(cfg)
	if err != nil {
		return err
	}
	db, err := engine.Open()
	if err != nil {
		return err
	}
	ts := float64(time.Now().UnixMilli()) / 1000.0
	if _, err := db.Exec(
		`INSERT INTO infra_config_history (ts, note, config_json) VALUES (?,?,?)`,
		ts, note, string(b),
	); err != nil {
		return err
	}
	_, err = db.Exec(
		`DELETE FROM infra_config_history WHERE id NOT IN
         (SELECT id FROM infra_config_history ORDER BY ts DESC LIMIT ?)`,
		MaxHistoryRows,
	)
	return err
}

// ListInfraHistory returns snapshots newest-first.
func ListInfraHistory() ([]InfraSnapshot, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT id, ts, note, config_json FROM infra_config_history ORDER BY ts DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []InfraSnapshot
	for rows.Next() {
		var s InfraSnapshot
		var j string
		if err := rows.Scan(&s.ID, &s.TS, &s.Note, &j); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(j), &s.Config)
		out = append(out, s)
	}
	return out, rows.Err()
}

// GetInfraSnapshot returns the config dict for a specific history id, or nil.
func GetInfraSnapshot(id int64) (map[string]any, error) {
	db, err := engine.Open()
	if err != nil {
		return nil, err
	}
	var j string
	err = db.QueryRow(
		`SELECT config_json FROM infra_config_history WHERE id=?`, id,
	).Scan(&j)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(j), &m); err != nil {
		return nil, err
	}
	return m, nil
}
