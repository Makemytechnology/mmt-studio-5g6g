// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
// Package lexer tokenises ASN.1 source text per ITU-T X.680.
package lexer

import (
	"fmt"
	"unicode"
)

type Lexer struct {
	src    []rune
	pos    int
	line   int
	col    int
	errors []string
}

func New(src string) *Lexer {
	return &Lexer{src: []rune(src), line: 1, col: 1}
}

func (l *Lexer) Errors() []string { return l.errors }

func (l *Lexer) errf(format string, a ...any) {
	l.errors = append(l.errors, fmt.Sprintf("lexer %d:%d: %s", l.line, l.col, fmt.Sprintf(format, a...)))
}

func (l *Lexer) peek(offset int) rune {
	p := l.pos + offset
	if p >= len(l.src) {
		return 0
	}
	return l.src[p]
}

func (l *Lexer) advance() rune {
	if l.pos >= len(l.src) {
		return 0
	}
	r := l.src[l.pos]
	l.pos++
	if r == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return r
}

// Next returns the next token, skipping whitespace and comments.
func (l *Lexer) Next() Token {
	for {
		l.skipWhitespace()
		// line comment --
		if l.peek(0) == '-' && l.peek(1) == '-' {
			l.skipLineComment()
			continue
		}
		// block comment /* */
		if l.peek(0) == '/' && l.peek(1) == '*' {
			l.skipBlockComment()
			continue
		}
		break
	}

	startLine, startCol := l.line, l.col

	if l.pos >= len(l.src) {
		return Token{Kind: TOKEN_EOF, Line: startLine, Column: startCol}
	}

	r := l.peek(0)

	// Identifiers / keywords (ASN.1 allows hyphens inside identifiers)
	if isLetter(r) {
		return l.lexIdentifier(startLine, startCol)
	}

	// Numbers
	if isDigit(r) {
		return l.lexNumber(startLine, startCol)
	}

	// Strings
	switch r {
	case '"':
		return l.lexCString(startLine, startCol)
	case '\'':
		return l.lexBHString(startLine, startCol)
	}

	// Symbols / multi-char operators
	switch r {
	case ':':
		if l.peek(1) == ':' && l.peek(2) == '=' {
			l.advance()
			l.advance()
			l.advance()
			return Token{Kind: TOKEN_ASSIGN, Lit: "::=", Line: startLine, Column: startCol}
		}
		l.advance()
		return Token{Kind: TOKEN_COLON, Lit: ":", Line: startLine, Column: startCol}
	case '.':
		if l.peek(1) == '.' && l.peek(2) == '.' {
			l.advance()
			l.advance()
			l.advance()
			return Token{Kind: TOKEN_ELLIPSIS, Lit: "...", Line: startLine, Column: startCol}
		}
		if l.peek(1) == '.' {
			l.advance()
			l.advance()
			return Token{Kind: TOKEN_RANGE, Lit: "..", Line: startLine, Column: startCol}
		}
		l.advance()
		return Token{Kind: TOKEN_DOT, Lit: ".", Line: startLine, Column: startCol}
	case '[':
		if l.peek(1) == '[' {
			l.advance()
			l.advance()
			return Token{Kind: TOKEN_DBL_LBRACK, Lit: "[[", Line: startLine, Column: startCol}
		}
		l.advance()
		return Token{Kind: TOKEN_LBRACKET, Lit: "[", Line: startLine, Column: startCol}
	case ']':
		if l.peek(1) == ']' {
			l.advance()
			l.advance()
			return Token{Kind: TOKEN_DBL_RBRACK, Lit: "]]", Line: startLine, Column: startCol}
		}
		l.advance()
		return Token{Kind: TOKEN_RBRACKET, Lit: "]", Line: startLine, Column: startCol}
	case '{':
		l.advance()
		return Token{Kind: TOKEN_LBRACE, Lit: "{", Line: startLine, Column: startCol}
	case '}':
		l.advance()
		return Token{Kind: TOKEN_RBRACE, Lit: "}", Line: startLine, Column: startCol}
	case '(':
		l.advance()
		return Token{Kind: TOKEN_LPAREN, Lit: "(", Line: startLine, Column: startCol}
	case ')':
		l.advance()
		return Token{Kind: TOKEN_RPAREN, Lit: ")", Line: startLine, Column: startCol}
	case ',':
		l.advance()
		return Token{Kind: TOKEN_COMMA, Lit: ",", Line: startLine, Column: startCol}
	case ';':
		l.advance()
		return Token{Kind: TOKEN_SEMICOLON, Lit: ";", Line: startLine, Column: startCol}
	case '|':
		l.advance()
		return Token{Kind: TOKEN_PIPE, Lit: "|", Line: startLine, Column: startCol}
	case '@':
		l.advance()
		return Token{Kind: TOKEN_AT, Lit: "@", Line: startLine, Column: startCol}
	case '!':
		l.advance()
		return Token{Kind: TOKEN_EXCLAMATION, Lit: "!", Line: startLine, Column: startCol}
	case '^':
		l.advance()
		return Token{Kind: TOKEN_CARET, Lit: "^", Line: startLine, Column: startCol}
	case '<':
		l.advance()
		return Token{Kind: TOKEN_LESS, Lit: "<", Line: startLine, Column: startCol}
	case '>':
		l.advance()
		return Token{Kind: TOKEN_GREATER, Lit: ">", Line: startLine, Column: startCol}
	case '&':
		l.advance()
		return Token{Kind: TOKEN_AMPERSAND, Lit: "&", Line: startLine, Column: startCol}
	case '-':
		// negative number? only in value contexts — but lex as negative NUMBER if followed by digit.
		if isDigit(l.peek(1)) {
			l.advance() // consume '-'
			tok := l.lexNumber(startLine, startCol)
			tok.Lit = "-" + tok.Lit
			return tok
		}
		l.advance()
		return Token{Kind: TOKEN_ILLEGAL, Lit: "-", Line: startLine, Column: startCol}
	}

	l.errf("unexpected character %q", r)
	l.advance()
	return Token{Kind: TOKEN_ILLEGAL, Lit: string(r), Line: startLine, Column: startCol}
}

func (l *Lexer) skipWhitespace() {
	for l.pos < len(l.src) {
		r := l.src[l.pos]
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			l.advance()
			continue
		}
		break
	}
}

func (l *Lexer) skipLineComment() {
	// consume opening --
	l.advance()
	l.advance()
	for l.pos < len(l.src) {
		// ASN.1 line comments end at newline OR at another --
		if l.peek(0) == '\n' {
			return
		}
		if l.peek(0) == '-' && l.peek(1) == '-' {
			l.advance()
			l.advance()
			return
		}
		l.advance()
	}
}

func (l *Lexer) skipBlockComment() {
	l.advance() // /
	l.advance() // *
	depth := 1
	for l.pos < len(l.src) && depth > 0 {
		if l.peek(0) == '/' && l.peek(1) == '*' {
			l.advance()
			l.advance()
			depth++
			continue
		}
		if l.peek(0) == '*' && l.peek(1) == '/' {
			l.advance()
			l.advance()
			depth--
			continue
		}
		l.advance()
	}
}

func (l *Lexer) lexIdentifier(startLine, startCol int) Token {
	start := l.pos
	// first char: letter
	l.advance()
	for l.pos < len(l.src) {
		r := l.peek(0)
		if isLetter(r) || isDigit(r) {
			l.advance()
			continue
		}
		// hyphen allowed only between alphanumerics, not at end or doubled
		if r == '-' && (isLetter(l.peek(1)) || isDigit(l.peek(1))) {
			l.advance()
			continue
		}
		break
	}
	lit := string(l.src[start:l.pos])
	if k, ok := LookupKeyword(lit); ok {
		return Token{Kind: k, Lit: lit, Line: startLine, Column: startCol}
	}
	// Convention: type refs start with an upper-case letter, value refs with lower.
	if unicode.IsUpper(rune(lit[0])) {
		return Token{Kind: TOKEN_TYPE_REF, Lit: lit, Line: startLine, Column: startCol}
	}
	return Token{Kind: TOKEN_IDENTIFIER, Lit: lit, Line: startLine, Column: startCol}
}

func (l *Lexer) lexNumber(startLine, startCol int) Token {
	start := l.pos
	for l.pos < len(l.src) && isDigit(l.peek(0)) {
		l.advance()
	}
	// real?
	if l.peek(0) == '.' && isDigit(l.peek(1)) {
		l.advance()
		for l.pos < len(l.src) && isDigit(l.peek(0)) {
			l.advance()
		}
		if l.peek(0) == 'e' || l.peek(0) == 'E' {
			l.advance()
			if l.peek(0) == '+' || l.peek(0) == '-' {
				l.advance()
			}
			for l.pos < len(l.src) && isDigit(l.peek(0)) {
				l.advance()
			}
		}
		return Token{Kind: TOKEN_REAL_NUMBER, Lit: string(l.src[start:l.pos]), Line: startLine, Column: startCol}
	}
	return Token{Kind: TOKEN_NUMBER, Lit: string(l.src[start:l.pos]), Line: startLine, Column: startCol}
}

func (l *Lexer) lexCString(startLine, startCol int) Token {
	l.advance() // consume opening "
	start := l.pos
	for l.pos < len(l.src) {
		if l.peek(0) == '"' {
			// doubled "" is an escaped quote within the string
			if l.peek(1) == '"' {
				l.advance()
				l.advance()
				continue
			}
			lit := string(l.src[start:l.pos])
			l.advance()
			return Token{Kind: TOKEN_CSTRING, Lit: lit, Line: startLine, Column: startCol}
		}
		l.advance()
	}
	l.errf("unterminated cstring")
	return Token{Kind: TOKEN_ILLEGAL, Lit: string(l.src[start:l.pos]), Line: startLine, Column: startCol}
}

// lexBHString handles '0110'B (bstring) and 'A0FF'H (hstring)
func (l *Lexer) lexBHString(startLine, startCol int) Token {
	l.advance() // '
	start := l.pos
	for l.pos < len(l.src) && l.peek(0) != '\'' {
		l.advance()
	}
	if l.peek(0) != '\'' {
		l.errf("unterminated bstring/hstring")
		return Token{Kind: TOKEN_ILLEGAL, Lit: string(l.src[start:l.pos]), Line: startLine, Column: startCol}
	}
	body := string(l.src[start:l.pos])
	l.advance() // closing '
	suffix := l.peek(0)
	switch suffix {
	case 'B':
		l.advance()
		return Token{Kind: TOKEN_BSTRING, Lit: body, Line: startLine, Column: startCol}
	case 'H':
		l.advance()
		return Token{Kind: TOKEN_HSTRING, Lit: body, Line: startLine, Column: startCol}
	}
	l.errf("bstring/hstring missing B/H suffix")
	return Token{Kind: TOKEN_ILLEGAL, Lit: body, Line: startLine, Column: startCol}
}

func isLetter(r rune) bool { return unicode.IsLetter(r) || r == '_' }
func isDigit(r rune) bool  { return r >= '0' && r <= '9' }

// Tokenize is a convenience function that runs the lexer to completion.
func Tokenize(src string) ([]Token, []string) {
	l := New(src)
	var toks []Token
	for {
		t := l.Next()
		toks = append(toks, t)
		if t.Kind == TOKEN_EOF {
			break
		}
	}
	return toks, l.Errors()
}
