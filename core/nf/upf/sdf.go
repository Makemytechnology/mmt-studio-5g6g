// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// SDF Filter parser — IPFilterRule per RFC 6733 / TS 29.244.
// Port of nf/upf/sdf_filter.py.
package upf

import (
	"fmt"
	"strings"
)

// SDFFilter is a parsed SDF filter rule.
type SDFFilter struct {
	Action    string // "permit" or "deny"
	Direction string // "in" or "out"
	Proto     uint8  // IP protocol number; 255 = any ("ip")
	SrcAddr   string // CIDR or "" for any
	SrcPrefix int
	SrcPortLo uint16
	SrcPortHi uint16
	DstAddr   string
	DstPrefix int
	DstPortLo uint16
	DstPortHi uint16
}

// String converts the filter back to IPFilterRule string format.
func (f *SDFFilter) String() string {
	proto := "ip"
	if f.Proto != 255 {
		proto = fmt.Sprintf("%d", f.Proto)
	}
	fmtAddr := func(addr string, prefix int) string {
		if addr == "" {
			return "any"
		}
		if prefix == 32 {
			return addr
		}
		return fmt.Sprintf("%s/%d", addr, prefix)
	}
	fmtPort := func(lo, hi uint16) string {
		if lo == hi {
			return fmt.Sprintf("%d", lo)
		}
		return fmt.Sprintf("%d-%d", lo, hi)
	}

	src := fmtAddr(f.SrcAddr, f.SrcPrefix)
	dst := fmtAddr(f.DstAddr, f.DstPrefix)
	s := fmt.Sprintf("%s %s %s from %s", f.Action, f.Direction, proto, src)
	if f.SrcPortLo != 0 || f.SrcPortHi != 65535 {
		s += " " + fmtPort(f.SrcPortLo, f.SrcPortHi)
	}
	s += " to " + dst
	if f.DstPortLo != 0 || f.DstPortHi != 65535 {
		s += " " + fmtPort(f.DstPortLo, f.DstPortHi)
	}
	return s
}

// ParseSDF parses an SDF filter rule string.
func ParseSDF(ruleStr string) (*SDFFilter, error) {
	tokens := strings.Fields(ruleStr)
	if len(tokens) < 6 {
		return nil, fmt.Errorf("SDF rule too short: %s", ruleStr)
	}

	f := &SDFFilter{
		SrcPortLo: 0, SrcPortHi: 65535,
		DstPortLo: 0, DstPortHi: 65535,
		Proto: 255,
	}
	i := 0

	// action
	if tokens[i] != "permit" && tokens[i] != "deny" {
		return nil, fmt.Errorf("invalid action: %s", tokens[i])
	}
	f.Action = tokens[i]
	i++

	// direction
	if tokens[i] != "in" && tokens[i] != "out" {
		return nil, fmt.Errorf("invalid direction: %s", tokens[i])
	}
	f.Direction = tokens[i]
	i++

	// protocol
	if tokens[i] == "ip" {
		f.Proto = 255
	} else {
		var p int
		if _, err := fmt.Sscanf(tokens[i], "%d", &p); err != nil {
			return nil, fmt.Errorf("invalid proto: %s", tokens[i])
		}
		f.Proto = uint8(p)
	}
	i++

	// "from"
	if tokens[i] != "from" {
		return nil, fmt.Errorf("expected 'from', got: %s", tokens[i])
	}
	i++

	// src addr
	f.SrcAddr, f.SrcPrefix = parseSDFAddr(tokens[i])
	i++

	// optional src port
	if i < len(tokens) && tokens[i] != "to" {
		f.SrcPortLo, f.SrcPortHi = parseSDFPort(tokens[i])
		i++
	}

	// "to"
	if i >= len(tokens) || tokens[i] != "to" {
		return nil, fmt.Errorf("expected 'to' at position %d", i)
	}
	i++

	// dst addr
	if i >= len(tokens) {
		return nil, fmt.Errorf("missing destination address")
	}
	f.DstAddr, f.DstPrefix = parseSDFAddr(tokens[i])
	i++

	// optional dst port
	if i < len(tokens) {
		f.DstPortLo, f.DstPortHi = parseSDFPort(tokens[i])
	}

	return f, nil
}

func parseSDFAddr(s string) (string, int) {
	if s == "any" {
		return "", 0
	}
	if idx := strings.Index(s, "/"); idx >= 0 {
		var prefix int
		fmt.Sscanf(s[idx+1:], "%d", &prefix)
		return s[:idx], prefix
	}
	return s, 32
}

func parseSDFPort(s string) (uint16, uint16) {
	if idx := strings.Index(s, "-"); idx >= 0 {
		var lo, hi uint16
		fmt.Sscanf(s[:idx], "%d", &lo)
		fmt.Sscanf(s[idx+1:], "%d", &hi)
		return lo, hi
	}
	var p uint16
	fmt.Sscanf(s, "%d", &p)
	return p, p
}
