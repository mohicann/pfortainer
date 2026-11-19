package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

// run4: 단계별 스냅샷을 모은다.
type runner struct {
	t     *testing.T
	a     *h.App
	m     *h.Masker
	steps []step
}

func newRunner(t *testing.T, a *h.App) *runner {
	m := a.Masker()
	m.Lit(time.Now().Format("2006-01-02"), "<today>")
	return &runner{t: t, a: a, m: m}
}

func (r *runner) add(name string, resp *h.Resp) {
	r.steps = append(r.steps, step{Name: name, Snap: a2(r.a.Capture(resp, r.m))})
}

func (r *runner) addDB(name string, resp *h.Resp, db any) {
	r.steps = append(r.steps, step{Name: name, Snap: a2(r.a.Capture(resp, r.m)), DB: db})
}

func a2(s h.Snap) h.Snap { return s }

func (r *runner) golden(name string) { h.Golden(r.t, name, r.steps) }

func hostdErr(a *h.App, key string, status int, body string) {
	a.Hostd.Set(key, h.Reply{Status: status, Body: body})
}

// TestStoragePages: 파일시스템·스토리지(풀+SMART)·스냅샷 페이지.
func TestStoragePages(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	for _, d := range []string{"ada0", "ada1", "ada2", "ada3", "ada4", "nda0", "nvme0"} {
		a.Hostd.Set("GET /smart?dev="+d, h.Reply{Status: 200, ContentType: "text/plain", Body: h.CaptureBody(t, "hostd", "/smart?dev="+d)})
	}
	r := newRunner(t, a)
	diskOrder(r.m)
	c := a.Client(h.Cookie("viewer1", "viewer"))
	r.add("filesystem", c.Get(t, "/filesystem"))
	r.add("storage", c.Get(t, "/storage"))
	r.add("snapshots", c.Get(t, "/snapshots"))
	r.add("snapshots_msgs", c.Get(t, "/snapshots?success=%3Cok%3E&error=bad+%26"))
	r.add("filesystem_operator", a.Client(h.Cookie("operator1", "operator")).Get(t, "/filesystem"))

	hostdErr(a, "GET /zfs-list", 500, "zfs: permission denied")
	r.add("filesystem_zfs_err", c.Get(t, "/filesystem"))
	r.add("snapshots_zfs_err", c.Get(t, "/snapshots"))
	hostdErr(a, "GET /zpool-list", 500, "zpool list failed")
	r.add("filesystem_zpool_err", c.Get(t, "/filesystem"))
	hostdErr(a, "GET /zpool-status", 500, "zpool status failed")
	r.add("storage_pool_err", c.Get(t, "/storage"))
	hostdErr(a, "GET /smart?dev=ada2", 500, "")
	a.Hostd.Set("GET /smart?dev=ada3", h.Reply{Status: 200, Body: "smartctl: garbage only\n"})
	r.add("storage_smart_partial", c.Get(t, "/storage"))
	hostdErr(a, "GET /smart-scan", 500, "smartctl missing")
	r.add("storage_smart_err", c.Get(t, "/storage"))
	hostdErr(a, "GET /zfs-list-snapshot", 500, "x")
	r.add("snapshots_snap_err", c.Get(t, "/snapshots"))
	a.Hostd.Set("GET /zfs-list", h.Reply{Status: 200, Body: ""})
	a.Hostd.Set("GET /zfs-list-snapshot", h.Reply{Status: 200, Body: "zdata@x\tnotanumber\t1\t2\nbadline\n"})
	r.add("snapshots_odd_lines", c.Get(t, "/snapshots"))
	r.golden("p4/storage_pages")

	// hostd 소켓 없음: 읽기 조회는 로컬 실행 폴백(PATH에 zfs 등 없음)
	b := h.StartAppWithCaptures(t, h.AppOpts{NoHostd: true})
	rb := newRunner(t, b)
	diskOrder(rb.m)
	cb := b.Client(h.Cookie("viewer1", "viewer"))
	for _, p := range []string{"/filesystem", "/storage", "/snapshots", "/localusers", "/shares", "/services", "/network", "/appjail", "/appjail/audio", "/files", "/diagnostics"} {
		rb.add("nohostd "+p, cb.Get(t, p))
	}
	rb.golden("p4/no_hostd_pages")
}

// TestStorageActions: 데이터셋·스냅샷·SMART 테스트 API.
func TestStorageActions(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	r := newRunner(t, a)
	c := a.Client(h.Cookie("operator1", "operator"))
	J := func(name, path string, body any) { r.add(name, c.PostJSON(t, path, body)) }
	J("fs_create_ok", "/api/filesystem/create", map[string]any{"parent": " zdata/apps ", "name": " new-ds_1.x:y "})
	J("fs_create_empty", "/api/filesystem/create", map[string]any{"parent": "zdata"})
	J("fs_create_bad_char", "/api/filesystem/create", map[string]any{"parent": "zdata", "name": "a/b"})
	J("fs_create_unicode", "/api/filesystem/create", map[string]any{"parent": "zdata", "name": "한글"})
	J("fs_create_bad_parent", "/api/filesystem/create", map[string]any{"parent": "zdata/../x", "name": "ok"})
	J("fs_create_number_field", "/api/filesystem/create", `{"parent":1,"name":"x"}`)
	J("fs_create_not_json", "/api/filesystem/create", `parent=zdata`)
	J("fs_delete_ok", "/api/filesystem/delete", map[string]any{"dataset": " zdata/apps/old "})
	J("fs_delete_pool", "/api/filesystem/delete", map[string]any{"dataset": "zdata"})
	J("fs_delete_empty", "/api/filesystem/delete", map[string]any{})
	J("snap_create_ok", "/api/snapshots/create", map[string]any{"dataset": "zdata/apps", "snapname": "manual-1"})
	J("snap_create_empty", "/api/snapshots/create", map[string]any{"dataset": "zdata/apps", "snapname": ""})
	J("snap_create_bad", "/api/snapshots/create", map[string]any{"dataset": "zdata/apps", "snapname": "a b"})
	J("snap_create_at", "/api/snapshots/create", map[string]any{"dataset": "zdata/apps", "snapname": "x@y"})
	J("snap_delete_ok", "/api/snapshots/delete", map[string]any{"name": "zdata/apps@manual-1"})
	J("snap_delete_no_at", "/api/snapshots/delete", map[string]any{"name": "zdata/apps"})
	J("snap_rollback_ok", "/api/snapshots/rollback", map[string]any{"name": "zdata/apps@manual-1"})
	J("snap_rollback_bad", "/api/snapshots/rollback", map[string]any{"name": ""})
	J("snap_clone_ok", "/api/snapshots/clone", map[string]any{"name": "zdata/apps@manual-1", "target": "zdata/apps-clone"})
	J("snap_clone_bad_target", "/api/snapshots/clone", map[string]any{"name": "zdata/apps@manual-1", "target": "zdata/a b"})
	J("smart_test_ok", "/api/smart/test", map[string]any{"device": "ada0", "test_type": "short"})
	J("smart_test_dev_path", "/api/smart/test", map[string]any{"device": "/dev/ada0", "test_type": "long"})
	J("smart_test_bad_type", "/api/smart/test", map[string]any{"device": "ada0", "test_type": "rm"})
	J("smart_test_bad_dev", "/api/smart/test", map[string]any{"device": "sda;rm", "test_type": "short"})
	J("smart_test_missing_field", "/api/smart/test", map[string]any{"device": "ada0"})
	a.Hostd.Set("POST /smart/test", h.Reply{Status: 200, ContentType: "application/json", Body: `{"output":"Testing has begun.\nPlease wait 2 minutes."}`})
	J("smart_test_output", "/api/smart/test", map[string]any{"device": "ada1", "test_type": "short"})
	for _, k := range []string{"/zfs/dataset", "/zfs/dataset/delete", "/zfs/snapshot", "/zfs/snapshot/delete", "/zfs/rollback", "/zfs/clone", "/smart/test"} {
		hostdErr(a, "POST "+k, 500, "cannot do it: dataset is busy")
	}
	J("fs_create_hostd_err", "/api/filesystem/create", map[string]any{"parent": "zdata", "name": "x"})
	J("fs_delete_hostd_err", "/api/filesystem/delete", map[string]any{"dataset": "zdata/x"})
	J("snap_create_hostd_err", "/api/snapshots/create", map[string]any{"dataset": "zdata/apps", "snapname": "s"})
	J("snap_delete_hostd_err", "/api/snapshots/delete", map[string]any{"name": "zdata/apps@s"})
	J("snap_rollback_hostd_err", "/api/snapshots/rollback", map[string]any{"name": "zdata/apps@s"})
	J("snap_clone_hostd_err", "/api/snapshots/clone", map[string]any{"name": "zdata/apps@s", "target": "zdata/c"})
	J("smart_test_hostd_err", "/api/smart/test", map[string]any{"device": "ada0", "test_type": "short"})
	r.golden("p4/storage_actions")

	b := h.StartAppWithCaptures(t, h.AppOpts{NoHostd: true})
	rb := newRunner(t, b)
	cb := b.Client(h.Cookie("operator1", "operator"))
	rb.add("nohostd_fs_create", cb.PostJSON(t, "/api/filesystem/create", map[string]any{"parent": "zdata", "name": "x"}))
	rb.add("nohostd_snap_create", cb.PostJSON(t, "/api/snapshots/create", map[string]any{"dataset": "zdata", "snapname": "x"}))
	rb.add("nohostd_smart", cb.PostJSON(t, "/api/smart/test", map[string]any{"device": "ada0", "test_type": "short"}))
	rb.golden("p4/storage_actions_no_hostd")
}

// nextRunTime: Rust configdb::next_run_time과 같은 규칙(UTC 기준). 골든에서 next_run을 식으로 바꾸는 데만 쓴다.
func nextRunTime(freq string, from int64) int64 {
	t := time.Unix(from, 0).UTC()
	mid := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
	switch freq {
	case "hourly":
		return time.Date(t.Year(), t.Month(), t.Day(), t.Hour(), 0, 0, 0, time.UTC).Add(time.Hour).Unix()
	case "daily":
		return mid.AddDate(0, 0, 1).Unix()
	case "weekly":
		d := mid.AddDate(0, 0, 1)
		for d.Weekday() != time.Monday {
			d = d.AddDate(0, 0, 1)
		}
		return d.Unix()
	case "monthly":
		return time.Date(t.Year(), t.Month()+1, 1, 0, 0, 0, 0, time.UTC).Unix()
	}
	return from + 86400
}

func schedRows(t *testing.T, a *h.App) any {
	rows := h.Rows(t, a.DB(t), `SELECT id, type, target, frequency, retention, prefix, enabled, last_run, next_run, created_at FROM schedules ORDER BY id`)
	for _, r := range rows {
		base, _ := r["created_at"].(int64)
		label := "created_at"
		if lr, ok := r["last_run"].(int64); ok {
			base, label = lr, "last_run"
		}
		if nr, ok := r["next_run"].(int64); ok && nr == nextRunTime(r["frequency"].(string), base) {
			r["next_run"] = "<next_run_time(frequency, " + label + ")>"
		}
	}
	return h.MaskNow(rows, "last_run", "created_at")
}

// diskOrder: SMART 조회가 buffer_unordered라 디스크 행·옵션 순서가 매번 달라 정렬해 비교한다.
func diskOrder(m *h.Masker) {
	m.Func(h.SortAdjacent(`(?s)<tr><td class="mono">(?:ada|da|nvd|nvme|ad|vtbd|mfid|nda)\d+</td>.*?</tr>`))
	m.Func(h.SortAdjacent(`<option value="(?:ada|da|nvd|nvme|ad|vtbd|mfid|nda)\d+">[^<]*</option>`))
}

// TestSchedules: 스냅샷 스케줄 CRUD + 즉시 실행.
func TestSchedules(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	r := newRunner(t, a)
	r.m.Re(`(auto|daily|pre)-\d{8}-\d{6}`, "$1-<ts>")
	diskOrder(r.m)
	r.m.Func(nearNowTimes)
	c := a.Client(h.Cookie("operator1", "operator"))
	F := func(name, path string, kv ...string) { r.addDB(name, c.PostForm(t, path, kv...), schedRows(t, a)) }
	F("create_ok", "/schedules", "type", "snapshot", "target", "zdata/apps", "frequency", "daily", "prefix", " daily ", "retention", " 14 ")
	F("create_defaults", "/schedules", "type", "snapshot", "target", "zdata", "frequency", "hourly")
	F("create_bad_retention", "/schedules", "type", "snapshot", "target", "zdata", "frequency", "weekly", "retention", "abc")
	F("create_neg_retention", "/schedules", "type", "snapshot", "target", "zdata", "frequency", "monthly", "retention", "-3")
	F("create_unknown_freq", "/schedules", "type", "snapshot", "target", "zdata", "frequency", "yearly")
	F("create_unknown_type", "/schedules", "type", "weird", "target", "zdata", "frequency", "daily")
	F("create_scrub", "/schedules", "type", "scrub", "target", "zdata", "frequency", "weekly")
	F("create_smart", "/schedules", "type", "smart_test", "target", "ada0", "frequency", "daily", "prefix", "long")
	F("create_missing", "/schedules", "type", "snapshot", "target", "")
	r.add("page_snapshots_with_schedules", c.Get(t, "/snapshots"))
	r.add("page_schedules", c.Get(t, "/schedules"))
	F("toggle_off", "/schedules/1/toggle")
	F("toggle_on", "/schedules/1/toggle")
	F("toggle_missing", "/schedules/999/toggle")
	F("toggle_bad_id", "/schedules/abc/toggle")
	F("run_snapshot", "/schedules/1/run")
	F("run_snapshot_retention0", "/schedules/4/run")
	F("run_unknown_type", "/schedules/6/run")
	F("run_scrub_local", "/schedules/7/run")
	F("run_smart", "/schedules/8/run")
	hostdErr(a, "POST /zfs/snapshot", 500, "out of space")
	F("run_snapshot_hostd_err", "/schedules/2/run")
	a.Hostd.Remove("POST /zfs/snapshot")
	hostdErr(a, "POST /zfs/snapshot/prune", 500, "prune failed")
	F("run_snapshot_prune_err", "/schedules/1/run")
	F("run_missing", "/schedules/999/run")
	F("delete_ok", "/schedules/3/delete")
	F("delete_missing", "/schedules/3/delete")
	r.golden("p4/schedules")
}

// TestFileManager: 파일 관리자 페이지·API.
func TestFileManager(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	list := h.CaptureBody(t, "hostd", "/fs/list?path=%2Fzdata&hidden=0&sort=name&order=asc")
	a.Hostd.Set("GET /fs/list", h.Reply{Status: 200, ContentType: "application/json", Body: list})
	a.Hostd.SetJSON("GET /fs/stat?path=%2Fzdata%2Fa.txt", map[string]any{"is_dir": false, "size": 11})
	a.Hostd.Set("GET /fs/read?path=%2Fzdata%2Fa.txt", h.Reply{Status: 200, Body: "hello <b>&\n"})
	a.Hostd.SetJSON("GET /fs/stat?path=%2Fzdata%2Fbig.bin", map[string]any{"is_dir": false, "size": 50 * 1024 * 1024})
	a.Hostd.SetJSON("GET /fs/stat?path=%2Fzdata%2Fbin.dat", map[string]any{"is_dir": false, "size": 4})
	a.Hostd.Set("GET /fs/read?path=%2Fzdata%2Fbin.dat", h.Reply{Status: 200, Body: "\x00\x01\xff\xfe"})
	a.Hostd.SetJSON("GET /fs/stat?path=%2Fzdata%2Fphoto.JPG", map[string]any{"is_dir": false, "size": 3})
	a.Hostd.Set("GET /fs/read?path=%2Fzdata%2Fphoto.JPG", h.Reply{Status: 200, Body: "jpg"})
	a.Hostd.SetJSON("GET /fs/stat?path=%2Fzdata%2Fquote%22name.txt", map[string]any{"is_dir": false, "size": 1})
	a.Hostd.Set("GET /fs/read?path=%2Fzdata%2Fquote%22name.txt", h.Reply{Status: 200, Body: "q"})
	hostdErr(a, "GET /fs/stat?path=%2Fzdata%2Fmissing", 500, "stat: no such file")
	hostdErr(a, "GET /fs/read?path=%2Fzdata%2Funreadable.txt", 500, "permission denied")
	a.Hostd.SetJSON("GET /fs/stat?path=%2Fzdata%2Funreadable.txt", map[string]any{"is_dir": false, "size": 1})
	r := newRunner(t, a)
	v := a.Client(h.Cookie("viewer1", "viewer"))
	o := a.Client(h.Cookie("operator1", "operator"))
	for _, p := range []string{"/files", "/files?path=/zdata", "/files?path=/zdata/../etc/./", "/files?path=zdata", "/files?path=/zdata&hidden=1&sort=size&order=desc", "/files?path=/zdata&sort=bogus&order=x", "/files?path=/zdata&success=ok%3Cx%3E&error=e"} {
		r.add("page "+p, v.Get(t, p))
	}
	for _, p := range []string{"/api/files/list?path=/zdata", "/api/files/list", "/api/files/list?path=rel", "/api/files/list?path=/zdata&hidden=1&sort=mtime&order=desc"} {
		r.add("api "+p, v.Get(t, p))
	}
	for _, p := range []string{"/files/edit?path=/zdata/a.txt", "/files/edit?path=/zdata/apps", "/files/edit?path=/zdata/big.bin", "/files/edit?path=/zdata/bin.dat", "/files/edit?path=/zdata/missing", "/files/edit", "/files/edit?path=/zdata/unreadable.txt"} {
		r.add("edit "+p, v.Get(t, p))
	}
	for _, p := range []string{"/api/files/download?path=/zdata/a.txt", "/api/files/download?path=/zdata/photo.JPG", "/api/files/download?path=/zdata/apps", "/api/files/download?path=relative", "/api/files/download?path=/zdata/quote%22name.txt", "/api/files/download?path=/zdata/unreadable.txt", "/api/files/download?path=/zdata/missing"} {
		r.add("download "+p, v.Get(t, p))
	}
	J := func(name, path string, body any) { r.add(name, o.PostJSON(t, path, body)) }
	J("save_ok", "/api/files/save", map[string]any{"path": "/zdata/a.txt", "content": "new\ncontent 한글"})
	J("save_rel", "/api/files/save", map[string]any{"path": "a.txt", "content": "x"})
	J("save_dir_traversal", "/api/files/save", map[string]any{"path": "/zdata/../etc/rc.conf", "content": "x"})
	J("mkdir_ok", "/api/files/mkdir", map[string]any{"dir": "/zdata", "name": "newdir"})
	J("mkdir_bad_name", "/api/files/mkdir", map[string]any{"dir": "/zdata", "name": "a/b"})
	J("mkdir_dotdot", "/api/files/mkdir", map[string]any{"dir": "/zdata", "name": ".."})
	J("mkdir_rel_dir", "/api/files/mkdir", map[string]any{"dir": "zdata", "name": "x"})
	J("create_ok", "/api/files/create", map[string]any{"dir": "/zdata/", "name": "new.txt"})
	J("create_empty", "/api/files/create", map[string]any{"dir": "/zdata", "name": ""})
	J("delete_ok", "/api/files/delete", map[string]any{"paths": []string{"/zdata/a.txt", "/zdata/b//c/"}})
	J("delete_empty", "/api/files/delete", map[string]any{"paths": []string{}})
	J("delete_root", "/api/files/delete", map[string]any{"paths": []string{"/"}})
	J("delete_rel", "/api/files/delete", map[string]any{"paths": []string{"/ok", "rel"}})
	J("rename_ok", "/api/files/rename", map[string]any{"path": "/zdata/a.txt", "newName": "b.txt"})
	J("rename_snake_key", "/api/files/rename", map[string]any{"path": "/zdata/a.txt", "new_name": "b.txt"})
	J("rename_bad", "/api/files/rename", map[string]any{"path": "/zdata/a.txt", "newName": "../b"})
	J("rename_root", "/api/files/rename", map[string]any{"path": "/", "newName": "x"})
	J("chmod_ok", "/api/files/chmod", map[string]any{"paths": []string{"/zdata/a.txt"}, "mode": "0644"})
	J("chmod_3digit", "/api/files/chmod", map[string]any{"paths": []string{"/zdata/a.txt"}, "mode": "755"})
	J("chmod_bad_mode", "/api/files/chmod", map[string]any{"paths": []string{"/zdata/a.txt"}, "mode": "u+x"})
	J("chmod_9", "/api/files/chmod", map[string]any{"paths": []string{"/zdata/a.txt"}, "mode": "0899"})
	J("chmod_no_paths", "/api/files/chmod", map[string]any{"mode": "644"})
	r.add("clip_copy", o.PostJSON(t, "/api/files/clip", map[string]any{"op": "copy", "paths": []string{"/zdata/a.txt", "/zdata//apps/"}}))
	clip := ""
	for _, sc := range r.steps[len(r.steps)-1].Snap.Headers["Set-Cookie"] {
		clip = strings.SplitN(sc, ";", 2)[0]
	}
	J("clip_bad_op", "/api/files/clip", map[string]any{"op": "move", "paths": []string{"/a"}})
	J("clip_rel", "/api/files/clip", map[string]any{"op": "cut", "paths": []string{"a"}})
	J("paste_no_clip", "/api/files/paste", map[string]any{"destDir": "/zdata/x"})
	oc := a.Client(h.Cookie("operator1", "operator") + "; " + clip)
	r.add("paste_copy", oc.PostJSON(t, "/api/files/paste", map[string]any{"destDir": "/zdata/dest/"}))
	r.add("paste_bad_dest", oc.PostJSON(t, "/api/files/paste", map[string]any{"destDir": "dest"}))
	r.add("clip_cut", o.PostJSON(t, "/api/files/clip", map[string]any{"op": "cut", "paths": []string{"/zdata/m.txt"}}))
	cut := ""
	for _, sc := range r.steps[len(r.steps)-1].Snap.Headers["Set-Cookie"] {
		cut = strings.SplitN(sc, ";", 2)[0]
	}
	occ := a.Client(h.Cookie("operator1", "operator") + "; " + cut)
	r.add("paste_cut", occ.PostJSON(t, "/api/files/paste", map[string]any{"destDir": "/zdata/dest"}))
	r.add("paste_garbage_cookie", a.Client(h.Cookie("operator1", "operator")+"; fm_clip=%%%garbage").PostJSON(t, "/api/files/paste", map[string]any{"destDir": "/zdata"}))
	hostdErr(a, "POST /fs/copy", 500, "copy: no space")
	r.add("paste_copy_hostd_err", oc.PostJSON(t, "/api/files/paste", map[string]any{"destDir": "/zdata/dest"}))
	// 업로드
	r.add("upload_ok", o.PostMultipart(t, "/api/files/upload",
		h.MultipartPart{Name: "dir", Content: "/zdata/up"},
		h.MultipartPart{Name: "files", Filename: "a.txt", Content: "AAA"},
		h.MultipartPart{Name: "files", Filename: "sub/dir/b.bin", Content: "\x00B"},
		h.MultipartPart{Name: "ignored", Content: "x"}))
	r.add("upload_files_before_dir", o.PostMultipart(t, "/api/files/upload", h.MultipartPart{Name: "files", Filename: "a.txt", Content: "A"}, h.MultipartPart{Name: "dir", Content: "/zdata"}))
	r.add("upload_rel_dir", o.PostMultipart(t, "/api/files/upload", h.MultipartPart{Name: "dir", Content: "zdata"}, h.MultipartPart{Name: "files", Filename: "a.txt", Content: "A"}))
	r.add("upload_no_files", o.PostMultipart(t, "/api/files/upload", h.MultipartPart{Name: "dir", Content: "/zdata"}))
	r.add("upload_field_without_filename", o.PostMultipart(t, "/api/files/upload", h.MultipartPart{Name: "dir", Content: "/zdata"}, h.MultipartPart{Name: "files", Content: "A"}))
	big := strings.Repeat("x", 3*1024*1024)
	r.add("upload_3mb", o.PostMultipart(t, "/api/files/upload", h.MultipartPart{Name: "dir", Content: "/zdata"}, h.MultipartPart{Name: "files", Filename: "big.txt", Content: big}))
	hostdErr(a, "POST /fs/write", 500, "write: read-only file system")
	r.add("upload_hostd_err", o.PostMultipart(t, "/api/files/upload", h.MultipartPart{Name: "dir", Content: "/zdata"}, h.MultipartPart{Name: "files", Filename: "a.txt", Content: "A"}))
	J("save_hostd_err", "/api/files/save", map[string]any{"path": "/zdata/a.txt", "content": "x"})
	for _, k := range []string{"/fs/mkdir", "/fs/create", "/fs/delete", "/fs/rename", "/fs/chmod"} {
		hostdErr(a, "POST "+k, 400, "invalid request from hostd")
	}
	J("mkdir_hostd_400", "/api/files/mkdir", map[string]any{"dir": "/zdata", "name": "x"})
	J("create_hostd_400", "/api/files/create", map[string]any{"dir": "/zdata", "name": "x"})
	J("delete_hostd_400", "/api/files/delete", map[string]any{"paths": []string{"/zdata/x"}})
	J("rename_hostd_400", "/api/files/rename", map[string]any{"path": "/zdata/x", "newName": "y"})
	J("chmod_hostd_400", "/api/files/chmod", map[string]any{"paths": []string{"/zdata/x"}, "mode": "644"})
	hostdErr(a, "GET /fs/list", 500, "opendir: permission denied")
	r.add("page_list_err", v.Get(t, "/files?path=/root"))
	r.add("api_list_err", v.Get(t, "/api/files/list?path=/root"))
	r.golden("p4/filemanager")
}

func smbStatus() map[string]any {
	return map[string]any{"running": true, "setup_ok": true, "shares": []map[string]any{
		{"name": "media", "path": "/zdata/media", "comment": "Media <files> & stuff", "valid_users": "@staff mohicann", "read_only": false, "browseable": true, "guest_ok": false},
		{"name": "public", "path": "/zdata/public", "comment": "", "valid_users": "", "read_only": true, "browseable": false, "guest_ok": true},
	}}
}

func nfsStatus() map[string]any {
	return map[string]any{"running": true, "exports": []map[string]any{
		{"name": "media", "path": "/zdata/media", "line": "/zdata/media -ro -maproot=root 192.168.0.0/24", "clients": "192.168.0.0/24"},
		{"name": "backup", "path": "/zbak", "line": "/zbak -alldirs", "clients": ""},
	}}
}

// TestShares: 로컬 사용자·그룹, SMB·NFS 공유.
func TestShares(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	r := newRunner(t, a)
	v := a.Client(h.Cookie("viewer1", "viewer"))
	o := a.Client(h.Cookie("operator1", "operator"))
	r.add("localusers_page", v.Get(t, "/localusers"))
	r.add("localusers_page_msgs", v.Get(t, "/localusers?success=%EC%99%84%EB%A3%8C&error=%3Cx%3E"))
	for _, tab := range []string{"", "?tab=users", "?tab=smb", "?tab=nfs", "?tab=bogus&success=s&error=e"} {
		r.add("shares_empty "+tab, v.Get(t, "/shares"+tab))
	}
	a.Hostd.SetJSON("GET /smb/status", smbStatus())
	a.Hostd.SetJSON("GET /nfs/status", nfsStatus())
	for _, tab := range []string{"", "?tab=smb", "?tab=nfs"} {
		r.add("shares_full "+tab, v.Get(t, "/shares"+tab))
	}
	a.Hostd.SetJSON("GET /smb/status", map[string]any{"running": false, "setup_ok": false})
	r.add("shares_smb_not_setup", v.Get(t, "/shares?tab=smb"))
	F := func(name, path string, kv ...string) { r.add(name, o.PostForm(t, path, kv...)) }
	F("user_create_ok", "/localusers", "username", "bob", "fullname", "Bob <B>", "shell", "/bin/sh", "password", "pw1", "smb_password", "smbpw")
	F("user_create_minimal", "/localusers", "username", "carol")
	F("user_create_bad_name", "/localusers", "username", "9bad name")
	F("user_create_bad_shell", "/localusers", "username", "dave", "shell", "/bin/zsh")
	F("user_create_empty", "/localusers", "username", "")
	F("user_delete", "/localusers/bob/delete")
	F("user_delete_root", "/localusers/root/delete")
	F("user_delete_encoded", "/localusers/b%20o%2Fb/delete")
	F("user_smbpasswd", "/localusers/bob/smbpasswd", "password", "newsmb")
	F("user_smbpasswd_empty", "/localusers/bob/smbpasswd", "password", "")
	F("group_create", "/localgroups", "name", "staff")
	F("group_create_empty", "/localgroups", "name", " ")
	F("group_delete", "/localgroups/staff/delete")
	F("group_member_add", "/localgroups/staff/member", "username", "bob", "action", "add")
	F("group_member_remove", "/localgroups/staff/member", "username", "bob", "action", "remove")
	F("group_member_bad_action", "/localgroups/staff/member", "username", "bob", "action", "toggle")
	F("smb_create_ok", "/shares", "name", "media", "path", "/zdata/media", "comment", "c", "valid_users", "@staff", "read_only", "on", "no_browse", "on", "guest_ok", "on")
	F("smb_create_minimal", "/shares", "name", "m2", "path", "/zdata/m2")
	F("smb_create_bad_name", "/shares", "name", "bad name", "path", "/zdata/x")
	F("smb_create_rel_path", "/shares", "name", "x", "path", "zdata/x")
	F("smb_create_empty", "/shares")
	F("smb_delete", "/shares/media/delete")
	F("smb_reload", "/shares/reload")
	F("smb_setup", "/shares/setup")
	F("nfs_create_ok", "/nfs/export", "path", "/zdata/media", "name", "media", "clients", "192.168.0.0/24 10.0.0.0/8", "read_only", "on", "maproot", "root")
	F("nfs_create_minimal", "/nfs/export", "path", "/zdata/m", "name", "m")
	F("nfs_create_bad_name", "/nfs/export", "path", "/zdata/m", "name", "../x")
	F("nfs_create_rel", "/nfs/export", "path", "zdata", "name", "m")
	F("nfs_delete", "/nfs/export/media/delete")
	F("nfs_reload", "/nfs/reload")
	for _, k := range []string{"/localusers/create", "/localusers/delete", "/localusers/smbpasswd", "/localgroups/create", "/localgroups/delete", "/localgroups/member", "/smb/share", "/smb/share/delete", "/smb/reload", "/smb/setup", "/nfs/export", "/nfs/export/delete", "/nfs/reload"} {
		hostdErr(a, "POST "+k, 500, "pw: user already exists")
	}
	F("user_create_hostd_err", "/localusers", "username", "bob")
	F("user_delete_hostd_err", "/localusers/bob/delete")
	F("user_smbpasswd_hostd_err", "/localusers/bob/smbpasswd", "password", "x")
	F("group_create_hostd_err", "/localgroups", "name", "staff")
	F("group_delete_hostd_err", "/localgroups/staff/delete")
	F("group_member_hostd_err", "/localgroups/staff/member", "username", "bob", "action", "add")
	F("smb_create_hostd_err", "/shares", "name", "media", "path", "/zdata/media")
	F("smb_delete_hostd_err", "/shares/media/delete")
	F("smb_reload_hostd_err", "/shares/reload")
	F("smb_setup_hostd_err", "/shares/setup")
	F("nfs_create_hostd_err", "/nfs/export", "path", "/zdata/m", "name", "m")
	F("nfs_delete_hostd_err", "/nfs/export/m/delete")
	F("nfs_reload_hostd_err", "/nfs/reload")
	hostdErr(a, "GET /localusers/status", 500, "boom")
	hostdErr(a, "GET /smb/status", 500, "boom smb")
	hostdErr(a, "GET /nfs/status", 500, "boom nfs")
	r.add("localusers_page_err", v.Get(t, "/localusers"))
	r.add("shares_page_err", v.Get(t, "/shares?tab=nfs"))
	a.Hostd.Set("GET /localusers/status", h.Reply{Status: 200, ContentType: "application/json", Body: `{"users":null}`})
	r.add("localusers_null", v.Get(t, "/localusers"))
	r.golden("p4/shares")
}

// TestServicesNetworkAppJail: 서비스·네트워크·AppJail 페이지와 jail 제어.
func TestServicesNetworkAppJail(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	r := newRunner(t, a)
	v := a.Client(h.Cookie("viewer1", "viewer"))
	o := a.Client(h.Cookie("operator1", "operator"))
	r.add("services", v.Get(t, "/services"))
	r.add("network", v.Get(t, "/network"))
	r.add("appjail", v.Get(t, "/appjail"))
	r.add("appjail_msgs", v.Get(t, "/appjail?success=%EC%8B%9C%EC%9E%91&error=%3Cb%3E"))
	r.add("appjail_detail", v.Get(t, "/appjail/audio"))
	r.add("appjail_detail_msgs", v.Get(t, "/appjail/audio?success=ok&error=no"))
	a.Hostd.Set("GET /appjail/detail?name=nope", h.Reply{Status: 400, Body: h.CaptureBody(t, "hostd", "/appjail/detail?name=nope")})
	r.add("appjail_detail_unknown", v.Get(t, "/appjail/nope"))
	r.add("appjail_detail_weird", v.Get(t, "/appjail/a%20b"))
	F := func(name, path string, kv ...string) { r.add(name, o.PostForm(t, path, kv...)) }
	F("start", "/appjail/audio/start")
	F("stop_back_detail", "/appjail/audio/stop", "back", "detail")
	F("restart_back_other", "/appjail/cloudreve/restart", "back", "https://evil.example")
	F("start_weird_name", "/appjail/a%20b/start")
	for _, k := range []string{"start", "stop", "restart"} {
		hostdErr(a, "POST /appjail/"+k, 500, "appjail: jail is locked")
	}
	F("start_hostd_err", "/appjail/audio/start")
	F("stop_hostd_err_detail", "/appjail/audio/stop", "back", "detail")
	F("restart_hostd_err", "/appjail/audio/restart")
	hostdErr(a, "GET /appjail/list", 500, "list failed")
	r.add("appjail_list_err", v.Get(t, "/appjail"))
	hostdErr(a, "GET /appjail/detail", 500, "detail failed")
	r.add("appjail_detail_err", v.Get(t, "/appjail/audio"))
	a.Hostd.SetJSON("GET /appjail/list", map[string]any{"jails": []any{}, "available": false, "error": "appjail not installed"})
	r.add("appjail_unavailable", v.Get(t, "/appjail"))
	hostdErr(a, "GET /jls", 500, "jls failed")
	r.add("services_jls_err", v.Get(t, "/services"))
	hostdErr(a, "GET /sockstat", 500, "sockstat failed")
	r.add("services_sockstat_err", v.Get(t, "/services"))
	hostdErr(a, "GET /network/status", 500, "ifconfig failed")
	r.add("network_err", v.Get(t, "/network"))
	r.golden("p4/services_network_appjail")
}

// TestDiagnostics: 진단 페이지와 로그·명령 API.
func TestDiagnostics(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	logDir := filepath.Join(a.Root, "var", "log")
	os.MkdirAll(logDir, 0o755)
	var lines []string
	for i := 1; i <= 250; i++ {
		lines = append(lines, "line "+strings.Repeat("x", i%7)+" #"+itoa(i))
	}
	os.WriteFile(filepath.Join(logDir, "pfortainer.log"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	a.Hostd.Set("GET /diag/log?file=messages&lines=200", h.Reply{Status: 200, Body: "Nov 19 host kernel: hi <b>\n"})
	a.Hostd.Set("GET /diag/log?file=auth&lines=5", h.Reply{Status: 200, Body: "auth line\n"})
	r := newRunner(t, a)
	v := a.Client(h.Cookie("viewer1", "viewer"))
	r.add("page", v.Get(t, "/diagnostics"))
	for _, p := range []string{
		"/api/diag/local-log?file=pfortainer", "/api/diag/local-log?file=pfortainer&lines=3", "/api/diag/local-log?file=pfortainer&lines=0",
		"/api/diag/local-log?file=pfortainer&lines=-5", "/api/diag/local-log?file=pfortainer&lines=abc", "/api/diag/local-log?file=pfortainer&lines=9999",
		"/api/diag/local-log?file=other", "/api/diag/local-log",
		"/api/diag/host-log?file=messages", "/api/diag/host-log?file=auth&lines=5", "/api/diag/host-log?file=dmesg&lines=50", "/api/diag/host-log?file=bogus",
		"/api/diag/host-log?file=messages&lines=1%26file%3Dauth",
		"/api/diag/cmd?cmd=df", "/api/diag/cmd?cmd=mount", "/api/diag/cmd?cmd=bogus", "/api/diag/cmd", "/api/diag/cmd?cmd=df%26x%3D1",
	} {
		r.add(p, v.Get(t, p))
	}
	os.Remove(filepath.Join(logDir, "pfortainer.log"))
	r.add("local_log_missing", v.Get(t, "/api/diag/local-log?file=pfortainer"))
	r.golden("p4/diagnostics")
}

func itoa(i int) string { return strings.TrimSpace(strings.Replace(string(rune('0'+i/100))+string(rune('0'+i/10%10))+string(rune('0'+i%10)), "\x00", "", -1)) }

// nearNowTimes: 페이지의 "MM-DD HH:MM"(KST) 중 지금·지금+1일 근처 값을 치환한다
// (마지막 실행 시각, 알 수 없는 주기의 다음 실행 시각).
func nearNowTimes(s string) string {
	kst := time.FixedZone("KST", 9*3600)
	now := time.Now()
	for _, off := range []struct {
		base time.Duration
		tag  string
	}{{0, "<now>"}, {24 * time.Hour, "<now+1d>"}} {
		for d := -2; d <= 2; d++ {
			ts := now.Add(off.base + time.Duration(d)*time.Minute).In(kst).Format("01-02 15:04")
			s = strings.ReplaceAll(s, ts, off.tag)
		}
	}
	return s
}
