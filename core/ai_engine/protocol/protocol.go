// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package protocol -- AI assistant protocol.
package protocol

import (
	"github.com/mmt/mmt-studio-core/db/engine"
	"github.com/mmt/mmt-studio-core/oam/logger"
)

// Status returns current state.
func Status() map[string]any {
	log := logger.Get("protocol")
	_ = log
	_ = engine.Open
	return map[string]any{"status": "ready"}
}
