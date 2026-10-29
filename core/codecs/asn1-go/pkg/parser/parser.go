// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package parser implements a recursive-descent parser for the subset of
// ITU-T X.680/X.681/X.682/X.683 that 3GPP ASN.1 schemas use in practice.
package parser

import (
	"fmt"
	"strconv"

	"github.com/mmt/asn1go/pkg/ast"
	"github.com/mmt/asn1go/pkg/lexer"
)

type Parser struct {
	lx     *lexer.Lexer
	cur    lexer.Token
	peek   lexer.Token
	errors ErrorList
}

func New(src string) *Parser {
	p := &Parser{lx: lexer.New(src)}
	p.cur = p.lx.Next()
	p.peek = p.lx.Next()
	return p
}

func (p *Parser) Errors() ErrorList { return p.errors }

func (p *Parser) errorf(format string, a ...any) {
	p.errors = append(p.errors, Error{
		Line: p.cur.Line, Column: p.cur.Column,
		Message: fmt.Sprintf(format, a...),
	})
}

func (p *Parser) next() {
	p.cur = p.peek
	p.peek = p.lx.Next()
}

func (p *Parser) accept(k lexer.TokenKind) bool {
	if p.cur.Kind == k {
		p.next()
		return true
	}
	return false
}

func (p *Parser) expect(k lexer.TokenKind) lexer.Token {
	t := p.cur
	if p.cur.Kind != k {
		p.errorf("expected %d, got %s (%q)", k, t, t.Lit)
		return t
	}
	p.next()
	return t
}

// ParseModules parses one or more modules from the source.
func (p *Parser) ParseModules() []*ast.Module {
	var mods []*ast.Module
	for p.cur.Kind != lexer.TOKEN_EOF {
		m := p.parseModule()
		if m != nil {
			mods = append(mods, m)
		}
		if len(p.errors) > 0 && p.cur.Kind == lexer.TOKEN_EOF {
			break
		}
	}
	return mods
}

func (p *Parser) parseModule() *ast.Module {
	m := &ast.Module{}
	if p.cur.Kind != lexer.TOKEN_TYPE_REF {
		p.errorf("expected module identifier, got %q", p.cur.Lit)
		return nil
	}
	m.Name = p.cur.Lit
	p.next()

	// Optional OID / defined value (skip — not interesting for codegen)
	if p.cur.Kind == lexer.TOKEN_LBRACE {
		p.skipBalanced(lexer.TOKEN_LBRACE, lexer.TOKEN_RBRACE)
	}

	p.expect(lexer.TOKEN_DEFINITIONS)

	// Tag default
	m.TagDefault = ast.TagExplicit
	switch p.cur.Kind {
	case lexer.TOKEN_AUTOMATIC:
		m.TagDefault = ast.TagAutomatic
		p.next()
		p.expect(lexer.TOKEN_TAGS)
	case lexer.TOKEN_IMPLICIT:
		m.TagDefault = ast.TagImplicit
		p.next()
		p.expect(lexer.TOKEN_TAGS)
	case lexer.TOKEN_EXPLICIT:
		p.next()
		p.expect(lexer.TOKEN_TAGS)
	}

	if p.cur.Kind == lexer.TOKEN_EXTENSIBILITY {
		p.next()
		p.expect(lexer.TOKEN_IMPLIED)
		m.Extensibility = true
	}

	p.expect(lexer.TOKEN_ASSIGN)
	p.expect(lexer.TOKEN_BEGIN)

	// Exports
	if p.cur.Kind == lexer.TOKEN_EXPORTS {
		p.next()
		if p.cur.Kind == lexer.TOKEN_ALL {
			m.ExportsAll = true
			p.next()
		} else {
			for p.cur.Kind != lexer.TOKEN_SEMICOLON && p.cur.Kind != lexer.TOKEN_EOF {
				if p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
					m.Exports = append(m.Exports, p.cur.Lit)
				}
				p.next()
				p.accept(lexer.TOKEN_COMMA)
			}
		}
		p.expect(lexer.TOKEN_SEMICOLON)
	}

	// Imports
	if p.cur.Kind == lexer.TOKEN_IMPORTS {
		p.next()
		for p.cur.Kind != lexer.TOKEN_SEMICOLON && p.cur.Kind != lexer.TOKEN_EOF {
			var syms []string
			for p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
				syms = append(syms, p.cur.Lit)
				p.next()
				// Parameterised-type import form: `ProtocolIE-Container{},`
				// The empty braces mark the symbol as a template.
				if p.cur.Kind == lexer.TOKEN_LBRACE {
					p.skipBalanced(lexer.TOKEN_LBRACE, lexer.TOKEN_RBRACE)
				}
				if !p.accept(lexer.TOKEN_COMMA) {
					break
				}
			}
			if p.cur.Kind != lexer.TOKEN_FROM {
				break
			}
			p.next()
			mod := ""
			if p.cur.Kind == lexer.TOKEN_TYPE_REF {
				mod = p.cur.Lit
				p.next()
			}
			// Optional OID/defined value after module name. Only a brace-form
			// ObjectIdentifierValue is accepted here; a bare identifier is the
			// start of the next symbol list, not an AssignedIdentifier.
			if p.cur.Kind == lexer.TOKEN_LBRACE {
				p.skipBalanced(lexer.TOKEN_LBRACE, lexer.TOKEN_RBRACE)
			}
			m.Imports = append(m.Imports, ast.Import{Symbols: syms, ModuleName: mod})
		}
		p.expect(lexer.TOKEN_SEMICOLON)
	}

	// Assignments
	for p.cur.Kind != lexer.TOKEN_END && p.cur.Kind != lexer.TOKEN_EOF {
		start := p.cur
		a := p.parseAssignment()
		if a != nil {
			m.Assignments = append(m.Assignments, *a)
		}
		if p.cur == start {
			// No progress — skip token to avoid infinite loop.
			p.errorf("unexpected token %q; skipping", p.cur.Lit)
			p.next()
		}
	}
	p.expect(lexer.TOKEN_END)
	return m
}

func (p *Parser) parseAssignment() *ast.Assignment {
	// Both type and value assignments start with an identifier.
	// Type refs (uppercase) -> type assignment or class/object set assignment
	// Identifiers (lowercase) -> value assignment
	switch p.cur.Kind {
	case lexer.TOKEN_TYPE_REF:
		name := p.cur.Lit
		p.next()

		// Parameterized: Name { Param1, Param2 } ::= ...
		var params []ast.Parameter
		if p.cur.Kind == lexer.TOKEN_LBRACE {
			params = p.parseParameters()
		}

		// Object set assignment: Name ClassRef ::= { obj | obj | ... }
		// Heuristic: TYPE_REF followed by ::= means we have `Name ClassRef ::= ...`
		if p.cur.Kind == lexer.TOKEN_TYPE_REF && p.peek.Kind == lexer.TOKEN_ASSIGN {
			className := p.cur.Lit
			p.next()
			p.expect(lexer.TOKEN_ASSIGN)
			os := p.parseObjectSet(className)
			return &ast.Assignment{Name: name, IsObjectSet: true, ObjectSet: os}
		}

		p.expect(lexer.TOKEN_ASSIGN)

		// Class? CLASS { ... }
		if p.cur.Kind == lexer.TOKEN_CLASS {
			cls := p.parseInfoObjectClass()
			cls.Name = name
			return &ast.Assignment{Name: name, IsClass: true, Class: cls}
		}

		// Object set assignment: ClassRef ::= { obj1 | obj2 | ... }
		// We detect by seeing a TypeRef followed by LBRACE (the { ... set } form), and
		// distinguish from a parameterized type instantiation.
		if p.cur.Kind == lexer.TOKEN_TYPE_REF && p.peek.Kind == lexer.TOKEN_ASSIGN {
			// unusual — fall through to type parse
		}

		// Try to parse as object set first: a single TypeRef whose RHS starts with '{' that
		// contains objects separated by | or containing fields.
		if p.cur.Kind == lexer.TOKEN_TYPE_REF && p.peek.Kind == lexer.TOKEN_LBRACE {
			// Look ahead: if after TypeRef LBRACE we see an ID keyword or identifier followed by a TypeRef,
			// this is an object set. Otherwise treat as type reference with constraint.
			// Heuristic: parse as Type; the generic type parser already handles "TypeRef { params }".
		}

		t := p.parseType()
		if params != nil {
			return &ast.Assignment{
				Name:          name,
				Parameterized: &ast.ParameterizedDef{Parameters: params, Type: t},
				Type:          t,
			}
		}
		return &ast.Assignment{Name: name, Type: t}

	case lexer.TOKEN_IDENTIFIER:
		// Value assignment: valuename Type ::= value
		name := p.cur.Lit
		p.next()
		t := p.parseType()
		p.expect(lexer.TOKEN_ASSIGN)
		v := p.parseValue()
		return &ast.Assignment{Name: name, IsValue: true, ValueType: t, Value: v}
	}
	return nil
}

// parseParameters consumes a parameter list: { Governor : Name, Governor : Name, ... }
func (p *Parser) parseParameters() []ast.Parameter {
	p.expect(lexer.TOKEN_LBRACE)
	var params []ast.Parameter
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		var par ast.Parameter
		// Governor:  TypeRef  or  ClassName  or built-in
		if p.cur.Kind == lexer.TOKEN_TYPE_REF || isBuiltinTypeStart(p.cur.Kind) {
			par.Governor = p.cur.Lit
			// Heuristic: an all-uppercase-with-hyphens name is a class governor in 3GPP.
			par.IsClass = isAllUpperHyphen(p.cur.Lit)
			p.next()
		}
		if p.cur.Kind == lexer.TOKEN_COLON {
			p.next()
			if p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
				par.Name = p.cur.Lit
				p.next()
			}
		} else {
			// DummyReference form without governor (name only)
			if p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
				par.Name = p.cur.Lit
				p.next()
			}
		}
		params = append(params, par)
		if !p.accept(lexer.TOKEN_COMMA) {
			break
		}
	}
	p.expect(lexer.TOKEN_RBRACE)
	return params
}

func isAllUpperHyphen(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r == '-' {
			continue
		}
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return false
		}
	}
	return true
}

func isBuiltinTypeStart(k lexer.TokenKind) bool {
	switch k {
	case lexer.TOKEN_INTEGER, lexer.TOKEN_BOOLEAN, lexer.TOKEN_NULL,
		lexer.TOKEN_REAL, lexer.TOKEN_BIT, lexer.TOKEN_OCTET,
		lexer.TOKEN_SEQUENCE, lexer.TOKEN_SET, lexer.TOKEN_CHOICE,
		lexer.TOKEN_ENUMERATED, lexer.TOKEN_OBJECT,
		lexer.TOKEN_PRINTABLE_STRING, lexer.TOKEN_UTF8_STRING,
		lexer.TOKEN_IA5_STRING, lexer.TOKEN_VISIBLE_STRING,
		lexer.TOKEN_NUMERIC_STRING, lexer.TOKEN_BMPSTRING,
		lexer.TOKEN_UNIVERSAL_STRING, lexer.TOKEN_GENERAL_STRING,
		lexer.TOKEN_TELETEX_STRING, lexer.TOKEN_GRAPHIC_STRING,
		lexer.TOKEN_VIDEOTEX_STRING, lexer.TOKEN_UTCTIME,
		lexer.TOKEN_GENERALIZED_TIME:
		return true
	}
	return false
}

// skipBalanced consumes a balanced open/close pair, including nested pairs.
func (p *Parser) skipBalanced(open, close lexer.TokenKind) {
	if p.cur.Kind != open {
		return
	}
	depth := 0
	for p.cur.Kind != lexer.TOKEN_EOF {
		if p.cur.Kind == open {
			depth++
		} else if p.cur.Kind == close {
			depth--
			if depth == 0 {
				p.next()
				return
			}
		}
		p.next()
	}
}

// --- Type parsing ---

func (p *Parser) parseType() ast.Type {
	var t ast.Type

	// Optional tag prefix:  [n] or [CLASS n] [IMPLICIT|EXPLICIT]
	var tag *ast.Tag
	if p.cur.Kind == lexer.TOKEN_LBRACKET {
		tg := p.parseTag()
		tag = &tg
	}

	switch p.cur.Kind {
	case lexer.TOKEN_BOOLEAN:
		p.next()
		t = ast.BooleanType{}
	case lexer.TOKEN_NULL:
		p.next()
		t = ast.NullType{}
	case lexer.TOKEN_REAL:
		p.next()
		t = ast.RealType{}
	case lexer.TOKEN_INTEGER:
		p.next()
		it := ast.IntegerType{}
		if p.cur.Kind == lexer.TOKEN_LBRACE {
			it.NamedNumbers = p.parseNamedNumberList()
		}
		t = it
	case lexer.TOKEN_ENUMERATED:
		p.next()
		t = p.parseEnumerated()
	case lexer.TOKEN_BIT:
		p.next()
		p.expect(lexer.TOKEN_STRING)
		bs := ast.BitStringType{}
		if p.cur.Kind == lexer.TOKEN_LBRACE {
			bs.NamedBits = p.parseNamedNumberList()
		}
		t = bs
	case lexer.TOKEN_OCTET:
		p.next()
		p.expect(lexer.TOKEN_STRING)
		t = ast.OctetStringType{}
	case lexer.TOKEN_OBJECT:
		p.next()
		p.expect(lexer.TOKEN_IDENTIFIER_KW)
		t = ast.ObjectIdentifierType{}
	case lexer.TOKEN_SEQUENCE:
		p.next()
		t = p.parseSequenceOrSequenceOf()
	case lexer.TOKEN_SET:
		p.next()
		t = p.parseSetOrSetOf()
	case lexer.TOKEN_CHOICE:
		p.next()
		t = p.parseChoice()
	case lexer.TOKEN_PRINTABLE_STRING, lexer.TOKEN_UTF8_STRING, lexer.TOKEN_IA5_STRING,
		lexer.TOKEN_VISIBLE_STRING, lexer.TOKEN_NUMERIC_STRING, lexer.TOKEN_BMPSTRING,
		lexer.TOKEN_UNIVERSAL_STRING, lexer.TOKEN_GENERAL_STRING, lexer.TOKEN_TELETEX_STRING,
		lexer.TOKEN_GRAPHIC_STRING, lexer.TOKEN_VIDEOTEX_STRING:
		kind := p.cur.Lit
		p.next()
		t = ast.CharStringType{Kind: kind}
	case lexer.TOKEN_TYPE_REF:
		ref := ast.TypeReference{TypeName: p.cur.Lit}
		p.next()
		if p.cur.Kind == lexer.TOKEN_DOT {
			p.next()
			if p.cur.Kind == lexer.TOKEN_AMPERSAND {
				// Class field reference: Class.&Field — treat as open-type-ish
				p.next()
				if p.cur.Kind == lexer.TOKEN_IDENTIFIER || p.cur.Kind == lexer.TOKEN_TYPE_REF {
					field := p.cur.Lit
					p.next()
					ot := ast.OpenType{ClassName: ref.TypeName, FieldName: field}
					// optional constraint
					if p.cur.Kind == lexer.TOKEN_LPAREN {
						ot.Constraint = p.parseConstraint()
					}
					return ot
				}
			} else if p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
				// Module.Type form
				ref.ModuleName = ref.TypeName
				ref.TypeName = p.cur.Lit
				p.next()
			}
		}
		// Parameterized instantiation: TypeRef { arg1, arg2 }
		if p.cur.Kind == lexer.TOKEN_LBRACE {
			ref.Args = p.parseActualParameters()
		}
		t = ref
	default:
		p.errorf("unexpected token in type: %q", p.cur.Lit)
		p.next()
		return nil
	}

	// Constraint(s) follow
	for p.cur.Kind == lexer.TOKEN_LPAREN {
		c := p.parseConstraint()
		t = applyConstraint(t, c)
	}

	if tag != nil {
		t = ast.TaggedType{Tag: *tag, Type: t}
	}
	return t
}

func (p *Parser) parseTag() ast.Tag {
	p.expect(lexer.TOKEN_LBRACKET)
	tag := ast.Tag{Class: ast.TagClassContextSpecific}
	// optional class
	// For MVP: accept bare number.
	if p.cur.Kind == lexer.TOKEN_NUMBER {
		n, _ := strconv.Atoi(p.cur.Lit)
		tag.Number = n
		p.next()
	}
	p.expect(lexer.TOKEN_RBRACKET)
	if p.accept(lexer.TOKEN_IMPLICIT) {
		tag.Implicit = true
	} else if p.accept(lexer.TOKEN_EXPLICIT) {
		tag.Explicit = true
	}
	return tag
}

// applyConstraint attaches a constraint to whichever type supports one.
func applyConstraint(t ast.Type, c *ast.Constraint) ast.Type {
	switch tt := t.(type) {
	case ast.IntegerType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.BitStringType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.OctetStringType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.CharStringType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.SequenceOfType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.SetOfType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.TypeReference:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	case ast.OpenType:
		tt.Constraint = mergeConstraint(tt.Constraint, c)
		return tt
	}
	return t
}

func mergeConstraint(existing, n *ast.Constraint) *ast.Constraint {
	if existing == nil {
		return n
	}
	// Combine as intersection.
	return &ast.Constraint{
		Kind:     ast.ConstraintIntersection,
		Operands: []*ast.Constraint{existing, n},
	}
}

func (p *Parser) parseNamedNumberList() []ast.NamedNumber {
	p.expect(lexer.TOKEN_LBRACE)
	var out []ast.NamedNumber
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		if p.cur.Kind != lexer.TOKEN_IDENTIFIER && p.cur.Kind != lexer.TOKEN_TYPE_REF {
			break
		}
		nn := ast.NamedNumber{Name: p.cur.Lit}
		p.next()
		if p.accept(lexer.TOKEN_LPAREN) {
			if p.cur.Kind == lexer.TOKEN_NUMBER {
				v, _ := strconv.ParseInt(p.cur.Lit, 10, 64)
				nn.Value = v
				p.next()
			} else if p.cur.Kind == lexer.TOKEN_IDENTIFIER {
				nn.Ref = p.cur.Lit
				p.next()
			}
			p.expect(lexer.TOKEN_RPAREN)
		}
		out = append(out, nn)
		if !p.accept(lexer.TOKEN_COMMA) {
			break
		}
	}
	p.expect(lexer.TOKEN_RBRACE)
	return out
}

func (p *Parser) parseEnumerated() ast.Type {
	p.expect(lexer.TOKEN_LBRACE)
	et := ast.EnumeratedType{}
	inExt := false
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		if p.accept(lexer.TOKEN_ELLIPSIS) {
			et.Extensible = true
			inExt = true
			if !p.accept(lexer.TOKEN_COMMA) {
				continue
			}
			continue
		}
		if p.cur.Kind != lexer.TOKEN_IDENTIFIER && p.cur.Kind != lexer.TOKEN_TYPE_REF {
			break
		}
		item := ast.EnumItem{Name: p.cur.Lit}
		p.next()
		if p.accept(lexer.TOKEN_LPAREN) {
			if p.cur.Kind == lexer.TOKEN_NUMBER {
				v, _ := strconv.ParseInt(p.cur.Lit, 10, 64)
				item.Value = v
				item.HasValue = true
				p.next()
			}
			p.expect(lexer.TOKEN_RPAREN)
		}
		if inExt {
			et.ExtensionEnums = append(et.ExtensionEnums, item)
		} else {
			et.RootEnums = append(et.RootEnums, item)
		}
		if !p.accept(lexer.TOKEN_COMMA) {
			break
		}
	}
	p.expect(lexer.TOKEN_RBRACE)
	return et
}

func (p *Parser) parseSequenceOrSequenceOf() ast.Type {
	// SEQUENCE (SIZE(...)) OF Type   --or--   SEQUENCE { ... }
	// Also:   SEQUENCE OF Type
	if p.cur.Kind == lexer.TOKEN_LPAREN {
		// SIZE constraint before OF
		c := p.parseConstraint()
		p.expect(lexer.TOKEN_OF)
		el := p.parseType()
		return ast.SequenceOfType{ElementType: el, Constraint: c}
	}
	if p.cur.Kind == lexer.TOKEN_OF {
		p.next()
		el := p.parseType()
		return ast.SequenceOfType{ElementType: el}
	}
	return p.parseSequenceBody()
}

func (p *Parser) parseSetOrSetOf() ast.Type {
	if p.cur.Kind == lexer.TOKEN_LPAREN {
		c := p.parseConstraint()
		p.expect(lexer.TOKEN_OF)
		el := p.parseType()
		return ast.SetOfType{ElementType: el, Constraint: c}
	}
	if p.cur.Kind == lexer.TOKEN_OF {
		p.next()
		el := p.parseType()
		return ast.SetOfType{ElementType: el}
	}
	// SET { ... }
	s := p.parseSequenceBody()
	seq := s.(ast.SequenceType)
	return ast.SetType{
		Components:         seq.Components,
		Extensible:         seq.Extensible,
		ExtensionAdditions: seq.ExtensionAdditions,
	}
}

func (p *Parser) parseSequenceBody() ast.Type {
	p.expect(lexer.TOKEN_LBRACE)
	seq := ast.SequenceType{}
	state := 0 // 0=root, 1=in-extension, 2=trailing root (after 2nd ...)
	var curExtGroup *ast.ExtensionAdditionGroup
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		// Ellipsis marker
		if p.accept(lexer.TOKEN_ELLIPSIS) {
			seq.Extensible = true
			if state == 0 {
				state = 1
			} else if state == 1 {
				state = 2
			}
			if p.cur.Kind == lexer.TOKEN_EXCLAMATION {
				// exception spec — skip until , or )
				p.next()
				for p.cur.Kind != lexer.TOKEN_COMMA && p.cur.Kind != lexer.TOKEN_RBRACE {
					p.next()
				}
			}
			p.accept(lexer.TOKEN_COMMA)
			continue
		}

		// Extension addition group [[ ... ]]
		if p.accept(lexer.TOKEN_DBL_LBRACK) {
			grp := ast.ExtensionAdditionGroup{}
			// optional version number "2:"
			if p.cur.Kind == lexer.TOKEN_NUMBER && p.peek.Kind == lexer.TOKEN_COLON {
				v, _ := strconv.Atoi(p.cur.Lit)
				grp.Version = &v
				p.next()
				p.next()
			}
			for p.cur.Kind != lexer.TOKEN_DBL_RBRACK && p.cur.Kind != lexer.TOKEN_EOF {
				comp, ok := p.parseComponent()
				if ok {
					grp.Components = append(grp.Components, comp)
				}
				if !p.accept(lexer.TOKEN_COMMA) {
					break
				}
			}
			p.expect(lexer.TOKEN_DBL_RBRACK)
			seq.ExtensionAdditions = append(seq.ExtensionAdditions, grp)
			curExtGroup = nil
			p.accept(lexer.TOKEN_COMMA)
			continue
		}

		comp, ok := p.parseComponent()
		if !ok {
			break
		}
		switch state {
		case 0:
			seq.Components = append(seq.Components, comp)
		case 1:
			// extension addition (not in [[ ]] group) — wrap in single-element group
			if curExtGroup == nil {
				seq.ExtensionAdditions = append(seq.ExtensionAdditions, ast.ExtensionAdditionGroup{})
				curExtGroup = &seq.ExtensionAdditions[len(seq.ExtensionAdditions)-1]
			}
			curExtGroup.Components = append(curExtGroup.Components, comp)
		case 2:
			seq.TrailingComponents = append(seq.TrailingComponents, comp)
		}
		if !p.accept(lexer.TOKEN_COMMA) {
			break
		}
	}
	p.expect(lexer.TOKEN_RBRACE)
	return seq
}

func (p *Parser) parseComponent() (ast.ComponentType, bool) {
	// COMPONENTS OF TypeRef
	if p.cur.Kind == lexer.TOKEN_COMPONENTS && p.peek.Kind == lexer.TOKEN_OF {
		p.next()
		p.next()
		if p.cur.Kind == lexer.TOKEN_TYPE_REF {
			name := p.cur.Lit
			p.next()
			return ast.ComponentType{ComponentsOf: name}, true
		}
	}

	if p.cur.Kind != lexer.TOKEN_IDENTIFIER {
		return ast.ComponentType{}, false
	}
	comp := ast.ComponentType{Name: p.cur.Lit}
	p.next()

	// Optional tag prefix before type
	var tag *ast.Tag
	if p.cur.Kind == lexer.TOKEN_LBRACKET {
		tg := p.parseTag()
		tag = &tg
	}
	comp.Tag = tag

	comp.Type = p.parseType()

	if p.accept(lexer.TOKEN_OPTIONAL) {
		comp.Optional = true
	} else if p.accept(lexer.TOKEN_DEFAULT) {
		comp.HasDefault = true
		comp.Default = p.parseValue()
	}
	return comp, true
}

func (p *Parser) parseChoice() ast.Type {
	p.expect(lexer.TOKEN_LBRACE)
	ch := ast.ChoiceType{}
	inExt := false
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		if p.accept(lexer.TOKEN_ELLIPSIS) {
			ch.Extensible = true
			inExt = true
			if p.cur.Kind == lexer.TOKEN_EXCLAMATION {
				p.next()
				for p.cur.Kind != lexer.TOKEN_COMMA && p.cur.Kind != lexer.TOKEN_RBRACE {
					p.next()
				}
			}
			p.accept(lexer.TOKEN_COMMA)
			continue
		}
		if p.accept(lexer.TOKEN_DBL_LBRACK) {
			for p.cur.Kind != lexer.TOKEN_DBL_RBRACK && p.cur.Kind != lexer.TOKEN_EOF {
				if p.cur.Kind == lexer.TOKEN_IDENTIFIER {
					alt := p.parseChoiceAlternative()
					ch.ExtensionAlternatives = append(ch.ExtensionAlternatives, alt)
				}
				if !p.accept(lexer.TOKEN_COMMA) {
					break
				}
			}
			p.expect(lexer.TOKEN_DBL_RBRACK)
			p.accept(lexer.TOKEN_COMMA)
			continue
		}
		if p.cur.Kind != lexer.TOKEN_IDENTIFIER {
			break
		}
		alt := p.parseChoiceAlternative()
		if inExt {
			ch.ExtensionAlternatives = append(ch.ExtensionAlternatives, alt)
		} else {
			ch.Alternatives = append(ch.Alternatives, alt)
		}
		if !p.accept(lexer.TOKEN_COMMA) {
			break
		}
	}
	p.expect(lexer.TOKEN_RBRACE)
	return ch
}

func (p *Parser) parseChoiceAlternative() ast.ChoiceAlternative {
	alt := ast.ChoiceAlternative{Name: p.cur.Lit}
	p.next()
	if p.cur.Kind == lexer.TOKEN_LBRACKET {
		tg := p.parseTag()
		alt.Tag = &tg
	}
	alt.Type = p.parseType()
	return alt
}

// --- Constraint parsing ---

func (p *Parser) parseConstraint() *ast.Constraint {
	p.expect(lexer.TOKEN_LPAREN)
	c := p.parseConstraintElements()
	// Check for root/extension split: ( root, ... )  or ( root, ..., additions )
	if p.accept(lexer.TOKEN_COMMA) {
		if p.accept(lexer.TOKEN_ELLIPSIS) {
			c.Extensible = true
			// additional extension operands allowed but ignored for MVP
			if p.accept(lexer.TOKEN_COMMA) {
				_ = p.parseConstraintElements()
			}
		}
	}
	p.expect(lexer.TOKEN_RPAREN)
	return c
}

func (p *Parser) parseConstraintElements() *ast.Constraint {
	// Handle UNION / INTERSECTION / parenthesized groups
	left := p.parseSingleConstraint()
	for {
		if p.accept(lexer.TOKEN_PIPE) || p.accept(lexer.TOKEN_UNION) {
			right := p.parseSingleConstraint()
			left = &ast.Constraint{Kind: ast.ConstraintUnion, Operands: []*ast.Constraint{left, right}}
			continue
		}
		if p.accept(lexer.TOKEN_CARET) || p.accept(lexer.TOKEN_INTERSECTION) {
			right := p.parseSingleConstraint()
			left = &ast.Constraint{Kind: ast.ConstraintIntersection, Operands: []*ast.Constraint{left, right}}
			continue
		}
		break
	}
	return left
}

func (p *Parser) parseSingleConstraint() *ast.Constraint {
	// SIZE(..)
	if p.accept(lexer.TOKEN_SIZE) {
		inner := p.parseConstraint()
		return &ast.Constraint{Kind: ast.ConstraintSize, Inner: inner}
	}
	if p.accept(lexer.TOKEN_CONTAINING) {
		t := p.parseType()
		return &ast.Constraint{Kind: ast.ConstraintContents, ContainedType: t}
	}
	if p.accept(lexer.TOKEN_PATTERN) {
		_ = p.parseValue()
		return &ast.Constraint{Kind: ast.ConstraintPattern}
	}

	// Object-set reference for table constraint: { ObjectSet } or { ObjectSet }{@field}
	if p.cur.Kind == lexer.TOKEN_LBRACE {
		p.next()
		c := &ast.Constraint{Kind: ast.ConstraintTable}
		// Single TypeRef inside braces = object set name
		if p.cur.Kind == lexer.TOKEN_TYPE_REF {
			c.ObjectSet = p.cur.Lit
			p.next()
		} else {
			// skip unknown content to closing brace
			depth := 1
			for depth > 0 && p.cur.Kind != lexer.TOKEN_EOF {
				if p.cur.Kind == lexer.TOKEN_LBRACE {
					depth++
				} else if p.cur.Kind == lexer.TOKEN_RBRACE {
					depth--
					if depth == 0 {
						break
					}
				}
				p.next()
			}
		}
		p.expect(lexer.TOKEN_RBRACE)
		// Optional {@field} follower
		if p.accept(lexer.TOKEN_LBRACE) {
			for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
				if p.accept(lexer.TOKEN_AT) {
					if p.cur.Kind == lexer.TOKEN_IDENTIFIER || p.cur.Kind == lexer.TOKEN_TYPE_REF {
						c.AtNotation = append(c.AtNotation, p.cur.Lit)
						p.next()
					}
				} else {
					p.next()
				}
			}
			p.expect(lexer.TOKEN_RBRACE)
		}
		return c
	}

	// MIN..MAX  or  number..number  or  valueref..valueref  or  single value
	lo, loOK := p.parseValueBound()
	if loOK && p.accept(lexer.TOKEN_RANGE) {
		hi, _ := p.parseValueBound()
		return &ast.Constraint{Kind: ast.ConstraintValue, LowerBound: lo, UpperBound: hi}
	}
	if loOK {
		return &ast.Constraint{Kind: ast.ConstraintSingleValue, LowerBound: lo}
	}
	// Fallback: skip until a closing paren/comma to avoid loop
	return &ast.Constraint{}
}

func (p *Parser) parseValueBound() (*ast.Value, bool) {
	switch p.cur.Kind {
	case lexer.TOKEN_NUMBER:
		v, _ := strconv.ParseInt(p.cur.Lit, 10, 64)
		p.next()
		var vv ast.Value = ast.IntegerValue{Int: v}
		return &vv, true
	case lexer.TOKEN_MIN:
		p.next()
		var vv ast.Value = ast.ValueRef{Name: "MIN"}
		return &vv, true
	case lexer.TOKEN_MAX:
		p.next()
		var vv ast.Value = ast.ValueRef{Name: "MAX"}
		return &vv, true
	case lexer.TOKEN_IDENTIFIER, lexer.TOKEN_TYPE_REF:
		name := p.cur.Lit
		p.next()
		var vv ast.Value = ast.ValueRef{Name: name}
		return &vv, true
	case lexer.TOKEN_CSTRING:
		s := p.cur.Lit
		p.next()
		var vv ast.Value = ast.StringValue{S: s}
		return &vv, true
	}
	return nil, false
}

// --- Value parsing ---

func (p *Parser) parseValue() ast.Value {
	switch p.cur.Kind {
	case lexer.TOKEN_NUMBER:
		v, _ := strconv.ParseInt(p.cur.Lit, 10, 64)
		p.next()
		return ast.IntegerValue{Int: v}
	case lexer.TOKEN_TRUE:
		p.next()
		return ast.BoolValue{B: true}
	case lexer.TOKEN_FALSE:
		p.next()
		return ast.BoolValue{B: false}
	case lexer.TOKEN_NULL:
		p.next()
		return ast.NullValue{}
	case lexer.TOKEN_CSTRING:
		s := p.cur.Lit
		p.next()
		return ast.StringValue{S: s}
	case lexer.TOKEN_BSTRING:
		s := p.cur.Lit
		p.next()
		return ast.BitStringValue{Bits: s}
	case lexer.TOKEN_HSTRING:
		s := p.cur.Lit
		p.next()
		return ast.BitStringValue{Hex: s}
	case lexer.TOKEN_IDENTIFIER, lexer.TOKEN_TYPE_REF:
		name := p.cur.Lit
		p.next()
		return ast.ValueRef{Name: name}
	case lexer.TOKEN_LBRACE:
		p.skipBalanced(lexer.TOKEN_LBRACE, lexer.TOKEN_RBRACE)
		return ast.NullValue{}
	}
	p.errorf("unexpected token in value: %q", p.cur.Lit)
	p.next()
	return ast.NullValue{}
}

// parseActualParameters consumes `{ arg, arg, ... }` (and the 3GPP
// `{{ ObjectSet }}` double-brace form) for parameterised-type instantiations.
// Each top-level argument is captured as a TypeReference naming the
// ObjectSet / type that was passed.
func (p *Parser) parseActualParameters() []ast.Type {
	p.expect(lexer.TOKEN_LBRACE)
	var args []ast.Type
	depth := 0 // tracks nesting of inner braces (e.g. the inner `{` of `{{X}}`)
	for p.cur.Kind != lexer.TOKEN_EOF {
		if p.cur.Kind == lexer.TOKEN_RBRACE && depth == 0 {
			break
		}
		if p.cur.Kind == lexer.TOKEN_LBRACE {
			depth++
			p.next()
			continue
		}
		if p.cur.Kind == lexer.TOKEN_RBRACE && depth > 0 {
			depth--
			p.next()
			continue
		}
		switch p.cur.Kind {
		case lexer.TOKEN_TYPE_REF, lexer.TOKEN_IDENTIFIER:
			args = append(args, ast.TypeReference{TypeName: p.cur.Lit})
			p.next()
		default:
			p.next()
		}
		if depth == 0 && !p.accept(lexer.TOKEN_COMMA) {
			// allow another TYPE_REF/IDENT immediately for `{{X}}` form
			if p.cur.Kind != lexer.TOKEN_TYPE_REF && p.cur.Kind != lexer.TOKEN_IDENTIFIER && p.cur.Kind != lexer.TOKEN_LBRACE && p.cur.Kind != lexer.TOKEN_RBRACE {
				break
			}
		}
	}
	p.expect(lexer.TOKEN_RBRACE)
	return args
}

// --- Information Object Class (X.681) ---

func (p *Parser) parseInfoObjectClass() *ast.InfoObjectClass {
	p.expect(lexer.TOKEN_CLASS)
	p.expect(lexer.TOKEN_LBRACE)
	cls := &ast.InfoObjectClass{}
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		if p.cur.Kind != lexer.TOKEN_AMPERSAND {
			p.next()
			continue
		}
		p.next() // &
		if p.cur.Kind != lexer.TOKEN_IDENTIFIER && p.cur.Kind != lexer.TOKEN_TYPE_REF {
			p.next()
			continue
		}
		name := p.cur.Lit
		isUpper := p.cur.Kind == lexer.TOKEN_TYPE_REF
		p.next()
		field := ast.ClassField{Name: "&" + name}
		switch {
		case isUpper:
			// &TypeField or &ValueSetField  (&Type alone = open type field)
			field.Kind = ast.TypeField
		default:
			// &fixedTypeValue field: &name <Type>
			field.Kind = ast.FixedTypeValueField
			field.Type = p.parseType()
		}
		if p.accept(lexer.TOKEN_UNIQUE) {
			field.Unique = true
		}
		if p.accept(lexer.TOKEN_OPTIONAL) {
			field.Optional = true
		} else if p.accept(lexer.TOKEN_DEFAULT) {
			field.Default = p.parseValue()
		}
		cls.Fields = append(cls.Fields, field)
		p.accept(lexer.TOKEN_COMMA)
	}
	p.expect(lexer.TOKEN_RBRACE)
	// Optional WITH SYNTAX block — parse literal-token → field-ref map.
	if p.accept(lexer.TOKEN_WITH) {
		// "SYNTAX" is lexed as TYPE_REF (uppercase identifier)
		if p.cur.Kind == lexer.TOKEN_TYPE_REF && p.cur.Lit == "SYNTAX" {
			p.next()
		}
		cls.Syntax = p.parseWithSyntaxBody()
	}
	return cls
}

// parseWithSyntaxBody parses the contents of WITH SYNTAX { ... } as a sequence
// of token sequences separated by & field refs. For each class field reference
// (&name), we collect the literal tokens that precede it into one entry:
//
//	[ ["ID"], ["CRITICALITY"], ["TYPE"], ["PRESENCE"] ]
//
// aligned one-to-one with class fields in declaration order. (WITH SYNTAX may
// reorder — we capture a map of literal -> field name via the Syntax slot.)
func (p *Parser) parseWithSyntaxBody() [][]string {
	p.expect(lexer.TOKEN_LBRACE)
	var pairs [][]string
	var lits []string
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		if p.cur.Kind == lexer.TOKEN_AMPERSAND {
			p.next()
			if p.cur.Kind == lexer.TOKEN_IDENTIFIER || p.cur.Kind == lexer.TOKEN_TYPE_REF {
				fieldRef := "&" + p.cur.Lit
				p.next()
				pair := append([]string(nil), lits...)
				pair = append(pair, fieldRef)
				pairs = append(pairs, pair)
				lits = nil
			}
			continue
		}
		if p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
			lits = append(lits, p.cur.Lit)
			p.next()
			continue
		}
		// Optional group [ ... ] — we skip opaquely.
		if p.accept(lexer.TOKEN_LBRACKET) {
			depth := 1
			for depth > 0 && p.cur.Kind != lexer.TOKEN_EOF {
				if p.cur.Kind == lexer.TOKEN_LBRACKET {
					depth++
				} else if p.cur.Kind == lexer.TOKEN_RBRACKET {
					depth--
				}
				p.next()
			}
			continue
		}
		p.next()
	}
	p.expect(lexer.TOKEN_RBRACE)
	return pairs
}

// parseObjectSet parses `{ { fields... } | { fields... } | ..., ... }`.
// Field values are stored positionally; the resolver binds them to class
// fields using the class WITH SYNTAX (looked up at resolve time).
func (p *Parser) parseObjectSet(className string) *ast.InfoObjectSet {
	os := &ast.InfoObjectSet{ClassName: className}
	p.expect(lexer.TOKEN_LBRACE)
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		if p.accept(lexer.TOKEN_ELLIPSIS) {
			os.Extensible = true
			p.accept(lexer.TOKEN_COMMA)
			continue
		}
		if p.cur.Kind == lexer.TOKEN_LBRACE {
			obj := p.parseObjectBody()
			os.Objects = append(os.Objects, obj)
			p.accept(lexer.TOKEN_PIPE)
			p.accept(lexer.TOKEN_COMMA)
			continue
		}
		// Bare reference to another object set (union-of-sets form,
		// e.g. NGAP-ELEMENTARY-PROCEDURES ::= { SetA | SetB, ... }). We
		// record as a reference-only object so the resolver can merge if
		// it wants to; at minimum we consume the tokens to make progress.
		if p.cur.Kind == lexer.TOKEN_TYPE_REF || p.cur.Kind == lexer.TOKEN_IDENTIFIER {
			ref := p.cur.Lit
			p.next()
			obj := ast.InfoObject{Fields: map[string]ast.ObjectField{"_ref": {ObjectRef: ref}}}
			os.Objects = append(os.Objects, obj)
			p.accept(lexer.TOKEN_PIPE)
			p.accept(lexer.TOKEN_COMMA)
			continue
		}
		// Unknown token at the top level — consume to avoid infinite loops.
		p.next()
	}
	p.expect(lexer.TOKEN_RBRACE)
	return os
}

// parseObjectBody parses `{ LITERAL value LITERAL value ... }`. Literals are
// uppercase keywords from the class's WITH SYNTAX (e.g. ID, CRITICALITY, TYPE,
// PRESENCE). Values follow each literal — either a value-ref (identifier) or
// a type-ref. We record them keyed by the literal token; the resolver
// translates literal→field name via the owning class.
func (p *Parser) parseObjectBody() ast.InfoObject {
	obj := ast.InfoObject{Fields: make(map[string]ast.ObjectField)}
	p.expect(lexer.TOKEN_LBRACE)
	for p.cur.Kind != lexer.TOKEN_RBRACE && p.cur.Kind != lexer.TOKEN_EOF {
		// literal label
		if p.cur.Kind != lexer.TOKEN_TYPE_REF && p.cur.Kind != lexer.TOKEN_IDENTIFIER {
			p.next()
			continue
		}
		label := p.cur.Lit
		p.next()
		// value: type reference, value reference, or literal
		var f ast.ObjectField
		switch p.cur.Kind {
		case lexer.TOKEN_TYPE_REF:
			// Could be a type (e.g. GlobalRANNodeID) OR a literal ref to an object
			// or value. For MVP we record as a TypeReference always; the resolver
			// decides based on the class field kind.
			f.Type = ast.TypeReference{TypeName: p.cur.Lit}
			f.ObjectRef = p.cur.Lit
			p.next()
		case lexer.TOKEN_IDENTIFIER:
			f.ObjectRef = p.cur.Lit
			f.Value = ast.ValueRef{Name: p.cur.Lit}
			p.next()
		case lexer.TOKEN_NUMBER:
			v := p.parseValue()
			f.Value = v
		default:
			p.next()
		}
		obj.Fields[label] = f
	}
	p.expect(lexer.TOKEN_RBRACE)
	return obj
}
