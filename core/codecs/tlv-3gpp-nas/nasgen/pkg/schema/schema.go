// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package schema defines the YAML structures describing NAS messages and IEs.
// The code generator reads these structures and produces Go source with
// Encode/Decode methods per TS 24.007 TLV rules.
package schema

// MessageDef — one NAS message, corresponding to one table in TS 24.501/24.301.
type MessageDef struct {
	Name        string    `yaml:"name"`
	Description string    `yaml:"description"`
	EPD         uint8     `yaml:"epd"`          // 0x7E=5GMM, 0x2E=5GSM, 0x07=EMM, 0x02=ESM
	MessageType uint8     `yaml:"message_type"` // 0x41 etc.
	Direction   string    `yaml:"direction"`    // "ue_to_network", "network_to_ue", "both"
	IEs         []IEEntry `yaml:"ies"`
}

// IEEntry — one row in a message's IE table.
type IEEntry struct {
	Name      string  `yaml:"name"`
	IEI       *string `yaml:"iei"`    // hex: "10" full byte, "B-" half-octet (high nibble), nil = mandatory
	Presence  string  `yaml:"presence"`
	Format    string  `yaml:"format"` // V, TV, LV, TLV, LV-E, TLV-E, T
	Length    string  `yaml:"length"` // human-readable: "1/2", "1", "3-n", "4-15"
	TypeRef   string  `yaml:"type_ref"`
	Condition string  `yaml:"condition,omitempty"`
}

// IETypeDef — one IE type definition.
type IETypeDef struct {
	Name         string         `yaml:"name"`
	Description  string         `yaml:"description"`
	MinLength    int            `yaml:"min_length"`
	MaxLength    int            `yaml:"max_length"` // 0 = unbounded
	Fields       []FieldDef     `yaml:"fields,omitempty"`
	GoType       string         `yaml:"go_type,omitempty"` // runtime-type override
	IsEnumerated bool           `yaml:"is_enumerated,omitempty"`
	EnumValues   []EnumValueDef `yaml:"enum_values,omitempty"`
}

// FieldDef — one field within a structured IE.
type FieldDef struct {
	Name           string         `yaml:"name"`
	Bits           int            `yaml:"bits,omitempty"`
	Bytes          int            `yaml:"bytes,omitempty"`
	Offset         int            `yaml:"offset,omitempty"` // bit offset within octet
	Type           string         `yaml:"type,omitempty"`
	Optional       bool           `yaml:"optional,omitempty"`
	RepeatUntilEnd bool           `yaml:"repeat_until_end,omitempty"`
	Values         map[int]string `yaml:"values,omitempty"`
	Spare          bool           `yaml:"spare,omitempty"`
}

type EnumValueDef struct {
	Name  string `yaml:"name"`
	Value int    `yaml:"value"`
}

// MessagesFile — YAML root for a messages definition file.
type MessagesFile struct {
	Messages []MessageDef `yaml:"messages"`
}

// IETypesFile — YAML root for an IE types definition file.
type IETypesFile struct {
	IETypes []IETypeDef `yaml:"ie_types"`
}

// IsHalfOctetIEI reports whether the IEI string (e.g. "B-", "C-") encodes a
// 4-bit IEI in the high nibble of a byte (Type 1 TV / half-octet pair).
func IsHalfOctetIEI(iei string) bool {
	return len(iei) == 2 && iei[1] == '-'
}

// IsMandatory reports whether the IE has no tag (presence M, no IEI).
func (ie IEEntry) IsMandatory() bool { return ie.IEI == nil }
