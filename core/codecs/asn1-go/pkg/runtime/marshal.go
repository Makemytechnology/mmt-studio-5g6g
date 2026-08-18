// Copyright (c) 2026 MakeMyTechnology. All rights reserved.
//
package runtime

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
)

// fieldMeta is parsed from `aper:"..."` struct tags.
type fieldMeta struct {
	ValueLB, ValueUB int64
	HasValueLB       bool
	HasValueUB       bool
	SizeLB, SizeUB   uint64
	HasSizeLB        bool
	HasSizeUB        bool
	ValueExt         bool
	SizeExt          bool
	Optional         bool
	IsExt            bool
	OpenType         bool
	Skip             bool
	DirectChoice     bool
}

func parseMeta(tag string) fieldMeta {
	var m fieldMeta
	for _, part := range strings.Split(tag, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if kv := strings.SplitN(part, ":", 2); len(kv) == 2 {
			switch kv[0] {
			case "valueLB":
				v, _ := strconv.ParseInt(kv[1], 10, 64)
				m.ValueLB = v
				m.HasValueLB = true
			case "valueUB":
				v, _ := strconv.ParseInt(kv[1], 10, 64)
				m.ValueUB = v
				m.HasValueUB = true
			case "sizeLB":
				v, _ := strconv.ParseUint(kv[1], 10, 64)
				m.SizeLB = v
				m.HasSizeLB = true
			case "sizeUB":
				v, _ := strconv.ParseUint(kv[1], 10, 64)
				m.SizeUB = v
				m.HasSizeUB = true
			}
			continue
		}
		switch part {
		case "valueExt":
			m.ValueExt = true
		case "sizeExt":
			m.SizeExt = true
		case "optional":
			m.Optional = true
		case "ext":
			m.IsExt = true
		case "openType":
			m.OpenType = true
		case "skip":
			m.Skip = true
		case "directChoice":
			m.DirectChoice = true
		}
	}
	return m
}

func MarshalAPER(v any) ([]byte, error) { return marshalPER(v, true) }
func MarshalUPER(v any) ([]byte, error) { return marshalPER(v, false) }

func UnmarshalAPER(b []byte, v any) error { return unmarshalPER(b, v, true) }
func UnmarshalUPER(b []byte, v any) error { return unmarshalPER(b, v, false) }

func marshalPER(v any, aligned bool) ([]byte, error) {
	w := NewWriter(aligned)
	rv := reflect.ValueOf(v)
	if rv.Kind() == reflect.Pointer {
		rv = rv.Elem()
	}
	if err := encodeValue(w, rv, fieldMeta{}); err != nil {
		return nil, err
	}
	return w.Bytes(), nil
}

func unmarshalPER(b []byte, v any, aligned bool) error {
	r := NewReader(b, aligned)
	rv := reflect.ValueOf(v)
	if rv.Kind() != reflect.Pointer {
		return fmt.Errorf("Unmarshal target must be a pointer")
	}
	return decodeValue(r, rv.Elem(), fieldMeta{})
}

// encodeValue dispatches on the reflect.Kind of rv and uses meta from the
// enclosing struct tag (if any) for constraints.
func encodeValue(w *PerBitData, rv reflect.Value, meta fieldMeta) error {
	switch rv.Kind() {
	case reflect.Bool:
		b := uint64(0)
		if rv.Bool() {
			b = 1
		}
		return w.PutBits(b, 1)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		return encodeInt(w, rv.Int(), meta)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return encodeInt(w, int64(rv.Uint()), meta)
	case reflect.Float64, reflect.Float32:
		return fmt.Errorf("REAL encoding not yet implemented")
	case reflect.String:
		return w.PutKMString(rv.String(), 7,
			meta.SizeExt, meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
	case reflect.Slice:
		// []byte = OCTET STRING
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			return w.PutOctetString(rv.Bytes(),
				meta.SizeExt, meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
		}
		return encodeSequenceOf(w, rv, meta)
	case reflect.Pointer:
		if rv.IsNil() {
			return nil
		}
		return encodeValue(w, rv.Elem(), meta)
	case reflect.Struct:
		return encodeStruct(w, rv, meta)
	}
	return fmt.Errorf("encodeValue: unsupported kind %s", rv.Kind())
}

func encodeInt(w *PerBitData, v int64, meta fieldMeta) error {
	switch {
	case meta.HasValueLB && meta.HasValueUB:
		if meta.ValueExt {
			inExt := v < meta.ValueLB || v > meta.ValueUB
			b := uint64(0)
			if inExt {
				b = 1
			}
			if err := w.PutBits(b, 1); err != nil {
				return err
			}
			if inExt {
				return w.PutUnconstrainedWhole(v)
			}
		}
		return w.PutConstrainedWhole(v, meta.ValueLB, meta.ValueUB)
	case meta.HasValueLB:
		return w.PutSemiConstrainedWhole(v, meta.ValueLB)
	default:
		return w.PutUnconstrainedWhole(v)
	}
}

// encodeSequenceOf: length + each element.
func encodeSequenceOf(w *PerBitData, rv reflect.Value, meta fieldMeta) error {
	length := uint64(rv.Len())
	// Validate SEQUENCE OF size constraint
	if meta.HasSizeUB && !meta.SizeExt && (length < meta.SizeLB || length > meta.SizeUB) {
		return fmt.Errorf("SEQUENCE OF length %d violates constraint SIZE(%d..%d)", length, meta.SizeLB, meta.SizeUB)
	}
	if meta.SizeExt {
		inExt := meta.HasSizeUB && (length < meta.SizeLB || length > meta.SizeUB)
		b := uint64(0)
		if inExt {
			b = 1
			meta.HasSizeUB = false
		}
		if err := w.PutBits(b, 1); err != nil {
			return err
		}
	}
	if err := w.PutLengthDeterminant(length, meta.SizeLB, meta.SizeUB, meta.HasSizeUB); err != nil {
		return err
	}
	for i := 0; i < rv.Len(); i++ {
		if err := encodeValue(w, rv.Index(i), fieldMeta{}); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	return nil
}

// encodeStruct handles SEQUENCE, SET, and CHOICE.
// CHOICE is detected by the presence of an int field named "Present".
// bitStringType is cached for ConvertibleTo checks.
var bitStringType = reflect.TypeOf(BitString{})

func encodeStruct(w *PerBitData, rv reflect.Value, meta fieldMeta) error {
	rt := rv.Type()
	// Detect BitString and all named types derived from it (e.g.
	// NRCellIdentity, AMFSetID, TransportLayerAddress). These are defined
	// as 'type X runtime.BitString' in the generated code. The struct has
	// exactly 2 fields (Bytes []byte, BitLength uint64) and is convertible
	// to BitString. Pass the parent field's size constraint from the aper
	// tag so PutBitString uses the correct constrained encoding.
	if rt.ConvertibleTo(bitStringType) && rt.NumField() == 2 {
		bs := rv.Convert(bitStringType).Interface().(BitString)
		return w.PutBitString(bs, meta.SizeExt, meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
	}
	// CHOICE detection
	if f, ok := rt.FieldByName("Present"); ok && f.Type.Kind() == reflect.Int {
		return encodeChoice(w, rv)
	}
	return encodeSequence(w, rv)
}

// APERExtensibleMarker is implemented by every generated type that
// corresponds to an extensible ASN.1 SEQUENCE / SET / CHOICE. The runtime
// uses it to decide whether to emit the leading 1-bit extension marker
// per X.691.
type APERExtensibleMarker interface {
	APERExtensible()
}

func isExtensible(rv reflect.Value) bool {
	if rv.CanAddr() {
		if _, ok := rv.Addr().Interface().(APERExtensibleMarker); ok {
			return true
		}
	}
	return false
}

func encodeSequence(w *PerBitData, rv reflect.Value) error {
	rt := rv.Type()
	var rootFields, extFields []int
	for i := 0; i < rt.NumField(); i++ {
		m := parseMeta(rt.Field(i).Tag.Get("aper"))
		if m.Skip {
			continue
		}
		if m.IsExt {
			extFields = append(extFields, i)
		} else {
			rootFields = append(rootFields, i)
		}
	}
	hasExtensions := len(extFields) > 0
	extensible := isExtensible(rv) || hasExtensions
	if extensible {
		anyExtPresent := false
		for _, i := range extFields {
			if !isNilLike(rv.Field(i)) {
				anyExtPresent = true
				break
			}
		}
		b := uint64(0)
		if anyExtPresent {
			b = 1
		}
		if err := w.PutBits(b, 1); err != nil {
			return err
		}
	}
	// Optional preamble (1 bit per OPTIONAL/DEFAULT root field)
	for _, i := range rootFields {
		m := parseMeta(rt.Field(i).Tag.Get("aper"))
		if !m.Optional {
			continue
		}
		present := uint64(0)
		if !isNilLike(rv.Field(i)) {
			present = 1
		}
		if err := w.PutBits(present, 1); err != nil {
			return err
		}
	}
	// Root components
	for _, i := range rootFields {
		m := parseMeta(rt.Field(i).Tag.Get("aper"))
		f := rv.Field(i)
		if m.Optional && isNilLike(f) {
			continue
		}
		if m.OpenType {
			var inner []byte
			var err error
			if m.DirectChoice && f.Kind() == reflect.Struct {
				// 3GPP-style: encode just the alternative selected by
				// `Present`, no CHOICE index. The receiver disambiguates
				// from a sibling `id` field via the table constraint.
				inner, err = marshalDirectChoice(f, w.Aligned)
			} else {
				inner, err = marshalFieldToBytes(f, m, w.Aligned)
			}
			if err != nil {
				return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
			}
			if err := w.PutOpenType(inner); err != nil {
				return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
			}
			continue
		}
		if err := encodeValue(w, f, m); err != nil {
			return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
		}
	}
	// Extensions (if any present)
	if hasExtensions {
		// bitmap of which extensions are present (normally-small length n, then n bits)
		n := uint64(len(extFields))
		// For MVP we emit a flat n-bit bitmap prefixed by the normally-small count.
		// This matches the X.691 §18.7 rule for a single extension addition group.
		if err := w.PutNormallySmallNonNegative(n - 1); err != nil {
			return err
		}
		presence := make([]bool, n)
		for idx, i := range extFields {
			presence[idx] = !isNilLike(rv.Field(i))
		}
		for _, p := range presence {
			b := uint64(0)
			if p {
				b = 1
			}
			if err := w.PutBits(b, 1); err != nil {
				return err
			}
		}
		for idx, i := range extFields {
			if !presence[idx] {
				continue
			}
			// Encode extension field as an open-type wrapper.
			inner, err := marshalFieldToBytes(rv.Field(i), parseMeta(rt.Field(i).Tag.Get("aper")), w.Aligned)
			if err != nil {
				return err
			}
			if err := w.PutOpenType(inner); err != nil {
				return err
			}
		}
	}
	return nil
}

func encodeChoice(w *PerBitData, rv reflect.Value) error {
	present := int(rv.FieldByName("Present").Int())
	if present <= 0 {
		return fmt.Errorf("CHOICE: no alternative selected")
	}
	rt := rv.Type()
	var alts []int
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "Present" {
			continue
		}
		alts = append(alts, i)
	}
	idx := present - 1
	if idx < 0 || idx >= len(alts) {
		return fmt.Errorf("CHOICE: Present=%d out of range", present)
	}
	// Extensible CHOICE: 1 bit indicating root (0) or extension (1).
	if isExtensible(rv) {
		// MVP: assume Present always selects a root alternative.
		if err := w.PutBits(0, 1); err != nil {
			return err
		}
	}
	if err := w.PutConstrainedWhole(int64(idx), 0, int64(len(alts)-1)); err != nil {
		return err
	}
	return encodeValue(w, rv.Field(alts[idx]), parseMeta(rt.Field(alts[idx]).Tag.Get("aper")))
}

func marshalFieldToBytes(rv reflect.Value, meta fieldMeta, aligned bool) ([]byte, error) {
	w := NewWriter(aligned)
	if err := encodeValue(w, rv, meta); err != nil {
		return nil, err
	}
	_ = w.AlignToByte()
	return w.Bytes(), nil
}

// APERAlternativeChooser is implemented by every generated `<ObjectSet>Value`
// type. Given the sibling Id field's integer value, it returns the Present
// index of the alternative that should be decoded.
type APERAlternativeChooser interface {
	APERAlternativeForID(id int64) int
}

// decodeDirectChoice reads JUST the alternative chosen by the sibling id,
// then sets Present + the alternative pointer to the freshly-decoded value.
func decodeDirectChoice(r *PerBitData, rv reflect.Value, id int64) error {
	if !rv.CanAddr() {
		return fmt.Errorf("directChoice decode: value not addressable")
	}
	chooser, ok := rv.Addr().Interface().(APERAlternativeChooser)
	if !ok {
		return fmt.Errorf("directChoice decode: %s lacks APERAlternativeForID", rv.Type().Name())
	}
	present := chooser.APERAlternativeForID(id)
	if present <= 0 {
		return fmt.Errorf("directChoice decode: no alternative for id=%d", id)
	}
	rt := rv.Type()
	var alts []int
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "Present" {
			continue
		}
		alts = append(alts, i)
	}
	idx := present - 1
	if idx >= len(alts) {
		return fmt.Errorf("directChoice decode: Present=%d out of range", present)
	}
	rv.FieldByName("Present").SetInt(int64(present))
	fi := alts[idx]
	f := rv.Field(fi)
	if f.Kind() == reflect.Pointer && f.IsNil() {
		f.Set(reflect.New(f.Type().Elem()))
	}
	return decodeValue(r, f, parseMeta(rt.Field(fi).Tag.Get("aper")))
}

// marshalDirectChoice encodes only the alternative selected by `Present` of
// a CHOICE-shaped struct, without writing a CHOICE index. Used inside open-
// type wrappers whose receiver-side type is determined by an &id sibling.
func marshalDirectChoice(rv reflect.Value, aligned bool) ([]byte, error) {
	rt := rv.Type()
	pf := rv.FieldByName("Present")
	if !pf.IsValid() {
		return nil, fmt.Errorf("directChoice: no Present field")
	}
	present := int(pf.Int())
	if present <= 0 {
		return nil, fmt.Errorf("directChoice: nothing selected")
	}
	var alts []int
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "Present" {
			continue
		}
		alts = append(alts, i)
	}
	idx := present - 1
	if idx >= len(alts) {
		return nil, fmt.Errorf("directChoice: Present=%d out of range", present)
	}
	w := NewWriter(aligned)
	fi := alts[idx]
	if err := encodeValue(w, rv.Field(fi), parseMeta(rt.Field(fi).Tag.Get("aper"))); err != nil {
		return nil, err
	}
	_ = w.AlignToByte()
	return w.Bytes(), nil
}

func isNilLike(v reflect.Value) bool {
	switch v.Kind() {
	case reflect.Pointer, reflect.Slice, reflect.Map, reflect.Interface:
		return v.IsNil()
	}
	return false
}

// --- decode ---

func decodeValue(r *PerBitData, rv reflect.Value, meta fieldMeta) error {
	switch rv.Kind() {
	case reflect.Bool:
		b, err := r.GetBits(1)
		if err != nil {
			return err
		}
		rv.SetBool(b == 1)
		return nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v, err := decodeInt(r, meta)
		if err != nil {
			return err
		}
		rv.SetInt(v)
		return nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v, err := decodeInt(r, meta)
		if err != nil {
			return err
		}
		rv.SetUint(uint64(v))
		return nil
	case reflect.String:
		s, err := r.GetKMString(7, meta.SizeExt, meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
		if err != nil {
			return err
		}
		rv.SetString(s)
		return nil
	case reflect.Slice:
		if rv.Type().Elem().Kind() == reflect.Uint8 {
			b, err := r.GetOctetString(meta.SizeExt, meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
			if err != nil {
				return err
			}
			rv.SetBytes(b)
			return nil
		}
		return decodeSequenceOf(r, rv, meta)
	case reflect.Pointer:
		if rv.IsNil() {
			rv.Set(reflect.New(rv.Type().Elem()))
		}
		return decodeValue(r, rv.Elem(), meta)
	case reflect.Struct:
		return decodeStruct(r, rv, meta)
	}
	return fmt.Errorf("decodeValue: unsupported kind %s", rv.Kind())
}

func decodeInt(r *PerBitData, meta fieldMeta) (int64, error) {
	switch {
	case meta.HasValueLB && meta.HasValueUB:
		if meta.ValueExt {
			ext, err := r.GetBits(1)
			if err != nil {
				return 0, err
			}
			if ext == 1 {
				return r.GetUnconstrainedWhole()
			}
		}
		return r.GetConstrainedWhole(meta.ValueLB, meta.ValueUB)
	case meta.HasValueLB:
		return r.GetSemiConstrainedWhole(meta.ValueLB)
	default:
		return r.GetUnconstrainedWhole()
	}
}

func decodeSequenceOf(r *PerBitData, rv reflect.Value, meta fieldMeta) error {
	if meta.SizeExt {
		ext, err := r.GetBits(1)
		if err != nil {
			return err
		}
		if ext == 1 {
			meta.HasSizeUB = false
		}
	}
	length, err := r.GetLengthDeterminant(meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
	if err != nil {
		return err
	}
	slice := reflect.MakeSlice(rv.Type(), int(length), int(length))
	for i := 0; i < int(length); i++ {
		if err := decodeValue(r, slice.Index(i), fieldMeta{}); err != nil {
			return fmt.Errorf("[%d]: %w", i, err)
		}
	}
	rv.Set(slice)
	return nil
}

func decodeStruct(r *PerBitData, rv reflect.Value, meta fieldMeta) error {
	rt := rv.Type()
	if rt.ConvertibleTo(bitStringType) && rt.NumField() == 2 {
		bs, err := r.GetBitString(meta.SizeExt, meta.SizeLB, meta.SizeUB, meta.HasSizeUB)
		if err != nil {
			return err
		}
		rv.Set(reflect.ValueOf(bs).Convert(rt))
		return nil
	}
	if f, ok := rt.FieldByName("Present"); ok && f.Type.Kind() == reflect.Int {
		return decodeChoice(r, rv)
	}
	return decodeSequence(r, rv)
}

func decodeSequence(r *PerBitData, rv reflect.Value) error {
	rt := rv.Type()
	var rootFields, extFields []int
	for i := 0; i < rt.NumField(); i++ {
		m := parseMeta(rt.Field(i).Tag.Get("aper"))
		if m.Skip {
			continue
		}
		if m.IsExt {
			extFields = append(extFields, i)
		} else {
			rootFields = append(rootFields, i)
		}
	}
	hasExtensions := len(extFields) > 0
	extensible := isExtensible(rv) || hasExtensions
	extPresent := false
	if extensible {
		b, err := r.GetBits(1)
		if err != nil {
			return err
		}
		extPresent = b == 1
	}
	// Optional preamble
	optBits := make(map[int]bool)
	for _, i := range rootFields {
		m := parseMeta(rt.Field(i).Tag.Get("aper"))
		if !m.Optional {
			continue
		}
		b, err := r.GetBits(1)
		if err != nil {
			return err
		}
		optBits[i] = b == 1
	}
	for _, i := range rootFields {
		m := parseMeta(rt.Field(i).Tag.Get("aper"))
		if m.Optional && !optBits[i] {
			continue
		}
		f := rv.Field(i)
		if m.Optional {
			if f.Kind() == reflect.Pointer && f.IsNil() {
				f.Set(reflect.New(f.Type().Elem()))
			}
		}
		if m.OpenType {
			inner, err := r.GetOpenType()
			if err != nil {
				return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
			}
			subR := NewReader(inner, r.Aligned)
			if m.DirectChoice && f.Kind() == reflect.Struct {
				// Use the previously-decoded Id sibling to choose the alternative.
				idVal := int64(0)
				if idF := rv.FieldByName("Id"); idF.IsValid() {
					idVal = idF.Int()
				}
				if err := decodeDirectChoice(subR, f, idVal); err != nil {
					return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
				}
				continue
			}
			if err := decodeValue(subR, f, m); err != nil {
				return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
			}
			continue
		}
		if err := decodeValue(r, f, m); err != nil {
			return fmt.Errorf("field %s: %w", rt.Field(i).Name, err)
		}
	}
	if extPresent {
		n1, err := r.GetNormallySmallNonNegative()
		if err != nil {
			return err
		}
		n := int(n1 + 1)
		presence := make([]bool, n)
		for i := 0; i < n; i++ {
			b, err := r.GetBits(1)
			if err != nil {
				return err
			}
			presence[i] = b == 1
		}
		for i := 0; i < n && i < len(extFields); i++ {
			if !presence[i] {
				continue
			}
			inner, err := r.GetOpenType()
			if err != nil {
				return err
			}
			subR := NewReader(inner, r.Aligned)
			f := rv.Field(extFields[i])
			if f.Kind() == reflect.Pointer && f.IsNil() {
				f.Set(reflect.New(f.Type().Elem()))
			}
			if err := decodeValue(subR, f, parseMeta(rt.Field(extFields[i]).Tag.Get("aper"))); err != nil {
				return err
			}
		}
	}
	return nil
}

func decodeChoice(r *PerBitData, rv reflect.Value) error {
	rt := rv.Type()
	var alts []int
	for i := 0; i < rt.NumField(); i++ {
		if rt.Field(i).Name == "Present" {
			continue
		}
		alts = append(alts, i)
	}
	if isExtensible(rv) {
		// Discard the extension/root bit (MVP supports root only).
		if _, err := r.GetBits(1); err != nil {
			return err
		}
	}
	idx, err := r.GetConstrainedWhole(0, int64(len(alts)-1))
	if err != nil {
		return err
	}
	rv.FieldByName("Present").SetInt(idx + 1)
	fi := alts[idx]
	f := rv.Field(fi)
	if f.Kind() == reflect.Pointer && f.IsNil() {
		f.Set(reflect.New(f.Type().Elem()))
	}
	return decodeValue(r, f, parseMeta(rt.Field(fi).Tag.Get("aper")))
}
