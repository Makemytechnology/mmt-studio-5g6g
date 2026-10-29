// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// validators.go — spec-compliance gates for MBS create paths.
//
// Spec anchors:
//
//   - TS 23.003 §15.2  — TMGI format. TMGI = MBS Service ID (3 octets)
//                        + MCC (3 digits) + MNC (2 or 3 digits). The
//                        on-air form is hex; the FQDN form
//                        "<svc>@<MCC>.<MNC>.mbms.3gppnetwork.org" is the
//                        DNS-friendly variant. We accept either.
//   - TS 23.003 §19.4.2 — TAI format. TAI = PLMN-ID (3 octets) + TAC
//                        (24 bits, 6 hex digits in 5G). Operator format
//                        is "<MCC><MNC>-<TAC>" with TAC as 6 hex chars.
//   - TS 23.501  Table 5.7.4-1 — Standardised 5QI values are
//                        {1..9, 65, 66, 67, 69, 70, 71, 72, 73, 74,
//                        75, 79, 80, 82, 83, 84, 85, 86, 87, 88}.
//                        Operator-use values are 128..254 (TS 23.501
//                        §5.7.4 "Non-standardized 5QI"). 5QI is a
//                        single octet so the bound is [1, 255].
//
// All validators return a wrapped error so the route layer can map
// them to HTTP 400 with a stable message.
package mbs

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/mmt/mmt-studio-core/db/engine"
)

// tmgiHexRE — 12 hex chars (6 octets) — MBS Service ID (3 octets) +
// PLMN-ID (3 octets). 12 chars is the canonical raw form.
// Some operator deployments use 14 chars (3-digit MNC) or
// the FQDN form; we accept those via tmgiFQDNRE below.
var tmgiHexRE = regexp.MustCompile(`^[0-9A-Fa-f]{12,14}$`)

// tmgiFQDNRE — "<svc>@<MCC>.<MNC>.mbms.3gppnetwork.org" per
// TS 23.003 §15.2. svc is 6 hex chars (3 octets). MCC is 3 digits,
// MNC is 2 or 3 digits.
var tmgiFQDNRE = regexp.MustCompile(
	`^[0-9A-Fa-f]{6}@[0-9]{3}\.[0-9]{2,3}\.mbms\.3gppnetwork\.org$`)

// taiRE — "<MCC><MNC>-<TAC>" with TAC as 6 hex chars (24 bits).
// MCC = 3 digits; MNC = 2 or 3 digits per TS 23.003 §2.2.
// E.g. "00101-000001" or "001999-ABCDEF".
var taiRE = regexp.MustCompile(`^[0-9]{5,6}-[0-9A-Fa-f]{6}$`)

// ValidateTMGI rejects malformed TMGIs at write time so the panel
// gets a clean 400 instead of a CHECK / FK / wire-format failure
// downstream. Empty input is accepted (the create paths gate on
// emptiness separately for a clearer message).
func ValidateTMGI(tmgi string) error {
	if tmgi == "" {
		return nil
	}
	if tmgiHexRE.MatchString(tmgi) {
		return nil
	}
	if tmgiFQDNRE.MatchString(tmgi) {
		return nil
	}
	return fmt.Errorf(
		"tmgi %q: must be 12-14 hex chars (raw form) or "+
			"<6hex>@<MCC>.<MNC>.mbms.3gppnetwork.org (FQDN form) "+
			"per TS 23.003 §15.2", tmgi)
}

// ValidateTAIList accepts a comma-separated list of TAIs; every entry
// must match `<MCC><MNC>-<TAC>` per TS 23.003 §19.4.2. Empty list is
// rejected (CreateArea gates on this for a clearer message; this
// function is called when the list is non-empty).
func ValidateTAIList(taiList string) error {
	if taiList == "" {
		return fmt.Errorf("tracking_areas: empty list")
	}
	parts := strings.Split(taiList, ",")
	for i, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			return fmt.Errorf("tracking_areas: empty entry at position %d", i)
		}
		if !taiRE.MatchString(p) {
			return fmt.Errorf(
				"tracking_areas[%d] %q: want '<MCC><MNC>-<TAC>' with "+
					"TAC as 6 hex chars (TS 23.003 §19.4.2)", i, p)
		}
	}
	return nil
}

// Validate5QI gates the 5QI value to the [1, 255] octet range.
// Standardised values (TS 23.501 Table 5.7.4-1) and operator-use
// values (128..254) are both accepted; out-of-range surfaces as 400.
func Validate5QI(qi int) error {
	if qi < 1 || qi > 255 {
		return fmt.Errorf("qos_5qi %d: out of [1, 255] (TS 23.501 §5.7.4)", qi)
	}
	return nil
}

// ValidateBitrate rejects negative bitrate budgets. Zero is accepted
// (caller chooses an unbounded session); >0 means an explicit cap.
func ValidateBitrate(kbps int) error {
	if kbps < 0 {
		return fmt.Errorf("max_bitrate_kbps %d: must be >= 0", kbps)
	}
	return nil
}

// ── TAI list management on an existing area ─────────────────────

// AppendTAIs adds TAIs to an existing area's tracking_areas list.
// Idempotent — TAIs already present are not duplicated. Returns the
// updated row.
func AppendTAIs(areaID int64, tais []string) (map[string]interface{}, error) {
	if len(tais) == 0 {
		return nil, fmt.Errorf("tais: empty")
	}
	for _, t := range tais {
		if !taiRE.MatchString(strings.TrimSpace(t)) {
			return nil, fmt.Errorf(
				"tais entry %q: want '<MCC><MNC>-<TAC>' with TAC as "+
					"6 hex chars (TS 23.003 §19.4.2)", t)
		}
	}
	cur, err := getArea(areaID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("area %d not found", areaID)
	}
	existing := strings.Split(asStr(cur["tracking_areas"]), ",")
	seen := map[string]bool{}
	out := []string{}
	for _, e := range existing {
		e = strings.TrimSpace(e)
		if e == "" || seen[e] {
			continue
		}
		seen[e] = true
		out = append(out, e)
	}
	for _, t := range tais {
		t = strings.TrimSpace(t)
		if seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	merged := strings.Join(out, ",")
	if _, err := engine.Exec(
		`UPDATE mbs_areas SET tracking_areas=? WHERE id=?`,
		merged, areaID); err != nil {
		return nil, err
	}
	return getArea(areaID)
}

// RemoveTAIs strips the listed TAIs from an existing area. Returns
// the updated row. Removing all TAIs is allowed; the caller may then
// AppendTAIs to repopulate.
func RemoveTAIs(areaID int64, tais []string) (map[string]interface{}, error) {
	cur, err := getArea(areaID)
	if err != nil {
		return nil, err
	}
	if cur == nil {
		return nil, fmt.Errorf("area %d not found", areaID)
	}
	drop := map[string]bool{}
	for _, t := range tais {
		drop[strings.TrimSpace(t)] = true
	}
	existing := strings.Split(asStr(cur["tracking_areas"]), ",")
	out := []string{}
	for _, e := range existing {
		e = strings.TrimSpace(e)
		if e == "" || drop[e] {
			continue
		}
		out = append(out, e)
	}
	merged := strings.Join(out, ",")
	if _, err := engine.Exec(
		`UPDATE mbs_areas SET tracking_areas=? WHERE id=?`,
		merged, areaID); err != nil {
		return nil, err
	}
	return getArea(areaID)
}

// asStr is the local stringifier used by the validators (mbs.go has
// its own qRow / qRows but no `asString` helper).
func asStr(v interface{}) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case []byte:
		return string(x)
	}
	return fmt.Sprintf("%v", v)
}
