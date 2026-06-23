package main

import (
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
)

const hostdSockPath = "/run/pfortainer/host.sock"

// runHostd starts the host-agent HTTP server on a Unix socket.
// It exposes /sockstat and /jls so pfortainer inside a Jail can call them.
func runHostd() {
	os.MkdirAll("/run/pfortainer", 0755)
	os.Remove(hostdSockPath)

	ln, err := net.Listen("unix", hostdSockPath)
	if err != nil {
		log.Fatalf("hostd: listen %s: %v", hostdSockPath, err)
	}
	os.Chmod(hostdSockPath, 0666)

	mux := http.NewServeMux()

	mux.HandleFunc("/sockstat", func(w http.ResponseWriter, r *http.Request) {
		out, err := exec.Command("sockstat", "-l").Output()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(out)
	})

	mux.HandleFunc("/jls", func(w http.ResponseWriter, r *http.Request) {
		out, err := exec.Command("jls", "jid", "name").Output()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Write(out)
	})

	mux.HandleFunc("/ps", func(w http.ResponseWriter, r *http.Request) {
		jid := r.URL.Query().Get("jid")
		if jid == "" {
			http.Error(w, "jid required", http.StatusBadRequest)
			return
		}
		out, err := exec.Command("ps", "-J", jid, "-o", "pid=").Output()
		if err != nil {
			// ps returns non-zero for empty jails; return empty body
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Write(out)
	})

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("hostd: serve: %v", err)
	}
}

// hostdClient returns an *http.Client that connects to the host-agent socket,
// or nil if the socket does not exist (fallback to local exec).
func hostdClient() *http.Client {
	if _, err := os.Stat(hostdSockPath); err != nil {
		return nil
	}
	return &http.Client{
		Transport: &http.Transport{
			Dial: func(_, _ string) (net.Conn, error) {
				return net.Dial("unix", hostdSockPath)
			},
		},
	}
}
