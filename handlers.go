package main

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/http"
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

type ImageVM struct {
	ID      string
	Tags    []string
	ShortID string
	Size    string
	Created string
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
			Status:  c.Status,
			Image:   c.Image,
			Created: fmtTS(c.Created),
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
		var label string
		if p.PublicPort > 0 {
			label = fmt.Sprintf("%d:%d/%s", p.PublicPort, p.PrivatePort, p.Type)
		} else {
			label = fmt.Sprintf("%d/%s", p.PrivatePort, p.Type)
		}
		if !seen[label] {
			seen[label] = true
			out = append(out, label)
		}
	}
	return out
}
