package harness

import (
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

// Reply: 가짜 소켓 서버(hostd/Podman)의 응답 한 건.
type Reply struct {
	Status      int      `json:"status"`
	ContentType string   `json:"content_type,omitempty"`
	Body        string   `json:"body"`
	Chunks      []string `json:"chunks,omitempty"` // 있으면 Body 대신 한 줄씩 flush하며 보냄(스트리밍)
	DelayMs     int      `json:"delay_ms,omitempty"`
}

// Recorded: 가짜 소켓 서버가 받은 요청.
type Recorded struct {
	Method      string `json:"method"`
	Path        string `json:"path"` // RequestURI 그대로(쿼리 포함)
	ContentType string `json:"content_type,omitempty"`
	Body        any    `json:"body,omitempty"` // JSON이면 파싱한 값, 아니면 문자열
}

// FakeSock: Unix 소켓 HTTP 서버. 요청을 기록하고 "METHOD URI" 키로 응답한다.
type FakeSock struct {
	Path string

	mu       sync.Mutex
	replies  map[string]Reply
	fallback func(r *http.Request) Reply
	records  []Recorded
	ln       net.Listener
	srv      *http.Server
}

// NewFakeSock: path에 소켓을 연다. fallback은 등록 안 된 요청의 응답.
func NewFakeSock(t testing.TB, path string, fallback func(r *http.Request) Reply) *FakeSock {
	t.Helper()
	_ = os.Remove(path)
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("fake socket listen %s: %v", path, err)
	}
	f := &FakeSock{Path: path, replies: map[string]Reply{}, fallback: fallback, ln: ln}
	f.srv = &http.Server{Handler: http.HandlerFunc(f.serve)}
	go f.srv.Serve(ln)
	t.Cleanup(func() { f.srv.Close(); os.Remove(path) })
	return f
}

// Set: "GET /zfs-list" 처럼 메서드와 URI(쿼리 포함)로 응답을 등록한다.
// 쿼리 없는 키는 쿼리가 붙은 요청에도 맞는다(정확한 키가 우선).
func (f *FakeSock) Set(key string, r Reply) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replies[key] = r
}

func (f *FakeSock) SetJSON(key string, v any) {
	b, _ := json.Marshal(v)
	f.Set(key, Reply{Status: 200, ContentType: "application/json", Body: string(b)})
}

func (f *FakeSock) Remove(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.replies, key)
}

func (f *FakeSock) serve(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(r.Body)
	rec := Recorded{Method: r.Method, Path: r.RequestURI, ContentType: r.Header.Get("Content-Type")}
	if len(raw) > 0 {
		var v any
		if json.Unmarshal(raw, &v) == nil {
			rec.Body = v
		} else {
			rec.Body = string(raw)
		}
	}
	f.mu.Lock()
	f.records = append(f.records, rec)
	rep, ok := f.replies[r.Method+" "+r.RequestURI]
	if !ok {
		rep, ok = f.replies[r.Method+" "+r.URL.Path]
	}
	f.mu.Unlock()
	if !ok {
		if f.fallback != nil {
			rep = f.fallback(r)
		} else {
			rep = Reply{Status: 404, Body: "fixture missing: " + r.Method + " " + r.RequestURI}
		}
	}
	if rep.DelayMs > 0 {
		time.Sleep(time.Duration(rep.DelayMs) * time.Millisecond)
	}
	if rep.ContentType != "" {
		w.Header().Set("Content-Type", rep.ContentType)
	}
	if rep.Status == 0 {
		rep.Status = 200
	}
	w.WriteHeader(rep.Status)
	if len(rep.Chunks) > 0 {
		fl, _ := w.(http.Flusher)
		for _, c := range rep.Chunks {
			io.WriteString(w, c)
			if fl != nil {
				fl.Flush()
			}
		}
		return
	}
	io.WriteString(w, rep.Body)
}

// Take: 지금까지 기록된 요청을 돌려주고 비운다. 정렬은 하지 않는다.
func (f *FakeSock) Take() []Recorded {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := f.records
	f.records = nil
	return out
}

// Sorted: 요청 목록을 비교용으로 정렬한 사본(병렬 호출 순서 차이 제거).
func Sorted(in []Recorded) []Recorded {
	out := append([]Recorded(nil), in...)
	sort.SliceStable(out, func(i, j int) bool {
		a, _ := json.Marshal(out[i])
		b, _ := json.Marshal(out[j])
		return string(a) < string(b)
	})
	return out
}

// HostdDefault: 가짜 hostd의 기본 응답. POST는 성공, GET은 "fixture missing" 500.
func HostdDefault(r *http.Request) Reply {
	if r.Method == http.MethodPost {
		return Reply{Status: 200, ContentType: "application/json", Body: `{"success":true}`}
	}
	return Reply{Status: 500, Body: "fixture missing: " + r.Method + " " + r.RequestURI}
}

// PodmanDefault: 가짜 Podman의 기본 응답. 액션(POST/DELETE)은 204, 조회는 404 JSON.
func PodmanDefault(r *http.Request) Reply {
	if r.Method == http.MethodPost || r.Method == http.MethodDelete {
		return Reply{Status: 204}
	}
	return Reply{Status: 404, ContentType: "application/json",
		Body: `{"cause":"no such object","message":"fixture missing: ` + strings.ReplaceAll(r.RequestURI, `"`, `'`) + `","response":404}`}
}
