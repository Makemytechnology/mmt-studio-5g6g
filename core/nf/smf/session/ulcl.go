// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// ulcl.go — install an Uplink Classifier (TS 23.501 §5.6.4 ULCL) /
// Branching Point at the UPF when SMF establish detects an
// AF-influence rule (TS 23.502 §4.3.6) for the PDU session.
//
// Control-plane wire: TS 29.244 §7.5.4 PFCP Session Modification
// Request carrying §7.5.4.17 Create-PDR and §7.5.4.17 Create-FAR
// IEs. The Create-PDR matches DL traffic to the AF rule's
// `target_ip[:target_port]`; the Create-FAR forwards the matched
// flow to the local DN attach point.
//
// Today the C dataplane treats the new PDR + FAR as additional
// match-and-forward state on the same session. A real ULCL would
// also re-anchor uplink traffic at a Local PSA — that's the next
// piece (UPF dataplane fork). The control-plane install is what
// this file delivers; install state is tracked in mec.ULCLState
// so the OAM panel sees whether the SMF→UPF push actually landed.

package session

import (
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/mmt/mmt-studio-core/edge/mec"
	"github.com/mmt/mmt-studio-core/oam/logger"
	"github.com/mmt/mmt-studio-core/nf/upf"
	upfmgr "github.com/mmt/mmt-studio-core/nf/upf"
)

// ULCL identifier base — keeps spec-mandated rule-id space (PDR
// IDs uint16, FAR IDs uint32) collision-free with the IDs the
// Establish path already allocates. The Establish path uses
// PDR IDs in the low hundreds and FAR IDs in the low millions;
// we start at 0xE000 / 0xE000_0000 so it's obvious which IE was
// installed by the AF-influence path on a wire trace.
const (
	ulclPDRBase uint16 = 0xE000
	ulclFARBase uint32 = 0xE0000000
)

// nextULCLPair returns a (PDR, FAR) id pair derived from the
// session + rule index so the same combination can be re-used
// idempotently on retransmits without colliding across sessions.
//
// The mixing function is intentionally simple — collisions only
// matter inside a single PDU session (PFCP IDs are session-scoped),
// so XOR-ing the rule index into the base is sufficient.
func nextULCLPair(pduSessionID uint8, ruleIdx int) (uint16, uint32) {
	pdr := ulclPDRBase + uint16(pduSessionID)*16 + uint16(ruleIdx&0x0F)
	far := ulclFARBase + uint32(pduSessionID)*16 + uint32(ruleIdx&0x0F)
	return pdr, far
}

// installULCLForSession installs every AF-influence rule that
// matches `dnn` as a PFCP Modify-batch on the session. The rule
// index becomes the per-session ULCL ID suffix so retries are
// idempotent. Returns the number of rules that were successfully
// installed; install attempts (success or fail) are recorded into
// mec.ULCLState so /api/mec/active-sessions can render them.
func installULCLForSession(imsi string, pduSessionID uint8, dnn string,
	rules []*mec.TrafficRule) int {
	if len(rules) == 0 {
		return 0
	}
	log := logger.Get("smf.ulcl").WithIMSI(imsi)
	now := time.Now().UTC().Format("2006-01-02 15:04:05")
	installed := 0

	for i, rule := range rules {
		pdrID, farID := nextULCLPair(pduSessionID, i)

		targetAddr, targetPort := parseTarget(rule.TargetIP, rule.TargetPort)
		if targetAddr == 0 {
			err := fmt.Errorf("rule %s has no resolvable target_ip", rule.RuleID)
			log.Warnf("ULCL install skipped pduSessID=%d rule=%s: %v",
				pduSessionID, rule.RuleID, err)
			mec.RecordULCLInstall(imsi, int(pduSessionID), rule.RuleID,
				pdrID, farID, err, now)
			continue
		}

		// SDF filter syntax (TS 29.244 §8.2.5 / TS 23.501 §5.7.2.4):
		//   "permit out ip from any to <ip>[/32] [<port>]"
		// is the form the local dataplane consumes.
		sdf := buildSDFForTarget(rule.TargetIP, rule.TargetPort)

		batch := upfmgr.ModifyBatch{
			CreatePDRs: []upfmgr.PDR{{
				PDRID: pdrID,
				// Higher precedence (lower numeric in TS 29.244 §8.2.11)
				// than the default downstream PDR so the ULCL match wins.
				Precedence: 1,
				PDISource:  1, // DL — match the UE-bound packet
				FARID:      farID,
				SDFRules:   sdf,
			}},
			CreateFARs: []upf.FAR{{
				FARID:    farID,
				Action:   1,             // FORW (TS 29.244 §8.2.26)
				DstIface: 1,             // Core / DN side
				PeerAddr: targetAddr,    // local DN attach point
				PeerPort: targetPort,
			}},
		}

		err := upfmgr.Default.ApplyModifyBatch(imsi, pduSessionID, batch)
		mec.RecordULCLInstall(imsi, int(pduSessionID), rule.RuleID,
			pdrID, farID, err, now)
		if err != nil {
			log.Warnf("ULCL install FAILED pduSessID=%d rule=%s pdr=%d far=%d: %v",
				pduSessionID, rule.RuleID, pdrID, farID, err)
			continue
		}
		log.Infof("ULCL installed pduSessID=%d rule=%s pdr=%d far=%d target=%s:%d (TS 23.501 §5.6.4)",
			pduSessionID, rule.RuleID, pdrID, farID, rule.TargetIP, rule.TargetPort)
		installed++
	}
	return installed
}

// parseTarget returns the network-byte-order uint32 IP and the
// uint16 port for an AF rule's target. Returns (0,0) on parse
// failure (caller skips with a warning rather than crashing the
// establish path).
func parseTarget(ip string, port int) (uint32, uint16) {
	addr := net.ParseIP(strings.TrimSpace(ip))
	if addr == nil {
		return 0, 0
	}
	v4 := addr.To4()
	if v4 == nil {
		return 0, 0
	}
	u := uint32(v4[0])<<24 | uint32(v4[1])<<16 | uint32(v4[2])<<8 | uint32(v4[3])
	if port < 0 || port > 0xFFFF {
		port = 0
	}
	return u, uint16(port)
}

func buildSDFForTarget(ip string, port int) string {
	ip = strings.TrimSpace(ip)
	if ip == "" {
		return ""
	}
	if port > 0 {
		return "permit out ip from any to " + ip + " " + strconv.Itoa(port)
	}
	return "permit out ip from any to " + ip
}
