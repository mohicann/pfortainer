package httpx

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"
)

// serde_json + serde_path_to_error 동작 흉내: axum Json<T> 추출기의 오류 문구를 같게 만든다.

// Kind: 필드 타입.
type Kind int

const (
	KStr     Kind = iota // String
	KBool                // bool
	KI64                 // i64
	KI32                 // i32
	KU16                 // u16
	KStrList             // Vec<String>
	KAny                 // serde_json::Value
)

// Field: serde derive 구조체 필드.
type Field struct {
	Name    string
	Kind    Kind
	Default bool // #[serde(default)]
	Option  bool // Option<T> (없거나 null이면 nil)
}

// JSONError: Json 추출 실패.
type JSONError struct {
	Syntax bool // 구문/EOF 오류 → "Failed to parse ...", 아니면 "Failed to deserialize ..."
	Path   string
	Msg    string
	Line   int
	Col    int
}

func (e *JSONError) Error() string {
	var b strings.Builder
	if e.Syntax {
		b.WriteString("Failed to parse the request body as JSON: ")
	} else {
		b.WriteString("Failed to deserialize the JSON body into the target type: ")
	}
	if e.Path != "" {
		b.WriteString(e.Path + ": ")
	}
	fmt.Fprintf(&b, "%s at line %d column %d", e.Msg, e.Line, e.Col)
	return b.String()
}

type jparser struct {
	s    []byte
	pos  int // 다음에 읽을 위치
	path []string
}

func (p *jparser) lineCol(at int) (int, int) {
	line, start := 1, 0
	for i := 0; i < at && i < len(p.s); i++ {
		if p.s[i] == '\n' {
			line++
			start = i + 1
		}
	}
	return line, at - start
}

// errAt: pos(읽은 바이트 수) 기준 위치로 오류.
func (p *jparser) errAt(at int, syntax bool, msg string) *JSONError {
	l, c := p.lineCol(at)
	return &JSONError{Syntax: syntax, Path: strings.Join(p.path, "."), Msg: msg, Line: l, Col: c}
}

func (p *jparser) eof(what string) *JSONError {
	return p.errAt(len(p.s), true, "EOF while parsing "+what)
}

func (p *jparser) ws() {
	for p.pos < len(p.s) {
		switch p.s[p.pos] {
		case ' ', '\t', '\n', '\r':
			p.pos++
		default:
			return
		}
	}
}

// jval: 파싱한 값(타입 판단용 정보 포함).
type jval struct {
	kind  byte // 's' 'n' 'b' 'z'(null) 'o' 'a'
	str   string
	num   string
	isInt bool
	b     bool
	obj   []kv
	arr   []*jval
	end   int // 값 마지막 바이트 다음 위치
}

type kv struct {
	k string
	v *jval
}

func (p *jparser) value() (*jval, *JSONError) {
	p.ws()
	if p.pos >= len(p.s) {
		return nil, p.eof("a value")
	}
	c := p.s[p.pos]
	switch {
	case c == '{':
		return p.object()
	case c == '[':
		return p.array()
	case c == '"':
		s, err := p.str()
		if err != nil {
			return nil, err
		}
		return &jval{kind: 's', str: s, end: p.pos}, nil
	case c == 't':
		return p.lit("true", &jval{kind: 'b', b: true})
	case c == 'f':
		return p.lit("false", &jval{kind: 'b', b: false})
	case c == 'n':
		return p.lit("null", &jval{kind: 'z'})
	case c == '-' || c >= '0' && c <= '9':
		return p.number()
	}
	return nil, p.errAt(p.pos+1, true, "expected value")
}

func (p *jparser) lit(word string, v *jval) (*jval, *JSONError) {
	for i := 0; i < len(word); i++ {
		if p.pos >= len(p.s) {
			return nil, p.eof("a value")
		}
		if p.s[p.pos] != word[i] {
			return nil, p.errAt(p.pos+1, true, "expected ident")
		}
		p.pos++
	}
	v.end = p.pos
	return v, nil
}

func (p *jparser) number() (*jval, *JSONError) {
	start := p.pos
	if p.s[p.pos] == '-' {
		p.pos++
	}
	if p.pos >= len(p.s) {
		return nil, p.eof("a value")
	}
	if p.s[p.pos] < '0' || p.s[p.pos] > '9' {
		return nil, p.errAt(p.pos+1, true, "invalid number")
	}
	isInt := true
	for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
		p.pos++
	}
	if p.pos < len(p.s) && p.s[p.pos] == '.' {
		isInt = false
		p.pos++
		if p.pos >= len(p.s) {
			return nil, p.eof("a value")
		}
		if p.s[p.pos] < '0' || p.s[p.pos] > '9' {
			return nil, p.errAt(p.pos+1, true, "invalid number")
		}
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
		}
	}
	if p.pos < len(p.s) && (p.s[p.pos] == 'e' || p.s[p.pos] == 'E') {
		isInt = false
		p.pos++
		if p.pos < len(p.s) && (p.s[p.pos] == '+' || p.s[p.pos] == '-') {
			p.pos++
		}
		if p.pos >= len(p.s) {
			return nil, p.eof("a value")
		}
		for p.pos < len(p.s) && p.s[p.pos] >= '0' && p.s[p.pos] <= '9' {
			p.pos++
		}
	}
	return &jval{kind: 'n', num: string(p.s[start:p.pos]), isInt: isInt, end: p.pos}, nil
}

func (p *jparser) str() (string, *JSONError) {
	p.pos++ // "
	var b strings.Builder
	for {
		if p.pos >= len(p.s) {
			return "", p.eof("a string")
		}
		c := p.s[p.pos]
		p.pos++
		switch {
		case c == '"':
			return b.String(), nil
		case c == '\\':
			if p.pos >= len(p.s) {
				return "", p.eof("a string")
			}
			e := p.s[p.pos]
			p.pos++
			switch e {
			case '"', '\\', '/':
				b.WriteByte(e)
			case 'b':
				b.WriteByte('\b')
			case 'f':
				b.WriteByte('\f')
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case 'u':
				if p.pos+4 > len(p.s) {
					return "", p.eof("a string")
				}
				n, err := strconv.ParseUint(string(p.s[p.pos:p.pos+4]), 16, 32)
				if err != nil {
					return "", p.errAt(p.pos+1, true, "invalid escape")
				}
				p.pos += 4
				r := rune(n)
				if r >= 0xD800 && r < 0xDC00 && p.pos+6 <= len(p.s) && p.s[p.pos] == '\\' && p.s[p.pos+1] == 'u' {
					n2, err := strconv.ParseUint(string(p.s[p.pos+2:p.pos+6]), 16, 32)
					if err == nil && n2 >= 0xDC00 && n2 < 0xE000 {
						r = (r-0xD800)<<10 | (rune(n2) - 0xDC00) + 0x10000
						p.pos += 6
					}
				}
				b.WriteRune(r)
			default:
				return "", p.errAt(p.pos, true, "invalid escape")
			}
		case c < 0x20:
			return "", p.errAt(p.pos, true, "control character (\\u0000-\\u001F) found while parsing a string")
		default:
			b.WriteByte(c)
		}
	}
}

func (p *jparser) object() (*jval, *JSONError) {
	p.pos++ // {
	v := &jval{kind: 'o'}
	p.ws()
	if p.pos >= len(p.s) {
		return nil, p.eof("an object")
	}
	if p.s[p.pos] == '}' {
		p.pos++
		v.end = p.pos
		return v, nil
	}
	for {
		p.ws()
		if p.pos >= len(p.s) {
			return nil, p.eof("an object")
		}
		if p.s[p.pos] != '"' {
			if p.s[p.pos] == '}' {
				return nil, p.errAt(p.pos+1, true, "trailing comma")
			}
			return nil, p.errAt(p.pos+1, true, "key must be a string")
		}
		k, err := p.str()
		if err != nil {
			return nil, err
		}
		p.ws()
		if p.pos >= len(p.s) {
			return nil, p.eof("an object")
		}
		if p.s[p.pos] != ':' {
			return nil, p.errAt(p.pos+1, true, "expected `:`")
		}
		p.pos++
		p.path = append(p.path, k)
		val, err := p.value()
		if err != nil {
			return nil, err
		}
		p.path = p.path[:len(p.path)-1]
		v.obj = append(v.obj, kv{k, val})
		p.ws()
		if p.pos >= len(p.s) {
			return nil, p.eof("an object")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
		case '}':
			p.pos++
			v.end = p.pos
			return v, nil
		default:
			return nil, p.errAt(p.pos+1, true, "expected `,` or `}`")
		}
	}
}

func (p *jparser) array() (*jval, *JSONError) {
	p.pos++
	v := &jval{kind: 'a'}
	p.ws()
	if p.pos >= len(p.s) {
		return nil, p.eof("a list")
	}
	if p.s[p.pos] == ']' {
		p.pos++
		v.end = p.pos
		return v, nil
	}
	for i := 0; ; i++ {
		p.path = append(p.path, fmt.Sprintf("[%d]", i))
		el, err := p.value()
		if err != nil {
			return nil, err
		}
		p.path = p.path[:len(p.path)-1]
		v.arr = append(v.arr, el)
		p.ws()
		if p.pos >= len(p.s) {
			return nil, p.eof("a list")
		}
		switch p.s[p.pos] {
		case ',':
			p.pos++
			p.ws()
			if p.pos < len(p.s) && p.s[p.pos] == ']' {
				return nil, p.errAt(p.pos+1, true, "trailing comma")
			}
		case ']':
			p.pos++
			v.end = p.pos
			return v, nil
		default:
			return nil, p.errAt(p.pos+1, true, "expected `,` or `]`")
		}
	}
}

// parseDoc: 문서 전체 파싱 + 뒤따르는 문자 확인.
func parseDoc(b []byte) (*jval, *JSONError) {
	p := &jparser{s: b}
	v, err := p.value()
	if err != nil {
		return nil, err
	}
	p.ws()
	if p.pos < len(p.s) {
		return nil, p.errAt(p.pos+1, true, "trailing characters")
	}
	return v, nil
}

func unexpected(v *jval) string {
	switch v.kind {
	case 's':
		return "string " + rustDebugStr(v.str)
	case 'b':
		if v.b {
			return "boolean `true`"
		}
		return "boolean `false`"
	case 'z':
		return "null"
	case 'o':
		return "map"
	case 'a':
		return "sequence"
	case 'n':
		if v.isInt {
			return "integer `" + strings.TrimPrefix(v.num, "+") + "`"
		}
		f, _ := strconv.ParseFloat(v.num, 64)
		s := strconv.FormatFloat(f, 'f', -1, 64)
		if !strings.ContainsAny(s, ".eE") && !math.IsInf(f, 0) {
			s += ".0"
		}
		return "floating point `" + s + "`"
	}
	return "value"
}

// rustDebugStr: Rust {:?} 문자열 표기.
func rustDebugStr(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch r {
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
		default:
			if r < 0x20 || r == 0x7f {
				fmt.Fprintf(&b, `\u{%x}`, r)
			} else {
				b.WriteRune(r)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}

func expectedName(k Kind) string {
	switch k {
	case KStr:
		return "a string"
	case KBool:
		return "a boolean"
	case KI64:
		return "i64"
	case KI32:
		return "i32"
	case KU16:
		return "u16"
	case KStrList:
		return "a sequence"
	}
	return "any valid JSON value"
}

// DecodeStruct: 본문을 구조체 명세대로 읽는다(값: string/bool/int64/[]string/any/nil).
func DecodeStruct(body []byte, structName string, fields []Field) (map[string]any, *JSONError) {
	doc, err := parseDoc(body)
	if err != nil {
		return nil, err
	}
	p := &jparser{s: body}
	if doc.kind != 'o' {
		return nil, p.errAt(doc.end, false, "invalid type: "+unexpected(doc)+", expected struct "+structName)
	}
	out := map[string]any{}
	seen := map[string]bool{}
	for _, e := range doc.obj {
		var f *Field
		for i := range fields {
			if fields[i].Name == e.k {
				f = &fields[i]
			}
		}
		if f == nil {
			continue
		}
		if seen[f.Name] {
			return nil, p.errAt(e.v.end, false, "duplicate field `"+f.Name+"`")
		}
		seen[f.Name] = true
		p.path = []string{f.Name}
		v, derr := convert(p, e.v, f.Kind, f.Option)
		p.path = nil
		if derr != nil {
			return nil, derr
		}
		out[f.Name] = v
	}
	for _, f := range fields {
		if _, ok := out[f.Name]; ok {
			continue
		}
		switch {
		case f.Option:
			out[f.Name] = nil
		case f.Default:
			out[f.Name] = zero(f.Kind)
		default:
			return nil, p.errAt(doc.end, false, "missing field `"+f.Name+"`")
		}
	}
	return out, nil
}

func zero(k Kind) any {
	switch k {
	case KStr:
		return ""
	case KBool:
		return false
	case KI64, KI32, KU16:
		return int64(0)
	case KStrList:
		return []string{}
	}
	return nil
}

func convert(p *jparser, v *jval, k Kind, option bool) (any, *JSONError) {
	if option && v.kind == 'z' {
		return nil, nil
	}
	bad := func() *JSONError {
		return p.errAt(v.end, false, "invalid type: "+unexpected(v)+", expected "+expectedName(k))
	}
	switch k {
	case KAny:
		return toAny(v), nil
	case KStr:
		if v.kind != 's' {
			return nil, bad()
		}
		return v.str, nil
	case KBool:
		if v.kind != 'b' {
			return nil, bad()
		}
		return v.b, nil
	case KI64, KI32, KU16:
		if v.kind != 'n' || !v.isInt {
			return nil, bad()
		}
		n, err := strconv.ParseInt(v.num, 10, 64)
		lo, hi := int64(math.MinInt64), int64(math.MaxInt64)
		if k == KI32 {
			lo, hi = math.MinInt32, math.MaxInt32
		}
		if k == KU16 {
			lo, hi = 0, math.MaxUint16
		}
		if err != nil || n < lo || n > hi {
			return nil, p.errAt(v.end, false, "invalid value: integer `"+v.num+"`, expected "+expectedName(k))
		}
		return n, nil
	case KStrList:
		if v.kind != 'a' {
			return nil, bad()
		}
		out := []string{}
		for i, el := range v.arr {
			if el.kind != 's' {
				p.path = append(p.path, fmt.Sprintf("[%d]", i))
				e := p.errAt(el.end, false, "invalid type: "+unexpected(el)+", expected a string")
				return nil, e
			}
			out = append(out, el.str)
		}
		return out, nil
	}
	return nil, bad()
}

// Value: serde_json::Value 대응(객체는 키 정렬 — BTreeMap).
type Value = any

// JNumber: serde_json 숫자(원문 보존).
type JNumber struct {
	Raw   string
	IsInt bool
}

func toAny(v *jval) any {
	switch v.kind {
	case 's':
		return v.str
	case 'b':
		return v.b
	case 'z':
		return nil
	case 'n':
		return JNumber{Raw: v.num, IsInt: v.isInt}
	case 'a':
		out := make([]any, len(v.arr))
		for i, e := range v.arr {
			out[i] = toAny(e)
		}
		return out
	case 'o':
		m := map[string]any{}
		for _, e := range v.obj {
			m[e.k] = toAny(e.v) // 같은 키는 마지막 값
		}
		return m
	}
	return nil
}

// DecodeValue: serde_json::Value로 읽기(구문 오류만).
func DecodeValue(body []byte) (any, *JSONError) {
	doc, err := parseDoc(body)
	if err != nil {
		return nil, err
	}
	return toAny(doc), nil
}

var _ = utf8.RuneError

// ScanValue: buf 앞(공백 뒤)의 JSON 값 하나를 읽어 끝 위치를 돌려준다(StreamDeserializer).
// eof=true면 값이 끝나기 전에 버퍼가 끝남. perr는 그 밖의 구문 오류 문구(위치 포함).
func ScanValue(buf []byte) (end int, perr string, eof bool) {
	p := &jparser{s: buf}
	v, err := p.value()
	if err != nil {
		if strings.HasPrefix(err.Msg, "EOF while") {
			return 0, "", true
		}
		return 0, fmt.Sprintf("%s at line %d column %d", err.Msg, err.Line, err.Col), false
	}
	return v.end, "", false
}
