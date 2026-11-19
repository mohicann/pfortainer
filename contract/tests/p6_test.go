package tests

import (
	"encoding/base64"
	"encoding/json"
	"golang.org/x/sys/unix"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

type hstep struct {
	Name  string       `json:"name"`
	Req   string       `json:"req"`
	Snap  h.Snap       `json:"snap"`
	Calls []h.ShimCall `json:"calls,omitempty"`
	Files any          `json:"files,omitempty"`
}

type hrun struct {
	t     *testing.T
	hd    *h.Hostd
	m     *h.Masker
	steps []hstep
}

func newHrun(t *testing.T, hd *h.Hostd) *hrun {
	return &hrun{t: t, hd: hd, m: hd.Masker()}
}

func (r *hrun) maskCalls(cs []h.ShimCall) []h.ShimCall {
	for i := range cs {
		for j := range cs[i].Args {
			cs[i].Args[j] = r.m.Apply(cs[i].Args[j])
		}
		cs[i].Stdin = r.m.Apply(cs[i].Stdin)
	}
	return cs
}

func (r *hrun) do(name, method, path string, body any) *h.Resp {
	var b string
	switch x := body.(type) {
	case nil:
	case string:
		b = x
	default:
		bb, _ := json.Marshal(x)
		b = string(bb)
	}
	resp := r.hd.Do(r.t, method, path, b)
	r.steps = append(r.steps, hstep{Name: name, Req: r.m.Apply(method + " " + path + " " + b), Snap: h.SnapResp(resp, r.m), Calls: r.maskCalls(r.hd.Calls())})
	return resp
}

func (r *hrun) files(root string) {
	r.steps[len(r.steps)-1].Files = treeSnap(r.t, root, r.m)
}

func (r *hrun) golden(name string) { h.Golden(r.t, name, r.steps) }

// treeSnap: 디렉터리 아래 파일 경로·모드·내용.
func treeSnap(t *testing.T, root string, m *h.Masker) any {
	var out []map[string]any
	filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		e := map[string]any{"path": m.Apply(p), "mode": info.Mode().String()}
		if info.Mode().IsRegular() {
			b, _ := os.ReadFile(p)
			e["content"] = m.Apply(string(b))
		}
		out = append(out, e)
		return nil
	})
	return out
}

func hostdWithCaptures(t *testing.T) *h.Hostd {
	hd := h.StartHostd(t, h.CmdRules(t))
	hd.InstallHostFiles(t)
	return hd
}

// TestHostdReads: 조회 엔드포인트 — 실제 수집 출력 기준.
func TestHostdReads(t *testing.T) {
	hd := hostdWithCaptures(t)
	r := newHrun(t, hd)
	r.m.Re(`"tcp_estab":[0-9.]+`, `"tcp_estab":"<host>"`)
	// metrics/host의 net은 HashMap이라 키 순서가 매번 다름 → JSON으로 다시 직렬화(키 정렬)
	r.m.Func(func(s string) string {
		if !strings.HasPrefix(s, `{"net":`) {
			return s
		}
		var v any
		if json.Unmarshal([]byte(s), &v) != nil {
			return s
		}
		b, _ := json.Marshal(v)
		return string(b)
	})
	for _, p := range []string{"/sockstat", "/jls", "/ps?jid=5", "/ps?jid=abc", "/ps", "/zpool-status", "/zpool-list", "/zfs-list", "/zfs-list-snapshot",
		"/zfs-list-snapshot?dataset=zdata%2Fapps", "/zfs-list-snapshot?dataset=..%2Fbad", "/zfs-list-snapshot?dataset=", "/smart-scan", "/smart?dev=ada0", "/smart?dev=nvme0", "/smart?dev=sda", "/smart?dev=ada0;ls",
		"/localusers/status", "/nfs/status", "/smb/status", "/network/status", "/appjail/list", "/appjail/detail?name=audio", "/appjail/detail?name=nope", "/appjail/detail?name=..%2F..",
		"/diag/cmd?cmd=df", "/diag/cmd?cmd=mount", "/diag/cmd?cmd=ifconfig", "/diag/cmd?cmd=bogus", "/diag/cmd", "/diag/log?file=dmesg", "/diag/log?file=passwd", "/diag/log", "/metrics/host"} {
		r.do(p, "GET", p, nil)
	}
	r.golden("p6/reads")
}

// TestHostdCommandFailures: 명령 실패·누락·느슨한 출력.
func TestHostdCommandFailures(t *testing.T) {
	hd := h.StartHostd(t, map[string]h.ShimRule{
		"smartctl -a /dev/ada1":       {Stdout: "partial output\nSMART overall-health self-assessment test result: PASSED\n", Exit: 4},
		"smartctl -a /dev/ada2":       {Exit: 2, Stderr: "open failed"},
		"zpool status":                {Exit: 1, Stderr: "no pools available"},
		"zfs *":                       {Exit: 1, Stderr: "cannot open: dataset does not exist", Stdout: "partial"},
		"sockstat -l":                 {Exit: 3},
		"jls jid name":                {Stdout: "1 a\n", Exit: 0},
		"service nfsd status":         {Stdout: "nfsd is running as pid 99.\n"},
		"service samba_server status": {Stdout: "smbd is running as pid 1 2.\n"},
		"pdbedit -L":                  {Stdout: "mohicann:3000:\nbob:3001:Bob\n"},
		"ifconfig -a":                 {Exit: 1, Stderr: "ifconfig: boom"},
		"netstat -rn":                 {Stdout: "Routing tables\n"},
		"tail *":                      {Stdout: "", Stderr: "tail: cannot open", Exit: 1},
		"df -h":                       {Stdout: "", Exit: 1},
		"appjail *":                   {Exit: 1, Stderr: "appjail: not ready"},
	})
	hd.InstallHostFiles(t)
	r := newHrun(t, hd)
	for _, p := range []string{"/smart?dev=ada1", "/smart?dev=ada2", "/zpool-status", "/zpool-list", "/zfs-list", "/zfs-list-snapshot", "/sockstat", "/jls", "/ps?jid=1",
		"/nfs/status", "/smb/status", "/localusers/status", "/network/status", "/diag/log?file=messages", "/diag/cmd?cmd=df", "/diag/cmd?cmd=top", "/appjail/list", "/appjail/detail?name=audio", "/smart-scan"} {
		r.do(p, "GET", p, nil)
	}
	r.golden("p6/command_failures")
}

// TestHostdZfsMutations: 데이터셋·스냅샷·롤백·복제(clone)·정리(prune).
func TestHostdZfsMutations(t *testing.T) {
	hd := h.StartHostd(t, map[string]h.ShimRule{
		"zfs *": {Stdout: ""},
		"zfs list -H -p -t snapshot -o name -s creation -r zdata/apps": {Stdout: "zdata/apps@auto-1\nzdata/apps@manual-x\nzdata/apps@auto-2\nzdata/apps@auto-3\nzdata/apps/child@auto-9\nzdata/apps@auto-4\n"},
		"zfs list -t snapshot -o name -s creation -H zdata/apps":       {Stdout: "zdata/apps@auto-1\nzdata/apps@manual-x\nzdata/apps@auto-2\nzdata/apps@auto-3\nzdata/apps@auto-4\n"},
		"zfs list -H -t snapshot -o name -s creation zdata/apps":       {Stdout: "zdata/apps@auto-1\nzdata/apps@manual-x\nzdata/apps@auto-2\nzdata/apps@auto-3\nzdata/apps@auto-4\n"},
		"zfs list -H -p -t snapshot -o name -s creation zdata/apps":    {Stdout: "zdata/apps@auto-1\nzdata/apps@manual-x\nzdata/apps@auto-2\nzdata/apps@auto-3\nzdata/apps@auto-4\n"},
		"zfs list -H -o name -t snapshot -s creation zdata/apps":       {Stdout: "zdata/apps@auto-1\nzdata/apps@manual-x\nzdata/apps@auto-2\nzdata/apps@auto-3\nzdata/apps@auto-4\n"},
		"zfs destroy zdata/apps@auto-2":                                {Exit: 1, Stderr: "dataset is busy"},
	})
	r := newHrun(t, hd)
	P := func(name, path string, body any) { r.do(name, "POST", path, body) }
	P("dataset_ok", "/zfs/dataset", map[string]any{"path": "zdata/apps/new"})
	P("dataset_bad", "/zfs/dataset", map[string]any{"path": "zdata/apps/../x;rm"})
	P("dataset_leading_slash", "/zfs/dataset", map[string]any{"path": "/zdata/x"})
	P("dataset_empty", "/zfs/dataset", map[string]any{})
	P("dataset_not_json", "/zfs/dataset", "path=x")
	P("dataset_delete_ok", "/zfs/dataset/delete", map[string]any{"name": "zdata/apps/new"})
	P("dataset_delete_pool", "/zfs/dataset/delete", map[string]any{"name": "zdata"})
	P("dataset_delete_bad", "/zfs/dataset/delete", map[string]any{"name": "-rf zdata"})
	P("snapshot_ok", "/zfs/snapshot", map[string]any{"dataset": "zdata/apps", "snapname": "manual-1"})
	P("snapshot_bad_name", "/zfs/snapshot", map[string]any{"dataset": "zdata/apps", "snapname": "a b"})
	P("snapshot_bad_ds", "/zfs/snapshot", map[string]any{"dataset": "zdata apps", "snapname": "x"})
	P("snapshot_delete_ok", "/zfs/snapshot/delete", map[string]any{"name": "zdata/apps@manual-1"})
	P("snapshot_delete_no_at", "/zfs/snapshot/delete", map[string]any{"name": "zdata/apps"})
	P("snapshot_delete_bad", "/zfs/snapshot/delete", map[string]any{"name": "zdata/apps@x y"})
	P("rollback_ok", "/zfs/rollback", map[string]any{"name": "zdata/apps@manual-1"})
	P("rollback_bad", "/zfs/rollback", map[string]any{"name": "zdata/apps"})
	P("clone_ok", "/zfs/clone", map[string]any{"name": "zdata/apps@manual-1", "target": "zdata/apps-clone"})
	P("clone_bad_target", "/zfs/clone", map[string]any{"name": "zdata/apps@manual-1", "target": "zdata/a b"})
	P("clone_bad_src", "/zfs/clone", map[string]any{"name": "zdata/apps", "target": "zdata/c"})
	P("prune_keep2", "/zfs/snapshot/prune", map[string]any{"dataset": "zdata/apps", "prefix": "auto", "keep": 2})
	P("prune_keep0", "/zfs/snapshot/prune", map[string]any{"dataset": "zdata/apps", "prefix": "auto", "keep": 0})
	P("prune_keep_many", "/zfs/snapshot/prune", map[string]any{"dataset": "zdata/apps", "prefix": "auto", "keep": 10})
	P("prune_bad_prefix", "/zfs/snapshot/prune", map[string]any{"dataset": "zdata/apps", "prefix": "a*", "keep": 1})
	P("prune_empty_prefix", "/zfs/snapshot/prune", map[string]any{"dataset": "zdata/apps", "prefix": "", "keep": 1})
	hd.Rule("zfs *", h.ShimRule{Exit: 1, Stderr: "cannot create: permission denied"})
	P("dataset_fail", "/zfs/dataset", map[string]any{"path": "zdata/x"})
	P("snapshot_fail", "/zfs/snapshot", map[string]any{"dataset": "zdata/apps", "snapname": "s"})
	r.golden("p6/zfs_mutations")
}

// TestHostdReplication: zfs send | receive 파이프, 증분 기준 선택, 대상 확인으로 last_snapshot 복구.
func TestHostdReplication(t *testing.T) {
	src := "zdata/apps@auto-1\nzdata/apps@auto-2\nzdata/apps@auto-3\n"
	cases := []struct {
		name  string
		rules map[string]h.ShimRule
		body  map[string]any
	}{
		{"first_full", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H zbak/apps": {Exit: 1, Stderr: "dataset does not exist"}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps"}},
		{"incremental", nil, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps", "last_snapshot": "auto-2"}},
		{"already_latest", nil, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps", "last_snapshot": "auto-3"}},
		{"recover_common", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H zbak/apps": {Stdout: "zbak/apps@auto-1\nzbak/apps@auto-2\n"}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps"}},
		{"recover_target_latest", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H zbak/apps": {Stdout: "zbak/apps@auto-3\n"}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps", "last_snapshot": "gone"}},
		{"recursive", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H -r zdata/apps": {Stdout: src}, "zfs list -t snapshot -o name -s creation -H zbak/apps": {Stdout: ""}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps", "recursive": true}},
		{"remote_ssh", nil, map[string]any{"source_dataset": "zdata/apps", "target_path": "backup@host:tank/apps", "last_snapshot": "auto-1"}},
		{"no_snapshots", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H zdata/apps": {Stdout: "\n"}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps"}},
		{"list_fails", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H zdata/apps": {Exit: 2}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps"}},
		{"bad_source", nil, map[string]any{"source_dataset": "zdata apps", "target_path": "zbak/apps"}},
		{"bad_target", map[string]h.ShimRule{"zfs list -t snapshot -o name -s creation -H zbak/a b": {Stdout: ""}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/a b"}},
		{"send_fails", map[string]h.ShimRule{"zfs send -i zdata/apps@auto-2 zdata/apps@auto-3": {Exit: 1, Stderr: "send: signal received"}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps", "last_snapshot": "auto-2"}},
		{"recv_fails", map[string]h.ShimRule{"zfs receive -F zbak/apps": {Exit: 1, Stderr: "cannot receive: destination has been modified"}}, map[string]any{"source_dataset": "zdata/apps", "target_path": "zbak/apps", "last_snapshot": "auto-2"}},
	}
	var all []hstep
	for _, c := range cases {
		rules := map[string]h.ShimRule{
			"zfs list -t snapshot -o name -s creation -H zdata/apps": {Stdout: src},
			"zfs list -t snapshot -o name -s creation -H zbak/apps":  {Stdout: "zbak/apps@auto-2\n"},
			"zfs send *":               {Stdout: "ZFS-STREAM-BYTES"},
			"zfs receive -F zbak/apps": {Stdout: "received"},
			"ssh *":                    {Stdout: "remote ok"},
		}
		for k, v := range c.rules {
			rules[k] = v
		}
		hd := h.StartHostd(t, rules)
		r := newHrun(t, hd)
		r.do(c.name, "POST", "/replication/run", c.body)
		all = append(all, r.steps...)
	}
	h.Golden(t, "p6/replication", all)
}

// TestHostdLocalUsers: pw/smbpasswd 호출(비밀번호는 stdin).
func TestHostdLocalUsers(t *testing.T) {
	hd := h.StartHostd(t, map[string]h.ShimRule{"pw *": {}, "smbpasswd *": {Stdout: "Added user bob.\n"}, "pdbedit -L": {Stdout: "bob:3001:\n"}})
	hd.InstallHostFiles(t)
	r := newHrun(t, hd)
	P := func(name, path string, body any) { r.do(name, "POST", path, body) }
	P("create_full", "/localusers/create", map[string]any{"username": "bob", "fullname": "Bob Kim", "shell": "/bin/sh", "password": "pw 1", "smb_password": "smb\npw"})
	P("create_minimal", "/localusers/create", map[string]any{"username": "carol"})
	P("create_bad_name", "/localusers/create", map[string]any{"username": "9bob"})
	P("create_bad_shell", "/localusers/create", map[string]any{"username": "dave", "shell": "/bin/zsh"})
	P("create_fullname_colon", "/localusers/create", map[string]any{"username": "eve", "fullname": "a:b,c"})
	P("delete_ok", "/localusers/delete", map[string]any{"username": "bob"})
	P("delete_root", "/localusers/delete", map[string]any{"username": "root"})
	P("delete_bad", "/localusers/delete", map[string]any{"username": "-r"})
	P("smbpasswd_ok", "/localusers/smbpasswd", map[string]any{"username": "bob", "password": "s3cret"})
	P("smbpasswd_empty", "/localusers/smbpasswd", map[string]any{"username": "bob", "password": ""})
	P("group_create", "/localgroups/create", map[string]any{"name": "staff"})
	P("group_create_bad", "/localgroups/create", map[string]any{"name": "st aff"})
	P("group_delete", "/localgroups/delete", map[string]any{"name": "staff"})
	P("group_delete_wheel", "/localgroups/delete", map[string]any{"name": "wheel"})
	P("member_add", "/localgroups/member", map[string]any{"group": "staff", "username": "bob", "action": "add"})
	P("member_remove", "/localgroups/member", map[string]any{"group": "staff", "username": "bob", "action": "remove"})
	P("member_bad_action", "/localgroups/member", map[string]any{"group": "staff", "username": "bob", "action": "x"})
	hd.Rule("pw *", h.ShimRule{Exit: 65, Stderr: "pw: user 'bob' already exists"})
	P("create_pw_fails", "/localusers/create", map[string]any{"username": "bob"})
	P("group_create_fails", "/localgroups/create", map[string]any{"name": "staff"})
	r.do("status", "GET", "/localusers/status", nil)
	r.golden("p6/localusers")
}

// TestHostdShares: SMB·NFS 설정 파일 쓰기/삭제와 reload.
func TestHostdShares(t *testing.T) {
	hd := h.StartHostd(t, map[string]h.ShimRule{"service *": {Stdout: "ok\n"}, "smbpasswd *": {}})
	root := hd.Root
	os.MkdirAll(filepath.Join(root, "usr/local/etc"), 0o755)
	os.WriteFile(filepath.Join(root, "usr/local/etc/smb4.conf"), []byte("[global]\n\tworkgroup = WORKGROUP\n"), 0o644)
	r := newHrun(t, hd)
	P := func(name, path string, body any) {
		r.do(name, "POST", path, body)
		r.files(filepath.Join(root, "usr/local/etc"))
	}
	Pn := func(name, path string, body any) {
		r.do(name, "POST", path, body)
		r.files(filepath.Join(root, "etc"))
	}
	P("smb_setup", "/smb/setup", nil)
	P("smb_setup_again", "/smb/setup", nil)
	P("smb_share_full", "/smb/share", map[string]any{"name": "media", "path": "/zdata/media", "comment": "Media", "valid_users": "@staff bob", "read_only": true, "browseable": false, "guest_ok": true})
	P("smb_share_min", "/smb/share", map[string]any{"name": "pub", "path": "/zdata/pub"})
	P("smb_share_bad_name", "/smb/share", map[string]any{"name": "../x", "path": "/zdata"})
	P("smb_share_rel_path", "/smb/share", map[string]any{"name": "x", "path": "zdata"})
	P("smb_share_newline_inject", "/smb/share", map[string]any{"name": "inj", "path": "/zdata/x", "comment": "c\n[global]\nevil = yes"})
	r.do("smb_status", "GET", "/smb/status", nil)
	P("smb_share_delete", "/smb/share/delete", map[string]any{"name": "pub"})
	P("smb_share_delete_missing", "/smb/share/delete", map[string]any{"name": "nope"})
	P("smb_reload", "/smb/reload", nil)
	Pn("nfs_export_full", "/nfs/export", map[string]any{"name": "media", "path": "/zdata/media", "line": "/zdata/media -ro -maproot=root 192.168.0.0/24", "clients": "192.168.0.0/24"})
	Pn("nfs_export_min", "/nfs/export", map[string]any{"name": "m", "path": "/zdata/m"})
	Pn("nfs_export_bad_name", "/nfs/export", map[string]any{"name": "a/b", "path": "/zdata"})
	Pn("nfs_export_rel", "/nfs/export", map[string]any{"name": "r", "path": "zdata"})
	r.do("nfs_status", "GET", "/nfs/status", nil)
	Pn("nfs_export_delete", "/nfs/export/delete", map[string]any{"name": "m"})
	Pn("nfs_export_delete_missing", "/nfs/export/delete", map[string]any{"name": "zz"})
	Pn("nfs_reload", "/nfs/reload", nil)
	hd.Rule("service *", h.ShimRule{Exit: 1, Stderr: "mountd not running"})
	Pn("nfs_reload_fails", "/nfs/reload", nil)
	P("smb_reload_fails", "/smb/reload", nil)
	os.Remove(filepath.Join(root, "usr/local/etc/smb4.conf"))
	P("smb_setup_no_main_conf", "/smb/setup", nil)
	r.golden("p6/shares")
}

// TestHostdAppJail: 시작/중지/재시작, 이름 검증, 동시 변경 직렬화(락).
func TestHostdAppJail(t *testing.T) {
	hd := h.StartHostd(t, map[string]h.ShimRule{"appjail start audio": {Stdout: "[00:00:01] [ info  ] [audio] Starting audio...\n"}, "appjail stop audio": {}, "appjail restart audio": {}})
	hd.InstallHostFiles(t)
	r := newHrun(t, hd)
	for _, a := range []string{"start", "stop", "restart"} {
		r.do(a, "POST", "/appjail/"+a, map[string]any{"name": "audio"})
	}
	r.do("start_unknown", "POST", "/appjail/start", map[string]any{"name": "nope"})
	r.do("start_bad_name", "POST", "/appjail/start", map[string]any{"name": "../audio"})
	r.do("start_dash", "POST", "/appjail/start", map[string]any{"name": "-x"})
	hd.Rule("appjail start audio", h.ShimRule{Exit: 1, Stdout: "out", Stderr: "[ error ] audio is already running"})
	r.do("start_fails", "POST", "/appjail/start", map[string]any{"name": "audio"})
	r.golden("p6/appjail")

	// 락: 0.4초 걸리는 restart 두 개를 동시에 → 직렬이면 0.8초 이상
	hd.Rule("appjail restart audio", h.ShimRule{DelayMs: 400})
	var wg sync.WaitGroup
	start := time.Now()
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); hd.Do(t, "POST", "/appjail/restart", `{"name":"audio"}`) }()
	}
	wg.Wait()
	if el := time.Since(start); el < 780*time.Millisecond {
		t.Errorf("appjail 변경이 직렬화되지 않음: %v", el)
	}
	hd.Calls()
}

// TestHostdComposeUp: podman-compose 출력 NDJSON 스트림.
func TestHostdComposeUp(t *testing.T) {
	hd := h.StartHostd(t, map[string]h.ShimRule{"podman-compose *": {Stdout: "line one\nline two <x>\n", Stderr: "warn: something\n"}})
	r := newHrun(t, hd)
	r.m.Re(`pfortainer-compose-[A-Za-z0-9]+`, "pfortainer-compose-<tmp>")
	// 임시 디렉터리: macOS Rust(tempfile)는 /var/folders/…, Go(TMPDIR 없음)는 /tmp — 플랫폼 차이라 같은 값으로
	r.m.Re(`(/private)?/(var/folders/[^"\s]*?|tmp)/pfortainer-compose`, "<TMP>/pfortainer-compose")
	// stdout·stderr를 동시에 읽어 줄 순서가 정해지지 않음 → log 줄만 정렬
	r.m.Func(func(s string) string {
		if !strings.HasPrefix(s, `{"line"`) {
			return s
		}
		lines := strings.Split(strings.TrimSuffix(s, "\n"), "\n")
		var logs, rest []string
		for _, l := range lines {
			if strings.Contains(l, `"type":"log"`) {
				logs = append(logs, l)
			} else {
				rest = append(rest, l)
			}
		}
		sort.Strings(logs)
		return strings.Join(append(logs, rest...), "\n") + "\n"
	})
	r.do("ok", "POST", "/compose-up", "services:\n  web:\n    image: nginx\n")
	hd.Rule("podman-compose *", h.ShimRule{Exit: 3, Stderr: "Error: no such image\n"})
	r.do("fails", "POST", "/compose-up", "services: {}\n")
	r.do("empty", "POST", "/compose-up", "")
	r.golden("p6/compose_up")
}

// TestHostdFs: 파일 작업(실제 임시 디렉터리에서).
func TestHostdFs(t *testing.T) {
	hd := h.StartHostd(t, nil)
	base := filepath.Join(hd.Dir, "data")
	mk := func(rel, content string, mode os.FileMode) {
		p := filepath.Join(base, rel)
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(content), mode)
	}
	mk("b.txt", "bbb", 0o644)
	mk("a.TXT", "a", 0o600)
	mk(".hidden", "h", 0o644)
	mk("sub/inner.bin", strings.Repeat("x", 2048), 0o755)
	mk("big.dat", strings.Repeat("y", 1536*1024), 0o644)
	os.MkdirAll(filepath.Join(base, "emptydir"), 0o750)
	os.Symlink("b.txt", filepath.Join(base, "link"))
	os.Symlink("/nonexistent/target", filepath.Join(base, "dangling"))
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	filepath.Walk(base, func(p string, info os.FileInfo, err error) error {
		d := time.Duration(len(strings.TrimPrefix(p, base))) * time.Minute
		tv := []unix.Timeval{unix.NsecToTimeval(fixed.Add(d).UnixNano()), unix.NsecToTimeval(fixed.Add(d).UnixNano())}
		unix.Lutimes(p, tv) // 심볼릭 링크 자체의 시각도 고정
		return nil
	})
	os.Lchown(filepath.Join(base, "link"), -1, -1)
	r := newHrun(t, hd)
	r.m.Re(`"owner":"[^"]*"`, `"owner":"<owner>"`).Re(`"group":"[^"]*"`, `"group":"<group>"`)
	enc := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, "/", "%2F"), " ", "%20") }
	r.m = &h.Masker{}
	r.m.Lit(enc(hd.Dir), "<DIR_ENC>").Lit(hd.Root, "<ROOT>").Lit(hd.Dir, "<DIR>")
	r.m.Re(`"owner":"[^"]*"`, `"owner":"<owner>"`).Re(`"group":"[^"]*"`, `"group":"<group>"`)
	G := func(name, path string) { r.do(name, "GET", path, nil) }
	G("list", "/fs/list?path="+enc(base))
	G("list_hidden", "/fs/list?path="+enc(base)+"&hidden=1")
	G("list_size_desc", "/fs/list?path="+enc(base)+"&sort=size&order=desc")
	G("list_mtime", "/fs/list?path="+enc(base)+"&sort=mtime&order=asc")
	G("list_bad_sort", "/fs/list?path="+enc(base)+"&sort=x&order=y")
	G("list_rel", "/fs/list?path=data")
	G("list_dotdot", "/fs/list?path="+enc(base)+"%2F..%2Fdata")
	G("list_missing", "/fs/list?path="+enc(base)+"%2Fnope")
	G("list_file", "/fs/list?path="+enc(base)+"%2Fb.txt")
	G("stat_file", "/fs/stat?path="+enc(base)+"%2Fb.txt")
	G("stat_dir", "/fs/stat?path="+enc(base)+"%2Fsub")
	G("stat_link", "/fs/stat?path="+enc(base)+"%2Flink")
	G("stat_missing", "/fs/stat?path="+enc(base)+"%2Fnope")
	G("read_file", "/fs/read?path="+enc(base)+"%2Fb.txt")
	G("read_dir", "/fs/read?path="+enc(base)+"%2Fsub")
	G("read_missing", "/fs/read?path="+enc(base)+"%2Fnope")
	G("read_rel", "/fs/read?path=b.txt")
	P := func(name, path string, body any) {
		r.do(name, "POST", path, body)
		r.files(base)
	}
	P("write_new", "/fs/write", map[string]any{"path": base + "/new.txt", "content_base64": base64.StdEncoding.EncodeToString([]byte("hello\n"))})
	P("write_overwrite_keeps_mode", "/fs/write", map[string]any{"path": base + "/a.TXT", "content_base64": base64.StdEncoding.EncodeToString([]byte("AA"))})
	P("write_bad_b64", "/fs/write", map[string]any{"path": base + "/x", "content_base64": "!!!"})
	P("write_rel", "/fs/write", map[string]any{"path": "x", "content_base64": ""})
	P("write_into_missing_dir", "/fs/write", map[string]any{"path": base + "/nodir/x", "content_base64": "eA=="})
	P("mkdir", "/fs/mkdir", map[string]any{"dir": base, "name": "made"})
	P("mkdir_exists", "/fs/mkdir", map[string]any{"dir": base, "name": "made"})
	P("mkdir_bad", "/fs/mkdir", map[string]any{"dir": base, "name": "a/b"})
	P("create", "/fs/create", map[string]any{"dir": base, "name": "empty.txt"})
	P("create_exists", "/fs/create", map[string]any{"dir": base, "name": "empty.txt"})
	P("rename", "/fs/rename", map[string]any{"path": base + "/empty.txt", "newName": "renamed.txt"})
	P("rename_bad", "/fs/rename", map[string]any{"path": base + "/renamed.txt", "newName": "../x"})
	P("rename_missing", "/fs/rename", map[string]any{"path": base + "/nope", "newName": "y"})
	P("chmod", "/fs/chmod", map[string]any{"paths": []string{base + "/renamed.txt", base + "/made"}, "mode": "700"})
	P("chmod_bad", "/fs/chmod", map[string]any{"paths": []string{base + "/renamed.txt"}, "mode": "999"})
	P("chmod_missing", "/fs/chmod", map[string]any{"paths": []string{base + "/nope"}, "mode": "644"})
	P("copy_file", "/fs/copy", map[string]any{"src": base + "/b.txt", "dst": base + "/made/b-copy.txt"})
	P("copy_dir", "/fs/copy", map[string]any{"src": base + "/sub", "dst": base + "/made/sub2"})
	P("copy_into_self", "/fs/copy", map[string]any{"src": base + "/made", "dst": base + "/made/inner"})
	P("copy_exists", "/fs/copy", map[string]any{"src": base + "/b.txt", "dst": base + "/made/b-copy.txt"})
	P("move", "/fs/move", map[string]any{"src": base + "/renamed.txt", "dst": base + "/made/moved.txt"})
	P("move_missing", "/fs/move", map[string]any{"src": base + "/nope", "dst": base + "/x"})
	P("delete", "/fs/delete", map[string]any{"paths": []string{base + "/made/sub2", base + "/new.txt"}})
	P("delete_root", "/fs/delete", map[string]any{"paths": []string{"/"}})
	P("delete_missing", "/fs/delete", map[string]any{"paths": []string{base + "/nope"}})
	P("delete_rel", "/fs/delete", map[string]any{"paths": []string{"data"}})
	r.golden("p6/fs")
}

// TestHostdRouter: 404/405/잘못된 JSON.
func TestHostdRouter(t *testing.T) {
	hd := h.StartHostd(t, nil)
	r := newHrun(t, hd)
	r.do("unknown", "GET", "/nope", nil)
	r.do("wrong_method", "GET", "/zfs/snapshot", nil)
	r.do("wrong_method_post", "POST", "/zfs-list", "{}")
	r.do("bad_json", "POST", "/zfs/snapshot", "{")
	r.do("json_wrong_type", "POST", "/zfs/snapshot", `{"dataset":1}`)
	r.do("no_body", "POST", "/zfs/snapshot", nil)
	r.golden("p6/router")
}

var _ = regexp.MustCompile
var _ = sort.Strings
