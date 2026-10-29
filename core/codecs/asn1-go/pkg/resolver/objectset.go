// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package resolver

import (
	"github.com/mmt/asn1go/pkg/ast"
)

// ObjectEntry is one row of a resolved object set, keyed by class field name
// (e.g. "&id" -> ProtocolIE id value, "&Value" -> Type of the IE).
type ObjectEntry struct {
	Fields map[string]ast.ObjectField
}

// ResolvedObjectSet is the object-set view the codegen consumes: a class and a
// list of entries whose field names come from the class declaration. Literal
// labels (ID, TYPE, ...) have been mapped back to class fields (&id, &Value)
// via the class's WITH SYNTAX.
type ResolvedObjectSet struct {
	Name    string
	Class   *ast.InfoObjectClass
	Entries []ObjectEntry
}

// BuildObjectSets walks every object set assignment in the registry and
// resolves it against its class WITH SYNTAX. Returns a map from the object-set
// name to the resolved form.
func (r *Registry) BuildObjectSets() map[string]*ResolvedObjectSet {
	result := make(map[string]*ResolvedObjectSet)

	// Index classes by name.
	classByName := map[string]*ast.InfoObjectClass{}
	for _, m := range r.Modules {
		for i := range m.Assignments {
			a := &m.Assignments[i]
			if a.IsClass {
				classByName[a.Name] = a.Class
			}
		}
	}

	for _, m := range r.Modules {
		for i := range m.Assignments {
			a := &m.Assignments[i]
			if !a.IsObjectSet {
				continue
			}
			cls := classByName[a.ObjectSet.ClassName]
			if cls == nil {
				// Unknown class — skip, codegen will ignore.
				continue
			}
			rset := &ResolvedObjectSet{Name: a.Name, Class: cls}
			// Build literal -> class-field mapping from WITH SYNTAX.
			// Each cls.Syntax entry has one or more literal tokens followed by a &field ref.
			literalToField := map[string]string{}
			for _, group := range cls.Syntax {
				if len(group) < 2 {
					continue
				}
				field := group[len(group)-1]
				// Use the last literal before the field ref as the primary key.
				for _, lit := range group[:len(group)-1] {
					literalToField[lit] = field
				}
			}
			for _, obj := range a.ObjectSet.Objects {
				entry := ObjectEntry{Fields: map[string]ast.ObjectField{}}
				for lit, f := range obj.Fields {
					fieldName, ok := literalToField[lit]
					if !ok {
						// Literal wasn't declared in WITH SYNTAX — skip.
						continue
					}
					entry.Fields[fieldName] = f
				}
				rset.Entries = append(rset.Entries, entry)
			}
			result[a.Name] = rset
		}
	}
	return result
}
