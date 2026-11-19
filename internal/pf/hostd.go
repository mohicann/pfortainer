package pf

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func nfsExportsDir() string { return Root("/etc/exports.d") }
func smbSharesDir() string  { return Root("/usr/local/etc/smb4.conf.d") }
func smbMainConf() string   { return Root("/usr/local/etc/smb4.conf") }
func appjailBin() string    { return Root("/usr/local/bin/appjail") }
func appjailJailsDir() string {
	return Root("/usr/local/appjail/jails")
}
func appjailConf() string { return Root("/usr/local/etc/appjail/appjail.conf") }

var (
	jailNameRe  = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)
	smartDevRe  = regexp.MustCompile(`^(ada|da|nvd|nvme|ad|vtbd|mfid|nda)[0-9]+$`)
	localUserRe = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_-]{0,30}$`)
	nfsNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,63}$`)
	smbNameRe   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,79}$`)
	zfsDsRe     = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_/.\-]{0,254}$`)
)

var appjailLock sync.Mutex

// RunHostd: root 권한 host agent(Unix 소켓).
func RunHostd() error {
	p := os.Getenv("PATH")
	if p == "" {
		p = "/sbin:/bin:/usr/sbin:/usr/bin:/usr/local/sbin:/usr/local/bin"
	} else {
		p += ":/usr/local/sbin:/usr/local/bin"
	}
	os.Setenv("PATH", p)
	cfg := LoadConfig()
	sock := cfg.HostdSocket
	os.MkdirAll(filepath.Dir(sock), 0o755)
	os.Remove(sock)
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return err
	}
	os.Chmod(sock, 0o666)
	log.Printf("hostd listening on unix:%s", sock)
	return http.Serve(ln, hostdRouter())
}

func audit(format string, a ...any) { log.Printf("[hostd audit] "+format, a...) }

func octet(w http.ResponseWriter, b []byte) {
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(200)
	w.Write(b)
}

func errMsg(w http.ResponseWriter, msg string) { httpx.Text(w, 500, msg) }
func badReq(w http.ResponseWriter, msg string) { httpx.Text(w, 400, msg) }

// runStdout: Output() — 실패 시 상태 문자열.
func runStdout(name string, args ...string) ([]byte, string) {
	audit("exec: %s %s", name, strings.Join(args, " "))
	r := runOutput(name, args, nil)
	if r.spawnErr != "" {
		return nil, r.spawnErr
	}
	if !r.ok {
		return nil, r.status
	}
	return r.stdout, ""
}

func runStdoutLenient(name string, args ...string) ([]byte, string) {
	audit("exec: %s %s", name, strings.Join(args, " "))
	r := runOutput(name, args, nil)
	if r.spawnErr != "" {
		return nil, r.spawnErr
	}
	if !r.ok {
		return r.stdout, r.status
	}
	return r.stdout, ""
}

func combinedText(r cmdResult) (string, string) {
	if r.spawnErr != "" {
		return "", r.spawnErr
	}
	text := strings.TrimSpace(lossy(append(append([]byte{}, r.stdout...), r.stderr...)))
	if r.ok {
		return text, ""
	}
	if text == "" {
		return "", r.status
	}
	return "", text
}

func runCombined(name string, args ...string) (string, string) {
	audit("exec: %s %s", name, strings.Join(args, " "))
	return combinedText(runOutput(name, args, nil))
}

func runCombinedStdin(name, input string, args ...string) (string, string) {
	audit("exec: %s %s <stdin>", name, strings.Join(args, " "))
	return combinedText(runOutput(name, args, []byte(input)))
}

func runWithStdin(name, input string, args ...string) {
	audit("exec: %s %s <stdin>", name, strings.Join(args, " "))
	cmd := exec.Command(name, args...)
	cmd.Stdin = strings.NewReader(input)
	cmd.Run()
}

func cleanValidatePath(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "/") {
		return "", false
	}
	c := cleanPath(raw)
	if strings.Contains(c, "..") {
		return "", false
	}
	return c, true
}

func hjson(w http.ResponseWriter, r *http.Request, name string, fields []httpx.Field) (map[string]any, bool) {
	return httpx.Json(w, r, name, fields)
}

func s(m map[string]any, k string) string { v, _ := m[k].(string); return v }

func hostdRouter() http.Handler {
	rt := httpx.NewRouter()
	G := func(p string, h http.HandlerFunc) { rt.HandleFunc("GET", p, h) }
	P := func(p string, h http.HandlerFunc) { rt.HandleFunc("POST", p, h) }
	G("/sockstat", hSockstat)
	G("/jls", hJls)
	G("/ps", hPs)
	G("/zpool-status", func(w http.ResponseWriter, r *http.Request) { stdoutOr500(w, "zpool", "status") })
	G("/zpool-list", func(w http.ResponseWriter, r *http.Request) {
		stdoutOr500(w, "zpool", "list", "-H", "-p", "-o", "name,size,alloc,free,health")
	})
	G("/metrics/host", func(w http.ResponseWriter, r *http.Request) { httpx.JSON(w, 200, hostSampleLocal()) })
	G("/zfs-list", func(w http.ResponseWriter, r *http.Request) {
		stdoutOr500(w, "zfs", "list", "-H", "-p", "-o", "name,type,used,avail,refer,mountpoint")
	})
	G("/zfs-list-snapshot", hZfsListSnapshot)
	G("/smart-scan", func(w http.ResponseWriter, r *http.Request) { stdoutOr500(w, "smartctl", "--scan") })
	G("/smart", hSmartGet)
	P("/smart/test", hSmartTest)
	P("/zfs/dataset", hZfsDatasetCreate)
	P("/zfs/dataset/delete", hZfsDatasetDelete)
	P("/zfs/snapshot", hZfsSnapshotCreate)
	P("/zfs/snapshot/delete", hZfsSnapNameCmd("destroy"))
	P("/zfs/rollback", hZfsSnapNameCmd("rollback"))
	P("/zfs/clone", hZfsClone)
	P("/zfs/snapshot/prune", hZfsPrune)
	G("/localusers/status", hLocalUsersStatus)
	P("/localusers/create", hLocalUsersCreate)
	P("/localusers/delete", hLocalUsersDelete)
	P("/localusers/smbpasswd", hLocalUsersSMBPasswd)
	P("/localgroups/create", hLocalGroupCmd("groupadd"))
	P("/localgroups/delete", hLocalGroupCmd("groupdel"))
	P("/localgroups/member", hLocalGroupMember)
	G("/nfs/status", hNfsStatus)
	P("/nfs/export", hNfsExportCreate)
	P("/nfs/export/delete", hNfsExportDelete)
	P("/nfs/reload", func(w http.ResponseWriter, r *http.Request) { reloadSvc(w, "mountd") })
	G("/smb/status", hSmbStatus)
	P("/smb/share", hSmbShareCreate)
	P("/smb/share/delete", hSmbShareDelete)
	P("/smb/reload", func(w http.ResponseWriter, r *http.Request) { reloadSvc(w, "samba_server") })
	P("/smb/setup", hSmbSetup)
	G("/network/status", hNetworkStatus)
	G("/diag/log", hDiagLog)
	G("/diag/cmd", hDiagCmd)
	P("/replication/run", hReplicationRun)
	P("/compose-up", hComposeUp)
	G("/appjail/list", hAppjailList)
	G("/appjail/detail", hAppjailDetail)
	P("/appjail/start", hAppjailAction("start"))
	P("/appjail/stop", hAppjailAction("stop"))
	P("/appjail/restart", hAppjailAction("restart"))
	G("/fs/list", hFsList)
	G("/fs/stat", hFsStat)
	G("/fs/read", hFsRead)
	P("/fs/write", hFsWrite)
	P("/fs/mkdir", hFsMkdir)
	P("/fs/create", hFsCreate)
	P("/fs/delete", hFsDelete)
	P("/fs/rename", hFsRename)
	P("/fs/chmod", hFsChmod)
	P("/fs/copy", hFsCopy)
	P("/fs/move", hFsMove)
	return rt
}

func stdoutOr500(w http.ResponseWriter, name string, args ...string) {
	out, e := runStdout(name, args...)
	if e != "" {
		errMsg(w, e)
		return
	}
	octet(w, out)
}

// 진단(감사 로그 없음)
func plainOut(w http.ResponseWriter, name string, args ...string) {
	r := runOutput(name, args, nil)
	switch {
	case r.spawnErr != "":
		errMsg(w, r.spawnErr)
	case !r.ok:
		errMsg(w, r.status)
	default:
		octet(w, r.stdout)
	}
}

func hSockstat(w http.ResponseWriter, r *http.Request) { plainOut(w, "sockstat", "-l") }
func hJls(w http.ResponseWriter, r *http.Request)      { plainOut(w, "jls", "jid", "name") }

func hPs(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "jid", Default: true}})
	if !ok {
		return
	}
	if s(q, "jid") == "" {
		badReq(w, "jid required")
		return
	}
	res := runOutput("ps", []string{"-J", s(q, "jid"), "-o", "pid="}, nil)
	if res.ok {
		octet(w, res.stdout)
		return
	}
	octet(w, nil)
}

func hZfsListSnapshot(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "dataset", Default: true}})
	if !ok {
		return
	}
	args := []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,creation,used,refer", "-s", "creation"}
	if d := s(q, "dataset"); d != "" {
		args = append(args, "-r", d)
	}
	stdoutOr500(w, "zfs", args...)
}

func hSmartGet(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "dev", Default: true}})
	if !ok {
		return
	}
	if !smartDevRe.MatchString(s(q, "dev")) {
		badReq(w, "invalid device")
		return
	}
	out, e := runStdoutLenient("smartctl", "-a", "/dev/"+s(q, "dev"))
	if len(out) == 0 && e != "" {
		errMsg(w, e)
		return
	}
	octet(w, out)
}

func hSmartTest(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "SmartTestReq", []httpx.Field{{Name: "device", Default: true}, {Name: "type", Default: true}})
	if !ok {
		return
	}
	dev, tt := s(q, "device"), s(q, "type")
	if !smartDevRe.MatchString(dev) {
		badReq(w, "invalid device")
		return
	}
	switch tt {
	case "short", "long", "conveyance", "offline":
	default:
		badReq(w, "invalid test type")
		return
	}
	audit("smart-test %s %s", dev, tt)
	res := runOutput("smartctl", []string{"-t", tt, "/dev/" + dev}, nil)
	if res.spawnErr != "" {
		errMsg(w, res.spawnErr)
		return
	}
	txt := strings.TrimSpace(lossy(append(append([]byte{}, res.stdout...), res.stderr...)))
	if !res.ok && !strings.Contains(txt, "has begun") && !strings.Contains(txt, "successful") {
		errMsg(w, txt)
		return
	}
	httpx.JSON(w, 200, map[string]string{"output": txt})
}

func hjsonOK(w http.ResponseWriter) { httpx.JSON(w, 200, map[string]bool{"success": true}) }

func combinedOK(w http.ResponseWriter, name string, args ...string) {
	if _, e := runCombined(name, args...); e != "" {
		errMsg(w, e)
		return
	}
	hjsonOK(w)
}

func hZfsDatasetCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "PathReq", []httpx.Field{{Name: "path", Default: true}})
	if !ok {
		return
	}
	if s(q, "path") == "" {
		badReq(w, "bad request")
		return
	}
	combinedOK(w, "zfs", "create", s(q, "path"))
}

func hZfsDatasetDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "NameReq", []httpx.Field{{Name: "name", Default: true}})
	if !ok {
		return
	}
	if s(q, "name") == "" {
		badReq(w, "bad request")
		return
	}
	combinedOK(w, "zfs", "destroy", s(q, "name"))
}

func hZfsSnapshotCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "SnapCreateReq", []httpx.Field{{Name: "dataset", Default: true}, {Name: "snapname", Default: true}})
	if !ok {
		return
	}
	if s(q, "dataset") == "" || s(q, "snapname") == "" {
		badReq(w, "bad request")
		return
	}
	combinedOK(w, "zfs", "snapshot", s(q, "dataset")+"@"+s(q, "snapname"))
}

func hZfsSnapNameCmd(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := hjson(w, r, "NameReq", []httpx.Field{{Name: "name", Default: true}})
		if !ok {
			return
		}
		n := s(q, "name")
		if n == "" || !strings.Contains(n, "@") {
			badReq(w, "invalid snapshot name")
			return
		}
		if cmd == "rollback" {
			combinedOK(w, "zfs", "rollback", "-r", n)
		} else {
			combinedOK(w, "zfs", "destroy", n)
		}
	}
}

func hZfsClone(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "CloneReq", []httpx.Field{{Name: "name", Default: true}, {Name: "target", Default: true}})
	if !ok {
		return
	}
	n, t := s(q, "name"), s(q, "target")
	if n == "" || t == "" || !strings.Contains(n, "@") {
		badReq(w, "invalid snapshot name")
		return
	}
	combinedOK(w, "zfs", "clone", n, t)
}

func hZfsPrune(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "PruneReq", []httpx.Field{{Name: "dataset", Default: true}, {Name: "prefix", Default: true}, {Name: "keep", Kind: httpx.KI64, Default: true}})
	if !ok {
		return
	}
	ds, prefix, keep := s(q, "dataset"), s(q, "prefix"), q["keep"].(int64)
	if ds == "" {
		badReq(w, "bad request")
		return
	}
	if keep <= 0 {
		hjsonOK(w)
		return
	}
	audit("zfs snapshot prune %s prefix=%s keep=%d", ds, prefix, keep)
	res := runOutput("zfs", []string{"list", "-H", "-p", "-t", "snapshot", "-o", "name,creation", "-s", "creation", "-r", ds}, nil)
	if res.spawnErr != "" {
		errMsg(w, res.spawnErr)
		return
	}
	if !res.ok {
		errMsg(w, res.status)
		return
	}
	var matching []string
	for _, line := range rustLines(strings.TrimSpace(lossy(res.stdout))) {
		if line == "" {
			continue
		}
		name, _, _ := strings.Cut(line, "\t")
		at := strings.LastIndex(name, "@")
		if at < 0 {
			continue
		}
		if name[:at] == ds && strings.HasPrefix(name[at+1:], prefix) {
			matching = append(matching, name)
		}
	}
	for int64(len(matching)) > keep {
		victim := matching[0]
		matching = matching[1:]
		audit("zfs destroy %s (prune)", victim)
		runOutput("zfs", []string{"destroy", victim}, nil)
	}
	hjsonOK(w)
}

func hLocalUsersStatus(w http.ResponseWriter, r *http.Request) {
	passwd, _ := readFileString(Root("/etc/passwd"))
	group, _ := readFileString(Root("/etc/group"))
	userGroups := map[string][]string{}
	groups := []LocalGroup{}
	for _, line := range rustLines(group) {
		f := strings.SplitN(line, ":", 4)
		if len(f) < 4 {
			continue
		}
		gid := parseI64(f[2])
		if gid < 1000 && f[0] != "wheel" {
			continue
		}
		members := []string{}
		for _, m := range strings.Split(f[3], ",") {
			m = strings.TrimSpace(m)
			if m != "" {
				members = append(members, m)
				userGroups[m] = append(userGroups[m], f[0])
			}
		}
		groups = append(groups, LocalGroup{f[0], gid, members})
	}
	smbUsers := map[string]bool{}
	if res := runOutput("pdbedit", []string{"-L"}, nil); res.ok {
		for _, line := range rustLines(lossy(res.stdout)) {
			if u, _, ok := strings.Cut(line, ":"); ok && u != "" {
				smbUsers[u] = true
			}
		}
	}
	users := []LocalUser{}
	for _, line := range rustLines(passwd) {
		f := strings.SplitN(line, ":", 7)
		if len(f) < 7 {
			continue
		}
		uid, gid := parseI64(f[2]), parseI64(f[3])
		if uid < 1000 {
			continue
		}
		g := userGroups[f[0]]
		if g == nil {
			g = []string{}
		}
		users = append(users, LocalUser{f[0], uid, gid, f[4], f[5], f[6], g, smbUsers[f[0]]})
	}
	httpx.JSON(w, 200, LocalUsersStatus{users, groups})
}

func hLocalUsersCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "LocalUserCreateReq", []httpx.Field{{Name: "username", Default: true}, {Name: "fullname", Default: true}, {Name: "shell", Default: true}, {Name: "password", Default: true}, {Name: "smb_password", Default: true}})
	if !ok {
		return
	}
	user := s(q, "username")
	if !localUserRe.MatchString(user) {
		badReq(w, "invalid username")
		return
	}
	shell := s(q, "shell")
	if shell == "" {
		shell = "/sbin/nologin"
	}
	switch shell {
	case "/sbin/nologin", "/bin/sh", "/usr/local/bin/bash", "/bin/csh":
	default:
		badReq(w, "invalid shell")
		return
	}
	args := []string{"useradd", "-n", user, "-m", "-s", shell}
	if fn := s(q, "fullname"); fn != "" {
		args = append(args, "-c", fn)
	}
	if _, e := runCombined("pw", args...); e != "" {
		errMsg(w, e)
		return
	}
	if pw := s(q, "password"); pw != "" {
		runWithStdin("pw", pw+"\n", "usermod", user, "-h", "0")
	}
	if sp := s(q, "smb_password"); sp != "" {
		runWithStdin("smbpasswd", sp+"\n"+sp+"\n", "-a", "-s", user)
	}
	hjsonOK(w)
}

func hLocalUsersDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "UsernameReq", []httpx.Field{{Name: "username", Default: true}})
	if !ok {
		return
	}
	u := s(q, "username")
	if !localUserRe.MatchString(u) {
		badReq(w, "bad request")
		return
	}
	runOutput("smbpasswd", []string{"-x", u}, nil)
	combinedOK(w, "pw", "userdel", "-n", u, "-r")
}

func hLocalUsersSMBPasswd(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "SmbPasswdReq", []httpx.Field{{Name: "username", Default: true}, {Name: "password", Default: true}})
	if !ok {
		return
	}
	u, p := s(q, "username"), s(q, "password")
	if !localUserRe.MatchString(u) || p == "" {
		badReq(w, "bad request")
		return
	}
	if _, e := runCombinedStdin("smbpasswd", p+"\n"+p+"\n", "-a", "-s", u); e != "" {
		errMsg(w, e)
		return
	}
	hjsonOK(w)
}

func hLocalGroupCmd(cmd string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := hjson(w, r, "NameReq", []httpx.Field{{Name: "name", Default: true}})
		if !ok {
			return
		}
		if !localUserRe.MatchString(s(q, "name")) {
			badReq(w, "bad request")
			return
		}
		combinedOK(w, "pw", cmd, "-n", s(q, "name"))
	}
}

func hLocalGroupMember(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "GroupMemberReq", []httpx.Field{{Name: "group", Default: true}, {Name: "username", Default: true}, {Name: "action", Default: true}})
	if !ok {
		return
	}
	if !localUserRe.MatchString(s(q, "group")) || !localUserRe.MatchString(s(q, "username")) {
		badReq(w, "bad request")
		return
	}
	flag := "-m"
	if s(q, "action") == "remove" {
		flag = "-d"
	}
	combinedOK(w, "pw", "groupmod", s(q, "group"), flag, s(q, "username"))
}

func svcRunning(name string) bool {
	res := runOutput("service", []string{name, "status"}, nil)
	if res.spawnErr != "" {
		return false
	}
	return strings.Contains(lossy(append(append([]byte{}, res.stdout...), res.stderr...)), "is running")
}

// readDirNames: read_dir(정렬 없음, OS 순서).
func readDirNames(dir string) []string {
	f, err := os.Open(dir)
	if err != nil {
		return nil
	}
	defer f.Close()
	n, _ := f.Readdirnames(-1)
	return n
}

// extStem: Path::extension / file_stem.
func extStem(name string) (string, string, bool) {
	i := strings.LastIndex(name, ".")
	if i <= 0 {
		return name, "", false
	}
	return name[:i], name[i+1:], true
}

func hNfsStatus(w http.ResponseWriter, r *http.Request) {
	running := svcRunning("nfsd")
	exports := []NfsExport{}
	dir := nfsExportsDir()
	for _, n := range readDirNames(dir) {
		stem, ext, ok := extStem(n)
		if !ok || ext != "exports" {
			continue
		}
		data, err := readFileString(dir + "/" + n)
		if err != nil {
			continue
		}
		for _, line := range rustLines(data) {
			line = strings.TrimSpace(line)
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			f := strings.Fields(line)
			clients := ""
			if len(f) > 1 {
				clients = strings.Join(f[1:], " ")
			}
			exports = append(exports, NfsExport{stem, f[0], line, clients})
			break
		}
	}
	httpx.JSON(w, 200, NfsStatus{running, exports})
}

func hNfsExportCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "NfsExport", []httpx.Field{{Name: "name"}, {Name: "path"}, {Name: "line", Default: true}, {Name: "clients", Default: true}})
	if !ok {
		return
	}
	if !nfsNameRe.MatchString(s(q, "name")) {
		badReq(w, "invalid export name")
		return
	}
	if _, ok := cleanValidatePath(s(q, "path")); !ok {
		badReq(w, "invalid path")
		return
	}
	if err := os.MkdirAll(nfsExportsDir(), 0o777); err != nil {
		errMsg(w, "exports.d mkdir: "+rustIOErr(err))
		return
	}
	out := nfsExportsDir() + "/" + s(q, "name") + ".exports"
	audit("nfs export write %s → %s", s(q, "name"), out)
	if err := os.WriteFile(out, []byte("# managed by pfortainer\n"+s(q, "line")+"\n"), 0o666); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

func removeOK(w http.ResponseWriter, p string) {
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

func hNfsExportDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "NameReq", []httpx.Field{{Name: "name", Default: true}})
	if !ok {
		return
	}
	if !nfsNameRe.MatchString(s(q, "name")) {
		badReq(w, "bad request")
		return
	}
	p := nfsExportsDir() + "/" + s(q, "name") + ".exports"
	audit("nfs export delete %s", p)
	removeOK(w, p)
}

func reloadSvc(w http.ResponseWriter, svc string) {
	out, e := runCombined("service", svc, "reload")
	if e != "" {
		out = e
	}
	httpx.JSON(w, 200, map[string]string{"output": out})
}

func parseSmbConf(data string) SmbShare {
	var sh SmbShare
	b := func(v string) bool { return strings.EqualFold(strings.TrimSpace(v), "yes") }
	for _, line := range rustLines(data) {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") && len(line) >= 2 {
			sh.Name = line[1 : len(line)-1]
			continue
		}
		i := strings.Index(line, "=")
		if i < 0 {
			continue
		}
		k, v := strings.TrimSpace(line[:i]), strings.TrimSpace(line[i+1:])
		switch strings.ToLower(k) {
		case "path":
			sh.Path = v
		case "comment":
			sh.Comment = v
		case "valid users":
			sh.ValidUsers = v
		case "read only":
			sh.ReadOnly = b(v)
		case "browseable":
			sh.Browseable = b(v)
		case "guest ok":
			sh.GuestOk = b(v)
		}
	}
	return sh
}

func smbShareToConf(sh SmbShare) string {
	yn := func(b bool) string {
		if b {
			return "Yes"
		}
		return "No"
	}
	out := fmt.Sprintf("[%s]\n\tpath = %s\n", sh.Name, sh.Path)
	if sh.Comment != "" {
		out += "\tcomment = " + sh.Comment + "\n"
	}
	if sh.ValidUsers != "" {
		out += "\tvalid users = " + sh.ValidUsers + "\n"
	}
	return out + "\tread only = " + yn(sh.ReadOnly) + "\n\tbrowseable = " + yn(sh.Browseable) + "\n\tguest ok = " + yn(sh.GuestOk) + "\n"
}

func hSmbStatus(w http.ResponseWriter, r *http.Request) {
	running := svcRunning("samba_server")
	mainConf, _ := os.ReadFile(smbMainConf())
	setupOK := strings.Contains(lossyStrict(mainConf), "shares.d")
	shares := []SmbShare{}
	dir := smbSharesDir()
	for _, n := range readDirNames(dir) {
		if _, ext, ok := extStem(n); !ok || ext != "conf" {
			continue
		}
		data, err := readFileString(dir + "/" + n)
		if err != nil {
			continue
		}
		if sh := parseSmbConf(data); sh.Name != "" {
			shares = append(shares, sh)
		}
	}
	httpx.JSON(w, 200, SmbStatus{running, setupOK, shares})
}

// lossyStrict: read_to_string(...).unwrap_or_default() — UTF-8 아니면 "".
func lossyStrict(b []byte) string {
	if !utf8Valid(b) {
		return ""
	}
	return string(b)
}

func hSmbShareCreate(w http.ResponseWriter, r *http.Request) {
	b, ok := readJSONBody(w, r)
	if !ok {
		return
	}
	var sh SmbShare
	if err := httpx.Unmarshal(b, &sh); err != nil {
		httpx.Text(w, 422, "Failed to deserialize the JSON body into the target type: "+err.Error())
		return
	}
	if !smbNameRe.MatchString(sh.Name) {
		badReq(w, "invalid share name (alphanumeric, hyphen, underscore only)")
		return
	}
	clean, ok := cleanValidatePath(sh.Path)
	if !ok {
		badReq(w, "invalid path")
		return
	}
	sh.Path = clean
	if err := os.MkdirAll(smbSharesDir(), 0o777); err != nil {
		errMsg(w, "shares.d mkdir: "+rustIOErr(err))
		return
	}
	p := smbSharesDir() + "/" + sh.Name + ".conf"
	audit("smb share write %s → %s", sh.Name, p)
	if err := os.WriteFile(p, []byte(smbShareToConf(sh)), 0o666); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

func hSmbShareDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "NameReq", []httpx.Field{{Name: "name", Default: true}})
	if !ok {
		return
	}
	n := s(q, "name")
	if n == "" || !smbNameRe.MatchString(n) {
		badReq(w, "invalid share name")
		return
	}
	p := smbSharesDir() + "/" + n + ".conf"
	audit("smb share delete %s", p)
	removeOK(w, p)
}

func hSmbSetup(w http.ResponseWriter, r *http.Request) {
	include := "include = " + smbSharesDir() + "/*.conf"
	data, err := readFileString(smbMainConf())
	if err != nil {
		errMsg(w, "smb4.conf 읽기 실패: "+rustIOErr(err))
		return
	}
	if strings.Contains(data, "shares.d") {
		httpx.JSON(w, 200, map[string]string{"message": "이미 설정되어 있습니다."})
		return
	}
	audit("smb setup: append include line to %s", smbMainConf())
	f, err := os.OpenFile(smbMainConf(), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		errMsg(w, "smb4.conf 쓰기 실패: "+rustIOErr(err))
		return
	}
	defer f.Close()
	if _, err := io.WriteString(f, "\n"+include+"\n"); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	httpx.JSON(w, 200, map[string]string{"message": "include 줄 추가 완료. Samba를 reload해주세요."})
}

var (
	flagsRe = regexp.MustCompile(`<([^>]*)>`)
	mtuRe   = regexp.MustCompile(`mtu (\d+)`)
)

func hNetworkStatus(w http.ResponseWriter, r *http.Request) {
	ifOut := ""
	if res := runOutput("ifconfig", []string{"-a"}, nil); res.spawnErr == "" {
		ifOut = lossy(res.stdout)
	}
	ifaces := []NetworkIface{}
	var cur *NetworkIface
	for _, line := range rustLines(ifOut) {
		if line == "" {
			continue
		}
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			if cur != nil {
				ifaces = append(ifaces, *cur)
				cur = nil
			}
			ci := strings.Index(line, ":")
			if ci < 0 {
				continue
			}
			ni := NetworkIface{Name: line[:ci], Flags: []string{}, Inet: []string{}, Inet6: []string{}}
			rest := line[ci+1:]
			if m := flagsRe.FindStringSubmatch(rest); m != nil {
				ni.Flags = strings.Split(m[1], ",")
				for _, f := range ni.Flags {
					if f == "UP" {
						ni.Up = true
					}
				}
			}
			if m := mtuRe.FindStringSubmatch(rest); m != nil {
				if v, ok := parseI32(m[1]); ok {
					ni.Mtu = v
				}
			}
			cur = &ni
			continue
		}
		if cur == nil {
			continue
		}
		t := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(t, "ether "):
			if f := strings.Fields(t); len(f) > 1 {
				cur.Mac = f[1]
			}
		case strings.HasPrefix(t, "inet "):
			f := strings.Fields(t)
			if len(f) >= 2 {
				ip := f[1]
				if len(f) >= 4 && f[2] == "netmask" {
					if hx, ok := strings.CutPrefix(f[3], "0x"); ok {
						if n, err := strconv.ParseUint(hx, 16, 32); err == nil && !strings.HasPrefix(hx, "+") {
							ip += "/" + itoa(popcount(uint32(n)))
						}
					}
				}
				cur.Inet = append(cur.Inet, ip)
			}
		case strings.HasPrefix(t, "inet6 "):
			f := strings.Fields(t)
			if len(f) >= 2 {
				ip := f[1]
				if len(f) >= 4 && f[2] == "prefixlen" {
					ip += "/" + f[3]
				}
				cur.Inet6 = append(cur.Inet6, ip)
			}
		case strings.HasPrefix(t, "status: "):
			cur.Status = t[len("status: "):]
		case strings.HasPrefix(t, "media: "):
			cur.Media = t[len("media: "):]
		}
	}
	if cur != nil {
		ifaces = append(ifaces, *cur)
	}
	routes := ""
	if res := runOutput("netstat", []string{"-rn"}, nil); res.spawnErr == "" {
		routes = strings.TrimSpace(lossy(res.stdout))
	}
	dns := []string{}
	if data, err := readFileString(Root("/etc/resolv.conf")); err == nil {
		for _, l := range rustLines(data) {
			if rest, ok := strings.CutPrefix(l, "nameserver "); ok {
				dns = append(dns, rest)
			}
		}
	}
	httpx.JSON(w, 200, NetworkStatus{ifaces, routes, dns})
}

func popcount(n uint32) int {
	c := 0
	for n != 0 {
		c += int(n & 1)
		n >>= 1
	}
	return c
}

var logPaths = map[string]string{"messages": "/var/log/messages", "security": "/var/log/security", "auth": "/var/log/auth.log", "dmesg": "/var/run/dmesg.boot"}

var diagCmds = map[string][]string{"df": {"df", "-h"}, "mount": {"mount"}, "netstat": {"netstat", "-rn"}, "sockstat": {"sockstat", "-l"},
	"ps": {"ps", "auxww"}, "ifconfig": {"ifconfig"}, "dmesg": {"dmesg"}, "top": {"top", "-H", "-S", "-n", "20", "-d", "1"}}

func combinedOrStatus(w http.ResponseWriter, res cmdResult) {
	if res.spawnErr != "" {
		errMsg(w, res.spawnErr)
		return
	}
	out := append(append([]byte{}, res.stdout...), res.stderr...)
	if len(out) == 0 && !res.ok {
		errMsg(w, res.status)
		return
	}
	octet(w, out)
}

func hDiagLog(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "file", Default: true}, {Name: "lines", Default: true}})
	if !ok {
		return
	}
	p, ok := logPaths[s(q, "file")]
	if !ok {
		badReq(w, "unknown log file: "+s(q, "file"))
		return
	}
	lines := s(q, "lines")
	if lines == "" {
		lines = "200"
	}
	combinedOrStatus(w, runOutput("tail", []string{"-n", lines, Root(p)}, nil))
}

func hDiagCmd(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "cmd", Default: true}})
	if !ok {
		return
	}
	args, ok := diagCmds[s(q, "cmd")]
	if !ok {
		badReq(w, "unknown command: "+s(q, "cmd"))
		return
	}
	combinedOrStatus(w, runOutput(args[0], args[1:], nil))
}

// ── 복제 ─────────────────────────────────────────────────────────────────────

func hReplicationRun(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "ReplicationRunReq", []httpx.Field{{Name: "source_dataset", Default: true}, {Name: "target_path", Default: true}, {Name: "last_snapshot", Default: true}, {Name: "recursive", Kind: httpx.KBool, Default: true}})
	if !ok {
		return
	}
	src, tgt, last, rec := s(q, "source_dataset"), s(q, "target_path"), s(q, "last_snapshot"), q["recursive"].(bool)
	if !zfsDsRe.MatchString(src) {
		badReq(w, "invalid source dataset")
		return
	}
	listArgs := []string{"list", "-t", "snapshot", "-o", "name", "-s", "creation", "-H"}
	if rec {
		listArgs = append(listArgs, "-r")
	}
	listArgs = append(listArgs, src)
	lr := runOutput("zfs", listArgs, nil)
	if lr.spawnErr != "" {
		errMsg(w, "zfs list snapshots 실패: "+lr.spawnErr)
		return
	}
	if !lr.ok {
		errMsg(w, "zfs list snapshots 실패: "+lr.status)
		return
	}
	var snaps []string
	for _, l := range rustLines(strings.TrimSpace(lossy(lr.stdout))) {
		if l = strings.TrimSpace(l); l != "" {
			snaps = append(snaps, l)
		}
	}
	if len(snaps) == 0 {
		badReq(w, src+"에 스냅샷이 없습니다. 먼저 스냅샷을 생성하세요.")
		return
	}
	currFull := snaps[len(snaps)-1]
	at := strings.LastIndex(currFull, "@")
	if at < 0 {
		at = 0
	}
	currLabel := currFull[at+1:]
	if last != "" && last == currLabel {
		httpx.JSON(w, 200, map[string]string{"current_snapshot": currLabel, "output": "이미 최신 상태입니다."})
		return
	}
	sendArgs := []string{"send"}
	if rec {
		sendArgs = append(sendArgs, "-R")
	}
	prev := ""
	if last != "" {
		pf := src + "@" + last
		for _, sn := range snaps {
			if sn == pf {
				prev = pf
			}
		}
	}
	isRemote := strings.Contains(tgt, ":") && !strings.HasPrefix(tgt, "/")
	if prev == "" && !isRemote && zfsDsRe.MatchString(tgt) {
		tr := runOutput("zfs", []string{"list", "-t", "snapshot", "-o", "name", "-s", "creation", "-H", tgt}, nil)
		if tr.ok {
			labels := map[string]bool{}
			for _, l := range rustLines(strings.TrimSpace(lossy(tr.stdout))) {
				t := strings.TrimSpace(l)
				labels[t[strings.LastIndex(t, "@")+1:]] = true
			}
			if labels[currLabel] {
				httpx.JSON(w, 200, map[string]string{"current_snapshot": currLabel, "output": "대상이 이미 최신 상태입니다."})
				return
			}
			for i := len(snaps) - 1; i >= 0; i-- {
				lb := snaps[i][strings.LastIndex(snaps[i], "@")+1:]
				if lb != currLabel && labels[lb] {
					prev = snaps[i]
					break
				}
			}
		}
	}
	if prev != "" {
		sendArgs = append(sendArgs, "-i", prev, currFull)
	} else {
		sendArgs = append(sendArgs, currFull)
	}
	var recvProg string
	var recvArgs []string
	if isRemote {
		ci := strings.Index(tgt, ":")
		host, ds := tgt[:ci], tgt[ci+1:]
		audit("zfs send %s | ssh %s zfs receive -F %s", strings.Join(sendArgs, " "), host, ds)
		recvProg, recvArgs = "ssh", []string{"-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=yes", host, "zfs", "receive", "-F", ds}
	} else {
		if !zfsDsRe.MatchString(tgt) {
			badReq(w, "invalid target path")
			return
		}
		audit("zfs send %s | recv %s", strings.Join(sendArgs, " "), tgt)
		recvProg, recvArgs = "zfs", []string{"receive", "-F", tgt}
	}
	send := exec.Command("zfs", sendArgs...)
	pr, pw, err := os.Pipe()
	if err != nil {
		errMsg(w, "pipe: "+rustIOErr(err))
		return
	}
	var sendErr, recvErr bytes.Buffer
	send.Stdout, send.Stderr = pw, &sendErr
	if err := send.Start(); err != nil {
		pr.Close()
		pw.Close()
		errMsg(w, "zfs send start: "+rustIOErr(err))
		return
	}
	pw.Close()
	recv := exec.Command(recvProg, recvArgs...)
	recv.Stdin, recv.Stderr = pr, &recvErr
	if err := recv.Start(); err != nil {
		pr.Close()
		send.Process.Kill()
		send.Wait()
		errMsg(w, "zfs receive start: "+rustIOErr(err))
		return
	}
	pr.Close()
	se := send.Wait()
	re := recv.Wait()
	if se != nil {
		msg := strings.TrimSpace(lossy(sendErr.Bytes()))
		if msg == "" {
			msg = exitDisplay(se)
		}
		errMsg(w, "zfs send 실패: "+msg)
		return
	}
	if re != nil {
		msg := strings.TrimSpace(lossy(recvErr.Bytes()))
		if msg == "" {
			msg = exitDisplay(re)
		}
		errMsg(w, "zfs receive 실패: "+msg)
		return
	}
	out := strings.TrimSpace(lossy(sendErr.Bytes()))
	if out == "" {
		mode := "전체"
		if prev != "" {
			mode = "증분"
		}
		out = mode + " 복제 완료: " + currFull
	}
	httpx.JSON(w, 200, map[string]string{"current_snapshot": currLabel, "output": out})
}

func exitDisplay(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return rustExitStatus(ee.ProcessState)
	}
	return rustIOErr(err)
}

func hComposeUp(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, httpx.BodyLimit+1))
	if err != nil || len(body) > httpx.BodyLimit {
		httpx.Text(w, http.StatusRequestEntityTooLarge, "Failed to buffer the request body: length limit exceeded")
		return
	}
	p, cleanup, err := composeTempFile(body)
	if err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	defer cleanup()
	streamComposeUp(p, newNDJSON(w))
}

// ── AppJail ──────────────────────────────────────────────────────────────────

func jailExists(name string) bool {
	if !jailNameRe.MatchString(name) {
		return false
	}
	fi, err := os.Stat(appjailJailsDir() + "/" + name)
	return err == nil && fi.IsDir()
}

func splitTabs(line string) []string {
	l := strings.TrimPrefix(line, "\t")
	l = strings.TrimSuffix(l, "\t")
	parts := strings.Split(l, "\t")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

func appjailTable(args ...string) ListTable {
	out, e := runStdout(appjailBin(), args...)
	if e != "" {
		return ListTable{Headers: []string{}, Rows: [][]string{}, Error: e}
	}
	var lines []string
	for _, l := range rustLines(lossy(out)) {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	t := ListTable{Headers: []string{}, Rows: [][]string{}}
	if len(lines) > 0 {
		t.Headers = splitTabs(lines[0])
		for _, l := range lines[1:] {
			t.Rows = append(t.Rows, splitTabs(l))
		}
	}
	return t
}

var listKeywords = []string{"name", "status", "type", "version", "boot", "priority", "network_ip4", "ports", "created"}

func parseJailRow(cols []string) (JailInfo, bool) {
	get := func(i int) string {
		if i < len(cols) {
			return cols[i]
		}
		return ""
	}
	if get(0) == "" {
		return JailInfo{}, false
	}
	st := get(1)
	return JailInfo{Name: get(0), Status: st, Running: st == "UP", JailType: get(2), Version: get(3), Boot: get(4) == "1", Priority: get(5), IP: get(6), Ports: get(7), Created: get(8)}, true
}

func appjailJails(only string) ([]JailInfo, string) {
	args := []string{"jail", "list", "-eHIp"}
	if only != "" {
		args = append(args, "-j", only)
	}
	args = append(args, listKeywords...)
	out, e := runStdout(appjailBin(), args...)
	if e != "" {
		return nil, e
	}
	jails := []JailInfo{}
	for _, l := range rustLines(lossy(out)) {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if j, ok := parseJailRow(splitTabs(l)); ok {
			jails = append(jails, j)
		}
	}
	prio := func(p string) int64 {
		if n, msg := httpx.ParseI64(p); msg == "" {
			return n
		}
		return 1<<63 - 1
	}
	sort.SliceStable(jails, func(i, j int) bool {
		a, b := prio(jails[i].Priority), prio(jails[j].Priority)
		if a != b {
			return a < b
		}
		return jails[i].Name < jails[j].Name
	})
	return jails, ""
}

func hAppjailList(w http.ResponseWriter, r *http.Request) {
	jails, e := appjailJails("")
	if e != "" {
		errMsg(w, e)
		return
	}
	httpx.JSON(w, 200, AppJailStatus{jails})
}

func hAppjailAction(action string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		q, ok := hjson(w, r, "NameReq", []httpx.Field{{Name: "name", Default: true}})
		if !ok {
			return
		}
		name := s(q, "name")
		if !jailExists(name) {
			badReq(w, "unknown jail")
			return
		}
		audit("appjail %s %s", action, name)
		appjailLock.Lock()
		out, e := runCombined(appjailBin(), action, name)
		appjailLock.Unlock()
		if e != "" {
			errMsg(w, e)
			return
		}
		httpx.JSON(w, 200, map[string]string{"output": out})
	}
}

func appjailDataset(name string) string {
	conf, _ := readFileString(appjailConf())
	val := func(key string) (string, bool) {
		v, found := "", false
		for _, l := range rustLines(conf) {
			if rest, ok := strings.CutPrefix(strings.TrimSpace(l), key); ok {
				if r2, ok := strings.CutPrefix(rest, "="); ok {
					v, found = strings.Trim(strings.TrimSpace(r2), `"`), true
				}
			}
		}
		return v, found
	}
	zp, ok := val("ZPOOL")
	if !ok {
		zp = "zroot"
	}
	zr, ok := val("ZROOTFS")
	if !ok {
		zr = "appjail"
	}
	return zp + "/" + zr + "/jails/" + name + "/jail"
}

func hAppjailDetail(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "name", Default: true}})
	if !ok {
		return
	}
	name := s(q, "name")
	if !jailExists(name) {
		badReq(w, "unknown jail")
		return
	}
	jails, e := appjailJails(name)
	if e != "" {
		errMsg(w, e)
		return
	}
	if len(jails) == 0 {
		badReq(w, "unknown jail")
		return
	}
	info := jails[0]
	tc, _ := readFileString(appjailJailsDir() + "/" + name + "/conf/template.conf")
	d := JailDetail{Info: info, Path: appjailJailsDir() + "/" + name + "/jail", TemplateConf: tc,
		Fstab: appjailTable("fstab", "jail", name, "list", "-eIp"), Expose: appjailTable("expose", "list", "-eIp", name),
		Healthcheck: appjailTable("healthcheck", "list", "-eIp", name), Dataset: appjailDataset(name), ZfsUsed: "-", ZfsRefer: "-", Processes: []JailProcess{}}
	if out, e := runStdout("zfs", "list", "-H", "-o", "used,refer", d.Dataset); e == "" {
		f := strings.Fields(strings.TrimSpace(lossy(out)))
		if len(f) > 0 {
			d.ZfsUsed = f[0]
		}
		if len(f) > 1 {
			d.ZfsRefer = f[1]
		}
	}
	if info.Running {
		res := runOutput("ps", []string{"-J", name, "-o", "pid=", "-o", "user=", "-o", "pcpu=", "-o", "rss=", "-o", "command="}, nil)
		if res.ok {
			for _, line := range rustLines(lossy(res.stdout)) {
				f := strings.Fields(line)
				if len(f) == 0 {
					continue
				}
				pid, ok := parseI32(f[0])
				if !ok {
					continue
				}
				g := func(i int) string {
					if i < len(f) {
						return f[i]
					}
					return ""
				}
				cmd := ""
				if len(f) > 4 {
					cmd = strings.Join(f[4:], " ")
				}
				d.Processes = append(d.Processes, JailProcess{pid, g(1), g(2), g(3), cmd})
			}
		}
	}
	httpx.JSON(w, 200, d)
}

// ── fs ───────────────────────────────────────────────────────────────────────

func hFsList(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "path", Default: true}, {Name: "hidden", Default: true}, {Name: "sort", Default: true}, {Name: "order", Default: true}})
	if !ok {
		return
	}
	cwd, _ := safePath(s(q, "path"))
	sortBy := "name"
	if s(q, "sort") == "mtime" {
		sortBy = "mtime"
	}
	dirs, files, err := listDir(cwd, s(q, "hidden") == "1", sortBy, s(q, "order") == "desc")
	if err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	httpx.JSON(w, 200, map[string]any{"dirs": dirs, "files": files})
}

func hFsStat(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "path", Default: true}})
	if !ok {
		return
	}
	p, _ := safePath(s(q, "path"))
	fi, err := os.Stat(p)
	if err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	httpx.JSON(w, 200, map[string]any{"size": uint64(fi.Size()), "is_dir": fi.IsDir()})
}

func hFsRead(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "path", Default: true}})
	if !ok {
		return
	}
	p, _ := safePath(s(q, "path"))
	b, err := os.ReadFile(p)
	if err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	octet(w, b)
}

func hFsWrite(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "FsWriteReq", []httpx.Field{{Name: "path", Default: true}, {Name: "content_base64", Default: true}})
	if !ok {
		return
	}
	p, _ := safePath(s(q, "path"))
	data, err := base64.StdEncoding.Strict().DecodeString(s(q, "content_base64"))
	if err != nil {
		badReq(w, "잘못된 인코딩입니다.")
		return
	}
	audit("fs write: %s (%d bytes)", p, len(data))
	if err := os.WriteFile(p, data, 0o666); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

var dirNameFields = []httpx.Field{{Name: "dir", Default: true}, {Name: "name", Default: true}}

func hFsMkdir(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "FsDirNameReq", dirNameFields)
	if !ok {
		return
	}
	dir, _ := safePath(s(q, "dir"))
	if !validName(s(q, "name")) {
		badReq(w, "잘못된 이름입니다.")
		return
	}
	t := strings.TrimRight(dir, "/") + "/" + s(q, "name")
	audit("fs mkdir: %s", t)
	if err := os.Mkdir(t, 0o777); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

func hFsCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "FsDirNameReq", dirNameFields)
	if !ok {
		return
	}
	dir, _ := safePath(s(q, "dir"))
	if !validName(s(q, "name")) {
		badReq(w, "잘못된 이름입니다.")
		return
	}
	t := strings.TrimRight(dir, "/") + "/" + s(q, "name")
	audit("fs create: %s", t)
	f, err := os.OpenFile(t, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o666)
	if err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	f.Close()
	hjsonOK(w)
}

func hFsDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "FsPathsReq", []httpx.Field{{Name: "paths", Kind: httpx.KStrList, Default: true}})
	if !ok {
		return
	}
	for _, raw := range q["paths"].([]string) {
		p, _ := safePath(raw)
		if p == "/" {
			badReq(w, "루트 디렉토리는 삭제할 수 없습니다.")
			return
		}
		audit("fs delete: %s", p)
		fi, err := os.Lstat(p)
		if err == nil {
			if fi.IsDir() {
				err = os.RemoveAll(p)
			} else {
				err = os.Remove(p)
			}
		}
		if err != nil {
			errMsg(w, rustIOErr(err))
			return
		}
	}
	hjsonOK(w)
}

func hFsRename(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "FsRenameReq", []httpx.Field{{Name: "path", Default: true}, {Name: "newName", Default: true}})
	if !ok {
		return
	}
	p, _ := safePath(s(q, "path"))
	nn := s(q, "newName")
	if nn == "" || strings.ContainsAny(nn, "/\x00") {
		badReq(w, "잘못된 이름입니다.")
		return
	}
	t := strings.TrimRight(parentDir(p), "/") + "/" + nn
	audit("fs rename: %s -> %s", p, t)
	if err := os.Rename(p, t); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

func hFsChmod(w http.ResponseWriter, r *http.Request) {
	q, ok := hjson(w, r, "FsChmodReq", []httpx.Field{{Name: "paths", Kind: httpx.KStrList, Default: true}, {Name: "mode", Default: true}})
	if !ok {
		return
	}
	ms := s(q, "mode")
	if !parseOctalU32(ms) {
		badReq(w, "잘못된 권한 형식입니다 (예: 644).")
		return
	}
	mode, _ := strconv.ParseUint(strings.TrimPrefix(ms, "+"), 8, 32)
	for _, raw := range q["paths"].([]string) {
		p, _ := safePath(raw)
		audit("fs chmod: %s -> %o", p, mode)
		if err := syscall.Chmod(p, uint32(mode)); err != nil {
			errMsg(w, rustIOErr(err))
			return
		}
	}
	hjsonOK(w)
}

func srcDst(w http.ResponseWriter, r *http.Request) (string, string, bool) {
	q, ok := hjson(w, r, "FsSrcDstReq", []httpx.Field{{Name: "src", Default: true}, {Name: "dst", Default: true}})
	if !ok {
		return "", "", false
	}
	a, _ := safePath(s(q, "src"))
	b, _ := safePath(s(q, "dst"))
	return a, b, true
}

func hFsCopy(w http.ResponseWriter, r *http.Request) {
	src, dst, ok := srcDst(w, r)
	if !ok {
		return
	}
	audit("fs copy: %s -> %s", src, dst)
	if err := copyPath(src, dst); err != nil {
		errMsg(w, rustIOErr(err))
		return
	}
	hjsonOK(w)
}

func hFsMove(w http.ResponseWriter, r *http.Request) {
	src, dst, ok := srcDst(w, r)
	if !ok {
		return
	}
	audit("fs move: %s -> %s", src, dst)
	err := os.Rename(src, dst)
	if err == nil {
		hjsonOK(w)
		return
	}
	var le *os.LinkError
	if errors.As(err, &le) && le.Err == syscall.EXDEV {
		if err := copyPath(src, dst); err != nil {
			errMsg(w, rustIOErr(err))
			return
		}
		fi, lerr := os.Lstat(src)
		var rerr error
		if lerr == nil && fi.IsDir() {
			rerr = os.RemoveAll(src)
		} else {
			rerr = os.Remove(src)
		}
		if rerr != nil {
			errMsg(w, "복사는 됐지만 원본 삭제 실패: "+rustIOErr(rerr))
			return
		}
		hjsonOK(w)
		return
	}
	errMsg(w, rustIOErr(err))
}

func readJSONBody(w http.ResponseWriter, r *http.Request) ([]byte, bool) {
	ct := r.Header.Get("Content-Type")
	if !strings.HasPrefix(ct, "application/json") {
		httpx.Text(w, http.StatusUnsupportedMediaType, "Expected request with `Content-Type: application/json`")
		return nil, false
	}
	b, err := io.ReadAll(io.LimitReader(r.Body, httpx.BodyLimit+1))
	if err != nil || len(b) > httpx.BodyLimit {
		httpx.Text(w, http.StatusRequestEntityTooLarge, "Failed to buffer the request body: length limit exceeded")
		return nil, false
	}
	return b, true
}
