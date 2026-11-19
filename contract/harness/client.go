package harness

import (
	"bytes"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Resp: 응답 캡처.
type Resp struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
}

func (r *Resp) Text() string { return string(r.Body) }

func (r *Resp) JSON(t testing.TB) any {
	t.Helper()
	var v any
	if err := json.Unmarshal(r.Body, &v); err != nil {
		t.Fatalf("JSON 아님 (%d): %v\n%s", r.Status, err, r.Body)
	}
	return v
}

// Client: 리다이렉트를 따라가지 않는 HTTP 클라이언트. Cookie는 그대로 보낸다.
type Client struct {
	Base   string
	Cookie string // "pfsession=..." (여러 개면 "; "로 연결)
	hc     *http.Client
}

func (a *App) Client(cookie string) *Client {
	return &Client{Base: a.URL, Cookie: cookie, hc: &http.Client{
		Timeout:       30 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

type Req struct {
	Method  string
	Path    string
	Header  map[string]string
	Body    io.Reader
	CT      string
}

func (c *Client) Do(t testing.TB, r Req) *Resp {
	t.Helper()
	req, err := http.NewRequest(r.Method, c.Base+r.Path, r.Body)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", "contract-test")
	if c.Cookie != "" {
		req.Header.Set("Cookie", c.Cookie)
	}
	if r.CT != "" {
		req.Header.Set("Content-Type", r.CT)
	}
	for k, v := range r.Header {
		req.Header.Set(k, v)
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", r.Method, r.Path, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return &Resp{Status: resp.StatusCode, Header: resp.Header, Body: b, Elapsed: time.Since(start)}
}

func (c *Client) Get(t testing.TB, path string) *Resp {
	t.Helper()
	return c.Do(t, Req{Method: "GET", Path: path})
}

// PostForm: application/x-www-form-urlencoded. 순서를 지키려고 [][2]string.
func (c *Client) PostForm(t testing.TB, path string, kv ...string) *Resp {
	t.Helper()
	return c.Do(t, Req{Method: "POST", Path: path, Body: strings.NewReader(FormEncode(kv...)), CT: "application/x-www-form-urlencoded"})
}

func FormEncode(kv ...string) string {
	var parts []string
	for i := 0; i+1 < len(kv); i += 2 {
		parts = append(parts, url.QueryEscape(kv[i])+"="+url.QueryEscape(kv[i+1]))
	}
	return strings.Join(parts, "&")
}

func (c *Client) PostJSON(t testing.TB, path string, v any) *Resp {
	t.Helper()
	var body io.Reader
	switch x := v.(type) {
	case string:
		body = strings.NewReader(x)
	default:
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	return c.Do(t, Req{Method: "POST", Path: path, Body: body, CT: "application/json"})
}

func (c *Client) PutJSON(t testing.TB, path string, v any) *Resp {
	t.Helper()
	var body io.Reader
	switch x := v.(type) {
	case string:
		body = strings.NewReader(x)
	default:
		b, _ := json.Marshal(v)
		body = bytes.NewReader(b)
	}
	return c.Do(t, Req{Method: "PUT", Path: path, Body: body, CT: "application/json"})
}

// MultipartPart: 멀티파트 한 칸. Filename이 있으면 파일 칸.
type MultipartPart struct {
	Name, Filename, Content string
}

func (c *Client) PostMultipart(t testing.TB, path string, parts ...MultipartPart) *Resp {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	w.SetBoundary("contractboundary0123456789")
	for _, p := range parts {
		if p.Filename != "" {
			fw, _ := w.CreateFormFile(p.Name, p.Filename)
			io.WriteString(fw, p.Content)
		} else {
			w.WriteField(p.Name, p.Content)
		}
	}
	w.Close()
	return c.Do(t, Req{Method: "POST", Path: path, Body: &buf, CT: w.FormDataContentType()})
}
