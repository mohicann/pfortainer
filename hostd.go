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
	// Ensure /usr/local/sbin (smartctl, etc.) is in PATH for daemon(8) environments.
	if p := os.Getenv("PATH"); p != "" {
		os.Setenv("PATH", p+":/usr/local/sbin:/usr/local/bin")
	} else {
		os.Setenv("PATH", "/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin")
	}

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

	// ZFS snapshot write endpoints — all require POST and run as host root.
	// Input/output is JSON.
	mux.HandleFunc("/zfs/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Dataset  string `json:"dataset"`
			SnapName string `json:"snapname"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dataset == "" || req.SnapName == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		auditLog("zfs snapshot %s@%s", req.Dataset, req.SnapName)
		out, err := exec.Command("zfs", "snapshot", req.Dataset+"@"+req.SnapName).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/zfs/snapshot/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"` // dataset@snapname
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Name, "@") {
			http.Error(w, "invalid snapshot name", http.StatusBadRequest)
			return
		}
		auditLog("zfs destroy %s", req.Name)
		out, err := exec.Command("zfs", "destroy", req.Name).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/zfs/rollback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"` // dataset@snapname
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Name, "@") {
			http.Error(w, "invalid snapshot name", http.StatusBadRequest)
			return
		}
		auditLog("zfs rollback -r %s", req.Name)
		out, err := exec.Command("zfs", "rollback", "-r", req.Name).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/zfs/clone", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name   string `json:"name"`   // dataset@snapname
			Target string `json:"target"` // new dataset
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" || req.Target == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !strings.Contains(req.Name, "@") {
			http.Error(w, "invalid snapshot name", http.StatusBadRequest)
			return
		}
		auditLog("zfs clone %s %s", req.Name, req.Target)
		out, err := exec.Command("zfs", "clone", req.Name, req.Target).CombinedOutput()
		if err != nil {
			msg := strings.TrimSpace(string(out))
			if msg == "" {
				msg = err.Error()
			}
			http.Error(w, msg, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/zfs/snapshot/prune", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Dataset string `json:"dataset"`
			Prefix  string `json:"prefix"`
			Keep    int    `json:"keep"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Dataset == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if req.Keep <= 0 {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"success":true}`))
			return
		}
		auditLog("zfs snapshot prune %s prefix=%s keep=%d", req.Dataset, req.Prefix, req.Keep)
		// List snapshots directly via zfs binary (host-side, no agent recursion)
		out, err := exec.Command("zfs", "list", "-H", "-p", "-t", "snapshot",
			"-o", "name,creation", "-s", "creation", "-r", req.Dataset).Output()
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		var matching []string
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			f := strings.SplitN(line, "\t", 2)
			name := f[0]
			atIdx := strings.LastIndex(name, "@")
			if atIdx < 0 {
				continue
			}
			ds := name[:atIdx]
			sn := name[atIdx+1:]
			if ds == req.Dataset && strings.HasPrefix(sn, req.Prefix) {
				matching = append(matching, name)
			}
		}
		for len(matching) > req.Keep {
			auditLog("zfs destroy %s (prune)", matching[0])
			exec.Command("zfs", "destroy", matching[0]).Run()
			matching = matching[1:]
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
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
