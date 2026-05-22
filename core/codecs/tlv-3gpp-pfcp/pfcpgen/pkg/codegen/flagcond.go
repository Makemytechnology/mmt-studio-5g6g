// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Generator support for TS 29.244 §8.2.x flag-conditional IEs.
//
// A "flag_conditional" IE has the shape:
//
//	Octet 5            flag byte (8 bits, each gates one sub-field)
//	(Octet 6)          optional Spare octet — set
//	                   spare_octet_after_flags: true in YAML
//	(per set bit)      a sub-field encoded per its declared `type`,
//	                   optionally preceded by a 1-byte or 2-byte
//	                   length when length_prefix is set
//
// Sub-field primitive types are spec-defined:
//
//	tbcd_digits  — string of digits per TS 23.003; TBCD-packed on
//	               wire per TS 29.274 §8.3 / §8.10
//	utf8         — UTF-8 string, raw bytes
//	uint16       — 2-byte big-endian unsigned
//	uint24       — 3-byte big-endian unsigned (carried in *uint32)
//	uint32       — 4-byte big-endian unsigned
//	uint64       — 8-byte big-endian unsigned
//	smmii_list   — count-prefixed list of length-prefixed bodies
//	               (TS 29.244 §8.2.5 SDF Filter SMMII sub-field)
//
// Each emits a struct field with the spec's natural Go type — string
// for digit / UTF-8 strings, *uintN for numeric primitives (nil = "field
// absent" — flag bit clear), [][]byte for smmii_list. No raw bytes or
// flag-arithmetic leak to consumers.
package codegen

import (
	"fmt"

	"github.com/dave/jennifer/jen"
	"github.com/mmt/pfcpgen/pkg/schema"
)

// generateFlagConditionalIE emits the struct + Encode + Decode +
// EncodeBytes + DecodeBytes for an IE whose YAML carries
// kind: flag_conditional.
func (g *Generator) generateFlagConditionalIE(f *jen.File, ie *schema.IETypeDef) {
	name := GoName(ie.Name)
	f.Commentf("// %s — %s", name, ie.Description)

	// Struct
	f.Type().Id(name).StructFunc(func(grp *jen.Group) {
		for _, fld := range ie.Fields {
			if fld.Spare {
				continue
			}
			grp.Id(GoName(fld.Name)).Add(flagCondGoType(fld))
		}
	})

	// Encode
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("Encode").Params().Index().Byte().
		BlockFunc(func(grp *jen.Group) {
			grp.Var().Id("flags").Byte()
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				grp.If(flagCondPresenceExpr(fld)).Block(
					jen.Id("flags").Op("|=").Lit(1 << uint(fld.Bit)),
				)
			}
			if ie.SpareOctetAfterFlags {
				grp.Id("out").Op(":=").Index().Byte().Values(jen.Id("flags"), jen.Lit(0))
			} else {
				grp.Id("out").Op(":=").Index().Byte().Values(jen.Id("flags"))
			}
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				flagCondEmitEncode(g, grp, fld)
			}
			grp.Return(jen.Id("out"))
		})

	// Decode
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("Decode").
		Params(jen.Id("b").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			minLen := 1
			if ie.SpareOctetAfterFlags {
				minLen = 2
			}
			grp.If(jen.Len(jen.Id("b")).Op("<").Lit(minLen)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			)
			grp.Id("flags").Op(":=").Id("b").Index(jen.Lit(0))
			grp.Id("off").Op(":=").Lit(minLen)
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				flagCondEmitDecode(g, grp, fld)
			}
			grp.Id("_").Op("=").Id("flags")
			grp.Id("_").Op("=").Id("off")
			grp.Return(jen.Nil())
		})

	// EncodeBytes / DecodeBytes — bridge to the TLV wrapper.
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("EncodeBytes").Params().Index().Byte().
		Block(jen.Return(jen.Id("t").Dot("Encode").Call()))
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("DecodeBytes").
		Params(jen.Id("v").Index().Byte()).Error().
		Block(jen.Return(jen.Id("t").Dot("Decode").Call(jen.Id("v"))))
}

// flagCondGoType returns the struct-field Go type for a flag-conditional
// sub-field. String types use plain `string` (empty = absent); numeric
// primitives use pointer (`*uintN`, nil = absent); smmii_list is `[][]byte`.
func flagCondGoType(fld schema.FieldDef) *jen.Statement {
	switch fld.Type {
	case "tbcd_digits", "utf8":
		return jen.String()
	case "uint16":
		return jen.Op("*").Uint16()
	case "uint24", "uint32":
		return jen.Op("*").Uint32()
	case "uint64":
		return jen.Op("*").Uint64()
	case "smmii_list":
		return jen.Index().Index().Byte()
	}
	panic(fmt.Sprintf("flagCondGoType: unsupported type %q on field %q", fld.Type, fld.Name))
}

// flagCondPresenceExpr returns a jen expression that evaluates true when
// the field is present (flag bit should be set).
func flagCondPresenceExpr(fld schema.FieldDef) *jen.Statement {
	name := GoName(fld.Name)
	switch fld.Type {
	case "tbcd_digits", "utf8":
		return jen.Id("t").Dot(name).Op("!=").Lit("")
	case "uint16", "uint24", "uint32", "uint64":
		return jen.Id("t").Dot(name).Op("!=").Nil()
	case "smmii_list":
		return jen.Len(jen.Id("t").Dot(name)).Op(">").Lit(0)
	}
	panic(fmt.Sprintf("flagCondPresenceExpr: unsupported type %q", fld.Type))
}

// flagCondEmitEncode emits the encode block for one sub-field. The
// emitted block is wrapped in `if t.Field-is-set { ... }` and walks
// the spec layout (length prefix → primitive bytes).
func flagCondEmitEncode(g *Generator, grp *jen.Group, fld schema.FieldDef) {
	name := GoName(fld.Name)
	grp.If(flagCondPresenceExpr(fld)).BlockFunc(func(bl *jen.Group) {
		switch fld.Type {
		case "tbcd_digits":
			bl.Id("tbcd").Op(",").Id("_").Op(":=").Add(g.qualRuntime("EncodeTBCD")).Call(jen.Id("t").Dot(name))
			emitLengthPrefix(bl, fld.LengthPrefix, jen.Len(jen.Id("tbcd")))
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("tbcd").Op("..."))
		case "utf8":
			bl.Id("s").Op(":=").Index().Byte().Parens(jen.Id("t").Dot(name))
			emitLengthPrefix(bl, fld.LengthPrefix, jen.Len(jen.Id("s")))
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("s").Op("..."))
		case "uint16":
			bl.Id("v").Op(":=").Op("*").Id("t").Dot(name)
			bl.Id("out").Op("=").Append(jen.Id("out"),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(8)),
				jen.Byte().Parens(jen.Id("v")))
		case "uint24":
			bl.Id("v").Op(":=").Op("*").Id("t").Dot(name)
			bl.Id("out").Op("=").Append(jen.Id("out"),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(16)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(8)),
				jen.Byte().Parens(jen.Id("v")))
		case "uint32":
			bl.Id("v").Op(":=").Op("*").Id("t").Dot(name)
			bl.Id("out").Op("=").Append(jen.Id("out"),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(24)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(16)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(8)),
				jen.Byte().Parens(jen.Id("v")))
		case "uint64":
			bl.Id("v").Op(":=").Op("*").Id("t").Dot(name)
			bl.Id("out").Op("=").Append(jen.Id("out"),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(56)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(48)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(40)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(32)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(24)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(16)),
				jen.Byte().Parens(jen.Id("v").Op(">>").Lit(8)),
				jen.Byte().Parens(jen.Id("v")))
		case "smmii_list":
			// Count-prefixed (1 byte) list of u16-length-prefixed bodies.
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Byte().Parens(jen.Len(jen.Id("t").Dot(name))))
			bl.For(jen.Id("_").Op(",").Id("body").Op(":=").Range().Id("t").Dot(name)).Block(
				jen.Id("n").Op(":=").Uint16().Parens(jen.Len(jen.Id("body"))),
				jen.Id("out").Op("=").Append(jen.Id("out"),
					jen.Byte().Parens(jen.Id("n").Op(">>").Lit(8)),
					jen.Byte().Parens(jen.Id("n"))),
				jen.Id("out").Op("=").Append(jen.Id("out"), jen.Id("body").Op("...")),
			)
		default:
			panic(fmt.Sprintf("flagCondEmitEncode: unsupported type %q", fld.Type))
		}
	})
}

// emitLengthPrefix appends 1 or 2 bytes describing the payload length.
func emitLengthPrefix(bl *jen.Group, prefix string, lenExpr *jen.Statement) {
	switch prefix {
	case "":
		// no prefix
	case "u8":
		bl.Id("out").Op("=").Append(jen.Id("out"), jen.Byte().Parens(lenExpr))
	case "u16":
		bl.Id("ln").Op(":=").Uint16().Parens(lenExpr)
		bl.Id("out").Op("=").Append(jen.Id("out"),
			jen.Byte().Parens(jen.Id("ln").Op(">>").Lit(8)),
			jen.Byte().Parens(jen.Id("ln")))
	default:
		panic(fmt.Sprintf("emitLengthPrefix: unsupported %q", prefix))
	}
}

// flagCondEmitDecode emits the decode block for one sub-field, gated by
// `if flags & (1<<bit) != 0 { ... }`. Each branch reads the length prefix
// (if any), checks bounds, decodes per primitive type, advances `off`.
func flagCondEmitDecode(g *Generator, grp *jen.Group, fld schema.FieldDef) {
	name := GoName(fld.Name)
	mask := 1 << uint(fld.Bit)
	grp.If(jen.Id("flags").Op("&").Lit(mask).Op("!=").Lit(0)).BlockFunc(func(bl *jen.Group) {
		// Length prefix (if any) → `n`.
		switch fld.LengthPrefix {
		case "u8":
			bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(1)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			)
			bl.Id("n").Op(":=").Int().Parens(jen.Id("b").Index(jen.Id("off")))
			bl.Id("off").Op("++")
			emitLVPayload(g, bl, fld, name)
		case "u16":
			bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(2)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			)
			bl.Id("n").Op(":=").Int().Parens(jen.Id("b").Index(jen.Id("off"))).Op("<<").Lit(8).
				Op("|").Int().Parens(jen.Id("b").Index(jen.Id("off").Op("+").Lit(1)))
			bl.Id("off").Op("+=").Lit(2)
			emitLVPayload(g, bl, fld, name)
		case "":
			// Fixed-width primitive; emit per type directly.
			emitFixedPayload(g, bl, fld, name)
		default:
			panic(fmt.Sprintf("flagCondEmitDecode: unsupported length_prefix %q", fld.LengthPrefix))
		}
	})
}

// emitLVPayload handles tbcd_digits / utf8 (string-valued) sub-fields with
// known length `n`.
func emitLVPayload(g *Generator, bl *jen.Group, fld schema.FieldDef, name string) {
	bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Id("n")).Block(
		jen.Return(g.qualRuntime("ErrBufferTooShort")),
	)
	switch fld.Type {
	case "tbcd_digits":
		bl.Id("t").Dot(name).Op("=").Add(g.qualRuntime("DecodeTBCD")).Call(
			jen.Id("b").Index(jen.Id("off").Op(":").Id("off").Op("+").Id("n")),
		)
	case "utf8":
		bl.Id("t").Dot(name).Op("=").String().Parens(
			jen.Id("b").Index(jen.Id("off").Op(":").Id("off").Op("+").Id("n")),
		)
	default:
		panic(fmt.Sprintf("emitLVPayload: type %q does not take a length prefix", fld.Type))
	}
	bl.Id("off").Op("+=").Id("n")
}

// emitFixedPayload handles uintN / smmii_list sub-fields with no length
// prefix (their byte width is fixed by the primitive type).
func emitFixedPayload(g *Generator, bl *jen.Group, fld schema.FieldDef, name string) {
	switch fld.Type {
	case "uint16":
		bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(2)).Block(
			jen.Return(g.qualRuntime("ErrBufferTooShort")),
		)
		bl.Id("v").Op(":=").Uint16().Parens(jen.Id("b").Index(jen.Id("off"))).Op("<<").Lit(8).
			Op("|").Uint16().Parens(jen.Id("b").Index(jen.Id("off").Op("+").Lit(1)))
		bl.Id("t").Dot(name).Op("=").Op("&").Id("v")
		bl.Id("off").Op("+=").Lit(2)
	case "uint24":
		bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(3)).Block(
			jen.Return(g.qualRuntime("ErrBufferTooShort")),
		)
		bl.Id("v").Op(":=").Uint32().Parens(jen.Id("b").Index(jen.Id("off"))).Op("<<").Lit(16).
			Op("|").Uint32().Parens(jen.Id("b").Index(jen.Id("off").Op("+").Lit(1))).Op("<<").Lit(8).
			Op("|").Uint32().Parens(jen.Id("b").Index(jen.Id("off").Op("+").Lit(2)))
		bl.Id("t").Dot(name).Op("=").Op("&").Id("v")
		bl.Id("off").Op("+=").Lit(3)
	case "uint32":
		bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(4)).Block(
			jen.Return(g.qualRuntime("ErrBufferTooShort")),
		)
		bl.Id("v").Op(":=").Qual("encoding/binary", "BigEndian").Dot("Uint32").Call(
			jen.Id("b").Index(jen.Id("off").Op(":")))
		bl.Id("t").Dot(name).Op("=").Op("&").Id("v")
		bl.Id("off").Op("+=").Lit(4)
	case "uint64":
		bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(8)).Block(
			jen.Return(g.qualRuntime("ErrBufferTooShort")),
		)
		bl.Id("v").Op(":=").Qual("encoding/binary", "BigEndian").Dot("Uint64").Call(
			jen.Id("b").Index(jen.Id("off").Op(":")))
		bl.Id("t").Dot(name).Op("=").Op("&").Id("v")
		bl.Id("off").Op("+=").Lit(8)
	case "smmii_list":
		bl.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(1)).Block(
			jen.Return(g.qualRuntime("ErrBufferTooShort")),
		)
		bl.Id("count").Op(":=").Int().Parens(jen.Id("b").Index(jen.Id("off")))
		bl.Id("off").Op("++")
		bl.Id("t").Dot(name).Op("=").Make(jen.Index().Index().Byte(), jen.Lit(0), jen.Id("count"))
		bl.For(jen.Id("i").Op(":=").Lit(0).Op(";").Id("i").Op("<").Id("count").Op(";").Id("i").Op("++")).Block(
			jen.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Lit(2)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			),
			jen.Id("m").Op(":=").Int().Parens(jen.Id("b").Index(jen.Id("off"))).Op("<<").Lit(8).
				Op("|").Int().Parens(jen.Id("b").Index(jen.Id("off").Op("+").Lit(1))),
			jen.Id("off").Op("+=").Lit(2),
			jen.If(jen.Len(jen.Id("b")).Op("<").Id("off").Op("+").Id("m")).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")),
			),
			jen.Id("t").Dot(name).Op("=").Append(jen.Id("t").Dot(name),
				jen.Append(jen.Parens(jen.Index().Byte()).Parens(jen.Nil()),
					jen.Id("b").Index(jen.Id("off").Op(":").Id("off").Op("+").Id("m")).Op("..."))),
			jen.Id("off").Op("+=").Id("m"),
		)
	default:
		panic(fmt.Sprintf("emitFixedPayload: unsupported type %q", fld.Type))
	}
}
