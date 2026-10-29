// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package iot — umbrella for cellular IoT subpackages:
//   - nbiot   NB-IoT / LTE-M PSM + eDRX + capability tracking
//             (TS 23.401 §4.3.22 PSM, §5.13a eDRX, §4.3.17 MTC).
//   - nidd    Non-IP Data Delivery sessions + CP/UP data path
//             (TS 23.682 §5.13, TS 23.401 §4.3.17.8).
//   - ambient Ambient IoT tag/reader registry (TS 22.369).
//   - redcap  RedCap / eRedCap RAT-type helpers (TS 23.501 §5.41).
//
// This file exists for the GUI panel's umbrella Status() endpoint
// and as a stable import target for top-level callers; per-domain
// state lives in the subpackages.
package iot

import "github.com/mmt/mmt-studio-core/db/engine"

// Status returns a tiny umbrella shape for the GUI panel; per-domain
// detail comes from each subpackage's own Status().
func Status() map[string]any {
	_ = engine.Open
	return map[string]any{"status": "ready"}
}
