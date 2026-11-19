package harness

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// Stub: 외부 HTTP(웹훅 등) 스텁. 프록시(HTTP_PROXY)로도 쓸 수 있게 절대 URI 요청을 그대로 받는다.
type Stub struct {
	URL string

	mu      sync.Mutex
	status  int
	body    string
	records []StubReq
	srv     *httptest.Server
}

type StubReq struct {
	Method      string `json:"method"`
	URI         string `json:"uri"` // 프록시 요청이면 절대 URL
	Host        string `json:"host"`
	ContentType string `json:"content_type,omitempty"`
	Body        any    `json:"body,omitempty"`
}

func NewStub(t testing.TB) *Stub {
	s := &Stub{status: 200, body: "ok"}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		rec := StubReq{Method: r.Method, URI: r.RequestURI, Host: r.Host, ContentType: r.Header.Get("Content-Type")}
		var v any
		if json.Unmarshal(raw, &v) == nil {
			rec.Body = v
		} else if len(raw) > 0 {
			rec.Body = string(raw)
		}
		s.mu.Lock()
		s.records = append(s.records, rec)
		st, b := s.status, s.body
		s.mu.Unlock()
		w.WriteHeader(st)
		io.WriteString(w, b)
	}))
	s.URL = s.srv.URL
	t.Cleanup(s.srv.Close)
	return s
}

func (s *Stub) Respond(status int, body string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.status, s.body = status, body
}

func (s *Stub) Take() []StubReq {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.records
	s.records = nil
	return out
}
