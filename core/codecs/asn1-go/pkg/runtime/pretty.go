// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// PrettyJSON renders a decoded ASN.1 value as human-readable JSON, hiding
// the noise that the generated Go shapes carry for codec convenience:
//
//   - CHOICE structs (`{Present int; AltA *A; AltB *B; ...}`) collapse to
//     `{"<selected-alt-name>": <value>}` instead of dumping every nil pointer.
//   - Fields tagged `aper:"skip"` (e.g. the Entry's `Presence` field) are
//     omitted because they aren't on the wire.
//   - Nil pointers and nil slices are omitted entirely.
//   - `[]byte` is rendered as a lower-case hex string for log readability.
//   - `BitString` becomes `"<hex>/<bitlength>"`.
//
// Pass any generated value (typically a top-level message). The output is
// stable enough to diff against captured fixtures in tests.
func PrettyJSON(v any) ([]byte, error) {
	return json.MarshalIndent(toPretty(reflect.ValueOf(v)), "", "  ")
}

// MustPrettyJSON panics on encode failure. Convenient for log statements
// and debug `fmt.Println` calls where the failure mode is "programmer error".
func MustPrettyJSON(v any) string {
	b, err := PrettyJSON(v)
	if err != nil {
		return fmt.Sprintf("<PrettyJSON error: %v>", err)
	}
	return string(b)
}

// stringer is implemented by every generated ENUMERATED type. We use it via
// PrettyJSON to render named values rather than raw integers.
type stringer interface{ String() string }

func toPretty(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}
	// Named integer types that have a String() method (typically enums) —
	// render as the named value. Limit to int64 underlying so we don't
	// accidentally call String() on Go's `time.Duration`-style aliases.
	if (rv.Kind() == reflect.Int64 || rv.Kind() == reflect.Int) && rv.Type().Name() != "" && rv.Type().Name() != "int" && rv.Type().Name() != "int64" {
		if s, ok := rv.Interface().(stringer); ok {
			return s.String()
		}
	}
	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return toPretty(rv.Elem())

	case reflect.Struct:
		// BitString — render compactly.
		if rv.Type().Name() == "BitString" && rv.Type().PkgPath() != "" {
			bs := rv.Interface().(BitString)
			return fmt.Sprintf("%x/%dbits", bs.Bytes, bs.BitLength)
		}
		// CHOICE detection: a Present:int field plus pointer fields.
		if pf := rv.FieldByName("Present"); pf.IsValid() && pf.Kind() == reflect.Int {
			present := int(pf.Int())
			if present <= 0 {
				return nil
			}
			rt := rv.Type()
			altIdx := 0
			for i := 0; i < rt.NumField(); i++ {
				if rt.Field(i).Name == "Present" {
					continue
				}
				altIdx++
				if altIdx == present {
					return map[string]any{rt.Field(i).Name: toPretty(rv.Field(i))}
				}
			}
			return nil
		}
		// Regular SEQUENCE.
		out := map[string]any{}
		rt := rv.Type()
		for i := 0; i < rt.NumField(); i++ {
			sf := rt.Field(i)
			if !sf.IsExported() {
				continue
			}
			tag := sf.Tag.Get("aper")
			if hasTagToken(tag, "skip") {
				continue
			}
			f := rv.Field(i)
			if (f.Kind() == reflect.Pointer || f.Kind() == reflect.Slice || f.Kind() == reflect.Map) && f.IsNil() {
				continue
			}
			out[sf.Name] = toPretty(f)
		}
		return out

	case reflect.Slice:
		// []byte → hex.
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return fmt.Sprintf("%x", rv.Bytes())
		}
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = toPretty(rv.Index(i))
		}
		return out

	case reflect.Array:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b := make([]byte, rv.Len())
			reflect.Copy(reflect.ValueOf(b), rv)
			return fmt.Sprintf("%x", b)
		}
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = toPretty(rv.Index(i))
		}
		return out

	case reflect.String:
		return rv.String()
	case reflect.Bool:
		return rv.Bool()
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return rv.Int()
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return rv.Uint()
	case reflect.Float32, reflect.Float64:
		return rv.Float()
	}
	return nil
}

func hasTagToken(tag, want string) bool {
	for _, p := range strings.Split(tag, ",") {
		if strings.TrimSpace(p) == want {
			return true
		}
	}
	return false
}
