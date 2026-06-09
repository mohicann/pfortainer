package main

import (
	"crypto/subtle"
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
	Name    string
	State   string
	Status  string
	Image   string
	Created string
	Ports   []string
	ShortID string
}

type ImageVM struct {
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

// ── Converters ────────────────────────────────────────────────────────────────

func toContainerVMs(cs []APIContainer) []ContainerVM {
	out := make([]ContainerVM, 0, len(cs))
	for _, c := range cs {
		name := "unnamed"
		if len(c.Names) > 0 {
			name = strings.TrimPrefix(c.Names[0], "/")
		}
		out = append(out, ContainerVM{
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
