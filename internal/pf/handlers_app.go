package pf

import (
	"io"
	"math"
	"net/http"
	"strings"

	"github.com/mohicann/pfortainer/internal/httpx"
)

func (a *App) renderPodmanErr(w http.ResponseWriter, u SessionUser, e *PodmanError) {
	unavailable := e.isSocketError()
	st := http.StatusInternalServerError
	if unavailable {
		st = http.StatusServiceUnavailable
	}
	c := base("", u)
	c["podman_unavailable"] = unavailable
	c["error"] = e.Error()
	page(w, st, "error.html", c)
}

func jsonErr(w http.ResponseWriter, status int, msg string) {
	httpx.JSON(w, status, map[string]string{"error": msg})
}

func jsonOK(w http.ResponseWriter) { httpx.JSON(w, 200, map[string]bool{"success": true}) }

func (a *App) containers(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	cs, e := a.PC.ListContainers()
	if e != nil {
		a.renderPodmanErr(w, u, e)
		return
	}
	vms := toContainerVms(cs)
	running := 0
	for _, c := range vms {
		if c.State == "running" {
			running++
		}
	}
	c := base("containers", u)
	c["stopped_count"] = int64(len(vms) - running)
	c["running_count"] = int64(running)
	c["containers"] = vms
	page(w, 200, "containers.html", c)
}

func (a *App) containerDetail(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	id := httpx.Param(r, "id")
	d, e := a.PC.InspectContainer(id)
	if e == nil {
		c := base("containers", u)
		c["c"] = toContainerDetailVm(d)
		c["inspect_failed"] = false
		page(w, 200, "container-detail.html", c)
		return
	}
	if !e.Transport && e.StatusCode == 404 {
		httpx.Text(w, 404, "컨테이너를 찾을 수 없습니다.")
		return
	}
	if !e.Transport && e.StatusCode >= 500 {
		if lc, le := a.PC.GetContainerByID(id); le == nil {
			c := base("containers", u)
			c["c"] = toContainerDetailVmFromList(lc)
			c["inspect_failed"] = true
			page(w, 200, "container-detail.html", c)
			return
		}
	}
	a.renderPodmanErr(w, u, e)
}

func (a *App) containerAction(w http.ResponseWriter, r *http.Request) {
	id, action := httpx.Param(r, "id"), httpx.Param(r, "action")
	var e *PodmanError
	switch action {
	case "start", "stop", "restart", "kill", "pause":
		e = a.PC.action("POST", "/containers/"+id+"/"+action)
	case "resume":
		e = a.PC.action("POST", "/containers/"+id+"/unpause")
	case "remove":
		e = a.PC.action("DELETE", "/containers/"+id)
	default:
		jsonErr(w, 400, "알 수 없는 작업입니다.")
		return
	}
	if e == nil {
		jsonOK(w)
		return
	}
	jsonErr(w, http.StatusConflict, containerActionErr(action, e))
}

func containerActionErr(action string, e *PodmanError) string {
	if e.Transport {
		return e.Error()
	}
	switch {
	case action == "remove" && e.StatusCode == 409:
		return "실행 중인 컨테이너는 삭제할 수 없습니다. 먼저 정지(stop) 후 다시 시도해주세요."
	case e.StatusCode == 404:
		return "컨테이너를 찾을 수 없습니다."
	case e.StatusCode == 409:
		return "현재 상태에서는 이 작업을 수행할 수 없습니다: " + e.Msg
	}
	return e.Msg
}

func (a *App) containerNewPage(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	page(w, 200, "container-new.html", base("containers", u))
}

func (a *App) containerBuild(w http.ResponseWriter, r *http.Request) {
	mp, ok := httpx.NewMultipart(w, r)
	if !ok {
		return
	}
	var tag, dockerfile string
	for {
		p, err := mp.Next()
		if err != nil {
			httpx.Text(w, 400, "요청 파싱 실패: "+err.Error())
			return
		}
		if p == nil {
			break
		}
		switch p.Name {
		case "tag":
			tag, _ = p.Text()
		case "dockerfile":
			dockerfile, _ = p.Text()
		}
	}
	if strings.TrimSpace(dockerfile) == "" {
		httpx.Text(w, 400, "Dockerfile 내용이 없습니다")
		return
	}
	s := newNDJSON(w)
	err := a.PC.BuildImage(strings.TrimSpace(tag), dockerfile, s.emit)
	if err == nil {
		s.raw(`{"type":"done","line":"0"}` + "\n")
		return
	}
	s.emit("error", err.Error())
	s.raw(`{"type":"done","line":"1"}` + "\n")
}

func (a *App) composeUp(w http.ResponseWriter, r *http.Request) {
	mp, ok := httpx.NewMultipart(w, r)
	if !ok {
		return
	}
	var compose string
	for {
		p, err := mp.Next()
		if err != nil {
			httpx.Text(w, 400, "요청 파싱 실패: "+err.Error())
			return
		}
		if p == nil {
			break
		}
		if p.Name == "compose" {
			compose, _ = p.Text()
		}
	}
	if strings.TrimSpace(compose) == "" {
		httpx.Text(w, 400, "docker-compose.yml 내용이 없습니다")
		return
	}
	if a.HC.AgentMode() == "host-socket" {
		body, err := a.HC.PostStream("/compose-up", []byte(compose))
		if err != nil {
			s := newNDJSON(w)
			s.emit("error", "hostd 연결 실패: "+err.Error())
			s.emit("done", "1")
			return
		}
		defer body.Close()
		w.Header().Set("Content-Type", "application/x-ndjson")
		w.Header().Set("X-Accel-Buffering", "no")
		w.WriteHeader(200)
		f, _ := w.(http.Flusher)
		buf := make([]byte, 32*1024)
		for {
			n, err := body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				if f != nil {
					f.Flush()
				}
			}
			if err != nil {
				return
			}
		}
	}
	s := newNDJSON(w)
	path, cleanup, err := composeTempFile([]byte(compose))
	if err != nil {
		s.emit("error", rustIOErr(err))
		s.emit("done", "1")
		return
	}
	defer cleanup()
	streamComposeUp(path, s)
}

func capPct(n int64) int32 {
	if n < 0 {
		return 0
	}
	if n > 100 {
		return 100
	}
	return int32(n)
}

// rround: Rust f64::round(0.5는 0에서 먼 쪽).
func rround(f float64) int64 { return int64(math.Round(f)) }

func pct(used, total int64) int32 {
	if total <= 0 {
		return 0
	}
	return capPct(rround(float64(used) / float64(total) * 100))
}

func (a *App) systemInfo(w http.ResponseWriter, r *http.Request) {
	u, ok := a.auth(w, r)
	if !ok {
		return
	}
	info, e := a.PC.GetSystemInfo()
	if e != nil {
		a.renderPodmanErr(w, u, e)
		return
	}
	h, st, v := info.Host, info.Store, info.Version
	memUsed, swapUsed := h.MemTotal-h.MemFree, h.SwapTotal-h.SwapFree
	osLabel := h.OS
	if h.Distribution.Distribution != "" {
		osLabel = h.Distribution.Distribution
		if h.Distribution.Version != "" {
			osLabel += " " + h.Distribution.Version
		}
	}
	s := SystemInfoVm{Hostname: h.Hostname, OS: osLabel, Kernel: h.Kernel, Arch: h.Arch, Uptime: h.Uptime, Cpus: h.Cpus,
		CpuUser: capPct(rround(h.CpuUtilization.UserPercent)), CpuSystem: capPct(rround(h.CpuUtilization.SystemPercent)), CpuIdle: capPct(rround(h.CpuUtilization.IdlePercent)),
		MemTotal: fmtBytes(h.MemTotal), MemUsed: fmtBytes(memUsed), MemFree: fmtBytes(h.MemFree), MemUsedPct: pct(memUsed, h.MemTotal),
		HasSwap: h.SwapTotal > 0, SwapTotal: fmtBytes(h.SwapTotal), SwapUsed: fmtBytes(swapUsed), SwapFree: fmtBytes(h.SwapFree), SwapUsedPct: pct(swapUsed, h.SwapTotal),
		PodmanVersion: v.Version, PodmanAPI: v.APIVersion, GoVersion: v.GoVersion, OsArch: v.OsArch,
		ContainerTotal: st.ContainerStore.Number, ContainerRunning: st.ContainerStore.Running, ContainerStopped: st.ContainerStore.Stopped, ContainerPaused: st.ContainerStore.Paused,
		ImageCount: st.ImageStore.Number, GraphDriver: st.GraphDriverName, GraphRoot: st.GraphRoot, RunRoot: st.RunRoot, VolumePath: st.VolumePath}
	c := base("system", u)
	c["s"] = s
	page(w, 200, "system.html", c)
}

type systemStatsResp struct {
	CpuUser     int32  `json:"cpuUser"`
	CpuSystem   int32  `json:"cpuSystem"`
	CpuIdle     int32  `json:"cpuIdle"`
	MemTotal    string `json:"memTotal"`
	MemUsed     string `json:"memUsed"`
	MemFree     string `json:"memFree"`
	MemUsedPct  int32  `json:"memUsedPct"`
	HasSwap     bool   `json:"hasSwap"`
	SwapTotal   string `json:"swapTotal"`
	SwapUsed    string `json:"swapUsed"`
	SwapFree    string `json:"swapFree"`
	SwapUsedPct int32  `json:"swapUsedPct"`
}

func (a *App) systemStats(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.auth(w, r); !ok {
		return
	}
	info, e := a.PC.GetSystemInfo()
	if e != nil {
		jsonErr(w, http.StatusBadGateway, e.Error())
		return
	}
	h := info.Host
	memUsed, swapUsed := h.MemTotal-h.MemFree, h.SwapTotal-h.SwapFree
	httpx.JSON(w, 200, systemStatsResp{capPct(rround(h.CpuUtilization.UserPercent)), capPct(rround(h.CpuUtilization.SystemPercent)), capPct(rround(h.CpuUtilization.IdlePercent)),
		fmtBytes(h.MemTotal), fmtBytes(memUsed), fmtBytes(h.MemFree), pct(memUsed, h.MemTotal), h.SwapTotal > 0, fmtBytes(h.SwapTotal), fmtBytes(swapUsed), fmtBytes(h.SwapFree), pct(swapUsed, h.SwapTotal)})
}

var _ = io.EOF
