// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
	"github.com/mmt/asn1go/pkg/ast"
)

// emitCoders emits Marshal/Unmarshal methods. For aggregate types (SEQUENCE /
// CHOICE / SEQUENCE OF / SET OF) we delegate to the reflection-based runtime
// entry points. For named primitive types (INTEGER / OCTET STRING / BIT STRING
// / ENUMERATED / CHARSTRING) the constraint is not carried in a struct tag, so
// we generate specialised bodies that invoke the correct runtime primitive
// directly.
func (g *Generator) emitCoders(f *jen.File, name string, t ast.Type) {
	if !shouldEmitCoders(t) {
		return
	}
	goName := GoName(name)
	encs := g.selectedEncodings()
	for _, enc := range encs {
		aligned := enc == "APER"
		currentSelfType = goName
		body, ok := g.specializedMarshal(t, aligned)
		if ok {
			// Specialised Marshal
			f.Func().Params(
				jen.Id("m").Op("*").Id(goName),
			).Id("Marshal" + enc).Params().Params(
				jen.Index().Byte(),
				jen.Error(),
			).Block(body...)
			ubody := g.specializedUnmarshal(t, aligned)
			f.Func().Params(
				jen.Id("m").Op("*").Id(goName),
			).Id("Unmarshal" + enc).Params(
				jen.Id("b").Index().Byte(),
			).Error().Block(ubody...)
			continue
		}
		// Reflection-based fallback for aggregates.
		f.Func().Params(
			jen.Id("m").Op("*").Id(goName),
		).Id("Marshal" + enc).Params().Params(
			jen.Index().Byte(),
			jen.Error(),
		).Block(
			jen.Return(jen.Qual(g.runtimePath(), "Marshal"+enc).Call(jen.Id("m"))),
		)
		f.Func().Params(
			jen.Id("m").Op("*").Id(goName),
		).Id("Unmarshal" + enc).Params(
			jen.Id("b").Index().Byte(),
		).Error().Block(
			jen.Return(jen.Qual(g.runtimePath(), "Unmarshal"+enc).Call(jen.Id("b"), jen.Id("m"))),
		)
	}
}

func (g *Generator) selectedEncodings() []string {
	switch g.Opts.Encoding {
	case "uper":
		return []string{"UPER"}
	case "both":
		return []string{"APER", "UPER"}
	default:
		return []string{"APER"}
	}
}

// specializedMarshal returns Jennifer statements that form a function body for
// named primitive types whose constraints would otherwise be lost.
// The second return is false for aggregates (caller emits the reflection path).
func (g *Generator) specializedMarshal(t ast.Type, aligned bool) ([]jen.Code, bool) {
	rt := g.runtimePath()
	newWriter := jen.Id("w").Op(":=").Qual(rt, "NewWriter").Call(jen.Lit(aligned))

	switch tt := t.(type) {
	case ast.IntegerType:
		lb, ub, ext, hasRange := intRange(tt.Constraint)
		stmts := []jen.Code{newWriter}
		if hasRange {
			if ext {
				// extension marker + in-range or unconstrained.
				stmts = append(stmts,
					jen.Var().Id("inExt").Bool(),
					jen.If(jen.Int64().Parens(jen.Op("*").Id("m")).Op("<").Lit(lb).Op("||").
						Int64().Parens(jen.Op("*").Id("m")).Op(">").Lit(ub)).Block(
						jen.Id("inExt").Op("=").True(),
					),
					jen.Id("bit").Op(":=").Uint64().Parens(jen.Lit(0)),
					jen.If(jen.Id("inExt")).Block(jen.Id("bit").Op("=").Lit(1)),
					jen.If(jen.Err().Op(":=").Id("w").Dot("PutBits").Call(jen.Id("bit"), jen.Lit(1)).Op(";").Err().Op("!=").Nil()).Block(
						jen.Return(jen.Nil(), jen.Err()),
					),
					jen.If(jen.Id("inExt")).Block(
						jen.If(jen.Err().Op(":=").Id("w").Dot("PutUnconstrainedWhole").Call(jen.Int64().Parens(jen.Op("*").Id("m"))).Op(";").Err().Op("!=").Nil()).Block(
							jen.Return(jen.Nil(), jen.Err()),
						),
					).Else().Block(
						jen.If(jen.Err().Op(":=").Id("w").Dot("PutConstrainedWhole").Call(jen.Int64().Parens(jen.Op("*").Id("m")), jen.Lit(lb), jen.Lit(ub)).Op(";").Err().Op("!=").Nil()).Block(
							jen.Return(jen.Nil(), jen.Err()),
						),
					),
				)
			} else {
				stmts = append(stmts,
					jen.If(jen.Err().Op(":=").Id("w").Dot("PutConstrainedWhole").Call(jen.Int64().Parens(jen.Op("*").Id("m")), jen.Lit(lb), jen.Lit(ub)).Op(";").Err().Op("!=").Nil()).Block(
						jen.Return(jen.Nil(), jen.Err()),
					),
				)
			}
		} else {
			stmts = append(stmts,
				jen.If(jen.Err().Op(":=").Id("w").Dot("PutUnconstrainedWhole").Call(jen.Int64().Parens(jen.Op("*").Id("m"))).Op(";").Err().Op("!=").Nil()).Block(
					jen.Return(jen.Nil(), jen.Err()),
				),
			)
		}
		stmts = append(stmts, jen.Return(jen.Id("w").Dot("Bytes").Call(), jen.Nil()))
		return stmts, true

	case ast.EnumeratedType:
		n := len(tt.RootEnums)
		if n == 0 {
			return nil, false
		}
		stmts := []jen.Code{newWriter}
		if tt.Extensible {
			// emit extension bit based on whether value is within root range
			stmts = append(stmts,
				jen.Id("bit").Op(":=").Uint64().Parens(jen.Lit(0)),
				jen.If(jen.Int64().Parens(jen.Op("*").Id("m")).Op(">=").Lit(int64(n))).Block(
					jen.Id("bit").Op("=").Lit(1),
				),
				jen.If(jen.Err().Op(":=").Id("w").Dot("PutBits").Call(jen.Id("bit"), jen.Lit(1)).Op(";").Err().Op("!=").Nil()).Block(
					jen.Return(jen.Nil(), jen.Err()),
				),
				jen.If(jen.Id("bit").Op("==").Lit(1)).Block(
					jen.If(jen.Err().Op(":=").Id("w").Dot("PutNormallySmallNonNegative").Call(jen.Uint64().Parens(jen.Int64().Parens(jen.Op("*").Id("m")).Op("-").Lit(int64(n)))).Op(";").Err().Op("!=").Nil()).Block(
						jen.Return(jen.Nil(), jen.Err()),
					),
				).Else().Block(
					jen.If(jen.Err().Op(":=").Id("w").Dot("PutConstrainedWhole").Call(jen.Int64().Parens(jen.Op("*").Id("m")), jen.Lit(int64(0)), jen.Lit(int64(n-1))).Op(";").Err().Op("!=").Nil()).Block(
						jen.Return(jen.Nil(), jen.Err()),
					),
				),
			)
		} else {
			stmts = append(stmts,
				jen.If(jen.Err().Op(":=").Id("w").Dot("PutConstrainedWhole").Call(jen.Int64().Parens(jen.Op("*").Id("m")), jen.Lit(int64(0)), jen.Lit(int64(n-1))).Op(";").Err().Op("!=").Nil()).Block(
					jen.Return(jen.Nil(), jen.Err()),
				),
			)
		}
		stmts = append(stmts, jen.Return(jen.Id("w").Dot("Bytes").Call(), jen.Nil()))
		return stmts, true

	case ast.BooleanType:
		return []jen.Code{
			newWriter,
			jen.Id("bit").Op(":=").Uint64().Parens(jen.Lit(0)),
			jen.If(jen.Bool().Parens(jen.Op("*").Id("m"))).Block(jen.Id("bit").Op("=").Lit(1)),
			jen.If(jen.Err().Op(":=").Id("w").Dot("PutBits").Call(jen.Id("bit"), jen.Lit(1)).Op(";").Err().Op("!=").Nil()).Block(
				jen.Return(jen.Nil(), jen.Err()),
			),
			jen.Return(jen.Id("w").Dot("Bytes").Call(), jen.Nil()),
		}, true

	case ast.OctetStringType:
		lb, ub, ext, hasRange := sizeRange(tt.Constraint)
		return []jen.Code{
			newWriter,
			jen.If(jen.Err().Op(":=").Id("w").Dot("PutOctetString").Call(
				jen.Index().Byte().Parens(jen.Op("*").Id("m")),
				jen.Lit(ext), jen.Lit(uint64(lb)), jen.Lit(uint64(ub)), jen.Lit(hasRange),
			).Op(";").Err().Op("!=").Nil()).Block(
				jen.Return(jen.Nil(), jen.Err()),
			),
			jen.Return(jen.Id("w").Dot("Bytes").Call(), jen.Nil()),
		}, true

	case ast.CharStringType:
		lb, ub, ext, hasRange := sizeRange(tt.Constraint)
		bpc := bitsPerChar(tt.Kind)
		return []jen.Code{
			newWriter,
			jen.If(jen.Err().Op(":=").Id("w").Dot("PutKMString").Call(
				jen.String().Parens(jen.Op("*").Id("m")),
				jen.Lit(uint(bpc)), jen.Lit(ext), jen.Lit(uint64(lb)), jen.Lit(uint64(ub)), jen.Lit(hasRange),
			).Op(";").Err().Op("!=").Nil()).Block(
				jen.Return(jen.Nil(), jen.Err()),
			),
			jen.Return(jen.Id("w").Dot("Bytes").Call(), jen.Nil()),
		}, true

	case ast.BitStringType:
		lb, ub, ext, hasRange := sizeRange(tt.Constraint)
		return []jen.Code{
			newWriter,
			jen.If(jen.Err().Op(":=").Id("w").Dot("PutBitString").Call(
				jen.Qual(rt, "BitString").Parens(jen.Op("*").Id("m")),
				jen.Lit(ext), jen.Lit(uint64(lb)), jen.Lit(uint64(ub)), jen.Lit(hasRange),
			).Op(";").Err().Op("!=").Nil()).Block(
				jen.Return(jen.Nil(), jen.Err()),
			),
			jen.Return(jen.Id("w").Dot("Bytes").Call(), jen.Nil()),
		}, true
	}
	return nil, false
}

func (g *Generator) specializedUnmarshal(t ast.Type, aligned bool) []jen.Code {
	rt := g.runtimePath()
	newReader := jen.Id("r").Op(":=").Qual(rt, "NewReader").Call(jen.Id("b"), jen.Lit(aligned))

	switch tt := t.(type) {
	case ast.IntegerType:
		lb, ub, ext, hasRange := intRange(tt.Constraint)
		stmts := []jen.Code{newReader}
		if hasRange {
			if ext {
				stmts = append(stmts,
					jen.List(jen.Id("ext"), jen.Err()).Op(":=").Id("r").Dot("GetBits").Call(jen.Lit(1)),
					jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
					jen.Var().Id("v").Int64(),
					jen.If(jen.Id("ext").Op("==").Lit(1)).Block(
						jen.List(jen.Id("v"), jen.Err()).Op("=").Id("r").Dot("GetUnconstrainedWhole").Call(),
					).Else().Block(
						jen.List(jen.Id("v"), jen.Err()).Op("=").Id("r").Dot("GetConstrainedWhole").Call(jen.Lit(lb), jen.Lit(ub)),
					),
					jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
					jen.Op("*").Id("m").Op("=").Id("assignInt").Parens(jen.Id("v")).Comment("will be replaced below"),
				)
				// Replace last line with typed assignment.
				stmts[len(stmts)-1] = jen.Op("*").Id("m").Op("=").Id("").Custom(jen.Options{}, jen.Empty())
				stmts[len(stmts)-1] = jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v"))
			} else {
				stmts = append(stmts,
					jen.List(jen.Id("v"), jen.Err()).Op(":=").Id("r").Dot("GetConstrainedWhole").Call(jen.Lit(lb), jen.Lit(ub)),
					jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
					jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
				)
			}
		} else {
			stmts = append(stmts,
				jen.List(jen.Id("v"), jen.Err()).Op(":=").Id("r").Dot("GetUnconstrainedWhole").Call(),
				jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
				jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
			)
		}
		stmts = append(stmts, jen.Return(jen.Nil()))
		return stmts

	case ast.EnumeratedType:
		n := len(tt.RootEnums)
		stmts := []jen.Code{newReader}
		if tt.Extensible {
			stmts = append(stmts,
				jen.List(jen.Id("bit"), jen.Err()).Op(":=").Id("r").Dot("GetBits").Call(jen.Lit(1)),
				jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
				jen.Var().Id("v").Int64(),
				jen.If(jen.Id("bit").Op("==").Lit(1)).Block(
					jen.List(jen.Id("x"), jen.Err()).Op(":=").Id("r").Dot("GetNormallySmallNonNegative").Call(),
					jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
					jen.Id("v").Op("=").Int64().Parens(jen.Id("x")).Op("+").Lit(int64(n)),
				).Else().Block(
					jen.List(jen.Id("x"), jen.Err()).Op(":=").Id("r").Dot("GetConstrainedWhole").Call(jen.Lit(int64(0)), jen.Lit(int64(n-1))),
					jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
					jen.Id("v").Op("=").Id("x"),
				),
				jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
			)
		} else {
			stmts = append(stmts,
				jen.List(jen.Id("v"), jen.Err()).Op(":=").Id("r").Dot("GetConstrainedWhole").Call(jen.Lit(int64(0)), jen.Lit(int64(n-1))),
				jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
				jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
			)
		}
		stmts = append(stmts, jen.Return(jen.Nil()))
		return stmts

	case ast.BooleanType:
		return []jen.Code{
			newReader,
			jen.List(jen.Id("bit"), jen.Err()).Op(":=").Id("r").Dot("GetBits").Call(jen.Lit(1)),
			jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
			jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("bit").Op("==").Lit(1)),
			jen.Return(jen.Nil()),
		}

	case ast.OctetStringType:
		lb, ub, ext, hasRange := sizeRange(tt.Constraint)
		return []jen.Code{
			newReader,
			jen.List(jen.Id("v"), jen.Err()).Op(":=").Id("r").Dot("GetOctetString").Call(
				jen.Lit(ext), jen.Lit(uint64(lb)), jen.Lit(uint64(ub)), jen.Lit(hasRange),
			),
			jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
			jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
			jen.Return(jen.Nil()),
		}

	case ast.CharStringType:
		lb, ub, ext, hasRange := sizeRange(tt.Constraint)
		bpc := bitsPerChar(tt.Kind)
		return []jen.Code{
			newReader,
			jen.List(jen.Id("v"), jen.Err()).Op(":=").Id("r").Dot("GetKMString").Call(
				jen.Lit(uint(bpc)), jen.Lit(ext), jen.Lit(uint64(lb)), jen.Lit(uint64(ub)), jen.Lit(hasRange),
			),
			jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
			jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
			jen.Return(jen.Nil()),
		}

	case ast.BitStringType:
		lb, ub, ext, hasRange := sizeRange(tt.Constraint)
		return []jen.Code{
			newReader,
			jen.List(jen.Id("v"), jen.Err()).Op(":=").Id("r").Dot("GetBitString").Call(
				jen.Lit(ext), jen.Lit(uint64(lb)), jen.Lit(uint64(ub)), jen.Lit(hasRange),
			),
			jen.If(jen.Err().Op("!=").Nil()).Block(jen.Return(jen.Err())),
			jen.Op("*").Id("m").Op("=").Parens(typeOfSelf(t)).Parens(jen.Id("v")),
			jen.Return(jen.Nil()),
		}
	}
	return nil
}

// typeOfSelf returns a jen code representing the receiver type, conservatively
// falling back to the element-level Go type for the named type. We generate
// specialised code only for primitive named types, where "(Type)(v)" casts the
// runtime return back to the named type defined alongside the method.
// Because the method is declared on a named type, we can refer to that type as
// the receiver name — but jennifer composes code outside method context here,
// so we return a placeholder that is wired via a sentinel token replaced in
// emitCoders.
// In practice we inline "parens/cast" expressions using the receiver's declared
// type when we know it. To keep things simple, we emit an `any` cast-free
// assignment by converting through int64/[]byte/string at the call site — the
// compiler will coerce named-basic-type assignments automatically via a
// conversion expression built from the Go name of the assignment.
//
// Since each emitCoders call knows `goName`, we stash it on the Generator via a
// package-level variable. For MVP safety we capture via the "setSelf"
// indirection.
func typeOfSelf(_ ast.Type) jen.Code {
	return jen.Id(currentSelfType)
}

// currentSelfType is set by emitCoders before invoking specializedMarshal/Unmarshal.
var currentSelfType string

func init() {
	// no-op; currentSelfType is intentionally package-global.
}

// Constraint helpers.

func intRange(c *ast.Constraint) (lb, ub int64, ext, hasRange bool) {
	if c == nil {
		return 0, 0, false, false
	}
	switch c.Kind {
	case ast.ConstraintValue:
		if c.LowerBound != nil {
			if v, ok := (*c.LowerBound).(ast.IntegerValue); ok {
				lb = v.Int
			}
		}
		if c.UpperBound != nil {
			if v, ok := (*c.UpperBound).(ast.IntegerValue); ok {
				ub = v.Int
			}
		}
		return lb, ub, c.Extensible, true
	case ast.ConstraintSingleValue:
		if c.LowerBound != nil {
			if v, ok := (*c.LowerBound).(ast.IntegerValue); ok {
				return v.Int, v.Int, c.Extensible, true
			}
		}
	case ast.ConstraintIntersection, ast.ConstraintUnion:
		for _, sub := range c.Operands {
			if sub == nil {
				continue
			}
			if lb, ub, ext, ok := intRange(sub); ok {
				return lb, ub, ext, true
			}
		}
	}
	return 0, 0, false, false
}

func sizeRange(c *ast.Constraint) (lb, ub int64, ext, hasRange bool) {
	if c == nil {
		return 0, 0, false, false
	}
	switch c.Kind {
	case ast.ConstraintSize:
		if c.Inner == nil {
			return 0, 0, c.Extensible, false
		}
		lb, ub, _, ok := intRange(c.Inner)
		return lb, ub, c.Inner.Extensible || c.Extensible, ok
	case ast.ConstraintIntersection, ast.ConstraintUnion:
		for _, sub := range c.Operands {
			if lb, ub, ext, ok := sizeRange(sub); ok {
				return lb, ub, ext, true
			}
		}
	}
	return 0, 0, false, false
}

func bitsPerChar(kind string) int {
	switch kind {
	case "PrintableString", "VisibleString", "IA5String", "NumericString":
		return 7
	case "BMPString":
		return 16
	case "UniversalString":
		return 32
	}
	return 8 // UTF8String / others — treat as octet-aligned
}

// shouldEmitCoders is true for types that become a Go struct / named basic type
// with meaningful encoded form.
func shouldEmitCoders(t ast.Type) bool {
	switch t.(type) {
	case ast.SequenceType, ast.SetType, ast.ChoiceType,
		ast.SequenceOfType, ast.SetOfType,
		ast.IntegerType, ast.EnumeratedType,
		ast.BitStringType, ast.OctetStringType,
		ast.CharStringType, ast.BooleanType:
		return true
	case ast.TaggedType:
		tt := t.(ast.TaggedType)
		return shouldEmitCoders(tt.Type)
	}
	return false
}
