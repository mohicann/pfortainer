package main

import (
	"bufio"
	"encoding/json"
	"fmt"
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

	// POST /smart/test — run a SMART self-test on a device.
	mux.HandleFunc("/smart/test", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Device   string `json:"device"`
			TestType string `json:"type"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !smartDevRe.MatchString(req.Device) {
			http.Error(w, "invalid device", http.StatusBadRequest)
			return
		}
		validTypes := map[string]bool{"short": true, "long": true, "conveyance": true, "offline": true}
		if !validTypes[req.TestType] {
			http.Error(w, "invalid test type", http.StatusBadRequest)
			return
		}
		auditLog("smart-test", req.Device, req.TestType)
		out, err := exec.Command("smartctl", "-t", req.TestType, "/dev/"+req.Device).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil && !strings.Contains(s, "has begun") && !strings.Contains(s, "successful") {
			http.Error(w, s, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": s})
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

	// ── 로컬 사용자/그룹 관리 ───────────────────────────────────────────────────────
	var localUserRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,30}$`)

	mux.HandleFunc("/localusers/status", func(w http.ResponseWriter, r *http.Request) {
		// Parse /etc/passwd for uid >= 1000
		passwdData, _ := os.ReadFile("/etc/passwd")
		groupData, _ := os.ReadFile("/etc/group")

		// Build group membership map: username → []groupname
		userGroups := make(map[string][]string)
		var groups []LocalGroup
		for _, line := range strings.Split(string(groupData), "\n") {
			f := strings.SplitN(line, ":", 4)
			if len(f) < 4 {
				continue
			}
			name := f[0]
			gid := 0
			fmt.Sscanf(f[2], "%d", &gid)
			if gid < 1000 && name != "wheel" {
				continue
			}
			var members []string
			for _, m := range strings.Split(f[3], ",") {
				if m = strings.TrimSpace(m); m != "" {
					members = append(members, m)
					userGroups[m] = append(userGroups[m], name)
				}
			}
			groups = append(groups, LocalGroup{Name: name, GID: gid, Members: members})
		}

		// Get SMB users via pdbedit
		smbUsers := make(map[string]bool)
		if out, err := exec.Command("pdbedit", "-L").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				if f := strings.SplitN(line, ":", 2); len(f) >= 1 && f[0] != "" {
					smbUsers[f[0]] = true
				}
			}
		}

		// Build user list
		var users []LocalUser
		for _, line := range strings.Split(string(passwdData), "\n") {
			f := strings.SplitN(line, ":", 7)
			if len(f) < 7 {
				continue
			}
			uid := 0
			gid := 0
			fmt.Sscanf(f[2], "%d", &uid)
			fmt.Sscanf(f[3], "%d", &gid)
			if uid < 1000 {
				continue
			}
			username := f[0]
			users = append(users, LocalUser{
				Username: username,
				UID:      uid,
				GID:      gid,
				FullName: f[4],
				Home:     f[5],
				Shell:    f[6],
				Groups:   userGroups[username],
				HasSMB:   smbUsers[username],
			})
		}
		if users == nil {
			users = []LocalUser{}
		}
		if groups == nil {
			groups = []LocalGroup{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(LocalUsersStatus{Users: users, Groups: groups})
	})

	mux.HandleFunc("/localusers/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Username    string `json:"username"`
			FullName    string `json:"fullname"`
			Shell       string `json:"shell"`
			Password    string `json:"password"`
			SMBPassword string `json:"smb_password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !localUserRe.MatchString(req.Username) {
			http.Error(w, "invalid username", http.StatusBadRequest)
			return
		}
		shell := req.Shell
		if shell == "" {
			shell = "/sbin/nologin"
		}
		validShells := map[string]bool{"/sbin/nologin": true, "/bin/sh": true, "/usr/local/bin/bash": true, "/bin/csh": true}
		if !validShells[shell] {
			http.Error(w, "invalid shell", http.StatusBadRequest)
			return
		}
		auditLog("pw useradd %s", req.Username)
		args := []string{"useradd", "-n", req.Username, "-m", "-s", shell}
		if req.FullName != "" {
			args = append(args, "-c", req.FullName)
		}
		if out, err := exec.Command("pw", args...).CombinedOutput(); err != nil {
			http.Error(w, strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		if req.Password != "" {
			auditLog("pw usermod %s -h 0 (set password)", req.Username)
			cmd := exec.Command("pw", "usermod", req.Username, "-h", "0")
			cmd.Stdin = strings.NewReader(req.Password + "\n")
			cmd.CombinedOutput()
		}
		if req.SMBPassword != "" {
			auditLog("smbpasswd -a %s", req.Username)
			cmd := exec.Command("smbpasswd", "-a", "-s", req.Username)
			cmd.Stdin = strings.NewReader(req.SMBPassword + "\n" + req.SMBPassword + "\n")
			cmd.CombinedOutput()
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/localusers/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Username string `json:"username"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !localUserRe.MatchString(req.Username) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		auditLog("pw userdel %s -r", req.Username)
		// Remove SMB account first (best-effort)
		exec.Command("smbpasswd", "-x", req.Username).Run()
		out, err := exec.Command("pw", "userdel", "-n", req.Username, "-r").CombinedOutput()
		if err != nil {
			http.Error(w, strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/localusers/smbpasswd", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !localUserRe.MatchString(req.Username) || req.Password == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		auditLog("smbpasswd -a %s", req.Username)
		cmd := exec.Command("smbpasswd", "-a", "-s", req.Username)
		cmd.Stdin = strings.NewReader(req.Password + "\n" + req.Password + "\n")
		out, err := cmd.CombinedOutput()
		if err != nil {
			http.Error(w, strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/localgroups/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !localUserRe.MatchString(req.Name) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		auditLog("pw groupadd %s", req.Name)
		out, err := exec.Command("pw", "groupadd", "-n", req.Name).CombinedOutput()
		if err != nil {
			http.Error(w, strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/localgroups/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !localUserRe.MatchString(req.Name) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		auditLog("pw groupdel %s", req.Name)
		out, err := exec.Command("pw", "groupdel", "-n", req.Name).CombinedOutput()
		if err != nil {
			http.Error(w, strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/localgroups/member", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Group    string `json:"group"`
			Username string `json:"username"`
			Action   string `json:"action"` // "add" | "remove"
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil ||
			!localUserRe.MatchString(req.Group) || !localUserRe.MatchString(req.Username) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		flag := "-m"
		if req.Action == "remove" {
			flag = "-d"
		}
		auditLog("pw groupmod %s %s %s", req.Group, flag, req.Username)
		out, err := exec.Command("pw", "groupmod", req.Group, flag, req.Username).CombinedOutput()
		if err != nil {
			http.Error(w, strings.TrimSpace(string(out)), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	// ── NFS export 관리 ────────────────────────────────────────────────────────────
	// Drop-in exports dir: /etc/exports.d/<name>.exports (FreeBSD 13+ mountd)
	const nfsExportsDir = "/etc/exports.d"
	var nfsNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)

	mux.HandleFunc("/nfs/status", func(w http.ResponseWriter, r *http.Request) {
		out, _ := exec.Command("service", "nfsd", "status").CombinedOutput()
		running := strings.Contains(string(out), "is running")

		var exports []NFSExport
		if files, _ := filepath.Glob(filepath.Join(nfsExportsDir, "*.exports")); files != nil {
			for _, f := range files {
				data, err := os.ReadFile(f)
				if err != nil {
					continue
				}
				name := strings.TrimSuffix(filepath.Base(f), ".exports")
				for _, line := range strings.Split(string(data), "\n") {
					line = strings.TrimSpace(line)
					if line == "" || strings.HasPrefix(line, "#") {
						continue
					}
					fields := strings.Fields(line)
					path := fields[0]
					clients := ""
					if len(fields) > 1 {
						clients = strings.Join(fields[1:], " ")
					}
					exports = append(exports, NFSExport{Name: name, Path: path, Line: line, Clients: clients})
					break
				}
			}
		}
		if exports == nil {
			exports = []NFSExport{}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(NFSStatus{Running: running, Exports: exports})
	})

	mux.HandleFunc("/nfs/export", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var e NFSExport
		if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !nfsNameRe.MatchString(e.Name) {
			http.Error(w, "invalid export name", http.StatusBadRequest)
			return
		}
		clean := filepath.Clean(e.Path)
		if !filepath.IsAbs(clean) || strings.Contains(clean, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		if err := os.MkdirAll(nfsExportsDir, 0755); err != nil {
			http.Error(w, "exports.d mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		outPath := filepath.Join(nfsExportsDir, e.Name+".exports")
		auditLog("nfs export write %s → %s", e.Name, outPath)
		content := "# managed by pfortainer\n" + e.Line + "\n"
		if err := os.WriteFile(outPath, []byte(content), 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/nfs/export/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !nfsNameRe.MatchString(req.Name) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		outPath := filepath.Join(nfsExportsDir, req.Name+".exports")
		auditLog("nfs export delete %s", outPath)
		if err := os.Remove(outPath); err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/nfs/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		auditLog("service mountd reload")
		out, err := exec.Command("service", "mountd", "reload").CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil && s == "" {
			s = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": s})
	})

	// ── SMB 공유 관리 ─────────────────────────────────────────────────────────────
	// Drop-in fragment directory: /usr/local/etc/samba4/shares.d/<name>.conf
	// Main smb4.conf must contain: include = /usr/local/etc/samba4/shares.d/*.conf

	const smbSharesDir = "/usr/local/etc/smb4.conf.d"
	const smbMainConf = "/usr/local/etc/smb4.conf"
	var smbShareNameRe = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)

	parseSMBConf := func(data []byte) SMBShare {
		var s SMBShare
		boolVal := func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "yes") }
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
				s.Name = line[1 : len(line)-1]
				continue
			}
			idx := strings.Index(line, "=")
			if idx < 0 {
				continue
			}
			k, v := strings.TrimSpace(line[:idx]), strings.TrimSpace(line[idx+1:])
			switch strings.ToLower(k) {
			case "path":
				s.Path = v
			case "comment":
				s.Comment = v
			case "valid users":
				s.ValidUsers = v
			case "read only":
				s.ReadOnly = boolVal(v)
			case "browseable":
				s.Browseable = boolVal(v)
			case "guest ok":
				s.GuestOK = boolVal(v)
			}
		}
		return s
	}

	smbShareToConf := func(s SMBShare) string {
		boolStr := func(b bool) string {
			if b {
				return "Yes"
			}
			return "No"
		}
		var sb strings.Builder
		sb.WriteString("[" + s.Name + "]\n")
		sb.WriteString("\tpath = " + s.Path + "\n")
		if s.Comment != "" {
			sb.WriteString("\tcomment = " + s.Comment + "\n")
		}
		if s.ValidUsers != "" {
			sb.WriteString("\tvalid users = " + s.ValidUsers + "\n")
		}
		sb.WriteString("\tread only = " + boolStr(s.ReadOnly) + "\n")
		sb.WriteString("\tbrowseable = " + boolStr(s.Browseable) + "\n")
		sb.WriteString("\tguest ok = " + boolStr(s.GuestOK) + "\n")
		return sb.String()
	}

	mux.HandleFunc("/smb/status", func(w http.ResponseWriter, r *http.Request) {
		// Check if samba is running
		out, _ := exec.Command("service", "samba_server", "status").CombinedOutput()
		running := strings.Contains(string(out), "is running")

		// Check setup: smb4.conf has include line for shares.d
		mainConf, _ := os.ReadFile(smbMainConf)
		setupOK := strings.Contains(string(mainConf), "shares.d")

		// List shares
		var shares []SMBShare
		entries, _ := filepath.Glob(filepath.Join(smbSharesDir, "*.conf"))
		for _, f := range entries {
			data, err := os.ReadFile(f)
			if err != nil {
				continue
			}
			s := parseSMBConf(data)
			if s.Name != "" {
				shares = append(shares, s)
			}
		}
		if shares == nil {
			shares = []SMBShare{}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SMBStatus{
			Running: running,
			SetupOK: setupOK,
			Shares:  shares,
		})
	})

	mux.HandleFunc("/smb/share", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var s SMBShare
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !smbShareNameRe.MatchString(s.Name) {
			http.Error(w, "invalid share name (alphanumeric, hyphen, underscore only)", http.StatusBadRequest)
			return
		}
		clean := filepath.Clean(s.Path)
		if !filepath.IsAbs(clean) || strings.Contains(clean, "..") {
			http.Error(w, "invalid path", http.StatusBadRequest)
			return
		}
		s.Path = clean
		if err := os.MkdirAll(smbSharesDir, 0755); err != nil {
			http.Error(w, "shares.d mkdir: "+err.Error(), http.StatusInternalServerError)
			return
		}
		confPath := filepath.Join(smbSharesDir, s.Name+".conf")
		auditLog("smb share write %s → %s", s.Name, confPath)
		if err := os.WriteFile(confPath, []byte(smbShareToConf(s)), 0644); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/smb/share/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Name == "" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !smbShareNameRe.MatchString(req.Name) {
			http.Error(w, "invalid share name", http.StatusBadRequest)
			return
		}
		confPath := filepath.Join(smbSharesDir, req.Name+".conf")
		auditLog("smb share delete %s", confPath)
		if err := os.Remove(confPath); err != nil && !os.IsNotExist(err) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"success":true}`))
	})

	mux.HandleFunc("/smb/reload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		auditLog("service samba_server reload")
		out, err := exec.Command("service", "samba_server", "reload").CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil && s == "" {
			s = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": s})
	})

	mux.HandleFunc("/smb/setup", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		const includeLine = "include = " + smbSharesDir + "/*.conf"
		data, err := os.ReadFile(smbMainConf)
		if err != nil {
			http.Error(w, "smb4.conf 읽기 실패: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if strings.Contains(string(data), "shares.d") {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"message":"이미 설정되어 있습니다."}`))
			return
		}
		auditLog("smb setup: append include line to %s", smbMainConf)
		f, err := os.OpenFile(smbMainConf, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			http.Error(w, "smb4.conf 쓰기 실패: "+err.Error(), http.StatusInternalServerError)
			return
		}
		defer f.Close()
		if _, err := f.WriteString("\n" + includeLine + "\n"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"include 줄 추가 완료. Samba를 reload해주세요."}`))
	})

	// ── 네트워크 인터페이스 ────────────────────────────────────────────────────────
	mux.HandleFunc("/network/status", func(w http.ResponseWriter, r *http.Request) {
		type NetworkIface struct {
			Name   string   `json:"name"`
			Flags  []string `json:"flags"`
			MAC    string   `json:"mac"`
			MTU    int      `json:"mtu"`
			Status string   `json:"status"`
			Inet   []string `json:"inet"`
			Inet6  []string `json:"inet6"`
			Media  string   `json:"media"`
			Up     bool     `json:"up"`
		}

		// parse ifconfig -a
		ifOut, _ := exec.Command("ifconfig", "-a").Output()
		var ifaces []NetworkIface
		var cur *NetworkIface
		for _, line := range strings.Split(string(ifOut), "\n") {
			if line == "" {
				continue
			}
			if line[0] != ' ' && line[0] != '\t' {
				// new interface line: "igc3: flags=8843<UP,...> metric 0 mtu 1500"
				if cur != nil {
					ifaces = append(ifaces, *cur)
				}
				colonIdx := strings.Index(line, ":")
				if colonIdx < 0 {
					continue
				}
				cur = &NetworkIface{Name: line[:colonIdx]}
				rest := line[colonIdx+1:]
				if m := regexp.MustCompile(`<([^>]*)>`).FindStringSubmatch(rest); len(m) > 1 {
					cur.Flags = strings.Split(m[1], ",")
					for _, f := range cur.Flags {
						if f == "UP" {
							cur.Up = true
							break
						}
					}
				}
				if m := regexp.MustCompile(`mtu (\d+)`).FindStringSubmatch(rest); len(m) > 1 {
					fmt.Sscanf(m[1], "%d", &cur.MTU)
				}
			} else if cur != nil {
				line = strings.TrimSpace(line)
				switch {
				case strings.HasPrefix(line, "ether "):
					cur.MAC = strings.Fields(line)[1]
				case strings.HasPrefix(line, "inet "):
					// "inet 192.168.10.110 netmask 0xffffff00 ..."
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						ip := fields[1]
						if len(fields) >= 4 && fields[2] == "netmask" {
							mask := fields[3]
							if strings.HasPrefix(mask, "0x") {
								var n uint32
								fmt.Sscanf(mask[2:], "%x", &n)
								ones := 0
								for n != 0 {
									ones += int(n & 1)
									n >>= 1
								}
								ip += fmt.Sprintf("/%d", ones)
							}
						}
						cur.Inet = append(cur.Inet, ip)
					}
				case strings.HasPrefix(line, "inet6 "):
					fields := strings.Fields(line)
					if len(fields) >= 2 {
						ip := fields[1]
						if len(fields) >= 4 && fields[2] == "prefixlen" {
							ip += "/" + fields[3]
						}
						// skip link-local only if there are others
						cur.Inet6 = append(cur.Inet6, ip)
					}
				case strings.HasPrefix(line, "status: "):
					cur.Status = strings.TrimPrefix(line, "status: ")
				case strings.HasPrefix(line, "media: "):
					cur.Media = strings.TrimPrefix(line, "media: ")
				}
			}
		}
		if cur != nil {
			ifaces = append(ifaces, *cur)
		}
		if ifaces == nil {
			ifaces = []NetworkIface{}
		}

		// routing table
		routeOut, _ := exec.Command("netstat", "-rn").Output()

		// DNS from /etc/resolv.conf
		var dnsServers []string
		if data, err := os.ReadFile("/etc/resolv.conf"); err == nil {
			for _, l := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(l, "nameserver ") {
					dnsServers = append(dnsServers, strings.TrimPrefix(l, "nameserver "))
				}
			}
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"interfaces": ifaces,
			"routes":     strings.TrimSpace(string(routeOut)),
			"dns":        dnsServers,
		})
	})

	// ── 앱 카탈로그 ────────────────────────────────────────────────────────────────
	var catalogNameRe = regexp.MustCompile(`^pf-[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	var imageRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_./:@-]{2,255}$`)

	mux.HandleFunc("/catalog/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name    string            `json:"name"`
			Image   string            `json:"image"`
			Ports   []string          `json:"ports"`
			Volumes []string          `json:"volumes"`
			Env     map[string]string `json:"env"`
			Cmd     []string          `json:"cmd"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !catalogNameRe.MatchString(req.Name) {
			http.Error(w, "invalid container name (must start with pf-)", http.StatusBadRequest)
			return
		}
		if !imageRe.MatchString(req.Image) {
			http.Error(w, "invalid image name", http.StatusBadRequest)
			return
		}

		args := []string{"run", "-d", "--name", req.Name, "--restart", "unless-stopped"}
		for _, p := range req.Ports {
			args = append(args, "-p", p)
		}
		for _, v := range req.Volumes {
			// ensure host path dir exists
			hostPath := strings.SplitN(v, ":", 2)[0]
			os.MkdirAll(hostPath, 0755)
			args = append(args, "-v", v)
		}
		for k, v := range req.Env {
			args = append(args, "-e", k+"="+v)
		}
		args = append(args, req.Image)
		args = append(args, req.Cmd...)

		auditLog("podman %s", strings.Join(args, " "))
		out, err := exec.Command("podman", args...).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil {
			if s == "" {
				s = err.Error()
			}
			http.Error(w, s, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": s})
	})

	mux.HandleFunc("/catalog/remove", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			Name string `json:"name"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || !catalogNameRe.MatchString(req.Name) {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		auditLog("catalog remove %s", req.Name)
		exec.Command("podman", "stop", req.Name).Run()
		out, err := exec.Command("podman", "rm", req.Name).CombinedOutput()
		s := strings.TrimSpace(string(out))
		if err != nil && s == "" {
			s = err.Error()
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"output": s})
	})

	// ── 진단/로그 ──────────────────────────────────────────────────────────────────
	allowedLogs := map[string]string{
		"messages": "/var/log/messages",
		"security": "/var/log/security",
		"auth":     "/var/log/auth.log",
		"dmesg":    "/var/log/dmesg",
	}
	allowedCmds := map[string][]string{
		"df":       {"df", "-h"},
		"mount":    {"mount"},
		"netstat":  {"netstat", "-rn"},
		"sockstat": {"sockstat", "-l"},
		"ps":       {"ps", "auxww"},
		"ifconfig": {"ifconfig"},
		"dmesg":    {"dmesg", "-T"},
		"top":      {"top", "-H", "-S", "-n", "20", "-d", "1"},
	}

	mux.HandleFunc("/diag/log", func(w http.ResponseWriter, r *http.Request) {
		file := r.URL.Query().Get("file")
		lines := r.URL.Query().Get("lines")
		if lines == "" {
			lines = "200"
		}
		path, ok := allowedLogs[file]
		if !ok {
			http.Error(w, "unknown log file: "+file, http.StatusBadRequest)
			return
		}
		out, err := exec.Command("tail", "-n", lines, path).CombinedOutput()
		if err != nil && len(out) == 0 {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(out)
	})

	mux.HandleFunc("/diag/cmd", func(w http.ResponseWriter, r *http.Request) {
		cmd := r.URL.Query().Get("cmd")
		args, ok := allowedCmds[cmd]
		if !ok {
			http.Error(w, "unknown command: "+cmd, http.StatusBadRequest)
			return
		}
		out, err := exec.Command(args[0], args[1:]...).CombinedOutput()
		if err != nil && len(out) == 0 {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write(out)
	})

	// ── ZFS 복제 ───────────────────────────────────────────────────────────────────
	var zfsDatasetRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_/.\-]{0,254}$`)

	mux.HandleFunc("/replication/run", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			SourceDataset string `json:"source_dataset"`
			TargetPath    string `json:"target_path"`
			LastSnapshot  string `json:"last_snapshot"`
			Recursive     bool   `json:"recursive"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if !zfsDatasetRe.MatchString(req.SourceDataset) {
			http.Error(w, "invalid source dataset", http.StatusBadRequest)
			return
		}

		// 1. 소스 데이터셋의 최신 스냅샷 조회
		listArgs := []string{"list", "-t", "snapshot", "-o", "name", "-s", "creation", "-H"}
		if req.Recursive {
			listArgs = append(listArgs, "-r")
		}
		listArgs = append(listArgs, req.SourceDataset)
		listOut, err := exec.Command("zfs", listArgs...).Output()
		if err != nil {
			http.Error(w, "zfs list snapshots 실패: "+err.Error(), http.StatusInternalServerError)
			return
		}
		var snaps []string
		for _, l := range strings.Split(strings.TrimSpace(string(listOut)), "\n") {
			if l = strings.TrimSpace(l); l != "" {
				snaps = append(snaps, l)
			}
		}
		if len(snaps) == 0 {
			http.Error(w, req.SourceDataset+"에 스냅샷이 없습니다. 먼저 스냅샷을 생성하세요.", http.StatusBadRequest)
			return
		}
		currFullSnap := snaps[len(snaps)-1] // e.g. "zdata/media@auto-20260628-120000"
		atIdx := strings.LastIndex(currFullSnap, "@")
		currSnapLabel := currFullSnap[atIdx+1:]

		if req.LastSnapshot != "" && req.LastSnapshot == currSnapLabel {
			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(map[string]string{
				"current_snapshot": currSnapLabel,
				"output":           "이미 최신 상태입니다.",
			})
			return
		}

		// 2. send 명령 구성 (증분 vs 전체)
		sendArgs := []string{"send"}
		if req.Recursive {
			sendArgs = append(sendArgs, "-R")
		}
		prevSnap := ""
		if req.LastSnapshot != "" {
			// 이전 스냅샷이 여전히 존재하는지 확인
			prevFull := req.SourceDataset + "@" + req.LastSnapshot
			for _, s := range snaps {
				if s == prevFull {
					prevSnap = prevFull
					break
				}
			}
		}
		if prevSnap != "" {
			sendArgs = append(sendArgs, "-i", prevSnap, currFullSnap)
			auditLog("zfs send -i %s %s | recv %s", prevSnap, currFullSnap, req.TargetPath)
		} else {
			sendArgs = append(sendArgs, currFullSnap)
			auditLog("zfs send %s | recv %s", currFullSnap, req.TargetPath)
		}

		sendCmd := exec.Command("zfs", sendArgs...)

		// 3. receive 명령 구성 (로컬 vs 원격)
		var recvCmd *exec.Cmd
		isRemote := strings.Contains(req.TargetPath, ":") && !strings.HasPrefix(req.TargetPath, "/")
		if isRemote {
			colonIdx := strings.Index(req.TargetPath, ":")
			hostPart := req.TargetPath[:colonIdx]
			dataset := req.TargetPath[colonIdx+1:]
			recvCmd = exec.Command("ssh",
				"-o", "StrictHostKeyChecking=accept-new",
				"-o", "BatchMode=yes",
				hostPart, "zfs", "receive", "-F", dataset,
			)
		} else {
			// 로컬 대상 경로 검증
			if !zfsDatasetRe.MatchString(req.TargetPath) {
				http.Error(w, "invalid target path", http.StatusBadRequest)
				return
			}
			recvCmd = exec.Command("zfs", "receive", "-F", req.TargetPath)
		}

		// 4. 파이프 연결
		pipe, err := sendCmd.StdoutPipe()
		if err != nil {
			http.Error(w, "pipe: "+err.Error(), http.StatusInternalServerError)
			return
		}
		recvCmd.Stdin = pipe
		var sendStderr, recvStderr strings.Builder
		sendCmd.Stderr = &sendStderr
		recvCmd.Stderr = &recvStderr

		if err := sendCmd.Start(); err != nil {
			http.Error(w, "zfs send start: "+err.Error(), http.StatusInternalServerError)
			return
		}
		if err := recvCmd.Start(); err != nil {
			sendCmd.Process.Kill()
			http.Error(w, "zfs receive start: "+err.Error(), http.StatusInternalServerError)
			return
		}
		sendErr := sendCmd.Wait()
		recvErr := recvCmd.Wait()

		if sendErr != nil {
			msg := sendStderr.String()
			if msg == "" {
				msg = sendErr.Error()
			}
			http.Error(w, "zfs send 실패: "+msg, http.StatusInternalServerError)
			return
		}
		if recvErr != nil {
			msg := recvStderr.String()
			if msg == "" {
				msg = recvErr.Error()
			}
			http.Error(w, "zfs receive 실패: "+msg, http.StatusInternalServerError)
			return
		}

		output := strings.TrimSpace(sendStderr.String())
		if output == "" {
			mode := "전체"
			if prevSnap != "" {
				mode = "증분"
			}
			output = fmt.Sprintf("%s 복제 완료: %s", mode, currFullSnap)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"current_snapshot": currSnapLabel,
			"output":           output,
		})
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
