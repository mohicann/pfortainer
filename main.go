package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "-hostd" {
		runHostd()
		return
	}

	cfg := loadConfig()

	cdb, err := openConfigDB(cfg.ConfigDB)
	if err != nil {
		log.Fatalf("config db: %v", err)
	}
	if err := cdb.BootstrapAdmin(cfg.AdminPassword); err != nil {
		log.Fatalf("bootstrap admin: %v", err)
	}

	pc := newPodmanClient(cfg.PodmanSocket)
	mc := newMetricsCollector(cfg.MetricsDB, cfg.MetricsRetainDays)
	sched := newScheduler(cdb)
	go sched.Start(context.Background())
	h := newHandlers(cfg, pc, mc, cdb, sched)

	// requireRole returns a middleware that checks the session role, injects
	// the SessionUser into context, and redirects to /login if unauthenticated.
	requireRole := func(minRole string, next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			u, ok := sessionUser(r, cfg.SessionSecret)
			if !ok {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			if !roleAtLeast(u.Role, minRole) {
				w.WriteHeader(http.StatusForbidden)
				render(w, "error", map[string]any{
					"ActivePage":        "",
					"PodmanUnavailable": false,
					"Error":             "권한이 없습니다. 관리자에게 문의하세요.",
				})
				return
			}
			next(w, withUser(r, u))
		})
	}

	// Convenience shorthands for the three role tiers.
	auth    := func(next http.HandlerFunc) http.Handler { return requireRole(RoleViewer, next) }
	authOp  := func(next http.HandlerFunc) http.Handler { return requireRole(RoleOperator, next) }
	authAdm := func(next http.HandlerFunc) http.Handler { return requireRole(RoleAdmin, next) }

	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.login)
	mux.HandleFunc("GET /login/totp", h.totpPage)
	mux.HandleFunc("POST /login/totp", h.totpVerify)
	mux.HandleFunc("GET /logout", h.logout)

	mux.Handle("GET /profile", auth(h.profilePage))
	mux.Handle("POST /profile/2fa/begin", auth(h.totpSetupBegin))
	mux.Handle("POST /profile/2fa/enable", auth(h.totpSetupConfirm))
	mux.Handle("POST /profile/2fa/disable", auth(h.totpDisable))

	// viewer: read-only pages
	mux.Handle("GET /{$}", auth(h.dashboard))
	mux.Handle("GET /containers", auth(h.containers))
	mux.Handle("GET /containers/new", auth(h.containerNewPage))
	mux.Handle("GET /containers/{id}", auth(h.containerDetail))
	mux.Handle("GET /images", auth(h.images))
	mux.Handle("GET /images/{id}", auth(h.imageDetail))
	mux.Handle("GET /system", auth(h.systemInfo))
	mux.Handle("GET /api/system/stats", auth(h.systemStats))
	mux.Handle("GET /storage", auth(h.storageHealth))
	mux.Handle("GET /filesystem", auth(h.filesystemInfo))
	mux.Handle("GET /files", auth(h.fileList))
	mux.Handle("GET /api/files/list", auth(h.fileListJSON))
	mux.Handle("GET /files/edit", auth(h.fileEdit))
	mux.Handle("GET /api/files/download", auth(h.fileDownload))
	mux.Handle("GET /services", auth(h.servicesInfo))
	mux.Handle("GET /metrics", auth(h.metricsPage))
	mux.Handle("GET /api/metrics/data", auth(h.mc.serveHTTP))

	// operator: write operations
	mux.Handle("POST /containers/{id}/{action}", authOp(h.containerAction))
	mux.Handle("POST /api/containers/build", authOp(h.containerBuild))
	mux.Handle("POST /api/compose/up", authOp(h.composeUp))
	mux.Handle("POST /images/{id}/{action}", authOp(h.imageAction))
	mux.Handle("POST /api/filesystem/create", authOp(h.filesystemCreate))
	mux.Handle("POST /api/filesystem/delete", authOp(h.filesystemDelete))
	mux.Handle("POST /api/files/upload", authOp(h.fileUpload))
	mux.Handle("POST /api/files/mkdir", authOp(h.fileMkdir))
	mux.Handle("POST /api/files/create", authOp(h.fileCreate))
	mux.Handle("POST /api/files/delete", authOp(h.fileDelete))
	mux.Handle("POST /api/files/rename", authOp(h.fileRename))
	mux.Handle("POST /api/files/chmod", authOp(h.fileChmod))
	mux.Handle("POST /api/files/save", authOp(h.fileSave))
	mux.Handle("POST /api/files/clip", authOp(h.fileClip))
	mux.Handle("POST /api/files/paste", authOp(h.filePaste))

	// 로컬 사용자/그룹
	mux.Handle("GET /localusers", auth(h.localUsersPage))
	mux.Handle("POST /localusers", authOp(h.localUserCreate))
	mux.Handle("POST /localusers/{username}/delete", authOp(h.localUserDelete))
	mux.Handle("POST /localusers/{username}/smbpasswd", authOp(h.localUserSMBPasswd))
	mux.Handle("POST /localgroups", authOp(h.localGroupCreate))
	mux.Handle("POST /localgroups/{name}/delete", authOp(h.localGroupDelete))
	mux.Handle("POST /localgroups/{name}/member", authOp(h.localGroupMember))

	// SMB 공유
	mux.Handle("GET /shares", auth(h.sharesPage))
	mux.Handle("POST /shares", authOp(h.shareCreate))
	mux.Handle("POST /shares/{name}/delete", authOp(h.shareDelete))
	mux.Handle("POST /shares/reload", authOp(h.shareReload))
	mux.Handle("POST /shares/setup", authOp(h.shareSetup))
	mux.Handle("POST /nfs/export", authOp(h.nfsCreate))
	mux.Handle("POST /nfs/export/{name}/delete", authOp(h.nfsDelete))
	mux.Handle("POST /nfs/reload", authOp(h.nfsReload))

	// SMART 테스트
	mux.Handle("POST /api/smart/test", authOp(h.smartTestRun))

	// 네트워크
	mux.Handle("GET /network", auth(h.networkPage))

	// 앱 카탈로그
	mux.Handle("GET /catalog", auth(h.catalogPage))
	mux.Handle("POST /catalog/{id}/install", authOp(h.catalogInstall))
	mux.Handle("POST /catalog/{id}/remove", authOp(h.catalogRemove))

	// 진단/로그
	mux.Handle("GET /diagnostics", auth(h.diagnosticsPage))
	mux.Handle("GET /api/diag/local-log", auth(h.diagLocalLog))
	mux.Handle("GET /api/diag/host-log", auth(h.diagHostLog))
	mux.Handle("GET /api/diag/cmd", auth(h.diagCmd))

	// ZFS 복제
	mux.Handle("GET /replications", auth(h.replicationPage))
	mux.Handle("POST /replications", authOp(h.replicationCreate))
	mux.Handle("POST /replications/{id}/delete", authOp(h.replicationDelete))
	mux.Handle("POST /replications/{id}/toggle", authOp(h.replicationToggle))
	mux.Handle("POST /replications/{id}/run", authOp(h.replicationRun))

	// 알림 설정
	mux.Handle("GET /alerts", auth(h.alertsPage))
	mux.Handle("POST /alerts", authOp(h.alertsSave))
	mux.Handle("POST /alerts/test", authOp(h.alertTest))

	// ZFS 스냅샷
	mux.Handle("GET /snapshots", auth(h.snapshots))
	mux.Handle("POST /api/snapshots/create", authOp(h.snapshotCreate))
	mux.Handle("POST /api/snapshots/delete", authOp(h.snapshotDelete))
	mux.Handle("POST /api/snapshots/rollback", authOp(h.snapshotRollback))
	mux.Handle("POST /api/snapshots/clone", authOp(h.snapshotClone))

	// 스케줄러
	mux.Handle("GET /schedules", auth(h.schedulesPage))
	mux.Handle("POST /schedules", authOp(h.scheduleCreate))
	mux.Handle("POST /schedules/{id}/toggle", authOp(h.scheduleToggle))
	mux.Handle("POST /schedules/{id}/delete", authOp(h.scheduleDelete))
	mux.Handle("POST /schedules/{id}/run", authOp(h.scheduleRunNow))

	// admin: user management
	mux.Handle("GET /admin/users", authAdm(h.adminUsers))
	mux.Handle("POST /admin/users", authAdm(h.adminUserCreate))
	mux.Handle("POST /admin/users/{username}/role", authAdm(h.adminUserUpdateRole))
	mux.Handle("POST /admin/users/{username}/password", authAdm(h.adminUserUpdatePassword))
	mux.Handle("POST /admin/users/{username}/delete", authAdm(h.adminUserDelete))

	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:        addr,
		Handler:     mux,
		ReadTimeout: 15 * time.Second,
		// WriteTimeout 없음: 빌드/compose 스트리밍이 수 분 걸릴 수 있음
	}

	log.Printf("pfortainer listening on http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
