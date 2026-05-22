// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package speccheck scans .go source files for spec citations
// (patterns like "TS 24.008 §10.5.6.3" or "RFC 1332 §3.2") and
// verifies each cited section actually exists in a local spec PDF
// or text file under the repo.
//
// Motivation: operator policy is that every spec citation in code
// comments/logs must be runtime-checkable — no quoting from memory,
// no hallucinated clauses, no drifted section numbers when a spec
// revision moves clauses around.
//
// Usage:
//
//	go test ./nf/tools/speccheck/...  # runs the repo-wide check
//
// The test fails if any citation is either:
//
//	VERIFIED   — local PDF has the exact section number (OK)
//	MISSING    — local PDF exists but the section isn't there
//	             (likely typo or drifted section number)
//	UNLOADED   — no local PDF/text for that doc at all (operator
//	             should add it to specs/3gpp/ or specs/ietf/)
//
// The test FAILS on MISSING (that's a bad citation — must fix).
// UNLOADED is reported but does not fail by default — operator can
// load PDFs incrementally. Set SPECCHECK_STRICT=1 to fail on UNLOADED
// too, once all referenced PDFs are loaded.
package speccheck

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
)

// Citation is a single (doc, section) reference found in code.
type Citation struct {
	Doc     string // e.g. "TS 24.008", "RFC 1332"
	Section string // e.g. "10.5.6.3", "3.2"
	File    string // absolute path
	Line    int
}

// Status is the per-citation verification outcome.
type Status int

const (
	StatusVerified Status = iota // §section found in the mapped local doc
	StatusMissing                // mapped doc is local BUT section not found
	StatusUnloaded               // no local doc mapped for this TS/RFC
)

// Result of verifying one citation.
type Result struct {
	Citation Citation
	Status   Status
	DocPath  string // path of the local file consulted (empty for UNLOADED)
	Detail   string // e.g. reason the section wasn't found
}

// citationRE matches "TS X.Y §a.b.c" or "RFC N §x.y" with the doc
// identifier and section glued together. Loose "§x.y" forms without
// a preceding doc id are NOT extracted — too ambiguous to verify.
var citationRE = regexp.MustCompile(
	`\b(TS\s+[0-9]+\.[0-9]+|RFC\s+[0-9]+)\s+§\s*([0-9A-Za-z]+(?:\.[0-9A-Za-z]+)*)`,
)

// wildcardRE matches placeholder sections like "§4.2.x" where the
// author deliberately left the subclause unspecified. Skipped by
// Scan because there's nothing to verify — just flag them so the
// author can come back later.
var wildcardRE = regexp.MustCompile(`\.x$|\.X$`)

// docMap maps canonical doc IDs to filepath.Glob patterns (relative
// to repo root) where the local PDF/text is expected to live. Add
// new specs here as they're loaded.
//
// Kept static because each entry is a deliberate decision: which
// exact revision of the PDF the operator considers authoritative.
//
// Layout: every 3GPP TS PDF lives under specs/3gpp/ (flat); every
// IETF RFC under specs/ietf/. The previous codecs/*/standards/ and
// services/*/standards/ subdirectories were consolidated into
// specs/3gpp/ to keep one source of truth — see CHANGELOG / commit
// "specs: consolidate every PDF into specs/3gpp + specs/ietf".
var docMap = map[string][]string{
	// ── TS 22.xxx (Service requirements) ────────────────────────────
	"TS 22.030": {"specs/3gpp/ts_122030v*p.pdf"}, // MMI procedures
	"TS 22.090": {"specs/3gpp/ts_122090v*p.pdf"}, // USSD svc requirements
	"TS 22.101": {"specs/3gpp/ts_122101v*p.pdf"}, // Service principles (Emergency Calls §10)
	"TS 22.125": {"specs/3gpp/ts_122125v*p.pdf"}, // UAS service requirements (UAV)
	"TS 22.137": {"specs/3gpp/ts_122137v*p.pdf"},
	"TS 22.186": {"specs/3gpp/ts_122186v*p.pdf"}, // V2X service requirements (5G)
	"TS 22.261": {"specs/3gpp/ts_122261v*p.pdf"}, // Service requirements (NTN among others) // ISAC stage 1 (sensing service)
	"TS 22.278": {"specs/3gpp/ts_122278v*p.pdf"}, // ProSe service requirements
	"TS 22.289": {"specs/3gpp/ts_122289v*p.pdf"},
	"TS 22.369": {"specs/3gpp/ts_122369v*p.pdf"}, // Ambient IoT stage 1

	// ── TS 23.xxx (Architecture) ────────────────────────────────────
	"TS 23.003": {"specs/3gpp/ts_123003v*p.pdf"},
	"TS 23.038": {"specs/3gpp/ts_123038v*p.pdf"}, // SMS alphabets / encoding
	"TS 23.040": {"specs/3gpp/ts_123040v*p.pdf"}, // SMS technical realization (TPDU)
	"TS 23.167": {"specs/3gpp/ts_123167v*p.pdf"}, // IMS Emergency Sessions
	"TS 23.228": {"specs/3gpp/ts_123228v*p.pdf"},
	"TS 23.256": {"specs/3gpp/ts_123256v*p.pdf"}, // UAS application architecture
	"TS 23.280": {"specs/3gpp/ts_123280v*p.pdf"},
	"TS 23.281": {"specs/3gpp/ts_123281v*p.pdf"}, // MCVideo Stage 2 (functional architecture)
	"TS 23.282": {"specs/3gpp/ts_123282v*p.pdf"}, // MCData Stage 2 (functional architecture)
	"TS 23.287": {"specs/3gpp/ts_123287v*p.pdf"},
	"TS 23.288": {"specs/3gpp/ts_123288v*p.pdf"},
	"TS 23.289": {"specs/3gpp/ts_123289v*p.pdf"},
	"TS 23.304": {"specs/3gpp/ts_123304v*p.pdf"}, // 5G ProSe architecture (D2D)
	"TS 23.379": {"specs/3gpp/ts_123379v*p.pdf"},
	"TS 23.434": {"specs/3gpp/ts_123434v*p.pdf"}, // SEAL — Service Enabler Architecture Layer
	"TS 23.401": {"specs/3gpp/ts_123401v*p.pdf"}, // EPS architecture (CIoT, PSM, eDRX)
	"TS 23.501": {"specs/3gpp/ts_123501v*p.pdf"},
	"TS 23.502": {"specs/3gpp/ts_123502v*p.pdf"},
	"TS 23.503": {"specs/3gpp/ts_123503v*p.pdf"},
	"TS 23.548": {"specs/3gpp/ts_123548v*p.pdf"},
	"TS 23.558": {"specs/3gpp/ts_123558v*p.pdf"}, // EDGEAPP — EAS / EES / ECS architecture
	"TS 23.586": {"specs/3gpp/ts_123586v*p.pdf"}, // Ranging / Sidelink Positioning
	"TS 23.682": {"specs/3gpp/ts_123682v*p.pdf"}, // SCEF / NIDD architecture
	"TS 23.273": {"specs/3gpp/ts_123273v*p.pdf"}, // 5G LCS Stage 2 (positioning architecture)

	// ── TS 24.xxx (NAS / SM protocols) ──────────────────────────────
	"TS 24.007": {"specs/3gpp/ts_124007v*p.pdf"},
	"TS 24.008": {"specs/3gpp/ts_124008v*p.pdf"},
	"TS 24.011": {"specs/3gpp/ts_124011v*p.pdf"}, // Point-to-point SMS support (CP/RP)
	"TS 24.080": {"specs/3gpp/ts_124080v*p.pdf"}, // SS layer-3 — Facility / SS-Operation codec
	"TS 24.147": {"specs/3gpp/ts_124147v*p.pdf"}, // IMS conferencing (focus / participant)
	"TS 24.229": {"specs/3gpp/ts_124229v*p.pdf"},
	"TS 24.250": {"specs/3gpp/ts_124250v*p.pdf"}, // Reliable Data Service over CP CIoT
	"TS 24.279": {"specs/3gpp/ts_124279v*p.pdf"},
	"TS 24.301": {"specs/3gpp/ts_124301v*p.pdf"},
	"TS 24.379": {"specs/3gpp/ts_124379v*p.pdf"},
	"TS 24.380": {"specs/3gpp/ts_124380v*p.pdf"},
	"TS 24.390": {"specs/3gpp/ts_124390v*p.pdf"}, // USSD over IMS
	"TS 24.501": {"specs/3gpp/ts_124501v*p.pdf"},
	"TS 24.604": {"specs/3gpp/ts_124604v*p.pdf"}, // CDIV (CFU/CFB/CFNRy/CFNRc/CD) using IMS
	"TS 24.607": {"specs/3gpp/ts_124607v*p.pdf"}, // OIP / OIR using IMS
	"TS 24.608": {"specs/3gpp/ts_124608v*p.pdf"}, // TIP / TIR using IMS
	"TS 24.610": {"specs/3gpp/ts_124610v*p.pdf"}, // Communication HOLD using IMS
	"TS 24.611": {"specs/3gpp/ts_124611v*p.pdf"}, // ACR + Communication Barring using IMS
	"TS 24.615": {"specs/3gpp/ts_124615v*p.pdf"}, // Communication Waiting (CW) using IMS
	"TS 24.629": {"specs/3gpp/ts_124629v*p.pdf"}, // Explicit Communication Transfer (ECT)
	"TS 24.502": {"specs/3gpp/ts_124502v*p.pdf"}, // NWu (IKEv2 / EAP-5G for non-3GPP access)
	"TS 24.546": {"specs/3gpp/ts_124546v*p.pdf"}, // SEAL Configuration Management (CM)
	"TS 24.547": {"specs/3gpp/ts_124547v*p.pdf"}, // SEAL Identity Management (IM)
	"TS 24.548": {"specs/3gpp/ts_124548v*p.pdf"}, // SEAL Group Management (GM)
	"TS 24.554": {"specs/3gpp/ts_124554v*p.pdf"}, // 5G ProSe NAS (PC3a / PC3, etc.)
	"TS 24.555": {"specs/3gpp/ts_124555v*p.pdf"}, // 5G ProSe PC5 signalling protocol
	"TS 24.577": {"specs/3gpp/ts_124577v*p.pdf"},
	"TS 24.587": {"specs/3gpp/ts_124587v*p.pdf"}, // V2X NAS layer
	"TS 24.588": {"specs/3gpp/ts_124588v*p.pdf"}, // V2X PC5 signalling protocol

	// ── TS 29.xxx (SBI + PFCP + GTP) ────────────────────────────────
	"TS 29.002": {"specs/3gpp/ts_129002v*p.pdf"}, // MAP application part (legacy SS-Operation codes)
	"TS 29.060": {"specs/3gpp/ts_129060v*p.pdf"},
	"TS 29.244": {"specs/3gpp/ts_129244v*p.pdf"},
	"TS 29.274": {"specs/3gpp/ts_129274v*p.pdf"},
	"TS 29.281": {"specs/3gpp/ts_129281v*p.pdf"},
	"TS 29.502": {"specs/3gpp/ts_129502v*p.pdf"},
	"TS 29.503": {"specs/3gpp/ts_129503v*p.pdf"},
	"TS 29.505": {"specs/3gpp/ts_129505v*p.pdf"},
	"TS 29.509": {"specs/3gpp/ts_129509v*p.pdf"},
	"TS 29.510": {"specs/3gpp/ts_129510v*p.pdf"},
	"TS 29.512": {"specs/3gpp/ts_129512v*p.pdf"},
	"TS 29.513": {"specs/3gpp/ts_129513v*p.pdf"},
	"TS 29.514": {"specs/3gpp/ts_129514v*p.pdf"},
	"TS 29.517": {"specs/3gpp/ts_129517v*p.pdf"},
	"TS 29.518": {"specs/3gpp/ts_129518v*p.pdf"},
	"TS 29.519": {"specs/3gpp/ts_129519v*p.pdf"},
	"TS 29.522": {"specs/3gpp/ts_129522v*p.pdf"},
	"TS 29.531": {"specs/3gpp/ts_129531v*p.pdf"},
	"TS 29.540": {"specs/3gpp/ts_129540v*p.pdf"}, // Nsmsf service (SMS Function)
	"TS 29.571": {"specs/3gpp/ts_129571v*p.pdf"},
	"TS 29.573": {"specs/3gpp/ts_129573v*p.pdf"},
	"TS 29.515": {"specs/3gpp/ts_129515v*p.pdf"}, // GMLC service operations (Ngmlc)
	"TS 29.572": {"specs/3gpp/ts_129572v*p.pdf"}, // LMF service operations (Nlmf_Location)

	// ── TS 28.xxx (Management) ──────────────────────────────────────
	"TS 28.532": {"specs/3gpp/ts_128532v*p.pdf"},

	// ── TS 31.xxx (USIM) / TS 33.xxx (Security) / TS 35.xxx (Algos) ─
	"TS 31.102": {"specs/3gpp/ts_131102v*p.pdf"},
	"TS 33.102": {"specs/3gpp/ts_133102v*p.pdf"},
	"TS 33.203": {"specs/3gpp/ts_133203v*p.pdf"}, // 3G/IMS access security (IMS-AKA, SA-binding)
	"TS 33.180": {"specs/3gpp/ts_133180v*p.pdf"}, // Mission Critical security (MIKEY-SAKKE, MCX user auth)
	"TS 33.220": {"specs/3gpp/ts_133220v*p.pdf"},
	"TS 33.402": {"specs/3gpp/ts_133402v*p.pdf"}, // Security for non-3GPP access (legacy EPS; some N3IWF refs)
	"TS 33.501": {"specs/3gpp/ts_133501v*p.pdf"},
	"TS 35.205": {"specs/3gpp/ts_135205v*p.pdf"},
	"TS 35.206": {"specs/3gpp/ts_135206v*p.pdf"},
	"TS 35.207": {"specs/3gpp/ts_135207v*p.pdf"},

	// ── TS 36.xxx (E-UTRAN / S1AP / LPP) ────────────────────────────
	"TS 36.355": {"specs/3gpp/ts_136355v*p.pdf"}, // LPP (LTE Positioning Protocol)
	"TS 36.413": {"specs/3gpp/ts_136413v*p.pdf"},

	// ── TS 37.xxx (Multi-RAT joint specs) ───────────────────────────
	"TS 37.355": {"specs/3gpp/ts_137355v*p.pdf"}, // LPP (multi-RAT) — NR + LTE positioning

	// ── TS 38.xxx (NG-RAN / NGAP / NR) ──────────────────────────────
	"TS 38.211": {"specs/3gpp/ts_138211v*p.pdf"}, // NR PHY channels and signals (PRS resources)
	"TS 38.300": {"specs/3gpp/ts_138300v*p.pdf"}, // NR overall architecture (NTN clause §16.14)
	"TS 38.305": {"specs/3gpp/ts_138305v*p.pdf"}, // NG-RAN UE positioning Stage 2 (E-CID, RTT, TDOA, AoA, AoD)
	"TS 38.331": {"specs/3gpp/ts_138331v*p.pdf"}, // NR RRC (NTN-specific IEs)
	"TS 38.412": {"specs/3gpp/ts_138412v*p.pdf"},
	"TS 38.413": {"specs/3gpp/ts_138413v*p.pdf"},
	"TS 38.455": {"specs/3gpp/ts_138455v*p.pdf"}, // NRPPa (NR Positioning Protocol A)
	"TS 38.821": {"specs/3gpp/ts_138821v*p.pdf"}, // TR — NR NTN solutions (informative study; v16.2)

	// ── IETF RFCs (loaded under specs/ietf/) ────────────────────────
	"RFC 1332": {"specs/ietf/rfc1332*.txt", "specs/ietf/rfc1332*.pdf"},
	"RFC 1661": {"specs/ietf/rfc1661*.txt", "specs/ietf/rfc1661*.pdf"},
	"RFC 1877": {"specs/ietf/rfc1877*.txt", "specs/ietf/rfc1877*.pdf"},
	"RFC 3232": {"specs/ietf/rfc3232*.txt", "specs/ietf/rfc3232*.pdf"},
	"RFC 3261": {"specs/ietf/rfc3261*.txt", "specs/ietf/rfc3261*.pdf"}, // SIP
	"RFC 3310": {"specs/ietf/rfc3310*.txt", "specs/ietf/rfc3310*.pdf"}, // HTTP Digest AKA (IMS REGISTER auth)
	// TODO RFC 2617 — HTTP Digest Authentication (referenced by RFC 3310
	// for opaque + A1). PDF not yet under specs/ietf/; download from
	// rfc-editor.org/rfc/rfc2617.txt and the citations in
	// services/ims/cscf/handler.go + services/ims/aka.go will verify.
	// TODO RFC 3264 — SDP Offer/Answer Model (referenced by services/ims
	// for re-INVITE hold/resume semantics + services/ims/media). PDF
	// not yet under specs/ietf/; download from
	// rfc-editor.org/rfc/rfc3264.txt and the citations in
	// services/ims/sdp.go + services/ims/media/media.go will verify.
	"RFC 3526": {"specs/ietf/rfc3526*.txt", "specs/ietf/rfc3526*.pdf"}, // MODP DH groups for IKE
	"RFC 3550": {"specs/ietf/rfc3550*.txt", "specs/ietf/rfc3550*.pdf"}, // RTP — fixed header layout
	"RFC 3748": {"specs/ietf/rfc3748*.txt", "specs/ietf/rfc3748*.pdf"}, // EAP
	"RFC 4303": {"specs/ietf/rfc4303*.txt", "specs/ietf/rfc4303*.pdf"}, // IPsec ESP
	"RFC 4566": {"specs/ietf/rfc4566*.txt", "specs/ietf/rfc4566*.pdf"}, // SDP — session description protocol
	"RFC 4493": {"specs/ietf/rfc4493*.txt", "specs/ietf/rfc4493*.pdf"}, // AES-CMAC
	"RFC 4868": {"specs/ietf/rfc4868*.txt", "specs/ietf/rfc4868*.pdf"}, // HMAC-SHA-256/384/512 in IPsec
	"RFC 4960": {"specs/ietf/rfc4960*.txt", "specs/ietf/rfc4960*.pdf"}, // SCTP
	"RFC 5031": {"specs/ietf/rfc5031*.txt", "specs/ietf/rfc5031*.pdf"}, // URN for Services (urn:service:sos)
	"RFC 6458": {"specs/ietf/rfc6458*.txt", "specs/ietf/rfc6458*.pdf"}, // SCTP sockets API
	"RFC 7296": {"specs/ietf/rfc7296*.txt", "specs/ietf/rfc7296*.pdf"}, // IKEv2
	"RFC 7807": {"specs/ietf/rfc7807*.txt", "specs/ietf/rfc7807*.pdf"}, // HTTP Problem Details
	"RFC 9048": {"specs/ietf/rfc9048*.txt", "specs/ietf/rfc9048*.pdf"}, // EAP-AKA'
}

// Scan walks root recursively, extracts every citation from .go
// files (excluding _test.go, generated code, and vendored deps),
// and returns the full list.
func Scan(root string) ([]Citation, error) {
	var cites []Citation
	err := filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			name := filepath.Base(path)
			// Skip generated codec dirs — their citations are in
			// spec-generated comments, not our responsibility.
			if name == "generated" || name == "vendor" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return nil // ignore unreadable
		}
		defer f.Close()
		scanner := bufio.NewScanner(f)
		// Some doc-heavy files have long comment blocks; bump limit.
		scanner.Buffer(make([]byte, 1<<20), 1<<20)
		ln := 0
		for scanner.Scan() {
			ln++
			matches := citationRE.FindAllStringSubmatch(scanner.Text(), -1)
			for _, m := range matches {
				doc := normalizeDoc(m[1])
				sec := m[2]
				if wildcardRE.MatchString(sec) {
					// Intentional placeholder — skip silently.
					// TODO log site for follow-up cleanup.
					continue
				}
				cites = append(cites, Citation{
					Doc: doc, Section: sec, File: path, Line: ln,
				})
			}
		}
		return nil
	})
	return cites, err
}

// normalizeDoc collapses whitespace in a doc identifier so
// "TS  29.244", "TS\t29.244", and "TS 29.244" map to the same key.
func normalizeDoc(raw string) string {
	fields := strings.Fields(raw)
	return strings.Join(fields, " ")
}

// pdfCache memoizes pdftotext output + the set of section numbers
// that appear at the start of lines in that text — avoids running
// pdftotext once per citation.
type pdfCache struct {
	mu       sync.Mutex
	byPath   map[string]sectionSet
}

// sectionSet is the set of section numbers (e.g. "10.5.6.3") that
// appear in a spec document. Only "header-like" occurrences count —
// i.e. the number at the start of a line, or the start of a table
// entry; bare textual references inside paragraphs are not counted
// because the operator wants to verify the clause EXISTS, not just
// that the characters appear somewhere.
type sectionSet map[string]struct{}

func newCache() *pdfCache { return &pdfCache{byPath: map[string]sectionSet{}} }

func (c *pdfCache) sections(path string) (sectionSet, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if s, ok := c.byPath[path]; ok {
		return s, nil
	}
	text, err := extractText(path)
	if err != nil {
		return nil, err
	}
	s := collectSections(text)
	c.byPath[path] = s
	return s, nil
}

// extractText runs pdftotext on a PDF (or reads a .txt verbatim).
func extractText(path string) (string, error) {
	if strings.HasSuffix(path, ".txt") {
		b, err := os.ReadFile(path)
		return string(b), err
	}
	// pdftotext -layout preserves the table / heading structure best.
	cmd := exec.Command("pdftotext", "-layout", path, "-")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("pdftotext %s: %w", path, err)
	}
	return string(out), nil
}

// headerRE matches a section number at the start of a line in the
// pdftotext -layout output. 3GPP / IETF headings follow the pattern
// `{number}  {Title}` (the number followed by ≥1 whitespace char
// before the title), so the regex looks for:
//
//   - top-level body sections: "7" / "10"
//   - multi-level sections:    "10.5.6.3" / "5.4.4a"
//   - annex clauses:           "A.9" / "C.3.2" (single uppercase
//                              letter as the first component)
//
// Each component allows an optional trailing lowercase letter to
// cover 3GPP's amendment numbering ("2a", "4b", "2.1A"). Titles can
// start with letters OR digits ("5GSM cause", "3GPP TS ..."), so
// the tail class is any non-whitespace.
//
// NOTE: ≥1 (not ≥2) whitespace. Some 3GPP specs (e.g. TS 24.229)
// emit top-level body headings with a single space between the
// clause number and the title ("5.4.1 Registration and authentication"),
// while TOC rows and deeper-level headings use Word-tab multi-space
// padding. Requiring ≥2 caused us to miss the §5.4.1-style rows and
// false-flag live citations as MISSING.
var headerRE = regexp.MustCompile(
	// Optional trailing dot after the section number accommodates
	// RFC-style headings ("1.  Introduction", "3.2.  Sending...");
	// 3GPP headings omit it ("7.5.4       PFCP Session Mod Req").
	`(?m)^\s*((?:[A-Z]|[0-9]+)[A-Za-z]?(?:\.[0-9]+[A-Za-z]?)*)\.?\s+\S`,
)

// lineNumberedHeaderRE handles PDFs that prefix every body line with a
// running line counter (TS 29.002 v19 emits e.g. "12347    17.6.4
// Supplementary service operations" and "853   13.1   MAP_SEND_..."),
// which makes headerRE record the counter as the section. We require
// the section to be multi-level (≥1 dot) so a bare prefixed integer
// like "17" in flowing text won't be mis-captured.
var lineNumberedHeaderRE = regexp.MustCompile(
	`(?m)^\s*[0-9]+\s+((?:[A-Z]|[0-9]+)[A-Za-z]?(?:\.[0-9]+[A-Za-z]?)+)\.?\s+\S`,
)

// collectSections walks the extracted text line-by-line and records
// every section-number-looking token at line start.
func collectSections(text string) sectionSet {
	s := make(sectionSet)
	for _, m := range headerRE.FindAllStringSubmatch(text, -1) {
		s[m[1]] = struct{}{}
	}
	for _, m := range lineNumberedHeaderRE.FindAllStringSubmatch(text, -1) {
		s[m[1]] = struct{}{}
	}
	return s
}

// Verify returns a Result for each citation — hitting the PDF cache
// so we pdftotext once per unique doc.
func Verify(root string, cites []Citation) ([]Result, error) {
	cache := newCache()
	out := make([]Result, 0, len(cites))
	for _, c := range cites {
		patterns, ok := docMap[c.Doc]
		if !ok {
			out = append(out, Result{
				Citation: c, Status: StatusUnloaded,
				Detail: "no local mapping; add to speccheck.docMap",
			})
			continue
		}
		path := ""
		for _, pat := range patterns {
			matches, _ := filepath.Glob(filepath.Join(root, pat))
			if len(matches) > 0 {
				sort.Strings(matches) // stable pick
				path = matches[0]
				break
			}
		}
		if path == "" {
			out = append(out, Result{
				Citation: c, Status: StatusUnloaded,
				Detail: "no local file matching mapped patterns; load the doc",
			})
			continue
		}
		sections, err := cache.sections(path)
		if err != nil {
			out = append(out, Result{
				Citation: c, Status: StatusUnloaded, DocPath: path,
				Detail: fmt.Sprintf("extract failed: %v", err),
			})
			continue
		}
		if _, found := sections[c.Section]; found {
			out = append(out, Result{
				Citation: c, Status: StatusVerified, DocPath: path,
			})
			continue
		}
		out = append(out, Result{
			Citation: c, Status: StatusMissing, DocPath: path,
			Detail: fmt.Sprintf("§%s not found at line-start in %s",
				c.Section, filepath.Base(path)),
		})
	}
	return out, nil
}

// Report prints a per-status summary to w. Returns (verified,
// missing, unloaded) counts.
func Report(w io.Writer, results []Result) (verified, missing, unloaded int) {
	byStatus := map[Status][]Result{}
	for _, r := range results {
		byStatus[r.Status] = append(byStatus[r.Status], r)
	}
	verified = len(byStatus[StatusVerified])
	missing = len(byStatus[StatusMissing])
	unloaded = len(byStatus[StatusUnloaded])

	if missing > 0 {
		fmt.Fprintf(w, "\n❌ MISSING (%d) — section not found in local PDF; bad citation:\n", missing)
		for _, r := range byStatus[StatusMissing] {
			fmt.Fprintf(w, "  %s §%s  (%s:%d)\n",
				r.Citation.Doc, r.Citation.Section,
				relPathSafe(r.Citation.File), r.Citation.Line)
		}
	}
	if unloaded > 0 {
		// Dedup by (doc) — users care about which docs to load, not
		// every call-site.
		docs := map[string]int{}
		for _, r := range byStatus[StatusUnloaded] {
			docs[r.Citation.Doc]++
		}
		var keys []string
		for k := range docs {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		fmt.Fprintf(w, "\n📄 UNLOADED (%d citations across %d docs) — add these PDFs to specs/3gpp/:\n",
			unloaded, len(docs))
		for _, k := range keys {
			fmt.Fprintf(w, "  %s  (%d citations)\n", k, docs[k])
		}
	}
	fmt.Fprintf(w, "\n✅ VERIFIED %d   ❌ MISSING %d   📄 UNLOADED %d\n",
		verified, missing, unloaded)
	return
}

// relPathSafe shortens a path against the working directory if
// possible, for cleaner output. Falls back to the absolute form.
func relPathSafe(p string) string {
	if wd, err := os.Getwd(); err == nil {
		if rel, err := filepath.Rel(wd, p); err == nil && !strings.HasPrefix(rel, "..") {
			return rel
		}
	}
	return p
}
