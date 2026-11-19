package tests

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

var (
	totpSecretRe = regexp.MustCompile(`\b[A-Z2-7]{32}\b`)
	preauthExpRe = regexp.MustCompile(`\|\d{10}\|[0-9a-f]{64}`)
)

func authMasker(a *h.App) *h.Masker {
	m := a.Masker()
	m.Re(totpSecretRe.String(), "<TOTP_SECRET>")
	m.Re(preauthExpRe.String(), "|<exp>|<sig>")
	m.Lit(time.Now().Format("2006-01-02"), "<today>")
	return m
}

type step struct {
	Name string  `json:"name"`
	Snap h.Snap  `json:"snap"`
	DB   any     `json:"db,omitempty"`
}

func shortBody(s h.Snap) h.Snap {
	if b, ok := s.Body.(string); ok && len(b) > 6000 {
		s.Body = b[:6000] + fmt.Sprintf("…<%d bytes>", len(b))
	}
	return s
}

// TestAuthLogin: 로그인·로그아웃·부트스트랩 admin.
func TestAuthLogin(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	a.StdUsers(t)
	m := authMasker(a)
	anon := a.Client("")
	var steps []step
	add := func(name string, r *h.Resp) {
		steps = append(steps, step{Name: name, Snap: shortBody(a.Capture(r, m))})
	}
	steps = append(steps, step{Name: "bootstrap_users", DB: a.Users(t)})
	add("login_ok_admin", anon.PostForm(t, "/login", "username", "admin", "password", h.AdminPassword))
	add("login_ok_trim_username", anon.PostForm(t, "/login", "username", "  operator1 ", "password", "operator-pw-1234"))
	add("login_password_not_trimmed", anon.PostForm(t, "/login", "username", "operator1", "password", " operator-pw-1234"))
	add("login_wrong_password", anon.PostForm(t, "/login", "username", "admin", "password", "nope"))
	add("login_unknown_user", anon.PostForm(t, "/login", "username", "ghost", "password", "nope"))
	add("login_empty", anon.PostForm(t, "/login", "username", "", "password", ""))
	add("login_case_sensitive", anon.PostForm(t, "/login", "username", "ADMIN", "password", h.AdminPassword))
	add("login_extra_fields", anon.PostForm(t, "/login", "username", "viewer1", "password", "viewer-pw-1234", "next", "/x"))
	add("login_page_while_logged_in", a.Client(h.Cookie("admin", "admin")).Get(t, "/login"))
	add("logout", a.Client(h.Cookie("admin", "admin")).Get(t, "/logout"))
	add("logout_anon", anon.Get(t, "/logout"))
	h.Golden(t, "p2/login", steps)
}

// TestAuthBootstrap: 사용자가 있으면 admin을 만들지 않음 + ADMIN_PASSWORD 기본값.
func TestAuthBootstrap(t *testing.T) {
	out := map[string]any{}
	a := h.StartApp(t, h.AppOpts{Env: map[string]string{"ADMIN_PASSWORD": "\x00unset"}})
	r := a.Client("").PostForm(t, "/login", "username", "admin", "password", "changeme")
	out["default_password_login"] = r.Status
	out["users_default"] = a.Users(t)

	b := h.StartApp(t, h.AppOpts{BeforeStart: func(b *h.App) {
		// 사용자 한 명이 있는 DB(마이그레이션 완료 상태)를 미리 만든다
		db, err := sql.Open("sqlite", b.ConfigDB)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		for _, q := range []string{
			`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, role INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, totp_secret TEXT NOT NULL DEFAULT '', totp_enabled INTEGER NOT NULL DEFAULT 0)`,
			`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL)`,
			`INSERT INTO meta VALUES ('users_role_migrated','1',1763500000)`,
			`INSERT INTO users (username,password_hash,role,created_at) VALUES ('solo','$2b$04$abcdefghijklmnopqrstuuJ9vY0bOqWm8kq3C3n0o9C4sJt3ZyC1e',2,1763500000)`,
		} {
			if _, err := db.Exec(q); err != nil {
				t.Fatal(err)
			}
		}
	}})
	out["users_existing"] = b.Users(t)
	h.Golden(t, "p2/bootstrap", out)
}

// TestRoleMigration: 옛 스키마(role TEXT, totp 칼럼 없음) → INTEGER 변환.
func TestRoleMigration(t *testing.T) {
	for _, tc := range []struct {
		name  string
		users []string
	}{
		{"with_rows", []string{"('admin','$2b$04$x','admin',1700000000)", "('op','$2b$04$y','operator',1700000001)", "('v','$2b$04$z','viewer',1700000002)", "('weird','$2b$04$w','superuser',1700000003)"}},
		{"empty_table", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := h.StartApp(t, h.AppOpts{BeforeStart: func(a *h.App) {
				db, err := sql.Open("sqlite", a.ConfigDB)
				if err != nil {
					t.Fatal(err)
				}
				defer db.Close()
				qs := []string{`CREATE TABLE users (id INTEGER PRIMARY KEY, username TEXT UNIQUE NOT NULL, password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'viewer', created_at INTEGER NOT NULL)`}
				for _, u := range tc.users {
					qs = append(qs, `INSERT INTO users (username,password_hash,role,created_at) VALUES `+u)
				}
				for _, q := range qs {
					if _, err := db.Exec(q); err != nil {
						t.Fatal(err)
					}
				}
			}})
			db := a.DB(t)
			out := map[string]any{
				"users":      h.MaskBcrypt(h.MaskNow(h.Rows(t, db, `SELECT id, username, password_hash, role, typeof(role) AS role_type, created_at, totp_secret, totp_enabled FROM users ORDER BY id`), "created_at")),
				"table_info": h.Rows(t, db, `SELECT name, type, "notnull", dflt_value, pk FROM pragma_table_info('users') ORDER BY cid`),
				"meta":       h.MaskNow(h.Rows(t, db, `SELECT key, value, updated_at FROM meta ORDER BY key`), "updated_at"),
				"tables":     h.Rows(t, db, `SELECT name FROM sqlite_master WHERE type='table' ORDER BY name`),
			}
			h.Golden(t, "p2/role_migration_"+tc.name, out)
		})
	}
}

// TestSchema: 새 DB에서 만들어지는 config.db/metrics.db 스키마(전체 DDL).
func TestSchema(t *testing.T) {
	a := h.StartApp(t, h.AppOpts{})
	db := a.DB(t)
	mdb, err := sql.Open("sqlite", a.MetricsDB)
	if err != nil {
		t.Fatal(err)
	}
	defer mdb.Close()
	norm := func(rows []map[string]any) []map[string]any {
		for _, r := range rows {
			if s, ok := r["sql"].(string); ok {
				r["sql"] = strings.Join(strings.Fields(s), " ")
			}
		}
		return rows
	}
	q := `SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`
	out := map[string]any{
		"config_db":           norm(h.Rows(t, db, q)),
		"config_meta":         h.MaskNow(h.Rows(t, db, `SELECT key, value, updated_at FROM meta ORDER BY key`), "updated_at"),
		"config_journal_mode": h.Rows(t, db, `PRAGMA journal_mode`),
		"metrics_db":          norm(h.Rows(t, mdb, q)),
		"metrics_journal_mode": h.Rows(t, mdb, `PRAGMA journal_mode`),
	}
	h.Golden(t, "p2/schema", out)
	_ = os.Remove(filepath.Join(a.Dir, "x"))
}

// TestAuthTOTPLogin: 2FA가 켜진 사용자의 로그인 흐름.
func TestAuthTOTPLogin(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	const secret = "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP"
	a.AddUser(t, "totpuser", "totp-pw-12345", "operator", secret)
	m := authMasker(a)
	anon := a.Client("")
	var steps []step
	add := func(name string, r *h.Resp) {
		steps = append(steps, step{Name: name, Snap: shortBody(a.Capture(r, m))})
	}
	r := anon.PostForm(t, "/login", "username", "totpuser", "password", "totp-pw-12345")
	add("login_password_ok", r)
	var pre string
	for _, sc := range r.Header.Values("Set-Cookie") {
		if strings.HasPrefix(sc, "pfpreauth=") {
			pre = strings.SplitN(sc, ";", 2)[0]
		}
	}
	if pre == "" {
		t.Fatal("pfpreauth 쿠키 없음")
	}
	pc := a.Client(pre)
	add("totp_page_no_preauth", anon.Get(t, "/login/totp"))
	add("totp_page_with_preauth", pc.Get(t, "/login/totp"))
	add("totp_page_expired", a.Client(h.PreAuthCookie(h.SessionSecret, "totpuser", time.Now().Unix()-1)).Get(t, "/login/totp"))
	add("totp_page_bad_sig", a.Client("pfpreauth=totpuser|9999999999|00").Get(t, "/login/totp"))
	add("totp_page_two_parts", a.Client("pfpreauth=totpuser|9999999999").Get(t, "/login/totp"))
	add("totp_verify_no_preauth", anon.PostForm(t, "/login/totp", "code", "000000"))
	add("totp_verify_wrong", pc.PostForm(t, "/login/totp", "code", "000000"))
	add("totp_verify_prev_window", pc.PostForm(t, "/login/totp", "code", " "+h.TOTPCode(secret, time.Now().Add(-30*time.Second))+" "))
	add("totp_verify_ok", pc.PostForm(t, "/login/totp", "code", h.TOTPCode(secret, time.Now())))
	add("totp_verify_far_window", pc.PostForm(t, "/login/totp", "code", h.TOTPCode(secret, time.Now().Add(-120*time.Second))))
	// 2FA 켜진 사용자가 아닌데 preauth 쿠키를 만든 경우
	a.AddUser(t, "plain", "plain-pw-12345", "viewer", "")
	add("totp_verify_user_without_secret", a.Client(h.PreAuthCookie(h.SessionSecret, "plain", time.Now().Unix()+300)).PostForm(t, "/login/totp", "code", "123456"))
	add("totp_verify_deleted_user", a.Client(h.PreAuthCookie(h.SessionSecret, "ghost", time.Now().Unix()+300)).PostForm(t, "/login/totp", "code", "123456"))
	h.Golden(t, "p2/totp_login", steps)
}

// TestProfile2FA: 프로필 페이지와 2FA 설정/해제.
func TestProfile2FA(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	a.StdUsers(t)
	m := authMasker(a)
	var steps []step
	add := func(name string, r *h.Resp) {
		steps = append(steps, step{Name: name, Snap: shortBody(a.Capture(r, m)), DB: a.Users(t)})
	}
	for _, role := range []string{"viewer", "operator", "admin"} {
		user := map[string]string{"viewer": "viewer1", "operator": "operator1", "admin": "admin"}[role]
		r := a.Client(h.Cookie(user, role)).Get(t, "/profile")
		steps = append(steps, step{Name: "profile_" + role, Snap: a.Capture(r, m)})
	}
	vc := a.Client(h.Cookie("viewer1", "viewer"))
	add("profile_success_msg", vc.Get(t, "/profile?success=%3Cb%3Ehi%3C/b%3E"))
	add("begin", vc.PostForm(t, "/profile/2fa/begin"))
	secret := a.Rows1(t, `SELECT totp_secret FROM users WHERE username='viewer1'`)
	add("enable_wrong_code", vc.PostForm(t, "/profile/2fa/enable", "secret", secret, "code", "000000"))
	add("enable_other_secret", vc.PostForm(t, "/profile/2fa/enable", "secret", "JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", "code", h.TOTPCode("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", time.Now())))
	add("profile_enabled", vc.Get(t, "/profile"))
	add("disable_wrong", vc.PostForm(t, "/profile/2fa/disable", "code", "000000"))
	add("disable_ok", vc.PostForm(t, "/profile/2fa/disable", "code", h.TOTPCode("JBSWY3DPEHPK3PXPJBSWY3DPEHPK3PXP", time.Now())))
	add("disable_when_off", vc.PostForm(t, "/profile/2fa/disable", "code", "000000"))
	add("enable_bad_base32", vc.PostForm(t, "/profile/2fa/enable", "secret", "not base32!", "code", "000000"))
	add("begin_ghost_user", a.Client(h.Cookie("ghost", "viewer")).PostForm(t, "/profile/2fa/begin"))
	add("profile_ghost_user", a.Client(h.Cookie("ghost", "viewer")).Get(t, "/profile"))
	h.Golden(t, "p2/profile_2fa", steps)
}

// TestAdminUsers: 관리자 사용자 관리.
func TestAdminUsers(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	a.StdUsers(t)
	m := authMasker(a)
	ac := a.Client(h.Cookie("admin", "admin"))
	var steps []step
	add := func(name string, r *h.Resp) {
		steps = append(steps, step{Name: name, Snap: shortBody(a.Capture(r, m)), DB: a.Users(t)})
	}
	add("page", ac.Get(t, "/admin/users"))
	add("page_with_msgs_ignored", ac.Get(t, "/admin/users?success=x&error=y"))
	add("create_empty", ac.PostForm(t, "/admin/users", "username", " ", "password", "longenough"))
	add("create_short_pw", ac.PostForm(t, "/admin/users", "username", "newbie", "password", "short"))
	add("create_ok_operator", ac.PostForm(t, "/admin/users", "username", " newbie ", "password", "longenough", "role", "operator"))
	add("create_dup", ac.PostForm(t, "/admin/users", "username", "newbie", "password", "longenough", "role", "admin"))
	add("create_bad_role_defaults_viewer", ac.PostForm(t, "/admin/users", "username", "r2", "password", "longenough", "role", "root"))
	add("create_no_role", ac.PostForm(t, "/admin/users", "username", "r3", "password", "longenough"))
	add("create_weird_name", ac.PostForm(t, "/admin/users", "username", "한글 이름<>&", "password", "12345678", "role", "viewer"))
	add("create_pw_multibyte_len", ac.PostForm(t, "/admin/users", "username", "mb", "password", "가나다", "role", "viewer"))
	add("login_new_user", a.Client("").PostForm(t, "/login", "username", "newbie", "password", "longenough"))
	add("role_change", ac.PostForm(t, "/admin/users/newbie/role", "role", "admin"))
	add("role_change_bad", ac.PostForm(t, "/admin/users/newbie/role", "role", "bogus"))
	add("role_self_demote", ac.PostForm(t, "/admin/users/admin/role", "role", "operator"))
	add("role_self_admin", ac.PostForm(t, "/admin/users/admin/role", "role", "admin"))
	add("role_missing_user", ac.PostForm(t, "/admin/users/ghost/role", "role", "viewer"))
	add("password_short", ac.PostForm(t, "/admin/users/newbie/password", "password", "1234567"))
	add("password_ok", ac.PostForm(t, "/admin/users/newbie/password", "password", "changed-pw-1"))
	add("password_missing_user", ac.PostForm(t, "/admin/users/ghost/password", "password", "changed-pw-1"))
	add("login_after_pw_change", a.Client("").PostForm(t, "/login", "username", "newbie", "password", "changed-pw-1"))
	a.Exec(t, `INSERT INTO desktop_prefs VALUES ('newbie','{}',1763500000)`)
	add("delete_self", ac.PostForm(t, "/admin/users/admin/delete"))
	add("delete_ok", ac.PostForm(t, "/admin/users/newbie/delete"))
	steps = append(steps, step{Name: "desktop_prefs_after_delete", DB: h.Rows(t, a.DB(t), `SELECT username FROM desktop_prefs`)})
	add("delete_missing", ac.PostForm(t, "/admin/users/newbie/delete"))
	add("delete_encoded_name", ac.PostForm(t, "/admin/users/%ED%95%9C%EA%B8%80%20%EC%9D%B4%EB%A6%84%3C%3E&/delete"))
	// 역할을 DB에서 바꿔도 쿠키의 역할이 그대로 쓰인다(세션은 무상태)
	a.Exec(t, `UPDATE users SET role=1 WHERE username='operator1'`)
	add("stale_cookie_role", a.Client(h.Cookie("operator1", "operator")).PostForm(t, "/nfs/reload"))
	h.Golden(t, "p2/admin_users", steps)
}
