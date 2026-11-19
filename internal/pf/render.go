package pf

import (
	"embed"
	"io/fs"
	"net/http"

	"github.com/mohicann/pfortainer/internal/askama"
	"github.com/mohicann/pfortainer/internal/httpx"
)

// templates/: pfortainer-rs templates/*.html 사본(같은 파일이어야 함 — TestTemplatesMatchRust).
//
//go:embed templates/*.html
var templateFS embed.FS

var tpl = func() *askama.Set {
	sub, _ := fs.Sub(templateFS, "templates")
	return askama.NewSet(sub)
}()

// page: HtmlTemplate(...).into_response(). 렌더 실패는 500 "template render error: …".
func page(w http.ResponseWriter, status int, name string, ctx map[string]any) {
	s, err := tpl.Render(name, ctx)
	if err != nil {
		httpx.Text(w, http.StatusInternalServerError, "template render error: "+err.Error())
		return
	}
	httpx.HTML(w, status, s)
}

// base: base.html을 쓰는 페이지 공통 값.
func base(active string, u SessionUser) map[string]any {
	return map[string]any{"active_page": active, "current_user": u}
}

func isSocketErrorMsg(msg string) bool {
	for _, s := range []string{"No such file", "no such file", "Connection refused", "connection refused", "unavailable"} {
		if contains(msg, s) {
			return true
		}
	}
	return false
}

func renderErrorPage(w http.ResponseWriter, u SessionUser, msg string, unavailable bool) {
	st := http.StatusInternalServerError
	if unavailable {
		st = http.StatusServiceUnavailable
	}
	c := base("", u)
	c["podman_unavailable"] = unavailable
	c["error"] = msg
	page(w, st, "error.html", c)
}
