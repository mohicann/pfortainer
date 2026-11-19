package pf

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"

	"github.com/mohicann/pfortainer/internal/httpx"
)

// PodmanError: Transport → "podman: {msg}", Api → message.
type PodmanError struct {
	Transport  bool
	Msg        string
	StatusCode int
	Cause      string
}

func (e *PodmanError) Error() string {
	if e.Transport {
		return "podman: " + e.Msg
	}
	return e.Msg
}

func (e *PodmanError) isSocketError() bool {
	if !e.Transport {
		return false
	}
	for _, s := range []string{"No such file", "no such file", "Connection refused", "connection refused"} {
		if strings.Contains(e.Msg, s) {
			return true
		}
	}
	return false
}

func transport(msg string) *PodmanError { return &PodmanError{Transport: true, Msg: msg} }

type Entrypoint string

func (e *Entrypoint) SerdeDecode(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		*e = Entrypoint(x)
		return "", true
	case []any:
		var parts []string
		for _, it := range x {
			s, ok := it.(string)
			if !ok {
				return "a string", false
			}
			parts = append(parts, s)
		}
		*e = Entrypoint(strings.Join(parts, " "))
		return "", true
	}
	return "a string or array of strings", false
}

type ApiContainer struct {
	ID        string            `json:"Id" serde:"default"`
	Names     []string          `json:"Names" serde:"default,null"`
	Image     string            `json:"Image" serde:"default"`
	ImageID   string            `json:"ImageID" serde:"default"`
	State     string            `json:"State" serde:"default"`
	Created   string            `json:"Created" serde:"default"`
	StartedAt int64             `json:"StartedAt" serde:"default"`
	ExitedAt  int64             `json:"ExitedAt" serde:"default"`
	ExitCode  int32             `json:"ExitCode" serde:"default"`
	Ports     []ApiPort         `json:"Ports" serde:"default,null"`
	Command   []string          `json:"Command" serde:"default,null"`
	Labels    map[string]string `json:"Labels" serde:"default,null"`
	Mounts    []string          `json:"Mounts" serde:"default,null"`
	Networks  []string          `json:"Networks" serde:"default,null"`
	Pid       int32             `json:"Pid" serde:"default"`
	Restarts  int32             `json:"Restarts" serde:"default"`
}

type ApiPort struct {
	HostIP        string `json:"host_ip" serde:"default"`
	ContainerPort uint16 `json:"container_port" serde:"default"`
	HostPort      uint16 `json:"host_port" serde:"default"`
	Range         uint16 `json:"range" serde:"default"`
	Protocol      string `json:"protocol" serde:"default"`
}

type ApiContainerDetail struct {
	ID              string                   `json:"Id" serde:"default"`
	Name            string                   `json:"Name" serde:"default"`
	Created         string                   `json:"Created" serde:"default"`
	Image           string                   `json:"Image" serde:"default"`
	Path            string                   `json:"Path" serde:"default"`
	Args            []string                 `json:"Args" serde:"default,null"`
	State           ContainerState           `json:"State" serde:"default"`
	RestartCount    int32                    `json:"RestartCount" serde:"default"`
	Mounts          []ContainerMount         `json:"Mounts" serde:"default,null"`
	Config          ContainerConfig          `json:"Config" serde:"default"`
	HostConfig      ContainerHostConfig      `json:"HostConfig" serde:"default"`
	NetworkSettings ContainerNetworkSettings `json:"NetworkSettings" serde:"default"`
}

type ContainerState struct {
	Status     string `json:"Status" serde:"default"`
	Running    bool   `json:"Running" serde:"default"`
	Paused     bool   `json:"Paused" serde:"default"`
	Restarting bool   `json:"Restarting" serde:"default"`
	Pid        int32  `json:"Pid" serde:"default"`
	ExitCode   int32  `json:"ExitCode" serde:"default"`
	Error      string `json:"Error" serde:"default"`
	StartedAt  string `json:"StartedAt" serde:"default"`
	FinishedAt string `json:"FinishedAt" serde:"default"`
}

type ContainerMount struct {
	Kind        string `json:"Type" serde:"default"`
	Name        string `json:"Name" serde:"default"`
	Source      string `json:"Source" serde:"default"`
	Destination string `json:"Destination" serde:"default"`
	Mode        string `json:"Mode" serde:"default"`
	RW          bool   `json:"RW" serde:"default"`
}

type ContainerConfig struct {
	Hostname   string            `json:"Hostname" serde:"default"`
	Env        []string          `json:"Env" serde:"default,null"`
	Cmd        []string          `json:"Cmd" serde:"default,null"`
	Entrypoint Entrypoint        `json:"Entrypoint" serde:"default"`
	WorkingDir string            `json:"WorkingDir" serde:"default"`
	Labels     map[string]string `json:"Labels" serde:"default,null"`
}

type ContainerHostConfig struct {
	NetworkMode   string        `json:"NetworkMode" serde:"default"`
	RestartPolicy RestartPolicy `json:"RestartPolicy" serde:"default"`
}

type RestartPolicy struct {
	Name string `json:"Name" serde:"default"`
}

type ContainerNetworkSettings struct {
	Ports    map[string]*[]PortBinding `json:"Ports" serde:"default,null"`
	Networks map[string]NetworkInfo    `json:"Networks" serde:"default,null"`
}

type PortBinding struct {
	HostIP   string `json:"HostIp" serde:"default"`
	HostPort string `json:"HostPort" serde:"default"`
}

type NetworkInfo struct {
	IPAddress  string `json:"IPAddress" serde:"default"`
	Gateway    string `json:"Gateway" serde:"default"`
	MacAddress string `json:"MacAddress" serde:"default"`
}

type ApiSystemInfo struct {
	Host    SystemHost    `json:"host" serde:"default"`
	Store   SystemStore   `json:"store" serde:"default"`
	Version SystemVersion `json:"version" serde:"default"`
}

type SystemHost struct {
	Arch           string         `json:"arch" serde:"default"`
	Cpus           int32          `json:"cpus" serde:"default"`
	CpuUtilization CpuUtilization `json:"cpuUtilization" serde:"default"`
	Distribution   Distribution   `json:"distribution" serde:"default"`
	Hostname       string         `json:"hostname" serde:"default"`
	Kernel         string         `json:"kernel" serde:"default"`
	MemFree        int64          `json:"memFree" serde:"default"`
	MemTotal       int64          `json:"memTotal" serde:"default"`
	OS             string         `json:"os" serde:"default"`
	SwapFree       int64          `json:"swapFree" serde:"default"`
	SwapTotal      int64          `json:"swapTotal" serde:"default"`
	Uptime         string         `json:"uptime" serde:"default"`
}

type CpuUtilization struct {
	IdlePercent   float64 `json:"idlePercent" serde:"default"`
	SystemPercent float64 `json:"systemPercent" serde:"default"`
	UserPercent   float64 `json:"userPercent" serde:"default"`
}

type Distribution struct {
	Distribution string `json:"distribution" serde:"default"`
	Version      string `json:"version" serde:"default"`
}

type SystemStore struct {
	ContainerStore  ContainerStore `json:"containerStore" serde:"default"`
	GraphDriverName string         `json:"graphDriverName" serde:"default"`
	GraphRoot       string         `json:"graphRoot" serde:"default"`
	ImageStore      ImageStore     `json:"imageStore" serde:"default"`
	RunRoot         string         `json:"runRoot" serde:"default"`
	VolumePath      string         `json:"volumePath" serde:"default"`
}

type ContainerStore struct {
	Number  int32 `json:"number" serde:"default"`
	Paused  int32 `json:"paused" serde:"default"`
	Running int32 `json:"running" serde:"default"`
	Stopped int32 `json:"stopped" serde:"default"`
}

type ImageStore struct {
	Number int32 `json:"number" serde:"default"`
}

type SystemVersion struct {
	APIVersion string `json:"APIVersion" serde:"default"`
	GoVersion  string `json:"GoVersion" serde:"default"`
	OS         string `json:"Os" serde:"default"`
	OsArch     string `json:"OsArch" serde:"default"`
	Version    string `json:"Version" serde:"default"`
}

type BuildMsg struct {
	Stream   string `json:"stream" serde:"default"`
	Error    string `json:"error" serde:"default"`
	Status   string `json:"status" serde:"default"`
	Progress string `json:"progress" serde:"default"`
	ID       string `json:"id" serde:"default"`
}

type ApiErrBody struct {
	Cause   string `json:"cause" serde:"default"`
	Message string `json:"message" serde:"default"`
}

type PodmanClient struct {
	sock string
	hc   *http.Client
}

func NewPodmanClient(sock string) *PodmanClient {
	return &PodmanClient{sock: sock, hc: &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", sock)
		},
		DisableCompression: true,
	}}}
}

// connectErr: hyper legacy client 연결 오류의 error_chain 표기.
func connectErr(err error) string {
	var en syscall.Errno
	if errors.As(err, &en) {
		return "client error (Connect): " + rustIOErr(en)
	}
	return "client error (Connect)"
}

func apiError(status int, body []byte) *PodmanError {
	var p ApiErrBody
	if httpx.Unmarshal(body, &p) != nil {
		p = ApiErrBody{}
	}
	msg := p.Message
	if msg == "" {
		msg = lossy(body)
	}
	return &PodmanError{StatusCode: status, Cause: p.Cause, Msg: msg}
}

// lossy: String::from_utf8_lossy.
func lossy(b []byte) string { return strings.ToValidUTF8(string(b), "�") }

func (c *PodmanClient) get(path string, dst any) *PodmanError {
	req, _ := http.NewRequest("GET", "http://localhost"+path, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return transport(connectErr(err))
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	if err != nil {
		return transport(err.Error())
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return apiError(resp.StatusCode, b)
	}
	if err := httpx.Unmarshal(b, dst); err != nil {
		return transport(err.Error())
	}
	return nil
}

func (c *PodmanClient) doRequest(method, path string) *PodmanError {
	req, _ := http.NewRequest(method, "http://localhost"+path, nil)
	resp, err := c.hc.Do(req)
	if err != nil {
		return transport(connectErr(err))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode <= 299 || resp.StatusCode == 304 {
		return nil
	}
	b, _ := io.ReadAll(resp.Body)
	return apiError(resp.StatusCode, b)
}

func (c *PodmanClient) ListContainers() ([]ApiContainer, *PodmanError) {
	var out []ApiContainer
	if e := c.get("/v5.0.0/libpod/containers/json?all=true", &out); e != nil {
		return nil, e
	}
	return out, nil
}

func (c *PodmanClient) GetContainerByID(id string) (*ApiContainer, *PodmanError) {
	cs, e := c.ListContainers()
	if e != nil {
		return nil, e
	}
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i], nil
		}
	}
	return nil, &PodmanError{StatusCode: 404, Msg: "컨테이너를 찾을 수 없습니다."}
}

// validURIPath: hyperlocal Uri::new(...).into()가 panic하지 않는 경로인지.
func validURIPath(p string) bool {
	for i := 0; i < len(p); i++ {
		c := p[i]
		if c <= 0x20 || c >= 0x7f || strings.IndexByte(`"<>\^`+"`{|}", c) >= 0 {
			return false
		}
	}
	return true
}

func (c *PodmanClient) InspectContainer(id string) (*ApiContainerDetail, *PodmanError) {
	p := "/v5.0.0/libpod/containers/" + id + "/json"
	if !validURIPath(p) {
		httpx.Abort()
	}
	var d ApiContainerDetail
	if e := c.get(p, &d); e != nil {
		return nil, e
	}
	return &d, nil
}

func (c *PodmanClient) action(method, path string) *PodmanError {
	if !validURIPath(path) {
		httpx.Abort()
	}
	return c.doRequest(method, path)
}

func (c *PodmanClient) GetSystemInfo() (*ApiSystemInfo, *PodmanError) {
	var s ApiSystemInfo
	if e := c.get("/v5.0.0/libpod/info", &s); e != nil {
		return nil, e
	}
	return &s, nil
}

// BuildImage: Dockerfile 하나짜리 tar(GNU 헤더)로 /build, 스트림을 emit(type, line).
func (c *PodmanClient) BuildImage(tag, dockerfile string, emit func(t, line string)) *PodmanError {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	hdr := &tar.Header{Name: "Dockerfile", Size: int64(len(dockerfile)), Mode: 0o644, ModTime: time.Unix(time.Now().Unix(), 0), Format: tar.FormatGNU, Typeflag: tar.TypeReg}
	if err := tw.WriteHeader(hdr); err != nil {
		return transport(err.Error())
	}
	tw.Write([]byte(dockerfile))
	tw.Close()
	path := "/build"
	if tag != "" {
		path += "?t=" + urlenc(tag)
	}
	req, _ := http.NewRequest("POST", "http://localhost"+path, bytes.NewReader(buf.Bytes()))
	req.Header.Set("Content-Type", "application/x-tar")
	resp, err := c.hc.Do(req)
	if err != nil {
		return transport("podman build: client error (Connect)")
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(resp.Body)
		return transport("podman build 실패 (HTTP " + itoa(resp.StatusCode) + "): " + strings.TrimSpace(lossy(b)))
	}
	var pending []byte
	var buildErr *string
	chunk := make([]byte, 32*1024)
	for {
		n, rerr := resp.Body.Read(chunk)
		pending = append(pending, chunk[:n]...)
		for {
			msg, used, perr, eof := nextBuildMsg(pending)
			if eof {
				break
			}
			if perr != "" {
				return transport("podman build 스트림 오류: " + perr)
			}
			if msg.Error != "" {
				line := strings.TrimRight(msg.Error, " \t\r\n")
				emit("error", line)
				buildErr = &line
			} else if msg.Stream != "" {
				line := strings.TrimRight(msg.Stream, " \t\r\n")
				if line != "" {
					emit("log", line)
				}
			} else if msg.Status != "" {
				line := msg.Status
				if msg.Progress != "" {
					line += " " + msg.Progress
				}
				if msg.ID != "" {
					line = msg.ID + ": " + line
				}
				emit("log", line)
			}
			pending = pending[used:]
		}
		if rerr != nil {
			break
		}
	}
	if buildErr != nil {
		return transport(*buildErr)
	}
	return nil
}

// nextBuildMsg: serde_json StreamDeserializer 한 칸. eof=true면 더 받아야 함(또는 끝).
func nextBuildMsg(buf []byte) (BuildMsg, int, string, bool) {
	i := 0
	for i < len(buf) && (buf[i] == ' ' || buf[i] == '\n' || buf[i] == '\r' || buf[i] == '\t') {
		i++
	}
	if i == len(buf) {
		return BuildMsg{}, 0, "", true
	}
	end, perr, eof := httpx.ScanValue(buf)
	if eof {
		return BuildMsg{}, 0, "", true
	}
	if perr != "" {
		return BuildMsg{}, 0, perr, false
	}
	var m BuildMsg
	if err := httpx.Unmarshal(buf[:end], &m); err != nil {
		return BuildMsg{}, 0, err.Error(), false
	}
	return m, end, "", false
}
