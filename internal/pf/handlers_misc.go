package pf

import (
	"net/http"
	"strings"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func (a *App) desktopPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	prefs, found, err := a.DB.DesktopPrefsGet(u.Username)
	if err != nil || !found {
		prefs = "{}"
	}
	page(w, 200, "desktop.html", map[string]any{"pool1": a.Cfg.MetricsPool1, "pool2": a.Cfg.MetricsPool2, "net_iface": a.Cfg.MetricsNetIface,
		"prefs_json": strings.ReplaceAll(prefs, "<", `\u003c`), "current_user": u})
}

const desktopPrefsMax = 64 * 1024

func (a *App) desktopPrefsGet(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	p, found, err := a.DB.DesktopPrefsGet(u.Username)
	switch {
	case err != nil:
		jsonErr(w, 500, err.Error())
	case found:
		httpx.RawJSONResp(w, 200, p)
	default:
		httpx.JSON(w, 200, map[string]any{})
	}
}

func (a *App) desktopPrefsPut(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	v, ok := httpx.JsonValue(w, r)
	if !ok {
		return
	}
	if _, isObj := v.(map[string]any); !isObj {
		jsonErr(w, 400, "object required")
		return
	}
	s := httpx.MarshalString(v)
	if len(s) > desktopPrefsMax {
		jsonErr(w, http.StatusRequestEntityTooLarge, "prefs too large")
		return
	}
	if err := a.DB.DesktopPrefsSet(u.Username, s); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) metricsPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	c := base("metrics", u)
	c["pool1"], c["pool2"], c["net_iface"] = a.Cfg.MetricsPool1, a.Cfg.MetricsPool2, a.Cfg.MetricsNetIface
	page(w, 200, "metrics.html", c)
}

func (a *App) metricsData(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	q, ok := httpx.Query(w, r, []httpx.UField{{Name: "chart", Default: true}, {Name: "after", OptInt: true}, {Name: "points", OptInt: true}})
	if !ok {
		return
	}
	after, points := int64(-1800), int64(60)
	if p := q["after"].(*int64); p != nil {
		after = *p
	}
	if p := q["points"].(*int64); p != nil {
		points = *p
	}
	if points < 0 {
		points = 0
	}
	resp, msg := a.MC.Query(qstr(q, "chart"), after, int(points))
	if msg != "" {
		httpx.Text(w, 400, msg)
		return
	}
	httpx.JSON(w, 200, resp)
}

func (a *App) alertsPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	c := base("alerts", u)
	c["settings"], c["error"], c["success"] = loadAlertSettings(a.DB), qstr(q, "error"), qstr(q, "success")
	page(w, 200, "alerts.html", c)
}

var alertsForm = []httpx.UField{{Name: "email_enabled", Default: true}, {Name: "smtp_host", Default: true}, {Name: "smtp_port", Default: true},
	{Name: "smtp_user", Default: true}, {Name: "smtp_pass", Default: true}, {Name: "smtp_from", Default: true}, {Name: "smtp_to", Default: true},
	{Name: "webhook_enabled", Default: true}, {Name: "webhook_url", Default: true}, {Name: "check_pool_health", Default: true},
	{Name: "check_smart", Default: true}, {Name: "check_capacity", Default: true}, {Name: "check_scrub", Default: true},
	{Name: "capacity_pct", Default: true}, {Name: "cooldown_hours", Default: true}}

func (a *App) alertsSave(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, alertsForm)
	if !ok {
		return
	}
	s := AlertSettings{EmailEnabled: qstr(f, "email_enabled") == "1", SMTPHost: strings.TrimSpace(qstr(f, "smtp_host")), SMTPPort: 587,
		SMTPUser: strings.TrimSpace(qstr(f, "smtp_user")), SMTPPass: qstr(f, "smtp_pass"), SMTPFrom: strings.TrimSpace(qstr(f, "smtp_from")),
		SMTPTo: strings.TrimSpace(qstr(f, "smtp_to")), WebhookEnabled: qstr(f, "webhook_enabled") == "1", WebhookURL: strings.TrimSpace(qstr(f, "webhook_url")),
		CheckPoolHealth: qstr(f, "check_pool_health") == "1", CheckSmart: qstr(f, "check_smart") == "1", CheckCapacity: qstr(f, "check_capacity") == "1",
		CheckScrub: qstr(f, "check_scrub") == "1", CapacityPct: 85, CooldownHours: 4}
	if v, ok := parseI32(qstr(f, "smtp_port")); ok {
		s.SMTPPort = v
	}
	if v, ok := parseI32(qstr(f, "capacity_pct")); ok {
		s.CapacityPct = v
	}
	if v, msg := httpx.ParseI64(qstr(f, "cooldown_hours")); msg == "" {
		s.CooldownHours = v
	}
	if s.SMTPPass == "" {
		s.SMTPPass = loadAlertSettings(a.DB).SMTPPass
	}
	if err := saveAlertSettings(a.DB, s); err != nil {
		httpx.RedirectTo(w, "/alerts?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/alerts?success=설정+저장+완료")
}

func (a *App) alertTest(w http.ResponseWriter, r *http.Request) {
	s := loadAlertSettings(a.DB)
	var errs []string
	if s.EmailEnabled {
		body := "pfortainer 알림 테스트입니다.\n\n발송 시각: " + toLocal(time.Now()).Format("2006-01-02 15:04:05")
		if err := sendAlertEmail(s, "[pfortainer] 테스트 알림", body); err != nil {
			errs = append(errs, "이메일: "+err.Error())
		}
	}
	if s.WebhookEnabled && s.WebhookURL != "" {
		if err := sendWebhook(s.WebhookURL, "test", "pfortainer", "[pfortainer] 테스트 알림", "pfortainer 알림 테스트입니다."); err != nil {
			errs = append(errs, "웹훅: "+err.Error())
		}
	}
	if len(errs) > 0 {
		httpx.RedirectTo(w, "/alerts?error="+urlenc(strings.Join(errs, " / ")))
		return
	}
	if !s.EmailEnabled && (!s.WebhookEnabled || s.WebhookURL == "") {
		httpx.RedirectTo(w, "/alerts?error=활성화된+채널이+없습니다")
		return
	}
	httpx.RedirectTo(w, "/alerts?success=테스트+알림+발송+완료")
}

// ── 복제 ─────────────────────────────────────────────────────────────────────

type replRow struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	SourceDataset string `json:"source_dataset"`
	TargetPath    string `json:"target_path"`
	Recursive     bool   `json:"recursive"`
	Schedule      string `json:"schedule"`
	Retention     int64  `json:"retention"`
	LastRunFmt    string `json:"last_run_fmt"`
	LastSnapshot  string `json:"last_snapshot"`
	LastStatus    string `json:"last_status"`
	LastError     string `json:"last_error"`
	Enabled       bool   `json:"enabled"`
}

func (a *App) replicationPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	tasks, err := a.DB.ListReplTasks()
	errMsg := qstr(q, "error")
	if err != nil {
		tasks, errMsg = nil, "복제 태스크 조회 실패: "+err.Error()
	}
	rows := []replRow{}
	for _, t := range tasks {
		lr := ""
		if t.LastRunAt != nil {
			lr = fmtShort(*t.LastRunAt)
		}
		rows = append(rows, replRow{t.ID, t.Name, t.SourceDataset, t.TargetPath, t.Recursive, t.Schedule, t.Retention, lr, t.LastSnapshot, t.LastStatus, t.LastError, t.Enabled})
	}
	c := base("replication", u)
	c["tasks"], c["agent_mode"], c["error"], c["success"] = rows, a.HC.AgentMode(), errMsg, qstr(q, "success")
	page(w, 200, "replications.html", c)
}

func (a *App) replicationCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "name", Default: true}, {Name: "source_dataset", Default: true}, {Name: "target_path", Default: true},
		{Name: "recursive", Default: true}, {Name: "schedule", Default: true}, {Name: "retention", Default: true}})
	if !ok {
		return
	}
	if qstr(f, "name") == "" || qstr(f, "source_dataset") == "" || qstr(f, "target_path") == "" {
		httpx.RedirectTo(w, "/replications?error=이름+소스+대상은+필수입니다")
		return
	}
	sched := qstr(f, "schedule")
	if sched == "" {
		sched = "manual"
	}
	ret, msg := httpx.ParseI64(qstr(f, "retention"))
	if msg != "" {
		ret = 14
	}
	_, err := a.DB.CreateReplTask(&DbReplTask{Name: qstr(f, "name"), SourceDataset: qstr(f, "source_dataset"), TargetPath: qstr(f, "target_path"),
		Recursive: qstr(f, "recursive") == "1", Schedule: sched, Retention: ret, Enabled: true})
	if err != nil {
		httpx.RedirectTo(w, "/replications?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/replications?success=복제+태스크+추가됨")
}

func (a *App) replicationDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathI64(w, r, "id")
	if !ok {
		return
	}
	if err := a.DB.DeleteReplTask(id); err != nil {
		httpx.RedirectTo(w, "/replications?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/replications?success=삭제됨")
}

func (a *App) replicationToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathI64(w, r, "id")
	if !ok {
		return
	}
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "enabled", Default: true}})
	if !ok {
		return
	}
	if err := a.DB.ToggleReplTask(id, qstr(f, "enabled") == "1"); err != nil {
		httpx.RedirectTo(w, "/replications?error="+urlenc(err.Error()))
		return
	}
	httpx.RedirectTo(w, "/replications")
}

func (a *App) replicationRun(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathI64(w, r, "id")
	if !ok {
		return
	}
	tasks, err := a.DB.ListReplTasks()
	if err != nil {
		httpx.RedirectTo(w, "/replications?error="+urlenc(err.Error()))
		return
	}
	var task *DbReplTask
	for i := range tasks {
		if tasks[i].ID == id {
			task = &tasks[i]
		}
	}
	if task == nil {
		httpx.RedirectTo(w, "/replications?error=태스크+없음")
		return
	}
	res, err := runReplication(a.HC, task.SourceDataset, task.TargetPath, task.LastSnapshot, task.Recursive)
	if err != nil {
		a.DB.UpdateReplTaskResult(id, task.LastSnapshot, "error", err.Error())
		httpx.RedirectTo(w, "/replications?error=복제+실패:+"+urlenc(err.Error()))
		return
	}
	a.DB.UpdateReplTaskResult(id, res.CurrentSnapshot, "ok", "")
	if task.Retention > 0 {
		pruneSnapshots(a.HC, task.TargetPath, "auto", int32(task.Retention))
	}
	httpx.RedirectTo(w, "/replications?success=복제+완료+→+"+urlenc(res.CurrentSnapshot))
}

func (a *App) schedulesPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	scheds, _ := a.DB.ListSchedules()
	all, _ := listZfsDatasets(a.HC)
	names := []string{}
	for _, d := range all {
		if d.Kind == "filesystem" {
			names = append(names, d.Name)
		}
	}
	pools, err := listZfsPools(a.HC)
	if err != nil {
		pools = []ZfsPool{}
	}
	disks, err := smartSummary(a.HC)
	if err != nil {
		disks = []DiskSmart{}
	}
	c := base("schedules", u)
	c["schedules"], c["datasets"], c["pools"], c["disks"] = scheduleRows(scheds), names, pools, disks
	c["error"], c["success"] = qstr(q, "error"), qstr(q, "success")
	page(w, 200, "schedules.html", c)
}
