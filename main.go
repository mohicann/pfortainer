package main

import (
	"fmt"
	"log"
	"net/http"
	"time"
)

func main() {
	cfg := loadConfig()
	pc := newPodmanClient(cfg.PodmanSocket)
	h := newHandlers(cfg, pc)

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
	mux.Handle("GET /containers/{id}", auth(h.containerDetail))
	mux.Handle("POST /containers/{id}/{action}", auth(h.containerAction))
	mux.Handle("GET /images", auth(h.images))
	mux.Handle("POST /images/{id}/{action}", auth(h.imageAction))
	mux.Handle("GET /system", auth(h.systemInfo))
	mux.Handle("GET /api/system/stats", auth(h.systemStats))
	mux.Handle("GET /filesystem", auth(h.filesystemInfo))
	mux.Handle("POST /api/filesystem/create", auth(h.filesystemCreate))
	mux.Handle("POST /api/filesystem/delete", auth(h.filesystemDelete))

	mux.Handle("GET /files", auth(h.fileList))
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

	addr := fmt.Sprintf("%s:%s", cfg.Host, cfg.Port)
	srv := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	log.Printf("pfortainer listening on http://%s", addr)
	log.Fatal(srv.ListenAndServe())
}
