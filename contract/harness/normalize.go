package harness

import (
	"encoding/json"
	"net/http"
	"regexp"
	"sort"
	"strings"
)

// Snap: 응답 + 부수효과 스냅샷(골든 단위).
type Snap struct {
	Status  int                 `json:"status"`
	Headers map[string][]string `json:"headers,omitempty"`
	Body    any                 `json:"body,omitempty"`
	Hostd   []Recorded          `json:"hostd,omitempty"`
	Podman  []Recorded          `json:"podman,omitempty"`
	Extra   map[string]any      `json:"extra,omitempty"`
}

// 비교하는 응답 헤더(그 밖은 프레임워크 차이라 제외: date, content-length, vary …).
var snapHeaders = []string{"Content-Type", "Location", "Set-Cookie", "Allow", "Cache-Control",
	"Content-Disposition", "X-Accel-Buffering", "X-Content-Type-Options", "Www-Authenticate"}

// Masker: 비결정 값 치환 규칙.
type Masker struct {
	pairs []struct{ re *regexp.Regexp; rep string }
	lits  []struct{ from, to string }
	funcs []func(string) string
}

// Func: 임의 후처리(정규식으로 안 되는 정렬 등).
func (m *Masker) Func(f func(string) string) *Masker {
	m.funcs = append(m.funcs, f)
	return m
}

// SortAdjacent: 정규식에 맞는 조각이 연달아 나오는 구간을 정렬한다
// (Rust HashMap 순회 순서처럼 정해지지 않은 순서를 비교에서 뺄 때).
func SortAdjacent(pattern string) func(string) string {
	re := regexp.MustCompile(pattern)
	return func(s string) string {
		locs := re.FindAllStringIndex(s, -1)
		if len(locs) < 2 {
			return s
		}
		var b strings.Builder
		last := 0
		for i := 0; i < len(locs); {
			j := i
			for j+1 < len(locs) && locs[j+1][0] == locs[j][1] {
				j++
			}
			b.WriteString(s[last:locs[i][0]])
			parts := make([]string, 0, j-i+1)
			for k := i; k <= j; k++ {
				parts = append(parts, s[locs[k][0]:locs[k][1]])
			}
			sort.Strings(parts)
			b.WriteString(strings.Join(parts, ""))
			last = locs[j][1]
			i = j + 1
		}
		b.WriteString(s[last:])
		return b.String()
	}
}

func (m *Masker) Lit(from, to string) *Masker {
	if from != "" {
		m.lits = append(m.lits, struct{ from, to string }{from, to})
	}
	return m
}

func (m *Masker) Re(pattern, rep string) *Masker {
	m.pairs = append(m.pairs, struct {
		re  *regexp.Regexp
		rep string
	}{regexp.MustCompile(pattern), rep})
	return m
}

func (m *Masker) Apply(s string) string {
	if m == nil {
		return s
	}
	for _, l := range m.lits {
		s = strings.ReplaceAll(s, l.from, l.to)
	}
	for _, p := range m.pairs {
		s = p.re.ReplaceAllString(s, p.rep)
	}
	for _, f := range m.funcs {
		s = f(s)
	}
	return s
}

// Masker: 앱 인스턴스별 경로·포트 치환이 들어간 기본 규칙.
func (a *App) Masker() *Masker {
	m := &Masker{}
	m.Lit(a.Root, "<ROOT>").Lit(a.Dir, "<DIR>").Lit("127.0.0.1:"+a.Port, "<HOST>")
	return m
}

var (
	wsBetweenTags = regexp.MustCompile(`>\s+<`)
	wsRun         = regexp.MustCompile(`\s+`)
)

// NormHTML: 템플릿 엔진 공백 차이를 없앤다(태그 사이 공백 제거, 연속 공백 1칸).
func NormHTML(s string) string {
	s = wsBetweenTags.ReplaceAllString(s, "><")
	s = wsRun.ReplaceAllString(s, " ")
	return strings.TrimSpace(s)
}

// SnapResp: 응답을 Snap으로. HTML은 정규화, JSON은 파싱, 나머지는 문자열.
func SnapResp(r *Resp, m *Masker) Snap {
	s := Snap{Status: r.Status, Headers: map[string][]string{}}
	for _, h := range snapHeaders {
		if vs := r.Header.Values(h); len(vs) > 0 {
			out := make([]string, len(vs))
			for i, v := range vs {
				out[i] = m.Apply(v)
			}
			if h == "Set-Cookie" {
				sort.Strings(out)
			}
			s.Headers[h] = out
		}
	}
	if len(s.Headers) == 0 {
		s.Headers = nil
	}
	s.Body = BodyValue(r.Header, r.Body, m)
	return s
}

// BodyValue: 바디를 비교 가능한 값으로.
func BodyValue(h http.Header, body []byte, m *Masker) any {
	if len(body) == 0 {
		return nil
	}
	ct := h.Get("Content-Type")
	if strings.Contains(ct, "html") {
		// 공백 정규화 뒤에 마스크(정렬 규칙이 태그 사이 공백에 영향받지 않게)
		return m.Apply(NormHTML(string(body)))
	}
	text := m.Apply(string(body))
	switch {
	case strings.Contains(ct, "json") && !strings.Contains(ct, "ndjson"):
		var v any
		if json.Unmarshal([]byte(text), &v) == nil {
			return map[string]any{"json": v}
		}
		return text
	case strings.Contains(ct, "html"):
		return NormHTML(text)
	default:
		return text
	}
}

// MaskRecords: 기록된 요청의 경로·바디에도 마스크를 적용한다.
func MaskRecords(in []Recorded, m *Masker) []Recorded {
	out := make([]Recorded, len(in))
	for i, r := range in {
		r.Path = m.Apply(r.Path)
		if r.Body != nil {
			b, _ := json.Marshal(r.Body)
			var v any
			json.Unmarshal([]byte(m.Apply(string(b))), &v)
			r.Body = v
		}
		out[i] = r
	}
	return Sorted(out)
}

// Capture: 응답과 그동안 hostd/Podman이 받은 요청을 한 Snap으로 묶는다.
func (a *App) Capture(r *Resp, m *Masker) Snap {
	if m == nil {
		m = a.Masker()
	}
	s := SnapResp(r, m)
	if a.Hostd != nil {
		s.Hostd = MaskRecords(a.Hostd.Take(), m)
	}
	if a.Podman != nil {
		s.Podman = MaskRecords(a.Podman.Take(), m)
	}
	return s
}

// Reset: 기록된 요청을 버린다(로그인 등 준비 단계 이후 호출).
func (a *App) Reset() {
	if a.Hostd != nil {
		a.Hostd.Take()
	}
	if a.Podman != nil {
		a.Podman.Take()
	}
}
