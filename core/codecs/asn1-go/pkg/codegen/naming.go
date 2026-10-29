// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package codegen

import (
	"strings"
	"unicode"
)

// GoName converts an ASN.1 identifier to an exported Go identifier.
//
//	"NGAP-PDU"           -> "NGAPPDU"
//	"procedureCode"      -> "ProcedureCode"
//	"id-GlobalRANNodeID" -> "IdGlobalRANNodeID"
//	"maxProtocolIEs"     -> "MaxProtocolIEs"
func GoName(asn1 string) string {
	if asn1 == "" {
		return ""
	}
	var b strings.Builder
	capitalizeNext := true
	for _, r := range asn1 {
		if r == '-' || r == '_' {
			capitalizeNext = true
			continue
		}
		if capitalizeNext {
			b.WriteRune(unicode.ToUpper(r))
			capitalizeNext = false
		} else {
			b.WriteRune(r)
		}
	}
	name := b.String()
	if reservedGo[name] {
		return name + "_"
	}
	return name
}

// GoFieldName returns a Go field name from an ASN.1 component name.
func GoFieldName(asn1 string) string { return GoName(asn1) }

var reservedGo = map[string]bool{
	"break": true, "default": true, "func": true, "interface": true, "select": true,
	"case": true, "defer": true, "go": true, "map": true, "struct": true,
	"chan": true, "else": true, "goto": true, "package": true, "switch": true,
	"const": true, "fallthrough": true, "if": true, "range": true, "type": true,
	"continue": true, "for": true, "import": true, "return": true, "var": true,
}
