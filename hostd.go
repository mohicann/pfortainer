package main

import (
	"bufio"
	"encoding/json"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const hostdSockPath = "/run/pfortainer/host.sock"

// smartDevRe whitelists the device names smartctl may be invoked against, so the
// dev query parameter cannot be used to inject arbitrary arguments or paths.
var smartDevRe = regexp.MustCompile(`^(ada|da|nvd|nvme|ad|vtbd|mfid|nda)[0-9]+$`)

// auditLog records every privileged operation the host agent performs. Because
// hostd runs as root, this trail is the primary record of what was executed on
// the host's behalf.
func auditLog(format string, args ...any) {
	log.Printf("[hostd audit] "+format, args...)
}

// runCmd executes a privileged command, audit-logs it, and returns its stdout.
// On a non-zero exit the captured stdout is still returned alongside the error,
// which matters for tools like smartctl that exit non-zero yet print useful data.
func runCmd(name string, args ...string) ([]byte, error) {
	auditLog("exec: %s %s", name, strings.Join(args, " "))
	return exec.Command(name, args...).Output()
}

// writeCmdOut writes command output to the response, treating any error as 500.
func writeCmdOut(w http.ResponseWriter, out []byte, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(out)
}

// writeCmdOutLenient writes whatever output was captured even when the command
// exited non-zero, failing only when there is nothing to return (e.g. smartctl).
func writeCmdOutLenient(w http.ResponseWriter, out []byte, err error) {
	if len(out) == 0 && err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Write(out)
}

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

	// Privileged read-only storage endpoints. These require host-level access
	// (vdev topology and SMART data are invisible inside the Jail) and so must be
	// served by the host agent rather than the Jail process.
	mux.HandleFunc("/zpool-status", func(w http.ResponseWriter, r *http.Request) {
		out, err := runCmd("zpool", "status")
		writeCmdOut(w, out, err)
	})

	mux.HandleFunc("/zpool-list", func(w http.ResponseWriter, r *http.Request) {
		out, err := runCmd("zpool", "list", "-H", "-p",
			"-o", "name,size,alloc,free,health,frag,cap,dedup")
		writeCmdOut(w, out, err)
	})

	mux.HandleFunc("/smart-scan", func(w http.ResponseWriter, r *http.Request) {
		out, err := runCmd("smartctl", "--scan")
		writeCmdOut(w, out, err)
	})

	mux.HandleFunc("/smart", func(w http.ResponseWriter, r *http.Request) {
		dev := r.URL.Query().Get("dev")
		if !smartDevRe.MatchString(dev) {
			http.Error(w, "invalid device", http.StatusBadRequest)
			return
		}
		out, err := runCmd("smartctl", "-a", "/dev/"+dev)
		writeCmdOutLenient(w, out, err)
	})

	mux.HandleFunc("/compose-up", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		content, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		dir, err := os.MkdirTemp("", "pfortainer-compose-*")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		defer os.RemoveAll(dir)

		composePath := filepath.Join(dir, "docker-compose.yml")
		if err := os.WriteFile(composePath, content, 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Accel-Buffering", "no")
		enc := json.NewEncoder(w)
		emit := func(ltype, line string) {
			enc.Encode(map[string]string{"type": ltype, "line": line})
			if flusher != nil {
				flusher.Flush()
			}
		}
		streamComposeUp(composePath, emit)
	})

	srv := &http.Server{Handler: mux}
	if err := srv.Serve(ln); err != nil {
		log.Fatalf("hostd: serve: %v", err)
	}
}

// streamComposeUp runs `podman-compose -f composePath up -d` and calls emit for
// each output line. emit("done", "0"|"1") is called last with the exit status.
func streamComposeUp(composePath string, emit func(ltype, line string)) {
	cmd := exec.Command("podman-compose", "-f", composePath, "up", "-d")

	pr, pw := io.Pipe()
	cmd.Stdout = pw
	cmd.Stderr = pw

	if err := cmd.Start(); err != nil {
		emit("error", "podman-compose 실행 실패: "+err.Error())
		emit("done", "1")
		return
	}

	waitDone := make(chan error, 1)
	go func() {
		waitDone <- cmd.Wait()
		pw.Close()
	}()

	scanner := bufio.NewScanner(pr)
	for scanner.Scan() {
		line := scanner.Text()
		ltype := "log"
		lower := strings.ToLower(line)
		if strings.Contains(lower, "error") || strings.Contains(lower, "failed") {
			ltype = "error"
		}
		emit(ltype, line)
	}

	exitCode := "0"
	if err := <-waitDone; err != nil {
		exitCode = "1"
	}
	emit("done", exitCode)
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
