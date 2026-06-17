package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// entrypointField handles Podman returning Entrypoint as either a string or []string.
type entrypointField string

func (e *entrypointField) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		*e = entrypointField(s)
		return nil
	}
	var arr []string
	if err := json.Unmarshal(data, &arr); err == nil {
		*e = entrypointField(strings.Join(arr, " "))
		return nil
	}
	return fmt.Errorf("cannot unmarshal Entrypoint")
}

type PodmanClient struct {
	hc *http.Client
}

func newPodmanClient(socketPath string) *PodmanClient {
	return &PodmanClient{
		hc: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					return (&net.Dialer{}).DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

type APIContainer struct {
	ID        string            `json:"Id"`
	Names     []string          `json:"Names"`
	Image     string            `json:"Image"`
	State     string            `json:"State"`
	Created   string            `json:"Created"`
	StartedAt int64             `json:"StartedAt"`
	ExitedAt  int64             `json:"ExitedAt"`
	ExitCode  int               `json:"ExitCode"`
	Ports     []APIPort         `json:"Ports"`
	Command   []string          `json:"Command"`
	Labels    map[string]string `json:"Labels"`
	Mounts    []string          `json:"Mounts"`
	Networks  []string          `json:"Networks"`
	Pid       int               `json:"Pid"`
	Restarts  int               `json:"Restarts"`
}

type APIPort struct {
	HostIP        string `json:"host_ip,omitempty"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	Range         uint16 `json:"range,omitempty"`
	Protocol      string `json:"protocol,omitempty"`
}

type APIImage struct {
	ID       string   `json:"Id"`
	RepoTags []string `json:"RepoTags"`
	Created  int64    `json:"Created"`
	Size     int64    `json:"Size"`
}

// APIContainerDetail mirrors the libpod /containers/{id}/json response.
// Notable difference from Docker-compat: Config.Entrypoint is a string, not []string.
type APIContainerDetail struct {
	ID      string   `json:"Id"`
	Name    string   `json:"Name"`
	Created string   `json:"Created"`
	Image   string   `json:"Image"`
	Path    string   `json:"Path"`
	Args    []string `json:"Args"`

	State struct {
		Status     string `json:"Status"`
		Running    bool   `json:"Running"`
		Paused     bool   `json:"Paused"`
		Restarting bool   `json:"Restarting"`
		Pid        int    `json:"Pid"`
		ExitCode   int    `json:"ExitCode"`
		Error      string `json:"Error"`
		StartedAt  string `json:"StartedAt"`
		FinishedAt string `json:"FinishedAt"`
	} `json:"State"`

	RestartCount int `json:"RestartCount"`

	Mounts []struct {
		Type        string `json:"Type"`
		Name        string `json:"Name"`
		Source      string `json:"Source"`
		Destination string `json:"Destination"`
		Mode        string `json:"Mode"`
		RW          bool   `json:"RW"`
	} `json:"Mounts"`

	Config struct {
		Hostname   string            `json:"Hostname"`
		Env        []string          `json:"Env"`
		Cmd        []string          `json:"Cmd"`
		Entrypoint entrypointField   `json:"Entrypoint"`
		WorkingDir string            `json:"WorkingDir"`
		Labels     map[string]string `json:"Labels"`
	} `json:"Config"`

	HostConfig struct {
		NetworkMode   string `json:"NetworkMode"`
		RestartPolicy struct {
			Name string `json:"Name"`
		} `json:"RestartPolicy"`
	} `json:"HostConfig"`

	NetworkSettings struct {
		Ports map[string][]struct {
			HostIP   string `json:"HostIp"`
			HostPort string `json:"HostPort"`
		} `json:"Ports"`
		Networks map[string]struct {
			IPAddress  string `json:"IPAddress"`
			Gateway    string `json:"Gateway"`
			MacAddress string `json:"MacAddress"`
		} `json:"Networks"`
	} `json:"NetworkSettings"`
}

func (c *PodmanClient) get(path string, out any) error {
	resp, err := c.hc.Get("http://podman" + path)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var apiErr struct {
			Cause   string `json:"cause"`
			Message string `json:"message"`
		}
		_ = json.Unmarshal(body, &apiErr)
		if apiErr.Message == "" {
			apiErr.Message = string(body)
		}
		return &PodmanError{StatusCode: resp.StatusCode, Cause: apiErr.Cause, Message: apiErr.Message}
	}
	return json.Unmarshal(body, out)
}

// PodmanError represents an error response from the Podman API.
type PodmanError struct {
	StatusCode int
	Cause      string
	Message    string
}

func (e *PodmanError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("podman: unexpected status %d", e.StatusCode)
}

// do performs a request against the Podman API and returns a *PodmanError
// if the response status code indicates failure.
func (c *PodmanClient) do(method, path string) error {
	req, err := http.NewRequest(method, "http://podman"+path, nil)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return fmt.Errorf("podman: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 || resp.StatusCode == http.StatusNotModified {
		return nil
	}

	body, _ := io.ReadAll(resp.Body)
	var apiErr struct {
		Cause   string `json:"cause"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(body, &apiErr)
	if apiErr.Message == "" {
		apiErr.Message = string(body)
	}
	return &PodmanError{StatusCode: resp.StatusCode, Cause: apiErr.Cause, Message: apiErr.Message}
}

func (c *PodmanClient) ListContainers() ([]APIContainer, error) {
	var result []APIContainer
	// Docker-compat /containers/json panics (nil pointer) on Podman 5.8.1/FreeBSD,
	// so use the native libpod endpoint instead.
	return result, c.get("/v5.0.0/libpod/containers/json?all=true", &result)
}

func (c *PodmanClient) GetContainerByID(id string) (*APIContainer, error) {
	cs, err := c.ListContainers()
	if err != nil {
		return nil, err
	}
	for i := range cs {
		if cs[i].ID == id {
			return &cs[i], nil
		}
	}
	return nil, &PodmanError{StatusCode: http.StatusNotFound, Message: "컨테이너를 찾을 수 없습니다."}
}

func (c *PodmanClient) ListImages() ([]APIImage, error) {
	var result []APIImage
	return result, c.get("/images/json", &result)
}

func (c *PodmanClient) InspectContainer(id string) (*APIContainerDetail, error) {
	var result APIContainerDetail
	// Use native libpod endpoint — Docker-compat /containers/{id}/json panics on Podman 5.8.1/FreeBSD.
	if err := c.get("/v5.0.0/libpod/containers/"+id+"/json", &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func (c *PodmanClient) RemoveImage(id string, force bool) error {
	path := "/images/" + id
	if force {
		path += "?force=true"
	}
	return c.do(http.MethodDelete, path)
}

func (c *PodmanClient) StartContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/start")
}

func (c *PodmanClient) StopContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/stop")
}

func (c *PodmanClient) RestartContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/restart")
}

func (c *PodmanClient) KillContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/kill")
}

func (c *PodmanClient) PauseContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/pause")
}

func (c *PodmanClient) UnpauseContainer(id string) error {
	return c.do(http.MethodPost, "/containers/"+id+"/unpause")
}

func (c *PodmanClient) RemoveContainer(id string) error {
	return c.do(http.MethodDelete, "/containers/"+id)
}

type APISystemInfo struct {
	Host struct {
		Arch           string `json:"arch"`
		CPUs           int    `json:"cpus"`
		CPUUtilization struct {
			IdlePercent   float64 `json:"idlePercent"`
			SystemPercent float64 `json:"systemPercent"`
			UserPercent   float64 `json:"userPercent"`
		} `json:"cpuUtilization"`
		Distribution struct {
			Distribution string `json:"distribution"`
			Version      string `json:"version"`
		} `json:"distribution"`
		Hostname  string `json:"hostname"`
		Kernel    string `json:"kernel"`
		MemFree   int64  `json:"memFree"`
		MemTotal  int64  `json:"memTotal"`
		OS        string `json:"os"`
		SwapFree  int64  `json:"swapFree"`
		SwapTotal int64  `json:"swapTotal"`
		Uptime    string `json:"uptime"`
	} `json:"host"`
	Store struct {
		ContainerStore struct {
			Number  int `json:"number"`
			Paused  int `json:"paused"`
			Running int `json:"running"`
			Stopped int `json:"stopped"`
		} `json:"containerStore"`
		GraphDriverName string `json:"graphDriverName"`
		GraphRoot       string `json:"graphRoot"`
		ImageStore      struct {
			Number int `json:"number"`
		} `json:"imageStore"`
		RunRoot    string `json:"runRoot"`
		VolumePath string `json:"volumePath"`
	} `json:"store"`
	Version struct {
		APIVersion string `json:"APIVersion"`
		GoVersion  string `json:"GoVersion"`
		Os         string `json:"Os"`
		OsArch     string `json:"OsArch"`
		Version    string `json:"Version"`
	} `json:"version"`
}

func (c *PodmanClient) GetSystemInfo() (*APISystemInfo, error) {
	var info APISystemInfo
	return &info, c.get("/v5.0.0/libpod/info", &info)
}
