// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package nssf — Network Slice Selection Function.
//
// SBI spec: TS 29.531 (Nnssf_NSSelection service).
//   PDF: specs/3gpp/ts_129531v190600p.pdf
//   §5.2.1   "Service Description"
//   §5.2.2.2 "GET" service operation — in particular §5.2.2.2.2 "Get
//            service operation of Nnssf_NSSelection service" which
//            lists the required query parameters (Requested NSSAI,
//            Subscribed S-NSSAI(s) with default-indication, NSSRG,
//            PLMN ID of SUPI, TAI, NF type, Requester ID) and the
//            response attributes (Allowed NSSAI, target AMF Set or
//            candidate AMF list, optionally Target NSSAI / target AMF
//            Service Set / Configured NSSAI / NSI ID / Mapping Of
//            Allowed NSSAI / rejected S-NSSAIs with cause).
//
// Procedure context: TS 23.502 §4.2.2.2.2 "General Registration"
// step 4a "[Conditional] Initial AMF to NSSF: Nnssf_NSSelection_Get".
// The conditional gate (§4.2.2.2.2 step 4a): invoke the NSSF only when
// "the initial AMF cannot serve all the S-NSSAI(s) from the Requested
// NSSAI permitted by the subscription information". We elide the gate
// and invoke unconditionally because today the NSSF is in-process; the
// TODO below promotes it to the real SBI gate.
//
// Selection semantics: TS 23.501 §5.15.5.2.1 "Registration to a set
// of Network Slices" — the serving-network Allowed NSSAI is derived
// from the UE's Requested NSSAI intersected with Subscribed-S-NSSAIs
// and the serving-network slice configuration (AMF PLMN-support set +
// gNB SupportedTAList). When the UE provides no Requested NSSAI, the
// AMF falls back to the default Subscribed S-NSSAIs per §5.15.3
// (S-NSSAIs subscribed as "default" in the UE's subscription data).
//
// Rejection cause encoding: TS 24.501 §9.11.3.46 "Rejected NSSAI" IE
// table 9.11.3.46.1 — each rejected S-NSSAI carries a 4-bit cause in
// octet 3 bits 4..1 (0 = not available in current PLMN/SNPN, 1 = not
// available in current registration area, 2 = failed/revoked NSSAA).
//
// Go port of nf/nssf/nssf_ns_selection.py.
package nssf

import (
	"fmt"
	"strings"

	"github.com/mmt/mmt-studio-core/db/crud"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/oam/pm"
)

// ── Rejected NSSAI cause values ─────────────────────────────────────
//
// TS 24.501 §9.11.3.46 figure 9.11.3.46.2 / table 9.11.3.46.1:
//
//	Cause value (octet 3 bits 4..1)
//	0 0 0 0  S-NSSAI not available in the current PLMN or SNPN
//	0 0 0 1  S-NSSAI not available in the current registration area
//	0 0 1 0  S-NSSAI not available due to the failed or revoked
//	         network slice-specific authentication and authorization
//	All other values are reserved.
const (
	RejectedCauseNotInPLMN           uint8 = 0
	RejectedCauseNotInRegistrationArea uint8 = 1
	RejectedCauseNSSAAFailedOrRevoked  uint8 = 2
)

// rejectedCauseString renders a 4-bit cause code for log output. Keeps
// the enum constants the source of truth and avoids callers caring
// about string tags (which prior to this version were passed through
// the NSSF selection path and translated downstream — a brittle two-
// layer mapping).
func rejectedCauseString(c uint8) string {
	switch c {
	case RejectedCauseNotInPLMN:
		return "not-in-plmn"
	case RejectedCauseNotInRegistrationArea:
		return "not-in-registration-area"
	case RejectedCauseNSSAAFailedOrRevoked:
		return "nssaa-failed-or-revoked"
	}
	return fmt.Sprintf("reserved(%d)", c)
}

// SNSSAI is the (SST, SD) pair — SD=0 or 0xFFFFFF means "wildcard /
// no SD field". See sdMatch for the comparison rule (TS 24.501
// §9.11.2.8 + TS 23.003 §28.4.2).
type SNSSAI struct {
	SST uint8
	SD  uint32 // 0xFFFFFF or 0 = wildcard
}

// SelectionResult is returned to the AMF for encoding in Registration Accept.
type SelectionResult struct {
	Allowed    []SNSSAI         // max 8 per TS 23.501 §5.15.4
	Rejected   []RejectedSNSSAI // max 8 per TS 24.501 §9.11.3.46 NOTE 0
	Subscribed []SNSSAI
	// TODO(spec: TS 29.531 §5.2.2.2.2 response attrs) — the real
	//   Nnssf_NSSelection_Get response ALSO carries:
	//     target AMF Set or list of candidate AMF(s), optional target
	//     AMF Service Set, Target NSSAI, Mapping Of Allowed NSSAI
	//     (HPLMN↔VPLMN for roaming), Configured NSSAI for the serving
	//     PLMN, NSI ID(s), NRF(s), nsagInfos.
	//   None are modelled yet — all single-PLMN deployment wouldn't
	//   exercise them. Add fields here when SBI is wired / roaming or
	//   AMF reallocation is supported.
}

// RejectedSNSSAI carries a §9.11.3.46 cause per Rejected S-NSSAI entry.
type RejectedSNSSAI struct {
	SNSSAI
	Cause uint8 // one of RejectedCauseXxx
}

// SelectAllowedNSSAI is the Nnssf_NSSelection_Get entry point.
//
//	requested: parsed from UE NAS (may be nil → fall back to the
//	           UE's default subscribed S-NSSAIs per TS 23.501
//	           §5.15.5.2.1).
//	amfSlices: AMF PLMNSupportList slice set (from ctx.Default).
//	gnbSlices: gNB SupportedTAList slice set.
//	taPolicyTAC: optional TAC for per-TA NSSAI filtering (empty = skip).
//
// TODO(spec: TS 29.531 §5.2.2.2.2 query params) — the real SBI GET
//   also takes: PLMN ID of the SUPI (for roaming scenarios), TAI,
//   NF type of NF Service Consumer, Requester ID, NSSRG Information,
//   Mapping Of Requested NSSAI, "UE support of subscription-based
//   restrictions" indication, UDM "provide all subscribed" indication,
//   NSAG support indication. None land here until SBI split.
func SelectAllowedNSSAI(imsi string, requested []SNSSAI,
	amfSlices, gnbSlices []SNSSAI, taPolicyTAC string) SelectionResult {
	log := logger.Get("nssf.ns_selection")
	pm.Inc(pm.NSSFSelAtt, 1)

	// Step 1: subscribed NSSAI from UDM (db/crud). Keep the is_default
	// flag per-entry — TS 23.501 §5.15.5.2.1 falls back to **default**
	// subscribed S-NSSAIs when the UE supplies no Requested NSSAI.
	allSubscribed, defaultSubscribed := loadSubscribedNSSAI(imsi)

	log.WithIMSI(imsi).Infof("NSSAI selection requested=%s subscribed=%s (default=%s) amf=%s gnb=%s",
		fmtSlices(requested), fmtSlices(allSubscribed), fmtSlices(defaultSubscribed),
		fmtSlices(amfSlices), fmtSlices(gnbSlices))

	// Step 2: pick the candidate set per TS 23.501 §5.15.5.2.1.
	//   - Requested NSSAI present → use it (UE is explicit about what
	//     slices it wants to register against).
	//   - Requested NSSAI absent → use the **default** subscribed
	//     S-NSSAIs (§5.15.3 + §5.15.5.2.1 "…if the UE has no
	//     Configured NSSAI nor an Allowed NSSAI for the serving
	//     PLMN"). An empty Requested list from a UE that actually has
	//     subscription coverage means the UE relies on the serving
	//     network to pick its defaults.
	//   Note: we DO NOT fall through to amfSlices when both Requested
	//   and defaults are empty — if the subscription lists zero
	//   defaults, the UE gets no service. That's a provisioning
	//   problem, not something NSSF should paper over with the AMF's
	//   own support set.
	candidates := requested
	if len(candidates) == 0 {
		candidates = defaultSubscribed
	}

	// Step 3: intersect. Each candidate is admitted only if it's in
	// the subscription, the AMF's support set, and the gNB's support
	// set. Any missing side yields a Rejected S-NSSAI with the
	// appropriate cause.
	var result SelectionResult
	result.Subscribed = allSubscribed
	for _, c := range candidates {
		okSub := matchesSet(c, allSubscribed) || len(allSubscribed) == 0
		okAmf := matchesSet(c, amfSlices)
		okGnb := matchesSet(c, gnbSlices)
		if okSub && okAmf && okGnb {
			// Per-TA filter (TS 23.501 §5.15.5.2 + §5.15.3.2).
			if taPolicyTAC != "" && !taPolicyAllows(c, taPolicyTAC) {
				if len(requested) > 0 {
					result.Rejected = append(result.Rejected, RejectedSNSSAI{c, RejectedCauseNotInRegistrationArea})
				}
				continue
			}
			result.Allowed = append(result.Allowed, c)
		} else if len(requested) > 0 {
			// TS 24.501 §5.5.1.2.4 (quoted verbatim):
			//   "The AMF verifies if the requested NSSAI is permitted based
			//    on the subscribed S-NSSAIs in the UE subscription and the
			//    mapped S-NSSAI(s), if provided by the UE, and if so then
			//    the AMF shall provide the UE with the allowed NSSAI for
			//    the PLMN or SNPN…"
			// Unmatched entries go into the Rejected NSSAI IE per
			// §9.11.3.46 — not a fault, just UE vs subscription /
			// network divergence. Cause = NotInPLMN (generic — the
			// specific "not in current TA" cause is reserved for the
			// TA-policy branch above).
			log.WithIMSI(imsi).Debugf("NSSAI rejected SST=%d SD=%06X: sub=%t amf=%t gnb=%t",
				c.SST, c.SD, okSub, okAmf, okGnb)
			result.Rejected = append(result.Rejected, RejectedSNSSAI{c, RejectedCauseNotInPLMN})
		}
	}

	// Step 3b: All-requested-rejected fallback per TS 23.501 v19.7.0
	// §5.15.5.2.1 (verbatim): "...the Allowed NSSAI is then
	// determined by taking into account the list of S-NSSAI(s) in the
	// Requested NSSAI permitted based on the Subscribed S-NSSAIs ...
	// or, if neither Requested NSSAI nor the mapping of Requested
	// NSSAI was provided or none of the S-NSSAIs in the Requested
	// NSSAI are permitted, all the S-NSSAI(s) marked as default in
	// the Subscribed S-NSSAIs..."
	//
	// Earlier this branch cited §5.5.1.2.4's "may include the allowed
	// subscribed S-NSSAI(s)" to justify NOT falling back, but that
	// "may" governs adding *extra* subscribed slices alongside
	// accepted requested ones — the all-rejected fallback is the
	// unambiguous SHALL in 23.501. The Rejected list keeps the UE-
	// requested entries (UE needs to know what it asked for was
	// turned down); Allowed is repopulated from defaults.
	if len(result.Allowed) == 0 && len(requested) > 0 && len(defaultSubscribed) > 0 {
		for _, c := range defaultSubscribed {
			if !matchesSet(c, amfSlices) || !matchesSet(c, gnbSlices) {
				continue
			}
			if taPolicyTAC != "" && !taPolicyAllows(c, taPolicyTAC) {
				continue
			}
			result.Allowed = append(result.Allowed, c)
		}
		if len(result.Allowed) > 0 {
			log.WithIMSI(imsi).Infof("NSSAI fallback: all requested rejected — using default subscribed=%s per TS 23.501 §5.15.5.2.1",
				fmtSlices(result.Allowed))
		}
	}

	// TS 23.501 §5.15.4: a UE can have up to 8 allowed S-NSSAIs per
	// access type. TS 24.501 §9.11.3.46 NOTE 0: the number of rejected
	// S-NSSAI(s) shall not exceed eight.
	if len(result.Allowed) > 8 {
		result.Allowed = result.Allowed[:8]
	}
	if len(result.Rejected) > 8 {
		result.Rejected = result.Rejected[:8]
	}

	// TODO(spec: TS 33.501 §6.1.4 + TS 23.501 §5.15.10) —
	//   Network-Slice-Specific Authentication and Authorization
	//   (NSSAA): S-NSSAIs subject to NSSAA start in a "pending"
	//   state and are only admitted to Allowed NSSAI after successful
	//   NSSAA (cause=2 on failure / revocation). We don't track NSSAA
	//   state per S-NSSAI yet; any subscribed NSSAA-gated slice will
	//   land in Allowed without authentication.

	if len(result.Allowed) > 0 {
		pm.Inc(pm.NSSFSelSucc, 1)
	} else {
		pm.Inc(pm.NSSFSelFail, 1)
	}
	log.WithIMSI(imsi).Infof("NSSAI result allowed=%d rejected=%d",
		len(result.Allowed), len(result.Rejected))
	return result
}

// matchesSet returns true if s is "present" in the set. SD matching is
// wildcard-aware: 0, 0xFFFFFF, or matching values all count.
func matchesSet(s SNSSAI, set []SNSSAI) bool {
	if len(set) == 0 {
		return true
	}
	for _, t := range set {
		if s.SST == t.SST && sdMatch(s.SD, t.SD) {
			return true
		}
	}
	return false
}

// sdMatch implements the S-NSSAI comparison rule implied by
// TS 24.501 §9.11.2.8 "S-NSSAI information element":
//
//	"Length of S-NSSAI contents (octet 2)
//	 0 0 0 0 0 0 0 1  SST
//	 0 0 0 0 0 1 0 0  SST and SD
//	 ...
//	 If the SST encoded in octet 3 is not associated with a valid SD
//	 value, and the sender needs to include a mapped HPLMN SST (octet
//	 7) and a mapped HPLMN SD (octets 8 to 10), then the sender shall
//	 set the SD value (octets 4 to 6) to 'no SD value associated with
//	 the SST'."
//
// TS 23.003 §28.4.2 fixes the wire encoding of "no SD value associated
// with the SST" as 0xFFFFFF. SST-only S-NSSAIs carry no SD field at all
// — the parser represents that as SD=0. Either encoding means
// "match any SD under this SST", so both 0 and 0xFFFFFF are wildcards
// on either side of the comparison.
func sdMatch(a, b uint32) bool {
	if a == 0 || b == 0 || a == 0xFFFFFF || b == 0xFFFFFF {
		return true
	}
	return a == b
}

// loadSubscribedNSSAI reads the UE's subscribed slice set from UDM and
// returns it as (all, defaults). Entries with ue_subscribed_nssai.is_default
// = 1 land in the defaults list; TS 23.501 §5.15.3 treats these as the
// Serving-PLMN defaults applied when the UE supplies no Requested NSSAI.
func loadSubscribedNSSAI(imsi string) (all, defaults []SNSSAI) {
	list, err := crud.SubscribedNSSAIList(imsi)
	if err != nil || len(list) == 0 {
		return nil, nil
	}
	for _, n := range list {
		s := SNSSAI{SST: uint8(n.SST), SD: parseHexSD(n.SD)}
		all = append(all, s)
		if n.IsDefault {
			defaults = append(defaults, s)
		}
	}
	// §5.15.3 edge: if the operator forgot to flag any default,
	// treat every subscribed slice as a default so the UE has service.
	// Logged at selection time via the "default=..." column.
	if len(defaults) == 0 {
		defaults = all
	}
	return all, defaults
}

// parseHexSD converts the SD column (stored as hex string, typically
// "000001" for a 3-byte SD) to a uint32. Empty / unparseable → 0,
// which sdMatch treats as wildcard.
func parseHexSD(s string) uint32 {
	var sd uint32
	for _, c := range s {
		sd <<= 4
		switch {
		case c >= '0' && c <= '9':
			sd |= uint32(c - '0')
		case c >= 'A' && c <= 'F':
			sd |= uint32(c-'A') + 10
		case c >= 'a' && c <= 'f':
			sd |= uint32(c-'a') + 10
		}
	}
	return sd
}

func taPolicyAllows(s SNSSAI, tac string) bool {
	// TS 23.501 §5.15.5.2 + §5.15.3.2: per-TA slice support. If the TAC
	// has a policy limiting which S-NSSAIs are available in that area,
	// unmatched S-NSSAIs must be flagged with cause=NotInRegistrationArea.
	// SD wildcards (0 / 0xFFFFFF, TS 23.003 §28.4.2) are rendered as
	// empty so the table's NULL-equivalent stored SD matches any query.
	sd := ""
	if s.SD != 0 && s.SD != 0xFFFFFF {
		sd = fmt.Sprintf("%06X", s.SD)
	}
	return crud.TANssaiPolicyAllows(tac, int(s.SST), sd)
}

// fmtSlices renders a list of S-NSSAIs as [(SST:SD), ...] for logging.
func fmtSlices(s []SNSSAI) string {
	if len(s) == 0 {
		return "[]"
	}
	parts := make([]string, 0, len(s))
	for _, x := range s {
		if x.SD == 0 || x.SD == 0xFFFFFF {
			parts = append(parts, fmt.Sprintf("(%d:*)", x.SST))
		} else {
			parts = append(parts, fmt.Sprintf("(%d:%06X)", x.SST, x.SD))
		}
	}
	return "[" + strings.Join(parts, ",") + "]"
}

// String renders a RejectedSNSSAI for log output — surfaces the cause
// name rather than the raw 4-bit code.
func (r RejectedSNSSAI) String() string {
	sd := "*"
	if r.SD != 0 && r.SD != 0xFFFFFF {
		sd = fmt.Sprintf("%06X", r.SD)
	}
	return fmt.Sprintf("(%d:%s,%s)", r.SST, sd, rejectedCauseString(r.Cause))
}
