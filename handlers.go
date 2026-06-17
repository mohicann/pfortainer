package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

type handlers struct {
	cfg *Config
	pc  *PodmanClient
}

func newHandlers(cfg *Config, pc *PodmanClient) *handlers {
	return &handlers{cfg: cfg, pc: pc}
}

// ── Auth ──────────────────────────────────────────────────────────────────────

func (h *handlers) loginPage(w http.ResponseWriter, r *http.Request) {
	render(w, "login", map[string]any{"Error": ""})
}

func (h *handlers) login(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	pw := r.FormValue("password")
	if subtle.ConstantTimeCompare([]byte(pw), []byte(h.cfg.AdminPassword)) == 1 {
		setAuthCookie(w, h.cfg.SessionSecret)
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}
	w.WriteHeader(http.StatusUnauthorized)
	render(w, "login", map[string]any{"Error": "비밀번호가 올바르지 않습니다."})
}

func (h *handlers) logout(w http.ResponseWriter, r *http.Request) {
	clearAuthCookie(w)
	http.Redirect(w, r, "/login", http.StatusFound)
}

// ── Pages ─────────────────────────────────────────────────────────────────────

type ContainerVM struct {
	ID      string
	Name    string
	State   string
	Status  string
	Image   string
	Created string
	Ports   []string
	ShortID string
}

type NetworkVM struct {
	Name       string
	IPAddress  string
	Gateway    string
	MacAddress string
}

type MountVM struct {
	Type        string
	Source      string
	Destination string
	Mode        string
	RW          bool
}

type ContainerDetailVM struct {
	ID            string
	ShortID       string
	Name          string
	Image         string
	Command       string
	Created       string
	State         string
	Status        string
	Running       bool
	Pid           int
	ExitCode      int
	StartedAt     string
	FinishedAt    string
	RestartCount  int
	RestartPolicy string
	NetworkMode   string
	Ports         []string
	Networks      []NetworkVM
	Mounts        []MountVM
	Env           []string
	Labels        map[string]string
}

type ImageVM struct {
	ID      string
	Tags    []string
	ShortID string
	Size    string
	Created string
}

type SystemInfoVM struct {
	Hostname  string
	OS        string
	Kernel    string
	Arch      string
	Uptime    string
	CPUs      int
	CPUUser   int
	CPUSystem int
	CPUIdle   int
	MemTotal   string
	MemUsed    string
	MemFree    string
	MemUsedPct int
	HasSwap     bool
	SwapTotal   string
	SwapUsed    string
	SwapFree    string
	SwapUsedPct int
	PodmanVersion string
	PodmanAPI     string
	GoVersion     string
	OsArch        string
	ContainerTotal   int
	ContainerRunning int
	ContainerStopped int
	ContainerPaused  int
	ImageCount       int
	GraphDriver string
	GraphRoot   string
	RunRoot     string
	VolumePath  string
}

func (h *handlers) dashboard(w http.ResponseWriter, r *http.Request) {
	cs, err := h.pc.ListContainers()
	if err != nil {
		log.Printf("podman error: %v", err)
		renderErr(w, err)
		return
	}
	imgs, err := h.pc.ListImages()
	if err != nil {
		log.Printf("podman error: %v", err)
		renderErr(w, err)
		return
	}
	vms := toContainerVMs(cs)
	running := filterRunning(vms)
	render(w, "dashboard", map[string]any{
		"ActivePage":        "dashboard",
		"Total":             len(vms),
		"Running":           len(running),
		"Stopped":           len(vms) - len(running),
		"ImageCount":        len(imgs),
		"RunningContainers": running,
	})
}

func (h *handlers) containers(w http.ResponseWriter, r *http.Request) {
	cs, err := h.pc.ListContainers()
	if err != nil {
		renderErr(w, err)
		return
	}
	vms := toContainerVMs(cs)
	runningCount := 0
	for _, c := range vms {
		if c.State == "running" {
			runningCount++
		}
	}
	render(w, "containers", map[string]any{
		"ActivePage":   "containers",
		"Containers":   vms,
		"RunningCount": runningCount,
		"StoppedCount": len(vms) - runningCount,
	})
}

func (h *handlers) containerDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	c, err := h.pc.InspectContainer(id)
	if err != nil {
		if pe, ok := err.(*PodmanError); ok && pe.StatusCode == http.StatusNotFound {
			http.Error(w, "컨테이너를 찾을 수 없습니다.", http.StatusNotFound)
			return
		}
		// Podman inspect panics on certain containers on FreeBSD (server-side nil pointer bug).
		// Fall back to list data so the page is still usable.
		if pe, ok := err.(*PodmanError); ok && pe.StatusCode >= 500 {
			lc, lerr := h.pc.GetContainerByID(id)
			if lerr == nil {
				render(w, "container-detail", map[string]any{
					"ActivePage":    "containers",
					"C":             toContainerDetailVMFromList(lc),
					"InspectFailed": true,
				})
				return
			}
		}
		renderErr(w, err)
		return
	}
	render(w, "container-detail", map[string]any{
		"ActivePage": "containers",
		"C":          toContainerDetailVM(c),
	})
}

// ── Container actions ────────────────────────────────────────────────────────

func (h *handlers) containerAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")

	var err error
	switch action {
	case "start":
		err = h.pc.StartContainer(id)
	case "stop":
		err = h.pc.StopContainer(id)
	case "restart":
		err = h.pc.RestartContainer(id)
	case "kill":
		err = h.pc.KillContainer(id)
	case "pause":
		err = h.pc.PauseContainer(id)
	case "resume":
		err = h.pc.UnpauseContainer(id)
	case "remove":
		err = h.pc.RemoveContainer(id)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "알 수 없는 작업입니다."})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": actionErrorMessage(action, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func actionErrorMessage(action string, err error) string {
	var pe *PodmanError
	if pErr, ok := err.(*PodmanError); ok {
		pe = pErr
	}
	if pe == nil {
		return err.Error()
	}
	switch {
	case action == "remove" && pe.StatusCode == http.StatusConflict:
		return "실행 중인 컨테이너는 삭제할 수 없습니다. 먼저 정지(stop) 후 다시 시도해주세요."
	case pe.StatusCode == http.StatusNotFound:
		return "컨테이너를 찾을 수 없습니다."
	case pe.StatusCode == http.StatusConflict:
		return "현재 상태에서는 이 작업을 수행할 수 없습니다: " + pe.Message
	default:
		return pe.Message
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

type systemStatsResponse struct {
	CPUUser     int    `json:"cpuUser"`
	CPUSystem   int    `json:"cpuSystem"`
	CPUIdle     int    `json:"cpuIdle"`
	MemTotal    string `json:"memTotal"`
	MemUsed     string `json:"memUsed"`
	MemFree     string `json:"memFree"`
	MemUsedPct  int    `json:"memUsedPct"`
	HasSwap     bool   `json:"hasSwap"`
	SwapTotal   string `json:"swapTotal"`
	SwapUsed    string `json:"swapUsed"`
	SwapFree    string `json:"swapFree"`
	SwapUsedPct int    `json:"swapUsedPct"`
}

func (h *handlers) systemStats(w http.ResponseWriter, r *http.Request) {
	info, err := h.pc.GetSystemInfo()
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
		return
	}
	host := info.Host
	capPct := func(n int) int {
		if n > 100 {
			return 100
		}
		if n < 0 {
			return 0
		}
		return n
	}
	memUsed := host.MemTotal - host.MemFree
	memPct := 0
	if host.MemTotal > 0 {
		memPct = capPct(int(math.Round(float64(memUsed) / float64(host.MemTotal) * 100)))
	}
	swapUsed := host.SwapTotal - host.SwapFree
	swapPct := 0
	if host.SwapTotal > 0 {
		swapPct = capPct(int(math.Round(float64(swapUsed) / float64(host.SwapTotal) * 100)))
	}
	writeJSON(w, http.StatusOK, systemStatsResponse{
		CPUUser:     capPct(int(math.Round(host.CPUUtilization.UserPercent))),
		CPUSystem:   capPct(int(math.Round(host.CPUUtilization.SystemPercent))),
		CPUIdle:     capPct(int(math.Round(host.CPUUtilization.IdlePercent))),
		MemTotal:    fmtBytes(host.MemTotal),
		MemUsed:     fmtBytes(memUsed),
		MemFree:     fmtBytes(host.MemFree),
		MemUsedPct:  memPct,
		HasSwap:     host.SwapTotal > 0,
		SwapTotal:   fmtBytes(host.SwapTotal),
		SwapUsed:    fmtBytes(swapUsed),
		SwapFree:    fmtBytes(host.SwapFree),
		SwapUsedPct: swapPct,
	})
}

func (h *handlers) systemInfo(w http.ResponseWriter, r *http.Request) {
	info, err := h.pc.GetSystemInfo()
	if err != nil {
		log.Printf("podman error: %v", err)
		renderErr(w, err)
		return
	}

	host := info.Host
	store := info.Store
	ver := info.Version

	capPct := func(n int) int {
		if n > 100 {
			return 100
		}
		if n < 0 {
			return 0
		}
		return n
	}

	memUsed := host.MemTotal - host.MemFree
	memPct := 0
	if host.MemTotal > 0 {
		memPct = capPct(int(math.Round(float64(memUsed) / float64(host.MemTotal) * 100)))
	}

	swapUsed := host.SwapTotal - host.SwapFree
	swapPct := 0
	if host.SwapTotal > 0 {
		swapPct = capPct(int(math.Round(float64(swapUsed) / float64(host.SwapTotal) * 100)))
	}

	osLabel := host.OS
	if host.Distribution.Distribution != "" {
		osLabel = host.Distribution.Distribution
		if host.Distribution.Version != "" {
			osLabel += " " + host.Distribution.Version
		}
	}

	vm := SystemInfoVM{
		Hostname:         host.Hostname,
		OS:               osLabel,
		Kernel:           host.Kernel,
		Arch:             host.Arch,
		Uptime:           host.Uptime,
		CPUs:             host.CPUs,
		CPUUser:          capPct(int(math.Round(host.CPUUtilization.UserPercent))),
		CPUSystem:        capPct(int(math.Round(host.CPUUtilization.SystemPercent))),
		CPUIdle:          capPct(int(math.Round(host.CPUUtilization.IdlePercent))),
		MemTotal:         fmtBytes(host.MemTotal),
		MemUsed:          fmtBytes(memUsed),
		MemFree:          fmtBytes(host.MemFree),
		MemUsedPct:       memPct,
		HasSwap:          host.SwapTotal > 0,
		SwapTotal:        fmtBytes(host.SwapTotal),
		SwapUsed:         fmtBytes(swapUsed),
		SwapFree:         fmtBytes(host.SwapFree),
		SwapUsedPct:      swapPct,
		PodmanVersion:    ver.Version,
		PodmanAPI:        ver.APIVersion,
		GoVersion:        ver.GoVersion,
		OsArch:           ver.OsArch,
		ContainerTotal:   store.ContainerStore.Number,
		ContainerRunning: store.ContainerStore.Running,
		ContainerStopped: store.ContainerStore.Stopped,
		ContainerPaused:  store.ContainerStore.Paused,
		ImageCount:       store.ImageStore.Number,
		GraphDriver:      store.GraphDriverName,
		GraphRoot:        store.GraphRoot,
		RunRoot:          store.RunRoot,
		VolumePath:       store.VolumePath,
	}

	render(w, "system", map[string]any{
		"ActivePage": "system",
		"S":          vm,
	})
}

func (h *handlers) images(w http.ResponseWriter, r *http.Request) {
	imgs, err := h.pc.ListImages()
	if err != nil {
		renderErr(w, err)
		return
	}
	render(w, "images", map[string]any{
		"ActivePage": "images",
		"Images":     toImageVMs(imgs),
	})
}

// ── Image actions ─────────────────────────────────────────────────────────────

func (h *handlers) imageAction(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	action := r.PathValue("action")
	force := r.URL.Query().Get("force") == "true"

	var err error
	switch action {
	case "remove":
		err = h.pc.RemoveImage(id, force)
	default:
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "알 수 없는 작업입니다."})
		return
	}

	if err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": imageActionErrorMessage(force, err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func imageActionErrorMessage(force bool, err error) string {
	pe, ok := err.(*PodmanError)
	if !ok {
		return err.Error()
	}
	switch {
	case pe.StatusCode == http.StatusNotFound:
		return "이미지를 찾을 수 없습니다."
	case pe.StatusCode == http.StatusConflict && force:
		return "실행 중인 컨테이너가 사용 중인 이미지는 강제 삭제할 수 없습니다. 컨테이너를 먼저 정지/삭제해주세요."
	case pe.StatusCode == http.StatusConflict:
		return "이 이미지를 사용 중인 컨테이너가 있어 삭제할 수 없습니다. 강제 삭제(force)를 시도해보세요."
	default:
		return pe.Message
	}
}

// ── Converters ────────────────────────────────────────────────────────────────

func toContainerVMs(cs []APIContainer) []ContainerVM {
	out := make([]ContainerVM, 0, len(cs))
	for _, c := range cs {
		name := "unnamed"
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, ContainerVM{
			ID:      c.ID,
			Name:    name,
			State:   strings.ToLower(c.State),
			Status:  containerStatusText(c),
			Image:   c.Image,
			Created: fmtTSStr(c.Created),
			Ports:   fmtPorts(c.Ports),
			ShortID: shortID(c.ID),
		})
	}
	return out
}

func toImageVMs(imgs []APIImage) []ImageVM {
	out := make([]ImageVM, 0, len(imgs))
	for _, img := range imgs {
		tags := make([]string, 0, len(img.RepoTags))
		for _, t := range img.RepoTags {
			if t != "<none>:<none>" {
				tags = append(tags, t)
			}
		}
		id := strings.TrimPrefix(img.ID, "sha256:")
		out = append(out, ImageVM{
			ID:      id,
			Tags:    tags,
			ShortID: shortID(id),
			Size:    fmtBytes(img.Size),
			Created: fmtTS(img.Created),
		})
	}
	return out
}

func toContainerDetailVM(c *APIContainerDetail) ContainerDetailVM {
	parts := []string{}
	if c.Config.Entrypoint != "" {
		parts = append(parts, string(c.Config.Entrypoint))
	}
	parts = append(parts, c.Config.Cmd...)
	command := strings.Join(parts, " ")

	ports := []string{}
	for port, bindings := range c.NetworkSettings.Ports {
		if len(bindings) == 0 {
			ports = append(ports, port)
			continue
		}
		for _, b := range bindings {
			ports = append(ports, fmt.Sprintf("%s:%s->%s", b.HostIP, b.HostPort, port))
		}
	}
	sort.Strings(ports)

	networks := make([]NetworkVM, 0, len(c.NetworkSettings.Networks))
	for name, n := range c.NetworkSettings.Networks {
		networks = append(networks, NetworkVM{
			Name:       name,
			IPAddress:  n.IPAddress,
			Gateway:    n.Gateway,
			MacAddress: n.MacAddress,
		})
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })

	mounts := make([]MountVM, 0, len(c.Mounts))
	for _, m := range c.Mounts {
		src := m.Source
		if m.Type == "volume" && m.Name != "" {
			src = m.Name
		}
		mounts = append(mounts, MountVM{
			Type:        m.Type,
			Source:      src,
			Destination: m.Destination,
			Mode:        m.Mode,
			RW:          m.RW,
		})
	}

	return ContainerDetailVM{
		ID:            c.ID,
		ShortID:       shortID(c.ID),
		Name:          strings.TrimPrefix(c.Name, "/"),
		Image:         c.Image,
		Command:       command,
		Created:       fmtTSStr(c.Created),
		State:         strings.ToLower(c.State.Status),
		Status:        c.State.Status,
		Running:       c.State.Running,
		Pid:           c.State.Pid,
		ExitCode:      c.State.ExitCode,
		StartedAt:     fmtTSStr(c.State.StartedAt),
		FinishedAt:    fmtTSStr(c.State.FinishedAt),
		RestartCount:  c.RestartCount,
		RestartPolicy: c.HostConfig.RestartPolicy.Name,
		NetworkMode:   c.HostConfig.NetworkMode,
		Ports:         ports,
		Networks:      networks,
		Mounts:        mounts,
		Env:           c.Config.Env,
		Labels:        c.Config.Labels,
	}
}

func toContainerDetailVMFromList(c *APIContainer) ContainerDetailVM {
	name := "unnamed"
	if len(c.Names) > 0 {
		name = strings.TrimPrefix(c.Names[0], "/")
	}
	state := strings.ToLower(c.State)

	mounts := make([]MountVM, 0, len(c.Mounts))
	for _, m := range c.Mounts {
		mounts = append(mounts, MountVM{Destination: m, RW: true})
	}

	networks := make([]NetworkVM, 0, len(c.Networks))
	for _, n := range c.Networks {
		networks = append(networks, NetworkVM{Name: n})
	}
	sort.Slice(networks, func(i, j int) bool { return networks[i].Name < networks[j].Name })

	return ContainerDetailVM{
		ID:           c.ID,
		ShortID:      shortID(c.ID),
		Name:         name,
		Image:        c.Image,
		Command:      strings.Join(c.Command, " "),
		State:        state,
		Status:       containerStatusText(*c),
		Running:      state == "running",
		Pid:          c.Pid,
		RestartCount: c.Restarts,
		Ports:        fmtPorts(c.Ports),
		Created:      fmtTSStr(c.Created),
		Labels:       c.Labels,
		Mounts:       mounts,
		Networks:     networks,
	}
}

func filterRunning(cs []ContainerVM) []ContainerVM {
	out := make([]ContainerVM, 0)
	for _, c := range cs {
		if c.State == "running" {
			out = append(out, c)
		}
	}
	return out
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func fmtTS(unix int64) string {
	return time.Unix(unix, 0).UTC().Format("2006-01-02 15:04")
}

func fmtTSStr(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02 15:04:05")
}

func fmtBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := int(math.Log(float64(b)) / math.Log(1024))
	if i >= len(units) {
		i = len(units) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(b)/math.Pow(1024, float64(i)), units[i])
}

func fmtPorts(ports []APIPort) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range ports {
		count := int(p.Range)
		if count == 0 {
			count = 1
		}
		for i := 0; i < count; i++ {
			cport := int(p.ContainerPort) + i
			var label string
			if p.HostPort > 0 {
				hostIP := p.HostIP
				if hostIP == "" {
					hostIP = "0.0.0.0"
				}
				label = fmt.Sprintf("%s:%d->%d/%s", hostIP, int(p.HostPort)+i, cport, p.Protocol)
			} else {
				label = fmt.Sprintf("%d/%s", cport, p.Protocol)
			}
			if !seen[label] {
				seen[label] = true
				out = append(out, label)
			}
		}
	}
	return out
}

// containerStatusText builds a Docker-style status string (e.g. "Up 2 hours",
// "Exited (0) 5 minutes ago") from libpod's container list fields.
func containerStatusText(c APIContainer) string {
	switch strings.ToLower(c.State) {
	case "running":
		return "Up " + humanizeDuration(time.Since(time.Unix(c.StartedAt, 0)))
	case "paused":
		return "Paused"
	case "created":
		return "Created"
	case "exited", "stopped":
		if c.ExitedAt > 0 {
			return fmt.Sprintf("Exited (%d) %s ago", c.ExitCode, humanizeDuration(time.Since(time.Unix(c.ExitedAt, 0))))
		}
		return fmt.Sprintf("Exited (%d)", c.ExitCode)
	default:
		return c.State
	}
}

func humanizeDuration(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%d seconds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%d minutes", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%d hours", int(d.Hours()))
	default:
		return fmt.Sprintf("%d days", int(d.Hours()/24))
	}
}
