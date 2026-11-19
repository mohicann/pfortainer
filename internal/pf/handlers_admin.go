package pf

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func fmtShort(t time.Time) string {
	t = toLocal(t)
	return fmt.Sprintf("%02d-%02d %02d:%02d", int(t.Month()), t.Day(), t.Hour(), t.Minute())
}

func fmtDate(t time.Time) string {
	t = toLocal(t)
	return fmt.Sprintf("%04d-%02d-%02d", t.Year(), int(t.Month()), t.Day())
}

func qstr(m map[string]any, k string) string { s, _ := m[k].(string); return s }

var msgQuery = []httpx.UField{{Name: "error", Default: true}, {Name: "success", Default: true}}

func (a *App) profileCtx(u SessionUser, enabled bool, secret, uri, success, errMsg string) map[string]any {
	c := base("", u)
	c["agent_mode"] = a.HC.AgentMode()
	c["totp_enabled"] = enabled
	c["setup_secret"] = secret
	c["otp_uri"] = uri
	c["success"] = success
	c["error"] = errMsg
	return c
}

func (a *App) profilePage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "success", Default: true}})
	if !ok {
		return
	}
	_, enabled, _ := a.DB.GetUserTOTP(u.Username)
	page(w, 200, "profile.html", a.profileCtx(u, enabled, "", "", qstr(q, "success"), ""))
}

func (a *App) totpSetupBegin(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	secret := generateTOTPSecret()
	if err := a.DB.SetTOTPSecret(u.Username, secret); err != nil {
		httpx.Text(w, 500, "db error: "+err.Error())
		return
	}
	page(w, 200, "profile.html", a.profileCtx(u, false, secret, totpAuthURI(secret, u.Username), "", ""))
}

func (a *App) totpSetupConfirm(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "secret", Default: true}, {Name: "code", Default: true}})
	if !ok {
		return
	}
	secret := qstr(f, "secret")
	code := strings.TrimSpace(qstr(f, "code"))
	if !verifyTOTP(secret, code) {
		page(w, 200, "profile.html", a.profileCtx(u, false, secret, totpAuthURI(secret, u.Username), "", "인증 코드가 올바르지 않습니다. 다시 시도하세요."))
		return
	}
	if err := a.DB.SetTOTPSecret(u.Username, secret); err != nil {
		httpx.Text(w, 500, "db error: "+err.Error())
		return
	}
	a.DB.EnableTOTP(u.Username)
	httpx.RedirectTo(w, "/profile?success=2FA가+활성화되었습니다.")
}

func (a *App) totpDisable(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "code", Default: true}})
	if !ok {
		return
	}
	code := strings.TrimSpace(qstr(f, "code"))
	secret, enabled, _ := a.DB.GetUserTOTP(u.Username)
	if !enabled || !verifyTOTP(secret, code) {
		page(w, 200, "profile.html", a.profileCtx(u, true, "", "", "", "인증 코드가 올바르지 않습니다."))
		return
	}
	a.DB.DisableTOTP(u.Username)
	httpx.RedirectTo(w, "/profile?success=2FA가+비활성화되었습니다.")
}

type adminUserRow struct {
	Username     string `json:"username"`
	RoleDisplay  string `json:"role_display"`
	CreatedAtFmt string `json:"created_at_fmt"`
}

func (a *App) adminUsersPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	users, err := a.DB.ListUsers()
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	}
	rows := []adminUserRow{}
	for _, x := range users {
		rows = append(rows, adminUserRow{x.Username, x.Role.Str(), fmtDate(x.CreatedAt)})
	}
	c := base("admin-users", u)
	c["users"] = rows
	c["error"] = errMsg
	c["success"] = ""
	page(w, 200, "admin-users.html", c)
}

func (a *App) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "username", Default: true}, {Name: "password", Default: true}, {Name: "role", Default: true}})
	if !ok {
		return
	}
	username := strings.TrimSpace(qstr(f, "username"))
	pw := qstr(f, "password")
	if username == "" || pw == "" {
		httpx.RedirectTo(w, "/admin/users?error=사용자명과+비밀번호를+입력하세요.")
		return
	}
	if len(pw) < 8 {
		httpx.RedirectTo(w, "/admin/users?error=비밀번호는+8자+이상이어야+합니다.")
		return
	}
	role, ok := RoleFromStr(qstr(f, "role"))
	if !ok {
		role = RoleViewer
	}
	if err := a.DB.CreateUser(username, pw, role); err != nil {
		httpx.RedirectTo(w, "/admin/users?error=생성+실패:+"+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/admin/users?success="+urlenc(username)+"+생성+완료")
}

func (a *App) adminUserUpdateRole(w http.ResponseWriter, r *http.Request) {
	me, ok := a.auth(w, r)
	if !ok {
		return
	}
	username := httpx.Param(r, "username")
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "role", Default: true}})
	if !ok {
		return
	}
	role, ok := RoleFromStr(qstr(f, "role"))
	if !ok {
		role = RoleViewer
	}
	if username == me.Username && role != RoleAdmin {
		httpx.RedirectTo(w, "/admin/users?error=자신의+관리자+권한을+제거할+수+없습니다")
		return
	}
	if err := a.DB.UpdateRole(username, role); err != nil {
		httpx.RedirectTo(w, "/admin/users?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/admin/users?success=역할+변경+완료")
}

func (a *App) adminUserUpdatePassword(w http.ResponseWriter, r *http.Request) {
	username := httpx.Param(r, "username")
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "password", Default: true}})
	if !ok {
		return
	}
	pw := qstr(f, "password")
	if len(pw) < 8 {
		httpx.RedirectTo(w, "/admin/users?error=비밀번호는+8자+이상이어야+합니다")
		return
	}
	if err := a.DB.UpdatePassword(username, pw); err != nil {
		httpx.RedirectTo(w, "/admin/users?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/admin/users?success=비밀번호+변경+완료")
}

func (a *App) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	me, ok := a.auth(w, r)
	if !ok {
		return
	}
	username := httpx.Param(r, "username")
	if username == me.Username {
		httpx.RedirectTo(w, "/admin/users?error=자신의+계정을+삭제할+수+없습니다")
		return
	}
	if err := a.DB.DeleteUser(username); err != nil {
		httpx.RedirectTo(w, "/admin/users?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/admin/users?success="+urlenc(username)+"+삭제+완료")
}
