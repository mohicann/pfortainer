package pf

import (
	"log"
	"net/http"
	"strings"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func strPtr(s string) *string { return &s }

func (a *App) loginPage(w http.ResponseWriter, r *http.Request) {
	page(w, 200, "login.html", map[string]any{"error": (*string)(nil)})
}

var loginForm = []httpx.UField{{Name: "username"}, {Name: "password"}}

func setCookieRedirect(w http.ResponseWriter, loc string, status int, cookies ...string) {
	for _, c := range cookies {
		w.Header().Add("Set-Cookie", c)
	}
	w.Header().Set("Location", loc)
	w.Header().Set("Content-Length", "0")
	w.WriteHeader(status)
}

func (a *App) login(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, loginForm)
	if !ok {
		return
	}
	username := strings.TrimSpace(f["username"].(string))
	u, err := a.DB.VerifyPassword(username, f["password"].(string))
	if err != nil {
		log.Printf("login: db error: %v", err)
		httpx.Text(w, 500, "internal error")
		return
	}
	if u == nil {
		page(w, http.StatusUnauthorized, "login.html", map[string]any{"error": strPtr("사용자명 또는 비밀번호가 올바르지 않습니다.")})
		return
	}
	_, enabled, _ := a.DB.GetUserTOTP(username)
	if enabled {
		setCookieRedirect(w, "/login/totp", http.StatusSeeOther, preAuthCookieHeader(a.Cfg.SessionSecret, username))
		return
	}
	setCookieRedirect(w, "/", http.StatusSeeOther, authCookieHeader(a.Cfg.SessionSecret, u.Username, u.Role))
}

func (a *App) totpPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := preAuthUser(r, a.Cfg.SessionSecret); !ok {
		httpx.Found(w, "/login")
		return
	}
	page(w, 200, "login_totp.html", map[string]any{"error": (*string)(nil)})
}

func (a *App) totpVerify(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "code"}})
	if !ok {
		return
	}
	username, ok := preAuthUser(r, a.Cfg.SessionSecret)
	if !ok {
		httpx.Found(w, "/login")
		return
	}
	code := strings.TrimSpace(f["code"].(string))
	secret, _, err := a.DB.GetUserTOTP(username)
	if err != nil {
		secret = ""
	}
	if secret == "" || !verifyTOTP(secret, code) {
		page(w, http.StatusUnauthorized, "login_totp.html", map[string]any{"error": strPtr("인증 코드가 올바르지 않습니다.")})
		return
	}
	u, err := a.DB.GetUser(username)
	if err != nil || u == nil {
		httpx.Found(w, "/login")
		return
	}
	setCookieRedirect(w, "/", http.StatusSeeOther, authCookieHeader(a.Cfg.SessionSecret, u.Username, u.Role), clearPreAuthCookieHeader())
}

func (a *App) logout(w http.ResponseWriter, r *http.Request) {
	setCookieRedirect(w, "/login", http.StatusFound, clearAuthCookieHeader())
}
