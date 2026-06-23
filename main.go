package main

import (
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
	pc := newPodmanClient(cfg.PodmanSocket)
	mc := newMetricsCollector(cfg.MetricsDB, cfg.MetricsRetainDays)
	h := newHandlers(cfg, pc, mc)

	mux := http.NewServeMux()

	mux.HandleFunc("GET /login", h.loginPage)
	mux.HandleFunc("POST /login", h.login)
	mux.HandleFunc("GET /logout", h.logout)

	auth := func(next http.HandlerFunc) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !isAuthenticated(r, cfg.SessionSecret) {
				http.Redirect(w, r, "/login", http.StatusFound)
				return
			}
			next(w, r)
		})
	}

	mux.Handle("GET /{$}", auth(h.dashboard))
	mux.Handle("GET /containers", auth(h.containers))
	mux.Handle("GET /containers/new", auth(h.containerNewPage))
	mux.Handle("GET /containers/{id}", auth(h.containerDetail))
	mux.Handle("POST /containers/{id}/{action}", auth(h.containerAction))
	mux.Handle("POST /api/containers/build", auth(h.containerBuild))
	mux.Handle("POST /api/compose/up", auth(h.composeUp))
	mux.Handle("GET /images", auth(h.images))
	mux.Handle("GET /images/{id}", auth(h.imageDetail))
	mux.Handle("POST /images/{id}/{action}", auth(h.imageAction))
	mux.Handle("GET /system", auth(h.systemInfo))
	mux.Handle("GET /api/system/stats", auth(h.systemStats))
	mux.Handle("GET /filesystem", auth(h.filesystemInfo))
	mux.Handle("POST /api/filesystem/create", auth(h.filesystemCreate))
	mux.Handle("POST /api/filesystem/delete", auth(h.filesystemDelete))

	mux.Handle("GET /files", auth(h.fileList))
	mux.Handle("GET /api/files/list", auth(h.fileListJSON))
	mux.Handle("GET /files/edit", auth(h.fileEdit))
	mux.Handle("GET /api/files/download", auth(h.fileDownload))
	mux.Handle("POST /api/files/upload", auth(h.fileUpload))
	mux.Handle("POST /api/files/mkdir", auth(h.fileMkdir))
	mux.Handle("POST /api/files/create", auth(h.fileCreate))
	mux.Handle("POST /api/files/delete", auth(h.fileDelete))
	mux.Handle("POST /api/files/rename", auth(h.fileRename))
	mux.Handle("POST /api/files/chmod", auth(h.fileChmod))
	mux.Handle("POST /api/files/save", auth(h.fileSave))
	mux.Handle("POST /api/files/clip", auth(h.fileClip))
	mux.Handle("POST /api/files/paste", auth(h.filePaste))

	mux.Handle("GET /services", auth(h.servicesInfo))
	mux.Handle("GET /metrics", auth(h.metricsPage))
	mux.Handle("GET /api/metrics/data", auth(h.mc.serveHTTP))

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
