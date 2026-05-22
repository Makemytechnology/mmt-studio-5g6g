// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package lexer

import "fmt"

type TokenKind int

const (
	TOKEN_ILLEGAL TokenKind = iota
	TOKEN_EOF
	TOKEN_COMMENT

	// Literals
	TOKEN_NUMBER
	TOKEN_REAL_NUMBER
	TOKEN_BSTRING
	TOKEN_HSTRING
	TOKEN_CSTRING

	// Identifiers
	TOKEN_TYPE_REF   // Upper-case start: MyType, NGAP-PDU
	TOKEN_IDENTIFIER // lower-case start: procedureCode, value

	// Keywords
	TOKEN_DEFINITIONS
	TOKEN_BEGIN
	TOKEN_END
	TOKEN_IMPORTS
	TOKEN_EXPORTS
	TOKEN_FROM
	TOKEN_ALL
	TOKEN_SEQUENCE
	TOKEN_SET
	TOKEN_OF
	TOKEN_CHOICE
	TOKEN_ENUMERATED
	TOKEN_INTEGER
	TOKEN_BOOLEAN
	TOKEN_BIT
	TOKEN_OCTET
	TOKEN_STRING
	TOKEN_NULL
	TOKEN_REAL
	TOKEN_OBJECT
	TOKEN_IDENTIFIER_KW // IDENTIFIER keyword (OBJECT IDENTIFIER)
	TOKEN_OPTIONAL
	TOKEN_DEFAULT
	TOKEN_CONTAINING
	TOKEN_SIZE
	TOKEN_WITH
	TOKEN_COMPONENT
	TOKEN_COMPONENTS
	TOKEN_AUTOMATIC
	TOKEN_TAGS
	TOKEN_IMPLICIT
	TOKEN_EXPLICIT
	TOKEN_UNIQUE
	TOKEN_CLASS
	TOKEN_CONSTRAINED
	TOKEN_BY
	TOKEN_PATTERN
	TOKEN_ABSTRACT_SYNTAX
	TOKEN_TYPE_IDENTIFIER
	TOKEN_TRUE
	TOKEN_FALSE
	TOKEN_EXTENSIBILITY
	TOKEN_IMPLIED
	TOKEN_PRINTABLE_STRING
	TOKEN_UTF8_STRING
	TOKEN_IA5_STRING
	TOKEN_VISIBLE_STRING
	TOKEN_NUMERIC_STRING
	TOKEN_BMPSTRING
	TOKEN_UNIVERSAL_STRING
	TOKEN_GENERAL_STRING
	TOKEN_TELETEX_STRING
	TOKEN_GRAPHIC_STRING
	TOKEN_VIDEOTEX_STRING
	TOKEN_UTCTIME
	TOKEN_GENERALIZED_TIME
	TOKEN_MIN
	TOKEN_MAX
	TOKEN_INCLUDES
	TOKEN_EXCEPT
	TOKEN_PRESENT
	TOKEN_ABSENT
	TOKEN_UNION
	TOKEN_INTERSECTION
	TOKEN_PLUS_INFINITY
	TOKEN_MINUS_INFINITY

	// Symbols
	TOKEN_ASSIGN      // ::=
	TOKEN_RANGE       // ..
	TOKEN_ELLIPSIS    // ...
	TOKEN_LBRACE      // {
	TOKEN_RBRACE      // }
	TOKEN_LPAREN      // (
	TOKEN_RPAREN      // )
	TOKEN_LBRACKET    // [
	TOKEN_RBRACKET    // ]
	TOKEN_DBL_LBRACK  // [[
	TOKEN_DBL_RBRACK  // ]]
	TOKEN_COMMA       // ,
	TOKEN_SEMICOLON   // ;
	TOKEN_COLON       // :
	TOKEN_PIPE        // |
	TOKEN_AT          // @
	TOKEN_EXCLAMATION // !
	TOKEN_CARET       // ^
	TOKEN_LESS        // <
	TOKEN_GREATER     // >
	TOKEN_DOT         // .
	TOKEN_AMPERSAND   // &
)

var keywords = map[string]TokenKind{
	"DEFINITIONS":       TOKEN_DEFINITIONS,
	"BEGIN":             TOKEN_BEGIN,
	"END":               TOKEN_END,
	"IMPORTS":           TOKEN_IMPORTS,
	"EXPORTS":           TOKEN_EXPORTS,
	"FROM":              TOKEN_FROM,
	"ALL":               TOKEN_ALL,
	"SEQUENCE":          TOKEN_SEQUENCE,
	"SET":               TOKEN_SET,
	"OF":                TOKEN_OF,
	"CHOICE":            TOKEN_CHOICE,
	"ENUMERATED":        TOKEN_ENUMERATED,
	"INTEGER":           TOKEN_INTEGER,
	"BOOLEAN":           TOKEN_BOOLEAN,
	"BIT":               TOKEN_BIT,
	"OCTET":             TOKEN_OCTET,
	"STRING":            TOKEN_STRING,
	"NULL":              TOKEN_NULL,
	"REAL":              TOKEN_REAL,
	"OBJECT":            TOKEN_OBJECT,
	"IDENTIFIER":        TOKEN_IDENTIFIER_KW,
	"OPTIONAL":          TOKEN_OPTIONAL,
	"DEFAULT":           TOKEN_DEFAULT,
	"CONTAINING":        TOKEN_CONTAINING,
	"SIZE":              TOKEN_SIZE,
	"WITH":              TOKEN_WITH,
	"COMPONENT":         TOKEN_COMPONENT,
	"COMPONENTS":        TOKEN_COMPONENTS,
	"AUTOMATIC":         TOKEN_AUTOMATIC,
	"TAGS":              TOKEN_TAGS,
	"IMPLICIT":          TOKEN_IMPLICIT,
	"EXPLICIT":          TOKEN_EXPLICIT,
	"UNIQUE":            TOKEN_UNIQUE,
	"CLASS":             TOKEN_CLASS,
	"CONSTRAINED":       TOKEN_CONSTRAINED,
	"BY":                TOKEN_BY,
	"PATTERN":           TOKEN_PATTERN,
	"ABSTRACT-SYNTAX":   TOKEN_ABSTRACT_SYNTAX,
	"TYPE-IDENTIFIER":   TOKEN_TYPE_IDENTIFIER,
	"TRUE":              TOKEN_TRUE,
	"FALSE":             TOKEN_FALSE,
	"EXTENSIBILITY":     TOKEN_EXTENSIBILITY,
	"IMPLIED":           TOKEN_IMPLIED,
	"PrintableString":   TOKEN_PRINTABLE_STRING,
	"UTF8String":        TOKEN_UTF8_STRING,
	"IA5String":         TOKEN_IA5_STRING,
	"VisibleString":     TOKEN_VISIBLE_STRING,
	"NumericString":     TOKEN_NUMERIC_STRING,
	"BMPString":         TOKEN_BMPSTRING,
	"UniversalString":   TOKEN_UNIVERSAL_STRING,
	"GeneralString":     TOKEN_GENERAL_STRING,
	"TeletexString":     TOKEN_TELETEX_STRING,
	"GraphicString":     TOKEN_GRAPHIC_STRING,
	"VideotexString":    TOKEN_VIDEOTEX_STRING,
	"UTCTime":           TOKEN_UTCTIME,
	"GeneralizedTime":   TOKEN_GENERALIZED_TIME,
	"MIN":               TOKEN_MIN,
	"MAX":               TOKEN_MAX,
	"INCLUDES":          TOKEN_INCLUDES,
	"EXCEPT":            TOKEN_EXCEPT,
	"PRESENT":           TOKEN_PRESENT,
	"ABSENT":            TOKEN_ABSENT,
	"UNION":             TOKEN_UNION,
	"INTERSECTION":      TOKEN_INTERSECTION,
	"PLUS-INFINITY":     TOKEN_PLUS_INFINITY,
	"MINUS-INFINITY":    TOKEN_MINUS_INFINITY,
}

// LookupKeyword returns the token kind for an identifier, or TOKEN_ILLEGAL if not a keyword.
// ASN.1 keywords are all-uppercase (by convention) — case sensitive.
func LookupKeyword(lit string) (TokenKind, bool) {
	k, ok := keywords[lit]
	return k, ok
}

type Token struct {
	Kind   TokenKind
	Lit    string
	Line   int
	Column int
}

func (t Token) String() string {
	return fmt.Sprintf("%s(%q)@%d:%d", tokenName(t.Kind), t.Lit, t.Line, t.Column)
}

func tokenName(k TokenKind) string {
	switch k {
	case TOKEN_EOF:
		return "EOF"
	case TOKEN_NUMBER:
		return "NUMBER"
	case TOKEN_REAL_NUMBER:
		return "REAL"
	case TOKEN_BSTRING:
		return "BSTRING"
	case TOKEN_HSTRING:
		return "HSTRING"
	case TOKEN_CSTRING:
		return "CSTRING"
	case TOKEN_TYPE_REF:
		return "TYPE_REF"
	case TOKEN_IDENTIFIER:
		return "IDENT"
	case TOKEN_ASSIGN:
		return "::="
	case TOKEN_RANGE:
		return ".."
	case TOKEN_ELLIPSIS:
		return "..."
	case TOKEN_LBRACE:
		return "{"
	case TOKEN_RBRACE:
		return "}"
	case TOKEN_LPAREN:
		return "("
	case TOKEN_RPAREN:
		return ")"
	case TOKEN_LBRACKET:
		return "["
	case TOKEN_RBRACKET:
		return "]"
	case TOKEN_DBL_LBRACK:
		return "[["
	case TOKEN_DBL_RBRACK:
		return "]]"
	case TOKEN_COMMA:
		return ","
	case TOKEN_SEMICOLON:
		return ";"
	case TOKEN_COLON:
		return ":"
	case TOKEN_PIPE:
		return "|"
	case TOKEN_AT:
		return "@"
	case TOKEN_DOT:
		return "."
	case TOKEN_AMPERSAND:
		return "&"
	case TOKEN_ILLEGAL:
		return "ILLEGAL"
	case TOKEN_COMMENT:
		return "COMMENT"
	}
	return fmt.Sprintf("KW_%d", int(k))
}
