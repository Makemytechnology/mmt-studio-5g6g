// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ipool — inner-IP allocator for non-3GPP UEs traversing
// the N3IWF.
//
// TS 24.502 §7.3.2.2 says the N3IWF assigns the UE an "internal IP
// address" via the IKEv2 Configuration Payload (RFC 7296 §3.15) on
// the IKE_AUTH success response. That address is what the UE uses
// as the source on packets it sends inside the IPsec tunnel — i.e.
// it has to be from a pool the operator has provisioned for the
// N3IWF, routable on the inner side toward the UPF.
//
// Scope: IPv4 today; IPv6 is a sibling problem with the same shape
// (§3.15.3) and can be added when needed. Pool is a CIDR; the
// allocator hands out non-network, non-broadcast addresses skipping
// the gateway (first host).
package ipool

import (
	"errors"
	"fmt"
	"net"
	"sync"
)

// Pool is a thread-safe IPv4 inner-address allocator over a CIDR.
type Pool struct {
	mu       sync.Mutex
	cidr     *net.IPNet
	gateway  uint32 // first host (not handed out)
	nextHint uint32 // round-robin starting point (linear scan from here)
	bcast    uint32
	used     map[uint32]struct{}
}

// New parses cidr ("10.0.0.0/24") and returns a Pool. The first
// host (network+1) is reserved as the operator-side gateway, so a
// /24 yields 253 usable addresses.
//
// Returns an error if cidr is malformed, isn't IPv4, or doesn't
// have at least 2 host bits (a /31 has only the network and one
// host, leaving zero usable addresses after the gateway reservation).
func New(cidr string) (*Pool, error) {
	_, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, fmt.Errorf("ipool: bad cidr %q: %w", cidr, err)
	}
	v4 := ipnet.IP.To4()
	if v4 == nil {
		return nil, fmt.Errorf("ipool: %q is not IPv4", cidr)
	}
	ones, bits := ipnet.Mask.Size()
	if bits != 32 {
		return nil, fmt.Errorf("ipool: %q mask not 32-bit", cidr)
	}
	if ones >= 31 {
		return nil, fmt.Errorf("ipool: prefix /%d leaves no usable hosts after gateway reservation",
			ones)
	}
	network := beU32(v4)
	hostBits := uint32(32 - ones)
	bcast := network | ((1 << hostBits) - 1)
	gateway := network + 1
	return &Pool{
		cidr:     ipnet,
		gateway:  gateway,
		nextHint: gateway + 1,
		bcast:    bcast,
		used:     make(map[uint32]struct{}),
	}, nil
}

// Allocate returns the next free IP from the pool, or an error when
// the pool is exhausted. Linear scan from nextHint — fine up to the
// kind of UE counts an N3IWF sees (thousands at most).
func (p *Pool) Allocate() (net.IP, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	// Walk the host range exactly once.
	first := p.gateway + 1
	last := p.bcast - 1
	if last < first {
		return nil, errors.New("ipool: no usable hosts")
	}
	candidate := p.nextHint
	if candidate < first || candidate > last {
		candidate = first
	}
	scanned := uint32(0)
	span := last - first + 1
	for scanned < span {
		if _, busy := p.used[candidate]; !busy {
			p.used[candidate] = struct{}{}
			next := candidate + 1
			if next > last {
				next = first
			}
			p.nextHint = next
			return u32BE(candidate), nil
		}
		candidate++
		if candidate > last {
			candidate = first
		}
		scanned++
	}
	return nil, errors.New("ipool: pool exhausted")
}

// Release returns ip to the free pool. No-op if ip wasn't allocated
// or doesn't belong to the CIDR — the allocator is the source of
// truth, not external bookkeeping.
func (p *Pool) Release(ip net.IP) {
	v4 := ip.To4()
	if v4 == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.used, beU32(v4))
}

// Used returns the number of currently-allocated addresses — useful
// for operator dashboards.
func (p *Pool) Used() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.used)
}

// Capacity returns the maximum number of UEs the pool can serve.
func (p *Pool) Capacity() int {
	first := p.gateway + 1
	last := p.bcast - 1
	if last < first {
		return 0
	}
	return int(last - first + 1)
}

// Gateway returns the operator-side gateway address inside the pool
// CIDR — the address the UE will use as its default-route next-hop
// inside the tunnel.
func (p *Pool) Gateway() net.IP { return u32BE(p.gateway) }

func beU32(b []byte) uint32 {
	return uint32(b[0])<<24 | uint32(b[1])<<16 | uint32(b[2])<<8 | uint32(b[3])
}

func u32BE(v uint32) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}
