// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"github.com/dave/jennifer/jen"
	"github.com/mmt/asn1go/pkg/ast"
	"github.com/mmt/asn1go/pkg/resolver"
)

// namedTypeTag returns the `aper:"..."` content for a named type's intrinsic
// constraints (e.g. ProtocolIE-ID = INTEGER (0..65535) -> "valueLB:0,valueUB:65535").
func (g *Generator) namedTypeTag(typeName string) string {
	resolved := g.resolveNamedConstraint(ast.TypeReference{TypeName: typeName})
	c := ast.ComponentType{Type: resolved}
	return g.buildAperTagComponent(c, false, nil)
}

type altByName struct {
	Name string
	Type string
}

// emitValueChoiceLookup writes a method on the Value type that maps an ID
// integer to the Present alternative index. The runtime invokes it during
// decode of a directChoice open-type so it knows which alternative to read.
func (g *Generator) emitValueChoiceLookup(f *jen.File, rs *resolver.ResolvedObjectSet, goName string, alts []altByName) {
	// Build a map of id valueRef name -> alternative type name.
	idToType := map[string]string{}
	for _, e := range rs.Entries {
		idField, ok := e.Fields["&id"]
		if !ok {
			continue
		}
		var idName string
		if vr, ok := idField.Value.(ast.ValueRef); ok {
			idName = vr.Name
		} else if idField.ObjectRef != "" {
			idName = idField.ObjectRef
		}
		if idName == "" {
			continue
		}
		// "&Value" field — what type does this entry's value hold?
		valField, ok := e.Fields[g.valueFieldName(rs)]
		if !ok {
			continue
		}
		if tr, ok := valField.Type.(ast.TypeReference); ok {
			idToType[idName] = tr.TypeName
		}
	}
	if len(idToType) == 0 {
		return
	}
	f.Func().Params(jen.Op("*").Id(goName)).Id("APERAlternativeForID").Params(
		jen.Id("id").Int64(),
	).Int().BlockFunc(func(grp *jen.Group) {
		grp.Switch(jen.Id("id")).BlockFunc(func(sw *jen.Group) {
			for _, a := range alts {
				// Find every id that maps to this alternative type.
				for idName, tname := range idToType {
					if tname != a.Type {
						continue
					}
					sw.Case(jen.Int64().Parens(jen.Id(GoName(idName)))).Block(
						jen.Return(jen.Id(goName + "Present" + GoName(a.Name))),
					)
				}
			}
		})
		grp.Return(jen.Lit(0))
	})
}

// valueFieldName returns the class field name that corresponds to the open-type
// "value" slot (typically "&Value" in 3GPP).
func (g *Generator) valueFieldName(rs *resolver.ResolvedObjectSet) string {
	for _, cf := range rs.Class.Fields {
		if cf.Kind == ast.TypeField {
			return cf.Name
		}
	}
	return "&Value"
}

// emitObjectSetChoices emits, for each resolved object set, a typed CHOICE-
// like Go struct that represents the union of all "Value" alternatives. This
// is the core 3GPP "open-type-on-table-constraint" unlock: given
//
//	NGSetupRequestIEs NGAP-PROTOCOL-IES ::= {
//	    { ID id-GlobalRANNodeID  CRITICALITY reject  TYPE GlobalRANNodeID ...} |
//	    { ID id-RANNodeName      CRITICALITY ignore  TYPE RANNodeName     ...} |
//	    ...
//	}
//
// we emit:
//
//	type NGSetupRequestIEsValue struct {
//	    Present int
//	    GlobalRANNodeID *GlobalRANNodeID
//	    RANNodeName     *RANNodeName
//	    ...
//	}
//	const (
//	    NGSetupRequestIEsValuePresentNothing         = 0
//	    NGSetupRequestIEsValuePresentGlobalRANNodeID = 1
//	    ...
//	)
//
// and a companion Entry struct with Id / Criticality / Value that mirrors the
// ProtocolIE-Field expansion.
func (g *Generator) emitObjectSetChoices(f *jen.File, sets map[string]*resolver.ResolvedObjectSet) {
	for _, rs := range sets {
		g.emitValueChoice(f, rs)
		g.emitEntryStruct(f, rs)
	}
}

func (g *Generator) emitValueChoice(f *jen.File, rs *resolver.ResolvedObjectSet) {
	// Find the class field of kind TypeField (the "&Value" open type).
	valueField := ""
	for _, cf := range rs.Class.Fields {
		if cf.Kind == ast.TypeField {
			valueField = cf.Name
			break
		}
	}
	if valueField == "" {
		return
	}

	goName := GoName(rs.Name) + "Value"
	var fields []jen.Code
	fields = append(fields, jen.Id("Present").Int())

	// Dedup alternatives by their Type name (same type can appear for multiple IDs).
	seen := map[string]bool{}
	var alts []altByName
	for _, e := range rs.Entries {
		of, ok := e.Fields[valueField]
		if !ok {
			continue
		}
		typeName := ""
		switch t := of.Type.(type) {
		case ast.TypeReference:
			typeName = t.TypeName
		}
		if typeName == "" {
			continue
		}
		if seen[typeName] {
			continue
		}
		seen[typeName] = true
		alts = append(alts, altByName{Name: typeName, Type: typeName})
	}
	for _, a := range alts {
		field := jen.Id(GoFieldName(a.Name)).Op("*").Id(GoName(a.Type))
		if tag := g.namedTypeTag(a.Type); tag != "" {
			field = field.Tag(map[string]string{"aper": tag})
		}
		fields = append(fields, field)
	}
	f.Type().Id(goName).Struct(fields...)

	// Emit an id-to-alternative lookup so the runtime can decode the
	// directChoice open-type using the sibling Id field.
	g.emitValueChoiceLookup(f, rs, goName, alts)

	f.Const().DefsFunc(func(grp *jen.Group) {
		grp.Id(goName + "PresentNothing").Op("=").Lit(0)
		for i, a := range alts {
			grp.Id(goName + "Present" + GoName(a.Name)).Op("=").Lit(i + 1)
		}
	})
}

func (g *Generator) emitEntryStruct(f *jen.File, rs *resolver.ResolvedObjectSet) {
	// Discover the id / criticality / presence field types by walking the class.
	var idType, critType, presType string
	for _, cf := range rs.Class.Fields {
		if cf.Kind != ast.FixedTypeValueField {
			continue
		}
		switch t := cf.Type.(type) {
		case ast.TypeReference:
			switch {
			case idType == "":
				idType = t.TypeName
			case critType == "":
				critType = t.TypeName
			case presType == "":
				presType = t.TypeName
			}
		}
	}
	if idType == "" {
		return
	}
	goEntry := GoName(rs.Name) + "Entry"
	var fields []jen.Code
	idTag := g.namedTypeTag(idType)
	idField := jen.Id("Id").Id(GoName(idType))
	if idTag != "" {
		idField = idField.Tag(map[string]string{"aper": idTag})
	}
	fields = append(fields, idField)
	if critType != "" {
		critTag := g.namedTypeTag(critType)
		critField := jen.Id("Criticality").Id(GoName(critType))
		if critTag != "" {
			critField = critField.Tag(map[string]string{"aper": critTag})
		}
		fields = append(fields, critField)
	}
	// Value is the table-constrained open type. The "directChoice" marker
	// tells the runtime to encode JUST the selected alternative (without a
	// CHOICE index), then wrap it in an open-type length prefix. This matches
	// the 3GPP wire format where the alternative is selected by the &id field
	// at the receiver.
	fields = append(fields,
		jen.Id("Value").Id(GoName(rs.Name)+"Value").Tag(map[string]string{"aper": "openType,directChoice"}),
	)
	// Presence is class metadata (used by the sender to decide whether to
	// include the IE) and is NOT encoded on the wire. Tag it `aperSkip` so
	// the runtime ignores it.
	if presType != "" {
		fields = append(fields, jen.Id("Presence").Id(GoName(presType)).Tag(map[string]string{"aper": "skip"}))
	}
	f.Type().Id(goEntry).Struct(fields...)

	// Also emit a const block with each known id->name mapping (helpful for
	// callers building messages) — only when the id field's value resolved to
	// a ValueRef (e.g. id-GlobalRANNodeID).
	f.Const().DefsFunc(func(grp *jen.Group) {
		for _, e := range rs.Entries {
			idField, ok := e.Fields["&id"]
			if !ok {
				continue
			}
			var name string
			if vr, ok := idField.Value.(ast.ValueRef); ok {
				name = vr.Name
			} else if idField.ObjectRef != "" {
				name = idField.ObjectRef
			}
			if name == "" {
				continue
			}
			// Map a marker constant to the id reference for callers.
			grp.Id(GoName(rs.Name) + "Entry" + GoName(name)).Op("=").Id(GoName(name))
		}
	})
}
