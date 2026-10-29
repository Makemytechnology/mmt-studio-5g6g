// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Best Master Clock Algorithm — TS 23.501 §5.27.1.6 method a):
//
//	"the NW-TT maintains the PTP port states of the PTP instance ...
//	 processes the Announce messages received on the NW-TT ports and
//	 from the DS-TTs ... executes the BMCA and determines the PTP
//	 port states" and "re-evaluates upon Sync or Announce receipt
//	 timeout (§5.27.1.5)".
//
// The dataset-comparison order follows IEEE 1588-2019 §9.3.4
// (priority1 → clockClass → clockAccuracy → offsetScaledLogVariance →
// priority2 → grandmasterIdentity, then stepsRemoved for same-GM
// vectors). Port-state decision is the §9.3.3 simple recommended-state
// logic: the port that received the overall best foreign vector
// becomes Follower (SLAVE), ports whose best foreign vector still
// loses to the local clock become Leader (MASTER), remaining ports
// with a foreign master better than the local clock go Passive.
package gptp

import "fmt"

// PortState — IEEE 1588-2019 §8.2.15.3.1 / IEEE 802.1AS port roles.
// Encodings match TS 24.539 portDS.portState (PMIC parameter 0x0012):
// IEEE 1588 Table 27 enumeration.
type PortState byte

const (
	PortStateInitializing PortState = 1
	PortStateFaulty       PortState = 2
	PortStateDisabled     PortState = 3
	PortStateListening    PortState = 4
	PortStatePreMaster    PortState = 5
	PortStateMaster       PortState = 6 // Leader
	PortStatePassive      PortState = 7
	PortStateUncalibrated PortState = 8
	PortStateSlave        PortState = 9 // Follower
)

func (s PortState) String() string {
	switch s {
	case PortStateMaster:
		return "Leader"
	case PortStateSlave:
		return "Follower"
	case PortStatePassive:
		return "Passive"
	case PortStateListening:
		return "Listening"
	default:
		return fmt.Sprintf("state(%d)", byte(s))
	}
}

// PriorityVector is the IEEE 1588 §9.3.4 comparison tuple.
type PriorityVector struct {
	Priority1    byte
	Quality      ClockQuality
	Priority2    byte
	Identity     [8]byte
	StepsRemoved uint16
}

// VectorFromAnnounce lifts an Announce body into a priority vector.
func VectorFromAnnounce(a *AnnounceBody) PriorityVector {
	return PriorityVector{
		Priority1:    a.GrandmasterPriority1,
		Quality:      a.GrandmasterQuality,
		Priority2:    a.GrandmasterPriority2,
		Identity:     a.GrandmasterIdentity,
		StepsRemoved: a.StepsRemoved,
	}
}

// Compare returns <0 when a is the better master, >0 when b is,
// 0 when identical — IEEE 1588-2019 §9.3.4 dataset comparison
// (lower value wins at every step).
func Compare(a, b PriorityVector) int {
	if a.Priority1 != b.Priority1 {
		return int(a.Priority1) - int(b.Priority1)
	}
	if a.Quality.ClockClass != b.Quality.ClockClass {
		return int(a.Quality.ClockClass) - int(b.Quality.ClockClass)
	}
	if a.Quality.ClockAccuracy != b.Quality.ClockAccuracy {
		return int(a.Quality.ClockAccuracy) - int(b.Quality.ClockAccuracy)
	}
	if a.Quality.OffsetScaledLogVariance != b.Quality.OffsetScaledLogVariance {
		return int(a.Quality.OffsetScaledLogVariance) - int(b.Quality.OffsetScaledLogVariance)
	}
	if a.Priority2 != b.Priority2 {
		return int(a.Priority2) - int(b.Priority2)
	}
	for i := range a.Identity {
		if a.Identity[i] != b.Identity[i] {
			return int(a.Identity[i]) - int(b.Identity[i])
		}
	}
	// Same grandmaster — §9.3.4 part 2: closer (fewer steps) wins.
	if a.StepsRemoved != b.StepsRemoved {
		return int(a.StepsRemoved) - int(b.StepsRemoved)
	}
	return 0
}

// bmcaPort is per-port foreign-master state.
type bmcaPort struct {
	best        PriorityVector
	hasForeign  bool
	lastRxNS    uint64
	timeoutNS   uint64 // announceReceiptTimeout × announceInterval
}

// BMCA is one PTP instance's master-selection engine (one per (g)PTP
// domain on the NW-TT).
type BMCA struct {
	// Local defaultDS as a priority vector (stepsRemoved = 0).
	local PriorityVector
	ports map[uint16]*bmcaPort
}

// DefaultAnnounceTimeoutNS: announceReceiptTimeout(3) × 1 s
// logAnnounceInterval 0 — IEEE 802.1AS defaults.
const DefaultAnnounceTimeoutNS = 3_000_000_000

// NewBMCA builds an engine for a local clock. gmCapable=false demotes
// the local clock (clockClass 255 = slave-only, IEEE 1588 §7.6.2.5).
func NewBMCA(identity [8]byte, priority1 byte, gmCapable bool) *BMCA {
	class := byte(248) // default clockClass
	if !gmCapable {
		class = 255 // slave-only
	}
	return &BMCA{
		local: PriorityVector{
			Priority1: priority1,
			Quality: ClockQuality{
				ClockClass:              class,
				ClockAccuracy:           0xFE, // unknown
				OffsetScaledLogVariance: 0x436A,
			},
			Priority2: priority1,
			Identity:  identity,
		},
		ports: map[uint16]*bmcaPort{},
	}
}

// EnsurePort registers a port with the default announce timeout.
func (b *BMCA) EnsurePort(port uint16) {
	if _, ok := b.ports[port]; !ok {
		b.ports[port] = &bmcaPort{timeoutNS: DefaultAnnounceTimeoutNS}
	}
}

// ProcessAnnounce ingests an Announce received on a port (nowNS = 5GS
// clock). The foreign vector's stepsRemoved is incremented by one hop
// per IEEE 1588 §9.3.2.5 (the Erbest is evaluated at this instance).
func (b *BMCA) ProcessAnnounce(port uint16, a *AnnounceBody, nowNS uint64) {
	b.EnsurePort(port)
	p := b.ports[port]
	v := VectorFromAnnounce(a)
	v.StepsRemoved++
	// Keep the best foreign vector seen on this port; a newer announce
	// from the same (or better) master refreshes the timeout.
	if !p.hasForeign || Compare(v, p.best) <= 0 {
		p.best = v
		p.hasForeign = true
		p.lastRxNS = nowNS
	}
}

// Tick expires foreign masters whose announces stopped
// (TS 23.501 §5.27.1.5: "re-evaluates ... upon Announce receipt
// timeout"). Call periodically or before States().
func (b *BMCA) Tick(nowNS uint64) {
	for _, p := range b.ports {
		if p.hasForeign && nowNS-p.lastRxNS > p.timeoutNS {
			p.hasForeign = false
		}
	}
}

// States runs the state-decision (§9.3.3): returns the recommended
// state per port plus whether the local clock is the grandmaster.
func (b *BMCA) States() (map[uint16]PortState, bool) {
	// Ebest — the overall best foreign vector across all ports.
	var ebest PriorityVector
	ebestPort := uint16(0)
	haveEbest := false
	for port, p := range b.ports {
		if !p.hasForeign {
			continue
		}
		if !haveEbest || Compare(p.best, ebest) < 0 {
			ebest, ebestPort, haveEbest = p.best, port, true
		}
	}

	out := make(map[uint16]PortState, len(b.ports))
	localIsGM := !haveEbest || Compare(b.local, ebest) < 0
	for port, p := range b.ports {
		switch {
		case localIsGM:
			// Local clock beats every foreign master → all ports lead.
			out[port] = PortStateMaster
		case port == ebestPort:
			out[port] = PortStateSlave
		case p.hasForeign && Compare(p.best, b.local) < 0:
			// Another master is reachable through this port and beats
			// us — stay quiet to avoid loops.
			out[port] = PortStatePassive
		default:
			out[port] = PortStateMaster
		}
	}
	return out, localIsGM
}

// Grandmaster returns the elected GM vector (the local clock when it
// wins the comparison).
func (b *BMCA) Grandmaster() PriorityVector {
	states, localGM := b.States()
	_ = states
	if localGM {
		return b.local
	}
	var best PriorityVector
	have := false
	for _, p := range b.ports {
		if p.hasForeign && (!have || Compare(p.best, best) < 0) {
			best, have = p.best, true
		}
	}
	if !have {
		return b.local
	}
	return best
}
