package tests

import (
	"os"
	"sort"
	"strings"
	"testing"

	h "github.com/mohicann/pfortainer/contract/harness"
)

// TestP0LoginPage: 하네스 동작 확인 — 앱 기동, 로그인 페이지, 부트스트랩 admin 로그인.
func TestP0LoginPage(t *testing.T) {
	a := h.StartApp(t, h.AppOpts{})
	a.DumpLogsOnFail()
	r := a.Client("").Get(t, "/login")
	h.Golden(t, "p0/login_page", a.Capture(r, nil))
	ck := a.Login(t, "admin", h.AdminPassword)
	if !strings.HasPrefix(ck, "pfsession=admin:admin.") {
		t.Fatalf("세션 쿠키 형식: %s", ck)
	}
	if ck != h.Cookie("admin", "admin") {
		t.Fatalf("하네스 서명과 다름: %s", ck)
	}
}

// TestDiscover: (도구) DISCOVER=1이면 관리자 로그인 후 모든 viewer GET을 부르고
// hostd/Podman이 받은 요청을 출력한다. 픽스처 수집 목록을 만들 때만 쓴다.
func TestDiscover(t *testing.T) {
	if os.Getenv("DISCOVER") == "" {
		t.Skip("DISCOVER=1 일 때만")
	}
	a := h.StartApp(t, h.AppOpts{})
	c := a.Client(h.Cookie("admin", "admin"))
	seen := map[string]bool{}
	for _, r := range Routes {
		if r.Method != "GET" || r.Layer == "public" {
			continue
		}
		resp := c.Get(t, r.Sample)
		for _, rec := range a.Hostd.Take() {
			seen["hostd  "+rec.Method+" "+rec.Path] = true
		}
		for _, rec := range a.Podman.Take() {
			seen["podman "+rec.Method+" "+rec.Path] = true
		}
		t.Logf("%-40s %d", r.Sample, resp.Status)
	}
	var keys []string
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Log(k)
	}
}
