package httpx

import (
	"fmt"
	"math"
	"reflect"
	"strconv"
	"strings"
)

// Unmarshal: serde_json::from_slice::<T> 흉내(오류 문구 포함, 경로 접두어 없음).
//
// Go 구조체 규칙: `json:"이름"` 태그, `serde:"default"`면 없어도 됨(기본값, null은 오류),
// 포인터는 Option(없거나 null이면 nil), any는 serde_json::Value, 타입 이름은 Rust 구조체 이름과 같게 둔다
// ("expected struct X" 문구에 쓰임). [N]any 는 튜플.
func Unmarshal(b []byte, dst any) error {
	doc, err := parseDoc(b)
	if err != nil {
		return plainErr(err)
	}
	p := &jparser{s: b}
	if e := assign(p, doc, reflect.ValueOf(dst).Elem()); e != nil {
		return plainErr(e)
	}
	return nil
}

// SerdeError: serde_json::Error Display.
type SerdeError struct{ Msg string }

func (e *SerdeError) Error() string { return e.Msg }

func plainErr(e *JSONError) error {
	return &SerdeError{fmt.Sprintf("%s at line %d column %d", e.Msg, e.Line, e.Col)}
}

func expectFor(t reflect.Type) string {
	switch t.Kind() {
	case reflect.String:
		return "a string"
	case reflect.Bool:
		return "a boolean"
	case reflect.Int64, reflect.Int:
		return "i64"
	case reflect.Int32:
		return "i32"
	case reflect.Int16:
		return "i16"
	case reflect.Uint64, reflect.Uint:
		return "u64"
	case reflect.Uint32:
		return "u32"
	case reflect.Uint16:
		return "u16"
	case reflect.Float64, reflect.Float32:
		return "f64"
	case reflect.Slice:
		return "a sequence"
	case reflect.Array:
		return fmt.Sprintf("a tuple of size %d", t.Len())
	case reflect.Map:
		return "a map"
	case reflect.Struct:
		return "struct " + t.Name()
	}
	return "a value"
}

// SerdeCustom: 직접 해석하는 타입(deserialize_with). 오류면 "expected …" 설명을 돌려준다.
type SerdeCustom interface {
	SerdeDecode(v any) (expecting string, ok bool)
}

var customType = reflect.TypeOf((*SerdeCustom)(nil)).Elem()

func assign(p *jparser, v *jval, dst reflect.Value) *JSONError {
	t := dst.Type()
	if dst.CanAddr() && dst.Addr().Type().Implements(customType) {
		exp, ok := dst.Addr().Interface().(SerdeCustom).SerdeDecode(toAny(v))
		if !ok {
			return p.errAt(v.end, false, "invalid type: "+unexpected(v)+", expected "+exp)
		}
		return nil
	}
	bad := func() *JSONError {
		return p.errAt(v.end, false, "invalid type: "+unexpected(v)+", expected "+expectFor(t))
	}
	switch t.Kind() {
	case reflect.Pointer:
		if v.kind == 'z' {
			dst.Set(reflect.Zero(t))
			return nil
		}
		nv := reflect.New(t.Elem())
		if e := assign(p, v, nv.Elem()); e != nil {
			return e
		}
		dst.Set(nv)
		return nil
	case reflect.Interface:
		dst.Set(reflect.ValueOf(toAny(v)))
		return nil
	case reflect.String:
		if v.kind != 's' {
			return bad()
		}
		dst.SetString(v.str)
	case reflect.Bool:
		if v.kind != 'b' {
			return bad()
		}
		dst.SetBool(v.b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if v.kind != 'n' || !v.isInt {
			return bad()
		}
		n, err := strconv.ParseInt(v.num, 10, 64)
		if err != nil || dst.OverflowInt(n) {
			return p.errAt(v.end, false, "invalid value: integer `"+v.num+"`, expected "+expectFor(t))
		}
		dst.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		if v.kind != 'n' || !v.isInt {
			return bad()
		}
		n, err := strconv.ParseUint(v.num, 10, 64)
		if err != nil || dst.OverflowUint(n) {
			return p.errAt(v.end, false, "invalid value: integer `"+v.num+"`, expected "+expectFor(t))
		}
		dst.SetUint(n)
	case reflect.Float32, reflect.Float64:
		if v.kind != 'n' {
			return bad()
		}
		f, _ := strconv.ParseFloat(v.num, 64)
		if math.IsInf(f, 0) {
			return p.errAt(v.end, true, "number out of range")
		}
		dst.SetFloat(f)
	case reflect.Slice:
		if v.kind != 'a' {
			return bad()
		}
		out := reflect.MakeSlice(t, len(v.arr), len(v.arr))
		for i, el := range v.arr {
			if e := assign(p, el, out.Index(i)); e != nil {
				return e
			}
		}
		dst.Set(out)
	case reflect.Array:
		if v.kind != 'a' {
			return bad()
		}
		if len(v.arr) != t.Len() {
			return p.errAt(v.end, false, fmt.Sprintf("invalid length %d, expected a tuple of size %d", len(v.arr), t.Len()))
		}
		for i, el := range v.arr {
			if e := assign(p, el, dst.Index(i)); e != nil {
				return e
			}
		}
	case reflect.Map:
		if v.kind != 'o' {
			return bad()
		}
		m := reflect.MakeMap(t)
		for _, e := range v.obj {
			ev := reflect.New(t.Elem()).Elem()
			if err := assign(p, e.v, ev); err != nil {
				return err
			}
			m.SetMapIndex(reflect.ValueOf(e.k).Convert(t.Key()), ev)
		}
		dst.Set(m)
	case reflect.Struct:
		if v.kind != 'o' {
			return bad()
		}
		seen := map[int]bool{}
		for _, e := range v.obj {
			idx := -1
			for i := 0; i < t.NumField(); i++ {
				if t.Field(i).IsExported() && jsonName(t.Field(i)) == e.k {
					idx = i
				}
			}
			if idx < 0 {
				continue
			}
			if seen[idx] {
				return p.errAt(e.v.end, false, "duplicate field `"+e.k+"`")
			}
			seen[idx] = true
			if e.v.kind == 'z' && strings.Contains(t.Field(idx).Tag.Get("serde"), "null") {
				dst.Field(idx).Set(reflect.Zero(t.Field(idx).Type)) // deserialize_null_default
				continue
			}
			if err := assign(p, e.v, dst.Field(idx)); err != nil {
				return err
			}
		}
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() || seen[i] || jsonName(f) == "-" {
				continue
			}
			if f.Type.Kind() == reflect.Pointer || strings.Contains(f.Tag.Get("serde"), "default") || f.Type.Kind() == reflect.Interface {
				continue
			}
			return p.errAt(v.end, false, "missing field `"+jsonName(f)+"`")
		}
	default:
		return bad()
	}
	return nil
}

func jsonName(f reflect.StructField) string {
	n, _, _ := strings.Cut(f.Tag.Get("json"), ",")
	if n == "" {
		return f.Name
	}
	return n
}
