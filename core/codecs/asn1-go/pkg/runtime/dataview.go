// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"encoding/json"
	"fmt"
	"reflect"
)

// DataJSON renders a decoded value as a Wireshark-like hierarchy of just the
// payload fields, dropping codec scaffolding:
//
//   - Container wrappers like `ProtocolIEs []FooEntry` collapse — the slice
//     becomes a flat `{<AltName>: <Value>, ...}` map keyed by alternative name.
//   - Entry structs (`Id`/`Criticality`/`Value`) inline as just their Value
//     alternative.
//   - CHOICE alternatives unwrap to `{<altName>: <data>}` like PrettyJSON.
//   - Enums render as their named ASN.1 value (via String()).
//
// If two entries in the same container select the same alternative, the
// collapsed form falls back to a JSON array (so no information is lost).
func DataJSON(v any) ([]byte, error) {
	return json.MarshalIndent(toData(reflect.ValueOf(v)), "", "  ")
}

// MustDataJSON is the panic-free form for log/debug use.
func MustDataJSON(v any) string {
	b, err := DataJSON(v)
	if err != nil {
		return fmt.Sprintf("<DataJSON error: %v>", err)
	}
	return string(b)
}

// DecodeAPERToData unmarshals APER bytes into receiver and returns the
// Wireshark-style data view as JSON.
func DecodeAPERToData(b []byte, receiver APERDecodable) (string, error) {
	if err := receiver.UnmarshalAPER(b); err != nil {
		return "", fmt.Errorf("decode APER: %w", err)
	}
	return MustDataJSON(receiver), nil
}

// DecodeUPERToData is the UPER twin of DecodeAPERToData.
func DecodeUPERToData(b []byte, receiver UPERDecodable) (string, error) {
	if err := receiver.UnmarshalUPER(b); err != nil {
		return "", fmt.Errorf("decode UPER: %w", err)
	}
	return MustDataJSON(receiver), nil
}

func toData(rv reflect.Value) any {
	if !rv.IsValid() {
		return nil
	}

	// Named integer types with a String() method (enums) → string name.
	if (rv.Kind() == reflect.Int64 || rv.Kind() == reflect.Int) &&
		rv.Type().Name() != "" && rv.Type().Name() != "int" && rv.Type().Name() != "int64" {
		if s, ok := rv.Interface().(stringer); ok {
			return s.String()
		}
	}

	switch rv.Kind() {
	case reflect.Pointer, reflect.Interface:
		if rv.IsNil() {
			return nil
		}
		return toData(rv.Elem())

	case reflect.Struct:
		// BitString — compact rendering.
		if rv.Type().Name() == "BitString" && rv.Type().PkgPath() != "" {
			bs := rv.Interface().(BitString)
			return fmt.Sprintf("%x/%dbits", bs.Bytes, bs.BitLength)
		}
		// CHOICE: collapse to {altName: altData}
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
					return map[string]any{rt.Field(i).Name: toData(rv.Field(i))}
				}
			}
			return nil
		}
		// Entry detection: struct with a Value field tagged openType — inline Value.
		if isEntryStruct(rv.Type()) {
			vf := rv.FieldByName("Value")
			return toData(vf)
		}
		// Regular SEQUENCE — drop codec-only fields.
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
			out[sf.Name] = toData(f)
		}
		// Container-only struct: a single field whose type is an Entry slice
		// (the "ProtocolIEs" wrapper produced by the codegen). Inline it so
		// the data view is one flat hierarchy.
		if len(out) == 1 && rt.NumField() == 1 {
			f0 := rv.Field(0)
			if f0.Kind() == reflect.Slice && isEntrySlice(f0) {
				for _, v := range out {
					return v
				}
			}
		}
		return out

	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return fmt.Sprintf("%x", rv.Bytes())
		}
		// If every element is an Entry-shaped struct, try to collapse the
		// slice into a single map keyed by alternative names. Fall back to
		// an ordered list if any key would collide (preserves all data).
		if isEntrySlice(rv) {
			merged, ok := mergeEntries(rv)
			if ok {
				return merged
			}
		}
		n := rv.Len()
		out := make([]any, n)
		for i := 0; i < n; i++ {
			out[i] = toData(rv.Index(i))
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
			out[i] = toData(rv.Index(i))
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

// isEntryStruct: a struct with a Value field tagged openType is the IE-entry
// shape produced by the codegen (Id / Criticality / Value / Presence).
func isEntryStruct(rt reflect.Type) bool {
	if rt.Kind() != reflect.Struct {
		return false
	}
	vf, ok := rt.FieldByName("Value")
	if !ok {
		return false
	}
	return hasTagToken(vf.Tag.Get("aper"), "openType")
}

func isEntrySlice(rv reflect.Value) bool {
	t := rv.Type().Elem()
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return isEntryStruct(t)
}

// mergeEntries collapses a slice of Entry structs into one map keyed by the
// CHOICE alternative name. Returns ok=false if two entries pick the same
// alternative (caller falls back to array form).
func mergeEntries(rv reflect.Value) (map[string]any, bool) {
	out := map[string]any{}
	for i := 0; i < rv.Len(); i++ {
		entry := toData(rv.Index(i))
		m, ok := entry.(map[string]any)
		if !ok || len(m) != 1 {
			return nil, false
		}
		for k, v := range m {
			if _, dup := out[k]; dup {
				return nil, false
			}
			out[k] = v
		}
	}
	return out, true
}
