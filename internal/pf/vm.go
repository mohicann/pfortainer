package pf

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

type ContainerVm struct {
	ID      string   `json:"id"`
	Name    string   `json:"name"`
	State   string   `json:"state"`
	Status  string   `json:"status"`
	Image   string   `json:"image"`
	Created string   `json:"created"`
	Ports   []string `json:"ports"`
	ShortID string   `json:"short_id"`
}

type NetworkVm struct {
	Name       string `json:"name"`
	IPAddress  string `json:"ip_address"`
	Gateway    string `json:"gateway"`
	MacAddress string `json:"mac_address"`
}

type MountVm struct {
	Kind        string `json:"kind"`
	Source      string `json:"source"`
	Destination string `json:"destination"`
	Mode        string `json:"mode"`
	RW          bool   `json:"rw"`
}

type ContainerDetailVm struct {
	ID            string            `json:"id"`
	ShortID       string            `json:"short_id"`
	Name          string            `json:"name"`
	Image         string            `json:"image"`
	Command       string            `json:"command"`
	Created       string            `json:"created"`
	State         string            `json:"state"`
	Status        string            `json:"status"`
	Running       bool              `json:"running"`
	Pid           int32             `json:"pid"`
	ExitCode      int32             `json:"exit_code"`
	StartedAt     string            `json:"started_at"`
	FinishedAt    string            `json:"finished_at"`
	RestartCount  int32             `json:"restart_count"`
	RestartPolicy string            `json:"restart_policy"`
	NetworkMode   string            `json:"network_mode"`
	Ports         []string          `json:"ports"`
	Networks      []NetworkVm       `json:"networks"`
	Mounts        []MountVm         `json:"mounts"`
	Env           []string          `json:"env"`
	Labels        map[string]string `json:"labels"`
}

type SystemInfoVm struct {
	Hostname         string `json:"hostname"`
	OS               string `json:"os"`
	Kernel           string `json:"kernel"`
	Arch             string `json:"arch"`
	Uptime           string `json:"uptime"`
	Cpus             int32  `json:"cpus"`
	CpuUser          int32  `json:"cpu_user"`
	CpuSystem        int32  `json:"cpu_system"`
	CpuIdle          int32  `json:"cpu_idle"`
	MemTotal         string `json:"mem_total"`
	MemUsed          string `json:"mem_used"`
	MemFree          string `json:"mem_free"`
	MemUsedPct       int32  `json:"mem_used_pct"`
	HasSwap          bool   `json:"has_swap"`
	SwapTotal        string `json:"swap_total"`
	SwapUsed         string `json:"swap_used"`
	SwapFree         string `json:"swap_free"`
	SwapUsedPct      int32  `json:"swap_used_pct"`
	PodmanVersion    string `json:"podman_version"`
	PodmanAPI        string `json:"podman_api"`
	GoVersion        string `json:"go_version"`
	OsArch           string `json:"os_arch"`
	ContainerTotal   int32  `json:"container_total"`
	ContainerRunning int32  `json:"container_running"`
	ContainerStopped int32  `json:"container_stopped"`
	ContainerPaused  int32  `json:"container_paused"`
	ImageCount       int32  `json:"image_count"`
	GraphDriver      string `json:"graph_driver"`
	GraphRoot        string `json:"graph_root"`
	RunRoot          string `json:"run_root"`
	VolumePath       string `json:"volume_path"`
}

func firstName(names []string) string {
	if len(names) == 0 {
		return "unnamed"
	}
	return strings.TrimLeft(names[0], "/")
}

func toContainerVms(cs []ApiContainer) []ContainerVm {
	out := []ContainerVm{}
	for i := range cs {
		c := &cs[i]
		out = append(out, ContainerVm{ID: c.ID, Name: firstName(c.Names), State: strings.ToLower(c.State), Status: containerStatusText(c),
			Image: c.Image, Created: fmtTsStr(c.Created), Ports: fmtPorts(c.Ports), ShortID: shortID(c.ID)})
	}
	return out
}

func toContainerDetailVm(c *ApiContainerDetail) ContainerDetailVm {
	var parts []string
	if c.Config.Entrypoint != "" {
		parts = append(parts, string(c.Config.Entrypoint))
	}
	parts = append(parts, c.Config.Cmd...)
	ports := []string{}
	for port, bs := range c.NetworkSettings.Ports {
		if bs == nil || len(*bs) == 0 {
			ports = append(ports, port)
			continue
		}
		for _, b := range *bs {
			ports = append(ports, b.HostIP+":"+b.HostPort+"->"+port)
		}
	}
	sort.Strings(ports)
	nets := []NetworkVm{}
	for name, n := range c.NetworkSettings.Networks {
		nets = append(nets, NetworkVm{name, n.IPAddress, n.Gateway, n.MacAddress})
	}
	sort.SliceStable(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	mounts := []MountVm{}
	for _, m := range c.Mounts {
		src := m.Source
		if m.Kind == "volume" && m.Name != "" {
			src = m.Name
		}
		mounts = append(mounts, MountVm{m.Kind, src, m.Destination, m.Mode, m.RW})
	}
	env := c.Config.Env
	if env == nil {
		env = []string{}
	}
	return ContainerDetailVm{ShortID: shortID(c.ID), ID: c.ID, Name: strings.TrimLeft(c.Name, "/"), Image: c.Image, Command: strings.Join(parts, " "),
		Created: fmtTsStr(c.Created), State: strings.ToLower(c.State.Status), Status: c.State.Status, Running: c.State.Running, Pid: c.State.Pid,
		ExitCode: c.State.ExitCode, StartedAt: fmtTsStr(c.State.StartedAt), FinishedAt: fmtTsStr(c.State.FinishedAt), RestartCount: c.RestartCount,
		RestartPolicy: c.HostConfig.RestartPolicy.Name, NetworkMode: c.HostConfig.NetworkMode, Ports: ports, Networks: nets, Mounts: mounts,
		Env: env, Labels: c.Config.Labels}
}

func toContainerDetailVmFromList(c *ApiContainer) ContainerDetailVm {
	state := strings.ToLower(c.State)
	mounts := []MountVm{}
	for _, m := range c.Mounts {
		mounts = append(mounts, MountVm{Destination: m, RW: true})
	}
	nets := []NetworkVm{}
	for _, n := range c.Networks {
		nets = append(nets, NetworkVm{Name: n})
	}
	sort.SliceStable(nets, func(i, j int) bool { return nets[i].Name < nets[j].Name })
	return ContainerDetailVm{ShortID: shortID(c.ID), ID: c.ID, Name: firstName(c.Names), Image: c.Image, Command: strings.Join(c.Command, " "),
		State: state, Status: containerStatusText(c), Running: state == "running", Pid: c.Pid, RestartCount: c.Restarts, Ports: fmtPorts(c.Ports),
		Created: fmtTsStr(c.Created), Labels: c.Labels, Mounts: mounts, Networks: nets, Env: []string{}}
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

// fmtTsStr: RFC3339 → "YYYY-MM-DD HH:MM:SS"(원래 오프셋 그대로), 실패·0이면 "".
func fmtTsStr(ts string) string {
	t, err := time.Parse(time.RFC3339Nano, ts)
	if err != nil || t.Unix() == 0 {
		return ""
	}
	return fmt.Sprintf("%04d-%02d-%02d %02d:%02d:%02d", t.Year(), int(t.Month()), t.Day(), t.Hour(), t.Minute(), t.Second())
}

func fmtBytes(b int64) string {
	if b <= 0 {
		return "0 B"
	}
	units := []string{"B", "KB", "MB", "GB", "TB"}
	i := int(math.Log(float64(b)) / math.Log(1024))
	if i > len(units)-1 {
		i = len(units) - 1
	}
	return fmt.Sprintf("%.1f %s", float64(b)/math.Pow(1024, float64(i)), units[i])
}

func fmtPorts(ports []ApiPort) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, p := range ports {
		count := int32(1)
		if p.Range != 0 {
			count = int32(p.Range)
		}
		for i := int32(0); i < count; i++ {
			cport := int32(p.ContainerPort) + i
			var label string
			if p.HostPort > 0 {
				hip := p.HostIP
				if hip == "" {
					hip = "0.0.0.0"
				}
				label = fmt.Sprintf("%s:%d->%d/%s", hip, int32(p.HostPort)+i, cport, p.Protocol)
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

func containerStatusText(c *ApiContainer) string {
	switch s := strings.ToLower(c.State); s {
	case "running":
		return "Up " + humanizeSince(c.StartedAt)
	case "paused":
		return "Paused"
	case "created":
		return "Created"
	case "exited", "stopped":
		if c.ExitedAt > 0 {
			return fmt.Sprintf("Exited (%d) %s ago", c.ExitCode, humanizeSince(c.ExitedAt))
		}
		return fmt.Sprintf("Exited (%d)", c.ExitCode)
	default:
		return s
	}
}

func humanizeSince(unix int64) string {
	secs := time.Now().Unix() - unix
	if secs < 0 {
		secs = 0
	}
	switch {
	case secs < 60:
		return fmt.Sprintf("%d seconds", secs)
	case secs < 3600:
		return fmt.Sprintf("%d minutes", secs/60)
	case secs < 86400:
		return fmt.Sprintf("%d hours", secs/3600)
	}
	return fmt.Sprintf("%d days", secs/86400)
}
