package pf

import (
	"net/http"
	"strings"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func (a *App) localUsersPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	errMsg := qstr(q, "error")
	st := LocalUsersStatus{Users: []LocalUser{}, Groups: []LocalGroup{}}
	if err := agentJSON(a.HC, "/localusers/status", &st); err != nil {
		errMsg = "사용자 목록 조회 실패: " + err.Error()
		st = LocalUsersStatus{Users: []LocalUser{}, Groups: []LocalGroup{}}
	}
	c := base("localusers", u)
	c["status"], c["error"], c["success"] = st, errMsg, qstr(q, "success")
	page(w, 200, "localusers.html", c)
}

func redirTab(w http.ResponseWriter, tab, kind, msg string) {
	httpx.RedirectTo(w, "/shares?"+tab+kind+"="+msg)
}

func (a *App) usersResult(w http.ResponseWriter, err error, success string) {
	if err != nil {
		httpx.RedirectTo(w, "/shares?tab=users&error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/shares?tab=users&success="+success)
}

func postSimple(hc *HostClient, path string, body any) error {
	_, err := hostPost(hc, path, body)
	return err
}

func (a *App) localUserCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "username", Default: true}, {Name: "fullname", Default: true}, {Name: "shell", Default: true}, {Name: "password", Default: true}, {Name: "smb_password", Default: true}})
	if !ok {
		return
	}
	err := postSimple(a.HC, "/localusers/create", map[string]any{"username": qstr(f, "username"), "fullname": qstr(f, "fullname"), "shell": qstr(f, "shell"), "password": qstr(f, "password"), "smb_password": qstr(f, "smb_password")})
	a.usersResult(w, err, "사용자+"+urlenc(qstr(f, "username"))+"+생성됨")
}

func (a *App) localUserDelete(w http.ResponseWriter, r *http.Request) {
	name := httpx.Param(r, "username")
	a.usersResult(w, postSimple(a.HC, "/localusers/delete", map[string]any{"username": name}), "사용자+"+urlenc(name)+"+삭제됨")
}

func (a *App) localUserSMBPasswd(w http.ResponseWriter, r *http.Request) {
	name := httpx.Param(r, "username")
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "password", Default: true}})
	if !ok {
		return
	}
	a.usersResult(w, postSimple(a.HC, "/localusers/smbpasswd", map[string]any{"username": name, "password": qstr(f, "password")}), urlenc(name)+"+SMB+비밀번호+설정됨")
}

func (a *App) localGroupCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "name", Default: true}})
	if !ok {
		return
	}
	a.usersResult(w, postSimple(a.HC, "/localgroups/create", map[string]any{"name": qstr(f, "name")}), "그룹+"+urlenc(qstr(f, "name"))+"+생성됨")
}

func (a *App) localGroupDelete(w http.ResponseWriter, r *http.Request) {
	name := httpx.Param(r, "name")
	a.usersResult(w, postSimple(a.HC, "/localgroups/delete", map[string]any{"name": name}), "그룹+"+urlenc(name)+"+삭제됨")
}

func (a *App) localGroupMember(w http.ResponseWriter, r *http.Request) {
	group := httpx.Param(r, "name")
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "username", Default: true}, {Name: "action", Default: true}})
	if !ok {
		return
	}
	a.usersResult(w, postSimple(a.HC, "/localgroups/member", map[string]any{"group": group, "username": qstr(f, "username"), "action": qstr(f, "action")}), "그룹+멤버+변경됨")
}

func (a *App) sharesPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "tab", Default: true}, {Name: "error", Default: true}, {Name: "success", Default: true}})
	if !ok {
		return
	}
	tab := qstr(q, "tab")
	if tab != "nfs" && tab != "users" {
		tab = "smb"
	}
	errMsg, localErr := qstr(q, "error"), ""
	smb := SmbStatus{Shares: []SmbShare{}}
	nfs := NfsStatus{Exports: []NfsExport{}}
	lu := LocalUsersStatus{Users: []LocalUser{}, Groups: []LocalGroup{}}
	switch tab {
	case "smb":
		var s SmbStatus
		if err := agentJSON(a.HC, "/smb/status", &s); err != nil {
			errMsg = "SMB 상태 조회 실패: " + err.Error()
		} else {
			smb = s
		}
	case "nfs":
		var s NfsStatus
		if err := agentJSON(a.HC, "/nfs/status", &s); err != nil {
			errMsg = "NFS 상태 조회 실패: " + err.Error()
		} else {
			nfs = s
		}
	case "users":
		var s LocalUsersStatus
		if err := agentJSON(a.HC, "/localusers/status", &s); err != nil {
			localErr = "사용자 목록 조회 실패: " + err.Error()
		} else {
			lu = s
		}
	}
	c := base("shares", u)
	c["tab"], c["smb"], c["nfs"], c["local_users"], c["local_error"] = tab, smb, nfs, lu, localErr
	c["agent_mode"] = a.HC.AgentMode()
	c["error"], c["success"] = errMsg, qstr(q, "success")
	page(w, 200, "shares.html", c)
}

func (a *App) nfsCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "path", Default: true}, {Name: "name", Default: true}, {Name: "clients", Default: true}, {Name: "read_only", Default: true}, {Name: "maproot", Default: true}})
	if !ok {
		return
	}
	line := qstr(f, "path")
	if qstr(f, "read_only") == "1" {
		line += "\t-ro"
	}
	if m := qstr(f, "maproot"); m != "" {
		line += "\t-maproot=" + m
	}
	for _, c := range strings.Fields(qstr(f, "clients")) {
		line += "\t" + c
	}
	err := postSimple(a.HC, "/nfs/export", NfsExport{Name: qstr(f, "name"), Path: qstr(f, "path"), Line: line, Clients: qstr(f, "clients")})
	if err != nil {
		httpx.RedirectTo(w, "/shares?tab=nfs&error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/shares?tab=nfs&success=NFS+export+"+urlenc(qstr(f, "name"))+"+저장됨")
}

func (a *App) nfsDelete(w http.ResponseWriter, r *http.Request) {
	name := httpx.Param(r, "name")
	if err := postSimple(a.HC, "/nfs/export/delete", map[string]any{"name": name}); err != nil {
		httpx.RedirectTo(w, "/shares?tab=nfs&error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/shares?tab=nfs&success=NFS+export+"+urlenc(name)+"+삭제됨")
}

func (a *App) nfsReload(w http.ResponseWriter, r *http.Request) {
	out, err := postOutput(a.HC, "/nfs/reload", nil)
	if err != nil {
		httpx.RedirectTo(w, "/shares?tab=nfs&error=reload+실패:+"+urlenc(err.Error()))
		return
	}
	if out == "" {
		out = "mountd reload 완료"
	}
	httpx.RedirectTo(w, "/shares?tab=nfs&success="+urlenc(out))
}

func (a *App) shareCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "name", Default: true}, {Name: "path", Default: true}, {Name: "comment", Default: true}, {Name: "valid_users", Default: true}, {Name: "read_only", Default: true}, {Name: "no_browse", Default: true}, {Name: "guest_ok", Default: true}})
	if !ok {
		return
	}
	if qstr(f, "name") == "" || qstr(f, "path") == "" {
		httpx.RedirectTo(w, "/shares?error=이름과+경로는+필수입니다")
		return
	}
	err := postSimple(a.HC, "/smb/share", SmbShare{Name: qstr(f, "name"), Path: qstr(f, "path"), Comment: qstr(f, "comment"), ValidUsers: qstr(f, "valid_users"),
		ReadOnly: qstr(f, "read_only") == "1", Browseable: qstr(f, "no_browse") != "1", GuestOk: qstr(f, "guest_ok") == "1"})
	if err != nil {
		httpx.RedirectTo(w, "/shares?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/shares?success=공유+"+urlenc(qstr(f, "name"))+"+저장됨")
}

func (a *App) shareDelete(w http.ResponseWriter, r *http.Request) {
	name := httpx.Param(r, "name")
	if err := postSimple(a.HC, "/smb/share/delete", map[string]any{"name": name}); err != nil {
		httpx.RedirectTo(w, "/shares?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/shares?success=공유+"+urlenc(name)+"+삭제됨")
}

func (a *App) shareReload(w http.ResponseWriter, r *http.Request) {
	out, err := postOutput(a.HC, "/smb/reload", nil)
	if err != nil {
		httpx.RedirectTo(w, "/shares?error=reload+실패:+"+urlenc(err.Error()))
		return
	}
	if out == "" {
		out = "Samba reload 완료"
	}
	httpx.RedirectTo(w, "/shares?success="+urlenc(out))
}

func (a *App) shareSetup(w http.ResponseWriter, r *http.Request) {
	resp, err := a.HC.Post("/smb/setup", nil)
	if err != nil {
		httpx.RedirectTo(w, "/shares?error="+urlenc(err.Error()))
		return
	}
	var m struct {
		Message string `json:"message" serde:"default"`
	}
	if httpx.Unmarshal(resp, &m) != nil {
		m.Message = ""
	}
	httpx.RedirectTo(w, "/shares?success="+urlenc(m.Message))
}

func (a *App) servicesInfo(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	list, fe := collectServices(a.HC, a.PC)
	c := base("services", u)
	c["services"], c["fetch_error"] = list, fe
	page(w, 200, "services.html", c)
}

func (a *App) networkPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	errMsg := ""
	st := NetworkStatus{Interfaces: []NetworkIface{}, DNS: []string{}}
	var s NetworkStatus
	if err := agentJSON(a.HC, "/network/status", &s); err != nil {
		errMsg = "네트워크 상태 조회 실패: " + err.Error()
	} else {
		st = s
	}
	c := base("network", u)
	c["interfaces"], c["routes"], c["dns"] = st.Interfaces, st.Routes, st.DNS
	c["agent_mode"], c["error"] = a.HC.AgentMode(), errMsg
	page(w, 200, "network.html", c)
}

// ── AppJail ──────────────────────────────────────────────────────────────────

func (a *App) appjailPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	errMsg := qstr(q, "error")
	var st AppJailStatus
	if err := agentJSON(a.HC, "/appjail/list", &st); err != nil {
		errMsg = "jail 목록 조회 실패: " + err.Error()
		st = AppJailStatus{}
	}
	if st.Jails == nil {
		st.Jails = []JailInfo{}
	}
	c := base("appjail", u)
	c["jails"], c["agent_mode"], c["error"], c["success"] = st.Jails, a.HC.AgentMode(), errMsg, qstr(q, "success")
	page(w, 200, "appjail.html", c)
}

func (a *App) appjailDetailPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	name := httpx.Param(r, "name")
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	var d JailDetail
	if err := agentJSON(a.HC, "/appjail/detail?name="+urlenc(name), &d); err != nil {
		httpx.Text(w, 404, "jail 조회 실패: "+err.Error())
		return
	}
	c := base("appjail", u)
	c["d"], c["error"], c["success"] = d, qstr(q, "error"), qstr(q, "success")
	page(w, 200, "appjail-detail.html", c)
}

func (a *App) appjailAction(w http.ResponseWriter, r *http.Request, action string) {
	name := httpx.Param(r, "name")
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "back", Default: true}})
	if !ok {
		return
	}
	_, err := postOutput(a.HC, "/appjail/"+action, map[string]any{"name": name})
	done := map[string]string{"start": "시작됨", "stop": "중지됨", "restart": "재시작됨"}[action]
	base := "/appjail"
	if qstr(f, "back") == "detail" {
		base = "/appjail/" + urlenc(name)
	}
	if err != nil {
		httpx.RedirectTo(w, base+"?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, base+"?success="+urlenc("jail "+name+" "+done))
}

func (a *App) appjailStart(w http.ResponseWriter, r *http.Request) { a.appjailAction(w, r, "start") }
func (a *App) appjailStop(w http.ResponseWriter, r *http.Request)  { a.appjailAction(w, r, "stop") }
func (a *App) appjailRestart(w http.ResponseWriter, r *http.Request) {
	a.appjailAction(w, r, "restart")
}

// ── 진단 ─────────────────────────────────────────────────────────────────────

func (a *App) diagnosticsPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	c := base("diagnostics", u)
	c["agent_mode"] = a.HC.AgentMode()
	page(w, 200, "diagnostics.html", c)
}

var diagLogQ = []httpx.UField{{Name: "file", Default: true}, {Name: "lines", Default: true}}

func (a *App) diagLocalLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	q, ok := httpx.Query(w, r, diagLogQ)
	if !ok {
		return
	}
	if qstr(q, "file") != "pfortainer" {
		httpx.Text(w, 400, "unknown log")
		return
	}
	data, err := readFileString(Root("/var/log/pfortainer.log"))
	if err != nil {
		httpx.Text(w, 500, rustIOErr(err))
		return
	}
	n, msg := httpx.ParseI64(qstr(q, "lines"))
	if msg != "" || n <= 0 {
		n = 200
	}
	lines := strings.Split(data, "\n")
	start := len(lines) - int(n)
	if start < 0 {
		start = 0
	}
	httpx.Text(w, 200, strings.Join(lines[start:], "\n"))
}

func (a *App) diagHostLog(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	q, ok := httpx.Query(w, r, diagLogQ)
	if !ok {
		return
	}
	lines := qstr(q, "lines")
	if lines == "" {
		lines = "200"
	}
	p := "/diag/log?file=" + qstr(q, "file") + "&lines=" + lines
	a.diagRelay(w, p)
}

func (a *App) diagCmd(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "cmd", Default: true}})
	if !ok {
		return
	}
	a.diagRelay(w, "/diag/cmd?cmd="+qstr(q, "cmd"))
}

func (a *App) diagRelay(w http.ResponseWriter, p string) {
	if !a.HC.socketExists() {
		httpx.Text(w, http.StatusBadGateway, (&HostError{Unavailable: true}).Error())
		return
	}
	if !validURIPath(p) {
		httpx.Abort()
	}
	b, err := a.HC.agentGet(p)
	if err != nil {
		httpx.Text(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(200)
	w.Write(b)
}
