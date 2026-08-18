// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
package kpi

import "sort"

// sortFloats sorts ss in place. Wrapper around stdlib so callers don't
// need their own sort import each time.
func sortFloats(ss []float64) {
	sort.Float64s(ss)
}

// pct returns the q-th percentile (0..1) of an already-sorted slice.
// Empty slice returns 0. q outside [0,1] is clamped.
func pct(sorted []float64, q float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	if q < 0 {
		q = 0
	}
	if q > 1 {
		q = 1
	}
	idx := int(float64(len(sorted)-1) * q)
	return sorted[idx]
}
