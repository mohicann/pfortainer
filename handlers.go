package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type handlers struct {
	cfg   *Config
	pc    *PodmanClient
	mc    *MetricsCollector
	cdb   *ConfigDB
	sched *Scheduler
}

func newHandlers(cfg *Config, pc *PodmanClient, mc *MetricsCollector, cdb *ConfigDB, sched *Scheduler) *handlers {
	return &handlers{cfg: cfg, pc: pc, mc: mc, cdb: cdb, sched: sched}
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
	username := strings.TrimSpace(r.FormValue("username"))
	pw := r.FormValue("password")
	user, err := h.cdb.VerifyPassword(username, pw)
	if err != nil {
		log.Printf("login: db error: %v", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	if user == nil {
		w.WriteHeader(http.StatusUnauthorized)
		render(w, "login", map[string]any{"Error": "사용자명 또는 비밀번호가 올바르지 않습니다."})
		return
	}
	_, totpEnabled, _ := h.cdb.GetUserTOTP(username)
	if totpEnabled {
		setPreAuthCookie(w, username, h.cfg.SessionSecret)
		http.Redirect(w, r, "/login/totp", http.StatusSeeOther)
		return
	}
	setAuthCookie(w, h.cfg.SessionSecret, user.Username, user.Role)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

func (h *handlers) totpPage(w http.ResponseWriter, r *http.Request) {
	if _, ok := getPreAuthUser(r, h.cfg.SessionSecret); !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	render(w, "login_totp", map[string]any{"Error": ""})
}

func (h *handlers) totpVerify(w http.ResponseWriter, r *http.Request) {
	username, ok := getPreAuthUser(r, h.cfg.SessionSecret)
	if !ok {
		http.Redirect(w, r, "/login", http.StatusFound)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	secret, _, err := h.cdb.GetUserTOTP(username)
	if err != nil || !verifyTOTP(secret, code) {
		w.WriteHeader(http.StatusUnauthorized)
		render(w, "login_totp", map[string]any{"Error": "인증 코드가 올바르지 않습니다."})
		return
	}
	user, _ := h.cdb.GetUser(username)
	clearPreAuthCookie(w)
	setAuthCookie(w, h.cfg.SessionSecret, username, user.Role)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// ── 프로필 / 2FA 설정 ─────────────────────────────────────────────────────────

type ProfileVM struct {
	ActivePage  string
	CurrentUser SessionUser
	AgentMode   string
	TOTPEnabled bool
	SetupSecret string
	OtpURI      string
	Success     string
	Error       string
}

func (h *handlers) profilePage(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	_, enabled, _ := h.cdb.GetUserTOTP(u.Username)
	render(w, "profile", ProfileVM{
		ActivePage:  "",
		CurrentUser: u,
		AgentMode:   agentMode(),
		TOTPEnabled: enabled,
		Success:     r.URL.Query().Get("success"),
	})
}

func (h *handlers) totpSetupBegin(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	secret := generateTOTPSecret()
	if err := h.cdb.SetTOTPSecret(u.Username, secret); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	render(w, "profile", ProfileVM{
		ActivePage:  "",
		CurrentUser: u,
		AgentMode:   agentMode(),
		TOTPEnabled: false,
		SetupSecret: secret,
		OtpURI:      totpAuthURI(secret, u.Username),
	})
}

func (h *handlers) totpSetupConfirm(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	secret := r.FormValue("secret")
	code := strings.TrimSpace(r.FormValue("code"))
	if !verifyTOTP(secret, code) {
		render(w, "profile", ProfileVM{
			ActivePage:  "",
			CurrentUser: u,
			AgentMode:   agentMode(),
			TOTPEnabled: false,
			SetupSecret: secret,
			OtpURI:      totpAuthURI(secret, u.Username),
			Error:       "인증 코드가 올바르지 않습니다. 다시 시도하세요.",
		})
		return
	}
	if err := h.cdb.SetTOTPSecret(u.Username, secret); err != nil {
		http.Error(w, "db error", http.StatusInternalServerError)
		return
	}
	h.cdb.EnableTOTP(u.Username)
	http.Redirect(w, r, "/profile?success=2FA가+활성화되었습니다.", http.StatusSeeOther)
}

func (h *handlers) totpDisable(w http.ResponseWriter, r *http.Request) {
	u := userFrom(r)
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return
	}
	code := strings.TrimSpace(r.FormValue("code"))
	secret, enabled, _ := h.cdb.GetUserTOTP(u.Username)
	if !enabled || !verifyTOTP(secret, code) {
		render(w, "profile", ProfileVM{
			ActivePage:  "",
			CurrentUser: u,
			AgentMode:   agentMode(),
			TOTPEnabled: true,
			Error:       "인증 코드가 올바르지 않습니다.",
		})
		return
	}
	h.cdb.DisableTOTP(u.Username)
	http.Redirect(w, r, "/profile?success=2FA가+비활성화되었습니다.", http.StatusSeeOther)
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

type ImageDetailVM struct {
	ID           string
	ShortID      string
	Digest       string
	Tags         []string
	RepoDigests  []string
	Created      string
	Author       string
	Architecture string
	Os           string
	Size         string
	VirtualSize  string
	Comment      string
	NamesHistory []string

	Env          []string
	Cmd          string
	Entrypoint   string
	ExposedPorts []string
	Labels       map[string]string
	WorkingDir   string
	User         string
	Volumes      []string
	Layers       []string
	LayerCount   int

	Containers []string // 이 이미지를 사용 중인 컨테이너 이름 목록
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

func (h *handlers) filesystemInfo(w http.ResponseWriter, r *http.Request) {
	pools, err := listZFSPools()
	if err != nil {
		log.Printf("zfs error: %v", err)
		renderErr(w, err)
		return
	}
	datasets, err := listZFSDatasets()
	if err != nil {
		log.Printf("zfs error: %v", err)
		renderErr(w, err)
		return
	}
	render(w, "filesystem", map[string]any{
		"ActivePage":  "filesystem",
		"CurrentUser": userFrom(r),
		"Pools":       pools,
		"Datasets":    datasets,
	})
}

func (h *handlers) storageHealth(w http.ResponseWriter, r *http.Request) {
	vm := StorageVM{ActivePage: "storage", CurrentUser: userFrom(r), AgentMode: agentMode()}
	if pools, err := poolStatus(); err != nil {
		log.Printf("storage: zpool status error: %v", err)
		vm.PoolError = err.Error()
	} else {
		vm.Pools = pools
	}
	if disks, err := smartSummary(); err != nil {
		log.Printf("storage: smart error: %v", err)
		vm.DiskError = err.Error()
	} else {
		vm.Disks = disks
	}
	render(w, "storage", vm)
}

func (h *handlers) filesystemCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Parent string `json:"parent"`
		Name   string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	parent := strings.TrimSpace(req.Parent)
	name := strings.TrimSpace(req.Name)
	if parent == "" || name == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "상위 데이터셋과 이름을 모두 입력하세요."})
		return
	}
	// ZFS 데이터셋 이름: 영문자·숫자·'-'·'_'·'.'·':'만 허용
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == ':') {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "데이터셋 이름에 사용할 수 없는 문자가 포함되어 있습니다. (허용: 영문·숫자·-·_·.·:)"})
			return
		}
	}
	if err := createZFSDataset(parent + "/" + name); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
}

func (h *handlers) filesystemDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset string `json:"dataset"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "잘못된 요청입니다."})
		return
	}
	name := strings.TrimSpace(req.Dataset)
	if name == "" || !strings.Contains(name, "/") {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "최상위 풀은 삭제할 수 없습니다."})
		return
	}
	if err := destroyZFSDataset(name); err != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"success": true})
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

func (h *handlers) imageDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	detail, err := h.pc.InspectImage(id)
	if err != nil {
		renderErr(w, err)
		return
	}
	containers, _ := h.pc.ListContainers()
	render(w, "image-detail", map[string]any{
		"ActivePage": "images",
		"I":          toImageDetailVM(detail, containers),
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

func toImageDetailVM(d *APIImageDetail, containers []APIContainer) ImageDetailVM {
	id := strings.TrimPrefix(d.ID, "sha256:")

	tags := make([]string, 0, len(d.RepoTags))
	for _, t := range d.RepoTags {
		if t != "<none>:<none>" {
			tags = append(tags, t)
		}
	}

	ports := make([]string, 0, len(d.Config.ExposedPorts))
	for p := range d.Config.ExposedPorts {
		ports = append(ports, p)
	}
	sort.Strings(ports)

	volumes := make([]string, 0, len(d.Config.Volumes))
	for v := range d.Config.Volumes {
		volumes = append(volumes, v)
	}
	sort.Strings(volumes)

	// 이미지를 사용 중인 컨테이너
	var usingContainers []string
	for _, c := range containers {
		cImgID := strings.TrimPrefix(c.ImageID, "sha256:")
		if strings.HasPrefix(cImgID, id) || strings.HasPrefix(id, cImgID) {
			name := c.ID
			if len(c.Names) > 0 {
				name = strings.TrimPrefix(c.Names[0], "/")
			}
			usingContainers = append(usingContainers, name)
		}
	}

	created := d.Created
	if t, err := time.Parse(time.RFC3339Nano, d.Created); err == nil {
		created = t.Format("2006-01-02 15:04:05")
	}

	digest := d.Digest
	if digest == "" && len(d.RepoDigests) > 0 {
		parts := strings.SplitN(d.RepoDigests[0], "@", 2)
		if len(parts) == 2 {
			digest = parts[1]
		}
	}

	return ImageDetailVM{
		ID:           id,
		ShortID:      shortID(id),
		Digest:       digest,
		Tags:         tags,
		RepoDigests:  d.RepoDigests,
		Created:      created,
		Author:       d.Author,
		Architecture: d.Architecture,
		Os:           d.Os,
		Size:         fmtBytes(d.Size),
		VirtualSize:  fmtBytes(d.VirtualSize),
		Comment:      d.Comment,
		NamesHistory: d.NamesHistory,
		Env:          d.Config.Env,
		Cmd:          strings.Join(d.Config.Cmd, " "),
		Entrypoint:   strings.Join(d.Config.Entrypoint, " "),
		ExposedPorts: ports,
		Labels:       d.Config.Labels,
		WorkingDir:   d.Config.WorkingDir,
		User:         d.Config.User,
		Volumes:      volumes,
		Layers:       d.RootFS.Layers,
		LayerCount:   len(d.RootFS.Layers),
		Containers:   usingContainers,
	}
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

// ── Metrics ───────────────────────────────────────────────────────────────────

func (h *handlers) metricsPage(w http.ResponseWriter, r *http.Request) {
	render(w, "metrics", map[string]any{"ActivePage": "metrics"})
}

// ── Container Create ──────────────────────────────────────────────────────────

func (h *handlers) containerNewPage(w http.ResponseWriter, r *http.Request) {
	render(w, "container-new", map[string]any{"ActivePage": "containers"})
}

// containerBuild handles POST /api/containers/build.
// It streams Podman build log lines as NDJSON to the client.
func (h *handlers) containerBuild(w http.ResponseWriter, r *http.Request) {
	tag := strings.TrimSpace(r.FormValue("tag"))
	dockerfile := r.FormValue("dockerfile")
	if strings.TrimSpace(dockerfile) == "" {
		http.Error(w, "Dockerfile 내용이 없습니다", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	emit := func(ltype, line string) {
		enc.Encode(map[string]string{"type": ltype, "line": line})
		if flusher != nil {
			flusher.Flush()
		}
	}

	if err := h.pc.BuildImage(tag, dockerfile, emit); err != nil {
		emit("error", err.Error())
		emit("done", "1")
		return
	}
	emit("done", "0")
}

// composeUp handles POST /api/compose/up.
// It saves the compose file to a temp dir, runs podman-compose up -d via hostd
// (or directly), and streams log lines as NDJSON.
func (h *handlers) composeUp(w http.ResponseWriter, r *http.Request) {
	content := r.FormValue("compose")
	if strings.TrimSpace(content) == "" {
		http.Error(w, "docker-compose.yml 내용이 없습니다", http.StatusBadRequest)
		return
	}

	w.Header().Set("Content-Type", "application/x-ndjson")
	w.Header().Set("X-Accel-Buffering", "no")
	flusher, _ := w.(http.Flusher)
	enc := json.NewEncoder(w)
	emit := func(ltype, line string) {
		enc.Encode(map[string]string{"type": ltype, "line": line})
		if flusher != nil {
			flusher.Flush()
		}
	}

	if hc := hostdClient(); hc != nil {
		resp, err := hc.Post("http://hostd/compose-up", "text/plain", strings.NewReader(content))
		if err != nil {
			emit("error", "hostd 연결 실패: "+err.Error())
			emit("done", "1")
			return
		}
		defer resp.Body.Close()
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			w.Write(append(scanner.Bytes(), '\n'))
			if flusher != nil {
				flusher.Flush()
			}
		}
		return
	}

	// direct exec (host mode, no jail)
	dir, err := os.MkdirTemp("", "pfortainer-compose-*")
	if err != nil {
		emit("error", err.Error())
		emit("done", "1")
		return
	}
	defer os.RemoveAll(dir)

	composePath := filepath.Join(dir, "docker-compose.yml")
	if err := os.WriteFile(composePath, []byte(content), 0644); err != nil {
		emit("error", err.Error())
		emit("done", "1")
		return
	}

	streamComposeUp(composePath, emit)
}

// ── Admin: User Management ────────────────────────────────────────────────────

type AdminUsersVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Users       []DBUser
	Error       string
	Success     string
}

func (h *handlers) adminUsers(w http.ResponseWriter, r *http.Request) {
	users, err := h.cdb.ListUsers()
	vm := AdminUsersVM{
		ActivePage:  "admin-users",
		CurrentUser: userFrom(r),
		Users:       users,
	}
	if err != nil {
		vm.Error = err.Error()
	}
	render(w, "admin-users", vm)
}

func (h *handlers) adminUserCreate(w http.ResponseWriter, r *http.Request) {
	username := strings.TrimSpace(r.FormValue("username"))
	password := r.FormValue("password")
	role := r.FormValue("role")
	redirect := func(msg, errMsg string) {
		q := "?success=" + msg
		if errMsg != "" {
			q = "?error=" + errMsg
		}
		http.Redirect(w, r, "/admin/users"+q, http.StatusSeeOther)
	}
	if username == "" || password == "" {
		redirect("", "사용자명과 비밀번호를 입력하세요.")
		return
	}
	if len(password) < 8 {
		redirect("", "비밀번호는 8자 이상이어야 합니다.")
		return
	}
	if err := h.cdb.CreateUser(username, password, role); err != nil {
		redirect("", "생성 실패: "+err.Error())
		return
	}
	redirect(username+" 생성 완료", "")
}

func (h *handlers) adminUserUpdateRole(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	role := r.FormValue("role")
	me := userFrom(r)
	if username == me.Username && role != RoleAdmin {
		http.Redirect(w, r, "/admin/users?error=자신의+관리자+권한을+제거할+수+없습니다", http.StatusSeeOther)
		return
	}
	if err := h.cdb.UpdateRole(username, role); err != nil {
		http.Redirect(w, r, "/admin/users?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users?success=역할+변경+완료", http.StatusSeeOther)
}

func (h *handlers) adminUserUpdatePassword(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	pw := r.FormValue("password")
	if len(pw) < 8 {
		http.Redirect(w, r, "/admin/users?error=비밀번호는+8자+이상이어야+합니다", http.StatusSeeOther)
		return
	}
	if err := h.cdb.UpdatePassword(username, pw); err != nil {
		http.Redirect(w, r, "/admin/users?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users?success=비밀번호+변경+완료", http.StatusSeeOther)
}

func (h *handlers) adminUserDelete(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	me := userFrom(r)
	if username == me.Username {
		http.Redirect(w, r, "/admin/users?error=자신의+계정을+삭제할+수+없습니다", http.StatusSeeOther)
		return
	}
	if err := h.cdb.DeleteUser(username); err != nil {
		http.Redirect(w, r, "/admin/users?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/admin/users?success="+username+"+삭제+완료", http.StatusSeeOther)
}

// ── 로컬 사용자/그룹 관리 ─────────────────────────────────────────────────────

type LocalUsersVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Status      LocalUsersStatus
	AgentMode   string
	Error       string
	Success     string
}

func (h *handlers) localUsersPage(w http.ResponseWriter, r *http.Request) {
	vm := LocalUsersVM{
		ActivePage:  "localusers",
		CurrentUser: userFrom(r),
		AgentMode:   agentMode(),
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	status, err := getLocalUsersStatus()
	if err != nil {
		vm.Error = "사용자 목록 조회 실패: " + err.Error()
	} else {
		vm.Status = status
	}
	render(w, "localusers", vm)
}

func (h *handlers) localUserCreate(w http.ResponseWriter, r *http.Request) {
	username := r.FormValue("username")
	err := createLocalUser(
		username,
		r.FormValue("fullname"),
		r.FormValue("shell"),
		r.FormValue("password"),
		r.FormValue("smb_password"),
	)
	if err != nil {
		http.Redirect(w, r, "/shares?tab=users&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=users&success=사용자+"+username+"+생성됨", http.StatusSeeOther)
}

func (h *handlers) localUserDelete(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if err := deleteLocalUser(username); err != nil {
		http.Redirect(w, r, "/shares?tab=users&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=users&success=사용자+"+username+"+삭제됨", http.StatusSeeOther)
}

func (h *handlers) localUserSMBPasswd(w http.ResponseWriter, r *http.Request) {
	username := r.PathValue("username")
	if err := setLocalUserSMBPasswd(username, r.FormValue("password")); err != nil {
		http.Redirect(w, r, "/shares?tab=users&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=users&success="+username+"+SMB+비밀번호+설정됨", http.StatusSeeOther)
}

func (h *handlers) localGroupCreate(w http.ResponseWriter, r *http.Request) {
	name := r.FormValue("name")
	if err := createLocalGroup(name); err != nil {
		http.Redirect(w, r, "/shares?tab=users&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=users&success=그룹+"+name+"+생성됨", http.StatusSeeOther)
}

func (h *handlers) localGroupDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := deleteLocalGroup(name); err != nil {
		http.Redirect(w, r, "/shares?tab=users&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=users&success=그룹+"+name+"+삭제됨", http.StatusSeeOther)
}

func (h *handlers) localGroupMember(w http.ResponseWriter, r *http.Request) {
	group := r.PathValue("name")
	username := r.FormValue("username")
	action := r.FormValue("action")
	if err := updateGroupMember(group, username, action); err != nil {
		http.Redirect(w, r, "/shares?tab=users&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=users&success=그룹+멤버+변경됨", http.StatusSeeOther)
}

// ── SMB 공유 관리 ─────────────────────────────────────────────────────────────

type SharesVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Tab         string // "smb" | "nfs" | "users"
	SMB         SMBStatus
	NFS         NFSStatus
	LocalUsers  LocalUsersStatus
	LocalError  string
	AgentMode   string
	Error       string
	Success     string
}

func (h *handlers) sharesPage(w http.ResponseWriter, r *http.Request) {
	tab := r.URL.Query().Get("tab")
	if tab != "nfs" && tab != "users" {
		tab = "smb"
	}
	vm := SharesVM{
		ActivePage:  "shares",
		CurrentUser: userFrom(r),
		Tab:         tab,
		AgentMode:   agentMode(),
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	switch tab {
	case "smb":
		if s, err := getSMBStatus(); err != nil {
			vm.Error = "SMB 상태 조회 실패: " + err.Error()
		} else {
			vm.SMB = s
		}
	case "nfs":
		if s, err := getNFSStatus(); err != nil {
			vm.Error = "NFS 상태 조회 실패: " + err.Error()
		} else {
			vm.NFS = s
		}
	case "users":
		if s, err := getLocalUsersStatus(); err != nil {
			vm.LocalError = "사용자 목록 조회 실패: " + err.Error()
		} else {
			vm.LocalUsers = s
		}
	}
	render(w, "shares", vm)
}

func (h *handlers) nfsCreate(w http.ResponseWriter, r *http.Request) {
	path := r.FormValue("path")
	name := r.FormValue("name")
	clients := r.FormValue("clients")
	readOnly := r.FormValue("read_only") == "1"
	mapRoot := r.FormValue("maproot")

	// Build the export line
	var line strings.Builder
	line.WriteString(path)
	if readOnly {
		line.WriteString("\t-ro")
	}
	if mapRoot != "" {
		line.WriteString("\t-maproot=" + mapRoot)
	}
	if clients != "" {
		for _, c := range strings.Fields(clients) {
			line.WriteString("\t" + c)
		}
	}

	e := NFSExport{Name: name, Path: path, Line: line.String(), Clients: clients}
	if err := createNFSExport(e); err != nil {
		http.Redirect(w, r, "/shares?tab=nfs&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=nfs&success=NFS+export+"+name+"+저장됨", http.StatusSeeOther)
}

func (h *handlers) nfsDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := deleteNFSExport(name); err != nil {
		http.Redirect(w, r, "/shares?tab=nfs&error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?tab=nfs&success=NFS+export+"+name+"+삭제됨", http.StatusSeeOther)
}

func (h *handlers) nfsReload(w http.ResponseWriter, r *http.Request) {
	out, err := reloadNFS()
	if err != nil {
		http.Redirect(w, r, "/shares?tab=nfs&error=reload+실패:+"+err.Error(), http.StatusSeeOther)
		return
	}
	msg := "mountd reload 완료"
	if out != "" {
		msg = out
	}
	http.Redirect(w, r, "/shares?tab=nfs&success="+msg, http.StatusSeeOther)
}

func (h *handlers) shareCreate(w http.ResponseWriter, r *http.Request) {
	boolField := func(name string) bool { return r.FormValue(name) == "1" }
	share := SMBShare{
		Name:       r.FormValue("name"),
		Path:       r.FormValue("path"),
		Comment:    r.FormValue("comment"),
		ValidUsers: r.FormValue("valid_users"),
		ReadOnly:   boolField("read_only"),
		Browseable: !boolField("no_browse"),
		GuestOK:    boolField("guest_ok"),
	}
	if share.Name == "" || share.Path == "" {
		http.Redirect(w, r, "/shares?error=이름과+경로는+필수입니다", http.StatusSeeOther)
		return
	}
	if err := createSMBShare(share); err != nil {
		http.Redirect(w, r, "/shares?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?success=공유+"+share.Name+"+저장됨", http.StatusSeeOther)
}

func (h *handlers) shareDelete(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := deleteSMBShare(name); err != nil {
		http.Redirect(w, r, "/shares?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?success=공유+"+name+"+삭제됨", http.StatusSeeOther)
}

func (h *handlers) shareReload(w http.ResponseWriter, r *http.Request) {
	out, err := reloadSamba()
	if err != nil {
		http.Redirect(w, r, "/shares?error=reload+실패:+"+err.Error(), http.StatusSeeOther)
		return
	}
	msg := "Samba reload 완료"
	if out != "" {
		msg = out
	}
	http.Redirect(w, r, "/shares?success="+msg, http.StatusSeeOther)
}

func (h *handlers) shareSetup(w http.ResponseWriter, r *http.Request) {
	msg, err := setupSamba()
	if err != nil {
		http.Redirect(w, r, "/shares?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/shares?success="+msg, http.StatusSeeOther)
}

// ── SMART 테스트 ───────────────────────────────────────────────────────────────

func (h *handlers) smartTestRun(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Device   string `json:"device"`
		TestType string `json:"test_type"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	output, err := startSmartTest(req.Device, req.TestType)
	if err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"output": output})
}

// ── 알림 설정 ─────────────────────────────────────────────────────────────────

type AlertsVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Settings    AlertSettings
	Error       string
	Success     string
}

func (h *handlers) alertsPage(w http.ResponseWriter, r *http.Request) {
	vm := AlertsVM{
		ActivePage:  "alerts",
		CurrentUser: userFrom(r),
		Settings:    loadAlertSettings(h.cdb),
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	render(w, "alerts", vm)
}

func (h *handlers) alertsSave(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/alerts?error="+err.Error(), http.StatusSeeOther)
		return
	}
	s := AlertSettings{
		EmailEnabled:    r.FormValue("email_enabled") == "1",
		SMTPHost:        strings.TrimSpace(r.FormValue("smtp_host")),
		SMTPPort:        587,
		SMTPUser:        strings.TrimSpace(r.FormValue("smtp_user")),
		SMTPPass:        r.FormValue("smtp_pass"),
		SMTPFrom:        strings.TrimSpace(r.FormValue("smtp_from")),
		SMTPTo:          strings.TrimSpace(r.FormValue("smtp_to")),
		WebhookEnabled:  r.FormValue("webhook_enabled") == "1",
		WebhookURL:      strings.TrimSpace(r.FormValue("webhook_url")),
		CheckPoolHealth: r.FormValue("check_pool_health") == "1",
		CheckSMART:      r.FormValue("check_smart") == "1",
		CheckCapacity:   r.FormValue("check_capacity") == "1",
		CheckScrub:      r.FormValue("check_scrub") == "1",
		CapacityPct:     85,
		CooldownHours:   4,
	}
	fmt.Sscanf(r.FormValue("smtp_port"), "%d", &s.SMTPPort)
	fmt.Sscanf(r.FormValue("capacity_pct"), "%d", &s.CapacityPct)
	fmt.Sscanf(r.FormValue("cooldown_hours"), "%d", &s.CooldownHours)

	// Keep existing password if field is blank (don't overwrite with empty)
	if s.SMTPPass == "" {
		existing := loadAlertSettings(h.cdb)
		s.SMTPPass = existing.SMTPPass
	}
	if err := saveAlertSettings(h.cdb, s); err != nil {
		http.Redirect(w, r, "/alerts?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/alerts?success=설정+저장+완료", http.StatusSeeOther)
}

func (h *handlers) alertTest(w http.ResponseWriter, r *http.Request) {
	s := loadAlertSettings(h.cdb)
	var errs []string
	if s.EmailEnabled {
		subject := "[pfortainer] 테스트 알림"
		body := "pfortainer 알림 테스트입니다.\n\n발송 시각: " + time.Now().Format("2006-01-02 15:04:05")
		if err := sendAlertEmail(s, subject, body); err != nil {
			errs = append(errs, "이메일: "+err.Error())
		}
	}
	if s.WebhookEnabled && s.WebhookURL != "" {
		if err := sendWebhook(s.WebhookURL, "test", "pfortainer", "테스트 알림"); err != nil {
			errs = append(errs, "웹훅: "+err.Error())
		}
	}
	if len(errs) > 0 {
		http.Redirect(w, r, "/alerts?error="+strings.Join(errs, " / "), http.StatusSeeOther)
		return
	}
	if !s.EmailEnabled && (!s.WebhookEnabled || s.WebhookURL == "") {
		http.Redirect(w, r, "/alerts?error=활성화된+채널이+없습니다", http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/alerts?success=테스트+알림+발송+완료", http.StatusSeeOther)
}

// ── ZFS 스냅샷 ────────────────────────────────────────────────────────────────

type SnapGroup struct {
	Dataset   string
	Snapshots []Snapshot
}

type SnapshotsVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Groups      []SnapGroup
	Datasets    []string // filesystem dataset names for create form
	Schedules   []DBSchedule
	SchedPools  []ZFSPool
	SchedDisks  []string
	Error       string
	Success     string
}

func (h *handlers) snapshots(w http.ResponseWriter, r *http.Request) {
	groups, groupOrder, err := listSnapshotsByDataset("")
	if err != nil {
		log.Printf("snapshots: list: %v", err)
	}

	datasets, _ := listZFSDatasets()
	var dsNames []string
	for _, d := range datasets {
		if d.Type == "filesystem" && !strings.Contains(d.Name, "/containers/") {
			dsNames = append(dsNames, d.Name)
		}
	}

	var sg []SnapGroup
	for _, ds := range groupOrder {
		// Podman 컨테이너 레이어(/containers/) 스냅샷 제외
		if strings.Contains(ds, "/containers/") {
			continue
		}
		snaps := groups[ds]
		// newest first
		for i, j := 0, len(snaps)-1; i < j; i, j = i+1, j-1 {
			snaps[i], snaps[j] = snaps[j], snaps[i]
		}
		sg = append(sg, SnapGroup{Dataset: ds, Snapshots: snaps})
	}

	scheds, _ := h.cdb.ListSchedules()
	schedPools, _ := listZFSPools()
	schedDisks, _ := listSmartDevices()

	vm := SnapshotsVM{
		ActivePage:  "snapshots",
		CurrentUser: userFrom(r),
		Groups:      sg,
		Datasets:    dsNames,
		Schedules:   scheds,
		SchedPools:  schedPools,
		SchedDisks:  schedDisks,
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	render(w, "snapshots", vm)
}

func (h *handlers) snapshotCreate(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Dataset  string `json:"dataset"`
		SnapName string `json:"snapname"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Dataset == "" || req.SnapName == "" {
		jsonErr(w, "dataset and snapname required", http.StatusBadRequest)
		return
	}
	if err := createSnapshot(req.Dataset, req.SnapName); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w)
}

func (h *handlers) snapshotDelete(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"` // dataset@snapname
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := destroySnapshot(req.Name); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w)
}

func (h *handlers) snapshotRollback(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name string `json:"name"` // dataset@snapname
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := rollbackSnapshot(req.Name); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w)
}

func (h *handlers) snapshotClone(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Name   string `json:"name"`   // source: dataset@snapname
		Target string `json:"target"` // new dataset path
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Name == "" || req.Target == "" {
		jsonErr(w, "name and target required", http.StatusBadRequest)
		return
	}
	if err := cloneSnapshot(req.Name, req.Target); err != nil {
		jsonErr(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonOK(w)
}

// ── 스케줄러 ──────────────────────────────────────────────────────────────────

type SchedulesVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Schedules   []DBSchedule
	Datasets    []string // for create form
	Pools       []ZFSPool
	Disks       []DiskSMART
	Error       string
	Success     string
}

func (h *handlers) schedulesPage(w http.ResponseWriter, r *http.Request) {
	scheds, _ := h.cdb.ListSchedules()
	datasets, _ := listZFSDatasets()
	pools, _ := listZFSPools()
	disks, _ := smartSummary()

	var dsNames []string
	for _, d := range datasets {
		if d.Type == "filesystem" {
			dsNames = append(dsNames, d.Name)
		}
	}

	vm := SchedulesVM{
		ActivePage:  "schedules",
		CurrentUser: userFrom(r),
		Schedules:   scheds,
		Datasets:    dsNames,
		Pools:       pools,
		Disks:       disks,
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	render(w, "schedules", vm)
}

func (h *handlers) scheduleCreate(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Redirect(w, r, "/snapshots?error="+err.Error(), http.StatusSeeOther)
		return
	}
	schedType := r.FormValue("type")
	target := r.FormValue("target")
	frequency := r.FormValue("frequency")
	prefix := strings.TrimSpace(r.FormValue("prefix"))
	if prefix == "" {
		prefix = "auto"
	}
	retention := 7
	fmt.Sscanf(r.FormValue("retention"), "%d", &retention)

	if schedType == "" || target == "" || frequency == "" {
		http.Redirect(w, r, "/snapshots?error=필수+항목+누락", http.StatusSeeOther)
		return
	}

	now := time.Now()
	next := nextRunTime(frequency, now)
	_, err := h.cdb.CreateSchedule(DBSchedule{
		Type: schedType, Target: target, Frequency: frequency,
		Retention: retention, Prefix: prefix, Enabled: true,
		NextRun: &next,
	})
	if err != nil {
		http.Redirect(w, r, "/snapshots?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/snapshots?success=스케줄+추가+완료", http.StatusSeeOther)
}

func (h *handlers) scheduleToggle(w http.ResponseWriter, r *http.Request) {
	var id int64
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	sched, err := h.cdb.GetSchedule(id)
	if err != nil {
		http.Redirect(w, r, "/snapshots?error="+err.Error(), http.StatusSeeOther)
		return
	}
	if err := h.cdb.ToggleSchedule(id, !sched.Enabled); err != nil {
		http.Redirect(w, r, "/snapshots?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/snapshots", http.StatusSeeOther)
}

func (h *handlers) scheduleDelete(w http.ResponseWriter, r *http.Request) {
	var id int64
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	if err := h.cdb.DeleteSchedule(id); err != nil {
		http.Redirect(w, r, "/snapshots?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/snapshots?success=삭제+완료", http.StatusSeeOther)
}

func (h *handlers) scheduleRunNow(w http.ResponseWriter, r *http.Request) {
	var id int64
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	if err := h.sched.RunNow(id); err != nil {
		http.Redirect(w, r, "/snapshots?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/snapshots?success=즉시+실행+완료", http.StatusSeeOther)
}

// ── 네트워크 ──────────────────────────────────────────────────────────────────

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

type NetStatusVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Interfaces  []NetworkIface
	Routes      string
	DNS         []string
	AgentMode   string
	Error       string
}

func (h *handlers) networkPage(w http.ResponseWriter, r *http.Request) {
	vm := NetStatusVM{
		ActivePage:  "network",
		CurrentUser: userFrom(r),
		AgentMode:   agentMode(),
	}
	b, err := hostGetAgent("/network/status")
	if err != nil {
		vm.Error = "네트워크 상태 조회 실패: " + err.Error()
	} else {
		var raw struct {
			Interfaces []NetworkIface `json:"interfaces"`
			Routes     string         `json:"routes"`
			DNS        []string       `json:"dns"`
		}
		if err := json.Unmarshal(b, &raw); err != nil {
			vm.Error = "파싱 실패: " + err.Error()
		} else {
			vm.Interfaces = raw.Interfaces
			vm.Routes = raw.Routes
			vm.DNS = raw.DNS
		}
	}
	render(w, "network", vm)
}

// ── 앱 카탈로그 ───────────────────────────────────────────────────────────────

type CatalogVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Apps        []CatalogApp
	Installed   map[string]string // appID → container state ("running"|"exited"|...)
	AgentMode   string
	Error       string
	Success     string
}

func (h *handlers) catalogPage(w http.ResponseWriter, r *http.Request) {
	vm := CatalogVM{
		ActivePage:  "catalog",
		CurrentUser: userFrom(r),
		AgentMode:   agentMode(),
		Apps:        CatalogApps,
		Installed:   map[string]string{},
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	// pf-* 컨테이너 목록으로 설치 상태 확인
	if cs, err := h.pc.ListContainers(); err == nil {
		for _, c := range cs {
			for _, name := range c.Names {
				if strings.HasPrefix(name, "pf-") {
					id := strings.TrimPrefix(name, "pf-")
					vm.Installed[id] = c.State
				}
			}
		}
	}
	render(w, "catalog", vm)
}

func (h *handlers) catalogInstall(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("id")
	var app *CatalogApp
	for i := range CatalogApps {
		if CatalogApps[i].ID == appID {
			app = &CatalogApps[i]
			break
		}
	}
	if app == nil {
		http.Redirect(w, r, "/catalog?error=앱+없음", http.StatusSeeOther)
		return
	}

	// 폼 값으로 {{field}} 대체
	replace := func(s string) string {
		for _, f := range app.Fields {
			s = strings.ReplaceAll(s, "{{"+f.Name+"}}", r.FormValue(f.Name))
		}
		return s
	}

	var ports []string
	for _, p := range app.Ports {
		ports = append(ports, replace(p))
	}
	var volumes []string
	for _, v := range app.Volumes {
		volumes = append(volumes, replace(v))
	}
	env := map[string]string{}
	for k, v := range app.Env {
		env[k] = replace(v)
	}

	body, _ := json.Marshal(map[string]interface{}{
		"name":    app.ContainerName(),
		"image":   app.Image,
		"ports":   ports,
		"volumes": volumes,
		"env":     env,
		"cmd":     app.Cmd,
	})
	if _, err := hostPost("/catalog/run", body); err != nil {
		http.Redirect(w, r, "/catalog?error=설치+실패:+"+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/catalog?success="+app.Name+"+설치+완료", http.StatusSeeOther)
}

func (h *handlers) catalogRemove(w http.ResponseWriter, r *http.Request) {
	appID := r.PathValue("id")
	var app *CatalogApp
	for i := range CatalogApps {
		if CatalogApps[i].ID == appID {
			app = &CatalogApps[i]
			break
		}
	}
	if app == nil {
		http.Redirect(w, r, "/catalog?error=앱+없음", http.StatusSeeOther)
		return
	}
	body, _ := json.Marshal(map[string]string{"name": app.ContainerName()})
	if _, err := hostPost("/catalog/remove", body); err != nil {
		http.Redirect(w, r, "/catalog?error=제거+실패:+"+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/catalog?success="+app.Name+"+제거됨", http.StatusSeeOther)
}

// ── 진단/로그 ─────────────────────────────────────────────────────────────────

func (h *handlers) diagnosticsPage(w http.ResponseWriter, r *http.Request) {
	render(w, "diagnostics", map[string]any{
		"ActivePage":  "diagnostics",
		"CurrentUser": userFrom(r),
		"AgentMode":   agentMode(),
	})
}

// diagLocalLog — Jail 내부 로그 직접 읽기 (pfortainer.log)
func (h *handlers) diagLocalLog(w http.ResponseWriter, r *http.Request) {
	allowed := map[string]string{
		"pfortainer": "/var/log/pfortainer.log",
	}
	file := r.URL.Query().Get("file")
	lines := r.URL.Query().Get("lines")
	if lines == "" {
		lines = "200"
	}
	path, ok := allowed[file]
	if !ok {
		http.Error(w, "unknown log", http.StatusBadRequest)
		return
	}
	out, err := os.ReadFile(path)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	// tail: last N lines
	linesAll := strings.Split(string(out), "\n")
	var n int
	fmt.Sscanf(lines, "%d", &n)
	if n <= 0 {
		n = 200
	}
	if len(linesAll) > n {
		linesAll = linesAll[len(linesAll)-n:]
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte(strings.Join(linesAll, "\n")))
}

// diagHostLog — host agent 경유 로그
func (h *handlers) diagHostLog(w http.ResponseWriter, r *http.Request) {
	file := r.URL.Query().Get("file")
	lines := r.URL.Query().Get("lines")
	if lines == "" {
		lines = "200"
	}
	b, err := hostGetAgent("/diag/log?file=" + file + "&lines=" + lines)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}

// diagCmd — host agent 경유 진단 명령
func (h *handlers) diagCmd(w http.ResponseWriter, r *http.Request) {
	cmd := r.URL.Query().Get("cmd")
	b, err := hostGetAgent("/diag/cmd?cmd=" + cmd)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write(b)
}

// ── ZFS 복제 ──────────────────────────────────────────────────────────────────

type ReplicationVM struct {
	ActivePage  string
	CurrentUser SessionUser
	Tasks       []DBReplTask
	AgentMode   string
	Error       string
	Success     string
}

func (h *handlers) replicationPage(w http.ResponseWriter, r *http.Request) {
	vm := ReplicationVM{
		ActivePage:  "replication",
		CurrentUser: userFrom(r),
		AgentMode:   agentMode(),
		Error:       r.URL.Query().Get("error"),
		Success:     r.URL.Query().Get("success"),
	}
	tasks, err := h.cdb.ListReplTasks()
	if err != nil {
		vm.Error = "복제 태스크 조회 실패: " + err.Error()
	} else {
		vm.Tasks = tasks
	}
	render(w, "replications", vm)
}

func (h *handlers) replicationCreate(w http.ResponseWriter, r *http.Request) {
	t := DBReplTask{
		Name:          r.FormValue("name"),
		SourceDataset: r.FormValue("source_dataset"),
		TargetPath:    r.FormValue("target_path"),
		Recursive:     r.FormValue("recursive") == "1",
		Schedule:      r.FormValue("schedule"),
		Enabled:       true,
	}
	if t.Name == "" || t.SourceDataset == "" || t.TargetPath == "" {
		http.Redirect(w, r, "/replications?error=이름+소스+대상은+필수입니다", http.StatusSeeOther)
		return
	}
	if t.Schedule == "" {
		t.Schedule = "manual"
	}
	if _, err := h.cdb.CreateReplTask(t); err != nil {
		http.Redirect(w, r, "/replications?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/replications?success=복제+태스크+추가됨", http.StatusSeeOther)
}

func (h *handlers) replicationDelete(w http.ResponseWriter, r *http.Request) {
	var id int64
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	if err := h.cdb.DeleteReplTask(id); err != nil {
		http.Redirect(w, r, "/replications?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/replications?success=삭제됨", http.StatusSeeOther)
}

func (h *handlers) replicationToggle(w http.ResponseWriter, r *http.Request) {
	var id int64
	fmt.Sscanf(r.PathValue("id"), "%d", &id)
	enabled := r.FormValue("enabled") == "1"
	if err := h.cdb.ToggleReplTask(id, enabled); err != nil {
		http.Redirect(w, r, "/replications?error="+err.Error(), http.StatusSeeOther)
		return
	}
	http.Redirect(w, r, "/replications", http.StatusSeeOther)
}

func (h *handlers) replicationRun(w http.ResponseWriter, r *http.Request) {
	var id int64
	fmt.Sscanf(r.PathValue("id"), "%d", &id)

	tasks, err := h.cdb.ListReplTasks()
	if err != nil {
		http.Redirect(w, r, "/replications?error="+err.Error(), http.StatusSeeOther)
		return
	}
	var task *DBReplTask
	for i := range tasks {
		if tasks[i].ID == id {
			task = &tasks[i]
			break
		}
	}
	if task == nil {
		http.Redirect(w, r, "/replications?error=태스크+없음", http.StatusSeeOther)
		return
	}

	result, runErr := runReplication(task.SourceDataset, task.TargetPath, task.LastSnapshot, task.Recursive)
	if runErr != nil {
		h.cdb.UpdateReplTaskResult(id, task.LastSnapshot, "error", runErr.Error())
		http.Redirect(w, r, "/replications?error=복제+실패:+"+runErr.Error(), http.StatusSeeOther)
		return
	}
	h.cdb.UpdateReplTaskResult(id, result.CurrentSnapshot, "ok", "")
	http.Redirect(w, r, "/replications?success=복제+완료+→+"+result.CurrentSnapshot, http.StatusSeeOther)
}

// jsonOK / jsonErr helpers (used by snapshot API handlers)
func jsonOK(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.Write([]byte(`{"success":true}`))
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	b, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(b)
}
