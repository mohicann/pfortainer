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
	mux.Handle("GET /images", auth(h.images))

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
