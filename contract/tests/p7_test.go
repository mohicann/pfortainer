package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

// P7: 구현 간 호환. OTHER_BIN(기본 Rust)과 SUT_BIN을 섞어 실행한다.
//   - 앱(A) ↔ hostd(B) 실제 소켓 연결(가짜 없음, 명령은 shim)
//   - 같은 config.db/metrics.db를 A로 만들고 B로 열기(양방향)
//   - 세션 쿠키·pre-auth 쿠키를 상대 구현이 그대로 인정

func pairs() [][2]string {
	sut, other := h.SUTBin(), h.OtherBin()
	return [][2]string{{sut, other}, {other, sut}}
}

func pairName(p [2]string) string {
	n := func(b string) string {
		if b == h.RustBin() {
			return "rust"
		}
		return "sut"
	}
	return n(p[0]) + "_app__" + n(p[1]) + "_hostd"
}

// TestInteropAppHostd: 앱이 실제 hostd(shim 명령)를 거쳐 페이지를 그린다.
// 골든은 구현 조합과 무관하게 하나(같은 결과여야 하므로).
func TestInteropAppHostd(t *testing.T) {
	for _, p := range pairs() {
		p := p
		t.Run(pairName(p), func(t *testing.T) {
			hd := h.StartHostdBin(t, p[1], h.CmdRules(t))
			hd.InstallHostFiles(t)
			hd.Rule("zfs snapshot zdata/apps@contract-1", h.ShimRule{})
			hd.Rule("appjail restart audio", h.ShimRule{Stdout: "restarted"})
			a := h.StartApp(t, h.AppOpts{Bin: p[0], HostdSocket: hd.Sock})
			a.DumpLogsOnFail()
			m := a.Masker()
			m.Lit(hd.Root, "<HROOT>").Lit(hd.Dir, "<HDIR>")
			diskOrder(m)
			c := a.Client(h.Cookie("admin", "admin"))
			var steps []step
			for _, path := range []string{"/filesystem", "/storage", "/snapshots", "/localusers", "/shares?tab=smb", "/shares?tab=nfs", "/services", "/network", "/appjail", "/appjail/audio", "/api/diag/cmd?cmd=df", "/api/diag/cmd?cmd=mount"} {
				steps = append(steps, step{Name: "GET " + path, Snap: a.Capture(c.Get(t, path), m)})
				hd.Calls()
			}
			steps = append(steps, step{Name: "snapshot_create", Snap: a.Capture(c.PostJSON(t, "/api/snapshots/create", map[string]any{"dataset": "zdata/apps", "snapname": "contract-1"}), m), DB: hd.Calls()})
			steps = append(steps, step{Name: "appjail_restart", Snap: a.Capture(c.PostForm(t, "/appjail/audio/restart", "back", "detail"), m), DB: hd.Calls()})
			// hostd 파일 작업(실제 임시 경로)
			data := filepath.Join(hd.Dir, "data")
			os.MkdirAll(data, 0o755)
			steps = append(steps, step{Name: "file_save", Snap: a.Capture(c.PostJSON(t, "/api/files/save", map[string]any{"path": data + "/note.txt", "content": "메모 <1>\n"}), m)})
			b, _ := os.ReadFile(filepath.Join(data, "note.txt"))
			steps = append(steps, step{Name: "file_on_disk", DB: string(b)})
			steps = append(steps, step{Name: "file_download", Snap: a.Capture(c.Get(t, "/api/files/download?path="+data+"/note.txt"), m)})
			h.Golden(t, "p7/app_hostd", steps)
		})
	}
}

// TestInteropDBSwitch: A로 상태를 만든 뒤 같은 DB로 B를 띄워 그대로 읽히는지.
func TestInteropDBSwitch(t *testing.T) {
	for _, p := range pairs() {
		p := p
		t.Run(pairName(p), func(t *testing.T) {
			a := h.StartAppWithCaptures(t, h.AppOpts{Bin: p[0]})
			const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
			ac := a.Client(h.Cookie("admin", "admin"))
			ac.PostForm(t, "/admin/users", "username", "opx", "password", "opx-password", "role", "operator")
			a.AddUser(t, "totpx", "totpx-password", "viewer", secret)
			ac.PostForm(t, "/schedules", "type", "snapshot", "target", "zdata/apps", "frequency", "daily", "retention", "3")
			ac.PostForm(t, "/replications", "name", "r1", "source_dataset", "zdata/apps", "target_path", "zbak/apps", "schedule", "daily")
			ac.PostForm(t, "/alerts", "webhook_enabled", "1", "webhook_url", "http://example.invalid/hook", "check_smart", "1")
			ac.PutJSON(t, "/api/desktop/prefs", `{"icons":{"files":[1,2]}}`)
			cookie := a.Login(t, "opx", "opx-password")
			pre := a.Client("").PostForm(t, "/login", "username", "totpx", "password", "totpx-password")
			var preCookie string
			for _, sc := range pre.Header.Values("Set-Cookie") {
				if strings.HasPrefix(sc, "pfpreauth=") {
					preCookie = strings.SplitN(sc, ";", 2)[0]
				}
			}

			a.Restart(p[1]) // 같은 DB·환경, 다른 구현
			a.LoadCaptures(t)
			m := a.Masker()
			m.Func(nearNowTimes)
			diskOrder(m)
			out := map[string]any{}
			out["opx_cookie_still_valid"] = a.Client(cookie).Get(t, "/profile").Status
			out["opx_login_again"] = a.Client("").PostForm(t, "/login", "username", "opx", "password", "opx-password").Status
			out["preauth_cookie_accepted"] = a.Client(preCookie).Get(t, "/login/totp").Status
			out["totp_verify"] = a.Client(preCookie).PostForm(t, "/login/totp", "code", h.TOTPCode(secret, time.Now())).Status
			ac2 := a.Client(h.Cookie("admin", "admin"))
			for _, path := range []string{"/admin/users", "/schedules", "/replications", "/alerts", "/api/desktop/prefs"} {
				out["GET "+path] = a.Capture(ac2.Get(t, path), m)
			}
			out["users"] = a.Users(t)
			out["meta_keys"] = h.Rows(t, a.DB(t), `SELECT key FROM meta ORDER BY key`)
			h.Golden(t, "p7/db_switch", out)
		})
	}
}
