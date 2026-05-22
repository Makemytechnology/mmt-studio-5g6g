// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package schema defines YAML structures for PFCP (TS 29.244) messages and IEs.
// Unlike NAS, PFCP IEs carry a 16-bit type code (identifying what the IE is)
// and both primitive and grouped IEs share the same dispatch mechanism.
package schema

// MessageDef — one PFCP message.
type MessageDef struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	MessageType uint8     `yaml:"message_type"`
	Direction   string    `yaml:"direction"` // cp_to_up / up_to_cp / both
	HasSEID     bool      `yaml:"has_seid"`  // true for session-related
	IEs         []IEEntry `yaml:"ies"`
}

// IEEntry — one IE row in a message's (or grouped IE's) IE table.
type IEEntry struct {
	Name      string `yaml:"name"`
	Presence  string `yaml:"presence"`           // M / C / O
	TypeRef   string `yaml:"type_ref"`           // references an IETypeDef.Name
	Multiple  bool   `yaml:"multiple,omitempty"` // can appear >1 time
	Condition string `yaml:"condition,omitempty"`
}

// IETypeDef — one IE type (primitive or grouped).
type IETypeDef struct {
	Name        string     `yaml:"name"`
	TypeCode    uint16     `yaml:"type_code"`
	Description string     `yaml:"description"`
	MinLength   int        `yaml:"min_length"`
	MaxLength   int        `yaml:"max_length"`
	GoType      string     `yaml:"go_type,omitempty"` // runtime-type override (FSEID/NodeID/FTEID)
	Grouped     bool       `yaml:"grouped,omitempty"`
	Members     []IEEntry  `yaml:"members,omitempty"` // when Grouped=true
	Fields      []FieldDef `yaml:"fields,omitempty"`  // structured IE decomposition

	// Kind selects which generator emitter is used. Empty / "byte_container"
	// is the legacy default (opaque Value []byte). "flag_conditional"
	// wires the §8.2.x flag-byte + spec-typed sub-fields layout used by
	// SDFFilter / UserID / VolumeThreshold / etc — see generateFlagConditionalIE
	// in pkg/codegen/ie.go.
	Kind string `yaml:"kind,omitempty"`

	// SpareOctetAfterFlags — for flag_conditional IEs that have a
	// dedicated spare octet between the flag byte and the first
	// conditional sub-field (TS 29.244 §8.2.5 SDF Filter is the
	// canonical example).
	SpareOctetAfterFlags bool `yaml:"spare_octet_after_flags,omitempty"`
}

// FieldDef — one field inside a structured (non-grouped) IE.
//
// For "byte_container"-style IEs, use `bits` (sub-byte flags +
// `offset`) or `bytes` (byte-aligned fixed fields).
//
// For "flag_conditional" IEs, use `bit:` (0-based index into the
// flag byte that gates this field's presence) plus `type:` (the
// spec-typed primitive: tbcd_digits / utf8 / uint16 / uint24 /
// uint32 / uint64 / smmii_list) plus optional length-prefix
// modifiers.
type FieldDef struct {
	Name   string         `yaml:"name"`
	Bits   int            `yaml:"bits,omitempty"`
	Bytes  int            `yaml:"bytes,omitempty"`
	Offset int            `yaml:"offset,omitempty"`
	Values map[int]string `yaml:"values,omitempty"`
	Spare  bool           `yaml:"spare,omitempty"`

	// flag_conditional fields
	Bit          int    `yaml:"bit,omitempty"`           // 0-based index into the flag byte
	Type         string `yaml:"type,omitempty"`          // primitive type name
	LengthPrefix string `yaml:"length_prefix,omitempty"` // "u8" or "u16" — when sub-field carries its own length
}

// MessagesFile / IETypesFile — YAML roots.
type MessagesFile struct {
	Messages []MessageDef `yaml:"messages"`
}
type IETypesFile struct {
	IETypes []IETypeDef `yaml:"ie_types"`
}
