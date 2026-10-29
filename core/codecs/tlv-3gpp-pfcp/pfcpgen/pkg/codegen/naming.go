// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"strings"
	"unicode"
)

func GoName(s string) string {
	var b strings.Builder
	upperNext := true
	for _, r := range s {
		switch {
		case r == '_' || r == '-' || r == ' ' || r == '.' || r == '/':
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

func FileSafe(s string) string {
	out := make([]rune, 0, len(s))
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_':
			out = append(out, r)
		case r >= 'A' && r <= 'Z':
			out = append(out, r+32)
		default:
			out = append(out, '_')
		}
	}
	return string(out)
}
