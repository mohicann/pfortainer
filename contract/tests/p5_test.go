package tests

import (
	"regexp"
	"database/sql"
	"encoding/json"
	"fmt"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	h "github.com/mohicann/pfortainer/contract/harness"
)

// ── 메트릭 ───────────────────────────────────────────────────────────────────

const metricsSchema = `CREATE TABLE IF NOT EXISTS metrics (
    time INTEGER PRIMARY KEY NOT NULL, cpu_user REAL NOT NULL DEFAULT 0, cpu_nice REAL NOT NULL DEFAULT 0,
    cpu_sys REAL NOT NULL DEFAULT 0, cpu_intr REAL NOT NULL DEFAULT 0, cpu_temp REAL NOT NULL DEFAULT 0,
    mem_free REAL NOT NULL DEFAULT 0, mem_active REAL NOT NULL DEFAULT 0, mem_inactive REAL NOT NULL DEFAULT 0,
    mem_wired REAL NOT NULL DEFAULT 0, load1 REAL NOT NULL DEFAULT 0, load5 REAL NOT NULL DEFAULT 0,
    load15 REAL NOT NULL DEFAULT 0, net_igc0_rx REAL NOT NULL DEFAULT 0, net_igc0_tx REAL NOT NULL DEFAULT 0,
    net_ts_rx REAL NOT NULL DEFAULT 0, net_ts_tx REAL NOT NULL DEFAULT 0, disk_read REAL NOT NULL DEFAULT 0,
    disk_write REAL NOT NULL DEFAULT 0, arc_size REAL NOT NULL DEFAULT 0, arc_hit REAL NOT NULL DEFAULT 0,
    arc_miss REAL NOT NULL DEFAULT 0, tcp_estab REAL NOT NULL DEFAULT 0, pool_zdata_pct REAL NOT NULL DEFAULT 0,
    pool_zdata_used REAL NOT NULL DEFAULT 0, pool_zdata_total REAL NOT NULL DEFAULT 0, pool_zboot_pct REAL NOT NULL DEFAULT 0,
    pool_zboot_used REAL NOT NULL DEFAULT 0, pool_zboot_total REAL NOT NULL DEFAULT 0)`

// seedMetrics: times의 각 시각에 j번째 행(값은 j에서 결정적으로 계산)을 넣는다.
func seedMetrics(t *testing.T, path string, times []int64) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(metricsSchema); err != nil {
		t.Fatal(err)
	}
	for j, ts := range times {
		args := []any{ts}
		for c := 1; c <= 28; c++ {
			args = append(args, float64(j*100+c)+0.25*float64(c%4))
		}
		ph := strings.TrimSuffix(strings.Repeat("?,", 29), ",")
		if _, err := db.Exec(`INSERT INTO metrics VALUES (`+ph+`)`, args...); err != nil {
			t.Fatal(err)
		}
	}
}

// relTimes: data[i][0](시각)을 기준 시각 대비 상대값으로 바꾼다.
func relTimes(t *testing.T, body []byte, base int64) any {
	var v struct {
		Labels []string    `json:"labels"`
		Data   [][]float64 `json:"data"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return string(body)
	}
	rows := make([][]any, len(v.Data))
	for i, r := range v.Data {
		row := make([]any, len(r))
		for k, x := range r {
			row[k] = x
		}
		if len(r) > 0 {
			row[0] = fmt.Sprintf("base%+d", int64(r[0])-base)
		}
		rows[i] = row
	}
	return map[string]any{"labels": v.Labels, "data": rows, "raw_prefix": string(body[:min(len(body), 40)])}
}

var charts = []string{"system.cpu", "system.cputemp", "system.ram", "system.load", "net.igc0", "net.tailscale0", "system.io", "zfs.arc_size", "zfs.hits", "ipv4.tcpsock", "zfspool.zdata", "zfspool.zboot"}

// TestMetrics: 메트릭 페이지·데이터 API(ring 경로와 DB 버킷 경로).
func TestMetrics(t *testing.T) {
	now := time.Now().Unix()
	ringBase := now
	a := h.StartAppWithCaptures(t, h.AppOpts{BeforeStart: func(a *h.App) {
		seedMetrics(t, a.MetricsDB, []int64{ringBase - 2000, ringBase - 1210, ringBase - 610, ringBase - 310, ringBase - 10})
	}})
	r := newRunner(t, a)
	v := a.Client(h.Cookie("viewer1", "viewer"))
	r.add("page", v.Get(t, "/metrics"))
	out := map[string]any{}
	for _, ch := range charts {
		resp := v.Get(t, "/api/metrics/data?chart="+ch)
		out["ring "+ch] = map[string]any{"status": resp.Status, "ct": resp.Header.Get("Content-Type"), "body": relTimes(t, resp.Body, ringBase)}
	}
	for _, q := range []string{"chart=system.cpu&after=-700", "chart=system.cpu&after=-1800&points=2", "chart=system.cpu&points=0", "chart=system.cpu&points=-3", "chart=system.cpu&after=0",
		"chart=bogus", "", "chart=system.cpu&after=abc", "chart=system.cpu&after=-1800.5", "chart=system.cpu&after=-1800&after=-60"} {
		resp := v.Get(t, "/api/metrics/data?"+q)
		out["q "+q] = map[string]any{"status": resp.Status, "ct": resp.Header.Get("Content-Type"), "body": relTimes(t, resp.Body, ringBase)}
	}
	h.Golden(t, "p5/metrics_ring", map[string]any{"steps": r.steps, "api": out})

	// DB 경로(after < -1800): 버킷이 절대 시각 배수라 기준 시각을 43200 배수에 맞춘다
	anchor := (now / 43200) * 43200
	var ts []int64
	for j := 0; j <= 100; j++ {
		ts = append(ts, anchor-int64(j)*300)
	}
	b := h.StartAppWithCaptures(t, h.AppOpts{BeforeStart: func(b *h.App) { seedMetrics(t, b.MetricsDB, ts) }})
	vb := b.Client(h.Cookie("viewer1", "viewer"))
	outb := map[string]any{}
	for _, ch := range charts {
		resp := vb.Get(t, "/api/metrics/data?chart="+ch+"&after=-86400&points=24")
		outb[ch] = map[string]any{"status": resp.Status, "body": relTimes(t, resp.Body, anchor)}
	}
	for _, q := range []string{"after=-86400&points=0", "after=-86400&points=1000", "after=-3600&points=7", "after=-1801&points=3"} {
		resp := vb.Get(t, "/api/metrics/data?chart=system.load&"+q)
		outb["load "+q] = map[string]any{"status": resp.Status, "body": relTimes(t, resp.Body, anchor)}
	}
	h.Golden(t, "p5/metrics_db", outb)
}

// ── 데스크탑 ─────────────────────────────────────────────────────────────────

func TestDesktop(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	a.StdUsers(t)
	r := newRunner(t, a)
	v := a.Client(h.Cookie("viewer1", "viewer"))
	o := a.Client(h.Cookie("operator1", "operator"))
	prefs := func() any { return h.Rows(t, a.DB(t), `SELECT username, prefs FROM desktop_prefs ORDER BY username`) }
	r.add("page_default", v.Get(t, "/desktop"))
	r.add("get_default", v.Get(t, "/api/desktop/prefs"))
	r.addDB("put_ok", v.PutJSON(t, "/api/desktop/prefs", `{"icons":{"files":[10,20]},"z":"</script><b>","n":1.50,"u":"é"}`), prefs())
	r.add("get_after_put", v.Get(t, "/api/desktop/prefs"))
	r.add("page_with_prefs", v.Get(t, "/desktop"))
	r.add("other_user_isolated", o.Get(t, "/api/desktop/prefs"))
	r.addDB("put_array", v.PutJSON(t, "/api/desktop/prefs", `[1,2]`), prefs())
	r.addDB("put_string", v.PutJSON(t, "/api/desktop/prefs", `"x"`), prefs())
	r.addDB("put_invalid", v.PutJSON(t, "/api/desktop/prefs", `{"a":`), prefs())
	r.addDB("put_empty", v.PutJSON(t, "/api/desktop/prefs", ``), prefs())
	big := `{"k":"` + strings.Repeat("x", 64*1024) + `"}`
	r.addDB("put_too_large", v.PutJSON(t, "/api/desktop/prefs", big), prefs())
	justUnder := `{"k":"` + strings.Repeat("y", 64*1024-8) + `"}`
	r.addDB("put_at_limit", v.PutJSON(t, "/api/desktop/prefs", justUnder), h.Rows(t, a.DB(t), `SELECT username, length(prefs) AS len FROM desktop_prefs ORDER BY username`))
	r.addDB("put_replace", v.PutJSON(t, "/api/desktop/prefs", `{"b":2,  "a":1}`), prefs())
	r.addDB("put_ghost_user", a.Client(h.Cookie("ghost", "viewer")).PutJSON(t, "/api/desktop/prefs", `{"g":1}`), prefs())
	r.add("put_form_ct", v.Do(t, h.Req{Method: "PUT", Path: "/api/desktop/prefs", Body: strings.NewReader(`{"a":1}`), CT: "text/plain"}))
	a.Exec(t, `UPDATE desktop_prefs SET prefs='not json <x>' WHERE username='operator1'`)
	a.Exec(t, `INSERT OR REPLACE INTO desktop_prefs VALUES ('operator1','not json <x>',1)`)
	r.add("get_corrupt", o.Get(t, "/api/desktop/prefs"))
	r.add("page_corrupt", o.Get(t, "/desktop"))
	r.golden("p5/desktop")
}

// ── 알림 ─────────────────────────────────────────────────────────────────────

func alertMeta(t *testing.T, a *h.App, m *h.Masker) any {
	rows := h.Rows(t, a.DB(t), `SELECT value FROM meta WHERE key='alert_settings'`)
	if len(rows) == 0 {
		return nil
	}
	raw := m.Apply(rows[0]["value"].(string))
	var v any
	json.Unmarshal([]byte(raw), &v)
	return map[string]any{"raw": raw, "parsed": v}
}

func closedPort(t *testing.T) string {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	p := fmt.Sprint(l.Addr().(*net.TCPAddr).Port)
	l.Close()
	return p
}

func maskStub(reqs []h.StubReq, stubURL string) []h.StubReq {
	for i := range reqs {
		b, _ := json.Marshal(reqs[i].Body)
		s := strings.ReplaceAll(string(b), stubURL, "<STUB>")
		s = tsRe.ReplaceAllString(s, `"<rfc3339>"`)
		var v any
		json.Unmarshal([]byte(s), &v)
		reqs[i].Body = v
		reqs[i].URI = strings.ReplaceAll(reqs[i].URI, stubURL, "<STUB>")
		if strings.HasPrefix(reqs[i].Host, "127.0.0.1:") {
			reqs[i].Host = "<STUB>"
		}
	}
	sort.Slice(reqs, func(i, j int) bool {
		a, _ := json.Marshal(reqs[i])
		b, _ := json.Marshal(reqs[j])
		return string(a) < string(b)
	})
	return reqs
}

var tsRe = regexpMust(`"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})"`)

func TestAlerts(t *testing.T) {
	stub := h.NewStub(t)
	// Discord URL(http)은 HTTP_PROXY로 스텁에 보낸다
	a := h.StartAppWithCaptures(t, h.AppOpts{Env: map[string]string{"HTTP_PROXY": stub.URL, "http_proxy": stub.URL, "NO_PROXY": "", "no_proxy": ""}})
	r := newRunner(t, a)
	r.m.Lit(stub.URL, "<STUB>")
	smtpPort := closedPort(t)
	r.m.Lit(smtpPort, "<SMTP_PORT>")
	r.m.Re(`이메일(%3A|:)[^"&]*?(%20%2F%20|( / )|$|")`, "이메일: <smtp error>$2")
	o := a.Client(h.Cookie("operator1", "operator"))
	v := a.Client(h.Cookie("viewer1", "viewer"))
	step := func(name string, resp *h.Resp) {
		r.steps = append(r.steps, struct2step(name, a.Capture(resp, r.m), map[string]any{"meta": alertMeta(t, a, r.m), "stub": maskStub(stub.Take(), stub.URL)}))
	}
	step("page_default", v.Get(t, "/alerts"))
	step("test_no_channels", o.PostForm(t, "/alerts/test"))
	step("save_webhook", o.PostForm(t, "/alerts", "webhook_enabled", "1", "webhook_url", " "+stub.URL+"/hook ", "check_pool_health", "1", "check_smart", "1", "capacity_pct", "90", "cooldown_hours", "2", "smtp_port", "x"))
	step("page_saved", v.Get(t, "/alerts?success=%EC%A0%80%EC%9E%A5"))
	step("test_webhook_ok", o.PostForm(t, "/alerts/test"))
	stub.Respond(500, strings.Repeat("e", 300))
	step("test_webhook_500", o.PostForm(t, "/alerts/test"))
	stub.Respond(204, "")
	step("save_discord", o.PostForm(t, "/alerts", "webhook_enabled", "1", "webhook_url", "http://discord.com/api/webhooks/123/abc", "check_capacity", "1", "check_scrub", "1"))
	step("test_discord", o.PostForm(t, "/alerts/test"))
	stub.Respond(200, "ok")
	step("save_email_and_webhook", o.PostForm(t, "/alerts", "email_enabled", "1", "smtp_host", " 127.0.0.1 ", "smtp_port", smtpPort, "smtp_user", "u", "smtp_pass", "secret-pw", "smtp_from", "pf@example.com", "smtp_to", "me@example.com", "webhook_enabled", "1", "webhook_url", stub.URL+"/h2"))
	step("save_keeps_password", o.PostForm(t, "/alerts", "email_enabled", "1", "smtp_host", "127.0.0.1", "smtp_port", smtpPort, "smtp_user", "u", "smtp_pass", "", "smtp_to", "me@example.com"))
	step("page_with_email", v.Get(t, "/alerts"))
	step("test_email_fail", o.PostForm(t, "/alerts/test"))
	step("save_bad_email_addr", o.PostForm(t, "/alerts", "email_enabled", "1", "smtp_host", "127.0.0.1", "smtp_port", smtpPort, "smtp_to", "not an address"))
	step("test_bad_addr", o.PostForm(t, "/alerts/test"))
	step("save_email_no_host", o.PostForm(t, "/alerts", "email_enabled", "1", "smtp_to", "me@example.com"))
	step("test_email_not_configured", o.PostForm(t, "/alerts/test"))
	step("save_webhook_enabled_no_url", o.PostForm(t, "/alerts", "webhook_enabled", "1"))
	step("test_webhook_no_url", o.PostForm(t, "/alerts/test"))
	step("save_numbers", o.PostForm(t, "/alerts", "smtp_port", "465", "capacity_pct", "-5", "cooldown_hours", "99999999999"))
	a.Exec(t, `UPDATE meta SET value='{broken' WHERE key='alert_settings'`)
	step("page_corrupt_meta", v.Get(t, "/alerts"))
	r.golden("p5/alerts")
}

func struct2step(name string, s h.Snap, db any) step { return step{Name: name, Snap: s, DB: db} }

// TestAlertCheckBackground: 5분 알림 점검(주기 단축) — 풀 상태·스크럽·SMART·용량, 쿨다운.
func TestAlertCheckBackground(t *testing.T) {
	stub := h.NewStub(t)
	settings := map[string]any{"email_enabled": false, "smtp_host": "", "smtp_port": 587, "smtp_user": "", "smtp_pass": "", "smtp_from": "", "smtp_to": "",
		"webhook_enabled": true, "webhook_url": stub.URL + "/alert", "check_pool_health": true, "check_smart": true, "check_capacity": true, "check_scrub": true,
		"capacity_pct": 10, "cooldown_hours": 4}
	sb, _ := json.Marshal(settings)
	a := h.StartApp(t, h.AppOpts{Scheduler: true, Env: map[string]string{"TEST_SCHED_INTERVALS": "3600,1,3600"},
		BeforeStart: func(a *h.App) {
			db, _ := sql.Open("sqlite", a.ConfigDB)
			defer db.Close()
			db.Exec(`CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL, updated_at INTEGER NOT NULL)`)
			db.Exec(`INSERT INTO meta VALUES ('alert_settings', ?, 1)`, string(sb))
			// 쿨다운 안: zroot 용량 경보는 1시간 전에 이미 보냄
			db.Exec(`CREATE TABLE alert_history (id INTEGER PRIMARY KEY, event_type TEXT NOT NULL, target TEXT NOT NULL, fired_at INTEGER NOT NULL)`)
			db.Exec(`INSERT INTO alert_history (event_type, target, fired_at) VALUES ('pool_capacity','zbak',?)`, time.Now().Unix()-3600)
			db.Exec(`INSERT INTO alert_history (event_type, target, fired_at) VALUES ('pool_capacity','zdata',?)`, time.Now().Unix()-5*3600)
		}})
	a.LoadCaptures(t)
	a.DumpLogsOnFail()
	zs := h.CaptureBody(t, "hostd", "/zpool-status")
	zs = strings.Replace(zs, " state: ONLINE", " state: DEGRADED\nstatus: One or more devices has been removed.\naction: Online the device.", 1)
	zs = strings.Replace(zs, "scrub repaired 0B in 01:42:40 with 0 errors", "scrub repaired 12K in 01:42:40 with 3 errors", 1)
	a.Hostd.Set("GET /zpool-status", h.Reply{Status: 200, Body: zs})
	for _, d := range []string{"ada0", "ada1", "ada3", "ada4", "nda0", "nvme0"} {
		body := h.CaptureBody(t, "hostd", "/smart?dev="+d)
		if d == "ada1" {
			body = strings.Replace(body, "result: PASSED", "result: FAILED!", 1)
		}
		a.Hostd.Set("GET /smart?dev="+d, h.Reply{Status: 200, Body: body})
	}
	a.Hostd.Set("GET /smart?dev=ada2", h.Reply{Status: 500, Body: "smartctl: open device failed"})
	time.Sleep(2500 * time.Millisecond)
	first := maskStub(stub.Take(), stub.URL)
	hist1 := h.Rows(t, a.DB(t), `SELECT event_type, target FROM alert_history ORDER BY event_type, target`)
	time.Sleep(1500 * time.Millisecond)
	second := maskStub(stub.Take(), stub.URL)
	hostd := a.Hostd.Take()
	paths := map[string]bool{}
	for _, rec := range hostd {
		paths[rec.Method+" "+rec.Path] = true
	}
	var ps []string
	for p := range paths {
		ps = append(ps, p)
	}
	sort.Strings(ps)
	h.Golden(t, "p5/alert_background", map[string]any{"first_round_webhooks": first, "history_after_first": hist1, "later_rounds_webhooks": second, "hostd_paths": ps})
}

// TestSchedulerBackground: 스케줄 틱(시작 시 즉시 + 주기)·복제 틱.
func TestSchedulerBackground(t *testing.T) {
	now := time.Now().Unix()
	a := h.StartApp(t, h.AppOpts{Scheduler: true, Env: map[string]string{"TEST_SCHED_INTERVALS": "1,3600,1"},
		BeforeStart: func(a *h.App) {
			db, _ := sql.Open("sqlite", a.ConfigDB)
			defer db.Close()
			for _, q := range []string{
				`CREATE TABLE schedules (id INTEGER PRIMARY KEY, type TEXT NOT NULL, target TEXT NOT NULL, frequency TEXT NOT NULL, retention INTEGER NOT NULL DEFAULT 7, prefix TEXT NOT NULL DEFAULT 'auto', enabled INTEGER NOT NULL DEFAULT 1, last_run INTEGER, next_run INTEGER, created_at INTEGER NOT NULL)`,
				fmt.Sprintf(`INSERT INTO schedules VALUES (1,'snapshot','zdata/apps','daily',5,'auto',1,NULL,%d,1)`, now-60),
				`INSERT INTO schedules VALUES (2,'snapshot','zdata/off','daily',5,'auto',0,NULL,0,1)`,
				`INSERT INTO schedules VALUES (3,'snapshot','zdata/later','daily',5,'auto',1,NULL,4102444800,1)`,
				`INSERT INTO schedules VALUES (4,'smart_test','ada0','weekly',0,'auto',1,NULL,NULL,1)`,
				`CREATE TABLE replication_tasks (id INTEGER PRIMARY KEY AUTOINCREMENT, name TEXT NOT NULL UNIQUE, source_dataset TEXT NOT NULL, target_path TEXT NOT NULL, recursive INTEGER NOT NULL DEFAULT 0, schedule TEXT NOT NULL DEFAULT 'manual', last_snapshot TEXT NOT NULL DEFAULT '', last_run_at INTEGER, last_status TEXT NOT NULL DEFAULT '', last_error TEXT NOT NULL DEFAULT '', enabled INTEGER NOT NULL DEFAULT 1, created_at INTEGER NOT NULL, retention INTEGER NOT NULL DEFAULT 14)`,
				`INSERT INTO replication_tasks (name, source_dataset, target_path, recursive, schedule, last_snapshot, created_at, retention) VALUES ('daily-never','zdata/apps','zbak/apps',1,'daily','zdata/apps@auto-old',1,3)`,
				`INSERT INTO replication_tasks (name, source_dataset, target_path, schedule, created_at) VALUES ('manual','zdata/m','zbak/m','manual',1)`,
				`INSERT INTO replication_tasks (name, source_dataset, target_path, schedule, enabled, created_at) VALUES ('off','zdata/o','zbak/o','daily',0,1)`,
				fmt.Sprintf(`INSERT INTO replication_tasks (name, source_dataset, target_path, schedule, last_run_at, created_at, retention) VALUES ('recent','zdata/r','zbak/r','daily',%d,1,0)`, now-60),
				`INSERT INTO replication_tasks (name, source_dataset, target_path, schedule, created_at, retention) VALUES ('fails','zdata/f','zbak/f','hourly',1,0)`,
			} {
				if _, err := db.Exec(q); err != nil {
					t.Fatal(err)
				}
			}
		}})
	a.DumpLogsOnFail()
	a.Hostd.Set("POST /replication/run", h.Reply{Status: 200, ContentType: "application/json", Body: `{"current_snapshot":"zdata/apps@auto-new","output":"sent"}`})
	// "fails" 태스크만 실패시키려면 바디로 구분해야 하므로 hostd가 소스별로 응답하게 한다
	time.Sleep(2500 * time.Millisecond)
	recs := a.Hostd.Take()
	m := a.Masker().Re(`auto-\d{8}-\d{6}`, "auto-<ts>")
	// 복제는 같은 응답이라 last_snapshot은 모두 auto-new
	type cnt struct {
		Rec   h.Recorded `json:"req"`
		Count int        `json:"count_at_least_1"`
	}
	seen := map[string]h.Recorded{}
	for _, rec := range h.MaskRecords(recs, m) {
		b, _ := json.Marshal(rec)
		seen[string(b)] = rec
	}
	var uniq []h.Recorded
	for _, rec := range seen {
		uniq = append(uniq, rec)
	}
	uniq = h.Sorted(uniq)
	sched := schedRows(t, a)
	repl := h.MaskNow(h.Rows(t, a.DB(t), `SELECT id, name, schedule, last_snapshot, last_run_at, last_status, last_error, enabled FROM replication_tasks ORDER BY id`), "last_run_at")
	// 스케줄 1은 한 번만 실행돼야 한다(next_run이 내일로 밀림)
	n1 := 0
	for _, rec := range recs {
		if rec.Path == "/zfs/snapshot" {
			n1++
		}
	}
	h.Golden(t, "p5/scheduler_background", map[string]any{"hostd_unique": uniq, "zfs_snapshot_calls": n1, "schedules": sched, "replication_tasks": repl})
}

// TestReplications: 복제 태스크 페이지·CRUD·즉시 실행.
func TestReplications(t *testing.T) {
	a := h.StartAppWithCaptures(t, h.AppOpts{})
	r := newRunner(t, a)
	r.m.Func(nearNowTimes)
	o := a.Client(h.Cookie("operator1", "operator"))
	rows := func() any {
		return h.MaskNow(h.Rows(t, a.DB(t), `SELECT id, name, source_dataset, target_path, recursive, schedule, retention, last_snapshot, last_run_at, last_status, last_error, enabled, created_at FROM replication_tasks ORDER BY id`), "last_run_at", "created_at")
	}
	F := func(name, path string, kv ...string) { r.addDB(name, o.PostForm(t, path, kv...), rows()) }
	r.add("page_empty", o.Get(t, "/replications"))
	F("create_ok", "/replications", "name", "apps", "source_dataset", "zdata/apps", "target_path", "zbak/apps", "recursive", "1", "schedule", "daily", "retention", "7")
	F("create_defaults", "/replications", "name", "m", "source_dataset", "zdata/m", "target_path", "zbak/m")
	F("create_dup", "/replications", "name", "apps", "source_dataset", "zdata/x", "target_path", "zbak/x")
	F("create_missing", "/replications", "name", "x")
	F("create_bad_retention", "/replications", "name", "r", "source_dataset", "a", "target_path", "b", "retention", "x", "recursive", "on")
	r.add("page_list", o.Get(t, "/replications?success=%3Cok%3E&error=err"))
	a.Hostd.Set("POST /replication/run", h.Reply{Status: 200, ContentType: "application/json", Body: `{"current_snapshot":"zdata/apps@auto-20261119-000000","output":"ok"}`})
	F("run_ok_with_prune", "/replications/1/run")
	F("run_no_retention", "/replications/3/run")
	r.add("page_after_run", o.Get(t, "/replications"))
	a.Hostd.Set("POST /replication/run", h.Reply{Status: 500, Body: "zfs send: signal received"})
	F("run_fail", "/replications/1/run")
	a.Hostd.Set("POST /replication/run", h.Reply{Status: 200, Body: `not json`})
	F("run_bad_json", "/replications/2/run")
	F("run_missing", "/replications/99/run")
	F("toggle_off", "/replications/1/toggle", "enabled", "0")
	F("toggle_on", "/replications/1/toggle", "enabled", "1")
	F("toggle_no_field", "/replications/2/toggle")
	F("toggle_missing", "/replications/99/toggle", "enabled", "1")
	F("delete_ok", "/replications/2/delete")
	F("delete_missing", "/replications/2/delete")
	r.golden("p5/replications")
}

func regexpMust(p string) *regexp.Regexp { return regexp.MustCompile(p) }
