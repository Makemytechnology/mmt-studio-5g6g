// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// pfd_management.go — TS 29.244 §6.2.5 PFCP PFD Management procedure
// (CP-side / SMF-initiated push of operator-curated PFDs to the UPF).
//
// Spec anchors:
//   - TS 29.244 §6.2.5    PFD Management procedure (general).
//   - TS 29.244 §6.2.5.2  CP function behaviour: SMF builds an
//                          ApplicationIDsPFDs grouped IE per app and
//                          sends one PFD-Management Request to each
//                          associated UPF.
//   - TS 29.244 §7.4.3.1  PFD Management Request (Message Type 3).
//   - TS 29.244 §7.4.3.2  PFD Management Response (Message Type 4).
//   - TS 29.244 §8.2.39   PFD Contents IE — flags + length-prefixed
//                          fields for FD / URL / DN / CP variants.
//   - TS 29.244 §8.2.40   PFD context (grouped) — wraps PFD Contents.
//   - TS 29.244 §8.2.42   Application ID's PFDs (grouped).
//   - TS 23.501 §5.8.2.4  Traffic Detection — operator app catalogue.
//   - TS 23.501 §5.8.2.8.4 PFD lifecycle (NEF push, local cache).
//
// Today PFDs are sourced from the operator-curated cache in
// security/dpi (driven by /api/dpi/* and the seed defaults). When a
// real PCF/NEF (TS 29.122 T8) lands, the same SendPFDManagement path
// will carry NEF-pushed deltas without further changes.
package upfclient

import (
	"encoding/binary"
	"fmt"
	"sync"

	genpfcp "github.com/mmt/pfcpgen/generated"
)

// PFDRule mirrors one row in dpi_pfd_rules so this package doesn't
// have to import security/dpi (avoiding a Go-module dep cycle: nf →
// security would force security to be a leaf, and security/dpi ought
// to be a leaf since it's PCC-flavoured operator state). Callers
// build []PFDRule from their own source — for our path that's the
// webservice with security/dpi.GetPFDRules.
type PFDRule struct {
	AppID         string
	DetectionType string // sni | dns | host | ip_range | port_range
	Pattern       string
}

// AppPFDs groups every PFDRule for one Application ID — the wire-
// shape of TS 29.244 §8.2.42 Application ID's PFDs (one ApplicationID
// + N PFDContext, each with one PFDContents).
type AppPFDs struct {
	AppID string
	Rules []PFDRule
}

// GroupRulesByApp packages flat PFD rows into per-app groupings the
// PFCP encoder consumes. Empty AppID rows are dropped (a PFDRule
// without an app reference can't ride the wire — TS 29.244 §8.2.42
// keys the grouped IE on Application ID).
func GroupRulesByApp(rules []PFDRule) []AppPFDs {
	idx := map[string]int{}
	out := make([]AppPFDs, 0)
	for _, r := range rules {
		if r.AppID == "" {
			continue
		}
		i, ok := idx[r.AppID]
		if !ok {
			out = append(out, AppPFDs{AppID: r.AppID})
			i = len(out) - 1
			idx[r.AppID] = i
		}
		out[i].Rules = append(out[i].Rules, r)
	}
	return out
}

// EncodePFDContents builds one TS 29.244 §8.2.39 PFD Contents IE
// payload for a single (detection_type, pattern) tuple.
//
// §8.2.39 octet layout:
//
//	Octet 5: flags  FD URL DN CP DNP AFD AURL ADNP   (bits 1..8)
//	Octet 6: spare
//	If FD  set: 2 octets length + N octets Flow-Description
//	If URL set: 2 octets length + N octets URL
//	If DN  set: 2 octets length + N octets Domain Name
//	If CP  set: 2 octets length + N octets Custom PFD content
//	(other variants — DNP, AFD, AURL, ADNP — not used here)
//
// Mapping our DetectionType → §8.2.39 variant:
//   - sni / dns / host       → DN  (Domain Name; matches the
//                                    operator-meaningful identifier)
//   - ip_range               → FD  (Flow Description, SDF filter
//                                    syntax: "permit out ip from any
//                                    to <cidr>")
//   - port_range             → FD  ("permit out ip from any to any
//                                    <range>")
//
// Returns nil for an unknown detection_type so the caller can skip.
func EncodePFDContents(detectionType, pattern string) []byte {
	if pattern == "" {
		return nil
	}
	const (
		flagFD = byte(0x01)
		flagDN = byte(0x04)
	)
	var flag byte
	var content string
	switch detectionType {
	case "sni", "dns", "host":
		flag = flagDN
		content = pattern
	case "ip_range":
		flag = flagFD
		content = fmt.Sprintf("permit out ip from any to %s", pattern)
	case "port_range":
		flag = flagFD
		// Pre-decapsulation SDF doesn't bind a transport family to a
		// port range, so use "ip" + numbered range. Real deployments
		// would split TCP/UDP — the local cache today keeps it simple.
		content = fmt.Sprintf("permit out ip from any to any %s", pattern)
	default:
		return nil
	}
	body := []byte(content)
	out := make([]byte, 0, 4+len(body))
	out = append(out, flag, 0x00) // flags + spare (§8.2.39 octet 5/6)
	var ll [2]byte
	binary.BigEndian.PutUint16(ll[:], uint16(len(body)))
	out = append(out, ll[:]...)
	out = append(out, body...)
	return out
}

// EncodeApplicationIDsPFDs converts an AppPFDs grouping into the
// generated codec's struct. TS 29.244 §8.2.42 carries one
// ApplicationID (§8.2.43) + multiple PFDContext (§8.2.40); each
// PFDContext wraps one PFDContents (§8.2.39).
func EncodeApplicationIDsPFDs(g AppPFDs) genpfcp.ApplicationIDsPFDs {
	out := genpfcp.ApplicationIDsPFDs{
		ApplicationID: genpfcp.ApplicationID{Value: []byte(g.AppID)},
	}
	for _, r := range g.Rules {
		body := EncodePFDContents(r.DetectionType, r.Pattern)
		if body == nil {
			continue
		}
		out.PFDContext = append(out.PFDContext, genpfcp.PFDContext{
			PFDContents: []genpfcp.PFDContents{{Value: body}},
		})
	}
	return out
}

// SendPFDManagement encodes a TS 29.244 §7.4.3.1 PFD Management
// Request from the supplied groupings and ships it to this UPF anchor
// over the existing PFCP transport. Blocks until the §7.4.3.2 PFD
// Management Response arrives or the transport times out.
//
// An empty groups slice still makes a valid Request (§6.2.5.1: "An
// empty PFD Management Request […] indicates removal of all PFDs at
// the UP function") — useful for full-cache resync. Per-app removal
// uses the same wire with `Rules` empty for that app.
func (p *PfcpBridge) SendPFDManagement(groups []AppPFDs) error {
	if p == nil || p.t == nil {
		return fmt.Errorf("SendPFDManagement: bridge not dialled")
	}
	apps := make([]genpfcp.ApplicationIDsPFDs, 0, len(groups))
	for _, g := range groups {
		apps = append(apps, EncodeApplicationIDsPFDs(g))
	}
	req := &genpfcp.PFDManagementRequest{
		ApplicationIDsPFDs: apps,
		NodeID: &genpfcp.NodeID{
			Type: 0, // §8.2.38 IPv4
			IPv4: p.localNodeIPv4(),
		},
	}
	payload, err := stripHeader(req)
	if err != nil {
		return fmt.Errorf("encode PFD-Management Request: %w", err)
	}
	respBytes, err := p.t.SendRequest(p.remote, pfcpRequest(
		genpfcp.MessageTypePFDManagementRequest, 0, payload))
	if err != nil {
		return fmt.Errorf("PFD-Management transport: %w", err)
	}
	var resp genpfcp.PFDManagementResponse
	if err := resp.Decode(respBytes); err != nil {
		return fmt.Errorf("decode PFD-Management Response: %w", err)
	}
	if resp.Cause.Value != 1 {
		// TS 29.244 §8.2.1 cause; 1 = Request accepted (success).
		return fmt.Errorf("PFD-Management rejected: cause=%d (§8.2.1)", resp.Cause.Value)
	}
	p.log.Infof("PFD-Management push to %s ok: %d app(s) (TS 29.244 §6.2.5)",
		p.remote, len(groups))
	return nil
}

// pfdPushTracker remembers which Application IDs were in the previous
// push so the next push can emit per-app removal markers (PFDContext-
// less ApplicationIDsPFDs entries) for apps the SMF no longer knows
// about. Per TS 29.244 §6.2.5.3 the UPF retains apps not present in a
// Request, so an explicit empty entry is the spec-compliant way to
// signal "this app went away".
var (
	pfdPushMu  sync.Mutex
	pfdPushSet = map[string]struct{}{}
)

// addRemovalDeltas augments the SMF's "current set" with explicit
// removal entries for app IDs that were in the last push but are
// absent now. The mutation also updates pfdPushSet so the next call
// computes its diff against this snapshot.
func addRemovalDeltas(groups []AppPFDs) []AppPFDs {
	pfdPushMu.Lock()
	defer pfdPushMu.Unlock()
	cur := make(map[string]struct{}, len(groups))
	for _, g := range groups {
		if g.AppID != "" {
			cur[g.AppID] = struct{}{}
		}
	}
	out := groups
	for prev := range pfdPushSet {
		if _, still := cur[prev]; !still {
			// Empty Rules → empty PFDContext on the wire → UPF removes
			// this app from its cache (TS 29.244 §6.2.5.3 per-app
			// removal semantics).
			out = append(out, AppPFDs{AppID: prev})
		}
	}
	pfdPushSet = cur
	return out
}

// PushPFDsToAll fans out the same PFD set to every UPF anchor in the
// router. Per-anchor failures are aggregated into the return error so
// a partial push is observable to the operator. TS 29.244 §6.2.5.2
// allows the CP to send the same Request to multiple UPFs — each
// anchor stores the PFDs locally for its PDR matching.
//
// On each call we compute the diff vs. the previous push and inject
// empty entries for removed app IDs so the UPF cache prunes them
// (TS 29.244 §6.2.5.3 per-app removal).
func (r *RouterBridge) PushPFDsToAll(groups []AppPFDs) error {
	if r == nil {
		return fmt.Errorf("PushPFDsToAll: nil router")
	}
	bridges := r.Bridges()
	if len(bridges) == 0 {
		return fmt.Errorf("PushPFDsToAll: no UPF bridges registered")
	}
	withDeltas := addRemovalDeltas(groups)
	var firstErr error
	for upfID, br := range bridges {
		if err := br.SendPFDManagement(withDeltas); err != nil {
			if r.log != nil {
				r.log.Warnf("PFD-Management to UPF %s failed: %v", upfID, err)
			}
			if firstErr == nil {
				firstErr = err
			}
		}
	}
	return firstErr
}

// DefaultRouter is the process-wide singleton populated by the upf
// bring-up path (upfloop.EnableMulti). The webservice / OAM panel
// reads this to push PFD updates without threading a handle through
// every layer. nil before EnableMulti runs.
var DefaultRouter *RouterBridge
