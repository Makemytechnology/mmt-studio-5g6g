// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"strings"
	"unicode"
)

// GoName converts schema identifiers (already mostly CamelCase in YAML) to safe
// Go identifiers. Leading digit → prefixed with "F". Non-alphanumerics stripped.
func GoName(s string) string {
	var b strings.Builder
	upperNext := true
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == ' ':
			upperNext = true
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			if upperNext {
				b.WriteRune(unicode.ToUpper(r))
			} else {
				b.WriteRune(r)
			}
			upperNext = false
		}
	}
	out := b.String()
	if out == "" {
		return "X"
	}
	if unicode.IsDigit(rune(out[0])) {
		out = "F" + out
	}
	return out
}

// ConstName returns an exported constant name combining type name + value name.
// e.g. ("RegistrationType5GS", "initial_registration") → "RegistrationType5GSInitialRegistration".
func ConstName(typ, valName string) string {
	return GoName(typ) + GoName(valName)
}
