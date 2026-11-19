package pf

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func isUnavailable(err error) bool {
	he, ok := err.(*HostError)
	return ok && he.Unavailable
}

func (a *App) filesystemInfo(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	pools, err := listZfsPools(a.HC)
	if err != nil {
		renderErrorPage(w, u, err.Error(), isUnavailable(err))
		return
	}
	ds, err := listZfsDatasets(a.HC)
	if err != nil {
		renderErrorPage(w, u, err.Error(), isUnavailable(err))
		return
	}
	c := base("filesystem", u)
	c["pools"], c["datasets"] = pools, ds
	page(w, 200, "filesystem.html", c)
}

func (a *App) filesystemCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "FilesystemCreateReq", []httpx.Field{{Name: "parent", Default: true}, {Name: "name", Default: true}})
	if !ok {
		return
	}
	parent, name := strings.TrimSpace(q["parent"].(string)), strings.TrimSpace(q["name"].(string))
	if parent == "" || name == "" {
		jsonErr(w, 400, "상위 데이터셋과 이름을 모두 입력하세요.")
		return
	}
	for _, c := range name {
		if !(c < 128 && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9') || strings.ContainsRune("-_.:", c)) {
			jsonErr(w, 400, "데이터셋 이름에 사용할 수 없는 문자가 포함되어 있습니다. (허용: 영문·숫자·-·_·.·:)")
			return
		}
	}
	if err := createZfsDataset(a.HC, parent+"/"+name); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) filesystemDelete(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "FilesystemDeleteReq", []httpx.Field{{Name: "dataset", Default: true}})
	if !ok {
		return
	}
	name := strings.TrimSpace(q["dataset"].(string))
	if name == "" || !strings.Contains(name, "/") {
		jsonErr(w, 400, "최상위 풀은 삭제할 수 없습니다.")
		return
	}
	if err := destroyZfsDataset(a.HC, name); err != nil {
		jsonErr(w, http.StatusConflict, err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) storageHealth(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	var pools []PoolStatus
	var disks []DiskSmart
	var perr, derr string
	if p, err := poolStatus(a.HC); err != nil {
		pools, perr = []PoolStatus{}, err.Error()
	} else {
		pools = p
	}
	if d, err := smartSummary(a.HC); err != nil {
		disks, derr = []DiskSmart{}, err.Error()
	} else {
		disks = d
	}
	c := base("storage", u)
	c["agent_mode"] = a.HC.AgentMode()
	c["pools"], c["disks"], c["pool_error"], c["disk_error"] = pools, disks, perr, derr
	page(w, 200, "storage.html", c)
}

func (a *App) smartTestRun(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "SmartTestReq", []httpx.Field{{Name: "device"}, {Name: "test_type"}})
	if !ok {
		return
	}
	out, err := startSmartTest(a.HC, q["device"].(string), q["test_type"].(string))
	if err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	httpx.JSON(w, 200, map[string]string{"output": out})
}

type snapRow struct {
	Name        string `json:"name"`
	SnapName    string `json:"snap_name"`
	CreationFmt string `json:"creation_fmt"`
	Used        string `json:"used"`
	Refer       string `json:"refer"`
}

type snapGroupRow struct {
	Dataset   string    `json:"dataset"`
	Total     int64     `json:"total"`
	Index     int64     `json:"index"`
	Snapshots []snapRow `json:"snapshots"`
}

type scheduleRow struct {
	ID         int64  `json:"id"`
	Kind       string `json:"kind"`
	Target     string `json:"target"`
	Frequency  string `json:"frequency"`
	Retention  int64  `json:"retention"`
	Prefix     string `json:"prefix"`
	Enabled    bool   `json:"enabled"`
	NextRunFmt string `json:"next_run_fmt"`
	LastRunFmt string `json:"last_run_fmt"`
}

func fmtLong(t time.Time) string {
	t = toLocal(t)
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
}

func scheduleRows(scheds []DbSchedule) []scheduleRow {
	rows := []scheduleRow{}
	for _, s := range scheds {
		row := scheduleRow{ID: s.ID, Kind: s.Kind, Target: s.Target, Frequency: s.Frequency, Retention: s.Retention, Prefix: s.Prefix, Enabled: s.Enabled}
		if s.NextRun != nil {
			row.NextRunFmt = fmtShort(*s.NextRun)
		}
		if s.LastRun != nil {
			row.LastRunFmt = fmtShort(*s.LastRun)
		}
		rows = append(rows, row)
	}
	return rows
}

func (a *App) snapshotsPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	q, ok := httpx.Query(w, r, msgQuery)
	if !ok {
		return
	}
	groups, order, _ := listSnapshotsByDataset(a.HC, "")
	all, _ := listZfsDatasets(a.HC)
	dsNames := []string{}
	for _, d := range all {
		if d.Kind == "filesystem" && !strings.Contains(d.Name, "/containers/") {
			dsNames = append(dsNames, d.Name)
		}
	}
	sg := []snapGroupRow{}
	for idx, ds := range order {
		if strings.Contains(ds, "/containers/") {
			continue
		}
		snaps := groups[ds]
		rows := []snapRow{}
		for i := len(snaps) - 1; i >= 0; i-- {
			s := snaps[i]
			rows = append(rows, snapRow{s.Name, s.SnapName, fmtLong(s.Creation), s.Used, s.Refer})
		}
		sg = append(sg, snapGroupRow{ds, int64(len(snaps)), int64(idx), rows})
	}
	scheds, _ := a.DB.ListSchedules()
	pools, _ := listZfsPools(a.HC)
	poolNames := []string{}
	for _, p := range pools {
		poolNames = append(poolNames, p.Name)
	}
	disks, err := listSmartDevices(a.HC)
	if err != nil {
		disks = []string{}
	}
	c := base("snapshots", u)
	c["groups"], c["datasets"], c["schedules"] = sg, dsNames, scheduleRows(scheds)
	c["sched_pools_json"] = httpx.MarshalString(poolNames)
	c["sched_disks_json"] = httpx.MarshalString(disks)
	c["datasets_json"] = httpx.MarshalString(dsNames)
	c["error"], c["success"] = qstr(q, "error"), qstr(q, "success")
	page(w, 200, "snapshots.html", c)
}

func listSmartDevices(hc *HostClient) ([]string, error) {
	out, err := hc.Get("/smart-scan", []string{"smartctl", "--scan"})
	if err != nil {
		return nil, err
	}
	return parseSmartScan(out), nil
}

func (a *App) snapshotCreate(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "SnapCreateReq", []httpx.Field{{Name: "dataset", Default: true}, {Name: "snapname", Default: true}})
	if !ok {
		return
	}
	ds, sn := q["dataset"].(string), q["snapname"].(string)
	if ds == "" || sn == "" {
		jsonErr(w, 400, "dataset and snapname required")
		return
	}
	if err := createSnapshot(a.HC, ds, sn); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) snapNameAction(w http.ResponseWriter, r *http.Request, f func(*HostClient, string) error) {
	q, ok := httpx.Json(w, r, "SnapNameReq", []httpx.Field{{Name: "name", Default: true}})
	if !ok {
		return
	}
	if err := f(a.HC, q["name"].(string)); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w)
}

func (a *App) snapshotDelete(w http.ResponseWriter, r *http.Request) {
	a.snapNameAction(w, r, destroySnapshot)
}
func (a *App) snapshotRollback(w http.ResponseWriter, r *http.Request) {
	a.snapNameAction(w, r, rollbackSnapshot)
}

func (a *App) snapshotClone(w http.ResponseWriter, r *http.Request) {
	q, ok := httpx.Json(w, r, "SnapCloneReq", []httpx.Field{{Name: "name", Default: true}, {Name: "target", Default: true}})
	if !ok {
		return
	}
	n, t := q["name"].(string), q["target"].(string)
	if n == "" || t == "" {
		jsonErr(w, 400, "name and target required")
		return
	}
	if err := cloneSnapshot(a.HC, n, t); err != nil {
		jsonErr(w, 500, err.Error())
		return
	}
	jsonOK(w)
}

// ── 스케줄 CRUD ──────────────────────────────────────────────────────────────

func (a *App) scheduleCreate(w http.ResponseWriter, r *http.Request) {
	f, ok := httpx.Form(w, r, []httpx.UField{{Name: "type", Default: true}, {Name: "target", Default: true}, {Name: "frequency", Default: true}, {Name: "prefix", Default: true}, {Name: "retention", Default: true}})
	if !ok {
		return
	}
	prefix := strings.TrimSpace(qstr(f, "prefix"))
	if prefix == "" {
		prefix = "auto"
	}
	retention, msg := httpx.ParseI64(strings.TrimSpace(qstr(f, "retention")))
	if msg != "" {
		retention = 7
	}
	kind, target, freq := qstr(f, "type"), qstr(f, "target"), qstr(f, "frequency")
	if kind == "" || target == "" || freq == "" {
		httpx.RedirectTo(w, "/snapshots?error=필수+항목+누락")
		return
	}
	next := NextRunTime(freq, time.Now())
	if _, err := a.DB.CreateSchedule(&DbSchedule{Kind: kind, Target: target, Frequency: freq, Retention: retention, Prefix: prefix, Enabled: true, NextRun: &next}); err != nil {
		httpx.RedirectTo(w, "/snapshots?error="+err.Error())
		return
	}
	httpx.RedirectTo(w, "/snapshots?success=스케줄+추가+완료")
}

func (a *App) scheduleToggle(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathI64(w, r, "id")
	if !ok {
		return
	}
	s, err := a.DB.GetSchedule(id)
	if err != nil {
		httpx.RedirectTo(w, "/snapshots?error="+err.Error())
		return
	}
	if err := a.DB.ToggleSchedule(id, !s.Enabled); err != nil {
		httpx.RedirectTo(w, "/snapshots?error="+err.Error())
		return
	}
	httpx.RedirectTo(w, "/snapshots")
}

func (a *App) scheduleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathI64(w, r, "id")
	if !ok {
		return
	}
	if err := a.DB.DeleteSchedule(id); err != nil {
		httpx.RedirectTo(w, "/snapshots?error="+err.Error())
		return
	}
	httpx.RedirectTo(w, "/snapshots?success=삭제+완료")
}

func (a *App) scheduleRunNow(w http.ResponseWriter, r *http.Request) {
	id, ok := httpx.PathI64(w, r, "id")
	if !ok {
		return
	}
	s, err := a.DB.GetSchedule(id)
	if err != nil {
		httpx.RedirectTo(w, "/snapshots?error="+err.Error())
		return
	}
	if err := runSchedule(a.HC, a.DB, &s); err != nil {
		httpx.RedirectTo(w, "/snapshots?error="+err.Error())
		return
	}
	httpx.RedirectTo(w, "/snapshots?success=즉시+실행+완료")
}

var _ = strconv.Itoa
