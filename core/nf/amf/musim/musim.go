// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package musim -- Multi-USIM support.
package musim

import (
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// List returns rows from musim_groups.
func List() ([]map[string]any, error) {
	db, err := engine.Open()
	if err != nil { return nil, err }
	rows, err := db.Query("SELECT * FROM musim_groups ORDER BY 1 LIMIT 1000")
	if err != nil { return nil, nil }
	defer rows.Close()
	cols, _ := rows.Columns()
	var out []map[string]any
	for rows.Next() {
		scan := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range scan { ptrs[i] = &scan[i] }
		if err := rows.Scan(ptrs...); err != nil { continue }
		row := make(map[string]any, len(cols))
		for i, name := range cols { row[name] = scan[i] }
		out = append(out, row)
	}
	return out, nil
}

// Status returns current state.
func Status() map[string]any {
	log := logger.Get("musim")
	_ = log
	_ = engine.Open
	list, _ := List()
	return map[string]any{"count": len(list)}
}
