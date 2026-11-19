// Package httpx는 axum 라우팅·추출기 동작(계약 테스트가 고정한 것)을 흉내 낸다.
//   - 404/405는 빈 바디, 405에는 Allow(GET이면 HEAD 포함, 등록 순서)
//   - 정적 세그먼트가 {param}보다 우선, 끝 슬래시·대소문자 접기 없음
//   - Path·Query·Form·Json·Multipart 거절 문구(axum 0.8 + serde)
package httpx

import (
	"context"
	"net/http"
	"net/url"
	"strings"
)

// Middleware: 라우트 묶음에 씌우는 계층(require_operator 등).
type Middleware func(http.Handler) http.Handler

type route struct {
	method  string
	pattern string
	segs    []string
	handler http.Handler
}

type Router struct{ routes []*route }

func NewRouter() *Router { return &Router{} }

// Handle: method+pattern 등록. 등록 순서가 Allow 헤더 순서다.
func (rt *Router) Handle(method, pattern string, h http.Handler, mw ...Middleware) {
	for i := len(mw) - 1; i >= 0; i-- {
		h = mw[i](h)
	}
	rt.routes = append(rt.routes, &route{method: method, pattern: pattern, segs: split(pattern), handler: h})
}

func (rt *Router) HandleFunc(method, pattern string, h http.HandlerFunc, mw ...Middleware) {
	rt.Handle(method, pattern, h, mw...)
}

func split(p string) []string { return strings.Split(strings.TrimPrefix(p, "/"), "/") }

type paramsKey struct{}

// Param: 퍼센트 디코딩한 경로 파라미터.
func Param(r *http.Request, name string) string {
	m, _ := r.Context().Value(paramsKey{}).(map[string]string)
	return m[name]
}

func (rt *Router) match(rawPath string) ([]*route, map[string]string) {
	segs := split(rawPath)
	var best []*route
	var bestRank []bool
	var bestParams map[string]string
	for _, r := range rt.routes {
		if len(r.segs) != len(segs) {
			continue
		}
		params := map[string]string{}
		rank := make([]bool, len(segs))
		ok := true
		for i, s := range r.segs {
			raw := segs[i]
			if strings.HasPrefix(s, "{") && strings.HasSuffix(s, "}") {
				if raw == "" {
					ok = false
					break
				}
				v, err := url.PathUnescape(raw)
				if err != nil {
					ok = false
					break
				}
				params[s[1:len(s)-1]] = v
				continue
			}
			if raw != s {
				ok = false
				break
			}
			rank[i] = true
		}
		if !ok {
			continue
		}
		switch cmp := compareRank(rank, bestRank); {
		case best == nil || cmp > 0:
			best, bestRank, bestParams = []*route{r}, rank, params
		case cmp == 0 && r.pattern == best[0].pattern:
			best = append(best, r)
		}
	}
	return best, bestParams
}

func compareRank(a, b []bool) int {
	if b == nil {
		return 1
	}
	for i := range a {
		if a[i] != b[i] {
			if a[i] {
				return 1
			}
			return -1
		}
	}
	return 0
}

func allow(routes []*route) string {
	var ms []string
	seen := map[string]bool{}
	add := func(m string) {
		if !seen[m] {
			seen[m] = true
			ms = append(ms, m)
		}
	}
	for _, r := range routes {
		add(r.method)
		if r.method == http.MethodGet {
			add(http.MethodHead)
		}
	}
	return strings.Join(ms, ",")
}

func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	routes, params := rt.match(r.URL.EscapedPath())
	hd := w.Header()
	if len(routes) == 0 {
		hd.Set("Content-Length", "0")
		w.WriteHeader(http.StatusNotFound)
		return
	}
	method := r.Method
	if method == http.MethodHead {
		method = http.MethodGet
	}
	for _, rr := range routes {
		if rr.method == method {
			ctx := context.WithValue(r.Context(), paramsKey{}, params)
			rr.handler.ServeHTTP(w, r.WithContext(ctx))
			return
		}
	}
	hd.Set("Allow", allow(routes))
	hd.Set("Content-Length", "0")
	w.WriteHeader(http.StatusMethodNotAllowed)
}
