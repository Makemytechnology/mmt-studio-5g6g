// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ipalloc — per-DNN IPv4/IPv6 address allocation for PDU sessions.
//
// Go port of nf/smf/ip_allocator.py. Thread-safe allocator backed by
// in-memory sets — allocations are ephemeral and rebuilt on restart
// (matches the Python reference). Each DNN carries a list of CIDR pools
// for v4 and v6; the allocator rotates through all usable hosts so the
// same UE never gets the same IP twice in a row (works around the
// Samsung IMS stack that caches P-CSCF/IPsec state per-IP).
//
// First host in each CIDR (".1" or "::1") is reserved for the UPF/gateway
// and never handed to a UE.
package ipalloc

import (
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"sync"
)

// ErrPoolExhausted is returned when no host addresses remain in any of
// the DNN's configured CIDRs.
var ErrPoolExhausted = errors.New("ipalloc: pool exhausted")

// key indexes allocations by (DNN, ip_version).
type key struct {
	dnn string
	v   int
}

// Allocator is a thread-safe IP bookkeeping store. Zero value is ready to
// use; the process-wide singleton lives at Default.
type Allocator struct {
	mu         sync.Mutex
	allocated  map[key]map[netip.Addr]struct{}
	nextOffset map[key]int
}

// Default is the process-wide allocator used by SMF.
var Default = NewAllocator()

// NewAllocator builds an empty allocator — mostly used by tests.
func NewAllocator() *Allocator {
	return &Allocator{
		allocated:  make(map[key]map[netip.Addr]struct{}),
		nextOffset: make(map[key]int),
	}
}

// Allocate returns the next free host IP from the supplied CIDR list.
// Version is 4 or 6. Thread-safe.
func (a *Allocator) Allocate(dnn string, cidrList []string, version int) (netip.Addr, error) {
	if version != 4 && version != 6 {
		return netip.Addr{}, fmt.Errorf("ipalloc: unsupported version %d", version)
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	k := key{dnn, version}
	if _, ok := a.allocated[k]; !ok {
		a.allocated[k] = make(map[netip.Addr]struct{})
	}

	hosts, err := buildHosts(cidrList, version)
	if err != nil {
		return netip.Addr{}, err
	}
	if len(hosts) == 0 {
		return netip.Addr{}, fmt.Errorf("ipalloc: no hosts in pool for DNN %q v%d", dnn, version)
	}

	offset := a.nextOffset[k] % len(hosts)
	for i := 0; i < len(hosts); i++ {
		addr := hosts[(offset+i)%len(hosts)]
		if _, taken := a.allocated[k][addr]; !taken {
			a.allocated[k][addr] = struct{}{}
			a.nextOffset[k] = (offset + i + 1) % len(hosts)
			return addr, nil
		}
	}
	return netip.Addr{}, ErrPoolExhausted
}

// AllocateDualStack draws one v4 and one v6 address (TS 24.501 §6.4.1 PDN-type
// IPv4v6). Either half failing releases the other so the UE never gets a
// half-allocated session.
func (a *Allocator) AllocateDualStack(dnn string, v4, v6 []string) (netip.Addr, netip.Addr, error) {
	v4Addr, err := a.Allocate(dnn, v4, 4)
	if err != nil {
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("v4: %w", err)
	}
	v6Addr, err := a.Allocate(dnn, v6, 6)
	if err != nil {
		a.Release(dnn, v4Addr)
		return netip.Addr{}, netip.Addr{}, fmt.Errorf("v6: %w", err)
	}
	return v4Addr, v6Addr, nil
}

// Release marks an address free. Safe to call on unknown DNNs / IPs.
func (a *Allocator) Release(dnn string, addr netip.Addr) {
	v := 4
	if addr.Is6() && !addr.Is4In6() {
		v = 6
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if set, ok := a.allocated[key{dnn, v}]; ok {
		delete(set, addr)
	}
}

// IsAllocated reports whether an address is currently in use.
func (a *Allocator) IsAllocated(dnn string, addr netip.Addr) bool {
	v := 4
	if addr.Is6() && !addr.Is4In6() {
		v = 6
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	set, ok := a.allocated[key{dnn, v}]
	if !ok {
		return false
	}
	_, t := set[addr]
	return t
}

// Usage exposes per-(DNN, version) allocation counts for the /api/kpis panel.
func (a *Allocator) Usage() map[string]int {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]int, len(a.allocated))
	for k, set := range a.allocated {
		out[fmt.Sprintf("%s_v%d", k.dnn, k.v)] = len(set)
	}
	return out
}

// PoolDetail is the richer shape returned by UsageDetail — keeps the
// allocated-address list so the Live Sessions panel can render them.
type PoolDetail struct {
	Count     int      `json:"count"`
	Addresses []string `json:"addresses"`
}

// UsageDetail returns per-(DNN, version) allocation counts plus the
// assigned addresses (dotted-quad / v6 textual). Ordered deterministic
// for stable UI rendering.
func (a *Allocator) UsageDetail() map[string]PoolDetail {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make(map[string]PoolDetail, len(a.allocated))
	for k, set := range a.allocated {
		addrs := make([]string, 0, len(set))
		for addr := range set {
			addrs = append(addrs, addr.String())
		}
		sort.Strings(addrs)
		out[fmt.Sprintf("%s_v%d", k.dnn, k.v)] = PoolDetail{
			Count: len(set), Addresses: addrs,
		}
	}
	return out
}

// buildHosts expands all supplied CIDRs into a flat, ordered list of
// assignable addresses — (network, broadcast, gateway) excluded per the
// Python reference. For IPv6 the "broadcast" concept doesn't exist, so
// only the network address + reserved-gateway are skipped.
func buildHosts(cidrList []string, version int) ([]netip.Addr, error) {
	var out []netip.Addr
	for _, cidr := range cidrList {
		pfx, err := netip.ParsePrefix(cidr)
		if err != nil {
			return nil, fmt.Errorf("ipalloc: bad CIDR %q: %w", cidr, err)
		}
		if pfx.Addr().Is4() && version != 4 {
			continue
		}
		if pfx.Addr().Is6() && !pfx.Addr().Is4In6() && version != 6 {
			continue
		}
		all := expandAll(pfx)
		// Drop network (index 0) + broadcast (v4 last) + gateway (next .1/::1).
		if pfx.Addr().Is4() {
			if len(all) > 2 {
				all = all[1 : len(all)-1] // strip network + broadcast
			} else {
				continue
			}
		} else {
			if len(all) > 1 {
				all = all[1:] // strip network ::0
			} else {
				continue
			}
		}
		if len(all) > 1 {
			out = append(out, all[1:]...) // skip gateway (first usable host)
		}
	}
	return out, nil
}

// expandAll walks every address inside a prefix. Capped at 1M entries so
// a /64 doesn't blow the heap — labs use /120 or smaller.
func expandAll(pfx netip.Prefix) []netip.Addr {
	const maxHosts = 1 << 20
	var out []netip.Addr
	for a := pfx.Masked().Addr(); pfx.Contains(a); a = a.Next() {
		out = append(out, a)
		if len(out) >= maxHosts {
			break
		}
	}
	return out
}
