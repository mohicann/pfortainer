package httpx

import (
	"math"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// serde_json 직렬화 흉내:
//   - 구조체 필드는 선언 순서(json 태그 이름, omitempty 지원), map은 키 정렬(json! = BTreeMap)
//   - HTML 문자(< > &)는 이스케이프하지 않음, 제어 문자만 \uXXXX (\n \t 등은 짧은 형태)
//   - f64는 ryu 표기(정수값이면 "2.0")

// Ordered: 키 순서를 지키는 JSON 객체(serde 구조체를 즉석에서 만들 때).
type Ordered []KV

type KV struct {
	K string
	V any
}

// RawJSON: 그대로 쓰는 JSON 조각.
type RawJSON string

// MarshalJSON: serde_json::to_string과 같은 바이트.
func MarshalJSON(v any) []byte {
	var b strings.Builder
	enc(&b, reflect.ValueOf(v))
	return []byte(b.String())
}

func MarshalString(v any) string { return string(MarshalJSON(v)) }

func enc(b *strings.Builder, v reflect.Value) {
	if !v.IsValid() {
		b.WriteString("null")
		return
	}
	if v.CanInterface() {
		if sj, ok := v.Interface().(interface{ SerdeJSON() string }); ok {
			b.WriteString(sj.SerdeJSON())
			return
		}
	}
	switch x := v.Interface().(type) {
	case RawJSON:
		b.WriteString(string(x))
		return
	case JNumber:
		b.WriteString(normNumber(x))
		return
	case Ordered:
		b.WriteByte('{')
		for i, kv := range x {
			if i > 0 {
				b.WriteByte(',')
			}
			encString(b, kv.K)
			b.WriteByte(':')
			enc(b, reflect.ValueOf(kv.V))
		}
		b.WriteByte('}')
		return
	}
	switch v.Kind() {
	case reflect.Interface, reflect.Pointer:
		if v.IsNil() {
			b.WriteString("null")
			return
		}
		enc(b, v.Elem())
	case reflect.Bool:
		if v.Bool() {
			b.WriteString("true")
		} else {
			b.WriteString("false")
		}
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		b.WriteString(strconv.FormatInt(v.Int(), 10))
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		b.WriteString(strconv.FormatUint(v.Uint(), 10))
	case reflect.Float32, reflect.Float64:
		b.WriteString(RyuF64(v.Float()))
	case reflect.String:
		encString(b, v.String())
	case reflect.Slice, reflect.Array:
		if v.Kind() == reflect.Slice && v.IsNil() && v.Type().Elem().Kind() != reflect.Uint8 {
			b.WriteString("[]")
			return
		}
		b.WriteByte('[')
		for i := 0; i < v.Len(); i++ {
			if i > 0 {
				b.WriteByte(',')
			}
			enc(b, v.Index(i))
		}
		b.WriteByte(']')
	case reflect.Map:
		keys := v.MapKeys()
		ks := make([]string, len(keys))
		for i, k := range keys {
			ks[i] = k.String()
		}
		sort.Strings(ks)
		b.WriteByte('{')
		for i, k := range ks {
			if i > 0 {
				b.WriteByte(',')
			}
			encString(b, k)
			b.WriteByte(':')
			enc(b, v.MapIndex(reflect.ValueOf(k).Convert(v.Type().Key())))
		}
		b.WriteByte('}')
	case reflect.Struct:
		t := v.Type()
		b.WriteByte('{')
		first := true
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			if !f.IsExported() {
				continue
			}
			tag := f.Tag.Get("json")
			if tag == "-" {
				continue
			}
			name, opts, _ := strings.Cut(tag, ",")
			if name == "" {
				name = f.Name
			}
			fv := v.Field(i)
			if strings.Contains(opts, "omitempty") && fv.IsZero() {
				continue
			}
			if !first {
				b.WriteByte(',')
			}
			first = false
			encString(b, name)
			b.WriteByte(':')
			enc(b, fv)
		}
		b.WriteByte('}')
	default:
		b.WriteString("null")
	}
}

func encString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		default:
			if c < 0x20 {
				b.WriteString(`\u00`)
				b.WriteByte("0123456789abcdef"[c>>4])
				b.WriteByte("0123456789abcdef"[c&0xf])
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('"')
}

// RyuF64: Rust ryu(serde_json) f64 표기.
func RyuF64(f float64) string {
	if math.IsNaN(f) || math.IsInf(f, 0) {
		return "null"
	}
	if f == 0 {
		if math.Signbit(f) {
			return "-0.0"
		}
		return "0.0"
	}
	a := math.Abs(f)
	if a >= 1e-5 && a < 1e16 {
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.Contains(s, ".") {
			s += ".0"
		}
		return s
	}
	s := strconv.FormatFloat(f, 'e', -1, 64) // 1e+21, 1.5e-07
	mant, exp, _ := strings.Cut(s, "e")
	exp = strings.TrimPrefix(exp, "+")
	neg := strings.HasPrefix(exp, "-")
	exp = strings.TrimLeft(strings.TrimPrefix(exp, "-"), "0")
	if neg {
		exp = "-" + exp
	}
	return mant + "e" + exp
}

// normNumber: serde_json이 숫자를 읽어 다시 쓸 때의 표기.
func normNumber(n JNumber) string {
	if n.IsInt {
		if i, err := strconv.ParseInt(n.Raw, 10, 64); err == nil {
			return strconv.FormatInt(i, 10)
		}
		if u, err := strconv.ParseUint(n.Raw, 10, 64); err == nil {
			return strconv.FormatUint(u, 10)
		}
	}
	f, _ := strconv.ParseFloat(n.Raw, 64)
	return RyuF64(f)
}

// Float: 숫자 값.
func (n JNumber) Float() float64 { f, _ := strconv.ParseFloat(n.Raw, 64); return f }
