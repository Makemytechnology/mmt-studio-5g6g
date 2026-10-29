// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
	"github.com/mmt/pfcpgen/pkg/schema"
)

// generateIEType emits Go code for one IE definition:
//   - runtime go_type aliases (FSEID, NodeID, FTEID) — a type alias + Encode/
//     Decode methods are provided by the runtime itself.
//   - Grouped IEs — struct with one field per sub-IE + recursive DecodeBytes
//     using runtime.Buffer.ForEachIE.
//   - Primitive / byte-container IEs — struct with `Value []byte` plus
//     DecodeBytes/EncodeBytes that copy bytes.
func (g *Generator) generateIEType(f *jen.File, ie *schema.IETypeDef) {
	switch {
	case ie.Kind == "flag_conditional":
		g.generateFlagConditionalIE(f, ie)
	case ie.GoType != "":
		g.generateRuntimeAlias(f, ie)
	case ie.Grouped:
		g.generateGroupedIE(f, ie)
	case len(ie.Fields) > 0 && ieFitsInOneByte(ie):
		g.generateBitFieldIE(f, ie)
	case len(ie.Fields) > 0:
		g.generateStructuredIE(f, ie)
	default:
		g.generateByteContainerIE(f, ie)
	}
	// IE type-code constant.
	f.Const().Id("IEType" + GoName(ie.Name)).Op("=").Lit(int(ie.TypeCode)).
		Commentf("PFCP IE type %d", ie.TypeCode)
}

// ieFitsInOneByte reports whether the IE's declared fields sum to ≤8 bits and
// use only the `bits` shape — in which case we generate a compact bit-packed
// representation (Decode(byte)/Encode() byte + DecodeBytes/EncodeBytes).
func ieFitsInOneByte(ie *schema.IETypeDef) bool {
	if len(ie.Fields) == 0 {
		return false
	}
	total := 0
	for _, f := range ie.Fields {
		if f.Bytes != 0 {
			return false
		}
		if f.Bits == 0 {
			return false
		}
		total += f.Bits
	}
	return total <= 8
}

func (g *Generator) generateRuntimeAlias(f *jen.File, ie *schema.IETypeDef) {
	f.Commentf("// %s — %s (runtime alias → %s)", GoName(ie.Name), ie.Description, ie.GoType)
	f.Type().Id(GoName(ie.Name)).Op("=").Add(g.qualRuntime(ie.GoType))
}

// generateBitFieldIE emits a struct for a ≤8-bit bit-field IE (ReportType,
// ReportingTriggers, GateStatus, ApplyAction, etc.).
// Emits Decode(byte), Encode() byte, and DecodeBytes/EncodeBytes so it
// composes with the TLV wrapper.
func (g *Generator) generateBitFieldIE(f *jen.File, ie *schema.IETypeDef) {
	name := GoName(ie.Name)
	f.Commentf("// %s — %s", name, ie.Description)
	f.Type().Id(name).StructFunc(func(grp *jen.Group) {
		for _, fld := range ie.Fields {
			if fld.Spare {
				continue
			}
			grp.Id(GoName(fld.Name)).Uint8().Commentf("%d bit(s), offset %d", fld.Bits, fld.Offset)
		}
	})

	// Named constants
	for _, fld := range ie.Fields {
		if len(fld.Values) == 0 {
			continue
		}
		keys := make([]int, 0, len(fld.Values))
		for k := range fld.Values {
			keys = append(keys, k)
		}
		sortInts(keys)
		f.Const().DefsFunc(func(group *jen.Group) {
			for _, v := range keys {
				group.Id(GoName(ie.Name) + GoName(fld.Values[v])).Op("=").Lit(v)
			}
		})
	}

	// Decode(v byte)
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("Decode").Params(jen.Id("v").Byte()).
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

	// Encode() byte
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("Encode").Params().Byte().
		BlockFunc(func(grp *jen.Group) {
			first := true
			var expr *jen.Statement
			for _, fld := range ie.Fields {
				if fld.Spare {
					continue
				}
				mask := byte((1 << fld.Bits) - 1)
				part := jen.Parens(jen.Id("t").Dot(GoName(fld.Name)).Op("&").Lit(int(mask)))
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

	// DecodeBytes / EncodeBytes — bridge to the TLV wrapper.
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("DecodeBytes").
		Params(jen.Id("v").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			grp.If(jen.Len(jen.Id("v")).Op("<").Lit(1)).Block(
				jen.Return(g.qualRuntime("ErrBufferTooShort")))
			grp.Id("t").Dot("Decode").Call(jen.Id("v").Index(jen.Lit(0)))
			grp.Return(jen.Nil())
		})
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("EncodeBytes").Params().Index().Byte().
		Block(jen.Return(jen.Index().Byte().Values(jen.Id("t").Dot("Encode").Call())))
}

func sortInts(a []int) {
	for i := 1; i < len(a); i++ {
		for j := i; j > 0 && a[j-1] > a[j]; j-- {
			a[j-1], a[j] = a[j], a[j-1]
		}
	}
}

// generateStructuredIE emits a struct with byte-aligned fields.
// Each field gets uint8/uint16/uint32 for 1/2/4-byte fields, []byte otherwise.
func (g *Generator) generateStructuredIE(f *jen.File, ie *schema.IETypeDef) {
	name := GoName(ie.Name)
	f.Commentf("// %s — %s", name, ie.Description)
	f.Type().Id(name).StructFunc(func(grp *jen.Group) {
		for _, fld := range ie.Fields {
			if fld.Spare {
				continue
			}
			grp.Id(GoName(fld.Name)).Add(structFieldType(fld))
		}
	})

	// Named constants for enumerated values.
	for _, fld := range ie.Fields {
		if len(fld.Values) == 0 {
			continue
		}
		keys := make([]int, 0, len(fld.Values))
		for k := range fld.Values {
			keys = append(keys, k)
		}
		sortInts(keys)
		f.Const().DefsFunc(func(group *jen.Group) {
			for _, v := range keys {
				group.Id(GoName(ie.Name) + GoName(fld.Values[v])).Op("=").Lit(v)
			}
		})
	}

	// DecodeBytes
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("DecodeBytes").
		Params(jen.Id("v").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			if ie.MinLength > 0 {
				grp.If(jen.Len(jen.Id("v")).Op("<").Lit(ie.MinLength)).Block(
					jen.Return(g.qualRuntime("ErrBufferTooShort")))
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

	// EncodeBytes
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("EncodeBytes").Params().Index().Byte().
		BlockFunc(func(grp *jen.Group) {
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

func structFieldType(fld schema.FieldDef) *jen.Statement {
	switch fld.Bytes {
	case 1:
		return jen.Uint8()
	case 2:
		return jen.Uint16()
	case 4:
		return jen.Uint32()
	case 8:
		return jen.Uint64()
	default:
		return jen.Index().Byte()
	}
}

func (g *Generator) emitStructFieldDecode(grp *jen.Group, fld schema.FieldDef) {
	name := GoName(fld.Name)
	grp.BlockFunc(func(bl *jen.Group) {
		bl.If(jen.Len(jen.Id("v")).Op("-").Id("off").Op("<").Lit(fld.Bytes)).Block(
			jen.Return(g.qualRuntime("ErrBufferTooShort")))
		switch fld.Bytes {
		case 1:
			bl.Id("t").Dot(name).Op("=").Id("v").Index(jen.Id("off"))
			bl.Id("off").Op("++")
		case 2:
			bl.Id("t").Dot(name).Op("=").Qual("encoding/binary", "BigEndian").Dot("Uint16").Call(
				jen.Id("v").Index(jen.Id("off").Op(":")))
			bl.Id("off").Op("+=").Lit(2)
		case 4:
			bl.Id("t").Dot(name).Op("=").Qual("encoding/binary", "BigEndian").Dot("Uint32").Call(
				jen.Id("v").Index(jen.Id("off").Op(":")))
			bl.Id("off").Op("+=").Lit(4)
		case 8:
			bl.Id("t").Dot(name).Op("=").Qual("encoding/binary", "BigEndian").Dot("Uint64").Call(
				jen.Id("v").Index(jen.Id("off").Op(":")))
			bl.Id("off").Op("+=").Lit(8)
		default:
			bl.Id("t").Dot(name).Op("=").Append(
				jen.Parens(jen.Index().Byte()).Parens(jen.Nil()),
				jen.Id("v").Index(jen.Id("off").Op(":").Id("off").Op("+").Lit(fld.Bytes)).Op("..."))
			bl.Id("off").Op("+=").Lit(fld.Bytes)
		}
	})
}

func (g *Generator) emitStructFieldEncode(grp *jen.Group, fld schema.FieldDef) {
	name := GoName(fld.Name)
	grp.BlockFunc(func(bl *jen.Group) {
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
		case 8:
			bl.Id("b").Op(":=").Make(jen.Index().Byte(), jen.Lit(8))
			bl.Qual("encoding/binary", "BigEndian").Dot("PutUint64").Call(jen.Id("b"), jen.Id("t").Dot(name))
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("b").Op("..."))
		default:
			bl.Id("out").Op("=").Append(jen.Id("out"), jen.Id("t").Dot(name).Op("..."))
		}
	})
}

func (g *Generator) generateByteContainerIE(f *jen.File, ie *schema.IETypeDef) {
	name := GoName(ie.Name)
	f.Commentf("// %s — %s", name, ie.Description)
	f.Type().Id(name).Struct(
		jen.Id("Value").Index().Byte(),
	)
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("DecodeBytes").
		Params(jen.Id("v").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			if ie.MinLength > 0 {
				grp.If(jen.Len(jen.Id("v")).Op("<").Lit(ie.MinLength)).Block(
					jen.Return(g.qualRuntime("ErrBufferTooShort")))
			}
			grp.Id("t").Dot("Value").Op("=").Append(
				jen.Parens(jen.Index().Byte()).Parens(jen.Nil()), jen.Id("v").Op("..."))
			grp.Return(jen.Nil())
		})
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("EncodeBytes").
		Params().Index().Byte().
		Block(jen.Return(jen.Id("t").Dot("Value")))
}

// generateGroupedIE emits a struct whose fields are each sub-IE (pointer for
// optional/conditional, slice for multiple, direct value for mandatory single),
// plus DecodeBytes (TLV loop) and EncodeBytes (concat).
func (g *Generator) generateGroupedIE(f *jen.File, ie *schema.IETypeDef) {
	name := GoName(ie.Name)
	f.Commentf("// %s — %s (grouped IE)", name, ie.Description)
	f.Type().Id(name).StructFunc(func(grp *jen.Group) {
		for _, sub := range ie.Members {
			g.emitMemberField(grp, sub)
		}
	})

	// DecodeBytes: iterate inner IEs, dispatch on type_code.
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("DecodeBytes").
		Params(jen.Id("v").Index().Byte()).Error().
		BlockFunc(func(grp *jen.Group) {
			grp.Id("b").Op(":=").Add(g.qualRuntime("NewBuffer")).Call(jen.Id("v"))
			grp.Return(jen.Id("b").Dot("ForEachIE").Call(
				jen.Func().Params(jen.Id("ie").Op("*").Add(g.qualRuntime("DecodedIE"))).Error().
					BlockFunc(func(fn *jen.Group) {
						g.emitMemberDispatchSwitch(fn, ie.Members, name)
					}),
			))
		})

	// EncodeBytes: concatenate each member encoded as TLV.
	f.Func().Params(jen.Id("t").Op("*").Id(name)).Id("EncodeBytes").
		Params().Index().Byte().
		BlockFunc(func(grp *jen.Group) {
			grp.Id("e").Op(":=").Add(g.qualRuntime("NewEncoder")).Call()
			for _, sub := range ie.Members {
				g.emitMemberEncode(grp, sub)
			}
			grp.Return(jen.Id("e").Dot("Bytes").Call())
		})
}

// emitMemberField declares the struct field for one sub-IE.
func (g *Generator) emitMemberField(grp *jen.Group, sub schema.IEEntry) {
	fieldType := jen.Id(GoName(sub.TypeRef))
	name := GoName(sub.Name)
	switch {
	case sub.Multiple:
		grp.Id(name).Index().Add(fieldType).Commentf("multiple allowed")
	case sub.Presence == "M":
		grp.Id(name).Add(fieldType)
	default:
		grp.Id(name).Op("*").Add(fieldType)
	}
}

// emitMemberEncode emits the encode path for one sub-IE member.
func (g *Generator) emitMemberEncode(grp *jen.Group, sub schema.IEEntry) {
	name := GoName(sub.Name)
	tr, ok := g.Repo.IETypes[sub.TypeRef]
	if !ok {
		grp.Comment("// unknown type ref: " + sub.TypeRef)
		return
	}
	typeCodeLit := jen.Lit(int(tr.TypeCode))
	encodeCall := func(ref *jen.Statement) *jen.Statement {
		// All generated IE types expose EncodeBytes() []byte (runtime aliases
		// have an Encode() method with the same signature).
		if tr.GoType != "" {
			return ref.Dot("Encode").Call()
		}
		return ref.Dot("EncodeBytes").Call()
	}
	switch {
	case sub.Multiple:
		grp.For(jen.Id("i").Op(":=").Range().Id("t").Dot(name)).BlockFunc(func(lp *jen.Group) {
			// Index into slice, method call auto-addresses.
			ref := jen.Id("t").Dot(name).Index(jen.Id("i"))
			lp.Id("_").Op("=").Id("e").Dot("EncodeIE").Call(typeCodeLit, encodeCall(ref))
		})
	case sub.Presence == "M":
		grp.Id("_").Op("=").Id("e").Dot("EncodeIE").Call(typeCodeLit, encodeCall(jen.Id("t").Dot(name)))
	default:
		grp.If(jen.Id("t").Dot(name).Op("!=").Nil()).Block(
			jen.Id("_").Op("=").Id("e").Dot("EncodeIE").Call(typeCodeLit, encodeCall(jen.Id("t").Dot(name))),
		)
	}
}

// emitMemberDispatchSwitch emits a `switch ie.Type { case X: ... }` block
// handling every declared member of a grouped IE or message IE list.
func (g *Generator) emitMemberDispatchSwitch(grp *jen.Group, members []schema.IEEntry, ctxName string) {
	grp.Switch(jen.Id("ie").Dot("Type")).BlockFunc(func(sw *jen.Group) {
		for _, sub := range members {
			tr, ok := g.Repo.IETypes[sub.TypeRef]
			if !ok {
				continue
			}
			sw.Case(jen.Lit(int(tr.TypeCode))).BlockFunc(func(cs *jen.Group) {
				g.emitMemberCaseBody(cs, sub, tr, ctxName)
			})
		}
		// default: ignore unknown IEs (forward-compat).
		sw.Default().Block(jen.Return(jen.Nil()))
	})
	grp.Return(jen.Nil())
}

func (g *Generator) emitMemberCaseBody(grp *jen.Group, sub schema.IEEntry, tr *schema.IETypeDef, ctxName string) {
	name := GoName(sub.Name)
	// Decode into a target identifier. Methods are called on the value (Go
	// auto-addresses when the method has a pointer receiver and the receiver
	// is addressable). We never wrap in `&`.
	decodeInto := func(target *jen.Statement) {
		methodName := "DecodeBytes"
		if tr.GoType != "" {
			methodName = "Decode"
		}
		grp.If(jen.Id("err").Op(":=").Add(target).Dot(methodName).Call(jen.Id("ie").Dot("Value")).Op(";").
			Id("err").Op("!=").Nil()).Block(
			jen.Return(g.qualRuntime("NewDecodeError").Call(
				jen.Lit(ctxName), jen.Lit(sub.Name), jen.Id("ie").Dot("Type"),
				jen.Lit(0), jen.Id("err"))),
		)
	}
	switch {
	case sub.Multiple:
		grp.Var().Id("tmp").Id(GoName(sub.TypeRef))
		decodeInto(jen.Id("tmp"))
		grp.Id("t").Dot(name).Op("=").Append(jen.Id("t").Dot(name), jen.Id("tmp"))
	case sub.Presence == "M":
		decodeInto(jen.Id("t").Dot(name))
	default:
		grp.Var().Id("tmp").Id(GoName(sub.TypeRef))
		decodeInto(jen.Id("tmp"))
		grp.Id("t").Dot(name).Op("=").Op("&").Id("tmp")
	}
	grp.Return(jen.Nil())
}
