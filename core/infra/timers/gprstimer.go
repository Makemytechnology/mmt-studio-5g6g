// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

// GPRS timer encoders (TS 24.008 §10.5.7 + TS 24.501 §9.11.2.4/§9.11.2.5).
// The NAS codec represents these timers as a single octet that mixes a
// unit code (top 3 bits) and a value (bottom 5 bits). The AMF broadcasts
// them on Registration Accept / Configuration Update, so every hand-coded
// hex literal in GMM call sites is a latent bug — encode from the typed
// time.Duration values already centralised in this package.

package timers

import "time"

// GPRS Timer 2 (TS 24.008 §10.5.7.4) — 1 octet, 0..255 seconds:
//
//	bits 8 7 6 : unit value (0 = 2 s, 1 = 1 min, 2 = decihours,
//	                           7 = deactivated)
//	bits 5..1  : binary value 0..31 per the unit
//
// This is the on-wire byte format used by T3346, T3396, T3448, etc.

// GPRS Timer 3 (TS 24.008 §10.5.7.4a) — 1 octet covering a wider range:
//
//	bits 8 7 6 : unit
//	  000 = 10 minutes
//	  001 = 1 hour
//	  010 = 10 hours
//	  011 = 2 seconds
//	  100 = 30 seconds
//	  101 = 1 minute
//	  110 = 320 hours
//	  111 = deactivated
//	bits 5..1  : value 0..31
//
// This is the on-wire byte format T3512 / T3324 / T3447 / T3448 use.

// GPRS Timer 3 unit codes (TS 24.008 §10.5.7.4a Table 10.5.163a).
const (
	gprsT3Unit10Minutes = 0x00 // bits 8..6 = 000
	gprsT3Unit1Hour     = 0x01 // bits 8..6 = 001
	gprsT3Unit10Hours   = 0x02 // bits 8..6 = 010
	gprsT3Unit2Seconds  = 0x03 // bits 8..6 = 011
	gprsT3Unit30Seconds = 0x04 // bits 8..6 = 100
	gprsT3Unit1Minute   = 0x05 // bits 8..6 = 101
	gprsT3Unit320Hours  = 0x06 // bits 8..6 = 110
	gprsT3UnitDeact     = 0x07 // bits 8..6 = 111 (value field ignored)
)

// EncodeGPRSTimer3 packs a time.Duration as a single GPRS-Timer-3 octet
// (TS 24.008 §10.5.7.4a). Chooses the smallest unit that can represent
// the duration without loss; falls back to the largest unit when the
// value exceeds 31 of it, saturating the value field at 31.
//
// Special cases:
//   - d == 0           → "deactivated" (0xE0, bits 7..6=11 unit, value=0).
//     Use EncodeGPRSTimer3Deactivated if you want that explicitly.
//   - d > 31*320h      → saturates to unit=320h, value=31.
func EncodeGPRSTimer3(d time.Duration) byte {
	if d <= 0 {
		return EncodeGPRSTimer3Deactivated()
	}
	type tier struct {
		unit byte
		step time.Duration
	}
	// Order: finest → coarsest so the first match is the most precise fit.
	tiers := []tier{
		{gprsT3Unit2Seconds, 2 * time.Second},
		{gprsT3Unit30Seconds, 30 * time.Second},
		{gprsT3Unit1Minute, time.Minute},
		{gprsT3Unit10Minutes, 10 * time.Minute},
		{gprsT3Unit1Hour, time.Hour},
		{gprsT3Unit10Hours, 10 * time.Hour},
		{gprsT3Unit320Hours, 320 * time.Hour},
	}
	for _, t := range tiers {
		if d%t.step == 0 && d/t.step <= 31 {
			v := byte(d / t.step)
			return (t.unit << 5) | (v & 0x1F)
		}
	}
	// Fallback: saturate on largest unit.
	return (gprsT3Unit320Hours << 5) | 0x1F
}

// EncodeGPRSTimer3Deactivated returns the "deactivated" sentinel (unit
// 111, value ignored — encoded with value=0).
func EncodeGPRSTimer3Deactivated() byte {
	return (gprsT3UnitDeact << 5) // 0xE0
}
