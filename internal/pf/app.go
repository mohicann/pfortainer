package pf

import (
	"log"
	"net/http"
	"os"

	"github.com/mohicann/pfortainer/internal/httpx"
)

// App: AppState.
type App struct {
	Cfg Config
	DB  *ConfigDB
	PC  *PodmanClient
	HC  *HostClient
	MC  *MetricsCollector
}

// Main: 앱 또는 -hostd.
func Main() {
	if len(os.Args) > 1 && os.Args[1] == "-hostd" {
		if err := RunHostd(); err != nil {
			log.Fatal(err)
		}
		return
	}
	cfg := LoadConfig()
	db, err := OpenConfigDB(cfg.ConfigDB)
	if err != nil {
		log.Fatal(err)
	}
	if err := db.BootstrapAdmin(cfg.AdminPassword); err != nil {
		log.Fatal(err)
	}
	a := &App{Cfg: cfg, DB: db, PC: NewPodmanClient(cfg.PodmanSocket), HC: NewHostClient(cfg.HostdSocket)}
	a.MC = NewMetricsCollector(cfg.MetricsDB, cfg.MetricsRetainDays, MetricsOptions{Pool1: cfg.MetricsPool1, Pool2: cfg.MetricsPool2, NetIface: cfg.MetricsNetIface, HostdSocket: cfg.HostdSocket})
	StartScheduler(a.HC, a.DB)
	addr := cfg.Host + ":" + cfg.Port
	log.Printf("pfortainer-go listening on http://%s", addr)
	srv := &http.Server{Addr: addr, Handler: a.Router()}
	log.Fatal(srv.ListenAndServe())
}

// auth: AuthUser 추출기 — 없으면 302 /login.
func (a *App) auth(w http.ResponseWriter, r *http.Request) (SessionUser, bool) {
	u, ok := sessionUser(r, a.Cfg.SessionSecret)
	if !ok {
		httpx.Found(w, "/login")
		return u, false
	}
	return u, true
}

func (a *App) requireRole(min Role) httpx.Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := sessionUser(r, a.Cfg.SessionSecret)
			if !ok {
				httpx.Found(w, "/login")
				return
			}
			if u.Role < min {
				httpx.Text(w, http.StatusForbidden, "권한이 없습니다. 관리자에게 문의하세요.")
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func (a *App) Router() http.Handler {
	rt := httpx.NewRouter()
	G := func(p string, h http.HandlerFunc) { rt.HandleFunc("GET", p, h) }
	P := func(p string, h http.HandlerFunc) { rt.HandleFunc("POST", p, h) }
	// viewer 묶음 (미들웨어 없음, 핸들러마다 auth)
	G("/", func(w http.ResponseWriter, r *http.Request) { httpx.RedirectTo(w, "/appjail") })
	G("/containers", a.containers)
	G("/containers/new", a.containerNewPage)
	G("/containers/{id}", a.containerDetail)
	G("/system", a.systemInfo)
	G("/api/system/stats", a.systemStats)
	G("/filesystem", a.filesystemInfo)
	G("/storage", a.storageHealth)
	G("/snapshots", a.snapshotsPage)
	G("/files", a.fileList)
	G("/api/files/list", a.fileListJSON)
	G("/files/edit", a.fileEdit)
	G("/api/files/download", a.fileDownload)
	G("/localusers", a.localUsersPage)
	G("/shares", a.sharesPage)
	G("/services", a.servicesInfo)
	G("/network", a.networkPage)
	G("/appjail", a.appjailPage)
	G("/bastille", func(w http.ResponseWriter, r *http.Request) { httpx.RedirectTo(w, "/appjail") })
	G("/appjail/{name}", a.appjailDetailPage)
	G("/diagnostics", a.diagnosticsPage)
	G("/api/diag/local-log", a.diagLocalLog)
	G("/api/diag/host-log", a.diagHostLog)
	G("/api/diag/cmd", a.diagCmd)
	G("/desktop", a.desktopPage)
	G("/api/desktop/prefs", a.desktopPrefsGet)
	rt.HandleFunc("PUT", "/api/desktop/prefs", a.desktopPrefsPut)
	G("/metrics", a.metricsPage)
	G("/api/metrics/data", a.metricsData)
	G("/alerts", a.alertsPage)
	G("/profile", a.profilePage)
	P("/profile/2fa/begin", a.totpSetupBegin)
	P("/profile/2fa/enable", a.totpSetupConfirm)
	P("/profile/2fa/disable", a.totpDisable)
	G("/replications", a.replicationPage)
	G("/schedules", a.schedulesPage)

	// operator 묶음
	op := a.requireRole(RoleOperator)
	O := func(p string, h http.HandlerFunc) { rt.HandleFunc("POST", p, h, op) }
	O("/containers/{id}/{action}", a.containerAction)
	O("/api/containers/build", a.containerBuild)
	O("/api/compose/up", a.composeUp)
	O("/api/filesystem/create", a.filesystemCreate)
	O("/api/filesystem/delete", a.filesystemDelete)
	O("/api/snapshots/create", a.snapshotCreate)
	O("/api/snapshots/delete", a.snapshotDelete)
	O("/api/snapshots/rollback", a.snapshotRollback)
	O("/api/snapshots/clone", a.snapshotClone)
	O("/schedules", a.scheduleCreate)
	O("/schedules/{id}/toggle", a.scheduleToggle)
	O("/schedules/{id}/delete", a.scheduleDelete)
	O("/schedules/{id}/run", a.scheduleRunNow)
	O("/api/smart/test", a.smartTestRun)
	O("/api/files/upload", a.fileUpload)
	O("/api/files/mkdir", a.fileMkdir)
	O("/api/files/create", a.fileCreate)
	O("/api/files/delete", a.fileDelete)
	O("/api/files/rename", a.fileRename)
	O("/api/files/chmod", a.fileChmod)
	O("/api/files/save", a.fileSave)
	O("/api/files/clip", a.fileClip)
	O("/api/files/paste", a.filePaste)
	O("/localusers", a.localUserCreate)
	O("/localusers/{username}/delete", a.localUserDelete)
	O("/localusers/{username}/smbpasswd", a.localUserSMBPasswd)
	O("/localgroups", a.localGroupCreate)
	O("/localgroups/{name}/delete", a.localGroupDelete)
	O("/localgroups/{name}/member", a.localGroupMember)
	O("/shares", a.shareCreate)
	O("/shares/{name}/delete", a.shareDelete)
	O("/shares/reload", a.shareReload)
	O("/shares/setup", a.shareSetup)
	O("/nfs/export", a.nfsCreate)
	O("/nfs/export/{name}/delete", a.nfsDelete)
	O("/nfs/reload", a.nfsReload)
	O("/appjail/{name}/start", a.appjailStart)
	O("/appjail/{name}/stop", a.appjailStop)
	O("/appjail/{name}/restart", a.appjailRestart)
	O("/alerts", a.alertsSave)
	O("/alerts/test", a.alertTest)
	O("/replications", a.replicationCreate)
	O("/replications/{id}/delete", a.replicationDelete)
	O("/replications/{id}/toggle", a.replicationToggle)
	O("/replications/{id}/run", a.replicationRun)

	// admin 묶음
	ad := a.requireRole(RoleAdmin)
	rt.HandleFunc("GET", "/admin/users", a.adminUsersPage, ad)
	rt.HandleFunc("POST", "/admin/users", a.adminUserCreate, ad)
	rt.HandleFunc("POST", "/admin/users/{username}/role", a.adminUserUpdateRole, ad)
	rt.HandleFunc("POST", "/admin/users/{username}/password", a.adminUserUpdatePassword, ad)
	rt.HandleFunc("POST", "/admin/users/{username}/delete", a.adminUserDelete, ad)

	G("/login", a.loginPage)
	P("/login", a.login)
	G("/login/totp", a.totpPage)
	P("/login/totp", a.totpVerify)
	G("/logout", a.logout)
	return rt
}
