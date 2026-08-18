// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package ast defines ASN.1 AST node types (subset sufficient for 3GPP PER/UPER codegen).
package ast

// TagDefault indicates the module-level tag mode.
type TagDefault int

const (
	TagExplicit TagDefault = iota
	TagImplicit
	TagAutomatic
)

// Module represents one ASN.1 module.
type Module struct {
	Name          string
	TagDefault    TagDefault
	Extensibility bool
	Imports       []Import
	Exports       []string // nil for EXPORTS ALL
	ExportsAll    bool
	Assignments   []Assignment
}

type Import struct {
	Symbols    []string
	ModuleName string
}

// Assignment is a top-level type or value assignment.
type Assignment struct {
	Name          string
	Type          Type              // for type assignments
	Value         Value             // for value assignments
	ValueType     Type              // the declared type of a value assignment
	Parameterized *ParameterizedDef // non-nil if this is a parameterized definition
	IsValue       bool
	IsObjectSet   bool
	ObjectSet     *InfoObjectSet
	IsClass       bool
	Class         *InfoObjectClass
}

// Type is the interface for all ASN.1 types.
type Type interface{ typeNode() }

type (
	BooleanType          struct{}
	NullType             struct{}
	RealType             struct{}
	ObjectIdentifierType struct{}
)

func (BooleanType) typeNode()          {}
func (NullType) typeNode()             {}
func (RealType) typeNode()             {}
func (ObjectIdentifierType) typeNode() {}

type IntegerType struct {
	NamedNumbers []NamedNumber
	Constraint   *Constraint
}

func (IntegerType) typeNode() {}

type NamedNumber struct {
	Name  string
	Value int64
	Ref   string // if the number is a valueref
}

type EnumeratedType struct {
	RootEnums      []EnumItem
	ExtensionEnums []EnumItem
	Extensible     bool
}

func (EnumeratedType) typeNode() {}

type EnumItem struct {
	Name     string
	Value    int64
	HasValue bool
}

type BitStringType struct {
	NamedBits  []NamedNumber
	Constraint *Constraint
}

func (BitStringType) typeNode() {}

type OctetStringType struct {
	Constraint *Constraint
}

func (OctetStringType) typeNode() {}

type CharStringType struct {
	Kind       string // "PrintableString", "UTF8String", ...
	Constraint *Constraint
}

func (CharStringType) typeNode() {}

type SequenceType struct {
	Components         []ComponentType
	Extensible         bool
	ExtensionAdditions []ExtensionAdditionGroup
	TrailingComponents []ComponentType // components after the second ... (rare)
}

func (SequenceType) typeNode() {}

type ComponentType struct {
	Name       string
	Type       Type
	Optional   bool
	Default    Value
	HasDefault bool
	Tag        *Tag
	Constraint *Constraint
	// COMPONENTS OF TypeRef — when set, indicates inclusion of another SEQUENCE's components.
	ComponentsOf string
}

type ExtensionAdditionGroup struct {
	Version    *int
	Components []ComponentType
}

type SequenceOfType struct {
	ElementType Type
	Constraint  *Constraint // SIZE
}

func (SequenceOfType) typeNode() {}

type SetType struct {
	Components         []ComponentType
	Extensible         bool
	ExtensionAdditions []ExtensionAdditionGroup
}

func (SetType) typeNode() {}

type SetOfType struct {
	ElementType Type
	Constraint  *Constraint
}

func (SetOfType) typeNode() {}

type ChoiceType struct {
	Alternatives          []ChoiceAlternative
	Extensible            bool
	ExtensionAlternatives []ChoiceAlternative
}

func (ChoiceType) typeNode() {}

type ChoiceAlternative struct {
	Name string
	Type Type
	Tag  *Tag
}

// TypeReference is a reference to a named type, optionally qualified by module and
// carrying an optional constraint and parameterization arguments.
type TypeReference struct {
	ModuleName string
	TypeName   string
	Constraint *Constraint
	Args       []Type
}

func (TypeReference) typeNode() {}

// TaggedType wraps another type with an explicit/implicit tag.
type TaggedType struct {
	Tag  Tag
	Type Type
}

func (TaggedType) typeNode() {}

type Tag struct {
	Class    TagClass
	Number   int
	Explicit bool
	Implicit bool
}

type TagClass int

const (
	TagClassContextSpecific TagClass = iota
	TagClassUniversal
	TagClassApplication
	TagClassPrivate
)

// OpenType is an ASN.1 Information Object Class field reference of kind &Type,
// also known as an "open type". In 3GPP, these are carried as table-constrained
// components and resolved at codegen time via object sets.
type OpenType struct {
	ClassName  string
	FieldName  string
	Constraint *Constraint
}

func (OpenType) typeNode() {}

// --- Constraints ---

type ConstraintKind int

const (
	ConstraintValue ConstraintKind = iota
	ConstraintSize
	ConstraintTable
	ConstraintContents
	ConstraintPattern
	ConstraintSingleValue
	ConstraintUnion
	ConstraintIntersection
	ConstraintInnerType
)

type Constraint struct {
	Kind       ConstraintKind
	LowerBound *Value
	UpperBound *Value
	Extensible bool
	Inner      *Constraint
	// Union/Intersection operands
	Operands []*Constraint
	// Table constraint
	ObjectSet  string
	AtNotation []string
	// Contents constraint
	ContainedType Type
}

// --- Values ---

type Value interface{ valueNode() }

type (
	IntegerValue struct{ Int int64 }
	BoolValue    struct{ B bool }
	StringValue  struct{ S string }
	RealValue    struct{ F float64 }
	NullValue    struct{}
	NamedValue   struct {
		Name string
		Val  Value
	}
	ValueRef     struct{ Name string }
	BitStringValue struct {
		Hex       string // from 'A0'H
		Bits      string // from '0110'B
		NamedBits []string
	}
)

func (IntegerValue) valueNode()   {}
func (BoolValue) valueNode()      {}
func (StringValue) valueNode()    {}
func (RealValue) valueNode()      {}
func (NullValue) valueNode()      {}
func (NamedValue) valueNode()     {}
func (ValueRef) valueNode()       {}
func (BitStringValue) valueNode() {}

// --- Information Object (X.681) ---

type InfoObjectClass struct {
	Name   string
	Fields []ClassField
	Syntax [][]string // WITH SYNTAX block — list of token sequences
}

type ClassFieldKind int

const (
	FixedTypeValueField ClassFieldKind = iota
	TypeField
	FixedTypeValueSetField
	VariableTypeValueField
	ObjectClassField
	ObjectSetClassField
)

type ClassField struct {
	Name     string // "&id", "&Value"
	Kind     ClassFieldKind
	Type     Type
	Optional bool
	Default  Value
	Unique   bool
}

type InfoObjectSet struct {
	ClassName  string
	Objects    []InfoObject
	Extensible bool
}

type InfoObject struct {
	Fields map[string]ObjectField
}

// ObjectField holds the binding for one field of an information object.
// Exactly one of Value / Type / ObjectRef will be set.
type ObjectField struct {
	Value     Value
	Type      Type
	ObjectRef string
}

// --- Parameterized (X.683) ---

type ParameterizedDef struct {
	Parameters []Parameter
	Type       Type
	Value      Value
	IsValue    bool
}

type Parameter struct {
	Governor string // type name or class name (e.g. "INTEGER" or "NGAP-PROTOCOL-IES")
	Name     string
	IsClass  bool
}
