package tests

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

const (
	cListURI = "/v5.0.0/libpod/containers/json?all=true"
	cInfoURI = "/v5.0.0/libpod/info"
	pgID     = "17f4688b30722916e8a38fece386fa3bded5c39e9f8d9019bc40349ede0a8669"
)

// containerList: 수집한 목록을 시간·상태를 바꿔 결정적으로 만든다.
func containerList(t *testing.T) []map[string]any {
	var cs []map[string]any
	if err := json.Unmarshal([]byte(h.CaptureBody(t, "podman", cListURI)), &cs); err != nil {
		t.Fatal(err)
	}
	now := time.Now().Unix()
	base := cs[0]
	clone := func(id, name, state string, started, exited, code int64) map[string]any {
		b, _ := json.Marshal(base)
		var c map[string]any
		json.Unmarshal(b, &c)
		c["Id"] = id
		c["Names"] = []string{name}
		c["State"] = state
		c["StartedAt"] = started
		c["ExitedAt"] = exited
		c["ExitCode"] = code
		return c
	}
	cs[0]["StartedAt"] = now - 30
	cs[1]["StartedAt"] = now - 5*60 - 5
	cs[2]["State"] = "exited"
	cs[2]["ExitCode"] = 137
	cs[2]["ExitedAt"] = now - 3*3600 - 5
	cs[3]["State"] = "paused"
	cs = append(cs,
		clone("aaaa000000000000000000000000000000000000000000000000000000000001", "created_one", "created", 0, 0, 0),
		clone("aaaa000000000000000000000000000000000000000000000000000000000002", "stopped_no_exit", "stopped", 0, 0, 1),
		clone("aaaa000000000000000000000000000000000000000000000000000000000003", "long_running", "running", now-2*86400-60, 0, 0),
		clone("aaaa000000000000000000000000000000000000000000000000000000000004", "<b>esc&\"name'</b>", "Removing", 0, 0, 0),
		clone("aaaa000000000000000000000000000000000000000000000000000000000005", "future_start", "running", now+500, 0, 0),
	)
	return cs
}

// podmanMasker: 라벨(HashMap) 순서는 Rust에서도 매번 달라서 정렬해 비교한다.
func podmanMasker(a *h.App) *h.Masker {
	return a.Masker().Func(h.SortAdjacent(`<div class="mono" style="font-size:.8rem">[^<]*</div>`))
}

func podmanApp(t *testing.T, opts h.AppOpts) *h.App {
	a := h.StartAppWithCaptures(t, opts)
	if a.Podman != nil {
		a.Podman.SetJSON("GET "+cListURI, containerList(t))
	}
	return a
}

func snapStep(a *h.App, name string, r *h.Resp, m *h.Masker) step {
	return step{Name: name, Snap: a.Capture(r, m)}
}

// TestContainersPages: 목록·상세·새로 만들기·시스템 페이지.
func TestContainersPages(t *testing.T) {
	a := podmanApp(t, h.AppOpts{})
	m := podmanMasker(a)
	c := a.Client(h.Cookie("viewer1", "viewer"))
	var steps []step
	steps = append(steps, snapStep(a, "list", c.Get(t, "/containers"), m))
	steps = append(steps, snapStep(a, "list_as_admin", a.Client(h.Cookie("admin", "admin")).Get(t, "/containers"), m))
	steps = append(steps, snapStep(a, "detail", c.Get(t, "/containers/"+pgID), m))
	steps = append(steps, snapStep(a, "detail_404", c.Get(t, "/containers/nope"), m))
	steps = append(steps, snapStep(a, "new", c.Get(t, "/containers/new"), m))
	steps = append(steps, snapStep(a, "system", c.Get(t, "/system"), m))
	steps = append(steps, snapStep(a, "system_stats", c.Get(t, "/api/system/stats"), m))

	// inspect 5xx → 목록 데이터로 대체
	a.Podman.Set("GET /v5.0.0/libpod/containers/"+pgID+"/json", h.Reply{Status: 500, ContentType: "application/json", Body: `{"cause":"panic","message":"runtime error: invalid memory address","response":500}`})
	steps = append(steps, snapStep(a, "detail_inspect_500_fallback", c.Get(t, "/containers/"+pgID), m))
	a.Podman.Set("GET /v5.0.0/libpod/containers/zzz/json", h.Reply{Status: 502, Body: "bad gateway text"})
	steps = append(steps, snapStep(a, "detail_inspect_502_not_in_list", c.Get(t, "/containers/zzz"), m))
	a.Podman.Set("GET /v5.0.0/libpod/containers/yyy/json", h.Reply{Status: 409, ContentType: "application/json", Body: `{"message":"conflict here","response":409}`})
	steps = append(steps, snapStep(a, "detail_inspect_409", c.Get(t, "/containers/yyy"), m))
	a.Podman.Set("GET /v5.0.0/libpod/containers/www/json", h.Reply{Status: 200, ContentType: "application/json", Body: `{"Id":"www"}`})
	steps = append(steps, snapStep(a, "detail_minimal_json", c.Get(t, "/containers/www"), m))

	// 목록 오류들
	a.Podman.SetJSON("GET "+cListURI, []any{})
	steps = append(steps, snapStep(a, "list_empty", c.Get(t, "/containers"), m))
	a.Podman.Set("GET "+cListURI, h.Reply{Status: 200, ContentType: "application/json", Body: `not json`})
	steps = append(steps, snapStep(a, "list_bad_json", c.Get(t, "/containers"), m))
	a.Podman.Set("GET "+cListURI, h.Reply{Status: 500, ContentType: "application/json", Body: `{"cause":"x","message":"boom <b>","response":500}`})
	steps = append(steps, snapStep(a, "list_500", c.Get(t, "/containers"), m))
	a.Podman.Set("GET "+cListURI, h.Reply{Status: 500, Body: `plain error`})
	steps = append(steps, snapStep(a, "list_500_plain", c.Get(t, "/containers"), m))
	a.Podman.Set("GET "+cListURI, h.Reply{Status: 200, ContentType: "application/json", Body: `null`})
	steps = append(steps, snapStep(a, "list_null", c.Get(t, "/containers"), m))

	a.Podman.Set("GET "+cInfoURI, h.Reply{Status: 500, ContentType: "application/json", Body: `{"message":"info failed","response":500}`})
	steps = append(steps, snapStep(a, "system_500", c.Get(t, "/system"), m))
	steps = append(steps, snapStep(a, "system_stats_500", c.Get(t, "/api/system/stats"), m))
	a.Podman.Set("GET "+cInfoURI, h.Reply{Status: 200, ContentType: "application/json", Body: `{}`})
	steps = append(steps, snapStep(a, "system_empty", c.Get(t, "/system"), m))
	steps = append(steps, snapStep(a, "system_stats_empty", c.Get(t, "/api/system/stats"), m))
	h.Golden(t, "p3/pages", steps)
}

// TestContainersNoPodman: Podman 소켓이 없을 때.
func TestContainersNoPodman(t *testing.T) {
	a := podmanApp(t, h.AppOpts{NoPodman: true})
	m := podmanMasker(a)
	c := a.Client(h.Cookie("operator1", "operator"))
	var steps []step
	for _, p := range []string{"/containers", "/containers/" + pgID, "/system", "/api/system/stats"} {
		steps = append(steps, snapStep(a, "get "+p, c.Get(t, p), m))
	}
	steps = append(steps, snapStep(a, "action_start", c.PostForm(t, "/containers/x/start"), m))
	steps = append(steps, snapStep(a, "build", c.PostMultipart(t, "/api/containers/build", h.MultipartPart{Name: "dockerfile", Content: "FROM x"}), m))
	h.Golden(t, "p3/no_podman", steps)
}

// TestContainerActions: 액션 → Docker 호환 API 호출과 오류 문구.
func TestContainerActions(t *testing.T) {
	a := podmanApp(t, h.AppOpts{})
	m := podmanMasker(a)
	c := a.Client(h.Cookie("operator1", "operator"))
	var steps []step
	for _, act := range []string{"start", "stop", "restart", "kill", "pause", "resume", "remove", "unpause", "bogus"} {
		steps = append(steps, snapStep(a, act, c.PostForm(t, "/containers/abc/"+act), m))
	}
	// 오류 응답
	a.Podman.Set("POST /containers/abc/start", h.Reply{Status: 304})
	steps = append(steps, snapStep(a, "start_304", c.PostForm(t, "/containers/abc/start"), m))
	a.Podman.Set("POST /containers/abc/start", h.Reply{Status: 404, ContentType: "application/json", Body: `{"message":"no such container","response":404}`})
	steps = append(steps, snapStep(a, "start_404", c.PostForm(t, "/containers/abc/start"), m))
	a.Podman.Set("POST /containers/abc/start", h.Reply{Status: 409, ContentType: "application/json", Body: `{"message":"already running","response":409}`})
	steps = append(steps, snapStep(a, "start_409", c.PostForm(t, "/containers/abc/start"), m))
	a.Podman.Set("POST /containers/abc/start", h.Reply{Status: 500, Body: `raw failure`})
	steps = append(steps, snapStep(a, "start_500_plain", c.PostForm(t, "/containers/abc/start"), m))
	a.Podman.Set("DELETE /containers/abc", h.Reply{Status: 409, ContentType: "application/json", Body: `{"message":"container is running","response":409}`})
	steps = append(steps, snapStep(a, "remove_409", c.PostForm(t, "/containers/abc/remove"), m))
	a.Podman.Set("DELETE /containers/abc", h.Reply{Status: 500, ContentType: "application/json", Body: `{"message":"","response":500}`})
	steps = append(steps, snapStep(a, "remove_500_empty_message", c.PostForm(t, "/containers/abc/remove"), m))
	steps = append(steps, snapStep(a, "encoded_id", c.PostForm(t, "/containers/a%20b%2Fc/stop"), m))
	steps = append(steps, snapStep(a, "get_method", c.Get(t, "/containers/abc/start"), m))
	h.Golden(t, "p3/actions", steps)
}

// TestContainerBuild: Dockerfile 빌드(멀티파트 → tar → NDJSON 스트림).
func TestContainerBuild(t *testing.T) {
	a := podmanApp(t, h.AppOpts{})
	m := podmanMasker(a)
	c := a.Client(h.Cookie("operator1", "operator"))
	df := h.MultipartPart{Name: "dockerfile", Content: "FROM alpine\nRUN echo hi\n"}
	var steps []step
	a.Podman.Set("POST /build", h.Reply{Status: 200, ContentType: "application/json", Chunks: []string{
		`{"stream":"STEP 1/2: FROM alpine\n"}`,
		`{"stream":"STEP 2/2: RUN echo hi\n"}{"stream":"hi\n"}`,
		`{"stream":"   \n"}`,
		`{"status":"Pulling fs layer","progress":"[==>   ] 1MB/3MB","id":"abc"}`,
		`{"status":"Done"}`,
		`{"stre`, `am":"split across frames\n"}`,
		`{"aux":{"ID":"sha256:123"}}`,
		`{"stream":"COMMIT myimg\n"}` + "\n",
	}})
	steps = append(steps, snapStep(a, "ok_with_tag", c.PostMultipart(t, "/api/containers/build", h.MultipartPart{Name: "tag", Content: "  my/img:1 + x "}, df, h.MultipartPart{Name: "other", Content: "ignored"}), m))
	steps = append(steps, snapStep(a, "ok_no_tag", c.PostMultipart(t, "/api/containers/build", df), m))
	steps = append(steps, snapStep(a, "dockerfile_as_file", c.PostMultipart(t, "/api/containers/build", h.MultipartPart{Name: "dockerfile", Filename: "Dockerfile", Content: "FROM busybox\n"}), m))
	steps = append(steps, snapStep(a, "empty_dockerfile", c.PostMultipart(t, "/api/containers/build", h.MultipartPart{Name: "tag", Content: "x"}, h.MultipartPart{Name: "dockerfile", Content: "  \n"}), m))
	steps = append(steps, snapStep(a, "urlencoded_body", c.PostForm(t, "/api/containers/build", "dockerfile", "FROM x"), m))
	steps = append(steps, snapStep(a, "bad_multipart", c.Do(t, h.Req{Method: "POST", Path: "/api/containers/build", Body: strings.NewReader("--zzz\r\nbroken"), CT: "multipart/form-data; boundary=zzz"}), m))

	a.Podman.Set("POST /build", h.Reply{Status: 200, ContentType: "application/json", Chunks: []string{
		`{"stream":"STEP 1/1: FROM nope\n"}`, `{"error":"pull failed: not found\n","errorDetail":{"message":"x"}}`, `{"stream":"after error\n"}`,
	}})
	steps = append(steps, snapStep(a, "stream_error", c.PostMultipart(t, "/api/containers/build", df), m))
	a.Podman.Set("POST /build", h.Reply{Status: 200, ContentType: "application/json", Chunks: []string{`{"stream":"ok\n"}`, `garbage{`}})
	steps = append(steps, snapStep(a, "stream_garbage", c.PostMultipart(t, "/api/containers/build", df), m))
	a.Podman.Set("POST /build", h.Reply{Status: 500, ContentType: "application/json", Body: `{"message":"build exploded"}` + "\n"})
	steps = append(steps, snapStep(a, "http_500", c.PostMultipart(t, "/api/containers/build", df), m))
	h.Golden(t, "p3/build", steps)
}

// TestComposeUp: hostd 경유 compose 스트림 릴레이와 오류.
func TestComposeUp(t *testing.T) {
	a := podmanApp(t, h.AppOpts{})
	m := podmanMasker(a)
	c := a.Client(h.Cookie("operator1", "operator"))
	compose := h.MultipartPart{Name: "compose", Content: "services:\n  web:\n    image: nginx\n"}
	var steps []step
	a.Hostd.Set("POST /compose-up", h.Reply{Status: 200, ContentType: "application/x-ndjson", Chunks: []string{
		`{"type":"log","line":"podman-compose version 1"}` + "\n", `{"type":"log","line":"creating web"}` + "\n", `{"type":"done","line":"0"}` + "\n",
	}})
	steps = append(steps, snapStep(a, "ok", c.PostMultipart(t, "/api/compose/up", compose, h.MultipartPart{Name: "x", Content: "y"}), m))
	steps = append(steps, snapStep(a, "compose_as_file", c.PostMultipart(t, "/api/compose/up", h.MultipartPart{Name: "compose", Filename: "docker-compose.yml", Content: "services: {}\n"}), m))
	steps = append(steps, snapStep(a, "empty", c.PostMultipart(t, "/api/compose/up", h.MultipartPart{Name: "compose", Content: " "}), m))
	steps = append(steps, snapStep(a, "no_field", c.PostMultipart(t, "/api/compose/up", h.MultipartPart{Name: "other", Content: "x"}), m))
	a.Hostd.Set("POST /compose-up", h.Reply{Status: 500, Body: "podman-compose not installed"})
	steps = append(steps, snapStep(a, "hostd_500", c.PostMultipart(t, "/api/compose/up", compose), m))
	a.Hostd.Set("POST /compose-up", h.Reply{Status: 400, Body: ""})
	steps = append(steps, snapStep(a, "hostd_400_empty", c.PostMultipart(t, "/api/compose/up", compose), m))
	h.Golden(t, "p3/compose", steps)

	// hostd 소켓이 없으면 로컬에서 podman-compose를 직접 실행(PATH에 없으므로 실패 스트림)
	b := podmanApp(t, h.AppOpts{NoHostd: true})
	r := b.Client(h.Cookie("operator1", "operator")).PostMultipart(t, "/api/compose/up", compose)
	h.Golden(t, "p3/compose_local_exec", b.Capture(r, podmanMasker(b)))
}
