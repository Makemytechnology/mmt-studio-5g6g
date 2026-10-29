// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Man-Machine Interface (MMI) string parser for supplementary
// services control — TS 22.030 §6.5.
//
// The UE keypad procedures defined in §6.5.2 are:
//
//	Activation         : *SC*SI#
//	Deactivation       : #SC*SI#
//	Interrogation      : *#SC*SI#
//	Registration       : *SC*SI#  and  **SC*SI#
//	Erasure            : ##SC*SI#
//
// where SC is the Service Code (2-3 digits, see TS 22.030 Annex B
// Table B.1) and SI is one or more *-separated Supplementary
// Information items. SI parts are optional and structured as
// SIA*SIB*SIC where the columns in Table B.1 say which (if any)
// the procedure carries:
//
//	DN = Directory Number  (e.g. forwarded-to number for CFU)
//	BS = Basic Service Group (TS 22.030 Annex C)
//	T  = No Reply Condition Timer (5..30 seconds)
//	PW = Password (4-digit, used by call barring per §6.5.4)
//	R  = UUS required option
//
// This file maps a raw keypad string to a typed Procedure struct so
// callers can route to the existing supplementary CRUD (see
// supplementary.go). It does NOT yet build the on-air Facility IE
// (TS 24.080 §3.6) — see codec.go for the §-cited scaffold.

package supplementary

import (
	"fmt"
	"strconv"
	"strings"
)

// Procedure is one of the five MMI control procedures from
// TS 22.030 §6.5.2.
type Procedure int

const (
	ProcUnknown       Procedure = iota
	ProcActivation              // *SC*SI#
	ProcDeactivation            // #SC*SI#
	ProcInterrogation           // *#SC*SI#
	ProcRegistration            // *SC*SI# / **SC*SI#
	ProcErasure                 // ##SC*SI#
)

func (p Procedure) String() string {
	switch p {
	case ProcActivation:
		return "activation"
	case ProcDeactivation:
		return "deactivation"
	case ProcInterrogation:
		return "interrogation"
	case ProcRegistration:
		return "registration"
	case ProcErasure:
		return "erasure"
	}
	return "unknown"
}

// MMIRequest is a parsed UE-side MMI procedure invocation per
// TS 22.030 §6.5.2.
type MMIRequest struct {
	Procedure   Procedure // §6.5.2 — Activation / Deactivation / ...
	ServiceCode string    // SC — see TS 22.030 Annex B Table B.1
	ServiceName string    // human-readable name from Table B.1 lookup
	SIA         string    // first SI part (DN / PW / R / ...)
	SIB         string    // second SI part (Basic Service Group, TS 22.030 Annex C)
	SIC         string    // third SI part (No Reply timer, etc.)
	Raw         string    // original input
}

// ssCodeName maps a Service Code (TS 22.030 Annex B Table B.1) to
// a stable internal name used by the rest of the supplementary
// services package (CFU, CFB, ... — see supplementary.go).
//
// The SC list below is verbatim from TS 22.030 Annex B Table B.1
// (Release 19); when adding entries cite the row from that table,
// not from memory.
var ssCodeName = map[string]string{
	// — Originating identification — TS 22.081 / TS 24.607
	"30": "CLIP", // §22.081
	"31": "CLIR", // §22.081
	"76": "COLP", // §22.081
	"77": "COLR", // §22.081
	// — Call forwarding — TS 22.082 / TS 24.604
	"21":  "CFU",              // §22.082
	"67":  "CFB",              // §22.082
	"61":  "CFNRy",            // §22.082 ("CF No Reply")
	"62":  "CFNRc",            // §22.082 ("CF Not Reachable")
	"002": "CFAll",            // all CF
	"004": "CFAllConditional", // all conditional CF
	// — Call waiting — TS 22.083 / TS 24.615
	"43": "CW", // §22.083
	// — Multi-party — TS 22.084 / TS 24.080 §4.5
	// (no MMI activation; see TS 22.030 §6.5.5)
	// — UUS — TS 22.087 (via CC, not SC-only)
	// — Call barring — TS 22.088 / TS 24.611
	"33":  "BAOC",          // §22.088
	"331": "BAOIC",         // §22.088
	"332": "BAOICexHC",     // §22.088 ("BAOIC exc home")
	"35":  "BAIC",          // §22.088
	"351": "BAICRoaming",   // §22.088
	"330": "BAAll",         // all Barring services
	"333": "BAAllOutgoing", // all outgoing
	"353": "BAAllIncoming", // all incoming
	// — ECT — TS 22.091 / TS 24.629
	"96": "ECT", // §22.091 (per TS 22.030 §6.5.5)
	// — CCBS — TS 22.093
	"37": "CCBS",
	// — CNAP — TS 22.096
	"300": "CNAP",
}

// ParseMMI parses a UE-entered MMI string per TS 22.030 §6.5.2.
//
// Returns ErrMMIBadFormat for syntactic problems, ErrMMIUnknownSC for
// service codes not present in TS 22.030 Annex B Table B.1, and the
// parsed Procedure / SC / SI parts otherwise.
//
// The trailing SEND character ('#' as the procedure terminator is
// already consumed; the SEND keystroke is a UE-local event and not
// part of the on-air procedure) is handled by stripping a single
// trailing '#'.
func ParseMMI(s string) (*MMIRequest, error) {
	if s == "" {
		return nil, fmt.Errorf("empty MMI string")
	}
	raw := s
	// §6.5.2: every procedure ends with '#'. Reject anything else
	// here so a stray digit after the procedure surfaces early.
	if !strings.HasSuffix(s, "#") {
		return nil, fmt.Errorf("MMI: missing terminating '#'")
	}
	s = s[:len(s)-1]

	var proc Procedure
	switch {
	case strings.HasPrefix(s, "*#"):
		// Interrogation per §6.5.2: "*#SC*SI#".
		proc = ProcInterrogation
		s = s[2:]
	case strings.HasPrefix(s, "##"):
		// Erasure per §6.5.2: "##SC*SI#".
		proc = ProcErasure
		s = s[2:]
	case strings.HasPrefix(s, "**"):
		// Registration (alternative form) per §6.5.2: "**SC*SI#".
		proc = ProcRegistration
		s = s[2:]
	case strings.HasPrefix(s, "*"):
		// Activation OR Registration per §6.5.2 "The UE shall
		// determine from the context whether ... activation or
		// registration was intended". For CF-style services with
		// a forwarded-to DN we treat any '*'-prefix carrying SIA as
		// Registration; bare *SC# stays as Activation.
		proc = ProcActivation
		s = s[1:]
	case strings.HasPrefix(s, "#"):
		proc = ProcDeactivation
		s = s[1:]
	default:
		return nil, fmt.Errorf("MMI: bad procedure prefix in %q", raw)
	}

	// At this point s = "SC[*SIA][*SIB][*SIC]".
	parts := strings.Split(s, "*")
	if len(parts) == 0 || parts[0] == "" {
		return nil, fmt.Errorf("MMI: missing service code in %q", raw)
	}
	sc := parts[0]
	if !isAllDigits(sc) {
		return nil, fmt.Errorf("MMI: non-digit service code %q", sc)
	}
	if len(sc) < 2 || len(sc) > 3 {
		// §6.5.2: "Service Code, SC( (2 or 3 digits)".
		return nil, fmt.Errorf("MMI: SC length %d (want 2 or 3)", len(sc))
	}
	name, ok := ssCodeName[sc]
	if !ok {
		// §6.5.2 spare codes "shall be reserved for future use" —
		// surface them as an error so callers can return the
		// "operation not provided" cause from TS 24.080 §4.3.
		return nil, fmt.Errorf("MMI: unknown service code %q (TS 22.030 Annex B)", sc)
	}

	req := &MMIRequest{
		Procedure:   proc,
		ServiceCode: sc,
		ServiceName: name,
		Raw:         raw,
	}
	// §6.5.2 specifies SI may have absent slots represented by an
	// empty position between two '*'s, e.g. "*SIA**SIC" means
	// SIA present, SIB absent, SIC present. parts already encodes
	// that since strings.Split(...,"*") yields "" for empties.
	if len(parts) > 1 {
		req.SIA = parts[1]
	}
	if len(parts) > 2 {
		req.SIB = parts[2]
	}
	if len(parts) > 3 {
		req.SIC = parts[3]
	}

	// Activation→Registration disambiguation per §6.5.2 ("a call
	// forwarding request with a single * would be interpreted as
	// registration if containing a forwarded-to number, or an
	// activation if not.")
	if proc == ProcActivation && req.SIA != "" && isCallForwarding(name) {
		req.Procedure = ProcRegistration
	}

	return req, nil
}

// NoReplyTimer parses SIC as the No Reply Condition Timer per
// TS 22.030 §6.5 (5..30 seconds). Returns ok=false if the SIC slot
// is empty or out-of-range; callers should use the spec default
// (20 s) per TS 24.604 §4.5.1 in that case.
func (r *MMIRequest) NoReplyTimer() (int, bool) {
	if r.SIC == "" {
		return 0, false
	}
	t, err := strconv.Atoi(r.SIC)
	if err != nil || t < 5 || t > 30 {
		return 0, false
	}
	return t, true
}

// BarringPassword returns SIA if the procedure targets a barring
// service (TS 22.030 Annex B "PW" column for the §22.088 rows).
func (r *MMIRequest) BarringPassword() (string, bool) {
	if !isBarring(r.ServiceName) {
		return "", false
	}
	return r.SIA, r.SIA != ""
}

// ForwardedNumber returns SIA if the procedure targets a call
// forwarding service (Annex B "DN" column for the §22.082 rows).
func (r *MMIRequest) ForwardedNumber() (string, bool) {
	if !isCallForwarding(r.ServiceName) {
		return "", false
	}
	return r.SIA, r.SIA != ""
}

func isAllDigits(s string) bool {
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func isCallForwarding(name string) bool {
	switch name {
	case CFU, CFB, CFNRy, CFNRc, "CFAll", "CFAllConditional":
		return true
	}
	return false
}

func isBarring(name string) bool {
	switch name {
	case BAOC, BAOIC, BAIC,
		"BAOICexHC", "BAICRoaming",
		"BAAll", "BAAllOutgoing", "BAAllIncoming":
		return true
	}
	return false
}

// TODO(spec: TS 22.030 §6.5.4): "Registration of new password"
// procedure (`**03*ZZ*OLD_PWD*NEW_PWD*NEW_PWD#`) is not yet parsed.
// Required when the UE wants to change the call-barring password
// independently of any active session.
//
// TODO(spec: TS 22.030 §6.5.5): legacy in-call control procedures
// (HOLD, MPTY, ECT) use single-digit shortcodes (`0 SEND`,
// `1 SEND`, ...) handled at SIP layer not via this MMI parser. The
// UE-side keypad → CC dispatch lives outside services/supplementary.
//
// TODO(spec: TS 22.030 §6.5.6): Call barring of incoming calls
// when roaming (`*351*PW#`) is in the SC table above but the
// roaming-state evaluation that gates it is a network-side check
// not yet wired here.
//
// TODO(spec: TS 22.030 Annex C): Basic Service Group SIB encoding
// (e.g. "11" = telephony, "13" = facsimile group 3) is currently
// passed through as a free-form string rather than validated and
// mapped to TS 22.004 service codes.
