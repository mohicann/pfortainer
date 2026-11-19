package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	h "github.com/mohicann/pfortainer/contract/harness"
)

// TestRouteInventory: routes.go가 Rust 라우터(main.rs, hostd.rs)와 같은지.
func TestRouteInventory(t *testing.T) {
	mainRs, err := os.ReadFile(filepath.Join(h.RustRepo(), "src", "main.rs"))
	if err != nil {
		t.Skipf("Rust 저장소 없음: %v", err)
	}
	s := string(mainRs)
	idx := func(k string) int {
		i := strings.Index(s, k)
		if i < 0 {
			t.Fatalf("main.rs에 %q 없음", k)
		}
		return i
	}
	groups := map[string]string{
		"viewer":   s[idx("let viewer_routes"):idx("let operator_routes")],
		"operator": s[idx("let operator_routes"):idx("let admin_routes")],
		"admin":    s[idx("let admin_routes"):idx("let app = Router")],
		"public":   s[idx("let app = Router"):],
	}
	methRe := regexp.MustCompile(`\b(get|post|put|delete)\(`)
	pathRe := regexp.MustCompile(`^\s*"([^"]+)"`)
	var rust []string
	for g, txt := range groups {
		txt = regexp.MustCompile(`(?m)^\s*//.*$`).ReplaceAllString(txt, "")
		chunks := strings.Split(txt, ".route(")
		for _, ch := range chunks[1:] {
			if i := strings.Index(ch, ".route_layer("); i >= 0 {
				ch = ch[:i]
			}
			if i := strings.Index(ch, ".with_state("); i >= 0 {
				ch = ch[:i]
			}
			pm := pathRe.FindStringSubmatch(ch)
			if pm == nil {
				continue
			}
			for _, mm := range methRe.FindAllStringSubmatch(ch, -1) {
				rust = append(rust, g+" "+strings.ToUpper(mm[1])+" "+pm[1])
			}
		}
	}
	var ours []string
	for _, r := range Routes {
		ours = append(ours, r.Layer+" "+r.Method+" "+r.Path)
	}
	sort.Strings(rust)
	sort.Strings(ours)
	if strings.Join(rust, "\n") != strings.Join(ours, "\n") {
		t.Errorf("라우트 목록 불일치\n%s", h.LineDiff(strings.Join(rust, "\n"), strings.Join(ours, "\n")))
	}

	hostdRs, _ := os.ReadFile(filepath.Join(h.RustRepo(), "src", "hostd.rs"))
	var rh, oh []string
	for _, m := range regexp.MustCompile(`\.route\("([^"]+)", (get|post)\(`).FindAllStringSubmatch(string(hostdRs), -1) {
		rh = append(rh, strings.ToUpper(m[2])+" "+m[1])
	}
	for _, r := range HostdRoutes {
		oh = append(oh, r.Method+" "+r.Path)
	}
	sort.Strings(rh)
	sort.Strings(oh)
	if strings.Join(rh, "\n") != strings.Join(oh, "\n") {
		t.Errorf("hostd 라우트 불일치\n%s", h.LineDiff(strings.Join(rh, "\n"), strings.Join(oh, "\n")))
	}
}

// 권한표 칸: 자격 증명 종류.
var authzCreds = []struct{ Name, Cookie string }{
	{"anon", ""},
	{"bad_sig", "pfsession=admin:admin.0000000000000000000000000000000000000000000000000000000000000000"},
	{"other_secret", h.SignCookie("not-the-secret", "admin", "admin")},
	{"unknown_role", h.Cookie("admin", "root")},
	{"empty_user", h.Cookie("", "admin")},
	{"ghost_admin", h.Cookie("ghost", "admin")}, // DB에 없는 사용자 — 쿠키만 믿는지
	{"viewer", h.Cookie("viewer1", "viewer")},
	{"operator", h.Cookie("operator1", "operator")},
	{"admin", h.Cookie("admin", "admin")},
}

type authzCell struct {
	Status   int    `json:"status"`
	Location string `json:"location,omitempty"`
	CT       string `json:"content_type,omitempty"`
	Body     string `json:"body,omitempty"` // 2xx·3xx가 아닐 때만(앞 300바이트)
	Hostd    int    `json:"hostd_calls"`    // 핸들러까지 갔는지 보는 지표
	Podman   int    `json:"podman_calls"`
}

// TestAuthzMatrix: 라우트 × 자격 증명. 라우트마다 새 앱(부수효과 격리).
// 바디 없는 요청을 보내므로 통과 칸은 핸들러의 입력 오류까지 기록된다.
func TestAuthzMatrix(t *testing.T) {
	for _, r := range Routes {
		r := r
		t.Run(h.Slug(r.Method+"_"+r.Path), func(t *testing.T) {
			t.Parallel()
			a := h.StartAppWithCaptures(t, h.AppOpts{})
			a.StdUsers(t)
			a.Exec(t, `INSERT INTO schedules (id,type,target,frequency,retention,prefix,enabled,created_at) VALUES (1,'snapshot','zdata/apps','daily',7,'auto',1,1763500000)`)
			a.Exec(t, `INSERT INTO replication_tasks (id,name,source_dataset,target_path,created_at) VALUES (1,'r1','zdata/apps','zbak/apps',1763500000)`)
			cells := map[string]authzCell{}
			for _, c := range authzCreds {
				a.Reset()
				cl := a.Client(c.Cookie)
				var resp *h.Resp
				switch r.Method {
				case "GET":
					resp = cl.Get(t, r.Sample)
				case "PUT":
					resp = cl.Do(t, h.Req{Method: "PUT", Path: r.Sample, Body: strings.NewReader(""), CT: "application/json"})
				default:
					resp = cl.Do(t, h.Req{Method: r.Method, Path: r.Sample, Body: strings.NewReader(""), CT: "application/x-www-form-urlencoded"})
				}
				cell := authzCell{Status: resp.Status, Location: resp.Header.Get("Location"), CT: resp.Header.Get("Content-Type")}
				if resp.Status >= 400 {
					b := resp.Text()
					if len(b) > 300 {
						b = b[:300]
					}
					cell.Body = b
				}
				cell.Hostd = len(a.Hostd.Take())
				cell.Podman = len(a.Podman.Take())
				cells[c.Name] = cell
			}
			h.Golden(t, "p1/authz/"+h.Slug(r.Method+"_"+r.Path), cells)
		})
	}
}

// TestRouter: 라우터 자체 동작.
func TestRouter(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	admin := a.Client(h.Cookie("admin", "admin"))
	anon := a.Client("")
	cases := []struct {
		name string
		c    *h.Client
		req  h.Req
	}{
		{"unknown_path", admin, h.Req{Method: "GET", Path: "/nope"}},
		{"unknown_path_anon", anon, h.Req{Method: "GET", Path: "/nope"}},
		{"trailing_slash", admin, h.Req{Method: "GET", Path: "/containers/"}},
		{"double_slash", admin, h.Req{Method: "GET", Path: "//containers"}},
		{"method_not_allowed_get_route", admin, h.Req{Method: "POST", Path: "/system"}},
		{"method_not_allowed_post_route", admin, h.Req{Method: "GET", Path: "/api/snapshots/create"}},
		{"method_not_allowed_delete", admin, h.Req{Method: "DELETE", Path: "/login"}},
		{"put_on_get_post", admin, h.Req{Method: "PUT", Path: "/admin/users"}},
		{"head_login", anon, h.Req{Method: "HEAD", Path: "/login"}},
		{"head_redirect_root", admin, h.Req{Method: "HEAD", Path: "/"}},
		{"options_login", anon, h.Req{Method: "OPTIONS", Path: "/login"}},
		{"root_redirect", admin, h.Req{Method: "GET", Path: "/"}},
		{"root_redirect_anon", anon, h.Req{Method: "GET", Path: "/"}},
		{"bastille_redirect", anon, h.Req{Method: "GET", Path: "/bastille"}},
		{"query_on_root", admin, h.Req{Method: "GET", Path: "/?x=1"}},
		{"encoded_path_param", admin, h.Req{Method: "GET", Path: "/appjail/a%2Fb"}},
		{"login_wrong_ct", anon, h.Req{Method: "POST", Path: "/login", Body: strings.NewReader(`{"username":"admin"}`), CT: "application/json"}},
		{"login_no_ct", anon, h.Req{Method: "POST", Path: "/login", Body: strings.NewReader("username=admin&password=x")}},
		{"login_missing_field", anon, h.Req{Method: "POST", Path: "/login", Body: strings.NewReader("username=admin"), CT: "application/x-www-form-urlencoded"}},
		{"cookie_among_others", nil, h.Req{Method: "GET", Path: "/profile", Header: map[string]string{"Cookie": "a=1; " + h.Cookie("admin", "admin") + "; b=2"}}},
		{"cookie_spaces", nil, h.Req{Method: "GET", Path: "/profile", Header: map[string]string{"Cookie": "  " + h.Cookie("admin", "admin") + "  "}}},
		{"cookie_dup_first_bad", nil, h.Req{Method: "GET", Path: "/profile", Header: map[string]string{"Cookie": "pfsession=x; " + h.Cookie("admin", "admin")}}},
		{"cookie_username_with_colon", nil, h.Req{Method: "GET", Path: "/profile", Header: map[string]string{"Cookie": h.SignCookie(h.SessionSecret, "ad:min", "admin")}}},
		{"cookie_username_with_dot", nil, h.Req{Method: "GET", Path: "/profile", Header: map[string]string{"Cookie": h.SignCookie(h.SessionSecret, "ad.min", "admin")}}},
		{"cookie_uppercase_sig", nil, h.Req{Method: "GET", Path: "/profile", Header: map[string]string{"Cookie": strings.ToUpper(h.Cookie("admin", "admin")[len("pfsession=admin:admin."):]) }}},
		{"json_body_on_form_route", admin, h.Req{Method: "POST", Path: "/schedules", Body: strings.NewReader(`{}`), CT: "application/json"}},
		{"form_body_on_json_route", admin, h.Req{Method: "PUT", Path: "/api/desktop/prefs", Body: strings.NewReader(`a=1`), CT: "application/x-www-form-urlencoded"}},
	}
	for _, c := range cases {
		cl := c.c
		if cl == nil {
			cl = anon
		}
		if c.name == "cookie_uppercase_sig" {
			c.req.Header = map[string]string{"Cookie": "pfsession=admin:admin." + strings.ToUpper(strings.TrimPrefix(h.Cookie("admin", "admin"), "pfsession=admin:admin."))}
		}
		a.Reset()
		resp := cl.Do(t, c.req)
		snap := a.Capture(resp, nil)
		if s, ok := snap.Body.(string); ok && len(s) > 400 {
			snap.Body = fmt.Sprintf("<%d bytes html>", len(s))
		}
		h.Golden(t, "p1/router/"+c.name, snap)
	}
}
