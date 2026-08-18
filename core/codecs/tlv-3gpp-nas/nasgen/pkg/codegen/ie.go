// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"sort"

	"github.com/dave/jennifer/jen"
	"github.com/mmt/nasgen/pkg/schema"
)

// generateIEType emits the Go type + Encode/Decode methods for one IETypeDef.
// Three shapes are handled:
//  1. go_type override — alias or reuse a runtime type (e.g. MobileIdentity5GS).
//  2. Single-byte bit-field IE (sum of bits ≤ 8) — packed in one byte.
//  3. Structured IE (bytes + nested bytes) — straight byte layout.
func (g *Generator) generateIEType(f *jen.File, ie *schema.IETypeDef) {
	switch {
	case ie.GoType != "":
		g.generateGoTypeAlias(f, ie)
	case ieNeedsBitFieldByte(ie):
		g.generateBitFieldIE(f, ie)
	default:
		g.generateStructIE(f, ie)
	}
}

func (g *Generator) generateGoTypeAlias(f *jen.File, ie *schema.IETypeDef) {
	// The IE's marshaling is handled by the runtime type itself.
	// We emit a type alias so message structs can reference pkg.<Name>.
	f.Commentf("// %s — %s", GoName(ie.Name), ie.Description)
	f.Type().Id(GoName(ie.Name)).Op("=").Add(g.qualRuntime(ie.GoType))
}

// --- Bit-field IE: one byte packing several sub-fields ---

func (g *Generator) generateBitFieldIE(f *jen.File, ie *schema.IETypeDef) {
	typeName := GoName(ie.Name)

	// struct
	f.Commentf("// %s — %s", typeName, ie.Description)
	f.Type().Id(typeName).StructFunc(func(grp *jen.Group) {
		for _, fld := range ie.Fields {
			if fld.Spare {
				continue
			}
			grp.Id(GoName(fld.Name)).Uint8().Commentf("%d bit(s), offset %d", fld.Bits, fld.Offset)
		}
	})

	// named constants for known values
	for _, fld := range ie.Fields {
		if len(fld.Values) == 0 {
			continue
		}
		// stable order
		keys := make([]int, 0, len(fld.Values))
		for k := range fld.Values {
			keys = append(keys, k)
		}
		sort.Ints(keys)
		f.Const().DefsFunc(func(group *jen.Group) {
			for _, val := range keys {
				name := fld.Values[val]
				group.Id(ConstName(ie.Name, name)).Op("=").Lit(val)
			}
		})
	}

	// Decode(v byte)
	f.Func().Params(jen.Id("t").Op("*").Id(typeName)).
		Id("Decode").Params(jen.Id("v").Byte()).
		BlockFunc(func(grp *jen.Group) {
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				mask := byte((1 << fld.Bits) - 1)
				expr := jen.Id("v")
				if fld.Offset > 0 {
					expr = jen.Parens(jen.Id("v").Op(">>").Lit(fld.Offset))
				}
				grp.Id("t").Dot(GoName(fld.Name)).Op("=").Add(expr).Op("&").Lit(int(mask))
			}
		})

	// DecodeBytes(v []byte) error — for TLV-wrapped use.
	f.Func().Params(jen.Id("t").Op("*").Id(typeName)).
		Id("DecodeBytes").Params(jen.Id("v").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			grp.If(jen.Len(jen.Id("v")).Op("<").Lit(1)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			)
			grp.Id("t").Dot("Decode").Call(jen.Id("v").Index(jen.Lit(0)))
			grp.Return(jen.Nil())
		})

	// EncodeBytes() []byte — for TLV-wrapped use.
	f.Func().Params(jen.Id("t").Op("*").Id(typeName)).
		Id("EncodeBytes").Params().Index().Byte().
		Block(jen.Return(jen.Index().Byte().Values(jen.Id("t").Dot("Encode").Call())))

	// Encode() byte
	f.Func().Params(jen.Id("t").Op("*").Id(typeName)).
		Id("Encode").Params().Byte().
		BlockFunc(func(grp *jen.Group) {
			first := true
			var expr *jen.Statement
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				mask := byte((1 << fld.Bits) - 1)
				part := jen.Parens(
					jen.Id("t").Dot(GoName(fld.Name)).Op("&").Lit(int(mask)),
				)
				if fld.Offset > 0 {
					part = jen.Parens(part.Op("<<").Lit(fld.Offset))
				}
				if first {
					expr = part
					first = false
				} else {
					expr = expr.Op("|").Add(part)
				}
			}
			if expr == nil {
				grp.Return(jen.Lit(0))
				return
			}
			grp.Return(expr)
		})
}

// --- Structured IE: straight byte layout with optional tail fields ---

func (g *Generator) generateStructIE(f *jen.File, ie *schema.IETypeDef) {
	typeName := GoName(ie.Name)

	f.Commentf("// %s — %s", typeName, ie.Description)
	f.Type().Id(typeName).StructFunc(func(grp *jen.Group) {
		for _, fld := range ie.Fields {
			if fld.Spare {
				continue
			}
			grp.Id(GoName(fld.Name)).Add(structFieldGoType(fld))
		}
		// fallback: raw payload for types that carry untyped bytes
		if len(ie.Fields) == 0 {
			grp.Id("Value").Index().Byte()
		}
	})

	// DecodeBytes(v []byte) error
	f.Func().Params(jen.Id("t").Op("*").Id(typeName)).
		Id("DecodeBytes").Params(jen.Id("v").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			if ie.MinLength > 0 {
				grp.If(jen.Len(jen.Id("v")).Op("<").Lit(ie.MinLength)).Block(
					jen.Return(g.qualRuntime("ErrBufferTooShort")),
				)
			}
			if len(ie.Fields) == 0 {
				grp.Id("t").Dot("Value").Op("=").Append(
					jen.Parens(jen.Index().Byte()).Parens(jen.Nil()), jen.Id("v").Op("..."),
				)
				grp.Return(jen.Nil())
				return
			}
			grp.Id("off").Op(":=").Lit(0)
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				g.emitStructFieldDecode(grp, fld)
			}
			grp.Id("_").Op("=").Id("off")
			grp.Return(jen.Nil())
		})

	// EncodeBytes() []byte
	f.Func().Params(jen.Id("t").Op("*").Id(typeName)).
		Id("EncodeBytes").Params().Index().Byte().
		BlockFunc(func(grp *jen.Group) {
			if len(ie.Fields) == 0 {
				grp.Return(jen.Id("t").Dot("Value"))
				return
			}
			grp.Id("out").Op(":=").Make(jen.Index().Byte(), jen.Lit(0))
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				g.emitStructFieldEncode(grp, fld)
			}
			grp.Return(jen.Id("out"))
		})
}

func structFieldGoType(fld schema.FieldDef) *jen.Statement {
	if fld.Type != "" {
		if fld.RepeatUntilEnd {
			return jen.Index().Id(GoName(fld.Type))
		}
		return jen.Id(GoName(fld.Type))
	}
	if fld.Optional {
		if fld.Bytes == 1 {
			return jen.Op("*").Uint8()
		}
		if fld.Bytes == 2 {
			return jen.Op("*").Uint16()
		}
		return jen.Op("*").Index().Byte()
	}
	switch fld.Bytes {
	case 1:
		return jen.Uint8()
	case 2:
		return jen.Uint16()
	case 4:
		return jen.Uint32()
	default:
		return jen.Index().Byte()
	}
}

func (g *Generator) emitStructFieldDecode(grp *jen.Group, fld schema.FieldDef) {
	name := GoName(fld.Name)

	// repeat_until_end requires a nested type with DecodeBytes (we don't support that fully yet);
	// stash the rest as raw bytes.
	if fld.RepeatUntilEnd {
		grp.Comment("// repeat_until_end: caller must populate " + name + " manually")
		return
	}

	// Wrap each field in its own block so locally-scoped temporaries (tmp, b) don't collide.
	grp.BlockFunc(func(bl *jen.Group) {
		if fld.Optional {
			bl.If(jen.Id("off").Op(">=").Len(jen.Id("v"))).Block(jen.Return(jen.Nil()))
		} else {
			bl.If(jen.Len(jen.Id("v")).Op("-").Id("off").Op("<").Lit(fld.Bytes)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			)
		}
		switch fld.Bytes {
		case 1:
			if fld.Optional {
				bl.Id("tmp").Op(":=").Id("v").Index(jen.Id("off"))
				bl.Id("t").Dot(name).Op("=").Op("&").Id("tmp")
			} else {
				bl.Id("t").Dot(name).Op("=").Id("v").Index(jen.Id("off"))
			}
			bl.Id("off").Op("++")
		case 2:
			bl.Id("t").Dot(name).Op("=").Qual("encoding/binary", "BigEndian").Dot("Uint16").Call(
				jen.Id("v").Index(jen.Id("off").Op(":")),
			)
			bl.Id("off").Op("+=").Lit(2)
		case 4:
			bl.Id("t").Dot(name).Op("=").Qual("encoding/binary", "BigEndian").Dot("Uint32").Call(
				jen.Id("v").Index(jen.Id("off").Op(":")),
			)
			bl.Id("off").Op("+=").Lit(4)
		default:
			bl.Id("t").Dot(name).Op("=").Append(
				jen.Parens(jen.Index().Byte()).Parens(jen.Nil()),
				jen.Id("v").Index(jen.Id("off").Op(":").Id("off").Op("+").Lit(fld.Bytes)).Op("..."),
			)
			bl.Id("off").Op("+=").Lit(fld.Bytes)
		}
	})
}

func (g *Generator) emitStructFieldEncode(grp *jen.Group, fld schema.FieldDef) {
	name := GoName(fld.Name)
	if fld.RepeatUntilEnd {
		grp.Comment("// repeat_until_end: " + name + " serialized as raw bytes")
		return
	}
	// Wrap each field in its own block so `b :=` doesn't collide with
	// siblings.
	grp.BlockFunc(func(bl *jen.Group) {
		if fld.Optional {
			bl.If(jen.Id("t").Dot(name).Op("!=").Nil()).BlockFunc(func(opt *jen.Group) {
				switch fld.Bytes {
				case 1:
					opt.Id("out").Op("=").Append(jen.Id("out"), jen.Op("*").Id("t").Dot(name))
				case 2:
					opt.Id("b").Op(":=").Make(jen.Index().Byte(), jen.Lit(2))
					opt.Qual("encoding/binary", "BigEndian").Dot("PutUint16").Call(
						jen.Id("b"), jen.Op("*").Id("t").Dot(name))
					opt.Id("out").Op("=").Append(jen.Id("out"), jen.Id("b").Op("..."))
				default:
					opt.Id("out").Op("=").Append(jen.Id("out"), jen.Op("*").Id("t").Dot(name).Op("..."))
				}
			})
			return
		}
		switch fld.Bytes {
		case 1:
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("t").Dot(name))
		case 2:
			bl.Id("b").Op(":=").Make(jen.Index().Byte(), jen.Lit(2))
			bl.Qual("encoding/binary", "BigEndian").Dot("PutUint16").Call(jen.Id("b"), jen.Id("t").Dot(name))
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("b").Op("..."))
		case 4:
			bl.Id("b").Op(":=").Make(jen.Index().Byte(), jen.Lit(4))
			bl.Qual("encoding/binary", "BigEndian").Dot("PutUint32").Call(jen.Id("b"), jen.Id("t").Dot(name))
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("b").Op("..."))
		default:
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("t").Dot(name).Op("..."))
		}
	})
}
