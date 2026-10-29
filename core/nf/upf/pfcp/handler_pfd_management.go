// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// handler_pfd_management.go — TS 29.244 §6.2.5 PFD Management on the
// UP function side: accept the SMF-pushed PFD set, cache it per app,
// and reply §7.4.3.2 PFD Management Response with cause=Accepted.
//
// Spec anchors:
//   - TS 29.244 §6.2.5.3  UP function behaviour: store the PFDs from
//                          the Request, replacing any prior set for
//                          the listed Application IDs. Empty Request
//                          (no ApplicationIDsPFDs) clears every PFD
//                          (full resync — §6.2.5.1).
//   - TS 29.244 §7.4.3.1  Request shape (Message Type 3).
//   - TS 29.244 §7.4.3.2  Response shape (Message Type 4) — Cause IE
//                          §8.2.1 mandatory; everything else optional.
//   - TS 29.244 §8.2.39   PFD Contents IE — flags + length-prefixed
//                          fields (FD / URL / DN / CP).
//   - TS 23.501 §5.8.2.4.2 Traffic Detection Information at UPF —
//                          stored PFDs drive PDR matching.
//
// Storage today: an in-memory cache, keyed by Application ID, holding
// the parsed (kind, pattern) pairs decoded from PFD Contents. The
// dataplane (DPDK PMD or socket/TUN) does not yet read the cache —
// the SDF/DN matchers in nf/upf/dataplane/src/upf_dpi.c remain stubs.
// The webservice surfaces the cache via /api/dpi/upf-pfd-state for
// OAM + tester verification.
package pfcp

import (
	"encoding/binary"
	"fmt"
	"net"
	"sort"
	"sync"

	genpfcp "github.com/mmt/pfcpgen/generated"
	runtime "github.com/mmt/pfcpgen/pkg/runtime"
)

// ParsedPFD is the cached, post-decode shape of one entry. "Kind"
// mirrors the §8.2.39 sub-field that carried the pattern: "fd" for
// Flow Description, "url" for URL, "dn" for Domain Name, "cp" for
// Custom PFD Content. Tester / GUI rendering keys on this.
type ParsedPFD struct {
	Kind    string `json:"kind"`
	Pattern string `json:"pattern"`
}

// pfdCache is the process-wide singleton — guarded by its own mutex
// so the API reader and the PFCP handler don't collide. Keyed by
// Application ID. nil entry = "app exists, no PFDs" (legitimate per
// §6.2.5.1 partial removal).
var (
	pfdMu    sync.RWMutex
	pfdCache = map[string][]ParsedPFD{}
)

// GetPFDCache returns a deep-ish snapshot of the UPF-side PFD cache
// for OAM / tester observation. Callers must not mutate the returned
// maps; the lists are copies.
func GetPFDCache() map[string][]ParsedPFD {
	pfdMu.RLock()
	defer pfdMu.RUnlock()
	out := make(map[string][]ParsedPFD, len(pfdCache))
	for k, v := range pfdCache {
		cp := make([]ParsedPFD, len(v))
		copy(cp, v)
		out[k] = cp
	}
	return out
}

// AppCount and TotalRules drive the OAM dashboard counters.
func PFDCacheStats() (apps, rules int) {
	pfdMu.RLock()
	defer pfdMu.RUnlock()
	for _, v := range pfdCache {
		rules += len(v)
	}
	return len(pfdCache), rules
}

// ListPFDApps returns the set of cached Application IDs (sorted) for
// stable test assertions.
func ListPFDApps() []string {
	pfdMu.RLock()
	defer pfdMu.RUnlock()
	out := make([]string, 0, len(pfdCache))
	for k := range pfdCache {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// handlePFDManagement implements TS 29.244 §6.2.5.3.
func (h *Handler) handlePFDManagement(hdr *runtime.Header, payload []byte, peer *net.UDPAddr) {
	defer h.traceHandler("pfd-management", peer)()

	var req genpfcp.PFDManagementRequest
	if err := req.Decode(payload); err != nil {
		h.log.Warnf("PFD-Management decode failed: %v — replying with cause=64 (System failure)", err)
		h.sendPFDResponse(peer, hdr.SequenceNumber, 64) // §8.2.1 System failure
		return
	}

	// Spec semantics — §6.2.5.1: an empty Request clears the cache
	// entirely; a non-empty Request replaces the per-app entries for
	// every listed Application ID. App IDs absent from the Request
	// keep their cached PFDs (per-app delta semantics).
	pfdMu.Lock()
	if len(req.ApplicationIDsPFDs) == 0 {
		pfdCache = map[string][]ParsedPFD{}
		h.log.Infof("PFD-Management: full cache cleared by empty Request from %s "+
			"(TS 29.244 §6.2.5.1)", peer)
	} else {
		for _, app := range req.ApplicationIDsPFDs {
			appID := string(app.ApplicationID.Value)
			parsed := make([]ParsedPFD, 0, len(app.PFDContext))
			for _, pc := range app.PFDContext {
				for _, pfd := range pc.PFDContents {
					if p := decodePFDContents(pfd.Value); p != nil {
						parsed = append(parsed, *p)
					}
				}
			}
			// Per-app removal is signalled by sending an
			// ApplicationIDsPFDs with no PFDContext.
			if len(parsed) == 0 {
				delete(pfdCache, appID)
			} else {
				pfdCache[appID] = parsed
			}
		}
		h.log.Infof("PFD-Management: stored %d app(s) from %s "+
			"(TS 29.244 §6.2.5.3)", len(req.ApplicationIDsPFDs), peer)
	}
	pfdMu.Unlock()

	// §7.4.3.2 Response with cause=1 (Request accepted).
	h.sendPFDResponse(peer, hdr.SequenceNumber, 1)
}

func (h *Handler) sendPFDResponse(peer *net.UDPAddr, seq uint32, cause uint8) {
	resp := &genpfcp.PFDManagementResponse{
		SequenceNumber: seq,
		Cause:          genpfcp.Cause{Value: cause},
	}
	out, err := stripHeader(resp)
	if err != nil {
		h.log.Warnf("PFD-Management response encode: %v", err)
		return
	}
	if err := h.t.SendResponse(peer, genpfcp.MessageTypePFDManagementResponse, 0, seq, out); err != nil {
		h.log.Warnf("PFD-Management response send: %v", err)
	}
}

// decodePFDContents parses one TS 29.244 §8.2.39 PFD Contents value
// into a ParsedPFD. We only consume the four common variants the
// SMF-side encoder emits today (FD / URL / DN / CP); other variants
// are decoded as a fallback "raw" entry so the cache observation
// surface remains lossless.
func decodePFDContents(v []byte) *ParsedPFD {
	if len(v) < 2 {
		return nil
	}
	const (
		flagFD  = byte(0x01)
		flagURL = byte(0x02)
		flagDN  = byte(0x04)
		flagCP  = byte(0x08)
	)
	flags := v[0]
	pos := 2 // octet 5 = flags, octet 6 = spare
	readField := func() (string, bool) {
		if pos+2 > len(v) {
			return "", false
		}
		ln := int(binary.BigEndian.Uint16(v[pos : pos+2]))
		pos += 2
		if pos+ln > len(v) {
			return "", false
		}
		s := string(v[pos : pos+ln])
		pos += ln
		return s, true
	}
	if flags&flagFD != 0 {
		if s, ok := readField(); ok {
			return &ParsedPFD{Kind: "fd", Pattern: s}
		}
	}
	if flags&flagURL != 0 {
		if s, ok := readField(); ok {
			return &ParsedPFD{Kind: "url", Pattern: s}
		}
	}
	if flags&flagDN != 0 {
		if s, ok := readField(); ok {
			return &ParsedPFD{Kind: "dn", Pattern: s}
		}
	}
	if flags&flagCP != 0 {
		if s, ok := readField(); ok {
			return &ParsedPFD{Kind: "cp", Pattern: s}
		}
	}
	// Unknown / unsupported variant — preserve as hex so the GUI can
	// still render the cache rather than silently dropping rows.
	return &ParsedPFD{Kind: "raw", Pattern: fmt.Sprintf("%x", v)}
}
