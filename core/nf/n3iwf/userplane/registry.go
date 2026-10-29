// Copyright (c) 2026 MakeMyTechnology. All rights reserved.

package userplane

import (
	"encoding/binary"
	"errors"
	"fmt"
	"sync"
)

// Registry indexes per-UE Bridges by the two demux keys the
// transport layer needs:
//
//	bySPI   — for inbound ESP-in-UDP frames on NWu (UDP/4500). The
//	          first 4 octets of an ESP packet are the SPI per RFC
//	          4303 §2; we look up the Bridge whose inbound SA owns
//	          that SPI.
//	byTEID  — for inbound G-PDUs on N3. TS 29.281 §5.1 puts the
//	          receiver's TEID in octets 5..8 of the GTP-U header.
//
// Bridges are added when CREATE_CHILD_SA completes (signalling SA)
// or when a PDU session is established (per-session UP SA). They're
// removed when the IKE SA is torn down or the PDU session is
// released.
//
// All methods are goroutine-safe — both transport recv loops can
// look up bridges concurrently.
type Registry struct {
	mu     sync.RWMutex
	bySPI  map[uint32]*Bridge // ESP SPIIn → Bridge
	byTEID map[uint32]*Bridge // GTP-U TEIDDown → Bridge
}

// NewRegistry returns an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		bySPI:  make(map[uint32]*Bridge),
		byTEID: make(map[uint32]*Bridge),
	}
}

// Add binds a bridge into both lookup tables. Returns an error if
// either of its keys collides with an existing bridge — collision
// indicates SPI / TEID allocation bugs upstream and should fail
// loudly rather than silently replace an active SA.
func (r *Registry) Add(b *Bridge) error {
	if b == nil {
		return errors.New("userplane registry: nil bridge")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.bySPI[b.SAIn.SPI]; ok {
		return fmt.Errorf("userplane registry: SPI %08x already registered", b.SAIn.SPI)
	}
	if _, ok := r.byTEID[b.TEIDDown]; ok {
		return fmt.Errorf("userplane registry: TEID %08x already registered", b.TEIDDown)
	}
	r.bySPI[b.SAIn.SPI] = b
	r.byTEID[b.TEIDDown] = b
	return nil
}

// Remove tears down both index entries. No-op if the bridge isn't
// registered.
func (r *Registry) Remove(b *Bridge) {
	if b == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.bySPI, b.SAIn.SPI)
	delete(r.byTEID, b.TEIDDown)
}

// LookupBySPI finds a bridge by inbound ESP SPI. Returns nil if no
// bridge owns that SPI (caller should drop the packet — RFC 4303
// §3.4.2: "if no valid SAD entry exists, the receiver MUST discard
// the packet").
func (r *Registry) LookupBySPI(spi uint32) *Bridge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.bySPI[spi]
}

// LookupByTEID finds a bridge by inbound G-PDU TEID. Returns nil if
// no bridge owns that TEID.
func (r *Registry) LookupByTEID(teid uint32) *Bridge {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.byTEID[teid]
}

// IsIKE returns true if the UDP/4500 datagram begins with the
// 4-octet "non-ESP marker" of zeroes. Used by the NAT-T receive
// path to demux IKE from ESP-in-UDP on the same port — per RFC
// 7296 §3.1: "When sent on UDP port 4500, IKE messages have
// prepended four octets of zeros. ... An implementation that does
// support NAT traversal MUST be able to receive both UDP-
// encapsulated ESP and non-UDP-encapsulated ESP packets."
func IsIKE(udpPayload []byte) bool {
	if len(udpPayload) < 4 {
		return false
	}
	return udpPayload[0] == 0 && udpPayload[1] == 0 &&
		udpPayload[2] == 0 && udpPayload[3] == 0
}

// PeekESPSPI returns the SPI in the first 4 octets of an ESP-in-UDP
// payload, ready to feed LookupBySPI. Caller has already confirmed
// !IsIKE — i.e. the leading 4 octets are non-zero.
func PeekESPSPI(udpPayload []byte) (uint32, error) {
	if len(udpPayload) < 4 {
		return 0, fmt.Errorf("userplane: payload %d < 4 octets, no SPI", len(udpPayload))
	}
	return binary.BigEndian.Uint32(udpPayload[:4]), nil
}
