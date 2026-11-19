package httpx

import (
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"
)

// BodyLimit: axum DefaultBodyLimit(2MB).
const BodyLimit = 2 * 1024 * 1024

// ── 응답 ─────────────────────────────────────────────────────────────────────

func Text(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(msg)))
	w.WriteHeader(status)
	io.WriteString(w, msg)
}

// JSON: serde_json 직렬화로 응답.
func JSON(w http.ResponseWriter, status int, v any) {
	b := MarshalJSON(v)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

// RawJSONResp: 이미 직렬화된 JSON 문자열.
func RawJSONResp(w http.ResponseWriter, status int, s string) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(s)))
	w.WriteHeader(status)
	io.WriteString(w, s)
}

func HTML(w http.ResponseWriter, status int, s string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(s)))
	w.WriteHeader(status)
	io.WriteString(w, s)
}

func validHeaderValue(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 && c != '\t' || c == 0x7f {
			return false
		}
	}
	return true
}

// Abort: axum 핸들러 panic처럼 응답 없이 연결을 끊는다.
func Abort() { panic(http.ErrAbortHandler) }

// RedirectTo: axum Redirect::to(303). 헤더 값이 유효하지 않으면 axum처럼 panic(연결 끊김).
func RedirectTo(w http.ResponseWriter, loc string) { redirect(w, loc, http.StatusSeeOther) }

// Found: 302 리다이렉트(session::redirect).
func Found(w http.ResponseWriter, loc string) { redirect(w, loc, http.StatusFound) }

func redirect(w http.ResponseWriter, loc string, status int) {
	if !validHeaderValue(loc) {
		Abort()
	}
	w.Header().Set("Location", loc)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

// ── 본문 읽기 ─────────────────────────────────────────────────────────────────

var errTooLarge = errors.New("length limit exceeded")

func readBody(r *http.Request) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r.Body, BodyLimit+1))
	if err != nil {
		return nil, err
	}
	if len(b) > BodyLimit {
		return nil, errTooLarge
	}
	return b, nil
}

func bufferErr(w http.ResponseWriter, err error) {
	if err == errTooLarge {
		Text(w, http.StatusRequestEntityTooLarge, "Failed to buffer the request body: length limit exceeded")
		return
	}
	Text(w, http.StatusBadRequest, "Failed to buffer the request body: "+err.Error())
}

func mediaType(r *http.Request) string {
	mt, _, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil {
		return ""
	}
	return mt
}

// Json: axum Json<T>. 실패하면 응답을 쓰고 ok=false.
func Json(w http.ResponseWriter, r *http.Request, structName string, fields []Field) (map[string]any, bool) {
	b, ok := jsonBody(w, r)
	if !ok {
		return nil, false
	}
	v, jerr := DecodeStruct(b, structName, fields)
	if jerr != nil {
		writeJSONErr(w, jerr)
		return nil, false
	}
	return v, true
}

// JsonValue: axum Json<serde_json::Value>.
func JsonValue(w http.ResponseWriter, r *http.Request) (any, bool) {
	b, ok := jsonBody(w, r)
	if !ok {
		return nil, false
	}
	v, jerr := DecodeValue(b)
	if jerr != nil {
		writeJSONErr(w, jerr)
		return nil, false
	}
	return v, true
}

func jsonBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	mt := mediaType(r)
	if !(mt == "application/json" || strings.HasPrefix(mt, "application/") && strings.HasSuffix(mt, "+json")) {
		Text(w, http.StatusUnsupportedMediaType, "Expected request with `Content-Type: application/json`")
		return nil, false
	}
	b, err := readBody(r)
	if err != nil {
		bufferErr(w, err)
		return nil, false
	}
	return b, true
}

func writeJSONErr(w http.ResponseWriter, e *JSONError) {
	if e.Syntax {
		Text(w, http.StatusBadRequest, e.Error())
		return
	}
	Text(w, http.StatusUnprocessableEntity, e.Error())
}

// ── urlencoded(Form·Query) ───────────────────────────────────────────────────

// FormDecode: serde_urlencoded의 퍼센트·+ 해석.
func FormDecode(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '+':
			b = append(b, ' ')
		case c == '%' && i+2 < len(s) && isHex(s[i+1]) && isHex(s[i+2]):
			b = append(b, unhex(s[i+1])<<4|unhex(s[i+2]))
			i += 2
		default:
			b = append(b, c)
		}
	}
	return string(b)
}

func isHex(c byte) bool {
	return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F'
}

func unhex(c byte) byte {
	switch {
	case c >= '0' && c <= '9':
		return c - '0'
	case c >= 'a' && c <= 'f':
		return c - 'a' + 10
	}
	return c - 'A' + 10
}

func pairs(raw string) [][2]string {
	var out [][2]string
	for _, p := range strings.Split(raw, "&") {
		if p == "" {
			continue
		}
		k, v, _ := strings.Cut(p, "=")
		out = append(out, [2]string{FormDecode(k), FormDecode(v)})
	}
	return out
}

// UField: urlencoded 구조체 필드(String / Option<i64>).
type UField struct {
	Name    string
	Default bool // #[serde(default)] (없으면 "")
	OptInt  bool // Option<i64>
}

// decodeURL: serde_urlencoded → 값(string 또는 *int64).
func decodeURL(raw string, fields []UField) (map[string]any, string) {
	out := map[string]any{}
	for _, kv := range pairs(raw) {
		var f *UField
		for i := range fields {
			if fields[i].Name == kv[0] {
				f = &fields[i]
			}
		}
		if f == nil {
			continue
		}
		if _, dup := out[f.Name]; dup {
			return nil, "duplicate field `" + f.Name + "`"
		}
		if f.OptInt {
			n, msg := ParseI64(kv[1])
			if msg != "" {
				return nil, f.Name + ": " + msg
			}
			out[f.Name] = &n
		} else {
			out[f.Name] = kv[1]
		}
	}
	for _, f := range fields {
		if _, ok := out[f.Name]; ok {
			continue
		}
		switch {
		case f.OptInt:
			out[f.Name] = (*int64)(nil)
		case f.Default:
			out[f.Name] = ""
		default:
			return nil, "missing field `" + f.Name + "`"
		}
	}
	return out, ""
}

// ParseI64: Rust i64::from_str와 같은 오류 문구.
func ParseI64(s string) (int64, string) {
	if s == "" {
		return 0, "cannot parse integer from empty string"
	}
	t := s
	if t[0] == '+' || t[0] == '-' {
		t = t[1:]
		if t == "" {
			return 0, "invalid digit found in string"
		}
	}
	for i := 0; i < len(t); i++ {
		if t[i] < '0' || t[i] > '9' {
			return 0, "invalid digit found in string"
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		if s[0] == '-' {
			return 0, "number too small to fit in target type"
		}
		return 0, "number too large to fit in target type"
	}
	return n, ""
}

// Form: axum Form<T>(POST 본문). 실패하면 응답을 쓰고 ok=false.
func Form(w http.ResponseWriter, r *http.Request, fields []UField) (map[string]any, bool) {
	var raw string
	if r.Method == http.MethodGet || r.Method == http.MethodHead {
		raw = r.URL.RawQuery
	} else {
		if mediaType(r) != "application/x-www-form-urlencoded" {
			Text(w, http.StatusUnsupportedMediaType, "Form requests must have `Content-Type: application/x-www-form-urlencoded`")
			return nil, false
		}
		b, err := readBody(r)
		if err != nil {
			bufferErr(w, err)
			return nil, false
		}
		raw = string(b)
	}
	v, msg := decodeURL(raw, fields)
	if msg != "" {
		Text(w, http.StatusUnprocessableEntity, "Failed to deserialize form body: "+msg)
		return nil, false
	}
	return v, true
}

// Query: axum Query<T>.
func Query(w http.ResponseWriter, r *http.Request, fields []UField) (map[string]any, bool) {
	v, msg := decodeURL(r.URL.RawQuery, fields)
	if msg != "" {
		Text(w, http.StatusBadRequest, "Failed to deserialize query string: "+msg)
		return nil, false
	}
	return v, true
}

// PathI64: axum Path<i64>.
func PathI64(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	s := Param(r, name)
	n, msg := ParseI64(s)
	if msg != "" {
		Text(w, http.StatusBadRequest, fmt.Sprintf("Invalid URL: Cannot parse `%s` to a `i64`", s))
		return 0, false
	}
	return n, true
}

// ── multipart ────────────────────────────────────────────────────────────────

const MultipartErr = "Error parsing `multipart/form-data` request"

// Multipart: axum Multipart 추출기.
type Multipart struct {
	mr *multipart.Reader
}

// Part: 필드 하나.
type Part struct {
	Name     string
	FileName *string
	p        *multipart.Part
}

type limitedBody struct {
	r io.Reader
	n int64
}

func (l *limitedBody) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errTooLarge
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// NewMultipart: 경계가 없으면 400 "Invalid `boundary` …".
func NewMultipart(w http.ResponseWriter, r *http.Request) (*Multipart, bool) {
	mt, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
	if err != nil || mt != "multipart/form-data" || params["boundary"] == "" {
		Text(w, http.StatusBadRequest, "Invalid `boundary` for `multipart/form-data` request")
		return nil, false
	}
	return &Multipart{mr: multipart.NewReader(&limitedBody{r: r.Body, n: BodyLimit}, params["boundary"])}, true
}

// Next: 다음 필드(없으면 nil, nil). 오류 문구는 axum MultipartError.
func (m *Multipart) Next() (*Part, error) {
	p, err := m.mr.NextRawPart()
	if err == io.EOF {
		return nil, nil
	}
	if err != nil {
		return nil, errors.New(MultipartErr)
	}
	pt := &Part{p: p}
	if _, params, err := mime.ParseMediaType(p.Header.Get("Content-Disposition")); err == nil {
		pt.Name = params["name"]
		if fn, ok := params["filename"]; ok {
			pt.FileName = &fn
		}
	}
	return pt, nil
}

// Bytes: 필드 내용.
func (p *Part) Bytes() ([]byte, error) {
	b, err := io.ReadAll(p.p)
	if err != nil {
		return nil, errors.New(MultipartErr)
	}
	return b, nil
}

// Text: 필드 내용(UTF-8 아닌 바이트는 multer처럼 오류).
func (p *Part) Text() (string, error) {
	b, err := p.Bytes()
	if err != nil {
		return "", err
	}
	return string(b), nil
}
