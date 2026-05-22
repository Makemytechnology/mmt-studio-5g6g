// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package resolver walks a collection of parsed modules and builds a global
// registry of type/value assignments, resolves integer value references used in
// constraints, and records parameterized-type instantiations and object sets
// for the code generator.
package resolver

import (
	"fmt"

	"github.com/mmt/asn1go/pkg/ast"
)

type Symbol struct {
	Module     *ast.Module
	Assignment *ast.Assignment
}

type Registry struct {
	Modules []*ast.Module
	// Fully-qualified symbol table: moduleName.typeName -> Symbol
	ByQualified map[string]Symbol
	// Unqualified lookup; first module to define a name wins (plus per-module scope).
	Global map[string]Symbol
	// Integer value constants (e.g. maxProtocolIEs)
	IntConstants map[string]int64
}

func Build(modules []*ast.Module) *Registry {
	r := &Registry{
		Modules:      modules,
		ByQualified:  make(map[string]Symbol),
		Global:       make(map[string]Symbol),
		IntConstants: make(map[string]int64),
	}
	for _, m := range modules {
		for i := range m.Assignments {
			a := &m.Assignments[i]
			sym := Symbol{Module: m, Assignment: a}
			r.ByQualified[m.Name+"."+a.Name] = sym
			if _, ok := r.Global[a.Name]; !ok {
				r.Global[a.Name] = sym
			}
			if a.IsValue {
				if iv, ok := a.Value.(ast.IntegerValue); ok {
					r.IntConstants[a.Name] = iv.Int
				}
			}
		}
	}
	// Resolve IMPORTS: for each imported symbol, if not already in Global, add.
	for _, m := range modules {
		for _, imp := range m.Imports {
			for _, sym := range imp.Symbols {
				key := imp.ModuleName + "." + sym
				if s, ok := r.ByQualified[key]; ok {
					if _, exists := r.Global[sym]; !exists {
						r.Global[sym] = s
					}
				}
			}
		}
	}
	return r
}

// ResolveInt resolves an integer-valued reference recursively. Returns (value, true) on success.
func (r *Registry) ResolveInt(name string) (int64, bool) {
	if v, ok := r.IntConstants[name]; ok {
		return v, true
	}
	if sym, ok := r.Global[name]; ok && sym.Assignment != nil && sym.Assignment.IsValue {
		switch v := sym.Assignment.Value.(type) {
		case ast.IntegerValue:
			return v.Int, true
		case ast.ValueRef:
			return r.ResolveInt(v.Name)
		}
	}
	return 0, false
}

// ResolveValue returns the integer for a value-bound in a constraint if resolvable,
// using either a literal or a value-ref.
func (r *Registry) ResolveValue(v ast.Value) (int64, bool) {
	switch vv := v.(type) {
	case ast.IntegerValue:
		return vv.Int, true
	case ast.ValueRef:
		if vv.Name == "MIN" || vv.Name == "MAX" {
			return 0, false
		}
		return r.ResolveInt(vv.Name)
	}
	return 0, false
}

// Lookup finds a type by unqualified or qualified name.
func (r *Registry) Lookup(name string) (Symbol, error) {
	if s, ok := r.Global[name]; ok {
		return s, nil
	}
	if s, ok := r.ByQualified[name]; ok {
		return s, nil
	}
	return Symbol{}, fmt.Errorf("unresolved reference: %s", name)
}
