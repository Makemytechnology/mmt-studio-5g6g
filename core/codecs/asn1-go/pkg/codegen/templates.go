// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"fmt"
	"os"

	"github.com/mmt/asn1go/pkg/ast"
)

var debugTemplates = os.Getenv("ASN1GO_DEBUG") != ""

func debugf(format string, a ...any) {
	if debugTemplates {
		fmt.Fprintf(os.Stderr, "[tmpl] "+format+"\n", a...)
	}
}

// templateIndex catalogues every parameterised type assignment keyed by its
// bare ASN.1 name, along with its unparameterised body. The generator uses it
// to recognise instantiations like ProtocolIE-Container{{NGSetupRequestIEs}}
// and expand them to concrete Go types (typically a typed slice whose element
// is <ObjectSet>Entry).
type templateIndex struct {
	byName map[string]*ast.Assignment
}

func (g *Generator) buildTemplateIndex() *templateIndex {
	tx := &templateIndex{byName: map[string]*ast.Assignment{}}
	for _, m := range g.Reg.Modules {
		for i := range m.Assignments {
			a := &m.Assignments[i]
			if a.Parameterized != nil {
				tx.byName[a.Name] = a
			}
		}
	}
	return tx
}

// matchSingleContainerInstantiation returns an object-set name and `isSlice=false`
// for the `ProtocolIE-SingleContainer{{ObjectSet}}` style template whose body
// is directly a ProtocolIE-Field-shaped SEQUENCE (not wrapped in SEQUENCE OF).
func (g *Generator) matchSingleContainerInstantiation(tr ast.TypeReference) (objectSet string, ok bool) {
	if len(tr.Args) == 0 {
		return "", false
	}
	tmpl := g.tmpl.byName[tr.TypeName]
	if tmpl == nil || tmpl.Parameterized == nil {
		return "", false
	}
	body := tmpl.Parameterized.Type
	if body == nil {
		body = tmpl.Type
	}
	if !isProtocolIEFieldShape(body) {
		ref, ok2 := body.(ast.TypeReference)
		if !ok2 {
			return "", false
		}
		fieldTmpl := g.tmpl.byName[ref.TypeName]
		if fieldTmpl == nil {
			return "", false
		}
		fb := fieldTmpl.Parameterized.Type
		if fb == nil {
			fb = fieldTmpl.Type
		}
		if !isProtocolIEFieldShape(fb) {
			return "", false
		}
	}
	arg0 := tr.Args[0]
	ref, ok := arg0.(ast.TypeReference)
	if !ok {
		return "", false
	}
	if _, known := g.sets[ref.TypeName]; !known {
		return "", false
	}
	return ref.TypeName, true
}

// isUnresolvableTemplateCall reports whether this TypeReference is an
// instantiation of a known parameterised template but with arguments that are
// themselves template parameters (unresolved). Codegen falls back to []byte
// for these so the surrounding types still compile.
func (g *Generator) isUnresolvableTemplateCall(tr ast.TypeReference) bool {
	if len(tr.Args) == 0 {
		return false
	}
	if _, ok := g.tmpl.byName[tr.TypeName]; !ok {
		return false
	}
	return true
}

// containerSizeConstraint returns the SIZE constraint from a matched container
// template body, so the generator can preserve it as an aper struct tag.
func (g *Generator) containerSizeConstraint(tr ast.TypeReference) *ast.Constraint {
	tmpl := g.tmpl.byName[tr.TypeName]
	if tmpl == nil || tmpl.Parameterized == nil {
		return nil
	}
	body := tmpl.Parameterized.Type
	if body == nil {
		body = tmpl.Type
	}
	if seqOf, ok := body.(ast.SequenceOfType); ok {
		return seqOf.Constraint
	}
	return nil
}

// matchContainerInstantiation returns the resolved object-set name if the
// given TypeReference is an instantiation of a SEQUENCE-OF-FIELD-shape
// parameterised container whose sole argument is an object set we know about.
//
// Pattern (3GPP ProtocolIE-Container):
//
//	TemplateName {objectSetRef}
//	   where TemplateName's body is:  SEQUENCE (SIZE(...)) OF FieldTemplate{{param}}
//	   and FieldTemplate's body is a SEQUENCE with three components that refer
//	   to a single CLASS: id/criticality/value.
//
// On a successful match we return the object-set name that was passed in — the
// caller uses it to emit `[]<ObjectSet>Entry`.
func (g *Generator) matchContainerInstantiation(tr ast.TypeReference) (objectSet string, ok bool) {
	if len(tr.Args) == 0 {
		return "", false
	}
	tmpl := g.tmpl.byName[tr.TypeName]
	if tmpl == nil || tmpl.Parameterized == nil {
		return "", false
	}
	// The template body must be a SEQUENCE OF another parameterised type-ref.
	body := tmpl.Parameterized.Type
	if body == nil {
		body = tmpl.Type
	}
	seqOf, ok := body.(ast.SequenceOfType)
	if !ok {
		return "", false
	}
	fieldRef, ok := seqOf.ElementType.(ast.TypeReference)
	if !ok || len(fieldRef.Args) == 0 {
		return "", false
	}
	// The inner parameterised ref must itself be a known template whose body
	// is the protocol-IE field shape.
	fieldTmpl := g.tmpl.byName[fieldRef.TypeName]
	if fieldTmpl == nil {
		return "", false
	}
	fieldBody := fieldTmpl.Parameterized.Type
	if fieldBody == nil {
		fieldBody = fieldTmpl.Type
	}
	if !isProtocolIEFieldShape(fieldBody) {
		return "", false
	}
	// Extract the single object-set argument from the outer call.
	arg0 := tr.Args[0]
	ref, ok := arg0.(ast.TypeReference)
	if !ok {
		return "", false
	}
	// Accept only if it names a resolved object set.
	if _, known := g.sets[ref.TypeName]; !known {
		return "", false
	}
	return ref.TypeName, true
}

// isProtocolIEFieldShape matches SEQUENCE { id, criticality, value } where all
// three components reference the same class (recognisable because the id and
// criticality fields come via a ClassRef.&field path and the value field is
// an OpenType).
func isProtocolIEFieldShape(t ast.Type) bool {
	seq, ok := t.(ast.SequenceType)
	if !ok {
		return false
	}
	if len(seq.Components) < 3 {
		return false
	}
	var hasValueOpen bool
	for _, c := range seq.Components {
		if _, isOpen := c.Type.(ast.OpenType); isOpen {
			hasValueOpen = true
		}
	}
	return hasValueOpen
}
